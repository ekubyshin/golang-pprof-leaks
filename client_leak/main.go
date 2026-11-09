package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	_ "net/http/pprof"
	"runtime"
	"time"
)

// Один клиент на процесс: IdleConnTimeout как у DefaultTransport.
var shared = &http.Client{
	Timeout: 3 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

func main() {
	addr := flag.String("http", "127.0.0.1:8080", "адрес HTTP и pprof")
	flag.Parse()
	runtime.MemProfileRate = 64 * 1024

	payload := bytes.Repeat([]byte("x"), 1024)
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(payload)
	}))

	http.HandleFunc("/", index(*addr))
	http.HandleFunc("/leak", func(w http.ResponseWriter, r *http.Request) {
		if err := leakyCall(downstream.URL); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/fixed", func(w http.ResponseWriter, r *http.Request) {
		if err := fixedCall(downstream.URL); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	http.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		runtime.GC()
		fmt.Fprintln(w, snapshot())
	})

	go logStats()
	log.Printf("client leak http://%s/leak  pprof http://%s/debug/pprof/", *addr, *addr)

	server := &http.Server{Addr: *addr, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}

// leakyCall создаёт Transport на запрос. Body закрыт, но у нулевого Transport
// IdleConnTimeout равен 0, поэтому простаивающее соединение не отпускается.
func leakyCall(url string) error {
	client := &http.Client{Transport: &http.Transport{}}
	return do(client, url)
}

func fixedCall(url string) error {
	return do(shared, url)
}

func do(client *http.Client, url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
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
