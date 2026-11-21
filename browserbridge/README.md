# Hive Browser Bridge (Go)

Standalone Go port of the upstream Hive Browser Bridge
(`tools/src/gcu/browser/bridge.py`) and a first-class copy of the Chrome
extension (`extension/`). This module has **zero external dependencies** — only
the Go standard library — and carries its own `go.mod`
(`github.com/aden-hive/hive-go/browserbridge`), so it does not modify or depend on
the main `go_hive/go-hive` module.

Current scope is **L0/L1/L2/L3a**:

- RFC 6455 WebSocket server on `127.0.0.1:14829/bridge` (optional legacy pair
  `9229` WS + `9230` status), compatible with the shipped 1.7.4 extension's
  hello/ping/pong and `{id,type,...params}` / `{id,result|error}` protocol.
  The relay runs the same WS-layer keepalive as upstream's
  `websockets.serve(ping_interval=20, ping_timeout=20)`: an unresponsive peer
  is closed with `1011 keepalive ping timeout`.
- Multi-profile connection registry keyed by `profileLabel`, same-label
  displacement (`4002`), app-level heartbeat (5 s ping / 12 s timeout) on top
  of the WS-layer keepalive, request routing, structured errors, in-flight
  failure on disconnect.
- **All 22 active upstream MCP tools**: lifecycle, tabs, navigation, inspection,
  `browser_interact` (13 actions), `browser_select`, advanced tools and
  `browser_script`.
- L2 context/tab-group registry: `create_context`, `register_context`,
  `destroy_context`, `list_contexts`, group-id routing, starred default,
  adopt/release hand-over, `tab_event`/`tab_group_event`/`cdp_event`
  consumption, stale-context sweep, health blockers, telemetry JSONL, and the
  social rate-limit hook.
- Local HTTP agent API on `127.0.0.1:14830`, including the side panel's
  `/contexts`, `/tabs/{id}/health`, action-history, profile-star and
  adopt/release endpoints.
- L3a process entry point `cmd/browserbridge` (start/stop, signals, legacy
  toggle) and a real-Chrome verification path (`cmd/chromeloader` +
  `scripts/real_chrome_e2e.sh`).

This is **not** full behavior parity. See [`PARITY.md`](PARITY.md) for all 33
upstream MCP tools, the 55 `BeelineBridge` public methods, 34 runtime features,
and upstream test mapping. Anything not listed as implemented is explicitly
**not available**; there is no silent fallback. **未移植不等于可用** (not ported
does not mean usable).

## Layout

```
browserbridge/
├── cmd/browserbridge/       process entry point (flags, signals, ready line)
├── cmd/chromeloader/        verification helper: loads the unpacked extension
│                            into branded Chrome via the DevTools pipe
├── ws/                      RFC 6455 server (handshake, frames, ping/pong, close)
├── extension/               verbatim Chrome extension 1.7.4 + SOURCE.md + LICENSE
├── scripts/package_extension.sh
├── scripts/real_chrome_e2e.sh
├── internal/wstest/         minimal test client (not production API)
├── bridge.go                registry, hello, routing, heartbeat, lifecycle
├── connection.go            per-profile socket + pending requests
├── commands*.go             upstream-named tool handlers + bridge control commands
├── commands_common.go       shared CDP helpers (viewport, shadow query, waits, region capture)
├── commands_lifecycle.go    browser_setup / browser_status / browser_stop
├── commands_interact.go     browser_interact (13 actions) + browser_select
├── commands_read.go         browser_get_text / browser_html / browser_console / browser_shadow_query
├── commands_view.go         browser_resize / browser_upload / browser_dialog_respond
├── commands_script.go       browser_script (skill script child process)
├── cdp.go                   CDP attach/eval/element helpers
├── artifacts.go             browser_html spill directory + writer
├── contexts.go              profile→tab-group registry
├── context_ops.go           context create/register/destroy/list + status
├── handover.go              adopt/release hand-over
├── events_consume.go        cdp_event / tab_event / tab_group_event consumers
├── health.go                health.py rules, blocker cache, tab health
├── sweep.go                 stale-context + orphan-tab sweep
├── state.go                 tab ownership/activity/actions/health/dialog state
├── telemetry_jsonl.go       daily JSONL telemetry
├── ratelimit.go             SocialRateLimiter + navigate-time hook
├── identity.go              HIVE_BROWSER_SESSION / display-name resolution
├── httpapi.go               /health, /status, /profiles, /contexts, /command, …
├── events.go                EventSink seam for external observers
├── observability.go         Telemetry / HealthProvider / RateLimiter contracts
└── PARITY.md                exhaustive status matrix
```

