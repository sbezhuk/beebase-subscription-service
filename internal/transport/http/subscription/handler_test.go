package subscription_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/sbezhuk/beebase-common/authmw"
	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/apple"
	"github.com/sbezhuk/beebase-subscription-service/internal/platform/google"
	subhttp "github.com/sbezhuk/beebase-subscription-service/internal/transport/http/subscription"
)

const testBundleID = "com.beebase.production"

// --- stub token parser ---

type stubParser struct {
	userID uuid.UUID
	err    error
}

func (s *stubParser) Parse(_ context.Context, _ string) (uuid.UUID, error) {
	if s.err != nil {
		return uuid.Nil, s.err
	}
	return s.userID, nil
}

// --- fake repository ---

type fakeRepo struct {
	mu          sync.Mutex
	subsByID    map[uuid.UUID]*subscription.Subscription
	subsByUser  map[uuid.UUID]*subscription.Subscription
	subsByTrans map[string]*subscription.Subscription
	subsByToken map[string]*subscription.Subscription
	events      map[string]bool
	failWith    error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		subsByID:    make(map[uuid.UUID]*subscription.Subscription),
		subsByUser:  make(map[uuid.UUID]*subscription.Subscription),
		subsByTrans: make(map[string]*subscription.Subscription),
		subsByToken: make(map[string]*subscription.Subscription),
		events:      make(map[string]bool),
	}
}

func (f *fakeRepo) FindByID(_ context.Context, id uuid.UUID) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subsByID[id]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeRepo) FindByUserID(_ context.Context, userID uuid.UUID) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subsByUser[userID]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeRepo) FindByProviderAndTransaction(_ context.Context, _ subscription.Provider, origTxID string) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subsByTrans[origTxID]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeRepo) FindByProviderAndPurchaseToken(_ context.Context, _ subscription.Provider, token string) (*subscription.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.subsByToken[token]
	if !ok {
		return nil, subscription.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (f *fakeRepo) Create(_ context.Context, s *subscription.Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		err := f.failWith
		f.failWith = nil
		return err
	}
	cp := *s
	f.subsByID[s.ID] = &cp
	f.subsByUser[s.UserID] = &cp
	if s.OriginalTransactionID != nil {
		f.subsByTrans[*s.OriginalTransactionID] = &cp
	}
	if s.PurchaseToken != nil {
		f.subsByToken[*s.PurchaseToken] = &cp
	}
	return nil
}

func (f *fakeRepo) Update(_ context.Context, s *subscription.Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *s
	f.subsByID[s.ID] = &cp
	f.subsByUser[s.UserID] = &cp
	if s.OriginalTransactionID != nil {
		f.subsByTrans[*s.OriginalTransactionID] = &cp
	}
	if s.PurchaseToken != nil {
		f.subsByToken[*s.PurchaseToken] = &cp
	}
	return nil
}

func (f *fakeRepo) Upsert(ctx context.Context, s *subscription.Subscription) error {
	f.mu.Lock()
	_, exists := f.subsByID[s.ID]
	f.mu.Unlock()
	if exists {
		return f.Update(ctx, s)
	}
	return f.Create(ctx, s)
}

func (f *fakeRepo) RecordEventIfNotExists(_ context.Context, event *subscription.Event) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := string(event.Provider) + ":" + event.EventID
	if f.events[key] {
		return false, nil
	}
	f.events[key] = true
	return true, nil
}

func (f *fakeRepo) GetEvent(_ context.Context, _ subscription.Provider, _ string) (*subscription.Event, error) {
	return nil, subscription.ErrNotFound
}

func (f *fakeRepo) WithTx(ctx context.Context, fn func(subscription.Repository) error) error {
	return fn(f)
}

// --- mock verifiers ---

type mockAppleVerifier struct {
	verifyTxFn func(string) (*apple.TransactionInfo, error)
}

func (m *mockAppleVerifier) VerifyNotification(_ string) (*apple.NotificationPayload, error) {
	return nil, errors.New("not used in this test")
}

