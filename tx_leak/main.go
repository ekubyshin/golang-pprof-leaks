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
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const insertEvent = `INSERT INTO events(payload) VALUES ($1)`

const sessionSQL = `
SELECT
  count(*) FILTER (WHERE state = 'idle') AS idle,
  count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_tx,
  count(*) FILTER (WHERE backend_xmin IS NOT NULL) AS with_xmin
FROM pg_stat_activity
WHERE datname = current_database()
  AND pid <> pg_backend_pid()`

// openTx считает незавершённые транзакции в логе.
// Срез не обязателен для самой утечки: database/sql запускает (*Tx).awaitDone,
// и эта горутина держит *sql.Tx, пока не вызовут Commit или Rollback.
var (
	txMu   sync.Mutex
	openTx []*sql.Tx
	pinned *sql.Conn
)

func main() {
	addr := flag.String("http", "127.0.0.1:8080", "адрес HTTP и pprof")
	dsn := flag.String("dsn", "postgres://leak:leak@127.0.0.1:5434/leak?sslmode=disable", "PostgreSQL DSN")
	flag.Parse()
	runtime.MemProfileRate = 64 * 1024

	db := openDB(*dsn)

	http.HandleFunc("/", index(*addr))
	http.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		if err := leaky(db); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/fixed", func(w http.ResponseWriter, r *http.Request) {
		if err := fixed(db); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		fmt.Fprintln(w, reportLine(db, 0))
	})

	go logStats(db)
	log.Printf("tx leak http://%s/leak  pprof http://%s/debug/pprof/", *addr, *addr)

	server := &http.Server{Addr: *addr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func openDB(dsn string) *sql.DB {
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

	pin, err := db.Conn(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pin.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS events (
			id bigint PRIMARY KEY GENERATED ALWAYS AS IDENTITY,
			payload text NOT NULL
		)`); err != nil {
		log.Fatal(err)
	}
	pinned = pin
	return db
}

// leaky открывает транзакцию от context.Background и не завершает её.
// INSERT пишет новую строку, поэтому транзакции не ждут друг друга.
// UPDATE одной и той же строки держал бы row lock до Commit или Rollback.
func leaky(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(context.Background(), insertEvent, "x"); err != nil {
		_ = tx.Rollback()
		return err
	}
	txMu.Lock()
	openTx = append(openTx, tx)
	txMu.Unlock()
	return nil
}

func fixed(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(context.Background(), insertEvent, "x"); err != nil {
		return err
	}
	return tx.Commit()
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

func pgSessions() (idle, idleInTx, withXmin int) {
	idle, idleInTx, withXmin = -1, -1, -1
	if pinned == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = pinned.QueryRowContext(ctx, sessionSQL).Scan(&idle, &idleInTx, &withXmin)
	return
}

func reportLine(db *sql.DB, base int) string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	st := db.Stats()
	txMu.Lock()
	nTx := len(openTx)
	txMu.Unlock()
	n := runtime.NumGoroutine()
	delta := ""
	if base > 0 {
		delta = fmt.Sprintf(" (%+5d)", n-base)
	}
	idle, idleInTx, withXmin := pgSessions()
	return fmt.Sprintf("goroutines=%5d%s  heap_inuse=%4dMiB  conns_open=%d conns_in_use=%d tx=%d  pg_idle=%d pg_idle_in_tx=%d pg_xmin=%d  gc=%d",
		n, delta, m.HeapInuse/1024/1024, st.OpenConnections, st.InUse, nTx, idle, idleInTx, withXmin, m.NumGC)
}

func logStats(db *sql.DB) {
	base := runtime.NumGoroutine()
	report := func() { log.Print(reportLine(db, base)) }
	report()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		report()
	}
}
