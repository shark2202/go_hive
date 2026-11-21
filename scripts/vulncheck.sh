#!/usr/bin/env bash
# vulncheck.sh — dependency vulnerability gate for the Go modules.
#
# Primary path: run govulncheck (symbol-level, source mode) against every Go
# module in this checkout (go-hive + browserbridge by default). When the tool
# is missing the script installs it into a cache directory via `go install`
# (network required, isolated GOBIN, never touches the repo's go.mod/go.sum).
#
# Fallback path (honest, never fake-green): when govulncheck cannot be
# obtained or the vulnerability database is unreachable, the script runs
# scripts/vuln_offline_review.py — a stdlib-only consistency review of go.mod
# / go.sum / embedded binary build info — and exits 3 with verdict
# UNVERIFIED. It never reports a pass it did not verify.
#
# Exit codes:
#   0  every module scanned by govulncheck, no reachable vulnerabilities
#   1  vulnerabilities found (verified by govulncheck)
#   3  UNVERIFIED — govulncheck/DB unavailable or a module fell back to the
#      offline review; CVE state unknown
#   2  usage / environment error
#
# Usage:
#   bash go_hive/scripts/vulncheck.sh [options]
#
# Options:
#   --module DIR       module to scan (repeatable; default: go-hive + browserbridge)
#   --report DIR       evidence directory (default: go_hive/dist/vulncheck/<utc-stamp>)
#   --binary-dir DIR   directory of built binaries to inspect in the fallback
#                      review (repeatable; default: newest dist/slots/<slot>)
#   --offline          never install govulncheck / never hit the network; go
#                      straight to the offline review (exit 3)
#   --no-install       use only an existing govulncheck binary
#   --strict           exit 1 (not 3) when unverified — for gates that must not
#                      proceed without a real scan
#   -h, --help         this help
#
# Environment:
#   GOVULNCHECK_BIN    explicit govulncheck binary
#   VULNCHECK_GOPROXY  proxy used only for `go install` (default:
#                      https://proxy.golang.org; the repo builds stay offline)
#   VULNCHECK_CACHE    tool cache dir (default: ~/.cache/hive-vulncheck)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_HIVE_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

MODULES=()
REPORT_DIR=""
BINARY_DIRS=()
OFFLINE=0
NO_INSTALL=0
STRICT=0

usage() { sed -n '2,40p' "$0"; }

while [ $# -gt 0 ]; do
	case "$1" in
		--module) MODULES+=("$2"); shift 2 ;;
		--report) REPORT_DIR="$2"; shift 2 ;;
		--binary-dir) BINARY_DIRS+=("$2"); shift 2 ;;
		--offline) OFFLINE=1; shift ;;
		--no-install) NO_INSTALL=1; shift ;;
		--strict) STRICT=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) echo "vulncheck: unknown argument: $1" >&2; exit 2 ;;
	esac
done

die() { echo "vulncheck: $*" >&2; exit 2; }

if [ "${#MODULES[@]}" -eq 0 ]; then
	for candidate in "$GO_HIVE_ROOT/go-hive" "$GO_HIVE_ROOT/browserbridge"; do
		[ -f "$candidate/go.mod" ] && MODULES+=("$candidate")
	done
fi
[ "${#MODULES[@]}" -gt 0 ] || die "no Go modules found under $GO_HIVE_ROOT"

if [ -z "$REPORT_DIR" ]; then
	REPORT_DIR="$GO_HIVE_ROOT/dist/vulncheck/$(date -u +%Y%m%d-%H%M%S)"
fi
mkdir -p "$REPORT_DIR" || die "cannot create report dir $REPORT_DIR"
REPORT_DIR="$(cd "$REPORT_DIR" && pwd)"

if [ "${#BINARY_DIRS[@]}" -eq 0 ]; then
	newest_slot="$(ls -1d "$GO_HIVE_ROOT"/dist/slots/20*-* 2>/dev/null | tail -1 || true)"
	[ -n "$newest_slot" ] && [ -d "$newest_slot" ] && BINARY_DIRS+=("$newest_slot")
fi

log() { printf 'vulncheck: %s\n' "$*"; }

