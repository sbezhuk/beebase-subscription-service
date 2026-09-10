package subscription_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
)

type mockGoogleClient struct {
	subFunc func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error)
}

func (m *mockGoogleClient) GetSubscription(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
	if m.subFunc != nil {
		return m.subFunc(ctx, packageName, subscriptionID, purchaseToken)
	}
	return nil, google.ErrSubscriptionNotFound
}

func buildPubSubPayload(messageID, packageName, subID, purchaseToken string, notifType int, eventTimeMillis int64) []byte {
	inner := map[string]any{
		"version":         "1.0",
		"packageName":     packageName,
		"eventTimeMillis": eventTimeMillis,
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
			"publishTime": time.Now().UTC().Format(time.RFC3339Nano),
		},
		"subscription": "projects/beebase-production/subscriptions/play-rtdn-sub",
	}
	payload, _ := json.Marshal(envelope)
	return payload
}

func TestHandleGoogleNotification_ValidPubSubMessage(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	token := "valid-purchase-token-12345"
	orderID := "GPA.1234-5678-9012-34567"

	initialSub, err := subscription.New(userID, subscription.ProviderGoogle, appsub.ProductProMonthly)
	require.NoError(t, err)
	initialSub.PurchaseToken = &token
	initialSub.Status = subscription.StatusActive
	require.NoError(t, repo.Create(context.Background(), initialSub))

	expiry := time.Now().UTC().Add(30 * 24 * time.Hour)
	client := &mockGoogleClient{
		subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
			require.Equal(t, "com.beebase.production", packageName)
			require.Equal(t, "beebase_pro", subscriptionID)
			require.Equal(t, token, purchaseToken)
			return &google.Subscription{
				PackageName:   packageName,
				ProductID:     subscriptionID,
				BasePlanID:    "monthly",
				PurchaseToken: purchaseToken,
				State:         google.SubscriptionStateActive,
				ExpiryTime:    expiry,
				AutoRenewing:  true,
				LatestOrderID: orderID,
			}, nil
		},
	}

	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(client, "com.beebase.production")

	payload := buildPubSubPayload("msg-valid-1", "com.beebase.production", "beebase_pro", token, google.NotificationTypeSubscriptionRenewed, time.Now().UnixMilli())
	err = svc.HandleGoogleNotification(context.Background(), payload)
	require.NoError(t, err)

	updated, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
	require.NoError(t, err)
	require.Equal(t, subscription.StatusActive, updated.Status)
	require.Equal(t, appsub.ProductProMonthly, updated.ProductID)
	require.NotNil(t, updated.TransactionID)
	require.Equal(t, orderID, *updated.TransactionID)
	require.NotNil(t, updated.ExpiresAt)
	require.True(t, updated.ExpiresAt.Equal(expiry))
	require.NotNil(t, updated.AutoRenew)
	require.True(t, *updated.AutoRenew)
	require.NotNil(t, updated.LastEventAt)

	// Verify event was recorded
	event, err := repo.GetEvent(context.Background(), subscription.ProviderGoogle, "msg-valid-1")
	require.NoError(t, err)
	require.Equal(t, "msg-valid-1", event.EventID)
}

func TestHandleGoogleNotification_MalformedPayloads(t *testing.T) {
	repo := newFakeRepo()
	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(&mockGoogleClient{}, "com.beebase.production")

	tests := []struct {
		name    string
		payload []byte
	}{
		{name: "empty payload", payload: nil},
		{name: "invalid json", payload: []byte(`{not json}`)},
		{name: "missing messageId", payload: []byte(`{"message":{"data":"dGVzdA=="}}`)},
		{name: "missing data", payload: []byte(`{"message":{"messageId":"123"}}`)},
		{name: "invalid base64", payload: []byte(`{"message":{"messageId":"123","data":"invalid!!base64=="}}`)},
		{
			name: "missing purchase token in subscriptionNotification",
			payload: func() []byte {
				inner := map[string]any{
					"version":     "1.0",
					"packageName": "com.beebase.production",
					"subscriptionNotification": map[string]any{
						"notificationType": 1,
						"subscriptionId":   "beebase_pro",
						"purchaseToken":    "",
					},
				}
				b, _ := json.Marshal(inner)
				enc := base64.StdEncoding.EncodeToString(b)
				outer, _ := json.Marshal(map[string]any{
					"message": map[string]any{"messageId": "123", "data": enc},
				})
				return outer
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := svc.HandleGoogleNotification(context.Background(), tt.payload)
			require.Error(t, err)
			require.True(t, errors.Is(err, appsub.ErrInvalidWebhookPayload))
		})
	}
}

