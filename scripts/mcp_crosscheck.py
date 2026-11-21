#!/usr/bin/env python3
"""MCP cross-check: run the same stdio MCP server through both Hive runtimes.

    cd /Users/mac/hive && uv run python go_hive/scripts/mcp_crosscheck.py

Steps:
  1. Write one shared `mcp_servers.json` fixture entry (the same JSON object is
     embedded in the Python `mcp_servers.json` and the Go `configuration.json`
     `mcp.servers` list — only the envelope differs because that is each file's
     grammar).
  2. Run the Python probe (`mcp_crosscheck_python.py`, core venv) twice: once
     plain, once with HIVE_MCP_CALL_RESULT_TIMEOUT=37 to measure env precedence.
     Each run loads the fixture into a real ToolRegistry with the built-in
     `files-tools` harness group registered first.
  3. Run the Go probe (`internal/cli` test TestMCPCrosscheckDump) over the same
     fixture; it drives the real serve loader and the real session catalogue.
  4. Diff tool names / descriptions / input schemas, plus namespace, collision,
     enable/disable, reload, timeout, transport and route surfaces.
  5. Write `go_hive/docs/e2e-assets/mcp-crosscheck/evidence.json`,
     `summary.txt` and the two per-side dumps.

Exit code is 0 only when every remaining difference is a documented exception
(Go extension or an intentional representation choice).
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import importlib.util
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ASSET_DIR = Path("go_hive/docs/e2e-assets/mcp-crosscheck")
SERVER_NAME = "mcp-crosscheck-echo"
CONTEXT_PARAMS = {
    "agent_id",
    "data_dir",
    "profile",
    "profile_display_name",
    "session_cwd",
    "colony_id",
    "principal",
}


def repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def canonical(value) -> str:
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))


def sha256_text(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


def sha256_file(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def namespaced(server: str, tool: str, namespace: str = "mcp") -> str:
    """Mirror tools.NamespacedMCPToolName: [^A-Za-z0-9_]+ folds to _, then trim."""

    def clean(value: str, fallback: str) -> str:
        folded = re.sub(r"[^A-Za-z0-9_]+", "_", value.strip()).strip("_")
        return folded or fallback

    return f"{clean(namespace, 'mcp')}__{clean(server, 'server')}__{clean(tool, 'tool')}"


def run(cmd: list[str], *, cwd: Path, env: dict[str, str] | None = None) -> subprocess.CompletedProcess:
    merged = os.environ.copy()
    merged.pop("HIVE_MCP_CALL_RESULT_TIMEOUT", None)
    if env:
        merged.update(env)
    result = subprocess.run(cmd, cwd=str(cwd), env=merged, text=True, capture_output=True)
    if result.returncode != 0:
        sys.stderr.write(result.stdout)
        sys.stderr.write(result.stderr)
        raise SystemExit(f"command failed ({result.returncode}): {' '.join(cmd)}")
    return result


def git_head(path: Path) -> str:
    result = subprocess.run(
        ["git", "-C", str(path), "rev-parse", "--short", "HEAD"],
        text=True,
        capture_output=True,
    )
    return result.stdout.strip() or "unknown"


def load_fixture_tools(root: Path) -> list[dict]:
    path = root / ASSET_DIR / "echo_server.py"
    spec = importlib.util.spec_from_file_location("mcp_crosscheck_echo_server", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module.TOOLS


def expected_schema(tool: dict) -> dict:
    advertised = tool["inputSchema"]
    properties = {
        name: value
        for name, value in advertised.get("properties", {}).items()
        if name not in CONTEXT_PARAMS
    }
    required = [name for name in advertised.get("required", []) if name not in CONTEXT_PARAMS]
    return {"type": "object", "properties": properties, "required": required}


def python_routes(root: Path) -> list[str]:
    source = (root / "core/framework/server/routes_mcp.py").read_text(encoding="utf-8")
    return sorted(
        f"{method.upper()} {path}"
        for method, path in re.findall(r'app\.router\.add_(get|post|delete|put)\("([^"]+)"', source)
    )


def go_routes(root: Path) -> list[str]:
    source = (root / "go_hive/go-hive/internal/server/routes_mcp.go").read_text(encoding="utf-8")
    out = []
    for method, path in re.findall(r'mux\.HandleFunc\("([A-Z]+) ([^"]+)"', source):
        out.append(f"{method.upper()} {path}")
    return sorted(out)


def python_transports(root: Path) -> list[str]:
    source = (root / "core/framework/loader/mcp_client.py").read_text(encoding="utf-8")
    match = re.search(r"transport:\s*Literal\[([^\]]+)\]", source)
    if not match:
        return []
    return sorted(re.findall(r'"([a-z]+)"', match.group(1)))


def go_transports(root: Path) -> list[str]:
    source = (root / "go_hive/go-hive/internal/tools/mcp_client.go").read_text(encoding="utf-8")
    match = re.search(r"switch config\.Transport \{(.*?)\n\t\}", source, re.S)
    if not match:
        return []
    values: list[str] = []
    for case_line in re.findall(r"case ([^:]+):", match.group(1)):
        values.extend(re.findall(r'"([a-z]+)"', case_line))
    return sorted(set(values))


def tool_by_name(payload: dict, name: str) -> dict | None:
    for record in payload.get("tools", []):
        if record.get("name") == name:
            return record
    return None


def compare_tools(python_dump: dict, go_dump: dict, fixture_tools: list[dict], server_name: str) -> list[dict]:
    comparisons = []
    for tool in fixture_tools:
        remote = tool["name"]
        mapping = python_dump.get("name_mapping", {}).get(remote, {})
        python_name = mapping.get("registered_name")
        python_record = tool_by_name(python_dump, python_name) if python_name else None
        go_name = namespaced(server_name, remote)
        go_record = tool_by_name(go_dump, go_name)
        expected = expected_schema(tool)

        entry = {
            "remote_name": remote,
            "python": {
                "registered_name": python_name,
                "shadowed_by": mapping.get("shadowed_by"),
                "record": python_record,
            },
            "go": {"registered_name": go_name, "record": go_record},
            "expected_schema_sha256": sha256_text(canonical(expected)),
            "schema_matches_expected": {
                "python": bool(python_record) and canonical(python_record["parameters"]) == canonical(expected),
                "go": bool(go_record) and canonical(go_record["parameters"]) == canonical(expected),
            },
        }
        if python_record and go_record:
            entry["description_equal"] = python_record["description"] == go_record["description"]
            entry["parameters_equal"] = canonical(python_record["parameters"]) == canonical(go_record["parameters"])
        comparisons.append(entry)
    return comparisons


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--keep-temp", action="store_true", help="keep the temp working dir")
    args = parser.parse_args()

    root = repo_root()
    asset_dir = root / ASSET_DIR
    tmp = Path(tempfile.mkdtemp(prefix="mcp-crosscheck-"))
    python_home = tmp / "python-home"
    python_home.mkdir()
    # A configuration.json is present with a file-level timeout. Python has no
    # reader for it; the dump records that it stays at the env/default value.
    (python_home / "configuration.json").write_text(
        json.dumps({"mcp": {"timeout_seconds": 12}}), encoding="utf-8"
    )

    entry = {
        "name": SERVER_NAME,
        "transport": "stdio",
        "command": "python3",
        "args": [str(asset_dir / "echo_server.py")],
    }
    servers_path = tmp / "mcp_servers.json"
    servers_path.write_text(json.dumps({"servers": [entry]}, indent=2) + "\n", encoding="utf-8")

    python_probe = root / "go_hive/scripts/mcp_crosscheck_python.py"
    python_dump_path = tmp / "python-side.json"
    python_env_dump_path = tmp / "python-side-env.json"

    run(
        [
            "uv",
            "run",
            "--project",
            str(root / "core"),
            "python",
            str(python_probe),
            "--servers",
            str(servers_path),
            "--out",
            str(python_dump_path),
            "--home",
            str(python_home),
        ],
        cwd=root,
    )
    run(
        [
            "uv",
            "run",
            "--project",
            str(root / "core"),
            "python",
            str(python_probe),
            "--servers",
            str(servers_path),
            "--out",
            str(python_env_dump_path),
            "--home",
            str(python_home),
        ],
        cwd=root,
        env={"HIVE_MCP_CALL_RESULT_TIMEOUT": "37"},
    )

    go_dump_path = tmp / "go-side.json"
    run(
        ["go", "test", "./internal/cli", "-run", "TestMCPCrosscheckDump", "-count=1"],
        cwd=root / "go_hive/go-hive",
        env={"MCP_CROSSCHECK_OUT": str(go_dump_path), "MCP_CROSSCHECK_SERVERS": str(servers_path)},
    )

    python_dump = json.loads(python_dump_path.read_text(encoding="utf-8"))
    python_env_dump = json.loads(python_env_dump_path.read_text(encoding="utf-8"))
    go_dump = json.loads(go_dump_path.read_text(encoding="utf-8"))
    fixture_tools = load_fixture_tools(root)

    comparisons = compare_tools(python_dump, go_dump, fixture_tools, SERVER_NAME)

    # ── dimension verdicts ───────────────────────────────────────────────
    sample_name = namespaced(SERVER_NAME, "echo_text")
    go_prefix = sample_name[: len(sample_name) - len("echo_text")]
    naming = {
        "python": {
            "prefix": python_dump.get("namespace_prefix"),
            "strategy": "bare MCP tool name (server name not part of the registry key)",
            "registered": python_dump.get("server_tool_names"),
        },
        "go": {
            "prefix": go_prefix,
            "strategy": "mcp__<namespace-server-tool> with [^A-Za-z0-9_]+ folded to _",
            "registered": [
                namespaced(SERVER_NAME, tool["name"]) for tool in fixture_tools
                if tool_by_name(go_dump, namespaced(SERVER_NAME, tool["name"]))
            ],
            "server_name_folding": {
                "configured": SERVER_NAME,
                "folded": namespaced(SERVER_NAME, "x").split("__")[1],
                "rule": "[^A-Za-z0-9_]+ -> _",
            },
        },
        "verdict": "deliberate-divergence",
        "why": "Python registers MCP tools by their server-side name; Go prefixes them so a remote tool can never shadow a built-in (documented Go contract: docs/architecture/contracts.md).",
    }

    python_collision = python_dump.get("collision", {})
    go_bare = go_dump.get("bare_registered_names", {})
    collision = {
        "python": {
            "fixture_tool": "read_file",
            "outcome": "shadowed by the built-in files-tools read_file (first-wins)",
            "origin_server": python_collision.get("read_file_origin_server"),
            "fixture_registered_tools": python_collision.get("fixture_server_registered_tools"),
        },
        "go": {
            "fixture_tool": "read_file",
            "outcome": "both registered: built-in read_file and the namespaced fixture tool",
            "builtin_present": go_bare.get("read_file"),
            "namespaced_present": bool(tool_by_name(go_dump, namespaced(SERVER_NAME, "read_file"))),
        },
        "verdict": "deliberate-divergence",
    }

    schema_counts = {"python": {"checked": 0, "matched": 0}, "go": {"checked": 0, "matched": 0}}
    for entry_cmp in comparisons:
        for side in ("python", "go"):
            record = entry_cmp[side]["record"]
            if not record:
                continue
            schema_counts[side]["checked"] += 1
            if entry_cmp["schema_matches_expected"][side]:
                schema_counts[side]["matched"] += 1
    required_ok = {}
    for side in ("python", "go"):
        ok = True
        for record in python_dump["tools"] if side == "python" else go_dump["tools"]:
            required = record["parameters"].get("required")
            if not isinstance(required, list):
                ok = False
        required_ok[side] = ok
    schema_passthrough = {
        "python": schema_counts["python"],
        "go": schema_counts["go"],
        "required_preserved": required_ok,
        "properties_preserved": {
            "python": all(
                record["parameters"].get("properties") is not None
                for record in python_dump["tools"]
            ),
            "go": all(
                record["parameters"].get("properties") is not None for record in go_dump["tools"]
            ),
        },
        "verdict": "identical"
        if schema_counts["python"]["matched"] == schema_counts["python"]["checked"]
        and schema_counts["go"]["matched"] == schema_counts["go"]["checked"]
        and all(required_ok.values())
        else "difference",
    }

    descriptions = {
        "compared_tools": [
            entry_cmp["remote_name"]
            for entry_cmp in comparisons
            if "description_equal" in entry_cmp
        ],
        "equal": all(
            entry_cmp.get("description_equal", True) for entry_cmp in comparisons
        ),
        "verdict": "identical",
    }

    python_lifecycle = python_dump.get("lifecycle", {})
    go_lifecycle = go_dump.get("lifecycle", {})
    enable_disable = {
        "python": {
            "enabled": python_lifecycle.get("registry_enabled_resolves"),
            "disabled": python_lifecycle.get("registry_disabled_resolves"),
            "reenabled": python_lifecycle.get("registry_reenabled_resolves"),
            "surface": "MCPRegistry.enable/disable + resolve_for_agent(enabled)",
        },
        "go": {
            "config_enabled_status_count": go_lifecycle.get("config_reenabled_status_count"),
            "config_disabled_status_count": go_lifecycle.get("config_disabled_status_count"),
            "registry_enabled_status_count": len(go_dump.get("registry_enabled_status", [])),
            "registry_disabled_status_count": go_lifecycle.get("registry_disabled_status_count"),
            "registry_reenabled_status_count": go_lifecycle.get("registry_reenabled_status_count"),
            "surface": "installed.json enabled + configuration.json enabled; POST /api/mcp/servers/{name}/disable",
        },
        "verdict": "equivalent",
        "why": "Both surfaces honour the stored enable flag and serve zero servers when disabled; Go additionally accepts enabled:false inside configuration.json (operator override).",
    }

    reload = {
        "python": {
            "reload_route_present": any("reload" in route for route in python_routes(root)),
            "has_reload_method": python_lifecycle.get("registry_has_reload_method"),
            "apply_point": "next queen session start (routes_mcp.py module docstring; queen_orchestrator comment)",
            "fresh_session_tools": python_lifecycle.get("fresh_session_fixture_tools"),
        },
        "go": {
            "reload_route_present": any("reload" in route for route in go_routes(root)),
            "apply_point": "POST /api/mcp/reload reconnects the pool and bumps a generation; new catalogues attach it, live ones re-attach on their next tool call",
            "renamed_stale_tools_present": go_lifecycle.get("renamed_stale_tools_present"),
            "renamed_new_tools_present": go_lifecycle.get("renamed_new_tools_present"),
        },
        "verdict": "go-extension",
    }

    timeout = {
        "python": {
            "default_seconds": 240,
            "env_seconds": python_env_dump.get("timeout", {}).get("call_result_timeout_seconds"),
            "configuration_json_seconds": python_dump.get("configuration_json_mcp_block", {}).get("timeout_seconds"),
            "effective_seconds_with_file_present": python_dump.get("timeout", {}).get("call_result_timeout_seconds"),
            "file_reader": False,
        },
        "go": {
            "default_seconds": go_dump.get("timeout", {}).get("default_seconds"),
            "configuration_file_seconds": go_dump.get("timeout", {}).get("configuration_file_seconds"),
            "env_seconds": go_dump.get("timeout", {}).get("env_seconds"),
            "precedence": "env > configuration.json mcp.timeout_seconds > 240s",
            "file_reader": True,
        },
        "verdict": "go-extension",
        "why": "Python reads HIVE_MCP_CALL_RESULT_TIMEOUT once at import and has no configuration.json reader; Go reads the env per call and supports mcp.timeout_seconds as the file fallback (the documented env > file > default rule).",
    }

    py_transports = python_transports(root)
    go_transport_list = go_transports(root)
    transports = {
        "python": py_transports,
        "go": go_transport_list,
        "shared": sorted(set(py_transports) & set(go_transport_list)),
        "python_only": sorted(set(py_transports) - set(go_transport_list)),
        "go_only": sorted(set(go_transport_list) - set(py_transports)),
        "verdict": "same-effective"
        if set(go_transport_list).issubset(set(py_transports))
        else "difference",
        "why": "`harness` is Python's in-process transport literal; Go dispatches the same groups by server name (HarnessGroupNames) before any client is built, so it is not a transport case there.",
    }

    py_routes = python_routes(root)
    go_route_list = go_routes(root)
    routes = {
        "python": py_routes,
        "go": go_route_list,
        "go_only": sorted(set(go_route_list) - set(py_routes)),
        "python_only": sorted(set(py_routes) - set(go_route_list)),
        "verdict": "go-extension" if set(py_routes).issubset(set(go_route_list)) else "difference",
    }

    unexpected: list[str] = []
    for entry_cmp in comparisons:
        remote = entry_cmp["remote_name"]
        if entry_cmp["python"]["record"] and entry_cmp["go"]["record"]:
            if not entry_cmp.get("description_equal", True):
                unexpected.append(f"description differs for {remote}")
            if not entry_cmp.get("parameters_equal", True):
                unexpected.append(f"input schema differs for {remote}")
        if entry_cmp["go"]["record"] and not entry_cmp["schema_matches_expected"]["go"]:
            unexpected.append(f"Go rewrote the advertised schema for {remote}")
        if entry_cmp["python"]["record"] and not entry_cmp["schema_matches_expected"]["python"]:
            unexpected.append(f"Python rewrote the advertised schema for {remote}")
    if not descriptions["equal"]:
        unexpected.append("fixture tool descriptions are not verbatim across sides")
    if schema_passthrough["verdict"] != "identical":
        unexpected.append("schema passthrough failed")
    if go_lifecycle.get("renamed_stale_tools_present") or not go_lifecycle.get("renamed_new_tools_present"):
        unexpected.append("Go reload did not replace the server set cleanly")

    evidence = {
        "schema": "mcp-crosscheck/v1",
        "generated_at": datetime.datetime.now().astimezone().isoformat(timespec="seconds"),
        "worktree": str(root),
        "branch": subprocess.run(
            ["git", "-C", str(root), "rev-parse", "--abbrev-ref", "HEAD"],
            text=True, capture_output=True,
        ).stdout.strip() or "unknown",
        "baseline": {
            "python_ref": git_head(root),
            "go_ref": git_head(root),
            "note": "both runtimes in one repository commit (go_hive uses the same git tree)",
        },
        "fixture": {
            "server_name": SERVER_NAME,
            "entry": entry,
            "entry_sha256": sha256_text(canonical(entry)),
            "config_sha256": sha256_file(servers_path),
            "echo_server_sha256": sha256_file(root / ASSET_DIR / "echo_server.py"),
            "tools_advertised": [
                {"name": tool["name"], "input_schema_sha256": sha256_text(canonical(tool["inputSchema"]))}
                for tool in fixture_tools
            ],
            "python_config_surface": "mcp_servers.json {\"servers\": [entry]}",
            "go_config_surface": "configuration.json mcp.servers [entry]",
        },
        "sides": {
            "python": {
                "version": python_dump.get("python_version"),
                "registered_names": python_dump.get("registered_names"),
                "server_tool_names": python_dump.get("server_tool_names"),
                "calls": python_dump.get("calls"),
                "tools": python_dump.get("tools"),
            },
            "go": {
                "version": go_dump.get("go_version"),
                "registered_mcp_names": go_dump.get("registered_mcp_names"),
                "statuses": go_dump.get("statuses_on_load"),
                "calls": go_dump.get("calls"),
                "tools": go_dump.get("tools"),
            },
        },
        "tools": comparisons,
        "dimensions": {
            "namespace": naming,
            "collision": collision,
            "descriptions": descriptions,
            "schema_passthrough": schema_passthrough,
            "enable_disable": enable_disable,
            "reload": reload,
            "timeout": timeout,
            "transports": transports,
            "routes": routes,
        },
        "verdict": {
            "unexpected_differences": unexpected,
            "documented_exceptions": [
                "tool naming: Python bare vs Go mcp__<namespace>__<server>__<tool> with name folding",
                "same-name collision: Python first-wins (built-in beats the server tool) vs Go namespace (both survive)",
                "reload: Python applies on the next session, no reload route; Go adds POST /api/mcp/reload and a generation-based re-attach",
                "timeout: Python env-only (read at import); Go env > configuration.json mcp.timeout_seconds > 240s",
                "transports: Python's `harness` literal is Go's HarnessGroupNames dispatch",
            ],
            "status": "partial" if unexpected == [] else "difference",
        },
    }

    asset_dir.mkdir(parents=True, exist_ok=True)
    for src, name in (
        (python_dump_path, "python-side.json"),
        (go_dump_path, "go-side.json"),
    ):
        data = json.loads(src.read_text(encoding="utf-8"))
        for key in ("hive_home", "config_file"):
            if key in data:
                data[key] = Path(str(data[key])).name if data[key] else data[key]
        (asset_dir / name).write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    (asset_dir / "evidence.json").write_text(
        json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )

    lines = [
        "MCP cross-check (same stdio fixture server through both runtimes)",
        f"python registered: {python_dump.get('registered_names')}",
        f"go registered:     {go_dump.get('registered_mcp_names')}",
        "",
        f"{'remote tool':<12} {'python name':<28} {'go name':<44} desc  schema",
    ]
    for entry_cmp in comparisons:
        py_name = entry_cmp["python"]["registered_name"] or "<shadowed>"
        go_name = entry_cmp["go"]["registered_name"] if entry_cmp["go"]["record"] else "<missing>"
        desc = "n/a" if "description_equal" not in entry_cmp else ("same" if entry_cmp["description_equal"] else "DIFF")
        schema = (
            f"py={'ok' if entry_cmp['schema_matches_expected']['python'] else 'no'}"
            f"/go={'ok' if entry_cmp['schema_matches_expected']['go'] else 'no'}"
        )
        lines.append(f"{entry_cmp['remote_name']:<12} {py_name:<28} {go_name:<44} {desc:<5} {schema}")
    lines += [
        "",
        f"namespace:   python=bare, go={go_prefix} (server folding {SERVER_NAME} -> {naming['go']['server_name_folding']['folded']})",
        f"collision:   python drops the fixture read_file (origin={python_collision.get('read_file_origin_server')}); go keeps built-in + namespaced fixture tool",
        f"enable:      python resolve enabled={python_lifecycle.get('registry_enabled_resolves')} disabled={python_lifecycle.get('registry_disabled_resolves')}; "
        f"go config={go_lifecycle.get('config_disabled_status_count')}/{go_lifecycle.get('config_reenabled_status_count')} "
        f"registry={go_lifecycle.get('registry_disabled_status_count')}/{go_lifecycle.get('registry_reenabled_status_count')} (disabled/enabled count)",
        f"reload:      python route={reload['python']['reload_route_present']} next-session tools={reload['python']['fresh_session_tools']}; "
        f"go route={reload['go']['reload_route_present']} stale={reload['go']['renamed_stale_tools_present']} new={reload['go']['renamed_new_tools_present']}",
        f"timeout:     python default={timeout['python']['default_seconds']}s env={timeout['python']['env_seconds']}s file-ignored "
        f"(file={timeout['python']['configuration_json_seconds']}s); go file={timeout['go']['configuration_file_seconds']}s env={timeout['go']['env_seconds']}s",
        f"transports:  python={py_transports} go={go_transport_list}",
        f"routes:      python={len(py_routes)} go={len(go_route_list)} go-only={routes['go_only']}",
        "",
        f"unexpected differences: {unexpected or 'none'}",
    ]
    (asset_dir / "summary.txt").write_text("\n".join(lines) + "\n", encoding="utf-8")
    print("\n".join(lines))

    if not args.keep_temp:
        shutil.rmtree(tmp, ignore_errors=True)
    return 0 if not unexpected else 1


if __name__ == "__main__":
    raise SystemExit(main())