func (m *mockAppleVerifier) VerifyTransaction(s string) (*apple.TransactionInfo, error) {
	if m.verifyTxFn != nil {
		return m.verifyTxFn(s)
	}
	return nil, errors.New("verifyTxFn not set")
}

func (m *mockAppleVerifier) VerifyRenewalInfo(_ string) (*apple.RenewalInfo, error) {
	return nil, nil
}

type mockGoogleClient struct {
	getSubFn func(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error)
}

func (m *mockGoogleClient) GetSubscription(ctx context.Context, packageName, subscriptionID, purchaseToken string) (*google.Subscription, error) {
	if m.getSubFn != nil {
		return m.getSubFn(ctx, packageName, subscriptionID, purchaseToken)
	}
	return nil, google.ErrSubscriptionNotFound
}

// --- helpers ---

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newSvc(repo subscription.Repository, verifier apple.Verifier) *appsub.Service {
	return appsub.NewService(repo, verifier, testBundleID, subscription.EnvironmentSandbox, testLogger())
}

// buildRouter returns a chi router with RequireAuth using the given token parser.
func buildRouter(svc *appsub.Service, parser authmw.AccessTokenParser) http.Handler {
	h := subhttp.NewHandler(svc, testLogger())
	r := chi.NewRouter()
	r.Route("/api/v1/subscription", func(r chi.Router) {
		r.Use(authmw.RequireAuth(parser))
		r.Get("/", h.GetSubscription)
		r.Post("/verify", h.VerifyPurchase)
		r.Post("/restore", h.RestorePurchases)
	})
	return r
}

func doJSON(t *testing.T, router http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeResp(t *testing.T, rec *httptest.ResponseRecorder) subhttp.SubscriptionResponse {
	t.Helper()
	var resp subhttp.SubscriptionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	return resp
}

// --- GET /subscription ---

func TestGetSubscription_NoRecord_ReturnsFree(t *testing.T) {
	userID := uuid.New()
	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/subscription/", nil, "tok")
	require.Equal(t, http.StatusOK, rec.Code)
	resp := decodeResp(t, rec)
	require.Equal(t, "free", resp.Entitlement)
	require.Nil(t, resp.Subscription)
}

func TestGetSubscription_ActiveSub_ReturnsPro(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	expires := time.Now().Add(30 * 24 * time.Hour).UTC()
	origTx := "orig-tx-active"
	sub := &subscription.Subscription{
		ID: uuid.New(), UserID: userID,
		Provider: subscription.ProviderApple, ProductID: appsub.ProductProMonthly,
		OriginalTransactionID: &origTx,
		Status:                subscription.StatusActive, ExpiresAt: &expires,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = repo.Create(context.Background(), sub)

	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, &mockAppleVerifier{}), parser)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/subscription/", nil, "tok")
	require.Equal(t, http.StatusOK, rec.Code)
	resp := decodeResp(t, rec)
	require.Equal(t, "pro", resp.Entitlement)
	require.NotNil(t, resp.Subscription)
	require.Equal(t, string(subscription.StatusActive), resp.Subscription.Status)
}

func TestGetSubscription_ExpiredSub_ReturnsFree(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	pastExpiry := time.Now().Add(-24 * time.Hour).UTC()
	sub := &subscription.Subscription{
		ID: uuid.New(), UserID: userID,
		Provider: subscription.ProviderApple, ProductID: appsub.ProductProMonthly,
		Status: subscription.StatusExpired, ExpiresAt: &pastExpiry,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = repo.Create(context.Background(), sub)

	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, &mockAppleVerifier{}), parser)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/subscription/", nil, "tok")
	require.Equal(t, http.StatusOK, rec.Code)
	resp := decodeResp(t, rec)
	require.Equal(t, "free", resp.Entitlement)
	require.NotNil(t, resp.Subscription)
}

