from __future__ import annotations

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MAKEFILE = (ROOT / "Makefile").read_text(encoding="utf-8")


def test_coverage_report_target_emits_html() -> None:
    assert "test-coverage-report:" in MAKEFILE
    assert "-coverprofile=$(COVER_PROFILE)" in MAKEFILE
    assert "tool cover -html=$(COVER_PROFILE)" in MAKEFILE
    assert "-o coverage.html" in MAKEFILE


def test_coverage_artifacts_are_ignored() -> None:
    gitignore = (ROOT / ".gitignore").read_text(encoding="utf-8")
    assert "/coverage.out" in gitignore
    assert "/coverage.html" in gitignore


def test_ci_runs_report_target_and_uploads_html() -> None:
    workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    assert "test-coverage-report" in workflow
    assert "name: go-coverage-html" in workflow
    assert "coverage.html" in workflow
