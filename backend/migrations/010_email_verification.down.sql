DROP INDEX IF EXISTS idx_users_email_verify_token;
ALTER TABLE users DROP COLUMN IF EXISTS email_verify_expires_at;
ALTER TABLE users DROP COLUMN IF EXISTS email_verify_token;
ALTER TABLE users DROP COLUMN IF EXISTS email_verified;
