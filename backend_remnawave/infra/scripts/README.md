# infra/scripts

## install-remnawave.sh

Wrapper around [Capybara-z/RemnaSetup](https://github.com/Capybara-z/RemnaSetup).
RemnaSetup is interactive: it installs the Remnawave panel + Postgres +
subscription page + Caddy/Nginx via docker-compose at `/opt/remnawave/` (or
the node-only flavor at `/opt/remnanode/`).

```bash
./install-remnawave.sh root@panel.example panel
./install-remnawave.sh root@node-ru.example node
```

The installer prompts for: `DOMAIN`, `MONITOR_PORT` (default 8443), `NODE_PORT`
(default 3001), `SECRET_KEY` (shared panel↔node), web server choice, optional
WARP/BBR. The shared `SECRET_KEY` is used between panel and node — your Go
backend talks only to the panel via `REMNAWAVE_BASE_URL`.

## After installation

1. **Generate an API token.** In the panel UI, Settings → API → create a token.
   Copy it to the backend's `REMNAWAVE_TOKEN` env var.
2. **Add nodes** in the panel UI. They auto-connect using `SECRET_KEY`.
3. **Create internal squads** in the panel. Each squad = one or more inbounds
   on one or more nodes. The squad UUID is what plans reference.
4. **Wire the СвязьОК admin UI** (`/admin/servers`) — add one row per squad.
   The `remnawave_squad_uuid` column is the link.

## Cascade routing (RU → foreign)

Remnawave does not have a "cascade" toggle. The RU node's xray config has to
outbound onto the foreign node:

- On the RU node: create an inbound (e.g. VLESS+REALITY) for clients to connect
  to. In its xray config, route foreign traffic through an `outbound` of type
  `vless` pointing at the foreign node.
- On the foreign node: a vanilla VLESS inbound that the RU node connects out to.
- Group both nodes' inbounds into a single internal squad. Users assigned to
  this squad get the cascaded path.

For direct foreign access (no cascade), use a separate squad whose inbound
points only at the foreign node.
