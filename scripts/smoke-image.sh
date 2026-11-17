#!/usr/bin/env bash
# Start the locally built runtime image with the development deployment and
# verify only the image artifact's process lifecycle. Protocol conformance and
# dependency readiness are owned by the repository protocol and assembly tests.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE_TAG="${IMAGE_TAG:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo local)}"
IMAGE="${IMAGE:-${AGENTGO_IMAGE:-agentgo-local/agentgo:$IMAGE_TAG}}"
PORT="${AGENTS_SMOKE_PORT:-18083}"
OPS_PORT="${AGENTS_SMOKE_OPS_PORT:-18085}"
NAME="agentgo-smoke-$$"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  -p "$PORT:8083" \
  -p "$OPS_PORT:8085" \
  -v "$ROOT/dev/agent-deployment:/agent-deployment:ro" \
  -v "$ROOT/dev/agent-deployment/skills:/skills:ro" \
  --tmpfs /data:uid=65532,gid=65532 \
  -e AGENTGO_ENV=development \
  -e AGENTGO_TOPOLOGY=single \
  -e AGENTGO_PROTOCOL_ADDR=:8083 \
  -e AGENTGO_OPS_ADDR=:8085 \
  -e AGENTGO_PROTOCOL_TOKEN=dev-protocol-token \
  -e AGENTGO_PUBLIC_URL=http://127.0.0.1:8083 \
  -e AGENTGO_AUDIT_DB_PATH=/data/agentgo-audit.db \
  -e AGENTGO_DEPLOYMENT_FILE=/agent-deployment/deployment.json \
  -e AGENTGO_MCP_TOOLS_A_URL=http://127.0.0.1:9/mcp \
  -e AGENTGO_MCP_TOOLS_B_URL=http://127.0.0.1:9/mcp \
  -e AGENTGO_MCP_TOOLS_A_TOKEN=smoke-not-used \
  -e AGENTGO_MCP_TOOLS_B_TOKEN=smoke-not-used \
  -e AGENTGO_LLM_BASE_URL=http://127.0.0.1:9/v1 \
  -e AGENTGO_LLM_API_KEY=smoke-not-used \
  -e AGENTGO_LLM_MODEL=smoke \
  -e AGENTGO_LLM_PROVIDER=openai \
  "$IMAGE" >/dev/null

live=false
for _ in $(seq 1 30); do
  # Liveness is the image artifact contract: the process is serving HTTP.
  # It intentionally does not turn unavailable MCP dependencies into a
  # build failure; readiness is exercised with real fixtures elsewhere.
  if curl -fsS "http://127.0.0.1:$OPS_PORT/livez" >/dev/null 2>&1; then
    live=true
    break
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null || true)" != true ]; then
    break
  fi
  sleep 1
done

if [ "$live" != true ]; then
  echo "❌ agentgo image did not become live" >&2
  docker logs "$NAME" >&2 || true
  exit 1
fi
echo "✅ agentgo image smoke passed"
