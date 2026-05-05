DROP INDEX IF EXISTS idx_transactions_user_created;
DROP TABLE IF EXISTS transactions;
ALTER TABLE plans DROP COLUMN IF EXISTS description;
ALTER TABLE plans DROP COLUMN IF EXISTS cost_kopecks;
ALTER TABLE users DROP COLUMN IF EXISTS balance_kopecks;
