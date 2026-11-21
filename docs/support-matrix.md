# 支持矩阵

> 版本：`hive 1.0.26-go.1`
> 快照：2026-11-19，分支 `w9/release` 工作树（发布包构建快照）。
> 状态定义：
> - **已实现**：代码存在，且本次文档验证或仓库测试实际跑过；
> - **部分**：有实现，但只覆盖上游能力的一部分，或没有接到用户入口；
> - **未实现**：这一版没有对应代码；
> - **未验证**：有代码/测试，但没有对真实外部系统跑过（没有凭据或没有环境）。
>
> 证据列里的 `实测` 指本次发布任务用真实二进制跑过；`测试` 指仓库内 Go 测试；`代码` 指只读了实现，没有运行。
>
> **2026-11-21 增量**：Python `.play.py` playbook 已从「unsupported」改为「已实现（需外部 python3）」——外部解释器 + stdio JSON-RPC，脚本字节不变；见 §1 对应行与 `docs/playbook-python.md`。
>
> **2026-11-21 增量 2（G2）**：新增「老用户无损替换」判定（§1.1）——v1/v2 非空旧 `HIVE_HOME` 双跑数据级无损、凭据 Fernet 双向互读、Go→Python 回滚与 v1.0.25→1.0.26 升级演练均实测通过。1 项行为差异 **P0-G2-1（v1 `exports/` 解析基准：Python=CWD、Go=`$HIVE_HOME`）待用户决策**，本次未改 exports 行为。证据：`../../docs/g2-lossless-migration.md`、`upgrade-and-rollback.md`、本页 §1.1、`g2-lossless-replacement.html`。

## 1. 总览

