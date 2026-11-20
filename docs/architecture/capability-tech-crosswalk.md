# 业务能力 ↔ 技术模块对照（生成物，勿手改）

> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` 生成。commit: `5a1cde61a121`
> 这是业务视图与技术视图的接缝：左边是人关心的结果，右边是代码里真实存在的包、入口与落盘文件。

## 成熟度与审批口径

- **live**：主路径有端到端测试（`go-hive/e2e/`）或真实二进制冒烟覆盖，能对着代码指出来回。
- **tested**：有单元/集成测试与真实入口，但没有端到端证据；接线正确性靠测试与代码阅读，不靠跑通。
- **unverified**：代码存在但未接线、无入口，或本次审计无法确认其可用性。

人工审批列：**yes** 表示该能力的关键动作必须由人完成；**partial** 表示默认路径可自动，
但缺凭据、ask_user、soul 提案 review、Sentinel escalate 等分支会停下来等人；**no** 表示全自动。

## 能力对照表

| 能力 | 业务结果 | 技术模块 | 入口（HTTP / CLI / 工具） | 状态与证据（落盘） | 人工审批 | 成熟度 | 已知限制 |
| --- | --- | --- | --- | --- | :---: | :---: | --- |
| **目标与结果**<br/>`goal-outcome` | 把客户的一句话目标变成可验收的结果：目标、加权成功标准、以及每轮结束时的验收判定。 | `internal/colony`<br>`internal/agentloop`<br>`internal/llmjudge`<br>`internal/schemas` | HTTP POST /api/colonies/{colony_id}/scaffold<br>tool run_worker(goal, tasks)<br>CLI hive colony info | ~/.hive/colonies/{colony}/worker.json（goal.description 与 goal.success_criteria）<br>worker 终止报告中的 goal_evaluation | partial | tested | 成功标准由模型生成，不是形式化规格；未声明 criteria 时不做验收判定，空 tracker 表会单独记为 empty_tracker。 |
| **Queen 分诊与对话**<br/>`queen-triage` | 用户消息在同一个会话内被理解、追问或分诊为直接回答、工具执行或派工，并以流式事件回放。 | `internal/server`<br>`internal/queen`<br>`internal/agentloop`<br>`internal/llm`<br>`internal/soul`<br>`internal/skills`<br>`internal/memory`<br>`internal/attachments` | HTTP POST /api/sessions/{session_id}/chat<br>HTTP GET /api/sessions/{session_id}/events (SSE)<br>CLI hive chat<br>CLI hive serve | ~/.hive/queens/{queen}/sessions/{session}/（parts、cursor.json、meta.json）<br>~/.hive/queens/{queen}/soul/soul.json | partial | live | 单会话单循环串行处理 turn；分诊依赖模型判断，没有确定性路由表；ask_user 与凭证表单会 park 到下一次用户输入。 |
| **Colony 部署单元**<br/>`colony-unit` | 一次目标对应一个可暂停、可恢复的 colony 目录，绑定 queen、tracker 与 worker 池，使长任务不与单个会话同生共死。 | `internal/colony`<br>`internal/host`<br>`internal/session`<br>`internal/storage` | HTTP POST /api/colonies/{colony_id}/scaffold<br>HTTP POST /api/sessions/{session_id}/colony（加载已有 colony）<br>CLI hive colony list\|info\|delete | ~/.hive/colonies/{colony}/{metadata.json, worker.json}<br>~/.hive/colonies/{colony}/{tracker/tracker.db, workers/, queens/, playbooks/} | no | tested | 会话 spawn colony 后即锁定，必须 compact-and-fork 才能继续；一个 colony 一个 tracker 库，跨 colony 不共享账本。 |
| **Worker 派发与回报**<br/>`worker-dispatch` | Queen 一次 run_worker 提交 N 个任务立即返回，worker 在后台并行执行，每个以一条结构化 [WORKER_REPORT] 回合回报。 | `internal/colony`<br>`internal/agentloop`<br>`internal/tools`<br>`internal/eventbus`<br>`internal/tasks` | tool run_worker<br>tool report_to_parent<br>HTTP GET /api/sessions/{session_id}/workers<br>HTTP POST /api/sessions/{session_id}/workers/stop-all | ~/.hive/colonies/{colony}/workers/{worker_id}/{worker.json, parts/}<br>会话中的一个 [WORKER_REPORT] 用户回合 | no | tested | 并发受 HIVE_MAX_CONCURRENT_WORKERS 闸门限制；worker 不能递归 run_playbook；stop 只保证不再启动，不回收已发起的上游调用。 |
| **技能与 Playbook**<br/>`skills-playbook` | 把可复用流程固化成技能与可执行 playbook，同一套动作可以批量派给多个 worker。 | `internal/skills`<br>`internal/playbook`<br>`internal/colony`<br>`internal/tools` | CLI hive skill install\|validate\|test\|trust<br>tool run_playbook<br>HTTP GET /api/sessions/{session_id}/colony/skills | skills/{name}/SKILL.md<br>~/.hive/colonies/{colony}/playbooks/*.play.json<br>~/.hive/queens/{queen}/skills_overrides.json | partial | tested | 技能是提示词加脚本，不是沙箱；playbook 并发受 HIVE_MAX_PLAYBOOK_CONCURRENCY 上限约束。 |
| **工具与凭据边界**<br/>`tools-credentials` | 模型只看见按 tier 过滤后的工具；外部系统的凭据在边界内注入，缺失时停下来等人填，而不是猜。 | `internal/tools`<br>`internal/tools/builtin`<br>`internal/tools/integrations`<br>`internal/mcpregistry`<br>`internal/credentials`<br>`internal/ratelimit`<br>`internal/llm` | CLI hive mcp install\|list\|doctor<br>HTTP GET\|PUT /api/credentials<br>HTTP GET /api/mcp/servers<br>tool search_tools | ~/.hive/credentials/（规格与存储）<br>~/.hive/secrets/<br>~/.hive/mcp_registry/<br>~/.hive/configuration.json（rate_limits） | yes | live | 没有 OS 级沙箱，MCP 子进程继承当前用户权限；未配置 rate_limits 时限速为无限；凭据缺失会 park 整个 turn。 |
| **记忆与沉淀**<br/>`memory-sediment` | 跨会话记住偏好与结论；Queen 把经历写进 soul，可审阅、可回滚，而不是每次从零开始。 | `internal/memory`<br>`internal/soul`<br>`internal/server`<br>`internal/agentloop` | HTTP GET\|PUT /api/memories/file<br>HTTP POST /api/queen/{queen_id}/soul/reflect<br>HTTP POST /api/queen/{queen_id}/soul/proposals/{proposal_id}/decide | ~/.hive/memories/*.md<br>~/.hive/queens/{queen}/soul/{soul.json, history/v*.json, log.jsonl, proposals/*.json} | partial | tested | 记忆是 Markdown + 提示注入，不是向量库；soul 有字符与条数上限，默认 review 模式下提案需人批准。 |
| **Tracker、计划与证据**<br/>`evidence-tracker` | 每一行结果都可追溯：worker 写共享账本，Queen 查询账本，任务计划跨回合存活，报告可按会话打包。 | `internal/tracker`<br>`internal/tasks`<br>`internal/colony`<br>`internal/server`<br>`internal/sessionreport` | tool tracker_upsert\|tracker_query\|tracker_sql<br>tool task_create\|task_update\|task_list\|task_get<br>HTTP GET /api/sessions/{session_id}/tasks<br>HTTP POST /api/sessions/{session_id}/report-bundle | ~/.hive/colonies/{colony}/tracker/tracker.db (SQLite WAL)<br>{session\|worker}/tasks.json<br>{session}/reminder_state.json | no | tested | tracker 写入是 worker 的自愿行为，漏写只能从报告文本恢复；SQLite 单写者，跨进程并发写需靠 WAL 与重试。 |
| **触发器与 Sentinel 自治**<br/>`triggers-sentinel` | 定时或事件触发能自动唤醒会话；Queen 停泊但目标未完成时，由 Sentinel 决定继续推、升级给人还是不动。 | `internal/triggers`<br>`internal/sentinel`<br>`internal/server`<br>`internal/eventbus` | HTTP POST /api/sessions/{session_id}/triggers<br>HTTP POST /api/sessions/{session_id}/triggers/{trigger_id}/run<br>HTTP PUT /api/config/sentinel<br>进程内 timer ticker | {session}/triggers.json<br>~/.hive/colonies/{colony}/notifications.json<br>~/.hive/colonies/{colony}/escalations/ | partial | tested | 定时器只在服务进程存活时运行；错过的触发需要 resolve_missed 决策，不会自动补跑；escalate 依赖外部渠道可达。 |
| **通知与提醒**<br/>`notifications` | 到点提醒、worker 报告与升级消息被送到人所在的渠道，并且有发送记录可查。 | `internal/agentloop`<br>`internal/server`<br>`internal/senders`<br>`internal/sentinel`<br>`internal/tasks` | 进程内 ReminderHub ticker<br>HTTP GET\|DELETE /api/sessions/{session_id}/reminders<br>HTTP GET /api/colony/{colony_id}/notifications<br>HTTP POST /api/sentinel/telegram/detect-chat | {session}/reminder_state.json<br>~/.hive/colonies/{colony}/notifications.json<br>senders 发送日志（sendlog） | no | tested | 提醒依赖进程内 ticker，重启只恢复状态，不补发错过的时点；抑制列表会静默丢弃被抑制的收件人。 |
| **渠道与仪表盘**<br/>`channels-dashboard` | 同一套会话既能从 Web Dashboard 看，也能从 CLI 或聊天渠道接入；状态与事件对操作者可见。 | `internal/server`<br>`internal/server/uiembed`<br>`internal/runtimehealth`<br>`internal/senders`<br>`internal/qmail` | CLI hive serve<br>CLI hive open<br>HTTP GET /api/channels<br>HTTP GET /api/events/global (SSE) | ~/.hive/queens/{queen}/sessions/{session}/（元数据与事件）<br>~/.hive/event_logs/events.jsonl<br>~/.hive/colonies/{colony}/notifications.json | no | tested | 渠道 webhook 需外网可达；embed_ui 需要构建标签，否则回退到磁盘前端产物。 |
| **发布与质量门禁**<br/>`release-quality` | 每次交付可验证：七门禁、架构漂移检查与发布清单都能在本地和 CI 复现，结论可签字。 | `internal/quality`<br>`internal/version` | make ci（fmt-check vet test cross parity parity-targets wiring-check）<br>python3 scripts/qc_check.py<br>go test ./internal/quality<br>python3 scripts/gen_arch.py --check | go_hive/QC_REPORT.md<br>go_hive/RELEASE_CHECKLIST.md<br>go_hive/architecture/graph.json | yes | tested | 门禁是仓库内脚本与 go test，不覆盖部署环境；架构漂移检查需手动或 CI 显式调用，不会自动阻断提交。 |

## 模块 → 能力 反向索引

| 模块 | 层 | 承载能力 |
| --- | --- | --- |
| `internal/agentloop` | runtime | Queen 分诊与对话、Worker 派发与回报、目标与结果、记忆与沉淀、通知与提醒 |
| `internal/attachments` | capability | Queen 分诊与对话 |
| `internal/colony` | orchestration | Colony 部署单元、Tracker、计划与证据、Worker 派发与回报、技能与 Playbook、目标与结果 |
| `internal/credentials` | capability | 工具与凭据边界 |
| `internal/eventbus` | foundation | Worker 派发与回报、触发器与 Sentinel 自治 |
| `internal/host` | orchestration | Colony 部署单元 |
| `internal/llm` | capability | Queen 分诊与对话、工具与凭据边界 |
| `internal/llmjudge` | runtime | 目标与结果 |
| `internal/mcpregistry` | capability | 工具与凭据边界 |
| `internal/memory` | capability | Queen 分诊与对话、记忆与沉淀 |
| `internal/playbook` | orchestration | 技能与 Playbook |
| `internal/qmail` | orchestration | 渠道与仪表盘 |
| `internal/quality` | foundation | 发布与质量门禁 |
| `internal/queen` | orchestration | Queen 分诊与对话 |
| `internal/ratelimit` | foundation | 工具与凭据边界 |
| `internal/runtimehealth` | transport | 渠道与仪表盘 |
| `internal/schemas` | foundation | 目标与结果 |
| `internal/senders` | capability | 渠道与仪表盘、通知与提醒 |
| `internal/sentinel` | orchestration | 触发器与 Sentinel 自治、通知与提醒 |
| `internal/server` | transport | Queen 分诊与对话、Tracker、计划与证据、渠道与仪表盘、触发器与 Sentinel 自治、记忆与沉淀、通知与提醒 |
| `internal/server/uiembed` | transport | 渠道与仪表盘 |
| `internal/session` | orchestration | Colony 部署单元 |
| `internal/sessionreport` | orchestration | Tracker、计划与证据 |
| `internal/skills` | capability | Queen 分诊与对话、技能与 Playbook |
| `internal/soul` | capability | Queen 分诊与对话、记忆与沉淀 |
| `internal/storage` | foundation | Colony 部署单元 |
| `internal/tasks` | orchestration | Tracker、计划与证据、Worker 派发与回报、通知与提醒 |
| `internal/tools` | capability | Worker 派发与回报、工具与凭据边界、技能与 Playbook |
| `internal/tools/builtin` | capability | 工具与凭据边界 |
| `internal/tools/integrations` | capability | 工具与凭据边界 |
| `internal/tracker` | orchestration | Tracker、计划与证据 |
| `internal/triggers` | orchestration | 触发器与 Sentinel 自治 |
| `internal/version` | foundation | 发布与质量门禁 |

## 未被业务能力直接引用的模块

共 11 个。基础层与入口层通常不需要挂到业务能力上；如果能力层出现这里的包，说明画图时漏了映射，应该补 `model.yaml` 的 `capabilities[].modules`。

| 模块 | 层 | 说明 |
| --- | --- | --- |
| `internal/cli` | assembly | 装配/传输面，通常只体现在能力的入口列 |
| `internal/charts` | capability | 能力层未映射，建议补充 modules |
| `internal/globaldb` | capability | 能力层未映射，建议补充 modules |
| `internal/maintenance` | capability | 能力层未映射，建议补充 modules |
| `cmd/hive` | entry | 基础/入口层，预期不挂业务能力 |
| `cmd/hive-cred` | entry | 基础/入口层，预期不挂业务能力 |
| `internal/atomicfs` | foundation | 基础/入口层，预期不挂业务能力 |
| `internal/hivehome` | foundation | 基础/入口层，预期不挂业务能力 |
| `internal/platform` | foundation | 基础/入口层，预期不挂业务能力 |
| `internal/agents` | orchestration | 编排/运行时组件，可按需挂到能力 |
| `internal/orchestrator` | runtime | 编排/运行时组件，可按需挂到能力 |

## 业务链路的读法

1. 从 `goal-outcome` 开始：目标与加权成功标准写进 colony 的 `worker.json`（`goal`）。
2. `queen-triage` 决定是直接回答还是派工；派工经 `worker-dispatch` 进入 colony。
3. worker 的证据落 `tracker/tracker.db`，计划落 `tasks.json`，报告以 `[WORKER_REPORT]` 回合回到 Queen。
4. 交付经 `channels-dashboard` 暴露；失败经 `improvement/signals.jsonl` 进入改进；
   下一轮由 `triggers-sentinel` 唤醒，人工审批点见上图六边形。
