# Browser Bridge Port Parity

Status legend:

| Status | Meaning |
|---|---|
| 已移植 | Go implementation exists and is covered by fake-extension protocol tests in this module. Listed gaps in the notes are still gaps. |
| 未移植 | No Go implementation, or only an interface/registry seam exists. |
| 未验证 | Implemented, but the required real-Chrome / real-upstream exercise could not be performed in this lane (no extension environment). |

This document does **not** claim full parity. L3a added the process entry point
(`cmd/browserbridge`), the remaining 13 active MCP tools (all 22 are now
handlers), real-Chrome verification, and artifact spill for `browser_html`.
The control-plane pass adds the 14831 RPC server + allowlist, `client_hello`
identity, server-pushed notify frames, the idle-exit watchdog with the
serve-side keepalive, and the last four bridge methods plus two HTTP routes.
Rows marked 未移植 are not available through any fallback, alias, or hidden
compatibility shim unless the row says otherwise.

## 0. Important upstream shape finding

`tools/*.py` contains **33** `@mcp.tool` definitions, but only **22** are
registered by `tools/__init__.py`:

- Active: lifecycle (3), tabs (4), navigation (2), inspection (5),
  interact (2), advanced (5), script (1) = 22.
- Legacy/unregistered: all 11 definitions in
  `tools/interactions.py` (`browser_click`, `browser_type`,
  `browser_click_coordinate`, `browser_hover`, `browser_hover_coordinate`,
  `browser_press`, `browser_press_at`, `browser_select`, `browser_scroll`,
  `browser_drag`, `browser_type_focused`). That module is not imported by
  `register_tools()`.

The 33-row table below covers every definition as requested. The `surface`
column records active vs legacy. **All 22 active tools are now implemented**
(`browser_interact` is the active click/type/key/scroll/screenshot surface; the
Go handlers named `browser_click` / `browser_type` keep mirroring the legacy
definitions for compatibility and are not the active path). The six remaining
未移植 rows are legacy-only tools upstream never registers.

## 1. MCP tools (33 definitions)

