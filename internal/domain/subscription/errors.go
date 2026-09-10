package subscription

import "errors"

// Domain errors for subscription management and idempotency.
var (
	ErrNotFound               = errors.New("subscription not found")
	ErrEventAlreadyProcessed  = errors.New("subscription event already processed")
	ErrInvalidStateTransition = errors.New("invalid subscription status transition")
	ErrInvalidProvider        = errors.New("invalid subscription provider")
	ErrInvalidStatus          = errors.New("invalid subscription status")
	ErrInvalidEnvironment     = errors.New("invalid subscription environment")
)