| 能力 | 状态 | 证据 |
| --- | --- | --- |
| 构建（无 cgo、离线） | 已实现 | `make build`、`make ci` 实测；`CGO_ENABLED=0 GOPROXY=off` |
| `hive --version` | 已实现 | 实测（发布包解压后）`hive 1.0.26-go.1` |
| `hive serve` + `/api/health` | 已实现 | 实测默认 `127.0.0.1:8787`；发布包实测 `--port 8797` 健康检查 200 |
| 老用户无损替换（旧 v1/v2 `HIVE_HOME` → Go 1.0.26） | 已实现（数据级无损；P0-G2-1 待决策） | 2026-11-21 同起点双跑：v1-rich 50/50、v2-rich 99/99 文件；JSON 字段差异 0、SQLite schema/行差异 0、读/API 探针差异 0；凭据 Fernet 双向互读；Go→Python 回滚 0 删除/0 覆盖；v1.0.25→1.0.26 升级 health 与内嵌 `/` 均 200，且迁移后旧二进制可回读。例外与决策见 §1.1 |
| 创建会话 `POST /api/sessions` | 已实现 | 实测 201，返回 `session_id` |
| 会话投递 `POST /api/sessions/{id}/chat` | 已实现 | 实测 200；无 key 时 503；未知会话 404；空消息 400 |
| 会话列表 `GET /api/sessions` / `hive session list` | 已实现 | 实测 |
| 停止会话 `hive session stop` | 已实现 | 实测成功；不存在时 404 |
| 会话转录持久化 | 已实现 | 实测 `conversations/parts/*.json` 落盘 |
| 同 id 会话恢复上下文 | 已实现，未复跑 | E2E 覆盖；本文档未单独验证 |
| 会话事件 SSE | 已实现 | 实测 `client_input_received`，20s 心跳 |
| OpenAI-compatible 传输 | 已实现 | 实测（本地假上游）：`/chat/completions` + Bearer |
| Anthropic Messages 传输 | 已实现 | 实测（本地假上游）：`/v1/messages` + `x-api-key` |
| 逐 token 模型流 | 已实现（OpenAI-compatible） | 实测 live SSE 单轮 12 帧增量；Anthropic/MiniMax 明确 unsupported 并回退非流式 |
| `run_worker` 并行 worker | 已实现 | 实测 queen 派 worker → worker 执行 → `[WORKER_REPORT]` 回传；`resume_worker_ids` 已实现（`internal/colony/worker_transcript.go`，按 worker 转录恢复；2026-11-21 按代码与本审计 GAP-11 更正旧口径） |
| worker 升级通道（doom-loop 自动升级 → `list_worker_questions` → `reply_to_worker` → worker 继续/`worker_not_active`） | 已实现 | 2026-11-21 真实 `hive serve` + 真实 colony（脚本化模型）E2E：worker 连续 3 次相同工具调用 → doom-loop 发 `ESCALATION_REQUESTED`（reason `Tool doom loop detected`，含 request_id）→ queen 会话收到 `[WORKER_ESCALATION]` → `list_worker_questions` 返回 `count=1` → `reply_to_worker` 返回 `delivered` → worker 收到 `[QUEEN_REPLY]` 后 `report_to_parent`；恰好 1 条升级记录。证据 `docs/e2e-assets/worker-escalation-e2e/escalation-chain/`，说明 `docs/worker-escalation-e2e.md`（含工具面对齐：parallel worker 无 `escalate`） |
| Playbook：Python `.play.py`（外部解释器） | 已实现（需外部 python3）；对外字段逐字对齐 | 2026-11-21 实测：`python3 3.9.6`（系统）与 `3.14.7`（`HIVE_PYTHON`）跑通 converge / 重试 + 退避 / lane 并发与速率门 / deadletter / resume；解释器按 `HIVE_PYTHON` → `python3` → `python` 查找，缺失时 `status=unsupported`、0 派发；纯 Go 二进制、无 cgo、无新依赖。**字段级对齐**：工具 payload / `started` 文案 / `[PLAYBOOK_COMPLETE]` 全文 / `playbook_runs` 行 / 0 派发 `hint` / 解析报错文案与上游逐字；`go_hive/scripts/playbook_verbatim_compare.py` 用真实上游工具 vs 真实 Go 工具跑同一夹具，5 场景 **120/120** 字段一致（`docs/e2e-assets/playbook-verbatim/REPORT.md`）；run 期间 colony 并发上限按上游 `max(原值, meta concurrency)` 临时抬高并恢复；`worker(schema=...)` 落到 spawn spec（`report_to_parent` + prompt）。**迁移**：Go 历史 `.play.json` 与上游 `.play.py` 原文件按名直接运行，0 改名 / 0 重写 / 0 用户动作（§9 迁移矩阵 + `TestPlaybookMigration*`）。真实 LLM 端到端：`TestRealLLMPlaybookVerbatimConvergeDeadletterE2E`（2 行 converge + 2 死信 + `[PLAYBOOK_COMPLETE]`），记录重放上游 **19/19** 字段一致（`REPORT-real-llm.md`）。见 `docs/playbook-python.md`；未验证：Windows 无 Python、任意第三方 import、其他 provider |
| Playbook：Go 原生 JSON | 已实现 | converge / 重试 / 退避 / 死信 / resume 有单测；同步执行；run log 写 `colony.db` 的 `playbook_runs` |
| tracker 共享账本 | 已实现 | 实测 colony 建库、worker 注入 binding、`tracker_*` 建表/upsert/query |
| 提醒中枢 | 已实现 | source 注册矩阵与上游 13 个 source 对齐：chat hub（task / colony_parallel / active_workers / tracker_snapshot / colony_worker_snapshot / searchable_tools / skills_catalog / turn_context / idle / sentinel / stream_stall）、worker hub（task / worker_tracker_snapshot / idle / stream_stall）、overseer hub（colony_parallel / active_workers / tracker_snapshot / colony_worker_snapshot / stream_stall）。`STREAM_STALLED` 由流看门狗真实触发并重跑同一 turn（真实 ChatRuntime 会话测试）；worker tracker 提醒在真实 SpawnBatch worker 转录中验证；预载 colony 会话按 `metadata.json` 的 `queen_name` 解析 scope。残余与未实现项见 `docs/reminders-coverage.md` §7 |
| 配置键 `turn_context` / `gcu_viewport_scale` | 已实现 | 实测 + 测试：`turn_context.<name>.enabled` 决定进程内注册的包是否启动并注入 queen 回合提示（queen-only、每包 5s 超时、异常跳过）；`gcu_viewport_scale`（0.1–1.0，默认 0.8）经 `server.GcuViewportScale` 注入 `HIVE_BROWSER_VIEWPORT_SCALE`，桥 `/status.viewport_scale` 实测 0.65 / 0.55（配置文件）/ 0.8（非法值回落）。上游 `get_gcu_viewport_scale` 自扩展化浏览器后无消费点，Go 侧只读并上报，不改截图/抓取几何 |
| 模型槽位与配置契约（config-env-align + config-put-hotswitch） | 部分→热切换已实测 | **已接通（代码 + 测试 + 真实模型）**：`queen_llm.<id>.*`（会话启动按 queen 选模型，她的 colony 沿用同一 provider）、`worker_llm.*`（colony 专用 worker 模型；worker 现在真正跑在该 provider 上）、`vision_fallback.*`（文本主模型的工具图片描述；真实图片 caption E2E 见 `docs/vision-caption-parity.md`）、`llm.aux_max_tokens`、`loop.max_tool_result_chars`、`worker_tools.always_enabled_categories`、`HIVE_MAX_ITER`、`HIVE_SPLIT_WORKER_EVENTS`，以及 `mcp.timeout_seconds` 优先级修复（env > file）。**热切换（2026-11-21 起，会话级）**：`llm`/`queen_llm.<id>` 写入即用 `llm.ProviderSlot`（`Reconfigure` → 同一 `NewHTTPProvider` 工厂重建）切换运行中会话，回合级 pin 保证进行中的回合用旧 provider 跑完、下一回合用新模型；`vision_fallback` 逐次 caption 重读槽位，写入即对下一张工具图片生效；`worker_llm` 与上游 `_write_role_section` 一致只对下一个 colony/新会话生效（`sessions_swapped: 0`）。实测：`go_hive/scripts/e2e_config_hotswap.py`（真实 `serve` + 两个假 OpenAI 网关：`queen-old`→`queen-new`、`caption-new` 带图、`worker-new`）+ 单测（`-race`）。**只读/不支持**：`llm.antigravity_client_id` / `llm.antigravity_client_secret` 读取点已有但无消费路径（端口走预发 access token，无 OAuth 刷新）；`channels.<name>.enabled` 不支持（Go 无渠道插件宿主）；`worker_llm.max_tokens` 两侧都没有消费点；`HIVE_TRACKER_WORKER_TOOL_CALLS`、`HIVE_HISTORY_READ_WORKERS`、`HIVE_NO_FILE_LOG`、`HIVE_COMPACTION_DEBUG`、`HIVE_PLAYBOOK_FAST_GRACE` 原列「Python 独有」已由 env-verbatim-port 车道接入（读取点/测试见 `contracts/config-support-ledger.json`；剩余 Python 独有仅 6 个 GCU 平台差异名）。残余：Go 的 pin 语义（上游原地 mutate，回合中途也会换模型）与 colony overseer 用 queen 模型的 Go 设计差异。逐键契约与计数见 `docs/env-config-contract-diff.md` |
| judge（success_criteria） | 已实现 | colony worker 带 criteria 时评估并发布 `judge_verdict`；chat 会话无 criteria 不评估 |
| stall_check（停滞检查） | 已实现 | judge 槽在 queen 回合以 `turn_done` 结束后判定「宣称要做却停手」，命中则注入一次提醒（与上游一致，不发布事件；`node_stalled` 属 doom-loop）。2026-11-20 真实 `hive serve` + 脚本化 OpenAI 兼容网关 E2E：judge true → 提醒进入下一轮模型请求与会话转录；judge false → 不提醒；开关关闭 → 0 次 judge 调用（`docs/stall-check-e2e.md`，证据 `docs/e2e-assets/stall-check-e2e/`）。threshold 缺省 0.5、数字字符串同 `float()` 语义、非法值不提醒（单测钉住） |
| 内建工具 `read_file` / `write_file` / `terminal_exec` | 已实现，未在文档中触发 | 单测覆盖；本次假模型未发工具调用 |
| 集成工具（会话可用） | 已实现 | 23 个安装器 102 模块 / 854 action，加 senders 1 模块 / 9 action，合计 **103 模块 / 863 action**；会话目录实测 113 个集成工具（默认，senders 关闭）；打开 `HIVE_EMAIL_SENDERS` 后 114 |
| 集成工具（默认关闭） | 已实现，默认关闭 | 仅 `senders`（`email_senders` / `HIVE_EMAIL_SENDERS`）；网络扫描器已与上游一致默认注册，见 §2.4 |
| 有意排除的 action | 未实现（有意） | `csv_sql`、`excel_sql` 依赖 DuckDB；纯 Go 无 cgo 不引入，见 §2.3 |
| Colony 磁盘加载 + 会话注册 | 已实现 | 实测 `serve --colony docs_probe` |
| Colony 多 worker 编排闭环 | 部分 | 包级测试；未用真实模型跑多 worker 任务 |
| 13 个默认 queen 档案 | 已实现 | 实测 `hive queen list` |
| 技能 / 记忆 / Tracker / 触发器 / 事件总线 | 部分 | 技能入口已实测：14 个 HTTP 端点，8788 上 `/api/skills`、queen 技能列表 200，create→body→patch→delete 为 201/200/200/200、删除后 404；记忆/Tracker/触发器仍以库层与单测为主 |
| Sentinel | 部分 | 8 个包移植、测试通过；未接真实 Telegram/Slack |
| Queen mail | 部分 | 正常 queen 双端已实测（2026-11-20，real LLM：发送→回复→续链至 depth 4）；Claude Code queen 运行器/MCP/回复闭环已实现并经真实 `claude -p` 验证 |
| Claude Code queens | 已实现 | 运行器 + 真实 dashboard 列表/transcript；残余：真实模型主动调 `message_queen` 未联调，配置仅在 serve 启动时读取 |
| Browser 工具 / `hive-browser` | 已实现 | 2026-11-20 实测：真实 Chrome + 仓库内 1.7.4 扩展，`hive serve` 自动拉起并守护 `browserbridge` 子进程；`hive-browser status/open/page text/evaluate/interact/snapshot/screenshot/tab` 真实驱动成功（`go_hive/scripts/e2e_browser_host.sh` 全绿，含 control RPC 14831 断言与裸桥空闲退出断言）；无扩展时 `status`/`setup` 退出 2。见 `docs/browser-host.md`。control RPC / `client_hello(owner_pid)` / 30s 空闲退出 / notify 注入 / 3 条面板代理已对齐 |
| React dashboard | 已内嵌（发布包） | `release.sh` 默认以 `-tags embed_ui` 构建；实测发布包 `/`、`/sessions`（深链）、`/favicon.png` 均 200。源码树不附带 `frontend/dist`，需 `embed_ui.sh` 现场构建 |
| 附件多模态（`attach_file`） | 已实现 | 图片/PDF 以真实内容块进入模型请求（OpenAI `image_url`/`file`，Anthropic `image`/`document`）；provider 不支持时显式 not-delivered，不静默丢。未验证真实视觉模型读图、5MB 以上图片缩放，见 §2.5 |
| Janitor | 部分 | `hive janitor status` 实测；`run --execute` 未执行 |
| `hive-cred` | 已实现 | 实测 `browse` / `inspect` |
| MCP 注册表与 CLI | 部分 | 14 个子命令注册；未对真实 MCP server 验证 |
| 在线 CVE 扫描 | 未实现 | Makefile 与 `.github/workflows/` 均无 `govulncheck` / OSV / nancy；见 §6 |

