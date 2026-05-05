ALTER TABLE users ADD COLUMN email_verified BOOL NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN email_verify_token TEXT;
ALTER TABLE users ADD COLUMN email_verify_expires_at TIMESTAMPTZ;

-- Grandfather pre-existing users (created before this migration) — they pre-date
-- the verification flow and shouldn't be locked out.
UPDATE users SET email_verified = TRUE WHERE created_at < NOW();

CREATE INDEX idx_users_email_verify_token ON users(email_verify_token) WHERE email_verify_token IS NOT NULL;
