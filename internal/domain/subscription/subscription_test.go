package subscription_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
)

func TestProviderConstants(t *testing.T) {
	tests := []struct {
		provider subscription.Provider
		valid    bool
	}{
		{subscription.ProviderApple, true},
		{subscription.ProviderGoogle, true},
		{subscription.Provider("stripe"), false},
		{subscription.Provider(""), false},
	}

	for _, tt := range tests {
		if got := tt.provider.Valid(); got != tt.valid {
			t.Errorf("Provider(%q).Valid() = %v; want %v", tt.provider, got, tt.valid)
		}
	}
}

func TestStatusConstants(t *testing.T) {
	validStatuses := []subscription.Status{
		subscription.StatusInactive,
		subscription.StatusActive,
		subscription.StatusGracePeriod,
		subscription.StatusBillingRetry,
		subscription.StatusExpired,
		subscription.StatusCancelled,
		subscription.StatusRevoked,
	}

	for _, s := range validStatuses {
		if !s.Valid() {
			t.Errorf("expected Status(%q).Valid() to be true", s)
		}
	}

	invalidStatuses := []subscription.Status{
		"unknown",
		"pending",
		"",
	}

	for _, s := range invalidStatuses {
		if s.Valid() {
			t.Errorf("expected Status(%q).Valid() to be false", s)
		}
	}
}

func TestValidStateTransitions(t *testing.T) {
	validCases := []struct {
		from subscription.Status
		to   subscription.Status
	}{
		// from inactive
		{subscription.StatusInactive, subscription.StatusActive},
		{subscription.StatusInactive, subscription.StatusGracePeriod},
		{subscription.StatusInactive, subscription.StatusBillingRetry},
		{subscription.StatusInactive, subscription.StatusCancelled},
		{subscription.StatusInactive, subscription.StatusExpired},
		{subscription.StatusInactive, subscription.StatusRevoked},

		// from active
		{subscription.StatusActive, subscription.StatusActive},
		{subscription.StatusActive, subscription.StatusCancelled},
		{subscription.StatusActive, subscription.StatusGracePeriod},
		{subscription.StatusActive, subscription.StatusBillingRetry},
		{subscription.StatusActive, subscription.StatusExpired},
		{subscription.StatusActive, subscription.StatusRevoked},

		// from grace_period
		{subscription.StatusGracePeriod, subscription.StatusActive},
		{subscription.StatusGracePeriod, subscription.StatusBillingRetry},
		{subscription.StatusGracePeriod, subscription.StatusExpired},
		{subscription.StatusGracePeriod, subscription.StatusRevoked},

		// from billing_retry
		{subscription.StatusBillingRetry, subscription.StatusActive},
		{subscription.StatusBillingRetry, subscription.StatusExpired},
		{subscription.StatusBillingRetry, subscription.StatusRevoked},

		// from cancelled
		{subscription.StatusCancelled, subscription.StatusActive},
		{subscription.StatusCancelled, subscription.StatusExpired},
		{subscription.StatusCancelled, subscription.StatusRevoked},

		// from expired
		{subscription.StatusExpired, subscription.StatusActive},
		{subscription.StatusExpired, subscription.StatusRevoked},
	}

	for _, tc := range validCases {
		if !subscription.CanTransition(tc.from, tc.to) {
			t.Errorf("expected CanTransition(%s, %s) to be true", tc.from, tc.to)
		}

		sub := &subscription.Subscription{
			Status: tc.from,
		}
		if err := sub.TransitionTo(tc.to); err != nil {
			t.Errorf("unexpected error transitioning %s -> %s: %v", tc.from, tc.to, err)
		}
		if sub.Status != tc.to {
			t.Errorf("expected status %s after transition, got %s", tc.to, sub.Status)
		}
	}
}

