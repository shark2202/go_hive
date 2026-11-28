# 一致性矩阵与证据追踪

> 适用：`hive`（Python 参考）↔ `go_hive`（Go 移植）
> 产物：`go_hive/docs/consistency-matrix.{json,html}`、`go_hive/docs/consistency-evidence-report.{json,md,html}`
> 生成：`go_hive/scripts/gen_consistency_matrix.py`、`go_hive/scripts/verify_consistency_evidence.py`

## 1. 三轴分开

矩阵把“一致”拆成三个不可互相替代的轴：

| 轴 | 回答什么 | 证据来源 | 不能证明什么 |
| --- | --- | --- | --- |
| **static** | Python 包/文件/函数在 Go 侧有没有对应物 | `package-parity.json`、`function-parity-matrix.json`、`contracts/module-inventory.json` | 行为是否一致；AST 函数匹配只是结构证据 |
| **contract** | 对外面（API/CLI/env/config/tool/skill/event）是否一致 | `go_hive/contracts/*.json`、`docs/coverage-registry.json` | 运行时行为；计数比较不是执行结果 |
| **behavior** | 已登记的行为、事件、缺口、测试引用 | C12 登记册、coverage registry、`// Parity:` 注解 | 测试是否真的跑过（除非另附执行证据） |

> 一条矩阵行显示 `aligned`，只表示**该行列出的证据**支持这个结论；不是整包逐行等价证明。

## 2. 证据模型

`consistency-matrix.json` 的 `evidence_catalog` 是证据索引。每条的稳定 ID 就是 trace id。

### 2.1 ID 语法

```
artifact:<name>          # 冻结/生成的输入文件，带 sha256
pkg:<python_package>     # package-parity 的一行
c12:<python_path>        # C12 登记册的一个模块
contract:<id>            # 一条契约行（api/cli/env/config/tools/skills/events/c12）
behavior:<name>          # 行为登记（c12/events/known-gaps/test-mapping）
```

行通过 `evidence_refs` 指向这些 ID；引用可带 `#selector` 片段，验证器按 `#` 前的基 ID 解析。

### 2.2 证据类型

| kind | 字段 | 验证方式 |
| --- | --- | --- |
| `artifact` | `path`、`sha256`、`bytes` | 文件存在；字节数、SHA-256 与矩阵一致 |
| `package` | `source`、`selector` | 源 artifact 可读；`pairs[py=<name>]` 可解析 |
| `c12-module` | `python_path`、`go_counterpart`、`status` | Python 文件存在；Go 对应物（若声明）存在 |
| `contract` | `source`、`selector`、`claimed_status` | 源 artifact 存在；行级引用可解析 |
| `behavior-registry` | `source`、`selector` | 源 artifact 存在 |

### 2.3 报告新增的追踪字段

`consistency-evidence-report.json` 不进入矩阵的确定性产物，因此可以带时间戳与提交：

```json
{
  "generated_at": "2026-11-28T03:40:00+00:00",
  "git_commit": "…",
  "matrix": {"path": "go_hive/docs/consistency-matrix.json", "sha256": "…"},
  "status": "ok",
  "totals": {"evidence": 321, "by_status": {"ok": 321}, "rows": 34, "dangling_refs": 0},
  "evidence": [{"id": "artifact:package-parity", "kind": "artifact", "status": "ok", "producer": "…"}],
  "codegraph": {"status": "ok", "index": {…}, "symbols": [{…}]}
}
```

## 3. 状态词

| 状态 | 含义 |
| --- | --- |
| `ok` | 证据可解析且哈希/锚点与矩阵一致 |
| `drift` | 文件缺失、SHA/字节变化、anchor 缺失、selector 不存在、引用悬空 |
| `not_requested` | 未启用 CodeGraph 检查 |
| `skipped` | CodeGraph 不可用；矩阵本身仍可验证 |

`--check` 在 `drift` 时退出 1；`skipped` 不阻断。

## 4. 维护流程

### 4.1 刷新

```bash
# 1) 先刷新上游静态/登记产物（按改动面选择）
uv run --no-project python3 go_hive/scripts/gen_function_parity.py
uv run --no-project python3 go_hive/scripts/gen_package_parity.py

# 2) 重建矩阵 + 证据报告
make -C go_hive/go-hive consistency-matrix

# 3) 校验可复现与证据可解析
make -C go_hive/go-hive consistency-matrix-check
```

等价命令：

```bash
uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --write
uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py --check
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --check
```

### 4.2 带 CodeGraph 锚点

```bash
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py \
  --codegraph --write
```

会额外检查 13 个关键概念符号（AgentLoop、ToolRegistry、CredentialStore、SessionManager、ColonyRuntime、SkillsManager、EventBus、ReminderHub、MCPClient、PlaybookRun、TriggerDefinition、MemoryFile，以及 Python-only 的 Orchestrator）是否在 CodeGraph 索引中可解析，并记录 `file:line`。

### 4.3 CI

`make -C go_hive/go-hive consistency-matrix-check` 是纯静态、可离线运行的。它依赖：

- `go_hive/docs/package-parity.json`
- `go_hive/docs/function-parity-matrix.json`
- `contracts/module-inventory.json`
- `go_hive/contracts/*.json`
- `go_hive/docs/coverage-registry.json`

任一输入变化而矩阵未重建，`--check` 会失败。

## 5. 新增证据源

1. 在生成器里把新输入加入 `input_paths` / `artifact_ids`，并生成稳定的 evidence ID。
2. 给相关行加 `evidence_refs`。
3. 在 `verify_consistency_evidence.py` 的 `PRODUCERS` 里补 `producer` / `reproduce`。
4. 如果引入新的 `kind`，在 `check_entry` 增加验证分支，并在本文 §2.2 登记。
5. 跑 `consistency-matrix` + `consistency-matrix-check`，确认报告 `status=ok`。

## 6. 边界

- **结构/契约计数 ≠ 行为等价**。矩阵是证据索引；真正的行为等价来自 C12 行、coverage registry 和实际执行的测试/探针。
- **行为轴不执行测试**。`verify_consistency_evidence.py` 只验证“证据存在且未漂移”，不重新跑 `go test` 或 Python pytest。
- **CodeGraph 是可选加速器**。没有 `codegraph` 时验证器 `skipped`，矩阵仍可校验。
- **未计算面**：`tools/src/aden_tools` 的 866 个 MCP tool 与 `core/frontend` 仍不在 C12 分母内；矩阵会显式标注 `not_computed`，不冒充 `unported=0`。
- **文档漂移以代码/最新账本为准**：`credentials-architecture.html` 的 C2/C3/C6、`alignment-differences.md §6` 的 C12 数字、`tool-definitions.json.statistics` 的 round-1 快照都可能过期；以 2026-11-28 的 `module-inventory.json`（239/24/10/0）和 `statistics.live_recheck` 为准。
