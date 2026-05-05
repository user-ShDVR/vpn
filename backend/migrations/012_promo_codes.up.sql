CREATE TABLE promo_codes (
    code TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('discount_percent','discount_kopecks','bonus_days')),
    value BIGINT NOT NULL,
    max_uses INT,
    used_count INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ,
    is_active BOOL NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE promo_redemptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code TEXT NOT NULL REFERENCES promo_codes(code),
    subscription_id UUID REFERENCES subscriptions(id) ON DELETE SET NULL,
    discount_kopecks BIGINT NOT NULL DEFAULT 0,
    bonus_days INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, code)
);

CREATE INDEX idx_promo_redemptions_user ON promo_redemptions(user_id);