## Build and test

```bash
cd go_hive/browserbridge
go build ./...
go test -race ./...
go vet ./...
```

Run the bridge:

```bash
go run ./cmd/browserbridge                  # ws 14829, legacy 9229/9230, http 14830
go run ./cmd/browserbridge -no-legacy       # skip the pre-migration 9229/9230 pair
go run ./cmd/browserbridge -help            # all flags
go run ./cmd/browserbridge -probe http://127.0.0.1:14830   # health check, exit code
```

`cmd/browserbridge` prints one JSON ready line on stdout and shuts down
gracefully on SIGINT/SIGTERM:

```json
{"ok":true,"ready":true,"version":"0.3.0-l3a","pid":1234,
 "ws":"127.0.0.1:14829","http":"127.0.0.1:14830","legacy":"127.0.0.1:9229",
 "legacy_status":"127.0.0.1:9230"}
```

Useful flags: `-artifacts` (spill directory for `browser_html`),
`-extension` (path reported by `browser_setup`), `-skills` +
`-script-command` (`browser_script`), `-telemetry-dir`, `-rate-limits`,
`-sweep`/`-no-sweep`, `-control` (14831), `-idle-grace`/`-idle-check`
(default 30 s / 10 s), `-parent-check` (desktop-parent poll, default 400 ms),
and `-no-idle-exit` (manual/dev runs that hold no control client).

The module is still usable as a library: `browserbridge.New(cfg)` →
`Start(ctx)` → `Shutdown(ctx)`.

Defaults:

| Listener | Address | Notes |
|---|---|---|
| Extension WebSocket | `127.0.0.1:14829` | Path `/bridge` |
| Agent HTTP | `127.0.0.1:14830` | Loopback-only middleware |
| Control RPC | `127.0.0.1:14831` | Path `/control`; allowlisted method calls (upstream `bridge_rpc.py`) |
| Legacy WebSocket | `127.0.0.1:9229` | Best-effort; disable with empty `LegacyAddr` |

## Supervised by `hive serve`

The normal way to run this binary is **not** by hand: `hive serve` starts it as a
detached child (`internal/cli/browser_supervisor.go` in the `go_hive/go-hive`
module — the main module deliberately does not import this one) and owns its
lifecycle. The process boundary is the contract, so keep these properties
stable:

