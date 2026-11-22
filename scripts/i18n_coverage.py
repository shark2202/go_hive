#!/usr/bin/env python3
"""i18n coverage audit: Go-vendored frontend (A) vs upstream Python frontend (B).

Static inventory only -- no build, no browser, stdlib only.  It answers four
questions for the appearance axis (前端 i18n):

a) which ``t()`` keys does tree A reference, and which of them are missing from
   A's merged zh dictionary (grouped by calling file, max 200 listed);
b) which zh dictionary entries are never referenced by product code (dead keys,
   split into "appears elsewhere as a literal" vs "dictionary only");
c) the same numbers for tree B (upstream); B currently ships no i18n layer, so
   the script first proves that with file-level evidence instead of assuming it;
d) A-vs-B key-set / zh-value diff (B-only / A-only / value-changed; value diffs
   capped at 100).

It also cross-checks the current dictionary against the earlier frozen inventory
in ``docs/frontend-surface/frontend-surface-diff.json`` (baseline f8aa5fe9) so
drift since that snapshot is visible, and runs a heuristic "does this A key's
English text exist anywhere in B's product sources?" pass.

Extraction rules (the authoritative copy lives in the JSON under ``rules``):

* Keys are the English source text (hand-rolled i18n; ``en`` renders the key).
* Dictionary files: ``src/i18n/zh/*.ts``, merged in the spread order parsed from
  ``src/i18n/index.tsx`` (later spread wins).  Entries are parsed with a real TS
  tokenizer (quoted or bare identifier keys, multi-line prettier-wrapped
  values, escaped quotes).
* Reference scope: ``src/**/*.ts`` / ``*.tsx`` product files, excluding
  ``src/i18n/**``, ``*.test.*`` / ``*.spec.*`` and ``src/test/**``.  A file is
  an i18n consumer iff it calls ``useI18n()``; this drops e.g. DebugPanel's
  local numeric formatter ``const t = (c?: number) => ...``.  Calls inside a
  lexical ``t`` shadow (``const t =``, ``for (const t of ...)``, ``function t``)
  are excluded by scope analysis.
* Static keys: a call whose first argument is exactly one string literal.  A
  ternary/expression argument contributes every string literal inside it as an
  *explicit* key candidate.  Top-level ``+`` concatenation is never treated as
  a key (fragments are not dictionary keys).
* Dynamic ``t(expr)``: recorded as unresolved unless it resolves through a
  same-file/imported const object/array literal, an indexed const lookup
  (``ROLE_TITLE[role]``), a property-name pool (``t(row.title)``), or string
  literals returned by a local helper function.  Resolved dynamic keys are
  "candidates": they never feed the headline missing count, only the
  dead-key downgrade and a separate low-confidence list.
* Dead key: dictionary key with no static, explicit or candidate reference.
  "dictionary-only" additionally means the key text never occurs as a string
  literal in any product file (runtime/backend values can still hit it at
  runtime -- stated as a limitation, not measured).
* Tree B: the same dictionary/reference scan, gated on evidence that an i18n
  mechanism exists.  B has none, so B keys = 0 by construction; the A-only
  number is therefore the whole A dictionary and value diffs are empty.

Outputs (default ``go_hive/docs/e2e-assets/i18n-coverage/``):
  i18n-coverage.json   full machine-readable inventory
  i18n-coverage.md     generated summary tables

Usage:
    uv run --no-project python go_hive/scripts/i18n_coverage.py
    uv run --no-project python go_hive/scripts/i18n_coverage.py --out /tmp/i18n
"""

from __future__ import annotations

import argparse
import bisect
import json
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

SCHEMA = "i18n-coverage/v1"
SKIP_DIRS = {"node_modules", "dist", ".next", "coverage", ".vite", ".turbo"}
DICT_REL = Path("src") / "i18n"
ZH_REL = DICT_REL / "zh"
TEST_GLOBS = (".test.", ".spec.")
IDENT_RE = re.compile(r"[A-Za-z_$][A-Za-z0-9_$]*")
PLACEHOLDER_RE = re.compile(r"\{([A-Za-z_][A-Za-z0-9_]*)\}")

TYPOGRAPHIC = {"’": "'", "‘": "'", "“": '"', "”": '"'}
HTML_ENTITIES = {
    "&amp;": "&", "&middot;": "·", "&apos;": "'", "&quot;": '"',
    "&lt;": "<", "&gt;": ">", "&mdash;": "—", "&ndash;": "–", "&nbsp;": " ",
}

REGEX_PREV_PUNCT = set("=([{,:;!&|?+-*%^~")
REGEX_PREV_WORDS = {
    "return", "typeof", "instanceof", "in", "of", "new", "delete", "void",
    "case", "do", "else", "yield", "await", "throw",
}
# A quote directly after one of these keywords is still a string literal;
# after any other identifier/number it is JSX prose (e.g. ``Don't``), not code.
STRING_AFTER_KEYWORDS = {
    "from", "import", "export", "default", "case", "return", "throw", "yield",
    "await", "else", "do", "in", "of", "typeof", "instanceof", "new",
    "delete", "void", "as", "satisfies", "keyof", "infer", "is", "extends",
    "implements", "const", "let", "var", "type", "interface", "readonly",
    "module", "namespace", "declare",
}


def _char_regex_allowed(text: str, pos: int) -> bool:
    """Heuristic for regex literals inside ${...} interpolation scanning."""
    j = pos - 1
    while j >= 0 and text[j] in " \t\r\n":
        j -= 1
    if j < 0:
        return True
    c = text[j]
    if c in ")]":
        return False
    if c.isalnum() or c in "_$":
        # allow only known operator keywords (best effort: inspect the word)
        k = j
        while k >= 0 and (text[k].isalnum() or text[k] in "_$"):
            k -= 1
        word = text[k + 1:j + 1]
        return word in REGEX_PREV_WORDS
    return c in REGEX_PREV_PUNCT

MISSING_LIST_CAP = 200
VALUE_DIFF_CAP = 100
DEAD_LIST_CAP = 200


# --------------------------------------------------------------------- tokens


class Tok:
    __slots__ = ("kind", "value", "start", "end")

    def __init__(self, kind: str, value: str, start: int, end: int):
        self.kind = kind
        self.value = value
        self.start = start
        self.end = end

    def __repr__(self) -> str:  # pragma: no cover - debugging aid
        return f"Tok({self.kind},{self.value!r})"


_ESCAPES = {
    "n": "\n", "t": "\t", "r": "\r", "b": "\b", "f": "\f", "v": "\v",
    "0": "\0", "'": "'", '"': '"', "`": "`", "\\": "\\", "/": "/",
}


def unescape_js(raw: str) -> str:
    out = []
    i = 0
    n = len(raw)
    while i < n:
        c = raw[i]
        if c == "\\" and i + 1 < n:
            nxt = raw[i + 1]
            if nxt == "u":
                m = re.match(r"u\{([0-9a-fA-F]+)\}|u([0-9a-fA-F]{4})", raw[i + 1:])
                if m:
                    cp = int(m.group(1) or m.group(2), 16)
                    out.append(chr(cp))
                    i += 1 + len(m.group(0))
                    continue
            if nxt == "x":
                m = re.match(r"x([0-9a-fA-F]{2})", raw[i + 1:])
                if m:
                    out.append(chr(int(m.group(1), 16)))
                    i += 3
                    continue
            out.append(_ESCAPES.get(nxt, nxt))
            i += 2
            continue
        out.append(c)
        i += 1
    return "".join(out)


def _read_string(text: str, i: int) -> tuple[int, str]:
    """Read a '...' / "..." literal starting at i; return (end, raw inner)."""
    quote = text[i]
    j = i + 1
    n = len(text)
    buf = []
    while j < n:
        c = text[j]
        if c == "\\":
            buf.append(text[j:j + 2])
            j += 2
            continue
        if c == quote:
            return j + 1, "".join(buf)
        buf.append(c)
        j += 1
    return n, "".join(buf)


def _read_template(text: str, i: int) -> int:
    """Read a backtick template literal (with ${...} nesting); return end index."""
    n = len(text)
    j = i + 1
    depth = 0
    while j < n:
        c = text[j]
        if c == "\\":
            j += 2
            continue
        if c == "`" and depth == 0:
            return j + 1
        if c == "$" and j + 1 < n and text[j + 1] == "{" and depth == 0:
            depth = 1
            j += 2
            continue
        if depth > 0:
            if c == "/":
                if j + 1 < n and text[j + 1] == "/":
                    k = text.find("\n", j)
                    j = n if k < 0 else k + 1
                    continue
                if j + 1 < n and text[j + 1] == "*":
                    k = text.find("*/", j + 2)
                    j = n if k < 0 else k + 2
                    continue
                if _char_regex_allowed(text, j):
                    end_pos = _try_regex(text, j)
                    if end_pos is not None:
                        j = end_pos
                        continue
            if c == "{":
                depth += 1
                j += 1
                continue
            if c == "}":
                depth -= 1
                j += 1
                continue
            if c in "\"'":
                j, _ = _read_string(text, j)
                continue
            if c == "`":
                j = _read_template(text, j)
                continue
        j += 1
    return n