### 1.1 老用户无损替换（G2：旧 `HIVE_HOME` → Go 1.0.26）

结论：**数据级无损、凭据互读、可回滚、升级实测通过**；未发现需要修复的数据损坏。判定依据是同一份非空旧 home 的双跑、Go 写过的 home 用 Python 读回、以及 v1.0.25→1.0.26 的升级/回退演练（主机 darwin/arm64，2026-11-21）。「无损」指数据级（文件/字段/SQLite 结构/可见值），**不等于逐字节一致**。

| 验收项 | 结论 | 关键证据 |
| --- | --- | --- |
| 数据级无损（迁移双跑） | 通过：v1-rich 50/50、v2-rich 99/99 文件；逐字节相同 47/95；唯一 1 个字节差异是 `worker.json` 键序（字段等价）；JSON 字段、SQLite schema+行、读探针、API 探针差异均为 0；交叉消费 manifest 前后不变 | `../../docs/g2-lossless-migration.md` §1/§4、`../../docs/e2e-assets/g2-home-e2e/summary.json` |
| 凭据互读 | 通过：两侧同为标准 Fernet（`credentials/credentials/<id>.enc` + `metadata/index.json` 2.0）；Python 写 Go 读、Go 写 Python 读，值/`expires_at`/metadata 保留。**格式兼容 ≠ 真实第三方 token 可用**（G2 用假值） | `../../docs/storage-compat/credentials-crossread.json`、G2 双跑的 `*-credentials-verify.json` |
| 回滚可行（Go 写过的 home → Python） | 通过：Python CLI/API/`serve` 读取会话/历史/凭据/colony/tracker 全一致；0 删除、0 覆盖；仅新增 `summary.json` 读缓存与追加共享日志；Go 二次启动幂等 | `upgrade-and-rollback.md` §3、`e2e-assets/g2-rollback/` |
| 升级实测（v1.0.25 → 1.0.26） | 通过：旧二进制建的 v2 home 由新二进制启动即迁移（health 200、内嵌 `/` 200）；`install.sh` 原地升级四二进制并 SHA256 校验；升级后旧二进制回读 0 写入；`verify_release_archive.sh` 13/13 | `upgrade-and-rollback.md` §4、`e2e-assets/g2-rollback/summary.json` |

