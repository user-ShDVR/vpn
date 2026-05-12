DROP TABLE IF EXISTS extra_gb_purchases;
DELETE FROM plans WHERE name IN ('Суточный', 'Базовый', 'Стандарт', 'Премиум', 'Максимум') AND NOT deprecated;
UPDATE plans SET deprecated = false WHERE name IN ('Месяц', '3 месяца', 'Год');
ALTER TABLE plans DROP COLUMN IF EXISTS deprecated;
ALTER TABLE plans DROP COLUMN IF EXISTS extra_gb_price_kopecks;
ALTER TABLE plans DROP COLUMN IF EXISTS reset_strategy;
ALTER TABLE plans DROP COLUMN IF EXISTS icon;