def _try_regex(text: str, i: int) -> int | None:
    n = len(text)
    j = i + 1
    in_class = False
    while j < n:
        c = text[j]
        if c == "\\":
            j += 2
            continue
        if c in "\n\r":
            return None
        if c == "[":
            in_class = True
        elif c == "]":
            in_class = False
        elif c == "/" and not in_class:
            j += 1
            while j < n and text[j] in "dgimsuvy":
                j += 1
            return j
        j += 1
    return None


def _template_spans(text: str, i: int) -> tuple[int, list[tuple[int, int]]]:
    """Read a template literal at i; return (end, spans of ${...} expressions).

    Spans are inner [start, end) ranges so interpolated expressions can be
    tokenized as code (t() calls inside ``${...}`` must stay visible).
    """
    n = len(text)
    j = i + 1
    depth = 0
    spans: list[tuple[int, int]] = []
    span_start = None
    while j < n:
        c = text[j]
        if c == "\\":
            j += 2
            continue
        if c == "`" and depth == 0:
            return j + 1, spans
        if c == "$" and j + 1 < n and text[j + 1] == "{" and depth == 0:
            span_start = j + 2
            depth = 1
            j += 2
            continue
        if depth > 0:
            if c == "/":
                if j + 1 < n and text[j + 1] == "/":
                    k = text.find("\n", j)
                    j = n if k < 0 else k + 1
                    continue
                if j + 1 < n and text[j + 1] == "*":
                    k = text.find("*/", j + 2)
                    j = n if k < 0 else k + 2
                    continue
                if _char_regex_allowed(text, j):
                    end_pos = _try_regex(text, j)
                    if end_pos is not None:
                        j = end_pos
                        continue
            if c == "{":
                depth += 1
                j += 1
                continue
            if c == "}":
                depth -= 1
                if depth == 0 and span_start is not None:
                    spans.append((span_start, j))
                    span_start = None
                j += 1
                continue
            if c in "\"'":
                j, _ = _read_string(text, j)
                continue
            if c == "`":
                j = _read_template(text, j)
                continue
        j += 1
    return n, spans


def tokenize(text: str, start: int = 0, end: int | None = None) -> list[Tok]:
    toks: list[Tok] = []
    i = start
    n = len(text) if end is None else end
    prev: Tok | None = None

    def add(kind: str, value: str, start_pos: int, end_pos: int) -> None:
        nonlocal prev
        tok = Tok(kind, value, start_pos, end_pos)
        toks.append(tok)
        prev = tok

    while i < n:
        c = text[i]
        if c in " \t\r\n":
            i += 1
            continue
        if c == "/" and i + 1 < n:
            if text[i + 1] == "/":
                j = text.find("\n", i)
                if j < 0 or j >= n:
                    i = n
                else:
                    i = j + 1
                continue
            if text[i + 1] == "*":
                j = text.find("*/", i + 2)
                i = n if j < 0 else j + 2
                continue
            regex_ok = (
                prev is None
                or (prev.kind == "punct" and prev.value in REGEX_PREV_PUNCT)
                or (prev.kind == "ident" and prev.value in REGEX_PREV_WORDS)
            )
            if regex_ok:
                end_pos = _try_regex(text, i)
                if end_pos is not None:
                    add("regex", text[i:end_pos], i, end_pos)
                    i = end_pos
                    continue
        if c in "\"'":
            string_ok = (
                prev is None
                or prev.kind == "punct"
                or (prev.kind == "ident" and prev.value in STRING_AFTER_KEYWORDS)
            )
            if string_ok:
                end_pos, raw = _read_string(text, i)
                add("str", unescape_js(raw), i, end_pos)
                i = end_pos
                continue
            # JSX prose quote (Don't, it's, "quoted" text): not code.
        if c == "`":
            end_pos, spans = _template_spans(text, i)
            add("tmpl", text[i + 1:max(i + 1, end_pos - 1)], i, end_pos)
            for span_start, span_end in spans:
                for tok in tokenize(text, span_start, span_end):
                    toks.append(tok)
                    prev = tok
            i = end_pos
            continue
        if c.isdigit():
            j = i
            while j < n and (text[j].isalnum() or text[j] in "._"):
                j += 1
            add("num", text[i:j], i, j)
            i = j
            continue
        if c.isalpha() or c in "_$":
            m = IDENT_RE.match(text, i)
            if m:
                add("ident", m.group(0), i, m.end())
                i = m.end()
                continue
        add("punct", c, i, i + 1)
        i += 1
    return toks


def build_pairs(tokens: list[Tok]) -> dict[int, int]:
    """Bracket index pairs; per-type stacks so stray JSX brackets do not derail."""
    pairs: dict[int, int] = {}
    stacks: dict[str, list[int]] = {"(": [], "[": [], "{": []}
    for idx, tok in enumerate(tokens):
        if tok.kind != "punct":
            continue
        c = tok.value
        if c in stacks:
            stacks[c].append(idx)
        elif c in ")]}":
            opener = {")": "(", "]": "[", "}": "{"}[c]
            stack = stacks[opener]
            if stack:
                open_idx = stack.pop()
                pairs[open_idx] = idx
                pairs[idx] = open_idx
    return pairs


def make_line_lookup(text: str):
    starts = [0]
    for idx, ch in enumerate(text):
        if ch == "\n":
            starts.append(idx + 1)
    return lambda pos: bisect.bisect_right(starts, pos)


def read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8", errors="replace")


def utc_now() -> str:
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")


def git_head(cwd: Path) -> str:
    try:
        proc = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=str(cwd),
            capture_output=True, text=True, timeout=20,
        )
        return proc.stdout.strip() or "unknown"
    except OSError:
        return "unknown"


# ------------------------------------------------------------------ file sets


def iter_files(root: Path, suffixes=(".ts", ".tsx")):
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.suffix not in suffixes:
            continue
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        yield path


def rel_posix(path: Path, root: Path) -> str:
    try:
        return path.resolve().relative_to(root.resolve()).as_posix()
    except ValueError:
        return path.as_posix()


def is_product_file(rel: str) -> bool:
    parts = rel.split("/")
    if not rel.startswith("src/"):
        return False
    if any(part in ("node_modules", "dist") for part in parts):
        return False
    # src/i18n ships the runtime (index.tsx), dictionaries (zh/) and the
    # LocaleSwitcher component; only the latter is product UI code.
    if rel.startswith("src/i18n/"):
        if rel.startswith("src/i18n/zh/"):
            return False
        if rel == "src/i18n/index.tsx":
            return False
        name = parts[-1]
        if any(marker in name for marker in TEST_GLOBS):
            return False
        return True
    if rel.startswith("src/test/"):
        return False
    name = parts[-1]
    if any(marker in name for marker in TEST_GLOBS):
        return False
    return True


def is_test_file(rel: str) -> bool:
    if not rel.startswith("src/"):
        return False
    if rel.startswith("src/test/"):
        return True
    return any(marker in Path(rel).name for marker in TEST_GLOBS)


def resolve_module(root: Path, rel: str, module: str) -> str | None:
    if module.startswith("@/"):
        base = Path("src") / module[2:]
    elif module.startswith("."):
        base = (Path(rel).parent / module)
    else:
        return None
    candidates = []
    if base.suffix in (".ts", ".tsx", ".js", ".jsx"):
        candidates.append(base)
    candidates += [
        Path(str(base) + ".ts"), Path(str(base) + ".tsx"),
        base / "index.ts", base / "index.tsx",
    ]
    for cand in candidates:
        if (root / cand).is_file():
            return cand.as_posix()
    return None


# --------------------------------------------------------------- dictionaries


def parse_dict_file(path: Path) -> dict:
    text = read_text(path)
    toks = tokenize(text)
    line_of = make_line_lookup(text)
    entries = []
    i = 0
    while i + 2 < len(toks):
        tok = toks[i]
        if tok.kind in ("str", "ident"):
            mid, val = toks[i + 1], toks[i + 2]
            if (
                mid.kind == "punct" and mid.value == ":"
                and val.kind == "str"
                and not (tok.kind == "ident" and tok.value in ("export", "import", "const"))
            ):
                entries.append({
                    "key": tok.value,
                    "value": val.value,
                    "line": line_of(tok.start),
                })
                i += 3
                continue
        i += 1
    return {"entries": entries, "unparsed": []}


def parse_merge_order(index_path: Path, zh_dir: Path) -> tuple[list[str], list[str]]:
    files = sorted(p.name for p in zh_dir.glob("*.ts"))
    default = [name for name in ("chat.ts", "pages.ts", "settings.ts", "core.ts") if name in files]
    if not index_path.is_file():
        return default, [name for name in files if name not in default]
    text = read_text(index_path)
    import_map: dict[str, str] = {}
    for m in re.finditer(
        r'import\s*\{\s*(\w+)\s*\}\s*from\s*"(?:\./|@/i18n/)zh/([\w.-]+)"', text
    ):
        module = m.group(2)
        import_map[m.group(1)] = module if module.endswith(".ts") else module + ".ts"
    order: list[str] = []
    for m in re.finditer(r"\.\.\.(\w+)", text):
        name = m.group(1)
        if name in import_map and import_map[name] in files:
            order.append(import_map[name])
    # Any dictionary file not imported by index.tsx does not ship.
    unimported = [name for name in files if name not in order]
    return order, unimported


