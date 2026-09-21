package subscription_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	appsub "github.com/sbezhuk/beebase-subscription-service/internal/application/subscription"
	"github.com/sbezhuk/beebase-subscription-service/internal/domain/subscription"
)

// activeSubscription returns a subscription that HasActiveAccess considers
// real Pro access for userID - the authoritative source entitlementFor
// always checks first, allowlist or not.
func activeSubscription(userID uuid.UUID) *subscription.Subscription {
	expires := time.Now().UTC().Add(24 * time.Hour)
	return &subscription.Subscription{
		ID:        uuid.New(),
		UserID:    userID,
		Provider:  subscription.ProviderApple,
		ProductID: appsub.ProductProMonthly,
		Status:    subscription.StatusActive,
		ExpiresAt: &expires,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}

func newService(repo *fakeRepo) *appsub.Service {
	return appsub.NewService(repo, nil, "com.beebase.production", subscription.EnvironmentProduction, testLogger())
}

// A. allowlist absent (WithProEntitlementAllowlist never called) -> behaves
// exactly as if the feature does not exist, for every kind of caller.
func TestEntitlementAllowlist_AbsentLeavesBehaviorUnchanged(t *testing.T) {
	repo := newFakeRepo()
	svc := newService(repo) // no WithProEntitlementAllowlist call at all

	freeUserID := uuid.New()
	result, err := svc.GetSubscription(context.Background(), freeUserID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Entitlement != appsub.EntitlementFree {
		t.Fatalf("entitlement = %s, want %s (allowlist absent)", result.Entitlement, appsub.EntitlementFree)
	}

	proUserID := uuid.New()
	if err := repo.Create(context.Background(), activeSubscription(proUserID)); err != nil {
		t.Fatalf("seed pro subscription: %v", err)
	}
	result, err = svc.GetSubscription(context.Background(), proUserID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Entitlement != appsub.EntitlementPro {
		t.Fatalf("entitlement = %s, want %s (real pro, allowlist absent)", result.Entitlement, appsub.EntitlementPro)
	}
}

// B. allowlist empty (WithProEntitlementAllowlist called with an empty/nil
// slice) -> identical to it never having been called at all.
func TestEntitlementAllowlist_EmptyIsIdenticalToAbsent(t *testing.T) {
	repo := newFakeRepo()
	svc := newService(repo).WithProEntitlementAllowlist(nil)

	userID := uuid.New()
	result, err := svc.GetSubscription(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Entitlement != appsub.EntitlementFree {
		t.Fatalf("entitlement = %s, want %s (allowlist empty)", result.Entitlement, appsub.EntitlementFree)
	}

	svc2 := newService(newFakeRepo()).WithProEntitlementAllowlist([]uuid.UUID{})
	result2, err := svc2.GetSubscription(context.Background(), userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result2.Entitlement != appsub.EntitlementFree {
		t.Fatalf("entitlement = %s, want %s (allowlist explicitly empty slice)", result2.Entitlement, appsub.EntitlementFree)
	}
}

// C. A normal Free user (not on the allowlist, no active subscription)
// keeps getting Free, even when an allowlist is configured for other users.
func TestEntitlementAllowlist_NonAllowlistedFreeUserUnaffected(t *testing.T) {
	repo := newFakeRepo()
	allowlistedID := uuid.New()
	svc := newService(repo).WithProEntitlementAllowlist([]uuid.UUID{allowlistedID})

	ordinaryFreeUser := uuid.New()
	result, err := svc.GetSubscription(context.Background(), ordinaryFreeUser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Entitlement != appsub.EntitlementFree {
		t.Fatalf("entitlement = %s, want %s (not allowlisted)", result.Entitlement, appsub.EntitlementFree)
	}
	if result.Subscription != nil {
		t.Fatalf("Subscription = %+v, want nil (no real record, never fabricated)", result.Subscription)
	}
}

// D. An existing real Pro user keeps getting Pro exactly as before - the
// allowlist branch in entitlementFor is never even reached for them,
// whether or not they also happen to be on the allowlist.
func TestEntitlementAllowlist_RealProUserUnaffected(t *testing.T) {
	repo := newFakeRepo()
	proUserID := uuid.New()
	sub := activeSubscription(proUserID)
	if err := repo.Create(context.Background(), sub); err != nil {
		t.Fatalf("seed pro subscription: %v", err)
	}

	for _, name := range []string{"not on allowlist", "also on allowlist"} {
		t.Run(name, func(t *testing.T) {
			svc := newService(repo)
			if name == "also on allowlist" {
				svc = svc.WithProEntitlementAllowlist([]uuid.UUID{proUserID})
			}
			result, err := svc.GetSubscription(context.Background(), proUserID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.Entitlement != appsub.EntitlementPro {
				t.Fatalf("entitlement = %s, want %s", result.Entitlement, appsub.EntitlementPro)
			}
			if result.Subscription == nil || result.Subscription.Status != subscription.StatusActive {
				t.Fatalf("Subscription = %+v, want the real active record untouched", result.Subscription)
			}
		})
	}
}

// E. An allowlisted Free user (no real active subscription) receives
// effective Pro entitlement - and *only* that: no Subscription record is
// fabricated or persisted on their behalf.
func TestEntitlementAllowlist_AllowlistedFreeUserGetsEffectivePro(t *testing.T) {
	repo := newFakeRepo()
	allowlistedID := uuid.New()
	svc := newService(repo).WithProEntitlementAllowlist([]uuid.UUID{allowlistedID})

	result, err := svc.GetSubscription(context.Background(), allowlistedID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Entitlement != appsub.EntitlementPro {
		t.Fatalf("entitlement = %s, want %s (allowlisted)", result.Entitlement, appsub.EntitlementPro)
	}
	// The allowlist changes only the reported entitlement string - it must
	// never fabricate or persist a Subscription record.
	if result.Subscription != nil {
		t.Fatalf("Subscription = %+v, want nil (allowlist must not fabricate a persisted record)", result.Subscription)
	}
	if _, err := repo.FindByUserID(context.Background(), allowlistedID); err == nil {
		t.Fatal("expected no persisted subscription row to have been created for the allowlisted user")
	}

	// An expired/cancelled real subscription also still resolves to
	// effective Pro via the allowlist, without the allowlist mutating that
	// stored record either.
	expired := time.Now().UTC().Add(-24 * time.Hour)
	lapsedSub := &subscription.Subscription{
		ID:        uuid.New(),
		UserID:    allowlistedID,
		Provider:  subscription.ProviderApple,
		ProductID: appsub.ProductProMonthly,
		Status:    subscription.StatusExpired,
		ExpiresAt: &expired,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := repo.Create(context.Background(), lapsedSub); err != nil {
		t.Fatalf("seed lapsed subscription: %v", err)
	}
	result, err = svc.GetSubscription(context.Background(), allowlistedID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Entitlement != appsub.EntitlementPro {
		t.Fatalf("entitlement = %s, want %s (allowlisted, real subscription lapsed)", result.Entitlement, appsub.EntitlementPro)
	}
	if result.Subscription == nil || result.Subscription.Status != subscription.StatusExpired {
		t.Fatalf("Subscription = %+v, want the real lapsed record reported unchanged", result.Subscription)
	}
}

// F. The allowlist never affects which user's data is read: GetSubscription
// still looks up strictly by the given userID, and an unrelated user (not
// allowlisted, no subscription) is never granted access as a side effect of
// someone else being on the allowlist. This is the closest analog, at this
// service's boundary, to "ownership/authentication completely unaffected" -
// subscription-service has no separate ownership concept beyond userID
// scoping, and this proves that scoping is untouched by the allowlist.
func TestEntitlementAllowlist_DoesNotLeakAcrossUsers(t *testing.T) {
	repo := newFakeRepo()
	allowlistedID := uuid.New()
	bystanderID := uuid.New()
	svc := newService(repo).WithProEntitlementAllowlist([]uuid.UUID{allowlistedID})

	bystanderResult, err := svc.GetSubscription(context.Background(), bystanderID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bystanderResult.Entitlement != appsub.EntitlementFree {
		t.Fatalf("entitlement = %s, want %s (bystander must not inherit another user's allowlist entry)", bystanderResult.Entitlement, appsub.EntitlementFree)
	}

	allowlistedResult, err := svc.GetSubscription(context.Background(), allowlistedID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowlistedResult.Entitlement != appsub.EntitlementPro {
		t.Fatalf("entitlement = %s, want %s", allowlistedResult.Entitlement, appsub.EntitlementPro)
	}
}
