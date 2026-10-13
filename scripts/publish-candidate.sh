#!/usr/bin/env bash
# Copy an already validated local candidate to GHCR without rebuilding.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MANIFEST="${MANIFEST:-$ROOT/build/candidate/candidate-digests.json}"
TARGET_REGISTRY="${TARGET_REGISTRY:-ghcr.io}"

[ -f "$MANIFEST" ] || { echo "❌ candidate manifest not found: $MANIFEST" >&2; exit 1; }

readarray -t manifest < <(python3 - "$MANIFEST" <<'PY'
import json, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
required = {"registry", "repository", "candidate_tag", "images"}
if missing := required - data.keys():
    raise SystemExit(f"missing manifest fields: {', '.join(sorted(missing))}")
if data["registry"] == "ghcr.io":
    raise SystemExit("candidate is already stored in ghcr.io")
digest = data["images"]["agents"]
print(data["registry"], data["repository"], digest, data["candidate_tag"])
PY
)
read -r source_registry repository digest candidate_tag <<<"${manifest[0]}"
source="$source_registry/$repository@$digest"
target="$TARGET_REGISTRY/$repository:$candidate_tag"
echo "── publishing $target ──"
docker buildx imagetools create -t "$target" "$source"
raw="$(docker buildx imagetools inspect --raw "$target")"
actual="sha256:$(printf '%s' "$raw" | sha256sum | cut -d' ' -f1)"
[ "$actual" = "$digest" ] || {
  echo "❌ published digest mismatch for $target: $actual != $digest" >&2
  exit 1
}
echo "✅ system-tested candidate published to $TARGET_REGISTRY"
