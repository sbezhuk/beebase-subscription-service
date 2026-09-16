// Package http wires the HTTP transport for the subscription service.
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/httpx"
	"github.com/sbezhuk/beebase-common/internalauth"
	subhttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http/subscription"
)

// NewRouter builds the root HTTP handler for the subscription service.
func NewRouter(
	log *slog.Logger,
	db *pgxpool.Pool,
	subscriptionHandler *subhttp.Handler,
	appleWebhookHandler http.Handler,
	googleWebhookHandler http.Handler,
	tokenParser authmw.AccessTokenParser,
	internalTokens ...string,
) http.Handler {
	internalToken := ""
	if len(internalTokens) > 0 {
		internalToken = internalTokens[0]
	}
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", HealthHandler)
	r.Get("/ready", ReadyHandler(db))

	// Authenticated subscription endpoints
	r.Route("/api/v1/subscription", func(r chi.Router) {
		r.Use(authmw.RequireAuth(tokenParser))
		r.Get("/", subscriptionHandler.GetSubscription)
		r.Post("/verify", subscriptionHandler.VerifyPurchase)
		r.Post("/restore", subscriptionHandler.RestorePurchases)
	})

	if appleWebhookHandler != nil {
		r.Method(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", appleWebhookHandler)
		r.Method(http.MethodPost, "/api/v1/subscription/webhooks/apple", appleWebhookHandler)
	}

	if googleWebhookHandler != nil {
		r.Method(http.MethodPost, "/api/v1/subscriptions/webhooks/google", googleWebhookHandler)
		r.Method(http.MethodPost, "/api/v1/subscription/webhooks/google", googleWebhookHandler)
	}
	r.With(internalauth.RequireAuth(internalToken)).Delete("/internal/api/v1/users/{userID}", func(w http.ResponseWriter, req *http.Request) {
		id, err := uuid.Parse(chi.URLParam(req, "userID"))
		if err != nil {
			httpx.WriteError(w, 400, "invalid_user_id", "invalid user id")
			return
		}
		if err := subscriptionHandler.DeleteUserData(req.Context(), id); err != nil {
			httpx.WriteError(w, 500, "cleanup_failed", "could not delete subscription data")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	return r
}

func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
