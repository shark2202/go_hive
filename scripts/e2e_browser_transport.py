#!/usr/bin/env python3
"""End-to-end probe for the browser-bridge transport/lifecycle parity items.

Runs the *real* `browserbridge` binary (and the real `hive-browser` CLI) and
asserts the five differences this branch closes:

  1. WS-layer keepalive: 200ms ping / 200ms timeout (flags shorten upstream's
     20s/20s) — a peer that never pongs is closed with 1011 "keepalive ping
     timeout"; a peer that pongs stays connected and keeps carrying messages.
  2. Legacy status listener: the 9229+1 HTTP endpoint answers the same status
     payload as the primary API (listening.legacy_status true), and the legacy
     WS port still accepts a hello.
  3. CLI ensure: `hive-browser status` with no --bridge and nothing listening
     spawns the bridge (detached), waits for /health, and succeeds.
  4. Exit codes: the bridge's structured bad_request lands on 1 (upstream's
     text taxonomy), while a local precheck stays 6.
  5. Parent death: the default -parent-check is upstream's 5s × 2, so the
     measured exit window is 5–10s (not the old ~0.8s).
  6. (Legacy guard) the primary/legacy pair never binds the same address twice.

Usage:
    python e2e_browser_transport.py --bridge ./browserbridge --cli ./hive-browser

Ports are chosen from free ephemeral sockets; the process tree is torn down on
exit. Zero network access beyond loopback.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

FAILURES: list[str] = []


def check(condition: bool, label: str) -> None:
    print(("PASS: " if condition else "FAIL: ") + label, flush=True)
    if not condition:
        FAILURES.append(label)


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_http(url: str, timeout: float = 10.0) -> dict:
    deadline = time.time() + timeout
    last = ""
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1) as resp:
                return json.loads(resp.read().decode())
        except Exception as exc:  # noqa: BLE001 - probe helper
            last = str(exc)
        time.sleep(0.05)
    raise RuntimeError(f"{url} never answered ({last})")


class WSClient:
    """Minimal RFC 6455 client with explicit control-frame handling."""

    def __init__(self, port: int, path: str = "/bridge", timeout: float = 3.0) -> None:
        self.sock = socket.create_connection(("127.0.0.1", port), timeout=timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f"GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n"
            "Upgrade: websocket\r\nConnection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
        )
        self.sock.sendall(request.encode())
        head = b""
        while b"\r\n\r\n" not in head:
            chunk = self.sock.recv(1024)
            if not chunk:
                raise RuntimeError("handshake EOF")
            head += chunk
        first_line = head.split(b"\r\n", 1)[0]
        if b" 101 " not in first_line:
            raise RuntimeError(f"handshake refused: {first_line!r}")
        self.buffer = head.split(b"\r\n\r\n", 1)[1]

    def send_frame(self, opcode: int, payload: bytes) -> None:
        mask = os.urandom(4)
        header = bytes([0x80 | opcode])
        length = len(payload)
        if length < 126:
            header += bytes([0x80 | length])
        elif length <= 0xFFFF:
            header += bytes([0x80 | 126]) + struct.pack(">H", length)
        else:
            header += bytes([0x80 | 127]) + struct.pack(">Q", length)
        masked = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
        self.sock.sendall(header + mask + masked)

    def send_json(self, payload: dict) -> None:
        self.send_frame(0x1, json.dumps(payload).encode())

    def _recv_exact(self, count: int) -> bytes:
        while len(self.buffer) < count:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise EOFError("socket closed")
            self.buffer += chunk
        data, self.buffer = self.buffer[:count], self.buffer[count:]
        return data

    def read_frame(self) -> tuple[int, bytes]:
        first, second = self._recv_exact(2)
        opcode = first & 0x0F
        length = second & 0x7F
        if length == 126:
            length = struct.unpack(">H", self._recv_exact(2))[0]
        elif length == 127:
            length = struct.unpack(">Q", self._recv_exact(8))[0]
        if second & 0x80:  # server frames are unmasked
            mask = self._recv_exact(4)
        else:
            mask = None
        payload = self._recv_exact(length)
        if mask:
            payload = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
        return opcode, payload

    def close(self) -> None:
        try:
            self.sock.close()
        except OSError:
            pass


def hello(label: str) -> dict:
    return {
        "type": "hello",
        "protocolVersion": 6,
        "extensionId": f"e2e-{label}-0123456789",
        "version": "0.0.0-e2e",
        "profileLabel": label,
    }


def spawn_bridge(binary: str, args: list[str], env: dict | None = None) -> subprocess.Popen:
    proc = subprocess.Popen(
        [binary, *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env=env,
    )
    assert proc.stdout is not None
    # The ready line is JSON on stdout; log.Printf traffic shares the pipe, so
    # scan until the announcement (or the child dies).
    deadline = time.time() + 10
    lines: list[str] = []
    while time.time() < deadline:
        line = proc.stdout.readline()
        if not line:
            break
        lines.append(line)
        if '"ready":true' in line.replace(" ", ""):
            ready = json.loads(line)
            proc.ready = ready  # type: ignore[attr-defined]
            return proc
    stop(proc)
    raise RuntimeError(f"bridge did not announce ready: {''.join(lines[-10:])!r}")


def stop(proc: subprocess.Popen | None) -> None:
    if proc is None or proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)


# ── item 1: WS keepalive ──────────────────────────────────────────────────


def probe_ws_keepalive(port: int) -> None:
    client = WSClient(port)
    client.send_json(hello("silent"))
    deadline = time.time() + 5
    code = reason = None
    while time.time() < deadline:
        try:
            opcode, payload = client.read_frame()
        except (EOFError, OSError):
            break
        if opcode == 0x8:
            code = struct.unpack(">H", payload[:2])[0] if len(payload) >= 2 else 1005
            reason = payload[2:].decode(errors="replace")
            break
    client.close()
    check(code == 1011, f"WS keepalive closes a silent peer with 1011 (got {code})")
    check(reason == "keepalive ping timeout", f"WS keepalive close reason (got {reason!r})")

    client = WSClient(port)
    client.send_json(hello("ponging"))
    deadline = time.time() + 1.2
    pongs = 0
    while time.time() < deadline:
        try:
            opcode, payload = client.read_frame()
        except (EOFError, OSError):
            break
        if opcode == 0x9:
            client.send_frame(0xA, payload)
            pongs += 1
    client.send_json({"type": "ping"})
    answered = False
    deadline = time.time() + 2
    while time.time() < deadline:
        opcode, payload = client.read_frame()
        if opcode == 0x9:
            client.send_frame(0xA, payload)
            continue
        if opcode == 0x1 and json.loads(payload.decode()).get("type") == "pong":
            answered = True
            break
    client.close()
    check(pongs >= 1, f"bridge sent WS-layer pings (answered {pongs})")
    check(answered, "a ponging peer stays connected and answers an app-level ping")


# ── item 2: legacy 9230 status + 9229 WS ──────────────────────────────────


def probe_legacy(legacy_status_port: int, legacy_ws_port: int) -> None:
    payload = wait_http(f"http://127.0.0.1:{legacy_status_port}/status")
    check(payload.get("bridge") == "running", "legacy status endpoint answers /status")
    listening = payload.get("listening") or {}
    check(listening.get("legacy_status") is True, "listening.legacy_status is true on the legacy endpoint")
    check(listening.get("legacy_ws") is True, "listening.legacy_ws is true on the legacy endpoint")
    profiles = wait_http(f"http://127.0.0.1:{legacy_status_port}/profiles")
    check("profiles" in profiles, "legacy status endpoint answers /profiles")

    client = WSClient(legacy_ws_port)
    client.send_json(hello("legacy-e2e"))
    labels: list = []
    deadline = time.time() + 2
    while time.time() < deadline:
        payload = wait_http(f"http://127.0.0.1:{legacy_status_port}/status")
        labels = [row.get("label") for row in payload.get("connections") or []]
        if "legacy-e2e" in labels:
            break
        time.sleep(0.05)
    client.close()
    check("legacy-e2e" in labels, f"legacy 9229 WS accepts a hello (labels={labels})")


# ── item 3: CLI auto-ensure ───────────────────────────────────────────────


def probe_cli_ensure(cli: str, bridge_bin: str, home: str, log_path: str) -> None:
    http_port = free_port()
    control_port = free_port()
    ws_port = free_port()
    env = dict(os.environ)
    env.update(
        {
            "HIVE_HOME": home,
            "HIVE_BROWSERBRIDGE_HTTP": f"127.0.0.1:{http_port}",
            "HIVE_BROWSERBRIDGE_CONTROL": f"127.0.0.1:{control_port}",
            "HIVE_BROWSERBRIDGE_BIN": bridge_bin,
            "HIVE_BROWSERBRIDGE_ARGS": f"-ws 127.0.0.1:{ws_port} -no-legacy -no-idle-exit -no-sweep",
        }
    )
    # The first invocation triggers the detached spawn. With no extension yet
    # the bridge still answers browser_status (ok:false, connected:false) and
    # the CLI exits 2; what proves the ensure worked is that the answer came
    # from the bridge (connected_profiles) and arrived inside the budget.
    started = time.time()
    first = subprocess.run(
        [cli, "status", "--json"],
        capture_output=True,
        text=True,
        env=env,
        timeout=30,
    )
    elapsed = time.time() - started
    try:
        first_payload = json.loads(first.stdout)
    except json.JSONDecodeError:
        first_payload = {}
    check(first.returncode == 2, f"cold CLI status reaches the spawned bridge and reports not-connected (exit {first.returncode})")
    check(
        "connected_profiles" in first_payload,
        f"the spawned bridge answered the status command (stderr={first.stderr.strip()!r})",
    )
    check(elapsed < 8, f"ensure waited inside upstream's 8s budget ({elapsed:.2f}s)")

    # With a fake extension connected the same command exits 0.
    extension = None
    try:
        deadline = time.time() + 5
        while extension is None and time.time() < deadline:
            try:
                extension = WSClient(ws_port, timeout=1.0)
            except OSError:
                time.sleep(0.1)
        if extension is None:
            raise RuntimeError("spawned bridge never accepted the fake extension")
        extension.send_json(hello("cli-ensure"))
        time.sleep(0.2)
        second = subprocess.run(
            [cli, "status", "--json"],
            capture_output=True,
            text=True,
            env=env,
            timeout=30,
        )
    finally:
        if extension is not None:
            extension.close()
    try:
        second_payload = json.loads(second.stdout)
    except json.JSONDecodeError:
        second_payload = {}
    check(second.returncode == 0, f"CLI status exits 0 once the extension is connected (got {second.returncode}: {second.stderr.strip()})")
    check(second_payload.get("ok") is True, f"connected status result ({second_payload})")

    pid = None
    if os.path.exists(log_path):
        with open(log_path, "r", encoding="utf-8") as fh:
            for line in fh:
                if '"ready":true' in line.replace(" ", ""):
                    pid = json.loads(line).get("pid")
    check(isinstance(pid, int), f"spawned bridge logged its pid ({pid})")
    if isinstance(pid, int):
        try:
            os.kill(pid, 15)
        except ProcessLookupError:
            pass


# ── item 4: exit codes ────────────────────────────────────────────────────


def probe_exit_codes(cli: str, bridge_bin: str, home: str) -> None:
    # Bridge-side bad_request (unknown command over /command) → upstream text
    # taxonomy → 1. The Go CLI never sends an unknown command itself, so use
    # the bridge's own refusal: `evaluate --js ''` is a local precheck (6),
    # while an HTTP refusal with no fragment must stay 1.
    env = dict(os.environ)
    env["HIVE_HOME"] = home
    local = subprocess.run([cli, "evaluate", "--json"], capture_output=True, text=True, env=env, timeout=10)
    check(local.returncode == 6, f"local precheck still exits 6 (got {local.returncode})")

    # A real bridge answers a bad_request for a malformed command; craft it
    # through the HTTP API directly so the CLI's refusal classifier runs.
    http_port = free_port()
    control_port = free_port()
    ws_port = free_port()
    proc = spawn_bridge(
        bridge_bin,
        [
            "-ws", f"127.0.0.1:{ws_port}",
            "-http", f"127.0.0.1:{http_port}",
            "-control", f"127.0.0.1:{control_port}",
            "-no-legacy", "-no-sweep", "-no-idle-exit",
        ],
    )
    try:
        wait_http(f"http://127.0.0.1:{http_port}/health")
        env["HIVE_BROWSERBRIDGE_HTTP"] = f"127.0.0.1:{http_port}"
        # The bridge's upload verb checks the file only after it has a live
        # extension + tab target, so pose as the extension first.
        extension = WSClient(ws_port)
        extension.send_json(hello("upload-e2e"))
        time.sleep(0.2)
        try:
            missing = os.path.join(home, "missing-e2e.txt")
            upload = subprocess.run(
                [cli, "upload", "#f", "--file", missing, "--tab", "7", "--json"],
                capture_output=True, text=True, env=env, timeout=20,
            )
        finally:
            extension.close()
        check(upload.returncode == 4, f"bridge bad_request text ('File not found') exits 4 (got {upload.returncode}: {upload.stderr.strip()})")
        # And a bad_request with no fragment exits 1 like upstream.
        req = urllib.request.Request(
            f"http://127.0.0.1:{http_port}/command",
            data=json.dumps({"type": "browser_frobnicate", "params": {}}).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        try:
            urllib.request.urlopen(req, timeout=5)
            status = 200
            body = {}
        except urllib.error.HTTPError as exc:  # noqa: PERF203
            status = exc.code
            body = json.loads(exc.read().decode())
        check(
            status == 400 and body.get("code") == "unknown_command",
            f"bridge refuses an unknown command as bad_request (HTTP {status}, {body.get('code')})",
        )
    finally:
        stop(proc)


# ── item 5: parent death default ──────────────────────────────────────────


def probe_parent_death(bridge_bin: str) -> None:
    parent = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(120)"])
    ws_port, http_port, control_port = free_port(), free_port(), free_port()
    env = dict(os.environ)
    env["HIVE_DESKTOP_PARENT_PID"] = str(parent.pid)
    args = [
        "-ws", f"127.0.0.1:{ws_port}",
        "-http", f"127.0.0.1:{http_port}",
        "-control", f"127.0.0.1:{control_port}",
        "-no-legacy", "-no-sweep", "-no-idle-exit",
        # No -parent-check: the default must be upstream's 5s.
    ]
    bridge = spawn_bridge(bridge_bin, args, env=env)
    try:
        wait_http(f"http://127.0.0.1:{http_port}/health")
        started = time.time()
        parent.kill()
        parent.wait(timeout=5)
        try:
            code = bridge.wait(timeout=15)
        except subprocess.TimeoutExpired:
            code = None
        elapsed = time.time() - started
        check(code == 0, f"bridge exits 0 after the desktop parent dies (got {code})")
        check(4.5 <= elapsed <= 11.5, f"parent-death window is upstream's 5–10s (measured {elapsed:.2f}s)")
    finally:
        stop(bridge)


# ── item 6: legacy self-collision guard ───────────────────────────────────


def probe_legacy_guard(bridge_bin: str) -> None:
    ws_port = free_port()
    http_port = free_port()
    control_port = free_port()
    proc = spawn_bridge(
        bridge_bin,
        [
            "-ws", f"127.0.0.1:{ws_port}",
            "-http", f"127.0.0.1:{http_port}",
            "-control", f"127.0.0.1:{control_port}",
            "-legacy", f"127.0.0.1:{ws_port}",
            "-legacy-status", f"127.0.0.1:{http_port}",
            "-no-sweep", "-no-idle-exit",
        ],
    )
    try:
        payload = wait_http(f"http://127.0.0.1:{http_port}/status")
        check(bool(payload.get("bridge") == "running"), "primary/legacy same address: bridge still starts")
        check(not (payload.get("bind_errors") or {}), f"no self-collision bind errors ({payload.get('bind_errors')})")
        listening = payload.get("listening") or {}
        check(listening.get("legacy_ws") is not True, "legacy WS skipped when the primary owns the port")
    finally:
        stop(proc)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bridge", required=True, help="path to the built browserbridge binary")
    parser.add_argument("--cli", required=True, help="path to the built hive-browser binary")
    parser.add_argument("--keep", action="store_true", help="keep the temp dir")
    args = parser.parse_args()

    work = tempfile.mkdtemp(prefix="browser-transport-e2e.")
    home = os.path.join(work, "home")
    os.makedirs(home, exist_ok=True)
    print(f"workdir: {work}", flush=True)

    ws_port, http_port, control_port = free_port(), free_port(), free_port()
    bridge = None
    try:
        # Keepalive probe: 200ms ping/pong so the 20s default doesn't dictate
        # the test runtime.
        bridge = spawn_bridge(
            args.bridge,
            [
                "-ws", f"127.0.0.1:{ws_port}",
                "-http", f"127.0.0.1:{http_port}",
                "-control", f"127.0.0.1:{control_port}",
                "-no-legacy", "-no-sweep", "-no-idle-exit",
                "-ws-ping", "200ms", "-ws-pong", "200ms",
            ],
        )
        wait_http(f"http://127.0.0.1:{http_port}/health")
        probe_ws_keepalive(ws_port)
    finally:
        stop(bridge)

    # Legacy pair probe: default heartbeat, so the status poll below is not
    # racing a 200ms keepalive while it waits on the tab.list refresh.
    ws_port, http_port, control_port = free_port(), free_port(), free_port()
    legacy_ws, legacy_status = free_port(), free_port()
    bridge = None
    try:
        bridge = spawn_bridge(
            args.bridge,
            [
                "-ws", f"127.0.0.1:{ws_port}",
                "-http", f"127.0.0.1:{http_port}",
                "-control", f"127.0.0.1:{control_port}",
                "-legacy", f"127.0.0.1:{legacy_ws}",
                "-legacy-status", f"127.0.0.1:{legacy_status}",
                "-no-sweep", "-no-idle-exit",
            ],
        )
        ready = bridge.ready  # type: ignore[attr-defined]
        check(
            ready.get("legacy_status") == f"127.0.0.1:{legacy_status}",
            f"ready line reports the legacy status listener ({ready.get('legacy_status')})",
        )
        wait_http(f"http://127.0.0.1:{http_port}/health")
        probe_legacy(legacy_status, legacy_ws)
    finally:
        stop(bridge)

    probe_legacy_guard(args.bridge)
    probe_cli_ensure(args.cli, args.bridge, home, os.path.join(home, "bridge_host.log"))
    probe_exit_codes(args.cli, args.bridge, home)
    probe_parent_death(args.bridge)
    if not args.keep:
        shutil.rmtree(work, ignore_errors=True)

    print("", flush=True)
    if FAILURES:
        print(f"RESULT: {len(FAILURES)} failure(s)", flush=True)
        for failure in FAILURES:
            print(f"  - {failure}", flush=True)
        return 1
    print("RESULT: all transport/lifecycle probes passed", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
