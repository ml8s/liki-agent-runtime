#!/usr/bin/env python3
"""Validate workflow references and signing-token policy."""

from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
USES_RE = re.compile(r"^\s*(?:-\s+)?uses:\s*(\S+)")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")


def main() -> int:
    errors: list[str] = []
    patterns = [
        ROOT / ".github" / "workflows",
        ROOT / ".github" / "actions",
    ]
    files: list[Path] = []
    for pattern in patterns:
        if pattern.is_dir():
            files.extend(pattern.rglob("*.yml"))
            files.extend(pattern.rglob("*.yaml"))
    for path in sorted(set(files)):
        try:
            document = yaml.safe_load(path.read_text(encoding="utf-8"))
        except yaml.YAMLError as error:
            errors.append(f"{path.relative_to(ROOT)}: invalid YAML: {error}")
            continue
        for job_id, job in (document.get("jobs") or {}).items():
            if not isinstance(job, dict):
                continue
            for step in job.get("steps") or []:
                if not isinstance(step, dict) or not isinstance(step.get("run"), str):
                    continue
                run_script = step["run"]
                if re.search(r"--identity-token\s+[\"']?\$RUNNER_TEMP/", run_script):
                    errors.append(
                        f"{path.relative_to(ROOT)}: job {job_id!r} passes an OIDC token "
                        "file path to cosign; consume the token string in the same step"
                    )
                if '--identity-token "$oidc_token"' in run_script and not re.search(
                    r"oidc_token=\"\$\(.*\)\"", run_script, flags=re.DOTALL
                ):
                    errors.append(
                        f"{path.relative_to(ROOT)}: job {job_id!r} uses oidc_token "
                        "without fetching it in the same shell step"
                    )
        for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            match = USES_RE.match(line)
            if not match:
                continue
            reference = match.group(1)
            if reference.startswith("./") or reference.startswith("docker://"):
                continue
            ref = reference.rsplit("@", 1)[-1]
            if not SHA_RE.fullmatch(ref):
                errors.append(f"{path.relative_to(ROOT)}:{number}: mutable action reference: {reference}")
            if reference.rsplit("@", 1)[0] == "docker/build-push-action":
                try:
                    document = yaml.safe_load(path.read_text(encoding="utf-8"))
                except yaml.YAMLError:
                    continue
                job_id = next(
                    (job for job, config in (document.get("jobs") or {}).items()
                     if any(
                         isinstance(step, dict)
                         and step.get("uses") == reference
                         and isinstance(step.get("with"), dict)
                         and "id" in step["with"]
                         for step in config.get("steps") or []
                     )),
                    None,
                )
                if job_id is not None:
                    errors.append(
                        f"{path.relative_to(ROOT)}:{number}: docker/build-push-action "
                        "id must be step-level, not an action input"
                    )
    if errors:
        print("Unpinned GitHub Actions found:", file=sys.stderr)
        print("\n".join(f"  {error}" for error in errors), file=sys.stderr)
        return 1
    print("GitHub Actions are SHA-pinned")
    return 0


if __name__ == "__main__":
    sys.exit(main())