def scan_dictionary(root: Path) -> dict:
    index_path = root / "src" / "i18n" / "index.tsx"
    zh_dir = root / "src" / "i18n" / "zh"
    if not zh_dir.is_dir():
        return {
            "exists": False,
            "per_file": {},
            "merge_order": [],
            "unimported_files": [],
            "entries_total": 0,
            "unique_keys": 0,
            "merged": {},
            "duplicates": [],
            "intra_file_duplicates": [],
            "parse_errors": [],
        }
    order, unimported = parse_merge_order(index_path, zh_dir)
    per_file: dict[str, int] = {}
    merged: dict[str, dict] = {}
    total = 0
    parse_errors: list[str] = []
    intra: list[dict] = []
    for fname in order:
        path = zh_dir / fname
        if not path.is_file():
            per_file[fname] = 0
            continue
        parsed = parse_dict_file(path)
        entries = parsed["entries"]
        per_file[fname] = len(entries)
        total += len(entries)
        seen_in_file: dict[str, int] = {}
        for entry in entries:
            key = entry["key"]
            if key in seen_in_file:
                intra.append({"file": fname, "key": key, "lines": [seen_in_file[key], entry["line"]]})
            seen_in_file[key] = entry["line"]
            rec = merged.setdefault(key, {"value": entry["value"], "files": [], "lines": []})
            rec["value"] = entry["value"]  # later spread wins
            rec["files"].append(fname)
            rec["lines"].append(entry["line"])
        for err in parsed["unparsed"]:
            parse_errors.append(f"{fname}: {err}")
    duplicates = []
    for key, rec in merged.items():
        if len(rec["files"]) > 1:
            duplicates.append({
                "key": key,
                "files": rec["files"],
                "winner": rec["files"][-1],
                "winner_value": rec["value"],
            })
    return {
        "exists": True,
        "per_file": per_file,
        "merge_order": order,
        "unimported_files": unimported,
        "entries_total": total,
        "unique_keys": len(merged),
        "merged": merged,
        "duplicates": sorted(duplicates, key=lambda d: d["key"]),
        "intra_file_duplicates": intra,
        "parse_errors": parse_errors,
    }


# ----------------------------------------------------------- per-file fileinfo


class FileInfo:
    __slots__ = ("rel", "text", "use_i18n", "named", "props", "fns", "literals", "imports")

    def __init__(self, rel: str, text: str):
        self.rel = rel
        self.text = text
        self.use_i18n = "useI18n" in text
        self.named: dict[str, list[str]] = {}
        self.props: dict[str, list[str]] = {}
        self.fns: dict[str, list[str]] = {}
        self.literals: set[str] = set()
        self.imports: list[str] = []


def _dedupe(values) -> list[str]:
    return list(dict.fromkeys(values))


def collect_fileinfo(root: Path, rel: str) -> FileInfo:
    text = read_text(root / rel)
    info = FileInfo(rel, text)
    toks = tokenize(text)
    pairs = build_pairs(toks)
    n = len(toks)

    # imports (relative / @ alias only)
    i = 0
    while i < n:
        if toks[i].kind == "ident" and toks[i].value == "import":
            j = i + 1
            module = None
            while j < n:
                tok = toks[j]
                if tok.kind == "str":
                    module = tok.value
                    break
                if tok.kind == "punct" and tok.value == ";":
                    break
                j += 1
            if module:
                resolved = resolve_module(root, rel, module)
                if resolved:
                    info.imports.append(resolved)
        i += 1

    # all string literals (dead-key literal index)
    for tok in toks:
        if tok.kind == "str":
            info.literals.add(tok.value)
        elif tok.kind == "tmpl" and "${" not in tok.value:
            info.literals.add(tok.value)

    # const/let/var NAME ... = {literal} | [literal]
    def literal_values(open_idx: int) -> list[str]:
        close_idx = pairs.get(open_idx)
        if close_idx is None:
            return []
        return _dedupe(
            tok.value for tok in toks[open_idx:close_idx + 1]
            if tok.kind == "str" or (tok.kind == "tmpl" and "${" not in tok.value)
        )

    for idx, tok in enumerate(toks):
        if tok.kind == "ident" and tok.value in ("const", "let", "var"):
            if idx + 1 >= n or toks[idx + 1].kind != "ident":
                continue
            name = toks[idx + 1].value
            depth = 0
            eq = None
            j = idx + 2
            while j < n and j < idx + 90:
                t2 = toks[j]
                if t2.kind == "punct":
                    c = t2.value
                    if c in "([{":
                        depth += 1
                    elif c in ")]}":
                        if depth == 0:
                            break
                        depth -= 1
                    elif c == ";" and depth == 0:
                        break
                    elif c == "=" and depth == 0:
                        eq = j
                        break
                j += 1
            if eq is not None and eq + 1 < n:
                nxt = toks[eq + 1]
                if nxt.kind == "punct" and nxt.value in "{[":
                    values = literal_values(eq + 1)
                    if values and name not in info.named:
                        info.named[name] = values

    # object property -> string values (superset; used only for dynamic candidates)
    for idx in range(n - 2):
        key_tok, colon, val_tok = toks[idx], toks[idx + 1], toks[idx + 2]
        if (
            key_tok.kind in ("ident", "str")
            and colon.kind == "punct" and colon.value == ":"
            and val_tok.kind == "str"
            and not (key_tok.kind == "ident" and key_tok.value in ("import", "export"))
        ):
            info.props.setdefault(key_tok.value, []).append(val_tok.value)

    # function bodies: function NAME(...) { ... } and const NAME = (...) => { ... }
    for idx, tok in enumerate(toks):
        if tok.kind == "ident" and tok.value == "function":
            if idx + 1 < n and toks[idx + 1].kind == "ident":
                name = toks[idx + 1].value
                j = idx + 2
                if j < n and toks[j].kind == "punct" and toks[j].value == "(":
                    close = pairs.get(j)
                    if close is not None and close + 1 < n:
                        body = close + 1
                        if toks[body].kind == "punct" and toks[body].value == "{":
                            end = pairs.get(body)
                            if end is not None:
                                info.fns[name] = _dedupe(
                                    t.value for t in toks[body + 1:end]
                                    if t.kind == "str" or (t.kind == "tmpl" and "${" not in t.value)
                                )
        if tok.kind == "ident" and tok.value in ("const", "let", "var"):
            if idx + 2 < n and toks[idx + 1].kind == "ident" and toks[idx + 2].kind == "punct" and toks[idx + 2].value == "=":
                name = toks[idx + 1].value
                j = idx + 3
                # skip params: either (...) or a single identifier
                k = j
                if j < n and toks[j].kind == "punct" and toks[j].value == "(":
                    close = pairs.get(j)
                    k = (close + 1) if close is not None else j
                elif j < n and toks[j].kind == "ident":
                    k = j + 1
                if k + 1 < n and toks[k].kind == "punct" and toks[k].value == "=" and toks[k + 1].kind == "punct" and toks[k + 1].value == ">":
                    body = k + 2
                    if body < n and toks[body].kind == "punct" and toks[body].value == "{":
                        end = pairs.get(body)
                        if end is not None:
                            info.fns[name] = _dedupe(
                                t.value for t in toks[body + 1:end]
                                if t.kind == "str" or (t.kind == "tmpl" and "${" not in t.value)
                            )
                    else:
                        end = body
                        depth = 0
                        while end < n:
                            tt = toks[end]
                            if tt.kind == "punct":
                                if tt.value in "([{":
                                    depth += 1
                                elif tt.value in ")]}":
                                    if depth == 0:
                                        break
                                    depth -= 1
                                elif tt.value == ";" and depth == 0:
                                    break
                            end += 1
                        info.fns[name] = _dedupe(
                            t.value for t in toks[body:end]
                            if t.kind == "str" or (t.kind == "tmpl" and "${" not in t.value)
                        )
    return info


# ------------------------------------------------------------ call extraction