**已知例外（逐条）**：

| 编号 | 例外 | 性质 / 影响 | 状态 |
| --- | --- | --- | --- |
| **P0-G2-1** | v1 `exports/` 解析基准：Python `migrate_v2` 用 CWD 相对 `exports/`，Go 锚定 `$HIVE_HOME/exports` | 行为差异：Python 从 `core/` 启动时会给旧 v1 home 写 `.migrated-v2` 但不搬 colony（数据滞留风险）；Go 无此问题，替换方向更安全 | **待用户决策**：是否要求 Go 逐行为复刻 Python 的 CWD 语义；本次未改 exports 行为，发布说明须注明 |
| F4 | `colonies/.migrations/` 被 `hive colony list` 当作 colony（Python 同行为） | 展示层；存在误 `colony delete .migrations` 的风险 | **待决策**：需两侧联合过滤点目录，单改 Go 会破坏对齐 |
| F5 | 迁移前旧二进制看不到 v2 布局会话 | 用户感知；非数据损坏。新版本启动迁移后可读，且迁移后旧二进制仍可读 | 文档化前提（升级即迁移） |
| F6 | CLI JSON 数字序列化：Python `1767225600.0` vs Go `1767225600` | 值级；严格字符串比对会差，读取无影响 | 归 value-parity lane（未修） |
| R-G2-1 | `colonies/alpha/worker.json` 的 `input_data.binding` 键序（Python 插入序 vs Go map 排序） | 排版级；解析后字段/值一致，交叉消费不重写 | 不修 |
| R-G2-2 | 新建/重建的 `*.db` 字节不同（SQLite 页布局） | 设计内；schema/行/触发器/`user_version` 全等 | 不修（按结构验证） |
| R-G2-3/4 | `/api/health.resources` 运行期指标；读探针归一化时间戳/epoch | 非存储数据 | 不纳入数据比较 |
| R-G2-5 | `provider_id`：Python `null` vs Go `""` | 空值语义；存储回写不丢 | 已记录，未修 |

