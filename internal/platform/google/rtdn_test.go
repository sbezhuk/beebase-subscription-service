package google_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
)

func TestParsePubSubNotification_Success(t *testing.T) {
	innerJSON := `{
		"version": "1.0",
		"packageName": "com.beebase.production",
		"eventTimeMillis": "1725969600000",
		"subscriptionNotification": {
			"version": "1.0",
			"notificationType": 2,
			"purchaseToken": "token-xyz-12345",
			"subscriptionId": "beebase_pro"
		}
	}`

	encoded := base64.StdEncoding.EncodeToString([]byte(innerJSON))
	envelope := map[string]any{
		"message": map[string]any{
			"messageId":   "msg-pubsub-100",
			"data":        encoded,
			"publishTime": "2026-09-10T10:00:00.000Z",
		},
		"subscription": "projects/beebase-production/subscriptions/play-rtdn",
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)

	env, notif, err := google.ParsePubSubNotification(body)
	require.NoError(t, err)
	require.NotNil(t, env)
	require.NotNil(t, notif)

	require.Equal(t, "msg-pubsub-100", env.Message.MessageID)
	require.Equal(t, "com.beebase.production", notif.PackageName)
	require.NotNil(t, notif.SubscriptionNotification)
	require.Equal(t, 2, notif.SubscriptionNotification.NotificationType)
	require.Equal(t, "beebase_pro", notif.SubscriptionNotification.SubscriptionID)
	require.Equal(t, "token-xyz-12345", notif.SubscriptionNotification.PurchaseToken)
	require.Equal(t, int64(1725969600000), int64(notif.EventTimeMillis))
	require.Equal(t, time.UnixMilli(1725969600000).UTC(), notif.EventTimeMillis.Time())
}

func TestParsePubSubNotification_NumericEventTimeMillis(t *testing.T) {
	innerJSON := `{
		"version": "1.0",
		"packageName": "com.beebase.production",
		"eventTimeMillis": 1725969600000,
		"testNotification": {
			"version": "1.0"
		}
	}`

	encoded := base64.StdEncoding.EncodeToString([]byte(innerJSON))
	envelope := map[string]any{
		"message": map[string]any{
			"messageId": "msg-test-1",
			"data":      encoded,
		},
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)

	env, notif, err := google.ParsePubSubNotification(body)
	require.NoError(t, err)
	require.Equal(t, "msg-test-1", env.Message.MessageID)
	require.NotNil(t, notif.TestNotification)
	require.Nil(t, notif.SubscriptionNotification)
	require.Equal(t, int64(1725969600000), int64(notif.EventTimeMillis))
}

func TestParsePubSubNotification_Errors(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		errSubstr string
	}{
		{
			name:      "empty payload",
			payload:   nil,
			errSubstr: "empty pubsub payload",
		},
		{
			name:      "malformed outer json",
			payload:   []byte(`not-json`),
			errSubstr: "malformed pubsub envelope",
		},
		{
			name:      "missing messageId",
			payload:   []byte(`{"message":{"data":"dGVzdA=="}}`),
			errSubstr: "missing messageId",
		},
		{
			name:      "missing data",
			payload:   []byte(`{"message":{"messageId":"123"}}`),
			errSubstr: "missing data",
		},
		{
			name:      "invalid base64 data",
			payload:   []byte(`{"message":{"messageId":"123","data":"!!invalid-base64@@"}}`),
			errSubstr: "invalid base64 data",
		},
		{
			name: "malformed inner developer notification json",
			payload: func() []byte {
				data := base64.StdEncoding.EncodeToString([]byte(`not-a-valid-json`))
				b, _ := json.Marshal(map[string]any{
					"message": map[string]any{
						"messageId": "123",
						"data":      data,
					},
				})
				return b
			}(),
			errSubstr: "malformed developer notification json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, notif, err := google.ParsePubSubNotification(tt.payload)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.errSubstr)
			require.Nil(t, env)
			require.Nil(t, notif)
		})
	}
}
