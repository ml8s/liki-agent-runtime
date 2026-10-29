#!/usr/bin/env python3
"""Keep local lint and CI lint on one pinned golangci-lint version."""

from __future__ import annotations

import re
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def fail(message: str) -> None:
    raise SystemExit(f"lint toolchain drift: {message}")


def main() -> int:
    version_file = ROOT / "deps" / "golangci-lint.version"
    lint_script = ROOT / "scripts" / "lint.sh"
    workflow = ROOT / ".github" / "workflows" / "ci.yml"
    makefile = ROOT / "Makefile"

    version = version_file.read_text(encoding="utf-8").strip()
    if not re.fullmatch(r"v\d+\.\d+\.\d+", version):
        fail(f"{version_file.relative_to(ROOT)} must contain a semantic version")

    script = lint_script.read_text(encoding="utf-8")
    if 'cmd/golangci-lint@"$version"' not in script:
        fail("scripts/lint.sh must run the pinned golangci-lint module")

    make = makefile.read_text(encoding="utf-8")
    if "scripts/lint.sh fmt --diff ./..." not in make:
        fail("Makefile must format through scripts/lint.sh")
    if "scripts/lint.sh run ./..." not in make:
        fail("Makefile must lint through scripts/lint.sh")

    ci = workflow.read_text(encoding="utf-8")
    if "golangci/golangci-lint-action" in ci:
        fail("CI must use scripts/lint.sh, not a separate golangci-lint action")
    if "make gate" not in ci:
        fail("CI must run the same local gate")

    print(f"✓ golangci-lint toolchain pinned to {version}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
