# 模块清单（生成物，勿手改）

> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` + `go list` 生成。commit: `6624019b734d`
> 修改分层或职责请改 `architecture/model.yaml`，不要编辑本文件。

## 分层统计

| 层 | 包数 | 说明 |
| --- | ---: | --- |
| 入口层（`entry`） | 4 | 进程入口，只做 argv 解析与退出码转换，不含业务逻辑。 |
| 测试与评测装置（`harness`） | 12 | 不随发布物交付的测试装置：e2e 确定性假上游、契约对照工具与发布脚本测试；只被测试与内部评测入口引用。 |
| 装配层（`assembly`） | 1 | 组合根：读配置、构造 home / 工具注册表 / MCP runtime / 会话与 colony 运行时，再把命令路由到下游。 |
| 传输层（`transport`） | 3 | HTTP + SSE 边界：路由、会话查找、请求校验、事件镜像；不含 agent 决策。 |
| 编排层（`orchestration`） | 14 | Queen / Colony / Worker 的生命周期与协调：派发、报告、计划、账本、触发器、看护。 |
| 执行原语层（`runtime`） | 3 | 唯一的执行原语 AgentLoop：一次 turn 内的模型调用、工具批处理、压缩、预算与停泊。 |
| 能力层（`capability`） | 20 | 可被循环调用的能力：工具、集成、LLM 传输、记忆、技能、soul、凭据、出站消息。 |
| 基础层（`foundation`） | 16 | 无业务语义的底座：事件总线、文件存储、schema、原子写、目录布局、限速、质量门禁。 |

共 73 个包、196 条包间 import 边。

## 模块表

| 包 | 层 | 职责 | 直接依赖 | 被依赖 | 入口 |
| --- | --- | --- | ---: | ---: | :---: |
| `cmd/hive` | entry | hive 主进程入口：把 argv 交给 internal/cli.Runner 并以返回码退出。 | 1 | 0 | 是 |
| `cmd/hive-browser` | entry | 浏览器 CLI 客户端（hive-browser）：解析动词与 flag，转发给独立的 Browser Bridge 服务端。 | 1 | 0 | 是 |
| `cmd/hive-cred` | entry | 凭证 CLI 入口（hive-cred）：复用 internal/cli 的凭证子命令。 | 1 | 0 | 是 |
| `cmd/hive-longmemeval` | entry | LongMemEval 评测入口（内部工具）：用真实 queen 会话跑基准，并按基准自带判分提示评分。 | 2 | 0 | 是 |
| `e2e/cmd/memgw` | harness | 确定性 OpenAI 兼容假上游的启动命令（memory e2e）。 | 1 | 0 |  |
| `e2e/cmd/recordproxy` | harness | 录制转发代理命令：把真实上游流量录成 e2e 夹具。 | 1 | 0 |  |
| `e2e/cmd/runtimelogs` | harness | 三级运行日志双跑的启动命令（e2e）。 | 1 | 0 |  |
| `e2e/cmd/stallgw` | harness | 确定性假上游命令（stream stall e2e）。 | 1 | 0 |  |
| `e2e/cmd/storagecompat` | harness | HIVE_HOME 存储兼容 / G2 无损迁移夹具的 Go 侧命令。 | 10 | 0 |  |
| `e2e/escalationgw` | harness | worker 升级 e2e 的确定性假上游网关。 | 0 | 0 |  |
| `e2e/memgw` | harness | memory e2e 的确定性假上游网关。 | 0 | 1 |  |
| `e2e/recordproxy` | harness | 录制转发代理：真实上游流量 → 可回放夹具。 | 0 | 1 |  |
| `e2e/soulgw` | harness | soul 反思测试的确定性假上游网关。 | 0 | 0 |  |
| `e2e/stallgw` | harness | stream stall e2e 的确定性假上游网关（可注入停滞）。 | 0 | 1 |  |
| `internal/release` | harness | 发布脚本（release_slot 等）的回归测试。 | 0 | 0 |  |
| `internal/toolgolden` | harness | 工具结果文本的规范化，用于两侧运行时对照。 | 0 | 0 |  |
| `internal/cli` | assembly | 装配根：解析命令与配置，构造 home、ToolRegistry、MCP runtime、ChatRuntime、ColonyRuntime，并注册 serve/chat/queen/colony/session/skill/mcp/janitor 子命令。 | 31 | 4 |  |
| `internal/runtimehealth` | transport | 进程与宿主机资源健康快照，供 /api/health/resources 使用。 | 0 | 1 |  |
| `internal/server` | transport | HTTP/SSE 传输层：路由注册、会话管理、ChatRuntime 的 turn 队列、colony/tracker/sentinel 适配器与事件桥。 | 37 | 1 |  |
| `internal/server/uiembed` | transport | embed_ui 构建标签下的前端静态资源；未带标签时返回空，让服务端回退到磁盘目录。 | 0 | 1 |  |
| `internal/agents` | orchestration | Agent 规格与发现：读取 agents 目录、worker 元数据与能力描述。 | 2 | 3 |  |
| `internal/claudecode` | orchestration | Claude Code queen 运行时：会话记录与收件箱、claude -p 回合、转录读取与进程树管理。 | 3 | 1 |  |
| `internal/colony` | orchestration | Colony 运行时：worker 生命周期与并发闸门、run_worker 批量派发、report_to_parent 回报、playbook 驱动、目标验收与改进信号。 | 13 | 4 |  |
| `internal/host` | orchestration | Host 组合根：把 colony 运行时、事件总线与 run 注册表装成可 Start/Spawn 的宿主，供传输层调用。 | 7 | 1 |  |
| `internal/longmemeval` | orchestration | LongMemEval 基准运行器：数据集读取、题目选择、会话播种、判分与结果汇总（内部评测工具）。 | 1 | 2 |  |
| `internal/playbook` | orchestration | Playbook 定义与带并发上限的 lane 执行器。 | 1 | 2 |  |
| `internal/queen` | orchestration | Queen 档案与身份定义（queen_profiles 的 Go 移植）。 | 0 | 3 |  |
| `internal/queenmail` | orchestration | Queen 之间的信件引擎：信件存储、待投递队列、queens/mail.jsonl 审计日志与 hive MCP stdio 面。 | 3 | 3 |  |
| `internal/sentinel` | orchestration | Queen 停泊期的自治监督：nudge / escalate 判定、升级投递、入站回复路由与恢复。 | 6 | 1 |  |
| `internal/session` | orchestration | 会话记录与磁盘布局：状态、transcript、游标、fork 与冷恢复。 | 3 | 4 |  |
| `internal/sessionreport` | orchestration | 会话报告打包（report-bundle）：把会话、worker 与证据汇总成一份可交付摘要。 | 0 | 1 |  |
| `internal/tasks` | orchestration | 任务计划存储：tasks.json 的增删改查、归档与提醒状态。 | 3 | 3 |  |
| `internal/tracker` | orchestration | 共享结果账本：SQLite(WAL) tracker.db 的表注册、行写入与查询。 | 0 | 4 |  |
| `internal/triggers` | orchestration | 触发器定义、cron 解析与 triggers.json 持久化。 | 1 | 1 |  |
| `internal/agentloop` | runtime | 唯一执行原语：一轮内的 LLM 调用、工具批处理与并发、压缩、预算/宽限、judge、提醒注入与停泊原因。 | 11 | 8 |  |
| `internal/llmjudge` | runtime | 基于 LLM 的判定器适配：把成功标准与升级判定包装成可注入的 Judge。 | 1 | 2 |  |
| `internal/orchestrator` | runtime | Python 图编排时代遗留的 DataBuffer 原语（权限、幻觉内容校验）。 | 0 | 0 |  |
| `internal/attachments` | capability | 会话附件存取（上传、下载、类型与大小校验）。 | 0 | 2 |  |
| `internal/charts` | capability | chart_render 的纯 Go 渲染实现（SVG 生成 + 可选 PNG 栅格化）。 | 2 | 1 |  |
| `internal/credentials` | capability | 凭证规格与加密存储、OAuth 状态与表单校验。 | 2 | 5 |  |
| `internal/globaldb` | capability | 全局数据表（CRM / global data）的 SQLite 访问。 | 0 | 1 |  |
| `internal/hiveconf` | capability | 全局 configuration.json 的类型严格读取（与 Python 参考同规则），供装配与编排层取配置。 | 2 | 3 |  |
| `internal/llm` | capability | LLM 传输：provider 选择、Anthropic/OpenAI 协议、流式事件、模型目录与出站限速门。 | 3 | 16 |  |
| `internal/maintenance` | capability | Janitor：孤儿会话、缓存、工件与事件日志的清理规则与报告。 | 2 | 2 |  |
| `internal/mcpregistry` | capability | MCP 服务器注册表 CLI 后端：索引、安装/启用、健康检查与清单渲染。 | 6 | 3 |  |
| `internal/mcpregistry/nativecli` | capability | mcp2cli 3.8.0 的 Go 子集实现：MCP 服务器注册表 native CLI 后端。 | 2 | 2 |  |
| `internal/memory` | capability | Markdown 记忆文件：召回、写入、按天时间线与反思沉淀。 | 3 | 3 |  |
| `internal/pipeline` | capability | 配置驱动的 trigger 中间件：Stage 接口、按 order 执行/reject 短路/transform 的 Runner、stage 注册表与 7 个内置 stage（llm_provider/credential_resolver/mcp_registry/skill_registry/input_validation/rate_limit/cost_guard）。 | 7 | 1 |  |
| `internal/senders` | capability | 出站消息发送：正文渲染、抑制列表、每发件人锁与发送日志。 | 2 | 2 |  |
| `internal/skills` | capability | 技能发现/安装/校验，以及 agent 侧的技能读取工具。 | 3 | 6 |  |
| `internal/soul` | capability | Queen 的 soul：分区、字符上限、版本历史、提案与回滚。 | 1 | 2 |  |
| `internal/tools` | capability | 工具注册表：eager/searchable 分层、MCP 服务器加载与代理执行、结果规范化与溢出落盘。 | 2 | 12 |  |
| `internal/tools/builtin` | capability | 内置工具实现：read_file / write_file / terminal_exec 等，刻意不依赖 server 与 agentloop。 | 3 | 1 |  |
| `internal/tools/integrationkit` | capability | 集成工具的共享原语：传输、凭据与参数 schema 复用件，供 internal/tools/integrations 各模块使用。 | 1 | 2 |  |
| `internal/tools/integrations` | capability | 集成工具集（装配根）：生成目录、领域实现与 InstallBatch* 入口；领域实现按 internal/tools/integrations/<domain> 子包迁出，子包只依赖 integrationkit。 | 5 | 1 |  |
| `internal/tools/integrations/excel` | capability | Excel 领域子包（结构试点）：工具目录、OOXML 读写与 6 个 action 实现；只 import integrationkit。 | 1 | 1 |  |
| `internal/visionfallback` | capability | 文本主模型下的图像转述回退：为工具结果里的图片生成 caption。 | 1 | 2 |  |
| `internal/arghelp` | foundation | argparse HelpFormatter 的 Go 移植：usage 折行、选项列宽、choices 与 section 渲染，供 CLI/MCP 帮助共用。 | 0 | 2 |  |
| `internal/atomicfs` | foundation | 原子文件写入与目录替换，避免半写状态。 | 0 | 15 |  |
| `internal/envcontract` | foundation | 环境变量 / configuration.json 冻结契约的只读守卫（报告模式）。 | 0 | 0 |  |
| `internal/eventbus` | foundation | 进程内事件总线：AgentEvent、订阅分发、SSE 事件源与事件日志。 | 0 | 5 |  |
| `internal/hiveclock` | foundation | 可注入时钟：供基准与测试冻结对外可见时间。 | 0 | 3 |  |
| `internal/hivehome` | foundation | ~/.hive 目录布局解析与 ensure（queens/colonies/memories/credentials/...）。 | 0 | 21 |  |
| `internal/logging` | foundation | serve 路径共享的进程日志 sink（时间戳前缀、目标选择）。 | 1 | 2 |  |
| `internal/platform` | foundation | 平台差异：路径、进程与 shell 选择。 | 0 | 1 |  |
| `internal/pyenv` | foundation | 以 Python 语义解析环境变量覆盖，保证 Go 读取方与参考实现得到同一个值。 | 0 | 6 |  |
| `internal/quality` | foundation | 仓库静态质量门禁：分层 import 检查与文件大小检查，以 go test 形式运行。 | 0 | 0 |  |
| `internal/ratelimit` | foundation | 调用限速：LLM 请求与工具调用的桶式限流器（rate_limits 配置）。 | 1 | 3 |  |
| `internal/resources` | foundation | 进程与宿主内存资源读取（darwin/linux 分离实现）。 | 1 | 2 |  |
| `internal/runtimelogs` | foundation | 三级运行日志（summary.json / details.jsonl / tool_logs.jsonl）的 schema、原子写与 logger。 | 0 | 3 |  |
| `internal/schemas` | foundation | 跨包共享的数据结构与序列化约定（报告、计划、工具 schema）。 | 0 | 6 |  |
| `internal/storage` | foundation | 会话与检查点的文件存储（part JSON、cursor、index），写入走原子替换。 | 3 | 3 |  |
| `internal/version` | foundation | 版本字符串与构建信息。 | 0 | 3 |  |

## 分层检查

未发现反向依赖：所有 import 边都从浅层指向深层。

## 无导入方的包

以下包没有任何内部导入方。属于未接线代码或纯门禁/入口约定，列在这里供人工判断：

- 包 internal/orchestrator 没有任何内部导入方（可能是未接线代码或纯入口约定）

## 备注

- `internal/orchestrator`：未接线：仓库内无调用方；本 port 以 run_worker 的扇出取代图编排。
- `internal/charts`：未接线：chart_render 尚未注册进 ToolRegistry。
- `internal/soul`：反思的身份读取通过 soul 侧接口 QueenProfileSource（LoadQueenProfile / FormatQueenIdentityPrompt）由装配层 internal/cli 注入 queen 实现；soul 不再 import internal/queen，反向依赖已清零。

---

共 73 个包；层的定义与业务能力对照见 `docs/architecture/README.md` 与 `docs/architecture/capability-tech-crosswalk.md`。
