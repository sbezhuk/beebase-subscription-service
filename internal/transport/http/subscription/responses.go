package subscription

import (
	"time"

	"github.com/google/uuid"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
)

// SubscriptionResponse is the public representation returned by all three
// subscription API endpoints.
type SubscriptionResponse struct {
	Entitlement  string              `json:"entitlement"`
	Subscription *SubscriptionDetail `json:"subscription,omitempty"`
}

// SubscriptionDetail carries the raw subscription fields for UI display.
type SubscriptionDetail struct {
	ID                    uuid.UUID  `json:"id"`
	UserID                uuid.UUID  `json:"user_id"`
	Provider              string     `json:"provider"`
	ProductID             string     `json:"product_id"`
	Status                string     `json:"status"`
	OriginalTransactionID *string    `json:"original_transaction_id,omitempty"`
	TransactionID         *string    `json:"transaction_id,omitempty"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	AutoRenew             *bool      `json:"auto_renew,omitempty"`
	CancelledAt           *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

func newSubscriptionResponse(result *appsub.VerificationResult) SubscriptionResponse {
	if result.Subscription == nil {
		return SubscriptionResponse{Entitlement: result.Entitlement}
	}
	return SubscriptionResponse{
		Entitlement:  result.Entitlement,
		Subscription: newSubscriptionDetail(result.Subscription),
	}
}

func newSubscriptionDetail(s *subscription.Subscription) *SubscriptionDetail {
	return &SubscriptionDetail{
		ID:                    s.ID,
		UserID:                s.UserID,
		Provider:              string(s.Provider),
		ProductID:             s.ProductID,
		Status:                string(s.Status),
		OriginalTransactionID: s.OriginalTransactionID,
		TransactionID:         s.TransactionID,
		ExpiresAt:             s.ExpiresAt,
		AutoRenew:             s.AutoRenew,
		CancelledAt:           s.CancelledAt,
		CreatedAt:             s.CreatedAt,
		UpdatedAt:             s.UpdatedAt,
	}
}
