ALTER TABLE users ADD COLUMN balance_kopecks BIGINT NOT NULL DEFAULT 0;

ALTER TABLE plans ADD COLUMN cost_kopecks BIGINT NOT NULL DEFAULT 0;
ALTER TABLE plans ADD COLUMN description TEXT NOT NULL DEFAULT '';

CREATE TABLE transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount_kopecks BIGINT NOT NULL,
    type TEXT NOT NULL,
    description TEXT,
    related_subscription_id UUID REFERENCES subscriptions(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_transactions_user_created ON transactions(user_id, created_at DESC);