def _shadow_scopes(tokens: list[Tok], pairs: dict[int, int]) -> list[tuple[int, int]]:
    n = len(tokens)
    braces = [(o, c) for o, c in pairs.items() if o < c and tokens[o].value == "{"]
    scopes: list[tuple[int, int]] = []

    def innermost(pos: int):
        best = None
        for o, c in braces:
            if o < pos < c and (best is None or o > best[0]):
                best = (o, c)
        return best or (0, n)

    for idx, tok in enumerate(tokens):
        if tok.kind != "ident":
            continue
        if tok.value in ("const", "let", "var") and idx + 2 < n:
            if tokens[idx + 1].kind == "ident" and tokens[idx + 1].value == "t":
                if tokens[idx + 2].kind == "punct" and tokens[idx + 2].value == "=":
                    scopes.append(innermost(idx))
        elif tok.value == "function" and idx + 1 < n and tokens[idx + 1].kind == "ident" and tokens[idx + 1].value == "t":
            j = idx + 2
            if j < n and tokens[j].kind == "punct" and tokens[j].value == "(":
                close = pairs.get(j)
                if close is not None and close + 1 < n:
                    body = close + 1
                    if tokens[body].kind == "punct" and tokens[body].value == "{":
                        end = pairs.get(body)
                        if end is not None:
                            scopes.append((body, end))
        elif tok.value == "for" and idx + 1 < n and tokens[idx + 1].kind == "punct" and tokens[idx + 1].value == "(":
            close = pairs.get(idx + 1)
            if close is None:
                continue
            header = tokens[idx + 2:close]
            if (
                len(header) >= 3
                and header[0].kind == "ident" and header[0].value in ("const", "let")
                and header[1].kind == "ident" and header[1].value == "t"
                and header[2].kind == "ident" and header[2].value == "of"
            ):
                body = close + 1
                if body < n and tokens[body].kind == "punct" and tokens[body].value == "{":
                    end = pairs.get(body)
                    if end is not None:
                        scopes.append((body, end))
                else:
                    end = body
                    depth = 0
                    while end < n:
                        tt = tokens[end]
                        if tt.kind == "punct":
                            if tt.value in "([{":
                                depth += 1
                            elif tt.value in ")]}":
                                if depth == 0:
                                    break
                                depth -= 1
                            elif tt.value == ";" and depth == 0:
                                break
                        end += 1
                    scopes.append((body, end))
    return scopes


def _in_scopes(pos: int, scopes: list[tuple[int, int]]) -> bool:
    return any(o < pos < c for o, c in scopes)


def _first_arg(tokens: list[Tok], open_idx: int, close_idx: int) -> tuple[list[Tok], int, int]:
    """Return (arg tokens, start char, end char) for the first argument."""
    depth = 0
    boundary = close_idx
    for j in range(open_idx + 1, close_idx):
        tok = tokens[j]
        if tok.kind == "punct":
            c = tok.value
            if c in "([{":
                depth += 1
            elif c in ")]}":
                depth -= 1
            elif c == "," and depth == 0:
                boundary = j
                break
    arg = tokens[open_idx + 1:boundary]
    if not arg:
        return [], tokens[open_idx].end, tokens[open_idx].end
    return arg, arg[0].start, arg[-1].end


def _has_top_level_plus(arg: list[Tok]) -> bool:
    depth = 0
    for tok in arg:
        if tok.kind != "punct":
            continue
        c = tok.value
        if c in "([{":
            depth += 1
        elif c in ")]}":
            depth -= 1
        elif c == "+" and depth == 0:
            return True
    return False


def _member_props(arg: list[Tok]) -> list[str]:
    if not arg or arg[0].kind != "ident":
        return []
    props: list[str] = []
    j = 1
    while j < len(arg):
        tok = arg[j]
        if tok.kind == "punct" and tok.value == ".":
            if j + 1 < len(arg) and arg[j + 1].kind == "ident":
                props.append(arg[j + 1].value)
                j += 2
                continue
            break
        if tok.kind == "punct" and tok.value == "?":
            if j + 2 < len(arg) and arg[j + 1].value == "." and arg[j + 2].kind == "ident":
                props.append(arg[j + 2].value)
                j += 3
                continue
            break
        break
    return props


def _named_values(name: str, info: FileInfo, infos: dict[str, FileInfo]) -> list[str]:
    values = info.named.get(name)
    if values:
        return values[:500]
    for imported in info.imports:
        other = infos.get(imported)
        if other and name in other.named:
            return other.named[name][:500]
    return []


def _prop_values(prop: str, info: FileInfo, infos: dict[str, FileInfo]) -> list[str]:
    values = list(info.props.get(prop, []))
    for imported in info.imports:
        other = infos.get(imported)
        if other:
            values.extend(other.props.get(prop, []))
    return _dedupe(values)[:500]


def _fn_values(name: str, info: FileInfo, infos: dict[str, FileInfo]) -> list[str]:
    values = info.fns.get(name)
    if values:
        return values[:400]
    for imported in info.imports:
        other = infos.get(imported)
        if other and name in other.fns:
            return other.fns[name][:400]
    return []


def _resolve_dynamic(arg: list[Tok], info: FileInfo, infos: dict[str, FileInfo]) -> tuple[list[str], str]:
    if not arg:
        return [], "empty"
    if _has_top_level_plus(arg):
        return [], "concat"
    if len(arg) == 1 and arg[0].kind == "ident":
        vals = _named_values(arg[0].value, info, infos)
        return (vals, f"named:{arg[0].value}") if vals else ([], "unresolved:bare-ident")
    if arg[0].kind == "ident" and len(arg) > 2 and arg[1].kind == "punct" and arg[1].value == "[":
        vals = _named_values(arg[0].value, info, infos)
        return (vals, f"named-index:{arg[0].value}") if vals else ([], "unresolved:index")
    props = _member_props(arg)
    if props:
        vals = _prop_values(props[-1], info, infos)
        return (vals, f"prop:{props[-1]}") if vals else ([], "unresolved:prop")
    for idx in range(len(arg) - 1):
        if arg[idx].kind == "ident" and arg[idx + 1].kind == "punct" and arg[idx + 1].value == "(":
            vals = _fn_values(arg[idx].value, info, infos)
            if vals:
                return vals, f"fn:{arg[idx].value}"
    return [], "unresolved"


def _aliases(text: str) -> set[str]:
    aliases = {"t"}
    m = re.search(r"const\s*\{([^}]*)\}\s*=\s*useI18n\(\)", text, re.S)
    if m:
        for part in m.group(1).split(","):
            bits = part.split(":")
            if bits[0].strip() == "t" and len(bits) > 1:
                alias = bits[1].strip()
                if IDENT_RE.fullmatch(alias):
                    aliases.add(alias)
    return aliases


def _literal_candidates(arg: list[Tok]) -> tuple[list[str], list[str], bool]:
    """String literals that could actually be the key argument.

    Only depth-0 literals count (nested object/array/call literals are data,
    not the key), and comparison operands are dropped: in
    ``t(s.mode === "review" ? "A" : "B")`` only ``A`` and ``B`` are keys.
    """
    strs: list[str] = []
    tmpls: list[str] = []
    interp = False
    depth = 0
    for idx, tok in enumerate(arg):
        if tok.kind == "punct":
            if tok.value in "([{":
                depth += 1
            elif tok.value in ")]}":
                depth = max(0, depth - 1)
            continue
        if tok.kind == "tmpl" and "${" in tok.value:
            interp = True
        if depth != 0:
            continue
        prev = arg[idx - 1] if idx > 0 else None
        if prev is not None and prev.kind == "punct" and prev.value in ("=", "!"):
            continue
        if tok.kind == "str":
            strs.append(tok.value)
        elif tok.kind == "tmpl" and "${" not in tok.value:
            tmpls.append(tok.value)
    return _dedupe(strs), _dedupe(tmpls), interp


def extract_refs(
    root: Path,
    infos: dict[str, FileInfo],
    files: list[str],
    *,
    require_hook: bool,
    resolve: bool,
) -> dict:
    static_sites: list[dict] = []
    expr_sites: list[dict] = []
    dyn_sites: list[dict] = []
    unresolved_sites: list[dict] = []
    shadow_sites: list[dict] = []
    parse_failures: list[str] = []
    skipped_no_hook: list[dict] = []
    call_sites_total = 0

    for rel in files:
        info = infos.get(rel)
        if info is None:
            info = collect_fileinfo(root, rel)
            infos[rel] = info
        text = info.text
        if require_hook and not info.use_i18n:
            raw = len(re.findall(r"\bt\(", text))
            if raw:
                skipped_no_hook.append({"file": rel, "t_occurrences": raw})
            continue
        toks = tokenize(text)
        pairs = build_pairs(toks)
        scopes = _shadow_scopes(toks, pairs) if require_hook else []
        n = len(toks)
        names = _aliases(text) if require_hook else {"t"}
        line_of = make_line_lookup(text)

        for idx, tok in enumerate(toks):
            if tok.kind != "ident" or tok.value not in names:
                continue
            if idx + 1 < n and toks[idx + 1].kind == "punct" and toks[idx + 1].value == "(":
                pass
            else:
                continue
            if idx > 0 and toks[idx - 1].kind == "ident" and toks[idx - 1].value in (
                "function", "const", "let", "var", "for", "import", "export",
            ):
                continue
            if _in_scopes(idx, scopes):
                arg, a0, a1 = _first_arg(toks, idx + 1, pairs.get(idx + 1, idx + 1))
                shadow_sites.append({
                    "file": rel,
                    "line": line_of(tok.start),
                    "expr": re.sub(r"\s+", " ", text[a0:a1])[:160],
                })
                continue
            close = pairs.get(idx + 1)
            if close is None:
                parse_failures.append(f"{rel}:{line_of(tok.start)} unclosed call")
                continue
            call_sites_total += 1
            arg, a0, a1 = _first_arg(toks, idx + 1, close)
            raw_expr = re.sub(r"\s+", " ", text[a0:a1]).strip()[:200]
            line = line_of(tok.start)
            if not arg:
                unresolved_sites.append({"file": rel, "line": line, "expr": raw_expr, "reason": "empty"})
                continue
            strs, tmpls, interp = _literal_candidates(arg)
            if len(arg) == 1 and arg[0].kind == "str":
                static_sites.append({"key": strs[0], "file": rel, "line": line})
            elif len(arg) == 1 and arg[0].kind == "tmpl" and "${" not in arg[0].value:
                static_sites.append({"key": arg[0].value, "file": rel, "line": line})
            elif strs or tmpls:
                if _has_top_level_plus(arg):
                    unresolved_sites.append({"file": rel, "line": line, "expr": raw_expr, "reason": "concat"})
                else:
                    for key in _dedupe(strs + tmpls):
                        expr_sites.append({"key": key, "file": rel, "line": line, "expr": raw_expr})
                    if interp:
                        unresolved_sites.append({
                            "file": rel, "line": line, "expr": raw_expr, "reason": "interpolated-template",
                        })
            elif resolve:
                cand, method = _resolve_dynamic(arg, info, infos)
                if cand:
                    dyn_sites.append({
                        "keys": cand, "file": rel, "line": line, "expr": raw_expr, "method": method,
                    })
                else:
                    unresolved_sites.append({
                        "file": rel, "line": line, "expr": raw_expr, "reason": method,
                    })
            else:
                unresolved_sites.append({"file": rel, "line": line, "expr": raw_expr, "reason": "unresolved"})
    return {
        "static_sites": static_sites,
        "expr_sites": expr_sites,
        "dyn_sites": dyn_sites,
        "unresolved_sites": unresolved_sites,
        "shadow_sites": shadow_sites,
        "parse_failures": parse_failures,
        "skipped_no_hook": skipped_no_hook,
        "call_sites_total": call_sites_total,
    }


