#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export AGENTGO_URL="${AGENTGO_URL:-http://127.0.0.1:8083}"
export AGENTGO_CHAT_UI_ADDR="${AGENTGO_CHAT_UI_ADDR:-127.0.0.1:8084}"
exec node dev/chat/server.mjs
