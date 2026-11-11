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
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const countItems = `SELECT COUNT(*) FROM items`

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
	flag.Parse()
	runtime.MemProfileRate = 64 * 1024

	db := openDB()

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

func openDB() *sql.DB {
	// cache=shared: одна память на все соединения процесса.
	db, err := sql.Open("sqlite", "file:txleak?mode=memory&cache=shared")
	if err != nil {
		log.Fatal(err)
	}
	// Пул не ограничиваем: каждая незакрытая транзакция забирает своё соединение.
	db.SetMaxOpenConns(0)

	// Одно соединение держим до конца процесса, иначе memory-база исчезнет,
	// когда закроется последнее.
	pin, err := db.Conn(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pin.ExecContext(context.Background(), `
		CREATE TABLE items (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			payload TEXT NOT NULL
		)`); err != nil {
		log.Fatal(err)
	}
	payload := strings.Repeat("x", 256)
	for i := 1; i <= 8; i++ {
		if _, err := pin.ExecContext(context.Background(),
			`INSERT INTO items(name, payload) VALUES (?, ?)`,
			fmt.Sprintf("item-%d", i), payload); err != nil {
			log.Fatal(err)
		}
	}
	pinned = pin
	return db
}

// leaky начинает транзакцию от context.Background и не завершает её.
// SELECT берёт shared-блокировку SQLite и не мешает таким же читателям.
// Запись здесь не делается: незакоммиченный UPDATE занял бы писателя,
// и следующие запросы встали бы в очередь.
func leaky(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(countItems).Scan(&n); err != nil {
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
	var n int
	if err := tx.QueryRow(countItems).Scan(&n); err != nil {
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
	return fmt.Sprintf("goroutines=%5d%s  heap_inuse=%4dMiB  conns_open=%d conns_in_use=%d tx=%d  gc=%d",
		n, delta, m.HeapInuse/1024/1024, st.OpenConnections, st.InUse, nTx, m.NumGC)
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