# --- locate / install govulncheck -----------------------------------------
CACHE="${VULNCHECK_CACHE:-$HOME/.cache/hive-vulncheck}"
find_govulncheck() {
	if [ -n "${GOVULNCHECK_BIN:-}" ] && [ -x "${GOVULNCHECK_BIN}" ]; then
		printf '%s\n' "$GOVULNCHECK_BIN"; return 0
	fi
	if command -v govulncheck >/dev/null 2>&1; then
		command -v govulncheck; return 0
	fi
	if [ -x "$CACHE/bin/govulncheck" ]; then
		printf '%s\n' "$CACHE/bin/govulncheck"; return 0
	fi
	return 1
}

GOVULN=""
if [ "$OFFLINE" = 1 ]; then
	GOVULN=""
	log "offline mode: govulncheck scan skipped (no network, no local DB)"
elif GOVULN="$(find_govulncheck)"; then
	log "using $( "$GOVULN" -version 2>&1 | head -1 ) at $GOVULN"
elif [ "$NO_INSTALL" = 1 ]; then
	log "govulncheck not found and install disabled (--offline/--no-install)"
else
	log "govulncheck not found; installing into $CACHE/bin (repo go.mod untouched)"
	mkdir -p "$CACHE/bin"
	if GOBIN="$CACHE/bin" GOPROXY="${VULNCHECK_GOPROXY:-https://proxy.golang.org}" \
		GOSUMDB="${VULNCHECK_GOSUMDB:-sum.golang.org}" GOFLAGS= GO111MODULE=on \
		go install golang.org/x/vuln/cmd/govulncheck@latest >"$REPORT_DIR/install.log" 2>&1; then
		GOVULN="$CACHE/bin/govulncheck"
		log "installed $( "$GOVULN" -version 2>&1 | head -1 )"
	else
		log "go install failed (see $REPORT_DIR/install.log); falling back to the offline review"
		GOVULN=""
	fi
fi

# --- fallback: offline review ----------------------------------------------
run_offline_review() {
	local reason="$1"
	log "UNVERIFIED ($reason)"
	local args=()
	for m in "${MODULES[@]}"; do args+=(--module "$m"); done
	if [ "${#BINARY_DIRS[@]}" -gt 0 ]; then
		for b in "${BINARY_DIRS[@]}"; do [ -d "$b" ] && args+=(--binary "$b"); done
	fi
	args+=(--out "$REPORT_DIR/offline-review.json")
	python3 "$SCRIPT_DIR/vuln_offline_review.py" "${args[@]}"
	local rc=$?
	{
		printf '# Dependency vulnerability scan — UNVERIFIED\n\n'
		printf 'Reason: %s\n\n' "$reason"
		printf 'govulncheck did not run, so dependency/stdlib CVE state is **UNKNOWN**.\n'
		printf 'The offline review above checks manifest integrity only and is not a scan pass.\n'
		printf 'Evidence: `offline-review.json`, `install.log` (if present).\n'
	} >"$REPORT_DIR/SUMMARY.md"
	if [ "$rc" = 1 ]; then
		log "offline review found consistency failures (see above); CVE state still UNVERIFIED"
		return 1
	fi
	if [ "$rc" = 2 ]; then
		return 2
	fi
	if [ "$STRICT" = 1 ]; then
		log "strict mode: treating UNVERIFIED as failure"
		return 1
	fi
	return 3
}

if [ -z "$GOVULN" ]; then
	if run_offline_review "govulncheck unavailable (offline / install disabled / install failed)"; then
		exit 0
	else
		exit $?
	fi
fi

