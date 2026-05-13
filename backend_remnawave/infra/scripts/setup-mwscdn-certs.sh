#!/usr/bin/env bash
#
# Bootstraps a Remnawave node behind MWS CDN with a Cloudflare-DNS-01 cert.
# End-to-end: installs Docker (if missing), the remnanode container, issues
# the LE cert via DNS-01 (no port 80 needed), mounts it into the node, sets
# up auto-renewal.
#
# Steps:
#   1. Install Docker + docker compose plugin if missing
#   2. Install remnanode at /opt/remnanode if missing (needs SECRET_KEY env)
#   3. Install Certbot docker-compose stub at /opt/certbot
#   4. Write Cloudflare credentials INI (chmod 600)
#   5. Run certbot --dns-cloudflare for the domain
#   6. Patch /opt/remnanode/docker-compose.yml to mount /opt/certbot/certs:ro
#   7. Restart the node
#   8. Install weekly renew cron
#
# Run on the *node* host (not on the panel host) as root.
#
# Usage:
#   setup-mwscdn-certs.sh <domain> <le-email>
#
# Environment:
#   # Cloudflare creds (one of):
#   CF_API_TOKEN=<scoped-token>                          # preferred
#   CF_API_EMAIL=<account-email> CF_API_KEY=<global-key> # or this pair
#
#   # Remnawave node bootstrap (only if /opt/remnanode missing):
#   SECRET_KEY=<panel-secret-key-blob>                   # base64 JSON from
#                                                        # panel → Nodes → Add
#   NODE_PORT=3001                                       # default 3001
#
#   # XHTTP_mws inbound generation:
#   INBOUND_PORT=8447                                    # default 8447
#   XHTTP_PATH=/xhttppath                                # default /xhttppath
#
# After cert issue the script writes /opt/remnanode/xhttp_mws.json — a
# ready-to-paste inbound block for Remnawave panel → Profiles → Add Inbound
# → JSON. Domain + cert paths already substituted.
#
# Token scope: Zone → DNS → Edit  (+ Zone → Zone → Read) for the target zone.
#
# Re-runs idempotent — cert reuse, compose patch guard, cron de-dupe.

set -euo pipefail

DOMAIN="${1:-}"
EMAIL="${2:-}"
CERTBOT_DIR="/opt/certbot"
NODE_DIR="/opt/remnanode"
NODE_COMPOSE="${NODE_DIR}/docker-compose.yml"
NODE_ENV="${NODE_DIR}/.env"
CREDS_FILE="${CERTBOT_DIR}/cloudflare.ini"

die() { echo "ERROR: $*" >&2; exit 1; }
note() { echo "==> $*"; }

[[ -n "$DOMAIN" && -n "$EMAIL" ]] || die "usage: $0 <domain> <le-email>
  env: CF_API_TOKEN=...  OR  CF_API_EMAIL=... CF_API_KEY=...
  if /opt/remnanode missing: SECRET_KEY=<panel-blob> [NODE_PORT=3001]"
[[ "$EUID" -eq 0 ]] || die "run as root (sudo)"

CF_API_TOKEN="${CF_API_TOKEN:-}"
CF_API_EMAIL="${CF_API_EMAIL:-}"
CF_API_KEY="${CF_API_KEY:-}"
if [[ -z "$CF_API_TOKEN" && ( -z "$CF_API_EMAIL" || -z "$CF_API_KEY" ) ]]; then
  die "set CF_API_TOKEN or both CF_API_EMAIL + CF_API_KEY"
fi

# ---- 1. Docker + compose plugin ----
if ! command -v docker >/dev/null; then
  note "installing docker engine via get.docker.com"
  curl -fsSL https://get.docker.com | sh
fi
if ! docker compose version >/dev/null 2>&1; then
  die "docker compose plugin missing — install docker-compose-plugin (apt/yum)"
fi
systemctl enable --now docker >/dev/null 2>&1 || true

