#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export AGENT_RUNTIME_URL="${AGENT_RUNTIME_URL:-http://127.0.0.1:8083}"
export AGENT_RUNTIME_CHAT_UI_ADDR="${AGENT_RUNTIME_CHAT_UI_ADDR:-127.0.0.1:8084}"
exec node dev/chat/server.mjs