func TestHandleGoogleNotification_WrongPackageOrProduct(t *testing.T) {
	repo := newFakeRepo()
	token := "test-token"
	initialSub, err := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
	require.NoError(t, err)
	initialSub.PurchaseToken = &token
	initialSub.Status = subscription.StatusActive
	require.NoError(t, repo.Create(context.Background(), initialSub))

	apiCalled := false
	client := &mockGoogleClient{
		subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
			apiCalled = true
			return nil, nil
		},
	}

	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(client, "com.beebase.production")

	t.Run("wrong package name is ignored without mutating or calling API", func(t *testing.T) {
		apiCalled = false
		payload := buildPubSubPayload("msg-wrong-pkg", "com.other.app", "beebase_pro", token, 2, time.Now().UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)
		require.False(t, apiCalled)

		sub, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
		require.NoError(t, err)
		require.Equal(t, subscription.StatusActive, sub.Status)
	})

	t.Run("wrong subscription ID is ignored without mutating or calling API", func(t *testing.T) {
		apiCalled = false
		payload := buildPubSubPayload("msg-wrong-prod", "com.beebase.production", "other_product", token, 2, time.Now().UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)
		require.False(t, apiCalled)

		sub, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
		require.NoError(t, err)
		require.Equal(t, subscription.StatusActive, sub.Status)
	})
}

func TestHandleGoogleNotification_GoogleAPILookup(t *testing.T) {
	repo := newFakeRepo()
	token := "token-api-lookup"
	sub, _ := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
	sub.PurchaseToken = &token
	require.NoError(t, repo.Create(context.Background(), sub))

	t.Run("upstream Google API error returns error for Pub/Sub retry", func(t *testing.T) {
		client := &mockGoogleClient{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return nil, errors.New("upstream connection timeout")
			},
		}
		svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, "com.beebase.production")

		payload := buildPubSubPayload("msg-err-1", "com.beebase.production", "beebase_pro", token, 2, time.Now().UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.Error(t, err)
		require.Contains(t, err.Error(), "upstream connection timeout")
	})

	t.Run("upstream 404 ErrSubscriptionNotFound records event and returns nil", func(t *testing.T) {
		client := &mockGoogleClient{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return nil, google.ErrSubscriptionNotFound
			},
		}
		svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, "com.beebase.production")

		payload := buildPubSubPayload("msg-404-1", "com.beebase.production", "beebase_pro", token, 2, time.Now().UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)

		// Check event recorded to avoid endless retries
		event, err := repo.GetEvent(context.Background(), subscription.ProviderGoogle, "msg-404-1")
		require.NoError(t, err)
		require.Equal(t, "msg-404-1", event.EventID)
	})
}

