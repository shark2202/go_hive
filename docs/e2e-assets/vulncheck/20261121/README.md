# vulncheck evidence — 2026-11-21 (darwin/arm64)

Command (from the g3-hardening worktree, module `go_hive/go-hive`):

```bash
bash ../scripts/vulncheck.sh --report ../docs/e2e-assets/vulncheck/20261121
# equivalent: cd go_hive/go-hive && make vulncheck
```

Tool: `govulncheck@v1.10.0` (installed by the script into `~/.cache/hive-vulncheck/bin`,
isolated GOBIN — the repo's `go.mod`/`go.sum` are untouched), DB `https://vuln.go.dev`
last modified `2026-11-11T02:51:11Z`, scan mode `source` / level `symbol`,
local toolchain `go1.26.5` (also the `go` directive in `go_hive/go-hive/go.mod`).

## Verdict: **FAIL** (exit 1)

`govulncheck` reports **23 unique OSV ids / 1,386 finding events** across both modules:

| module | unique OSVs | events | SBOM modules |
| --- | --- | --- | --- |
| go-hive | 23 | 778 | 13 |
| browserbridge | 23 | 608 | 2 |

All 23 are **Go standard library** issues in the go1.26 line, fixed in
`go1.26.6` / `go1.26.9` / `go1.26.10` / `go1.26.11`; no third-party module
finding was traced. Full list: `SUMMARY.md`; machine-readable: `summary.json`.

Remediation (not applied in this lane — it changes the pinned toolchain):
rebuild with a patched toolchain (`go >= 1.26.11`, i.e. bump the `go` directive
in `go_hive/go-hive/go.mod` out of the approval-gated dependency freeze),
then re-run `make vulncheck`; the gate stays red until then.

## Files

| file | sha256 | note |
| --- | --- | --- |
| `govulncheck-go-hive.json.gz` | `4f82f3054bab60c5e48d88da39333f0b986dbd54fb1cdb2fe4856c2fb755a37b` | raw govulncheck JSON (gzipped); decompressed sha256 `732e791ac338e7a7d552183544bb57d32dd07dfc2d261ee25d28a6464445d7e8` (also in `result-go-hive.json.raw_sha256`) |
| `govulncheck-browserbridge.json.gz` | `792f9f603148bff9ab19480967c99c4d95154f3a811252f11997beb96e57b84b` | raw govulncheck JSON (gzipped); decompressed sha256 `267b7a0721fd207540f0af9b8ad19f0d3c3cc2f041da2e75b21c7bc5a3ee4364` |
| `result-go-hive.json`, `result-browserbridge.json` | — | parsed per-module findings (unique OSVs + event traces) |
| `summary.json` | — | aggregate report (verdict, unique OSVs, per-module facts) |
| `SUMMARY.md` | — | human-readable verdict + OSV table |
| `offline-fallback-demo.log.txt` | — | `--offline` run on the same tree: consistency review passes, exit **3 = UNVERIFIED** (no CVE claim) |
| `binary-scan-slot18.log.txt` | — | `govulncheck -mode=binary` on the shipped `dist/slots/20261121-18/hive`: **18 stdlib vulnerabilities reachable in the binary**, exit 3; 3 module-level + 1 package-level stdlib advisories are present but not called |
| `binary-scan-slot18-verbose.log.txt` | — | same binary scan with `-show verbose` (lists the package/module-level advisories) |

## Not verified here

* GitHub Actions (`go-vulncheck` job in `.github/workflows/ci.yml`) was **not
  executed** — no Actions run in this environment; only the same `make vulncheck`
  command was run locally.
* Windows / Linux toolchains and archives were not scanned.