| # | Tool | Signature (upstream) | Return shape (upstream) | Surface | Status | Notes |
|---|---|---|---|---|---|---|
| 1 | `browser_setup` | `()` | `{ok, connected, status, connected_profiles[]}`; disconnected adds `install_url, instructions{step_1..4}, note` | active | 已移植 | `cmdBrowserSetup`; connected path lists every live profile. Disconnected path returns the store URL plus a Load-unpacked hint and `local_extension_path` when `Config.ExtensionDir` is set. |
| 2 | `browser_status` | `(profile=None)` | `{ok, connected, profile, running, groupId?, activeTab?, browser_profile?, connected_profiles, tabs?, error?}` | active | 已移植 | `cmdBrowserStatus`; reports `ok:false, connected:false` when the extension is absent (state report, not a transport error), `running:false` when no context is registered for the profile. |
| 3 | `browser_stop` | `(profile=None)` | `close_profile_context()` result | active | 已移植 | `cmdBrowserStop` → `CloseProfileContext` (destroy the group, drop the registry entry). A failed destroy reports `status:deferred` and keeps the registry row so a retry can still reach Chrome (upstream dead-letters instead). |
| 4 | `browser_tabs` | `(profile=None)` | `{ok, tabsHint?, total, activeTabId, tabs[{id,url,title,groupId}]}` | active | 已移植 | `browser_tabs` returns extension tabs; `group_id` now routes to the owning Chrome profile. `activeTabId` is still null (agent-profile vs browser-label parameter mismatch remains). |
| 5 | `browser_open` | `(url, background=False, profile=None, profile_display_name=None, browser_profile=None)` | `{ok, tabId, url, title, background, browser_profile, groupTabCount?, groupTabs?, tabsHint?}` | active | 已移植 | Creates+navigates+optionally activates; `group_id` routes to the minting connection. Group summary/context fields still absent. |
| 6 | `browser_close` | `(tab_id=None, profile=None)` | `{ok, closed}` | active | 已移植 | `tab_id` is required because active-tab context is not ported. |
| 7 | `browser_activate_tab` | `(tab_id, profile=None)` | `{ok, tabId}` | active | 已移植 | Uses extension `tab.activate`. |
| 8 | `browser_navigate` | `(url, tab_id=None, profile=None, wait_until="load", profile_display_name=None, browser_profile=None)` | `{ok, tabId, url, title, browser_profile, groupTabCount?, groupTabs?, tabsHint?}` | active | 已移植 | `tab_id` required; social profile-view rate-limit hook is enforced when enabled; group summary absent; `networkidle` is a 500 ms heuristic. |
| 9 | `browser_reload` | `(tab_id=None, profile=None)` | `{ok, action:"reload", url}` | active | 已移植 | `tab_id` required; dialog-blocked reload surface absent. |
| 10 | `browser_screenshot` | `(intent, tab_id=None, profile=None, full_page=False, selector=None, annotate=True)` | `list[TextContent(metadata JSON), ImageContent(image/jpeg)]`; spill variant adds `saved_to` | active | 已移植 | `format=jpeg&quality=75&max_width=800` asks Chrome for the 800px JPEG via `clip.scale` (browser-side resample; Skia vs PIL is an acknowledged, quantified approximation — see `docs/fixtures/browser-return-contract/README.md`); geometry fields (`imageWidth/physicalScale/capturedWidth/capturedHeight`) match upstream; the CLI spills `saved_to` + `_image`. Annotation draws the tracked last-interaction highlight as a page overlay (same geometry/label formula; fonts/alpha differ from PIL). |
| 11 | `browser_shadow_query` | `(selector, tab_id=None, profile=None)` | `{ok, selector, tag, rect{x,y,w,h,cx,cy}, cssWidth, cssHeight, note}` | active | 已移植 | `>>>` piercing ported from `_SHADOW_QUERY_JS`; rect returned as viewport fractions plus `cssWidth`/`cssHeight`. Verified against real Chrome (`#host >>> .item` → `SPAN`). |
| 12 | `browser_snapshot` | `(tab_id=None, profile=None, mode="default")` | `{ok, tabId, url, length, saved_to}` (tree spilled to file; errors pass through) | active | 已移植 | Bridge renders `[ref=eN]` + `[href="…"]` (+ DOM-orphan link section), `_clean_inline_text_boxes`, DOM fallback (>50000 nodes / visible<10 && >500) and the pending-dialog gate; the CLI spills the tree and returns `{ok,tabId,url,length,saved_to}` so `page snapshot --head` reads real content. Sample tree is byte-identical to upstream (112 chars). |
| 13 | `browser_console` | `(tab_id=None, profile=None, level=None)` | `{ok,message,suggestion}` stub upstream | active | 已移植 | Upstream is a stub; the Go handler returns the same three fields verbatim. Console rows still land in the telemetry JSONL via the `cdp_event` consumer. |
| 14 | `browser_html` | `(tab_id=None, profile=None, selector=None)` | `{ok, tabId, selector, length, saved_to}` or `{ok,tabId,html,selector}` | active | 已移植 | Spills `outerHTML` to `<ArtifactDir>/browser_html_<ms>_tab<id>.html`; a selector that matches nothing returns `html:null`. |
| 15 | `browser_interact` | `(action, tab_id=None, profile=None, selector=None, coordinate=None, start_selector=None, start_coordinate=None, text=None, clear_first=True, use_insert_text=True, modifiers=None, repeat=1, scroll_direction="down", scroll_amount=500, intent=None, full_page=False, annotate=True, region=None, duration=None, wait_for_selector=None, wait_for_text=None, timeout_ms=None, auto_snapshot_mode="simple", wait_after_ms=0)` | `list[TextContent, ImageContent?]` | active | 已移植 | All 13 actions. Parameter-relevance guard, fraction coordinates (`0..1`), `key` "+" modifier parsing, `repeat`, HTML5 drag synthesis, zoom `crop_box`, auto-snapshot (`default/simple/interactive/off`). Returns Go-native JSON instead of MCP content blocks; coordinate click/hover/key attach the annotated screenshot as `data`+`mimeType` (R15), which the CLI collapses to `saved_to`+`_image` like upstream `_build_visual_response` / `_blocks_to_cli_dict`. |
| 16 | `browser_select` | `(selector, values, tab_id=None, profile=None)` | `{ok, action:"select", selector, selected[]}`; errors add `options` | active | 已移植 | Sets `selected` per option, dispatches `change`, reports `selected`/`options`; invalid values produce `ok:false` with the option list. |
| 17 | `browser_click` | `(selector, tab_id=None, profile=None, button="left", double_click=False, timeout_ms=5000, auto_snapshot_mode="simple")` | click result `{ok,action:"click",selector,x,y,method,focused_element?}` plus `snapshot`/`snapshot_error` | legacy | 已移植 | Pointer click + auto-snapshot; no highlight overlay, focused-element descriptor, or JS fallback. |
| 18 | `browser_click_coordinate` | `(x, y, tab_id=None, profile=None, button="left")` | `list[TextContent(click result), ImageContent]` | legacy | 未移植 | Not registered upstream; the active path is `browser_interact` with `coordinate`. |
| 19 | `browser_type` | `(selector, text, tab_id=None, profile=None, delay_ms=1, clear_first=True, timeout_ms=30000, use_insert_text=True, auto_snapshot_mode="simple")` | type result `{ok,action:"type",selector,length}` plus `snapshot`/`snapshot_error` | legacy | 已移植 | `Input.insertText` default; per-keystroke path when `use_insert_text=false`. |
| 20 | `browser_type_focused` | `(text, tab_id=None, profile=None, delay_ms=1, clear_first=True, use_insert_text=True, auto_snapshot_mode="simple")` | type result with `selector=null` plus snapshot | active | 已移植 | Types into the focused element; no pointer click. |
| 21 | `browser_press` | `(key, selector=None, tab_id=None, profile=None, modifiers=None)` | press result `{ok,action:"press",...}` | legacy | 已移植 | Key map for Enter/Tab/Escape/arrows/etc.; modifier bitmask. |
| 22 | `browser_hover` | `(selector, tab_id=None, profile=None, timeout_ms=30000)` | `{ok,action:"hover",...}` | legacy | 未移植 | Not registered upstream; `browser_interact` `hover` covers it. |
| 23 | `browser_hover_coordinate` | `(x, y, tab_id=None, profile=None)` | `list[TextContent, ImageContent]` | legacy | 未移植 | Not registered upstream; `browser_interact` `hover` + `coordinate` covers it. |
| 24 | `browser_press_at` | `(x, y, key, tab_id=None, profile=None)` | `list[TextContent, ImageContent]` | legacy | 未移植 | Not registered upstream; `browser_interact` `key` + `coordinate` covers it. |
| 25 | `browser_select` | `(selector, values, tab_id=None, profile=None)` | `{ok,action:"select",selector,selected[]}` | legacy (duplicate name) | 未移植 | The legacy duplicate is dead code upstream (row 16 is the active tool, and it is ported). |
| 26 | `browser_scroll` | `(direction="down", amount=500, selector=None, tab_id=None, profile=None, auto_snapshot_mode="simple")` | `{ok,action:"scroll",direction,amount,method,container,steps}` plus snapshot | legacy | 已移植 | Uses CDP mouseWheel; upstream uses chunked JS scrollBy with container discovery. |
| 27 | `browser_drag` | `(start_selector, end_selector, tab_id=None, profile=None, timeout_ms=30000)` | `{ok,action:"drag",...}` | legacy | 未移植 | Not registered upstream; `browser_interact` `drag` covers it. |
| 28 | `browser_evaluate` | `(script, tab_id=None, profile=None)` | `{ok, action:"evaluate", result}` plus `blockers[]` on failure | active | 已移植 | No toast, no `JSON.stringify` un-nesting; health blockers are cached by CDP errors but not attached to the tool envelope. |
| 29 | `browser_get_text` | `(selector, tab_id=None, profile=None, timeout_ms=30000)` | `{ok, selector, text}` | active | 已移植 | Polls `textContent` until it exists (30 s default); missing element → `ok:false` with `Element not found`. |
| 30 | `browser_resize` | `(width, height, tab_id=None, profile=None)` | resize result | active | 已移植 | `Emulation.setDeviceMetricsOverride` (`deviceScaleFactor:0`, `mobile:false`); verified `window.innerWidth == 1280` in real Chrome. |
| 31 | `browser_upload` | `(selector, file_paths, trigger_selector=None, tab_id=None, profile=None, timeout_ms=30000)` | `{ok,action:"upload",selector,files,count,accepted}` | active | 已移植 | Direct + triggered modes; picker interception via `Page.setInterceptFileChooserDialog`; one-shot `change` listener gives the `accepted.change_fired` signal. Verified in real Chrome. |
| 32 | `browser_dialog_respond` | `(action: accept|dismiss, tab_id=None, profile=None, prompt_text=None)` | dialog/navigation result | active | 已移植 | `Page.handleJavaScriptDialog` sent directly (never through a guarded path); "no dialog showing" becomes `ok:false, error:"No native dialog is open on this tab"`. Verified accept in real Chrome. |
| 33 | `browser_script` | `(skill, script, args=None, timeout_s=60, tab_id=None, profile=None, profile_display_name=None, browser_profile=None)` | script result dict | active | 已移植 | Resolution + path-escape rules ported. **Divergence:** the script runs out-of-process (`Config.ScriptCommand`, default `uv run python`) with a JSON stdin envelope and `HIVE_BRIDGE_HTTP` instead of in-process `ctx.bridge`; result is `{ok,result,duration_ms}` from the script's stdout JSON. |

## 2. `bridge.py` public methods (55)

`BeelineBridge` exposes 55 public methods (the brief listed a subset; all are
included here). Signatures are copied from the upstream AST.

