package subscription_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
)

// --- In-memory fake repository ---

type fakeRepo struct {
	mu           sync.Mutex
	subsByID     map[uuid.UUID]*subscription.Subscription
	subsByTrans  map[string]*subscription.Subscription
	subsByToken  map[string]*subscription.Subscription
	events       map[string]*subscription.Event // key: provider + ":" + event_id
	failNextWith error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		subsByID:    make(map[uuid.UUID]*subscription.Subscription),
		subsByTrans: make(map[string]*subscription.Subscription),
		subsByToken: make(map[string]*subscription.Subscription),
		events:      make(map[string]*subscription.Event),
	}
}

func (f *fakeRepo) FindByID(_ context.Context, id uuid.UUID) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sub, ok := f.subsByID[id]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *sub
	return &cp, nil
}

func (f *fakeRepo) FindByUserID(_ context.Context, userID uuid.UUID) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, sub := range f.subsByID {
		if sub.UserID == userID {
			cp := *sub
			return &cp, nil
		}
	}
	return nil, subscription.ErrNotFound
}

func (f *fakeRepo) FindByProviderAndTransaction(_ context.Context, provider subscription.Provider, originalTransactionID string) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(provider) + ":" + originalTransactionID
	sub, ok := f.subsByTrans[key]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *sub
	return &cp, nil
}

func (f *fakeRepo) FindByProviderAndPurchaseToken(_ context.Context, provider subscription.Provider, purchaseToken string) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(provider) + ":" + purchaseToken
	sub, ok := f.subsByToken[key]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *sub
	return &cp, nil
}

func (f *fakeRepo) Create(_ context.Context, s *subscription.Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextWith != nil {
		err := f.failNextWith
		f.failNextWith = nil
		return err
	}
	cp := *s
	f.subsByID[s.ID] = &cp
	if s.OriginalTransactionID != nil {
		key := string(s.Provider) + ":" + *s.OriginalTransactionID
		f.subsByTrans[key] = &cp
	}
	if s.PurchaseToken != nil {
		key := string(s.Provider) + ":" + *s.PurchaseToken
		f.subsByToken[key] = &cp
	}
	return nil
}

func (f *fakeRepo) Update(_ context.Context, s *subscription.Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextWith != nil {
		err := f.failNextWith
		f.failNextWith = nil
		return err
	}
	cp := *s
	f.subsByID[s.ID] = &cp
	if s.OriginalTransactionID != nil {
		key := string(s.Provider) + ":" + *s.OriginalTransactionID
		f.subsByTrans[key] = &cp
	}
	if s.PurchaseToken != nil {
		key := string(s.Provider) + ":" + *s.PurchaseToken
		f.subsByToken[key] = &cp
	}
	return nil
}

func (f *fakeRepo) Upsert(ctx context.Context, s *subscription.Subscription) error {
	f.mu.Lock()
	_, exists := f.subsByID[s.ID]
	f.mu.Unlock()
	if exists {
		return f.Update(ctx, s)
	}
	return f.Create(ctx, s)
}

func (f *fakeRepo) RecordEventIfNotExists(_ context.Context, event *subscription.Event) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextWith != nil {
		err := f.failNextWith
		f.failNextWith = nil
		return false, err
	}
	key := string(event.Provider) + ":" + event.EventID
	if _, exists := f.events[key]; exists {
		return false, nil
	}
	cp := *event
	f.events[key] = &cp
	return true, nil
}

func (f *fakeRepo) GetEvent(_ context.Context, provider subscription.Provider, eventID string) (*subscription.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(provider) + ":" + eventID
	ev, ok := f.events[key]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *ev
	return &cp, nil
}

func (f *fakeRepo) WithTx(ctx context.Context, fn func(txRepo subscription.Repository) error) error {
	// Execute within the fake repo
	return fn(f)
}

// --- Mock Apple verifier ---

type mockVerifier struct {
	notificationFn func(signedPayload string) (*apple.NotificationPayload, error)
	transactionFn  func(signedTransactionInfo string) (*apple.TransactionInfo, error)
	renewalFn      func(signedRenewalInfo string) (*apple.RenewalInfo, error)
}

type mockAppleAPI struct {
	response *apple.SubscriptionResponse
	err      error
}

func (m *mockAppleAPI) GetSubscription(context.Context, string) (*apple.SubscriptionResponse, error) {
	return m.response, m.err
}