值格式级残余（JSON 排版/键序、时间戳 `+00:00` vs `Z`、凭据目录权限更严、`provider_id`）与覆盖缺口见 `../../docs/storage-contract-diff.md` §5。**未验证边界**：真实用户数据长尾（超大 parts、损坏 JSON、权限错误）、Windows/Linux 实机、真实 OAuth 刷新/第三方 token、sessions/history 的 HTTP 端点抓取；G2 双跑只覆盖 macOS arm64 与假凭据。

## 2. 集成工具（按 INTEGRATION_BACKLOG.md）

### 2.1 口径

上游 `aden_tools/tools/*` 有 **104 个目录 / 865 个 action**。其中 `file_system_toolkits` 只提供共享 helper（`@mcp.tool` 为 0），不是工具模块，所以与工具对齐的模块面是 **103 个**。

本工作树的覆盖：

| 类别 | 模块 | action | 说明 |
| --- | ---: | ---: | --- |
| `internal/tools/integrations` 的 23 个安装器 | 102 | 854 | 全部由 `sessionIntegrationInstallers` 接入会话目录 |
| `internal/senders`（`hiveserver.RegisterSenderTools`） | 1 | 9 | 不在 integrations 包内；受 `email_senders` 开关控制，默认关闭 |
| **Go 合计** | **103** | **863** | 模块面 102+1 / 104（`file_system_toolkits` 非工具模块） |
| 有意排除 | 0 | 2 | `csv_sql`、`excel_sql`（DuckDB 依赖），见 §2.3 |

23 个安装器的 action 明细见 `REMAINING_MODULES.md` §5（各安装器测试各自钉住 `installed` 数）。安装器之间无重名工具：`TestSessionToolCatalogueInstallsEveryBatch` 用「逐个安装后名字集合 ⊆ 会话目录」+「`installed` 计数等于 registry 大小」双重校验。

会话目录的实测口径（2026-11-21，本工作树）：

```text
默认（不设功能开关）                            → installed=113 registry=113
HIVE_EMAIL_SENDERS=1                            → installed=114 registry=114
```

6 个网络扫描器与 `risk_score` 默认注册（与上游 `aden_tools` 一致）；默认与打开 senders 的差只有 senders 1 个工具。

### 2.2 按 backlog 优先级

| 优先级 | backlog 未实现 | 现在的状态 | 结论 |
| --- | --- | --- | --- |
| P0（默认角色必备） | 8 模块 / 21 action | `excel`、`pdf_read`、`web_scrape`、`web_search`、`image_generate` 已移植并接入会话；`senders` 已接入但默认关闭；`attach_file` 已接入（多模态见 §2.5）；`file_system_toolkits` 由内建三件套覆盖基础能力 | **已覆盖** |
| P1（单 token SaaS） | 16 模块 / 161 action | 已移植并接入会话目录 | **已覆盖** |
| P2（企业系统/CRM/财务/BI） | 19 模块 / 150 action | 已移植并接入会话目录 | **已覆盖** |
| P3（基础设施/可观测/数据平台） | 15 模块 / 147 action | 已移植并接入会话目录 | **已覆盖** |
| P4（数据获取/营销/通讯长尾） | 26 模块 / 201 action | 已移植并接入会话目录 | **已覆盖** |

P0 逐项：

| 模块 | 需要的凭据 | 状态 |
| --- | --- | --- |
| `senders_tool` | 无（本地） | 已接入；`email_senders` / `HIVE_EMAIL_SENDERS` 默认关闭 |
| `excel_tool` | 无（本地） | 已接入；6 个 action，`excel_sql` 有意排除 |
| `image_gen_tool` | `HIVE_API_KEY` 等 | 已接入（`image_generate`，batch3-media） |
| `web_search_tool` | Brave / Google CSE | 已接入（`web_search`，batch3-media） |
| `pdf_read_tool` | 无（本地） | 已接入 |
| `web_scrape_tool` | `HIVE_HOME` 等 | 已接入 |
| `attach_file_tool` | `HIVE_STORAGE_PATH` | 已接入；多模态限制见 §2.5 |
| `file_system_toolkits` | 无（本地） | 上游为共享 helper，无工具；基础读写由内建 `read_file`/`write_file`/`terminal_exec` 承担 |

### 2.3 有意不移植（2 个 action，非遗漏）

| 上游 | 未移植 action | 原因 |
| --- | --- | --- |
| `csv_tool` | `csv_sql` | 上游用 DuckDB（native 依赖）；纯 Go 不做 cgo，`gen_catalog.py` 与 `doc.go` 均已注明排除 |
| `excel_tool` | `excel_sql` | 同上；`excel.go` 与测试断言该 action 返回 `ErrUnknownAction` |

这两个 action 在工具描述里不出现，调用时明确返回未知 action，不假装支持。

### 2.4 默认关闭的开关

| 工具 | 开关 | 默认 | 说明 |
| --- | --- | --- | --- |
| `senders` | `HIVE_EMAIL_SENDERS=1` 或 `email_senders` | 关闭 | 需要本地发信配置 |

