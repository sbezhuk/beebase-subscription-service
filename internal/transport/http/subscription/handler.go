// Package subscription provides HTTP handlers for the authenticated
// subscription API endpoints.
package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/httpx"
	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
)

// Error codes for subscription API failures.
const (
	CodeVerificationFailed  = "verification_failed"
	CodeUnsupportedProvider = "unsupported_provider"
)

// Handler exposes the subscription HTTP endpoints.
type Handler struct {
	service *appsub.Service
	log     *slog.Logger
}

func (h *Handler) DeleteUserData(ctx context.Context, userID uuid.UUID) error {
	return h.service.DeleteAllByUser(ctx, userID)
}

// NewHandler returns a Handler backed by service.
func NewHandler(service *appsub.Service, log *slog.Logger) *Handler {
	return &Handler{service: service, log: log}
}

func (h *Handler) requireUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	userID, ok := authmw.UserIDFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, authmw.CodeMissingAuthorization, "missing authentication")
		return uuid.Nil, false
	}
	return userID, true
}

// GetSubscription handles GET /api/v1/subscription.
func (h *Handler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	result, err := h.service.GetSubscription(r.Context(), userID)
	if err != nil {
		httpx.WriteInternalError(w, h.log, err)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, newSubscriptionResponse(result))
}

// VerifyPurchase handles POST /api/v1/subscription/verify.
func (h *Handler) VerifyPurchase(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "unable to read request body")
		return
	}

	var req VerifyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "request body must be valid JSON")
		return
	}

	if fields := req.Validate(); len(fields) > 0 {
		httpx.WriteValidationError(w, fields)
		return
	}

	result, verifyErr := h.verifyByProvider(r, userID, &req)
	if verifyErr != nil {
		h.writeServiceError(w, verifyErr)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, newSubscriptionResponse(result))
}

// RestorePurchases handles POST /api/v1/subscription/restore.
func (h *Handler) RestorePurchases(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUserID(w, r)
	if !ok {
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "unable to read request body")
		return
	}

	var req RestoreRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidBody, "request body must be valid JSON")
		return
	}

	if fields := req.Validate(); len(fields) > 0 {
		httpx.WriteValidationError(w, fields)
		return
	}

	verifyReq := req.toVerifyRequest()
	result, verifyErr := h.verifyByProvider(r, userID, &verifyReq)
	if verifyErr != nil {
		h.writeServiceError(w, verifyErr)
		return
	}

	httpx.WriteJSON(w, http.StatusOK, newSubscriptionResponse(result))
}

var errUnsupportedProvider = errors.New("unsupported provider")

func (h *Handler) verifyByProvider(r *http.Request, userID uuid.UUID, req *VerifyRequest) (*appsub.VerificationResult, error) {
	switch strings.ToLower(req.Provider) {
	case "apple":
		return h.service.VerifyApplePurchase(r.Context(), userID, req.SignedTransaction)
	case "google":
		return h.service.VerifyGooglePurchase(r.Context(), userID, req.PurchaseToken, req.SubscriptionID, req.PackageName)
	default:
		return nil, errUnsupportedProvider
	}
}

func (h *Handler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, appsub.ErrInvalidWebhookPayload),
		errors.Is(err, appsub.ErrUnsupportedProduct),
		errors.Is(err, appsub.ErrBundleIDMismatch):
		httpx.WriteError(w, http.StatusBadRequest, CodeVerificationFailed, err.Error())
	case errors.Is(err, errUnsupportedProvider):
		httpx.WriteError(w, http.StatusBadRequest, CodeUnsupportedProvider, "provider must be apple or google")
	default:
		httpx.WriteInternalError(w, h.log, err)
	}
}