func (m *mockVerifier) VerifyNotification(signedPayload string) (*apple.NotificationPayload, error) {
	if m.notificationFn != nil {
		return m.notificationFn(signedPayload)
	}
	return nil, nil
}

func (m *mockVerifier) VerifyTransaction(signedTransactionInfo string) (*apple.TransactionInfo, error) {
	if m.transactionFn != nil {
		return m.transactionFn(signedTransactionInfo)
	}
	return nil, nil
}

func (m *mockVerifier) VerifyRenewalInfo(signedRenewalInfo string) (*apple.RenewalInfo, error) {
	if m.renewalFn != nil {
		return m.renewalFn(signedRenewalInfo)
	}
	return nil, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestVerifyApplePurchase_ReconcilesAuthoritativeAPIState(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	local := &apple.TransactionInfo{
		OriginalTransactionID: "orig-1", TransactionID: "local-tx", BundleID: "com.beebase.production",
		ProductID: appsub.ProductProMonthly, Environment: "Production", ExpiresDate: time.Now().Add(24 * time.Hour).UnixMilli(),
	}
	authoritative := *local
	authoritative.TransactionID = "api-tx"
	authoritative.ExpiresDate = time.Now().Add(-time.Hour).UnixMilli()
	verifier := &mockVerifier{transactionFn: func(signed string) (*apple.TransactionInfo, error) {
		if signed == "api-signed" {
			return &authoritative, nil
		}
		return local, nil
	}}
	svc := appsub.NewService(repo, verifier, local.BundleID, subscription.EnvironmentProduction, testLogger()).WithAppleAPI(&mockAppleAPI{
		response: &apple.SubscriptionResponse{Data: []apple.SubscriptionData{{LastTransactions: []apple.LastTransaction{{Status: 2, SignedTransactionInfo: "api-signed"}}}}},
	})
	result, err := svc.VerifyApplePurchase(context.Background(), userID, "local-signed")
	if err != nil {
		t.Fatalf("VerifyApplePurchase: %v", err)
	}
	if result.Subscription.Status != subscription.StatusExpired {
		t.Fatalf("status = %s, want expired", result.Subscription.Status)
	}
	if *result.Subscription.TransactionID != "api-tx" {
		t.Fatalf("transaction ID = %s, want api-tx", *result.Subscription.TransactionID)
	}
}

func TestVerifyApplePurchase_APIUnavailableKeepsLocalVerification(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	local := &apple.TransactionInfo{
		OriginalTransactionID: "orig-2", TransactionID: "local-tx", BundleID: "com.beebase.production",
		ProductID: appsub.ProductProMonthly, Environment: "Production", ExpiresDate: time.Now().Add(24 * time.Hour).UnixMilli(),
	}
	svc := appsub.NewService(repo, &mockVerifier{transactionFn: func(string) (*apple.TransactionInfo, error) { return local, nil }}, local.BundleID, subscription.EnvironmentProduction, testLogger()).WithAppleAPI(&mockAppleAPI{err: apple.ErrAPIUnavailable})
	result, err := svc.VerifyApplePurchase(context.Background(), userID, "local-signed")
	if err != nil {
		t.Fatalf("VerifyApplePurchase: %v", err)
	}
	if result.Subscription.Status != subscription.StatusActive {
		t.Fatalf("status = %s, want active fallback", result.Subscription.Status)
	}
}

func TestHandleAppleNotification_Mapping(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"

	cases := []struct {
		name             string
		notificationType string
		subtype          string
		renewalStatus    int
		gracePeriodExp   int64
		expectedStatus   subscription.Status
		expectedAutoRen  bool
	}{
		{
			name:             "SUBSCRIBED -> active",
			notificationType: apple.NotificationTypeSubscribed,
			subtype:          apple.SubtypeInitialBuy,
			renewalStatus:    1,
			expectedStatus:   subscription.StatusActive,
			expectedAutoRen:  true,
		},
		{
			name:             "DID_RENEW -> active",
			notificationType: apple.NotificationTypeDidRenew,
			renewalStatus:    1,
			expectedStatus:   subscription.StatusActive,
			expectedAutoRen:  true,
		},
		{
			name:             "DID_CHANGE_RENEWAL_STATUS auto-renew disabled -> cancelled",
			notificationType: apple.NotificationTypeDidChangeRenewalStatus,
			subtype:          apple.SubtypeAutoRenewDisabled,
			renewalStatus:    0,
			expectedStatus:   subscription.StatusCancelled,
			expectedAutoRen:  false,
		},
		{
			name:             "DID_CHANGE_RENEWAL_STATUS auto-renew enabled -> active",
			notificationType: apple.NotificationTypeDidChangeRenewalStatus,
			subtype:          apple.SubtypeAutoRenewEnabled,
			renewalStatus:    1,
			expectedStatus:   subscription.StatusActive,
			expectedAutoRen:  true,
		},
		{
			name:             "DID_FAIL_TO_RENEW standard -> billing_retry",
			notificationType: apple.NotificationTypeDidFailToRenew,
			subtype:          apple.SubtypeBillingRetry,
			expectedStatus:   subscription.StatusBillingRetry,
			expectedAutoRen:  false,
		},
		{
			name:             "DID_FAIL_TO_RENEW with grace period -> grace_period",
			notificationType: apple.NotificationTypeDidFailToRenew,
			subtype:          apple.SubtypeGracePeriod,
			gracePeriodExp:   time.Now().Add(16 * 24 * time.Hour).UnixMilli(),
			expectedStatus:   subscription.StatusGracePeriod,
			expectedAutoRen:  false,
		},
		{
			name:             "GRACE_PERIOD_EXPIRED -> expired",
			notificationType: apple.NotificationTypeGracePeriodExpired,
			expectedStatus:   subscription.StatusExpired,
			expectedAutoRen:  false,
		},
		{
			name:             "EXPIRED -> expired",
			notificationType: apple.NotificationTypeExpired,
			subtype:          apple.SubtypeVoluntary,
			expectedStatus:   subscription.StatusExpired,
			expectedAutoRen:  false,
		},
		{
			name:             "REFUND -> revoked",
			notificationType: apple.NotificationTypeRefund,
			expectedStatus:   subscription.StatusRevoked,
			expectedAutoRen:  false,
		},
		{
			name:             "REVOKE -> revoked",
			notificationType: apple.NotificationTypeRevoke,
			expectedStatus:   subscription.StatusRevoked,
			expectedAutoRen:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			userID := uuid.New()
			origTransID := "apple_orig_trans_1001"

			// Seed existing active subscription
			seedSub := &subscription.Subscription{
				ID:                    uuid.New(),
				UserID:                userID,
				Provider:              subscription.ProviderApple,
				ProductID:             appsub.ProductProMonthly,
				OriginalTransactionID: &origTransID,
				Status:                subscription.StatusActive,
				CreatedAt:             time.Now().UTC(),
				UpdatedAt:             time.Now().UTC(),
			}
			_ = repo.Create(ctx, seedSub)

			verifier := &mockVerifier{
				notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
					return &apple.NotificationPayload{
						NotificationType: tc.notificationType,
						Subtype:          tc.subtype,
						NotificationUUID: "notif-uuid-" + tc.notificationType,
						Data: apple.NotificationData{
							BundleID:              bundleID,
							SignedTransactionInfo: "signed_tx_info",
							SignedRenewalInfo:     "signed_renewal_info",
						},
					}, nil
				},
				transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
					return &apple.TransactionInfo{
						OriginalTransactionID: origTransID,
						TransactionID:         "tx_2002",
						BundleID:              bundleID,
						ProductID:             appsub.ProductProMonthly,
						ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
						Environment:           "Sandbox",
					}, nil
				},
				renewalFn: func(signedRenewalInfo string) (*apple.RenewalInfo, error) {
					return &apple.RenewalInfo{
						OriginalTransactionID:  origTransID,
						AutoRenewStatus:        tc.renewalStatus,
						GracePeriodExpiresDate: tc.gracePeriodExp,
					}, nil
				},
			}

			svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
			err := svc.HandleAppleNotification(ctx, "mock_signed_payload")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			updated, err := repo.FindByID(ctx, seedSub.ID)
			if err != nil {
				t.Fatalf("FindByID: %v", err)
			}
			if updated.Status != tc.expectedStatus {
				t.Errorf("got status %s, want %s", updated.Status, tc.expectedStatus)
			}
			if updated.AutoRenew != nil && *updated.AutoRenew != tc.expectedAutoRen {
				t.Errorf("got autoRenew %v, want %v", *updated.AutoRenew, tc.expectedAutoRen)
			}
		})
	}
}