func TestHandleGoogleNotification_StateMapping(t *testing.T) {
	tests := []struct {
		name           string
		notifType      int
		googleState    google.SubscriptionState
		autoRenewing   bool
		cancReason     google.CancellationReason
		expectedStatus subscription.Status
		expectedRenew  bool
	}{
		{
			name:           "Active with auto-renew",
			notifType:      google.NotificationTypeSubscriptionRenewed,
			googleState:    google.SubscriptionStateActive,
			autoRenewing:   true,
			expectedStatus: subscription.StatusActive,
			expectedRenew:  true,
		},
		{
			name:           "Active with auto-renew disabled (cancelled)",
			notifType:      google.NotificationTypeSubscriptionCanceled,
			googleState:    google.SubscriptionStateActive,
			autoRenewing:   false,
			expectedStatus: subscription.StatusCancelled,
			expectedRenew:  false,
		},
		{
			name:           "In Grace Period",
			notifType:      google.NotificationTypeSubscriptionInGracePeriod,
			googleState:    google.SubscriptionStateInGracePeriod,
			autoRenewing:   true,
			expectedStatus: subscription.StatusGracePeriod,
			expectedRenew:  true,
		},
		{
			name:           "On Hold (billing retry)",
			notifType:      google.NotificationTypeSubscriptionOnHold,
			googleState:    google.SubscriptionStateOnHold,
			autoRenewing:   false,
			expectedStatus: subscription.StatusBillingRetry,
			expectedRenew:  false,
		},
		{
			name:           "Canceled state with future expiry",
			notifType:      google.NotificationTypeSubscriptionCanceled,
			googleState:    google.SubscriptionStateCanceled,
			autoRenewing:   false,
			expectedStatus: subscription.StatusCancelled,
			expectedRenew:  false,
		},
		{
			name:           "Expired state",
			notifType:      google.NotificationTypeSubscriptionExpired,
			googleState:    google.SubscriptionStateExpired,
			autoRenewing:   false,
			expectedStatus: subscription.StatusExpired,
			expectedRenew:  false,
		},
		{
			name:           "Revoked via notification type 12",
			notifType:      google.NotificationTypeSubscriptionRevoked,
			googleState:    google.SubscriptionStateActive,
			autoRenewing:   false,
			expectedStatus: subscription.StatusRevoked,
			expectedRenew:  false,
		},
		{
			name:           "Revoked via developer initiated cancellation",
			notifType:      google.NotificationTypeSubscriptionCanceled,
			googleState:    google.SubscriptionStateCanceled,
			autoRenewing:   false,
			cancReason:     google.CancellationReasonDeveloperInitiated,
			expectedStatus: subscription.StatusRevoked,
			expectedRenew:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepo()
			token := "token-" + tt.name
			sub, _ := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
			sub.PurchaseToken = &token
			sub.Status = subscription.StatusActive
			require.NoError(t, repo.Create(context.Background(), sub))

			expiry := time.Now().UTC().Add(10 * 24 * time.Hour)
			client := &mockGoogleClient{
				subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
					return &google.Subscription{
						PackageName:        packageName,
						ProductID:          subscriptionID,
						BasePlanID:         "monthly",
						PurchaseToken:      purchaseToken,
						State:              tt.googleState,
						ExpiryTime:         expiry,
						AutoRenewing:       tt.autoRenewing,
						CancellationReason: tt.cancReason,
					}, nil
				},
			}

			svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
				WithGoogle(client, "com.beebase.production")

			payload := buildPubSubPayload("msg-"+tt.name, "com.beebase.production", "beebase_pro", token, tt.notifType, time.Now().UnixMilli())
			err := svc.HandleGoogleNotification(context.Background(), payload)
			require.NoError(t, err)

			updated, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
			require.NoError(t, err)
			require.Equal(t, tt.expectedStatus, updated.Status)
			require.NotNil(t, updated.AutoRenew)
			require.Equal(t, tt.expectedRenew, *updated.AutoRenew)
		})
	}
}

func TestHandleGoogleNotification_UnknownPurchaseToken(t *testing.T) {
	repo := newFakeRepo()
	token := "unknown-purchase-token"

	client := &mockGoogleClient{
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

	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(client, "com.beebase.production")

	payload := buildPubSubPayload("msg-unknown-token", "com.beebase.production", "beebase_pro", token, 2, time.Now().UnixMilli())
	err := svc.HandleGoogleNotification(context.Background(), payload)
	require.NoError(t, err)

	// Verify no subscription was created or guessed
	_, err = repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
	require.ErrorIs(t, err, subscription.ErrNotFound)

	// Idempotency event is recorded
	event, err := repo.GetEvent(context.Background(), subscription.ProviderGoogle, "msg-unknown-token")
	require.NoError(t, err)
	require.Equal(t, "msg-unknown-token", event.EventID)
}

func TestHandleGoogleNotification_DuplicateMessageID(t *testing.T) {
	repo := newFakeRepo()
	token := "token-duplicate-msg"
	sub, _ := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
	sub.PurchaseToken = &token
	sub.Status = subscription.StatusActive
	require.NoError(t, repo.Create(context.Background(), sub))

	callCount := 0
	client := &mockGoogleClient{
		subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
			callCount++
			return &google.Subscription{
				PackageName:   packageName,
				ProductID:     subscriptionID,
				BasePlanID:    "monthly",
				PurchaseToken: purchaseToken,
				State:         google.SubscriptionStateInGracePeriod,
				ExpiryTime:    time.Now().UTC().Add(5 * 24 * time.Hour),
				AutoRenewing:  true,
			}, nil
		},
	}

	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(client, "com.beebase.production")

	payload := buildPubSubPayload("msg-dup-1", "com.beebase.production", "beebase_pro", token, 6, time.Now().UnixMilli())

	// First call: succeeds and updates to grace_period
	err := svc.HandleGoogleNotification(context.Background(), payload)
	require.NoError(t, err)
	require.Equal(t, 1, callCount)

	updated, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
	require.NoError(t, err)
	require.Equal(t, subscription.StatusGracePeriod, updated.Status)

	// Second call with same payload / messageId: idempotent no-op
	err = svc.HandleGoogleNotification(context.Background(), payload)
	require.NoError(t, err)

	// Verify state remained grace_period and was not corrupted
	same, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
	require.NoError(t, err)
	require.Equal(t, subscription.StatusGracePeriod, same.Status)
}

