-- Pricing v2: 5-tier scheme (Суточный/Базовый/Стандарт/Премиум/Максимум).
-- Adds icon, reset_strategy, extra_gb_price_kopecks, deprecated columns.
-- Old plans (Месяц/3 месяца/Год) marked deprecated — kept for FK from
-- existing subscriptions but filtered out of /subscription/purchase.

ALTER TABLE plans ADD COLUMN icon TEXT NOT NULL DEFAULT '';
ALTER TABLE plans ADD COLUMN reset_strategy TEXT NOT NULL DEFAULT 'NO_RESET';
ALTER TABLE plans ADD COLUMN extra_gb_price_kopecks BIGINT NOT NULL DEFAULT 0;
ALTER TABLE plans ADD COLUMN deprecated BOOLEAN NOT NULL DEFAULT false;

UPDATE plans SET deprecated = true WHERE name IN ('Месяц', '3 месяца', 'Год');

INSERT INTO plans (name, icon, duration_days, traffic_limit_gb, max_devices, cost_kopecks, reset_strategy, extra_gb_price_kopecks, server_count, description)
SELECT v.name, v.icon, v.duration_days, v.traffic_limit_gb, v.max_devices, v.cost_kopecks, v.reset_strategy, v.extra_gb_price_kopecks, v.server_count, v.description
FROM (VALUES
  ('Суточный', '📅', 1,  3,   1, 1500::bigint,   'DAY',   7900::bigint,  1,
   'Безлимитный трафик на стандартных VPN-серверах. Трафик серверов мобильного обхода обновляется ежедневно — 3 ГБ в сутки. Подключение 1 устройства.'),
  ('Базовый',  '◆',  30, 20,  1, 15900::bigint,  'MONTH', 7900::bigint,  1,
   'Безлимитный трафик на стандартных VPN-серверах. Трафик серверов мобильного обхода учитывается отдельно — 20 ГБ включено в тариф. Подключение 1 устройства. Доп. трафик на 30 дней — от 79 ₽.'),
  ('Стандарт', '⚡', 30, 50,  2, 27900::bigint,  'MONTH', 7900::bigint,  1,
   'Безлимитный трафик на стандартных VPN-серверах. Трафик серверов мобильного обхода учитывается отдельно — 50 ГБ включено в тариф. Подключение до 2 устройств. Доп. трафик на 30 дней — от 79 ₽.'),
  ('Премиум',  '💎', 30, 100, 3, 44900::bigint,  'MONTH', 7900::bigint,  1,
   'Безлимитный трафик на стандартных VPN-серверах. Трафик серверов мобильного обхода учитывается отдельно — 100 ГБ включено в тариф. Подключение до 3 устройств. Доп. трафик на 30 дней — от 79 ₽.'),
  ('Максимум', '👑', 30, 250, 10, 113400::bigint, 'MONTH', 7900::bigint,  2,
   'Безлимитный трафик на стандартных VPN-серверах. Трафик серверов мобильного обхода учитывается отдельно — 250 ГБ включено в тариф. Подключение до 5 устройств. Доп. трафик на 30 дней — от 79 ₽.')
) AS v(name, icon, duration_days, traffic_limit_gb, max_devices, cost_kopecks, reset_strategy, extra_gb_price_kopecks, server_count, description)
WHERE NOT EXISTS (SELECT 1 FROM plans p WHERE p.name = v.name AND NOT p.deprecated);

-- Track extra-GB top-ups for analytics + reconciliation.
CREATE TABLE IF NOT EXISTS extra_gb_purchases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subscription_id UUID REFERENCES subscriptions(id) ON DELETE SET NULL,
    gb INT NOT NULL,
    cost_kopecks BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_extra_gb_user ON extra_gb_purchases(user_id, created_at DESC);
