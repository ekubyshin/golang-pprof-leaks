package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	_ "net/http/pprof" // /debug/pprof/ на DefaultServeMux
	"runtime"
	"time"
)

// На проде runtime.MemProfileRate по умолчанию 512 КБ: в короткий прогон
// мелкие буферы net/http почти не попадают в heap-профиль. Для демо берём
// сэмпл чаще, чтобы рост был виден уже через десятки секунд.
const demoMemProfileRate = 64 * 1024

// Клиент один на весь процесс. В демо течёт незакрытый Body.
var client = &http.Client{Transport: http.DefaultTransport}

func main() {
	addr := flag.String("http", "127.0.0.1:8080", "адрес HTTP и pprof")
	flag.Parse()

	runtime.MemProfileRate = demoMemProfileRate

	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(payload)
	}))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "leak:       http://%s/leak\n", *addr)
		fmt.Fprintf(w, "fixed:      http://%s/fixed\n", *addr)
		fmt.Fprintf(w, "gc:         http://%s/gc\n", *addr)
		fmt.Fprintf(w, "pprof:      http://%s/debug/pprof/\n", *addr)
		fmt.Fprintf(w, "downstream: %s\n", downstream.URL)
	})
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
		fmt.Fprintln(w, snapshot().String())
	})

	go logStats()

	log.Printf("http://%s/leak", *addr)
	log.Printf("http://%s/fixed", *addr)
	log.Printf("pprof http://%s/debug/pprof/", *addr)
	log.Printf("downstream %s", downstream.URL)
	log.Printf("heap sample rate %d bytes", runtime.MemProfileRate)

	server := &http.Server{
		Addr:              *addr,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}

// payload — небольшое тело, чтобы в профиле были буферы, а не гигабайты данных.
var payload = bytes.Repeat([]byte("x"), 1024)

// leakyCall повторяет баг с прода: ответ получен, Body не закрыт.
// http.Get использует context.Background, поэтому завершение обработчика
// не отменяет соединение и readLoop остаётся жить.
func leakyCall(url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	_ = resp // Body не закрываем
	return nil
}

func fixedCall(url string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

type stats struct {
	goroutines int
	heapInuse  uint64
	stackInuse uint64
	sys        uint64
	gc         uint32
}

func snapshot() stats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return stats{
		goroutines: runtime.NumGoroutine(),
		heapInuse:  m.HeapInuse,
		stackInuse: m.StackInuse,
		sys:        m.Sys,
		gc:         m.NumGC,
	}
}

func (s stats) String() string {
	return fmt.Sprintf(
		"goroutines=%5d  heap_inuse=%4dMiB  stack=%5dKiB  sys=%4dMiB  gc=%d",
		s.goroutines,
		s.heapInuse/1024/1024,
		s.stackInuse/1024,
		s.sys/1024/1024,
		s.gc,
	)
}

func logStats() {
	base := runtime.NumGoroutine()
	report := func() {
		s := snapshot()
		log.Printf("goroutines=%5d (%+5d)  heap_inuse=%4dMiB  stack=%5dKiB  sys=%4dMiB  gc=%d",
			s.goroutines,
			s.goroutines-base,
			s.heapInuse/1024/1024,
			s.stackInuse/1024,
			s.sys/1024/1024,
			s.gc,
		)
	}
	report()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		report()
	}
}
