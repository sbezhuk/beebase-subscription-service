# beebase-subscription-service

In-app purchase verification and subscription management service for
[BeeBase](https://github.com/sbezhuk/beebase-auth-service#trust-model),
an open-source backend for a beekeeper management application split into
microservices. See [CLAUDE.md](https://github.com/sbezhuk/beebase-auth-service/blob/main/CLAUDE.md)
for the architectural rules this service follows.

Register/login/refresh live in `beebase-auth-service` — this service manages
user subscriptions, verifies in-app purchase receipts (Apple App Store StoreKit 2
and Google Play Billing), resolves user entitlement status (`free` vs `pro`),
and validates current store state on demand. It never
trusts a user ID from anywhere but a JWKS-verified access token, and never
trusts a purchase claim without server-side cryptographic store verification.

Related services: `beebase-auth-service` (users, refresh tokens, JWT issuing,
public key JWKS, session revocation via Redis), `beebase-gateway` (single entry
point for clients).

This service is reachable through `beebase-gateway` at `/api/v1/subscription`.
There are currently no public Apple or Google webhook routes in the HTTP
router; entitlement changes are reconciled through verify/restore requests.

## Requirements

- Go 1.27+
- PostgreSQL 16 (or Docker, to run it for you)
- Redis 7+ (shared session store for token revocation via `beebase-common`)
- [golang-migrate](https://github.com/golang-migrate/migrate) CLI, for applying
  migrations outside Docker: `make migrate-install`
- A running `beebase-auth-service` (or anything serving a compatible
  JWKS document) reachable at `AUTH_JWKS_URL`
- (Optional for local store verification) Apple App Store credentials and
  Google Play Service Account credentials

## Quick start

```bash
cp .env.example .env
#  point AUTH_JWKS_URL at a running auth-service, e.g.
#   http://localhost:8081/.well-known/jwks.json
#  point REDIS_ADDR at a running Redis instance, e.g.
#   localhost:6379

# Option A: run Postgres in Docker, app on the host
docker compose up -d postgres
make migrate-up
make run

# Option B: run everything in Docker (migrations run once, automatically)
docker compose up --build
```

Verify it's up:

```bash
curl http://localhost:8080/health   # liveness — always 200 while the process is up
curl http://localhost:8080/ready    # readiness — 200 only if the database is reachable

TOKEN=...  # an access_token from auth-service's /api/v1/auth/register or /login

# Check current subscription entitlement (returns "free" if no active subscription)
curl http://localhost:8080/api/v1/subscription -H "Authorization: Bearer $TOKEN"

# Verify an Apple StoreKit 2 in-app purchase
curl -X POST http://localhost:8080/api/v1/subscription/verify \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"provider":"apple","signed_transaction":"<JWS_TRANSACTION_STRING>"}'

# Verify a Google Play in-app purchase
curl -X POST http://localhost:8080/api/v1/subscription/verify \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"provider":"google","purchase_token":"<PURCHASE_TOKEN>","subscription_id":"beebase_pro"}'

# Restore existing purchases
curl -X POST http://localhost:8080/api/v1/subscription/restore \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"provider":"apple","signed_transaction":"<JWS_TRANSACTION_STRING>"}'
```

The full API surface is documented in [api/openapi.yaml](api/openapi.yaml).

Note: this repo's `docker-compose.yml` is for standalone single-service
development only. To run the full BeeBase stack together, use
`beebase-gateway`'s docker-compose, which builds every service from
sibling checkouts and routes between them.

## Configuration

All configuration is via environment variables (see
[.env.example](.env.example) for the full list — it is a template only,
never read by the app, Docker Compose, or deployment tooling; copy it
once to create your real `.env`, which is what actually gets loaded).
Production configuration is generated at deploy time from AWS SSM
Parameter Store (see `beebase-gateway/deploy/deploy.sh`) — `.env.example`
is never used as a fallback, in development or in production.

| Variable | Default | Description |
| ----------------------------- | ----------------------- | ----------------------------------------------------------------- |
| `APP_ENV` | `development` | `development` or `production` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `HTTP_PORT` | `8080` | Port the HTTP server listens on |
| `HTTP_READ_TIMEOUT` | `5s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | `10s` | Response write timeout |
| `HTTP_IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout |
| `HTTP_SHUTDOWN_TIMEOUT` | `15s` | Max time to wait for graceful shutdown |
| `DATABASE_URL` | *(required)* | PostgreSQL DSN |
| `DATABASE_CONNECT_TIMEOUT` | `10s` | Timeout for the initial DB connection |
| `AUTH_JWKS_URL` | *(required)* | auth-service's public key endpoint, used to verify access tokens |
| `REDIS_ADDR` | *(required)* | Redis host:port, used for access-token revocation checks |
| `REDIS_CONNECT_TIMEOUT` | `5s` | Timeout for initial Redis connection |
| `APPLE_BUNDLE_ID` | `com.beebase.production` | Expected iOS application bundle identifier |
| `APPLE_KEY_ID` | *(unset)* | App Store Connect API Key ID |
| `APPLE_ISSUER_ID` | *(unset)* | App Store Connect API Issuer ID |
| `APPLE_PRIVATE_KEY` | *(unset)* | App Store Connect API private key (PEM format) |
| `APPLE_ENVIRONMENT` | `Sandbox` / `Production`| `Sandbox` (default in development) or `Production` |
| `GOOGLE_PACKAGE_NAME` | `com.beebase.production` | Expected Android package name |
| `GOOGLE_SERVICE_ACCOUNT_JSON` | *(unset)* | Service account credentials JSON for Google Play Developer API |
| `TEST_DATABASE_URL` | *(unset)* | Used only by integration tests, never by the app |

## Project structure

```
cmd/server/                       entry point: wires config, logger, db, redis/sessions, verifiers, services, server
api/openapi.yaml                  API contract
migrations/                       SQL migrations (golang-migrate format)
internal/
  domain/subscription/            Subscription entity, lifecycle Status state machine, Repository + Event ports; no infrastructure dependency
  application/subscription/       use cases: GetSubscription, VerifyPurchase, RestorePurchases
  platform/
    apple/                        StoreKit 2 JWS receipt & notification verifier, Apple root CA chain validation
    google/                       Google Play Developer API client (AndroidPublisher)
    postgres/                     pgx connection pool
  repository/postgres/            domain repository & idempotency event store implemented against PostgreSQL (pgx, explicit SQL)
  transport/http/                 chi router, health/ready handlers, request logging
    subscription/                 authenticated subscription HTTP handlers (/api/v1/subscription)
```

logger, JSON response/error helpers, the graceful-shutdown server wrapper,
Redis session store, and JWKS-based access-token verification (`RequireAuth`
middleware) all come from [beebase-common](https://github.com/sbezhuk/beebase-common),
shared by every BeeBase service.

## Entitlements and Store Verification

### Entitlement model

BeeBase subscription status resolves to one of two entitlement tiers:

- **`free`**: Default for all registered users without an active subscription.
- **`pro`**: Granted to users who hold an active subscription (`beebase_pro_monthly`, `beebase_pro_yearly`, or `beebase_pro`).

Entitlement is calculated dynamically via `HasActiveAccess()`: a subscription is considered active if its status is `active`, `grace_period`, or `billing_retry`, or if it is `cancelled` but its `expires_at` timestamp is still in the future.

### Verification flow

1. **Apple StoreKit 2 (`POST /api/v1/subscription/verify`)**:
   - The iOS client sends a `signed_transaction` JWS string obtained from StoreKit 2.
   - The server cryptographically validates the JWS certificate chain against Apple Root CA certificates.
   - Validates that the bundle ID matches `APPLE_BUNDLE_ID` and the transaction environment matches `APPLE_ENVIRONMENT`.
   - Uses the trusted `originalTransactionId` to reconcile with Apple's App Store Server API using a short-lived ES256 JWT signed by `APPLE_PRIVATE_KEY`.
   - Verifies the API response's signed transaction data with Apple's public certificate chain, then records the authoritative subscription state.
   - If Apple is temporarily unavailable, the locally verified transaction is retained; authentication, environment, and malformed-response failures are rejected.

2. **Google Play Billing (`POST /api/v1/subscription/verify`)**:
   - The Android client sends a `purchase_token` and `subscription_id`.
   - The server validates the token against the Google Play Developer API (AndroidPublisher API v3) using the service account credentials.
   - Checks package name and subscription status, then updates the user's subscription record.

3. **Restore Purchases (`POST /api/v1/subscription/restore`)**:
   - Allows users to re-link an existing StoreKit 2 or Google Play purchase to their BeeBase account upon reinstallation or device transfer.

### Reconciliation

The currently exposed API is request-driven: authenticated verify and restore
calls validate the submitted purchase with Apple or Google and persist the
authoritative subscription state. Store webhook/RTDN delivery is not exposed
by the current HTTP router.

## Ownership

Every subscription belongs to exactly one user (the `sub` claim of their verified access token). Authenticated endpoints (`GET /api/v1/subscription`, `POST /api/v1/subscription/verify`, `POST /api/v1/subscription/restore`) strictly scope all database queries and updates to the caller's `user_id`. There is no code path allowing a user to inspect or claim another user's subscription.

## Development

```bash
make run                # go run ./cmd/server
make fmt                # go fmt ./...
make vet                # go vet ./...
make test               # unit tests: go test ./... -v
make lint               # golangci-lint run
make tidy               # go mod tidy

make migrate-up         # apply migrations to DATABASE_URL
make migrate-down       # roll back the last migration
make migrate-new name=add_something   # scaffold a new migration pair

make build              # build binary into bin/
make docker-up          # start postgres + app via docker-compose
make docker-down        # stop docker-compose services
```

### Integration tests

Integration tests exercise the PostgreSQL repository and transaction handling against a real PostgreSQL database. They require `TEST_DATABASE_URL` to be set and are skipped automatically if unset:

```bash
docker compose up -d postgres
createdb -h localhost -p 5432 -U beebase beebase_subscription_test
migrate -path migrations -database "$TEST_DATABASE_URL" up

TEST_DATABASE_URL=postgres://beebase:beebase@localhost:5432/beebase_subscription_test?sslmode=disable \
  go test -tags=integration ./internal/repository/postgres/...
```