func TestGetSubscription_CancelledFutureExpiry_ReturnsPro(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	futureExpiry := time.Now().Add(7 * 24 * time.Hour).UTC()
	sub := &subscription.Subscription{
		ID: uuid.New(), UserID: userID,
		Provider: subscription.ProviderApple, ProductID: appsub.ProductProMonthly,
		Status: subscription.StatusCancelled, ExpiresAt: &futureExpiry,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	_ = repo.Create(context.Background(), sub)

	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, &mockAppleVerifier{}), parser)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/subscription/", nil, "tok")
	require.Equal(t, http.StatusOK, rec.Code)
	resp := decodeResp(t, rec)
	require.Equal(t, "pro", resp.Entitlement)
}

func TestGetSubscription_Unauthenticated_Returns401(t *testing.T) {
	parser := &stubParser{err: authmw.ErrInvalidToken}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	rec := doJSON(t, router, http.MethodGet, "/api/v1/subscription/", nil, "bad")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// --- POST /subscription/verify ---

func TestVerifyPurchase_Apple_Valid_ReturnsPro(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	origTx := "verify-apple-orig"
	expires := time.Now().Add(30 * 24 * time.Hour).UnixMilli()

	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTx, TransactionID: "tx-1",
				BundleID: testBundleID, ProductID: appsub.ProductProMonthly,
				ExpiresDate: expires, Environment: "Sandbox",
			}, nil
		},
	}

	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, verifier), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "valid-jwt"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeResp(t, rec)
	require.Equal(t, "pro", resp.Entitlement)
	require.NotNil(t, resp.Subscription)
	require.Equal(t, appsub.ProductProMonthly, resp.Subscription.ProductID)
}

func TestVerifyPurchase_Google_Valid_ReturnsPro(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()

	gClient := &mockGoogleClient{
		getSubFn: func(_ context.Context, pkg, subID, tok string) (*google.Subscription, error) {
			return &google.Subscription{
				PackageName: pkg, ProductID: subID, BasePlanID: "monthly",
				PurchaseToken: tok, State: google.SubscriptionStateActive,
				ExpiryTime: time.Now().Add(30 * 24 * time.Hour).UTC(), AutoRenewing: true,
			}, nil
		},
	}

	parser := &stubParser{userID: userID}
	svc := newSvc(repo, &mockAppleVerifier{}).WithGoogle(gClient, "com.beebase.production")
	router := buildRouter(svc, parser)

	body := map[string]string{"provider": "google", "purchase_token": "google-tok-abc"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeResp(t, rec)
	require.Equal(t, "pro", resp.Entitlement)
	require.NotNil(t, resp.Subscription)
}

func TestVerifyPurchase_MissingProvider_Returns400(t *testing.T) {
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	body := map[string]string{"signed_transaction": "something"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyPurchase_Apple_MissingSignedTransaction_Returns400(t *testing.T) {
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	body := map[string]string{"provider": "apple"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyPurchase_Google_MissingPurchaseToken_Returns400(t *testing.T) {
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	body := map[string]string{"provider": "google"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyPurchase_UnsupportedProvider_Returns400(t *testing.T) {
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	body := map[string]string{"provider": "stripe", "signed_transaction": "x"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "unsupported_provider")
}

func TestVerifyPurchase_Apple_VerificationFailure_Returns400(t *testing.T) {
	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return nil, errors.New("invalid signature")
		},
	}
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), verifier), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "bad-jwt"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "verification_failed")
}

func TestVerifyPurchase_Idempotent(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	origTx := "orig-idempotent"
	expires := time.Now().Add(30 * 24 * time.Hour).UnixMilli()

	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTx, TransactionID: "tx-i",
				BundleID: testBundleID, ProductID: appsub.ProductProMonthly,
				ExpiresDate: expires, Environment: "Sandbox",
			}, nil
		},
	}

	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, verifier), parser)
	body := map[string]string{"provider": "apple", "signed_transaction": "valid-tx"}

	rec1 := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusOK, rec1.Code)

	rec2 := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusOK, rec2.Code)

	resp2 := decodeResp(t, rec2)
	require.Equal(t, "pro", resp2.Entitlement)
}