网络扫描器（`port_scan`、`dns_security_scan`、`ssl_tls_scan`、`http_headers_scan`、`subdomain_enumerate`、`tech_stack_detect`）与 `risk_score` 不再有开关：它们和上游一样默认注册，会向模型指定的目标发探测流量，调用方只能扫描已获授权的目标。

扫描器随批次直接注册进会话目录；`TestSessionToolCatalogueRegistersSecurityScannersByDefault` 钉住 7 个工具默认存在，且旧的 `HIVE_NETWORK_SCANNERS=0` 不再影响工具面。

### 2.5 `attach_file` 的多模态路径（图片 / PDF → 模型内容块）

`attach_file`（batch8-final）的多模态已贯通到模型请求，不再止步于工具结果层：

- **工具层**：真实图片（png/jpg/jpeg/webp/gif）生成 `image_url` 块（`data:<mime>;base64,...`）；PDF 生成 provider-neutral 的 `file` 块（`file_data` data URL + 文件名）。两者都经 `tools.ToolResult.ImageContent` 返回，同时照常复制到会话附件目录并发布 chip。
- **运行时层**：`hive serve` 的 chat runtime 通过 `server.ChatRuntimeDeps.ExecuteBlocks` 接收这些块（装配在 `sessionToolCatalogue.ExecuteBlocks`），写入工具消息的 `image_content`；会话恢复时从存储重建，历史消息继续带块。
- **消息装配层**：`NodeConversation.ToLLMMessages(model)` 按模型能力决定块的位置——Anthropic 系（`anthropic/`、`claude-`、`kimi/`、`hive/`）图片留在 `tool_result` 内；其他 API 把块 hoist 到紧随工具批次的 user 消息（与上游 `_hoist_tool_result_images` 一致）。`llm.ChatMessage.ContentParts` 承载块，文本投影仍保留给日志与非多模态调用方。
- **provider 形状**：OpenAI 兼容端点原样发送 `image_url` / `file` 块（OpenAI 与 OpenRouter 的 Chat Completions 均支持 PDF `file` 块）；Anthropic 传输把 `file` 转成 `document` 块（base64 source），`image_url` 转成 `image` 块。
- **不支持时不静默丢**：catalog `supports_vision=false` 的模型收到显式 `[IMAGE attachment not delivered: …]` 占位文本；不在 PDF 支持清单（Anthropic / OpenAI / OpenRouter 系）的端点收到 `[PDF attachment not delivered: …]`。超过块上限（图片 20 MB、PDF 32 MB）的文件只发 chip，条目用 `inline_error` 说明原因。结果 JSON 的 `inline` 字段标注 `text_preview` / `image_block` / `file_block` / `none`。
- **安全**：内容块只携带 base64 字节与文件名（`filepath.Base`），不携带源路径或凭据；凭据从不进入消息装配。
- **文本预览**：内联上限仍为 8 KiB，完整文件以 chip 交付。

证据：`internal/tools/integrations/attach_file_test.go`（块形状、多附件、超限、无路径泄露）、`internal/agentloop/node_conversation_tools_test.go`（hoist、能力门禁）、`internal/llm/multimodal_test.go`（真实 HTTP 请求体：OpenAI `image_url`/`file`，Anthropic `image`/`document`）、`internal/server/chat_runtime_attachments_test.go`（fake provider 捕获 chat runtime 请求：图片、PDF、多附件、文本模型门禁、流式路径、会话恢复）。

## 3. 平台与 cgo 矩阵

### 3.1 cgo

| 检查 | 结果 | 命令 |
| --- | --- | --- |
| 源码中的 `import "C"` | **0** | `grep -rn 'import "C"' --include='*.go' . \| wc -l` |
| SQLite | `modernc.org/sqlite v1.60.1`（纯 Go） | `go.mod` |
| 构建环境 | `CGO_ENABLED=0` | `Makefile` 的 `OFFLINE` |

### 3.2 六个目标

`release.sh` 实测（2026-11-19，本工作树，全部 `-tags embed_ui`）：

| GOOS | GOARCH | 编译 | 测试执行 | 归档 |
| --- | --- | --- | --- | --- |
| darwin | amd64 | 通过 | 未执行 | `hive-1.0.26-go.1-darwin-amd64.tar.gz` |
| darwin | arm64 | 通过 | **本机全量通过** | `hive-1.0.26-go.1-darwin-arm64.tar.gz` |
| linux | amd64 | 通过 | 未执行 | `hive-1.0.26-go.1-linux-amd64.tar.gz` |
| linux | arm64 | 通过 | 未执行 | `hive-1.0.26-go.1-linux-arm64.tar.gz` |
| windows | amd64 | 通过 | 未执行 | `hive-1.0.26-go.1-windows-amd64.zip` |
| windows | arm64 | 通过 | 未执行 | `hive-1.0.26-go.1-windows-arm64.zip` |

「编译通过」只证明能为该目标生成二进制；**测试只在 darwin/arm64 本机跑过**。CI 定义（`.github/workflows/go.yml`）包含 Ubuntu 上的 `make ci` 与六目标交叉编译，`release.yml` 负责打包，但本次没有运行 GitHub Actions。

