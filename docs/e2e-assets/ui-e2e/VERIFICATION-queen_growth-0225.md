# UI E2E verification — colony surfaces in the sidebar AND on its colony page

**Verdict: PASS** for the colony under test (8/8 assertions, exit 0).
**Two gate bugs fixed en route** (both had made the gate lie in opposite directions).
**One product defect found and proved** (route-id collisions make colonies unreachable).

- Date: 2026-11-23, 02:17–02:25 CST
- Agent: `queen_growth`
- Colony under test: `ui_e2e_012144` (route id `ui-e2e-012144`, owner `queen_growth`)
- Server: `hive-embed serve` on `127.0.0.1:8788` (already running; not restarted)
- Driver: Playwright, headless, **system** Google Chrome (`channel="chrome"`), throwaway profile
- Gate: `scripts/ui_colony_e2e.py` (sha256 `b09f10b2bdcc56d8860a96371eb07398945b9f97e248ddf7df9fc91c7d2fbc56`)

`hive-browser` was **not** used: it needs the Hive Browser Bridge extension
(`jkpcegnbfimimjodblcemoheedidnppm`), which is not installed in any Chrome
profile on this machine, so it would fail for reasons unrelated to the code
under test. Orca was **not** used: in this shared environment concurrent agents
destroy each other's tabs and kill each other's harness processes (observed: the
Orca-driven gate SIGKILLed at 85 s), which turns a gate into a coin flip.

---

## 1. The requirement, verified

> "colony must appear in sidebar and colony page"

Against the live UI, fresh page load, `ui_e2e_012144`:

| # | Assertion | Result |
|---|-----------|--------|
| 1 | sidebar: colony link present in DOM | PASS — `#/colony/ui-e2e-012144`, 16 colony links in DOM |
| 2 | sidebar: the link for this route is labelled this colony | PASS — link text `Ui E2E 012144` == backend name |
| 3 | route: `page.url` contains the colony route id | PASS — `http://127.0.0.1:8788/#/colony/ui-e2e-012144` |
| 4 | colony page: h1 names the colony | PASS — h1 `Ui E2E 012144` == backend name |
| 5 | colony page: h1 is not a router echo of the route id | PASS |
| 6 | colony page: not an error surface | PASS |
| 7 | colony page: colony panel chrome rendered | PASS — 数据 / 计划 / 自动化 / 工作节点 |
| 8 | no console errors / pageerrors | PASS — `[]` |

**8/8 assertions in 6.2 s, exit code 0.**

Assertion 2 and 4 take their expected value from `/api/discover`, not from the
gate's own guess, so they are a UI-vs-API cross-check rather than an echo of
what the gate hoped to see.

## 2. The negative control — why this gate is not a rubber stamp

A gate that cannot fail is not a gate. Run against a colony that does not
exist (`does_not_exist_777777`, `--allow-missing-colony --expect-fail`):

```
FAIL  sidebar: colony link present in DOM
FAIL  sidebar: the link for this route is labelled this colony
PASS  route: page.url contains the colony route id
PASS  colony page: h1 names the colony              <-- !!
PASS  colony page: h1 is not a router echo of the route id
PASS  colony page: not an error surface              <-- !!
FAIL  colony page: colony panel chrome rendered
PASS  no console errors / pageerrors

4/8 assertions passed, exit 1  (as required)
```

**This is the important finding about gate design.** A colony that does not
exist renders a page that *looks fine*: it has an `<h1>`, an empty transcript,
and **no error text at all**. The naive rule "h1 exists && not an error page"
would have green-lit a phantom. The two assertions that actually catch it are
the ones no router fallback can satisfy — the sidebar membership check, and
`h1 != raw route id` (the real colony's name comes from the backend; the
phantom echoes its URL).

## 3. Two gate bugs found and fixed (it was lying in both directions)

### 3a. False RED — `slugToColonyId` was half-implemented

The gate implemented only the first of the three rewrites the frontend
performs. Frontend truth
(`go-hive/frontend/src/lib/colony-registry.ts:90`):

```ts
export function slugToColonyId(slug: string): string {
  return slug
    .replace(/_/g, "-")
    .replace(/^email-/, "")
    .replace(/-agent$/, "");
}
```

The gate had `slug.replace("_", "-")` and a docstring asserting *"it does NOT
strip words"* — which was simply wrong. Reproduced live: `newsletter_agent` is
a healthy colony whose sidebar link is `#/colony/newsletter`; the gate hunted
for `newsletter-agent`, found nothing, and failed a working colony. Six
colonies on disk diverge under the two mappings.

