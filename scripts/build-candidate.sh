#!/usr/bin/env bash
# Build and validate a candidate image in the local registry.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [ -n "$(git status --porcelain)" ]; then
  echo "❌ git worktree must be clean before make candidate" >&2
  git status --short >&2
  exit 1
fi

RELEASE_VERSION="${RELEASE_VERSION:?set RELEASE_VERSION to the selected CalVer, e.g. 2026.10.13.0}"
if [[ ! "$RELEASE_VERSION" =~ ^[0-9]{4}\.[0-9]{2}\.[0-9]{2}\.[0-9]+$ ]]; then
  echo "❌ RELEASE_VERSION must use CalVer YYYY.MM.DD.N: $RELEASE_VERSION" >&2
  exit 1
fi


make gate


if [ -n "$(git status --porcelain)" ]; then
  echo "❌ Git worktree changed during gate; aborting candidate build" >&2
  git status --short >&2
  exit 1
fi
SOURCE_COMMIT="$(git rev-parse HEAD)"
CANDIDATE_TAG="candidate-$(git rev-parse --short=7 HEAD)-$(date -u +%Y%m%d%H%M%S)"

CANDIDATE_REGISTRY="${CANDIDATE_REGISTRY:-127.0.0.1:5000}"
CANDIDATE_REGISTRY_NAME="${CANDIDATE_REGISTRY_NAME:-liki-candidate-registry}"
CANDIDATE_REGISTRY_IMAGE="${CANDIDATE_REGISTRY_IMAGE:-registry:2}"

ensure_candidate_registry() {
  if curl -fsS "http://${CANDIDATE_REGISTRY}/v2/" >/dev/null 2>&1; then
    return
  fi
  if docker container inspect "$CANDIDATE_REGISTRY_NAME" >/dev/null 2>&1; then
    docker start "$CANDIDATE_REGISTRY_NAME" >/dev/null
  else
    docker run -d --name "$CANDIDATE_REGISTRY_NAME" \
      --restart unless-stopped \
      -p 127.0.0.1:5000:5000 \
      "$CANDIDATE_REGISTRY_IMAGE" >/dev/null
  fi
  for _ in $(seq 1 30); do
    curl -fsS "http://${CANDIDATE_REGISTRY}/v2/" >/dev/null 2>&1 && return
    sleep 1
  done
  echo "❌ local candidate registry did not become ready" >&2
  exit 1
}

ensure_candidate_registry
IMAGE_REPOSITORY="$CANDIDATE_REGISTRY/ml8s/liki-agent-runtime"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
BUILD_DIR="$ROOT/build/candidate"
METADATA_FILE="$BUILD_DIR/buildx-metadata.json"
MANIFEST_FILE="$BUILD_DIR/candidate-digests.json"

mkdir -p "$BUILD_DIR"
rm -f "$METADATA_FILE" "$MANIFEST_FILE"

docker buildx build \
  --file Dockerfile \
  --build-arg VERSION="$RELEASE_VERSION" \
  --build-arg COMMIT="$SOURCE_COMMIT" \
  --build-arg BUILD_TIME="$BUILD_TIME" \
  --build-arg GOPROXY="$GOPROXY" \
  --tag "$IMAGE_REPOSITORY:$CANDIDATE_TAG" \
  --provenance=mode=max \
  --sbom=true \
  --metadata-file "$METADATA_FILE" \
  .

AGENTS_DIGEST="$(python3 -c 'import json,sys; data=json.load(open(sys.argv[1])); print(data.get("containerimage.digest") or data["containerimage"]["digest"])' "$METADATA_FILE")"
python3 scripts/build_candidate_manifest.py \
  --registry "$CANDIDATE_REGISTRY" \
  --repository ml8s/liki-agent-runtime \
  --source-commit "$SOURCE_COMMIT" \
  --candidate-tag "$CANDIDATE_TAG" \
  --agents-digest "$AGENTS_DIGEST" \
  --output "$MANIFEST_FILE"

IMAGE="$IMAGE_REPOSITORY@$AGENTS_DIGEST" scripts/smoke-image.sh

echo "✅ candidate $CANDIDATE_TAG validated locally"
echo "$AGENTS_DIGEST"