| # | Method | Signature | Status | Notes |
|---|---|---|---|---|
| 1 | `is_connected` | `()` | 已移植 | `Bridge.IsConnected()` exists. |
| 2 | `connection_help` | `()` | 已移植 | Condensed actionable text (`connectionHelp()`) returned by `browser_status`/`browser_stop`/`browser_setup` when no extension is connected. Upstream's port-holder probe (`ss -ltnp`) is not ported (macOS has no `ss`). |
| 3 | `resolve_connection` | `(browser_profile: str \| None)` | 已移植 | Explicit label, tab ownership, starred default (`browser_default.json`), then first-come; stale pinned label falls back to the sole connection. |
| 4 | `has_public_listeners` | `()` | 未移植 | Deliberately local-only; no public-listener concept. |
| 5 | `start` | `(port=BRIDGE_PORT)` | 已移植 | Binds WS/HTTP; legacy 9229 best-effort. |
| 6 | `stop` | `()` | 已移植 | Shuts down listeners and fails pending. |
| 7 | `connected_profiles` | `()` | 已移植 | `Bridge.Profiles()` / `GET /profiles`. |
| 8 | `status_payload` | `()` | 已移植 | `Bridge.Status()` subset with upstream-overlapping fields. |
| 9 | `create_context` | `(agent_id, display_name=None, browser_profile="default")` | 已移植 | `Bridge.CreateContext`: resolves the connection first, honors window affinity, recycles a persisted saved-group chip (protocol ≥5), registers the seed tab. |
| 10 | `register_context` | `(profile, group_id, name=None, browser_profile="default")` | 已移植 | `Bridge.RegisterContext` upserts without requiring a live connection (rehydration). |
| 11 | `destroy_context` | `(group_id, browser_profile=None)` | 已移植 | Routes to the minting connection, remembers `persistedGroup`, drops tab attribution, hard-prunes the registry. |
| 12 | `list_contexts` | `()` | 已移植 | `Bridge.ListContexts` returns the upstream row shape (`profile/groupId/activeTab/name/tabs/last_active_ms/dormant/blockers/status`). |
| 13 | `record_action` | `(tab_id, verb, *, target=None, ok=True, ts_ms=None)` | 已移植 | Bounded 200-entry per-tab ring buffer; tool calls record verbs after `Execute`. |
| 14 | `get_tab_actions` | `(tab_id, *, limit=8, since_ms=None)` | 已移植 | Newest-first, limit clamp, `last_action_ts_ms`; exposed as `GET /tabs/{id}/actions`. |
| 15 | `create_tab` | `(url="about:blank", group_id=None)` | 已移植 | Extension `tab.create`; group id routes to the minting connection and is forwarded. |
| 16 | `close_tab` | `(tab_id)` | 已移植 | Extension `tab.close`. |
| 17 | `adopt_tab` | `(profile, tab_id)` | 已移植 | Conflict refusal, already-owned fast path, lazy group re-create, seed-tab close, from-user notify hook. Protocol gate ≥4. |
| 18 | `release_tab` | `(tab_id)` | 已移植 | Protocol gate ≥4, post-call group verification (`ungroup_no_op`), attribution clear, from-user notify. |
| 19 | `list_tabs` | `(group_id=None)` | 已移植 | Extension `tab.list`; group filter is also used to route to the owning connection. |
| 20 | `activate_tab` | `(tab_id)` | 已移植 | Extension `tab.activate`. |
| 21 | `reveal_tab` | `(tab_id)` | 已移植 | `Bridge.RevealTab` sends `tab.reveal` (activate + raise window) and degrades to `tab.activate` with `fallback:"activate"` when the extension answers "Unknown command". Exposed as `POST /tabs/{id}/reveal`. |
| 22 | `cdp_attach` | `(tab_id)` | 已移植 | Lazy attach before every CDP command; first attach runs a best-effort `tab.audit`. |
| 23 | `cdp_detach` | `(tab_id)` | 已移植 | `Bridge.CDPDetach` sends `cdp.detach`, drops the local attach flag and the health cache for that attach session. Reachable through the control RPC allowlist. |
| 24 | `audit_tab` | `(tab_id)` | 已移植 | `tab.audit` snapshot returned raw; also cached for health classification. |
| 25 | `get_tab_blockers` | `(tab_id)` | 已移植 | Cached `health.py` blocker list, deduped by kind. |
| 26 | `tab_health` | `(tab_id)` | 已移植 | Fresh audit when forced/uncached; empty fresh snapshots never clear error-path blockers. Exposed as `GET /tabs/{id}/health`. |
| 27 | `navigate` | `(tab_id, url, wait_until="load", timeout_ms=30000)` | 已移植 | `Page.navigate` + readyState poll; no dialogs, networkidle heuristic. |
| 28 | `go_back` | `(tab_id)` | 已移植 | `Bridge.GoBackHistory`: `history.back()` via `Runtime.evaluate`, 300 ms settle, pending-dialog surfaced instead of a stale URL. Reachable through the control RPC allowlist. |
| 29 | `go_forward` | `(tab_id)` | 已移植 | `history.forward()` twin of `go_back` with the same dialog handling. Reachable through the control RPC allowlist. |
| 30 | `reload` | `(tab_id)` | 已移植 | `Page.reload` + URL readback; no dialog race handling. |
| 31 | `handle_javascript_dialog` | `(tab_id, accept, prompt_text=None)` | 已移植 | `browser_dialog_respond` sends `Page.handleJavaScriptDialog` straight over the connection (short 5 s budget, no attach retry), clears the tracked dialog and translates Chrome's "no dialog showing" into a clean `ok:false`. |
| 32 | `get_pending_dialog` | `(tab_id)` | 已移植 | Per-tab state from `Page.javascriptDialogOpening/Closed`. |
| 33 | `list_pending_dialogs` | `()` | 已移植 | All tracked dialogs keyed by tab id. |
| 34 | `click` | `(tab_id, selector, button="left", click_count=1, timeout_ms=5000)` | 已移植 | JS rect + pointer events; no highlight/focused descriptor/fallback. |
| 35 | `click_coordinate` | `(tab_id, x, y, button="left", click_count=1)` | 已移植 | Internal coordinate path in `page.click`; `browser_interact`'s coordinate click attaches the annotated screenshot (`data`+`mimeType`, R15). |
| 36 | `type_text` | `(tab_id, selector, text, clear_first=True, delay_ms=1, timeout_ms=30000, use_insert_text=True)` | 已移植 | Pointer focus + `Input.insertText`/per-char. |
| 37 | `press_key` | `(tab_id, key, selector=None, modifiers=None)` | 已移植 | Modifier mask + special-key map. |
| 38 | `shadow_query` | `(tab_id, selector)` | 已移植 | `_shadowQuery` JS ported; `browser_shadow_query` converts the CSS-px rect to viewport fractions. |
| 39 | `hover` | `(tab_id, selector, timeout_ms=30000)` | 已移植 | Shadow-piercing selector → `scrollIntoView` → `Input.dispatchMouseEvent mouseMoved`; rect recorded as the last-interaction highlight (visible page overlay still absent). |
| 40 | `hover_coordinate` | `(tab_id, x, y)` | 已移植 | `mouseMoved` with `buttons:0` at CSS px. |
| 41 | `press_key_at` | `(tab_id, x, y, key)` | 已移植 | Move-then-key so native hit-testing routes the event. |
| 42 | `highlight_rect` | `(tab_id, x, y, w, h, label="", color=None, border_style="solid")` | 未移植 | The last-interaction *rect* is recorded for `browser_screenshot` annotation; the visible pulsing page overlay is still absent. |
| 43 | `highlight_point` | `(tab_id, x, y, label="")` | 未移植 | Same: point recorded for screenshot annotation; no page overlay. |
| 44 | `clear_highlight` | `(tab_id)` | 未移植 | Highlight state is dropped when the tab closes / navigates; no page overlay to remove. |
| 45 | `scroll` | `(tab_id, direction="down", amount=500, selector=None)` | 已移植 | CDP wheel; upstream container heuristic/chunking absent. |
| 46 | `select_option` | `(tab_id, selector, values)` | 已移植 | `selectOptions`: sets `selected`, dispatches `change`, reports `selected`/`options` and invalid values. |
| 47 | `evaluate` | `(tab_id, script)` | 已移植 | Runtime.evaluate wrapper; no blocker/unnest extras. |
| 48 | `snapshot` | `(tab_id, timeout_s=30.0, mode="default")` | 已移植 | refs (`[ref=eN]`), href map (`[href="…"]` + DOM-orphan section), DOM fallback (`_dom_snapshot`) and `_clean_inline_text_boxes`; pending-dialog gate returns `{ok:false, error, pending_dialog}` like the tool layer. |
| 49 | `get_text` | `(tab_id, selector, timeout_ms=30000)` | 已移植 | `browser_get_text` polls `textContent`. |
| 50 | `get_attribute` | `(tab_id, selector, attribute, timeout_ms=30000)` | 已移植 | `Bridge.GetAttribute` polls `getAttribute` until it exists (30 s default). No active upstream tool wrapper (dead method upstream too), reachable through the control RPC allowlist. |
| 51 | `screenshot` | `(tab_id, full_page=False, selector=None, timeout_s=30.0, selector_timeout_ms=5000)` | 已移植 | Base64 PNG/JPEG capture plus the tool-layer shape when asked (`format=jpeg&quality=75&max_width=800` → browser-native 800px JPEG; `cssWidth/cssHeight/dpr/capturedWidth/capturedHeight/physicalScale/annotated/clip`). Resampling/encoding are browser-native (approximation vs PIL, quantified in the fixtures README). |
| 52 | `screenshot_region` | `(tab_id, x0, y0, x1, y1, scale=2.0, timeout_s=30.0)` | 已移植 | `captureRegion` implements the clip maths for `browser_interact` `zoom` (JPEG, `crop_box`, `captureScale`); no resize to the 800 px output width. |
| 53 | `wait_for_selector` | `(tab_id, selector, timeout_ms=5000)` | 已移植 | Polls `querySelector(...) !== null` with adaptive 50/200 ms sleeps. |
| 54 | `wait_for_text` | `(tab_id, text, timeout_ms=5000)` | 已移植 | Polls `document.body.innerText.includes(text)`. |
| 55 | `resize` | `(tab_id, width, height)` | 已移植 | `Emulation.setDeviceMetricsOverride`. |

