# browser-final-parity fixtures

Raw output of the final browser-parity round (2026-11-22, branch
`browser-final-parity`, lane baseline `e3a531a3`; main had advanced to
`3bea069f` with no overlapping browser files when the round closed).

| File | What it is |
| --- | --- |
| `dual-run.txt` | One run of `uv run --offline --project core python go_hive/scripts/e2e_browser_final_parity.py`: the upstream Python panel handlers vs the real Go e2e bodies, plus 24 CLI validation cases (upstream handlers / argparse vs the built `hive-browser`), each with its exit code. Both sections end `MATCH`. |
| `e2e-dump.json` | The Go side of the panel comparison — the `contexts` / `tabs` bodies captured from the real server (`E2E_DUMP=1`) with the fake extension attached. Reused by `--skip-e2e`. |

What the dual run proves:

1. **`isHiveGroup` value level** — with the same fixture the fake extension
   feeds the Go e2e (profile `e2e-profile` → Chrome group 777, tab 101 inside
   it, tab 102 ungrouped), `routes_workers.handle_browser_tabs` and
   `GET /api/browser/tabs` return byte-equal JSON after decode:
   `isHiveGroup:false` and `hive_group_count:0` everywhere. Upstream is
   always-false because it reads `ctx.get("groupId")` from
   `list_active_contexts()` rows keyed `group_id` (`routes_workers.py:341-360`
   vs `lifecycle.py:127-135`); the Go route now mirrors that outcome instead
   of enriching from the bridge registry.
2. **evaluate/script exit-6 conditions** — the real `cmd_evaluate` /
   `cmd_script` handlers (and the real argparse parser for `--timeout-s`) vs
   the built `hive-browser` binary, both with no connected extension, over the
   same case list: missing/blank JS, `@empty` / empty stdin, missing
   `--skill`/`--script`, `--args` JSON/对象 errors, whitespace-only values,
   `-`/`@file` raw-value handling, `--browser-profile` validation against a
   small live fake bridge, and the argparse-order `--timeout-s` case. All 24
   exit codes match; message texts differ only where Python and Go error
   strings are inherently different (details in `dual-run.txt`).

Reproduce: `bash go_hive/scripts/e2e_browser_panel.sh` for the bare e2e, or
the command above for the full report (needs `go` on PATH; everything runs on
ephemeral ports and temp dirs, the resident bridge on 14829–14831 is never
touched).

## Decision update (2026-11-22 深夜，用户拍板"选择最优解")

第 1 项（`isHiveGroup`）**最终决策反转为保留 Go 真富化**：上游的恒 false 源于其自身键错
（`groupId` vs `group_id`），镜像 bug 会让面板富化名存实亡。因此本目录里面板段的
`MATCH` 结论视为**该轮的历史证据**；当前期望是 **panel 段 MISMATCH by design**
（登记为有意改进，复查条件=上游修复键后按 parity 复盘）。CLI 段 24 例 `MATCH` 不受影响，仍为现行断言。