# --- real scan --------------------------------------------------------------
parse_module_json() { # <raw-json> <module-name>
	python3 - "$1" "$2" <<'PY'
import hashlib, json, sys
raw, name = sys.argv[1], sys.argv[2]
decoder = json.JSONDecoder()
text = open(raw, encoding="utf-8", errors="replace").read()
idx, n = 0, len(text)
cfg, sbom, findings, osvs, messages = {}, {}, [], {}, 0
while idx < n:
    while idx < n and text[idx] in " 	\r\n":
        idx += 1
    if idx >= n:
        break
    try:
        obj, end = decoder.raw_decode(text, idx)
    except json.JSONDecodeError:
        break
    idx = end
    if not isinstance(obj, dict):
        continue
    if "config" in obj:
        cfg = obj["config"]
    elif "SBOM" in obj:
        sbom = obj["SBOM"]
    elif "finding" in obj:
        findings.append(obj["finding"])
    elif "osv" in obj and isinstance(obj["osv"], dict):
        o = obj["osv"]
        fixed = ""
        affected = o.get("affected") or []
        for a in affected:
            for rng in a.get("ranges") or []:
                for ev in rng.get("events") or []:
                    if ev.get("fixed"):
                        fixed = ev["fixed"]
        pkg = ""
        if affected:
            pkg = (affected[0].get("package") or {}).get("name", "")
        osvs[o.get("id", "")] = {
            "id": o.get("id", ""),
            "summary": (o.get("summary") or "")[:300],
            "fixed": fixed,
            "package": pkg,
        }
    else:
        messages += 1
mods = [m for m in (sbom.get("modules") or []) if isinstance(m, dict)]

unique_ids = []
seen = set()
for f in findings:
    oid = f.get("osv") or ""
    if oid and oid not in seen:
        seen.add(oid)
        unique_ids.append(oid)

# Aggregate per-OSV facts from the findings themselves. The OSV objects carry
# fixes for every affected module (stdlib and golang.org/x/...); only the
# trace entries say which one actually applies to this build.
agg = {}
for f in findings:
    oid = f.get("osv") or ""
    tr = (f.get("trace") or [{}])[0]
    a = agg.setdefault(oid, {"modules": set(), "packages": set(), "fixed": set()})
    a["modules"].add(tr.get("module") or "")
    a["packages"].add(tr.get("package") or "")
    a["fixed"].add(f.get("fixed_version") or "")
out = {
    "module": name,
    "raw": raw,
    "raw_sha256": hashlib.sha256(open(raw, "rb").read()).hexdigest(),
    "config": cfg,
    "sbom_modules": len(mods),
    "messages": messages,
    "finding_events": len(findings),
    "unique_osv": [
        {
            "id": oid,
            "summary": (osvs.get(oid) or {}).get("summary", ""),
            "fixed": ", ".join(sorted(x for x in agg.get(oid, {}).get("fixed", set()) if x)),
            "packages": sorted(x for x in agg.get(oid, {}).get("packages", set()) if x),
            "trace_modules": sorted(x for x in agg.get(oid, {}).get("modules", set()) if x),
        }
        for oid in unique_ids
    ],
    "findings": [
        {
            "osv": f.get("osv"),
            "module": (f.get("trace") or [{}])[0].get("module"),
            "package": (f.get("trace") or [{}])[0].get("package"),
            "function": (f.get("trace") or [{}])[0].get("function"),
            "fixed_version": f.get("fixed_version"),
        }
        for f in findings
    ],
}
print(json.dumps(out, ensure_ascii=False))
PY
}

MODULE_RESULTS=()
OVERALL=0
for module in "${MODULES[@]}"; do
	name="$(basename "$module")"
	raw="$REPORT_DIR/govulncheck-$name.json"
	log "scanning $name (govulncheck -json ./...)"
	( cd "$module" && env CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
		"$GOVULN" -json ./... ) >"$raw" 2>"$REPORT_DIR/govulncheck-$name.err"
	rc=$?
	parsed="$(parse_module_json "$raw" "$name")"
	if [ -z "$parsed" ]; then
		log "$name: UNVERIFIED (could not parse govulncheck output; rc=$rc)"
		printf '%s\n' "{\"module\":\"$name\",\"verdict\":\"UNVERIFIED\",\"reason\":\"unparsable output rc=$rc\"}" \
			>"$REPORT_DIR/result-$name.json"
		[ "$OVERALL" -lt 3 ] && OVERALL=3
		continue
	fi
	printf '%s\n' "$parsed" >"$REPORT_DIR/result-$name.json"
	nfind="$(printf '%s' "$parsed" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["findings"]))')"
	db_meta="$(printf '%s' "$parsed" | python3 -c 'import json,sys; c=json.load(sys.stdin)["config"]; print(c.get("scanner_version","?"), c.get("db_last_modified","?"))')"
	if [ "$nfind" -gt 0 ]; then
		log "$name: $nfind reachable finding(s); scanner=$db_meta"
		OVERALL=1
	elif [ "$rc" = 0 ]; then
		log "$name: no vulnerabilities found; scanner=$db_meta"
	else
		# Non-zero rc with an empty findings list is a scanner/env error, not a pass.
		log "$name: UNVERIFIED (govulncheck rc=$rc, no findings parsed; see govulncheck-$name.err)"
		printf '%s\n' "{\"module\":\"$name\",\"verdict\":\"UNVERIFIED\",\"reason\":\"govulncheck rc=$rc\"}" \
			>"$REPORT_DIR/result-$name.json"
		[ "$OVERALL" -lt 3 ] && OVERALL=3
	fi
