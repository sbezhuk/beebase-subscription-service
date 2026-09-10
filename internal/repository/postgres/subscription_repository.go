package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
)

// SubscriptionRepository implements domain/subscription.Repository against PostgreSQL.
type SubscriptionRepository struct {
	db Querier
}

// NewSubscriptionRepository returns a SubscriptionRepository backed by db.
func NewSubscriptionRepository(db Querier) *SubscriptionRepository {
	return &SubscriptionRepository{db: db}
}

// WithTx runs fn within a database transaction. If fn returns an error, the transaction
// is rolled back; otherwise it is committed.
func (r *SubscriptionRepository) WithTx(ctx context.Context, fn func(txRepo subscription.Repository) error) error {
	pool, ok := r.db.(*pgxpool.Pool)
	if !ok {
		// Already inside a transaction (e.g. pgx.Tx) or a mock querier
		return fn(r)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	txRepo := NewSubscriptionRepository(tx)
	if err := fn(txRepo); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit tx: %w", err)
	}

	return nil
}

func scanSubscription(row pgx.Row) (*subscription.Subscription, error) {
	var (
		s           subscription.Subscription
		providerStr string
		statusStr   string
		envStr      *string
	)

	err := row.Scan(
		&s.ID,
		&s.UserID,
		&providerStr,
		&s.ProductID,
		&s.OriginalTransactionID,
		&s.TransactionID,
		&s.PurchaseToken,
		&statusStr,
		&envStr,
		&s.ExpiresAt,
		&s.AutoRenew,
		&s.CancelledAt,
		&s.LastEventAt,
		&s.CreatedAt,
		&s.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, subscription.ErrNotFound
		}
		return nil, err
	}

	s.Provider = subscription.Provider(providerStr)
	s.Status = subscription.Status(statusStr)
	if envStr != nil {
		env := subscription.Environment(*envStr)
		s.Environment = &env
	}

	return &s, nil
}

// FindByID retrieves a subscription by internal UUID.
func (r *SubscriptionRepository) FindByID(ctx context.Context, id uuid.UUID) (*subscription.Subscription, error) {
	const q = `
		SELECT id, user_id, provider, product_id, original_transaction_id, transaction_id,
		       purchase_token, status, environment, expires_at, auto_renew, cancelled_at,
		       last_event_at, created_at, updated_at
		FROM subscriptions
		WHERE id = $1
	`
	sub, err := scanSubscription(r.db.QueryRow(ctx, q, id))
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("postgres: find subscription by id: %w", err)
	}
	return sub, nil
}

// FindByUserID retrieves the most recently updated subscription for a user.
func (r *SubscriptionRepository) FindByUserID(ctx context.Context, userID uuid.UUID) (*subscription.Subscription, error) {
	const q = `
		SELECT id, user_id, provider, product_id, original_transaction_id, transaction_id,
		       purchase_token, status, environment, expires_at, auto_renew, cancelled_at,
		       last_event_at, created_at, updated_at
		FROM subscriptions
		WHERE user_id = $1
		ORDER BY updated_at DESC
		LIMIT 1
	`
	sub, err := scanSubscription(r.db.QueryRow(ctx, q, userID))
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("postgres: find subscription by user id: %w", err)
	}
	return sub, nil
}

// FindByProviderAndTransaction looks up a subscription by provider and originalTransactionID.
func (r *SubscriptionRepository) FindByProviderAndTransaction(ctx context.Context, provider subscription.Provider, originalTransactionID string) (*subscription.Subscription, error) {
	const q = `
		SELECT id, user_id, provider, product_id, original_transaction_id, transaction_id,
		       purchase_token, status, environment, expires_at, auto_renew, cancelled_at,
		       last_event_at, created_at, updated_at
		FROM subscriptions
		WHERE provider = $1 AND original_transaction_id = $2
	`
	sub, err := scanSubscription(r.db.QueryRow(ctx, q, string(provider), originalTransactionID))
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("postgres: find subscription by provider and transaction: %w", err)
	}
	return sub, nil
}

// FindByProviderAndPurchaseToken looks up a subscription by provider and purchaseToken.
func (r *SubscriptionRepository) FindByProviderAndPurchaseToken(ctx context.Context, provider subscription.Provider, purchaseToken string) (*subscription.Subscription, error) {
	const q = `
		SELECT id, user_id, provider, product_id, original_transaction_id, transaction_id,
		       purchase_token, status, environment, expires_at, auto_renew, cancelled_at,
		       last_event_at, created_at, updated_at
		FROM subscriptions
		WHERE provider = $1 AND purchase_token = $2
	`
	sub, err := scanSubscription(r.db.QueryRow(ctx, q, string(provider), purchaseToken))
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("postgres: find subscription by provider and purchase token: %w", err)
	}
	return sub, nil
}

