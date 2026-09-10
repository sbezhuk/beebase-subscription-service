-- Extend subscriptions table with provider-specific details.
ALTER TABLE subscriptions
    ADD COLUMN provider VARCHAR(20) NOT NULL,
    ADD COLUMN product_id VARCHAR(100) NOT NULL,
    ADD COLUMN original_transaction_id VARCHAR(255),
    ADD COLUMN transaction_id VARCHAR(255),
    ADD COLUMN purchase_token TEXT,
    ADD COLUMN environment VARCHAR(20),
    ADD COLUMN expires_at TIMESTAMPTZ,
    ADD COLUMN auto_renew BOOLEAN,
    ADD COLUMN cancelled_at TIMESTAMPTZ,
    ADD COLUMN last_event_at TIMESTAMPTZ;

-- Constrain provider, environment, and status to allowed domain values.
ALTER TABLE subscriptions
    ADD CONSTRAINT chk_subscriptions_provider CHECK (provider IN ('apple', 'google')),
    ADD CONSTRAINT chk_subscriptions_environment CHECK (environment IS NULL OR environment IN ('sandbox', 'production')),
    ADD CONSTRAINT chk_subscriptions_status CHECK (status IN ('inactive', 'active', 'grace_period', 'billing_retry', 'expired', 'cancelled', 'revoked'));

-- Lookup indexes for provider-specific purchase identifiers.
-- Partial unique indexes ensure efficient lookup while allowing multiple null values
-- so legitimate Apple and Google subscriptions coexist seamlessly.
CREATE UNIQUE INDEX idx_subscriptions_provider_orig_trans_id
    ON subscriptions (provider, original_transaction_id)
    WHERE original_transaction_id IS NOT NULL;

CREATE UNIQUE INDEX idx_subscriptions_provider_purchase_token
    ON subscriptions (provider, purchase_token)
    WHERE purchase_token IS NOT NULL;

-- Webhook event idempotency table.
-- Stores incoming notification events from Apple/Google to prevent duplicate processing.
CREATE TABLE subscription_events (
    id           UUID PRIMARY KEY,
    provider     VARCHAR(20) NOT NULL,
    event_id     VARCHAR(255) NOT NULL,
    event_type   VARCHAR(100) NOT NULL,
    payload      JSONB NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_subscription_events_provider_event UNIQUE (provider, event_id),
    CONSTRAINT chk_subscription_events_provider CHECK (provider IN ('apple', 'google'))
);

CREATE INDEX idx_subscription_events_processed_at ON subscription_events (processed_at);