Windows 已知未验证点：

- `os.Rename` 覆盖已存在文件的原子写语义；
- 目录 fsync 不存在；
- `SIGTERM` 不存在，停止路径只能用 `os.Interrupt`；
- PTY 工具在 Windows 上没有可用实现。

## 4. 离线与网络

| 项 | 限制 | 验证 |
| --- | --- | --- |
| 构建 | 首次需要 `go mod download` 预热 module cache；之后 `GOPROXY=off GOSUMDB=off` 可离线 | 实测 `make ci`、`release.sh` 均在该环境变量下完成 |
| 运行时下载 | 无：不下载可执行代码、不装依赖 | 代码/构建约束 |
| Dashboard（源码树） | 仓库**不附带** `frontend/dist`；没有该目录时 `serve` 只提供 API | 代码路径；本次先跑 `embed_ui.sh` 生成 |
| Dashboard（发布包） | 单二进制内嵌；`serve` 会打印 `Dashboard: http://127.0.0.1:<port>` | 实测 `/`、`/sessions`、`/favicon.png` 200 |
| `embed_ui.sh` | 需要 Node/npm，或预构建 `FRONTEND_DIST` + `SKIP_FRONTEND_BUILD=1` | 实测：`npm ci` + `vite build` 6.32s，产出 41.8 MB 嵌入二进制 |
| `hive open` | 启动服务后向 `https://api.adenhq.com/v1/gateway/availability` 发一次可用性 ping | 代码路径；`serve` 不发 |
| 集成工具 | 真正调用时访问第三方 API；没有凭据就不会成功 | 测试只用假上游 |
| 网络扫描器 | 默认注册；调用时会向模型指定的主机探测（仅限已获授权目标） | 代码 + 会话目录测试 + 本地 127.0.0.1 真实调用 |
| 技能注册表 / MCP `update` | 使用时访问注册表或索引 | 代码路径 |

## 5. 未验证清单（按风险排序）

| 项 | 为什么没验证 | 影响 |
| --- | --- | --- |
| 真实厂商模型调用 | 发布任务没有可用的真实 key | 请求形状已验证；鉴权/额度/模型可用性未知 |
| 863 个集成 action 的真实调用 | 需要大量第三方凭据（backlog 里 74 个模块需凭据） | 只有 `httptest`/假上游证据；真实 API 的字段漂移未覆盖 |
| 真实视觉模型读图/PDF 端到端 | 本机无本地网关（11434/8000/1234/8080/3000 均未监听）；配置的远程网关没有可解析的明文凭据，vision 能力未知 | 块形状与装配由 fake provider、假上游 HTTP、流式路径与会话恢复钉住；真模型对图片内容的理解未验证 |
| Colony 多 worker 闭环 | 需要真实模型与长任务 | 调度/报告/预算有单测，端到端未跑 |
| Sentinel 通知 | 需要 Telegram/Slack 凭据 | 引擎单测通过，真实投递未验证 |
| Claude Code queen 真实模型主动调 `message_queen` | 未做真实模型回合联调 | 运行器/回复闭环已验证，但“她自己在回合中调 MCP 工具”只有 fake/契约测试 |
| Windows 运行 | 无 Windows 机器 | 只有交叉编译 |
| 在线 CVE 扫描 | **仓库没有做**：Makefile/CI 中没有 `govulncheck`、OSV、nancy 或等价步骤 | 依赖漏洞状态未知；只能确认依赖锁定、无 cgo、无运行时下载 |
| `hive janitor run --execute` | 会删除数据，未在发布验证中执行 | 只验证了 `status` |
| `hive colony delete` | 破坏性操作，未执行 | 只验证了 `list`/`info` |
| G2 真实用户数据长尾 | 双跑夹具是「非空且字段完整」的代表样本 | 超大 parts、损坏 JSON、权限错误的极端形状未覆盖 |
| G2 凭据真实第三方可用性 | G2/存储双跑使用假值凭据 | 格式兼容与双向互读已实测；真实 OAuth 刷新/第三方 token 未验证 |
| G2 平台覆盖 | 回滚/升级演练只在 macOS arm64 | Windows/Linux 未实机（与 §3.2 口径一致） |

## 6. 质量门禁实测

在 `go_hive/go-hive/` 下：

```bash
make ci    # fmt-check + vet + test + cross + parity + parity-targets + wiring-check
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test -tags e2e -count=1 ./e2e/...
```

2026-11-19 本工作树实测：

| 项 | 结果 |
| --- | --- |
| `make ci` | 退出码 0；gofmt OK；vet 无输出；37 个包 `ok`，6 个包无测试文件 |
| 测试声明数（parity 口径） | 4,566（含 `e2e` 构建标签下的 2 条） |
| 默认构建执行的测试 | `make test` 退出码 0（38 个包 ok、0 fail；逐条 pass/skip 以 `go test -v` 为准） |
| `e2e`（`-tags e2e`） | `ok github.com/aden-hive/hive-go/e2e 3.247s` |
| `make cross` | 六目标全部编译通过，退出码 0 |
| cgo | `import "C"` 0 处 |
| `check_parity.sh` | `4566/4566` 个测试有 `Parity:` 或 `Parity-exempt:` 注解，0 missing |
| `check_parity_targets.sh` | 4,843 条引用（3,773 test + 1,070 prod），0 missing files/tests/symbols |