func TestHandleAppleNotification_ProductAndBundleValidation(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"

	t.Run("yearly product accepted", func(t *testing.T) {
		repo := newFakeRepo()
		userID := uuid.New()
		origTransID := "apple_yearly_1001"

		seedSub := &subscription.Subscription{
			ID:                    uuid.New(),
			UserID:                userID,
			Provider:              subscription.ProviderApple,
			ProductID:             appsub.ProductProYearly,
			OriginalTransactionID: &origTransID,
			Status:                subscription.StatusActive,
		}
		_ = repo.Create(ctx, seedSub)

		verifier := &mockVerifier{
			notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeDidRenew,
					NotificationUUID: "notif-yearly-1",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "signed_tx_info",
					},
				}, nil
			},
			transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: origTransID,
					TransactionID:         "tx_yearly",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProYearly,
					ExpiresDate:           time.Now().Add(365 * 24 * time.Hour).UnixMilli(),
					Environment:           "Production",
				}, nil
			},
		}

		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentProduction, testLogger())
		if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		updated, _ := repo.FindByID(ctx, seedSub.ID)
		if updated.ProductID != appsub.ProductProYearly {
			t.Errorf("got product %s, want %s", updated.ProductID, appsub.ProductProYearly)
		}
	})

	t.Run("unknown product ignored safely without mutating state", func(t *testing.T) {
		repo := newFakeRepo()
		userID := uuid.New()
		origTransID := "apple_unknown_prod"

		seedSub := &subscription.Subscription{
			ID:                    uuid.New(),
			UserID:                userID,
			Provider:              subscription.ProviderApple,
			ProductID:             appsub.ProductProMonthly,
			OriginalTransactionID: &origTransID,
			Status:                subscription.StatusActive,
		}
		_ = repo.Create(ctx, seedSub)

		verifier := &mockVerifier{
			notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-unknown-prod",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "signed_tx_info",
					},
				}, nil
			},
			transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: origTransID,
					TransactionID:         "tx_unknown",
					BundleID:              bundleID,
					ProductID:             "unrelated_game_coins",
				}, nil
			},
		}

		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// State must remain untouched
		unchanged, _ := repo.FindByID(ctx, seedSub.ID)
		if unchanged.ProductID != appsub.ProductProMonthly {
			t.Errorf("product was changed to %s", unchanged.ProductID)
		}
	})

	t.Run("incorrect Bundle ID ignored safely without mutating state", func(t *testing.T) {
		repo := newFakeRepo()
		userID := uuid.New()
		origTransID := "apple_wrong_bundle"

		seedSub := &subscription.Subscription{
			ID:                    uuid.New(),
			UserID:                userID,
			Provider:              subscription.ProviderApple,
			ProductID:             appsub.ProductProMonthly,
			OriginalTransactionID: &origTransID,
			Status:                subscription.StatusActive,
		}
		_ = repo.Create(ctx, seedSub)

		verifier := &mockVerifier{
			notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-wrong-bundle",
					Data: apple.NotificationData{
						BundleID:              "com.othercompany.app",
						SignedTransactionInfo: "signed_tx_info",
					},
				}, nil
			},
			transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: origTransID,
					TransactionID:         "tx_wrong_bundle",
					BundleID:              "com.othercompany.app",
					ProductID:             appsub.ProductProMonthly,
				}, nil
			},
		}

		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		unchanged, _ := repo.FindByID(ctx, seedSub.ID)
		if unchanged.Status != subscription.StatusActive {
			t.Errorf("status was unexpectedly changed")
		}
	})
}

