#!/usr/bin/env python3
"""Freeze the Python default session tool surface for the Go flat catalogue.

The Go port advertises the Python reference default tool set by default
(``HIVE_TOOL_SURFACE`` unset/``flat``) so the LLM-visible ``tools[]`` carries
the same names as the Python runtime. This script extracts that set from the
frozen contract and writes the small, embeddable manifest the Go catalogue
reads:

    reference layers  llm_request ∪ lifecycle ∪ synthetic
    (24 + 16 + 1 entries -> 39 unique names; ask_user is in llm_request and
    synthetic, write_skill in llm_request and lifecycle)

``mcp_catalog`` is the Tool Library / deferred layer and is NOT part of the
default model request, so its entries are excluded. A feature-gated
``llm_request`` entry (``layer_detail.llm_request.feature_gate``, e.g.
``search_timeline`` behind ``memory_timeline``) is also excluded: the default
surface is the gate-off shape.

Nothing is rewritten: every name/description/input_schema is copied verbatim
from ``contracts/tool-definitions.json`` (the Python side of the freeze).

Usage:
    uv run --no-project python go_hive/scripts/gen_tool_surface_flat.py
    uv run --no-project python go_hive/scripts/gen_tool_surface_flat.py --check

--check exits 1 when the committed file differs from a fresh extraction
(the CI drift check used by the flat-surface lane).
"""
from __future__ import annotations

import hashlib
import json
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CONTRACT = REPO_ROOT / "go_hive/contracts/tool-definitions.json"
OUT = REPO_ROOT / "go_hive/go-hive/internal/cli/tool_surface_flat.json"

DEFAULT_LAYERS = ("llm_request", "lifecycle", "synthetic")


def build() -> dict:
    raw = CONTRACT.read_bytes()
    doc = json.loads(raw)
    tools = []
    layers: dict[str, list[str]] = {name: [] for name in DEFAULT_LAYERS}
    for tool in doc["tools"]:
        # Feature-gated llm_request tools (search_timeline behind the
        # memory_timeline flag) are advertised only when their gate is on; the
        # default surface is the gate-off shape, so they stay out.
        gate = (tool.get("layer_detail") or {}).get("llm_request") or {}
        if gate.get("feature_gate"):
            continue
        present = [layer for layer in DEFAULT_LAYERS if layer in tool.get("layers", [])]
        if not present:
            continue
        for layer in present:
            layers[layer].append(tool["name"])
        tools.append(
            {
                "name": tool["name"],
                "description": tool["description"],
                "input_schema": tool["input_schema"],
                "layers": present,
            }
        )
    tools.sort(key=lambda entry: entry["name"].encode("utf-8"))
    for names in layers.values():
        names.sort(key=lambda name: name.encode("utf-8"))

    reference = doc.get("reference", {})
    return {
        "schema_version": "1.0",
        "kind": "hive-flat-tool-surface",
        "provenance": {
            "purpose": (
                "Python reference default session tool surface advertised by the "
                "Go catalogue (flat names, one tool per action)"
            ),
            "source": "go_hive/contracts/tool-definitions.json",
            "source_sha256": hashlib.sha256(raw).hexdigest(),
            "reference": {
                "name": reference.get("name", ""),
                "package": reference.get("package", ""),
                "version": reference.get("version", ""),
                "commit": reference.get("commit", ""),
                "core_commit": reference.get("core_commit", ""),
                "tools_commit": reference.get("tools_commit", ""),
            },
            "layers": list(DEFAULT_LAYERS),
            "excluded_layers": ["mcp_catalog"],
            "canonical_order": "bytewise by name (Python sorted())",
            "generator": "go_hive/scripts/gen_tool_surface_flat.py",
            "schema_rule": (
                "name/description/input_schema are the reference layer's verbatim values; "
                "the Go side keeps its live schema for P1-frozen tools and for lifecycle "
                "entries the Python Tool Library renders without a schema (layer gap, "
                "see docs/tool-flat-surface.md §3)"
            ),
        },
        "layer_counts": {
            "llm_request": len(layers["llm_request"]),
            "lifecycle": len(layers["lifecycle"]),
            "synthetic": len(layers["synthetic"]),
            "unique": len(tools),
        },
        "layers": {name: layers[name] for name in DEFAULT_LAYERS},
        "tools": tools,
    }


def main(argv: list[str]) -> int:
    payload = build()
    text = json.dumps(payload, indent=1, ensure_ascii=False) + "\n"
    if "--check" in argv:
        current = OUT.read_text(encoding="utf-8") if OUT.exists() else ""
        if current != text:
            print(f"{OUT} is out of date; re-run {pathlib.Path(__file__).name}", file=sys.stderr)
            return 1
        print(f"{OUT} is up to date ({payload['layer_counts']})")
        return 0
    OUT.write_text(text, encoding="utf-8")
    print(f"wrote {OUT} ({payload['layer_counts']})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
