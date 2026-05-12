-- Extra GB pricing: 79 ₽ per 10 GB = 7.9 ₽ per 1 GB = 790 kopecks.
-- Stored per-1GB; UI sells in 10-GB increments.
UPDATE plans SET extra_gb_price_kopecks = 790 WHERE extra_gb_price_kopecks = 7900;
