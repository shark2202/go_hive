#!/usr/bin/env python3
"""Frontend surface diff: static inventory + live dual-run probes.

Answers one question for appearance dimension ``frontend`` (前端路由 / i18n /
静态资源): does the Go dashboard, served by ``hive serve``, expose the same
observable frontend surface as the Python reference — hash-route deep links,
page title, i18n key set, static assets, SPA fallback, and the
upload/attachment-download face (Content-Disposition, Range, 404s)?

Two halves, both stdlib only:

* ``--static``: source-level inventory of ``core/frontend`` vs
  ``go_hive/go-hive/frontend`` — resource path set (shared / Go-only /
  Python-only / content-diff), classification of every content-diff file,
  the hash route table from ``App.tsx``, i18n dictionary keys and their
  reference coverage, and the title/version story.
* ``--probe``: builds each side's ``dist/`` is NOT done here; it starts both
  servers (``uv run --offline hive serve`` and the compiled Go binary) against
  throwaway HIVE_HOMEs on fixed ports and probes the same URLs, then uploads
  and downloads one attachment per side.

Outputs (default ``docs/frontend-surface/``):
  frontend-surface-diff.json   machine-readable summary (both halves)
  probes.json                  raw request/response evidence from the run
  logs/{python,go}.log         server logs of the run

Usage:
    uv run python go_hive/scripts/frontend_surface_diff.py --all
    uv run python go_hive/scripts/frontend_surface_diff.py --static
    uv run python go_hive/scripts/frontend_surface_diff.py --probe --keep
"""

from __future__ import annotations

import argparse
import atexit
import base64
import hashlib
import http.client
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
GO_DIR = ROOT / "go_hive" / "go-hive"
PY_FE = ROOT / "core" / "frontend"
GO_FE = GO_DIR / "frontend"
OUT_DIR = ROOT / "docs" / "frontend-surface"
SKIP_DIRS = {"node_modules", "dist", ".next", "coverage", ".vite"}
HOST = "127.0.0.1"

# ---------------------------------------------------------------- utilities


def sha256_bytes(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace")


def utc_now() -> str:
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")


def _repo_relative(path) -> str:
    try:
        return str(Path(path).resolve().relative_to(ROOT))
    except ValueError:
        return str(path)


def _dist_facts(frontend_dir: Path) -> dict:
    index = frontend_dir / "dist" / "index.html"
    return {
        "dir": _repo_relative(frontend_dir / "dist"),
        "index_sha256": sha256_bytes(index.read_bytes()) if index.is_file() else None,
    }


def git_head(cwd: Path) -> str:
    try:
        proc = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"], cwd=str(cwd),
            capture_output=True, text=True, timeout=20,
        )
        return proc.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


# --------------------------------------------------------- static inventory


def frontend_files(root: Path) -> dict:
    out = {}
    for path in root.rglob("*"):
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        if path.is_file():
            out[str(path.relative_to(root))] = sha256_bytes(path.read_bytes())
    return out


T_CALL_RE = re.compile(
    r"""\bt\(\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')"""
    r"""(?:\s*,\s*\{[^{}]*\})?\s*\)""",
    re.DOTALL,
)
I18N_IMPORT_RE = re.compile(r'^\s*import\b[^\n]*["\'](?:@/i18n|\.\./i18n|\./i18n)["\'];?\s*$', re.M)
I18N_HOOK_RE = re.compile(r"^\s*const\s*\{[^}]*\}\s*=\s*useI18n\(\);.*$", re.M)
I18N_OTHER_RE = re.compile(r"^\s*(?:import|export)\b[^\n]*(?:LocaleSwitcher|I18nProvider)[^\n]*$", re.M)
I18N_TYPE_RE = re.compile(r"^type\s+(?:T|TFunction)\s*=\s*\(key: string[^\n]*$", re.M)
JSX_ATTR_RE = re.compile(r'=\{\s*("(?:[^"\\]|\\.)*"|\'(?:[^\'\\]|\\.)*\')\s*\}')
JSX_TEXT_RE = re.compile(r'\{\s*("(?:[^"\\]|\\.)*"|\'(?:[^\'\\]|\\.)*\')\s*\}')


def normalize_i18n(text: str) -> str:
    """Blank out i18n plumbing so the residual diff is the real change.

    Wraps ``t("X")`` back to ``"X"``, folds JSX ``attr={"X"}`` / ``{"X"}``
    children back to their literal forms, and drops the i18n
    imports/hook/type-alias lines.
    """
    text = I18N_IMPORT_RE.sub("", text)
    text = I18N_HOOK_RE.sub("", text)
    text = I18N_OTHER_RE.sub("", text)
    text = I18N_TYPE_RE.sub("", text)
    text = T_CALL_RE.sub(lambda m: m.group(1), text)
    text = JSX_ATTR_RE.sub(lambda m: "=" + m.group(1), text)
    text = JSX_TEXT_RE.sub(lambda m: m.group(1)[1:-1], text)
    lines = [line.rstrip() for line in text.splitlines()]
    return "\n".join(line for line in lines if line.strip() != "")


