# Server deployment scripts

Provision a fresh Ubuntu/Debian VPS with 3x-ui + VLESS-REALITY inbound and register it in the СвязьOK backend.

## Prerequisites

**Local:** `ssh`, `scp`, `jq`, `curl`. SSH key access to the target server (root or sudo).

**Target:** clean Ubuntu 20.04+ / Debian 11+ VPS. Public IP. Ports 2053 (panel), 2096 (sub), 443 (inbound) open.

**Backend:** running with admin user. Get an admin JWT (`POST /api/v1/auth/login` with admin creds → token).

## Usage

```bash
SERVER_NAME="ru-msk-1" \
SERVER_HOST="1.2.3.4" \
SERVER_TYPE="entry" \
BACKEND_URL="https://api.cascade.example" \
ADMIN_TOKEN="<admin_jwt>" \
./deploy-server.sh
```

Optional overrides (defaults):
- `SSH_USER=root`, `SSH_PORT=22`
- `PANEL_PORT=2053`, `PANEL_SUB_PORT=2096`, `INBOUND_PORT=443`
- `REALITY_DEST=learn.microsoft.com:443`, `REALITY_SNI=learn.microsoft.com`
- `PANEL_USER`, `PANEL_PASS` (auto-generated if not set)

## What it does

1. SCPs `remote-setup.sh` to the target.
2. Runs the remote installer:
   - `apt install curl jq`
   - 3x-ui (MHSanaei v2.x) install via official `install.sh user pass port`
   - Enables subscription service on `PANEL_SUB_PORT` with random path
   - Generates REALITY x25519 keypair via `/server/getNewX25519Cert`
   - Creates VLESS+REALITY+xhttp inbound with random shortId + xhttp path
3. Registers the server in backend via `POST /api/v1/admin/servers`.

## Output

Prints panel URL, credentials, inbound ID, REALITY public key, shortId. **Save these securely** — they are not stored anywhere except 3x-ui DB and our backend's `servers` table.

## Cascade mode (entry → exit)

If `SERVER_TYPE=entry` and you set all `EXIT_*` env vars, the script writes a
`cascade-out` VLESS+REALITY outbound into the entry server's Xray config and
routes its inbound through the foreign exit:

```bash
EXIT_HOST=foreign.example.com \
EXIT_PORT=443 \
EXIT_UUID=<uuid-on-exit> \
EXIT_PUBKEY=<exit-reality-pubkey> \
EXIT_SHORT_ID=<exit-reality-shortid> \
EXIT_SNI=<exit-reality-sni> \
SERVER_NAME=ru-msk-1 SERVER_HOST=1.2.3.4 SERVER_TYPE=entry \
BACKEND_URL=https://api.example.com ADMIN_TOKEN=<jwt> \
./deploy-server.sh
```

The default routing forwards **everything** through `cascade-out`. If you want
RU domains to bypass the exit (direct from the entry), add a `geosite:category-ru`
rule with `outboundTag: "direct"` in the Xray config manually. The script does
not currently set this rule — open issue if you need it automated.

## Verifying deeplink schemes (Happ / V2RayTun / Hiddify)

The cabinet's `/subscriptions` page renders three "Open in client" buttons:
`hiddify://install-config?url=...`, `v2raytun://import/...`, `happ://add/...`.

Hiddify's deeplink is documented and reliable. V2RayTun and Happ change their
URI schemes occasionally — the buttons in the cabinet are best-guess and may
fail silently. Always show users the "Copy URL" fallback (already present).

To verify:

1. Install latest Happ / V2RayTun / Hiddify on a test device (Android+iOS).
2. From the cabinet, tap each deeplink button.
3. Expected: app opens, prompts to import the subscription.
4. If a button fails: open the app and use "Copy URL" → "Add subscription"
   manually to confirm the URL itself is correct.
5. Update the deeplink format in `web/templates/subscriptions.templ` if the
   verified scheme differs.

## Limitations
- `INBOUND_PORT=443` collides with anything else on port 443. Use `INBOUND_PORT=8443` if HAProxy/Nginx already on 443.
- The script uses `https://api.ipify.org` to detect the server's public IP for the subscription URL. If that's blocked, set `SUB_URL_OVERRIDE=http://your.ip:2096` (TODO: not yet wired).
- 3x-ui versions before v2.x have a different `setting/update` payload — script targets v2.x only.
