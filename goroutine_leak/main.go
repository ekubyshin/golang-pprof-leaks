package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"time"
)

func main() {
	addr := flag.String("http", "127.0.0.1:8080", "адрес HTTP и pprof")
	flag.Parse()
	runtime.MemProfileRate = 64 * 1024

	http.HandleFunc("/", index(*addr))
	http.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		leaky()
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/fixed", func(w http.ResponseWriter, r *http.Request) {
		fixed()
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		fmt.Fprintln(w, snapshot())
	})

	go logStats()
	log.Printf("goroutine leak http://%s/leak  pprof http://%s/debug/pprof/", *addr, *addr)

	server := &http.Server{Addr: *addr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

// leaky запускает горутину на запрос. Писать в out некому, горутина не выходит
// и удерживает буфер.
func leaky() {
	out := make(chan []byte)
	go func() {
		out <- bytes.Repeat([]byte("x"), 4*1024)
	}()
}

func fixed() {
	_ = bytes.Repeat([]byte("x"), 4*1024)
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

func snapshot() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("goroutines=%5d  heap_inuse=%4dMiB  stack=%5dKiB  sys=%4dMiB  gc=%d",
		runtime.NumGoroutine(), m.HeapInuse/1024/1024, m.StackInuse/1024, m.Sys/1024/1024, m.NumGC)
}

func logStats() {
	base := runtime.NumGoroutine()
	report := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		n := runtime.NumGoroutine()
		log.Printf("goroutines=%5d (%+5d)  heap_inuse=%4dMiB  stack=%5dKiB  sys=%4dMiB  gc=%d",
			n, n-base, m.HeapInuse/1024/1024, m.StackInuse/1024, m.Sys/1024/1024, m.NumGC)
	}
	report()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		report()
	}
}
