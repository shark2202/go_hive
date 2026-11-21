# 升级兼容门禁：首次实测证据（2026-11-21）

- 被测：**上一交付** = 发布归档 `hive-1.0.26-go.1-darwin-arm64.tar.gz`（2026-11-20 构建）解出的 `hive`；
  **候选** = 本工作树 `HEAD 4646507b` 的 `go build -trimpath ./cmd/hive`。
- 二进制：旧 `hive 1.0.26-go.1` sha256 `6a0edde9…6f56a`；新 `hive 1.0.26-go.1` sha256 `0aacfa14…7caa57`
  （版本字符串相同，代码不同；判定按行为，不按版本号）。
- 命令（仓库根）：
  ```bash
  bash go_hive/scripts/upgrade_compat_check.sh \
      --old-dir <extracted hive-1.0.26-go.1-darwin-arm64> \
      --new-dir <built head> \
      --work <scratch>/evidence-run \
      --out  <scratch>/evidence-run/upgrade-compat-report.json \
      --label "release-archive-1.0.26-go.1 -> HEAD 4646507b" --port-base 8920
  ```
- 结果：**PASS** — 3 夹具 × 19 检查 = **57 PASS / 0 FAIL / 0 SKIP**（聚合报告 `report.json`）。

## 1. 逐夹具结果

| 夹具 | 迁移写入（新增/删除/修改） | 删除内容守恒 | 二次启动 | 旧二进制 CLI 回读 | 旧二进制 serve | 迁移 marker |
| --- | --- | --- | --- | --- | --- | --- |
| v1（pre-v2 布局） | 19 / 8 / **0** | 8/8 全部按内容找回 | 0/0/1* | **0/0/0** | health 200，**0/0/0** | v2+v3+colony_sessions 全置位 |
| v2（pre-v3 布局） | 49 / 40 / **0** | 40/40 全部按内容找回 | 0/0/1* | **0/0/0** | health 200，**0/0/0** | v3+colony_sessions 置位 |
| v3（已是当前布局） | 3 / 0 / 1* | —（无删除） | 0/0/1* | **0/0/0** | health 200，**0/0/0** | 不变 |

`*` 唯一允许的修改是 `logs/hive-server.log` 追加（churn 白名单）。
SQLite 搬移（`colonies/<c>/data/tracker.db` → `tracker/tracker.db`）按**逻辑摘要**守恒：
表集合/行数/行哈希完全一致（字节因 journal 模式不同而变化，属已登记项 R-G2-2）。

## 2. 读面验证（候选二进制，`serve` + HTTP）

| 检查 | v1 | v2 | v3 |
| --- | --- | --- | --- |
| `GET /api/health` | 200 | 200 | 200 |
| `GET /api/sessions/history` | 200，迁移后的 `session_20260101_000001_aaaaaaaa`（message_count=2，last_message=`hi there`） | 200，`session_20260201_000001_bbbbbbbb`（同上） | 200，同 v2 |
| `GET /api/discover` | 200，含 `legacy_colony` | 200，含 `alpha` | 200，含 `alpha` |
| `GET /api/credentials` | 200，列出种子凭据 `upgrade_compat_key`（Fernet，Python 参考写入） | 同左 | 同左 |
| `GET /api/colonies/<c>/sessions` | 200 | 200（alpha） | 200（alpha） |

## 3. slot 门禁联动（`dist/slots` 实测，非归档面）

| 场景 | 结果 |
| --- | --- |
| `release_slot.sh 20261121-18`，上一 slot = `20261121-16`（真实上一交付二进制） | `last_status=ok`、`last_compat=ok`，`manifest.upgrade_compat.status=ok`，`compat.json` verdict=PASS（3 用例 57 检查） |
| 故障注入：把 `20261121-18/hive` 换成“拒绝 serve”的 stub 后再出 `20261121-20` | `last_status=compat_failed`、`last_compat=compat_failed`、`manifest.upgrade_compat.status=compat_failed`，脚本退出 1；报告 9 FAIL（每夹具 M11-1 旧 CLI 退出码、M12 旧二进制看不到会话、M13-1 旧 `serve` health）——正是“旧版本在新 home 上不可用”这一类回归 |

证据：`slot-gate/cadence-ok.json`、`slot-gate/cadence-compat_failed.json`、
`slot-gate/slot-20261121-{18,20}-{manifest,compat}.json`。

## 4. 契约自洽门禁（`make compat-check`）

```
[PASS] C1 env/config manifests self-consistent — 124 env names, inventory 124; config schema_version=1
[PASS] C2 env audit counts reproduce the pinned expectation — {common:83, go:118, go_only:35, python:89, python_only:6}
[PASS] C3 configuration.json reader anchors resolve — 134 config keys, every reader anchor resolves
[PASS] C4 storage contract self-consistent — 18 file classes, fields/presence vocabulary ok, inventory partition ok
[PASS] C5 DB schema versions agree (python vs go) — tracker schema_version=3 both sides; bootstrap tables=[_tracker_changes,_tracker_meta,_tracker_registry]
[PASS] C6 API routes frozen (export_api_routes --check)
[PASS] C7 API response shapes <-> proto (check_api_proto)
[PASS] C8 tool definitions frozen (freeze_tool_contract check) — 54 reference tools, 103 go-only tools
[PASS] C9 api-responses corpus self-consistent — 77 endpoints, index/diff/files agree
[PASS] C10 tool-face extras (tool-surface/browser-tools/tool-output) — 3 checks clean (uv)
compat-check: pass=10 fail=0 skip=0 verdict=PASS
```

门禁自测：`bash go_hive/scripts/upgrade_compat_check.sh --self-test` → 5 个合成场景
（clean / health 500 / 用户文件被改 / 删除未守恒 / 旧二进制写入）判定全部符合预期，
聚合退出码 1（有注入回归时）。

## 5. 观察与结论

1. **发布归档 1.0.26 的二进制不含启动迁移**（在 v2 home 上 serve 不写 `.migrated-v3`）。
   因此 v3 夹具由候选二进制补迁移，`case.json.v3_prepared_by = "new (previous binary has no startup migration)"`。
   这正是门禁要暴露的事实：上一交付不具备迁移能力时，“升级”完全由候选承担。
2. `hive colony list` 仍会把 `colonies/.migrations` 当作 colony（G2 已登记 F4，
   Python 同行为）；不影响读面判定，但展示层风险仍在。
3. `colonies/.migrations/tracker/tracker.db` 会被启动时的 tracker backfill 创建
   （对 `.migrations` 目录也建库）；属新增文件，不破坏数据，建议后续连同 F4 一起处理。
4. 除上述两点外，未发现任何数据丢失/覆盖/不可读：v1/v2 迁移的删除项全部按内容
   在迁移后树中找回，旧二进制回读与回滚 serve 均 **0 写入**。