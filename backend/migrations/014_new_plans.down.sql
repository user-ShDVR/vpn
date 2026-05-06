DELETE FROM plans WHERE name IN ('Базовый', 'Премиум', 'Базовый годовой', 'Премиум годовой');
ALTER TABLE plans DROP COLUMN IF EXISTS server_count;
