ALTER TABLE servers ADD COLUMN max_clients INT NOT NULL DEFAULT 200;
ALTER TABLE servers ADD COLUMN client_count INT NOT NULL DEFAULT 0;

-- Sync client_count from existing server_clients
UPDATE servers SET client_count = (
    SELECT COUNT(*) FROM server_clients WHERE server_clients.server_id = servers.id
);