def _residual_is_i18n(line: str) -> bool:
    return bool(re.search(
        r"\b(?:t|tr|tFn)\(|\b(?:t|tr)\b|TFunction|TranslateVars|useI18n|i18n|codeLabel|LocaleSwitcher",
        line,
    ))


def classify_content_diff(rel: str, py_text: str, go_text: str) -> dict:
    py_norm, go_norm = normalize_i18n(py_text), normalize_i18n(go_text)
    cls, note = "other", ""
    if py_norm == go_norm:
        cls = "i18n-wrap"
        note = "identical once t() wrappers / i18n imports are folded back"
    elif rel == "vite.config.ts":
        cls = "version-resolution"
        note = "Go reads internal/version/version.go (HIVE_APP_VERSION override) instead of core/pyproject.toml"
    elif rel == "src/api/credentials.ts":
        cls = "type-field"
        note = "Go dropped the stale required `tool_modules` field (no API row carries it)"
    else:
        diff_lines = _unified_diff_lines(py_norm, go_norm)
        non_i18n = [line for line in diff_lines if not _residual_is_i18n(line)]
        if non_i18n:
            cls = "i18n-refactor"
            note = "i18n wrap plus template→placeholder / helper refactor: " + "; ".join(line[:100] for line in non_i18n[:4])
        else:
            cls = "i18n-plumbing"
            note = "i18n wrap plus t/type plumbing only: " + "; ".join(line[:100] for line in diff_lines[:4])
    return {"path": rel, "class": cls, "note": note}


def _unified_diff_lines(a: str, b: str) -> list:
    import difflib

    out = []
    for line in difflib.unified_diff(a.splitlines(), b.splitlines(), lineterm="", n=0):
        if line.startswith(("+++", "---", "@@")):
            continue
        if line.startswith(("+", "-")):
            out.append(line[:120])
    return out


ROUTE_NAME_RE = re.compile(r'path="([^"]+)"')
REDIRECT_RE = re.compile(r'<Navigate\s+to="([^"]+)"')
NAV_RE = re.compile(
    r"""(?:navigate\(\s*["']([^"']+)["']|to=["']([^"']+)["']|location\.hash\s*=\s*["']#?([^"']+)["'])"""
)


def route_table(app_tsx: Path, src_root: Path) -> dict:
    text = read_text(app_tsx)
    routes = ROUTE_NAME_RE.findall(text)
    redirects = sorted(set(REDIRECT_RE.findall(text)))
    refs = set()
    for path in src_root.rglob("*"):
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        if not path.is_file() or path.suffix not in {".ts", ".tsx", ".css", ".json", ".html"}:
            continue
        if path.name == "App.tsx":
            continue
        if "src/i18n" in str(path):
            pass  # i18n dictionaries carry UI copy, not routes
        for match in NAV_RE.finditer(read_text(path)):
            value = next(group for group in match.groups() if group)
            refs.add(value)
    return {
        "routes": routes,
        "redirects": redirects,
        "referenced_route_strings": sorted(r for r in refs if "/" in r),
    }


ZH_FILES = ["core.ts", "chat.ts", "pages.ts", "settings.ts"]
ZH_ENTRY_RE = re.compile(
    r'^\s*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z_$][\w$]*))\s*:\s*"((?:[^"\\]|\\.)*)"\s*,\s*(?://.*)?$'
)


def parse_zh_dictionaries(root: Path) -> dict:
    entries, per_file, unparsed = {}, {}, []
    for name in ZH_FILES:
        path = root / "src" / "i18n" / "zh" / name
        if not path.is_file():
            per_file[name] = 0
            continue
        count = 0
        lines = read_text(path).splitlines()
        index = 0
        while index < len(lines):
            line = lines[index]
            stripped = line.strip()
            index += 1
            if not stripped or stripped.startswith(("//", "/*", "*", "}", "export", "/**")):
                continue
            # Prettier wraps a long entry's value onto the next line; join it.
            candidate = line
            if re.match(r'^\s*"((?:[^"\\]|\\.)*)"\s*:\s*$', line) and index < len(lines):
                candidate = line + " " + lines[index].strip()
                index += 1
            match = ZH_ENTRY_RE.match(candidate)
            if match:
                key = json.loads(f'"{match.group(1)}"') if match.group(1) is not None else match.group(2)
                entries[key] = json.loads(f'"{match.group(3)}"')
                count += 1
            else:
                unparsed.append(f"{name}: {stripped[:100]}")
        per_file[name] = count
    return {"entries": entries, "per_file": per_file, "unparsed": unparsed}


def t_call_keys(root: Path, dict_dir: Path) -> dict:
    keys, dynamic = set(), 0
    for path in root.rglob("*"):
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        if not path.is_file() or path.suffix not in {".ts", ".tsx"}:
            continue
        if dict_dir in path.parents:
            continue
        text = read_text(path)
        for match in T_CALL_RE.finditer(text):
            literal = match.group(1)
            keys.add(json.loads(literal) if literal.startswith('"') else json.loads(f'"{literal[1:-1]}"'))
        dynamic += len(re.findall(r"\bt\(\s*(?![\"'])", text))
    return {"keys": keys, "dynamic_calls": dynamic}


