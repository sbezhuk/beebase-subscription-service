package google_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
)

// Ensure DefaultClient satisfies the Client interface at compile time.
var _ google.Client = (*google.DefaultClient)(nil)

type mockRoundTripper struct {
	roundTripFn func(req *http.Request) (*http.Response, error)
}

func (m *mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.roundTripFn(req)
}

func newMockHTTPClient(statusCode int, body string) *http.Client {
	return &http.Client{
		Transport: &mockRoundTripper{
			roundTripFn: func(req *http.Request) (*http.Response, error) {
				res := &http.Response{
					StatusCode: statusCode,
					Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    req,
				}
				res.Header.Set("Content-Type", "application/json")
				return res, nil
			},
		},
	}
}

func generateTestServiceAccountJSON(t *testing.T) string {
	t.Helper()
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	privKeyBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privKeyBytes,
	})

	return fmt.Sprintf(`{
		"type": "service_account",
		"project_id": "beebase-test",
		"private_key_id": "test-key-id",
		"private_key": %q,
		"client_email": "test@beebase-test.iam.gserviceaccount.com",
		"client_id": "123456789",
		"auth_uri": "https://accounts.google.com/o/oauth2/auth",
		"token_uri": "https://oauth2.googleapis.com/token"
	}`, string(pemBlock))
}

func TestNewClient_Options(t *testing.T) {
	ctx := context.Background()

	t.Run("creates client with raw JSON credentials", func(t *testing.T) {
		validJSON := generateTestServiceAccountJSON(t)

		cfg := google.Config{
			ServiceAccountJSON: validJSON,
			PackageName:        "com.beebase.production",
		}

		mockClient := newMockHTTPClient(http.StatusOK, `{}`)
		client, err := google.NewClient(ctx, cfg, option.WithHTTPClient(mockClient))
		if err != nil {
			t.Fatalf("unexpected error creating client: %v", err)
		}
		if client == nil {
			t.Fatal("expected non-nil client")
		}
	})

	t.Run("creates client without credentials using WithoutAuthentication", func(t *testing.T) {
		cfg := google.Config{
			PackageName: "com.beebase.production",
		}

		client, err := google.NewClient(ctx, cfg, option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("unexpected error creating unauthenticated client: %v", err)
		}
		if client == nil {
			t.Fatal("expected non-nil client")
		}
	})
}

