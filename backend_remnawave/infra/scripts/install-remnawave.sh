#!/usr/bin/env bash
#
# install-remnawave.sh — thin wrapper that runs Capybara-z/RemnaSetup on a
# remote host. RemnaSetup itself is the source of truth for what gets installed
# (panel + Postgres + subscription page + Caddy, or node-only); we just SSH in
# and run it.
#
# Usage:
#   ./install-remnawave.sh root@HOST [panel|node]
#
# After the script finishes, log into the panel UI, generate an API token, and
# put it into the backend's REMNAWAVE_TOKEN env var.
set -euo pipefail

TARGET="${1:-}"
MODE="${2:-panel}"

if [[ -z "$TARGET" ]]; then
    echo "usage: $0 root@HOST [panel|node]" >&2
    exit 1
fi

case "$MODE" in
    panel|node) ;;
    *) echo "mode must be 'panel' or 'node'" >&2; exit 1 ;;
esac

echo ">> running RemnaSetup ($MODE) on $TARGET"
# RemnaSetup is interactive; this just kicks the bootstrap and hands the
# operator the menu. Read the prompts on the remote side.
ssh -t "$TARGET" "bash -c '\$(curl -fsSL https://raw.githubusercontent.com/Capybara-z/RemnaSetup/main/install_remnawave.sh)'"

cat <<EOF

Done. Next steps:
  1. Open https://<DOMAIN> in browser, finish the first-run admin setup.
  2. Settings → API tokens → generate a token. Put it in REMNAWAVE_TOKEN.
  3. Add nodes via panel UI (if you ran with mode=panel).
  4. Create internal squads + inbounds, copy their UUIDs into the СвязьОК admin
     UI (/admin/servers) — those squad UUIDs are what plans bind to.

For cascade routing (RU entry → foreign exit) configure the xray config of the
RU node's inbound to outbound onto the foreign node. Remnawave does not have a
"cascade" toggle; it is set up inside the node's xray config.
EOF