| Property | Contract the supervisor relies on |
|---|---|
| Listeners | `-http <addr>` (default `127.0.0.1:14830`), `-control <addr>` (default `127.0.0.1:14831`), WS `14829`, legacy pair `9229` WS + `9230` status best-effort |
| Readiness | `GET <http>/health` answers `200` within the start budget (10 s); the JSON ready line on stdout is informational |
| Shutdown | `SIGTERM` stops the process promptly (the `signal.NotifyContext` path); `SIGKILL` after a 5 s grace |
| Parent watch | `_watch_parent` parity: with a live `HIVE_DESKTOP_PARENT_PID`, `-parent-check` (default **5 s**, upstream's `_PARENT_CHECK_INTERVAL_S`) polls it and two consecutive misses exit the bridge gracefully (exit 0) — measured 5–10 s, the same window upstream has. Keep the idle row below as the fallback for a bridge started without the hint |
| WS keepalive | `-ws-ping`/`-ws-pong` (default 20 s/20 s, upstream's websockets values): the relay pings every connection and closes a silent one with `1011 keepalive ping timeout` — measured telemetry in `docs/browser-bridge-surface-audit.html` §04.10 |
| Idle exit | Exit code **0** after `-idle-grace` (30 s) with zero live control clients *and* no live `HIVE_DESKTOP_PARENT_PID`. The supervisor treats exit 0 as intentional: it parks instead of restarting and starts a fresh child on the next `ensure()` (the keepalive's next reconnect) |
| Output | stdout/stderr are appended to `$HIVE_HOME/browserbridge.log` |
| Home | `HIVE_HOME` is passed through; `-artifacts` points at `$HIVE_HOME/tool-artifacts` |
| Restart | a non-zero exit restarts with 500 ms/1 s/2 s backoff up to 3 times; a busy port stops the loop instead of restarting |

The `hive-browser` CLI also starts this binary on demand (upstream
`ensure_bridge_host_running`): no bridge answering `/health`, no explicit
`--bridge`, and the CLI spawns a detached child, waits up to 8 s, and logs it to
`$HIVE_HOME/bridge_host.log`. Extra flags travel through
`HIVE_BROWSERBRIDGE_ARGS`, exactly as they do for the serve supervisor.

The supervisor itself still lives in the main module
(`internal/cli/browser_supervisor.go`); this binary owns the worker-side idle
watchdog. The serve-side keepalive
(`internal/cli/browser_control.go`) holds one control-RPC connection for the
runtime's lifetime, re-issues `client_hello(owner_pid)`, subscribes to notify
frames, and closes the socket on shutdown. Agents drive the bridge through
`hive-browser <verb> --json` (`go_hive/go-hive/cmd/hive-browser`) or the
control RPC, never by hand-crafting `/command` envelopes.

### Control RPC (14831)

`ws://127.0.0.1:14831/control` speaks the upstream `bridge_rpc.py` protocol:
`{"rpc_id","method","args","params"}` in, `{"rpc_id","ok","result"|"error"}`
out, plus unsolicited `{"type":"notify","profile","text",...}` frames for
subscribed clients. The method names are the upstream 46-name `RPC_METHODS`
allowlist (`RPCMethods()`), dispatched onto the same handlers as `/command`.
`client_hello(pid)` identifies the owning runtime process (the reaper closes
sockets whose owner died or that never identify within 30 s), and
`status_payload` / `control_rpc_clients` / `runtime_alive` expose the same
signal the idle watchdog reads.

End-to-end proof (real Chrome + this module's bundled 1.7.4 extension, with
`hive serve` supervising): `go_hive/scripts/e2e_browser_host.sh`, documented in
[`../docs/browser-host.md`](../docs/browser-host.md).

## Install the Chrome extension

The extension is copied unchanged from upstream
`tools/browser-extension/` (version 1.7.4, protocol version 6). Provenance and
sync instructions are in [`extension/SOURCE.md`](extension/SOURCE.md).

### Load unpacked (developer install)

1. Open `chrome://extensions`.
2. Enable **Developer mode** (top-right).
3. Click **Load unpacked**.
4. Select `go_hive/browserbridge/extension` (the folder containing
   `manifest.json`).
5. Start the Go bridge. The extension connects to
   `ws://127.0.0.1:14829/bridge` automatically (it rotates to `9229` while
   probing).

### Zip distribution

```bash
go_hive/browserbridge/scripts/package_extension.sh
# dist/hive-browser-bridge-1.7.4.zip, manifest.json at the zip root
go_hive/browserbridge/scripts/package_extension.sh --check
```

The script validates the manifest's referenced files and refuses to build a zip
with a nested wrapper directory. Unzip and load the resulting directory as
above, or distribute the zip to users who will unpack it before
**Load unpacked**.

## HTTP API

All routes bind to loopback; requests from non-loopback peers are rejected even
if the listener is misconfigured.

### `GET /health`

```json
{"ok":true,"status":"ok","connected":true,"connections":1,"uptime_ms":1234}
```

### `GET /status`

Connection/protocol/tab counts. Field names overlap the upstream `/status`
where possible, plus `tabs`/`tabs_total`. Tab counts are refreshed with a short
live `tab.list` per profile (750 ms budget); failures fall back to the cached
count. `bridge_version` is this binary's version, `runtime_version` stays
`null` unless an embedder sets one (upstream's production default), and
`bind_errors` names any optional listener (`legacy_ws`, `legacy_status`,
`control_rpc`) that failed to bind, so a port conflict is reported as exactly
that.

```json
{
  "bridge": "running",
  "port": 14829,
  "connected": true,
  "connections": [{
    "label": "work",
    "extension_id": "…",
    "version": "1.7.4",
    "protocol_version": 6,
    "connected_since_ms": 1200,
    "last_pong_age_ms": 12,
    "in_flight": 0,
    "is_default": true,
    "tabs": 3
  }],
  "uptime_ms": 4000,
  "bridge_version": "0.3.0-l3a",
  "runtime_version": null,
  "tabs": {"work": 3},
  "tabs_total": 3,
  "profiles": ["work"],
  "listening": {"ws": true, "status": true, "control_rpc": true, "legacy_ws": true, "legacy_status": true},
  "capabilities": ["browser_tabs", "..."],
  "system_blocker": null,
  "bind_errors": {}
}
```

`listening.legacy_status` is `true` whenever the legacy pair is bound: the
bridge serves the same status payload on `9229 + 1` (upstream's
`LEGACY_STATUS_PORT`), which is the endpoint later runtimes still probe as a
fallback. `-no-legacy` skips both legacy listeners; a primary listener that
already owns the legacy address skips the duplicate bind (upstream's
`port != LEGACY_BRIDGE_PORT` guard).

### `GET /profiles`

```json
{"ok":true,"profiles":[{"label":"work","extension_id":"…","version":"1.7.4","protocol_version":6,"connected_since_ms":1200,"is_default":true,"starred":true,"tabs":3}]}
```

### `GET /contexts`

Upstream row shape. `groupId` is `null` after a soft prune; `activeTab` falls
back to the most recently driven tab; `dormant` flips after 6 h of inactivity;
`blockers` are deduped by kind across the group's tabs.

```json
{"ok":true,"contexts":[{
  "profile":"queen-1","groupId":101,"activeTab":501,"name":"Queen One",
  "tabs":[{"id":501,"url":"https://example.com/","title":"…","groupId":101}],
  "last_active_ms":1730000000000,"dormant":false,"blockers":[],
  "status":"waiting"
}]}
```

### `GET /tabs/{id}/health`

Returns the same `Blocker` structs the side panel renders. `?force=1` re-runs
`tab.audit`; without it cached blockers are returned (a fresh empty snapshot
never clears blockers populated by a CDP-error path).

```json
{"ok":true,"tab_id":11,"url":"chrome://settings/","blockers":[
  {"kind":"privileged_scheme","severity":"block","title":"Privileged Chrome page",
   "detail":"This tab is on chrome, which Chrome forbids extensions from automating.",
   "fix":"Navigate to a regular http(s) page or open a new tab.",
   "context":{"url":"chrome://settings/","scheme":"chrome"}}
]}
```

### `GET /tabs/{id}/actions`

`limit` (default 8) and `since` (ms) return newest-first action-history rows.

```json
{"ok":true,"actions":[{"ts_ms":1730000000000,"verb":"navigate","target":"https://example.com/","ok":true}],"last_action_ts_ms":1730000000000}
```

### `POST /profiles/default`

Body `{"label":"work","extensionId":"…"}`. Stars the default connection; the
choice persists to `~/.hive/browser_default.json`.

### `POST /contexts/{profile}/adopt-tab`

Body `{"tabId":650}`. Moves a tab into the agent's group (lazily re-mints a
soft-pruned group). Returns `409` with `code=conflict` when another agent owns
the tab.

### `POST /tabs/{id}/release`

Removes the tab from its group; returns `ungroup_no_op` if Chrome reports the
tab still grouped.

### `POST /command`

Body: `{"profile"?: "<Chrome label>", "type": "<tool>", "params": {...}}`.
`profile` is a convenience for the routing label; `params.browser_profile`
takes precedence when both are present.

```bash
curl -s http://127.0.0.1:14830/command \
  -H 'Content-Type: application/json' \
  -d '{"type":"browser_tabs","params":{"browser_profile":"work"}}'
```

Success:

```json
{"ok":true,"result":{"ok":true,"total":3,"activeTabId":null,"tabs":[...]}}
```

Failure (HTTP status varies, see below):

```json
{"ok":false,"code":"not_connected","message":"no browser extension is connected — …","error":{"code":"not_connected","message":"…","retryable":true}}
```

| Code | HTTP |
|---|---|
| `bad_request`, `unknown_command`, `no_browser_profile`, `no_such_profile`, `unsupported_extension`, `ungroup_no_op` | 400 |
| `not_found` | 404 |
| `ambiguous_profile` (legacy), `invalid_state`, `conflict` | 409 |
| `rate_limited` | 429 |
| `not_connected` | 503 |
| `timeout` | 504 |
| `connection_lost`, `extension_error`, `cdp_error`, `create_failed` | 502/500 |

## Commands

The canonical names are the upstream MCP tool names. Parameter names follow
upstream too (`tab_id`, `browser_profile`, `wait_until`, `timeout_ms`,
`auto_snapshot_mode`, …). `tab_id` is required for tab-scoped commands; the
bridge tracks a context's active tab for `/contexts`, but the tool wrappers do
not yet default to it. Group-scoped commands accept `group_id` and route to the
Chrome profile that minted the group.

| Command | Parameters (subset) | Result |
|---|---|---|
| `browser_setup` | — | `{ok,connected,status,connected_profiles[]}`, or install guidance + `install_url` when disconnected |
| `browser_status` | `profile?` | `{ok,connected,profile,running,groupId?,activeTab?,browser_profile?,connected_profiles,tabs?}` |
| `browser_stop` | `profile?` | `{ok,status:"stopped"|"not_running",profile,closedTabs?}` |
| `browser_tabs` | `profile?` | `{ok,total,activeTabId,tabs,tabsHint?}` |
| `browser_open` | `url, background=false, browser_profile?, profile?, profile_display_name?` | `{ok,tabId,url,title,background,browser_profile}` |
| `browser_close` | `tab_id, profile?` | `{ok,closed}` |
| `browser_activate_tab` | `tab_id, profile?` | `{ok,tabId}` |
| `browser_navigate` | `url, tab_id, profile?, wait_until="load", browser_profile?` | `{ok,tabId,url,title,browser_profile}` |
| `browser_reload` | `tab_id, profile?` | `{ok,action:"reload",url}` |
| `browser_interact` | `action, tab_id?, selector?, coordinate?, start_selector?, start_coordinate?, text?, modifiers?, repeat?, scroll_direction?/scroll_amount?, region?, duration?, wait_for_selector?/wait_for_text?, intent?, full_page?, timeout_ms?, auto_snapshot_mode="simple", wait_after_ms?` | action result (+ `snapshot` unless `auto_snapshot_mode="off"`, image data for `screenshot`/`zoom`) |
| `browser_select` | `selector, values[], tab_id?` | `{ok,action:"select",selector,selected[]}`; invalid values add `options` |
| `browser_get_text` | `selector, tab_id?, timeout_ms=30000` | `{ok,selector,text}` |
| `browser_resize` | `width, height, tab_id?` | `{ok,action:"resize",width,height}` |
| `browser_upload` | `selector, file_paths[], trigger_selector?, tab_id?, timeout_ms=30000` | `{ok,action:"upload",selector,files,count,accepted}` |
| `browser_dialog_respond` | `action(accept\|dismiss), tab_id?, prompt_text?` | `{ok,tab_id,dialog}` or `{ok:false,error:"No native dialog is open on this tab"}` |
| `browser_console` | `tab_id?, level?` | upstream stub: `{ok,message,suggestion}` |
| `browser_html` | `tab_id?, selector?` | `{ok,tabId,selector,length,saved_to}` or `{ok,tabId,html:null}` |
| `browser_shadow_query` | `selector('>>>'), tab_id?` | `{ok,selector,tag,rect{fractions},cssWidth,cssHeight,note}` |
| `browser_script` | `skill, script, args?, timeout_s=60, tab_id?` | `{ok,result,duration_ms}` (skill script child process) |
| `browser_click` | `selector, tab_id, profile?, button="left", double_click=false, timeout_ms=5000, auto_snapshot_mode="simple"` | click result + `snapshot` |
| `browser_type` | `selector, text, tab_id, profile?, delay_ms=1, clear_first=true, timeout_ms=30000, use_insert_text=true, auto_snapshot_mode="simple"` | type result + `snapshot` |
| `browser_type_focused` | `text, tab_id, profile?, …` | type result + `snapshot` |
| `browser_press` | `key, selector?, tab_id, profile?, modifiers?` | `{ok,action:"press",...}` |
| `browser_scroll` | `direction="down", amount=500, selector?, tab_id, profile?, auto_snapshot_mode="simple"` | scroll result + `snapshot` |
| `browser_snapshot` | `tab_id, profile?, mode="default"` | `{ok,tabId,url,tree,nodes}` |
| `browser_screenshot` | `intent, tab_id, profile?, full_page=false, selector?` | `{ok,data,mimeType,format}` |
| `browser_evaluate` | `script, tab_id, profile?` | `{ok,action:"evaluate",result}` |

The L3a tools accept `tab_id=None`, in which case the profile's tracked active
tab is used (one registered context, or the context named by `profile`). The
earlier tool set still requires an explicit `tab_id`.

Legacy aliases from the first migration brief (`tabs.list`, `tab.open`,
`tab.close`, `tab.navigate`, `page.click`, `page.type`, `page.keypress`,
`page.scroll`, `page.snapshot`, `page.screenshot`, `page.eval`) are accepted as
compatibility shims but are **not** upstream names. Use the `browser_*` names.

`browser_click` / `browser_type` mirror the **legacy** `tools/interactions.py`
definitions. The active upstream tool surface uses `browser_interact`, which is
not ported yet (see `PARITY.md` §0).

### Bridge control commands

These names mirror upstream `BeelineBridge` methods / the RPC allowlist. They are
accepted by `POST /command` but are **not** upstream MCP tool names.

| Command | Parameters | Result |
|---|---|---|
| `create_context` | `agent_id`, `display_name?`, `browser_profile?` | `{groupId,tabId,windowId?,browser_profile}` |
| `register_context` | `profile`, `group_id`, `name?`, `browser_profile?` | `{ok}` |
| `destroy_context` | `group_id`, `browser_profile?` | `context.destroy` payload |
| `list_contexts` | — | context rows (see `GET /contexts`) |
| `adopt_tab` | `profile`, `tab_id`, `from_user?` | `{ok,tabId,groupId,already_owned?}` |
| `release_tab` | `tab_id`, `from_user?` | `{ok,tabId,…}` |
| `audit_tab` | `tab_id` | raw `tab.audit` snapshot |
| `tab_health` | `tab_id`, `force_audit?` | `{ok,tab_id,url,blockers}` |
| `get_tab_blockers` | `tab_id` | `blockers[]` |
| `record_action` | `tab_id`, `verb`, `target?`, `ok?` | `{ok}` |
| `get_tab_actions` | `tab_id`, `limit?`, `since_ms?` | `{actions,last_action_ts_ms}` |
| `get_pending_dialog` | `tab_id` | dialog or `null` |
| `list_pending_dialogs` | — | tracked dialogs |

### Wire command distribution

Every Go command is translated to the extension's actual message types; no new
extension protocol strings were invented.

| Go command | Extension messages |
|---|---|
| `browser_tabs` | `tab.list` |
| `browser_open` | `tab.create` → `tab.activate` (unless `background`) → `cdp.attach` → `cdp(Page.enable)` → `cdp(Page.navigate)` → `cdp(Runtime.evaluate)` for readyState/url/title |
| `browser_close` | `tab.close` |
| `browser_activate_tab` | `tab.activate` |
| `browser_navigate` | `cdp.attach` → `cdp(Page.enable)` → `cdp(Page.navigate)` → `cdp(Runtime.evaluate)` polling |
| `browser_reload` | `cdp.attach` → `cdp(Page.enable)` → `cdp(Page.reload)` → `cdp(Runtime.evaluate)` |
| `browser_click` | `cdp.attach` → `cdp(Runtime.evaluate)` rect → `cdp(Input.dispatchMouseEvent)` ×3; auto-snapshot adds `cdp(Accessibility.enable)` + `cdp(Accessibility.getFullAXTree)` + `cdp(Runtime.evaluate)` |
| `browser_type` | `cdp.attach` → `cdp(Runtime.evaluate)` focus/clear/rect → `cdp(Input.dispatchMouseEvent)` ×3 → `cdp(Input.insertText)`; auto-snapshot as above |
| `browser_type_focused` | `cdp.attach` → `cdp(Input.insertText)`; auto-snapshot as above |
| `browser_press` | `cdp.attach` → `cdp(Input.dispatchKeyEvent)` keyDown + keyUp |
| `browser_scroll` | `cdp.attach` → `cdp(Runtime.evaluate)` point → `cdp(Input.dispatchMouseEvent)` `mouseWheel` |
| `browser_snapshot` | `cdp.attach` → `cdp(Accessibility.enable)` → `cdp(Accessibility.getFullAXTree)` → `cdp(Runtime.evaluate)` url |
| `browser_screenshot` | `cdp.attach` → `cdp(Page.enable)` → `cdp(Page.captureScreenshot)`; full page adds `cdp(Page.getLayoutMetrics)` |
| `browser_evaluate` | `cdp.attach` → `cdp(Runtime.evaluate)` |
| `browser_interact` (clicks) | `cdp.attach` → `cdp(Runtime.evaluate)` rect → `cdp(Input.dispatchMouseEvent)` ×3 (+ auto-snapshot) |
| `browser_interact` (hover) | `cdp.attach` → `cdp(Runtime.evaluate)` rect → `cdp(Input.dispatchMouseEvent)` `mouseMoved` |
| `browser_interact` (type/key) | `cdp(Input.insertText)` / `cdp(Input.dispatchKeyEvent)` (+ optional focus click) |
| `browser_interact` (scroll) | `cdp(Input.dispatchMouseEvent)` `mouseWheel` |
| `browser_interact` (drag) | `cdp(Input.dispatchMouseEvent)` ×3 → `cdp(Runtime.evaluate)` HTML5 drag events |
| `browser_interact` (screenshot/zoom) | `cdp(Page.captureScreenshot)` (+ `clip` + `Page.getLayoutMetrics` for full page) |
| `browser_interact` (wait) | `cdp(Runtime.evaluate)` polling |
| `browser_interact` (key + coordinate) | mouse move then `cdp(Input.dispatchKeyEvent)` |
| `browser_select` | `cdp.attach` → `cdp(Runtime.evaluate)` (set `option.selected`, dispatch `change`) |
| `browser_get_text` | `cdp.attach` → `cdp(Runtime.evaluate)` polling `textContent` |
| `browser_resize` | `cdp.attach` → `cdp(Emulation.setDeviceMetricsOverride)` |
| `browser_upload` | `cdp(DOM.enable)` → optional `cdp(Page.setInterceptFileChooserDialog)` + trigger click → `cdp(DOM.getDocument)`/`cdp(DOM.querySelector)` → `cdp(DOM.resolveNode)` → `cdp(Runtime.callFunctionOn)` → `cdp(DOM.setFileInputFiles)` → read back |
| `browser_dialog_respond` | `cdp.attach` → `cdp(Page.handleJavaScriptDialog)` |
| `browser_html` | `cdp.attach` → `cdp(Runtime.evaluate)` `outerHTML` (spilled) |
| `browser_shadow_query` | `cdp.attach` → `cdp(Runtime.evaluate)` `_shadowQuery` + viewport probe |
| `browser_script` | no extension message: spawns the skill script (child process) with `HIVE_BRIDGE_*` env |

## Protocol compatibility

The Go server speaks the extension's protocol as implemented in
`extension/background.js` and `extension/offscreen.js`:

Client → server:

```json
{"type":"hello","version":"1.7.4","protocolVersion":6,"extensionId":"…","profileLabel":"work"}
{"type":"ping"}
{"type":"pong"}
{"type":"cdp_event", "...": "..."}
{"type":"tab_event", "...": "..."}
{"type":"tab_group_event", "...": "..."}
{"id":"1","result":{...}}
{"id":"1","error":"message"}
```

Server → client:

```json
{"type":"ping"}
{"type":"pong"}
{"id":"1","type":"tab.list"}
{"id":"2","type":"tab.create","url":"https://…"}
{"id":"3","type":"cdp","tabId":11,"method":"Runtime.evaluate","params":{...}}
```

The extension's manifest has no WebSocket host permission requirement (only
HTTP `127.0.0.1:14830` for its side panel); no manifest change is needed. The
Go bridge does **not** modify the extension.

## Capability matrix (short form)

Implemented and fake-extension-tested:

- WebSocket transport, `/bridge`, legacy 9229, hello registry (multi-profile),
  displacement 4002, two-way app heartbeat, ping/pong frames, id routing,
  structured errors, disconnect failure.
- All 22 active upstream tools: tabs list/open/close/activate, navigate/reload,
  `browser_interact` (left/right/middle/double/triple click, hover, type, key,
  scroll, drag, screenshot, zoom, wait), `browser_select`, `browser_get_text`,
  `browser_resize`, `browser_upload`, `browser_dialog_respond`,
  `browser_console`, `browser_html` (artifact spill), `browser_shadow_query`,
  `browser_script`, `browser_setup`/`browser_status`/`browser_stop`,
  snapshot (basic AX), screenshot (raw base64), evaluate.
- HTTP `/health`, `/status`, `/profiles`, `/command`; tab-id and group-id
  routing; starred default (`~/.hive/browser_default.json`).
- Context/tab-group registry: create/register/destroy/list, window affinity,
  persisted-group recycle, soft/hard prune.
- Handover: adopt/release with conflict refusal, lazy re-mint and
  `ungroup_no_op` verification; from-user notify hook.
- Event consumers: `tab_event` attribution, `tab_group_event` prune,
  `cdp_event` viewport/console/canary telemetry, pending dialogs, main-frame
  health invalidation.
- Stale 30 s sweep: registry reconciliation, forward orphan-group reaping,
  protocol-6 ungrouped-tab reaping.
- Health: `health.py` blocker rules, cached `tab.audit`, `/tabs/{id}/health`,
  `/contexts` blocker rollup, `/status` `system_blocker`.
- Telemetry JSONL (opt-in), per-tab action history, social rate-limit hook
  (opt-in), side-panel endpoints (`/contexts`, health, actions, star,
  adopt/release).
- `cmd/browserbridge` (flags, signals, ready line, `-probe`, `-version`).

Not implemented (not available; no fallback):

- Snapshot refs (`eN`), href map, DOM-snapshot fallback; screenshot
  annotation/resize/spill and the coordinate-action visual response.
- History back/forward, `tab.reveal`, `cdp_detach`, page highlight overlays,
  `get_attribute`.
- RPC control plane (`bridge_rpc.py`), `/tabs/{id}/reveal`, `/shutdown`.
- Exact `networkidle` semantics (500 ms heuristic).
- The six legacy `interactions.py` tools upstream never registers
  (`browser_click_coordinate`, `browser_hover`, `browser_hover_coordinate`,
  `browser_press_at`, legacy `browser_select`, `browser_drag`) — use
  `browser_interact`.

## Real Chrome verification

`scripts/real_chrome_e2e.sh` builds both binaries, serves a local fixture page,
starts the bridge, launches a real Chrome with a throwaway profile, loads the
bundled extension and drives the whole tool flow through `POST /command`,
writing every raw response plus a decoded screenshot into an evidence
directory. It exits non-zero if any step fails.

```bash
go_hive/browserbridge/scripts/real_chrome_e2e.sh /tmp/bridge-e2e
```

Chrome 137+ no longer honours `--load-extension` in branded builds, so
`cmd/chromeloader` starts Chrome with `--remote-debugging-pipe
--enable-unsafe-extension-debugging` and calls `Extensions.loadUnpacked` over
the DevTools pipe. Requirements: macOS/Linux, `curl`, `jq`, Chrome/Chromium,
`uv` or `python3`. See PARITY.md §10 for the recorded run (Chrome 158 + the
1.7.4 extension) and the exact observations.

## Tests

```bash
cd go_hive/browserbridge
go test -race ./...
go vet ./...
```

Coverage:

- `ws/ws_test.go`: handshake, text echo, fragmentation, ping/pong, close echo,
  HTTP/path rejection.
- `bridge_test.go`: hello/profile registry, synthetic label, heartbeat and
  ping-timeout, command round-trip, disconnect fails in-flight, same-label
  displacement, first-come default, tab routing across profiles, HTTP API.
- `commands_tools_test.go`: per-tool wire shapes for 14 handlers, auto-snapshot,
  CDP attach reuse, extension errors, eval exceptions.
- `commands_l3_test.go`: the L3a surface against the fake extension —
  `browser_interact` (click by selector/coordinate, type, key with modifiers and
  `repeat`, scroll, drag, zoom, wait, screenshot, param-misuse guards), select
  (success + invalid values), get_text, html spill, console stub, shadow query
  fractions, resize, upload (direct + triggered, change signal, missing file),
  dialog respond (accept/dismiss/no-dialog), lifecycle tools, tab defaulting,
  `browser_script` (run, path escape, unknown skill, timeout, non-JSON output),
  and the 22-active-tool registry check.
- `cmd/browserbridge/main_test.go`: flag defaults/overrides, listener binding,
  context-cancel shutdown, `/health` probe.
- `contexts_test.go`: context create/register/lifecycle, group routing, starred
  default, adopt/release (conflict, lazy re-mint, `ungroup_no_op`, notify),
  protocol gate, disconnect soft-prune.
- `events_health_test.go`: tab/group event attribution, `cdp_event` consumers,
  dialog tracking, health rules + `/tabs/{id}/health`, `/contexts` shape,
  side-panel adopt/release/star endpoints, `system_blocker` rollup.
- `sweep_telemetry_test.go`: stale sweep reconciliation, forward/ungrouped
  orphan reaping, rate-limit rejection, telemetry redaction, action history.
- `extension_test.go`: manifest JSON/version, referenced-file existence, package
  zip shape.

Real-Chrome coverage comes from `scripts/real_chrome_e2e.sh`, which is not part
of `go test` (it needs a Chrome install and takes ~30 s).

## L3 seams

`ContextStore`, `EventSink`, `HealthProvider`, `Telemetry`, `RateLimiter`,
`ArtifactDir`, `SkillDirs`, `ScriptCommand` and `ExtensionDir` are wired: the
context registry drives routing, the socket reader consumes all three event
types, `JSONLTelemetry` writes upstream-shaped rows, the social limiter runs at
navigate time, `browser_html` spills to `ArtifactDir`, and `browser_script`
resolves skills under `SkillDirs` and runs them with `ScriptCommand`.

Still open: the RPC control plane and allowlist, snapshot refs/hrefs, screenshot
annotation/spill, history navigation and `tab.reveal`, and the upstream
in-process `browser_script` semantics. Per-connection
pending/counter/attach state is isolated, so new code must not assume a single
socket.

## License

Apache-2.0. See [`LICENSE`](LICENSE); the copied extension carries the same
license in [`extension/LICENSE`](extension/LICENSE).