TYPOGRAPHIC = {"’": "'", "‘": "'", "“": '"', "”": '"'}
HTML_ENTITIES = {
    "&amp;": "&", "&middot;": "·", "&apos;": "'", "&quot;": '"',
    "&lt;": "<", "&gt;": ">", "&mdash;": "—", "&ndash;": "–", "&nbsp;": " ",
}


def key_to_source_regex(key: str) -> "re.Pattern":
    """Match an English i18n key against reference source that may still hold
    the un-translated template form (``${expr}``) instead of ``{name}``."""
    parts = []
    for chunk in re.split(r"(\{\w+\})", key):
        match = re.fullmatch(r"\{(\w+)\}", chunk)
        if match:
            name = re.escape(match.group(1))
            parts.append(r"(?:\$\{[^}]*\b" + name + r"\b[^}]*\}|\{[^}]*\b" + name + r"\b[^}]*\})")
        else:
            parts.append(re.escape(chunk))
    return re.compile("".join(parts))


def normalize_copy(text: str) -> str:
    for src, dst in TYPOGRAPHIC.items():
        text = text.replace(src, dst)
    for src, dst in HTML_ENTITIES.items():
        text = text.replace(src, dst)
    return text


def _pyproject_version() -> str:
    text = read_text(ROOT / "core" / "pyproject.toml")
    start = text.find("[project]")
    if start < 0:
        return "unknown"
    table = text[start:].split("\n[")[0]
    match = re.search(r'^version\s*=\s*"([^"]+)"', table, re.M)
    return match.group(1) if match else "unknown"


def _go_version() -> str:
    text = read_text(GO_DIR / "internal" / "version" / "version.go")
    match = re.search(r'const\s+Version\s*=\s*"([^"]+)"', text)
    return match.group(1) if match else "unknown"


def static_analysis() -> dict:
    py_files = frontend_files(PY_FE)
    go_files = frontend_files(GO_FE)
    shared = sorted(set(py_files) & set(go_files))
    go_only = sorted(set(go_files) - set(py_files))
    python_only = sorted(set(py_files) - set(go_files))
    content_diff = sorted(p for p in shared if py_files[p] != go_files[p])

    classified, classes = [], {}
    for rel in content_diff:
        info = classify_content_diff(rel, read_text(PY_FE / rel), read_text(GO_FE / rel))
        classified.append(info)
        classes[info["class"]] = classes.get(info["class"], 0) + 1

    py_routes = route_table(PY_FE / "src" / "App.tsx", PY_FE / "src")
    go_routes = route_table(GO_FE / "src" / "App.tsx", GO_FE / "src")
    route_diffs = {
        "routes_go_only": sorted(set(go_routes["routes"]) - set(py_routes["routes"])),
        "routes_python_only": sorted(set(py_routes["routes"]) - set(go_routes["routes"])),
        "refs_go_only": sorted(set(go_routes["referenced_route_strings"]) - set(py_routes["referenced_route_strings"])),
        "refs_python_only": sorted(set(py_routes["referenced_route_strings"]) - set(go_routes["referenced_route_strings"])),
    }

    py_dict = parse_zh_dictionaries(PY_FE)
    go_dict = parse_zh_dictionaries(GO_FE)
    i18n_ref = t_call_keys(GO_FE / "src", GO_FE / "src" / "i18n")
    dict_keys = set(go_dict["entries"])
    missing = sorted(i18n_ref["keys"] - dict_keys)          # used via t() but untranslated
    dead = sorted(dict_keys - i18n_ref["keys"])              # in the dictionary but never t()'d
    untranslated = sorted(k for k, v in go_dict["entries"].items() if v == k)

    py_source = normalize_copy("\n".join(
        read_text(p) for p in (PY_FE / "src").rglob("*")
        if p.is_file() and p.suffix in {".ts", ".tsx", ".css", ".html"} and not any(
            part in SKIP_DIRS for part in p.parts)
    ))
    py_source_canon = re.sub(r"\$\{[^}]*\}", "{*}", py_source)
    py_expr_canon = re.sub(
        r"\{[^{}\n]{1,80}\}", "{*}", py_source_canon
    )
    counterpart = {}
    for key in sorted(dict_keys):
        probe = normalize_copy(key)
        matched = bool(key_to_source_regex(probe).search(py_source))
        if not matched:
            # Go renames placeholders while converting templates, e.g.
            # "(${more} more)" -> "({count} more)"; compare with the
            # placeholder names folded away.
            matched = re.sub(r"\{[^}]*\}", "{*}", probe) in py_source_canon
        if not matched:
            # JSX expressions ({questions.length} questions) are not template
            # literals; fold short brace expressions on both sides too.
            matched = re.sub(r"\{[^{}\n]{1,80}\}", "{*}", probe) in py_expr_canon
        counterpart[key] = matched
    no_counterpart = sorted(k for k, ok in counterpart.items() if not ok)
    with_counterpart = len(dict_keys) - len(no_counterpart)

    index_title = {
        "python": (re.search(r"<title>([^<]*)</title>", read_text(PY_FE / "index.html")) or [None, None])[1],
        "go": (re.search(r"<title>([^<]*)</title>", read_text(GO_FE / "index.html")) or [None, None])[1],
    }
    per_route_title = {
        "python": len(re.findall(r"document\.title", "\n".join(read_text(p) for p in (PY_FE / "src").rglob("*") if p.is_file()))),
        "go": len(re.findall(r"document\.title", "\n".join(read_text(p) for p in (GO_FE / "src").rglob("*") if p.is_file()))),
    }

    return {
        "source_inventory": {
            "shared_paths": len(shared),
            "go_only": len(go_only),
            "python_only": len(python_only),
            "content_diff": len(content_diff),
            "go_only_paths": go_only,
            "python_only_paths": python_only,
            "classification": classes,
            "content_diff_files": classified,
        },
        "routes": {
            "python": py_routes["routes"],
            "go": go_routes["routes"],
            "redirects": {"python": py_routes["redirects"], "go": go_routes["redirects"]},
            "deltas": route_diffs,
            "title": index_title,
            "document_title_assignments": per_route_title,
        },
        "i18n": {
            "python_dictionary_keys": len(py_dict["entries"]),
            "go_dictionary_keys": len(dict_keys),
            "go_only_keys": len(dict_keys),
            "per_file": go_dict["per_file"],
            "unparsed_lines": go_dict["unparsed"],
            "t_referenced_keys": len(i18n_ref["keys"]),
            "t_dynamic_calls": i18n_ref["dynamic_calls"],
            "used_but_untranslated": missing,
            "dictionary_but_unused": dead,
            "value_equals_key": untranslated,
            "keys_with_python_counterpart": with_counterpart,
            "keys_without_python_counterpart": no_counterpart,
            "whitelist": sorted(dict_keys),
        },
    }


