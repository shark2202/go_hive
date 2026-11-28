# Testing framework port (`core/framework/testing`)

C12 status: **8 / 8 ported** — `testing` has no `unported` entries and no recorded
behavioural divergence. The seven modules that were the last unported files are
ported into `go_hive/go-hive/internal/testframework`; `llm_judge.py` stays in
`internal/llmjudge` (product path `skills/cli.py`).

## Module map

| Python | Go | Notes |
| --- | --- | --- |
| `test_case.py` | `internal/testframework/test_case.go` | `Test`, `ApprovalStatus`, `TestType`, approval/execution transitions, wire format |
| `test_result.py` | `test_result.go` | `TestResult`, `TestSuiteResult`, `ErrorCategory`, summary/report helpers |
| `test_storage.py` | `test_storage.go` | JSON store, four indexes, result history + `latest.json`, stats |
| `categorizer.py` | `categorizer.go` | pattern tables, precedence, confidence, fix suggestions, iteration guidance |
| `debug_tool.py` | `debug_tool.go` | `DebugInfo`, `DebugTool`, optional Runtime loader, failure summary |
| `approval_types.py` | `approval_types.go` | `ApprovalAction`, request/result/batch types and validation messages |
| `approval_cli.py` | `approval_cli.go` | interactive review, batch approval, `$EDITOR` fallback |

`internal/cli/test_approval.go` is the only production caller: a **hidden**
`hive test-approval` dev harness. Upstream ships these modules as library
functions with no registered CLI entry (the `__init__` docstring names
`test-run`/`test-debug`/`test-list`, but none exists in `framework/cli.py`), so
the harness is a Go-side mount, not a ported user-facing command. It is
`Hidden: true`, which keeps the frozen C11 CLI surface unchanged, and it
registers no model tool.

## Preserved behaviour

- **Types and wire format.** Field names are the upstream snake_case names;
  `test_type` keeps the `outcome` value for `SUCCESS_CRITERIA`; pydantic
  `extra="allow"` is reproduced with a preserved `Extra` map on `Test`,
  `TestResult`, and `TestSuiteResult`.
- **Timestamps.** Upstream writes naive local `datetime.isoformat()` values
  (`2026-11-26T10:20:30.123456`, microseconds only when non-zero). `Timestamp`
  emits that shape instead of Go's RFC3339 form so Python readers see the same
  documents.
- **Approval transitions.** `Approve`, `Modify` (keeps `original_code`),
  `Reject`, `RecordResult`, `IsApproved`, `PassRate` (None-equivalent `(0,
  false)` before the first run).
- **Storage.** `{base}/tests/{goal}/{id}.json`, `indexes/{by_goal,by_approval,
  by_type,by_criteria}`, `results/{id}/{timestamp}.json` + `latest.json`,
  `suites/`. Index arrays use Python's `json.dump` separators (`["a", "b"]`, no
  trailing newline). `UpdateTest` moves the `by_approval` entry when the status
  changes and refreshes `updated_at`; `DeleteTest` removes index entries and
  the result directory.
- **Categorizer.** The full upstream pattern tables, case-insensitive, with the
  same precedence (logic → implementation → edge → default implementation),
  the same confidence formula (1.0 pass, 0.3 no match, `0.5 + share*0.4` capped
  at 0.9), the same `msg` log key, fix suggestions, and iteration guidance.
- **Debug reports.** `DebugInfo` carries test definition, actual output,
  error/category/stack, logs, runtime data, fix suggestion, and iteration
  guidance. A missing test is reported inside the report (`test_name:
  "unknown"`, "Test … not found in goal …"), matching upstream. The Runtime
  loader is optional and uses `load_run_sync` semantics; load failures and
  missing runs become the upstream `runtime_data.error` strings.
- **Approval CLI.** `InteractiveApproval` renders the same review page
  (`[i/total]`, type, criteria, confidence, description, input/expected JSON,
  code block, `[a]pprove  [r]eject  [e]dit  [s]kip`), re-prompts on invalid
  choices with the upstream message, defaults an empty rejection reason to
  "No reason provided", treats an unchanged editor buffer as a plain approval
  ("No modifications made"), leaves skipped tests pending, and falls back to
  the original code when `$EDITOR` fails or is missing. `BatchApproval` keeps
  the upstream counts and messages, including the `"Test %sd successfully"`
  wording (`modifyd`/`rejectd`).

## Go-side decisions (not upstream)

- **Concurrency.** Upstream `TestStorage` has no locking. The Go store holds an
  `RWMutex` around every read-modify-write and writes through
  `internal/atomicfs`, so parallel saves cannot drop an index entry and readers
  never see a half-written document. This is in-process only; there is no
  cross-process file lock (same as upstream, which assumes one process).
- **Test seams.** `Now` is a package-level clock, and `InteractiveApproval`
  takes explicit `io.Reader`/`io.Writer`/editor seams; upstream monkeypatches
  `datetime.now()` and reads `input()` directly.

## `hive test-approval` (hidden dev harness)

```
hive test-approval --root DIR [--goal GOAL]                  # interactive review
hive test-approval --root DIR --batch REQUESTS.json          # BatchApprovalRequest
hive test-approval --root DIR --goal GOAL --debug TEST_ID
```

Exit codes: `0` completed with no error results; `1` storage failure, missing
test, unreadable/invalid batch file, or an error result; `2` usage error
(missing `--root`, `--debug` without `--goal`, `--batch` + `--debug`, or a
`--goal`/batch-goal mismatch).

## Tests

| File | Covers |
| --- | --- |
| `test_case_test.go` | schema/defaults, approve/modify/reject/record conversions, timestamp wire shape, extras round-trip, enum/range validation |
| `test_result_test.go` | result/suite constructors, summary dict, pass rate, category filtering, validation, extras |
| `test_storage_test.go` | save/load/delete, index contents and transitions, result history ordering/limit, stats, corrupt test/index/result documents, concurrent saves and status flips |
| `categorizer_test.go` | passed/logic/implementation/edge categories, stack-trace and log-derived text, full pattern families and precedence, confidence, suggestions, guidance |
| `debug_tool_test.go` | missing-test report, full report fields, category/fix suggestion, runtime loader shapes, failure summary buckets |
| `approval_types_test.go` | `validate_action` messages, result constructors, batch request JSON, action enum |
| `approval_cli_test.go` | interactive approve/reject/modify/skip, invalid choice, EOF, storage error results, batch counts/messages/skip persistence |
| `internal/cli/test_approval_test.go` | flag parsing, conflicting modes, review exit codes, batch/debug modes, hidden-from-help |

The upstream reference tests are `core/tests/test_testing_framework.py`; Go
tests that mirror one of its functions carry a `// Parity:` annotation, and
Go-only guards (concurrency, corrupt storage, CLI exits, wire format) carry
`// Parity-exempt:` with the reason.
