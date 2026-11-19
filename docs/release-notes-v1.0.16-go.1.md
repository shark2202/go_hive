# 发布说明：hive 1.0.16-go.1

> 发布包版本：`hive 1.0.16-go.1`（Go 模块 `github.com/aden-hive/hive-go`）
> 对应上游：`aden-hive` / `aden-tools` 1.0.14（提交 `c6c2cdcf`）
> 构建日期：2026-11-19
> 构建环境：macOS 26.7.1（darwin/arm64）、Go 1.26.5、`CGO_ENABLED=0`

这是 `1.0.15-go.1` 之后的行为对齐版本：把八层 runtime spine 里原本空着的**方法层、自治层、沉淀层、能力边界**补上了生产路径，同时修掉多项“实现错了”的缺陷。

## 1. 相对 1.0.15-go.1 的变化

**组织层（colony / worker）**
- `run_worker` 闭环：Queen 按需派 worker、worker 完成回传 `[WORKER_REPORT]`（含 `duration_seconds`、`output_file`）。
- worker 身份修复：fork 的 `worker.json.system_prompt` 改为 worker 定义，不再写入 Queen persona；`worker.json.tools` 真实生效（分类展开为具体工具，拒绝 queen 生命周期工具）。
- worker 转录落盘：`workers/<id>/conversations/parts/*.json`、`meta.json`、`result.json`、`events.jsonl`；`resume_worker_ids` 可基于磁盘转录续跑，无转录时明确 unsupported。
- overseer 工具面：`run_worker` + tracker + task 工具，持久循环消费注入；`serve --colony` 预载会话拥有 ChatRuntime，可对话、可触发、可派活。
- colony fork 目录补齐 `tracker/` 运行库（启动时创建）。

**方法层（skill / playbook）**
- `write_skill` / `skill_view` / `list_skills`：colony 与 queen 两个 scope，路径与名称校验，worker 只读。
- `run_playbook`：Python `.play.py` **不能执行**，返回 `dispatched=0` 与明确原因，不伪造成功；Go 原生 JSON playbook 走真 runner（读 tracker 未完成行、派 worker、并发/重试/退避、断点续跑），运行日志写 `<colony>/colony.db` 的 `playbook_runs`。

**自治层（trigger / sentinel）**
- `QueenExecutor`/`InjectTrigger`：timer 到点注入 `[TRIGGER: type/id]` 并运行一轮；`triggers.json` 在启动/建会话/load_colony/fork 四处恢复，`StopSession` 清理。
- Sentinel：`NewManager` 接入运行时，`ask_user` 停车在 idle tick 分类（continue→nudge、done→完成报告、needs_human→`colony/escalations/<id>.json` + 现有 notifier）；无凭据只落盘不伪造发送。

**沉淀层（memory / soul）**
- reflection 成功后自动沉淀 soul：只 append、内容重复不写、满区只淘汰自己写过的条目；路径校验与跨 queen 隔离；`GET /api/queen/{id}/soul` 兼容读取。

**能力边界（MCP）**
- `hive serve` 启动时连接 MCP 服务器、拉 `tools/list`、以 `mcp__<server>__<tool>` 命名空间注册进会话与 worker 工具面，可执行；不可达服务器降级不影响启动；`GET /api/mcp/servers` 显示连接状态与工具数，`POST /api/mcp/reload` 支持重载。

**质量与治理**
- 七道门禁；新增概念接线审计与八层 runtime spine 审计。
- 看门狗升级：六类异常告警（health / serve / 门禁 / `go_hive` dirty / 台账停滞 / HEAD 停滞）+ macOS 通知 + 15 分钟去抖；两个守护状态文件分离。

## 2. 质量证据

| 项 | 结果 |
| --- | --- |
| 测试声明数（parity 口径） | **4,395** |
| `check_parity.sh` | **4395/4395**，0 missing |
| `check_parity_targets.sh` | **4,645** 条引用（3,674 test + 971 prod），0 missing |
| 七道门禁 | 全部退出码 0 |
| QC 全量 | **8/8 PASS** |
| 冒烟 | **13/13 PASS** |

## 3. 已知差异（不隐藏）

- **Python playbook 不能执行**：Go runner 是原生的，进程内无 Python 解释器；`.play.py` 明确 unsupported。
- **Anthropic / MiniMax 不流式**：OpenAI-compatible 逐帧；两者明确回退非流式。
- **Sentinel 无 live E2E**：分类与升级落盘有测试，尚未在生产环境跑过完整“停车→nudge→升级”链路。
- **第三方集成未验证**：81 个需凭据模块仍标「未验证」。
- **在线漏洞库未比对**：`govulncheck` 不可装。
- **overseer 与 chat Queen 双面并存**：worker 报告进入已绑定 colony 的 chat 会话；overseer 自身不订阅报告。
- **仍缺**：`search_messages`、`stall_check`、并行只读工具、预算执行、per-queen 工具白名单。

## 4. 安装与校验

```bash
tar -xzf hive-1.0.16-go.1-darwin-arm64.tar.gz
./hive --version        # hive 1.0.16-go.1
shasum -a 256 -c SHA256SUMS
```

## 5. 升级说明

从 `1.0.15-go.1` 升级无需迁移。新增目录：`<colony>/tracker/tracker.db`、`<colony>/workers/`、`<colony>/skills/`、`<queen>/soul/sediment.json` 会在首次使用时创建。