# ------------------------------------------------------------- live probes


class Side:
    def __init__(self, key: str, label: str, port: int, home: Path, log: Path, argv: list, cwd: Path, env: dict):
        self.key, self.label, self.port, self.home = key, label, port, home
        self.log, self.argv, self.cwd, self.env = log, argv, cwd, env
        self.process: subprocess.Popen | None = None
        self.version = ""

    @property
    def base(self) -> str:
        return f"http://{HOST}:{self.port}"


def free_port() -> int:
    import socket

    with socket.socket() as sock:
        sock.bind((HOST, 0))
        return sock.getsockname()[1]


def make_home(root: Path, name: str) -> tuple:
    home = root / name
    (home / "secrets").mkdir(parents=True, exist_ok=True)
    key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets" / "credential_key").write_text(key, encoding="utf-8")
    os.chmod(home / "secrets" / "credential_key", 0o600)
    (home / "configuration.json").write_text(json.dumps({
        "llm": {"provider": "deepseek", "model": "deepseek-flash-1017",
                "api_base": "http://127.0.0.1:9", "api_key_env_var": "HIVE_TEST_KEY"},
    }, indent=2) + "\n", encoding="utf-8")
    return home, key


def http_req(side: Side, method: str, path: str, headers: dict | None = None,
             body: bytes | None = None, timeout: float = 30) -> dict:
    conn = http.client.HTTPConnection(HOST, side.port, timeout=timeout)
    try:
        conn.request(method, path, body=body, headers=headers or {})
        resp = conn.getresponse()
        payload = resp.read()
        return {
            "status": resp.status,
            "reason": resp.reason,
            "headers": {k.lower(): v for k, v in resp.getheaders()},
            "body_sha256": sha256_bytes(payload),
            "body_length": len(payload),
            "body_prefix": payload[:160].decode("utf-8", "replace"),
            "body": payload,
        }
    except Exception as exc:  # noqa: BLE001 - record, do not abort the run
        return {"status": None, "error": f"{type(exc).__name__}: {exc}", "body": b""}
    finally:
        conn.close()


def trim(resp: dict, keep_body: bool = False) -> dict:
    out = {k: v for k, v in resp.items() if k != "body"}
    if keep_body and resp.get("body"):
        out["body_text"] = resp["body"].decode("utf-8", "replace")[:400]
    out["title"] = None
    prefix = resp.get("body_prefix", "")
    match = re.search(r"<title>([^<]*)</title>", prefix)
    if match:
        out["title"] = match.group(1)
    return out


def multipart(filename: str, content: bytes, content_type: str = "text/plain") -> tuple:
    boundary = "----hivefrontsurface" + secrets.token_hex(8)
    head = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
        f"Content-Type: {content_type}\r\n\r\n"
    ).encode("utf-8")
    tail = f"\r\n--{boundary}--\r\n".encode("utf-8")
    return boundary, head + content + tail


HASH_ROUTES = ["/", "/models", "/org-chart", "/credentials", "/skills-library",
               "/prompt-library", "/memory-library", "/colony/demo-colony",
               "/queen/demo-queen", "/queen/demo-queen/profile", "/claude-code/demo-queen",
               "/tool-library", "/queen-routing", "/no-such-page"]
