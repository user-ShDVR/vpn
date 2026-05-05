#!/usr/bin/env bash
# remote-setup.sh — runs ON the target server. Installs 3x-ui (MHSanaei v2.x),
# generates a REALITY keypair, configures subscription service, and creates a
# VLESS+REALITY+xhttp inbound. Prints a JSON line with the relevant facts at
# the end (prefixed __DEPLOY_JSON__) for the orchestrator to consume.
#
# Required env: PANEL_USER PANEL_PASS PANEL_PORT PANEL_SUB_PORT INBOUND_PORT
#               REALITY_DEST REALITY_SNI SERVER_NAME

set -euo pipefail

: "${PANEL_USER:?}"; : "${PANEL_PASS:?}"; : "${PANEL_PORT:?}"; : "${PANEL_SUB_PORT:?}"
: "${INBOUND_PORT:?}"; : "${REALITY_DEST:?}"; : "${REALITY_SNI:?}"; : "${SERVER_NAME:?}"

err() { echo "remote-setup error: $*" >&2; exit 1; }

# --- 1. Deps ---
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl jq ca-certificates >/dev/null

# --- 2. Install 3x-ui (non-interactive: install.sh accepts user/pass/port) ---
if ! systemctl is-active --quiet x-ui 2>/dev/null; then
  echo "installing 3x-ui..."
  bash <(curl -fsSL https://raw.githubusercontent.com/mhsanaei/3x-ui/master/install.sh) \
    "$PANEL_USER" "$PANEL_PASS" "$PANEL_PORT" >/tmp/x-ui-install.log 2>&1 \
    || { tail -50 /tmp/x-ui-install.log >&2; err "x-ui install failed"; }
else
  # Reapply credentials/port via /usr/local/x-ui/x-ui CLI in case we are re-running.
  /usr/local/x-ui/x-ui setting -username "$PANEL_USER" -password "$PANEL_PASS" >/dev/null 2>&1 || true
  /usr/local/x-ui/x-ui setting -port "$PANEL_PORT" >/dev/null 2>&1 || true
  systemctl restart x-ui
fi

# Wait for panel
PANEL_BASE="http://127.0.0.1:$PANEL_PORT"
for i in $(seq 1 30); do
  if curl -fsS "$PANEL_BASE/" >/dev/null 2>&1; then break; fi
  sleep 1
  [[ "$i" -eq 30 ]] && err "panel did not become ready"
done

# --- 3. Login (cookie auth) ---
COOKIE=$(mktemp)
trap 'rm -f "$COOKIE"' EXIT

LOGIN_RESP=$(curl -fsS -c "$COOKIE" "$PANEL_BASE/login" \
  -d "username=$PANEL_USER&password=$PANEL_PASS")
[[ "$(jq -r '.success' <<<"$LOGIN_RESP")" == "true" ]] || err "login failed: $LOGIN_RESP"

# --- 4. Configure subscription service (enable + port + path) ---
# Random sub path so it is not guessable.
SUB_PATH="/$(tr -dc 'a-zA-Z0-9' </dev/urandom | head -c12)/sub/"

CURRENT_SETTINGS=$(curl -fsS -b "$COOKIE" "$PANEL_BASE/panel/setting/all")
SETTINGS_OBJ=$(jq -r '.obj // {}' <<<"$CURRENT_SETTINGS")

UPDATED_SETTINGS=$(jq \
  --arg subPort "$PANEL_SUB_PORT" \
  --arg subPath "$SUB_PATH" \
  '. + {
    subEnable: true,
    subListen: "",
    subPort: ($subPort|tonumber),
    subPath: $subPath,
    subDomain: "",
    subKeyFile: "",
    subCertFile: "",
    subShowInfo: true,
    subURI: "",
    subJsonURI: ""
  }' <<<"$SETTINGS_OBJ")

UPDATE_RESP=$(curl -fsS -b "$COOKIE" "$PANEL_BASE/panel/setting/update" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "subEnable=true" \
  --data-urlencode "subListen=" \
  --data-urlencode "subPort=$PANEL_SUB_PORT" \
  --data-urlencode "subPath=$SUB_PATH" \
  --data-urlencode "subDomain=" \
  --data-urlencode "subShowInfo=true")
[[ "$(jq -r '.success' <<<"$UPDATE_RESP")" == "true" ]] || err "settings update failed: $UPDATE_RESP"

curl -fsS -b "$COOKIE" -X POST "$PANEL_BASE/panel/setting/restartPanel" >/dev/null 2>&1 || true
# Wait for restart
sleep 3
for i in $(seq 1 30); do
  if curl -fsS "$PANEL_BASE/" >/dev/null 2>&1; then break; fi
  sleep 1
  [[ "$i" -eq 30 ]] && err "panel did not come back after restart"
done

# Re-login after restart (cookies invalidated)
> "$COOKIE"
curl -fsS -c "$COOKIE" "$PANEL_BASE/login" \
  -d "username=$PANEL_USER&password=$PANEL_PASS" >/dev/null

# --- 5. Generate REALITY keypair ---
KEYS_RESP=$(curl -fsS -b "$COOKIE" -X POST "$PANEL_BASE/server/getNewX25519Cert")
[[ "$(jq -r '.success' <<<"$KEYS_RESP")" == "true" ]] || err "key gen failed: $KEYS_RESP"
PRIVATE_KEY=$(jq -r '.obj.privateKey' <<<"$KEYS_RESP")
PUBLIC_KEY=$(jq -r '.obj.publicKey'   <<<"$KEYS_RESP")

# Random shortId (8 hex chars per REALITY convention)
SHORT_ID=$(tr -dc '0-9a-f' </dev/urandom | head -c 8)
# Random xhttp path
XHTTP_PATH="/$(tr -dc 'a-zA-Z0-9' </dev/urandom | head -c10)"

# --- 6. Add VLESS+REALITY+xhttp inbound ---
SETTINGS_JSON=$(jq -nc '{clients: [], decryption: "none", fallbacks: []}')
STREAM_JSON=$(jq -nc \
  --arg dest "$REALITY_DEST" \
  --arg sni "$REALITY_SNI" \
  --arg priv "$PRIVATE_KEY" \
  --arg sid "$SHORT_ID" \
  --arg xpath "$XHTTP_PATH" \
  '{
    network: "xhttp",
    security: "reality",
    realitySettings: {
      show: false,
      xver: 0,
      dest: $dest,
      serverNames: [$sni],
      privateKey: $priv,
      minClient: "",
      maxClient: "",
      maxTimediff: 0,
      shortIds: [$sid],
      settings: { publicKey: "", fingerprint: "chrome", serverName: "", spiderX: "/" }
    },
    xhttpSettings: { path: $xpath, host: "", scMaxConcurrentPosts: "100-200", scMaxEachPostBytes: "1000000-2000000", scMinPostsIntervalMs: "10-50", noSSEHeader: false, xPaddingBytes: "100-1000", mode: "auto" }
  }')
SNIFFING_JSON=$(jq -nc '{enabled: true, destOverride: ["http","tls","quic"]}')

# 3x-ui add inbound payload uses stringified JSON for nested fields.
ADD_RESP=$(curl -fsS -b "$COOKIE" "$PANEL_BASE/panel/api/inbounds/add" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-urlencode "remark=$SERVER_NAME" \
  --data-urlencode "enable=true" \
  --data-urlencode "expiryTime=0" \
  --data-urlencode "listen=" \
  --data-urlencode "port=$INBOUND_PORT" \
  --data-urlencode "protocol=vless" \
  --data-urlencode "settings=$SETTINGS_JSON" \
  --data-urlencode "streamSettings=$STREAM_JSON" \
  --data-urlencode "tag=inbound-$INBOUND_PORT" \
  --data-urlencode "sniffing=$SNIFFING_JSON")

if [[ "$(jq -r '.success' <<<"$ADD_RESP")" != "true" ]]; then
  err "add inbound failed: $ADD_RESP"
fi
INBOUND_ID=$(jq -r '.obj.id' <<<"$ADD_RESP")
[[ "$INBOUND_ID" == "null" || -z "$INBOUND_ID" ]] && err "no inbound id in response: $ADD_RESP"

# --- 6b. Cascade outbound (entry → exit) ---
# If this is an entry server and EXIT_* are provided, merge a "cascade-out"
# VLESS+REALITY outbound into the Xray config and add a routing rule that
# sends inbound-$INBOUND_PORT traffic through it. RU domains/IPs continue to
# use direct (geosite:cn-style "ru-direct" rule must be set on the panel
# manually for now; here we route everything through cascade-out by default).
if [[ "${SERVER_TYPE:-}" == "entry" && -n "$EXIT_HOST" && -n "$EXIT_PORT" && -n "$EXIT_UUID" && -n "$EXIT_PUBKEY" && -n "$EXIT_SHORT_ID" && -n "$EXIT_SNI" ]]; then
  echo "applying cascade outbound to exit $EXIT_HOST:$EXIT_PORT..."
  CFG_RESP=$(curl -fsS -b "$COOKIE" "$PANEL_BASE/panel/setting/getXrayConfig")
  CFG=$(jq -r '.obj // empty' <<<"$CFG_RESP")
  [[ -z "$CFG" ]] && err "could not fetch xray config"

  CASCADE_OUT=$(jq -nc \
    --arg host "$EXIT_HOST" \
    --argjson port "$EXIT_PORT" \
    --arg uuid "$EXIT_UUID" \
    --arg pbk "$EXIT_PUBKEY" \
    --arg sid "$EXIT_SHORT_ID" \
    --arg sni "$EXIT_SNI" \
    '{
      tag: "cascade-out",
      protocol: "vless",
      settings: {
        vnext: [{
          address: $host, port: $port,
          users: [{ id: $uuid, encryption: "none", flow: "" }]
        }]
      },
      streamSettings: {
        network: "xhttp",
        security: "reality",
        realitySettings: {
          serverName: $sni, fingerprint: "chrome",
          publicKey: $pbk, shortId: $sid, spiderX: "/"
        }
      }
    }')

  RULE=$(jq -nc \
    --argjson port "$INBOUND_PORT" \
    '{type: "field", inboundTag: ["inbound-" + ($port|tostring)], outboundTag: "cascade-out"}')

  NEW_CFG=$(jq \
    --argjson out "$CASCADE_OUT" \
    --argjson rule "$RULE" \
    '
      .outbounds = (.outbounds // []) | map(select(.tag != "cascade-out")) + [$out]
      | .routing = (.routing // {rules: []})
      | .routing.rules = (.routing.rules // []) | map(select(.outboundTag != "cascade-out")) + [$rule]
    ' <<<"$CFG")

  UPD_RESP=$(curl -fsS -b "$COOKIE" "$PANEL_BASE/panel/setting/updateConfig" \
    -H "Content-Type: application/x-www-form-urlencoded" \
    --data-urlencode "xrayTemplateConfig=$(jq -c . <<<"$NEW_CFG")")
  if [[ "$(jq -r '.success' <<<"$UPD_RESP")" != "true" ]]; then
    echo "warning: cascade config update failed: $UPD_RESP" >&2
  else
    # Restart Xray to apply
    curl -fsS -b "$COOKIE" -X POST "$PANEL_BASE/server/restartXrayService" >/dev/null 2>&1 || true
  fi
fi

# --- 7. Output JSON for the orchestrator ---
# Allow caller to override the public IP (for hosts where ipify is blocked or
# when the inbound IP differs from the public one routed through CDN/HAProxy).
if [[ -n "${SUB_URL_OVERRIDE:-}" ]]; then
  SUB_URL_BASE="$SUB_URL_OVERRIDE"
else
  SUB_URL_BASE="http://$(curl -fsS https://api.ipify.org || echo 127.0.0.1):$PANEL_SUB_PORT"
fi
jq -nc \
  --argjson inbound_id "$INBOUND_ID" \
  --arg sub_url "$SUB_URL_BASE" \
  --arg sub_path "$SUB_PATH" \
  --arg public_key "$PUBLIC_KEY" \
  --arg short_id "$SHORT_ID" \
  --arg xhttp_path "$XHTTP_PATH" \
  '{inbound_id: $inbound_id, sub_url: $sub_url, sub_path: $sub_path, public_key: $public_key, short_id: $short_id, xhttp_path: $xhttp_path}' \
  | sed 's/^/__DEPLOY_JSON__ /'
