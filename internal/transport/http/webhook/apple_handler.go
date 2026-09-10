// Package webhook provides HTTP handlers for store notification webhooks.
package webhook

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/sbezhuk/beebase-common/httpx"
	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
)

// AppleHandler handles incoming App Store Server Notifications V2 HTTP requests.
type AppleHandler struct {
	service *appsub.Service
	log     *slog.Logger
}

// NewAppleHandler constructs an AppleHandler backed by service.
func NewAppleHandler(service *appsub.Service, log *slog.Logger) *AppleHandler {
	return &AppleHandler{
		service: service,
		log:     log,
	}
}

type statusResponse struct {
	Status string `json:"status"`
}

// ServeHTTP processes POST /api/v1/subscriptions/webhooks/apple.
func (h *AppleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	var req apple.WebhookRequestBody
	if err := json.Unmarshal(body, &req); err != nil || req.SignedPayload == "" {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "invalid JSON payload or missing signedPayload")
		return
	}

	if err := h.service.HandleAppleNotification(r.Context(), req.SignedPayload); err != nil {
		if errors.Is(err, appsub.ErrInvalidWebhookPayload) {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_notification", err.Error())
			return
		}
		httpx.WriteInternalError(w, h.log, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}
