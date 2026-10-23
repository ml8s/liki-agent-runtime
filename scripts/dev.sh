#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

export LIKI_ENV="${LIKI_ENV:-development}"
export LIKI_LOG_LEVEL="${LIKI_LOG_LEVEL:-debug}"
# Development-only skills root override: the deployment pins an absolute
# container path, so the host run rebinds it to the same fixture.

exec go run ./cmd/liki-agent-runtime
