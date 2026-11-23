#!/usr/bin/env python3
"""Small guard tests for the coverage registry generator."""

from __future__ import annotations

import copy
import importlib.util
import json
import subprocess
import sys
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "gen_coverage_registry.py"
ROOT = SCRIPT.parents[2]


def load_generator():
    spec = importlib.util.spec_from_file_location("gen_coverage_registry", SCRIPT)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


REGISTRY = load_generator()


class CoverageRegistryTests(unittest.TestCase):
    def test_generate_is_deterministic(self):
        first = REGISTRY.generate()
        second = REGISTRY.generate()
        self.assertEqual(REGISTRY.render_json(first), REGISTRY.render_json(second))
        self.assertEqual(REGISTRY.render_markdown(first), REGISTRY.render_markdown(second))

    def test_every_row_has_required_fields(self):
        data = REGISTRY.generate()
        for section in (
            "synthetic_dispatch",
            "events",
            "phase_gates",
            "test_mapping",
            "known_gaps",
        ):
            self.assertTrue(data[section], f"{section} must not be empty")
            for row in data[section]:
                for field in ("id", "upstream_anchor", "go_anchor", "status", "owner"):
                    self.assertIn(field, row, f"{section} row missing {field}: {row}")

    def test_summary_matches_rows(self):
        data = REGISTRY.generate()
        synthetic = data["synthetic_dispatch"]
        self.assertEqual(data["summary"]["synthetic_dispatch"]["total"], len(synthetic))
        self.assertEqual(data["summary"]["known_gaps"]["total"], len(data["known_gaps"]))
        self.assertEqual(
            sum(
                data["summary"]["synthetic_dispatch"][status] for status in ("dispatch_port", "builder_only", "missing")
            ),
            len(synthetic),
        )

    def test_known_gaps_cover_the_registered_findings(self):
        data = REGISTRY.generate()
        ids = {gap["id"] for gap in data["known_gaps"]}
        self.assertEqual(
            ids,
            {
                "gap:suggest_colony_dispatch",
                "gap:task_create_new_colony_pivot",
                "gap:worker_skills_scope",
                "gap:queen_preset_defaults",
                "gap:compaction_long_reflection",
                "gap:memory_cloud_sync_schedule_push",
                "gap:env_ledger_anchor",
                "gap:colony_pivot_lean_handoff",
                "gap:dm_fork_seed_compaction",
            },
        )
        for gap in data["known_gaps"]:
            self.assertIn(gap["status"], {"open", "in_fix", "fixed"})

    def test_diff_detects_an_injected_upstream_row(self):
        old = json.loads(REGISTRY.render_json(REGISTRY.generate()))
        new = copy.deepcopy(old)
        new["synthetic_dispatch"].append(
            {
                "id": "synthetic:fake_probe_tool",
                "tool": "fake_probe_tool",
                "upstream_anchor": "core/framework/agent_loop/agent_loop.py:1",
                "go_anchor": "",
                "status": "missing",
                "owner": "agentloop",
                "notes": "injected by the test",
            }
        )
        new["summary"]["synthetic_dispatch"]["total"] += 1
        new["summary"]["synthetic_dispatch"]["missing"] += 1
        new["summary"]["rows_total"] += 1
        issues = REGISTRY.registry_diff(old, new)
        self.assertTrue(any("synthetic_dispatch: added" in issue for issue in issues))

    def test_check_mode_passes_on_clean_tree(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "--check"],
            cwd=ROOT,
            capture_output=True,
            text=True,
        )
        self.assertEqual(
            result.returncode,
            0,
            f"--check failed:\nstdout={result.stdout}\nstderr={result.stderr}",
        )

    def test_markdown_has_no_absolute_paths(self):
        markdown = REGISTRY.render_markdown(REGISTRY.generate())
        self.assertNotIn(str(ROOT), markdown)
        self.assertNotIn("/Users/", markdown)


if __name__ == "__main__":
    unittest.main()
