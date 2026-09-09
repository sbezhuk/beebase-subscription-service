# beebase-subscription

Subscription service for BeeBase with dedicated PostgreSQL storage.

## Endpoints

- `GET /health`: Liveness probe
- `GET /ready`: Readiness probe (pings database)
- `GET /test`: Test route (verifies database connectivity)
- `GET /api/v1/subscription/test`: Test route alias
- `GET /api/v1/subscriptions/test`: Test route alias

## Development

```bash
# Run PostgreSQL locally
docker compose up -d postgres migrate

# Run service
make run
```
