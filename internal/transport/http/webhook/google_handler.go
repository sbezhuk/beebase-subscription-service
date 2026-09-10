// Package webhook provides HTTP handlers for store notification webhooks.
package webhook

import (
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/sbezhuk/beebase-common/httpx"
	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
)

// GoogleHandler handles incoming Google Cloud Pub/Sub push messages for RTDN.
type GoogleHandler struct {
	service *appsub.Service
	log     *slog.Logger
}

// NewGoogleHandler constructs a GoogleHandler backed by service.
func NewGoogleHandler(service *appsub.Service, log *slog.Logger) *GoogleHandler {
	return &GoogleHandler{
		service: service,
		log:     log,
	}
}

// ServeHTTP processes POST /api/v1/subscriptions/webhooks/google.
func (h *GoogleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	// Limit request body to 1MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "unable to read request body")
		return
	}

	if err := h.service.HandleGoogleNotification(r.Context(), body); err != nil {
		if errors.Is(err, appsub.ErrInvalidWebhookPayload) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_notification", err.Error())
			return
		}
		httpx.WriteInternalError(w, h.log, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}
