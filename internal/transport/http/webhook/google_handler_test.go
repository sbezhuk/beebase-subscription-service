package webhook_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
	webhookhttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http/webhook"
)

type mockGoogleClientForHandler struct {
	subFunc func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error)
}

func (m *mockGoogleClientForHandler) GetSubscription(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
	if m.subFunc != nil {
		return m.subFunc(ctx, packageName, subscriptionID, purchaseToken)
	}
	return nil, google.ErrSubscriptionNotFound
}

func buildTestPubSubBody(messageID, packageName, subID, purchaseToken string, notifType int) []byte {
	inner := map[string]any{
		"version":         "1.0",
		"packageName":     packageName,
		"eventTimeMillis": time.Now().UnixMilli(),
		"subscriptionNotification": map[string]any{
			"version":          "1.0",
			"notificationType": notifType,
			"purchaseToken":    purchaseToken,
			"subscriptionId":   subID,
		},
	}
	innerBytes, _ := json.Marshal(inner)
	encoded := base64.StdEncoding.EncodeToString(innerBytes)

	envelope := map[string]any{
		"message": map[string]any{
			"messageId":   messageID,
			"data":        encoded,
			"publishTime": time.Now().UTC().Format(time.RFC3339),
		},
		"subscription": "projects/beebase-production/subscriptions/play-rtdn-sub",
	}
	payload, _ := json.Marshal(envelope)
	return payload
}

func TestGoogleHandler_ServeHTTP(t *testing.T) {
	pkgName := "com.beebase.production"

	t.Run("POST with valid Pub/Sub message returns 200 OK", func(t *testing.T) {
		repo := newMockRepo()
		token := "handler-test-token"
		sub, _ := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
		sub.PurchaseToken = &token
		sub.Status = subscription.StatusActive
		_ = repo.Create(context.Background(), sub)

		client := &mockGoogleClientForHandler{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return &google.Subscription{
					PackageName:   packageName,
					ProductID:     subscriptionID,
					BasePlanID:    "monthly",
					PurchaseToken: purchaseToken,
					State:         google.SubscriptionStateActive,
					ExpiryTime:    time.Now().UTC().Add(30 * 24 * time.Hour),
					AutoRenewing:  true,
				}, nil
			},
		}

		svc := appsub.NewService(repo, nil, pkgName, subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, pkgName)
		handler := webhookhttp.NewGoogleHandler(svc, testLogger())

		body := buildTestPubSubBody("msg-http-1", pkgName, "beebase_pro", token, 2)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/google", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"status":"ok"`)
	})

	t.Run("GET method returns 405 Method Not Allowed", func(t *testing.T) {
		svc := appsub.NewService(newMockRepo(), nil, pkgName, subscription.EnvironmentProduction, testLogger())
		handler := webhookhttp.NewGoogleHandler(svc, testLogger())

		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/webhooks/google", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("Malformed JSON returns 400 Bad Request", func(t *testing.T) {
		svc := appsub.NewService(newMockRepo(), nil, pkgName, subscription.EnvironmentProduction, testLogger())
		handler := webhookhttp.NewGoogleHandler(svc, testLogger())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/google", bytes.NewReader([]byte(`not-json`)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "invalid_notification")
	})

	t.Run("Invalid base64 in data returns 400 Bad Request", func(t *testing.T) {
		svc := appsub.NewService(newMockRepo(), nil, pkgName, subscription.EnvironmentProduction, testLogger())
		handler := webhookhttp.NewGoogleHandler(svc, testLogger())

		payload, _ := json.Marshal(map[string]any{
			"message": map[string]any{
				"messageId": "msg-bad-b64",
				"data":      "!!not-base64@@",
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/google", bytes.NewReader(payload))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "invalid_notification")
	})

	t.Run("Missing messageId returns 400 Bad Request", func(t *testing.T) {
		svc := appsub.NewService(newMockRepo(), nil, pkgName, subscription.EnvironmentProduction, testLogger())
		handler := webhookhttp.NewGoogleHandler(svc, testLogger())

		payload, _ := json.Marshal(map[string]any{
			"message": map[string]any{
				"data": "dGVzdA==",
			},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/google", bytes.NewReader(payload))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "invalid_notification")
	})

	t.Run("Internal error returns 500 Internal Server Error", func(t *testing.T) {
		repo := newMockRepo()
		repo.failNextWith = errors.New("db connection failure")
		client := &mockGoogleClientForHandler{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return &google.Subscription{
					PackageName:   packageName,
					ProductID:     subscriptionID,
					PurchaseToken: purchaseToken,
					State:         google.SubscriptionStateActive,
				}, nil
			},
		}

		svc := appsub.NewService(repo, nil, pkgName, subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, pkgName)
		handler := webhookhttp.NewGoogleHandler(svc, testLogger())

		body := buildTestPubSubBody("msg-db-err", pkgName, "beebase_pro", "tok", 2)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/google", bytes.NewReader(body))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "internal_error")
	})
}