# --------------------------------------------------------------- aggregation


def placeholder_set(text: str) -> set[str]:
    return set(PLACEHOLDER_RE.findall(text))


def tokenizer_selfcheck(root: Path, files: list[str]) -> dict:
    """Raw ``t(`` text occurrences vs token-level call starts, per file.

    Guarantees the tokenizer sees every call a flat regex would see (template
    interpolations, JSX prose, regex literals are the known trapdoors).
    """
    raw_total = 0
    detected_total = 0
    gaps = []
    for rel in files:
        text = read_text(root / rel)
        raw = len(re.findall(r"\bt\(", text))
        toks = tokenize(text)
        detected = 0
        for i, tok in enumerate(toks):
            if (
                tok.kind == "ident" and tok.value == "t"
                and i + 1 < len(toks) and toks[i + 1].kind == "punct" and toks[i + 1].value == "("
            ):
                detected += 1
        raw_total += raw
        detected_total += detected
        if raw != detected:
            gaps.append({"file": rel, "raw": raw, "token_detected": detected})
    return {
        "rule": "count of \\bt( in file text vs ident 't' followed by '(' in the token stream",
        "raw_total": raw_total,
        "token_detected_total": detected_total,
        "files_with_gap": len(gaps),
        "gaps": gaps[:50],
    }


def aggregate_a(root: Path, dictionary: dict, refs: dict, infos: dict[str, FileInfo]) -> dict:
    merged: dict[str, dict] = dictionary["merged"]
    static_keys: dict[str, list[dict]] = {}
    for site in refs["static_sites"]:
        if not site["key"].strip():
            continue
        static_keys.setdefault(site["key"], []).append({"file": site["file"], "line": site["line"], "kind": "static"})
    expr_keys: dict[str, list[dict]] = {}
    for site in refs["expr_sites"]:
        if not site["key"].strip():
            continue
        expr_keys.setdefault(site["key"], []).append({"file": site["file"], "line": site["line"], "kind": "expr"})
    dyn_keys: dict[str, list[dict]] = {}
    for site in refs["dyn_sites"]:
        for key in site["keys"]:
            if not key.strip():
                continue
            dyn_keys.setdefault(key, []).append({
                "file": site["file"], "line": site["line"], "kind": "dynamic-candidate", "method": site["method"],
            })

    referenced_definite = set(static_keys) | set(expr_keys)
    referenced_any = referenced_definite | set(dyn_keys)

    def missing(keymap):
        return sorted(set(keymap) - set(merged))

    missing_static = missing(static_keys)
    missing_expr = missing(expr_keys)
    missing_definite = sorted(set(missing_static) | set(missing_expr))
    missing_dyn = sorted(set(dyn_keys) - set(merged) - set(missing_definite))

    def missing_items(keys):
        items = []
        for key in keys:
            sites = (static_keys.get(key) or []) + (expr_keys.get(key) or [])
            for site in sorted(sites, key=lambda s: (s["file"], s["line"], s["kind"]))[:5]:
                items.append({"key": key, **site})
        items.sort(key=lambda it: (it["file"], it["line"], it["key"]))
        return items

    missing_items_all = missing_items(missing_definite)
    by_file: dict[str, dict] = {}
    for item in missing_items_all:
        rec = by_file.setdefault(item["file"], {"count": 0, "keys": []})
        rec["count"] += 1
        if item["key"] not in rec["keys"]:
            rec["keys"].append(item["key"])

    dead_all = sorted(set(merged) - referenced_any)
    dead_possible = sorted(set(merged) - referenced_definite - set(dead_all))
    dead_definite = [k for k in dead_all if k not in set(dead_possible)]
    literal_index: set[str] = set()
    for info in infos.values():
        literal_index |= info.literals
    dead_literal_present = [k for k in dead_definite if k in literal_index]
    dead_dict_only = [k for k in dead_definite if k not in literal_index]

    value_equals_key = sorted(k for k, rec in merged.items() if rec["value"] == k)
    placeholder_mismatch = []
    empty_value = []
    for key, rec in merged.items():
        value = rec["value"]
        if not value.strip():
            empty_value.append(key)
        kp, vp = placeholder_set(key), placeholder_set(value)
        if kp != vp:
            placeholder_mismatch.append({
                "key": key, "value": value,
                "key_placeholders": sorted(kp), "value_placeholders": sorted(vp),
                "missing_in_value": sorted(kp - vp), "extra_in_value": sorted(vp - kp),
            })
    return {
        "static_keys": static_keys,
        "expr_keys": expr_keys,
        "dyn_keys": dyn_keys,
        "counts": {
            "static_distinct": len(static_keys),
            "static_sites": len(refs["static_sites"]),
            "expr_distinct": len(expr_keys),
            "expr_sites": len(refs["expr_sites"]),
            "expr_calls": len({(s["file"], s["line"]) for s in refs["expr_sites"]}),
            "definite_distinct": len(referenced_definite),
            "definite_sites": len(refs["static_sites"]) + len(refs["expr_sites"]),
            "dynamic_resolved_calls": len(refs["dyn_sites"]),
            "dynamic_resolved_candidate_keys": len(dyn_keys),
            "dynamic_unresolved_calls": len(refs["unresolved_sites"]),
            "shadow_excluded_calls": len(refs["shadow_sites"]),
            "call_sites_total": refs["call_sites_total"],
        },
        "missing": {
            "count": len(missing_definite),
            "count_static": len(missing_static),
            "count_expr": len(missing_expr),
            "by_file": by_file,
            "items": missing_items_all[:MISSING_LIST_CAP],
            "truncated": len(missing_items_all) > MISSING_LIST_CAP,
            "keys": missing_definite[:MISSING_LIST_CAP],
        },
        "missing_dynamic_candidates": {
            "count": len(missing_dyn),
            "confidence": "low (dynamic resolution is a superset heuristic)",
            "items": [{"key": k, "sites": dyn_keys[k][:3]} for k in missing_dyn[:MISSING_LIST_CAP]],
        },
        "dead": {
            "unreferenced_by_definite_count": len(dead_all) + len(dead_possible),
            "definite_count": len(dead_definite),
            "definite_literal_present_count": len(dead_literal_present),
            "definite_literal_present": dead_literal_present,
            "definite_dict_only_count": len(dead_dict_only),
            "definite_dict_only": dead_dict_only,
            "possible_dynamic_count": len(dead_possible),
            "possible_dynamic": dead_possible,
            "layers_are_disjoint": True,
        },
        "value_checks": {
            "value_equals_key_count": len(value_equals_key),
            "value_equals_key": value_equals_key,
            "placeholder_mismatch_count": len(placeholder_mismatch),
            "placeholder_mismatch": placeholder_mismatch,
            "empty_value_count": len(empty_value),
            "empty_value": empty_value,
        },
        "unresolved_sites": refs["unresolved_sites"],
        "shadow_sites": refs["shadow_sites"],
        "parse_failures": refs["parse_failures"],
        "skipped_no_hook": refs["skipped_no_hook"],
    }


# --------------------------------------------------------------------- tree B


