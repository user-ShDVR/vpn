#!/usr/bin/env bash
# deploy-server.sh — provision a fresh Ubuntu/Debian VPS with 3x-ui + VLESS-REALITY
# inbound and register it in the СвязьOK backend.
#
# Usage:
#   SERVER_NAME="ru-1" \
#   SERVER_HOST="1.2.3.4" \
#   SERVER_TYPE="entry" \
#   BACKEND_URL="https://api.example.com" \
#   ADMIN_TOKEN="<jwt-of-admin-user>" \
#   ./deploy-server.sh
#
# Optional env (defaults shown):
#   SSH_USER=root
#   SSH_PORT=22
#   PANEL_PORT=2053
#   PANEL_SUB_PORT=2096
#   INBOUND_PORT=443
#   REALITY_DEST=learn.microsoft.com:443
#   REALITY_SNI=learn.microsoft.com
#   PANEL_USER=<random>
#   PANEL_PASS=<random>
#
# Requires locally: ssh, scp, jq, curl. Requires SSH key access to the target.
# Tested with 3x-ui v2.x (MHSanaei).

set -euo pipefail

# --- Required ---
: "${SERVER_NAME:?SERVER_NAME required}"
: "${SERVER_HOST:?SERVER_HOST required}"
: "${SERVER_TYPE:?SERVER_TYPE required (entry|exit)}"
: "${BACKEND_URL:?BACKEND_URL required}"
: "${ADMIN_TOKEN:?ADMIN_TOKEN required (admin JWT)}"

# --- Optional ---
SSH_USER="${SSH_USER:-root}"
SSH_PORT="${SSH_PORT:-22}"
PANEL_PORT="${PANEL_PORT:-2053}"
PANEL_SUB_PORT="${PANEL_SUB_PORT:-2096}"
INBOUND_PORT="${INBOUND_PORT:-443}"
REALITY_DEST="${REALITY_DEST:-learn.microsoft.com:443}"
REALITY_SNI="${REALITY_SNI:-learn.microsoft.com}"
PANEL_USER="${PANEL_USER:-admin$(tr -dc 'a-z0-9' </dev/urandom | head -c6)}"
PANEL_PASS="${PANEL_PASS:-$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c20)}"

# Optional cascade-mode (entry → exit). If all EXIT_* are set and SERVER_TYPE=entry,
# remote-setup.sh writes a cascade outbound + routing rules into the entry server's
# Xray config so traffic exits through the foreign server.
EXIT_HOST="${EXIT_HOST:-}"
EXIT_PORT="${EXIT_PORT:-}"
EXIT_UUID="${EXIT_UUID:-}"
EXIT_PUBKEY="${EXIT_PUBKEY:-}"
EXIT_SHORT_ID="${EXIT_SHORT_ID:-}"
EXIT_SNI="${EXIT_SNI:-}"

# Override public sub URL (for CDN/HAProxy in front of 3x-ui sub service).
SUB_URL_OVERRIDE="${SUB_URL_OVERRIDE:-}"

if [[ "$SERVER_TYPE" != "entry" && "$SERVER_TYPE" != "exit" ]]; then
  echo "SERVER_TYPE must be 'entry' or 'exit'" >&2
  exit 1
fi

for cmd in ssh scp jq curl; do
  command -v "$cmd" >/dev/null 2>&1 || { echo "missing: $cmd" >&2; exit 1; }
done

SSH_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -p "$SSH_PORT")
SCP_OPTS=(-o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 -P "$SSH_PORT")

REMOTE_SCRIPT_PATH="$(dirname "$0")/remote-setup.sh"
if [[ ! -f "$REMOTE_SCRIPT_PATH" ]]; then
  echo "remote-setup.sh not found next to this script" >&2
  exit 1
fi

echo ">>> [1/4] Copy remote-setup.sh to $SERVER_HOST"
scp "${SCP_OPTS[@]}" "$REMOTE_SCRIPT_PATH" "$SSH_USER@$SERVER_HOST:/tmp/remote-setup.sh"

