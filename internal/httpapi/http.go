package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"smm-tg-bot/internal/app"
	"smm-tg-bot/internal/payments"
)

func Mount(r chi.Router, service *app.Service, hub *payments.Hub, logger *slog.Logger) {
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	r.Post("/webhooks/{provider}", func(w http.ResponseWriter, r *http.Request) {
		provider := chi.URLParam(r, "provider")
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
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