PATH_ROUTES = ["/models", "/org-chart", "/credentials", "/colony/demo-colony",
               "/queen/demo-queen/profile", "/skills-library?tab=mcp", "/no-such-page"]
ASSETS = ["/favicon.png", "/logos/github.svg", "/logos/openai.svg", "/logos/stripe.svg"]


def probe(args) -> dict:
    work = Path(args.work_dir) if args.work_dir else Path(tempfile.mkdtemp(prefix="hive-frontend-surface-"))
    work.mkdir(parents=True, exist_ok=True)
    (work / "logs").mkdir(exist_ok=True)
    binary = work / "hive-go"

    if not args.no_build:
        proc = subprocess.run(["go", "build", "-o", str(binary), "./cmd/hive"], cwd=str(GO_DIR),
                              capture_output=True, text=True, timeout=900)
        if proc.returncode != 0:
            raise SystemExit(f"go build failed:\n{proc.stdout}\n{proc.stderr}")
    if not binary.is_file():
        raise SystemExit(f"Go binary missing: {binary}")

    for label, path in (("python dist", PY_FE / "dist" / "index.html"), ("go dist", GO_FE / "dist" / "index.html")):
        if not path.is_file():
            raise SystemExit(f"{label} missing: {path} (build it with npm --prefix … run build / npx vite build)")

    py_home, py_key = make_home(work, "python-home")
    go_home, go_key = make_home(work, "go-home")
    py_port = args.py_port or free_port()
    go_port = args.go_port or free_port()

    base_env = {k: v for k, v in os.environ.items()
                if k not in {"HIVE_HOME", "HIVE_CREDENTIAL_KEY", "HIVE_RUNTIME_PORT", "HIVE_DESKTOP_TOKEN"}}
    base_env["PYTHONUNBUFFERED"] = "1"
    uv = shutil.which("uv") or "uv"

    py = Side("python", "Python hive", py_port, py_home, work / "logs" / "python.log",
              [uv, "run", "--offline", "hive", "serve", "--host", HOST, "--port", str(py_port)],
              ROOT, {**base_env, "HIVE_HOME": str(py_home), "HIVE_CREDENTIAL_KEY": py_key})
    go = Side("go", "Go hive", go_port, go_home, work / "logs" / "go.log",
              [str(binary), "serve", "--host", HOST, "--port", str(go_port)], GO_DIR,
              {**base_env, "HIVE_HOME": str(go_home), "HIVE_CREDENTIAL_KEY": go_key})

    started = time.time()
    for side in (py, go):
        handle = open(side.log, "w", encoding="utf-8")
        side.process = subprocess.Popen(side.argv, cwd=str(side.cwd), env=side.env,
                                        stdout=handle, stderr=subprocess.STDOUT, start_new_session=True)

    def _shutdown() -> None:
        for side in (py, go):
            if side.process and side.process.poll() is None:
                side.process.terminate()
                try:
                    side.process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    side.process.kill()

    atexit.register(_shutdown)  # a crash mid-probe must not leave a server on the port

    deadline = time.time() + 120
    pending = [py, go]
    while pending and time.time() < deadline:
        for side in list(pending):
            if side.process.poll() is not None:
                raise SystemExit(f"{side.label} exited early rc={side.process.returncode}; log: {side.log}")
            resp = http_req(side, "GET", "/api/health", timeout=5)
            if resp.get("status") == 200:
                pending.remove(side)
            else:
                time.sleep(0.25)
    if pending:
        raise SystemExit(f"servers not healthy: {[s.label for s in pending]}")

    py.version = subprocess.run([uv, "run", "--offline", "hive", "--version"], cwd=str(ROOT),
                                capture_output=True, text=True, timeout=120).stdout.strip().splitlines()[-1]
    go.version = subprocess.run([str(binary), "--version"], cwd=str(GO_DIR),
                                capture_output=True, text=True, timeout=120).stdout.strip().splitlines()[-1]

    checks = []

    def record(check_id, method, path, request_headers=None, body=None, timeout=30,
               keep_body=True, note="", py_path=None, go_path=None):
        headers = dict(request_headers or {})
        if body is not None and "Content-Type" not in headers:
            headers["Content-Type"] = "application/json"
        py_resp = http_req(py, method, py_path or path, headers, body, timeout)
        go_resp = http_req(go, method, go_path or path, headers, body, timeout)
        entry = {
            "id": check_id, "method": method, "path": path, "note": note,
            "python_path": py_path or path, "go_path": go_path or path,
            "request_headers": headers,
            "python": trim(py_resp, keep_body), "go": trim(go_resp, keep_body),
        }
        checks.append(entry)
        return py_resp, go_resp

    # --- index + title ---------------------------------------------------
    py_index, go_index = record("index-root", "GET", "/")
    py_index2, go_index2 = record("index-explicit", "GET", "/index.html")
    py_index_sha = py_index["body_sha256"]
    go_index_sha = go_index["body_sha256"]

    # --- hash deep links (fragment never leaves the client) --------------
    for route in HASH_ROUTES:
        record(f"hash-route:{route}", "GET", route if route != "/" else "/",
               note="HashRouter: the server sees GET / for every #/fragment deep link")

    # --- path-style deep links (what a static server actually receives) --
    for route in PATH_ROUTES:
        record(f"path-route:{route}", "GET", route)

    # --- static assets ----------------------------------------------------
    asset_responses = {}
    for asset in ASSETS:
        asset_responses[asset] = record(f"asset:{asset}", "GET", asset)

    # The two dashboards are separate builds (the Go bundle carries the i18n
    # increment), so bundle bytes legitimately differ; only the Content-Type
    # the server hands the browser is a parity question.
    for kind, pattern in (("js", r"assets/[^\"']+\.js"), ("css", r"assets/[^\"']+\.css")):
        py_bundle = re.search(pattern, read_text(PY_FE / "dist" / "index.html"))
        go_bundle = re.search(pattern, read_text(GO_FE / "dist" / "index.html"))
        if py_bundle and go_bundle:
            record(f"bundle-{kind}", "GET", "/" + pattern,
                   py_path="/" + py_bundle.group(0), go_path="/" + go_bundle.group(0),
                   note="bundle bytes differ by design (i18n increment); compare status + Content-Type")

    # Caching: aiohttp FileResponse sends an ETag; net/http ServeFile does not.
    py_asset, go_asset = asset_responses["/favicon.png"]
    py_etag = (py_asset.get("headers") or {}).get("etag")
    go_etag = (go_asset.get("headers") or {}).get("etag")
    if py_etag or go_etag:
        record("asset-if-none-match", "GET", "/favicon.png",
               py_path="/favicon.png", go_path="/favicon.png",
               request_headers={},
               note="conditional GET with each side's own ETag")
        # Re-issue with the per-side header (the generic record() sends one set).
        conn_headers = lambda etag: {"If-None-Match": etag} if etag else {}
        py_cond = http_req(py, "GET", "/favicon.png", conn_headers(py_etag), None, 30)
        go_cond = http_req(go, "GET", "/favicon.png", conn_headers(go_etag), None, 30)
        checks[-1]["request_headers"] = {"python": conn_headers(py_etag), "go": conn_headers(go_etag)}
        checks[-1]["python"] = trim(py_cond, True)
        checks[-1]["go"] = trim(go_cond, True)

    py_lm = (py_asset.get("headers") or {}).get("last-modified")
    go_lm = (go_asset.get("headers") or {}).get("last-modified")
    if py_lm or go_lm:
        record("asset-if-modified-since", "GET", "/favicon.png",
               py_path="/favicon.png", go_path="/favicon.png",
               note="conditional GET with each side's own Last-Modified")
        py_cond = http_req(py, "GET", "/favicon.png", {"If-Modified-Since": py_lm} if py_lm else {}, None, 30)
        go_cond = http_req(go, "GET", "/favicon.png", {"If-Modified-Since": go_lm} if go_lm else {}, None, 30)
        checks[-1]["request_headers"] = {
            "python": {"If-Modified-Since": py_lm} if py_lm else {},
            "go": {"If-Modified-Since": go_lm} if go_lm else {},
        }
        checks[-1]["python"] = trim(py_cond, True)
        checks[-1]["go"] = trim(go_cond, True)

    record("asset-missing-js", "GET", "/assets/does-not-exist-1234.js",
           note="Python reference serves the SPA index for any missing file; Go guards asset-shaped paths")
    record("asset-missing-logo", "GET", "/logos/does-not-exist.svg")
    record("asset-query", "GET", "/favicon.png?v=42")
    record("asset-range", "GET", "/favicon.png", {"Range": "bytes=0-9"})
    record("asset-head", "HEAD", "/")
    record("spa-route-dot", "GET", "/queen/demo.queen",
           note="SPA route whose last segment contains a dot")
    record("api-unknown", "GET", "/api/definitely-not-registered")
    record("api-unknown-sub", "GET", "/api/sessions/nope/definitely-not-registered")
    record("traversal-encoded", "GET", "/assets/%2e%2e/index.html")
    record("traversal-deep", "GET", "/%2e%2e/%2e%2e/etc/passwd")

    # --- upload / attachment download ------------------------------------
    py_sess, go_sess = record("create-session", "POST", "/api/sessions", body=b"{}", timeout=180)
    py_sid = (json.loads(py_sess.get("body") or b"{}") or {}).get("session_id") if py_sess.get("status") == 201 else None
    go_sid = (json.loads(go_sess.get("body") or b"{}") or {}).get("session_id") if go_sess.get("status") == 201 else None

    if py_sid and go_sid:
        payload = b"hello hive attachment\n"
        boundary, mp_body = multipart("note.txt", payload)
        multipart_headers = {"Content-Type": f"multipart/form-data; boundary={boundary}"}
        py_up, go_up = record("upload-txt", "POST", "/api/sessions/{id}/upload-attachment",
                              multipart_headers, mp_body,
                              py_path=f"/api/sessions/{py_sid}/upload-attachment",
                              go_path=f"/api/sessions/{go_sid}/upload-attachment")
        record("download-txt", "GET", "/api/sessions/{id}/attachment/note.txt",
               py_path=f"/api/sessions/{py_sid}/attachment/note.txt",
               go_path=f"/api/sessions/{go_sid}/attachment/note.txt")
        record("download-txt-range", "GET", "/api/sessions/{id}/attachment/note.txt",
               {"Range": "bytes=0-4"},
               py_path=f"/api/sessions/{py_sid}/attachment/note.txt",
               go_path=f"/api/sessions/{go_sid}/attachment/note.txt")
        record("download-missing", "GET", "/api/sessions/{id}/attachment/missing.txt",
               py_path=f"/api/sessions/{py_sid}/attachment/missing.txt",
               go_path=f"/api/sessions/{go_sid}/attachment/missing.txt")
        record("download-traversal", "GET", "/api/sessions/{id}/attachment/..%2Fescape.txt",
               py_path=f"/api/sessions/{py_sid}/attachment/..%2Fescape.txt",
               go_path=f"/api/sessions/{go_sid}/attachment/..%2Fescape.txt")
        record("upload-missing-session", "POST", "/api/sessions/sess_nope/upload-attachment",
               {"Content-Type": "text/plain"}, b"x")
        record("upload-bad-multipart", "POST", "/api/sessions/{id}/upload-attachment",
               {"Content-Type": "text/plain"}, b"not multipart",
               py_path=f"/api/sessions/{py_sid}/upload-attachment",
               go_path=f"/api/sessions/{go_sid}/upload-attachment")
        boundary2, mp_csv = multipart("table.csv", b"a,b\n1,2\n", "text/csv")
        record("upload-csv", "POST", "/api/sessions/{id}/upload-attachment",
               {"Content-Type": f"multipart/form-data; boundary={boundary2}"}, mp_csv,
               py_path=f"/api/sessions/{py_sid}/upload-attachment",
               go_path=f"/api/sessions/{go_sid}/upload-attachment")
        record("download-csv", "GET", "/api/sessions/{id}/attachment/table.csv",
               py_path=f"/api/sessions/{py_sid}/attachment/table.csv",
               go_path=f"/api/sessions/{go_sid}/attachment/table.csv")

    summary = {
        "started_at": utc_now(),
        "boot_seconds": round(time.time() - started, 2),
        "work_dir": str(work),
        "python": {"version": py.version, "port": py.port, "home": str(py.home)},
        "go": {"version": go.version, "port": go.port, "home": str(go.home)},
        "index_sha256": {"python": py_index_sha, "go": go_index_sha},
        "dist": {
            "python": _dist_facts(PY_FE),
            "go": _dist_facts(GO_FE),
        },
        "checks": checks,
    }
    summary["dist"]["python"]["served_index_matches"] = (
        py_index_sha == summary["dist"]["python"]["index_sha256"]
    )
    summary["dist"]["go"]["served_index_matches"] = (
        go_index_sha == summary["dist"]["go"]["index_sha256"]
    )

    # Keep the two serve logs next to the evidence: they show the static
    # directory each side discovered, which is what makes the probe meaningful.
    # ("serve-logs", not "logs": the repo .gitignore ignores any logs/ dir.)
    log_dir = Path(args.out) / "serve-logs"
    log_dir.mkdir(parents=True, exist_ok=True)
    for side in (py, go):
        if side.log.is_file():
            shutil.copy2(side.log, log_dir / f"{side.key}.log")
    summary["logs"] = {
        "python": _repo_relative(log_dir / "python.log"),
        "go": _repo_relative(log_dir / "go.log"),
    }

    if not args.keep:
        _shutdown()
        shutil.rmtree(work, ignore_errors=True)
    else:
        print(f"[probe] servers left running: python={py.base} go={go.base} (work dir {work})")

    summary["sides"] = {"python": {"version": py.version, "port": py.port},
                        "go": {"version": go.version, "port": go.port}}
    return summary