echo ">>> [2/4] Install 3x-ui + add inbound (this can take 1-2 min)"
REMOTE_OUTPUT=$(ssh "${SSH_OPTS[@]}" "$SSH_USER@$SERVER_HOST" "
  PANEL_USER='$PANEL_USER' \
  PANEL_PASS='$PANEL_PASS' \
  PANEL_PORT='$PANEL_PORT' \
  PANEL_SUB_PORT='$PANEL_SUB_PORT' \
  INBOUND_PORT='$INBOUND_PORT' \
  REALITY_DEST='$REALITY_DEST' \
  REALITY_SNI='$REALITY_SNI' \
  SERVER_NAME='$SERVER_NAME' \
  SERVER_TYPE='$SERVER_TYPE' \
  EXIT_HOST='$EXIT_HOST' \
  EXIT_PORT='$EXIT_PORT' \
  EXIT_UUID='$EXIT_UUID' \
  EXIT_PUBKEY='$EXIT_PUBKEY' \
  EXIT_SHORT_ID='$EXIT_SHORT_ID' \
  EXIT_SNI='$EXIT_SNI' \
  SUB_URL_OVERRIDE='$SUB_URL_OVERRIDE' \
  bash /tmp/remote-setup.sh
")

# remote-setup.sh prints a JSON line prefixed with __DEPLOY_JSON__ at the end
JSON_LINE=$(printf '%s\n' "$REMOTE_OUTPUT" | grep '^__DEPLOY_JSON__ ' | tail -n1 || true)
if [[ -z "$JSON_LINE" ]]; then
  echo ">>> ERROR: could not find result JSON in remote output" >&2
  echo "$REMOTE_OUTPUT" >&2
  exit 1
fi
DEPLOY_JSON="${JSON_LINE#__DEPLOY_JSON__ }"

INBOUND_ID=$(jq -r '.inbound_id'   <<<"$DEPLOY_JSON")
SUB_PATH=$(jq -r '.sub_path'       <<<"$DEPLOY_JSON")
SUB_URL=$(jq -r '.sub_url'         <<<"$DEPLOY_JSON")
PUB_KEY=$(jq -r '.public_key'      <<<"$DEPLOY_JSON")
SHORT_ID=$(jq -r '.short_id'       <<<"$DEPLOY_JSON")

echo ">>> [3/4] 3x-ui ready"
echo "    panel:    http://$SERVER_HOST:$PANEL_PORT"
echo "    user:     $PANEL_USER"
echo "    pass:     $PANEL_PASS"
echo "    inbound:  $INBOUND_ID (port $INBOUND_PORT, REALITY)"
echo "    sub:      ${SUB_URL}${SUB_PATH}<sub_id>"
echo "    pubkey:   $PUB_KEY"
echo "    shortId:  $SHORT_ID"

echo ">>> [4/4] Register in backend $BACKEND_URL"
PANEL_URL="http://$SERVER_HOST:$PANEL_PORT"
REGISTER_PAYLOAD=$(jq -n \
  --arg name        "$SERVER_NAME" \
  --arg panel_url   "$PANEL_URL" \
  --arg panel_user  "$PANEL_USER" \
  --arg panel_pass  "$PANEL_PASS" \
  --argjson inbound "$INBOUND_ID" \
  --arg type        "$SERVER_TYPE" \
  --arg host        "$SERVER_HOST" \
  --argjson port    "$INBOUND_PORT" \
  --arg sub_url     "$SUB_URL" \
  --arg sub_path    "$SUB_PATH" \
  '{
    name: $name, panel_url: $panel_url, panel_user: $panel_user, panel_pass: $panel_pass,
    inbound_id: $inbound, type: $type, host: $host, port: $port,
    sub_url: $sub_url, sub_path: $sub_path
  }')

REGISTER_RESPONSE=$(curl -fsS -X POST "$BACKEND_URL/api/v1/admin/servers" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "$REGISTER_PAYLOAD") || {
    echo ">>> register failed; payload: $REGISTER_PAYLOAD" >&2
    exit 1
  }

SERVER_ID=$(jq -r '.id' <<<"$REGISTER_RESPONSE")
echo ">>> done. server_id=$SERVER_ID"
echo
echo "Save these credentials securely:"
echo "  PANEL_USER=$PANEL_USER"
echo "  PANEL_PASS=$PANEL_PASS"
echo "  REALITY_PRIVATE_KEY (already on server, retrievable from 3x-ui inbound)"
