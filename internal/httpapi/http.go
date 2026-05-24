package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"smm-tg-bot/internal/app"
	"smm-tg-bot/internal/payments"
)

func Mount(r chi.Router, service *app.Service, hub *payments.Hub, logger *slog.Logger) {
	r.Use(requestLogger(logger))
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Get("/webhooks/{provider}", func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		logger.Info("payment webhook probe", "provider", provider, "remote_addr", r.RemoteAddr, "forwarded_for", r.Header.Get("X-Forwarded-For"), "forwarded_proto", r.Header.Get("X-Forwarded-Proto"))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("webhook endpoint is reachable; payment systems must send POST requests"))
	})
	r.Post("/webhooks/{provider}", func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		logger.Info("payment webhook received", "provider", provider, "bytes", len(raw), "remote_addr", r.RemoteAddr)
		event, err := hub.ParseWebhook(provider, r, raw)
		if err != nil {
			logger.Warn("payment webhook rejected", "provider", provider, "error", err)
			http.Error(w, "bad webhook", http.StatusBadRequest)
			return
		}
		if err := service.HandlePaymentEvent(r.Context(), event); err != nil {
			logger.Warn("payment webhook handle", "provider", provider, "error", err)
			http.Error(w, "handle error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			if r.URL.Path == "/healthz" && rw.status < 500 {
				return
			}
			logger.Info("http request", "method", r.Method, "path", r.URL.Path, "status", rw.status, "remote_addr", r.RemoteAddr, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
