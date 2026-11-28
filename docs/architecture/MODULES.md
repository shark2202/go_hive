# 模块清单（生成物，勿手改）

> 由 `python3 scripts/gen_arch.py` 从 `architecture/model.yaml` + `go list` 生成。commit: `d90acd8b4e9d`
> 修改分层或职责请改 `architecture/model.yaml`，不要编辑本文件。

## 分层统计

| 层 | 包数 | 说明 |
| --- | ---: | --- |
| 入口层（`entry`） | 5 | 进程入口，只做 argv 解析与退出码转换，不含业务逻辑。 |
| 测试与评测装置（`harness`） | 12 | 不随发布物交付的测试装置：e2e 确定性假上游、契约对照工具与发布脚本测试；只被测试与内部评测入口引用。 |
| 装配层（`assembly`） | 6 | 组合根：读配置、构造 home / 工具注册表 / MCP runtime / 会话与 colony 运行时，再把命令路由到下游。 |
| 传输层（`transport`） | 7 | HTTP + SSE 边界：路由、会话查找、请求校验、事件镜像；不含 agent 决策。 |
| 编排层（`orchestration`） | 14 | Queen / Colony / Worker 的生命周期与协调：派发、报告、计划、账本、触发器、看护。 |
| 执行原语层（`runtime`） | 2 | 唯一的执行原语 AgentLoop：一次 turn 内的模型调用、工具批处理、压缩、预算与停泊。 |
| 能力层（`capability`） | 38 | 可被循环调用的能力：工具、集成、LLM 传输、记忆、技能、soul、凭据、出站消息。 |
| 基础层（`foundation`） | 17 | 无业务语义的底座：事件总线、文件存储、schema、原子写、目录布局、限速、质量门禁。 |
| 未映射层（`unmapped`） | 5 | 生成器为 model.yaml 尚未登记的包临时创建的层；补齐后应消失。 |

共 106 个包、311 条包间 import 边。

## 模块表