func TestVerifyPurchase_UnsupportedProduct_Returns400(t *testing.T) {
	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: "any", TransactionID: "any-tx",
				BundleID: testBundleID, ProductID: "fake_pro_product",
				Environment: "Sandbox",
			}, nil
		},
	}
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), verifier), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "some-tx"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestVerifyPurchase_Unauthenticated_Returns401(t *testing.T) {
	parser := &stubParser{err: authmw.ErrInvalidToken}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "x"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/verify", body, "tok")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// --- POST /subscription/restore ---

func TestRestorePurchases_Apple_Valid_ReturnsPro(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	origTx := "restore-apple-orig"
	expires := time.Now().Add(15 * 24 * time.Hour).UnixMilli()

	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTx, TransactionID: "restore-tx",
				BundleID: testBundleID, ProductID: appsub.ProductProYearly,
				ExpiresDate: expires, Environment: "Sandbox",
			}, nil
		},
	}
	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, verifier), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "restore-signed-tx"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/restore", body, "tok")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeResp(t, rec)
	require.Equal(t, "pro", resp.Entitlement)
}

func TestRestorePurchases_Google_Valid_ReturnsPro(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()

	gClient := &mockGoogleClient{
		getSubFn: func(_ context.Context, pkg, subID, tok string) (*google.Subscription, error) {
			return &google.Subscription{
				PackageName: pkg, ProductID: subID, BasePlanID: "yearly",
				PurchaseToken: tok, State: google.SubscriptionStateActive,
				ExpiryTime: time.Now().Add(300 * 24 * time.Hour).UTC(), AutoRenewing: true,
			}, nil
		},
	}
	parser := &stubParser{userID: userID}
	svc := newSvc(repo, &mockAppleVerifier{}).WithGoogle(gClient, "com.beebase.production")
	router := buildRouter(svc, parser)

	body := map[string]string{"provider": "google", "purchase_token": "restore-google-token"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/restore", body, "tok")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp := decodeResp(t, rec)
	require.Equal(t, "pro", resp.Entitlement)
}

func TestRestorePurchases_InvalidJSON_Returns400(t *testing.T) {
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscription/restore", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRestorePurchases_VerificationFailure_Returns400(t *testing.T) {
	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return nil, errors.New("invalid receipt")
		},
	}
	parser := &stubParser{userID: uuid.New()}
	router := buildRouter(newSvc(newFakeRepo(), verifier), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "bad"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/restore", body, "tok")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestRestorePurchases_RepeatedIsIdempotent(t *testing.T) {
	repo := newFakeRepo()
	userID := uuid.New()
	origTx := "restore-idempotent"
	expires := time.Now().Add(30 * 24 * time.Hour).UnixMilli()

	verifier := &mockAppleVerifier{
		verifyTxFn: func(_ string) (*apple.TransactionInfo, error) {
			return &apple.TransactionInfo{
				OriginalTransactionID: origTx, TransactionID: "tx-r",
				BundleID: testBundleID, ProductID: appsub.ProductProMonthly,
				ExpiresDate: expires, Environment: "Sandbox",
			}, nil
		},
	}
	parser := &stubParser{userID: userID}
	router := buildRouter(newSvc(repo, verifier), parser)
	body := map[string]string{"provider": "apple", "signed_transaction": "restore-signed"}

	rec1 := doJSON(t, router, http.MethodPost, "/api/v1/subscription/restore", body, "tok")
	require.Equal(t, http.StatusOK, rec1.Code)

	rec2 := doJSON(t, router, http.MethodPost, "/api/v1/subscription/restore", body, "tok")
	require.Equal(t, http.StatusOK, rec2.Code)
	resp2 := decodeResp(t, rec2)
	require.Equal(t, "pro", resp2.Entitlement)
}

func TestRestorePurchases_Unauthenticated_Returns401(t *testing.T) {
	parser := &stubParser{err: authmw.ErrInvalidToken}
	router := buildRouter(newSvc(newFakeRepo(), &mockAppleVerifier{}), parser)

	body := map[string]string{"provider": "apple", "signed_transaction": "x"}
	rec := doJSON(t, router, http.MethodPost, "/api/v1/subscription/restore", body, "tok")
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