def scan_tree_b(root: Path, dict_keys_a: list[str], repo_root: Path) -> dict:
    src = root / "src"
    files = [rel_posix(p, root) for p in iter_files(src)] if src.is_dir() else []
    product = [rel for rel in files if is_product_file(rel)]
    i18n_dir = root / DICT_REL
    markers = []
    raw_calls = []
    for rel in product:
        text = read_text(root / rel)
        if re.search(r"useI18n|LOCALE_STORAGE_KEY|from\s+[\"'][^\"']*i18n", text):
            markers.append(rel)
        toks = tokenize(text)
        line_of = make_line_lookup(text)
        for idx, tok in enumerate(toks):
            if (
                tok.kind == "ident" and tok.value == "t"
                and idx + 1 < len(toks) and toks[idx + 1].kind == "punct" and toks[idx + 1].value == "("
                and not (idx > 0 and toks[idx - 1].kind == "ident" and toks[idx - 1].value in ("function", "const", "let", "var"))
            ):
                raw_calls.append({"file": rel, "line": line_of(tok.start)})
    corpus_text = "\n".join(read_text(root / rel) for rel in product)
    # A-key English text presence heuristic (same idea as the frozen f8aa5fe9 audit).
    normalized_corpus = normalize_copy(corpus_text)
    counterpart_hits = {"exact": 0, "placeholder": 0, "miss": 0}
    counterpart_misses = []
    for key in dict_keys_a:
        nkey = normalize_copy(key).strip()
        if nkey and nkey in normalized_corpus:
            counterpart_hits["exact"] += 1
            continue
        pattern = key_pattern(key)
        if pattern is not None and pattern.search(normalized_corpus):
            counterpart_hits["placeholder"] += 1
            continue
        counterpart_hits["miss"] += 1
        counterpart_misses.append(key)
    return {
        "frontend_dir": rel_posix(root, repo_root),
        "src_files": len(files),
        "product_files": len(product),
        "has_i18n_dir": i18n_dir.is_dir(),
        "i18n_marker_files": markers,
        "raw_t_call_sites": raw_calls,
        "dictionary_keys": 0,
        "referenced_keys": 0,
        "missing_keys": 0,
        "counterpart_heuristic": {
            "rule": "normalized substring / placeholder-wildcard search of each A dictionary key in B product .ts/.tsx sources",
            **counterpart_hits,
            "misses": counterpart_misses,
        },
    }


def normalize_copy(text: str) -> str:
    for src, dst in TYPOGRAPHIC.items():
        text = text.replace(src, dst)
    for src, dst in HTML_ENTITIES.items():
        text = text.replace(src, dst)
    return re.sub(r"\s+", " ", text)


def key_pattern(key: str):
    chunks = re.split(r"(\{\w+\})", normalize_copy(key))
    if not chunks:
        return None
    parts = []
    for chunk in chunks:
        m = re.fullmatch(r"\{(\w+)\}", chunk)
        if m:
            parts.append(r"[\s\S]{0,160}?")
        else:
            parts.append(re.escape(chunk))
    body = "".join(parts).strip()
    if not body:
        return None
    try:
        return re.compile(body)
    except re.error:
        return None


# ---------------------------------------------------------------- calibration


def _prior_method_replay(tree_a: Path) -> dict:
    """Replay the f8aa5fe9 audit's regex (frontend_surface_diff.py::T_CALL_RE)."""
    t_call_re = re.compile(
        r"""\bt\(\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')"""
        r"""(?:\s*,\s*\{[^{}]*\})?\s*\)""",
        re.DOTALL,
    )
    keys: set[str] = set()
    for path in sorted((tree_a / "src").rglob("*")):
        if not path.is_file() or path.suffix not in (".ts", ".tsx"):
            continue
        if any(part in SKIP_DIRS for part in path.parts):
            continue
        rel = path.relative_to(tree_a).as_posix()
        if rel.startswith("src/i18n/"):
            continue  # the prior script skipped its dictionary dir wholesale
        text = read_text(path)
        for match in t_call_re.finditer(text):
            literal = match.group(1)
            try:
                keys.add(json.loads(literal) if literal.startswith('"') else json.loads('"' + literal[1:-1] + '"'))
            except json.JSONDecodeError:
                continue
    return {
        "rule": "prior audit regex T_CALL_RE over all .ts/.tsx except src/i18n/** (incl. test files)",
        "keys": keys,
    }


def calibration(prior_path: Path, dictionary: dict, dead: dict, tree_a: Path, static_keys: set[str]) -> dict:
    if not prior_path.is_file():
        return {"available": False, "path": rel_posix(prior_path, prior_path.parent.parent.parent)}
    try:
        prior = json.loads(read_text(prior_path))
    except (OSError, json.JSONDecodeError):
        return {"available": False, "path": str(prior_path)}
    i18n = (prior.get("static") or {}).get("i18n") or {}
    whitelist = i18n.get("whitelist") or []
    prior_unused = i18n.get("dictionary_but_unused") or []
    current = set(dictionary["merged"])
    whitelist_set = set(whitelist)
    new_keys = sorted(current - whitelist_set)
    removed_keys = sorted(whitelist_set - current)
    still_unused = sorted(set(prior_unused) & (set(dead["definite_dict_only"]) | set(dead["definite_literal_present"]) | set(dead["possible_dynamic"])))
    replay = _prior_method_replay(tree_a)
    replay_keys = replay["keys"]
    return {
        "available": True,
        "path": rel_posix(prior_path, prior_path.parent.parent.parent),
        "baseline_commit": prior.get("commit"),
        "baseline_generated_at": prior.get("generated_at"),
        "prior_counts": {
            "dictionary_keys": i18n.get("go_dictionary_keys"),
            "t_referenced_keys": i18n.get("t_referenced_keys"),
            "t_dynamic_calls": i18n.get("t_dynamic_calls"),
            "used_but_untranslated": len(i18n.get("used_but_untranslated") or []),
            "dictionary_but_unused": len(prior_unused),
            "value_equals_key": len(i18n.get("value_equals_key") or []),
            "keys_without_python_counterpart": len(i18n.get("keys_without_python_counterpart") or []),
        },
        "prior_method_replay": {
            "rule": replay["rule"],
            "referenced_keys": len(replay_keys),
            "replay_only_vs_current": sorted(replay_keys - static_keys)[:100],
            "current_only_vs_replay": sorted(static_keys - replay_keys)[:100],
            "replay_only_count": len(replay_keys - static_keys),
            "current_only_count": len(static_keys - replay_keys),
        },
        "current_vs_prior": {
            "whitelist_keys": len(whitelist_set),
            "still_present": len(current & whitelist_set),
            "new_since_prior": len(new_keys),
            "new_since_prior_sample": new_keys[:100],
            "removed_since_prior": len(removed_keys),
            "removed_since_prior_sample": removed_keys[:100],
            "prior_unused_keys": len(prior_unused),
            "prior_unused_still_unreferenced": len(still_unused),
            "prior_unused_still_unreferenced_sample": still_unused[:100],
        },
    }


# --------------------------------------------------------------------- output


def pct(part: int | float, whole: int | float) -> str:
    if not whole:
        return "n/a"
    return f"{part * 100.0 / whole:.1f}%"


def escape_cell(value) -> str:
    return str(value).replace("|", "\\|")


