"""Field-level probe: Python `framework.server.queen_mail_mcp` vs Go `hive queen-mail-mcp`.

Spawns each server over stdio, performs the MCP handshake, and dumps
initialize + tools/list results to JSON for a verbatim diff.
"""
import json
import os
import select
import subprocess
import sys
import time

HIVE = "/Users/mac/.hermes/profiles/omh-tui/cache/scratch/qmail-mcp/hive"
PY = "/Users/mac/hive/.venv/bin/python"
CORE = "/Users/mac/hive/core"
OUT = "/Users/mac/.hermes/profiles/omh-tui/cache/scratch/qmail-mcp"


def run(cmd, cwd, timeout=20):
    env = dict(os.environ)
    env.update({
        "HIVE_QUEEN_MAIL_URL": "http://127.0.0.1:59999",
        "HIVE_QUEEN_MAIL_TOKEN": "probe-token",
        "HIVE_QUEEN_MAIL_FROM": "queen_probe",
        "PYTHONPATH": CORE,
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
        send({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "probe", "version": "1"}}})
        out["initialize"] = read_id(1, time.time() + timeout)
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        send({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
        out["tools_list"] = read_id(2, time.time() + timeout)
        # unknown method error shape
        send({"jsonrpc": "2.0", "id": 3, "method": "no/such", "params": {}})
        out["unknown"] = read_id(3, time.time() + timeout)
        # call list_queens against an unreachable URL: compare transport-error text
        send({"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": {"name": "list_queens", "arguments": {}}})
        out["call_list_queens"] = read_id(4, time.time() + timeout)
        send({"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": {"name": "nope", "arguments": {}}})
        out["call_unknown_tool"] = read_id(5, time.time() + timeout)
    finally:
        proc.kill()
        try:
            err = proc.stderr.read()
        except Exception:
            err = ""
    return out, err


py, pyerr = run([PY, "-m", "framework.server.queen_mail_mcp"], CORE)
go, goerr = run([HIVE, "queen-mail-mcp"], "/tmp")

json.dump({"python": py, "go": go}, open(f"{OUT}/handshake.json", "w"), ensure_ascii=False, indent=2)
print("python stderr head:", pyerr[:300].replace("\n", " | "))
print("go stderr head:", goerr[:300].replace("\n", " | "))
print("keys:", {k: bool(v) for k, v in py.items()}, {k: bool(v) for k, v in go.items()})

def norm_tools(res):
    return {t["name"]: t for t in (res or {}).get("result", {}).get("tools", [])}

pt, gt = norm_tools(py.get("tools_list")), norm_tools(go.get("tools_list"))
print("tool names py/go:", sorted(pt), sorted(gt))
for name in sorted(set(pt) | set(gt)):
    a, b = pt.get(name), gt.get(name)
    if not a or not b:
        print(name, "MISSING", bool(a), bool(b)); continue
    print(f"[{name}] desc equal:", a.get("description") == b.get("description"),
          "| schema equal:", a.get("inputSchema") == b.get("inputSchema"),
          "| extra keys py:", sorted(set(a) - {"name", "description", "inputSchema"}),
          "| go:", sorted(set(b) - {"name", "description", "inputSchema"}))
    if a.get("inputSchema") != b.get("inputSchema"):
        print("  py schema:", json.dumps(a.get("inputSchema"), ensure_ascii=False))
        print("  go schema:", json.dumps(b.get("inputSchema"), ensure_ascii=False))
print("initialize py:", json.dumps((py.get("initialize") or {}).get("result", {}), ensure_ascii=False))
print("initialize go:", json.dumps((go.get("initialize") or {}).get("result", {}), ensure_ascii=False))
print("unknown py:", json.dumps(py.get("unknown"), ensure_ascii=False))
print("unknown go:", json.dumps(go.get("unknown"), ensure_ascii=False))
print("call list_queens py:", json.dumps(py.get("call_list_queens"), ensure_ascii=False)[:400])
print("call list_queens go:", json.dumps(go.get("call_list_queens"), ensure_ascii=False)[:400])
print("call unknown py:", json.dumps(py.get("call_unknown_tool"), ensure_ascii=False)[:300])
print("call unknown go:", json.dumps(go.get("call_unknown_tool"), ensure_ascii=False)[:300])
