package apple

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrAPIConfiguration  = errors.New("apple api configuration error")
	ErrAPIAuthentication = errors.New("apple api authentication failed")
	ErrAPIUnavailable    = errors.New("apple api unavailable")
	ErrAPIResponse       = errors.New("apple api response error")
	ErrAPIMalformed      = errors.New("malformed apple api response")
	ErrAPIEnvironment    = errors.New("apple api environment mismatch")
)

const (
	EnvironmentProductionBaseURL = "https://api.storekit.apple.com/inApps/v1"
	EnvironmentSandboxBaseURL    = "https://api.storekit-sandbox.apple.com/inApps/v1"
	appleAPIAudience             = "appstoreconnect-v1"
	appleJWTLifetime             = 5 * time.Minute
)

// APIConfig contains server-only credentials and endpoint selection for the
// App Store Server API. The raw PrivateKey is not retained after parsing.
type APIConfig struct {
	KeyID       string
	IssuerID    string
	BundleID    string
	PrivateKey  string
	Environment string
	HTTPClient  *http.Client
}

// APIClient is a concurrency-safe App Store Server API client. It creates a
// short-lived JWT for each request and does not persist authentication tokens.
type APIClient struct {
	keyID      string
	issuerID   string
	bundleID   string
	privateKey *ecdsa.PrivateKey
	baseURL    string
	http       *http.Client
}

// SubscriptionAPI is the small application-facing boundary for Apple
// server-side subscription reconciliation.
type SubscriptionAPI interface {
	GetSubscription(context.Context, string) (*SubscriptionResponse, error)
}

type SubscriptionResponse struct {
	Environment      string            `json:"environment"`
	AppAppleID       int64             `json:"appAppleId"`
	BundleID         string            `json:"bundleId"`
	Status           int               `json:"status"`
	LastTransactions []LastTransaction `json:"lastTransactions"`
}

type LastTransaction struct {
	Status                int    `json:"status"`
	SignedTransactionInfo string `json:"signedTransactionInfo"`
	SignedRenewalInfo     string `json:"signedRenewalInfo"`
}

func NewAPIClient(cfg APIConfig) (*APIClient, error) {
	if strings.TrimSpace(cfg.KeyID) == "" || strings.TrimSpace(cfg.IssuerID) == "" || strings.TrimSpace(cfg.BundleID) == "" || strings.TrimSpace(cfg.PrivateKey) == "" {
		return nil, ErrAPIConfiguration
	}
	privateKey := strings.ReplaceAll(cfg.PrivateKey, `\n`, "\n")
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, fmt.Errorf("%w: private key is not PEM", ErrAPIConfiguration)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse private key: %v", ErrAPIConfiguration, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		return nil, fmt.Errorf("%w: private key must be P-256 ECDSA", ErrAPIConfiguration)
	}

	baseURL := EnvironmentProductionBaseURL
	if strings.EqualFold(cfg.Environment, "sandbox") {
		baseURL = EnvironmentSandboxBaseURL
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &APIClient{keyID: cfg.KeyID, issuerID: cfg.IssuerID, bundleID: cfg.BundleID, privateKey: key, baseURL: baseURL, http: client}, nil
}

func (c *APIClient) GetSubscription(ctx context.Context, transactionID string) (*SubscriptionResponse, error) {
	if strings.TrimSpace(transactionID) == "" {
		return nil, fmt.Errorf("%w: missing transaction id", ErrAPIResponse)
	}
	token, err := c.authToken(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/subscriptions/"+url.PathEscape(transactionID), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create request", ErrAPIResponse)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: status %d", ErrAPIAuthentication, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: status %d", ErrAPIUnavailable, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrAPIResponse, resp.StatusCode)
	}
	var result SubscriptionResponse
	if err := jsonDecoder(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIMalformed, err)
	}
	if result.Status < 1 || result.Status > 5 || len(result.LastTransactions) == 0 {
		return nil, ErrAPIMalformed
	}
	return &result, nil
}

func (c *APIClient) authToken(now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iss": c.issuerID,
		"iat": now.Unix(),
		"exp": now.Add(appleJWTLifetime).Unix(),
		"aud": appleAPIAudience,
		"bid": c.bundleID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = c.keyID
	token.Header["typ"] = "JWT"
	signed, err := token.SignedString(c.privateKey)
	if err != nil {
		return "", fmt.Errorf("%w: sign jwt", ErrAPIConfiguration)
	}
	return signed, nil
}

// jsonDecoder is deliberately bounded so an upstream error cannot cause an
// unbounded response allocation. Response bodies are never included in errors.
func jsonDecoder(r io.Reader, dst any) error {
	return json.NewDecoder(io.LimitReader(r, 2<<20)).Decode(dst)
}
