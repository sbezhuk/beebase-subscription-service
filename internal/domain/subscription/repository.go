package subscription

import (
	"context"

	"github.com/google/uuid"
)

// Repository is the domain port for persisting and retrieving subscriptions and webhook events.
type Repository interface {
	// FindByID retrieves a subscription by internal UUID. Returns ErrNotFound if missing.
	FindByID(ctx context.Context, id uuid.UUID) (*Subscription, error)

	// FindByUserID retrieves the most recently updated subscription for a user.
	// Returns ErrNotFound if missing.
	FindByUserID(ctx context.Context, userID uuid.UUID) (*Subscription, error)

	// FindByProviderAndTransaction looks up a subscription by provider and originalTransactionID.
	// Used primarily by Apple App Store server notifications. Returns ErrNotFound if missing.
	FindByProviderAndTransaction(ctx context.Context, provider Provider, originalTransactionID string) (*Subscription, error)

	// FindByProviderAndPurchaseToken looks up a subscription by provider and purchaseToken.
	// Used primarily by Google Play RTDN notifications. Returns ErrNotFound if missing.
	FindByProviderAndPurchaseToken(ctx context.Context, provider Provider, purchaseToken string) (*Subscription, error)

	// Create persists a new subscription record.
	Create(ctx context.Context, s *Subscription) error

	// Update updates an existing subscription record by its ID.
	Update(ctx context.Context, s *Subscription) error

	// Upsert inserts a subscription or updates its fields on ID conflict.
	Upsert(ctx context.Context, s *Subscription) error

	// RecordEventIfNotExists atomically inserts an event into subscription_events.
	// Returns (inserted = true, nil) if the event was newly inserted.
	// Returns (inserted = false, nil) if an event with the same (provider, event_id) was already recorded.
	RecordEventIfNotExists(ctx context.Context, event *Event) (inserted bool, err error)

	// GetEvent retrieves a previously recorded event by provider and eventID.
	// Returns ErrNotFound if missing.
	GetEvent(ctx context.Context, provider Provider, eventID string) (*Event, error)

	// WithTx runs fn within a database transaction. If fn returns an error, the transaction
	// is rolled back; otherwise it is committed.
	WithTx(ctx context.Context, fn func(txRepo Repository) error) error
}
