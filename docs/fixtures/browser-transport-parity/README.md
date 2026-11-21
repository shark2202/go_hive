# browser-transport-parity fixtures

Raw output of the transport/lifecycle parity round (R16–R20, 2026-11-21,
branch `browser-transport-parity`, baseline `93b5a3a0`).

| File | What it is |
| --- | --- |
| `e2e-output.txt` | `bash go_hive/scripts/e2e_browser_transport.sh` — builds the real `browserbridge` + real `hive-browser`, then runs `scripts/e2e_browser_transport.py` (23 assertions, all PASS). |

What the probe covers, all against real binaries on ephemeral ports:

1. **WS-layer keepalive (R16)** — `-ws-ping 200ms -ws-pong 200ms`: a raw WS
   peer that never pongs is closed with `1011 "keepalive ping timeout"` (the
   Python `websockets` code/reason); a ponging peer answers six heartbeat
   cycles and an app-level ping.
2. **legacy pair (R17)** — `GET 9230/status` serves the primary payload
   (`listening.legacy_status:true`), `9229` accepts a hello (label visible in
   `/status.connections`), and a primary listener that owns the legacy address
   skips the duplicate bind with no `bind_errors` self-collision.
3. **CLI auto-ensure (R18)** — `hive-browser status --json` with nothing
   listening and no `--bridge` spawns a detached bridge and answers through it
   in 0.62 s; with a fake extension connected the same command exits 0.
4. **exit codes (R19)** — local precheck 6; bridge `bad_request` text decides
   (`upload` → `File not found` → 4; unknown command → HTTP 400
   `unknown_command`).
5. **parent death (R20)** — default `-parent-check` (5 s × 2, idle watchdog
   disabled): the bridge exits 0 after 10.04–10.06 s, upstream's 5–10 s
   window (the old 400 ms acceleration measured 0.78–0.87 s; baseline without
   any watch was 40 s).

Reproduce: `bash go_hive/scripts/e2e_browser_transport.sh`. Ports are chosen
from free ephemeral sockets; the developer's resident bridge
(14829–14831) is never touched and everything is torn down on exit.
