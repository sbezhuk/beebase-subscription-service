package apple_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testPrivateKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestNewAPIClientAndJWTClaims(t *testing.T) {
	var gotURL string
	client, err := apple.NewAPIClient(apple.APIConfig{
		KeyID: "KEY123", IssuerID: "issuer", BundleID: "com.example.app", PrivateKey: testPrivateKey(t),
		Environment: "Sandbox",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
			if len(parts) != 3 {
				t.Fatalf("authorization is not a JWT")
			}
			parsed, _, parseErr := new(jwt.Parser).ParseUnverified(parts[0]+"."+parts[1]+"."+parts[2], jwt.MapClaims{})
			if parseErr != nil {
				t.Fatalf("parse JWT: %v", parseErr)
			}
			claims := parsed.Claims.(jwt.MapClaims)
			if claims["iss"] != "issuer" || claims["aud"] != "appstoreconnect-v1" || claims["bid"] != "com.example.app" {
				t.Fatalf("unexpected claims: %#v", claims)
			}
			if r.Header.Get("Authorization") == "" || r.URL.Host != "api.storekit-sandbox.apple.com" {
				t.Fatalf("request did not use sandbox API")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"environment":"Sandbox","data":[{"subscriptionGroupIdentifier":"group","lastTransactions":[{"status":1,"signedTransactionInfo":"signed"}]}]}`)), Header: make(http.Header)}, nil
		})},
	})
	if err != nil {
		t.Fatalf("NewAPIClient: %v", err)
	}
	result, err := client.GetSubscription(context.Background(), "orig/transaction")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if len(result.Data) != 1 || len(result.Data[0].LastTransactions) != 1 {
		t.Fatalf("nested subscription data was not decoded: %+v", result)
	}
	if !strings.Contains(gotURL, "/subscriptions/orig%2Ftransaction") {
		t.Fatalf("transaction ID was not escaped in URL: %s", gotURL)
	}
}

func TestNewAPIClientValidation(t *testing.T) {
	if _, err := apple.NewAPIClient(apple.APIConfig{}); !errors.Is(err, apple.ErrAPIConfiguration) {
		t.Fatalf("missing configuration error = %v", err)
	}
	if _, err := apple.NewAPIClient(apple.APIConfig{KeyID: "k", IssuerID: "i", BundleID: "b", PrivateKey: "not pem"}); !errors.Is(err, apple.ErrAPIConfiguration) {
		t.Fatalf("invalid PEM error = %v", err)
	}
}

func TestAPIClientErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "auth", status: http.StatusUnauthorized, want: apple.ErrAPIAuthentication},
		{name: "apple invalid transaction", status: http.StatusBadRequest, body: `{"errorCode":4000006,"errorMessage":"The transaction id is invalid."}`, want: apple.ErrAPIResponse},
		{name: "server", status: http.StatusBadGateway, want: apple.ErrAPIUnavailable},
		{name: "malformed", status: http.StatusOK, body: `{`, want: apple.ErrAPIMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := apple.NewAPIClient(apple.APIConfig{
				KeyID: "k", IssuerID: "i", BundleID: "b", PrivateKey: testPrivateKey(t),
				HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
				})},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetSubscription(context.Background(), "transaction")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.name == "apple invalid transaction" {
				var apiErr *apple.APIError
				if !errors.As(err, &apiErr) || apiErr.AppleErrorCode != 4000006 || apiErr.AppleMessage != "The transaction id is invalid." {
					t.Fatalf("Apple diagnostics = %+v", apiErr)
				}
			}
		})
	}

	t.Run("network timeout is unavailable", func(t *testing.T) {
		client, err := apple.NewAPIClient(apple.APIConfig{
			KeyID: "k", IssuerID: "i", BundleID: "b", PrivateKey: testPrivateKey(t),
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetSubscription(context.Background(), "transaction")
		if !errors.Is(err, apple.ErrAPIUnavailable) {
			t.Fatalf("error = %v, want unavailable", err)
		}
	})
}

func TestAppleAPIClientUsesProductionByDefault(t *testing.T) {
	var gotURL string
	client, err := apple.NewAPIClient(apple.APIConfig{
		KeyID: "k", IssuerID: "i", BundleID: "b", PrivateKey: testPrivateKey(t),
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"lastTransactions":[{"status":1,"signedTransactionInfo":"signed"}]}]}`)), Header: make(http.Header)}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSubscription(context.Background(), "tx"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotURL, apple.EnvironmentProductionBaseURL+"/subscriptions/") {
		t.Fatalf("got URL %s", gotURL)
	}
}
