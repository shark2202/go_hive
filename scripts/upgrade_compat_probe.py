#!/usr/bin/env python3
"""Upgrade-compat gate helper: fixture credential seeding and verdict checks.

Companion of ``go_hive/scripts/upgrade_compat_check.sh`` (the orchestration
driver). The driver runs one *case* per legacy ``HIVE_HOME`` fixture
(``v1`` / ``v2`` / ``v3``) and records, for each case:

  case.json                  case context (fixture, homes, binary provenance)
  s0..s5.json                whole-home manifests (via g2_rollback_probe.py)
  d_<step>.json              manifest diffs (via g2_rollback_probe.py)
  probes/<step>/<slug>.http  first line = HTTP status, rest = body
  old-cold.json etc.         CLI reads of the *old* binary

Subcommands
-----------

``seed-credentials --home H [--credential-id ID]``
    Write one Fernet-encrypted credential into a fixture home using the
    *Python reference* implementation (``core/framework``), so the fixture
    carries the same credential shape a real Python-era install had. Run it
    from ``core/`` with the project venv (``uv run python …``). When the venv
    or ``cryptography`` is unavailable the driver records a SKIP for the
    credentials check instead of failing the case — never a silent pass.

``verify --work DIR --out FILE [--label LABEL]``
    Evaluate every case under ``<work>/cases/*/`` and write the gate report
    JSON. Prints one line per check plus a summary; exits 1 when any check
    FAILs. SKIP entries are listed with their reason.

Rules (each becomes a check row; ``hard`` rules fail the gate):

  M1  new `serve` #1 health == 200
  M2  new `serve` #1 read APIs 200 (history / discover / credentials / colony)
  M3  read APIs expose the migrated fixture sessions (id + last message)
  M4  read APIs expose the migrated colony
  M5  credentials metadata lists the seeded credential (SKIP if unseeded)
  M6  migration markers match the fixture contract after `serve` #1
  M7  `serve` #1 does not modify user data (modified ⊆ churn allowlist)
  M8  every file removed by `serve` #1 has its content conserved in the
      after-tree (sha256 for plain files, SQLite logical digest for *.db)
  M9  `serve` #1 leaves no same-path SQLite DB with a changed logical digest
  M10 new `serve` #2 is idempotent: 0 added / 0 removed / no marker change;
      modified ⊆ churn allowlist; 0 sqlite changes
  M11 old binary CLI reads exit 0 and write nothing (0 added/removed/modified)
  M12 old binary still reads the migrated sessions/colonies
  M13 old binary `serve` on the migrated home: health 200 + no user-data
      writes (the user rolled back to the previous release)

Churn allowlist (documented, benign, not user data): serve logs, the serve
marker, SQLite WAL sidecars, the developer-readable credential index and the
per-session summary cache.
"""

from __future__ import annotations

import argparse
import fnmatch
import hashlib
import json
import sys
import tempfile
from pathlib import Path

CHURN_PATTERNS = [
    "logs/*",
    "*/logs/*",
    "maintenance/server.lock",
    "*.db-wal",
    "*.db-shm",
    "*.db-journal",
    "credentials/metadata/index.json",
    "*/summary.json",
    "summary.json",
]


def is_churn(rel: str) -> bool:
    return any(fnmatch.fnmatch(rel, pattern) for pattern in CHURN_PATTERNS)


def read_json(path: Path):
    return json.loads(path.read_text(encoding="utf-8"))


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


# ---------------------------------------------------------------------------
# check result bookkeeping
# ---------------------------------------------------------------------------


class CaseReport:
    def __init__(self, case: str) -> None:
        self.case = case
        self.checks: list[dict] = []

    def add(self, cid: str, title: str, status: str, detail: str, evidence: str = "") -> None:
        self.checks.append({"id": cid, "title": title, "status": status, "detail": detail, "evidence": evidence})

    def passed(self, cid: str, title: str, detail: str, evidence: str = "") -> None:
        self.add(cid, title, "PASS", detail, evidence)

    def failed(self, cid: str, title: str, detail: str, evidence: str = "") -> None:
        self.add(cid, title, "FAIL", detail, evidence)

    def skipped(self, cid: str, title: str, reason: str) -> None:
        self.add(cid, title, "SKIP", reason)

    @property
    def verdict(self) -> str:
        return "FAIL" if any(c["status"] == "FAIL" for c in self.checks) else "PASS"

    def summary(self) -> dict:
        return {
            "case": self.case,
            "verdict": self.verdict,
            "pass": sum(1 for c in self.checks if c["status"] == "PASS"),
            "fail": sum(1 for c in self.checks if c["status"] == "FAIL"),
            "skip": sum(1 for c in self.checks if c["status"] == "SKIP"),
            "checks": self.checks,
        }


