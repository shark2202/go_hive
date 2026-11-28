#!/usr/bin/env python3
"""Guard tests for the package parity matrix generator."""

from __future__ import annotations

import importlib.util
import json
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "gen_package_parity.py"
ROOT = SCRIPT.parents[2]


def load_generator():
    spec = importlib.util.spec_from_file_location("gen_package_parity", SCRIPT)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PARITY = load_generator()


class PackageParityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.data = PARITY.compute()

    def test_render_is_deterministic(self):
        first = PARITY.render(self.data)
        second = PARITY.render(PARITY.compute())
        self.assertEqual(first, second)
        self.assertEqual(
            json.dumps(self.data, ensure_ascii=False, indent=1, sort_keys=True),
            json.dumps(PARITY.compute(), ensure_ascii=False, indent=1, sort_keys=True),
        )

    def test_every_python_package_is_mapped_once(self):
        names = [p["py"] for p in self.data["pairs"]]
        self.assertEqual(len(names), len(set(names)))
        atlas = json.loads((ROOT / "go_hive/docs/hive-package-graph.json").read_text())
        self.assertEqual(set(names), set(atlas["packages"]))

    def test_every_go_package_is_claimed_or_declared_go_only(self):
        claimed = {g["package"].removeprefix("internal/") for p in self.data["pairs"] for g in p["go"]}
        go_only = {g["name"] for g in self.data["go_only"]}
        self.assertFalse(claimed & go_only)
        gos = PARITY.go_packages()
        self.assertEqual(claimed | go_only, set(gos))

    def test_verdict_counts_match_rows(self):
        counts = {}
        for p in self.data["pairs"]:
            counts[p["verdict"]] = counts.get(p["verdict"], 0) + 1
        self.assertEqual(counts, {k: v for k, v in self.data["summary"]["verdicts"].items() if v})
        self.assertEqual(sum(counts.values()), len(self.data["pairs"]))

    def test_dimension_parser_sees_all_24(self):
        self.assertEqual(len(self.data["dimensions"]), 24)
        self.assertEqual(self.data["dimensions"]["desktop"], "out_of_scope")

    def test_open_gap_anchors_exist(self):
        for gap in self.data["gaps"]:
            if gap["status"] != "open":
                continue
            path = gap["go_anchor"].split(":")[0]
            self.assertTrue((ROOT / path).exists(), gap["go_anchor"])


if __name__ == "__main__":
    unittest.main()