done

# --- summary ----------------------------------------------------------------
python3 - "$REPORT_DIR" <<'PY'
import json, os, sys
from datetime import datetime, timezone
report_dir = sys.argv[1]
mods = []
for name in sorted(os.listdir(report_dir)):
    if not name.startswith("result-") or not name.endswith(".json"):
        continue
    try:
        mods.append(json.load(open(os.path.join(report_dir, name), encoding="utf-8")))
    except Exception:
        pass
findings_total = sum(m.get("finding_events", len(m.get("findings") or [])) for m in mods)
uniq = {}
for m in mods:
    for o in m.get("unique_osv") or []:
        rec = uniq.setdefault(o.get("id"), {"id": o.get("id"), "summary": o.get("summary", ""), "fixed": o.get("fixed", ""), "modules": []})
        if m.get("module") not in rec["modules"]:
            rec["modules"].append(m.get("module"))
unverified = [m for m in mods if m.get("verdict") == "UNVERIFIED"]
verdict = "FAIL" if uniq else ("UNVERIFIED" if unverified else "PASS")
report = {
    "schema_version": 1,
    "tool": "go_hive/scripts/vulncheck.sh",
    "generated_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
    "verdict": verdict,
    "unique_osv": list(uniq.values()),
    "finding_events": findings_total,
    "modules": mods,
}
open(os.path.join(report_dir, "summary.json"), "w", encoding="utf-8").write(
    json.dumps(report, ensure_ascii=False, indent=2) + "\n"
)
lines = ["# Dependency vulnerability scan", ""]
lines.append(f"- verdict: **{verdict}**")
lines.append(f"- unique OSV ids: {len(uniq)}; finding events (symbol/package traces): {findings_total}")
for m in mods:
    cfg = m.get("config") or {}
    if m.get("verdict") == "UNVERIFIED":
        lines.append(f"- {m.get('module')}: UNVERIFIED ({m.get('reason','')}) — CVE state unknown")
    else:
        lines.append(
            f"- {m.get('module')}: {len(m.get('unique_osv') or [])} unique / "
            f"{m.get('finding_events', len(m.get('findings') or []))} events, "
            f"{m.get('sbom_modules', 0)} SBOM module(s), "
            f"scanner={cfg.get('scanner_version','?')}, DB={cfg.get('db_last_modified','?')}, "
            f"mode={cfg.get('scan_mode','?')}/{cfg.get('scan_level','?')}, go={cfg.get('go_version','?')}"
        )
if uniq:
    lines += ["", "| OSV | fixed in | module(s) | summary |", "| --- | --- | --- | --- |"]
    for o in uniq.values():
        summary = (o.get("summary") or "").replace("|", "\\|")[:160]
        lines.append(
            f"| {o.get('id')} | {o.get('fixed') or '-'} | "
            f"{', '.join(o.get('modules') or [])} | {summary} |"
        )
# Keep the per-event list for auditability, but collapsed by OSV id.
open(os.path.join(report_dir, "SUMMARY.md"), "w", encoding="utf-8").write("\n".join(lines) + "\n")
print("\n".join(lines[:40]))
PY

echo ""
if [ "$OVERALL" = 0 ]; then
	log "PASS: no reachable vulnerabilities (verified by govulncheck)"
elif [ "$OVERALL" = 1 ]; then
	log "FAIL: reachable vulnerabilities found — see $REPORT_DIR/SUMMARY.md"
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		echo "::error::govulncheck found reachable vulnerabilities; see $REPORT_DIR/SUMMARY.md"
	fi
else
	log "UNVERIFIED: at least one module has no verified scan — this is NOT a pass"
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		echo "::warning::dependency vulnerability scan UNVERIFIED for at least one module; see $REPORT_DIR/SUMMARY.md"
	fi
	[ "$STRICT" = 1 ] && OVERALL=1
fi
log "evidence: $REPORT_DIR"
exit "$OVERALL"