func TestHandleAppleNotification_UserMapping(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"

	t.Run("appAccountToken resolves user on initial purchase", func(t *testing.T) {
		repo := newFakeRepo()
		userID := uuid.New()
		origTransID := "apple_token_user_1"

		verifier := &mockVerifier{
			notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-app-account-token",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "signed_tx_info",
					},
				}, nil
			},
			transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: origTransID,
					TransactionID:         "tx_token_1",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
					AppAccountToken:       userID.String(),
					ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
					Environment:           "Production",
				}, nil
			},
		}

		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentProduction, testLogger())
		if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		found, err := repo.FindByProviderAndTransaction(ctx, subscription.ProviderApple, origTransID)
		if err != nil {
			t.Fatalf("FindByProviderAndTransaction: %v", err)
		}
		if found.UserID != userID {
			t.Errorf("got userID %s, want %s", found.UserID, userID)
		}
		if found.Status != subscription.StatusActive {
			t.Errorf("got status %s, want active", found.Status)
		}
	})

	t.Run("missing appAccountToken with existing subscription resolves user via originalTransactionId", func(t *testing.T) {
		repo := newFakeRepo()
		userID := uuid.New()
		origTransID := "apple_orig_resolved"

		seedSub := &subscription.Subscription{
			ID:                    uuid.New(),
			UserID:                userID,
			Provider:              subscription.ProviderApple,
			ProductID:             appsub.ProductProMonthly,
			OriginalTransactionID: &origTransID,
			Status:                subscription.StatusCancelled,
		}
		_ = repo.Create(ctx, seedSub)

		verifier := &mockVerifier{
			notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeDidRenew,
					NotificationUUID: "notif-no-token-existing-sub",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "signed_tx_info",
					},
				}, nil
			},
			transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: origTransID,
					TransactionID:         "tx_renew_2",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
					AppAccountToken:       "", // Empty
					ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
					Environment:           "Sandbox",
				}, nil
			},
		}

		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		updated, _ := repo.FindByID(ctx, seedSub.ID)
		if updated.UserID != userID {
			t.Errorf("got userID %s, want %s", updated.UserID, userID)
		}
		if updated.Status != subscription.StatusActive {
			t.Errorf("got status %s, want active", updated.Status)
		}
	})

	t.Run("unresolved user does not create fake subscription, records event for audit", func(t *testing.T) {
		repo := newFakeRepo()
		origTransID := "apple_orphan_transaction"

		verifier := &mockVerifier{
			notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
				return &apple.NotificationPayload{
					NotificationType: apple.NotificationTypeSubscribed,
					NotificationUUID: "notif-orphan-event",
					Data: apple.NotificationData{
						BundleID:              bundleID,
						SignedTransactionInfo: "signed_tx_info",
					},
				}, nil
			},
			transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
				return &apple.TransactionInfo{
					OriginalTransactionID: origTransID,
					TransactionID:         "tx_orphan",
					BundleID:              bundleID,
					ProductID:             appsub.ProductProMonthly,
					AppAccountToken:       "", // No user ID
				}, nil
			},
		}

		svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
		if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Subscription must NOT be created
		_, err := repo.FindByProviderAndTransaction(ctx, subscription.ProviderApple, origTransID)
		if !errors.Is(err, subscription.ErrNotFound) {
			t.Errorf("expected ErrNotFound for orphan transaction, got %v", err)
		}

		// Event must be recorded in subscription_events for audit & reconciliation
		event, err := repo.GetEvent(ctx, subscription.ProviderApple, "notif-orphan-event")
		if err != nil {
			t.Fatalf("expected event to be recorded: %v", err)
		}
		if event.EventID != "notif-orphan-event" {
			t.Errorf("got eventID %s, want notif-orphan-event", event.EventID)
		}
	})
}

