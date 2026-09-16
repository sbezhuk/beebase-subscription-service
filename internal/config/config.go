// Package config loads subscription-service configuration from environment
// variables with sane defaults for local development.
package config

import (
	"fmt"
	"os"
	"time"
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
