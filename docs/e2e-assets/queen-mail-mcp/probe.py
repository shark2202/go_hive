"""Field-level probe: Python `framework.server.queen_mail_mcp` vs Go `hive queen-mail-mcp`.

Spawns each server over stdio with the same environment, performs the MCP
handshake against a local fixture of `hive serve`'s queen-mail routes, and
dumps every answer to handshake.json for a field-level diff (also printed).

Re-run after changing either side:

    python3 probe.py                       # builds the Go binary if needed
    HIVE_GO_BIN=/path/to/hive python3 probe.py

Overridable: HIVE_PYTHON (default <repo>/.venv/bin/python), HIVE_CORE
(default <repo>/core), HIVE_GO_BIN (default: build <repo>/go_hive/go-hive),
HIVE_MCP_PROBE_OUT (default: handshake.json next to this file).

Two registered deviations are accounted for, not hidden: serverInfo.version
(Go reports its binary version; FastMCP reports its library version) and the
tool-answer text, which each side re-serializes its own way (json.dumps vs
encoding/json) and which this probe therefore compares as JSON, not bytes.
"""

import json
import os
import select
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.normpath(os.path.join(HERE, "..", "..", "..", ".."))
MODULE = os.path.join(REPO, "go_hive", "go-hive")
PY = os.environ.get("HIVE_PYTHON", os.path.join(REPO, ".venv", "bin", "python"))
if not os.path.exists(PY):
    PY = sys.executable  # worktrees have no .venv of their own
CORE = os.environ.get("HIVE_CORE", os.path.join(REPO, "core"))
OUT = os.environ.get("HIVE_MCP_PROBE_OUT", os.path.join(HERE, "handshake.json"))


