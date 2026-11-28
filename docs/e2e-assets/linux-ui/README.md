# Linux UI E2E gate

`go_hive/scripts/ui_e2e_linux.sh` builds an embedded-UI Linux binary and runs
`go_hive/scripts/ui_e2e_linux.py` inside the existing `hive-linux-qa:ui` image.
It uses the container's `/usr/bin/chromium` over a minimal DevTools Protocol
client and the Python standard library only. There is no Playwright, npm
install, network fetch, or host-darwin binary in the path.

```bash
# Repeatable run (rebuilds the Linux binary; uses GOPROXY=off)
go_hive/scripts/ui_e2e_linux.sh

# Reuse the existing binary
go_hive/scripts/ui_e2e_linux.sh --no-build

# Prove the gate can go red: load about:blank instead of the app
go_hive/scripts/ui_e2e_linux.sh --no-build --fault dom-empty \
  --evidence-dir go_hive/docs/e2e-assets/linux-ui/fault \
  --json-out go_hive/docs/e2e-assets/linux-ui/fault/ui-e2e-linux-fault.json
```

Exit codes: `0` pass, `1` assertion failure (including the fault self-check),
`2` environment error (wrong binary, no Chromium, serve or CDP unavailable).

## What the gate asserts

1. The binary is ELF for the container architecture, and its SHA-256 is recorded.
2. A throwaway `HIVE_HOME` boots `hive-embed serve` on a free port; `/api/health`
   is 200.
3. `POST /api/sessions` creates the source queen DM session.
4. The DM is forked into a colony through the popup path
   (`source_session_id` + `colony_id`), returning a live colony session and a
   worker.
5. The **source** `meta.json` records `colony_spawned=true` and
   `spawned_colony_id=<colony>`. This is the spawn lock.
6. `/api/discover`, `/api/colonies/tools-index`, and disk `metadata.json` agree
   on the colony, queen, `created_at`, and `queen_session_id`.
7. In real Chromium: the first-run "Welcome to Hive" tour is dismissed (Skip
   button) and does not return after a reload; the sidebar contains a link to
   `#/colony/<route-id>` with the English **Colonies** label and the colony's
   display name. `route-id` is derived with `slugToColonyId`: `_` -> `-`, strip
   `email-` and `-agent`.
8. Clicking the sidebar link renders the colony page: `h1` names the colony,
   the queen-title chip is present ("Head of Growth"), the **CONVERSATION**
   panel renders, and the **Data / Plan / Automations / Workers** Colony panel
   tabs render; no not-found/404 state.
9. Negative control: `#/colony/ui-gate-ghost-does-not-exist` is absent from the
   sidebar; it does not render the positive colony's heading, queen chip, or
   panel tabs; it shows the explicit `Colony not found` state; and it is not
   stuck on the `Connecting to agent...` overlay. The last two checks are what
   make this gate red for the old permanent-spinner bug.
10. Cleanup deletes the colony via `DELETE /api/colonies/<id>?purge=1`, stops
    both sessions, stops serve, and removes the throwaway home.

## Resolved P2: unknown colony route

`#/colony/<nonexistent>` previously showed an indefinite **"Connecting to
agent..."** spinner. The cause was in `frontend/src/pages/colony-chat.tsx`: the
not-found branch required `!colony && !isNewChat && !agentState.loading`, while
the session-load effect returned early when there was no `agentPath`; nothing
ever cleared the initial `loading: true`.

The fix makes `ColonyContext.refresh` resolve after the refreshed colony list is
committed and records a settled lookup for the route. Once a lookup settles
without a match, the page renders `Colony not found: <id>`; while the lookup is
still in flight it keeps the legitimate connecting state. The fix and a
regression test live in:

* `go_hive/go-hive/frontend/src/pages/colony-chat.tsx`
* `go_hive/go-hive/frontend/src/pages/colony-chat.test.tsx`
* `go_hive/go-hive/frontend/src/context/ColonyContext.tsx`

The regression test was red on the old tree (2 of 4 cases failed) and green
after the fix (4/4). `linux-colony-ghost.png` now shows the explicit not-found
branch; the gate's `j` step asserts both `not_found=true` and `connecting=false`.


## Why the old evidence said "not found"

The old Linux screenshots were real renders. The assertions missed because:

* The built SPA is a **hash router**: links are `#/colony/<route-id>`, not
  `/colony/<slug>` and not the display name. Asserting on a plain path or on the
  on-disk slug finds nothing.
* The first-run **Welcome to Hive** modal covers the page. The gate now
  dismisses it and records the before/after state.
* The UI is **English** by default ("Colonies", "CONVERSATION", "Data"), so a
  hard-coded `蜂群` assertion false-fails.
* `colony_spawned=false` / `spawned_colony_id=null` in the create response is
  the **child colony session's** response. Those fields describe the *source DM
  session*. The source `meta.json` is `true` / `ui_gate_linux` after a successful
  fork. Reading the child response as the spawn state is a harness bug. The old
  `ui-create.json` is exactly that child response: it has `has_worker=true`,
  `queen_phase=colony`, and `colony_spawned=false`; it never reads the source
  session's `meta.json`.

## Historical note

The pre-fix P2 was the permanent **"Connecting to agent..."** spinner described
above. It is no longer a current finding: the product fix and regression test
are in the files listed above, and the refreshed gate evidence has
`product_findings: []`, `not_found=true`, and `connecting=false` for the ghost
route. The old finding remains useful as a regression case, not as an open
product issue.