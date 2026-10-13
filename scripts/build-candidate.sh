#!/usr/bin/env bash
# Build and publish the single local candidate image, record its
# digest, and smoke that immutable digest.
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
IMAGE_REPOSITORY="ghcr.io/ml8s/liki-agents"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
BUILD_DIR="$ROOT/build/candidate"
METADATA_FILE="$BUILD_DIR/buildx-metadata.json"
MANIFEST_FILE="$BUILD_DIR/candidate-digests.json"

mkdir -p "$BUILD_DIR"
rm -f "$METADATA_FILE" "$MANIFEST_FILE"

docker buildx build --push \
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
  --repository ml8s/liki-agents \
  --source-commit "$SOURCE_COMMIT" \
  --candidate-tag "$CANDIDATE_TAG" \
  --agents-digest "$AGENTS_DIGEST" \
  --output "$MANIFEST_FILE"

IMAGE="$IMAGE_REPOSITORY@$AGENTS_DIGEST" scripts/smoke-image.sh

echo "✅ candidate $CANDIDATE_TAG published"
echo "$AGENTS_DIGEST"
