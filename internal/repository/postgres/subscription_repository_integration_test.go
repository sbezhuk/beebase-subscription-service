//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	repopostgres "github.com/sbezhuk/beebase-subscription-service/internal/repository/postgres"
)

func TestSubscriptionRepository_CreateAndFind(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	repo := repopostgres.NewSubscriptionRepository(tx)
	userID := uuid.New()
	origTransID := "apple_orig_trans_123"
	transID := "apple_trans_123"
	env := subscription.EnvironmentProduction
	expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)
	autoRenew := true

	lastEventAt := time.Now().UTC().Truncate(time.Microsecond)
	sub := &subscription.Subscription{
		ID:                    uuid.New(),
		UserID:                userID,
		Provider:              subscription.ProviderApple,
		ProductID:             "beebase_pro_monthly",
		OriginalTransactionID: &origTransID,
		TransactionID:         &transID,
		Status:                subscription.StatusActive,
		Environment:           &env,
		ExpiresAt:             &expiresAt,
		AutoRenew:             &autoRenew,
		LastEventAt:           &lastEventAt,
		CreatedAt:             time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:             time.Now().UTC().Truncate(time.Microsecond),
	}

	if err := repo.Create(ctx, sub); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 1. FindByID
	byID, err := repo.FindByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if byID.ID != sub.ID || byID.UserID != userID || byID.Provider != subscription.ProviderApple {
		t.Errorf("FindByID mismatch: got %+v, want %+v", byID, sub)
	}
	if *byID.OriginalTransactionID != origTransID {
		t.Errorf("FindByID original_transaction_id = %v, want %v", *byID.OriginalTransactionID, origTransID)
	}

	// 2. FindByUserID
	byUser, err := repo.FindByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("FindByUserID: %v", err)
	}
	if byUser.ID != sub.ID {
		t.Errorf("FindByUserID ID = %v, want %v", byUser.ID, sub.ID)
	}

	// 3. FindByProviderAndTransaction
	byTrans, err := repo.FindByProviderAndTransaction(ctx, subscription.ProviderApple, origTransID)
	if err != nil {
		t.Fatalf("FindByProviderAndTransaction: %v", err)
	}
	if byTrans.ID != sub.ID {
		t.Errorf("FindByProviderAndTransaction ID = %v, want %v", byTrans.ID, sub.ID)
	}

	// Missing lookups return ErrNotFound
	_, err = repo.FindByID(ctx, uuid.New())
	if !errors.Is(err, subscription.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing ID, got %v", err)
	}

	_, err = repo.FindByProviderAndTransaction(ctx, subscription.ProviderApple, "non_existent")
	if !errors.Is(err, subscription.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing transaction, got %v", err)
	}
}

func TestSubscriptionRepository_GooglePurchaseTokenAndUpsert(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	repo := repopostgres.NewSubscriptionRepository(tx)
	userID := uuid.New()
	token := "google_purchase_token_xyz"
	env := subscription.EnvironmentSandbox
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour).Truncate(time.Microsecond)
	autoRenew := true

	sub := &subscription.Subscription{
		ID:            uuid.New(),
		UserID:        userID,
		Provider:      subscription.ProviderGoogle,
		ProductID:     "beebase_pro_annual",
		PurchaseToken: &token,
		Status:        subscription.StatusActive,
		Environment:   &env,
		ExpiresAt:     &expiresAt,
		AutoRenew:     &autoRenew,
		CreatedAt:     time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:     time.Now().UTC().Truncate(time.Microsecond),
	}

	// Upsert on new record inserts
	if err := repo.Upsert(ctx, sub); err != nil {
		t.Fatalf("Upsert insert: %v", err)
	}

	byToken, err := repo.FindByProviderAndPurchaseToken(ctx, subscription.ProviderGoogle, token)
	if err != nil {
		t.Fatalf("FindByProviderAndPurchaseToken: %v", err)
	}
	if byToken.ID != sub.ID {
		t.Errorf("FindByProviderAndPurchaseToken ID = %v, want %v", byToken.ID, sub.ID)
	}

	// Modify status and update
	sub.Status = subscription.StatusCancelled
	cancelledAt := time.Now().UTC().Truncate(time.Microsecond)
	sub.CancelledAt = &cancelledAt
	sub.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)

	if err := repo.Update(ctx, sub); err != nil {
		t.Fatalf("Update: %v", err)
	}

	updated, err := repo.FindByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("FindByID after Update: %v", err)
	}
	if updated.Status != subscription.StatusCancelled {
		t.Errorf("expected status cancelled, got %s", updated.Status)
	}
	if updated.CancelledAt == nil {
		t.Errorf("expected non-nil CancelledAt")
	}

	// Upsert on existing record updates
	sub.Status = subscription.StatusExpired
	sub.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.Upsert(ctx, sub); err != nil {
		t.Fatalf("Upsert update: %v", err)
	}

	upserted, err := repo.FindByID(ctx, sub.ID)
	if err != nil {
		t.Fatalf("FindByID after Upsert update: %v", err)
	}
	if upserted.Status != subscription.StatusExpired {
		t.Errorf("expected status expired, got %s", upserted.Status)
	}
}

func TestSubscriptionRepository_EventIdempotency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })

	repo := repopostgres.NewSubscriptionRepository(tx)
	eventID := "apple_notification_uuid_456"
	payload := []byte(`{"notificationType":"DID_RENEW","subtype":"INITIAL_BUY"}`)

	event1 := subscription.NewEvent(subscription.ProviderApple, eventID, "DID_RENEW", payload)

	// First insert must succeed and return inserted = true
	inserted, err := repo.RecordEventIfNotExists(ctx, event1)
	if err != nil {
		t.Fatalf("first RecordEventIfNotExists: %v", err)
	}
	if !inserted {
		t.Errorf("expected inserted = true for fresh event, got false")
	}

	// Second insert with same (provider, event_id) must return inserted = false without error
	event2 := subscription.NewEvent(subscription.ProviderApple, eventID, "DID_RENEW", payload)
	inserted2, err := repo.RecordEventIfNotExists(ctx, event2)
	if err != nil {
		t.Fatalf("duplicate RecordEventIfNotExists: %v", err)
	}
	if inserted2 {
		t.Errorf("expected inserted = false for duplicate event, got true")
	}

	// Verify the event was recorded and can be fetched
	got, err := repo.GetEvent(ctx, subscription.ProviderApple, eventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.EventID != eventID || got.EventType != "DID_RENEW" || got.Provider != subscription.ProviderApple {
		t.Errorf("GetEvent = %+v, want eventID %s", got, eventID)
	}

	// Different provider with same event_id is independent
	googleEvent := subscription.NewEvent(subscription.ProviderGoogle, eventID, "SUBSCRIPTION_RENEWED", payload)
	insertedGoogle, err := repo.RecordEventIfNotExists(ctx, googleEvent)
	if err != nil {
		t.Fatalf("Google RecordEventIfNotExists: %v", err)
	}
	if !insertedGoogle {
		t.Errorf("expected inserted = true for same eventID under different provider")
	}
}