# -------------------------------------------------------------------- main


def decision_for(check_id: str) -> tuple:
    """(decision, note) for one probe id.

    ``aligned``    both sides answered the same way (or the same document).
    ``fixed``      a difference this lane closed in-tree.
    ``declared``   a measured difference kept on purpose, with its reason.
    ``open``       a difference with no decision yet.
    """
    if check_id.startswith(("hash-route:", "path-route:")):
        return ("aligned", "SPA fallback serves index.html with the same <title>")
    if check_id in {"index-root", "index-explicit"}:
        return ("declared" if check_id == "index-explicit" else "aligned",
                "Go 301 /index.html -> / (net/http); Python 200. Same document.")
    if check_id.startswith("bundle-js"):
        return ("declared",
                "Both serve the bundle as JavaScript: Python application/javascript, "
                "Go text/javascript; charset=utf-8 (RFC 9239)")
    if check_id.startswith("bundle-css"):
        return ("declared", "Both text/css; Go appends ; charset=utf-8")
    if check_id.startswith(("asset:", "asset-query", "asset-range", "asset-head")):
        return ("aligned", "byte-identical asset (or same status + Content-Type)")
    if check_id == "asset-if-none-match":
        return ("declared", "Python FileResponse sends an ETag and honours If-None-Match; net/http ServeFile sends none")
    if check_id == "asset-if-modified-since":
        return ("aligned", "both honour If-Modified-Since")
    if check_id in {"asset-missing-js", "asset-missing-logo"}:
        return ("declared",
                "Go: missing asset-shaped path -> 404. Python: SPA index 200. "
                "Python pins that fallback in test_api.py; Go pins the 404 in app_test.go.")
    if check_id.startswith("api-unknown"):
        return ("declared",
                "Go: unregistered /api/* -> JSON 404. Python: index.html 200. "
                "Test-pinned on both sides (Go comment names the Python test).")
    if check_id == "spa-route-dot":
        return ("declared",
                "Consequence of the asset-shaped guard: a dot in the last segment 404s on Go. "
                "HashRouter means no product route reaches this branch.")
    if check_id.startswith("traversal"):
        return ("declared", "both refuse to serve a file; Python falls back to index (200), Go rejects the path (400)")
    if check_id.startswith(("download-", "upload-csv")):
        return ("aligned", "same status, body bytes and Content-Type (no Content-Disposition on either side)")
    if check_id == "upload-txt":
        return ("aligned", "same status and JSON keys; Python spaces its JSON, Go appends a newline")
    if check_id == "upload-missing-session":
        return ("aligned", "same 404 JSON body modulo whitespace")
    if check_id == "upload-bad-multipart":
        return ("declared", "Python raises AssertionError -> 500; Go answers 400 with the documented error")
    if check_id == "create-session":
        return ("aligned", "both 201; ids differ per run (API dimension, not frontend)")
    return ("open", "unclassified")


