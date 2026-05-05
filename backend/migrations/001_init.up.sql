CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE plans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    duration_days INT NOT NULL,
    traffic_limit_gb INT,
    max_devices INT DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    plan_id UUID REFERENCES plans(id),
    starts_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    is_active BOOL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE servers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    panel_url TEXT NOT NULL,
    panel_user TEXT NOT NULL,
    panel_pass TEXT NOT NULL,
    inbound_id INT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('entry', 'exit')),
    host TEXT NOT NULL,
    port INT NOT NULL,
    sub_url TEXT NOT NULL DEFAULT '',
    sub_path TEXT NOT NULL DEFAULT '/sub/',
    is_active BOOL DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE server_clients (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    server_id UUID REFERENCES servers(id),
    client_uuid UUID NOT NULL,
    xray_email TEXT NOT NULL,
    sub_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, server_id)
);

CREATE INDEX idx_subscriptions_user_id ON subscriptions(user_id);
CREATE INDEX idx_subscriptions_expires_at ON subscriptions(expires_at) WHERE is_active = TRUE;
CREATE INDEX idx_server_clients_user_id ON server_clients(user_id);

INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices)
VALUES ('Free', 30, 200, 5);