func TestGetSubscription_Validation(t *testing.T) {
	ctx := context.Background()
	client, err := google.NewClient(ctx, google.Config{PackageName: ""}, option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	t.Run("missing package name returns ErrInvalidArguments", func(t *testing.T) {
		_, err := client.GetSubscription(ctx, "", "beebase_pro", "token123")
		if !errors.Is(err, google.ErrInvalidArguments) {
			t.Errorf("expected ErrInvalidArguments, got %v", err)
		}
	})

	t.Run("missing purchase token returns ErrInvalidArguments", func(t *testing.T) {
		_, err := client.GetSubscription(ctx, "com.beebase.production", "beebase_pro", "")
		if !errors.Is(err, google.ErrInvalidArguments) {
			t.Errorf("expected ErrInvalidArguments, got %v", err)
		}
	})
}

func TestGetSubscription_Success(t *testing.T) {
	ctx := context.Background()
	packageName := "com.beebase.production"
	token := "sample_purchase_token_xyz"

	t.Run("successful active subscription lookup and normalization", func(t *testing.T) {
		mockResponse := `{
			"kind": "androidpublisher#subscriptionPurchaseV2",
			"startTime": "2026-09-01T12:00:00Z",
			"subscriptionState": "SUBSCRIPTION_STATE_ACTIVE",
			"acknowledgementState": "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED",
			"lineItems": [
				{
					"productId": "beebase_pro",
					"expiryTime": "2026-10-01T12:00:00Z",
					"latestSuccessfulOrderId": "GPA.1234-5678-9012-34567",
					"autoRenewingPlan": {
						"autoRenewEnabled": true
					},
					"offerDetails": {
						"basePlanId": "monthly"
					}
				}
			]
		}`

		mockHTTP := newMockHTTPClient(http.StatusOK, mockResponse)
		client, err := google.NewClient(ctx, google.Config{
			PackageName: packageName,
		}, option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		sub, err := client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if err != nil {
			t.Fatalf("GetSubscription: %v", err)
		}

		if sub.PackageName != packageName {
			t.Errorf("got packageName %s, want %s", sub.PackageName, packageName)
		}
		if sub.ProductID != "beebase_pro" {
			t.Errorf("got productId %s, want beebase_pro", sub.ProductID)
		}
		if sub.BasePlanID != "monthly" {
			t.Errorf("got basePlanId %s, want monthly", sub.BasePlanID)
		}
		if sub.FullProductID() != "beebase_pro_monthly" {
			t.Errorf("got FullProductID %s, want beebase_pro_monthly", sub.FullProductID())
		}
		if sub.PurchaseToken != token {
			t.Errorf("got purchaseToken %s, want %s", sub.PurchaseToken, token)
		}
		if sub.State != google.SubscriptionStateActive {
			t.Errorf("got state %s, want ACTIVE", sub.State)
		}
		if !sub.AutoRenewing {
			t.Errorf("expected AutoRenewing = true, got false")
		}
		if sub.LatestOrderID != "GPA.1234-5678-9012-34567" {
			t.Errorf("got latestOrderId %s, want GPA.1234-5678-9012-34567", sub.LatestOrderID)
		}
		if sub.AcknowledgementState != "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED" {
			t.Errorf("got acknowledgementState %s", sub.AcknowledgementState)
		}
		if sub.CancelledAt != nil {
			t.Errorf("expected CancelledAt = nil, got %v", sub.CancelledAt)
		}
		if sub.TestPurchase {
			t.Errorf("expected TestPurchase = false, got true")
		}

		expectedExpiry := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
		if !sub.ExpiryTime.Equal(expectedExpiry) {
			t.Errorf("got ExpiryTime %v, want %v", sub.ExpiryTime, expectedExpiry)
		}
	})

	t.Run("successful yearly plan with user cancellation", func(t *testing.T) {
		mockResponse := `{
			"kind": "androidpublisher#subscriptionPurchaseV2",
			"startTime": "2025-10-01T10:00:00Z",
			"subscriptionState": "SUBSCRIPTION_STATE_CANCELED",
			"acknowledgementState": "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED",
			"canceledStateContext": {
				"userInitiatedCancellation": {
					"cancelTime": "2026-05-15T09:30:00Z"
				}
			},
			"testPurchase": {},
			"lineItems": [
				{
					"productId": "beebase_pro",
					"expiryTime": "2026-10-01T10:00:00Z",
					"latestSuccessfulOrderId": "GPA.9999-8888-7777-66666",
					"autoRenewingPlan": {
						"autoRenewEnabled": false
					},
					"offerDetails": {
						"basePlanId": "yearly"
					}
				}
			]
		}`

		mockHTTP := newMockHTTPClient(http.StatusOK, mockResponse)
		client, err := google.NewClient(ctx, google.Config{
			PackageName: packageName,
		}, option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		sub, err := client.GetSubscription(ctx, "", "", token)
		if err != nil {
			t.Fatalf("GetSubscription: %v", err)
		}

		if sub.FullProductID() != "beebase_pro_yearly" {
			t.Errorf("got FullProductID %s, want beebase_pro_yearly", sub.FullProductID())
		}
		if sub.State != google.SubscriptionStateCanceled {
			t.Errorf("got state %s, want CANCELED", sub.State)
		}
		if sub.AutoRenewing {
			t.Errorf("expected AutoRenewing = false, got true")
		}
		if sub.CancelledAt == nil {
			t.Fatalf("expected non-nil CancelledAt")
		}
		expectedCancel := time.Date(2026, 5, 15, 9, 30, 0, 0, time.UTC)
		if !sub.CancelledAt.Equal(expectedCancel) {
			t.Errorf("got CancelledAt %v, want %v", sub.CancelledAt, expectedCancel)
		}
		if sub.CancellationReason != google.CancellationReasonUserInitiated {
			t.Errorf("got CancellationReason %s, want USER_INITIATED", sub.CancellationReason)
		}
		if !sub.TestPurchase {
			t.Errorf("expected TestPurchase = true, got false")
		}
	})

	t.Run("states mapping: grace period, on hold, expired, paused", func(t *testing.T) {
		states := []struct {
			rawState      string
			expectedState google.SubscriptionState
		}{
			{"SUBSCRIPTION_STATE_IN_GRACE_PERIOD", google.SubscriptionStateInGracePeriod},
			{"SUBSCRIPTION_STATE_ON_HOLD", google.SubscriptionStateOnHold},
			{"SUBSCRIPTION_STATE_EXPIRED", google.SubscriptionStateExpired},
			{"SUBSCRIPTION_STATE_PAUSED", google.SubscriptionStatePaused},
			{"SUBSCRIPTION_STATE_PENDING", google.SubscriptionStatePending},
			{"UNKNOWN_NEW_STATE", google.SubscriptionStateUnspecified},
		}

		for _, tc := range states {
			t.Run(tc.rawState, func(t *testing.T) {
				resp := fmt.Sprintf(`{
					"subscriptionState": "%s",
					"lineItems": [{
						"productId": "beebase_pro",
						"expiryTime": "2026-10-01T12:00:00Z"
					}]
				}`, tc.rawState)

				mockHTTP := newMockHTTPClient(http.StatusOK, resp)
				c, err := google.NewClient(ctx, google.Config{PackageName: packageName},
					option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())
				if err != nil {
					t.Fatalf("NewClient: %v", err)
				}
				s, err := c.GetSubscription(ctx, packageName, "beebase_pro", "tok")
				if err != nil {
					t.Fatalf("GetSubscription: %v", err)
				}
				if s.State != tc.expectedState {
					t.Errorf("got state %s, want %s", s.State, tc.expectedState)
				}
			})
		}
	})
}

func TestGetSubscription_Errors(t *testing.T) {
	ctx := context.Background()
	packageName := "com.beebase.production"
	token := "token_err_test"

	t.Run("404 Not Found returns ErrSubscriptionNotFound", func(t *testing.T) {
		errResp := `{
			"error": {
				"code": 404,
				"message": "The subscription purchase token was not found",
				"status": "NOT_FOUND"
			}
		}`

		mockHTTP := newMockHTTPClient(http.StatusNotFound, errResp)
		client, err := google.NewClient(ctx, google.Config{
			PackageName: packageName,
		}, option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		_, err = client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, google.ErrSubscriptionNotFound) {
			t.Errorf("expected ErrSubscriptionNotFound, got %v", err)
		}
	})

	t.Run("500 Internal Server Error returns ErrGoogleAPI", func(t *testing.T) {
		errResp := `{
			"error": {
				"code": 500,
				"message": "Internal error",
				"status": "INTERNAL"
			}
		}`

		mockHTTP := newMockHTTPClient(http.StatusInternalServerError, errResp)
		client, err := google.NewClient(ctx, google.Config{
			PackageName: packageName,
		}, option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		_, err = client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, google.ErrGoogleAPI) {
			t.Errorf("expected ErrGoogleAPI, got %v", err)
		}
	})

	t.Run("empty line items returns ErrMalformedResponse", func(t *testing.T) {
		mockHTTP := newMockHTTPClient(http.StatusOK, `{"subscriptionState": "SUBSCRIPTION_STATE_ACTIVE", "lineItems": []}`)
		client, _ := google.NewClient(ctx, google.Config{PackageName: packageName},
			option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())

		_, err := client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if !errors.Is(err, google.ErrMalformedResponse) {
			t.Errorf("expected ErrMalformedResponse for empty lineItems, got %v", err)
		}
	})

	t.Run("missing expiry_time returns ErrMalformedResponse", func(t *testing.T) {
		mockHTTP := newMockHTTPClient(http.StatusOK, `{"lineItems": [{"productId": "beebase_pro"}]}`)
		client, _ := google.NewClient(ctx, google.Config{PackageName: packageName},
			option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())

		_, err := client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if !errors.Is(err, google.ErrMalformedResponse) {
			t.Errorf("expected ErrMalformedResponse for missing expiry_time, got %v", err)
		}
	})

	t.Run("invalid expiry_time format returns ErrMalformedResponse", func(t *testing.T) {
		mockHTTP := newMockHTTPClient(http.StatusOK, `{"lineItems": [{"productId": "beebase_pro", "expiryTime": "not-a-timestamp"}]}`)
		client, _ := google.NewClient(ctx, google.Config{PackageName: packageName},
			option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())

		_, err := client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if !errors.Is(err, google.ErrMalformedResponse) {
			t.Errorf("expected ErrMalformedResponse for bad timestamp, got %v", err)
		}
	})

	t.Run("filter subscriptionID mismatch returns ErrSubscriptionNotFound", func(t *testing.T) {
		mockHTTP := newMockHTTPClient(http.StatusOK, `{"lineItems": [{"productId": "different_app_product", "expiryTime": "2026-10-01T12:00:00Z"}]}`)
		client, _ := google.NewClient(ctx, google.Config{PackageName: packageName},
			option.WithHTTPClient(mockHTTP), option.WithoutAuthentication())

		_, err := client.GetSubscription(ctx, packageName, "beebase_pro", token)
		if !errors.Is(err, google.ErrSubscriptionNotFound) {
			t.Errorf("expected ErrSubscriptionNotFound when product ID is not found, got %v", err)
		}
	})
}
