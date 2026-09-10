DROP TABLE IF EXISTS subscription_events;

DROP INDEX IF EXISTS idx_subscriptions_provider_purchase_token;
DROP INDEX IF EXISTS idx_subscriptions_provider_orig_trans_id;

ALTER TABLE subscriptions
    DROP CONSTRAINT IF EXISTS chk_subscriptions_status,
    DROP CONSTRAINT IF EXISTS chk_subscriptions_environment,
    DROP CONSTRAINT IF EXISTS chk_subscriptions_provider,
    DROP COLUMN IF EXISTS last_event_at,
    DROP COLUMN IF EXISTS cancelled_at,
    DROP COLUMN IF EXISTS auto_renew,
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS environment,
    DROP COLUMN IF EXISTS purchase_token,
    DROP COLUMN IF EXISTS transaction_id,
    DROP COLUMN IF EXISTS original_transaction_id,
    DROP COLUMN IF EXISTS product_id,
    DROP COLUMN IF EXISTS provider;
