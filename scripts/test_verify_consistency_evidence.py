#!/usr/bin/env python3
"""Guard tests for the consistency evidence verifier."""

from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "verify_consistency_evidence.py"
ROOT = SCRIPT.parents[2]


def load_verifier():
    spec = importlib.util.spec_from_file_location("verify_consistency_evidence", SCRIPT)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


VERIFIER = load_verifier()


class EvidenceVerifierTests(unittest.TestCase):
    def test_self_test_passes(self):
        self.assertEqual(VERIFIER.self_test(), 0)

    def test_missing_artifact_is_drift(self):
        record = VERIFIER.check_entry({"kind": "artifact", "path": "__missing__"}, {})
        self.assertEqual(record["status"], "drift")
        self.assertTrue(any("missing artifact" in p for p in record["problems"]))

    def test_sha_mismatch_is_drift(self):
        real = ROOT / "go_hive/docs/package-parity.json"
        record = VERIFIER.check_entry(
            {
                "kind": "artifact",
                "path": str(real.relative_to(ROOT)),
                "sha256": "0" * 64,
                "bytes": real.stat().st_size,
            },
            {},
        )
        self.assertEqual(record["status"], "drift")
        self.assertTrue(any("sha256" in p for p in record["problems"]))

    def test_selector_resolution(self):
        data = {"pairs": [{"py": "agent_loop"}, {"py": "server"}]}
        self.assertTrue(VERIFIER.selector_resolves(data, "pairs[py=agent_loop]"))
        self.assertFalse(VERIFIER.selector_resolves(data, "pairs[py=missing]"))

    def test_pick_symbol_prefers_type_and_hint(self):
        rows = [
            {"node": {"language": "go", "kind": "import", "name": "X", "filePath": "hive/x.go", "startLine": 1}},
            {"node": {"language": "go", "kind": "struct", "name": "X", "filePath": "internal/x/x.go", "startLine": 2}},
        ]
        picked = VERIFIER.pick_symbol(rows, "go", "X", "internal/x/")
        self.assertIsNotNone(picked)
        self.assertEqual(picked["kind"], "struct")
        self.assertEqual(picked["line"], 2)

    def test_report_is_ok_with_current_matrix(self):
        if not VERIFIER.MATRIX.is_file():
            self.skipTest("consistency matrix not generated")
        report = VERIFIER.build_report(with_codegraph=False)
        self.assertEqual(report["status"], "ok")
        self.assertEqual(report["totals"]["dangling_refs"], 0)
        self.assertGreater(report["totals"]["evidence"], 0)

    def test_html_render_has_status_and_totals(self):
        report = VERIFIER.build_report(with_codegraph=False)
        page = VERIFIER.render_html(report)
        self.assertIn("Consistency evidence report", page)
        self.assertIn(report["status"].upper(), page)
        self.assertIn("Reproduce", page)


if __name__ == "__main__":
    unittest.main()