func TestHandleAppleNotification_Idempotency(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"
	repo := newFakeRepo()
	userID := uuid.New()
	origTransID := "apple_idempotent_trans"

	seedSub := &subscription.Subscription{
		ID:                    uuid.New(),
		UserID:                userID,
		Provider:              subscription.ProviderApple,
		ProductID:             appsub.ProductProMonthly,
		OriginalTransactionID: &origTransID,
		Status:                subscription.StatusActive,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	_ = repo.Create(ctx, seedSub)

	notificationUUID := "notif-idempotency-test-123"
	verifier := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeDidRenew,
				NotificationUUID: notificationUUID,
				Data: apple.NotificationData{
					BundleID:              bundleID,
					SignedTransactionInfo: "signed_tx_info",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_idempotent",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
				Environment:           "Production",
			}, nil
		},
	}

	svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentProduction, testLogger())

	// First request: processes and updates subscription
	if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("first notification: %v", err)
	}

	firstUpdated, _ := repo.FindByID(ctx, seedSub.ID)
	firstUpdatedAt := firstUpdated.UpdatedAt

	// Advance clock slightly
	time.Sleep(5 * time.Millisecond)

	// Second request: duplicate notificationUUID must NOT mutate subscription state
	if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("second notification: %v", err)
	}

	secondUpdated, _ := repo.FindByID(ctx, seedSub.ID)
	if !secondUpdated.UpdatedAt.Equal(firstUpdatedAt) {
		t.Errorf("second duplicate notification mutated UpdatedAt: first %v, second %v", firstUpdatedAt, secondUpdated.UpdatedAt)
	}
}