def load_http(path: Path) -> tuple[int | None, object]:
    """Parse a probe capture: first line ``status=<code>`` then the body."""
    if not path.exists():
        return None, None
    text = path.read_text(encoding="utf-8", errors="replace")
    status, _, body = text.partition("\n")
    code = None
    if status.startswith("status="):
        try:
            code = int(status.split("=", 1)[1].strip())
        except ValueError:
            code = None
    try:
        return code, json.loads(body)
    except json.JSONDecodeError:
        return code, body


def diff_paths(diff: dict) -> tuple[list[dict], list[dict], list[dict]]:
    return diff.get("added", []), diff.get("removed", []), diff.get("modified", [])


# ---------------------------------------------------------------------------
# seed-credentials
# ---------------------------------------------------------------------------


def cmd_seed_credentials(args) -> int:
    home = Path(args.home).resolve()
    key_path = home / "secrets" / "credential_key"
    key = args.key or "MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDA="
    key_path.parent.mkdir(parents=True, exist_ok=True)
    if not key_path.exists():
        key_path.write_text(key, encoding="utf-8")
    key = key_path.read_text(encoding="utf-8").strip()

    core = Path(__file__).resolve().parents[2] / "core"
    if str(core) not in sys.path:
        sys.path.insert(0, str(core))
    try:
        import pydantic  # noqa: F401
        from framework.credentials.models import CredentialKey, CredentialObject, CredentialType
        from framework.credentials.storage import EncryptedFileStorage
    except ImportError as exc:
        print(json.dumps({"seeded": False, "reason": f"python reference unavailable: {exc}"}))
        return 0

    from pydantic import SecretStr

    storage = EncryptedFileStorage(home / "credentials", encryption_key=key.encode())
    credential = CredentialObject(
        id=args.credential_id,
        name="Upgrade compat drill key",
        credential_type=CredentialType.API_KEY,
        provider_id="upgrade_compat",
        auto_refresh=False,
        description="seeded by upgrade_compat_probe.py for the upgrade-compat gate",
        tags=["upgrade-compat"],
        keys={"api_key": CredentialKey(name="api_key", value=SecretStr("sk-upgrade-compat"))},
    )
    storage.save(credential)
    print(json.dumps({"seeded": True, "credential_id": args.credential_id, "ids": sorted(storage.list_all())}))
    return 0


# ---------------------------------------------------------------------------
# verify
# ---------------------------------------------------------------------------