## 3. Runtime features

| # | Feature | Status | Notes |
|---|---|---|---|
| 1 | RFC 6455 server handshake/frames/fragmentation | 已移植 | Standard-library implementation in `ws/`; client masking required, 16 MiB cap. |
| 2 | `/bridge` endpoint on 14829 | 已移植 | Wrong paths get HTTP 404. |
| 3 | Legacy 9229 listener | 已移植 | Default enabled, best-effort bind; disable with empty `-legacy`. |
| 4 | `hello` parse (version/extensionId/protocolVersion/profileLabel) | 已移植 | Synthetic `profile-<8>` fallback for protocol ≥5 without a label. |
| 5 | Multi-profile connection registry | 已移植 | `map[label]*Connection`, per-connection pending/counter/attach cache. |
| 6 | Same-label reconnect displacement (close 4002) | 已移植 | Old pending fails with `connection_lost`. |
| 7 | App-level two-way ping/pong (5 s / 12 s) | 已移植 | Bridge pings; extension pings are answered; timeout closes 4001. |
| 8 | WebSocket ping/pong control frames | 已移植 | Server answers pings; does not run its own WS-level ping ticker. |
| 9 | Request id → result/error routing | 已移植 | `{id,type,...params}` out; `{id,result|error}` back. |
| 10 | Structured error codes | 已移植 | `bad_request`, `unknown_command`, `not_connected`, `no_browser_profile`, `ambiguous_profile` (legacy constant), `timeout`, `connection_lost`, `extension_error`, `cdp_error`, `not_found`, `invalid_state`, `conflict`, `no_such_profile`, `unsupported_extension`, `create_failed`, `ungroup_no_op`, `rate_limited`. |
| 11 | Disconnect fails all in-flight requests | 已移植 | Per-connection scope; other profiles untouched. |
| 12 | Tab-id → owning profile routing | 已移植 | Learned from `tab.list`/`tab.create`/`tab_event`; removed on close/removed. |
| 13 | Group-id → owning profile routing | 已移植 | Registry lookup routes group-scoped `tab.list`/`tab.create`; missing owner is never silently sent to another profile. |
| 14 | Starred default / sole-connection default policy | 已移植 | Starred label from `browser_default.json`, then earliest-connected; stale pinned label falls back only when a single connection is live. |
| 15 | Context create/destroy/list tab groups | 已移植 | Full registry lifecycle incl. seed tab, window affinity, persisted-group recycle, hard/soft prune. |
| 16 | Handover (`adopt_tab`/`release_tab`) | 已移植 | Conflict refusal, lazy re-mint, `ungroup_no_op` verification, protocol ≥4 gate. |
| 17 | `tab_event` lifecycle registry updates | 已移植 | Ownership from group/opener, active-tab tracking, removed eviction, counts. |
| 18 | `tab_group_event` pruning | 已移植 | `removed` soft-prunes the registry (identity kept for hand-over). |
| 19 | `cdp_event` consumers (viewport probe, console, dialogs) | 已移植 | `[hive_vp]` → viewport_event, canary row, compact console rows, dialog open/close tracking, main-frame health invalidation. Probe re-injection is not ported. |
| 20 | Stale-context / orphan-tab sweep | 已移植 | 30 s loop (configurable): `tabGroup.list` reconciliation, two-sweep forward orphan reaping, protocol-6 ungrouped reaper. |
| 21 | Health blockers / `tab_health` | 已移植 | `health.py` rules ported (privileged scheme, Web Store, foreign extension, DevTools, enterprise policy, file access); connection rollup in `/status`. |
| 22 | Telemetry JSONL | 已移植 | Daily `browser-YYYY-MM-DD.jsonl` under `TelemetryDir` (opt-in): tool_call, connection_event, context_event, cdp_event, viewport_event, attach_canary; redaction + truncation. |
| 23 | Social rate-limit hook | 已移植 | Upstream limit table + env/config overrides; navigate-time LinkedIn/Instagram `profile_view` check/record. Opt-in (`EnableRateLimits`). JSON store, not SQLite. |
| 24 | HTTP agent API on 127.0.0.1:14830 (`/health`, `/status`, `/profiles`, `/command`) | 已移植 | Loopback-only middleware; `/status` refreshes tab counts with a 750 ms live `tab.list`; `system_blocker` included. |
| 25 | Upstream read/control HTTP surface (`GET /contexts`, `GET/POST /profiles/default`, `GET /tabs/{id}/health`, `GET /tabs/{id}/actions`, adopt/release POSTs) | 已移植 | Field names follow upstream; adopt conflict returns 409. |
| 26 | Upstream remaining HTTP surface (`POST /tabs/{id}/reveal`, `POST /shutdown`) | 已移植 | Reveal returns the extension payload (or `fallback:"activate"`); `/shutdown` answers 202 `{"ok":true,"message":"Bridge shutdown initiated."}` and invokes `Config.ShutdownRequest`, which the supervised binary wires to a graceful exit (exit 0 = intentional, so the supervisor does not respawn). |
| 26b | Panel support endpoints (`GET /tabs`, `POST /contexts/close`) | 已移植 | Additive to the upstream HTTP API: `/tabs` returns the extension's unfiltered `tab.list` rows, `/contexts/close` implements the upstream body contract `{profile}` / `{all:true}` and returns `{"closed":[{profile,ok,status,…}]}`. They exist so the Go server's three panel proxies pass through real data instead of re-implementing the registry. |
| 27 | RPC control plane (`bridge_rpc.py`) | 已移植 | `control.go` / `control_dispatch.go` serve `ws://127.0.0.1:14831/control` with the exact 46-name `RPC_METHODS` allowlist, `{rpc_id,method,args,params}` requests, per-request goroutines, `client_hello(owner_pid)`, `subscribe_notifications`, server-pushed `{type:"notify"}` frames, `active_client_count` / `runtime_alive`, and the 10 s zombie reaper. `status_payload` gains `listening.control_rpc`, `control_rpc_clients` and `runtime_alive`. |
| 28 | Screenshot resize/annotate/artifact spill | 已移植 | `max_width` → browser-native 800px JPEG (`clip.scale`); last-interaction highlights annotate via page overlay; the CLI spills the JPEG and returns `saved_to`/`_image`. Resampling/encoding vs PIL is the only approximation (quantified in `docs/fixtures/browser-return-contract/README.md`). |
| 29 | AX refs (`eN`) and shadow-piercing selectors | 已移植 | Snapshot refs (`[ref=eN]`), href map + DOM-orphan link section, DOM-snapshot fallback and InlineTextBox cleaning are ported; click/type still use plain `querySelector` (no ref-based selector resolution yet). |
| 30 | Native dialog handling | 已移植 | `browser_dialog_respond` → `Page.handleJavaScriptDialog`; tracked dialogs from `Page.javascriptDialogOpening/Closed`; verified `confirm()` accept in real Chrome. |
| 31 | Real Chrome extension integration | 已移植 | `scripts/real_chrome_e2e.sh` loads the real 1.7.4 extension into Google Chrome 158 (throwaway profile, `cmd/chromeloader` + `Extensions.loadUnpacked`) and drives the full tool flow. Evidence in §10. |
| 32 | Exact `networkidle` semantics | 未移植 | Approximated as `readyState=complete` + 500 ms. |
| 33 | Tab-group protocol v5/v6 features (saved-group recycle, regroup, listUngrouped) | 已移植 | Persisted-chip recycle on create, `regrouped` event attribution, protocol-6 ungrouped reaper. Regroup is extension-driven; no bridge-issued `tabGroup.move`. |
| 34 | Process entry point (`cmd/browserbridge`) | 已移植 | Flags for WS/HTTP/legacy, sweep, telemetry, rate limits, artifacts, skills, script command; `-no-legacy`; SIGINT/SIGTERM graceful shutdown; JSON ready line; `-probe` health check; `-version`; `-parent-check` for the `HIVE_DESKTOP_PARENT_PID` watch (upstream `_watch_parent`). |