func TestHandleGoogleNotification_OutOfOrderEvent(t *testing.T) {
	repo := newFakeRepo()
	token := "token-out-of-order"
	t1 := time.UnixMilli(1700000000000).UTC()
	t2 := time.UnixMilli(1690000000000).UTC() // Older than t1

	expiryT1 := time.Now().UTC().Add(30 * 24 * time.Hour)
	expiryT2 := time.Now().UTC().Add(10 * 24 * time.Hour) // Older than expiryT1

	sub, _ := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
	sub.PurchaseToken = &token
	sub.Status = subscription.StatusActive
	sub.LastEventAt = &t1
	sub.ExpiresAt = &expiryT1
	require.NoError(t, repo.Create(context.Background(), sub))

	t.Run("rejects event with older eventTimeMillis", func(t *testing.T) {
		client := &mockGoogleClient{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return &google.Subscription{
					PackageName:   packageName,
					ProductID:     subscriptionID,
					BasePlanID:    "monthly",
					PurchaseToken: purchaseToken,
					State:         google.SubscriptionStateCanceled,
					ExpiryTime:    expiryT1,
					AutoRenewing:  false,
				}, nil
			},
		}

		svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, "com.beebase.production")

		// Notification sent with t2 (older than existing t1)
		payload := buildPubSubPayload("msg-older-event", "com.beebase.production", "beebase_pro", token, 3, t2.UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)

		// State must remain active (update rejected)
		current, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
		require.NoError(t, err)
		require.Equal(t, subscription.StatusActive, current.Status)
	})

	t.Run("rejects event with older expiration date", func(t *testing.T) {
		tNewer := time.UnixMilli(1710000000000).UTC()
		client := &mockGoogleClient{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return &google.Subscription{
					PackageName:   packageName,
					ProductID:     subscriptionID,
					BasePlanID:    "monthly",
					PurchaseToken: purchaseToken,
					State:         google.SubscriptionStateCanceled,
					ExpiryTime:    expiryT2, // Older expiration
					AutoRenewing:  false,
				}, nil
			},
		}

		svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, "com.beebase.production")

		payload := buildPubSubPayload("msg-older-expiry", "com.beebase.production", "beebase_pro", token, 3, tNewer.UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)

		// State must remain active
		current, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
		require.NoError(t, err)
		require.Equal(t, subscription.StatusActive, current.Status)
	})
}

func TestHandleGoogleNotification_TestNotification(t *testing.T) {
	repo := newFakeRepo()
	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(&mockGoogleClient{}, "com.beebase.production")

	inner := map[string]any{
		"version":         "1.0",
		"packageName":     "com.beebase.production",
		"eventTimeMillis": time.Now().UnixMilli(),
		"testNotification": map[string]any{
			"version": "1.0",
		},
	}
	innerBytes, _ := json.Marshal(inner)
	encoded := base64.StdEncoding.EncodeToString(innerBytes)
	envelope := map[string]any{
		"message": map[string]any{
			"messageId": "msg-test-notif-1",
			"data":      encoded,
		},
	}
	payload, _ := json.Marshal(envelope)

	err := svc.HandleGoogleNotification(context.Background(), payload)
	require.NoError(t, err)

	event, err := repo.GetEvent(context.Background(), subscription.ProviderGoogle, "msg-test-notif-1")
	require.NoError(t, err)
	require.Equal(t, "TEST_NOTIFICATION", event.EventType)
}