# ---- 2. remnanode (only if /opt/remnanode missing) ----
if [[ ! -d "$NODE_DIR" ]]; then
  : "${SECRET_KEY:?missing SECRET_KEY env — copy the blob from Remnawave panel → Nodes → Add (the eyJ... base64 string after SECRET_KEY=)}"
  NODE_PORT="${NODE_PORT:-3001}"
  note "installing remnanode at $NODE_DIR (NODE_PORT=$NODE_PORT)"
  mkdir -p "$NODE_DIR"
  cat > "$NODE_COMPOSE" <<'YAML'
services:
  remnanode:
    container_name: remnanode
    hostname: remnanode
    image: remnawave/node:latest
    network_mode: host
    restart: always
    cap_add:
      - NET_ADMIN
    ulimits:
      nofile:
        soft: 1048576
        hard: 1048576
    env_file:
      - .env
YAML
  # Strip optional surrounding quotes — panel renders the blob inside ""
  # but env_file expects raw value without them.
  cleaned_key="${SECRET_KEY%\"}"; cleaned_key="${cleaned_key#\"}"
  cat > "$NODE_ENV" <<EOF
NODE_PORT=$NODE_PORT
SECRET_KEY="$cleaned_key"
EOF
  chmod 600 "$NODE_ENV"
  (cd "$NODE_DIR" && docker compose pull && docker compose up -d)
else
  note "remnanode already installed at $NODE_DIR"
fi
[[ -f "$NODE_COMPOSE" ]] || die "$NODE_COMPOSE missing after install"

# ---- 3. Certbot stub ----
note "preparing $CERTBOT_DIR"
mkdir -p "$CERTBOT_DIR"
cat > "$CERTBOT_DIR/docker-compose.yml" <<'YAML'
services:
  certbot:
    container_name: certbot
    image: certbot/dns-cloudflare
    volumes:
      - ./certs:/etc/letsencrypt
      - ./var-lib-letsencrypt:/var/lib/letsencrypt
      - ./cloudflare.ini:/cloudflare.ini:ro
YAML

# ---- 4. Cloudflare credentials INI ----
note "writing $CREDS_FILE (chmod 600)"
if [[ -n "$CF_API_TOKEN" ]]; then
  cat > "$CREDS_FILE" <<EOF
dns_cloudflare_api_token = $CF_API_TOKEN
EOF
else
  cat > "$CREDS_FILE" <<EOF
dns_cloudflare_email = $CF_API_EMAIL
dns_cloudflare_api_key = $CF_API_KEY
EOF
fi
chmod 600 "$CREDS_FILE"

# ---- 5. Issue cert (skip if already exists) ----
CERT_PATH="$CERTBOT_DIR/certs/live/$DOMAIN/fullchain.pem"
if [[ -f "$CERT_PATH" ]]; then
  note "cert already exists at $CERT_PATH — skipping issue"
else
  note "requesting cert for $DOMAIN via Cloudflare DNS-01"
  docker run --rm \
    -v "$CERTBOT_DIR/certs:/etc/letsencrypt" \
    -v "$CERTBOT_DIR/var-lib-letsencrypt:/var/lib/letsencrypt" \
    -v "$CREDS_FILE:/cloudflare.ini:ro" \
    certbot/dns-cloudflare certonly \
    --dns-cloudflare \
    --dns-cloudflare-credentials /cloudflare.ini \
    --dns-cloudflare-propagation-seconds 30 \
    --non-interactive --agree-tos \
    --email "$EMAIL" \
    -d "$DOMAIN"
  [[ -f "$CERT_PATH" ]] || die "certbot did not produce $CERT_PATH"
fi

# ---- 6. Patch node compose to mount certs ----
VOL_LINE="      - '/opt/certbot/certs:/etc/letsencrypt:ro'"
if grep -qF "/opt/certbot/certs:/etc/letsencrypt" "$NODE_COMPOSE"; then
  note "node compose already mounts certs — skipping patch"