def check_new_serve(
    report: CaseReport,
    case: dict,
    ctx: dict,
    step: str,
    ids: tuple[str, str, str] | None,
    expect_markers: dict,
    idempotent: bool,
) -> None:
    """Step checks for one `serve` run.

    ``ids`` is (health, read-apis, markers) for the first startup; the second
    startup reuses those rows with its own labels (``None`` -> M10-* ids).
    """
    probes = ctx["probes"].get(step, {})
    if ids is None:
        health_id, api_id, marker_id = "M10-1", "M10-2", "M10-3"
    else:
        health_id, api_id, marker_id = ids
    health = probes.get("health", {}).get("status")
    if health == 200:
        report.passed(health_id, f"{step}: health 200", f"HTTP {health}", ctx["evidence"].get(step, ""))
    else:
        report.failed(health_id, f"{step}: health 200", f"HTTP {health}", ctx["evidence"].get(step, ""))

    api_failures = []
    for slug, entry in probes.items():
        if slug == "health":
            continue
        if entry.get("status") != 200:
            api_failures.append(f"{slug}={entry.get('status')}")
    if api_failures:
        report.failed(api_id, f"{step}: read APIs 200", ", ".join(api_failures), ctx["evidence"].get(step, ""))
    else:
        listed = ", ".join(sorted(k for k in probes if k != "health")) or "health only"
        report.passed(api_id, f"{step}: read APIs 200", listed, ctx["evidence"].get(step, ""))

    if not idempotent:
        # M3: sessions readable after the upgrade (content survives).
        history = probes.get("sessions_history", {}).get("body")
        expect_ids = case.get("expect_cold_sessions", [])
        if isinstance(history, dict) and isinstance(history.get("sessions"), list):
            by_id = {s.get("session_id"): s for s in history["sessions"]}
            missing = [sid for sid in expect_ids if sid not in by_id]
            if missing:
                report.failed("M3", "migrated sessions visible via GET /api/sessions/history", f"missing {missing}; got {sorted(by_id)}", "probes/new1/sessions_history.http")
            else:
                detail = "; ".join(f"{sid}: message_count={by_id[sid].get('message_count')} last_message={by_id[sid].get('last_message')!r}" for sid in expect_ids)
                report.passed("M3", "migrated sessions visible via GET /api/sessions/history", detail, "probes/new1/sessions_history.http")
        else:
            report.failed("M3", "migrated sessions visible via GET /api/sessions/history", f"unreadable payload: {history!r}"[:200], "probes/new1/sessions_history.http")

        # M4: colony visible through GET /api/discover.
        expect_colony = case.get("expect_colony")
        discover = probes.get("discover", {}).get("body")
        if expect_colony:
            if isinstance(discover, (dict, list)) and expect_colony in json.dumps(discover):
                report.passed("M4", "migrated colony visible via GET /api/discover", f"colony {expect_colony!r} present", "probes/new1/discover.http")
            else:
                report.failed("M4", "migrated colony visible via GET /api/discover", f"colony {expect_colony!r} not found", "probes/new1/discover.http")

        # M5: credential metadata readable (SKIP when seeding was not possible).
        if case.get("seeded_credential_id"):
            creds = probes.get("credentials", {}).get("body")
            cred_ids = [c.get("credential_id") for c in creds.get("credentials", [])] if isinstance(creds, dict) else []
            if case["seeded_credential_id"] in cred_ids:
                report.passed("M5", "credential metadata readable via GET /api/credentials", f"ids={cred_ids}", "probes/new1/credentials.http")
            else:
                report.failed("M5", "credential metadata readable via GET /api/credentials", f"{case['seeded_credential_id']} missing; ids={cred_ids}", "probes/new1/credentials.http")
        else:
            report.skipped("M5", "credential metadata readable via GET /api/credentials", case.get("credential_seed_reason") or "no credential seeded")

    # M6: migration markers.
    snap_after = Path(ctx["snap"][step])
    markers_after = read_json(snap_after).get("markers", {})
    mismatch = {k: {"want": v, "got": markers_after.get(k)} for k, v in expect_markers.items() if markers_after.get(k) != v}
    if mismatch:
        report.failed(marker_id, f"{step}: migration markers", json.dumps(mismatch), str(snap_after))
    else:
        report.passed(marker_id, f"{step}: migration markers", json.dumps(expect_markers, sort_keys=True), str(snap_after))


def check_diff(report: CaseReport, cid: str, title: str, diff_file: Path, *, allow_added: bool, allow_removed: bool, allow_mod_allowlist: bool = True) -> dict:
    diff = read_json(diff_file)
    added, removed, modified = diff_paths(diff)
    sqlite_changed = diff.get("sqlite_changes", {})
    marker_changes = diff.get("marker_changes", {})
    problems = []
    if added and not allow_added:
        problems.append(f"added={len(added)} (first: {added[0]['path']})")
    if removed and not allow_removed:
        problems.append(f"removed={len(removed)} (first: {removed[0]['path']})")
    bad_mod = [m["path"] for m in modified if not (allow_mod_allowlist and is_churn(m["path"]))]
    if bad_mod:
        problems.append(f"user-data modified={bad_mod}")
    if sqlite_changed:
        problems.append(f"sqlite logical changes={list(sqlite_changed)}")
    if marker_changes:
        problems.append(f"marker changes={marker_changes}")
    detail = (
        f"added={len(added)} removed={len(removed)} modified={len(modified)} "
        f"sqlite_changed={len(sqlite_changed)} marker_changes={len(marker_changes)}"
    )
    if problems:
        report.failed(cid, title, detail + " | " + "; ".join(problems), str(diff_file))
    else:
        report.passed(cid, title, detail, str(diff_file))
    return diff