## 4. Upstream test mapping

Upstream test files live in `tools/tests/`. Go equivalents are in this module.

| Upstream file | What it covers | Go equivalent | Status |
|---|---|---|---|
| `test_bridge_host_lifetime.py` | Parent-process watch dog shuts the bridge down / debounces a miss | `cmd/browserbridge` `TestIdleWatchdog*` (idle exit, desktop-parent hold, control-client hold) + `TestDesktopParentWatchExits` (`_watch_parent`: parent died → exit ≤2s) + `internal/cli` `TestBrowserSupervisorParksOnIdleExit` (exit 0 parks, `ensure()` respawns) + `TestBrowserSupervisorRefusesRespawnAfterRuntimeDeath` (no restart into a dead runtime) | 已移植 |
| `test_bridge_multiconnection.py` | Two connections, default/sole/starred resolution, explicit routing, tab routing, unknown profile, ping-loop registration, disconnect isolation | `TestAppPingPongKeepsConnectionAlive`, `TestPingTimeoutDisconnects`, `TestFirstComeClaimsDefault`, `TestStarredDefaultWinsOverFirstCome`, `TestTabRoutingAcrossProfiles`, `TestDisconnectFailsInFlightRequest`, `TestDisconnectSoftPrunesContextSlice` | 已移植 |
| `test_bridge_rpc_allowlist.py` | Every forwarded bridge method is allowlisted; unknown method error | `TestRPCAllowlistCoversDispatch` (46 names + every one has a dispatcher), `TestRPCUnknownMethodIsRejected`, `TestRPCClientHelloRegistersOwnerAndCounts`, `TestRPCDispatchAgainstFakeExtension`, `TestRPCNotifyOnlyReachesSubscribers`, `TestRPCConcurrentCallsAreMultiplexed`, `TestRPCReaperClosesZombies`, `TestControlNotifyOnHandover` | 已移植 |
| `test_browser_cleanup_phase1.py` | Context close dead-letter, forward/ungrouped orphan reapers, group recycle, window affinity | `TestStaleSweepSoftPrunesAndReapsOrphanGroup`, `TestUngroupedOrphanReap`, window/recycle paths in `TestAdoptTabLazyGroupRecreateAfterSoftPrune`; dead-letter queue absent | 未移植 |
| `test_browser_discovery.py` | `connected_profiles` RPC reachability, listing every connection | `TestHTTPAPI` / `GET /profiles` (not the RPC path) | 未移植 |
| `test_browser_profile_conflict.py` | Explicit profile mismatch, reuse of binding, cold-start binding | partial: `no_browser_profile` + sole-connection fallback; no `browser_profile_conflict` binding guard | 未移植 |
| `test_browser_tools_comprehensive.py` | 41 tool-level tests: context isolation, tab ops, navigate/click/type/drag/snapshot/screenshot/wait/evaluate/upload/dialog/JSON unnest | per-command tests in `commands_tools_test.go` + `commands_l3_test.go` + `commands_page_contract_test.go` (interact actions, select, get_text, html spill, shadow query, resize, upload direct/triggered, dialog respond, script resolve/run, snapshot refs/href/DOM fallback, screenshot 800px JPEG + annotation); JSON unnest still absent | 未移植 |

## 5. Summary counts

- MCP tools: 33 total — 27 已移植, 6 未移植, 0 未验证.
  (The 6 remaining are the legacy `interactions.py` tools upstream never registers.)
- `bridge.py` methods: 55 total — 51 已移植, 4 未移植, 0 未验证.
  (Remaining: `has_public_listeners`, `highlight_rect`, `highlight_point`,
  `clear_highlight`.)
- Runtime features: 34 total — 33 已移植, 1 未移植, 0 未验证.
  (Remaining: exact `networkidle` semantics. Screenshot resize/annotate/spill
  and AX refs/`eN` were ported in the 2026-11-21 return-contract round; the
  remaining approximation there is the browser-native image codec, quantified
  in `docs/fixtures/browser-return-contract/`.)
- Upstream test files: 7 total — 3 已移植, 4 未移植, 0 未验证.
- **Grand total: 129 rows — 114 已移植, 15 未移植, 0 未验证.**

Control-plane delta (vs the L3a 103/26): +9 已移植, −9 未移植 — reveal/shutdown
HTTP, the RPC control plane, five former bridge methods (reveal, cdp_detach,
go_back, go_forward, get_attribute) and the two upstream test files whose
surfaces are now covered.

## 6. Wiring and remaining seams

Wired:

- `ContextStore` / `MemoryContextStore` (`contexts.go`): profile→group registry,
  soft/hard prune, group lookup.
- `EventSink` (`events.go`): still published for external observers; the bridge
  now also consumes `cdp_event`, `tab_event`, and `tab_group_event` internally.
- `Blocker` (`observability.go`): upstream wire shape; health rules in
  `health.go`.
- `Telemetry` (`observability.go`): `JSONLTelemetry` implements the optional
  `EventLogger` interface for context/CDP rows.
- `RateLimiter` (`observability.go`): `SocialRateLimiter` (`ratelimit.go`).
- Per-connection state isolates `pending`, `counter`, `in_flight`, and the CDP
  attach cache; L2 state (`state.go`) is separately mutexed and never assumes a
  single socket.
- `Config.ArtifactDir` / `Config.SkillDirs` / `Config.ScriptCommand` /
  `Config.ExtensionDir` (`bridge.go`): L3a knobs for artifact spill, the
  `browser_script` child process, and the setup hint.

Still open (not available; no fallback):

