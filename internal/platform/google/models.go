// Package google provides Google Play Developer API client integration,
// subscription retrieval, and normalized subscription models.
package google

import "time"

// SubscriptionState represents the normalized lifecycle state of a Google Play subscription.
type SubscriptionState string

const (
	SubscriptionStateUnspecified   SubscriptionState = "UNSPECIFIED"
	SubscriptionStatePending       SubscriptionState = "PENDING"
	SubscriptionStateActive        SubscriptionState = "ACTIVE"
	SubscriptionStatePaused        SubscriptionState = "PAUSED"
	SubscriptionStateInGracePeriod SubscriptionState = "IN_GRACE_PERIOD"
	SubscriptionStateOnHold        SubscriptionState = "ON_HOLD"
	SubscriptionStateCanceled      SubscriptionState = "CANCELED"
	SubscriptionStateExpired       SubscriptionState = "EXPIRED"
)

// CancellationReason indicates what party or cause initiated subscription cancellation.
type CancellationReason string

const (
	CancellationReasonUserInitiated      CancellationReason = "USER_INITIATED"
	CancellationReasonSystemInitiated    CancellationReason = "SYSTEM_INITIATED"
	CancellationReasonDeveloperInitiated CancellationReason = "DEVELOPER_INITIATED"
	CancellationReasonReplacement        CancellationReason = "REPLACEMENT"
	CancellationReasonUnknown            CancellationReason = "UNKNOWN"
)

// Subscription is the internal normalized Google subscription model containing
// only the fields needed by the application service for domain mapping and persistence.
// It isolates the domain and application layers from Google Play Developer API SDK types.
type Subscription struct {
	PackageName          string             `json:"packageName"`
	ProductID            string             `json:"productId"`
	BasePlanID           string             `json:"basePlanId"`
	PurchaseToken        string             `json:"purchaseToken"`
	State                SubscriptionState  `json:"state"`
	ExpiryTime           time.Time          `json:"expiryTime"`
	StartTime            *time.Time         `json:"startTime,omitempty"`
	AutoRenewing         bool               `json:"autoRenewing"`
	LatestOrderID        string             `json:"latestOrderId"`
	CancelledAt          *time.Time         `json:"cancelledAt,omitempty"`
	CancellationReason   CancellationReason `json:"cancellationReason"`
	AcknowledgementState string             `json:"acknowledgementState"`
	TestPurchase         bool               `json:"testPurchase"`
}

// FullProductID returns "beebase_pro_monthly" if ProductID is "beebase_pro" and BasePlanID is "monthly",
// or ProductID if BasePlanID is empty.
func (s *Subscription) FullProductID() string {
	if s.BasePlanID != "" {
		return s.ProductID + "_" + s.BasePlanID
	}
	return s.ProductID
}
