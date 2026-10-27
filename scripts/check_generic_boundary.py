#!/usr/bin/env python3
"""Fail when product-specific names enter the generic runtime."""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SKIP_DIRS = {".git", "node_modules", "data", "bin"}
SKIP_FILES = {".env"}
SKIP_PATHS = {Path("scripts/check_generic_boundary.py"), Path("docs/A2A_STANDARD_PLAN.md")}
PATTERNS = {
    "product name": re.compile(r"\b(?:liki|Liki)\b"),
    "legacy env prefix": re.compile(r"\bLIKI_[A-Z0-9_]+"),
    "legacy metric prefix": re.compile(r"\bliki_agents_"),
    "private callback": re.compile(r"CALLBACK_(?:URL|SECRET)|hk\.liki\.|Standard Webhooks"),
    "private identity header": re.compile(r"X-Liki-(?:User|Tenant)-ID", re.IGNORECASE),
    "legacy fixture service": re.compile(r"\b(?:Engine|Counsel)\b"),
}


def main() -> int:
    errors: list[str] = []
    checked = 0
    for path in ROOT.rglob("*"):
        if not path.is_file() or any(part in SKIP_DIRS for part in path.parts):
            continue
        relative = path.relative_to(ROOT)
        if str(relative).startswith("coverage") or path.name in SKIP_FILES or relative in SKIP_PATHS:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for number, line in enumerate(text.splitlines(), 1):
            for reason, pattern in PATTERNS.items():
                if pattern.search(line):
                    errors.append(f"{relative}:{number}: {reason}: {line.strip()}")
        checked += 1
    if errors:
        print("Generic runtime boundary violations:", file=sys.stderr)
        print("\n".join(f"  {error}" for error in errors), file=sys.stderr)
        return 1
    print(f"Generic runtime boundary clean ({checked} files)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
