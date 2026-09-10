package subscription

import (
	"fmt"
	"time"
)

// validTransitions defines all permitted (from -> to) status transitions.
//
// Rules encoded:
// - active -> active, cancelled, grace_period, billing_retry, expired, revoked
// - grace_period -> active, billing_retry, expired, revoked
// - billing_retry -> active, expired, revoked
// - cancelled -> active (renewal/reactivation), expired, revoked
// - expired -> active (new valid renewal/purchase confirmed), revoked
// - revoked: terminal state (no transitions allowed; new subscription requires new record)
// - inactive -> any valid state (initial creation before store confirmation)
var validTransitions = map[Status]map[Status]bool{
	StatusInactive: {
		StatusActive:       true,
		StatusGracePeriod:  true,
		StatusBillingRetry: true,
		StatusCancelled:    true,
		StatusExpired:      true,
		StatusRevoked:      true,
	},
	StatusActive: {
		StatusActive:       true, // renewal or plan change
		StatusCancelled:    true, // auto-renew turned off
		StatusGracePeriod:  true, // billing failure, store in grace period
		StatusBillingRetry: true, // billing failure without grace period
		StatusExpired:      true, // period ended without renewal
		StatusRevoked:      true, // refunded / revoked by store
	},
	StatusGracePeriod: {
		StatusActive:       true, // payment recovered
		StatusBillingRetry: true, // grace period ended without recovery, moved to account hold/retry
		StatusExpired:      true, // grace period ended without payment recovery
		StatusRevoked:      true, // revoked / refunded
	},
	StatusBillingRetry: {
		StatusActive:  true, // payment recovered
		StatusExpired: true, // retry window expired without recovery
		StatusRevoked: true, // revoked / refunded
	},
	StatusCancelled: {
		StatusActive:  true, // resubscribed / turned auto-renew back on
		StatusExpired: true, // billing period ended without renewal
		StatusRevoked: true, // revoked / refunded
	},
	StatusExpired: {
		StatusActive:  true, // renewed or re-purchased
		StatusRevoked: true, // refunded / revoked
	},
	StatusRevoked: {
		// Terminal: revoked access cannot be re-activated in-place.
	},
}

// CanTransition reports whether transitioning from current status to target status is valid.
func CanTransition(from, to Status) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	return allowed[to]
}

// TransitionTo updates the subscription's status to newStatus if the transition is permitted.
// Returns ErrInvalidStateTransition if the transition is disallowed.
func (s *Subscription) TransitionTo(newStatus Status) error {
	if !newStatus.Valid() {
		return fmt.Errorf("%w: %s", ErrInvalidStatus, newStatus)
	}

	if !CanTransition(s.Status, newStatus) {
		return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidStateTransition, s.Status, newStatus)
	}

	s.Status = newStatus
	s.UpdatedAt = time.Now().UTC()
	return nil
}