else
  note "backing up $NODE_COMPOSE → ${NODE_COMPOSE}.bak.$(date +%s)"
  cp "$NODE_COMPOSE" "${NODE_COMPOSE}.bak.$(date +%s)"

  note "patching $NODE_COMPOSE (adding cert volume)"
  python3 - "$NODE_COMPOSE" "$VOL_LINE" <<'PY'
import sys, re, pathlib
path, vol_line = sys.argv[1], sys.argv[2]
src = pathlib.Path(path).read_text()
lines = src.splitlines()
svc_re = re.compile(r'^( {2,4})remnanode:\s*$')
svc_idx = next((i for i, l in enumerate(lines) if svc_re.match(l)), None)
if svc_idx is None:
    sys.exit("no remnanode: service in compose")
svc_indent = svc_re.match(lines[svc_idx]).group(1)
key_indent = svc_indent + '  '
end = len(lines)
for i in range(svc_idx + 1, len(lines)):
    if lines[i].strip() and not lines[i].startswith(key_indent):
        end = i; break
vol_idx = next((i for i in range(svc_idx + 1, end)
                if lines[i].startswith(key_indent + 'volumes:')), None)
if vol_idx is None:
    lines.insert(end, key_indent + 'volumes:')
    lines.insert(end + 1, vol_line)
else:
    lines.insert(vol_idx + 1, vol_line)
pathlib.Path(path).write_text('\n'.join(lines) + '\n')
PY
fi

# ---- 7. Restart node ----
note "restarting remnanode"
(cd "$NODE_DIR" && docker compose down && docker compose up -d)

# ---- 8. Cron-based renewal (weekly) ----
CRON_LINE="0 3 * * 1 cd $CERTBOT_DIR && docker compose run --rm certbot renew --quiet && cd $NODE_DIR && docker compose restart"
if crontab -l 2>/dev/null | grep -qF "$CERTBOT_DIR && docker compose run --rm certbot renew"; then
  note "renew cron already present"
else
  note "installing weekly renew cron (Mon 03:00)"
  ( crontab -l 2>/dev/null; echo "$CRON_LINE" ) | crontab -
fi

# ---- 9. Emit ready-to-paste XHTTP_mws inbound JSON ----
INBOUND_PORT="${INBOUND_PORT:-8447}"
XHTTP_PATH="${XHTTP_PATH:-/xhttppath}"
INBOUND_FILE="${NODE_DIR}/xhttp_mws.json"
note "writing inbound JSON → $INBOUND_FILE"
cat > "$INBOUND_FILE" <<EOF
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    {
      "tag": "XHTTP_mws",
      "port": $INBOUND_PORT,
      "listen": "0.0.0.0",
      "protocol": "vless",
      "settings": {
        "clients": [],
        "fallbacks": [],
        "decryption": "none"
      },
      "sniffing": {
        "enabled": true,
        "destOverride": ["http", "tls", "quic"]
      },
      "streamSettings": {
        "network": "xhttp",
        "security": "tls",
        "tlsSettings": {
          "minVersion": "1.2",
          "certificates": [
            {
              "keyFile": "/etc/letsencrypt/live/$DOMAIN/privkey.pem",
              "certificateFile": "/etc/letsencrypt/live/$DOMAIN/fullchain.pem"
            }
          ]
        },
        "xhttpSettings": {
          "mode": "auto",
          "path": "$XHTTP_PATH"
        }
      }
    }
  ],
  "outbounds": [
    { "tag": "DIRECT", "protocol": "freedom" },
    { "tag": "BLOCK",  "protocol": "blackhole" }
  ],
  "routing": { "rules": [] }
}
EOF

note "done. cert: $CERT_PATH"
note "node reads /etc/letsencrypt/live/$DOMAIN/{fullchain,privkey}.pem"
note "next: open Remnawave panel → Профили → выбрать профиль → Добавить инбаунд"
note "      → paste contents of $INBOUND_FILE"
note "      → assign profile to this node and restart"
