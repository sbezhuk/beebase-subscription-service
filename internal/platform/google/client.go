package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/androidpublisher/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

var (
	ErrSubscriptionNotFound = errors.New("google subscription not found")
	ErrInvalidArguments     = errors.New("invalid google play arguments")
	ErrGoogleAPI            = errors.New("google play api error")
	ErrMalformedResponse    = errors.New("malformed google play api response")
)

// Client defines the interface for communicating with the Google Play Developer API.
// It isolates the domain and application layers from Google SDK types.
type Client interface {
	GetSubscription(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*Subscription, error)
}

// Config holds configuration parameters for the Google Play client.
type Config struct {
	ServiceAccountJSON string // File path or raw JSON string
	PackageName        string // Default Android package name (e.g. "com.beebase.production")
}

// DefaultClient implements Client against Google Play Developer API v3.
type DefaultClient struct {
	service     *androidpublisher.Service
	packageName string
}

// NewClient constructs a Google Play Client authenticated with the provided service account.
// Additional option.ClientOption values can be passed to override transport (e.g. for testing).
func NewClient(ctx context.Context, cfg Config, opts ...option.ClientOption) (*DefaultClient, error) {
	var clientOpts []option.ClientOption

	if cfg.ServiceAccountJSON != "" {
		trimmed := strings.TrimSpace(cfg.ServiceAccountJSON)
		if strings.HasPrefix(trimmed, "{") {
			clientOpts = append(clientOpts, option.WithCredentialsJSON([]byte(trimmed)))
		} else {
			clientOpts = append(clientOpts, option.WithCredentialsFile(trimmed))
		}
	} else if len(opts) == 0 {
		clientOpts = append(clientOpts, option.WithoutAuthentication())
	}

	clientOpts = append(clientOpts, opts...)
	clientOpts = append(clientOpts, option.WithScopes(androidpublisher.AndroidpublisherScope))

	srv, err := androidpublisher.NewService(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create androidpublisher service: %w", err)
	}

	return &DefaultClient{
		service:     srv,
		packageName: cfg.PackageName,
	}, nil
}

// GetSubscription retrieves and normalizes a Google Play subscription purchase
// via Google Play Developer API v3 (Subscriptions V2 endpoint).
func (c *DefaultClient) GetSubscription(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*Subscription, error) {
	if packageName == "" {
		packageName = c.packageName
	}
	if packageName == "" {
		return nil, fmt.Errorf("%w: missing package name", ErrInvalidArguments)
	}
	if purchaseToken == "" {
		return nil, fmt.Errorf("%w: missing purchase token", ErrInvalidArguments)
	}

	subV2, err := c.service.Purchases.Subscriptionsv2.Get(packageName, purchaseToken).Context(ctx).Do()
	if err != nil {
		var gErr *googleapi.Error
		if errors.As(err, &gErr) {
			if gErr.Code == http.StatusNotFound {
				return nil, fmt.Errorf("%w: package=%s token=%s: %v", ErrSubscriptionNotFound, packageName, purchaseToken, err)
			}
			return nil, fmt.Errorf("%w: status %d: %s", ErrGoogleAPI, gErr.Code, gErr.Message)
		}
		return nil, fmt.Errorf("%w: %v", ErrGoogleAPI, err)
	}

	return normalizeSubscriptionV2(packageName, purchaseToken, subscriptionID, subV2)
}

func normalizeSubscriptionV2(packageName, purchaseToken, filterSubID string, sub *androidpublisher.SubscriptionPurchaseV2) (*Subscription, error) {
	if sub == nil {
		return nil, fmt.Errorf("%w: empty subscription response", ErrMalformedResponse)
	}
	if len(sub.LineItems) == 0 {
		return nil, fmt.Errorf("%w: subscription contains no line items", ErrMalformedResponse)
	}

	// Find the relevant line item (matching filterSubID if provided)
	item := sub.LineItems[0]
	if filterSubID != "" {
		matched := false
		for _, li := range sub.LineItems {
			if li.ProductId == filterSubID {
				item = li
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("%w: product %q not found in line items", ErrSubscriptionNotFound, filterSubID)
		}
	}

	productID := item.ProductId
	basePlanID := ""
	if item.OfferDetails != nil {
		basePlanID = item.OfferDetails.BasePlanId
	}

	if item.ExpiryTime == "" {
		return nil, fmt.Errorf("%w: missing expiry_time in line item", ErrMalformedResponse)
	}

	expiryTime, err := time.Parse(time.RFC3339, item.ExpiryTime)
	if err != nil {
		return nil, fmt.Errorf("%w: parse expiry_time %q: %v", ErrMalformedResponse, item.ExpiryTime, err)
	}

	autoRenewing := false
	if item.AutoRenewingPlan != nil {
		autoRenewing = item.AutoRenewingPlan.AutoRenewEnabled
	}

	var startTime *time.Time
	if sub.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, sub.StartTime); err == nil {
			utc := t.UTC()
			startTime = &utc
		}
	}

	var (
		cancelledAt  *time.Time
		cancelReason CancellationReason = CancellationReasonUnknown
	)

	if sub.CanceledStateContext != nil {
		if sub.CanceledStateContext.UserInitiatedCancellation != nil {
			cancelReason = CancellationReasonUserInitiated
			if sub.CanceledStateContext.UserInitiatedCancellation.CancelTime != "" {
				if t, err := time.Parse(time.RFC3339, sub.CanceledStateContext.UserInitiatedCancellation.CancelTime); err == nil {
					utc := t.UTC()
					cancelledAt = &utc
				}
			}
		} else if sub.CanceledStateContext.SystemInitiatedCancellation != nil {
			cancelReason = CancellationReasonSystemInitiated
		} else if sub.CanceledStateContext.DeveloperInitiatedCancellation != nil {
			cancelReason = CancellationReasonDeveloperInitiated
		} else if sub.CanceledStateContext.ReplacementCancellation != nil {
			cancelReason = CancellationReasonReplacement
		}
	}

	return &Subscription{
		PackageName:          packageName,
		ProductID:            productID,
		BasePlanID:           basePlanID,
		PurchaseToken:        purchaseToken,
		State:                mapV2State(sub.SubscriptionState),
		ExpiryTime:           expiryTime.UTC(),
		StartTime:            startTime,
		AutoRenewing:         autoRenewing,
		LatestOrderID:        item.LatestSuccessfulOrderId,
		CancelledAt:          cancelledAt,
		CancellationReason:   cancelReason,
		AcknowledgementState: sub.AcknowledgementState,
		TestPurchase:         sub.TestPurchase != nil,
	}, nil
}

func mapV2State(raw string) SubscriptionState {
	switch raw {
	case "SUBSCRIPTION_STATE_ACTIVE":
		return SubscriptionStateActive
	case "SUBSCRIPTION_STATE_IN_GRACE_PERIOD":
		return SubscriptionStateInGracePeriod
	case "SUBSCRIPTION_STATE_ON_HOLD":
		return SubscriptionStateOnHold
	case "SUBSCRIPTION_STATE_CANCELED":
		return SubscriptionStateCanceled
	case "SUBSCRIPTION_STATE_EXPIRED":
		return SubscriptionStateExpired
	case "SUBSCRIPTION_STATE_PAUSED":
		return SubscriptionStatePaused
	case "SUBSCRIPTION_STATE_PENDING":
		return SubscriptionStatePending
	default:
		return SubscriptionStateUnspecified
	}
}
