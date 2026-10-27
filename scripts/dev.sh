#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

export AGENT_RUNTIME_ENV="${AGENT_RUNTIME_ENV:-development}"
export AGENT_RUNTIME_LOG_LEVEL="${AGENT_RUNTIME_LOG_LEVEL:-debug}"

exec go run ./cmd/agent-runtime
