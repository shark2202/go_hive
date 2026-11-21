# Browser-bridge residuals fixtures (R09/R10/R11)

Baseline: `browser-ws-residuals`, browserbridge `0.3.0-l3a`, 2026-11-21.

| File | Produced by | Shows |
|---|---|---|
| `e2e-output.txt` | `bash go_hive/scripts/e2e_browser_residuals.sh` | Real `cmd/browserbridge` binary + fake extension over the real WS protocol: R09 same-tab `_cdp` serialization (the second frame only reaches the extension after the first reply is released), R10 17 MiB frame accepted/relayed with the connection surviving, R11 `/status` fields. |
| `status-healthy.json` | `GET /status` on a bridge with all listeners free (`-ws 19029 -http 19030 -control 19031 -legacy 19032`) | `bridge_version=0.3.0-l3a`, `runtime_version=null`, `bind_errors={}`, `listening.legacy_status=false` (Go has no 9230 listener). |
| `status-bind-error.json` | Same, with the legacy port held by another process | `bind_errors.legacy_ws` names the address and the bind error; `listening.legacy_ws=false` — the diagnostic upstream's `_bind_errors` provides, instead of an unexplained disconnected rail. |

Go-only additive `/status` fields kept on purpose (documented in
`browserbridge/status.go`): `tabs`, `tabs_total`, `profiles`,
`capabilities`, `viewport_scale`, `connections[].tabs`. They do not replace
any upstream field.

Encoding/codec approximations (Skia vs PIL/libjpeg JPEG bytes, dpr>1 annotation
width) are unchanged by this round and remain quantified in
`../browser-return-contract/README.md`.
