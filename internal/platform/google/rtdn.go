package google

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Standard Google Play RTDN subscription notification types.
// See: https://developer.android.com/google/play/billing/rtdn-reference#sub
const (
	NotificationTypeSubscriptionRecovered            = 1
	NotificationTypeSubscriptionRenewed              = 2
	NotificationTypeSubscriptionCanceled             = 3
	NotificationTypeSubscriptionPurchased            = 4
	NotificationTypeSubscriptionOnHold               = 5
	NotificationTypeSubscriptionInGracePeriod        = 6
	NotificationTypeSubscriptionRestarted            = 7
	NotificationTypeSubscriptionPriceChangeConfirmed = 8
	NotificationTypeSubscriptionDeferred             = 9
	NotificationTypeSubscriptionPaused               = 10
	NotificationTypeSubscriptionPauseScheduleChanged = 11
	NotificationTypeSubscriptionRevoked              = 12
	NotificationTypeSubscriptionExpired              = 13
)

// PubSubPushEnvelope represents the standard outer payload pushed by Google Cloud Pub/Sub.
type PubSubPushEnvelope struct {
	Message      PubSubMessage `json:"message"`
	Subscription string        `json:"subscription"`
}

// PubSubMessage represents the inner message object in a Pub/Sub push envelope.
type PubSubMessage struct {
	Attributes  map[string]string `json:"attributes,omitempty"`
	Data        string            `json:"data"`
	MessageID   string            `json:"messageId"`
	PublishTime string            `json:"publishTime"`
}

// DeveloperNotification represents the decoded JSON payload sent by Google Play RTDN.
type DeveloperNotification struct {
	Version                    string                      `json:"version"`
	PackageName                string                      `json:"packageName"`
	EventTimeMillis            FlexibleTimestamp           `json:"eventTimeMillis"`
	SubscriptionNotification   *SubscriptionNotification   `json:"subscriptionNotification,omitempty"`
	OneTimeProductNotification *OneTimeProductNotification `json:"oneTimeProductNotification,omitempty"`
	TestNotification           *TestNotification           `json:"testNotification,omitempty"`
}

// SubscriptionNotification holds the notification details for subscription lifecycle events.
type SubscriptionNotification struct {
	Version          string `json:"version"`
	NotificationType int    `json:"notificationType"`
	PurchaseToken    string `json:"purchaseToken"`
	SubscriptionID   string `json:"subscriptionId"`
}

// OneTimeProductNotification holds notification details for one-time in-app purchases.
type OneTimeProductNotification struct {
	Version          string `json:"version"`
	NotificationType int    `json:"notificationType"`
	PurchaseToken    string `json:"purchaseToken"`
	SKU              string `json:"sku"`
}

// TestNotification is present when testing RTDN from the Google Play Console.
type TestNotification struct {
	Version string `json:"version"`
}

// FlexibleTimestamp supports unmarshaling eventTimeMillis from both numeric (int64) and string representations.
type FlexibleTimestamp int64

// UnmarshalJSON parses a millisecond timestamp formatted either as a JSON number or quoted string.
func (ft *FlexibleTimestamp) UnmarshalJSON(b []byte) error {
	raw := strings.Trim(string(b), "\"")
	if raw == "null" || raw == "" {
		*ft = 0
		return nil
	}
	val, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid flexible timestamp %q: %w", raw, err)
	}
	*ft = FlexibleTimestamp(val)
	return nil
}

// Time converts the millisecond epoch timestamp to a time.Time in UTC.
func (ft FlexibleTimestamp) Time() time.Time {
	if ft <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ft)).UTC()
}

// ParsePubSubNotification decodes the outer Pub/Sub push envelope and the inner
// base64-encoded Google Play DeveloperNotification JSON.
func ParsePubSubNotification(payload []byte) (*PubSubPushEnvelope, *DeveloperNotification, error) {
	if len(payload) == 0 {
		return nil, nil, fmt.Errorf("empty pubsub payload")
	}

	var env PubSubPushEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, nil, fmt.Errorf("malformed pubsub envelope: %w", err)
	}

	if env.Message.MessageID == "" {
		return nil, nil, fmt.Errorf("missing messageId in pubsub envelope")
	}

	if env.Message.Data == "" {
		return nil, nil, fmt.Errorf("missing data in pubsub message")
	}

	decoded, err := base64.StdEncoding.DecodeString(env.Message.Data)
	if err != nil {
		var err2 error
		decoded, err2 = base64.URLEncoding.DecodeString(env.Message.Data)
		if err2 != nil {
			return nil, nil, fmt.Errorf("invalid base64 data in pubsub message: %w", err)
		}
	}

	var notif DeveloperNotification
	if err := json.Unmarshal(decoded, &notif); err != nil {
		return nil, nil, fmt.Errorf("malformed developer notification json: %w", err)
	}

	return &env, &notif, nil
}
