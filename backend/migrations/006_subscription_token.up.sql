ALTER TABLE users ADD COLUMN subscription_token TEXT UNIQUE;
UPDATE users SET subscription_token = encode(gen_random_bytes(24), 'hex') WHERE subscription_token IS NULL;
ALTER TABLE users ALTER COLUMN subscription_token SET NOT NULL;
CREATE INDEX idx_users_subscription_token ON users(subscription_token);
