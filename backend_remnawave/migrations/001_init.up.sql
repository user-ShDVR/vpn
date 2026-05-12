CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Users: balance, email verification, password reset, referrals.
-- Remnawave fields hold the panel-side identifier + cached subscription URL.
CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    referral_code TEXT UNIQUE,
    balance_kopecks BIGINT NOT NULL DEFAULT 0,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    email_verify_token TEXT,
    email_verify_expires_at TIMESTAMPTZ,
    password_reset_token TEXT,
    password_reset_expires_at TIMESTAMPTZ,
    remnawave_uuid UUID,
    remnawave_short_uuid TEXT,
    subscription_url TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_users_remnawave_uuid ON users(remnawave_uuid);
CREATE INDEX idx_users_email_verify_token ON users(email_verify_token) WHERE email_verify_token IS NOT NULL;
CREATE INDEX idx_users_password_reset_token ON users(password_reset_token) WHERE password_reset_token IS NOT NULL;

-- Plans. squad_uuids: which Remnawave internal squads the plan grants access to.
-- server_count is kept for display copy ("N servers") but is derived from squad_uuids length in practice.
CREATE TABLE plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    duration_days INT NOT NULL,
    traffic_limit_gb INT,
    max_devices INT NOT NULL DEFAULT 1,
    cost_kopecks BIGINT NOT NULL DEFAULT 0,
    server_count INT NOT NULL DEFAULT 1,
    squad_uuids UUID[] NOT NULL DEFAULT ARRAY[]::UUID[],
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_id UUID NOT NULL REFERENCES plans(id),
    starts_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_subscriptions_user_id ON subscriptions(user_id);
CREATE INDEX idx_subscriptions_expires_at ON subscriptions(expires_at) WHERE is_active = TRUE;

-- Servers map "location" rows to Remnawave internal squad UUIDs.
-- No 3x-ui credentials; Remnawave is one panel.
CREATE TABLE servers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    remnawave_squad_uuid UUID NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('entry', 'exit')),
    country TEXT NOT NULL DEFAULT '',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE referrals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    referrer_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    referred_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bonus_days INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(referred_id)
);
CREATE INDEX idx_referrals_referrer ON referrals(referrer_id);

-- Balance ledger.
CREATE TABLE transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount_kopecks BIGINT NOT NULL,
    type TEXT NOT NULL,
    description TEXT,
    related_subscription_id UUID REFERENCES subscriptions(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_transactions_user ON transactions(user_id, created_at DESC);

-- Payments. provider='platega', bill_id = Platega transactionId, custom = our payload (= payments.id).
CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    bill_id TEXT NOT NULL DEFAULT '',
    amount_kopecks BIGINT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'RUB',
    status TEXT NOT NULL DEFAULT 'pending',
    link_url TEXT,
    pay_url TEXT,
    custom TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    paid_at TIMESTAMPTZ
);
CREATE INDEX idx_payments_user ON payments(user_id, created_at DESC);
CREATE INDEX idx_payments_pending ON payments(status, created_at) WHERE status = 'pending';

-- Promo codes.
CREATE TABLE promo_codes (
    code TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('discount_percent', 'discount_kopecks', 'bonus_days')),
    value BIGINT NOT NULL,
    max_uses INT,
    used_count INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE promo_redemptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code TEXT NOT NULL REFERENCES promo_codes(code),
    subscription_id UUID REFERENCES subscriptions(id),
    discount_kopecks BIGINT NOT NULL DEFAULT 0,
    bonus_days INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, code)
);

-- Seed initial paid plans. squad_uuids stay empty; admin fills them post-deploy
-- once Remnawave squads exist (via /admin/servers + plan edit UI).
INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, server_count, description) VALUES
    ('Месяц', 30,  NULL, 3, 19900,  1, 'Безлимитный трафик, до 3 устройств'),
    ('3 месяца', 90,  NULL, 3, 49900,  1, 'Безлимитный трафик, до 3 устройств. Экономия 16%.'),
    ('Год', 365, NULL, 5, 169900, 2, 'Безлимитный трафик, до 5 устройств, 2 локации. Экономия 29%.');