def cmd_verify(args) -> int:
    work = Path(args.work).resolve()
    cases_dir = work / "cases"
    reports = []
    for case_dir in sorted(p for p in cases_dir.iterdir() if p.is_dir()):
        case_file = case_dir / "case.json"
        if not case_file.exists():
            continue
        case = read_json(case_file)
        report = CaseReport(case_dir.name)
        snapshots = case["snapshots"]
        ctx = {
            "snap": {"new1": snapshots["s1"], "new2": snapshots["s2"], "old_serve": snapshots["s5"]},
            "probes": {},
            "evidence": {},
        }
        probes_root = case_dir / "probes"
        for step_dir in sorted(probes_root.iterdir()) if probes_root.is_dir() else []:
            if not step_dir.is_dir():
                continue
            step_probes = {}
            for f in sorted(step_dir.glob("*.http")):
                code, body = load_http(f)
                step_probes[f.stem] = {"status": code, "body": body}
            ctx["probes"][step_dir.name] = step_probes
            ctx["evidence"][step_dir.name] = str(step_dir)

        # ---- step 1: new binary migrates / opens the old home -------------
        check_new_serve(report, case, ctx, "new1", ("M1", "M2", "M6"), case["expect_markers_after_new"], idempotent=False)

        # ---- M7: no user-data modification, M8: content conservation, ---
        # ---- M9: no same-path SQLite logical change                     --
        d_new1 = read_json(Path(case["diffs"]["new1"]))
        added, removed, modified = diff_paths(d_new1)
        bad_mod = [m["path"] for m in modified if not is_churn(m["path"])]
        if bad_mod:
            report.failed("M7", "serve #1 does not modify user data", f"modified={bad_mod}", case["diffs"]["new1"])
        else:
            report.passed("M7", "serve #1 does not modify user data", f"modified={len(modified)} (churn only)" if modified else "modified=0", case["diffs"]["new1"])

        s0 = read_json(Path(case["snapshots"]["s0"]))
        s1 = read_json(Path(case["snapshots"]["s1"]))
        added_hashes = {s1["files"][p].get("sha256") for p in s1["files"] if p not in s0["files"]}
        added_sqlite_digests = [
            json.dumps(v, sort_keys=True) for p, v in s1.get("sqlite", {}).items() if p not in s0.get("sqlite", {})
        ]
        unconserved = []
        for entry in removed:
            path = entry["path"]
            if path.endswith(".db"):
                before_digest = json.dumps(s0.get("sqlite", {}).get(path, {}), sort_keys=True)
                if before_digest not in added_sqlite_digests:
                    unconserved.append(path)
            elif s0["files"][path].get("sha256") not in added_hashes:
                unconserved.append(path)
        if unconserved:
            report.failed("M8", "serve #1 removed files are content-conserved (moves, not deletions)", f"no matching content in the after-tree: {unconserved}", case["diffs"]["new1"])
        else:
            report.passed("M8", "serve #1 removed files are content-conserved (moves, not deletions)", f"{len(removed)} removed path(s) all re-found by content", case["diffs"]["new1"])

        same_path_sqlite = [
            p for p in d_new1.get("sqlite_changes", {}) if p in s0.get("sqlite", {}) and p in s1.get("sqlite", {})
        ]
        if same_path_sqlite:
            report.failed("M9", "serve #1 leaves no existing SQLite DB logically changed", f"changed in place: {same_path_sqlite}", case["diffs"]["new1"])
        else:
            report.passed("M9", "serve #1 leaves no existing SQLite DB logically changed", "0 same-path logical changes", case["diffs"]["new1"])

        # ---- step 2: second startup idempotency --------------------------
        if "new2" in ctx["probes"]:
            check_new_serve(report, case, ctx, "new2", None, case["expect_markers_after_new2"], idempotent=True)
            check_diff(
                report,
                "M10-4",
                "serve #2 is idempotent (no migrations, no user-data writes)",
                Path(case["diffs"]["new2"]),
                allow_added=False,
                allow_removed=False,
            )

        # ---- step 3: old binary read-back --------------------------------
        old_read = case.get("old_read", {})
        if old_read:
            bad = {k: v for k, v in old_read.items() if k.startswith("rc_") and v != 0}
            if bad:
                report.failed("M11-1", "old binary CLI reads exit 0", json.dumps(bad), old_read.get("evidence", ""))
            else:
                report.passed("M11-1", "old binary CLI reads exit 0", json.dumps({k: v for k, v in old_read.items() if k.startswith("rc_")}), old_read.get("evidence", ""))
            check_diff(
                report,
                "M11-2",
                "old binary read-back writes nothing (snapshot diff 0/0/0)",
                Path(case["diffs"]["old_read"]),
                allow_added=False,
                allow_removed=False,
                allow_mod_allowlist=False,
            )
            cold = case.get("old_cold_sessions")
            if case.get("expect_cold_sessions"):
                if cold is None:
                    report.failed("M12", "old binary still sees the migrated sessions", "cold session list unreadable", old_read.get("cold_evidence", ""))
                else:
                    missing = [sid for sid in case["expect_cold_sessions"] if sid not in cold]
                    if missing:
                        report.failed("M12", "old binary still sees the migrated sessions", f"missing {missing}; got {cold}", old_read.get("cold_evidence", ""))
                    else:
                        report.passed("M12", "old binary still sees the migrated sessions", f"cold sessions={cold}", old_read.get("cold_evidence", ""))
            colony_names = case.get("old_colonies")
            if case.get("expect_colony") and colony_names is not None:
                if case["expect_colony"] in colony_names:
                    report.passed("M12-2", "old binary still sees the migrated colonies", f"colonies={colony_names}", old_read.get("colony_evidence", ""))
                else:
                    report.failed("M12-2", "old binary still sees the migrated colonies", f"{case['expect_colony']} missing; got {colony_names}", old_read.get("colony_evidence", ""))

        # ---- step 4: old binary serve (rollback) -------------------------
        if "old_serve" in ctx["probes"]:
            health = ctx["probes"]["old_serve"].get("health", {}).get("status")
            if health == 200:
                report.passed("M13-1", "old binary serve on the migrated home: health 200", f"HTTP {health}", ctx["evidence"]["old_serve"])
            else:
                report.failed("M13-1", "old binary serve on the migrated home: health 200", f"HTTP {health}", ctx["evidence"]["old_serve"])
            check_diff(
                report,
                "M13-2",
                "old binary serve on the migrated home writes no user data",
                Path(case["diffs"]["old_serve"]),
                allow_added=False,
                allow_removed=False,
            )
        reports.append(report.summary())
        reports[-1]["fixture"] = case["fixture"]
        reports[-1]["home"] = case["home"]
        reports[-1]["v3_prepared_by"] = case.get("v3_prepared_by")
        reports[-1]["old_version"] = case.get("old", {}).get("version")
        reports[-1]["new_version"] = case.get("new", {}).get("version")

    total_pass = sum(r["pass"] for r in reports)
    total_fail = sum(r["fail"] for r in reports)
    total_skip = sum(r["skip"] for r in reports)
    payload = {
        "schema_version": 1,
        "tool": "go_hive/scripts/upgrade_compat_probe.py verify",
        "label": args.label,
        "work": str(work),
        "old": args.old or None,
        "new": args.new or None,
        "cases": reports,
        "summary": {"cases": len(reports), "pass": total_pass, "fail": total_fail, "skip": total_skip},
        "verdict": "FAIL" if total_fail else ("PASS" if reports else "FAIL"),
    }
    if not reports:
        payload["verdict"] = "FAIL"
        payload["error"] = f"no cases found under {cases_dir}"

    out_path = Path(args.out) if args.out else work / "upgrade-compat-report.json"
    out_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    for report in reports:
        extra = f" v3_prepared_by={report.get('v3_prepared_by')}" if report.get("v3_prepared_by") else ""
        print(f"--- case {report['case']} (fixture {report.get('fixture')}){extra}")
        for check in report["checks"]:
            mark = {"PASS": "PASS", "FAIL": "FAIL", "SKIP": "SKIP"}[check["status"]]
            print(f"[{mark}] {report['case']}/{check['id']} {check['title']} — {check['detail']}")
    skip_note = f" ({total_skip} checks SKIPPED — not verified)" if total_skip else ""
    print(
        f"upgrade-compat: {len(reports)} case(s) pass={total_pass} fail={total_fail} skip={total_skip} "
        f"verdict={payload['verdict']}{skip_note} -> {out_path}"
    )
    return 0 if payload["verdict"] == "PASS" else 1


