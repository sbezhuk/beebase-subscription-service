package webhook_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
	webhookhttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http/webhook"
)

type mockRepo struct {
	mu           sync.Mutex
	failNextWith error
	events       map[string]bool
}

func newMockRepo() *mockRepo {
	return &mockRepo{events: make(map[string]bool)}
}

func (m *mockRepo) FindByID(_ context.Context, _ uuid.UUID) (*subscription.Subscription, error) {
	return nil, subscription.ErrNotFound
}

func (m *mockRepo) FindByUserID(_ context.Context, _ uuid.UUID) (*subscription.Subscription, error) {
	return nil, subscription.ErrNotFound
}

func (m *mockRepo) FindByProviderAndTransaction(_ context.Context, _ subscription.Provider, _ string) (*subscription.Subscription, error) {
	return nil, subscription.ErrNotFound
}

func (m *mockRepo) FindByProviderAndPurchaseToken(_ context.Context, _ subscription.Provider, _ string) (*subscription.Subscription, error) {
	return nil, subscription.ErrNotFound
}

func (m *mockRepo) Create(_ context.Context, _ *subscription.Subscription) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNextWith != nil {
		err := m.failNextWith
		m.failNextWith = nil
		return err
	}
	return nil
}

func (m *mockRepo) Update(_ context.Context, _ *subscription.Subscription) error {
	return nil
}

func (m *mockRepo) Upsert(_ context.Context, _ *subscription.Subscription) error {
	return nil
}

func (m *mockRepo) RecordEventIfNotExists(_ context.Context, event *subscription.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNextWith != nil {
		err := m.failNextWith
		m.failNextWith = nil
		return false, err
	}
	key := string(event.Provider) + ":" + event.EventID
	if m.events[key] {
		return false, nil
	}
	m.events[key] = true
	return true, nil
}

func (m *mockRepo) GetEvent(_ context.Context, _ subscription.Provider, _ string) (*subscription.Event, error) {
	return nil, subscription.ErrNotFound
}

func (m *mockRepo) WithTx(ctx context.Context, fn func(txRepo subscription.Repository) error) error {
	return fn(m)
}

type mockAppleVerifier struct {
	verifyNotifFn func(signedPayload string) (*apple.NotificationPayload, error)
	verifyTxFn    func(signedTx string) (*apple.TransactionInfo, error)
}

func (m *mockAppleVerifier) VerifyNotification(signedPayload string) (*apple.NotificationPayload, error) {
	if m.verifyNotifFn != nil {
		return m.verifyNotifFn(signedPayload)
	}
	return nil, nil
}

func (m *mockAppleVerifier) VerifyTransaction(signedTx string) (*apple.TransactionInfo, error) {
	if m.verifyTxFn != nil {
		return m.verifyTxFn(signedTx)
	}
	return nil, nil
}

func (m *mockAppleVerifier) VerifyRenewalInfo(_ string) (*apple.RenewalInfo, error) {
	return nil, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAppleHandler_ServeHTTP(t *testing.T) {
	bundleID := "com.beebase.production"

	t.Run("non-POST method returns 405", func(t *testing.T) {
		repo := newMockRepo()
		verifier := &mockAppleVerifier{}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/webhooks/apple", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected status 405, got %d", rec.Code)
		}
	})

	t.Run("malformed body returns 400", func(t *testing.T) {
		repo := newMockRepo()
		verifier := &mockAppleVerifier{}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte("not-json")))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("missing signedPayload returns 400", func(t *testing.T) {
		repo := newMockRepo()
		verifier := &mockAppleVerifier{}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(`{"other":"field"}`)))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("invalid signed payload returns 400", func(t *testing.T) {
		repo := newMockRepo()
		verifier := &mockAppleVerifier{
			verifyNotifFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return nil, errors.New("invalid signature")
			},
		}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		body := `{"signedPayload":"invalid_jwt_payload"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", rec.Code)
		}
	})

	t.Run("valid notification returns 200", func(t *testing.T) {
		repo := newMockRepo()
		userUUID := uuid.New()
		verifier := &mockAppleVerifier{
			verifyNotifFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-uuid-valid",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "valid_tx",
					},
				}, nil
			},
			verifyTxFn: func(signedTx string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: "orig_tx_123",
					TransactionID:         "tx_123",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
					AppAccountToken:       userUUID.String(),
				}, nil
			},
		}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		body := `{"signedPayload":"valid_signed_jwt"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("duplicate notification returns 200", func(t *testing.T) {
		repo := newMockRepo()
		userUUID := uuid.New()
		verifier := &mockAppleVerifier{
			verifyNotifFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-uuid-duplicate",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "valid_tx",
					},
				}, nil
			},
			verifyTxFn: func(signedTx string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: "orig_tx_123",
					TransactionID:         "tx_123",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
					AppAccountToken:       userUUID.String(),
				}, nil
			},
		}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		body := `{"signedPayload":"duplicate_jwt"}`

		// First delivery
		req1 := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(body)))
		rec1 := httptest.NewRecorder()
		handler.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusOK {
			t.Fatalf("first delivery failed with code %d", rec1.Code)
		}

		// Second delivery (duplicate notificationUUID)
		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(body)))
		rec2 := httptest.NewRecorder()
		handler.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Errorf("expected status 200 on duplicate, got %d", rec2.Code)
		}
	})

	t.Run("unknown notification type returns 200", func(t *testing.T) {
		repo := newMockRepo()
		verifier := &mockAppleVerifier{
			verifyNotifFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: "UNKNOWN_FUTURE_APPLE_TYPE",
					NotificationUUID: "notif-uuid-unknown",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "valid_tx",
					},
				}, nil
			},
			verifyTxFn: func(signedTx string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: "orig_tx_unknown",
					TransactionID:         "tx_unknown",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
				}, nil
			},
		}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		body := `{"signedPayload":"unknown_jwt"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status 200 for unknown notification type, got %d", rec.Code)
		}
	})

	t.Run("database failure returns 500", func(t *testing.T) {
		repo := newMockRepo()
		repo.failNextWith = errors.New("database connection refused")

		userUUID := uuid.New()
		verifier := &mockAppleVerifier{
			verifyNotifFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-uuid-db-fail",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "valid_tx",
					},
				}, nil
			},
			verifyTxFn: func(signedTx string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: "orig_tx_fail",
					TransactionID:         "tx_fail",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
					AppAccountToken:       userUUID.String(),
				}, nil
			},
		}
		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		handler := webhookhttp.NewAppleHandler(svc, testLogger())

		body := `{"signedPayload":"valid_payload"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhooks/apple", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Errorf("expected status 500 on db failure, got %d", rec.Code)
		}
	})
}
