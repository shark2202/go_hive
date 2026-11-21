#!/usr/bin/env python3
"""Offline dependency/toolchain review used when govulncheck cannot run.

This is the honest fallback of ``scripts/vulncheck.sh``: it performs only
checks that need no network and no third-party Python package, and it never
claims that dependency CVE state is known.  The OSV/vuln.go.dev database is
NOT consulted here, so the verdict is ``UNVERIFIED`` by construction.

Checks
  V1  go.mod parses; module path / go directive / toolchain are recorded.
  V2  every required module version has an h1: line in go.sum (append-only
      integrity of the module graph).
  V3  `go mod verify` (offline, uses the local module cache).
  V4  the local Go toolchain version is recorded.
  V5  each built binary's embedded build info (`go version -m`) agrees with
      go.mod: embedded dependencies are a subset of the build list and carry
      the same versions.
  V6  advisory coverage: UNVERIFIED unless --advisories is supplied, and even
      then the report says "snapshot only, not live OSV data".

Exit codes
  0  every check PASS and advisory data was supplied
  1  a local consistency check FAILED
  3  UNVERIFIED: dependency CVE state was not checked (the normal offline
     outcome; callers must not treat this as a pass)

Usage
  python3 scripts/vuln_offline_review.py --module go_hive/go-hive \
      [--module go_hive/browserbridge] [--binary DIR]... --out review.json
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path


def sh(cmd: list[str], cwd: Path | None = None) -> tuple[int, str, str]:
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(cwd) if cwd else None,
            text=True,
            capture_output=True,
            timeout=180,
        )
        return proc.returncode, proc.stdout, proc.stderr
    except FileNotFoundError:
        return 127, "", f"{cmd[0]}: not found"
    except subprocess.TimeoutExpired:
        return 124, "", f"{' '.join(cmd)}: timed out"


def go_env() -> dict[str, str]:
    env = dict(os.environ)
    env.update({"CGO_ENABLED": "0", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=mod"})
    return env


def run_go(args: list[str], cwd: Path) -> tuple[int, str, str]:
    try:
        proc = subprocess.run(
            args,
            cwd=str(cwd),
            text=True,
            capture_output=True,
            timeout=180,
            env=go_env(),
        )
        return proc.returncode, proc.stdout, proc.stderr
    except FileNotFoundError:
        return 127, "", f"{args[0]}: not found"
    except subprocess.TimeoutExpired:
        return 124, "", f"{' '.join(args)}: timed out"


def version_tuple(v: str) -> tuple[int, ...] | None:
    m = re.match(r"go(\d+)\.(\d+)(?:\.(\d+))?", v)
    if not m:
        return None
    return tuple(int(x) for x in m.groups() if x is not None)


def read_go_mod(module_dir: Path) -> tuple[dict, list[dict], list[str]]:
    """Return (header, requirements, notes). Prefer `go mod edit -json`."""
    reqs: list[dict] = []
    header = {"module": "", "go": "", "toolchain": ""}
    notes: list[str] = []
    rc, out, err = run_go(["go", "mod", "edit", "-json"], module_dir)
    if rc == 0:
        try:
            data = json.loads(out)
            header["module"] = data.get("Module", {}).get("Path", "")
            header["go"] = data.get("Go", "") or ""
            header["toolchain"] = data.get("Toolchain", "") or ""
            for item in data.get("Require", []) or []:
                reqs.append(
                    {
                        "path": item.get("Path", ""),
                        "version": item.get("Version", ""),
                        "indirect": bool(item.get("Indirect")),
                    }
                )
            return header, reqs, notes
        except Exception as exc:  # pragma: no cover - defensive
            notes.append(f"go mod edit -json unparsable: {exc}")
    else:
        notes.append(f"go mod edit -json failed (rc={rc}): {err.strip()[:200]}")
    text = (module_dir / "go.mod").read_text(encoding="utf-8")
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("module "):
            header["module"] = line.split(None, 1)[1]
        elif line.startswith("go "):
            header["go"] = line.split(None, 1)[1]
        elif line.startswith("toolchain "):
            header["toolchain"] = line.split(None, 1)[1]
        elif line.startswith("require "):
            parts = line.split()
            if len(parts) >= 3:
                reqs.append({"path": parts[1], "version": parts[2], "indirect": "indirect" in line})
    return header, reqs, notes


def check_go_sum(module_dir: Path, reqs: list[dict]) -> tuple[str, str]:
    sums = module_dir / "go.sum"
    if not reqs:
        return "PASS", "no required modules (nothing to pin)"
    if not sums.exists():
        return "FAIL", f"{len(reqs)} required module(s) but go.sum is missing"
    zip_hash: set[tuple[str, str]] = set()
    mod_hash: set[tuple[str, str]] = set()
    for line in sums.read_text(encoding="utf-8", errors="ignore").splitlines():
        parts = line.split()
        if len(parts) >= 3 and parts[-1].startswith("h1:"):
            if parts[1].endswith("/go.mod"):
                mod_hash.add((parts[0], parts[1][: -len("/go.mod")]))
            else:
                zip_hash.add((parts[0], parts[1]))
    missing = [
        f"{r['path']} {r['version']}"
        for r in reqs
        if (r["path"], r["version"]) not in zip_hash and (r["path"], r["version"]) not in mod_hash
    ]
    if missing:
        return "FAIL", "no go.sum entry for " + ", ".join(missing[:5]) + (
            f" (+{len(missing) - 5} more)" if len(missing) > 5 else ""
        )
    go_mod_only = [
        f"{r['path']}" for r in reqs if (r["path"], r["version"]) not in zip_hash
    ]
    if go_mod_only:
        return (
            "PASS",
            f"{len(reqs)} required module(s) pinned; go.mod-hash-only (source not needed in the default build): "
            + ", ".join(go_mod_only[:5]),
        )
    return "PASS", f"{len(reqs)} required module(s) all have h1: zip entries"


def check_binary(module_dir: Path, header: dict, reqs: list[dict], binary: Path) -> dict:
    rc, out, err = run_go(["go", "version", "-m", str(binary)], module_dir)
    if rc != 0:
        return {"binary": str(binary), "status": "FAIL", "detail": f"go version -m failed: {err.strip()[:200]}"}
    go_version = ""
    first = out.splitlines()[0] if out else ""
    m = re.search(r": (go[0-9][^\s]*)", first)
    if m:
        go_version = m.group(1)
    deps = []
    for line in out.splitlines():
        parts = line.strip().split("\t")
        if len(parts) >= 3 and parts[0] == "dep":
            deps.append({"path": parts[1], "version": parts[2]})
    req_map = {r["path"]: r["version"] for r in reqs}
    mismatched = [
        d for d in deps if d["path"] in req_map and req_map[d["path"]] != d["version"]
    ]
    extra = [d for d in deps if d["path"] not in req_map]
    local_go = ""
    _, go_out, _ = run_go(["go", "env", "GOVERSION"], module_dir)
    local_go = go_out.strip()
    status = "PASS"
    detail = f"go {go_version or '?'}; {len(deps)} embedded dep(s), all match go.mod"
    if mismatched:
        status = "FAIL"
        detail = "embedded dep version mismatch: " + ", ".join(
            f"{d['path']} {d['version']} != {req_map[d['path']]}" for d in mismatched[:3]
        )
    elif extra:
        status = "WARN"
        detail = f"embedded deps not in go.mod: " + ", ".join(f"{d['path']} {d['version']}" for d in extra[:3])
    if go_version and local_go and go_version != local_go:
        note = f"binary built with {go_version}, local toolchain {local_go}"
        detail = f"{detail}; {note}"
        if status == "PASS":
            status = "WARN"
    return {
        "binary": str(binary),
        "status": status,
        "detail": detail,
        "go_version": go_version,
        "deps": deps,
    }


def load_advisories(path: str | None) -> tuple[list[dict], str]:
    if not path:
        return [], "no advisory snapshot supplied"
    try:
        data = json.loads(Path(path).read_text(encoding="utf-8"))
    except Exception as exc:
        return [], f"advisory snapshot unreadable: {exc}"
    entries = data.get("advisories", data if isinstance(data, list) else [])
    return entries, f"snapshot {path} with {len(entries)} entries (snapshot only, not live OSV)"


def main() -> int:
    parser = argparse.ArgumentParser(description="Offline Go dependency/toolchain review (no CVE coverage)")
    parser.add_argument("--module", action="append", required=True, help="Go module directory (repeatable)")
    parser.add_argument("--binary", action="append", default=[], help="directory with built binaries (repeatable)")
    parser.add_argument("--advisories", help="optional JSON advisory snapshot")
    parser.add_argument("--out", help="write the JSON report here")
    args = parser.parse_args()

    checks: list[dict] = []
    modules: list[dict] = []
    binaries: list[dict] = []
    advisories, adv_note = load_advisories(args.advisories)

    def add(cid: str, title: str, status: str, detail: str) -> None:
        checks.append({"id": cid, "title": title, "status": status, "detail": detail})

    for raw in args.module:
        module_dir = Path(raw).resolve()
        if not (module_dir / "go.mod").exists():
            add("V1", f"{raw}: go.mod", "FAIL", f"no go.mod at {module_dir}")
            continue
        header, reqs, notes = read_go_mod(module_dir)
        modules.append({"dir": str(module_dir), "header": header, "requires": reqs})
        add(
            "V1",
            f"{header.get('module') or raw}: go.mod parsed",
            "PASS" if header.get("module") else "FAIL",
            f"module={header.get('module') or '?'} go={header.get('go') or '?'} "
            f"toolchain={header.get('toolchain') or '(unset)'} requires={len(reqs)}"
            + (f" ({'; '.join(notes)})" if notes else ""),
        )
        status, detail = check_go_sum(module_dir, reqs)
        add("V2", f"{header.get('module') or raw}: go.sum pins", status, detail)
        rc, out, err = run_go(["go", "mod", "verify"], module_dir)
        add(
            "V3",
            f"{header.get('module') or raw}: go mod verify",
            "PASS" if rc == 0 else "FAIL",
            (out.strip() or err.strip() or f"rc={rc}")[:400],
        )
        _, go_out, _ = run_go(["go", "env", "GOVERSION"], module_dir)
        add("V4", f"{header.get('module') or raw}: toolchain", "INFO", go_out.strip() or "unknown")
        for bdir in args.binary:
            base = Path(bdir)
            if not base.exists():
                add("V5", f"{base}: binary dir", "SKIP", "directory does not exist")
                continue
            for name in ("hive", "hive-cred", "hive-browser", "browserbridge"):
                candidate = base / name
                if not candidate.exists():
                    continue
                result = check_binary(module_dir, header, reqs, candidate)
                binaries.append(result)
                add("V5", f"{name}: embedded build info", result["status"], result["detail"])

    # CVE coverage is the one thing this script cannot do offline.
    if advisories:
        known = {(a.get("module"), a.get("version")) for a in advisories}
        hit = [
            m
            for m in modules
            for r in m["requires"]
            if (r["path"], r["version"]) in known
        ]
        add(
            "V6",
            "advisory snapshot check",
            "FAIL" if hit else "PASS",
            f"{adv_note}; matching pinned versions: {len(hit)}",
        )
    else:
        add(
            "V6",
            "advisory / CVE coverage",
            "UNVERIFIED",
            "no OSV/vuln.go.dev data consulted: dependency CVE state is UNKNOWN "
            "(run scripts/vulncheck.sh with govulncheck for real coverage)",
        )

    failed = [c for c in checks if c["status"] == "FAIL"]
    unverified = [c for c in checks if c["status"] == "UNVERIFIED"]
    verdict = "FAIL" if failed else ("UNVERIFIED" if unverified else "PASS")
    report = {
        "schema_version": 1,
        "tool": "go_hive/scripts/vuln_offline_review.py",
        "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "verdict": verdict,
        "checks": checks,
        "modules": modules,
        "binaries": binaries,
        "unverified": [
            "Dependency and Go stdlib CVE state: not checked offline (no OSV database, no govulncheck)."
        ]
        if unverified
        else [],
    }
    for c in checks:
        print(f"[{c['status']:>10}] {c['id']} {c['title']} — {c['detail']}")
    print("")
    print("UNVERIFIED: no CVE/vulnerability database was consulted (offline mode).")
    print("This report is a consistency review, NOT a vulnerability scan pass.")
    if args.out:
        Path(args.out).parent.mkdir(parents=True, exist_ok=True)
        Path(args.out).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(f"report: {args.out}")
    if failed:
        return 1
    if unverified:
        return 3
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
