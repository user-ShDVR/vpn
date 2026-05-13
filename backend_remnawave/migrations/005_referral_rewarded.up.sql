-- Defer referrer bonus until the referred user actually pays. Tracks when
-- (if ever) the referrer got their +N days, so each referral only rewards
-- once. NULL = pending.
ALTER TABLE referrals ADD COLUMN rewarded_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_referrals_pending ON referrals(referred_id) WHERE rewarded_at IS NULL;
