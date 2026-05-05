-- Seed sample paid plans (idempotent: skip if name already exists)
INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, description)
SELECT 'Базовый', 30, NULL, 2, 49000, E'• Безлимит трафика\n• 2 устройства\n• Стандартная скорость'
WHERE NOT EXISTS (SELECT 1 FROM plans WHERE name = 'Базовый');

INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, description)
SELECT 'Премиум', 30, NULL, 5, 69000, E'• Безлимит трафика\n• 5 устройств\n• Приоритетный канал\n• Все локации'
WHERE NOT EXISTS (SELECT 1 FROM plans WHERE name = 'Премиум');

INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, description)
SELECT 'Семейный', 30, NULL, 8, 99000, E'• Безлимит трафика\n• 8 устройств\n• Все локации\n• Поддержка 24/7'
WHERE NOT EXISTS (SELECT 1 FROM plans WHERE name = 'Семейный');

INSERT INTO plans (name, duration_days, traffic_limit_gb, max_devices, cost_kopecks, description)
SELECT 'Премиум год', 365, NULL, 5, 745000, E'• Безлимит трафика\n• 5 устройств\n• -10% от месячной цены\n• Все локации'
WHERE NOT EXISTS (SELECT 1 FROM plans WHERE name = 'Премиум год');