func TestHandleAppleNotification_OutOfOrderEvents(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"
	repo := newFakeRepo()
	userID := uuid.New()
	origTransID := "apple_out_of_order_1001"

	futureExpiry := time.Now().Add(60 * 24 * time.Hour).Truncate(time.Microsecond)
	seedSub := &subscription.Subscription{
		ID:                    uuid.New(),
		UserID:                userID,
		Provider:              subscription.ProviderApple,
		ProductID:             appsub.ProductProMonthly,
		OriginalTransactionID: &origTransID,
		Status:                subscription.StatusActive,
		ExpiresAt:             &futureExpiry,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	_ = repo.Create(ctx, seedSub)

	// Older notification with past expiration arrives out of order
	pastExpiry := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	verifier := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeExpired,
				NotificationUUID: "notif-delayed-older-expired",
				Data: apple.NotificationData{
					BundleID:              bundleID,
					SignedTransactionInfo: "signed_tx_info",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_delayed_old",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				ExpiresDate:           pastExpiry,
				Environment:           "Production",
			}, nil
		},
	}

	svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentProduction, testLogger())
	if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Active status and newer future expiration must be preserved!
	current, _ := repo.FindByID(ctx, seedSub.ID)
	if current.Status != subscription.StatusActive {
		t.Errorf("status was overwritten by older event: got %s, want active", current.Status)
	}
	if !current.ExpiresAt.Equal(futureExpiry) {
		t.Errorf("ExpiresAt was overwritten by older event: got %v, want %v", current.ExpiresAt, futureExpiry)
	}
}

func TestHandleAppleNotification_TerminalRevokedState(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"
	repo := newFakeRepo()
	userID := uuid.New()
	origTransID := "apple_revoked_sub"

	seedSub := &subscription.Subscription{
		ID:                    uuid.New(),
		UserID:                userID,
		Provider:              subscription.ProviderApple,
		ProductID:             appsub.ProductProMonthly,
		OriginalTransactionID: &origTransID,
		Status:                subscription.StatusRevoked, // Terminal
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	_ = repo.Create(ctx, seedSub)

	// Notification trying to reactivate revoked subscription
	verifier := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeDidRenew,
				NotificationUUID: "notif-try-reactivate-revoked",
				Data: apple.NotificationData{
					BundleID:              bundleID,
					SignedTransactionInfo: "signed_tx_info",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_renew_attempt",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
			}, nil
		},
	}

	svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentSandbox, testLogger())
	if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	current, _ := repo.FindByID(ctx, seedSub.ID)
	if current.Status != subscription.StatusRevoked {
		t.Errorf("terminal revoked state was mutated to %s", current.Status)
	}
}

// ENV-01: Fail closed on Sandbox vs Production environment mismatch.
func TestHandleAppleNotification_EnvironmentValidation(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"

	testCases := []struct {
		name         string
		expectedEnv  subscription.Environment
		incomingEnv  string
		shouldMutate bool
	}{
		{
			name:         "expected Prod, incoming Prod -> processed",
			expectedEnv:  subscription.EnvironmentProduction,
			incomingEnv:  "Production",
			shouldMutate: true,
		},
		{
			name:         "expected Prod, incoming Sandbox -> dropped safely without mutation",
			expectedEnv:  subscription.EnvironmentProduction,
			incomingEnv:  "Sandbox",
			shouldMutate: false,
		},
		{
			name:         "expected Sandbox, incoming Sandbox -> processed",
			expectedEnv:  subscription.EnvironmentSandbox,
			incomingEnv:  "Sandbox",
			shouldMutate: true,
		},
		{
			name:         "expected Sandbox, incoming Prod -> dropped safely without mutation",
			expectedEnv:  subscription.EnvironmentSandbox,
			incomingEnv:  "Production",
			shouldMutate: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			userID := uuid.New()
			origTransID := "apple_trans_env_test"

			seedSub := &subscription.Subscription{
				ID:                    uuid.New(),
				UserID:                userID,
				Provider:              subscription.ProviderApple,
				ProductID:             appsub.ProductProMonthly,
				OriginalTransactionID: &origTransID,
				Status:                subscription.StatusInactive,
				CreatedAt:             time.Now().UTC(),
				UpdatedAt:             time.Now().UTC(),
			}
			_ = repo.Create(ctx, seedSub)

			verifier := &mockVerifier{
				notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
					return &apple.NotificationPayload{
						NotificationType: apple.NotificationTypeSubscribed,
						NotificationUUID: "notif-uuid-env-test",
						Data: apple.NotificationData{
							BundleID:              bundleID,
							Environment:           tc.incomingEnv,
							SignedTransactionInfo: "signed_tx_info",
						},
					}, nil
				},
				transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
					return &apple.TransactionInfo{
						OriginalTransactionID: origTransID,
						TransactionID:         "tx_env_1",
						BundleID:              bundleID,
						ProductID:             appsub.ProductProMonthly,
						AppAccountToken:       userID.String(),
						Environment:           tc.incomingEnv,
						ExpiresDate:           time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
					}, nil
				},
			}

			svc := appsub.NewService(repo, verifier, bundleID, tc.expectedEnv, testLogger())
			err := svc.HandleAppleNotification(ctx, "payload")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			current, _ := repo.FindByID(ctx, seedSub.ID)
			if tc.shouldMutate {
				if current.Status != subscription.StatusActive {
					t.Errorf("expected status active, got %s", current.Status)
				}
			} else {
				if current.Status != subscription.StatusInactive {
					t.Errorf("expected status to remain inactive on environment mismatch, got %s", current.Status)
				}
			}
		})
	}
}