func TestHandleGoogleNotification_RevocationWithEarlierExpiryApplied(t *testing.T) {
	repo := newFakeRepo()
	token := "token-revocation-expiry"
	tBase := time.UnixMilli(1700000000000).UTC()
	tNewer := time.UnixMilli(1700000100000).UTC()
	tOlder := time.UnixMilli(1699999900000).UTC()

	originalExpiry := time.Now().UTC().Add(365 * 24 * time.Hour) // 1 year from now
	revocationExpiry := time.Now().UTC()                         // immediate revocation (earlier expiry)

	sub, err := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProYearly)
	require.NoError(t, err)
	sub.PurchaseToken = &token
	sub.Status = subscription.StatusActive
	sub.LastEventAt = &tBase
	sub.ExpiresAt = &originalExpiry
	require.NoError(t, repo.Create(context.Background(), sub))

	t.Run("newer revocation with earlier expiry is successfully applied", func(t *testing.T) {
		client := &mockGoogleClient{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return &google.Subscription{
					PackageName:        packageName,
					ProductID:          subscriptionID,
					BasePlanID:         "yearly",
					PurchaseToken:      purchaseToken,
					State:              google.SubscriptionStateExpired,
					ExpiryTime:         revocationExpiry, // earlier than originalExpiry
					AutoRenewing:       false,
					CancellationReason: google.CancellationReasonDeveloperInitiated,
				}, nil
			},
		}

		svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, "com.beebase.production")

		// Newer event timestamp (tNewer > tBase), but earlier expiry
		payload := buildPubSubPayload("msg-revoke-newer", "com.beebase.production", "beebase_pro", token, google.NotificationTypeSubscriptionRevoked, tNewer.UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)

		updated, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
		require.NoError(t, err)
		require.Equal(t, subscription.StatusRevoked, updated.Status)
		require.False(t, updated.HasActiveAccess(time.Now().UTC()))
	})

	t.Run("older revocation is rejected by last_event_at ordering protection", func(t *testing.T) {
		// Reset to active subscription with last_event_at = tBase
		sub.Status = subscription.StatusActive
		sub.LastEventAt = &tBase
		sub.ExpiresAt = &originalExpiry
		require.NoError(t, repo.Update(context.Background(), sub))

		client := &mockGoogleClient{
			subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
				return &google.Subscription{
					PackageName:   packageName,
					ProductID:     subscriptionID,
					BasePlanID:    "yearly",
					PurchaseToken: purchaseToken,
					State:         google.SubscriptionStateActive,
					ExpiryTime:    revocationExpiry,
					AutoRenewing:  false,
				}, nil
			},
		}

		svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
			WithGoogle(client, "com.beebase.production")

		// Older event timestamp (tOlder < tBase)
		payload := buildPubSubPayload("msg-revoke-older", "com.beebase.production", "beebase_pro", token, google.NotificationTypeSubscriptionRevoked, tOlder.UnixMilli())
		err := svc.HandleGoogleNotification(context.Background(), payload)
		require.NoError(t, err)

		// State must remain active (rejected by last_event_at)
		current, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
		require.NoError(t, err)
		require.Equal(t, subscription.StatusActive, current.Status)
	})
}

func TestHandleGoogleNotification_GracePeriodToBillingRetry(t *testing.T) {
	repo := newFakeRepo()
	token := "token-grace-to-onhold"
	expiry := time.Now().UTC().Add(7 * 24 * time.Hour)

	sub, err := subscription.New(uuid.New(), subscription.ProviderGoogle, appsub.ProductProMonthly)
	require.NoError(t, err)
	sub.PurchaseToken = &token
	sub.Status = subscription.StatusGracePeriod
	sub.ExpiresAt = &expiry
	require.NoError(t, repo.Create(context.Background(), sub))

	client := &mockGoogleClient{
		subFunc: func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
			return &google.Subscription{
				PackageName:   packageName,
				ProductID:     subscriptionID,
				BasePlanID:    "monthly",
				PurchaseToken: purchaseToken,
				State:         google.SubscriptionStateOnHold,
				ExpiryTime:    expiry,
				AutoRenewing:  false,
			}, nil
		},
	}

	svc := appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger()).
		WithGoogle(client, "com.beebase.production")

	payload := buildPubSubPayload("msg-on-hold", "com.beebase.production", "beebase_pro", token, google.NotificationTypeSubscriptionOnHold, time.Now().UnixMilli())
	err = svc.HandleGoogleNotification(context.Background(), payload)
	require.NoError(t, err)

	updated, err := repo.FindByProviderAndPurchaseToken(context.Background(), subscription.ProviderGoogle, token)
	require.NoError(t, err)
	require.Equal(t, subscription.StatusBillingRetry, updated.Status)
	require.False(t, updated.HasActiveAccess(time.Now().UTC()))
}
