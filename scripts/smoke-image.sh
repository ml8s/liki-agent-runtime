#!/usr/bin/env bash
# Start the locally built runtime image with the development deployment and
# verify its operational and public A2A surfaces. MCP dependency health needs
# real engine/counsel upstreams and is covered by assembly E2E.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE_TAG="${IMAGE_TAG:-local}"
IMAGE="${IMAGE:-${LIKI_AGENT_RUNTIME_IMAGE:-ghcr.io/ml8s/liki-agent-runtime:$IMAGE_TAG}}"
PORT="${AGENTS_SMOKE_PORT:-18083}"
OPS_PORT="${AGENTS_SMOKE_OPS_PORT:-18085}"
NAME="liki-agent-runtime-smoke-$$"

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --name "$NAME" \
  -p "$PORT:8083" \
  -p "$OPS_PORT:8085" \
  -v "$ROOT/dev/agent-deployment:/agent-deployment:ro" \
  --tmpfs /data:uid=65532,gid=65532 \
  -e LIKI_ENV=development \
  -e LIKI_AGENT_RUNTIME_TOPOLOGY=single \
  -e LIKI_AGENT_RUNTIME_ADDR=:8083 \
  -e LIKI_AGENT_RUNTIME_OPS_ADDR=:8085 \
  -e LIKI_AGENT_RUNTIME_PUBLIC_URL=http://127.0.0.1:8083 \
  -e LIKI_AGENT_RUNTIME_DB_PATH=/data/liki-agent-runtime-audit.db \
  -e LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE=/agent-deployment/deployment.json \
  -e LIKI_AGENT_RUNTIME_SKILLS_ROOT=/agent-deployment/skills \
  -e LIKI_MCP_ENGINE_URL=http://127.0.0.1:9/mcp \
  -e LIKI_MCP_COUNSEL_URL=http://127.0.0.1:9/mcp/bazi \
  -e LIKI_MCP_ENGINE_TOKEN=smoke-not-used \
  -e LIKI_MCP_COUNSEL_TOKEN=smoke-not-used \
  -e LIKI_LLM_BASE_URL=http://127.0.0.1:9/v1 \
  -e LIKI_LLM_API_KEY=smoke-not-used \
  -e LIKI_LLM_MODEL=smoke \
  -e LIKI_LLM_PROVIDER=openai \
  "$IMAGE" >/dev/null

ready=false
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$OPS_PORT/version" >/dev/null 2>&1; then
    ready=true
    break
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null || true)" != true ]; then
    break
  fi
  sleep 1
done

if [ "$ready" != true ]; then
  echo "❌ liki-agent-runtime image did not become ready" >&2
  docker logs "$NAME" >&2 || true
  exit 1
fi

curl -fsS "http://127.0.0.1:$OPS_PORT/version" | grep -q 'version'
curl -fsS "http://127.0.0.1:$PORT/.well-known/agent-card.json" | grep -q '"protocolBinding":"JSONRPC"'
echo "✅ liki-agent-runtime image smoke passed"