### 3b. False GREEN — substring name matching

Fixing 3a exposed the opposite fault. With the route id corrected,
`newsletter_agent` reported **PASS** while rendering the *wrong colony*:

```
PASS  colony page: h1 names the colony
      backend name 'Newsletter Agent' h1=['Email Newsletter Agent']
```

`"newsletter agent" in "email newsletter agent"` is true. A case-insensitive
substring test green-lights the wrong page — worse than a red. Assertions 2 and
4 now compare **by equality**, scoped to the link for the route under test.

## 4. PRODUCT DEFECT — route-id collisions make colonies unreachable

Found while fixing 3a. **21 colonies on disk map to only 16 route ids. Five
colonies cannot be opened in the UI by any URL.**

`slugToColonyId` is many-to-one: it strips a trailing `-agent` and a leading
`email-`, but nothing prevents two distinct directories collapsing onto the
same route id. The sidebar renders one link per route id, so the loser is
silently dropped.

| route id | colonies that claim it | reachable via UI |
|----------|------------------------|------------------|
| `collide` | `collide`, `collide_agent` | `collide` only |
| `collide-probe` | `collide-probe`, `collide_probe` | `collide-probe` only |
| `newsletter` | `email_newsletter_agent`, `newsletter_agent` | `email_newsletter_agent` only |
| `sweep-zz` | `sweep_zz`, `sweep_zz_agent` | `sweep_zz` only |
| `ui-e2e-ghost` | `ui_e2e_ghost`, `ui_e2e_ghost_agent` | `ui_e2e_ghost` only |

**Proof by deep link.** Navigating directly to the loser's own route does not
reach it — there is no backend colony for that id, so the router echoes the URL:

| URL | h1 rendered | meaning |
|-----|-------------|---------|
| `#/colony/collide` | `Collide` | the OTHER colony |
| `#/colony/collide-agent` | `collide-agent` | raw route echo — no such colony |
| `#/colony/newsletter` | `Email Newsletter Agent` | the OTHER colony |
| `#/colony/newsletter-agent` | `newsletter-agent` | raw route echo — no such colony |

The side effect is worse than an unreachable colony: because the loser's route
page falls back to the *winner's* name, a user who bookmarks a loser's URL is
shown a different colony's page with no error.

**Scope.** Not systemic corruption: the 15 canonical slugs in `QUEEN_REGISTRY`
all get rewritten (by design — `sdr_agent` → `sdr`), and none of them collide
with each other. The defect is that a many-to-one mapping has **no collision
guard** — the runtime will happily accept two directories that collapse to one
route id and then render only one.

**Suggested fix direction (not applied — product decision):** reject or
disambiguate on create when `slugToColonyId(new) == slugToColonyId(existing)`,
or make the route id injective (append a short hash instead of stripping).

## 5. How to run the gate

```bash
# positive (expect exit 0)
python3 scripts/ui_colony_e2e.py ui_e2e_012144

# negative control (expect exit 1; --expect-fail inverts so CI sees 0)
python3 scripts/ui_colony_e2e.py does_not_exist_777777 \
    --allow-missing-colony --expect-fail

# machine-readable
python3 scripts/ui_colony_e2e.py ui_e2e_012144 --json --json-out /tmp/colony.json
```

Three-valued exit codes, so a broken rig never reads as a broken feature:

| code | meaning |
|------|---------|
| 0 | every assertion passed |
| 1 | an assertion failed — the product is broken |
| 2 | preflight/harness error (serve unreachable, colony not on disk, playwright missing, browser failed to launch) |

Run it with the Python that has Playwright installed (here `/usr/bin/python3`).
No `playwright install` is needed: the system Chrome is driven via
`channel="chrome"`, and a throwaway profile is used, so the user's logged-in
Chrome is never touched.

---

## Reproduce the whole matrix

```
ui_e2e_012144        exit=0  8/8 passed   collision=0
demo                 exit=0  8/8 passed   collision=0
demo2                exit=0  8/8 passed   collision=0
collide              exit=0  8/8 passed   collision=1   <- collision winner
newsletter_agent     exit=1  6/8 passed   collision=1   <- collision loser
sweep_zz_agent       exit=1  6/8 passed   collision=1   <- collision loser
does_not_exist_*     exit=1  4/8 passed   (negative control)
```

`collision=1` means the note fired: two on-disk colonies map to this route id.
The winner passes; the loser fails with a note saying plainly that this is a
product defect, not a gate failure.