- Visible page highlight overlays (`highlight_rect` / `highlight_point` /
  `clear_highlight` inject a pulsing box upstream); the last-interaction
  highlight is recorded for screenshot annotation only.
- `has_public_listeners`.
- `browser_close` / navigation still require an explicit `tab_id`; only the
  L3a tools resolve the context's active tab automatically.
- The desktop panel's three Go-server routes (`/api/browser/contexts`,
  `/api/browser/tabs`, `/api/browser/contexts/close`) are no longer exempt from
  `scripts/surface_audit_allowlist.json`: the Go server now proxies them to the
  bridge HTTP endpoints above (`internal/server/routes_browser_panel.go`), so
  the bridge keeps the registry and the panel sees real data.

## 7. Full typed signatures (copied from upstream AST)

These are the exact parameter names, annotations/defaults, and return
annotations extracted from `tools/*.py`. `AutoSnapshotMode` is
`Literal["default","simple","interactive","off"]`; `_TIMEOUT_DEFAULT_S` is 60.

```python
# tools/advanced.py
browser_evaluate(script: str, tab_id: int | None = None, profile: str | None = None) -> dict
browser_get_text(selector: str, tab_id: int | None = None, profile: str | None = None, timeout_ms: int = 30000) -> dict
browser_resize(width: int, height: int, tab_id: int | None = None, profile: str | None = None) -> dict
browser_upload(selector: str, file_paths: list[str], trigger_selector: str | None = None, tab_id: int | None = None, profile: str | None = None, timeout_ms: int = 30000) -> dict
browser_dialog_respond(action: Literal['accept', 'dismiss'], tab_id: int | None = None, profile: str | None = None, prompt_text: str | None = None) -> dict

# tools/inspection.py
browser_screenshot(intent: str, tab_id: int | None = None, profile: str | None = None, full_page: bool = False, selector: str | None = None, annotate: bool = True) -> list
browser_shadow_query(selector: str, tab_id: int | None = None, profile: str | None = None) -> dict
browser_snapshot(tab_id: int | None = None, profile: str | None = None, mode: Literal['default', 'simple', 'interactive'] = 'default') -> dict
browser_console(tab_id: int | None = None, profile: str | None = None, level: str | None = None) -> dict
browser_html(tab_id: int | None = None, profile: str | None = None, selector: str | None = None) -> dict

# tools/interact.py
browser_interact(action: Literal['left_click', 'right_click', 'middle_click', 'double_click', 'triple_click', 'hover', 'type', 'key', 'scroll', 'drag', 'screenshot', 'zoom', 'wait'], tab_id: int | None = None, profile: str | None = None, selector: str | None = None, coordinate: list[float] | None = None, start_selector: str | None = None, start_coordinate: list[float] | None = None, text: str | None = None, clear_first: bool = True, use_insert_text: bool = True, modifiers: str | None = None, repeat: int = 1, scroll_direction: Literal['up', 'down', 'left', 'right'] = 'down', scroll_amount: int = 500, intent: str | None = None, full_page: bool = False, annotate: bool = True, region: list[float] | None = None, duration: float | None = None, wait_for_selector: str | None = None, wait_for_text: str | None = None, timeout_ms: int | None = None, auto_snapshot_mode: AutoSnapshotMode = 'simple', wait_after_ms: int = 0) -> list
browser_select(selector: str, values: list[str], tab_id: int | None = None, profile: str | None = None) -> dict

# tools/interactions.py (legacy, not registered)
browser_click(selector: str, tab_id: int | None = None, profile: str | None = None, button: Literal['left', 'right', 'middle'] = 'left', double_click: bool = False, timeout_ms: int = 5000, auto_snapshot_mode: AutoSnapshotMode = 'simple') -> dict
browser_click_coordinate(x: float, y: float, tab_id: int | None = None, profile: str | None = None, button: Literal['left', 'right', 'middle'] = 'left') -> list
browser_type(selector: str, text: str, tab_id: int | None = None, profile: str | None = None, delay_ms: int = 1, clear_first: bool = True, timeout_ms: int = 30000, use_insert_text: bool = True, auto_snapshot_mode: AutoSnapshotMode = 'simple') -> dict
browser_type_focused(text: str, tab_id: int | None = None, profile: str | None = None, delay_ms: int = 1, clear_first: bool = True, use_insert_text: bool = True, auto_snapshot_mode: AutoSnapshotMode = 'simple') -> dict
browser_press(key: str, selector: str | None = None, tab_id: int | None = None, profile: str | None = None, modifiers: list[str] | None = None) -> dict
browser_hover(selector: str, tab_id: int | None = None, profile: str | None = None, timeout_ms: int = 30000) -> dict
browser_hover_coordinate(x: float, y: float, tab_id: int | None = None, profile: str | None = None) -> list
browser_press_at(x: float, y: float, key: str, tab_id: int | None = None, profile: str | None = None) -> list
browser_select(selector: str, values: list[str], tab_id: int | None = None, profile: str | None = None) -> dict
browser_scroll(direction: Literal['up', 'down', 'left', 'right'] = 'down', amount: int = 500, selector: str | None = None, tab_id: int | None = None, profile: str | None = None, auto_snapshot_mode: AutoSnapshotMode = 'simple') -> dict
browser_drag(start_selector: str, end_selector: str, tab_id: int | None = None, profile: str | None = None, timeout_ms: int = 30000) -> dict

# tools/lifecycle.py
browser_setup() -> dict
browser_status(profile: str | None = None) -> dict
browser_stop(profile: str | None = None) -> dict

# tools/navigation.py
browser_navigate(url: str, tab_id: int | None = None, profile: str | None = None, wait_until: Literal['commit', 'domcontentloaded', 'load', 'networkidle'] = 'load', profile_display_name: str | None = None, browser_profile: str | None = None) -> dict
browser_reload(tab_id: int | None = None, profile: str | None = None) -> dict

# tools/script.py
browser_script(skill: str, script: str, args: dict[str, Any] | None = None, timeout_s: float = 60, tab_id: int | None = None, profile: str | None = None, profile_display_name: str | None = None, browser_profile: str | None = None) -> dict

# tools/tabs.py
browser_tabs(profile: str | None = None) -> dict
browser_open(url: str, background: bool = False, profile: str | None = None, profile_display_name: str | None = None, browser_profile: str | None = None) -> dict
browser_close(tab_id: int | None = None, profile: str | None = None) -> dict
browser_activate_tab(tab_id: int, profile: str | None = None) -> dict
```

## 8. Explicit non-claims

- Real Chrome integration **is now exercised** for the L3a surface by
  `scripts/real_chrome_e2e.sh` (Chrome 158 + the vendored 1.7.4 extension, §10).
  Everything else — multi-profile hand-over, sweeps, rate limits, the full-page
  screenshot path — is still asserted only against a fake extension speaking the
  documented `{id,type,...params}` / `{id,result|error}` protocol.
- The real-Chrome run covers one Chrome profile and one macOS host; it does not
  prove behavior on Windows/Linux, headless mode, or with several profiles
  connected at once.
- Passing `go test ./...` proves the Go protocol implementation, not upstream
  behavioral equivalence for unlisted edge cases.
- `browser_snapshot` / `browser_screenshot` / `browser_interact` return shapes
  are Go-native JSON, not the upstream MCP content-block list. Screenshots are
  resized to 800px JPEG by the browser itself (approximation vs upstream's PIL
  pass, quantified in `docs/fixtures/browser-return-contract/`); coordinate
  click/hover/key attach that same annotated screenshot (`data`+`mimeType`,
  R15, measured in `docs/fixtures/browser-annotated-actions/`).