// Create persists a new subscription record.
func (r *SubscriptionRepository) Create(ctx context.Context, s *subscription.Subscription) error {
	const q = `
		INSERT INTO subscriptions (
			id, user_id, provider, product_id, original_transaction_id, transaction_id,
			purchase_token, status, environment, expires_at, auto_renew, cancelled_at,
			last_event_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)
	`
	var envStr *string
	if s.Environment != nil {
		str := string(*s.Environment)
		envStr = &str
	}

	_, err := r.db.Exec(ctx, q,
		s.ID, s.UserID, string(s.Provider), s.ProductID, s.OriginalTransactionID, s.TransactionID,
		s.PurchaseToken, string(s.Status), envStr, s.ExpiresAt, s.AutoRenew, s.CancelledAt,
		s.LastEventAt, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: create subscription: %w", err)
	}
	return nil
}

// Update updates an existing subscription record by its ID.
func (r *SubscriptionRepository) Update(ctx context.Context, s *subscription.Subscription) error {
	const q = `
		UPDATE subscriptions
		SET product_id = $2,
		    original_transaction_id = $3,
		    transaction_id = $4,
		    purchase_token = $5,
		    status = $6,
		    environment = $7,
		    expires_at = $8,
		    auto_renew = $9,
		    cancelled_at = $10,
		    last_event_at = $11,
		    updated_at = $12
		WHERE id = $1
	`
	var envStr *string
	if s.Environment != nil {
		str := string(*s.Environment)
		envStr = &str
	}

	tag, err := r.db.Exec(ctx, q,
		s.ID, s.ProductID, s.OriginalTransactionID, s.TransactionID,
		s.PurchaseToken, string(s.Status), envStr, s.ExpiresAt, s.AutoRenew, s.CancelledAt,
		s.LastEventAt, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: update subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return subscription.ErrNotFound
	}
	return nil
}

// Upsert inserts a subscription or updates its mutable fields on ID conflict.
func (r *SubscriptionRepository) Upsert(ctx context.Context, s *subscription.Subscription) error {
	const q = `
		INSERT INTO subscriptions (
			id, user_id, provider, product_id, original_transaction_id, transaction_id,
			purchase_token, status, environment, expires_at, auto_renew, cancelled_at,
			last_event_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)
		ON CONFLICT (id) DO UPDATE
		SET product_id = EXCLUDED.product_id,
		    original_transaction_id = EXCLUDED.original_transaction_id,
		    transaction_id = EXCLUDED.transaction_id,
		    purchase_token = EXCLUDED.purchase_token,
		    status = EXCLUDED.status,
		    environment = EXCLUDED.environment,
		    expires_at = EXCLUDED.expires_at,
		    auto_renew = EXCLUDED.auto_renew,
		    cancelled_at = EXCLUDED.cancelled_at,
		    last_event_at = EXCLUDED.last_event_at,
		    updated_at = EXCLUDED.updated_at
	`
	var envStr *string
	if s.Environment != nil {
		str := string(*s.Environment)
		envStr = &str
	}

	_, err := r.db.Exec(ctx, q,
		s.ID, s.UserID, string(s.Provider), s.ProductID, s.OriginalTransactionID, s.TransactionID,
		s.PurchaseToken, string(s.Status), envStr, s.ExpiresAt, s.AutoRenew, s.CancelledAt,
		s.LastEventAt, s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert subscription: %w", err)
	}
	return nil
}

// RecordEventIfNotExists atomically inserts an event into subscription_events.
// Uses ON CONFLICT (provider, event_id) DO NOTHING to achieve DB-level idempotency.
// Returns inserted = true if new, or inserted = false if the event already existed.
func (r *SubscriptionRepository) RecordEventIfNotExists(ctx context.Context, event *subscription.Event) (bool, error) {
	const q = `
		INSERT INTO subscription_events (id, provider, event_id, event_type, payload, processed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (provider, event_id) DO NOTHING
	`
	tag, err := r.db.Exec(ctx, q,
		event.ID, string(event.Provider), event.EventID, event.EventType, event.Payload, event.ProcessedAt,
	)
	if err != nil {
		return false, fmt.Errorf("postgres: record subscription event: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

// GetEvent retrieves a previously recorded event by provider and eventID.
func (r *SubscriptionRepository) GetEvent(ctx context.Context, provider subscription.Provider, eventID string) (*subscription.Event, error) {
	const q = `
		SELECT id, provider, event_id, event_type, payload, processed_at
		FROM subscription_events
		WHERE provider = $1 AND event_id = $2
	`
	var (
		e           subscription.Event
		providerStr string
	)

	err := r.db.QueryRow(ctx, q, string(provider), eventID).Scan(
		&e.ID,
		&providerStr,
		&e.EventID,
		&e.EventType,
		&e.Payload,
		&e.ProcessedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, subscription.ErrNotFound
		}
		return nil, fmt.Errorf("postgres: get subscription event: %w", err)
	}
	e.Provider = subscription.Provider(providerStr)

	return &e, nil
}