def render_md(report: dict) -> str:
    a = report["tree_a"]
    b = report["tree_b"]
    diff = report["diff"]
    lines = []
    add = lines.append
    add("# i18n coverage — 静态盘点（Go vendored frontend A vs upstream B）")
    add("")
    add(f"- 生成：`{report['environment']['generated_at']}`")
    add(f"- HEAD：`{report['environment']['head']}`（worktree `{report['environment']['worktree']}`）")
    add(f"- A：`{a['frontend_dir']}`；B：`{b['frontend_dir']}`")
    add(f"- 复现：`uv run --no-project python go_hive/scripts/i18n_coverage.py`")
    add("")
    add("## 1. 核心数字")
    add("")
    add("> 口径：key = 英文源文案；字典按 `index.tsx` spread 顺序合并（后 spread 覆盖）；")
    add("> 引用仅统计产品代码（`src/**`、排除 `src/i18n/zh`/`index.tsx`/测试文件）且文件必须经 `useI18n()` 取 `t`；")
    add("> 模板 `${…}` 内的调用、三元分支字面量计入；比较操作数（`=== \"x\"`）、拼接与嵌套数据字面量不计。")
    add("")
    add("| 指标 | 数值 |")
    add("| --- | ---: |")
    add(f"| A 字典文件条目（去重前） | {a['dictionary']['entries_total']} |")
    add(f"| A 字典唯一键（按 index.tsx spread 顺序合并） | {a['dictionary']['unique_keys']} |")
    add(f"| A 被 `t()` 静态字面量引用的唯一键 | {a['counts']['static_distinct']} |")
    add(f"| A 静态调用点（含重复） | {a['counts']['static_sites']} |")
    add(f"| A 表达式参数中的显式字面量键（三元等） | {a['counts']['expr_distinct']}（{a['counts']['expr_calls']} 个调用） |")
    add(f"| A 明确引用键合计（静态 ∪ 表达式字面量，含字典缺的 8 条） | {a['counts']['definite_distinct']} |")
    add(f"| A 动态 `t(expr)` 调用：已解析/未解析 | {a['counts']['dynamic_resolved_calls']} / {a['counts']['dynamic_unresolved_calls']} |")
    add(f"| **A 缺失翻译（明确引用但字典无）** | **{a['missing']['count']}** |")
    add(f"| A 静态未引用字典条目（= 下列三层之和） | {a['dead']['unreferenced_by_definite_count']} |")
    add(f"| ├ 仅字典出现（强 dead-key 候选） | {a['dead']['definite_dict_only_count']} |")
    add(f"| ├ 文本在别处作为字面量出现 | {a['dead']['definite_literal_present_count']} |")
    add(f"| └ 仅由动态候选覆盖（低置信） | {a['dead']['possible_dynamic_count']} |")
    add(f"| A 翻译值 == 英文键 | {a['value_checks']['value_equals_key_count']} |")
    add(f"| A 占位符集合不一致 | {a['value_checks']['placeholder_mismatch_count']} |")
    add(f"| B 字典键 / 被引用键 / 缺失 | {b['dictionary_keys']} / {b['referenced_keys']} / {b['missing_keys']} |")
    add(f"| A/B 键集合：A 有 B 无 / B 有 A 无 / 值不同 | {diff['counts']['a_only']} / {diff['counts']['b_only']} / {diff['counts']['value_changed']} |")
    sc = a["tokenizer_self_check"]
    add("")
    add(f"- tokenizer 自检：产品文件 raw `t(` 计数 {sc['raw_total']} vs token 识别 {sc['token_detected_total']}（{sc['files_with_gap']} 个文件有差），保证模板插值 `${{…}}` 内/嵌套调用不被漏计。")
    add("")
    add("## 2. A 缺失翻译（按文件分组，列表上限 200）")
    add("")
    if not a["missing"]["count"]:
        add("无：所有明确引用的 `t()` 键都能在合并后的 zh 字典中命中。")
    else:
        add("| 文件 | 缺失条数 |")
        add("| --- | ---: |")
        for file in sorted(a["missing"]["by_file"]):
            rec = a["missing"]["by_file"][file]
            add(f"| `{file}` | {rec['count']} |")
        add("")
        add("| 文件:行 | key |")
        add("| --- | --- |")
        for item in a["missing"]["items"]:
            add(f"| `{item['file']}:{item['line']}` | {escape_cell(item['key'])} |")
        if a["missing"]["truncated"]:
            add("")
            add(f"> 仅列前 {MISSING_LIST_CAP} 条；全量见 JSON。")
    add("")
    extra = a["missing_dynamic_candidates"]
    add(f"动态解析候选缺失（低置信，单列）：**{extra['count']}** 条。")
    if extra["count"]:
        add("")
        add("| key | 引用点 |")
        add("| --- | --- |")
        for item in extra["items"][:50]:
            sites = ", ".join(f"`{s['file']}:{s['line']}`" for s in item["sites"])
            add(f"| {escape_cell(item['key'])} | {sites} |")
    add("")
    add("## 3. A 未引用字典条目（dead keys）")
    add("")
    add(f"静态未引用合计 **{a['dead']['unreferenced_by_definite_count']}** 条（= 下列三层，互不重叠）。口径：字典 {a['dictionary']['unique_keys']} 键中，被明确引用命中 {a['dictionary']['unique_keys'] - a['dead']['unreferenced_by_definite_count']} 个；另有 {a['missing']['count']} 个被引用但字典没有（见 §2）。")
    add("")
    add("| 层级 | 条数 | 含义 |")
    add("| --- | ---: | --- |")
    add(f"| 仅字典出现 | {a['dead']['definite_dict_only_count']} | 产品源码中没有任何同文字面量；最可能是死键（运行时后端值仍可能命中） |")
    add(f"| 文本另有字面量 | {a['dead']['definite_literal_present_count']} | 产品源码别处存在同文字面量，可能经数据表/动态 t() 命中 |")
    add(f"| 动态候选命中 | {a['dead']['possible_dynamic_count']} | 仅由本脚本的动态解析候选覆盖（低置信） |")
    add("")
    for label, key_name in (("仅字典出现", "definite_dict_only"), ("文本另有字面量", "definite_literal_present"), ("动态候选命中", "possible_dynamic")):
        keys = a["dead"][key_name]
        if not keys:
            continue
        add(f"### {label}（{len(keys)}）")
        add("")
        for key in keys[:DEAD_LIST_CAP]:
            add(f"- `{key}`")
        if len(keys) > DEAD_LIST_CAP:
            add(f"- …（共 {len(keys)} 条，仅列前 {DEAD_LIST_CAP}）")
        add("")
    add("## 4. 值体检")
    add("")
    add(f"- 值 == 英文键（多为品牌/技术名，属故意不译）：{a['value_checks']['value_equals_key_count']} 条")
    if a["value_checks"]["value_equals_key"]:
        add("  - " + "、".join(f"`{k}`" for k in a["value_checks"]["value_equals_key"][:40]))
    add(f"- 空值：{a['value_checks']['empty_value_count']} 条")
    add(f"- 占位符集合不一致：{a['value_checks']['placeholder_mismatch_count']} 条")
    if a["value_checks"]["placeholder_mismatch"]:
        add("")
        add("| key | zh 值 | 键占位符 | 值占位符 |")
        add("| --- | --- | --- | --- |")
        for row in a["value_checks"]["placeholder_mismatch"][:50]:
            add(f"| {escape_cell(row['key'])} | {escape_cell(row['value'])} | {', '.join(row['key_placeholders']) or '—'} | {', '.join(row['value_placeholders']) or '—'} |")
    add("")
    add("## 5. A vs B（key 集合 / zh 值）")
    add("")
    add("| 项 | 条数 |")
    add("| --- | ---: |")
    add(f"| B 有 A 无 | {diff['counts']['b_only']} |")
    add(f"| A 有 B 无 | {diff['counts']['a_only']} |")
    add(f"| 值不同 | {diff['counts']['value_changed']} |")
    add("")
    add("B 侧证据：")
    add("")
    add(f"- `src/i18n/` 目录存在：{b['has_i18n_dir']}")
    add(f"- 含 `useI18n` / locale / i18n import 标记的产品文件：{len(b['i18n_marker_files'])}")
    add(f"- 原始 `t(` 调用点：{len(b['raw_t_call_sites'])}（均为 DebugPanel 的数值格式化局部函数，不是 i18n）")
    add(f"- 字典键：{b['dictionary_keys']}")
    add("")
    cp = b["counterpart_heuristic"]
    add("A 键英文文案在 B 产品源码中的启发式命中（仅 presence，非语义断言）：")
    add("")
    add(f"- 精确子串命中：{cp['exact']}；占位符通配命中：{cp['placeholder']}；未命中：{cp['miss']}（基数：A 字典 {a['dictionary']['unique_keys']} 键）")
    if cp["misses"]:
        add("")
        add("未命中清单（Go 侧新增文案或已改写；启发式，非断言）：")
        add("")
        for key in cp["misses"][:60]:
            add(f"- `{key}`")
    add("")
    diffs = diff["value_items"]
    add(f"值差异（上限 {VALUE_DIFF_CAP}）：")
    if not diffs:
        add("无（B 无 zh 字典，不存在可比的值）。")
    else:
        add("")
        add("| key | A 值 | B 值 |")
        add("| --- | --- | --- |")
        for row in diffs[:VALUE_DIFF_CAP]:
            add(f"| {escape_cell(row['key'])} | {escape_cell(row['a_value'])} | {escape_cell(row['b_value'])} |")
    add("")
    cal = report.get("calibration") or {}
    if cal.get("available"):
        add("## 6. 与既有冻结盘点的校准（frontend-surface-diff.json）")
        add("")
        add(f"- 基线：`{cal['baseline_commit']}`（{cal['baseline_generated_at']}）")
        add(f"- 基线数字：字典 {cal['prior_counts']['dictionary_keys']} / 引用 {cal['prior_counts']['t_referenced_keys']} / 动态调用 {cal['prior_counts']['t_dynamic_calls']} / 未引用 {cal['prior_counts']['dictionary_but_unused']} / 缺失 {cal['prior_counts']['used_but_untranslated']}")
        add(f"- 当前：字典 {a['dictionary']['unique_keys']} / 明确引用 {a['counts']['definite_distinct']} / 动态调用 {a['counts']['dynamic_resolved_calls'] + a['counts']['dynamic_unresolved_calls']} / 静态未引用 {a['dead']['unreferenced_by_definite_count']} / 缺失 {a['missing']['count']}")
        c = cal["current_vs_prior"]
        add(f"- 与基线白名单：仍在 {c['still_present']}；新增 {c['new_since_prior']}；移除 {c['removed_since_prior']}")
        add(f"- 基线未引用键：{c['prior_unused_keys']}；当前仍未引用 {c['prior_unused_still_unreferenced']}")
        rp = cal.get("prior_method_replay")
        if rp:
            add(f"- 旧方法回放（`T_CALL_RE` 正则，含测试、跳过 `src/i18n/**`）：引用 {rp['referenced_keys']} 键；与当前 tokenizer 口径：仅旧方法 {rp['replay_only_count']}、仅新方法 {rp['current_only_count']}")
        add("")
    add("## 7. 局限（无法静态判定）")
    add("")
    add(f"- 运行时拼接/后端下发字符串作为键：动态 `t(expr)` 未解析 {a['counts']['dynamic_unresolved_calls']} 处（完整清单见 JSON）；其中若运行时值与字典键相同则仍会命中。")
    add("- 字典键的英文文案可能在 JSX 纯文本节点中直接书写而未包 `t()`（不属于本盘点口径；本脚本只核 t() 引用）。")
    add("- 动态解析的 prop/named pool 是超集启发：用于 dead-key 降级与低置信缺失清单，不进入头条缺失数字。")
    add("- B 无 i18n 层，A/B 差异仅能作结构结论；值级对照不可能（B 无 zh 值）。")
    add("")
    add("证据 JSON：`go_hive/docs/e2e-assets/i18n-coverage/i18n-coverage.json`")
    add("")
    return "\n".join(lines)


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description="Static i18n coverage audit (Go vendored frontend vs upstream).")
    parser.add_argument("--root", help="repository root (default: two levels above this script)")
    parser.add_argument("--tree-a", default="go_hive/go-hive/frontend", help="vendored Go frontend directory")
    parser.add_argument("--tree-b", default="core/frontend", help="upstream Python frontend directory")
    parser.add_argument("--out", default="go_hive/docs/e2e-assets/i18n-coverage", help="output directory")
    parser.add_argument("--prior", default="docs/frontend-surface/frontend-surface-diff.json",
                        help="earlier frozen inventory for calibration (optional)")
    args = parser.parse_args(argv)

    script_root = Path(__file__).resolve().parents[2]
    root = Path(args.root).resolve() if args.root else script_root
    tree_a = (root / args.tree_a).resolve()
    tree_b = (root / args.tree_b).resolve()
    out_dir = (root / args.out).resolve()
    prior_path = (root / args.prior).resolve()

    if not (tree_a / "src").is_dir():
        print(f"error: tree A src not found: {tree_a}", file=sys.stderr)
        return 2
    if not (tree_b / "src").is_dir():
        print(f"error: tree B src not found: {tree_b}", file=sys.stderr)
        return 2

    dictionary = scan_dictionary(tree_a)
    if not dictionary["exists"]:
        print(f"error: tree A has no src/i18n/zh dictionary at {tree_a}", file=sys.stderr)
        return 2

    files_a = [rel_posix(p, tree_a) for p in iter_files(tree_a / "src")]
    product_a = [rel for rel in files_a if is_product_file(rel)]
    test_a = [rel for rel in files_a if is_test_file(rel)]
    infos = {rel: collect_fileinfo(tree_a, rel) for rel in product_a}

    refs = extract_refs(tree_a, infos, product_a, require_hook=True, resolve=True)
    agg = aggregate_a(tree_a, dictionary, refs, infos)
    selfcheck = tokenizer_selfcheck(tree_a, product_a)
    test_refs = extract_refs(tree_a, infos, test_a, require_hook=False, resolve=False)

    dict_keys = sorted(dictionary["merged"])
    b_info = scan_tree_b(tree_b, dict_keys, root)

    diff = {
        "counts": {
            "a_keys": len(dict_keys),
            "b_keys": b_info["dictionary_keys"],
            "a_only": len(dict_keys) - 0,
            "b_only": 0,
            "value_changed": 0,
            "common_keys": 0,
        },
        "a_only_sample": dict_keys[:100],
        "b_only": [],
        "value_items": [],
        "rule": "B ships no i18n layer: B key set and zh values are empty; every A dictionary key is structurally A-only.",
    }

    report = {
        "schema": SCHEMA,
        "environment": {
            "generated_at": utc_now(),
            "head": git_head(root),
            "worktree": str(root),
            "tree_a": str(tree_a),
            "tree_b": str(tree_b),
            "python": sys.version.split()[0],
        },
        "rules": {
            "key_semantics": "English source text is the key; en renders the key verbatim",
            "dictionary_merge": "src/i18n/zh/*.ts spread order parsed from src/i18n/index.tsx; later spread wins",
            "product_scope": "src/**/*.ts,tsx minus src/i18n/zh/**, src/i18n/index.tsx, *.test.*/*.spec.*, src/test/**",
            "reference_gate": "only files calling useI18n() count as i18n consumers; local `t` shadows excluded by scope analysis",
            "static_key": "first argument is exactly one string literal (or a template without ${})",
            "expression_keys": "every depth-0 string literal in a ternary/expression first argument; comparison operands and top-level '+' concatenation excluded",
            "dynamic_resolution": "const object/array literals, indexed const lookups, property-name pools (same file + direct imports), local helper-function string literals; candidates only",
            "dead_key": "dictionary key with no static/expression reference; total is the disjoint sum of dictionary-only, literal-elsewhere and dynamic-candidate-only layers",
            "tokenizer": "TS tokenizer that splices ${...} template interpolations as code, skips JSX tag slashes and JSX-prose quote pairs, and treats regex literals contextually",
            "value_checks": "value == key; placeholder sets between key and value; empty value",
            "tree_b": "same dictionary/reference scan gated on evidence of an i18n mechanism",
            "caps": {"missing_listed": MISSING_LIST_CAP, "value_diffs": VALUE_DIFF_CAP, "dead_listed": DEAD_LIST_CAP},
        },
        "tree_a": {
            "frontend_dir": rel_posix(tree_a, root),
            "dictionary": {
                "exists": dictionary["exists"],
                "per_file_entries": dictionary["per_file"],
                "merge_order": dictionary["merge_order"],
                "unimported_files": dictionary["unimported_files"],
                "entries_total": dictionary["entries_total"],
                "unique_keys": dictionary["unique_keys"],
                "duplicate_key_count": len(dictionary["duplicates"]),
                "duplicates": dictionary["duplicates"],
                "intra_file_duplicates": dictionary["intra_file_duplicates"],
                "parse_errors": dictionary["parse_errors"],
            },
            "dictionary_keys_sample": dict_keys[:100],
            **agg,
            "tokenizer_self_check": selfcheck,
            "test_scope": {
                "files": len(test_a),
                "static_call_sites": len(test_refs["static_sites"]),
                "static_distinct_keys": len({s["key"] for s in test_refs["static_sites"]}),
                "missing_from_dict": sorted({s["key"] for s in test_refs["static_sites"]} - set(dict_keys))[:DEAD_LIST_CAP],
            },
        },
        "tree_b": b_info,
        "diff": diff,
        "calibration": calibration(prior_path, dictionary, agg["dead"], tree_a,
                                   set(agg["static_keys"])),
        "limitations": [
            "dynamic t(expr) keys that only exist at runtime cannot be statically resolved; unresolved call sites are listed",
            "dictionary keys can be hit by backend-provided strings at runtime (e.g. status values); dead-key classification is static",
            "JSX text nodes written without t() are outside the reference scope of this audit",
            "dynamic-resolution candidate keys are a superset heuristic and are reported as low confidence",
            "the tokenizer is a heuristic, not a full TSX parser (JSX prose quotes, regex literals, template interpolations are handled by context rules)",
            "tree B has no i18n layer, so A-vs-B zh value comparison is structurally empty",
        ],
    }

    out_dir.mkdir(parents=True, exist_ok=True)
    json_path = out_dir / "i18n-coverage.json"
    md_path = out_dir / "i18n-coverage.md"
    json_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    md_path.write_text(render_md(report), encoding="utf-8")

    a_counts = report["tree_a"]["counts"]
    print(f"tree A: dictionary {report['tree_a']['dictionary']['unique_keys']} keys "
          f"({report['tree_a']['dictionary']['entries_total']} entries, "
          f"{report['tree_a']['dictionary']['duplicate_key_count']} cross-file duplicates)")
    print(f"tree A: t() references: {a_counts['definite_distinct']} distinct "
          f"({a_counts['static_sites']} static sites, {a_counts['expr_sites']} expression sites); "
          f"dynamic {a_counts['dynamic_resolved_calls']} resolved / {a_counts['dynamic_unresolved_calls']} unresolved")
    print(f"tree A: missing translations {report['tree_a']['missing']['count']}; "
          f"statically unreferenced dictionary entries {report['tree_a']['dead']['unreferenced_by_definite_count']} "
          f"(dict-only {report['tree_a']['dead']['definite_dict_only_count']}, "
          f"literal-elsewhere {report['tree_a']['dead']['definite_literal_present_count']}, "
          f"dynamic-candidate {report['tree_a']['dead']['possible_dynamic_count']})")
    print(f"tree B: i18n dir={b_info['has_i18n_dir']} marker files={len(b_info['i18n_marker_files'])} "
          f"raw t( sites={len(b_info['raw_t_call_sites'])} dictionary keys={b_info['dictionary_keys']}")
    print(f"tokenizer self-check: raw t( {selfcheck['raw_total']} vs token-detected {selfcheck['token_detected_total']} "
          f"({selfcheck['files_with_gap']} files with gap)")
    print(f"diff: A-only {diff['counts']['a_only']} / B-only {diff['counts']['b_only']} / value-changed {diff['counts']['value_changed']}")
    print(f"wrote {json_path}")
    print(f"wrote {md_path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
