-- Seed a free 1-day trial plan. Granted after email verification:
-- 1 day for direct sign-ups, 3 days when the user came via a referral
-- (extended by +2 days in code on verify). Cost 0 means
-- ActivateFreePlanIfNone picks this over the paid plans.
INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, server_count, description)
SELECT 'Тест-драйв', 1, 5, 2, 0, 1, 'Пробный день — подтвердите email и подключайтесь'
WHERE NOT EXISTS (SELECT 1 FROM plans WHERE cost_kopecks = 0);
