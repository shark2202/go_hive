# Observability dual-run evidence (2026-11-21)

Raw samples backing `go_hive/docs/observability-surface-diff.md` (appearance dimension 16).
Baseline: `main` f8aa5fe9 · Python hive 1.0.14 · Go 1.0.26-go.1.

| File | What it is |
|---|---|
| `python-hive-server-default.txt` | Python `$HIVE_HOME/logs/hive-server.log`, default human format (ANSI is in the file) |
| `python-hive-server-json.txt` | Same file with `LOG_FORMAT=json` (`ENV=production` is equivalent) |
| `python-sentinel-log.txt` | Python `$HIVE_HOME/logs/sentinel.log` with `HIVE_SENTINEL_LOG=1` |
| `go-hive-server-default.txt` | Go `$HIVE_HOME/logs/hive-server.log`, default (after the timestamp alignment) |
| `go-sentinel-log.txt` | Go `$HIVE_HOME/logs/sentinel.log` with `HIVE_SENTINEL_LOG=1` |
| `runtime-logs-ring.txt` | `GET /api/runtime-logs` entry text on both sides + the known differences |
| `rotation-probe.txt` | Python 21 MB rotation probe sizes + the Go rotation tests that pin the same parameters |
| `watchdog-fault-injection.txt` | Fault-injected watchdog runs: 7 alert classes, osascript invocations, debounce, and the two defects found |

Generation (all in throwaway `HIVE_HOME`s; no repo state touched):

```bash
cd core && HIVE_HOME=<tmp> uv run --offline hive serve --port 18787
cd go_hive/go-hive && CGO_ENABLED=0 go build -o /tmp/hive-go ./cmd/hive
HIVE_HOME=<tmp> /tmp/hive-go serve --port 18788
# then: ls <tmp>/logs; head -n3 <tmp>/logs/hive-server.log | cat -v
```

The watchdog transcript was produced by running the real `go_hive/scripts/watchdog_report.sh`
against a throwaway git fixture with `WATCHDOG_REPO` / `WATCHDOG_PORT` / `WATCHDOG_STATE` /
`WATCHDOG_REPORT` / `WATCHDOG_SERVE_PATTERN` / `WATCHDOG_ONCE=1` overrides; `osascript`
was either stubbed (to record invocations) or the real binary.

Keep these files small: they are samples, not full logs.