# ---------------------------------------------------------------------------
# record-case
# ---------------------------------------------------------------------------

# Fixture contract: the session ids / colony the fixture seeds, and the
# migration markers that must be set once a candidate has opened the home.
# v1 fixture: queen/session/<sid> -> queens/default/sessions/<sid>, exports/
#   colony -> colonies/<name> (see storage_compat_gen_fixture.py build_v1).
# v2 fixture: agents/... -> queens|colonies trees (build_v2); the v3 case runs
#   the same fixture through the previous delivery first.
EXPECTATIONS = {
    "v1": {
        "cold_sessions": ["session_20260101_000001_aaaaaaaa"],
        "colony": "legacy_colony",
        "markers": {".migrated-v2": True, ".migrated-v3": True, "colony_sessions_v1": True},
    },
    "v2": {
        "cold_sessions": ["session_20260201_000001_bbbbbbbb"],
        "colony": "alpha",
        "markers": {".migrated-v2": True, ".migrated-v3": True, "colony_sessions_v1": True},
    },
    "v3": {
        "cold_sessions": ["session_20260201_000001_bbbbbbbb"],
        "colony": "alpha",
        "markers": {".migrated-v2": True, ".migrated-v3": True, "colony_sessions_v1": True},
    },
}


def cmd_record_case(args) -> int:
    work = Path(args.work).resolve()
    case_dir = work / "cases" / args.case
    case_dir.mkdir(parents=True, exist_ok=True)
    expect = EXPECTATIONS[args.fixture]

    def parse_names(path: Path):
        if not path.exists():
            return None
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
        except json.JSONDecodeError:
            return None
        if isinstance(data, list):
            return [entry.get("session_id") or entry.get("name") for entry in data if isinstance(entry, dict)]
        return None

    old_cold = parse_names(case_dir / "old-cold.json")
    old_colonies = parse_names(case_dir / "old-colony.json")

    case = {
        "case": args.case,
        "fixture": args.fixture,
        "home": args.home,
        "work": str(work),
        "old": {
            "binary": args.old_binary,
            "version": args.old_version,
            "sha256": args.old_sha256,
        },
        "new": {
            "binary": args.new_binary,
            "version": args.new_version,
            "sha256": args.new_sha256,
        },
        "v3_prepared_by": args.v3_prepared_by or None,
        "seeded_credential_id": "upgrade_compat_key" if args.seeded in ("1", "true", "True") else None,
        "credential_seed_reason": args.seed_reason,
        "expect_cold_sessions": expect["cold_sessions"],
        "expect_colony": expect["colony"],
        "expect_markers_after_new": expect["markers"],
        "expect_markers_after_new2": expect["markers"],
        "snapshots": {
            "s0": str(case_dir / "s0.json"),
            "s1": str(case_dir / "s1.json"),
            "s2": str(case_dir / "s2.json"),
            "s3": str(case_dir / "s3.json"),
            "s4": str(case_dir / "s4.json"),
            "s5": str(case_dir / "s5.json"),
        },
        "diffs": {
            "new1": str(case_dir / "d_new1.json"),
            "new2": str(case_dir / "d_new2.json"),
            "old_read": str(case_dir / "d_old_read.json"),
            "old_serve": str(case_dir / "d_old_serve.json"),
        },
        "old_read": {
            "rc_session": args.rc_session,
            "rc_colony": args.rc_colony,
            "rc_queen": args.rc_queen,
            "evidence": str(case_dir / "probes"),
            "cold_evidence": str(case_dir / "old-cold.json"),
            "colony_evidence": str(case_dir / "old-colony.json"),
        },
        "old_cold_sessions": old_cold,
        "old_colonies": old_colonies,
    }
    (case_dir / "case.json").write_text(json.dumps(case, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"recorded case {args.case} ({args.fixture}) -> {case_dir / 'case.json'}")
    return 0


# ---------------------------------------------------------------------------
# self-test: prove the gate FAILs on synthetic regressions
# ---------------------------------------------------------------------------


def _synthetic_case(root: Path, name: str, mutation: str | None) -> None:
    """Write one synthetic case dir exercising the verify rules without servers."""
    case_dir = root / "cases" / name
    (case_dir / "probes" / "new1").mkdir(parents=True, exist_ok=True)
    (case_dir / "probes" / "new2").mkdir(parents=True, exist_ok=True)
    (case_dir / "probes" / "old_serve").mkdir(parents=True, exist_ok=True)

    user_file = "queens/q1/sessions/s1/state.json"
    markers = {".migrated-v2": True, ".migrated-v3": True, "colony_sessions_v1": True}
    s0 = {"files": {user_file: {"sha256": "h-state", "size": 10}}, "sqlite": {}, "markers": markers}
    s1 = json.loads(json.dumps(s0))
    s2 = json.loads(json.dumps(s0))
    s3 = json.loads(json.dumps(s0))
    s4 = json.loads(json.dumps(s0))
    s5 = json.loads(json.dumps(s0))

    d_new1 = {"added": [], "removed": [], "modified": [], "sqlite_changes": {}, "marker_changes": {}}
    d_new2 = {"added": [], "removed": [], "modified": [], "sqlite_changes": {}, "marker_changes": {}}
    d_old_read = {"added": [], "removed": [], "modified": [], "sqlite_changes": {}, "marker_changes": {}}
    d_old_serve = {"added": [], "removed": [], "modified": [], "sqlite_changes": {}, "marker_changes": {}}

    if mutation == "user-mod":
        d_new1["modified"] = [{"path": user_file, "before_sha256": "h-state", "after_sha256": "h-changed"}]
    elif mutation == "unconserved":
        s0["files"]["queen/old_state.json"] = {"sha256": "h-lost", "size": 3}
        d_new1["removed"] = [{"path": "queen/old_state.json", "size": 3}]
    elif mutation == "old-write":
        s4["files"]["queens/q1/sessions/s1/summary.json"] = {"sha256": "h-new", "size": 1}
        d_old_read["added"] = [{"path": "queens/q1/sessions/s1/summary.json", "size": 1}]

    for snap, data in (("s0", s0), ("s1", s1), ("s2", s2), ("s3", s3), ("s4", s4), ("s5", s5)):
        (case_dir / f"{snap}.json").write_text(json.dumps(data))
    for diff, data in (
        ("d_new1", d_new1),
        ("d_new2", d_new2),
        ("d_old_read", d_old_read),
        ("d_old_serve", d_old_serve),
    ):
        (case_dir / f"{diff}.json").write_text(json.dumps(data))

    health = "500" if mutation == "no-health" else "200"
    (case_dir / "probes" / "new1" / "health.http").write_text(f"status={health}\n")
    (case_dir / "probes" / "new1" / "sessions_history.http").write_text(
        'status=200\n{"sessions":[{"session_id":"session_20260101_000001_aaaaaaaa","message_count":2,"last_message":"hi there"}]}'
    )
    (case_dir / "probes" / "new1" / "discover.http").write_text('status=200\n{"Your Agents":[{"name":"legacy_colony"}]}')
    (case_dir / "probes" / "new1" / "credentials.http").write_text('status=200\n{"credentials":[]}')
    (case_dir / "probes" / "new1" / "colony_sessions.http").write_text('status=200\n{"sessions":[]}')
    (case_dir / "probes" / "new2" / "health.http").write_text("status=200\n")
    (case_dir / "probes" / "old_serve" / "health.http").write_text("status=200\n")

    case = {
        "case": name,
        "fixture": "v1",
        "home": str(case_dir / "home"),
        "old": {"binary": "/synthetic/old", "version": "old", "sha256": "0"},
        "new": {"binary": "/synthetic/new", "version": "new", "sha256": "1"},
        "v3_prepared_by": None,
        "seeded_credential_id": None,
        "credential_seed_reason": "synthetic",
        "expect_cold_sessions": ["session_20260101_000001_aaaaaaaa"],
        "expect_colony": "legacy_colony",
        "expect_markers_after_new": markers,
        "expect_markers_after_new2": markers,
        "snapshots": {f"s{i}": str(case_dir / f"s{i}.json") for i in range(6)},
        "diffs": {
            "new1": str(case_dir / "d_new1.json"),
            "new2": str(case_dir / "d_new2.json"),
            "old_read": str(case_dir / "d_old_read.json"),
            "old_serve": str(case_dir / "d_old_serve.json"),
        },
        "old_read": {"rc_session": 0, "rc_colony": 0, "rc_queen": 0, "evidence": str(case_dir / "probes")},
        "old_cold_sessions": ["session_20260101_000001_aaaaaaaa"],
        "old_colonies": ["legacy_colony"],
    }
    (case_dir / "case.json").write_text(json.dumps(case))


def cmd_self_test(args) -> int:
    work = Path(args.work or "/tmp").resolve()
    root = Path(tempfile.mkdtemp(prefix="upgrade-compat-selftest-", dir=str(work)))
    scenarios = {
        "clean": None,
        "no-health": "no-health",
        "user-mod": "user-mod",
        "unconserved": "unconserved",
        "old-write": "old-write",
    }
    expected_fail = {
        "clean": set(),
        "no-health": {"M1"},
        "user-mod": {"M7"},
        "unconserved": {"M8"},
        "old-write": {"M11-2"},
    }
    ok = True
    for name, mutation in scenarios.items():
        _synthetic_case(root, name, mutation)
    out = root / "selftest-report.json"
    rc = cmd_verify(argparse.Namespace(work=str(root), out=str(out), label="selftest", old="", new=""))
    report = json.loads(out.read_text(encoding="utf-8"))
    by_case = {c["case"]: c for c in report["cases"]}
    for name, want in expected_fail.items():
        case = by_case.get(name)
        if case is None:
            print(f"self-test: MISSING case {name}")
            ok = False
            continue
        failed_ids = {row["id"] for row in case["checks"] if row["status"] == "FAIL"}
        if not want:
            if case["verdict"] != "PASS":
                print(f"self-test: {name} expected PASS, got FAIL {sorted(failed_ids)}")
                ok = False
        elif not want <= failed_ids:
            print(f"self-test: {name} expected FAIL ids {sorted(want)}, got {sorted(failed_ids)}")
            ok = False
    if rc != 1:
        print(f"self-test: aggregate verify exit={rc}, want 1 (mutations must fail)")
        ok = False
    print(f"self-test: {'PASS' if ok else 'FAIL'} ({len(scenarios)} scenarios, work={root})")
    return 0 if ok else 1


# ---------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="cmd", required=True)

    p = sub.add_parser("seed-credentials")
    p.add_argument("--home", required=True)
    p.add_argument("--credential-id", default="upgrade_compat_key")
    p.add_argument("--key", default="")
    p.set_defaults(func=cmd_seed_credentials)

    p = sub.add_parser("verify")
    p.add_argument("--work", required=True)
    p.add_argument("--out")
    p.add_argument("--label", default="")
    p.add_argument("--old", default="")
    p.add_argument("--new", default="")
    p.set_defaults(func=cmd_verify)

    p = sub.add_parser("record-case")
    p.add_argument("--case", required=True)
    p.add_argument("--fixture", required=True, choices=sorted(EXPECTATIONS))
    p.add_argument("--work", required=True)
    p.add_argument("--home", required=True)
    p.add_argument("--old-version", default="")
    p.add_argument("--new-version", default="")
    p.add_argument("--old-sha256", default="")
    p.add_argument("--new-sha256", default="")
    p.add_argument("--old-binary", default="")
    p.add_argument("--new-binary", default="")
    p.add_argument("--seeded", default="false")
    p.add_argument("--seed-reason", default="")
    p.add_argument("--v3-prepared-by", default="")
    p.add_argument("--rc-session", type=int, default=0)
    p.add_argument("--rc-colony", type=int, default=0)
    p.add_argument("--rc-queen", type=int, default=0)
    p.add_argument("--old-serve-ran", type=int, default=0)
    p.set_defaults(func=cmd_record_case)

    p = sub.add_parser("self-test")
    p.add_argument("--work", default="")
    p.set_defaults(func=cmd_self_test)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
