#!/usr/bin/env python3
"""Static regression tests for candidate manifest generation."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True

_SPEC = importlib.util.spec_from_file_location(
    "build_candidate_manifest",
    Path(__file__).with_name("build_candidate_manifest.py"),
)
module = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(module)


def test_candidate_build_fails_fast_before_expensive_gate() -> None:
    script = Path(__file__).with_name("build-candidate.sh").read_text(encoding="utf-8")
    assert script.index("RELEASE_VERSION=") < script.index("make gate")


def test_candidate_smokes_locally_before_remote_publication() -> None:
    script = Path(__file__).with_name("build-candidate.sh").read_text(encoding="utf-8")
    assert script.index("docker buildx build") < script.index("scripts/smoke-image.sh")
    assert "docker push" not in script
    assert "--push" not in script
    assert script.index("ensure_candidate_registry") < script.index("scripts/smoke-image.sh")
    assert "candidate-publish" not in script
    
class CandidateManifestTest(unittest.TestCase):
    def test_generates_exact_contract(self) -> None:
        manifest = module.generate_manifest(
            registry="127.0.0.1:5000",
            repository="ml8s/liki-agents",
            source_commit="a" * 40,
            candidate_tag="candidate-aaaaaaa-20260101010101",
            agents_digest="sha256:" + "b" * 64,
        )
        self.assertEqual(
            {
                "registry": "127.0.0.1:5000",
                "repository": "ml8s/liki-agents",
                "source_commit": "a" * 40,
                "candidate_tag": "candidate-aaaaaaa-20260101010101",
                "images": {"agents": "sha256:" + "b" * 64},
            },
            manifest,
        )
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "candidate" / "candidate-digests.json"
            module.write_manifest(output, manifest)
            self.assertEqual(json.loads(output.read_text()), manifest)


if __name__ == "__main__":
    unittest.main()
