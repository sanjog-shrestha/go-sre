package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"sync"
	"fmt"
	"strings"
	"sync/atomic"	
)

type metrics struct {
	mu sync.Mutex
	counters map[string]int64
	startTime time.Time
}

func newMetrics() *metrics {
	return &metrics{
		counters: make(map[string]int64),
		startTime: time.Now(),
	}
}

func (m *metrics) inc(path,method string, status int) {	
	key := fmt.Sprintf("%s|%s|%d", path, method, status)
	m.mu.Lock()
	m.counters[key]++
	m.mu.Unlock()
}

func(m *metrics) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b strings.Builder
	b.WriteString("# HELP http_requests_total The total number of HTTP requests.\n")
	b.WriteString("# TYPE http_requests_total counter\n")
	for key, count := range m.counters {
		parts := strings.SplitN(key, "|", 3)
		path,method, status := parts[0], parts[1], parts[2]
		fmt.Fprintf(&b, "http_requests_total{path=\"%q\",method=\"%q\",status=\"%q\"} %d\n", path, method, status, count)
	}

	b.WriteString(fmt.Sprintf("# HELP process_start_time_seconds Time since process start.\n"))
	b.WriteString(fmt.Sprintf("# TYPE process_uptime_seconds gauge\n"))
	fmt.Fprintf(&b, "process_uptime_seconds %f\n", time.Since(m.startTime).Seconds())
	
	return b.String()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func instrument(m *metrics, logger *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next(rec, r)

		duration := time.Since(start)
		m.inc(r.URL.Path, r.Method, rec.status)
		logger.Info("request handled", "path", r.URL.Path, "method", r.Method, "status", rec.status, "duration_ms", duration.Milliseconds(),)
	}
}

var ready atomic.Bool

func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Hello, from sre-go-scratch!\n"))
}

func livezHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("alive\n"))
}

func readyzHandler(w http.ResponseWriter, r *http.Request) {
	if !ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready\n"))
}

func metricsHandler(m *metrics) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain,; version=0.0.4")
		w.Write([]byte(m.render()))
	}
}


func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	m := newMetrics()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	go func() {
	  	time.Sleep(5 * time.Second)
		ready.Store(true)
		logger.Info("service is now ready")
	}()

	mux := http.NewServeMux()
	mux.Handle("/", instrument(m,logger, rootHandler))
	mux.Handle("/healthz", instrument(m,logger, readyzHandler))
	mux.Handle("/livez", instrument(m,logger, livezHandler))
	mux.Handle("/readyz", instrument(m,logger, readyzHandler))
	mux.Handle("/metrics", metricsHandler(m))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		logger.Info("Starting server", "addr", srv.Addr)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	sig := <-stop
	logger.Info("Shutdown signal received", "signal", sig.String())

	ready.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("Server shut down cleanly")
}
