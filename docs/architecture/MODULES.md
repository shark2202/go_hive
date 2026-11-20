# 模块清单（生成物，勿手改）

> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` + `go list` 生成。commit: `4c198a558dfd`
> 修改分层或职责请改 `architecture/model.yaml`，不要编辑本文件。

## 分层统计

| 层 | 包数 | 说明 |
| --- | ---: | --- |
| 入口层（`entry`） | 2 | 进程入口，只做 argv 解析与退出码转换，不含业务逻辑。 |
| 装配层（`assembly`） | 1 | 组合根：读配置、构造 home / 工具注册表 / MCP runtime / 会话与 colony 运行时，再把命令路由到下游。 |
| 传输层（`transport`） | 3 | HTTP + SSE 边界：路由、会话查找、请求校验、事件镜像；不含 agent 决策。 |
| 编排层（`orchestration`） | 12 | Queen / Colony / Worker 的生命周期与协调：派发、报告、计划、账本、触发器、看护。 |
| 执行原语层（`runtime`） | 3 | 唯一的执行原语 AgentLoop：一次 turn 内的模型调用、工具批处理、压缩、预算与停泊。 |
| 能力层（`capability`） | 14 | 可被循环调用的能力：工具、集成、LLM 传输、记忆、技能、soul、凭据、出站消息。 |
| 基础层（`foundation`） | 9 | 无业务语义的底座：事件总线、文件存储、schema、原子写、目录布局、限速、质量门禁。 |

共 44 个包、124 条包间 import 边。

## 模块表

| 包 | 层 | 职责 | 直接依赖 | 被依赖 | 入口 |
| --- | --- | --- | ---: | ---: | :---: |
| `cmd/hive` | entry | hive 主进程入口：把 argv 交给 internal/cli.Runner 并以返回码退出。 | 1 | 0 | 是 |
| `cmd/hive-cred` | entry | 凭证 CLI 入口（hive-cred）：复用 internal/cli 的凭证子命令。 | 1 | 0 | 是 |
| `internal/cli` | assembly | 装配根：解析命令与配置，构造 home、ToolRegistry、MCP runtime、ChatRuntime、ColonyRuntime，并注册 serve/chat/queen/colony/session/skill/mcp/janitor 子命令。 | 23 | 2 |  |
| `internal/runtimehealth` | transport | 进程与宿主机资源健康快照，供 /api/health/resources 使用。 | 0 | 1 |  |
| `internal/server` | transport | HTTP/SSE 传输层：路由注册、会话管理、ChatRuntime 的 turn 队列、colony/tracker/sentinel 适配器与事件桥。 | 30 | 1 |  |
| `internal/server/uiembed` | transport | embed_ui 构建标签下的前端静态资源；未带标签时返回空，让服务端回退到磁盘目录。 | 0 | 1 |  |
| `internal/agents` | orchestration | Agent 规格与发现：读取 agents 目录、worker 元数据与能力描述。 | 2 | 2 |  |
| `internal/colony` | orchestration | Colony 运行时：worker 生命周期与并发闸门、run_worker 批量派发、report_to_parent 回报、playbook 驱动、目标验收与改进信号。 | 11 | 3 |  |
| `internal/host` | orchestration | Host 组合根：把 colony 运行时、事件总线与 run 注册表装成可 Start/Spawn 的宿主，供传输层调用。 | 7 | 1 |  |
| `internal/playbook` | orchestration | Playbook 定义与带并发上限的 lane 执行器。 | 0 | 1 |  |
| `internal/qmail` | orchestration | Queen 之间的信件存储、待投递队列与 queens/mail.jsonl 审计日志。 | 2 | 1 |  |
| `internal/queen` | orchestration | Queen 档案与身份定义（queen_profiles 的 Go 移植）。 | 0 | 3 |  |
| `internal/sentinel` | orchestration | Queen 停泊期的自治监督：nudge / escalate 判定、升级投递、入站回复路由与恢复。 | 4 | 1 |  |
| `internal/session` | orchestration | 会话记录与磁盘布局：状态、transcript、游标、fork 与冷恢复。 | 3 | 2 |  |
| `internal/sessionreport` | orchestration | 会话报告打包（report-bundle）：把会话、worker 与证据汇总成一份可交付摘要。 | 0 | 1 |  |
| `internal/tasks` | orchestration | 任务计划存储：tasks.json 的增删改查、归档与提醒状态。 | 3 | 3 |  |
| `internal/tracker` | orchestration | 共享结果账本：SQLite(WAL) tracker.db 的表注册、行写入与查询。 | 0 | 3 |  |
| `internal/triggers` | orchestration | 触发器定义、cron 解析与 triggers.json 持久化。 | 1 | 1 |  |
| `internal/agentloop` | runtime | 唯一执行原语：一轮内的 LLM 调用、工具批处理与并发、压缩、预算/宽限、judge、提醒注入与停泊原因。 | 6 | 7 |  |
| `internal/llmjudge` | runtime | 基于 LLM 的判定器适配：把成功标准与升级判定包装成可注入的 Judge。 | 1 | 2 |  |
| `internal/orchestrator` | runtime | Python 图编排时代遗留的 DataBuffer 原语（权限、幻觉内容校验）。 | 0 | 0 |  |
| `internal/attachments` | capability | 会话附件存取（上传、下载、类型与大小校验）。 | 0 | 2 |  |
| `internal/charts` | capability | chart_render 的纯 Go 渲染实现（SVG 生成 + 可选 PNG 栅格化）。 | 1 | 0 |  |
| `internal/credentials` | capability | 凭证规格与加密存储、OAuth 状态与表单校验。 | 2 | 3 |  |
| `internal/globaldb` | capability | 全局数据表（CRM / global data）的 SQLite 访问。 | 0 | 1 |  |
| `internal/llm` | capability | LLM 传输：provider 选择、Anthropic/OpenAI 协议、流式事件、模型目录与出站限速门。 | 3 | 10 |  |
| `internal/maintenance` | capability | Janitor：孤儿会话、缓存、工件与事件日志的清理规则与报告。 | 2 | 2 |  |
| `internal/mcpregistry` | capability | MCP 服务器注册表 CLI 后端：索引、安装/启用、健康检查与清单渲染。 | 4 | 2 |  |
| `internal/memory` | capability | Markdown 记忆文件：召回、写入、按天时间线与反思沉淀。 | 3 | 3 |  |
| `internal/senders` | capability | 出站消息发送：正文渲染、抑制列表、每发件人锁与发送日志。 | 2 | 1 |  |
| `internal/skills` | capability | 技能发现/安装/校验，以及 agent 侧的技能读取工具。 | 3 | 5 |  |
| `internal/soul` | capability | Queen 的 soul：分区、字符上限、版本历史、提案与回滚。 | 0 | 1 |  |
| `internal/tools` | capability | 工具注册表：eager/searchable 分层、MCP 服务器加载与代理执行、结果规范化与溢出落盘。 | 1 | 8 |  |
| `internal/tools/builtin` | capability | 内置工具实现：read_file / write_file / terminal_exec 等，刻意不依赖 server 与 agentloop。 | 2 | 1 |  |
| `internal/tools/integrations` | capability | 集成工具集：研究、天气、行情、媒体、网络扫描等外部系统的工具模块与参数 schema。 | 2 | 1 |  |
| `internal/atomicfs` | foundation | 原子文件写入与目录替换，避免半写状态。 | 0 | 15 |  |
| `internal/eventbus` | foundation | 进程内事件总线：AgentEvent、订阅分发、SSE 事件源与事件日志。 | 0 | 5 |  |
| `internal/hivehome` | foundation | ~/.hive 目录布局解析与 ensure（queens/colonies/memories/credentials/...）。 | 0 | 15 |  |
| `internal/platform` | foundation | 平台差异：路径、进程与 shell 选择。 | 0 | 1 |  |
| `internal/quality` | foundation | 仓库静态质量门禁：分层 import 检查与文件大小检查，以 go test 形式运行。 | 0 | 0 |  |
| `internal/ratelimit` | foundation | 调用限速：LLM 请求与工具调用的桶式限流器（rate_limits 配置）。 | 1 | 3 |  |
| `internal/schemas` | foundation | 跨包共享的数据结构与序列化约定（报告、计划、工具 schema）。 | 0 | 5 |  |
| `internal/storage` | foundation | 会话与检查点的文件存储（part JSON、cursor、index），写入走原子替换。 | 3 | 2 |  |
| `internal/version` | foundation | 版本字符串与构建信息。 | 0 | 2 |  |

## 分层检查

未发现反向依赖：所有 import 边都从浅层指向深层。

## 无导入方的包

以下包没有任何内部导入方。属于未接线代码或纯门禁/入口约定，列在这里供人工判断：

- 包 internal/charts 没有任何内部导入方（可能是未接线代码或纯入口约定）
- 包 internal/orchestrator 没有任何内部导入方（可能是未接线代码或纯入口约定）

## 备注

- `internal/orchestrator`：未接线：仓库内无调用方；本 port 以 run_worker 的扇出取代图编排。
- `internal/charts`：未接线：chart_render 尚未注册进 ToolRegistry。

---

共 44 个包；层的定义与业务能力对照见 `docs/architecture/README.md` 与 `docs/architecture/capability-tech-crosswalk.md`。