| 包 | 层 | 职责 | 直接依赖 | 被依赖 | 入口 |
| --- | --- | --- | ---: | ---: | :---: |
| `cmd/hive` | entry | hive 主进程入口：把 argv 交给 internal/cli.Runner 并以返回码退出。 | 1 | 0 | 是 |
| `cmd/hive-browser` | entry | 浏览器 CLI 客户端（hive-browser）：解析动词与 flag，转发给独立的 Browser Bridge 服务端。 | 1 | 0 | 是 |
| `cmd/hive-cred` | entry | 凭证 CLI 入口（hive-cred）：复用 internal/cli 的凭证子命令。 | 1 | 0 | 是 |
| `cmd/hive-longmemeval` | entry | LongMemEval 评测入口（内部工具）：用真实 queen 会话跑基准，并按基准自带判分提示评分。 | 3 | 0 | 是 |
| `e2e/cmd/memgw` | harness | 确定性 OpenAI 兼容假上游的启动命令（memory e2e）。 | 1 | 0 |  |
| `e2e/cmd/recordproxy` | harness | 录制转发代理命令：把真实上游流量录成 e2e 夹具。 | 1 | 0 |  |
| `e2e/cmd/runtimelogs` | harness | 三级运行日志双跑的启动命令（e2e）。 | 1 | 0 |  |
| `e2e/cmd/stallgw` | harness | 确定性假上游命令（stream stall e2e）。 | 1 | 0 |  |
| `e2e/cmd/storagecompat` | harness | HIVE_HOME 存储兼容 / G2 无损迁移夹具的 Go 侧命令。 | 13 | 0 |  |
| `e2e/escalationgw` | harness | worker 升级 e2e 的确定性假上游网关。 | 0 | 0 |  |
| `e2e/memgw` | harness | memory e2e 的确定性假上游网关。 | 0 | 1 |  |
| `e2e/recordproxy` | harness | 录制转发代理：真实上游流量 → 可回放夹具。 | 0 | 1 |  |
| `e2e/soulgw` | harness | soul 反思测试的确定性假上游网关。 | 0 | 0 |  |
| `e2e/stallgw` | harness | stream stall e2e 的确定性假上游网关（可注入停滞）。 | 0 | 1 |  |
| `hive` | entry | 公共 Go SDK facade：给衍生项目提供 loop/colony/session/queen-profile/tool/event/memory/soul/skill/playbook/tracker 的稳定封装，不暴露 internal 实现。 | 18 | 0 | 是 |
| `internal/release` | harness | 发布脚本（release_slot 等）的回归测试。 | 0 | 0 |  |
| `internal/toolgolden` | harness | 工具结果文本的规范化，用于两侧运行时对照。 | 0 | 0 |  |
| `internal/cli` | assembly | 装配根：解析命令与配置，构造 home、ToolRegistry、MCP runtime、ChatRuntime、ColonyRuntime，并注册 serve/chat/queen/colony/session/skill/mcp/janitor 子命令。 | 41 | 4 |  |
| `internal/cli/browsercli` | assembly | Browser CLI 命令族：bridge 客户端、命令解析、ensure/control、help/usage；通过 root adapter 接入 CLI 分发。 | 3 | 1 |  |
| `internal/cli/browsersupervisor` | assembly | Browser Bridge 生命周期子包：启动/停止、二进制解析、健康检查、进程属性与子进程环境；通过窄 seam 注入控制 keepalive。 | 2 | 2 |  |
| `internal/cli/cliargs` | assembly | CLI argparse 兼容层：命令/参数/位置参数类型、解析与 help/usage 渲染；不依赖 internal/cli。 | 1 | 2 |  |
| `internal/cli/cliguard` | assembly | CLI panic 边界：把未捕获 panic 映射为 Python compat 的崩溃头、traceback 与退出码；由 cmd/* 入口复用。 | 1 | 1 |  |
| `internal/cli/toolcatalog` | assembly | CLI 工具目录投影：task/search 工具、flat surface、Tool Library 与 lifecycle 行；不依赖 internal/cli 的运行时装配。 | 10 | 1 |  |
| `internal/runtimehealth` | transport | 进程与宿主机资源健康快照，供 /api/health/resources 使用。 | 0 | 1 |  |
| `internal/server` | transport | HTTP/SSE 传输层：路由注册、会话管理、ChatRuntime 的 turn 队列、colony/tracker/sentinel 适配器与事件桥。 | 43 | 5 |  |
| `internal/server/routesagents` | transport | Agent 路由子包：discover、delete、metadata 更新与 check-agent；通过回调注入 session 与凭据校验，不依赖父 server 包。 | 3 | 1 |  |
| `internal/server/routesapp` | transport | App-extra HTTP 路由子包：资源健康、browser status/stream 与 tab reveal；通过 Deps 注入资源快照，不依赖父 server 包。 | 2 | 1 |  |
| `internal/server/routesbrowser` | transport | Browser panel HTTP 路由子包：contexts/tabs/close 三端点；通过 Deps 注入 bridge URL 与 client，不依赖父 server 包。 | 1 | 1 |  |
| `internal/server/routeutil` | transport | HTTP/JSON 路由共享原语：JSON 写入、请求体解码、Python truthy/int 兼容与内部错误响应。 | 0 | 4 |  |
| `internal/server/uiembed` | transport | embed_ui 构建标签下的前端静态资源；未带标签时返回空，让服务端回退到磁盘目录。 | 0 | 1 |  |
| `internal/agents` | orchestration | Agent 规格与发现：读取 agents 目录、worker 元数据与能力描述。 | 2 | 4 |  |
| `internal/claudecode` | orchestration | Claude Code queen 运行时：会话记录与收件箱、claude -p 回合、转录读取与进程树管理。 | 3 | 1 |  |
| `internal/colony` | orchestration | Colony 运行时：worker 生命周期与并发闸门、run_worker 批量派发、report_to_parent 回报、playbook 驱动、目标验收与改进信号。 | 15 | 6 |  |
| `internal/host` | orchestration | Host 组合根：把 colony 运行时、事件总线与 run 注册表装成可 Start/Spawn 的宿主，供传输层调用。 | 7 | 2 |  |
| `internal/longmemeval` | orchestration | LongMemEval 基准运行器：数据集读取、题目选择、会话播种、判分与结果汇总（内部评测工具）。 | 4 | 2 |  |
| `internal/playbook` | orchestration | Playbook 定义与带并发上限的 lane 执行器。 | 2 | 3 |  |
| `internal/queen` | orchestration | Queen 档案与身份定义（queen_profiles 的 Go 移植）。 | 1 | 5 |  |
| `internal/queenmail` | orchestration | Queen 之间的信件引擎：信件存储、待投递队列、queens/mail.jsonl 审计日志与 hive MCP stdio 面。 | 3 | 3 |  |
| `internal/sentinel` | orchestration | Queen 停泊期的自治监督：nudge / escalate 判定、升级投递、入站回复路由与恢复。 | 6 | 2 |  |
| `internal/session` | orchestration | 会话记录与磁盘布局：状态、transcript、游标、fork 与冷恢复。 | 3 | 5 |  |
| `internal/sessionreport` | orchestration | 会话报告打包（report-bundle）：把会话、worker 与证据汇总成一份可交付摘要。 | 0 | 1 |  |
| `internal/tasks` | orchestration | 任务计划存储：tasks.json 的增删改查、归档与提醒状态。 | 4 | 5 |  |
| `internal/tracker` | orchestration | 共享结果账本：SQLite(WAL) tracker.db 的表注册、行写入与查询。 | 3 | 6 |  |
| `internal/triggers` | orchestration | 触发器定义、cron 解析与 triggers.json 持久化。 | 1 | 2 |  |
| `internal/agentloop` | runtime | 唯一执行原语：一轮内的 LLM 调用、工具批处理与并发、压缩、预算/宽限、judge、提醒注入与停泊原因。 | 11 | 10 |  |
| `internal/llmjudge` | runtime | 基于 LLM 的判定器适配：把成功标准与升级判定包装成可注入的 Judge。 | 1 | 2 |  |
| `internal/astanalyze` | capability | 只读仓库 AST 分析核心（ast_analyze）：逐文件事实、unit/import 图、wave、风险与有界报告；属 post-1.0 Go-only 扩展。 | 1 | 1 |  |
| `internal/attachments` | capability | 会话附件存取（上传、下载、类型与大小校验）。 | 0 | 2 |  |
| `internal/charts` | capability | chart_render 的纯 Go 渲染实现（SVG 生成 + 可选 PNG 栅格化）。 | 2 | 1 |  |
| `internal/credentials` | capability | 凭证规格与加密存储、OAuth 状态与表单校验。 | 2 | 8 |  |
| `internal/globaldb` | capability | 全局数据表（CRM / global data）的 SQLite 访问。 | 0 | 3 |  |
| `internal/hiveconf` | capability | 全局 configuration.json 的类型严格读取（与 Python 参考同规则），供装配与编排层取配置。 | 2 | 5 |  |
| `internal/llm` | capability | LLM 传输：provider 选择、Anthropic/OpenAI 协议、流式事件、模型目录与出站限速门。 | 3 | 20 |  |
| `internal/maintenance` | capability | Janitor：孤儿会话、缓存、工件与事件日志的清理规则与报告。 | 3 | 2 |  |
| `internal/mcpregistry` | capability | MCP 服务器注册表 CLI 后端：索引、安装/启用、健康检查与清单渲染。 | 6 | 3 |  |
| `internal/mcpregistry/nativecli` | capability | mcp2cli 3.8.0 的 Go 子集实现：MCP 服务器注册表 native CLI 后端。 | 2 | 2 |  |
| `internal/memory` | capability | Markdown 记忆文件：召回、写入、按天时间线与反思沉淀。 | 4 | 6 |  |
| `internal/nativehttp` | capability | 原生 HTTP 工具面（http_request/download/upload）：请求、响应、下载/上传、原子落盘与会话 cookie；属 post-1.0 Go-only 扩展。 | 1 | 1 |  |
| `internal/pipeline` | capability | 配置驱动的 trigger 中间件：Stage 接口、按 order 执行/reject 短路/transform 的 Runner、stage 注册表与 7 个内置 stage（llm_provider/credential_resolver/mcp_registry/skill_registry/input_validation/rate_limit/cost_guard）。 | 7 | 2 |  |
| `internal/senders` | capability | 出站消息发送：正文渲染、抑制列表、每发件人锁与发送日志。 | 2 | 2 |  |
| `internal/skills` | capability | 技能发现/安装/校验，以及 agent 侧的技能读取工具。 | 3 | 8 |  |
| `internal/soul` | capability | Queen 的 soul：分区、字符上限、版本历史、提案与回滚。 | 1 | 3 |  |
| `internal/tools` | capability | 工具注册表：eager/searchable 分层、MCP 服务器加载与代理执行、结果规范化与溢出落盘。 | 3 | 14 |  |
| `internal/tools/builtin` | capability | 内置工具实现：read_file / write_file / terminal_exec 等，刻意不依赖 server 与 agentloop。 | 7 | 2 |  |
| `internal/tools/integrationkit` | capability | 集成工具的共享原语：传输、凭据与参数 schema 复用件，供 internal/tools/integrations 各模块使用。 | 1 | 18 |  |
| `internal/tools/integrations` | capability | 集成工具集（装配根）：生成目录、领域实现与 InstallBatch* 入口；领域实现按 internal/tools/integrations/<domain> 子包迁出，子包只依赖 integrationkit。 | 21 | 2 |  |
| `internal/tools/integrations/ai` | capability | AI 平台领域子包：Pinecone / Weights & Biases / Langfuse / Hugging Face / Google Cloud Vision 的目录、39 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/cloudflare` | capability | Cloudflare 领域子包（第六个领域子包）：zones / DNS / analytics / security / cache / Workers·R2·Pages / 账户管理的目录、54 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/collab` | capability | 协作/知识库领域子包（P3 第十三个领域子包）：Jira / Confluence / Asana / Notion / Linear 的目录、60 个 action 实现、账号别名 helper 与 Linear GraphQL 传输；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/commerce` | capability | 电商/财务领域子包（P3 第十五个领域子包）：Shopify / QuickBooks / Plaid / Razorpay 的目录、29 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/crm` | capability | CRM/客户支持领域子包（P3 第十四个领域子包）：Salesforce / Pipedrive / Zoho CRM / Attio、Intercom / Zendesk / Freshdesk / Greenhouse 与 HubSpot / Obsidian 的目录、109 个 action 实现、OAuth2 刷新与账号 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/data` | capability | 数据库/数据平台领域子包（P3 第十二个领域子包）：Postgres / Redis / MongoDB / Supabase（含 pgwire/RESP 线协议）与 Snowflake / BigQuery / Databricks / Redshift / Azure SQL / Power BI / SAP 的目录、65 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/devops` | capability | DevOps/hosting 领域子包（第二个领域子包）：GitLab / AWS S3 / Vercel / Docker Hub 的目录、31 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/enrichment` | capability | Enrichment/search 领域子包（第五个领域子包）：Apify / Apollo.io / Lusha / SerpAPI / Exa 的目录、36 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/excel` | capability | Excel 领域子包（结构试点）：工具目录、OOXML 读写与 6 个 action 实现；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/gworkspace` | capability | Google Workspace 领域子包（P3 第十一个领域子包）：Google Sheets / Docs / Gmail / Calendar 的目录、39 个 action 实现、账号别名与 OAuth2 刷新 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/image` | capability | 图像生成领域子包：image_generate 目录、artifact/SSRF/reference 处理与 action 实现；只依赖 integrationkit 与 stdlib。 | 1 | 1 |  |
| `internal/tools/integrations/infra` | capability | 基础设施自动化领域子包（第七个领域子包）：Terraform / PagerDuty / Prometheus / Kafka / n8n / Tines 的目录、32 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/pdf` | capability | PDF 读取领域子包（第十个领域子包）：pdf_read 工具目录、单 action 实现与从零实现的解析栈（xref/流解码/词法/文本抽取/字体编码）；只 import integrationkit。 | 1 | 2 |  |
| `internal/tools/integrations/schedchat` | capability | 调度/聊天领域子包（第九个领域子包）：Brevo / Cal.com / Calendly / Discord / Mattermost 的目录、40 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/scrape` | capability | Web-scrape 领域子包（第十六个领域子包）：web_scrape 工具目录、单 action 实现、无依赖 HTML 词法/抽取栈与 go-rod 浏览器池；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/social` | capability | 社交媒体领域子包（第四个领域子包）：YouTube / Reddit / Twitter-X / Twilio / Pushover 的目录、36 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/tools/integrations/webintel` | capability | Web-intelligence 领域子包（第八个领域子包）：SimilarWeb / News / Google Maps 的目录、42 个 action 实现与共享 helper；只 import integrationkit。 | 1 | 1 |  |
| `internal/visionfallback` | capability | 文本主模型下的图像转述回退：为工具结果里的图片生成 caption。 | 1 | 2 |  |
| `internal/arghelp` | foundation | argparse HelpFormatter 的 Go 移植：usage 折行、选项列宽、choices 与 section 渲染，供 CLI/MCP 帮助共用。 | 0 | 2 |  |
| `internal/atomicfs` | foundation | 原子文件写入与目录替换，避免半写状态。 | 0 | 18 |  |
| `internal/envcontract` | foundation | 环境变量 / configuration.json 冻结契约的只读守卫（报告模式）。 | 0 | 0 |  |
| `internal/eventbus` | foundation | 进程内事件总线：AgentEvent、订阅分发、SSE 事件源与事件日志。 | 0 | 6 |  |
| `internal/hiveclock` | foundation | 可注入时钟：供基准与测试冻结对外可见时间。 | 0 | 3 |  |
| `internal/hivehome` | foundation | ~/.hive 目录布局解析与 ensure（queens/colonies/memories/credentials/...）。 | 0 | 26 |  |
| `internal/logging` | foundation | serve 路径共享的进程日志 sink（时间戳前缀、目标选择）。 | 1 | 2 |  |
| `internal/netguard` | foundation | 出站 URL/IP 安全策略：拒绝回环、私网、链路本地、CGNAT、ULA 与 metadata 地址，并在拨号时校验解析 IP。 | 0 | 1 |  |
| `internal/platform` | foundation | 平台差异：路径、进程与 shell 选择。 | 0 | 1 |  |
| `internal/pyenv` | foundation | 以 Python 语义解析环境变量覆盖，保证 Go 读取方与参考实现得到同一个值。 | 0 | 9 |  |
| `internal/quality` | foundation | 仓库静态质量门禁：分层 import 检查与文件大小检查，以 go test 形式运行。 | 0 | 0 |  |
| `internal/ratelimit` | foundation | 调用限速：LLM 请求与工具调用的桶式限流器（rate_limits 配置）。 | 1 | 3 |  |
| `internal/resources` | foundation | 进程与宿主内存资源读取（darwin/linux 分离实现）。 | 1 | 2 |  |
| `internal/runtimelogs` | foundation | 三级运行日志（summary.json / details.jsonl / tool_logs.jsonl）的 schema、原子写与 logger。 | 0 | 3 |  |
| `internal/schemas` | foundation | 跨包共享的数据结构与序列化约定（报告、计划、工具 schema）。 | 0 | 8 |  |
| `internal/storage` | foundation | 会话与检查点的文件存储（part JSON、cursor、index），写入走原子替换。 | 3 | 4 |  |
| `internal/version` | foundation | 版本字符串与构建信息。 | 0 | 4 |  |
| `internal/astgrep` | unmapped | 未在 architecture/model.yaml 中登记（生成器占位）。 | 0 | 2 |  |
| `internal/channels` | unmapped | 未在 architecture/model.yaml 中登记（生成器占位）。 | 0 | 2 |  |
| `internal/lsp` | unmapped | 未在 architecture/model.yaml 中登记（生成器占位）。 | 0 | 1 |  |
| `internal/streamtoken` | unmapped | 未在 architecture/model.yaml 中登记（生成器占位）。 | 1 | 1 |  |
| `internal/testframework` | unmapped | 未在 architecture/model.yaml 中登记（生成器占位）。 | 1 | 1 |  |

## 分层检查

发现反向依赖（低层 import 高层）：

- 反向依赖：internal/streamtoken（unmapped）导入了更高层 internal/credentials（capability）
- 反向依赖：internal/testframework（unmapped）导入了更高层 internal/atomicfs（foundation）

## 无导入方的包

无。

## 备注

- `internal/charts`：未接线：chart_render 尚未注册进 ToolRegistry。
- `internal/soul`：反思的身份读取通过 soul 侧接口 QueenProfileSource（LoadQueenProfile / FormatQueenIdentityPrompt）由装配层 internal/cli 注入 queen 实现；soul 不再 import internal/queen，反向依赖已清零。

---

共 106 个包；层的定义与业务能力对照见 `docs/architecture/README.md` 与 `docs/architecture/capability-tech-crosswalk.md`。
