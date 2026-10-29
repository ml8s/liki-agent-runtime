#!/usr/bin/env bash
# Run the exact golangci-lint version pinned for CI. Local and CI must never
# drift between separate binaries or PATH-installed versions.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION_FILE="$ROOT/deps/golangci-lint.version"
GO="${GO:-go}"

version="$(tr -d ' \t\r\n' < "$VERSION_FILE")"
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "invalid golangci-lint version in deps/golangci-lint.version: $version" >&2
  exit 1
fi

if ! command -v "$GO" >/dev/null 2>&1; then
  echo "go is required to run golangci-lint $version" >&2
  exit 127
fi

export GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.10}"
exec "$GO" run \
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@"$version" "$@"
