package config

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// setRequiredEnv sets the env vars Load requires so tests can focus on
// PRO_ENTITLEMENT_ALLOWLIST parsing without unrelated failures.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/test")
	t.Setenv("AUTH_JWKS_URL", "http://localhost/.well-known/jwks.json")
	t.Setenv("REDIS_ADDR", "localhost:6379")
}

// unsetEnv ensures key is completely absent (not merely empty) for the
// duration of the test, restoring whatever was there before afterward.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		}
	})
}

func TestLoad_ProEntitlementAllowlist_UnsetIsEmpty(t *testing.T) {
	setRequiredEnv(t)
	unsetEnv(t, "PRO_ENTITLEMENT_ALLOWLIST")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ProEntitlementAllowlist) != 0 {
		t.Fatalf("ProEntitlementAllowlist = %v, want empty when unset", cfg.ProEntitlementAllowlist)
	}
}

func TestLoad_ProEntitlementAllowlist_EmptyOrWhitespaceIsEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t"} {
		t.Run("raw="+raw, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", raw)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfg.ProEntitlementAllowlist) != 0 {
				t.Fatalf("ProEntitlementAllowlist = %v, want empty for %q", cfg.ProEntitlementAllowlist, raw)
			}
		})
	}
}

func TestLoad_ProEntitlementAllowlist_SingleValidUUID(t *testing.T) {
	setRequiredEnv(t)
	id := uuid.New()
	t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", id.String())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ProEntitlementAllowlist) != 1 || cfg.ProEntitlementAllowlist[0] != id {
		t.Fatalf("ProEntitlementAllowlist = %v, want [%s]", cfg.ProEntitlementAllowlist, id)
	}
}

func TestLoad_ProEntitlementAllowlist_MultipleValidUUIDs(t *testing.T) {
	setRequiredEnv(t)
	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()
	t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", id1.String()+","+id2.String()+","+id3.String())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []uuid.UUID{id1, id2, id3}
	if len(cfg.ProEntitlementAllowlist) != len(want) {
		t.Fatalf("ProEntitlementAllowlist = %v, want %v", cfg.ProEntitlementAllowlist, want)
	}
	for i, id := range want {
		if cfg.ProEntitlementAllowlist[i] != id {
			t.Fatalf("ProEntitlementAllowlist = %v, want %v", cfg.ProEntitlementAllowlist, want)
		}
	}
}

func TestLoad_ProEntitlementAllowlist_TrimsWhitespaceAroundEntries(t *testing.T) {
	setRequiredEnv(t)
	id1 := uuid.New()
	id2 := uuid.New()
	t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", "  "+id1.String()+" , \t"+id2.String()+"  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ProEntitlementAllowlist) != 2 || cfg.ProEntitlementAllowlist[0] != id1 || cfg.ProEntitlementAllowlist[1] != id2 {
		t.Fatalf("ProEntitlementAllowlist = %v, want [%s %s]", cfg.ProEntitlementAllowlist, id1, id2)
	}
}

func TestLoad_ProEntitlementAllowlist_DuplicateUUIDsAreDeduplicated(t *testing.T) {
	setRequiredEnv(t)
	id1 := uuid.New()
	id2 := uuid.New()
	t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", id1.String()+","+id2.String()+","+id1.String())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []uuid.UUID{id1, id2}
	if len(cfg.ProEntitlementAllowlist) != len(want) {
		t.Fatalf("ProEntitlementAllowlist = %v, want %v (deduplicated, first-occurrence order)", cfg.ProEntitlementAllowlist, want)
	}
	for i, id := range want {
		if cfg.ProEntitlementAllowlist[i] != id {
			t.Fatalf("ProEntitlementAllowlist = %v, want %v", cfg.ProEntitlementAllowlist, want)
		}
	}
}

func TestLoad_ProEntitlementAllowlist_MalformedUUIDFailsLoad(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", "not-a-uuid")

	cfg, err := Load()
	if err == nil {
		t.Fatal("expected Load to fail for a malformed PRO_ENTITLEMENT_ALLOWLIST entry, got nil error")
	}
	if cfg != nil {
		t.Fatalf("expected nil Config on error, got %+v", cfg)
	}
	if !strings.Contains(err.Error(), "PRO_ENTITLEMENT_ALLOWLIST") {
		t.Fatalf("error %q does not identify PRO_ENTITLEMENT_ALLOWLIST as the invalid setting", err.Error())
	}
}

func TestLoad_ProEntitlementAllowlist_MixedValidAndMalformedFailsLoad(t *testing.T) {
	setRequiredEnv(t)
	valid := uuid.New()
	t.Setenv("PRO_ENTITLEMENT_ALLOWLIST", valid.String()+",not-a-uuid")

	cfg, err := Load()
	if err == nil {
		t.Fatal("expected Load to fail when any entry is malformed, even alongside valid ones, got nil error")
	}
	if cfg != nil {
		t.Fatalf("expected nil Config on error, got %+v", cfg)
	}
	if !strings.Contains(err.Error(), "PRO_ENTITLEMENT_ALLOWLIST") {
		t.Fatalf("error %q does not identify PRO_ENTITLEMENT_ALLOWLIST as the invalid setting", err.Error())
	}
}
