#!/usr/bin/env bash
#
# Sets up Let's Encrypt certs for a Remnawave node fronted by MWS CDN.
# Steps automated from the manual guide:
#   1. Install a Certbot docker-compose stub at /opt/certbot
#   2. Issue a cert via certbot --standalone (port 80 must be free)
#   3. Patch /opt/remnanode/docker-compose.yml to mount /opt/certbot/certs
#      into the node as :ro
#   4. Restart the node
#   5. Install a weekly renew cron
#
# Run on the *node* host (not on the panel host).
#
# Usage:
#   setup-mwscdn-certs.sh <domain> <admin-email>
#
# Re-runs are safe — cert reuse + idempotent compose patch.

set -euo pipefail

DOMAIN="${1:-}"
EMAIL="${2:-}"
CERTBOT_DIR="/opt/certbot"
NODE_DIR="/opt/remnanode"
NODE_COMPOSE="${NODE_DIR}/docker-compose.yml"

die() { echo "ERROR: $*" >&2; exit 1; }
note() { echo "==> $*"; }

[[ -n "$DOMAIN" && -n "$EMAIL" ]] || die "usage: $0 <domain> <admin-email>"
[[ "$EUID" -eq 0 ]] || die "run as root (sudo)"
command -v docker >/dev/null || die "docker not installed"
docker compose version >/dev/null 2>&1 || die "docker compose plugin missing"
[[ -d "$NODE_DIR" ]] || die "$NODE_DIR not found — install remnanode first"
[[ -f "$NODE_COMPOSE" ]] || die "$NODE_COMPOSE missing"

# ---- 1. Certbot stub ----
note "preparing $CERTBOT_DIR"
mkdir -p "$CERTBOT_DIR"
cat > "$CERTBOT_DIR/docker-compose.yml" <<'YAML'
services:
  certbot:
    container_name: certbot
    image: certbot/certbot
    network_mode: host
    volumes:
      - ./certs:/etc/letsencrypt
      - ./var-lib-letsencrypt:/var/lib/letsencrypt
YAML

# ---- 2. Issue cert (skip if already exists) ----
CERT_PATH="$CERTBOT_DIR/certs/live/$DOMAIN/fullchain.pem"
if [[ -f "$CERT_PATH" ]]; then
  note "cert already exists at $CERT_PATH — skipping issue"
else
  note "freeing port 80 (stopping anything that listens on :80)"
  if ss -tlnp 2>/dev/null | grep -q ':80 '; then
    die "port 80 is busy. stop the listener (caddy/nginx/etc) and re-run"
  fi

  note "requesting cert for $DOMAIN"
  docker run --rm \
    -v "$CERTBOT_DIR/certs:/etc/letsencrypt" \
    -v "$CERTBOT_DIR/var-lib-letsencrypt:/var/lib/letsencrypt" \
    --network host \
    certbot/certbot certonly --standalone \
    --non-interactive --agree-tos \
    --email "$EMAIL" \
    -d "$DOMAIN"
  [[ -f "$CERT_PATH" ]] || die "certbot did not produce $CERT_PATH"
fi

# ---- 3. Patch node compose to mount certs ----
VOL_LINE="      - '/opt/certbot/certs:/etc/letsencrypt:ro'"
if grep -qF "/opt/certbot/certs:/etc/letsencrypt" "$NODE_COMPOSE"; then
  note "node compose already mounts certs — skipping patch"
else
  note "backing up $NODE_COMPOSE → ${NODE_COMPOSE}.bak.$(date +%s)"
  cp "$NODE_COMPOSE" "${NODE_COMPOSE}.bak.$(date +%s)"

  note "patching $NODE_COMPOSE (adding cert volume)"
  # Insert the mount line into the first remnanode service's `volumes:` block.
  # If no `volumes:` key exists for the service, create one.
  python3 - "$NODE_COMPOSE" "$VOL_LINE" <<'PY'
import sys, re, pathlib
path, vol_line = sys.argv[1], sys.argv[2]
src = pathlib.Path(path).read_text()
lines = src.splitlines()
# Find `remnanode:` service header
svc_re = re.compile(r'^( {2,4})remnanode:\s*$')
svc_idx = next((i for i, l in enumerate(lines) if svc_re.match(l)), None)
if svc_idx is None:
    sys.exit("no remnanode: service in compose")
svc_indent = svc_re.match(lines[svc_idx]).group(1)
key_indent = svc_indent + '  '
# Find end of service block
end = len(lines)
for i in range(svc_idx + 1, len(lines)):
    if lines[i].strip() and not lines[i].startswith(key_indent):
        end = i; break
# Find or create `volumes:` inside service
vol_idx = next((i for i in range(svc_idx + 1, end)
                if lines[i].startswith(key_indent + 'volumes:')), None)
if vol_idx is None:
    lines.insert(end, key_indent + 'volumes:')
    lines.insert(end + 1, vol_line)
else:
    # Insert as the first item under volumes:
    lines.insert(vol_idx + 1, vol_line)
pathlib.Path(path).write_text('\n'.join(lines) + '\n')
PY
fi

# ---- 4. Restart node ----
note "restarting remnanode"
(cd "$NODE_DIR" && docker compose down && docker compose up -d)

# ---- 5. Cron-based renewal (weekly) ----
CRON_LINE="0 3 * * 1 cd $CERTBOT_DIR && docker compose run --rm certbot renew --quiet && cd $NODE_DIR && docker compose restart"
if crontab -l 2>/dev/null | grep -qF "$CERTBOT_DIR && docker compose run --rm certbot renew"; then
  note "renew cron already present"
else
  note "installing weekly renew cron (Mon 03:00)"
  ( crontab -l 2>/dev/null; echo "$CRON_LINE" ) | crontab -
fi

note "done. cert: $CERT_PATH"
note "node now reads /etc/letsencrypt/live/$DOMAIN/{fullchain,privkey}.pem"
note "next step: paste the XHTTP_mws inbound JSON into the node's xray config with this domain"
