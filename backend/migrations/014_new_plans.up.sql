-- Add server_count to plans and reseed with new tariff lineup.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS server_count INT NOT NULL DEFAULT 1;

-- Wipe legacy plans not in active subscriptions; deactivate stale subs first
-- so FK doesn't bite us. Existing active subs stay tied to their old plan_id
-- (orphan plan rows kept).
DELETE FROM plans
WHERE name IN ('Базовый', 'Премиум', 'Семейный', 'Премиум год')
  AND id NOT IN (SELECT DISTINCT plan_id FROM subscriptions WHERE is_active);

-- Seed new lineup. ON CONFLICT skipped — assumes unique name. If clash, skip.
INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, server_count, description)
VALUES
  ('Базовый',         30,  NULL, 3, 20000,  1, 'Безлимитный трафик, 3 устройства, 1 локация'),
  ('Премиум',         30,  NULL, 6, 35000,  2, 'Безлимитный трафик, 6 устройств, 2 локации'),
  ('Базовый годовой', 365, NULL, 3, 200000, 1, 'Базовый на 12 месяцев — выгода 17%'),
  ('Премиум годовой', 365, NULL, 6, 350000, 2, 'Премиум на 12 месяцев — выгода 17%')
ON CONFLICT DO NOTHING;
