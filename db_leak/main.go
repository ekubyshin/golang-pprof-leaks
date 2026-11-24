package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const query = `SELECT id, name, payload FROM items ORDER BY id`

// Меньше числа строк в items: иначе Next дойдёт до конца выборки и закроет Rows сам.
const pageSize = 5

// Сессии этого сервиса, кроме соединения, с которого снимаем статистику.
const sessionSQL = `
SELECT
  count(*) FILTER (WHERE state = 'idle') AS idle,
  count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_tx,
  count(*) FILTER (WHERE backend_xmin IS NOT NULL) AS with_xmin
FROM pg_stat_activity
WHERE datname = current_database()
  AND pid <> pg_backend_pid()`

type item struct {
	ID      int
	Name    string
	Payload string
}

func main() {
	addr := flag.String("http", "127.0.0.1:8080", "адрес HTTP и pprof")
	dsn := flag.String("dsn", "postgres://leak:leak@127.0.0.1:5433/leak?sslmode=disable", "PostgreSQL DSN")
	flag.Parse()
	runtime.MemProfileRate = 64 * 1024

	db, stats := openDB(*dsn)

	http.HandleFunc("/", index(*addr))
	http.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		items, err := leaky(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintf(w, "items=%d\n", len(items))
	})
	http.HandleFunc("/fixed", func(w http.ResponseWriter, r *http.Request) {
		items, err := fixed(db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintf(w, "items=%d\n", len(items))
	})
	http.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		fmt.Fprintln(w, reportLine(db, stats, 0))
	})

	go logStats(db, stats)
	log.Printf("rows leak http://%s/leak  pprof http://%s/debug/pprof/", *addr, *addr)

	server := &http.Server{Addr: *addr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

// openDB возвращает пул и отдельное соединение для pg_stat_activity:
// отчёт не занимает ещё одну сессию на каждый вызов.
func openDB(dsn string) (*sql.DB, *sql.Conn) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	// Потолок соединений на сервере Postgres, не в пуле клиента.
	db.SetMaxOpenConns(0)
	db.SetMaxIdleConns(2)

	var pingErr error
	for i := 0; i < 30; i++ {
		pingErr = db.Ping()
		if pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		log.Fatal(pingErr)
	}

	stats, err := db.Conn(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := stats.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS items (
			id integer PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
			name text NOT NULL,
			payload text NOT NULL
		)`); err != nil {
		log.Fatal(err)
	}
	var n int
	if err := stats.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM items`).Scan(&n); err != nil {
		log.Fatal(err)
	}
	if n == 0 {
		payload := strings.Repeat("x", 256)
		for i := 1; i <= 8; i++ {
			if _, err := stats.ExecContext(context.Background(),
				`INSERT INTO items(name, payload) VALUES ($1, $2)`,
				fmt.Sprintf("item-%d", i), payload); err != nil {
				log.Fatal(err)
			}
		}
	}
	return db, stats
}

func leaky(db *sql.DB) ([]item, error) {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		return nil, err
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Name, &it.Payload); err != nil {
			return nil, err
		}
		items = append(items, it)
		if len(items) == pageSize {
			break
		}
	}
	return items, rows.Err()
}

func fixed(db *sql.DB) ([]item, error) {
	rows, err := db.QueryContext(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.ID, &it.Name, &it.Payload); err != nil {
			return nil, err
		}
		items = append(items, it)
		if len(items) == pageSize {
			break
		}
	}
	return items, rows.Err()
}

func index(addr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "leak:  http://%s/leak\nfixed: http://%s/fixed\ngc:    http://%s/gc\npprof: http://%s/debug/pprof/\n", addr, addr, addr, addr)
	}
}

func pgSessions(stats *sql.Conn) (idle, idleInTx, withXmin int) {
	idle, idleInTx, withXmin = -1, -1, -1
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = stats.QueryRowContext(ctx, sessionSQL).Scan(&idle, &idleInTx, &withXmin)
	return
}

func reportLine(db *sql.DB, stats *sql.Conn, base int) string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	st := db.Stats()
	n := runtime.NumGoroutine()
	delta := ""
	if base > 0 {
		delta = fmt.Sprintf(" (%+5d)", n-base)
	}
	idle, idleInTx, withXmin := pgSessions(stats)
	return fmt.Sprintf("goroutines=%5d%s  heap_inuse=%4dMiB  conns_open=%d conns_in_use=%d  pg_idle=%d pg_idle_in_tx=%d pg_xmin=%d  gc=%d",
		n, delta, m.HeapInuse/1024/1024, st.OpenConnections, st.InUse, idle, idleInTx, withXmin, m.NumGC)
}

func logStats(db *sql.DB, stats *sql.Conn) {
	base := runtime.NumGoroutine()
	report := func() { log.Print(reportLine(db, stats, base)) }
	report()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		report()
	}
}