- `browser_script` runs the skill script as a **child process** (default
  `uv run python`), not in-process: there is no `ctx.bridge` object, the script
  gets a JSON stdin envelope plus `HIVE_BRIDGE_*` env vars and is expected to
  print JSON. Upstream's `async def run(ctx)` contract is not enforced by the Go
  side, and hook modules (`hooks/rate_limit_hook.py`) do not apply.
- Tab defaulting (`tab_id=None` → context active tab) applies to the L3a tools;
  `browser_close`, `browser_activate_tab`, `browser_reload` and the tab-scoped
  commands that predate L3a still require an explicit `tab_id`.
- Telemetry JSONL is **opt-in** (`Config.TelemetryDir`); the default bridge
  writes no telemetry file. The upstream logger writes by default.
- The social rate-limit hook is **opt-in** (`Config.EnableRateLimits`) with the
  upstream conservative limits. Records persist as JSON
  (`social_rate_limits.json`), not upstream's SQLite schema; limit semantics
  (min-delay, hourly/daily/weekly caps, ceilings, env/config overrides) match.
- From-user hand-over notifications go to `Config.Notify` *and* out over the
  control-RPC push transport to every subscribed client.
- The idle-exit watchdog only runs in `cmd/browserbridge`; the library exposes
  `ControlClients()` / `RuntimeAlive()` for a host that wants its own policy.
  `POST /shutdown` with no `Config.ShutdownRequest` is a no-op (202, no exit).

## 9. Command and endpoint surface

Bridge control commands added to `POST /command` (names mirror upstream
`BeelineBridge` methods / the RPC allowlist, **not** upstream MCP tool names):

| Command | Parameters | Result |
|---|---|---|
| `create_context` | `agent_id`, `display_name?`, `browser_profile?` | `{groupId, tabId, windowId?, browser_profile}` |
| `register_context` | `profile`, `group_id`, `name?`, `browser_profile?` | `{ok}` |
| `destroy_context` | `group_id`, `browser_profile?` | extension `context.destroy` payload |
| `list_contexts` | — | `[{profile, groupId, activeTab, name, tabs, last_active_ms, dormant, blockers, status}]` |
| `adopt_tab` | `profile`, `tab_id`, `from_user?` | `{ok, tabId, groupId, already_owned?}` |
| `release_tab` | `tab_id`, `from_user?` | `{ok, tabId, ...}` |
| `audit_tab` | `tab_id` | raw `tab.audit` snapshot |
| `tab_health` | `tab_id`, `force_audit?` | `{ok, tab_id, url, blockers[]}` |
| `get_tab_blockers` | `tab_id` | `blockers[]` |
| `record_action` | `tab_id`, `verb`, `target?`, `ok?` | `{ok}` |
| `get_tab_actions` | `tab_id`, `limit?`, `since_ms?` | `{actions[], last_action_ts_ms}` |
| `get_pending_dialog` | `tab_id` | dialog or `null` |
| `list_pending_dialogs` | — | `[{tab_id,type,message,default_prompt,url,opened_at_ms}]` |

HTTP endpoints added (loopback-only):

| Method | Path | Purpose |
|---|---|---|
| GET | `/contexts` | upstream row shape, blockers rolled up per context |
| GET | `/tabs` | every tab the extension sees, unfiltered (panel proxy source; 503 `not_connected` with no extension) |
| POST | `/contexts/close` | body `{profile}` or `{all:true}`; returns `{"closed":[…]}` (panel proxy source) |
| GET | `/tabs/{id}/health` | `?force=1` re-audits; cached blockers otherwise |
| GET | `/tabs/{id}/actions` | `limit`, `since` incremental polling |
| POST | `/contexts/{profile}/adopt-tab` | body `{tabId}`; 409 on agent conflict |
| POST | `/tabs/{id}/release` | user-initiated release (`from_user=true`) |
| POST | `/profiles/default` | body `{label, extensionId?}`; stars the default |
| POST | `/tabs/{id}/reveal` | activate + raise the Chrome window; `fallback:"activate"` on old extensions |
| POST | `/shutdown` | 202 `{"ok":true,"message":"Bridge shutdown initiated."}`; invokes `ShutdownRequest` (graceful exit under the supervisor) |

Control RPC (`ws://127.0.0.1:14831/control`), the 46 upstream `RPC_METHODS`:
`create_context`, `destroy_context`, `register_context`, `list_contexts`,
`create_tab`, `close_tab`, `list_tabs`, `activate_tab`, `navigate`, `go_back`,
`go_forward`, `reload`, `click`, `click_coordinate`, `type_text`, `press_key`,
`press_key_at`, `hover`, `hover_coordinate`, `scroll`, `select_option`,
`screenshot`, `screenshot_region`, `snapshot`, `evaluate`, `get_text`,
`get_attribute`, `shadow_query`, `wait_for_selector`, `wait_for_text`,
`resize`, `handle_javascript_dialog`, `get_pending_dialog`,
`get_tab_blockers`, `tab_health`, `audit_tab`, `record_action`, `cdp_attach`,
`cdp_detach`, `_cdp`, `status_payload`, `connected_profiles`,
`connection_help`, `is_connected`, plus the two per-connection methods
`client_hello` and `subscribe_notifications`. Unknown names get
`{code:"unknown_method"}`.

Test evidence is in `contexts_test.go`, `events_health_test.go`, and
`sweep_telemetry_test.go` (20 L2 tests); L3a adds `commands_l3_test.go` (tool
surface against the fake extension) and `cmd/browserbridge/main_test.go` (flags,
listener binding, signal/cancel shutdown, probe). The control plane adds
`control_test.go` (allowlist/hello/notify/concurrency/reaper), `history_http_test.go`
(reveal/shutdown/history/attribute/detach) and the cmd idle-watchdog tests.
Gate: `go test -race ./...`, `go vet ./...`, `gofmt -l .`.

## 10. L3a real-Chrome evidence

`scripts/real_chrome_e2e.sh` (plus `cmd/chromeloader`) was run on macOS 26.7.1
against **Google Chrome 158.0.8127.37** and the vendored extension 1.7.4
(`jkpcegnbfimimjodblcemoheedidnppm`, protocol 6).

What it does and what it proved:

- Chrome 137+ ignores `--load-extension` in branded builds, so the extension is
  loaded through the DevTools pipe (`--remote-debugging-pipe
  --enable-unsafe-extension-debugging`) and `Extensions.loadUnpacked`. The
  extension then dialled `ws://127.0.0.1:14829/bridge` on its own.
- The bridge bound ws 14829, legacy 9229 and HTTP 14830 (log line
  `legacy websocket listening: 127.0.0.1:9229`).
- Flow, all `ok:true` against the live browser, driven through `POST /command`:

