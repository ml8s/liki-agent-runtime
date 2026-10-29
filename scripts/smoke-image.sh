#!/usr/bin/env bash
# Start the locally built runtime image with the development deployment and
# verify its operational and public A2A surfaces. MCP dependency health needs
# real MCP upstreams and is covered by assembly E2E.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE_TAG="${IMAGE_TAG:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo local)}"
IMAGE="${IMAGE:-${AGENT_RUNTIME_IMAGE:-agent-runtime-local/agent-runtime:$IMAGE_TAG}}"
PORT="${AGENTS_SMOKE_PORT:-18083}"
OPS_PORT="${AGENTS_SMOKE_OPS_PORT:-18085}"
NAME="agent-runtime-smoke-$$"

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
  -e AGENT_RUNTIME_ENV=development \
  -e AGENT_RUNTIME_TOPOLOGY=single \
  -e AGENT_RUNTIME_PROTOCOL_ADDR=:8083 \
  -e AGENT_RUNTIME_OPS_ADDR=:8085 \
  -e AGENT_RUNTIME_PROTOCOL_TOKEN=dev-protocol-token \
  -e AGENT_RUNTIME_PUBLIC_URL=http://127.0.0.1:8083 \
  -e AGENT_RUNTIME_AUDIT_DB_PATH=/data/agent-runtime-audit.db \
  -e AGENT_RUNTIME_DEPLOYMENT_FILE=/agent-deployment/deployment.json \
  -e AGENT_RUNTIME_MCP_TOOLS_A_URL=http://127.0.0.1:9/mcp \
  -e AGENT_RUNTIME_MCP_TOOLS_B_URL=http://127.0.0.1:9/mcp \
  -e AGENT_RUNTIME_MCP_TOOLS_A_TOKEN=smoke-not-used \
  -e AGENT_RUNTIME_MCP_TOOLS_B_TOKEN=smoke-not-used \
  -e AGENT_RUNTIME_LLM_BASE_URL=http://127.0.0.1:9/v1 \
  -e AGENT_RUNTIME_LLM_API_KEY=smoke-not-used \
  -e AGENT_RUNTIME_LLM_MODEL=smoke \
  -e AGENT_RUNTIME_LLM_PROVIDER=openai \
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
  echo "❌ agent-runtime image did not become ready" >&2
  docker logs "$NAME" >&2 || true
  exit 1
fi

curl -fsS "http://127.0.0.1:$OPS_PORT/version" | grep -q 'version'
curl -fsS "http://127.0.0.1:$PORT/.well-known/agent-card.json" | grep -q '"protocolBinding":"JSONRPC"'

# Credential separation with both tokens configured: the ops token owns
# /metrics (accepted with standard case-insensitive Bearer semantics), and
# neither credential crosses into the other surface.
ops_status="$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$OPS_PORT/metrics")"
if [ "$ops_status" != "401" ]; then
  echo "❌ unauthenticated /metrics returned HTTP $ops_status, want 401" >&2
  exit 1
fi
ops_status="$(curl -sS -o /dev/null -w '%{http_code}' \
  -H 'Authorization: bearer dev-ops-token' "http://127.0.0.1:$OPS_PORT/metrics")"
if [ "$ops_status" != "200" ]; then
  echo "❌ ops-token /metrics returned HTTP $ops_status, want 200" >&2
  exit 1
fi
ops_status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  -H 'Authorization: Bearer dev-ops-token' -H 'Content-Type: application/json' \
  --data '{}' "http://127.0.0.1:$PORT/a2a")"
if [ "$ops_status" != "401" ]; then
  echo "❌ protocol surface accepted the ops token (HTTP $ops_status), want 401" >&2
  exit 1
fi
ops_status="$(curl -sS -o /dev/null -w '%{http_code}' \
  -H 'Authorization: Bearer dev-protocol-token' "http://127.0.0.1:$OPS_PORT/metrics")"
if [ "$ops_status" != "401" ]; then
  echo "❌ ops surface accepted the protocol token (HTTP $ops_status), want 401" >&2
  exit 1
fi
echo "✅ agent-runtime image smoke passed"
