# UI E2E gate — colony appears in sidebar AND on colony page

Requirement under test: **a colony must appear in the sidebar and on its colony page.**

## Run

```sh
./run.sh ui_e2e_012144
# or point at another server:
./run.sh ui_e2e_012144 http://127.0.0.1:8788
```

Exit codes: `0` pass · `1` an assertion failed · `2` preflight/harness error.

Env knobs: `HIVE_BASE`, `HIVE_E2E_OUT` (artifact dir), `HIVE_E2E_PYTHON`.

## What it does

1. Loads the real Hive SPA from `/` in the **installed Google Chrome**, headless,
   via Playwright `channel="chrome"`. No browser download; throwaway profile, so
   the user's logged-in Chrome is never touched.
2. **Sidebar** — asserts a `a[href*='/colony/']` link for the colony id exists,
   and the colony's name is visible in the list.
3. **Colony page** — navigates to `#/colony/<id>` and asserts the route, the `<h1>`,
   that the page is not an error surface, and that the colony panel chrome rendered
   (`数据/计划/自动化/工作节点`). The panel check is what stops a failed-to-resolve
   colony from passing as an empty shell.
4. Asserts zero console errors, and writes screenshots + a JSON assertion report.

## Assertions

| Assertion | Why it matters |
|---|---|
| sidebar: loaded the Hive SPA | the app itself came up, not a proxy error page |
| sidebar: colony link present | requirement, part 1 |
| sidebar: colony name visible | the link is labelled, not a blank row |
| colony page: route is the colony URL | we landed where we asked to |
| colony page: heading names the colony | requirement, part 2 |
| colony page: not an error surface | not a 404/error shell |
| colony page: colony panel rendered | the colony actually resolved and rendered |
| no console errors | no silent failures on the way |

## Verified to discriminate

A gate that cannot fail is worse than no gate. Both directions were confirmed:

| Input | Result |
|---|---|
| `ui_e2e_012144` (exists) | **PASS**, exit 0, all 8 assertions true |
| `ui_e2e_does_not_exist_999999` | **FAIL**, exit 1 — sidebar link, sidebar name, and panel checks all false |
| `nope_xyz` (no disk dir) | preflight **FAIL**, exit 2 |
| base `http://127.0.0.1:9999` (nothing listening) | preflight **FAIL**, exit 2 |

## Why not `hive-browser`

The `hive-browser` toolchain drives the user's Chrome through the Hive Browser
Bridge extension (`jkpcegnbfimimjodblcemoheedidnppm`). That extension is **not
installed** on this machine, so `hive-browser status` reports
`connected: false` and every command fails — for a reason unrelated to the code
under test. A gate built on it would be a false red here.

This harness drives a Chrome that Orca/Playwright launches from the system
install, so it needs no extension and no bridge. If the bridge extension does get
installed and the team prefers `hive-browser`, this script's assertions port over
directly — only the driver changes.

## Files

- `verify_colony_ui.py` — the assertions (this is the test).
- `run.sh` — preflight (server up? colony on disk?) + invocation.
- `ui-e2e-artifacts/` — screenshots + JSON report from the latest run (gitignored).
