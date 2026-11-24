package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"runtime"
	"time"
)

// appCtx живёт до конца процесса. appCancel из запросов не вызывается,
// дочерние контексты остаются в его списке.
var appCtx, appCancel = context.WithCancel(context.Background())

func main() {
	defer appCancel()

	addr := flag.String("http", "127.0.0.1:8080", "адрес HTTP и pprof")
	flag.Parse()
	runtime.MemProfileRate = 64 * 1024

	http.HandleFunc("/", index(*addr))
	http.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		if err := leaky(w); err != nil {
			log.Print(err)
		}
	})
	http.HandleFunc("/fixed", func(w http.ResponseWriter, r *http.Request) {
		if err := fixed(w); err != nil {
			log.Print(err)
		}
	})
	http.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		fmt.Fprintln(w, snapshot())
	})

	go logStats()
	log.Printf("context leak http://%s/leak  pprof http://%s/debug/pprof/", *addr, *addr)

	server := &http.Server{Addr: *addr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func leaky(w io.Writer) error {
	ctx, cancel := context.WithCancel(appCtx)
	go func() {
		<-ctx.Done()
	}()
	if _, err := fmt.Fprintln(w, "ok"); err != nil {
		cancel()
		return err
	}
	return nil
}

func fixed(w io.Writer) error {
	ctx, cancel := context.WithCancel(appCtx)
	defer cancel()
	go func() {
		<-ctx.Done()
	}()
	_, err := fmt.Fprintln(w, "ok")
	return err
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