class Fixture(BaseHTTPRequestHandler):
    """The two queen-mail routes the MCP server calls, with fixed answers."""

    def _send(self, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self._send({"queens": [{"id": "queen_ivy", "name": "Ivy", "title": "Gardener"}]})

    def do_POST(self):
        self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self._send({"sent": "hop_1", "to": "queen_ivy", "depth": 1})

    def log_message(self, format, *args):  # noqa: A002 - matches BaseHTTPRequestHandler
        pass


def build_go_binary():
    target = os.path.join(tempfile.mkdtemp(prefix="queen-mail-mcp-probe-"), "hive")
    subprocess.run(["go", "build", "-o", target, "./cmd/hive"], cwd=MODULE, check=True)
    return target


def run(cmd, cwd, url, timeout=20):
    env = dict(os.environ)
    env.update({
        "HIVE_QUEEN_MAIL_URL": url,
        "HIVE_QUEEN_MAIL_TOKEN": "probe-token",
        "HIVE_QUEEN_MAIL_FROM": "queen_probe",
        "PYTHONPATH": CORE,
        "PYTHONUNBUFFERED": "1",
    })
    proc = subprocess.Popen(cmd, cwd=cwd, env=env, stdin=subprocess.PIPE,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1)
    out = {}

    def send(obj):
        proc.stdin.write(json.dumps(obj) + "\n")
        proc.stdin.flush()

    def read_id(want, deadline):
        while time.time() < deadline:
            r, _, _ = select.select([proc.stdout], [], [], 0.2)
            if not r:
                continue
            line = proc.stdout.readline()
            if not line:
                break
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if msg.get("id") == want:
                return msg
        return None

    try:
        requests = [
            (1, {"method": "initialize", "params": {
                "protocolVersion": "2024-11-05", "capabilities": {},
                "clientInfo": {"name": "probe", "version": "1"}}}),
            (2, {"method": "tools/list", "params": {}}),
            (3, {"method": "tools/call", "params": {"name": "list_queens", "arguments": {}}}),
            (4, {"method": "tools/call", "params": {
                "name": "message_queen", "arguments": {"queen_id": "queen_ivy", "message": "Hi"}}}),
            (5, {"method": "tools/call", "params": {"name": "nope", "arguments": {}}}),
            (6, {"method": "resources/list", "params": {}}),
            (7, {"method": "prompts/list", "params": {}}),
            (8, {"method": "no/such", "params": {}}),
            (9, {"method": "logging/setLevel", "params": {"level": "info"}}),
            (10, {"method": "ping", "params": {}}),
        ]
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        for request_id, request in requests:
            send({"jsonrpc": "2.0", "id": request_id, **request})
            out[str(request_id)] = read_id(request_id, time.time() + timeout)
    finally:
        proc.kill()
        try:
            err = proc.stderr.read()
        except Exception:
            err = ""
    return out, err


def normalize(message):
    """Tool-answer text is re-serialized by each side: Python json.dumps (", "/
    ": " separators, insertion order) vs Go encoding/json (compact, sorted
    keys). JSON object order and spacing carry no meaning, so compare the text
    as the JSON it is — a registered difference kept out of the field diff."""
    if not isinstance(message, dict):
        return message
    result = message.get("result")
    if not isinstance(result, dict):
        return message
    structured = result.get("structuredContent")
    if not isinstance(structured, dict) or not isinstance(structured.get("result"), str):
        return message
    try:
        parsed = json.loads(structured["result"])
    except json.JSONDecodeError:
        return message
    content = result.get("content")
    if isinstance(content, list):
        content = [({**part, "text": parsed}
                    if isinstance(part, dict) and part.get("type") == "text" else part)
                   for part in content]
    return {**message, "result": {**result, "content": content,
                                  "structuredContent": {**structured, "result": parsed}}}


def diff_paths(a, b, path="", found=None):
    """Every JSON path where a and b differ (key order doesn't matter)."""
    if found is None:
        found = []
    if type(a) is not type(b):
        found.append((path, a, b))
    elif isinstance(a, dict):
        for key in sorted(set(a) | set(b)):
            if key not in a or key not in b:
                found.append((f"{path}.{key}", a.get(key), b.get(key)))
            else:
                diff_paths(a[key], b[key], f"{path}.{key}", found)
    elif isinstance(a, list):
        if len(a) != len(b):
            found.append((path, a, b))
        else:
            for i, (x, y) in enumerate(zip(a, b)):
                diff_paths(x, y, f"{path}[{i}]", found)
    elif a != b:
        found.append((path, a, b))
    return found


def main():
    fixture = HTTPServer(("127.0.0.1", 0), Fixture)
    threading.Thread(target=fixture.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{fixture.server_address[1]}"

    go_bin = os.environ.get("HIVE_GO_BIN") or build_go_binary()
    mcp_version = subprocess.run(
        [PY, "-c", "import importlib.metadata as m; print(m.version('mcp'))"],
        capture_output=True, text=True).stdout.strip()

    py, pyerr = run([PY, "-m", "framework.server.queen_mail_mcp"], CORE, url)
    go, goerr = run([go_bin, "queen-mail-mcp"], os.path.dirname(go_bin), url)

    document = {
        "meta": {"mcp": mcp_version},
        "python": py,
        "go": go,
    }
    with open(OUT, "w", encoding="utf-8") as fh:
        json.dump(document, fh, ensure_ascii=False, indent=2)

    print(f"mcp {mcp_version}; python stderr head: {pyerr[:200].strip()!r}")
    print(f"go stderr head: {goerr[:200].strip()!r}")
    print(f"written: {OUT}")

    # Known, registered exception: serverInfo.version is the FastMCP library
    # version on the Python side and the binary's own version on the Go side
    # (docs/queen-mail.md §6). Everything else must compare equal.
    known = {"1.result.serverInfo.version"}
    unexpected = 0
    for key in sorted(py, key=int):
        paths = diff_paths(normalize(py[key]), normalize(go[key]), f"{key}")
        if not paths:
            print(f"[{key}] SAME")
            continue
        for path, a, b in paths:
            tag = "KNOWN-EXCEPTION" if path in known else "UNEXPECTED"
            if tag == "UNEXPECTED":
                unexpected += 1
            print(f"[{key}] {tag} {path}:")
            print(f"    python: {json.dumps(a, ensure_ascii=False)}")
            print(f"    go:     {json.dumps(b, ensure_ascii=False)}")
    print("unexpected diffs:", unexpected)
    fixture.shutdown()
    return 1 if unexpected else 0


if __name__ == "__main__":
    sys.exit(main())
