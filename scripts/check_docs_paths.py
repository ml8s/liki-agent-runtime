#!/usr/bin/env python3
"""Check repo-local file paths referenced in documentation."""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DOC_GLOBS = ("README.md", "CONTRIBUTING.md", "SECURITY.md", "docs/*.md")
BACKTICK = re.compile(r"`([^`\n]+)`")
PATH_PREFIXES = ("internal/", "cmd/", "scripts/", "dev/", "docs/", "tests/")


def referenced_files() -> list[Path]:
    files: set[Path] = set()
    for pattern in DOC_GLOBS:
        files.update(ROOT.glob(pattern))
    return sorted(files)


def main() -> int:
    errors: list[str] = []
    checked = 0
    for path in referenced_files():
        text = path.read_text(encoding="utf-8")
        for number, line in enumerate(text.splitlines(), 1):
            for match in BACKTICK.finditer(line):
                candidate = match.group(1).split()[0].rstrip(".,;:)")
                if not candidate.startswith(PATH_PREFIXES):
                    continue
                checked += 1
                if not (ROOT / candidate).exists():
                    errors.append(
                        f"{path.relative_to(ROOT)}:{number}: missing repo path: {candidate}"
                    )
    if errors:
        print("Invalid documentation paths:", file=sys.stderr)
        print("\n".join(f"  {error}" for error in errors), file=sys.stderr)
        return 1
    print(f"Documentation paths valid ({checked} repo-local references)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