func TestInvalidStateTransitions(t *testing.T) {
	invalidCases := []struct {
		from subscription.Status
		to   subscription.Status
	}{
		// from revoked (terminal state)
		{subscription.StatusRevoked, subscription.StatusActive},
		{subscription.StatusRevoked, subscription.StatusGracePeriod},
		{subscription.StatusRevoked, subscription.StatusBillingRetry},
		{subscription.StatusRevoked, subscription.StatusCancelled},
		{subscription.StatusRevoked, subscription.StatusExpired},
		{subscription.StatusRevoked, subscription.StatusRevoked},

		// from expired
		{subscription.StatusExpired, subscription.StatusGracePeriod},
		{subscription.StatusExpired, subscription.StatusBillingRetry},
		{subscription.StatusExpired, subscription.StatusCancelled},

		// from grace_period
		{subscription.StatusGracePeriod, subscription.StatusCancelled},

		// from billing_retry
		{subscription.StatusBillingRetry, subscription.StatusCancelled},
		{subscription.StatusBillingRetry, subscription.StatusGracePeriod},

		// invalid status value
		{subscription.StatusActive, subscription.Status("not_a_status")},
	}

	for _, tc := range invalidCases {
		if subscription.CanTransition(tc.from, tc.to) {
			t.Errorf("expected CanTransition(%s, %s) to be false", tc.from, tc.to)
		}

		sub := &subscription.Subscription{
			Status: tc.from,
		}
		if err := sub.TransitionTo(tc.to); err == nil {
			t.Errorf("expected error transitioning %s -> %s, got nil", tc.from, tc.to)
		}
	}
}

func TestCancelledVsExpiredSemantics(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(7 * 24 * time.Hour)
	past := now.Add(-1 * time.Hour)

	t.Run("cancelled with future expiry retains active access", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusCancelled,
			ExpiresAt: &future,
		}
		if !sub.HasActiveAccess(now) {
			t.Errorf("cancelled subscription with future expiry should have active access")
		}
	})

	t.Run("cancelled with past expiry loses active access", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusCancelled,
			ExpiresAt: &past,
		}
		if sub.HasActiveAccess(now) {
			t.Errorf("cancelled subscription with past expiry should NOT have active access")
		}
	})

	t.Run("cancelled with nil expiry loses active access", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusCancelled,
			ExpiresAt: nil,
		}
		if sub.HasActiveAccess(now) {
			t.Errorf("cancelled subscription with nil expiry should NOT have active access")
		}
	})

	t.Run("expired subscription loses active access even if expires_at is future", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusExpired,
			ExpiresAt: &future,
		}
		if sub.HasActiveAccess(now) {
			t.Errorf("expired subscription should never have active access")
		}
	})

	t.Run("revoked subscription loses active access immediately regardless of expires_at", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusRevoked,
			ExpiresAt: &future,
		}
		if sub.HasActiveAccess(now) {
			t.Errorf("revoked subscription should immediately lose active access")
		}
	})

	t.Run("active subscription with future expiry has active access", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusActive,
			ExpiresAt: &future,
		}
		if !sub.HasActiveAccess(now) {
			t.Errorf("active subscription with future expiry should have active access")
		}
	})

	t.Run("grace period subscription with future expiry has active access", func(t *testing.T) {
		sub := &subscription.Subscription{
			Status:    subscription.StatusGracePeriod,
			ExpiresAt: &future,
		}
		if !sub.HasActiveAccess(now) {
			t.Errorf("grace_period subscription should have active access")
		}
	})
}

func TestNewSubscription(t *testing.T) {
	userID := uuid.New()

	sub, err := subscription.New(userID, subscription.ProviderApple, "beebase_pro_monthly")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sub.ID == uuid.Nil {
		t.Errorf("expected non-nil UUID")
	}
	if sub.UserID != userID {
		t.Errorf("expected userID %s, got %s", userID, sub.UserID)
	}
	if sub.Provider != subscription.ProviderApple {
		t.Errorf("expected provider apple, got %s", sub.Provider)
	}
	if sub.Status != subscription.StatusInactive {
		t.Errorf("expected status inactive, got %s", sub.Status)
	}

	_, err = subscription.New(userID, "invalid_provider", "beebase_pro_monthly")
	if err == nil {
		t.Errorf("expected error for invalid provider, got nil")
	}
}

func TestIsNewerExpiryThan(t *testing.T) {
	t1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	subOld := &subscription.Subscription{ExpiresAt: &t1}
	subNew := &subscription.Subscription{ExpiresAt: &t2}
	subNil := &subscription.Subscription{ExpiresAt: nil}

	if !subNew.IsNewerExpiryThan(subOld) {
		t.Errorf("subNew should be newer than subOld")
	}
	if subOld.IsNewerExpiryThan(subNew) {
		t.Errorf("subOld should not be newer than subNew")
	}
	if !subNew.IsNewerExpiryThan(subNil) {
		t.Errorf("subNew should be newer than subNil")
	}
	if subNil.IsNewerExpiryThan(subNew) {
		t.Errorf("subNil should not be newer than subNew")
	}
}
