# docs-go-crosswalk：上游架构文档 → go_hive 映射报告

> 车道：`docs-go-crosswalk`（worktree `.wt/docs-go-crosswalk`，base `main@949e39b2`）。
> 日期：2026-11-23。范围：`go_hive/02-architecture.md`、`03-agent-loop.md`、`04-colony-runtime.md`、`05-tools-and-integrations.md`、`07-memory-and-skills.md`、`08-storage-and-data.md`（06 不在范围）。
> 目的：这 6 篇描述的是**上游 Hive（Python 参考实现）**；本车道给每篇加统一映射块、修正陈旧数字/口径，并加一个可重放的文档路径校验器。

## 1. 映射表

| 文档 | 主题 | go_hive 对应实现（已验证存在） | 状态 |
| --- | --- | --- | --- |
| `02-architecture.md` | 分层架构 / 消息生命周期 / 并发 / 代码地图 | `internal/server`、`internal/agentloop`、`internal/colony`、`internal/host`、`internal/tools`（Queen 侧另有 `internal/queen`） | 已移植 |
| `03-agent-loop.md` | AgentLoop 回合循环 / 工具执行 / judge / 压缩 / 停泊 | `internal/agentloop`（循环主干与 `internals` 等价物）、`internal/server/chat_runtime*.go`（会话装配） | 部分：循环主干已移植；judge J3/J4 等残差 |
| `04-colony-runtime.md` | ColonyRuntime / Worker / Tracker / Playbook / Triggers / Sentinel | `internal/colony`、`internal/host`、`internal/playbook`、`internal/triggers` | 部分：24 维对齐 12 已对齐 / 10 部分 / 2 未验证 |
| `05-tools-and-integrations.md` | 内置工具 / MCP / 浏览器桥 / 集成目录 | `internal/tools`、`internal/mcpregistry`、`browserbridge`（独立 Go module，`go_hive/browserbridge`） | 部分：工具/MCP 已对齐；第三方集成多为无凭据未验证 |
| `07-memory-and-skills.md` | 记忆作用域 / 反思 / 召回 / LongMemEval / Skill / Soul | `internal/memory`、`internal/soul`、`internal/skills` | 已移植（LongMemEval 无 Go 分数，属外部验证缺口） |
| `08-storage-and-data.md` | HIVE_HOME / 会话存储 / SQLite / 迁移 / 保留清理 | `internal/storage`、`internal/hivehome`、`internal/maintenance` | 已移植 |

包路径用 `go list ./internal/...` 与目录存在性双重确认（2026-11-23，worktree 内）。`internal/storage/hivehome/maintenance` 这种嵌套路径在仓库中不存在；实际是三个并列包 `internal/storage` / `internal/hivehome` / `internal/maintenance`，文档按真实布局写。

## 2. 修改清单

| 文件 | 修改 |
| --- | --- |
| `go_hive/02-architecture.md` | 标题下加三行映射块；§3.1 `7,497 行` → `约 7,500 行（2026-11-23 实测 7,501）`；§7 补“Go 侧没有等价的惰性导出表；公开面是 Go 包路径，SDK exports 基准属待拍板 D2” |
| `go_hive/03-agent-loop.md` | 标题下加三行映射块；文件注记与 §12 的 `7,497 行` 同步改为 `约 7,500 行（2026-11-23 实测 7,501）`（与 02 同一实测口径，避免同文档自相矛盾） |
| `go_hive/04-colony-runtime.md` | 标题下加三行映射块（status 引用 `docs/alignment-differences.md` 的 24 维结果） |
| `go_hive/05-tools-and-integrations.md` | 标题下加三行映射块；§3 标题改为“集成目录（上游 104 个）”；§3 增 Go 侧口径：104 是上游目录数，Go 侧按 `contracts/tool-definitions.json` 工具契约计（reference 59；会话面 present 48/59，round 9），集成实现按 action 分组在 `internal/tools/integrations`，不承诺目录一一对应；§9 两处“104 个集成”限定为“上游 104 个集成” |
| `go_hive/07-memory-and-skills.md` | 标题下加三行映射块；§1 标题“三层结构” → “三层作用域（global / colony / agent）”，正文补“queen 与 worker 同属 `agents/` 下细分”；§6 同口径替换“global / colony / queen” |
| `go_hive/08-storage-and-data.md` | 标题下加三行映射块（08 原有 Go 注记保留） |
| `go_hive/scripts/check_doc_paths.py` | 新增轻量校验器（stdlib）：抽取 6 篇中反引号包裹的 `core/framework/...`、`core/frontend/...`、`internal/...` 路径，忽略 glob 与 `(...)`/`::`/`:line` 装饰后缀；`internal/...` 相对 `go_hive/go-hive/`、`core/...` 相对仓库根；`--check` 有断链退出 1 |
| `go_hive/docs/e2e-assets/docs-go-crosswalk/REPORT.md` | 本报告 |

## 3. 校验器输出

```text
$ python3 go_hive/scripts/check_doc_paths.py
docs: 6
checked paths: 47
missing: 0

$ python3 go_hive/scripts/check_doc_paths.py --check
docs: 6
checked paths: 47
missing: 0
exit=0
```

负向自测：临时在 `02-architecture.md` 追加不存在的 `` `internal/__doc_path_probe__` ``，`--check` 报 `missing: 1` 并退出 1；恢复后重新 exit 0（未提交该探针）。

改动前基线也为 `missing: 0`（27 条路径），即 6 篇原有路径没有断链；本次新增引用全部可解析，无需在报告中登记“原有断链”。

## 4. 口径说明（与任务描述的两处偏差）

- **05 的“47”**：任务描述写“内置 47”。仓库当前权威口径是 `docs/tool-contract-diff.md` §18（round 9，2026-11-23）的 **48/59**；`47` 是 §17 的旧口径（`present ≥ 47` 断言仍保留）。文档写 48/59 并注明 round 9，避免引入新的陈旧数字。
- **08 的 `internal/storage/hivehome/maintenance`**：该嵌套路径不存在。按 `go list` 结果写成 `internal/storage`、`internal/hivehome`、`internal/maintenance` 三个真实包。

## 5. 门禁记录

| 检查 | 结果 |
| --- | --- |
| `uv run python -m py_compile go_hive/scripts/check_doc_paths.py` | exit 0 |
| `python3 go_hive/scripts/check_doc_paths.py --check` | exit 0（47 条，missing 0） |
| `gofmt -l internal/ cmd/`（`go_hive/go-hive`） | 空输出（本车道未改 Go） |
| `bash go_hive/scripts/merge_postcheck.sh` | exit 0 |
