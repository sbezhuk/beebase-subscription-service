// Package config loads subscription-service configuration from environment
// variables with sane defaults for local development.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Config holds all runtime configuration for the subscription-service.
type Config struct {
	Env string // "development" or "production"

	HTTPPort            string
	HTTPReadTimeout     time.Duration
	HTTPWriteTimeout    time.Duration
	HTTPIdleTimeout     time.Duration
	HTTPShutdownTimeout time.Duration

	LogLevel string // "debug", "info", "warn", "error"

	DatabaseURL            string
	DatabaseConnectTimeout time.Duration

	// Apple App Store configuration
	AppleBundleID    string
	AppleKeyID       string
	AppleIssuerID    string
	ApplePrivateKey  string
	AppleEnvironment string // "Sandbox" or "Production"

	// Google Play configuration
	GoogleServiceAccountJSON string
	GooglePackageName        string

	// AuthJWKSURL points at auth-service's JWKS endpoint for token verification.
	AuthJWKSURL string
	// RedisAddr is the shared session store for token revocation.
	RedisAddr            string
	RedisConnectTimeout  time.Duration
	InternalServiceToken string

	// ProEntitlementAllowlist grants effective Pro entitlement to these
	// user IDs regardless of their real subscription state - see
	// application/subscription.Service.WithProEntitlementAllowlist. Empty
	// (the default, when PRO_ENTITLEMENT_ALLOWLIST is unset or blank)
	// leaves entitlement decisions completely unaffected.
	ProEntitlementAllowlist []uuid.UUID
}

// Load builds a Config from environment variables, falling back to
// defaults suitable for local development where a variable is unset.
func Load() (*Config, error) {
	env := getEnv("APP_ENV", "development")

	appleEnv := getEnv("APPLE_ENVIRONMENT", "")
	if appleEnv == "" {
		if env == "production" {
			appleEnv = "Production"
		} else {
			appleEnv = "Sandbox"
		}
	}

	proAllowlist, err := parseUUIDAllowlist("PRO_ENTITLEMENT_ALLOWLIST")
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Env: env,

		HTTPPort:            getEnv("HTTP_PORT", "8080"),
		HTTPReadTimeout:     getDuration("HTTP_READ_TIMEOUT", 5*time.Second),
		HTTPWriteTimeout:    getDuration("HTTP_WRITE_TIMEOUT", 10*time.Second),
		HTTPIdleTimeout:     getDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		HTTPShutdownTimeout: getDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),

		LogLevel: getEnv("LOG_LEVEL", "info"),

		DatabaseURL:            getEnv("DATABASE_URL", ""),
		DatabaseConnectTimeout: getDuration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),

		AppleBundleID:    getEnv("APPLE_BUNDLE_ID", "com.beebase.production"),
		AppleKeyID:       getEnv("APPLE_KEY_ID", ""),
		AppleIssuerID:    getEnv("APPLE_ISSUER_ID", ""),
		ApplePrivateKey:  getEnv("APPLE_PRIVATE_KEY", ""),
		AppleEnvironment: appleEnv,

		GoogleServiceAccountJSON: getEnv("GOOGLE_SERVICE_ACCOUNT_JSON", ""),
		GooglePackageName:        getEnv("GOOGLE_PACKAGE_NAME", "com.beebase.production"),

		AuthJWKSURL:          getEnv("AUTH_JWKS_URL", ""),
		RedisAddr:            getEnv("REDIS_ADDR", ""),
		RedisConnectTimeout:  getDuration("REDIS_CONNECT_TIMEOUT", 5*time.Second),
		InternalServiceToken: getEnv("INTERNAL_SERVICE_TOKEN", ""),

		ProEntitlementAllowlist: proAllowlist,
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("config: DATABASE_URL is required")
	}
	if cfg.AuthJWKSURL == "" {
		return nil, fmt.Errorf("config: AUTH_JWKS_URL is required")
	}
	if cfg.RedisAddr == "" {
		return nil, fmt.Errorf("config: REDIS_ADDR is required")
	}
	if env == "production" {
		if cfg.AppleKeyID == "" || cfg.AppleIssuerID == "" || cfg.ApplePrivateKey == "" {
			return nil, fmt.Errorf("config: APPLE_KEY_ID, APPLE_ISSUER_ID, and APPLE_PRIVATE_KEY are required in production")
		}
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

// parseUUIDAllowlist parses key as a comma-separated list of user IDs,
// trimming whitespace around each entry, skipping empty entries (e.g. a
// trailing comma), and deduplicating repeated IDs while preserving
// first-occurrence order. Unset or blank (after trimming) returns a nil,
// error-free allowlist, so the feature is fully inert unless explicitly
// configured.
//
// Fails fast: any non-empty entry that isn't a well-formed UUID is a
// configuration error, not something to silently drop - a typo here must
// never quietly narrow (or, if the typo happened to collide, widen) who
// receives effective Pro access. The error names key and the single
// offending entry only, never the full configured list.
func parseUUIDAllowlist(key string) ([]uuid.UUID, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := uuid.Parse(part)
		if err != nil {
			return nil, fmt.Errorf("config: %s contains an invalid user id %q", key, part)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}