这些数字是**本工作树**的实测值，与 `STATUS.md` 中的 2,238/2,045 等历史快照不同（文档快照时间不同）。

2026-11-20（attach-multimodal 工作树）复跑：

| 项 | 结果 |
| --- | --- |
| `make fmt-check vet cross parity parity-targets wiring-check` | 退出码 0：gofmt OK；vet 无输出；六目标交叉编译通过；parity `4898/4898`、0 missing；refs 5,183（3,939 test + 1,244 prod）0 missing；wiring_check 12/12 |
| `make test`（全量） | 4,898 条声明；一轮全量通过，随后一轮复跑出现既有 `internal/server::TestOverseerAskUserParksSessionAndAnswerRelays` 时序 flake（`cursor pending_input after answer = <nil>`）；隔离 `-run TestOverseerAskUserParksSessionAndAnswerRelays -count=10` 通过 10/10，与本次 attach 改动无关，未修改该测试 |
| 本次车道目标测试 | `internal/server` 6 条 attach 用例、`internal/llm` 3 条请求体用例、`internal/agentloop` 3 条门禁用例、`internal/cli` 1 条 worker 块透传用例、`internal/tools/integrations` attach 全组均通过 |

2026-11-21（config-env-align 工作树）复跑：

| 项 | 结果 |
| --- | --- |
| `make ci` | 退出码 0：gofmt OK；`go vet` 无输出；45 个包 `ok`、14 个包无测试文件、0 FAIL；六目标交叉编译通过；parity `5186/5186` 注解、0 missing；parity refs 5,532（4,122 test + 1,410 prod）、0 missing；wiring_check 12 个接线文件 / 47 项要求全部命中 |
| 契约冻结自洽 | `freeze_env_config_contract.py --check` OK；`check_env_config_contract.py --strict` OK —— env：Python 89 / Go 105 / 共有 70 / Python 独有 19 / Go 独有 35；config：134 键（aligned 125 / go_partial 4 / python_only 0 / go_only 4 / unread 1） |
| 文件规模门禁 | `internal/quality` 通过：`loader.go` 1754 行（≤1936）、`loop.go` 1486 行（拆分 vision caption 与 parseToolInput 后 ≤1500）、`conversation.go` 1535 行（≤1549 allowlist）、`chat_runtime.go` 1498 行（≤1500） |

2026-11-21（G2 无损替换收口复跑；本工作树只改文档，按派工要求不跑全量 CI）：

| 项 | 结果 |
| --- | --- |
| `gofmt -l .` | 空 |
| `go vet`（server/storage/tracker/credentials/session/agents/cli） | 退出码 0，无输出 |
| 目标包测试 | `internal/{storage,tracker,credentials,session,agents}` 全 `ok`；`internal/server -run TestStorageStartup` `ok` |
| `make parity` | 5,306/5,306 测试带 parity 注解，0 missing |
| `make parity-targets` | 5,687 refs（4,213 test + 1,474 prod），0 missing |

发布包本身另有一轮验证（`hive --version`、`shasum -a 256 -c SHA256SUMS`、解压 darwin-arm64 后 `/` 200）记录在 `release-notes-v1.0.26-go.1.md`。

## 7. 复现

源码树：

```bash
cd go_hive/go-hive
make build
./dist/hive --version
HIVE_HOME="$HOME/.hive-support-matrix" ./dist/hive serve --port 8787
```

另开终端：

```bash
curl -sS http://127.0.0.1:8787/api/health
curl -sS -X POST http://127.0.0.1:8787/api/sessions -H 'Content-Type: application/json' -d '{}'
```

发布包（带 dashboard）：

```bash
bash go_hive/scripts/embed_ui.sh
bash go_hive/scripts/release.sh
cd go_hive/go-hive/dist/release && shasum -a 256 -c SHA256SUMS
bash go_hive/install.sh --from go_hive/go-hive/dist/release
hive --version && hive serve
```

`quickstart.md` 有完整的分步流程与无 key 行为；`configuration.md` 有模型、凭据与 colony 配置的解析优先级；`release-notes-v1.0.26-go.1.md` 有发布包清单与升级说明。

G2（无损替换 / 回滚）一键复现：

```bash
bash go_hive/scripts/g2_home_e2e_run.sh --work /tmp/g2-home-e2e
bash go_hive/scripts/g2_rollback_drill.sh --work /tmp/g2-rollback
```

产物分别写入仓库根 `docs/e2e-assets/g2-home-e2e/` 与 `go_hive/docs/e2e-assets/g2-rollback/`；说明见 `../../docs/g2-lossless-migration.md` 与 `upgrade-and-rollback.md`。
