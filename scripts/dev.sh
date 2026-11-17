#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

export AGENTGO_ENV="${AGENTGO_ENV:-development}"
export AGENTGO_LOG_LEVEL="${AGENTGO_LOG_LEVEL:-debug}"

exec go run ./cmd/agentgo
