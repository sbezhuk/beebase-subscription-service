// Package subscription holds the core subscription domain model, lifecycle
// state machine, and persistence interfaces. It has no dependencies on HTTP,
// SQL/pgx, or external payment provider SDKs.
package subscription

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Provider identifies the app store platform handling billing.
type Provider string

const (
	ProviderApple  Provider = "apple"
	ProviderGoogle Provider = "google"
)

// Valid returns true if the provider is a supported platform.
func (p Provider) Valid() bool {
	return p == ProviderApple || p == ProviderGoogle
}

// Status represents the subscription lifecycle state.
type Status string

const (
	StatusInactive     Status = "inactive"
	StatusActive       Status = "active"
	StatusGracePeriod  Status = "grace_period"
	StatusBillingRetry Status = "billing_retry"
	StatusExpired      Status = "expired"
	StatusCancelled    Status = "cancelled"
	StatusRevoked      Status = "revoked"
)

// Valid returns true if the status is one of the recognized lifecycle states.
func (s Status) Valid() bool {
	switch s {
	case StatusInactive, StatusActive, StatusGracePeriod,
		StatusBillingRetry, StatusExpired, StatusCancelled, StatusRevoked:
		return true
	default:
		return false
	}
}

// Environment represents whether a subscription was purchased in sandbox or production.
type Environment string

const (
	EnvironmentSandbox    Environment = "sandbox"
	EnvironmentProduction Environment = "production"
)

// Valid returns true if the environment is known.
func (e Environment) Valid() bool {
	return e == EnvironmentSandbox || e == EnvironmentProduction
}

// Subscription is the core domain entity representing an account's store subscription.
type Subscription struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	Provider              Provider
	ProductID             string
	OriginalTransactionID *string
	TransactionID         *string
	PurchaseToken         *string
	Status                Status
	Environment           *Environment
	ExpiresAt             *time.Time
	AutoRenew             *bool
	CancelledAt           *time.Time
	LastEventAt           *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// New constructs a Subscription with a new UUID and UTC timestamps.
func New(userID uuid.UUID, provider Provider, productID string) (*Subscription, error) {
	if !provider.Valid() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidProvider, provider)
	}

	now := time.Now().UTC()
	return &Subscription{
		ID:        uuid.New(),
		UserID:    userID,
		Provider:  provider,
		ProductID: productID,
		Status:    StatusInactive,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// HasActiveAccess evaluates whether the subscription grants Pro privileges at reference time now.
//
// Semantic rules:
// - StatusActive / StatusGracePeriod: grants active access (if ExpiresAt is nil or in the future).
// - StatusCancelled: the user cancelled auto-renewal, but access continues until the current period expires (ExpiresAt > now).
// - StatusRevoked: store refunded or revoked access immediately; access is false regardless of ExpiresAt.
// - StatusExpired / StatusBillingRetry / StatusInactive: access is false.
func (s *Subscription) HasActiveAccess(now time.Time) bool {
	switch s.Status {
	case StatusActive, StatusGracePeriod:
		if s.ExpiresAt != nil {
			return s.ExpiresAt.After(now)
		}
		return true
	case StatusCancelled:
		return s.ExpiresAt != nil && s.ExpiresAt.After(now)
	default:
		return false
	}
}

// IsNewerExpiryThan reports whether this subscription's expiration is strictly after other's expiration.
// Useful when reconciling out-of-order webhook events that carry expiration dates.
func (s *Subscription) IsNewerExpiryThan(other *Subscription) bool {
	if s.ExpiresAt == nil {
		return false
	}
	if other == nil || other.ExpiresAt == nil {
		return true
	}
	return s.ExpiresAt.After(*other.ExpiresAt)
}
