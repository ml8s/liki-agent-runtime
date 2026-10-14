#!/usr/bin/env python3
"""Generate the release candidate digest manifest."""

from __future__ import annotations

import argparse
import json
import re
from pathlib import Path

COMMIT_RE = re.compile(r"^[0-9a-f]{40}$")
TAG_RE = re.compile(r"^candidate-[0-9a-f]{7}-\d{14}$")
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")

def generate_manifest(
    *,
    registry: str,
    repository: str,
    source_commit: str,
    candidate_tag: str,
    agents_digest: str,
) -> dict[str, str | dict[str, str]]:
    """Return the stable candidate manifest representation."""
    if repository != "ml8s/liki-agent-runtime":
        raise ValueError("repository must be ml8s/liki-agent-runtime")
    if not registry or registry.startswith(("http://", "https://")):
        raise ValueError("registry must be a registry host without a scheme")
    if not COMMIT_RE.fullmatch(source_commit):
        raise ValueError("source_commit must be a full 40-character SHA")
    if not TAG_RE.fullmatch(candidate_tag):
        raise ValueError("candidate_tag must use candidate-<7-char-sha>-<YYYYmmddHHMMSS>")
    if not DIGEST_RE.fullmatch(agents_digest):
        raise ValueError("agents image digest must use an immutable sha256 digest")
    return {
        "registry": registry,
        "repository": repository,
        "source_commit": source_commit,
        "candidate_tag": candidate_tag,
        "images": {
            "agents": agents_digest,
        },
    }


def write_manifest(path: Path, manifest: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--registry", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--candidate-tag", required=True)
    parser.add_argument("--agents-digest", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    manifest = generate_manifest(
        registry=args.registry,
        repository=args.repository,
        source_commit=args.source_commit,
        candidate_tag=args.candidate_tag,
        agents_digest=args.agents_digest,
    )
    write_manifest(args.output, manifest)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
