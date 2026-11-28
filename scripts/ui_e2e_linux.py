#!/usr/bin/env python3
"""Linux UI E2E gate for Hive: boot serve, create a colony, assert the real DOM.

Runs *inside* a Linux container with /usr/bin/chromium and no Playwright. It
uses only the Python standard library: a small DevTools-Protocol client over
the Chromium websocket, a throwaway HIVE_HOME, and a real `hive-embed serve`.

What it proves (each step is scored, not decorative):

  a. the binary is ELF for this container's architecture (never a darwin build)
  b. serve answers /api/health on a free port and a throwaway home
  c. a queen DM session is created through POST /api/sessions
  d. that session is forked into a colony through the **popup path**
     (source_session_id + colony_id), which is what the Create Colony dialog
     does; the response must carry a live colony session and a worker
  e. the source session's meta.json records colony_spawned=true and
     spawned_colony_id=<colony> -- the spawn lock, not the child response
  f. /api/discover, /api/colonies/tools-index and disk metadata.json agree
  g. a real headless Chromium dismisses the first-run tour, then the sidebar
     contains a link to #/colony/<route-id> (slugToColonyId rules) whose label
     names the colony, with the English "Colonies" section label
  h. clicking that link renders the colony page: h1 names the colony, the
     queen's title chip is present, the conversation and Colony panel tabs
     render, and the page is not an error state
  i. negative control: a nonexistent colony is not in the sidebar and does not
     render a working colony page (no h1 identity, no queen chip, no panel)
  j. cleanup deletes the colony and sessions and the temporary HIVE_HOME

Exit codes: 0 PASS, 1 assertion failure, 2 environment error (no Linux binary,
no Chromium, serve never healthy, browser/CDP unavailable). A gate that cannot
go red is decoration; run with --fault dom-empty to exercise the red path.

Usage (inside hive-linux-qa:ui, repo mounted at /repo):
  python3 go_hive/scripts/ui_e2e_linux.py \
      --binary go_hive/go-hive/dist/hive-embed-linux-arm64 \
      --evidence-dir go_hive/docs/e2e-assets/linux-ui \
      --json-out go_hive/docs/e2e-assets/linux-ui/ui-e2e-linux.json
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import platform
import re
import secrets
import shutil
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path
from urllib.parse import urlsplit

EXIT_PASS = 0
EXIT_ASSERTION_FAILED = 1
EXIT_ENVIRONMENT_ERROR = 2

SCHEMA = "hive.ui-e2e-linux/v1"
DEFAULT_COLONY = "ui_gate_linux"
GHOST_COLONY = "ui_gate_ghost_does_not_exist"
CHROMIUM = "/usr/bin/chromium"
# userStorage namespaces every key: `hive:u:<activeUser>:<raw>`, and an
# unauthenticated local install uses "_". Setting the bare key would not stop
# the tour; this is the one the app actually reads.
TUTORIAL_KEY = "hive:u:_:tutorial-seen-v1"


def now_iso() -> str:
    return datetime.now(timezone.utc).astimezone().isoformat(timespec="seconds")


def slug_to_route_id(slug: str) -> str:
    """Mirror src/lib/colony-registry.ts::slugToColonyId."""
    out = slug.replace("_", "-")
    if out.startswith("email-"):
        out = out[len("email-"):]
    if out.endswith("-agent"):
        out = out[: -len("-agent")]
    return out


def normalize_name(value: str) -> str:
    """Identity, not typography: `ui_gate_linux` == `Ui Gate Linux`."""
    return re.sub(r"[^a-z0-9]", "", (value or "").lower())


def sha256_file(path: Path) -> str:
    import hashlib
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def elf_arch(path: Path) -> tuple[str, str]:
    """Return (magic/format, arch) without trusting the filename."""
    with path.open("rb") as handle:
        header = handle.read(20)
    if len(header) < 20 or header[:4] != b"\x7fELF":
        if header[:4] in (b"\xcf\xfa\xed\xfe", b"\xce\xfa\xed\xfe"):
            return "Mach-O", "darwin"
        return "unknown", "unknown"
    machine = struct.unpack("<H", header[18:20])[0]
    return "ELF", {0xB7: "arm64", 0x3E: "amd64", 0x03: "386"}.get(machine, f"machine-{machine}")


def machine_arch() -> str:
    machine = platform.machine().lower()
    return {"aarch64": "arm64", "x86_64": "amd64", "arm64": "arm64", "amd64": "amd64"}.get(
        machine, machine)


class HttpResponse:
    __slots__ = ("status", "json", "text", "error", "content_type")

    def __init__(self, status=None, body_json=None, text="", error="", content_type=""):
        self.status = status
        self.json = body_json
        self.text = text
        self.error = error
        self.content_type = content_type


def http_call(method: str, url: str, payload=None, timeout: float = 30.0) -> HttpResponse:
    headers = {"Accept": "application/json"}
    data = None
    if payload is not None:
        data = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            raw = response.read()
            text = raw.decode("utf-8", errors="replace")
            try:
                parsed = json.loads(text) if text else None
            except ValueError:
                parsed = None
            return HttpResponse(response.status, parsed, text, "",
                                response.headers.get("Content-Type", ""))
    except urllib.error.HTTPError as exc:
        text = exc.read().decode("utf-8", errors="replace")
        try:
            parsed = json.loads(text) if text else None
        except ValueError:
            parsed = None
        return HttpResponse(exc.code, parsed, text, "",
                            exc.headers.get("Content-Type", "") if exc.headers else "")
    except Exception as exc:  # noqa: BLE001 - transport failure is data
        return HttpResponse(None, None, "", f"{type(exc).__name__}: {exc}")


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_health(proc: subprocess.Popen, port: int, timeout: float) -> tuple[bool, str]:
    deadline = time.monotonic() + timeout
    last = "no response"
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            return False, f"serve exited while booting (rc={proc.returncode})"
        response = http_call("GET", f"http://127.0.0.1:{port}/api/health", timeout=2.0)
        if response.status == 200:
            return True, ""
        last = response.error or f"HTTP {response.status}"
        time.sleep(0.15)
    return False, f"/api/health not 200 within {timeout:.0f}s (last: {last})"


def stop_process(proc: subprocess.Popen | None, grace: float = 15.0) -> tuple[bool, str]:
    if proc is None:
        return True, "no serve process"
    if proc.poll() is not None:
        return True, f"serve already exited (rc={proc.returncode})"
    proc.terminate()
    try:
        proc.wait(timeout=grace)
        return True, f"serve stopped (rc={proc.returncode})"
    except subprocess.TimeoutExpired:
        proc.kill()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            return False, "serve did not die after SIGKILL"
        return False, "serve required SIGKILL"


class Recorder:
    def __init__(self):
        self.steps: list[dict] = []
        self.cleanup: list[dict] = []
        self.notes: list[str] = []
        self.findings: list[dict] = []

    def step(self, step_id: str, title: str, ok: bool, actual: str,
             checks: list[dict] | None = None, fields=None, http_status=None,
             duration_ms=None) -> bool:
        self.steps.append({
            "id": step_id, "title": title, "status": "pass" if ok else "fail",
            "actual": actual, "checks": checks or [], "fields": fields or {},
            "http_status": http_status, "duration_ms": duration_ms,
        })
        return ok

    def check(self, name: str, ok: bool, actual: str) -> dict:
        return {"name": name, "ok": bool(ok), "actual": actual}

    def failed(self) -> list[str]:
        return [s["id"] for s in self.steps if s["status"] == "fail"]

    def cleanup_action(self, action: str, ok: bool, detail: str) -> None:
        self.cleanup.append({"action": action, "ok": bool(ok), "detail": detail})

    def cleanup_ok(self) -> bool:
        return all(item["ok"] for item in self.cleanup)


# ------------------------------------------------------------------- CDP client


class WebSocket:
    """Minimal RFC6455 client: text/binary frames, masking, ping/pong."""

    def __init__(self, host: str, port: int, path: str, timeout: float = 60.0):
        self.sock = socket.create_connection((host, port), timeout=timeout)
        self.sock.settimeout(timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f"GET {path} HTTP/1.1\r\nHost: {host}:{port}\r\n"
            "Upgrade: websocket\r\nConnection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n"
        )
        self.sock.sendall(request.encode())
        buffer = b""
        while b"\r\n\r\n" not in buffer:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise RuntimeError("chromium closed the websocket during handshake")
            buffer += chunk
        head, self.buffer = buffer.split(b"\r\n\r\n", 1)
        if b" 101 " not in head.split(b"\r\n")[0]:
            raise RuntimeError("websocket handshake failed: " + head.decode(errors="replace")[:300])
        self.fragment = b""

    def _read_exact(self, count: int) -> bytes:
        while len(self.buffer) < count:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise RuntimeError("chromium closed the websocket")
            self.buffer += chunk
        out, self.buffer = self.buffer[:count], self.buffer[count:]
        return out

    def _frame(self, opcode: int, payload: bytes) -> None:
        header = bytearray([0x80 | opcode])
        length = len(payload)
        if length < 126:
            header.append(0x80 | length)
        elif length < 65536:
            header.append(0x80 | 126)
            header += struct.pack("!H", length)
        else:
            header.append(0x80 | 127)
            header += struct.pack("!Q", length)
        mask = os.urandom(4)
        header += mask
        self.sock.sendall(bytes(header) + bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))

    def send(self, text: str) -> None:
        self._frame(0x1, text.encode())

    def recv(self) -> str:
        while True:
            first, second = self._read_exact(2)
            fin, opcode = first & 0x80, first & 0x0F
            masked, length = second & 0x80, second & 0x7F
            if length == 126:
                length = struct.unpack("!H", self._read_exact(2))[0]
            elif length == 127:
                length = struct.unpack("!Q", self._read_exact(8))[0]
            mask = self._read_exact(4) if masked else b""
            payload = self._read_exact(length)
            if masked:
                payload = bytes(b ^ mask[i % 4] for i, b in enumerate(payload))
            if opcode == 0x9:  # ping
                self._frame(0xA, payload)
                continue
            if opcode == 0x8:
                raise RuntimeError("chromium closed the websocket")
            if opcode in (0x0, 0x1, 0x2):
                self.fragment += payload
                if fin:
                    data, self.fragment = self.fragment, b""
                    return data.decode(errors="replace")

    def close(self) -> None:
        try:
            self.sock.close()
        except OSError:
            pass


class Chrome:
    def __init__(self, binary: str, profile: Path, log_path: Path):
        self.binary = binary
        self.profile = profile
        self.profile.mkdir(parents=True, exist_ok=True)
        self.log = log_path.open("w", encoding="utf-8")
        self.proc = subprocess.Popen(
            [binary, "--headless=new", "--no-sandbox", "--disable-gpu",
             "--disable-dev-shm-usage", "--hide-scrollbars", "--mute-audio",
             "--no-first-run", "--no-default-browser-check",
             "--remote-debugging-port=0", f"--user-data-dir={profile}",
             "--window-size=1440,900", "about:blank"],
            stdout=self.log, stderr=subprocess.STDOUT)
        self.ws = None
        self.cdp = None
        self.events: list[dict] = []
        self.next_id = 0

    def connect(self, timeout: float = 30.0) -> None:
        deadline = time.monotonic() + timeout
        port = None
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                raise RuntimeError(f"chromium exited during startup (rc={self.proc.returncode})")
            try:
                port = int((self.profile / "DevToolsActivePort").read_text().splitlines()[0])
                break
            except (OSError, ValueError, IndexError):
                time.sleep(0.1)
        if not port:
            raise RuntimeError("chromium never wrote DevToolsActivePort")
        targets = None
        for _ in range(100):
            try:
                with urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list", timeout=3) as response:
                    targets = json.loads(response.read().decode())
                break
            except Exception:  # noqa: BLE001
                time.sleep(0.1)
        if not targets:
            raise RuntimeError("chromium DevTools endpoint never listed a target")
        page = next((t for t in targets if t.get("type") == "page"), None)
        if not page:
            raise RuntimeError("chromium has no page target")
        parts = urlsplit(page["webSocketDebuggerUrl"])
        self.ws = WebSocket("127.0.0.1", port, parts.path)
        self.cdp = True
        self.cmd("Page.enable")
        self.cmd("Runtime.enable")

    def cmd(self, method: str, **params):
        self.next_id += 1
        message_id = self.next_id
        self.ws.send(json.dumps({"id": message_id, "method": method, "params": params}))
        while True:
            message = json.loads(self.ws.recv())
            if message.get("id") == message_id:
                if "error" in message:
                    raise RuntimeError(f"{method}: {message['error']}")
                return message.get("result", {})
            if message.get("method"):
                self.events.append(message)
                if len(self.events) > 300:
                    self.events = self.events[-300:]

    def eval(self, expression: str, await_promise: bool = False):
        result = self.cmd("Runtime.evaluate", expression=expression, returnByValue=True,
                          awaitPromise=await_promise, userGesture=True)
        if result.get("exceptionDetails"):
            raise RuntimeError("page JS error: " + json.dumps(result["exceptionDetails"])[:500])
        return result.get("result", {}).get("value")

    def wait(self, expression: str, timeout: float = 20.0, interval: float = 0.2):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                value = self.eval(expression)
                if value:
                    return value
            except Exception:  # noqa: BLE001 - navigation can invalidate execution context
                pass
            time.sleep(interval)
        return None

    def navigate(self, url: str, timeout: float = 30.0) -> None:
        self.cmd("Page.navigate", url=url)
        self.wait("document.readyState === 'complete'", timeout=timeout)

    def screenshot(self, path: Path) -> str:
        path.parent.mkdir(parents=True, exist_ok=True)
        data = self.cmd("Page.captureScreenshot", format="png", fromSurface=True)
        path.write_bytes(base64.b64decode(data["data"]))
        return str(path)

    def console_errors(self) -> list[str]:
        errors: list[str] = []
        for event in self.events:
            if event.get("method") == "Runtime.consoleAPICalled":
                params = event.get("params") or {}
                if params.get("type") in ("error", "assert"):
                    text = " ".join(str(arg.get("value", arg.get("description", "")))
                                    for arg in params.get("args") or [])
                    errors.append(text[:300])
            elif event.get("method") == "Runtime.exceptionThrown":
                details = (event.get("params") or {}).get("exceptionDetails") or {}
                errors.append((details.get("text") or "uncaught exception")[:300])
        return errors[-10:]

    def close(self) -> None:
        try:
            if self.ws:
                self.ws.close()
        except Exception:  # noqa: BLE001
            pass
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self.proc.kill()
        self.log.close()


# ---------------------------------------------------------------------- helpers


def sidebar_snapshot(chrome: Chrome) -> dict:
    return chrome.eval("""(() => {
      const scope = document.querySelector('aside') || document.querySelector('nav') || document;
      const links = [...scope.querySelectorAll('a[href*="/colony/"]')]
        .map(a => ({href: a.getAttribute('href') || '', text: (a.textContent || '').trim()}));
      const sections = [...scope.querySelectorAll('button, span, h2, h3')]
        .map(e => (e.textContent || '').trim())
        .filter(t => t);
      const badges = [...scope.querySelectorAll('.sr-only')]
        .map(e => (e.textContent || '').trim()).filter(t => /^[0-9]+$/.test(t));
      return {links, sections: sections.slice(0, 40), badges,
              body: (document.body.innerText || '').slice(0, 1200)};
    })()""")


def body_snapshot(chrome: Chrome) -> dict:
    return chrome.eval("""(() => {
      const text = document.body.innerText || '';
      const h1 = document.querySelector('h1');
      const header = h1 ? (h1.parentElement ? h1.parentElement.innerText : h1.innerText) : '';
      const tabs = [...document.querySelectorAll('button, a, [role="tab"]')]
        .map(e => (e.textContent || '').trim())
        .filter(t => /^(Data|Plan|Automations|Workers|数据|计划|自动化|工作节点)$/.test(t));
      return {
        url: location.href,
        hash: location.hash,
        path: location.pathname,
        h1: h1 ? (h1.textContent || '').trim() : '',
        header: (header || '').slice(0, 500),
        tabs: [...new Set(tabs)],
        has_conversation: /conversation|对话|会话/i.test(text),
        has_colony_panel: /Colony panel|Colony Panel|殖民地面板/.test(text),
        not_found: /Colony not found|Colony not found:|蜂群不存在|殖民地不存在/.test(text),
        page_not_found: /Page not found|404|The page you.re looking for/i.test(text),
        connecting: /Connecting to agent|Connecting to .*\\.\\.\\./.test(text),
        body: text.slice(0, 1600),
      };
    })()""")


def find_source_meta(home: Path, session_id: str) -> Path | None:
    for path in home.glob("queens/*/sessions/*/meta.json"):
        if path.parent.name == session_id:
            return path
    for path in home.glob("**/meta.json"):
        if path.parent.name == session_id:
            return path
    return None


def flatten_discover(payload) -> list[dict]:
    rows: list[dict] = []
    if isinstance(payload, dict):
        for value in payload.values():
            if isinstance(value, list):
                rows.extend(item for item in value if isinstance(item, dict))
    return rows


def pick_colony(rows: list[dict], colony_id: str) -> dict | None:
    for row in rows:
        if row.get("name") == colony_id:
            return row
        path = str(row.get("path") or "")
        if path.rstrip("/").rsplit("/", 1)[-1] == colony_id:
            return row
    return None


# ------------------------------------------------------------------------- gate


def run_gate(rec: Recorder, args, env: dict) -> int:
    home = Path(env["hive_home"])
    base = env["base_url"]
    colony_id = args.colony_id
    route_id = slug_to_route_id(colony_id)
    display_name = colony_id.replace("_", " ").title()

    # ---------------------------------------------------------------- a: binary
    binary = Path(env["binary"])
    fmt, arch = elf_arch(binary)
    host_arch = machine_arch()
    checks = [
        rec.check("server binary exists", binary.is_file(), str(binary)),
        rec.check("server binary is ELF", fmt == "ELF", fmt),
        rec.check("server binary matches container arch", arch == host_arch,
                  f"binary={arch} container={host_arch}"),
    ]
    ok = rec.step("a", "Linux server binary is runnable here", all(c["ok"] for c in checks),
                  f"{binary.name}: {fmt}/{arch} ({env['binary_sha256'][:16]}…)",
                  checks=checks, fields={"format": fmt, "arch": arch,
                                         "sha256": env["binary_sha256"]})
    if not ok:
        return EXIT_ENVIRONMENT_ERROR

    # The route id is not the on-disk slug. Pin the frontend's transformation
    # so a future gate edit cannot quietly assert against the wrong URL.
    mappings = {
        "ui_gate_linux": "ui-gate-linux",
        "email_inbound_agent": "inbound",
        "email_inbound": "inbound",
        "outbound_agent": "outbound",
        "demo": "demo",
    }
    bad = {slug: slug_to_route_id(slug) for slug, want in mappings.items()
           if slug_to_route_id(slug) != want}
    rec.step("a2", "slugToColonyId mapping matches the frontend", not bad,
             f"cases={mappings} mismatches={bad}",
             checks=[rec.check("slug -> route id", not bad, repr(bad))])
    if bad:
        return EXIT_ENVIRONMENT_ERROR

    # ----------------------------------------------------------------- b: serve
    checks = [rec.check("/api/health is 200", True, f"HTTP {env['health_status']}")]
    rec.step("b", "hive-embed serve answers on the throwaway home", True,
             f"{base} · HIVE_HOME={home} · pid={env['serve_pid']}", checks=checks,
             http_status=env["health_status"])
    if args.fault == "dom-empty":
        rec.notes.append("fault injection active: browser will load about:blank, "
                         "so the positive DOM assertions must fail")

    # ------------------------------------------------------------ c: DM session
    dm = http_call("POST", base + "/api/sessions", {}, timeout=args.timeout)
    dm_body = dm.json if isinstance(dm.json, dict) else {}
    dm_session = str(dm_body.get("session_id") or "")
    dm_queen = str(dm_body.get("queen_id") or "")
    checks = [
        rec.check("DM create is 201", dm.status == 201, f"HTTP {dm.status}"),
        rec.check("DM carries session_id", bool(dm_session), repr(dm_session)),
        rec.check("DM is an independent queen session",
                  dm_body.get("queen_phase") == "independent",
                  repr(dm_body.get("queen_phase"))),
    ]
    ok = rec.step("c", "create the source queen DM session", all(c["ok"] for c in checks),
                  f"HTTP {dm.status} session={dm_session or 'none'} queen={dm_queen or 'none'}",
                  checks=checks, http_status=dm.status,
                  fields={"session_id": dm_session, "queen_id": dm_queen,
                          "body": dm_body})
    if not ok:
        return EXIT_ASSERTION_FAILED
    env["_dm_session"] = dm_session

    profiles = http_call("GET", base + "/api/queen/profiles", timeout=args.timeout)
    queens = (profiles.json or {}).get("queens", []) if isinstance(profiles.json, dict) else []
    profile = next((q for q in queens if isinstance(q, dict) and q.get("id") == dm_queen), {})
    queen_title = str(profile.get("title") or "")
    queen_name = str(profile.get("name") or dm_body.get("queen_name") or "")
    rec.step("c2", "resolve the queen's display title", profiles.status == 200 and bool(queen_title),
             f"HTTP {profiles.status} queen={dm_queen!r} name={queen_name!r} title={queen_title!r}",
             checks=[rec.check("queen title available", bool(queen_title), repr(queen_title))],
             http_status=profiles.status)

    # ------------------------------------------------------- d: fork into colony
    fork_payload = {
        "source_session_id": dm_session,
        "colony_id": colony_id,
        "queen_name": dm_queen,
        "initial_phase": "colony",
        "initial_prompt": "UI E2E gate: prove the colony renders in the sidebar and on its page.",
    }
    fork = http_call("POST", base + "/api/sessions", fork_payload, timeout=max(args.timeout, 180))
    fork_body = fork.json if isinstance(fork.json, dict) else {}
    child_session = str(fork_body.get("session_id") or "")
    checks = [
        rec.check("fork create is 201", fork.status == 201, f"HTTP {fork.status}"),
        rec.check("fork returns a session", bool(child_session), repr(child_session)),
        rec.check("fork is bound to the colony", fork_body.get("colony_id") == colony_id,
                  repr(fork_body.get("colony_id"))),
        rec.check("fork has a worker", fork_body.get("has_worker") is True,
                  repr(fork_body.get("has_worker"))),
        rec.check("fork phase is colony", fork_body.get("queen_phase") == "colony",
                  repr(fork_body.get("queen_phase"))),
    ]
    ok = rec.step("d", "fork the DM into a colony through the popup path",
                  all(c["ok"] for c in checks),
                  f"HTTP {fork.status} colony={fork_body.get('colony_id')!r} "
                  f"session={child_session or 'none'} worker={fork_body.get('has_worker')!r}",
                  checks=checks, http_status=fork.status,
                  fields={"body": fork_body, "request": {k: v for k, v in fork_payload.items()
                                                         if k != "initial_prompt"}})
    if not ok:
        rec.notes.append("fork response: " + json.dumps(fork_body, ensure_ascii=False)[:1200])
        return EXIT_ASSERTION_FAILED
    env["_child_session"] = child_session
    env["_colony_id"] = colony_id

    # ------------------------------------------------- e: spawn lock on source
    meta_path = find_source_meta(home, dm_session)
    meta = {}
    if meta_path:
        try:
            meta = json.loads(meta_path.read_text(encoding="utf-8"))
        except ValueError:
            meta = {}
    checks = [
        rec.check("source meta.json exists", meta_path is not None,
                  str(meta_path or "not found under queens/*/sessions")),
        rec.check("colony_spawned is true", meta.get("colony_spawned") is True,
                  repr(meta.get("colony_spawned"))),
        rec.check("spawned_colony_id names this colony",
                  meta.get("spawned_colony_id") == colony_id,
                  repr(meta.get("spawned_colony_id"))),
    ]
    ok = rec.step("e", "source session records the spawn lock", all(c["ok"] for c in checks),
                  f"colony_spawned={meta.get('colony_spawned')!r} "
                  f"spawned_colony_id={meta.get('spawned_colony_id')!r}",
                  checks=checks,
                  fields={"meta_path": str(meta_path) if meta_path else None,
                          "meta": meta})
    # The child response's colony_spawned=false is expected: the flag describes
    # the session that spawned the colony, not the new colony session. Record
    # it so nobody mistakes it for the product state.
    rec.notes.append(
        "fork response colony_spawned=%r spawned_colony_id=%r is the CHILD session; "
        "the source session's meta.json is authoritative for the spawn lock"
        % (fork_body.get("colony_spawned"), fork_body.get("spawned_colony_id")))
    env["spawn_state"] = {
        "child_response": {
            "colony_spawned": fork_body.get("colony_spawned"),
            "spawned_colony_id": fork_body.get("spawned_colony_id"),
        },
        "source_meta": {
            "colony_spawned": meta.get("colony_spawned"),
            "spawned_colony_id": meta.get("spawned_colony_id"),
            "path": str(meta_path) if meta_path else None,
        },
        "verdict": "completed" if (meta.get("colony_spawned") is True and
                                    meta.get("spawned_colony_id") == colony_id) else "not_completed",
        "explanation": "colony_spawned/spawned_colony_id describe the SOURCE DM session "
                       "that forked the colony. The child colony session's response carries "
                       "false/null by design; reading it as the spawn state is a harness bug.",
    }
    if not ok:
        return EXIT_ASSERTION_FAILED

    # ------------------------------------------------------ f: API + disk cross
    time.sleep(0.5)
    discover = http_call("GET", base + "/api/discover", timeout=args.timeout)
    rows = flatten_discover(discover.json)
    row = pick_colony(rows, colony_id)
    tools = http_call("GET", base + "/api/colonies/tools-index", timeout=args.timeout)
    tool_row = next((r for r in (tools.json or {}).get("colonies", [])
                     if isinstance(r, dict) and r.get("name") == colony_id), None) \
        if isinstance(tools.json, dict) else None
    colony_meta_path = home / "colonies" / colony_id / "metadata.json"
    colony_meta = {}
    if colony_meta_path.is_file():
        try:
            colony_meta = json.loads(colony_meta_path.read_text(encoding="utf-8"))
        except ValueError:
            colony_meta = {}
    api_created = (tool_row or {}).get("created_at")
    disk_created = colony_meta.get("created_at")
    checks = [
        rec.check("/api/discover lists the colony", row is not None,
                  f"HTTP {discover.status} rows={len(rows)}"),
        rec.check("/api/colonies/tools-index lists the colony", tool_row is not None,
                  f"HTTP {tools.status}"),
        rec.check("tools-index queen matches", bool(tool_row) and
                  (tool_row or {}).get("queen_name") == dm_queen,
                  repr((tool_row or {}).get("queen_name"))),
        rec.check("colony metadata.json exists", colony_meta_path.is_file(),
                  str(colony_meta_path)),
        rec.check("API and disk created_at agree",
                  api_created is not None and disk_created is not None and
                  abs(float(api_created) - float(disk_created)) < 1e-6,
                  f"api={api_created!r} disk={disk_created!r}"),
        rec.check("colony metadata records the queen session",
                  colony_meta.get("queen_session_id") == child_session,
                  f"queen_session_id={colony_meta.get('queen_session_id')!r}"),
    ]
    ok = rec.step("f", "API, disk metadata and the live session agree", all(c["ok"] for c in checks),
                  f"discover={row is not None} tools={tool_row is not None} "
                  f"created_at={disk_created!r}", checks=checks,
                  http_status=tools.status,
                  fields={"discover_row": row, "tools_row": tool_row,
                          "colony_metadata": colony_meta})
    if row and row.get("name"):
        display_name = str(row.get("name")) or display_name
    if not ok:
        return EXIT_ASSERTION_FAILED

    # ---------------------------------------------------------------- g+: browser
    chrome = None
    try:
        chrome = Chrome(args.chromium, home / "chrome-profile", home / "chromium.log")
        try:
            chrome.connect(timeout=args.timeout)
        except Exception as exc:  # noqa: BLE001
            rec.step("g0", "headless Chromium starts and exposes CDP", False,
                     f"{type(exc).__name__}: {exc}", fields={"chromium": args.chromium})
            rec.notes.append(f"chromium log: {home / 'chromium.log'}")
            return EXIT_ENVIRONMENT_ERROR
        rec.step("g0", "headless Chromium starts and exposes CDP", True,
                 f"{args.chromium} · profile={home / 'chrome-profile'}",
                 fields={"chromium": args.chromium})

        # ------------------------------------------------------------ g: onboarding
        if args.fault == "dom-empty":
            chrome.navigate("about:blank", timeout=args.timeout)
            rec.step("g", "fault injection: the page renders no app DOM", False,
                     "about:blank (negative self-check, not a product run)")
            sidebar = {"links": [], "sections": [], "badges": [], "body": ""}
        else:
            chrome.navigate(base + "/", timeout=args.timeout)
            tour = chrome.wait("!!document.querySelector('[aria-label=\"Skip tour\"]')", timeout=4.0)
            tour_present = bool(tour)
            dismissed = False
            method = "not present"
            if tour_present:
                clicked = chrome.eval(
                    "(() => {const b=document.querySelector('[aria-label=\"Skip tour\"]');"
                    "if(!b) return false; b.click(); return true;})()")
                dismissed = bool(clicked) and bool(
                    chrome.wait("!document.querySelector('[aria-label=\"Skip tour\"]')", timeout=5.0))
                method = "Skip tour button"
            # Mark the tour seen the same way the app does (namespaced key, JSON
            # true) and reload; the real content must then be uncovered.
            chrome.eval(f"localStorage.setItem('{TUTORIAL_KEY}', 'true')")
            chrome.cmd("Page.reload", ignoreCache=False)
            chrome.wait("document.readyState === 'complete'", timeout=args.timeout)
            chrome.wait("document.querySelectorAll('a[href*=\"/colony/\"]').length > 0",
                        timeout=args.timeout)
            tour_after = bool(chrome.wait("!!document.querySelector('[aria-label=\"Skip tour\"]')",
                                          timeout=1.0))
            checks = [
                rec.check("onboarding was observed or absent", True,
                          f"present={tour_present}"),
                rec.check("onboarding is gone after dismissal/reload", not tour_after,
                          f"after_reload_present={tour_after}"),
            ]
            rec.step("g", "first-run onboarding is handled", not tour_after,
                     f"present={tour_present} dismissed={dismissed} method={method} "
                     f"after_reload={tour_after}", checks=checks,
                     fields={"present": tour_present, "dismissed": dismissed,
                             "method": method, "after_reload_present": tour_after,
                             "storage_key": TUTORIAL_KEY})
            chrome.screenshot(Path(args.evidence_dir) / "linux-root.png")
            sidebar = sidebar_snapshot(chrome)

        # ---------------------------------------------------------- h: sidebar
        links = sidebar.get("links") or []
        hrefs = [link["href"] for link in links]
        labels = [link["text"] for link in links]
        route_re = re.compile(r"(?:#)?/colony/" + re.escape(route_id) + r"(?:$|[/?#])")
        target = next((link for link in links if route_re.search(link["href"])), None)
        has_english_colonies = any(
            re.fullmatch(r"Colonies\s*[0-9]*", text, re.IGNORECASE)
            for text in (sidebar.get("sections") or [])) or \
            "colonies" in (sidebar.get("body") or "").lower()
        checks = [
            rec.check("sidebar rendered colony links", bool(links), f"{len(links)} link(s)"),
            rec.check(f"sidebar contains #/colony/{route_id}", target is not None,
                      f"target={target!r} links={hrefs[:8]}"),
            rec.check("sidebar label names this colony",
                      any(normalize_name(label) == normalize_name(display_name)
                          for label in labels),
                      f"want~{normalize_name(display_name)!r} got={[normalize_name(l) for l in labels[:8]]}"),
            rec.check("sidebar uses the English Colonies label", has_english_colonies,
                      "sections=" + repr((sidebar.get("sections") or [])[:12])),
        ]
        ok = rec.step("h", "colony appears in the sidebar after real JS renders",
                      all(c["ok"] for c in checks),
                      f"{len(links)} link(s) · target={target['href'] if target else 'absent'} "
                      f"· labels={labels[:5]}",
                      checks=checks,
                      fields={"links": links, "sections": sidebar.get("sections"),
                              "badges": sidebar.get("badges"),
                              "body": (sidebar.get("body") or "")[:800]})
        if not ok:
            rec.notes.append("sidebar probe: " + json.dumps(
                {"links": links, "body": (sidebar.get("body") or "")[:500]},
                ensure_ascii=False))
            return EXIT_ASSERTION_FAILED

        # ------------------------------------------------------- i: click + page
        clicked = chrome.eval(
            "(() => {const a=[...document.querySelectorAll('a[href*=\"/colony/\"]')]"
            f".find(x => /(?:#)?\\/colony\\/{re.escape(route_id)}(?:$|[/?#])/.test(x.getAttribute('href')||''));"
            "if(!a) return null; a.scrollIntoView({block:'center'}); a.click();"
            "return a.getAttribute('href');})()")
        if not clicked:
            rec.step("i0", "click the sidebar link", False, "target anchor disappeared")
            return EXIT_ASSERTION_FAILED
        route_ok = chrome.wait(
            f"(location.hash + location.pathname).includes('/colony/{route_id}')",
            timeout=args.timeout)
        chrome.wait("document.querySelector('h1') && document.querySelector('h1').textContent.trim().length > 0",
                    timeout=args.timeout)
        time.sleep(0.8)
        page = body_snapshot(chrome)
        h1_ok = normalize_name(page.get("h1", "")) == normalize_name(display_name)
        queen_header_ok = bool(queen_title) and queen_title in (page.get("header") or "")
        panel_tabs = set(page.get("tabs") or [])
        need_tabs = {"Data", "Plan", "Automations", "Workers"}
        checks = [
            rec.check("sidebar click navigates to the colony route", bool(route_ok),
                      f"url={page.get('url')} hash={page.get('hash')}"),
            rec.check("colony page h1 names the colony", h1_ok,
                      f"h1={page.get('h1')!r} want~{display_name!r}"),
            rec.check("queen title chip is rendered", queen_header_ok,
                      f"want={queen_title!r} header={page.get('header')!r}"),
            rec.check("conversation panel is rendered", bool(page.get("has_conversation")),
                      f"body={page.get('body', '')[:300]!r}"),
            rec.check("Colony panel is rendered", bool(page.get("has_colony_panel")),
                      f"tabs={sorted(panel_tabs)}"),
            rec.check("Data/Plan/Automations/Workers tabs render",
                      need_tabs.issubset(panel_tabs), f"tabs={sorted(panel_tabs)}"),
            rec.check("colony page is not a not-found state", not page.get("not_found"),
                      f"not_found={page.get('not_found')}"),
            rec.check("colony page is not the app 404", not page.get("page_not_found"),
                      f"page_not_found={page.get('page_not_found')}"),
        ]
        ok = rec.step("i", "clicking the sidebar entry renders the real colony page",
                      all(c["ok"] for c in checks),
                      f"h1={page.get('h1')!r} queen={queen_title!r} tabs={sorted(panel_tabs)} "
                      f"conversation={page.get('has_conversation')}",
                      checks=checks,
                      fields={"page": page,
                              "console_errors": chrome.console_errors()})
        colony_shot = chrome.screenshot(Path(args.evidence_dir) / "linux-colony.png")
        rec.steps[-1]["fields"]["screenshot"] = colony_shot
        if not ok:
            rec.notes.append("colony page body: " + (page.get("body") or "")[:800])
            return EXIT_ASSERTION_FAILED

        # -------------------------------------------------- j: negative control
        ghost_route = slug_to_route_id(GHOST_COLONY)
        ghost_re = re.compile(r"(?:#)?/colony/" + re.escape(ghost_route) + r"(?:$|[/?#])")
        ghost_listed = any(ghost_re.search(href) for href in hrefs)
        before_ghost = sidebar_snapshot(chrome)
        chrome.eval(f"location.hash = '#/colony/{ghost_route}'")
        time.sleep(2.5)
        ghost = body_snapshot(chrome)
        ghost_sections = sidebar_snapshot(chrome)
        ghost_sidebar_listed = any(
            ghost_re.search(link.get("href", "")) for link in ghost_sections.get("links") or [])
        ghost_named = normalize_name(ghost.get("h1", "")) == normalize_name(display_name)
        ghost_has_queen = bool(queen_title) and queen_title in (ghost.get("header") or "")
        ghost_has_panels = bool(ghost.get("has_colony_panel")) or bool(ghost.get("tabs"))
        # A working colony page is the failure condition. An explicit "Colony
        # not found" is the ideal negative; a perpetual "Connecting to agent…"
        # is recorded as a product finding below, not silently accepted.
        ghost_is_working_page = bool(ghost_named or ghost_has_queen or ghost_has_panels)
        checks = [
            rec.check("ghost colony is absent from the sidebar", not ghost_sidebar_listed,
                      f"listed={ghost_sidebar_listed}"),
            rec.check("ghost route is not a working colony page", not ghost_is_working_page,
                      f"named={ghost_named} queen={ghost_has_queen} panels={ghost_has_panels}"),
            rec.check("ghost route has no colony panel tabs", not ghost.get("tabs"),
                      f"tabs={ghost.get('tabs')}"),
            # The page must resolve an unknown route, not leave the user on an
            # overlay that can never clear. These two checks are what make the
            # gate red for the old permanent \"Connecting to agent...\" bug.
            rec.check("ghost route shows an explicit not-found state", bool(ghost.get("not_found")),
                      f"not_found={ghost.get('not_found')} h1={ghost.get('h1')!r}"),
            rec.check("ghost route is not stuck on the connecting overlay", not ghost.get("connecting"),
                      f"connecting={ghost.get('connecting')}"),
        ]
        ok = rec.step("j", "negative control: a nonexistent colony does not render",
                      all(c["ok"] for c in checks),
                      f"listed={ghost_sidebar_listed} h1={ghost.get('h1')!r} "
                      f"not_found={ghost.get('not_found')} connecting={ghost.get('connecting')}",
                      checks=checks,
                      fields={"ghost": ghost, "sidebar": ghost_sections})
        ghost_shot = chrome.screenshot(Path(args.evidence_dir) / "linux-colony-ghost.png")
        rec.steps[-1]["fields"]["screenshot"] = ghost_shot
        if ghost.get("not_found") is False and ghost.get("connecting"):
            rec.findings.append({
                "id": "unknown-colony-stuck-connecting",
                "severity": "P2",
                "kind": "product",
                "summary": "A nonexistent /colony/<id> route shows 'Connecting to agent…' "
                           "indefinitely instead of the app's explicit 'Colony not found' state.",
                "evidence": {
                    "route": f"#/colony/{ghost_route}",
                    "h1": ghost.get("h1"),
                    "body_excerpt": (ghost.get("body") or "")[:500],
                    "screenshot": ghost_shot,
                    "source": "frontend/src/pages/colony-chat.tsx: initial state loading=true; "
                              "the not-found branch also requires !agentState.loading",
                },
                "suggested_fix": "When colonyId is present, no matching colony exists after "
                                 "refreshColonies resolves, and no routeState.agentPath exists, clear "
                                 "loading and render the not-found state (or a 404) instead of a "
                                 "perpetual connecting overlay.",
            })
        if not ok:
            return EXIT_ASSERTION_FAILED

        # ---------------------------------------------------------- evidence
        rec.step("k", "evidence artifacts written", True,
                 f"root={Path(args.evidence_dir) / 'linux-root.png'} "
                 f"colony={colony_shot} ghost={ghost_shot}",
                 fields={"screenshots": [str(Path(args.evidence_dir) / "linux-root.png"),
                                         colony_shot, ghost_shot]})
        return EXIT_PASS
    finally:
        if chrome is not None:
            chrome.close()


def parse_args(argv):
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", default="go_hive/go-hive/dist/hive-embed-linux-arm64")
    parser.add_argument("--chromium", default=CHROMIUM)
    parser.add_argument("--colony-id", default=DEFAULT_COLONY)
    parser.add_argument("--evidence-dir", default="go_hive/docs/e2e-assets/linux-ui")
    parser.add_argument("--json-out", default=None)
    parser.add_argument("--serve-log-out", default=None)
    parser.add_argument("--timeout", type=float, default=45.0)
    parser.add_argument("--health-timeout", type=float, default=90.0)
    parser.add_argument("--keep", action="store_true", help="keep the throwaway HIVE_HOME")
    parser.add_argument("--fault", choices=["none", "dom-empty"], default="none",
                        help="dom-empty loads about:blank to prove the gate can go red")
    parser.add_argument("--image", default=os.environ.get("HIVE_LINUX_QA_IMAGE", "hive-linux-qa:ui"))
    return parser.parse_args(argv)


def prepare_home(home: Path) -> str:
    home.mkdir(parents=True, exist_ok=False)
    (home / "secrets").mkdir()
    key = base64.urlsafe_b64encode(secrets.token_bytes(32)).decode("ascii")
    (home / "secrets" / "credential_key").write_text(key + "\n", encoding="utf-8")
    (home / "configuration.json").write_text(json.dumps({
        "llm": {"provider": "openai", "model": "gpt-4o-mini",
                "api_base": "http://127.0.0.1:9/v1", "api_key": "ui-e2e-no-llm"},
    }, indent=2) + "\n", encoding="utf-8")
    return key


def render_table(result: dict) -> str:
    env = result["environment"]
    lines = [
        "Hive Linux UI E2E gate — headless Chromium against a real hive-embed serve",
        f"  image      : {env.get('image')} ({env.get('arch')})",
        f"  binary     : {env.get('binary')} sha256={env.get('binary_sha256', '')[:16]}… "
        f"({env.get('binary_format') or 'not checked'}/{env.get('binary_arch') or '?'})",
        f"  base URL   : {env.get('base_url')}",
        f"  HIVE_HOME  : {env.get('hive_home')} "
        f"[{'kept' if env.get('home_kept') else 'deleted'}]",
        f"  chromium   : {env.get('chromium')}",
        f"  colony     : {env.get('colony_id')} (route {env.get('route_id')})",
        f"  started at : {result.get('generated_at')}", "",
    ]
    for step in result["steps"]:
        label = {"pass": "PASS", "fail": "FAIL"}.get(step["status"], "?")
        lines.append(f"[{step['id']}] {label}  {step['title']}")
        lines.append(f"      {step['actual']}")
        for item in step.get("checks", []):
            if not item["ok"]:
                lines.append(f"      failed: {item['name']} — {item['actual']}")
    lines.append("")
    lines.append("cleanup:")
    for item in result.get("cleanup", []):
        lines.append(f"  {'ok ' if item['ok'] else 'BAD'} {item['action']} — {item['detail']}")
    lines.append("")
    for finding in result.get("product_findings", []):
        lines.append(f"product finding [{finding.get('severity')}]: {finding.get('summary')}")
    for note in result.get("notes", []):
        lines.append(f"note: {note}")
    lines.append(f"result: {'PASS' if result['ok'] else 'FAIL'} (exit {result['exit_code']})")
    return "\n".join(lines)


def main(argv=None) -> int:
    args = parse_args(argv)
    repo_root = Path(__file__).resolve().parents[2]
    binary = Path(args.binary).expanduser()
    if not binary.is_absolute():
        binary = repo_root / binary
    evidence_dir = Path(args.evidence_dir).expanduser()
    if not evidence_dir.is_absolute():
        evidence_dir = repo_root / evidence_dir
    evidence_dir.mkdir(parents=True, exist_ok=True)
    home = Path(os.environ.get("HIVE_LINUX_E2E_SCRATCH", "/tmp")) / \
        f"ui-e2e-linux-{os.getpid()}-{secrets.token_hex(4)}"

    env = {
        "image": args.image,
        "arch": machine_arch(),
        "platform": platform.platform(),
        "python": platform.python_version(),
        "chromium": args.chromium,
        "binary": str(binary),
        "binary_format": "",
        "binary_arch": "",
        "binary_sha256": "",
        "hive_home": str(home),
        "home_kept": False,
        "colony_id": args.colony_id,
        "route_id": slug_to_route_id(args.colony_id),
        "base_url": "",
        "serve_port": None,
        "serve_pid": None,
        "health_status": None,
        "evidence_dir": str(evidence_dir),
    }
    rec = Recorder()
    proc = None
    log_handle = None
    exit_code = EXIT_ENVIRONMENT_ERROR
    started = time.monotonic()
    try:
        if not binary.is_file():
            rec.notes.append(f"no server binary at {binary}")
        else:
            fmt, arch = elf_arch(binary)
            env["binary_format"], env["binary_arch"] = fmt, arch
            env["binary_sha256"] = sha256_file(binary)
            if fmt != "ELF" or arch != machine_arch():
                rec.notes.append(
                    f"refusing to run {binary}: {fmt}/{arch} (container is {machine_arch()}); "
                    "build with CGO_ENABLED=0 GOOS=linux GOARCH=<container arch>")
            elif not Path(args.chromium).is_file():
                rec.notes.append(f"no chromium at {args.chromium}")
            else:
                key = prepare_home(home)
                log_handle = (home / "serve.log").open("w", encoding="utf-8")
                child_env = dict(os.environ)
                child_env.update({"HIVE_HOME": str(home), "HIVE_CREDENTIAL_KEY": key,
                                  "HIVE_BROWSER_BRIDGE": "0"})
                child_env.pop("HIVE_DESKTOP_TOKEN", None)
                port = free_port()
                env["base_url"] = f"http://127.0.0.1:{port}"
                env["serve_port"] = port
                proc = subprocess.Popen(
                    [str(binary), "serve", "--host", "127.0.0.1", "--port", str(port)],
                    env=child_env, cwd=str(home), stdout=log_handle, stderr=subprocess.STDOUT)
                env["serve_pid"] = proc.pid
                healthy, reason = wait_health(proc, port, args.health_timeout)
                if not healthy:
                    rec.notes.append(f"serve never became healthy: {reason}")
                else:
                    health = http_call("GET", env["base_url"] + "/api/health", timeout=5)
                    env["health_status"] = health.status
                    exit_code = run_gate(rec, args, env)
    except Exception as exc:  # noqa: BLE001
        rec.notes.append(f"environment error: {type(exc).__name__}: {exc}")
        exit_code = EXIT_ENVIRONMENT_ERROR
    finally:
        if log_handle is not None:
            log_handle.flush()
        # Tear down through the same API a user's delete button uses, while
        # serve is still alive. The temp home is removed afterwards, so this
        # also proves the delete path itself, not just rmtree.
        if env.get("base_url") and env.get("_colony_id"):
            colony_id = env["_colony_id"]
            deleted_colony = http_call(
                "DELETE", env["base_url"] + f"/api/colonies/{colony_id}?purge=1",
                timeout=args.timeout)
            rec.cleanup_action("delete the colony through the API",
                               deleted_colony.status in (200, 204),
                               f"HTTP {deleted_colony.status}")
            rec.cleanup_action("colony directory removed",
                               not (home / "colonies" / colony_id).exists(),
                               str(home / "colonies" / colony_id))
        for label, key in (("stop the colony session", "_child_session"),
                           ("stop the source DM session", "_dm_session")):
            session = env.get(key)
            if not session or not env.get("base_url"):
                continue
            stopped_session = http_call(
                "DELETE", env["base_url"] + f"/api/sessions/{session}",
                timeout=args.timeout)
            # Purging the colony already stops its live session, so a 404 here
            # means "already gone", not a cleanup failure.
            ok = stopped_session.status in (200, 204) or (
                key == "_child_session" and stopped_session.status == 404)
            detail = f"HTTP {stopped_session.status} session={session}"
            if stopped_session.status == 404 and key == "_child_session":
                detail += " (already stopped by the colony purge)"
            rec.cleanup_action(label, ok, detail)
        stopped, detail = stop_process(proc)
        if proc is not None:
            rec.cleanup_action("stop serve", stopped, detail)
        if args.serve_log_out and (home / "serve.log").is_file():
            try:
                shutil.copy(home / "serve.log", Path(args.serve_log_out).expanduser())
            except OSError as exc:
                rec.notes.append(f"could not save serve log: {exc}")
        env["home_kept"] = bool(args.keep)
        if not args.keep:
            if not home.exists():
                rec.cleanup_action("delete temp HIVE_HOME", True, f"{home} (never created)")
            else:
                try:
                    shutil.rmtree(home)
                    rec.cleanup_action("delete temp HIVE_HOME", True, str(home))
                except OSError as exc:
                    rec.cleanup_action("delete temp HIVE_HOME", False, f"{home}: {exc}")
    return finish(rec, env, args, exit_code, started)


def finish(rec: Recorder, env: dict, args, exit_code: int, started: float) -> int:
    failed = rec.failed()
    ok = exit_code == EXIT_PASS and not failed and rec.cleanup_ok()
    if exit_code == EXIT_PASS and (failed or not rec.cleanup_ok()):
        exit_code = EXIT_ASSERTION_FAILED
        ok = False
    result = {
        "schema": SCHEMA,
        "status": ("PASS_WITH_PRODUCT_FINDING" if ok and rec.findings
                   else ("PASS" if ok else "FAIL")),
        "ok": ok,
        "exit_code": exit_code,
        "generated_at": now_iso(),
        "duration_ms": round((time.monotonic() - started) * 1000, 1),
        "image": env.get("image"),
        "arch": env.get("arch"),
        "binary_sha256": env.get("binary_sha256"),
        "serve_port": env.get("serve_port"),
        "environment": env,
        "spawn_state": env.get("spawn_state"),
        "api": {s["id"]: {"http_status": s.get("http_status"),
                          "status": s["status"], "actual": s["actual"]}
                for s in rec.steps if s["id"] in ("b", "c", "c2", "d", "e", "f")},
        "dom_assertions": [{"step": s["id"], "name": c["name"], "ok": c["ok"],
                            "actual": c["actual"]}
                           for s in rec.steps if s["id"] in ("g", "h", "i", "j")
                           for c in s.get("checks", [])],
        "steps": rec.steps,
        "failed": failed,
        "cleanup": rec.cleanup,
        "cleanup_ok": rec.cleanup_ok(),
        "product_findings": rec.findings,
        "notes": rec.notes,
        "screenshots": [str(Path(args.evidence_dir) / name)
                        for name in ("linux-root.png", "linux-colony.png",
                                     "linux-colony-ghost.png")
                        if (Path(args.evidence_dir) / name).exists()],
    }
    payload = json.dumps(result, indent=2, ensure_ascii=False) + "\n"
    if args.json_out:
        out = Path(args.json_out).expanduser()
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(payload, encoding="utf-8")
    print(render_table(result))
    return exit_code


if __name__ == "__main__":
    sys.exit(main())
