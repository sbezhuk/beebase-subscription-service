CREATE TABLE subscriptions (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL,
    status      VARCHAR(50) NOT NULL DEFAULT 'inactive',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_subscriptions_user ON subscriptions (user_id);
