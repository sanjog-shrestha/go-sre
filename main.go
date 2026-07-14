package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed",
		},
		[]string{"path", "method", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func instrument(logger *slog.Logger, path string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next(rec, r)

		duration := time.Since(start)
		status := strconv.Itoa(rec.status)

		httpRequestsTotal.WithLabelValues(path, r.Method, status).Inc()
		httpRequestDuration.WithLabelValues(path, r.Method).Observe(duration.Seconds())

		logger.Info("request handled",
			"path", path,
			"method", r.Method,
			"status", rec.status,
			"duration_ms", duration.Milliseconds(),
		)
	}
}

func recoverMiddleware(logger *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered",
					"error", rec,
					"path", r.URL.Path,
				)
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("internal server error\n"))
			}
		}()
		next(w, r)
	}
}

func withMiddleware(logger *slog.Logger, path string, h http.HandlerFunc) http.HandlerFunc {
	return instrument(logger, path, recoverMiddleware(logger, h))
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

func simulateErrorHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "simulated internal error\n", http.StatusInternalServerError)
}

func simulateSlowHandler(w http.ResponseWriter, r *http.Request) {
	time.Sleep(300 * time.Millisecond)
	w.Write([]byte("slow response completed\n"))
}

func simulatePanicHandler(w http.ResponseWriter, r *http.Request) {
	panic("simulated panic for testing recovery middleware")
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

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
	mux.Handle("/", instrument(logger, "/", rootHandler))
	mux.Handle("/healthz", instrument(logger, "/healthz", readyzHandler))
	mux.Handle("/livez", instrument(logger, "/livez", livezHandler))
	mux.Handle("/readyz", instrument(logger, "/readyz", readyzHandler))
	mux.Handle("/simulate-error", withMiddleware(logger, "/simulate-error", simulateErrorHandler))
	mux.Handle("/simulate-slow", withMiddleware(logger, "/simulate-slow", simulateSlowHandler))
	mux.Handle("/simulate-panic", withMiddleware(logger, "/simulate-panic", simulatePanicHandler))
	mux.Handle("/metrics", promhttp.Handler())

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