// ORD-01: Prevent stale out-of-order events from overwriting newer state based on signedDate.
func TestHandleAppleNotification_SignedDateOutOfOrder(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"
	repo := newFakeRepo()
	userID := uuid.New()
	origTransID := "apple_ord_test_trans"

	t1 := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 10, 10, 5, 0, 0, time.UTC)
	t3 := time.Date(2026, 9, 10, 10, 10, 0, 0, time.UTC)

	autoRenewTrue := true
	seedSub := &subscription.Subscription{
		ID:                    uuid.New(),
		UserID:                userID,
		Provider:              subscription.ProviderApple,
		ProductID:             appsub.ProductProMonthly,
		OriginalTransactionID: &origTransID,
		Status:                subscription.StatusActive,
		AutoRenew:             &autoRenewTrue,
		LastEventAt:           &t2, // Sub is currently at T2 (active, auto-renew true)
		CreatedAt:             t2,
		UpdatedAt:             t2,
	}
	_ = repo.Create(ctx, seedSub)

	// 1. Stale event from T1 (auto-renew disabled) arrives out-of-order
	verifierT1 := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeDidChangeRenewalStatus,
				Subtype:          apple.SubtypeAutoRenewDisabled,
				NotificationUUID: "notif-stale-t1",
				SignedDate:       t1.UnixMilli(),
				Data: apple.NotificationData{
					BundleID:              bundleID,
					Environment:           "Production",
					SignedTransactionInfo: "signed_tx_info",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_ord_1",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				AppAccountToken:       userID.String(),
				SignedDate:            t1.UnixMilli(),
				Environment:           "Production",
			}, nil
		},
		renewalFn: func(signedRenewalInfo string) (*apple.RenewalInfo, error) {
			return &apple.RenewalInfo{
				OriginalTransactionID: origTransID,
				AutoRenewStatus:       0,
				SignedDate:            t1.UnixMilli(),
			}, nil
		},
	}

	svc := appsub.NewService(repo, verifierT1, bundleID, subscription.EnvironmentProduction, testLogger())
	if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sub must NOT be modified by stale T1 event
	afterT1, _ := repo.FindByID(ctx, seedSub.ID)
	if afterT1.Status != subscription.StatusActive {
		t.Errorf("status was corrupted by older event: got %s, want active", afterT1.Status)
	}
	if afterT1.AutoRenew == nil || !*afterT1.AutoRenew {
		t.Errorf("autoRenew was corrupted by older event: got %v, want true", afterT1.AutoRenew)
	}
	if !afterT1.LastEventAt.Equal(t2) {
		t.Errorf("LastEventAt was corrupted by older event: got %v, want %v", afterT1.LastEventAt, t2)
	}

	// 2. Newer event from T3 (auto-renew disabled) arrives in-order
	verifierT3 := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeDidChangeRenewalStatus,
				Subtype:          apple.SubtypeAutoRenewDisabled,
				NotificationUUID: "notif-fresh-t3",
				SignedDate:       t3.UnixMilli(),
				Data: apple.NotificationData{
					BundleID:              bundleID,
					Environment:           "Production",
					SignedTransactionInfo: "signed_tx_info",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_ord_3",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				AppAccountToken:       userID.String(),
				SignedDate:            t3.UnixMilli(),
				Environment:           "Production",
			}, nil
		},
		renewalFn: func(signedRenewalInfo string) (*apple.RenewalInfo, error) {
			return &apple.RenewalInfo{
				OriginalTransactionID: origTransID,
				AutoRenewStatus:       0,
				SignedDate:            t3.UnixMilli(),
			}, nil
		},
	}

	svcT3 := appsub.NewService(repo, verifierT3, bundleID, subscription.EnvironmentProduction, testLogger())
	if err := svcT3.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sub must be updated by newer T3 event
	afterT3, _ := repo.FindByID(ctx, seedSub.ID)
	if afterT3.Status != subscription.StatusCancelled {
		t.Errorf("status was not updated by newer event: got %s, want cancelled", afterT3.Status)
	}
	if afterT3.AutoRenew == nil || *afterT3.AutoRenew != false {
		t.Errorf("autoRenew was not updated by newer event: got %v, want false", afterT3.AutoRenew)
	}
	if !afterT3.LastEventAt.Equal(t3) {
		t.Errorf("LastEventAt was not updated: got %v, want %v", afterT3.LastEventAt, t3)
	}
}