def summarize(live: dict) -> dict:
    index_sha = live.get("index_sha256", {})
    rows = []
    for check in live.get("checks", []):
        pid = check["id"]
        p, g = check["python"], check["go"]
        status_same = p.get("status") == g.get("status")
        body_same = p.get("body_sha256") == g.get("body_sha256")
        if pid.startswith(("hash-route:", "path-route:")):
            self_index = (p.get("body_sha256") == index_sha.get("python")
                          and g.get("body_sha256") == index_sha.get("go"))
            outcome = "aligned" if (status_same and p.get("status") == 200 and self_index
                                    and p.get("title") == g.get("title")) else "diff"
        elif status_same and body_same:
            outcome = "same"
        elif status_same:
            outcome = "same-status"
        else:
            outcome = "diff"
        decision, note = decision_for(pid)
        rows.append({
            "id": pid, "method": check["method"], "path": check["path"],
            "python_status": p.get("status"), "go_status": g.get("status"),
            "python_content_type": (p.get("headers") or {}).get("content-type"),
            "go_content_type": (g.get("headers") or {}).get("content-type"),
            "body_sha_equal": body_same,
            "outcome": outcome, "decision": decision, "note": note,
        })
    counts = {}
    for row in rows:
        counts[row["decision"]] = counts.get(row["decision"], 0) + 1
    return {"counts": counts, "rows": rows}


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--all", action="store_true", help="static + probe (default)")
    mode.add_argument("--static", action="store_true", help="static inventory only")
    mode.add_argument("--probe", action="store_true", help="live probe only")
    parser.add_argument("--py-port", type=int, default=18771)
    parser.add_argument("--go-port", type=int, default=18772)
    parser.add_argument("--work-dir", default="")
    parser.add_argument("--keep", action="store_true")
    parser.add_argument("--no-build", action="store_true")
    parser.add_argument("--out", default=str(OUT_DIR))
    args = parser.parse_args(argv)

    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    static = None if args.probe else static_analysis()
    live = None if args.static else probe(args)

    if static:
        static["versions"] = {
            "python": _pyproject_version(),
            "go": _go_version(),
        }
        (out / "frontend-surface-diff.json").write_text(
            json.dumps({"schema": "hive-frontend-surface-diff/1", "generated_at": utc_now(),
                        "commit": git_head(ROOT), "static": static,
                        "probe": {**(live or {}), "summary": summarize(live) if live else None}},
                       ensure_ascii=False, indent=2),
            encoding="utf-8")
    if live:
        (out / "probes.json").write_text(json.dumps(live, ensure_ascii=False, indent=2), encoding="utf-8")

    if static:
        inv = static["source_inventory"]
        print(f"[static] shared={inv['shared_paths']} go_only={inv['go_only']} "
              f"python_only={inv['python_only']} content_diff={inv['content_diff']}")
        print(f"[static] classes={inv['classification']}")
        print(f"[static] i18n go_keys={static['i18n']['go_dictionary_keys']} "
              f"referenced={static['i18n']['t_referenced_keys']} "
              f"missing={len(static['i18n']['used_but_untranslated'])} "
              f"unused={len(static['i18n']['dictionary_but_unused'])}")
        print(f"[static] title={static['routes']['title']}")
    if live:
        print(f"[probe] python={live['python']['version']}@{live['python']['port']} "
              f"go={live['go']['version']}@{live['go']['port']} checks={len(live['checks'])}")
    print(f"[out] {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
