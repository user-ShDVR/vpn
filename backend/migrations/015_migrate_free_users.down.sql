-- Reversal recreates a placeholder Free row but cannot restore prior plan_id
-- linkage on subscriptions that were migrated. Best-effort.
INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, server_count, description)
SELECT 'Free', 3, 5, 5, 0, 1, 'Бесплатный тариф'
WHERE NOT EXISTS (SELECT 1 FROM plans WHERE name = 'Free');