// SEM-01: Fail closed on unknown DID_CHANGE_RENEWAL_STATUS subtype.
func TestHandleAppleNotification_UnknownRenewalSubtypeFailClosed(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"
	repo := newFakeRepo()
	userID := uuid.New()
	origTransID := "apple_sem_trans"

	autoRenewFalse := false
	seedSub := &subscription.Subscription{
		ID:                    uuid.New(),
		UserID:                userID,
		Provider:              subscription.ProviderApple,
		ProductID:             appsub.ProductProMonthly,
		OriginalTransactionID: &origTransID,
		Status:                subscription.StatusCancelled,
		AutoRenew:             &autoRenewFalse,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	_ = repo.Create(ctx, seedSub)

	verifier := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeDidChangeRenewalStatus,
				Subtype:          "FUTURE_UNRECOGNIZED_SUBTYPE",
				NotificationUUID: "notif-unknown-subtype",
				Data: apple.NotificationData{
					BundleID:              bundleID,
					Environment:           "Production",
					SignedTransactionInfo: "signed_tx_info",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_sem_1",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				AppAccountToken:       userID.String(),
				Environment:           "Production",
			}, nil
		},
	}

	svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentProduction, testLogger())
	if err := svc.HandleAppleNotification(ctx, "payload"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must fail closed: status must NOT be reactivated to active
	after, _ := repo.FindByID(ctx, seedSub.ID)
	if after.Status != subscription.StatusCancelled {
		t.Errorf("expected status to remain cancelled on unknown renewal subtype, got %s", after.Status)
	}
}

// VER-01: Return ErrInvalidWebhookPayload if SignedRenewalInfo is present but fails verification.
func TestHandleAppleNotification_CorruptedSignedRenewalInfo(t *testing.T) {
	ctx := context.Background()
	bundleID := "com.beebase.production"
	repo := newFakeRepo()
	origTransID := "apple_ver_trans"

	verifier := &mockVerifier{
		notificationFn: func(signedPayload string) (*apple.NotificationPayload, error) {
			return &apple.NotificationPayload{
				NotificationType: apple.NotificationTypeDidRenew,
				NotificationUUID: "notif-corrupted-renewal",
				Data: apple.NotificationData{
					BundleID:              bundleID,
					Environment:           "Production",
					SignedTransactionInfo: "valid_tx_info",
					SignedRenewalInfo:     "corrupted_renewal_info_jws",
				},
			}, nil
		},
		transactionFn: func(signedTransactionInfo string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTransID,
				TransactionID:         "tx_ver_1",
				BundleID:              bundleID,
				ProductID:             appsub.ProductProMonthly,
				Environment:           "Production",
			}, nil
		},
		renewalFn: func(signedRenewalInfo string) (*apple.RenewalInfo, error) {
			return nil, errors.New("cryptographic signature verification failed on renewal info")
		},
	}

	svc := appsub.NewService(repo, verifier, bundleID, subscription.EnvironmentProduction, testLogger())
	err := svc.HandleAppleNotification(ctx, "payload")
	if err == nil {
		t.Fatalf("expected error for corrupted signedRenewalInfo, got nil")
	}
	if !errors.Is(err, appsub.ErrInvalidWebhookPayload) {
		t.Errorf("expected ErrInvalidWebhookPayload, got %v", err)
	}
}
