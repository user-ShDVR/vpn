DROP INDEX IF EXISTS idx_referrals_pending;
ALTER TABLE referrals DROP COLUMN IF EXISTS rewarded_at;