| Step | Command | Observed |
|---|---|---|
| 1 | `browser_status` | `connected:true`, one profile (extension-generated label, e.g. `vivid-orange-wren`, v1.7.4, protocol 6) |
| 2 | `browser_tabs` | tab list from the live extension |
| 3 | `browser_open` | tab created for `http://127.0.0.1:18099/index.html` |
| 4 | `browser_navigate` | `readyState=complete`, title `Bridge E2E Fixture` |
| 5 | `browser_interact` `left_click` | click landed; auto-`snapshot` attached; `document.title` became `clicked` |
| 6 | `browser_interact` `type` | typed value read back as `hello bridge` via `browser_evaluate` |
| 7 | `browser_interact` `key` / `scroll` / `zoom` | keyDown+keyUp, `deltaY=300` wheel, zoom JPEG with `crop_box` |
| 8 | `browser_select` | `selected:["b"]` |
| 9 | `browser_get_text` | `hello from fixture` |
| 10 | `browser_resize` | `window.innerWidth == 1280` afterwards |
| 11 | `browser_shadow_query` | `#host >>> .item` → `tag:"SPAN"`, fraction rect |
| 12 | `browser_upload` | `accepted.change_fired=true`, file listed in `accepted.files` |
| 13 | `browser_html` | artifact written, `saved_to` readable |
| 14 | `browser_screenshot` | PNG decoded (53 KB); the render shows the typed value, the `Beta` selection, `upload-payload.txt` and the shadow text |
| 15 | `browser_snapshot` | 25 AX nodes |
| 16 | `browser_console` | upstream stub message verbatim |
| 17 | `browser_interact` `left_click` + `browser_dialog_respond` | `confirm()` accepted; `window.__confirmed === true` |
| 18 | `browser_script` | skill script ran, reached `HIVE_BRIDGE_HTTP/health` (`bridge_health_ok:true`) |
| 19 | `create_context` → `browser_status` → `browser_stop` | context registered (`running:true`), then `status:"stopped", closedTabs:1` |

The same run also produced `GET /tabs/{id}/actions` rows for the interact verbs
(`click`, `type`, `key`, `scroll`, `select`) and an empty blocker list for a
plain `http://` tab. Evidence files (raw JSON per step, `screenshot.png`,
`bridge.log`, `chrome-ready.json`, `artifact.html`) live in the run's evidence
directory; the script prints its path and exits non-zero on any failed step.

Known gaps this run surfaced: the tracked pending-dialog record was already
cleared when `browser_dialog_respond` answered, so the success envelope carried
`dialog:null` (upstream can have the same race); coordinate-targeted actions do
not return the annotated screenshot upstream attaches.

## 11. Control-plane evidence (14831 / idle exit / serve keepalive)

Unit (fake extension + fake control clients, `go test -race ./...`):

| Test | What it proves |
|---|---|
| `TestRPCAllowlistCoversDispatch` | The allowlist is exactly the upstream 46 names and every one (except `client_hello` / `subscribe_notifications`) has a dispatcher — no drift, no reflective lookup |
| `TestRPCUnknownMethodIsRejected` | A name outside the allowlist never reaches the bridge (`unknown_method`, `retryable:false`) |
| `TestRPCClientHelloRegistersOwnerAndCounts` | `client_hello(pid)` registers the owner, `previous` on re-hello, `status.control_rpc_clients == 1`, `runtime_alive == true`, invalid pid is a result-level `invalid_pid`, disconnect drops the count to 0 |
| `TestRPCDispatchAgainstFakeExtension` | A real command travels over 14831 (`list_tabs`, positional `activate_tab(11)`, `navigate` with kwargs) and reaches the extension; a bridge error comes back as a structured error envelope |
| `TestRPCNotifyOnlyReachesSubscribers` | `subscribe_notifications` gates the push; the frame is `{type:"notify", profile, text, tabId, action}`; a non-subscriber gets nothing |
| `TestRPCConcurrentCallsAreMultiplexed` | 12 overlapping calls on one socket all get answered (per-request goroutines) |
| `TestRPCReaperClosesZombies` | Both zombie classes (dead owner PID, never identified) are dropped and their sockets closed |
| `TestControlNotifyOnHandover` | `adopt_tab`/`release_tab` from the side panel feed the same push transport |
| `TestPanelTabsHTTPReturnsEveryVisibleTab` / `...WithoutExtensionIsNotConnected` | `GET /tabs` returns the extension's grouped + ungrouped rows; with no extension it is the bridge's own 503 `not_connected` with an empty `tabs` array |
| `TestPanelCloseContextsHTTPClosesProfileAndAll` | `POST /contexts/close` closes one profile through `context.destroy` (registry entry dropped) and reports `{"closed":[]}` for `all` with nothing registered; a body without `profile`/`all` is the upstream 400 |
| `cmd/... TestIdleWatchdogExits` | No control client + no `HIVE_DESKTOP_PARENT_PID` → the binary exits 0 after the grace |
| `cmd/... TestIdleWatchdogHoldsWhileDesktopAlive` / `...HoldsWhileControlClientConnected` | A live desktop parent or one control client suppresses the exit |
| `cmd/... TestDesktopParentWatchExits` | Upstream `_watch_parent`: with `HIVE_DESKTOP_PARENT_PID` dead the binary exits on its own inside the ≤2s budget, even with the idle watchdog disabled (measured 0.09s in-test at `-parent-check 20ms`; 783–866ms real-machine at the 400ms default) |
| `cmd/... TestShutdownRouteEndsTheProcess` | `POST /shutdown` answers 202 and the process exits 0 |
| `internal/cli` `TestBrowserSupervisorParksOnIdleExit` | Exit code 0 parks the supervisor (no restart counter); `ensure()` starts a fresh child |
| `internal/cli` `TestBrowserSupervisorRefusesRespawnAfterRuntimeDeath` | Once the runtime owner is gone, `ensure()` is a no-op and a stale wake cannot restart the loop (`runtimeGone`), matching upstream's never-respawn-into-a-dead-runtime guard |
| `internal/cli` `TestControlKeepalive*` | `client_hello(owner_pid)` + `subscribe_notifications` + notify routing, reconnect after a drop, wake-on-unreachable; serve then hands each frame to `SessionManager.DeliverBrowserNotify` (queen by session id, worker by colony worker id — see `docs/browser-host.md` §2.2) |

Process-level (`go_hive/scripts/e2e_browser_host.sh`, macOS 26.7.1, real Chrome
158 + the bundled 1.7.4 extension):

- step 04: bridge child `browserbridge -http 127.0.0.1:14830 -control 127.0.0.1:14831 …`;
  `GET /status` → `listening.control_rpc:true`, `control_rpc_clients:1`,
  `runtime_alive:true` (serve's keepalive identified with serve's PID).
- step 07: `POST /tabs/{id}/reveal` answered
  `{"ok":true,"revealed":true,"prevState":"normal"}` against the live extension
  (the harness reveals before synthetic input: a minimized/occluded window makes
  Chrome drop CDP mouse/key events).
- step 06/07: the extension connects and the `hive-browser` flow passes against
  the live browser.
- step 08: SIGTERM serve → serve exits, bridge child gone, nothing answers
  `:14830/health`.
- step 09: a bare `browserbridge` with **no** control client and **no** desktop
  parent (2 s grace for the test) exits on its own with code 0, logging
  `no control RPC clients and desktop is gone — starting 2s grace before self-exit`.
- 2026-11-20 parent-death run (isolated ports, `kill -9` on the desktop parent):
  a bare bridge exits in **783 ms** with the default `-parent-check 400ms`
  (baseline before the watch: **40 023 ms**), 866 ms with `-no-idle-exit`, and
  815 ms under `hive serve` with **no respawn** (`Browser bridge: runtime gone —
  not restarting`). Real-Chrome CLI negatives: missing `--intent` / `--region`
  → exit 1; usage errors → exit 2 (full table: `docs/browser-host.md` §7).

`go_hive/browserbridge/scripts/real_chrome_e2e.sh` was re-run after the change:
29 assertions, all `ok`, `PASS: all steps succeeded against real Chrome
(Google Chrome 158.0.8127.37)` — the 22 active tools did not regress. The
script now passes `-no-idle-exit` because it holds no control client (the idle
watchdog is covered by the host E2E step above and the cmd tests).
