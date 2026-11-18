# Go Hive 复刻状态面板（看门狗）

更新时间：2026-11-18（P0 可运行切片集成后；整树 `make ci` 退出码 0）
维护方式：每个里程碑更新；每 5 分钟由看门狗任务读取本文件 + `scripts/watchdog.sh` 的实时快照并汇报。

## 0. 时间治理（截止 2026-11-19 18:00，本地）

用户已两次提前交付时间：**2026-11-19 18:00 之前**，并要求「做好质量保障，时间治理」。据此确定口径：

- **已锁定、不再回退的资产**：2238 条测试全绿、2238/2238 parity 注解、2045 条上游引用 0 missing、六平台交叉编译、离线无 cgo。剩余时间的任何改动都不得让这些数字下降，也不得放宽门禁。
- **优先级（冲突时自上而下）**：① 门禁全绿且可复现 > ② 独立验证 PASS > ③ 覆盖面广度 > ④ 内部结构优雅。宁可少接一个路由，不交一个自证 PASS 的包。
- **本轮波次（按性价比排序）**：
  - W1（已落地）：低耦合会话读写路由组——`GET /api/sessions/history`、`GET /api/colonies/{id}/sessions`、`GET /api/colonies/{id}/active-session`、`DELETE /api/sessions/history/{sid}`、`PATCH /api/sessions/{sid}/title`、`POST /api/sessions/{sid}/reveal`。只依赖磁盘布局与既有 seam，无外部进程依赖，风险最低。
  - W2（已落地）：同一族的内存/SSE 读路径——`GET /api/sessions/live`（SSE）、snapshot、report-bundle、attachments 元信息。
  - W3（已落地）：`POST /api/sessions` 会话创建，含普通创建与 fork-into-new-colony 分支；另有 agents 发现/删除/元数据与 `GET /api/sessions/{id}/live_tools` 同批合并。
  - 三条波次均已并入 19:19 的整树 `make ci` 复跑，独立 verifier 对最终快照统一复核。
- **P0 可运行切片（2026-11-18 晚，后于上述基线）**：`hive serve` 装配真实 `ChatRuntime`；新增 `internal/llm/transport.go`、`internal/tools/builtin`、`internal/server/chat_runtime.go`/`chat_routes.go`、`e2e/p0_chat_test.go`。该批改动后整树 `make ci` 实测退出码 0：2,238/2,238 parity 注解、2,045 refs（0 missing）、55 shorthand/prose；E2E 与其 `-race` 均通过。
- **明确延期（2026-11-19 18:00 前不承诺）**：colony/worker 编排闭环、104 个集成工具、sentinel、browser/前端资源路由、`internal/platform` 终端层。这些在 `14`/`15` 文档按缺口登记，不虚报为「已完成」。
- **每轮固定收尾（缺一不可）**：六道门禁（fmt/vet/test/cross/parity/parity-targets）→ 刷新 `STATUS.md`、`11`、`14`、`15`、`README.md`、`00` 的实测计数 → 交独立 verifier 复核 → 回填 PASS/FAIL 与 `Command run:` 证据。未拿到 PASS 的包一律在本文标记「验证待跑」，不得写成已完成。
- **交付口径**：18:00 前交付的是一个**门禁全绿、可离线构建、会话域读写路由基本闭环**的 Go 移植快照，附一份逐项列明「已交付 / 已延期 / 已知差异」的清单；不以「全部完成」表述任何未验证部分。

## 1. 目标

基于 Go 复刻 OpenHive 全部功能特性，严格以相同语义的测试保证质量；发挥 Go 跨平台优势，需要 cgo 的地方用 purego / 纯 Go 实现规避；重构工程落盘 `./go_hive`；同时落盘产品目标、产品需求与设计、质量保证三套文档。

## 2. 总体进度

| 阶段 | 内容 | 状态 |
|---|---|---|
| P0 | 模块脚手架、atomicfs、hivehome、session、version | 已完成 |
| P1 | tracker（SQLite schema v3 / 触发器 / 白名单 / worker 绑定） | 已完成 |
| P1 | credentials（模型 / Fernet / 4 种存储 / 模板 / provider / store / 密钥引导 / OAuth2） | 已完成（独立验证 PASS） |
| P1 | memory、skills（发现/信任/覆盖/目录/安装请求/实时重载/工具门控） | 已完成 |
| P1 | triggers（missed-fire 计算 / cron+interval / 持久化） | 已完成 |
| P2 | eventbus（事件、类型、日志、策略、历史分页/尾部、快照） | 已完成 |
| P2 | storage / globaldb / schemas（会话与检查点存储、迁移、网格查询、run 模型） | 已完成（88 测试；整树 `make ci` 覆盖） |
| P2 | tasks（任务记录、状态流转、提醒、hook 门禁、作用域） | 已完成（128 测试，Lane I 已合并主树） |
| P2 | AgentLoop 会话核心（Lane G） | 已完成（248 测试，已合并主树） |
| P2 | orchestrator（DataBuffer / 输出校验 / 幻觉检测 / safe_eval 沙箱） | 已完成（40 测试，已合并主树） |
| P2 | 工具注册与分层（Lane F） | 已完成（52 测试，已合并主树） |
| P2 | MCP 连接池（`internal/tools`：按服务器名复用 + refcount、transition latch、健康检查、重连、清理） | 已完成（17 测试，16 条上游对等 + 1 条单例豁免） |
| P2 | llm 核心（catalog / capabilities / key pool / endpoints / stream events / key check / mock） | 已完成（107 测试，已合并主树） |
| P2 | llm provider 纯逻辑层（LiteLLM helper 对等：重试/退避/缓存断点/成本/JSON 修复/工具转换/请求塑形/extra kwargs/非流式 complete/AnthropicProvider 包装） | 已完成（`internal/llm` 共 215 测试） |
| P2 | playbook（convergence runner + rows helper） | 已完成（32 测试，已合并主树） |
| P2 | 社交平台限流器（`internal/ratelimit`：SQLite 计数、四层限额解析、停用判定） | 已完成（21 测试，已合并主树） |
| P2 | Colony 运行时 | 进行中（colony.db 记账 + resume 推导 + worker 定义 + colony seed + browser reap + worker 调度器 + 运行时 overseer + worker 工具分层 spawn + worker binding 传播 + started_at + worker loop-config 覆盖白名单 + 自适应 worker 运行预算 共 84 测试；server 会话与 worker 升级路由 9 测试、worker 列表预算遥测序列化 1 测试、global-db 数据路由纯逻辑 8 测试、会话目录解析 13 测试（8 上游对等 + 5 验证补强豁免）、事件历史游标分页 HTTP 路由 15 测试（12 上游对等含 4 条截断用例 + 3 条 Python `int()`/ASCII 空白/`json.dumps` 数值估计豁免）、会话详情四路由 9 测试（5 上游对等 + 4 豁免：`is_loading` 202、冷会话磁盘投影、`entry_points` 顶层 next-fire/火计数、triggers 路由包装）；DM 会话 fork 纯逻辑 5 测试已落地，HTTP 路由待服务端） |
| P3 | HTTP/SSE 服务、CLI、purego 终端层 | 进行中（10 组 20 条配置类路由已接线：models / sentinel / features / llm-sections / llm / provider-library / rate-limits / profile+avatar / external-skills，另接 agents 四路由（`GET /api/discover`、`DELETE /api/agents`、`PATCH /api/agents/metadata`、`POST /api/credentials/check-agent`）、会话创建 `POST /api/sessions`、`GET /api/sessions/{id}/live_tools`、会话事件历史游标分页、会话详情四路由（`GET`/`DELETE /api/sessions/{id}`、`/stats`、`/triggers`）与触发器生命周期六路由（创建、PATCH、`run`、`activate`、`deactivate`、`colony/resolve_missed`）及配套 timer/webhook 运行时；CLI 已完成：`cmd/hive` 根入口 `serve`/`open`/`queen`/`colony`/`session`/`chat` + 12 个 `skill` 子命令 + `debugger` + `mcp` 14 个子命令（整段透传注册）+ `janitor` 2 个子命令（`run`/`status`）+ LLMJudge + 独立控制台入口 `cmd/hive-cred`（`token`/`browse`/`inspect`）；P0 会话切片已接线：`POST /api/sessions` 创建真实会话（`llm.NewHTTPProvider` + `builtin` 工具 + `ChatRuntime` 持久化 transcript）、`POST /api/sessions/{id}/chat`、`GET /api/sessions/{id}/events`（会话事件 SSE）；其余路由模块、完整模型流式管线与终端层待接） |
| P2 | janitor / 保留策略（tier-1 日志、tier-2 worker 深清理、tier-3 queen 卫生、安全上下文、存档、server marker） | 已完成（库层 42 测试；CLI 入口 `hive janitor run/status` 已接线并新增 12 条 CLI 测试） |
| P4 | 产品目标 / 需求 / 设计 / 质量保证 / parity 矩阵 / CI | 文档已落盘；P0 集成后 `make ci` 退出码 0，parity 2,238/2,238、2,045 条引用 0 missing、55 shorthand/prose，六平台交叉编译通过 |

### 本轮完成（P0 可运行切片：serve / 会话创建 / chat / 真实 HTTP LLM / 内建工具 / 持久化 / E2E，门禁 PASS）

P0 口径见 `/Users/mac/hive/.omp/WATCHDOG.md`：交付可运行切片，不代表 Python 全量对等。

- `internal/llm/transport.go`（新增）：`NewHTTPProvider` 为 `Provider.Call` 装上真实 HTTP 传输，`(*Provider).AComplete` 满足 `llm.Completer`；OpenAI-compatible 走 `POST {api_base}/chat/completions`（基址缺 `/v1` 时自动补，Bearer），Anthropic Messages 走 `POST {api_base}/v1/messages`（`x-api-key` + `anthropic-version: 2023-06-01`，代理端点改 Bearer）；模型族决定协议（`anthropic/*`、`claude-*`、`kimi/*`、`hive/*` → Anthropic，`openrouter/*` → OpenAI）；`transport_test.go` 覆盖文本/工具调用/429 映射/Anthropic 文本与 tool_use/`Completer` 断言/`NewProvider` 不设 `Call`。
- `internal/tools/builtin`（新增）：`Defs()` + `Toolbox.Execute` 提供 `read_file`/`write_file`/`terminal_exec`；沙箱根默认进程 cwd，拒绝越界路径与符号链接逃逸；`read_file` 上限 10 MiB / 2000 行 / 50 KiB 输出，`write_file` 原子写，`terminal_exec` 用 `/bin/bash -lc`、默认 60 秒、上限 600 秒、进程组超时终止、每路 256 KiB；`builtin_test.go` 12 条。
- `internal/server/chat_runtime.go`、`chat_routes.go`（新增）：`NewChatRuntime` 用注入的 `llm.Completer`、工具定义与执行器驱动既有 `agentloop`，每会话一个 worker goroutine 串行消费消息；`POST /api/sessions/{id}/chat` 对空消息 400、未知会话 404、无 LLM 503、已 spawn colony 409 `session_locked`，接受后返回 200 `{"status":"queen","delivered":true}`；`GET /api/sessions/{id}/events` 为会话事件 SSE（20 秒心跳、`?types=` 过滤、背压丢帧）。
- `internal/agentloop.FileConversationStore` 持久化 transcript：`<HIVE_HOME>/queens/default/sessions/<id>/conversations/parts/*.json`；`ChatRuntime` 构造时经 `RestoreConversation` 恢复同 ID 会话。
- `internal/cli/loader.go`（修改）：`defaultServe` 的 `CreateSession` 走 `createChatSession`；`resolveChatProvider` 按「请求 model > `serve --model` > `configuration.json` 的 `llm.model` > `gpt-4o-mini`」解析，provider 由配置或模型前缀推断，key 取 `ANTHROPIC_API_KEY`/`OPENAI_API_KEY` 等映射（`llm.api_key_env_var` 可覆盖）并以 `HIVE_API_KEY` 兜底，base 取 `ANTHROPIC_API_BASE`/`ANTHROPIC_BASE_URL` 与 `OPENAI_BASE_URL`/`OPENAI_API_BASE`；本地模型（`ollama/vllm/lm_studio/llamacpp`）不需要 key。缺 key 时会话仍可列出，chat 返回 503，不伪造成功。
- `e2e/p0_chat_test.go`（新增，`//go:build e2e`）：`go test -tags e2e ./e2e/...` 以 `httptest` + 脚本化 `llm.Completer` 走 create → chat → 持久化 transcript → 第二次 chat，不访问网络；工具执行由 `internal/tools/builtin` 单测覆盖，生产装配在 `createChatSession`。
- **验证状态（PASS）**：`make ci` 退出码 0——`fmt-check`、`vet`、`test`、六平台 `cross`（darwin/linux/windows × amd64/arm64）与 `parity` 全部通过；parity **2,238/2,238 已注解、0 missing**，`check_parity_targets` **2,045 refs、0 missing files/tests、55 shorthand/prose**。`go test -tags e2e -count=1 ./e2e/...` 与 `go test -race -tags e2e -count=1 ./e2e/...` 均 ok；凭据门禁 `TestCredentialFlowEnvKeyReachesUpstream` 通过（真实二进制 + 假 OpenAI 上游收到 `Authorization: Bearer test-key`）；二进制冒烟 `dist/hive-darwin-arm64 --version` → `hive 1.0.14-go.1`，健康检查/启动就绪、`POST /api/sessions` 201、无 key 时 chat 503 `{"error":"No LLM configured for this session"}`。
- **明确延期**：216 条 Python 路由完整对等、104 个集成工具、Sentinel、browser、React dashboard（仅在存在 `frontend/dist` 时提供静态资源，仓库未附带该产物）、queen/colony 多 worker 编排闭环、逐 token 模型流（当前 SSE 只发会话总线事件）。

### 上一轮完成（agents 发现/删除/元数据路由 + 会话创建与 live_tools，基线 2150→2215）

- `internal/agents/`（新增包，6 个非测试文件 / 781 行，34 条测试）：移植 `framework/agents/discovery.py` 与 agent 路径校验。`DiscoverAgents` 汇总 colony 目录（`IsColonyDir` 要求至少一个 worker 配置）、跳过 `deleted` 为真的 metadata、`pythonTitle` 复刻 `str.title()` 的大小写游标语义、`birthtime_*` 三平台取创建时间；`WorkerEntry` / `AgentEntry` 的 name/description/task/spawned_at/icon 用 `any` 保留 Python `dict.get` 的 `None` 传播；`CountSessions` / `CountRuns` / `LastActive` / `ExtractAgentStats` 对齐 workers + queens 树遍历与 `runs.jsonl` 去重；`FindWorkerConfigs` + `pyStemSuffix` 复刻 `pathlib.Path.suffix`（`i<=0 || i>=len-1` 时 suffix 为空）；`validate.go` 的 `AllowedRoots` 校验 `$HIVE_HOME/colonies/`、`exports/`、`examples/`、`$HIVE_HOME/agents/` 四个根与越界消息。
- `internal/server/routes_agents.go`（新增）：`GET /api/discover`（按活会话注册表标注 `is_loaded`，返回 `Your Agents` 分组与完整 worker 列表）、`DELETE /api/agents`（软删除写 `metadata.json` 的 `deleted: true`，`ensure_ascii=True` 转义非 ASCII；`purge` 时删除目录；`$HIVE_HOME/agents/` 下框架 agent 403）、`PATCH /api/agents/metadata`（`ensure_ascii=False` 原样写 Unicode 图标，保留其余键）、`POST /api/credentials/check-agent`（路径校验失败 400，其余 500 `Internal server error while checking credentials`）。
- `internal/server/routes_session_create.go` + `routes_session_live_tools.go`（新增）：`POST /api/sessions` 的普通创建与 fork-into-new-colony 分支、`GET /api/sessions/{id}/live_tools`（eager + ask_user 工具视图，非注册表全集）。
- `internal/server/routes_agents_test.go`（新增，443 行，12 条）、`routes_session_create_test.go`（新增，502 行，14 条）、`routes_session_live_tools_test.go`（新增，331 行，5 条）：1 条真实对等（`tests/test_validate_agent_path.py::TestHTTPEndpointsRejectMaliciousPaths::test_check_agent_credentials_rejects_traversal`），其余 30 条 `Parity-exempt` 逐条钉住软删除 ASCII 转义、PATCH 原样 Unicode、purge、框架 agent 403、发现载荷字段、创建分支、live_tools 形态，以及随后补强的 check-agent 余下分支、尾随数据/空体、数值字面量保真与 `decodeJSONBytes` 语义（`routes_agents_test.go` 后 3 条）。
- 主树测试 2150→**2215**（+65：agents 34、server 31）；注解构成 1,838 个仅带 `.py` `Parity:` 引用、376 个仅 `Parity-exempt`（1 个两者兼有）；refs 2007→**2035**、shorthand/prose 46、0 missing；六道门禁实测全绿（`make ci` 退出码 0，六平台交叉编译通过）。
- 计数校准（19:25 复测）：上一版文档把 server 记为 276 条、全树记为 2,212，实测为 279 与 2,215；差值为 `routes_agents_test.go` 的三条 `Parity-exempt` 补强测试，无新增实现代码。

### 上一轮完成（W3 report-bundle 与 `internal/sessionreport` + colony 生命周期路由，基线 2120→2150）

- `internal/sessionreport/`（新增包，移植 `framework/server/_session_report.py`，346 行）：`ScrubCredentials` 八类凭据正则（JWT、Bearer、api_key/secret 赋值、OAuth 回调 URL、sk/pk/rk、GitHub、Slack、Google）、`WriteUserReportMarker`（`user_report.json`，severity 非法回退 medium，覆盖写、`ensure_ascii` 风格 `\uXXXX` 转义含代理对）、`BuildSessionReportBundle`（tar.gz BestCompression、先排除 `data/reports` 再计数、文本后缀脱敏并把非法 UTF-8 替换为 U+FFFD、二进制原样、单文件 256 MiB / 合计 1 GiB 预算与 `omitted_paths`、manifest）、`Sha256Hex`、`SeverityValid`。
- `internal/server/routes_session_report.go`（新增）：`POST /api/sessions/{id}/report-bundle` —— 以 `queen_resume_from or id` 解析存储目录（回退 `FindQueenSessionDir`），缺失返回 404 `{"error":"session folder not found"}`；请求体按 `str(value or "")` 取 description、severity 缺省或未知回退 medium；落 marker 后在 `os.TempDir()/hive-session-reports` 写 `report_%Y%m%d_%H%M%S_<sid>.tar.gz`，返回 `{ok, filename, bundle_path, size, sha256, content_type, marker, stats}`。
- `internal/server/routes_session_colony_test.go`（W3 colony 车道，18 条豁免）：colony 会话生命周期与 dismiss/pivot 路由；上游对应模块无 Python 测试，契约由实现固定。
- 测试 2120→**2150**（+30：sessionreport 8、report-bundle 路由 4、colony 生命周期路由 18，全部为 `Parity-exempt`）；注解构成 1,812 个仅带 `.py` `Parity:` 引用、337 个仅 `Parity-exempt`（1 个两者兼有）；refs 仍为 **2,007**、shorthand/prose 44、0 missing。
- 说明：本节快照之后，W3 agents 与 W4 create-session 两条并发车道已合并；最终计数见上方「本轮完成」。

### 上一轮完成（W2 实时会话/附件路由组 + 全库注释清理，基线 2101→2120）

- `internal/server/routes_session_live.go`（新增）：`GET /api/sessions/{id}/snapshot` 投影（活会话返回 `_session_to_live_dict` 形态，未知会话 404 `Session '<id>' not found`）；`GET /api/sessions/live` 以 SSE 首帧推送全量会话快照，随后由既有事件循环续推。
- `internal/server/routes_session_attachment.go`（新增）：`POST /api/sessions/{id}/attachments` 上传——文件名经 `internal/attachments` 净化去重、二进制/非 UTF-8 拒绝、按允许清单强制 MIME 与扩展名、CSV 与 64 KiB 文本预览；`GET /api/sessions/{id}/attachments/{name}` 回读并对越界路径返回 404；`_colony_root_for_session_dir` 复刻 colony 根推导（`meta.json` 的 `colony_id` → colony 树，回退 queen DM 目录）。
- 新增包 `internal/attachments`（移植 `tools/src/aden_tools/utils/attachments.py`）：`SanitizeBasename`（路径剥离、非法字符替换、空白折叠、200 字符上限）、`DisambiguateFilename`（`-2`/`-3` 去重后缀）、`LooksLikeText`（NUL/控制字符与 UTF-8 探测）、`TextExtToMIME`（共享允许清单）。
- 新增包 `internal/runtimehealth`（移植 `framework/host/runtime_health.py`）：网络错误标记分类、上游健康状态生命周期、降级原因 240 字符截断。
- `internal/colony/runtime.go` 增 `ActiveWorkerCount`（QUEUED/PENDING/RUNNING 计数）；`worker_started_at` 补 `started_at` 墙钟默认值与 `WorkerInfo` 形态。
- 注释清理：按「除 `// Parity:` / `// Parity-exempt:` 门禁注解外禁止代码注释」的要求，用 `go/parser` 按字节区间删除全库非注解注释（累计 4,604 + 820 + 3 个区间），`gofmt -l` 归零；`//go:build`、`//go:embed` 等编译指令保留。
- 测试 2101→**2120**（+19：live 2、attachment 6、attachments 5、active_worker_count 1、colony started_at 2、runtimehealth 3）；注解构成 1,812 个仅带 `.py` `Parity:` 引用、307 个仅 `Parity-exempt`（1 个两者兼有）；refs 2004→**2007**、shorthand/prose 44、0 missing。六道门禁实测全绿；**独立验证待跑**。

### 上一轮完成（会话读写路由组 W1，基线 2088→2101）

- `internal/server/routes_session_history.go`（新增，262 行）：移植 `framework/server/routes_sessions.py` 的六条会话盘面路由。`GET /api/sessions/history` 以 `session.ListColdSessions` 读磁盘列表，再按活会话注册表做覆盖——命中的会话 `cold: false / live: true`，`meta.json` 缺 colony 绑定时从活会话补 `agent_name` / `agent_path`；`GET /api/colonies/{id}/sessions` 先以 `validateColonyIDSegment` 拒绝空串、`.`、`..`、路径分隔符与 NUL（400 `Invalid colony_id`），再列出 colony 树并保留 `colony_fork` 会话；`GET /api/colonies/{id}/active-session` 返回最新一条 `has_messages` 为真的会话，没有则显式 `"session": null`；`DELETE /api/sessions/history/{sid}` 先尽力停掉活会话，再用 `FindQueenSessionDir` 定位（列举失败回退 default queen 路径），目录为符号链接时按 `shutil.rmtree` 语义返回 500 `Cannot call rmtree on a symbolic link` 且不 unlink 目标；`PATCH /api/sessions/{sid}/title` 仅认 DM 会话（404 `No such queen conversation`），标题必须是字符串（400 `title must be a string`），空串清除并回 `null`，写盘失败落 aiohttp 通用 500 信封；`POST /api/sessions/{sid}/reveal` 对活会话按 `QueenResumeFrom`/`QueenName` 解析存储目录，`MkdirAll` 后交给 `platform.OpenPath`，启动器失败回 `str(exc)` 文本 500。
- `internal/server/routes_session_history_test.go`（新增，441 行，10 条）：1 条真实对等 `core/tests/test_session_titles.py::test_title_route_sets_and_clears_a_users_title`；9 条 `Parity-exempt` 逐条钉住未知/colony 会话 404、非字符串标题 400 且不落盘、写盘失败通用 500、冷列表的活会话覆盖与 colony fork 过滤、colony id 校验、colony 列表保留 fork、active-session 跳过空会话与空结果 `null`、删除活会话并移除目录、reveal 的路径解析与启动器失败、符号链接目录拒绝删除。
- `internal/session/cold.go`（241 行，配套 `cold_test.go` +2 条豁免）：`ListColdSessions` / `ListColonySessions` / `ColonyActiveSession` 的 cold 列表纯逻辑——汇总 `meta.json` 与 `summary.json`、按 session_id 严格降序、`has_messages` 要求 `conversations/` 存在且 `message_count > 0`、colony 列表不过滤 fork、active-session 取最新非空会话。
- `internal/platform/open.go`（新增，39 行）：按 darwin/windows/other 选 `open`/`explorer`/`xdg-open`，`Start` 后由 goroutine `Wait` 收尸但忽略退出码，复刻 `Popen` 不 `wait()` 的语义（成功只代表启动器已 spawn）。
- `internal/hivehome/home.go`（修改）：新增 `ColonyQueensDir` / `ColonyQueenSessionDir` 等 colony 树路径解析，供历史、reveal 与删除路由共用；`internal/server/session_dirs.go` 追加 1 条尾分隔符/坏目录惰性遍历豁免测试；`internal/server/app.go`、`routes_sessions.go`（修改）把六条路由挂到既有 `SessionRouteDeps{Home, Manager, Reveal}` 上。
- 主树测试 2088→**2101**（+13：历史路由 10、cold 列表 2、目录解析 1）；注解构成 1,810 个仅带 `.py` `Parity:` 引用、290 个仅 `Parity-exempt`（1 个两者兼有）；refs 2003→**2004**、shorthand/prose 43、0 missing。六道门禁实测全绿；**独立验证待跑**。

### 本轮完成（触发器路由组与运行时，基线 2059→2088）

- `internal/server/routes_session_triggers.go`（新增，586 行）：移植 `framework/server/routes_sessions.py` 的触发器生命周期路由。`POST /api/sessions/{id}/triggers` 校验 `name`/`task` 必填、仅接受 timer、`cron` 或 `interval_minutes` 二选一，经 `_slugify_trigger_id` 生成 id，启动计时器失败时回滚定义，成功则置 `enabled`、持久化并发布 `TRIGGER_ACTIVATED`；`PATCH .../{trigger_id}` 合并任务与配置（`_validate_merged_timer_config` 对 cron/interval 复检），活动触发器先停旧计时器/退订 webhook 再重挂，发布 `TRIGGER_UPDATED`；`POST .../{trigger_id}/run` 走 colony/queen 门禁并以 `forced: true` 注入，随后 `_emit_trigger_fired`；`POST .../colony/resolve_missed` 接受 `{trigger_id: decision}`，逐值产出 `invalid_decision:` 标记；`POST .../{trigger_id}/activate` 启动 timer/webhook、以 `already_active` 短路，事件 `config_out` 合并 next-fire 与火统计；`POST .../{trigger_id}/deactivate` 停止计时器、退订 webhook、清除 next-fire、发布 `TRIGGER_DEACTIVATED`。请求体统一以 `UseNumber()` 解码，保留 Python 的 int/float 区分（`str(5) == "5"`，`5.0` 仍为 `5.0`）。
- `internal/server/trigger_runtime.go`（新增，708 行）：运行时辅助层。`slugifyTriggerID` 复刻小写归一、非字母数字折叠为 `-`、40 字符截断、空名回退 `schedule`、`user-` 前缀与 `-2`/`-3` 去重；`NextDueFrom` 先 cron 后 interval；`StartTriggerTimer` 按 `interval_minutes × 60` 或 `NextCronMatch` 播种下一触发、非法/缺失配置回退 60 秒；另有 `runTimerLoop`、`EmitTriggerFired`（火计数 + `last_fired_at` 毫秒 + `TRIGGER_FIRED`）、`PersistActiveTriggers`、`ResolveMissed`、`injectCatchUp`、`StartTriggerWebhook`（按 trigger id 作 stream 过滤订阅，再挂共享监听器）、`handleTriggerWebhook`、`webhookMethods`。`TriggerInjector` 为可选接口，把 `agentloop.TriggerEvent` 注入 queen。
- `internal/server/webhook_server.go`（新增，270 行）：移植 `framework/host/webhook_server.py` 的 HTTP 契约——精确路径查找、四类 JSON 响应（404/405/401/202）、JSON-or-raw-body 解析、可选 HMAC-SHA256 校验、每次接受发布一条 `WEBHOOK_RECEIVED`。Go 端在请求时查找路径，故启动后新增路由立即生效；上游 aiohttp 在启动前注册路由，这是有意保留的平台差异。
- `internal/server/routes_session_triggers_test.go`（新增，1,409 行，29 条）：8 条真实对等——3 条移植 `framework/server/tests/test_api.py::TestSessionCRUD` 的 `test_update_trigger_task` / `test_update_trigger_cron_restarts_active_timer` / `test_update_trigger_cron_rejects_invalid_expression`，5 条移植 `tests/test_missed_triggers.py` 的五个 `test_resolve_missed_*`；21 条 `Parity-exempt` 逐条写明理由，钉住创建/运行/激活/停用/resolve 路由的错误信封与事件载荷、webhook 按 stream 分发、计时器种子与火统计、`slugifyTriggerID`、`NextDueFrom`，以及内嵌 webhook 服务器的响应契约。
- 主树测试 2059→**2088**；注解构成 1,809 个仅带 `.py` `Parity:` 引用、278 个仅 `Parity-exempt`（1 个两者兼有）；refs 1995→**2003**、shorthand/prose 43、0 missing。

### 上一轮完成（会话详情四路由，基线 2050→2059）

- `internal/server/routes_session_detail.go`（新增，254 行）：移植 `framework/server/routes_sessions.py` 的会话详情接口。`GET /api/sessions/{id}` 四分派——活跃会话走 `_session_to_live_dict` + `entry_points`；加载中（`SessionManager.is_loading`）返回 202；已结束但磁盘有 part 文件的冷会话返回 `get_cold_session_info` 投影（`session_id` / `cold: true` / `live: false` / `has_messages` / `created_at`（`st_ctime` 经 `ModTime`，被真值 `meta.json created_at` 覆盖）/ `agent_name` / `agent_path` / `colony_spawned` / `spawned_colony_id`）；其余 404 且正文为 `{"error": "Session '<id>' not found"}`。`DELETE /api/sessions/{id}` 对已停止/未知会话同样 200 `stopped: true`；`GET /api/sessions/{id}/stats` 透传 colony `get_stats`；`GET /api/sessions/{id}/triggers` 复用 `triggers.BuildTriggerView`。
- `internal/server/routes_session_detail.go`：`entry_points` 逐条对齐上游实测契约——每条 `{id, name: description 或 id, entry_node: "", trigger_type, trigger_config, task}`，顶层另有 `next_fire_in`/`next_fire_at`（毫秒，`int((time.time()+remaining)*1000)`）与 `fire_count`/`last_fired_at`。这些 next-fire/火统计字段刻意**不**嵌进 `trigger_config`（与 `BuildTriggerView` 的合并形态不同），并有专门测试钉住。
- `internal/server/session.go`（修改）：`Session` 增加触发器视图状态（`AvailableTriggerDefs` / `AvailableTriggerOrder` / `ActiveTriggers` / `NextFireTimes` / `FireStats` / `TriggerClock`）与配套访问器；`SessionManager` 增加 `loading` 标志与 `IsLoading`/`MarkLoading`/`ClearLoading`。
- `internal/server/routes_sessions.go`（修改）：`SessionRouteDeps` 增加 `Manager *SessionManager`；`RegisterSessionRoutes` 追加详情、删除、stats、triggers 四条路由；`liveSessions` 优先取 `Manager`，回退 `LiveSessions`。`internal/server/app.go`（修改）：`AppDeps.Manager` 透传给路由注册。
- `internal/server/routes_session_detail_test.go`（新增，327 行，9 条）：5 条逐条移植 `core/framework/server/tests/test_api.py::TestSessionCRUD`（`test_get_session_found` / `test_get_session_not_found` / `test_stop_session` / `test_stop_session_not_found` / `test_session_stats`）；4 条 `Parity-exempt` 钉住 `is_loading` 202 分支、`get_cold_session_info` 磁盘投影、`entry_points` 顶层 next-fire/火统计字段、`handle_list_triggers` 路由包装。
- 主树测试 2050→**2059**；注解构成 1,801 个仅带 `.py` `Parity:` 引用、257 个仅 `Parity-exempt`（1 个两者兼有）；refs 1990→**1995**、shorthand/prose 43、0 missing。

### 上一轮完成（事件历史端点 Verifier 复审：4 项分歧修复，基线 2044→2050）

独立 verifier 对上一版事件历史端点给出 **PARTIAL**（自证 PASS 不成立），指出四项实锤，均已修复并补回归测试：

- **假豁免**：上一版把 `_truncate_large_event` 标为 `Parity-exempt` 并称「上游无测试」，实际 `core/tests/test_events_history_tail.py` 有 4 条 `test_truncate_*`。现已逐条移植为 `TestTruncateStripsFullRequestField` / `TestTruncateReplacesOversizedField` / `TestTruncateLeavesSmallEventsUntouched` / `TestTruncateHandlesMissingOrNonDictData`，注解改回 `// Parity: tests/test_events_history_tail.py::...`，假豁免删除。
- **空行判定分歧**：`_read_events_page` 用 Python `bytes.strip()`，只剥离 ASCII 空白；上一版用 `bytes.TrimSpace`（Unicode 集），会把只含 U+00A0 的行误判为空行而改变分页偏移。现改为 `bytes.Trim(text, " \t\n\r\v\f")`，回归测试 `TestEventsHistoryOnlyASCIIBlankLinesAreDropped` 复现 NBSP 行仍占行、尾/中/首页偏移 72/27/0、页大小 4/5/3。
- **数值查询参数分歧**：`_int_param` 走 Python `int()`，接受 Unicode 空白、`+`/`-`、数字间单下划线；上一版用 `strconv.Atoi`。现以 `parsePythonInt` 复刻（`unicode.IsSpace` 裁剪、符号、严格单下划线夹数字），非 ASCII Nd 数字按文档化偏差拒绝；回归测试 `TestQueryIntParamFollowsPythonInt` 覆盖 16 组输入。
- **截断尺寸估计分歧**：`len(json.dumps(value, default=str))` 的数字渲染与 Go `'g'` 格式不同（整数任意精度、`-0`→`0`、浮点 `repr` 在十进制指数 `[-4,16)` 内定点否则科学计数且指数至少两位）。现新增 `writePythonFloat`/`writePythonNumber`/`fixedFloat`/`scientificFloat`，并用 `json.Number` 保留字面量（`ReadEventsPage` 经 `decodeEventLine` 以 `UseNumber` 解析，尾随数据按 `json.loads` 拒绝）；`TestPythonJSONLenMatchesPythonDumps` 钉住 13 组长度，数值边界用例断言 `_original_bytes == 81000`（`json.dumps` 度量的是字段值列表本身，非外层 dict 的 81013）。
- `internal/eventbus/events_history_tail_test.go` 的 deferral 清单由 13 条改为 9 条（4 条 `test_truncate_*` 已移交并落地于 server 车道）。
- 主树测试 2044→**2050**；注解构成 1,796 个仅带 `.py` `Parity:` 引用、253 个仅 `Parity-exempt`（1 个两者兼有）；refs 1986→**1990**、shorthand/prose 43、0 missing；`gofmt -l` 空、`go vet` 静默、`go test ./...` 全绿、两道 parity 门禁退出码 0。

### 同轮初版（`GET /api/sessions/{id}/events/history` 游标分页，基线 2035→2044）

- `internal/server/routes_events_history.go`（新增，537 行）：完整移植 `framework/server/routes_sessions.py` 的事件历史接口。`ReadEventsPage` 逐行对齐 `_read_events_page`——从 EOF 或 `before_offset` 向后按 64KB 块读取，直到累计换行数超过 `limit`；`pos == 0` 时把偏移 0 计为行首，否则丢弃块首的半行；每行止于下一下一行首减一或 `upper`；空行按 Python `bytes.strip()` 的 ASCII 空白集（`" \t\n\r\v\f"`）过滤；以 `UseNumber` 解析 JSON、保留数字字面量，尾随数据按 `json.loads` 拒绝并跳过该行；返回 `(events, start_offset, total)`，游标页 `total = -1`，尾页才做全文件换行计数。`countEventsTotal` 对齐 `_count_events_total` 的 1MB 扫描与「无尾换行补 1」。`TruncateLargeEvent` 对齐 `_truncate_large_event`：`full_request` 替换为 `{"_truncated": true, "_reason": "diagnostic_field"}`，嵌套 dict/list 超过 64KB 替换为带 `_original_bytes` 的标记，标量跳过，返回浅拷贝且不改输入；`pythonJSONLen` 复刻 `len(json.dumps(value, default=str))` 的分隔符、键序、ensure_ascii 转义与 Python `float.__repr__` 数字渲染。
- `internal/server/routes_events_history.go`（处理函数）：`limit` 缺省 500、坏值回退 500、夹在 `[1, 10000]`；`before_offset`/`before_index` 仅在可解析时生效；会话目录或 `events.jsonl` 不存在返回 200 的空载荷（10 个键全零）；读取放入 goroutine 并以 `time.After(20s)` 竞速，超时返回 503 `events_history_timeout`，读取错误回退空载荷；随后逐条截断、按「尾页 `max(1, total - returned + 1)` / 游标页 `max(1, before_index - returned)`」计算 `start_index` 并把每条事件的 `seq` 改写为绝对 1 基行号；`has_more_older = start_offset > 0`、`truncated = total > returned`（仅当 `total >= 0`）。
- `internal/server/routes_sessions.go`（修改）：`SessionRouteDeps` 增加 `Home`、`EventsReadPage`、`EventsReadTimeout` 三个 seam；`RegisterSessionRoutes` 追加 `GET /api/sessions/{session_id}/events/history`，与既有 `GET /api/sessions` 同处挂载。
- `internal/server/app.go`（修改）：`AppDeps` 增加 `Home hivehome.Home`，`NewApp` 把 `LiveSessions` 与 `Home` 一并交给 `RegisterSessionRoutes`，事件历史路由随应用装配一起上线。
- `internal/server/routes_events_history_test.go`（新增，594 行，15 条）：8 条逐条移植 `core/tests/test_events_history_page.py`（小文件 7 组参数 × 尾换行两态、300×8192 大文件跨阈值分 6 页、首页即尾页、游标页不再重算 total、末页 `start_offset == 0`、偏移 0 空页、handler 成功载荷 3/3、读取超时返回 503）；4 条移植 `core/tests/test_events_history_tail.py::test_truncate_*`；3 条 `Parity-exempt` 钉住 Python `int()` 查询参数、ASCII-only 空行过滤与 `json.dumps` 数字尺寸估计。
- 主树测试 2035→**2044**；注解构成 1792 个仅带 `.py` `Parity:` 引用、251 个仅 `Parity-exempt`（1 个两者兼有）；refs 1978→**1986**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（`hive debugger` 命令，基线 2027→2035）

- `internal/cli/debugger.go`（新增，192 行）：移植 `framework/debugger/cli.py` 的 `register_debugger_commands` + `cmd_debugger`。注册单个叶子命令 `hive debugger`，七个参数 `--session` / `--port`（int）/ `--logs-dir` / `--limit-files`（int）/ `--output` / `--no-open` / `--include-tests`；`cmdDebugger` 依次判定三条分支——(a) 日志目录不存在、不是目录或没有 `*.jsonl`：打印两行「`No LLM debug logs found in {logs_dir}.`」与 `HIVE_ENABLE_LLM_LOGS=1` 提示，退出 0；(b) 可视化脚本缺失：打印「`Visualizer script missing: {path}`」与 datalog 引导，退出 1；(c) 正常：按上游固定顺序拼参数并 `DebuggerExec`，返回子进程退出码。
- `internal/cli/debugger.go`：参数透传条件逐条对齐 Python——`--session` 仅非空、`--port` 仅非零、`--logs-dir` 仅显式非空、`--limit-files` 只要 `wasProvided` 就转发（显式 0 也转发）、`--output` 仅非空、`--no-open` 与 `--include-tests` 排在最后。默认日志目录取 `hivehome` 根下的 `llm_logs`。
- `internal/cli/debugger.go`：脚本路径复刻 `Path(__file__).resolve().parents[3]` 语义——由 `runtime.Caller(0)` 得到源文件，向上四层到仓库根，再拼 `scripts/llm_debug_log_visualizer.py`；执行层用 `Stdin/Stdout/Stderr` 继承实现 Python `subprocess.call` 的「继承 stdio + 返回码」行为。
- `internal/cli/cli.go`（修改）：`Runner` 新增 `DebuggerScript func() string`、`DebuggerExec func([]string) int`、`DebuggerPython string` 三个 seam 并在 `NewRunner()` 接线；`rootCommand()` 把 `debuggerCommands()` 插在 skill 与 mcp 之间，与 `framework/cli.py::main` 的注册顺序（loader → skill → debugger → mcp → janitor）一致。
- `internal/cli/args.go`（修改）：`kindInt` 解析失败改用 `pyRepr`，错误文本变为 `argument --port: invalid int value: 'abc'`，与 argparse 的单引号 `%r` 一致（同时被 `hive debugger --port` 与既有子命令共享）。
- `internal/cli/debugger_test.go`（新增，8 条，全部 `Parity-exempt`）：上游 `framework/debugger/cli.py` 没有单元测试，`core/tests` 中也无 CLI 级用例，故每条写明理由——命令形状（七个参数、默认值、帮助语）、无日志目录的两行提示与退出 0、目录存在但无 `*.jsonl`、脚本缺失的单行提示与退出 1、参数按固定顺序转发、默认值不转发、日志目录默认落到 `HIVE_HOME/llm_logs`、`--port abc` 走用法错误退出 2。
- 主树测试 2027→**2035**；注解构成 1784 个仅带 `.py` `Parity:` 引用、250 个仅 `Parity-exempt`（1 个两者兼有）；refs 保持 **1978**（新增测试无 `.py` 引用）、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（`hive-cred` 独立入口 + 对等测试，基线 2017→2027）

- `internal/cli/cred.go`（新增，399 行）：移植 `framework/credentials/cli.py` 的完整命令树 `hive-cred {token,browse,inspect}`。`token` 走 `credentials.ResolveSecret`（本地已收集账户优先，再查加密库），别名命中后重新经 `GetCredentialEx(..., true, true)` 触发 OAuth 刷新——过期令牌先刷新再打印，绝不把磁盘上的旧值交给脚本；`SecretNotFound` 映射 exit 4 且消息同时点明「`credential_id` 是凭据名（如 `google`），不是 `get_account_info` 报出的 opaque 账户 id」并附排序去重后的 `Known credential ids:` 提示；`CredentialExpiredError` 映射 `reauth_required`（exit 1），提示用户重连、不要重试；`browse`/`inspect` 只输出账户、别名与键名；无 `--json` 时 `token` 的 stdout 恰好是「值 + 换行」，便于 `TOK=$(hive-cred token ...)` 捕获；`HIVE_CRED_LOG` 把日志重定向到文件以保持 stdout 干净；用法错误按 `_Parser.error` 语义走 exit 6（而非 argparse 的 2）。
- `cmd/hive-cred/main.go`（新增）：独立控制台入口 `os.Exit(cli.NewCredRunner().Run(os.Args[1:]))`，对应 Python 的 `hive-cred = framework.credentials.cli:main`。
- `internal/cli/cred_test.go`（新增，10 条全部上游对等）：逐条移植 `core/tests/test_hive_cred_cli.py`——过期 OAuth 先刷新（`ya29.FRESH`、`refreshes==1`、`expires_in_seconds>3000`）、纯文本输出仅为值、刷新失败为 reauth exit 而非旧令牌泄漏、未知账户 exit 4、not-found 消息点明命名空间、`Known credential ids` 读取 `credential_id` 字段且适配器故障时返回空、控制台入口注册、browse 列账户但从不含值、inspect 只给键名、本地已收集账户（含 `--account default`）可解析。
- `internal/tracker/errors.go`（修改）：导出 `WriteIndentedJSON`，供 cred 的 JSON 信封复用同一套 `json.dumps(indent=2, default=str)` 语义。
- `internal/credentials/resolve.go`（修改）：导出 `LocalRegistryDefault` seam 供测试替换本地注册表，避免读取开发者真实 `$HIVE_HOME/credentials`。
- 主树测试 2017→**2027**；注解构成 1784 个仅带 `.py` `Parity:` 引用、242 个仅 `Parity-exempt`（1 个两者兼有）；refs 1968→**1978**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。
- 独立验证（2026-11-18 16:31，子代理 `verify`）：**PASS**，8 项检查全部带回 `Command run:` 证据——离线无 cgo 构建、`internal/cli|credentials|tracker` 三包测试、`go vet`、`gofmt`、两道 parity 门禁、10 条 cred 对等用例逐条 PASS、泄漏断言与上游 `test_hive_cred_cli.py` 对照、20 路并发 `browse --json` 幂等无崩溃、空/Unicode/超长 ID 的退出码 4/6 边界。5 处已记录但不影响契约的差异：用法文本引号与 usage 行、无密钥时 stderr 多一行密钥生成日志（上游 `NullHandler` 静默）、JSON 是否 `ensure_ascii`、`connectable` 恒空（上游测试亦打桩为空）、`ListAccounts` 错误被吞（仅本地注册表损坏可达）。

### 上一轮完成（`hive janitor` 命令组，基线 2005→2017）

- `internal/cli/janitor.go`（新增）：完整移植 `framework/maintenance/cli.py`。`janitorCommands()` 注册 `hive janitor` 组（`Dest: "janitor_command"`，Help/Description 逐字对齐）与 `run`（`--execute` / `--tiers` / `--legacy` / `--junk` / `--no-archive` / `--port` / `--verbose|-v` 七个参数）、`status` 两个子命令；`cmdJanitorRun` 复刻 tiers 解析与两条失败分支（均退出 2）、`needs_session_safety = execute and (tiers∩{2,3} or legacy or junk)`、活服务器拒执行块（`<port>` 保持字面）、`ForOffline` 安全上下文、`RunOnce`、报告渲染、`elapsed`、`ERROR:` 与 dry-run 尾行；`cmdJanitorStatus` 无记录时打印 `no janitor run recorded` 并退出 1；`printJanitorReport` 逐字复刻 `_print_report` 的表头、`%-22s files=%-7d freed=%-10s skipped=%d` 列宽、空目标跳过、`errors=N`、TOTAL、archive/manifest 行与 verbose manifest；`printJanitorManifest` 在任意读/解码/缺键错误处中止整个列表；`janitorHuman` / `janitorTiersRepr` / `janitorCtime` / `janitorTruthy` 等辅助函数对齐 Python 的 `_human`、`sorted(set)`、`time.ctime` 与真值语义。
- `internal/cli/cli.go`（修改）：`rootCommand()` 在 loader、skill、mcp 之后追加 `r.janitorCommands()`，与 `framework/cli.py::main` 的注册顺序一致（janitor 最后）。
- `internal/cli/janitor_test.go`（新增，12 条，全部 `Parity-exempt`）：上游 `framework/maintenance/cli.py` 没有单元测试（只有 `core/tests/test_janitor_*.py` 覆盖库层），故每条均写明理由——帮助页经根树列出 `run`/`status`、缺子命令退出 2、dry-run 报告与退出 0、两条非法 `--tiers` 分支、tiers 容忍空白、活服务器（401 也算）拒绝破坏性执行并打印完整拒执行块、tier 1 在服务器存活时仍允许执行、无报告时 `status` 退出 1、`status` 渲染已保存报告、`_print_report` 精确布局、verbose manifest 精确行与畸形文件中止、`_human` 单位阶梯。
- 输出与真实 Python 对齐：用 `/Users/mac/hive/.venv/bin/python` 跑上游 `_print_report` 与等价 dict，逐字节比对 `TestPrintJanitorReportFormat` 的期望文本（含本地时区 `time.ctime(0)`）。
- 主树测试 2005→**2017**；注解构成 1774 个仅带 `.py` `Parity:` 引用、242 个仅 `Parity-exempt`（1 个两者兼有）；refs 保持 **1968**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、`make cross`（6 平台）、两道 parity 门禁全绿。

### 上一轮完成（`hive mcp` 根入口接线，基线 1999→2005）

- `internal/cli/cli.go`（修改）：`Runner` 新增 `MCPMain func(argv []string) int` seam，`NewRunner` 用它构造 `mcpregistry.CLI{Out, Err, In}` 并调用 `Main`；`rootCommand()` 在 loader、skill 之后追加 `r.mcpCommands()`，与 Python `framework/cli.py::main` 的注册顺序一致。
- `internal/cli/cli.go`（修改）：新增 `mcpCommands()`，注册 `hive mcp` 组（`Help: "Manage MCP servers"`，14 个子命令）并标记 `Passthrough: true`。不用通用解析器是因为 mcp 自己的 argparse 接受 `--json`、`--args`、`--env` 以及 `add local -- <cmd>` 的 `--` 尾部，通用解析器会把这些当作未知参数拒绝。
- `internal/cli/args.go`（修改）：`commandSpec` 增加 `Passthrough`、`parsedArgs` 增加 `rest` 与 `rawArgs()`；`parseCommand` 在子命令/位置参数解析之前短路，把命令名之后的全部 token 原样交给处理函数。
- `internal/cli/mcp_test.go`（新增，6 条）：3 条上游对等——帮助页列出全部 14 个子命令（`test_register_mcp_commands_creates_all_subcommands`）、`mcp list` 空表（`test_main_dispatches_mcp_list_through_real_argparse`）、`mcp update nonexistent` 退出 1（`test_main_dispatches_mcp_update_named_through_real_argparse`）；3 条 `Parity-exempt`——根帮助列出 `mcp`、三种含 `--json`/`--` 的参数整段字节透传、未知子命令退出 2。测试用 `mcpregistry.NewAtBase(t.TempDir())` + `RegistryFunc` 注入隔离空注册表，不触网、不读 `HIVE_HOME`。
- 主树测试 1999→**2005**；注解构成 1774 `Parity` + 230 `Parity-exempt` + 1 兼有；refs 1965→**1968**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（queen 默认档案对等测试，基线 1989→1999）

- `internal/queen/profiles_test.go`（新增，10 条，全部上游对等）：逐条移植 `core/framework/agents/queen/tests/test_queen_profiles_defaults.py`——13 个默认 queen 从内嵌 `queen_defaults/*.yaml` 加载且 name/title 非空、capability 默认全空、`DefaultToolsFor` 已知/未知、`materializeDefaultQueen` 只写 persona、未改默认的自愈等价性（`reflect.DeepEqual` 对齐 `DefaultProfilePersona`）、未知 id 返回 nil、退役标题精确命中才替换且用户自选标题不动、无退役 queen 原样透传、每条退役标题都指向真实 queen。
- `internal/queen/profiles.go`（修改）：新增 `DefaultSkillsFor`，对应上游 `default_skills_for` 返回 `frozenset(profile.get("default_preset_skills") or ())`；`profiles.go` 404 行。
- 主树测试 1989→**1999**；注解构成 1771 `Parity` + 227 `Parity-exempt` + 1 兼有；refs 1955→**1965**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（会话列表路由 + TTY 语义 + 快照校正，基线 1974→1989）

- `internal/server/routes_sessions.go`（新增）：`GET /api/sessions` 实时会话列表，`_session_to_live_dict` 的 15 字段逐一对等——`session_id` / `colony_id` / `has_worker`（`colony_id is not None`）/ `agent_path`（`worker_path` 或 `""`）/ `queen_id` / `queen_phase`（有 colony 回退 `staging`，否则 `planning`）/ `queen_profile` / `queen_model` / `queen_supports_images`（走 `llm.SupportsImageToolResults`）/ `created_at` / `uptime_seconds`（`round(..., 1)`，Go 用 `math.RoundToEven`）/ `messages_sent` / `tokens_used` / `cost_usd` / `state`。
- `internal/server/session.go`（修改）：`Session` 增加 `WorkerPath`；`SessionManager` 增加 `order []string` 维持 Python dict 的插入序，重复注册原位替换，`list_sessions()` 顺序与上游一致；`StopSession` 退订 `WorkerHandoffSub` 与 `MemoryReflectionSubs`。
- `internal/server/routes_sessions_test.go`（新增，6 条）：2 条上游对等（列表空/已加载）+ 4 条 `Parity-exempt`（15 字段逐一断言、queen-only 缺省、colony 无 phase 状态、`list_sessions` 插入序）。
- TTY 语义修正（`internal/cli`）：`stdinIsTTY()` 改用 `github.com/mattn/go-isatty v0.0.24`（`IsTerminal || IsCygwinTerminal`），对齐 Python `sys.stdin.isatty()` 的 ioctl 语义；旧的 `os.ModeCharDevice` 会把 `/dev/null` 误判为终端。新增 3 条测试钉住：`TestStdinIsTTYFalseForDevNull`、`TestPromptYesNoNonInteractive`、`TestSkillForkNonInteractiveAborts`。
- 账本校正：`loader_test.go` 4 条与 `cold_test.go` 2 条早前已落地但未进文档，本轮补齐记录；因此本轮真实新增 9 条（server +6、cli +3），另 6 条为账本修正。
- 主树测试 1974→**1989**；注解构成 1761 `Parity` + 227 `Parity-exempt` + 1 兼有；refs 1952→**1955**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（CLI 子系统：根入口 + skill 子命令 + LLMJudge，基线 1907→1974）

- `internal/cli/skill_test.go`（36 条）：逐条移植 `core/tests/test_skill_cli_commands.py` 全部 9 个测试类——`init`(4) / `validate`(4) / `doctor`(3) / `install`(3) / `remove`(2) / `search`(3) / `info`(3) / `--json` 输出形态(5) / `test`(9)；覆盖 SKILL.md 生成与校验、license 缺失告警、doctor 三技能检查、注册表安装（含 pack）、去重与确认提示、表格/JSON 输出、eval 运行器与 LLM Judge 集成。
- `internal/cli/cli_test.go`（6 条）：根帮助提及 `hive`、`serve --help` 含 host/port、根帮助列出 `serve`/`queen`/`colony`/`session`/`chat`、`queen list` 与 `colony list` 帮助、未知命令非零退出。
- `internal/cli/loader_test.go`（3 条）：前端构建失败保留命令错误文本与 stderr；清理 `tsconfig.app.tsbuildinfo` 并在 Windows 使用 `npm.cmd`；`hive open` 先启动 gateway ping 再进入 `serve`。
- `internal/llmjudge/judge_test.go`（22 条）：Provider 注入与参数（`System` 为空、`MaxTokens=ResolveAuxMaxTokens(0)`、`JSONMode`）、裸 JSON / Markdown 围栏 / 空白 / 缺省解释 / `passes` 强转等解析形态、异常与非法 JSON 错误路径、惰性 Anthropic 客户端（`claude-haiku-4-5-20251001`）、LiteLLM `gpt-4o-mini` 回退、跨 judge 复用。
- `cmd/hive/main.go` 接线 `cli.NewRunner().Run(os.Args[1:])`；`rootCommand()` 依次注册 `loaderCommands()` 与 `skillCommands()`。当时 `mcp` / 凭据 / `maintenance` 尚未注册到根入口（`mcp` 已在后续一轮补齐，见本节上方的本轮完成）。
- `internal/queen/profiles.go`（本轮扩到 404 行）+ `queen_defaults/*.yaml`（13 份）：`hive queen list/show/sessions` 依赖的默认身份档案、`catalog.json` 读写、标题继承与移除标记辅助；当时的直接单元测试缺口已在本轮补为 `profiles_test.go` 10 条对等测试。
- 文档化缺口：Python 的 `sys.path` 引导（`test_cli_entry_point.py::TestConfigurePaths`）由 Go 编译器静态解析取代，无对应测试。
- 主树测试 1907→**1974**；注解构成 1758 `Parity` + 215 `Parity-exempt` + 1 兼有；refs 1885→**1952**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（LLM 配置语义对齐 + 启动/健康 HTTP 级测试，基线 1889→1907）

`internal/server/llm_config_test.go` 由 17 条增至 24 条（2 条上游对等 + 22 条 `Parity-exempt`），新增 7 条钉住与 Python 逐字一致的语义：

- 配置数值的源拼写透传：`HiveConfig` 用 `UseNumber` 解码，手写 `123` 不被渲染成 `123.0`；`api_base`/`model` 等数值字段经 `stringPointer` 转字符串，避免 Go 侧把上游 `str()` 结果退化为 `nil`。
- 校验只认布尔 `false`：复刻 `check.get("valid") is False`，`0`、`""`、缺失都算通过。
- 订阅 preset 无 `api_base` 时删除配置里残留的 `api_base` 与 `api_key_env_var`。
- `connected_providers` 跳过 `google` / `together_ai` 别名。
- custom 变更中止时不落盘；配置含尾随垃圾时整体读空（对齐 `json.load`）。

同时新增 `internal/server/app_test.go`（8 条：2 条上游对等 + 6 条 `Parity-exempt`）与 `internal/server/boot_status_test.go`（3 条 `Parity-exempt`），把健康检查、SPA 回退、desktop token、no-store、panic 兜底、启动状态与启动阶段历史上限纳入 HTTP 级对等；服务端测试 128→**146**。

主树测试 1889→**1907**；注解构成 1691 `Parity` + 215 `Parity-exempt` + 1 兼有；refs 1883→**1885**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（元信息与 LLM 配置路由完整接线，基线 1853→1889）

配置类路由从 7 条扩到 20 条，`RegisterConfigRoutes` 现在覆盖 `routes_config.py::register_routes` 挂载的全部端点：

- `internal/server/config_meta.go`（新增，482 行）：sentinel GET/PUT（缺省块、整段替换、非对象 500、刷新失败不影响响应）；模型目录直接返回共享 catalogue；rate limits（声明缺省、按请求成员顺序合并覆盖并回传文档序告警、非对象 400）；profile GET/PUT（只接受合法 theme/density/sort 值、身份段镜像 `global-memory/user-profile.md` 且只替换 `## Identity` 段、非对象 profile 复刻上游 500）；avatar（JPEG/PNG/WebP、2 MB 上限、空文件与未知类型 400、写入后按扩展名探测顺序回读、一小时缓存头）；external skills（默认空、路径解析、裁剪写回、坏列表 400、非可迭代存储值 500）。
- `internal/server/llm_config.go`（新增，1,002 行）：`GET/PUT /api/config/llm`。GET 汇总 active/detected 订阅并过滤未配置项；PUT 分派 custom / subscription / api-key 三条写入分支，API key 走 8 步解析链（显式 key、`api_key_env_var`、配置段、provider 默认变量、凭据存储等），仅显式 `valid: false` 才阻断，订阅限额与目录校验、失败不落盘、陈旧 `api_base` / `api_key_env_var` 清理均与上游一致。
- `internal/server/routes_config.go`（修改）：新增 13 条注册（models、sentinel ×2、llm ×2、rate-limits ×2、profile ×3、external-skills ×2），所有写路径共用严格 JSON 解析。
- 测试 +36（`internal/server` 92→**128**）：`routes_config_meta_test.go`（19 条：1 条上游对等 + 18 条 `Parity-exempt`）、`llm_config_test.go`（17 条：2 条上游对等 + 15 条 `Parity-exempt`），全部经真实 `httptest` 服务断言状态码、JSON 与落盘。
- 主树测试 1853→**1889**；注解构成 1689 `Parity` + 199 `Parity-exempt` + 1 兼有；refs 1880→**1883**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、`go test -race ./internal/server/`、两道 parity 门禁全绿。

### 上一轮完成（独立验证修复：配置写串行化 + aiohttp 500 语义，基线 1852→1853）

独立 verification 代理对本轮配置路由给出 FAIL，阻塞项是并发 handler 下的配置文件丢写；以下修复已落地并复跑门禁：

- `internal/server/config_features.go`（修改）：新增包级 `configMu`、持锁的 `WriteHiveConfig` 与 `MutateHiveConfig`（读—改—写整体加锁；mutate 返回 false 时不写盘），`UpdateFeatures` 改走 `MutateHiveConfig`。上游 aiohttp 单事件循环天然串行，Go 并发 handler 必须显式串行化，否则两个请求读同一底稿互相覆盖（验证代理实测 25 条库写入仅存活 5 条）。
- `internal/server/provider_library.go`（修改）：provider 库保存/删除、槽位写入、槽位清空四处读改写全部改走 `MutateHiveConfig`；清空不存在的角色保持不写盘；所有 `500` 改回通用 `{"error": "Internal server error"}`，不再泄漏 Go 错误文本。
- `internal/server/routes_config.go`（修改）：`readConfigBody` 换成 `decodeConfigBody`，解码到 `any` 后区分两态——空体/坏 JSON/尾随垃圾为 400 `{"error": "Invalid JSON body"}`；能解析但不是对象（`[1,2]`、`null`、标量）在 aiohttp 下会在 `.get` 处抛 `AttributeError`，故回 500 通用错误体。
- 测试改为 5 种输入 × 4 条写路径的状态码/错误体矩阵；新增 `TestConcurrentConfigWritesDoNotOverwriteEachOther`（`Parity-exempt`）：25 个并发 provider 库 PUT + 1 个特性 PUT，断言全部 26 次写都存活，`-race` 通过。
- 主树测试 1852→**1853**（+1 并发回归），`internal/server` 91→**92**；注解构成 1686 `Parity` + 166 `Parity-exempt` + 1 兼有；refs 仍 **1880**、shorthand/prose 43、0 missing；`gofmt`、`go vet`、`go test ./...`、`go test -race ./internal/server/`、两道 parity 门禁全绿。

### 上一轮完成（配置路由 HTTP 接线 + judge 重定向语义，基线 1835→1852）

- `internal/server/routes_config.go`（新增，135 行，纯 Go）复刻 `framework/server/routes_config.py` 的 HTTP 外层，不引入 aiohttp 等价框架：
  - 7 条路由：`GET/PUT /api/config/features`、`GET/PUT /api/config/llm-sections`、`GET/PUT /api/config/provider-library`、`POST /api/config/provider-library/apply`，由 `RegisterConfigRoutes` 挂到 Go 1.22+ 模式 mux。
  - 请求体解析（初版为 `readConfigBody`，本轮改为 `decodeConfigBody`）：整体读取后解码到 `any`；空体、坏 JSON、尾随垃圾回 400 `{"error": "Invalid JSON body"}`，可解析的非对象回 500 通用错误体，对齐 aiohttp `request.json()` 加错误中间件的实际行为；四条写路径共用。
  - handler 只做「解析 → 决策层 → `writeJSON`」，状态码与响应体原样透传；热应用会话、健康检查与热切换仍经 `ConfigRouteDeps` 注入。
- `internal/server/judge.go`（修改）修正重定向语义：`TypesafeJudge` 改用 `CheckRedirect` 返回 `http.ErrUseLastResponse` 的客户端，3xx 视为错误，对齐 httpx 默认 `follow_redirects=False` 加 `raise_for_status()`；此前 `http.DefaultClient` 会静默跟随。
- 测试 +17（`internal/server` 74→91）：
  - `routes_config_test.go`（新增，16 条）：7 条镜像 `test_routes_config_features.py`，8 条镜像 `test_routes_provider_library.py`，全部经真实 `httptest` 服务器断言状态码、JSON 与落盘；1 条 `Parity-exempt` 覆盖四条写路径共用的严格 JSON 解析（本轮扩为两态矩阵，另一条并发回归为本轮新增）。
  - `judge_test.go` 追加 1 条 `Parity-exempt`：双服务器实测 302 不被跟随、目标零命中。
  - `stall_check_test.go` 原豁免用例改为真实上游对等（`framework/server/tests/test_stall_check.py::test_the_judge_slot_saves_without_the_chat_key_check_and_the_option_toggles`）：经 `/api/config/llm-sections` 与 `/api/config/features` 驱动，断言 judge 段逐字回读、provider key 校验未被调用、`stall_check` 开启后判官槽可用；豁免说明移除。
- 主树测试由 1835 增至 **1852**（+17）；refs 由 1864 增至 **1880**（+16：15 条路由对等 + 1 条豁免转对等），shorthand/prose 43，0 missing；`internal/server` 74→91；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、`go test -race ./internal/server/`、两道 parity 门禁全绿。

### 上一轮完成（配置特性开关 + LLM 槽位 + provider 库决策层，基线 1817→1835）

- `internal/server/config_features.go`（新增，250 行，纯 Go）复刻 `framework/server/routes_config.py` 的特性端点决策层与 `framework/config.py` 的开关解析：
  - 解析顺序固定为 `HIVE_*` 环境变量 > `configuration.json` 顶层值 > 缺省值；环境变量去空白后非空才生效，`0/false/no/off`（大小写不敏感）为假。文件层 `adaptive_tool_budget`/`email_senders`/`memory_timeline` 只认 `isinstance(value, bool)`（字符串 `"true"` 回落缺省）；`stall_check` 复刻上游 `bool(...)`，非空字符串（含 `"false"`）与非空容器都为真。
  - `FeaturesResponse` 输出四项：`adaptive_tool_budget`（缺省 true）、`email_senders`、`memory_timeline`、`stall_check`（缺省 false）。
  - `UpdateFeatures` 接受裸对象或 `{"features": {...}}`；白名单外键忽略，已知键非布尔或全部缺席即 400，错误文本逐字对齐，且校验失败绝不写盘。
  - `WriteHiveConfig` 同目录临时文件 + 两空格缩进 + 尾换行 + `os.Rename` 原子替换；无关键原样保留。
  - `adaptive_tool_budget` 更新后热应用到运行中会话：colony `metadata.json` 里该键为布尔即为持久 pin，pin 优先；只翻转「有显式值且与目标不同」的 colony 并计入 `colonies_applied`；metadata 读/解析失败静默跳过。
  - `email_senders` 更新后 `SyncEmailSendersEnv` 把 `HIVE_EMAIL_SENDERS` 写成 `"1"/"0"`，令随后拉起的 MCP 子进程继承。
- `internal/server/provider_library.go`（新增，244 行，纯 Go）复刻 `routes_config.py` 的槽位与 provider 库：
  - 槽位 `llm`/`worker_llm`/`vision_fallback`/`judge` 共享 `WriteRoleSection`：`model` 必填、`provider` 缺省 `openai`、key 可由 `api_key_env_var` 间接提供、段逐字存储。
  - 健康检查四条件同时成立才发生（未显式 `validate: false`、key 非空、`api_base` 非空、角色非 `judge`），且只有验证器返回**显式** false 才阻断写盘；错误文本 `Key check failed against <base>: <msg>`，缺省 `unknown error`。
  - 仅 `llm` 槽写入成功后触发热切换，模型串 `provider + "/" + model`；`PutLLMSection` 拒绝清除主槽（错误文本逐字对齐），其余槽 null 即删除。
  - provider 库逐字存取；删最后一条剪掉 `provider_library` 键；坏库读作空；`ApplyProviderLibrary` 先校验角色（400）再查库（404），命中后复用同一写入路径，校验/健康检查/热切换行为与直接写槽完全一致。
  - 验证器与热切换经 `RoleWriteDeps` 注入，测试精确断言四元组/三元组的调用参数，不依赖网络或运行中会话。
- 测试 +18（`internal/server` 56→74）：`config_features_test.go` 10 条、`provider_library_test.go` 8 条，逐条镜像 `core/tests/test_routes_config_features.py` 与 `core/tests/test_routes_provider_library.py`。
- 主树测试由 1817 增至 **1835**（+18）；refs 由 1846 增至 **1864**（+18），shorthand/prose 43，0 missing；`internal/server` 56→74；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（judge 判官槽 + stall check 停滞检查，基线 1807→1817）

- `internal/server/judge.go`（新增，148 行，纯 Go）复刻 `framework/server/judge.py`：可选的 `judge` 配置槽，以概率回答是非问题。
  - `JudgeSlot` 每次调用都重读 `configuration.json`，槽必须为对象且 `api_base`、`api_key` 均为真值，否则返回 nil（对应上游 `slot()`）。
  - `AskJudge` 在槽不可用、协议未注册或调用失败时记录日志并返回 `ok=false`，从不向调用方抛错；`protocols` 参数非空时覆盖默认注册表（上游 `protocols or PROTOCOLS`）。
  - `TypesafeJudge` 向 `<api_base>/systemone` POST `{"state", "model", "questions": {"answer": {"type": "noul", "instructions", "criteria?"}}}`，20 秒超时；非 2xx、缺 `answers.answer.noul` 或 `noul` 不可转 float 均为错误。
- `internal/server/stall_check.go`（新增，243 行，纯 Go）复刻 `framework/server/stall_check.py`：当 Developer 选项 `stall_check` 开启且判官槽可用时，每个以「等待用户」结束的 queen 回合交给判官审视。
  - 逐字保留上游 `_QUESTION`、`_CRITERIA`（true/false 两侧）与 `REMINDER` 文本；state 含 `previous_turn`（上一回合回复尾部 600 字符与工具列表，仅当存在时）、`user_message` 尾部 2000 字符、`assistant_reasoning_tail` 与 `assistant_reply` 各 1500 字符、`tools_called_this_turn`。
  - 仅在 `turn_done`、存在回复、用户发起、本回合未判过、且回复不是可见错误（`agentloop.IsVisibleError`）时判定；每条用户消息至多一次判定与一次提醒，p ≥ 阈值（槽 `threshold` 经 `float()` 强制，缺省 0.5）时以 `is_client_input=false` 注入 `REMINDER`。
  - 工具调用开启新一次模型调用，清空 reasoning/reply，使判官只看最后一次模型调用的言辞；`EventClientInputReceived` 时把上一回合的回复与工具存为 `previous_turn`（对应「用户指出上一件作品的缺陷」场景）。
  - Python 用 `asyncio.create_task` 避开 publish 等待；Go 的 `Publish` 同样等待处理器，故判定在 goroutine 中执行，`StallCheckSubscription` 以 `sync.WaitGroup` 跟踪并提供 `Subscriptions`/`Wait`/`Close`，与 `SubscribeTitleTrigger` 同型。
- `internal/server/hive_config.go`（新增，147 行）复刻 `framework/config.py` 的读取语义：`HiveConfig` 容忍 BOM、任何读取/解析失败读作空配置；`StallCheckEnabled` 让 `HIVE_STALL_CHECK` 环境变量优先于文件内顶层 `stall_check`；并集中提供 `pyTruthy`/`pyStr`/`toFloat` 的 Python 语义。
- `internal/agentloop/visible_error.go`（新增，45 行）复刻上游可见错误判定：仅 `[Error: LLM call failed: ` 前缀，允许前导空白；`[System:` 是注入的用户消息，不是可见错误。
- 测试 +10（`internal/server` 46→56）：`judge_test.go` 3 条上游对等；`stall_check_test.go` 6 条上游对等 + 1 条 `Parity-exempt`（上游该用例经 `/api/config/llm-sections`、`/api/config/features` HTTP 路由驱动，路由尚未移植；豁免测试覆盖其文件层语义：judge 段逐字存取且跳过聊天 provider key 校验，`stall_check` 顶层开关即时启停判官槽）。
- 主树测试由 1807 增至 **1817**（+10）；refs 由 1837 增至 **1846**（+9），shorthand/prose 43，0 missing；`internal/server` 46→56；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上上轮完成（会话目录解析验证补强，基线 1802→1807）

- 独立验证发现三处与上游 Python 语义不一致，`internal/server/session_dirs.go` 已修正：
  - **惰性遍历**：上游 `_iter_queen_session_dirs` 是生成器，`_find_queen_session_dir` / `_derive_resume_colony_id` 命中即停，永远不会列举排序靠后的坏目录；旧实现先把全部候选项收齐再消费，坏目录会让本应成功的查找报错。改为 `forEachQueenSessionDir` 访问器模式，`visit` 返回 false 即停，错误只有在遍历真正走到时才上抛。
  - **符号链接**：`Path.is_dir()` 会跟随符号链接，`os.DirEntry.IsDir()` 不会；符号链接的 queen / colony 树会被静默跳过。新增 `entryIsDir`（`os.Stat`）复刻跟随语义。
  - **路径规范化**：`pathlib.Path("/colonies/acme/")` 在构造时去掉尾分隔符，父链中不含 colony 归属；Go 的 `filepath.Dir` 对尾分隔符的行为不同，导致把 `/colonies/acme` 误判为 colony。`ColonyIDForSessionDir` 改为先 `filepath.Clean` 再取父目录。
- `internal/server/session_dirs_test.go` 追加 5 条 `Parity-exempt` 回归测试（上游 fixture 未覆盖）：尾分隔符归属、符号链接树遍历、命中后坏目录不上抛、DM 命中即停、坏目录排在匹配前时错误上抛。
- 逐项与 Python 参考实现做黑盒对照：20 个目录场景 × iter/find/derive、11 个纯路径探针、25 个 LinkedIn 规范化输入，TOTAL DIFFS: 0（Python OSError 标签与 Go ERROR 视为等价）。
- 主树测试由 1802 增至 **1807**（+5）；refs 保持 **1837**（新增测试均为 `Parity-exempt`，不产生 `path::test` 引用），shorthand/prose 43，0 missing；`internal/server` 41→46；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（server 会话目录解析，基线 1794→1802）

- `internal/server/session_dirs.go`（新增，144 行，纯 Go）复刻 `framework/server/session_manager.py` 的会话目录解析——同一会话 id 可能同时存在于 queen DM 树（`queens/<q>/sessions/<sid>`）与 colony overseer 树（`colonies/<c>/queens/<q>/sessions/<sid>`），因为 DM 会话 fork 进 colony 后保留原 id：
  - `IterQueenSessionDirs` 一次遍历先产出 DM 候选、再产出每个 colony 的候选，仅收集 `exists()` 为真的路径，对应 `_iter_queen_session_dirs`。`listDirIfExists` 复刻 `Path.exists()` + `iterdir()` 的错误语义：stat 失败视为目录不存在（跳过），列举失败则上抛。
  - `ColonyIDForSessionDir` 只沿祖先查找 `colonies` 的直接子目录，目录自身从不视为归属，因此 `/colonies/acme` 本身不会被误判；DM 路径返回 false。
  - `FindQueenSessionDir` 取首个匹配，否则回退 `queens/default/sessions/<sid>`，保留 DM 优先的既有语义。
  - `DeriveResumeColonyID` 在同一遍历上跳过 DM 候选取首个 colony 归属；重复的 DM 目录排在前面也不会让 resume 漏掉 colony 副本；文件系统错误读作「无 colony」，对应上游 `OSError` 防护。
  - 以 `hivehome.Home` 依赖注入取代上游补丁的模块全局 `QUEENS_DIR` / `COLONIES_DIR`。
- `internal/server/session_dirs_test.go`（新增，136 行 / 8 条测试），逐条镜像 `core/framework/server/tests/test_session_dir_resolution.py`：从路径读出 colony、DM 路径无 colony、生成器找到仅 colony 的会话、生成器产出共享 id 的全部目录（DM 在前）、重复 DM 目录下 resume 仍找到 colony、真实 DM 会话 resume 无 colony、无匹配无 colony、首匹配查找仍偏好 DM 树。
- 同时修正 `NormalizeLinkedIn` 的 scheme 剥离：由连续 `TrimPrefix("http://")` + `TrimPrefix("https://")` 改为单次 `CutPrefix`（http 或 https 只剥一层），与 Python `re.sub(r"^https?://", "", u)` 在 `http://https://evil.com` 这类输入上一致。
- 主树测试由 1794 增至 **1802**（+8）；refs 由 1829 增至 **1837**（+8），shorthand/prose 43，0 missing；`internal/server` 33→41；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（global-db 数据路由纯逻辑，基线 1786→1794）

- `internal/server/global_data.go`（新增，219 行，纯 Go）复刻 `framework/server/routes_colony_workers.py` 中 global-db 代理路由的决策层：
  - `NormalizeLinkedIn` 去 scheme/www、截断 `?`/`#`、去尾斜杠并小写；`DeriveLeadID` 优先 LinkedIn slug，回退到去空白小写的 email，二者都缺失时 ok=false。
  - `PlanInsertRow` 复刻 insert 的 PK 处理：单 PK 且空值时，leads.lead_id 由 `DeriveLeadID` 派生（失败即 400，且不触达后端）；其他表在 PK 列类型不含 "int" 时铸 16 字节随机 hex id（32 字符，`uuid4().hex` 形状，测试可注入）；`rowTruthy` 对齐 Python 真值语义（nil/""/0/false/空容器为假）。
  - `MapInsertResult` 把后端结果映射为 `{"inserted": N, "pk": attempted_pk}`；409 保留 attempted pk 并返回 `error="conflict"` 信封，其余错误交给 `GlobalDBErrorResponse`（NotSignedIn→401 + signed_out，GlobalDbError→其 status 或 400，未知→502）。
  - `ChangesSinceParam` 复刻 `request.query.get("since") or None`：空串视为初始化、不传游标。
- `internal/server/global_data_test.go`（新增，139 行 / 8 条测试），逐条镜像 `core/framework/server/tests/test_global_data_routes.py`：LinkedIn 优先派生、email 回退与无标识、insert 模式与派生 PK、缺标识 400 且无后端写入、409 冲突带 pk、changes 初始化省略 since、游标透传、未登录 401。上游经 aiohttp TestClient 断言 HTTP 外层，这里把同一决策逻辑从 handler 拆出直接测试；HTTP handler 接线仍属 P3。
- 主树测试由 1786 增至 **1794**（+8）；refs 由 1821 增至 **1829**（+8），shorthand/prose 43，0 missing；`internal/server` 25→33；`gofmt`、`go vet`、`go test ./internal/server/`、两道 parity 门禁全绿。

### 上一轮完成（colony 自适应 worker 运行预算，基线 1766→1786）

- `internal/colony/adaptive_budget.go`（262 行，纯 Go）复刻 `framework/host/colony_runtime.py` 的 `_maybe_adapt_colony_budget` / `_apply_adaptive_budget` / `get_stats().adaptive_tool_budget`：
  - 采样准入严格——只收 `status == "success"`；`budget_limited` / `budget_pinned` / 零调用成功全部排除；`tool_calls_used` 按 Python `isinstance(int)` 语义校验（`pyInt`：bool 接受、字符串/浮点/None 拒绝）。滚动窗口 50 条，同时维护 colony 生命周期内的成功最大值。
  - 标称预算 `nominal = max(2.0 × median, ceil(1.25 × maxSuccess))`，钳到 `[floor=30, profile base=200]`；p90 > 4 × median 的异构样本直接跳过（没有单一常态）；样本数不足 3 条不动。
  - 缩小是统计性的、立即广播：所有非 pinned、非 persistent 的 PENDING/RUNNING worker 在下一个迭代边界被 `ApplyLifetimeBudgetCap` 收缩（先释放注册表锁再碰循环状态锁，避免锁序反转）。提升只能由 1.25× max-guard 的证据驱动，且从不广播——已收缩的 worker 用旧上限跑完，新准入的 worker 经 `ApplyAdaptiveBudget` 拿到新标称。
  - `ApplyAdaptiveBudget` 幂等，pinned / persistent / 未启用时 no-op；开关来自 `RuntimeOptions.AdaptiveToolBudget` 或 `HIVE_ADAPTIVE_TOOL_BUDGET`；策略常量（floor / 最小样本 / 乘数 / 窗口 / 离散度上限）均为可被测钉住的环境派生变量。
- `internal/agentloop` 侧补齐收缩契约：`ApplyLifetimeBudgetCap` 拒绝提升、等值、≤0、无 grace 轮与无上限循环；`LifetimeBudget` / `ToolCallsUsed` / `BudgetLimited`（读实时 grace 计数器，让被硬停的 worker 仍是删失样本）供采样器与取消路径读取。
- 测试 19 条，逐条对应 `core/tests/test_adaptive_tool_budget.py`：`internal/agentloop/budget_cap_test.go` 5 条（收缩返回 true、拒绝提升/零/无上限、无 grace 拒绝、`ToolCallsUsed`、`BudgetLimited` 实时计数器）；`internal/colony/adaptive_budget_test.go` 14 条（样本不足不缩、触底并收缩在飞 worker、max-guard 领先观测最大值、中位数上升被拒、证据驱动抬升从陈旧低标称恢复、抬升封顶 profile base、抬升不解除已收缩 worker、max-guard 在窗口淘汰后存活、离散度守卫、常态昂贵时不缩、采样排除、开关、准入收缩、get_stats 暴露标称）。
- 收尾补齐：上游 `test_workers_route_serializes_budget_telemetry` 已由 `internal/server/worker_list_test.go` 覆盖——`WorkerInfoToDict` 复刻 `_worker_info_to_dict`，result 输出 `tool_calls_used` / `budget_limited`，缺字段的旧结果按零值回退（等价 `getattr` 默认）。HTTP handler 接线仍属 P3，但序列化契约已对等，上游 20 条全部覆盖。
- 主树测试由 1766 增至 **1786**（+20）；refs 由 1801 增至 **1821**（+20），shorthand/prose 43，0 missing；`internal/agentloop` 313→318、`internal/colony` 69→83、`internal/server` 24→25；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（MCP 连接池，基线 1749→1766）

- `internal/tools/mcp_connection_manager.go`（新增，528 行，纯 Go）复刻 `core/framework/loader/mcp_connection_manager.py`：
  - 进程级按服务器名复用的 `MCPClientPool`，用 refcounts 决定「最后一个使用者释放时才断开」；`NewClient`（默认 `NewMCPClient`）与 `HealthProbe`（默认 HTTP `/health`，5s 超时、配置头、unix socket 直拨）为注入点。
  - `threading.Event` 的 Go 对应物 `mcpPoolTransition`（closed-channel 一次性广播 + `sync.Once`），`Acquire` / `Release` / `HealthCheck` / `Reconnect` / `CleanupAll` 都经该 latch 串行化连接与重连；等待超时 30s 后强制清理卡住的 transition（先删再 set 再重试），并发 acquire 不会建出第二个客户端，输掉竞争的新客户端会被断开并重试。
  - `Release` 在最后一个引用时先从池中摘除并清空 config，再在 transition 保护下断开；断开错误与上游一样吞掉（返回 nil）。`Reconnect` 保留 refcount；构造失败时删除池项与 transition；重连期间所有持有者都已释放则丢弃新客户端并返回 `MCP server '<name>' was fully released during reconnect`；未知服务器返回 `Unknown MCP server: <name>`。
  - `HealthCheck`：stdio/sse 调用 `ListTools` 并检查可选的 `ListToolsError()` 与 `IsConnected()`（Go 的 `ListTools` 无 error 返回值，故以可选接口承载上游的 `list_tools` 抛错语义）；http/unix 要求 URL / SocketPath 非空后走 health probe；未知 transport 为 false。
  - `CleanupAll` 先等待/强制清理所有 transition，再整体摘除 pool/refcounts/configs 并逐个断开，保证不与进行中的 connect 竞争。
- `internal/tools/mcp_connection_manager_test.go`（新增，543 行 / 17 条测试）：16 条 `// Parity:` 逐条镜像 `core/tests/test_mcp_connection_manager.py`（同服务器同客户端与 refcount、部分释放保持连接、最后释放断开并清池、8 goroutine 并发 acquire/release、cleanup_all 全断开、reconnect 保留 refcount、http 不可达、stdio 健康 / tools 错误、sse 走 list_tools、未知服务器、连接失败清理 transition、三条 disconnect 失败路径、已完全释放后 reconnect 报错）；1 条 `Parity-exempt` 锁定 `SharedMCPClientPool` 的进程单例契约（上游经 fixture 走 `get_instance()`，但没有单例身份断言）。
- 主树测试由 1749 增至 **1766**（+17）；refs 由 1785 增至 **1801**（+16），shorthand/prose 43，0 missing；`internal/tools` 由 119 增至 **136**；`gofmt`、`go vet`、`go test ./internal/tools/`（含 `-race`）与两道 parity 门禁全绿。

### 上一轮完成（社交平台限流器，基线 1749）

- `internal/ratelimit/rate_limiter.go`（新增，576 行，纯 Go + `modernc.org/sqlite`，零 cgo）复刻 `core/framework/rate_limiter.py`：
  - 8 组 (platform, action) 限额按上游插入序保存（linkedin profile_view/invite/message/comment、x reply/dm/post、instagram dm），每组含 hourly/daily/weekly 默认值与天花板、`min_delay_s`；`GetAllLimits` 按同一顺序返回行。
  - `resolveLimit` 复刻 `_resolve_limit`：环境变量 `HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW>`（原始拼写转大写）> `configuration.json` 的 `rate_limits` 段 `<platform>.<action>.<window>` > 默认值；env 值钳到天花板，配置值按 `max(1, int(v))` 取整但**不**钳天花板（与上游一致）；非法 env 继续下探；配置读取失败静默返回空（镜像上游 try/except）。
  - `Check` 先查最小间隔（返回 `too_fast` + 一位小数 `retry_after_seconds` + `min_delay_seconds`），再按 hourly(3600, 不停) → daily(86400, 停) → weekly(604800, 停) 顺序计数；阻断结果带上已累计的 count/limit，未定义的窗口保持 nil（映射 Python None）；重试秒数 `round1((最早记录 + 窗口) - now)` 且钳 ≥0。
  - `Record` 小写化 platform/action 后写 `actions` 表（`target_id` / `session_id` 可选）；`GetDailyCounts` / `GetWeeklyCounts` 按 24h / 7d 窗口聚合；默认库 `$HIVE_HOME/social_rate_limits.db`，schema 与索引名与上游一致，可共享同一 SQLite 文件。
  - `SetConfigOverrides` 注入配置源，测试可隔离开发机 `~/.hive/configuration.json`。
- `internal/ratelimit/rate_limiter_test.go`（新增，436 行 / 21 条测试）：20 条 `// Parity:` 逐条镜像 `core/tests/test_social_rate_limiter.py`（建表、空库放行、小时/日/周限额、账户与平台隔离、最小间隔、记录与归一化、4 条 env 覆盖、日/周计数、未知动作、min_delay 表）；1 条 `Parity-exempt` 锁定配置文件中间优先级与「钳 1 不钳天花板」的差异。
- 主树测试由 1728 增至 **1749**（+21）；refs 由 1765 增至 **1785**（+20），shorthand/prose 43，0 missing；新增包 `internal/ratelimit`；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（AnthropicProvider 向后兼容包装，基线 1728）

- `internal/llm/anthropic.go`（新增，171 行，纯 Go）复刻 `core/framework/llm/anthropic.py` 的薄包装：记住模型、解析 API key、把 `complete()` / `acomplete()` 全部转发给内部 `LiteLLMProvider`。
  - 默认模型 `claude-haiku-4-5-20251001`；密钥优先级与上游 `_get_api_key_from_credential_store` 一致——显式参数 → 凭据存储 → `ANTHROPIC_API_KEY` 环境变量；全部缺失时报上游原文错误 `Anthropic API key required. Set ANTHROPIC_API_KEY env var or pass api_key.`（Go 构造函数不能 raise，用 error 返回表达同一契约）。
  - `AnthropicProvider` 暴露 `APIKey` / `Model` / `Provider`（`_provider` 的 Go 对应物）；`Complete` 原样透传 OpenAI 形态 messages 与全部 `CompleteOptions`。
  - `AComplete` 满足 `Completer` 接口：以 `ChatMessagesToMaps` 把结构化 `ChatMessage` 转成上游的 dict 形态（assistant `tool_calls` 为 `{"id","type":"function","function":{"name","arguments"}}`、只调工具时 content 为 `nil`、tool 结果带 `tool_call_id`），并按 `LLMProvider.acomplete` 的 fallback 用空行拼接 `SystemDynamicSuffix`。
  - 凭据存储不引入 `internal/credentials`（该包无默认单例），改为包级注入点 `SetAnthropicCredentialLookup`，宿主在启动时装配真实存储。
- `internal/llm/anthropic_test.go`（新增，9 条测试）：7 条 `// Parity:` 逐条镜像 `core/tests/test_litellm_provider.py` 的 `test_anthropic_provider_is_llm_provider` / `init_defaults` / `init_custom_model` / `uses_litellm_internally` / `complete` / `passes_response_format` / `passes_json_mode`；2 条 `Parity-exempt`——密钥解析优先级（上游构造即 raise，无对应用例）与 `AComplete` 的类型桥接 / system 后缀拼接（Go 独有胶水）。
- 主树测试由 1719 增至 **1728**（+9）；refs 由 1758 增至 **1765**（+7），shorthand/prose 43，0 missing；`internal/llm` 由 206 增至 **215**；`gofmt`、`go vet`、`go build ./...`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（LLM complete 非流式路径，基线 1719）

- `internal/llm/complete.go`（新增，259 行，纯 Go）复刻 `LiteLLMProvider.complete`（`litellm.py:1858`）与 `_completion_with_rate_limit_retry`（`litellm.py:1686`）：
  - 请求组装：`max_tokens` 缺省解析为 `ResolveAuxMaxTokens`（8192 上限）；显式 system 以裸 `{"role":"system","content":...}` 前置（与上游一致，不走 `BuildSystemMessage`）；应用历史缓存断点；`json_mode` 时追加 JSON 指令；kwargs 顺序为 base → `ExtraKwargs` → `api_key` / `api_base`；工具转 OpenAI 格式后按 `setdefault` 语义补 `tool_choice`（Ollama `auto`、Hive 代理 `required`）；`response_format` 透传。
  - 重试循环：内容或工具调用齐备立即返回；空响应最多重试 `min(retries, 3)` 次，会话末条非 system 消息是 `assistant` 或 `finish_reason == "length"` 时短路；429 按 `ComputeRetryDelay(..., Jitter: true)` 退避，上限 `RATE_LIMIT_MAX_RETRIES = 10`；其他错误原样返回。
  - 响应归一化：`Content` / `Model`（缺省回落 provider 模型）/ `StopReason` / `ToolCalls` / `CostUSD`（`ExtractCost`）/ 输入输出 token / 缓存 token 与 creation token。
  - 网络调用经 `CompletionCall` 注入，`SleepFunc` 可替换退避等待，包内保持零网络依赖；调用方消息浅拷贝后再打缓存断点，不污染会话。
- `internal/llm/complete_test.go`（新增，10 条测试）：7 条 `// Parity:` 逐条镜像 `core/tests/test_litellm_provider.py` 的 `test_complete_basic` / `test_complete_with_system_prompt` / `test_complete_with_tools` / `test_json_mode_adds_instruction_to_system_prompt` / `test_json_mode_creates_system_prompt_if_none` / `test_json_mode_false_no_instruction` / `test_json_mode_default_is_false`；3 条 `Parity-exempt`（429 重试循环、空响应重试三条短路、kwargs 组装与 `tool_choice` 优先级）——上游重试循环依赖 `litellm.RateLimitError` 异常类型，无独立测试可直接对等，故以注入传输层验证接线并在注释写明。
- 主树测试由 1709 增至 **1719**（+10）；refs 由 1751 增至 **1758**（+7），shorthand/prose 43，0 missing；`internal/llm` 由 196 增至 **206**；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（LLM 请求塑形与 extra kwargs，基线 1709）

- `internal/llm/request_shape.go`（新增，261 行，纯 Go）补齐 `litellm.py` 与 `config.py` 中不需要网络的请求构造逻辑：
  - `IsAnthropicModel`（只认 `anthropic/`、`claude-` 前缀，不匹配 OpenRouter 路由）。
  - `Provider.RequestParallelToolCalls` / `NoteError` / `ParallelToolCallsRefused`：OpenAI 风格后端在有工具且未被拒绝时请求 `parallel_tool_calls`；Anthropic 自身成批处理、无工具请求不提该参数；网关拒绝后按 provider 记忆一次，重试时去掉且此后不再询问。
  - `BuildOpenRouterToolCompatMessages`：为不支持原生函数调用的 OpenRouter 子 provider 构造 JSON-only 提示词（persona + 兼容指令 + 有序工具清单），静态前缀与动态后缀在支持 `cache_control` 的模型上分块、其余拼成一个字符串；滚动缓存断点打在末条消息；丢弃空 assistant 消息；输入消息浅拷贝，避免污染调用方。
  - `ExtraLLMKWargs`：复刻 `get_llm_extra_kwargs` —— Claude Code 订阅 `extra_headers.authorization`、Codex 订阅 `Authorization` + `User-Agent: CodexBar` + `ChatGPT-Account-Id` + `store=false` + `allowed_openai_params`、Ollama 注入 `num_ctx`（默认 16384，可配置）、其余透传 `extra_body`。API key 与 Codex account id 作为参数传入，函数保持纯函数。
- `internal/llm/request_shape_test.go`（新增，5 条测试，4 条带 `// Parity:` + 1 条 `Parity-exempt`）：覆盖并行工具调用决策与拒绝闩锁、tool-compat 两种 system 形状与空 assistant 过滤、num_ctx 默认/自定义、非 Ollama 与空配置返回空；订阅分支与 `extra_body` 透传因上游无测试覆盖而显式豁免。
- 主树测试由 1704 增至 **1709**（+5）；refs 由 1741 增至 **1751**（+10），shorthand/prose 43，0 missing；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 更早完成（LiteLLM provider 纯逻辑层，基线 1704）

- `internal/llm/provider.go`（新增，904 行，纯 Go，无网络调用）复刻 `core/framework/llm/litellm.py` 中可离线验证的全部 helper：
  - 模型/端点分类：`DefaultModel`、`IsMinimaxModel`、`IsLocalModel`、`IsOllamaModel`、`EnsureOllamaChatPrefix`、`DefaultAPIBaseForModel`（minimax / openrouter / ollama 默认 base，用户显式 api_base 优先）、五个内置 base 常量（Minimax / OpenRouter / Kimi / Hive）。
  - 缓存策略：`ModelSupportsCacheControl`（大小写敏感的 Anthropic / OpenRouter / Gemini / GLM 前缀白名单）、`BuildSystemMessage`（静态 system + 动态后缀分块，Anthropic 顶层 `cache_control`，其余拼接）、`HistoryCacheBreakpointIndex` / `ApplyHistoryCacheBreakpoint`（最后一个断点落在末条非 system 消息，system-only/空列表跳过）。
  - 重试：`ParseRetryAfter`（retry-after-ms → retry-after 秒 → HTTP-date，负数钳 0）、`ComputeRetryDelay`（指数 `2*2^attempt` 上限 120s、backpressure 地板 1.0s、短重试阈值 5.0s、可选抖动 ×[0.75,1.25] / ×[1.0,1.5]+[0,0.5]、显式 Retry-After 优先于 backpressure）。
  - 用量/成本：`ExtractCacheTokens`（details 优先、creation 回退顶层、OpenRouter/Anthropic 原生形状）、`ExtractCost`（usage.cost → hidden response_cost → 目录定价）、`CostFromCatalogPricing`（`(plain*in + cached*read + creation*write + output*out)/1e6`，`plain = max(input-cached-creation,0)`，cache 费率缺省回落 input）。
  - 工具/JSON：`ToolToOpenAIFormat`、`CloseTruncatedJSONFragment`（截断尾部最多 256 字符）、`RepairTruncatedToolArguments`、`ParseToolCallArguments`。
  - 日志摘要：`SummarizeMessageContent` / `SummarizeMessagesForLog` / `SummarizeRequestForLog`（含 system-only 标记）。
- `internal/llm/llm.go`：`Completion` 增加 `CostUSD float64`，与 Python `LLMResponse` 字段对齐，避免 agent loop → eventbus → 前端链路上丢失成本。
- `internal/llm/provider_test.go`（新增，84 条，全部带 `// Parity:`）：逐条镜像 `core/tests/test_litellm_provider.py` 的 init / tool conversion / retry / minimax / local-ollama / breakpoint / JSON instruction / cache-control / build-system-message / cache tokens / streaming fallback / cost / cost-from-tokens 各测试类。成本类用 `deepseek-v4-pro` 目录价精确断言 `0.003161 USD`。
- 有意分歧（已写进测试注释）：LiteLLM 的 `completion_cost` / `cost_per_token` 无 Go 对应物，改由目录定价计算；Python `None` 组件按负数钳 0；`hive/`→`anthropic/`、`kimi/`→`anthropic/` 代理改写上游无测试覆盖，暂不移植（列为 backlog）；真实 HTTP `AComplete` 尚未实现（P0 已补：`internal/llm/transport.go` 的 `NewHTTPProvider`/`AComplete`，见「本轮完成（P0 可运行切片…）」）。
- 主树测试由 1620 增至 **1704**（+84）；refs 由 1658 增至 **1741**（+83），shorthand/prose 43，0 missing；`gofmt`、`go vet`、`go test ./...`、`make cross`、两道 parity 门禁全绿。

### 上一轮完成（工具分层 keep-set 不变量，基线 1613）

- `internal/tools/tier_invariants_test.go`（新增，7 条测试，全部 parity）：逐条移植 `core/tests/test_toolset_tier_invariants.py`，锁住 2026-07 工具成本分析的结构约束，防止类别编辑悄悄把 eager 层重新吹大。
  - `browser_core` / `browser_extended` 只在共享锚点 `browser_setup` 上重叠，并集等于 legacy `browser_basic ∪ browser_interaction`；`terminal_core` / `terminal_extended` 不相交且并集等于 `terminal_basic`。
  - 两个 keep-set 引用的类别都存在于 `ToolCategories`；worker eager 非空且不含 11 个 `MEASURED_COLD` 工具（browser 冷尾、`search_messages`、`terminal_rg`/`glob`、`pdf_read`）；queen eager 不含 browser 冷尾。
  - worker keep-set ≤ 24 个具体工具名（当前 9），且仍保留实测热核 `browser_setup` / `terminal_exec` / `attach_file` / `web_scrape`。
  - 测试侧的 `MEASURED_COLD` / `WORKER_KEEP_SET_MAX = 24` 与上游一致地定义在测试文件内（Python 同样如此），生产表 `tier_defaults.go` 无需改动。
- 主树测试由 1613 增至 **1620**（+7）；refs 由 1651 增至 **1658**（+7），shorthand/prose 43，0 missing；`gofmt`、`go vet`、`go test ./internal/tools/`、两道 parity 门禁全绿；六平台 `make cross` 通过。

### 上一轮完成（工具执行器隔离 / 上下文传播 / 超时探测策略 + 每工具超时预算，基线 1602）

- `internal/tools/execute_test.go`（新增，9 条测试，全部 parity）补齐 `execute.go` 此前零测试的缺口：
  - 4 条执行器隔离/上下文用例：工具在专属 `hive-tool*` 池上执行（`ToolPoolNameFromContext` 前缀断言 + `SharedToolPool().Name()`）、hung 工具不饿死默认池（2 个执行器占满 `NewToolPool(2, ...)` 后无关 goroutine 仍在 <1s 内完成）、执行上下文经 `SetExecutionContext` 传播进执行器（`data_dir` 可见）、未设置时 `GetExecutionContext()` 为 nil；后两条对应 `core/tests/test_tool_context_propagation.py`，前两条对应 `core/tests/test_tool_executor_isolation.py`。
  - 5 条对应 `core/tests/test_tool_timeout_probe_policy.py` 的异步探测用例：alive / resetting 不 kill（探测恰好 1 次、kill 0），dead / unknown / 无探测能力回退 legacy kill（kill 1）。每条都校验错误结果文本含 `timed out after`、`DO NOT kill`、`NOT an application or browser crash`、`AUTOMATICALLY` 等遥测针。
- `internal/agentloop/loop.go`：补齐上游 `AgentLoop._resolve_tool_timeout` 与每工具超时预算接线。
  - `LoopConfig` 新增 `ToolCallTimeoutSeconds`（0 表示 60，零值配置仍取上游默认）与 `ToolTimeoutOverrides`（按工具名前缀匹配、最长前缀优先；`nil` 表示 Python 默认 `{"browser_": 180}`，空非 nil map 关闭覆盖）。
  - `ResolveToolTimeout(name)` 复刻最长前缀解析；`callExecutor` 据解析出的预算用 goroutine + `select`（结果 / `ctx.Done()` / 定时器）执行，超时即放弃并返回 `IsError` 结果（含 "timed out after Ns"），与上游一致不 kill（Go 执行器同步、无 liveness probe）。
  - `internal/tools/mcp_client.go` 新增 `MCPCallResultTimeout()` 访问器，供不变量测试断言 agent 侧预算低于 MCP 调用结果上限（240s）。
- `internal/agentloop/loop_tool_timeout_test.go`（新增，2 条）：1 条对应 `test_tool_timeout_probe_policy.py::test_loopconfig_prefix_timeout_resolution`（180/60 解析、最长前缀、空覆盖回退、MCP 上限不变量）；1 条 `Parity-exempt` 验证 Go 侧 `callExecutor` 真的执行了预算（0.05s 预算下 5s 卡死调用在 <2s 内返回超时错误）。
- 主树测试由 1602 增至 **1613**（+11）；refs 由 1641 增至 **1651**（+10），shorthand/prose 43，0 missing；`gofmt`、`go vet`、`go test ./...`、`-race`（agentloop）、两道 parity 门禁全绿。

### 上一轮完成（session 自动标题，基线 1595）

- `internal/server/session_titles.go`（新增，参考 `framework/server/session_titles.py`，纯 Go）：queen 每轮结束且会话尚无标题时，用首条用户消息 + 最新回复让辅助模型起名；用户设置的标题永不被覆盖。标题写入会话 `meta.json` 的 `title` / `title_source`（`auto` | `user`），写入走 `atomicfs`（tmp + rename）。
  - `CleanTitle`：剥 `<think>` / `<system-reminder>` 块（`(?s)` 非贪婪；RE2 无反向引用，两个分支各自写闭合标签，leftmost-first 与 Python 扫描序一致）、取首个非空行、按 Python `\s` 折叠空白、剥引号/标记与尾部标点、去 `title:` 前缀（大小写不敏感，含全角冒号）、超过 60 字符按码点截断为 59 + `…`。
  - 关键分歧防护：Go 的 `\s` 是 ASCII，而 Python 的 `re`/`str.strip` 是 Unicode（Zs/Zl/Zp、C1 NEL）；新增 `pySpace` 与 `pySplitLines`（含 `\r\n`/`\v`/`\f`/`\x1c-\x1e`/`\x85`/U+2028/U+2029），并用 Python 逐个输入核对输出一致。
  - `ReadTitle` / `WriteTitle`：缺失或损坏的 `meta.json` 读作无标题；空标题清除 `title` 与 `title_source` 且保留其它键；返回清洗后的标题（清除时为空串）。
  - `GenerateTitle`：摘录各 1,500 码点，请求体逐字为 `User:\n…\n\nAssistant:\n…`，system 提示词与上游逐字一致，`max_tokens` 取 `TitleTokenBudget`（占位 8192，config 包落地后接 `get_aux_max_tokens`）。
  - `SubscribeTitleTrigger`：订阅 `client_output_delta`（记住 queen 最新 snapshot）与 `llm_turn_complete`；`tool_use`/`tool_calls` 轮次不起名；已有标题、无回复、命名进行中均跳过。Go 的 `Publish` 会等待 handler，因此命名在 goroutine 中执行（等价 `asyncio.create_task`），句柄提供 `Subscriptions` / `Wait` / `Close`。
- `internal/server/session_titles_test.go`：7 条测试，5 条对应 `core/tests/test_session_titles.py`（`clean_title` 4 组参数化、完成一轮后写标题并广播 `session_title_updated`、纯工具轮不起名、用户标题不被替换、设置/清除标题的 meta 契约——其 aiohttp PATCH 路由部分待服务端路由 lane）；2 条 `Parity-exempt`（Python Unicode 空白/行边界补充用例、命名失败不落标题且下一轮重试）。
- 主树测试由 1595 增至 **1602**（+7）；refs 由 1635 增至 **1641**，shorthand/prose 43，0 missing；`gofmt`、`go vet`、`go test ./internal/server/`、`-race`（server）、两道 parity 门禁全绿。

### 上一轮完成（worker loop-config 覆盖白名单，基线 1581）

- `internal/colony/worker_loop_config.go`（新增，参考 `framework/host/colony_runtime.py::_build_worker_loop_config`，纯 Go）：这是 queen 每次 spawn worker 时的唯一收口——把 queen 给的 `loop_config_overrides` 变成 worker 真正的 `agentloop.LoopConfig`。
  - `AllowedWorkerLoopOverrides` 复刻 `_ALLOWED_WORKER_LOOP_OVERRIDES` 的 5 个键（`max_iterations` / `grace_iterations` / `tool_call_budget` / `tool_call_lifetime_budget` / `max_context_tokens`）；`tool_call_hard_multiple`、`judge_every_n_turns` 等框架字段**不可被 queen 调**。
  - 起点是 worker profile（`DefaultWorkerLoopConfig`：3 + 1 grace、30 soft budget、framework-locked 3x hard multiple、200 lifetime），因此 live spawn 与 `worker_definition.py` 不会漂移；`max_context_tokens` 与上游一致地保留 `LoopConfig` 自身默认值（180_000）。
  - 未知键静默丢弃（拼错不能杀死 spawn）；非整数 → `LoopOverrideError`（"must be int"），越界 → "out of range [lo, hi]"。边界表与上游一致：`max_iterations` 1–1000、`grace_iterations` 0–3、`tool_call_budget` 0–200、`tool_call_lifetime_budget` 0–2000、`max_context_tokens` 1000–1,000,000；两个 0 是「无上限 / 关闭累计上限」的合法哨兵。
  - 有意分歧：`pyInt` 沿用 Python `isinstance(value, int)` 的两个怪癖——`bool` 接受（true→1）、整数值的 `float64` 拒绝（Python `json` 把 `2.0` 解成 float）；因此调用方应使用 `json.Decoder.UseNumber()` 传递覆盖值，Go 侧用 `json.Number` 保住整数语义。多键同时非法时按排序键报错，保证可复现（Python 按插入序）。
- `internal/agentloop/loop.go`：`LoopConfig` additive 补齐上游字段 `GraceIterations`(0)、`ToolCallBudget`(0)、`ToolCallHardMultiple`(5)、`ToolCallLifetimeBudget`(0)、`JudgeEveryNTurns`(1)，`DefaultLoopConfig()` 同步；既有调用方仍从默认值出发，无行为变化。
- `internal/colony/worker_loop_config_test.go`：14 条测试，13 条逐条对应 `core/tests/test_loop_config_overrides.py` 的 13 个用例（无覆盖返回 worker profile、各字段单独覆盖、两个 0 哨兵、hard multiple 不可调、组合覆盖、未知键丢弃、9 组越界参数化、非整数、grace 0/2、白名单集合锁定），1 条 `Parity-exempt` 锁定 `json.Number` 契约与 `9.0` 拒绝。
- 主树测试由 1581 增至 **1595**（+14）；refs 由 1622 增至 **1635**，0 missing；`gofmt`、`go vet`、`go test ./...`、`-race`（colony + agentloop）、两道 parity 门禁全绿。

### 本轮完成（MCP CLI 移植，基线 1415）

- `internal/mcpregistry` 新增两个源文件（纯 Go，无 cgo）：
  - `cli.go`（1,827 行）：`hive mcp` 全部 14 个子命令——install/add/mode/run/remove/enable/disable/list/info/config/search/health/init/update；argparse 兼容解析（`--flag=value`、`nargs`、`add -- <command...>`、`run` 透传 `--`）；首次使用安全提示（哨兵文件）；缺失凭据交互提示与 override 落盘；表格与 `--json` 双输出；索引缺失/陈旧时的刷新分支与离线回退；`update` 的 pin/本地服务器拒绝、失败回滚、override 与禁用状态保留。
  - `mcp2cli.go`（354 行）：`MCP2CLIInvocation` 把 stdio 配置拼成单条 shell 命令行、http/sse 加 `--transport`、headers 走 `HIVE_MCP_HEADER_n` 环境变量；`cacheKey` 复刻 Python `json.dumps` 转义的稳定摘要；`ShellQuote`/`ShellJoin`/`ShellSplit` 对齐 `shlex`；`RunMCP2CLI` 执行外部 `mcp2cli`（默认 `--list`、设置 `MCP2CLI_CACHE_DIR`、合并环境）；`CLIServersPrompt`/`RegistryCLIServersPrompt` 生成 CLI 模式服务器的提示块。
- 测试 77 条：`cli_test.go` 62 条对应 `core/tests/test_mcp_registry_cli.py` 的 62 个测试；`mcp2cli_test.go` 12 条对应 `core/tests/test_mcp_cli.py` 的 12 个测试（其中 3 条 Go 独有行为豁免：QueenPhaseState 未移植、真实 mcp2cli 子进程不可离线、aiohttp 路由未移植）；`mcp_registry_loader_test.go` 3 条对应 `core/tests/test_mcp_registry_loader.py` 的冲突先到先得、已注册服务器优先、`max_tools` 预算与耗尽跳过。
- 有意分歧：Python dict 插入序 → Go 落盘按名称排序（`CLIServers` 顺序随之不同，内容等价）；`mcp2cli` 子进程调用在单测中通过 `RunServerFunc` 注入点验证，真实二进制执行路径留作集成验证；HTTP 路由版 `POST /api/mcp/servers` 待服务端子系统。
- 主树测试由 1415 增至 **1492**；refs 由 1465 增至 **1538**，0 missing；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 本轮追加（tool input coercer 对等测试，基线 1492）

- `internal/tools/coerce_test.go` 新增 21 条，逐条对应 `core/tests/test_tool_input_coercer.py`：空输入/无 schema/未知属性/类型已匹配的直通，字符串→整数（拒绝小数）/数字（整数收敛为 int）/布尔，联合类型先匹配，NaN/inf 拒绝，`[{label}]`/`[{value}|{text}|{name}]`/单键对象解包，JSON 字符串数组解析、标量包裹为单元素数组、非法 JSON 包裹，嵌套 `questions[].options` 真实 ask_user 形状，字符串属性解包 `{label}` 对象。
- 未识别形状保持原样（多键且无已知键的对象不猜），与上游「让真 bug 暴露」的原则一致。
- 主树测试由 1492 增至 **1513**；refs 由 1538 增至 **1559**，0 missing；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 本轮完成（fork-compaction 状态标记，基线 1574）

- `internal/server/compaction_status.go`（新增，参考 `framework/server/compaction_status.py`，纯 Go）：fork 出来的 colony queen 会话在后台做上下文压缩时，把状态写进 `<queen_dir>/compaction_status.json`，供后续 colony 冷加载判断是否等待。`mark_in_progress` / `mark_done`（带 `messages_compacted`、`summary_chars`）/ `mark_failed`（错误文本按**字符**截断到 500，等价 Python `str[:500]`）；所有写入 fail-soft（出错只记日志）；`get_status` 对缺失/损坏文件返回 `nil`。`await_completion` 轮询直到离开 `in_progress`，超时返回最后一次观测值并告警；Go 版加 `context.Context` 以便服务端关停时中断轮询。`sweep_stale_in_progress` 在服务启动时把孤儿 `in_progress` 改写成 `failed`（"server restarted while compaction was in progress"），避免每个冷加载都等满 180s。
- 有意分歧：时间戳用 RFC3339Nano（项目统一规则，Python 为 `datetime.isoformat()`）；写入走 `atomicfs`，因此也会补建父目录。两者对共享测试不可见。
- `internal/server/compaction_status_test.go`：7 条测试，5 条逐条对应 `core/tests/test_compaction_sweep.py`（缺失根目录 no-op、清理 in_progress、保留 done/failed、多 queen、无 marker 的会话不生成文件），2 条 `Parity-exempt` 补验 mark_*/get_status 往返与 500 字符截断、以及 await_completion 的轮询/超时/取消（Python 套件只覆盖 sweep）。
- 主树测试由 1574 增至 **1581**（+7）；refs 由 1616 增至 **1622**，0 missing；`gofmt`、`go vet`、`go test ./...`、`-race`（server）、两道 parity 门禁全绿。

### 本轮完成（stream stall 提醒源，基线 1566）

- `internal/agentloop/reminders.go`：补齐 `ReminderPoint` 缺失的四个值——`TOOL_BUDGET_CHECKPOINT`（工具预算软阈值 / 硬上限两处触发）、`PRE_COMPACT`、`STOP`、`STREAM_STALLED`（反应式点：看门狗取消停滞流时同步咨询，不能延后到迭代边界），与 `framework/agent_loop/reminders.py` 的枚举全量对齐。
- `internal/agentloop/stream_stall.go`（新增，参考 `framework/agent_loop/stream_stall.py`）：`StreamStallSource` 作为 `STREAM_STALLED` 的 `ReminderSource`。只看 `LoopSignals.StallReason` / `StallElapsed`：disabled、`maxPerTurn <= 0`、已达上限、无 signals、`StallReason == ""` 五种情况静默。命中时自增本轮计数，标签表 `ttft → no tokens before TTFT budget`、`inactive → stream went silent after producing events`（未知 reason 回退原字符串），正文用 `%.0f` 秒并与 Python 逐字一致，meta 携带 reason / elapsed / nudge_count / cap，另有一条 `[stream_stall] producing nudge (...)` 日志。`ResetTurn` 重装每轮上限。
- `internal/agentloop/stream_stall_test.go`：8 条测试，7 条逐条对应 `core/tests/test_stream_stall.py`（inactive 正文与 meta、ttft 标签、每轮上限与 Reset、disabled、零上限、无 reason、points），1 条 `Parity-exempt` 补验 meta.elapsed 与日志行（Python 套件未断言）。
- `loop.go`：`LoopConfig` 新增 `ContinueNudgeEnabled`（默认 true）与 `ContinueNudgeMaxPerTurn`（默认 3），对齐 `internals/types.py`。
- 有意缺口：Go 循环尚未实现 stream watchdog，本 lane 只交付提醒源本身（与 Python 测试的隔离口径一致）；把源接进循环、在停滞取消处同步 `Collect`、每轮起始 `ResetTurn` 是后续 lane。
- 主树测试由 1566 增至 **1574**（+8）；refs 由 1608 增至 **1616**，0 missing；`gofmt`、`go vet`、`go test ./...`、`-race`（agentloop）、两道 parity 门禁全绿。

### 本轮完成（idle nudge / 提醒 hub 时钟，基线 1529）

- `internal/agentloop/park_reason.go`（新增，纯 Go）：`LoopActivity` 三态（executing / awaiting_user / interrupted）、11 个 `ParkReason` 常量与 `AllParkReasons` 全表、`IsExternalWait`（ask_user / credential_form / colony_suggestion / awaiting_queen）、`IsSilentPark`（turn_done / user_stopped / cold_interrupted）、`IsBroken`（llm_error / empty_responses / doom_loop）、`Activity` 映射（六个刻意停泊 → AwaitingUser，其余 → Interrupted），以及循环运行快照 `LoopSignals`。逐条对齐 `framework/agent_loop/reminders.py` 的 `ParkReason` 语义。
- `internal/agentloop/reminder_hub.go`（重写）：`Reminder` 增加 `Meta`（含 `energizes` 覆盖）；新增可选接口 `TurnObserver` / `EnergizingSource` / `TemporalSource`（`TickInterval`）/ `RichReminderSource`（`RenderReminder`，优先于裸 `Render`）；`ReminderHub` 增加已绑定子集、`pending` 缓冲与 ticker 生命周期——`Start` / `stopTicker` / `Stop` / `HasTicker` / `runTicker` / `tickOnce` / `Post` / `TakePending` / `PendingLen`。一次 fan-out 的 `mu` 串行化所有源（ticker goroutine 与循环不并发渲染同一 stateful 源），`pendingMu` 独立以免 `Post`/`TakePending` 阻塞在渲染后；`Stop` 通过 `done` 通道 join ticker，测试断言有 happens-before。
- `internal/agentloop/idle_nudge.go`（新增，参考 `framework/agent_loop/idle_nudge.py`）：`IdleNudgeSource` 作为 `IDLE_TICK` 的 `ReminderSource`，按 `ParkReason` 分派六种子态（between_turns / slow_ttft / parked_no_question / parked_open_tasks / parked_broken + 分派期直接抑制的 TP）。三套独立预算（general / awaiting / broken），按变体封顶（`parked_no_question` → 1；`parked_open_tasks` 且 TurnDone → 1；其余 `MaxNudges`），自限速以「上次求值时间」为键（`lastEvalAt`，任务查询也因此每预算窗口最多一次），gate 日志按状态签名去重（触发 nudge 与 `Reset` 时清空签名）。`TickInterval = clamp(最小正预算/2, 0.01, 5.0)`。任务 / 目标 / Sentinel 自动驾控通过 `ReminderAgentContext` 上的注入闭包读取，避开 `internal/tasks → internal/agentloop` 的导入环；nudge 正文与 Python 逐字对齐（含 doom-loop 专用文案——引导换一种做法而非重试被熔断的调用）。
- `internal/agentloop/idle_nudge_test.go`：37 条测试，逐条对应 `core/tests/test_idle_nudge.py` 的同名用例（属性表全表 / 各子态触发与抑制 / 三套预算 / 封顶与自限速 / gate 去重与重登 / Reset 重装 / 空任务 TurnDone 不 nudge / ticker 停泊与唤醒 / `TakePending` 清空 / 无 temporal 源时 `Start` 为空操作 / 默认配置值）。Python 用 monkeypatch 绕过 `session_id` 守卫，Go 用 `SessionID: "session-1"` 的 provider 上下文保持同等覆盖。
- `loop.go`：`LoopConfig` 新增 `SessionIdleNudgeSeconds` / `SessionIdleNudgeMaxPerSession` / `SessionIdleNudgeAwaitingSeconds` / `SessionIdleNudgeBrokenSeconds`，`DefaultLoopConfig` 按 Python `internals/types.py` 取 120 / 3 / 120 / 30（general 预算 0 是合法值，调用方应从默认值出发）。
- 主树测试由 1529 增至 **1566**（+37，全部带 `.py` 完整路径 `Parity:`）；refs 由 1571 增至 **1608**，0 missing；`gofmt`、`go vet`、`go test ./...`、`-race`（agentloop）、两道 parity 门禁全绿。

### 本轮完成（MCP 传输层，基线 1513）

- `internal/tools/` 的 MCP 传输从接口桩补齐为真实实现，仍为纯 Go（无 cgo）：
  - `mcp_client.go`：`NewMCPClient` 按 transport 分发 stdio/http/unix/sse；`SplitMCPContent`（文本按 `\n` 连接、图片 → `image_url` 块、PDF → `file` 块且文件名取 `_meta.filename` 或 `document.pdf`）；`httpMCPClient`（unix `/health` 探测 + `/mcp/v1` JSON-RPC、连接/超时错误分类、unix 单次重连重试且失败抛原始错误、http 从不重试、30s 重连冷却、存活探测）；`stdioMCPClient`（新行分隔 JSON-RPC、握手超限时不发布连接、凭据不泄漏进错误、死会话单次重试、结果超时上限）。
  - `mcp_transport_support.go`（新增）：进程启动（stdin/stdout 管道先于 `Start`、stderr 丢弃、kill 后 `Wait` 回收）、长行安全的 `mcpLineReader`、按名排序的 `sortMCPTools`。
  - `mcp_sse.go`（新增）：旧版 HTTP+SSE 传输——GET 事件流等待 `endpoint` 事件、POST 到消息端点、多行 `data` 拼接、`failAll`/`shutdown` 一次清理、30s 建连超时。
  - `mcp.go`：`DefaultMCPClientFactory` 接回 `NewMCPClient`，注册表在无注入时即可使用真实传输。
- 测试 16 条：`mcp_client_test.go` 13 条逐条对应 `core/tests/test_mcp_client.py`（unix/sse 建连、unix 重试成功与耗尽、http 错误包裹不重试、内容拆分 5 种形状、stdio 握手超时且不泄密）；3 条 `Parity-exempt` 为子进程夹具与 Go 独有 wire 往返（上游经 MCP SDK，不经手新行 JSON-RPC 帧）。
- 修复：`os/exec` 的 `StdinPipe`/`StdoutPipe` 必须在 `Start` 前创建；子进程用 `-test.run` 承载凭据参数（未知 flag 会让测试二进制提前退出）。
- 主树测试由 1513 增至 **1529**；refs 由 1559 增至 **1571**，0 missing；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（MCP 注册表移植，基线 1329）

- `internal/mcpregistry/` 新建（纯 Go，无 cgo，6 个源文件 / 2,065 行），复刻 `framework/loader/mcp_registry.py`：
  - `registry.go`：目录初始化与幂等 bootstrap（损坏 JSON 报 `DecodeError` 不静默覆盖）、`installed.json` 17 字段条目、`AddLocal` 四种 transport（stdio/http/sse/unix）、`Install`（版本 pin / transport 覆盖 / manifest 快照）、内置服务器退役（按 argv 内容识别，保留用户自有条目）、启停/移除/模式/override、`ListInstalled` / `GetServer` / `CLIServers` / `ServerConfig`、`Search`、`UpdateIndex` / `IsIndexStale`（兼容旧 `last_fetched.json`）。
  - `resolve.go`：`ResolveForAgent`（include/tags/exclude/profile/versions/`max_tools`，按名排序后截断；harness 组合成；禁用跳过；缺失仅告警）、`manifestToServerConfig`（env/headers 合并、override 胜出、unix）、`LoadAgentSelection`（类型不符字段静默丢弃）。
  - `health.go`：`HealthClient` / `ConnectionManager` / `ClientFactory` 三个接口——池化连接优先（acquire/release 各一次，池内不健康为硬错误），否则新建客户端；记录 `last_health_status`/`last_error`/`last_tool_count`/`last_health_check_at`；`HealthCheckAll` 批量探测。
  - `version.go`：编译期版本优先，为空时回退 `pyproject.toml` 的 `[project].version`（section-aware，`[tool.*]` 不干扰），缺失返回 `"unknown"`；`values.go` 为 Python 语义辅助函数；`errors.go` 为四段式错误。
- 测试 86 条（`registry_test.go` 53、`resolve_test.go` 22、`health_test.go` 9、`version_test.go` 2），与 `core/tests/test_mcp_registry.py` 的 86 个测试逐条对应。
- 有意分歧：Python dict 插入序 → Go 按名称排序迭代（上游断言仅依赖集合/计数）；JSON 落盘键序与临时文件名不同，用 `atomicfs.WriteJSON` 的原子替换语义对齐；`max_tools` 仅接受 JSON 整数（`UseNumber` + `intValue`），与 `isinstance(x, int)` 一致。
- 主树测试由 1329 增至 **1415**；refs 由 1381 增至 **1465**，0 missing；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 上一轮完成（janitor / 保留策略移植，基线 1285）

- `internal/maintenance/` 新建（纯 Go，无 cgo）：`config.go`（`RetentionConfig` / `DefaultRetentionConfig` / 环境与文件覆盖 / `ForOffline`）、`retention.go`（保留扫描核心：`SafetyContext` + `Refresh`、`IsSafeToPrune` 六道门禁、`IterQueenSessions` / `IterWorkerSessions`、`SpilloverReferenceCorpus` + `FindOrphanSpillovers`、`PruneQueenSession`、`DeepCleanWorker`、`PruneTier1Logs`）、`janitor.go`（`RunOnce` 三档编排、session 锁 TTL 3600s、全局锁 TTL 2h、`LoadLastReport`、`WriteServerMarker` / `LiveServerOwnsHiveHome` / `ClearServerMarker`、离线执行破坏性档位的拒绝文案）、`archive.go`（`ArchiveDisposer` 先写 `.tmp` 再落盘、失败保源）、`lock_unix.go` / `lock_windows.go` / `pid_unix.go` / `pid_windows.go`（build tag 分离，Windows 用 OpenProcess 探活）。
- 修正移植缺陷：`IterWorkerSessions` 原先把 `colonies/<colony>` 当 worker 目录，缺 `workers` 段；对照 `retention.py:561-581` 的 `workers_dir = colony / "workers"` 改为 `colonies/<colony>/workers`，8 条 worker 深清理用例随即从 5 失败转全绿。
- `internal/server/worker_read.go`：`ReadWorkerFromDisk` / `ReadWorkerConversation`，深清理后仍能读墓碑（status=historical、task、result.summary）且会话返回空 transcript。
- 测试 44 条（`maintenance` 42 + `agentloop` 1 + `server` 1）：`tier1_test.go` 7、`safety_test.go` 12、`queen_hygiene_test.go` 8、`worker_deepclean_test.go` 8、`review_fixes_test.go` 7，分别对应 `core/tests/test_janitor_{tier1,safety,queen_hygiene,worker_deepclean,review_fixes}.py`；`agentloop/spillover_headers_test.go` 1 条对应 `test_janitor_review_fixes.py::test_extractors_match_all_header_formats`；`server/worker_read_test.go` 1 条对应 `test_janitor_worker_deepclean.py::test_endpoints_render_after_deep_clean`。
- 主树测试由 1285 增至 **1329**；refs 由 1337 增至 **1381**，0 missing；`gofmt`、`go vet`、`go test ./...`、`go test -race ./internal/maintenance/ ./internal/agentloop/ ./internal/server/`、`make cross`、两道 parity 门禁全绿。

### 上一轮完成（worker 升级 handoff，基线 1281）

- `internal/server/session.go`：复刻 `framework/server/session_manager.py` 的会话面——`Session`（事件总线、queen executor、colony 运行时、worker 升级订阅 id、`PendingEscalations` 收件箱、memory 反射订阅）、`SessionManager.Add/Get/StopSession`；`StopSession` 弹出会话后退订 handoff 与 memory 订阅并清空订阅 id，已停止会话再次升级不会再投递。
- `internal/server/queen_orchestrator.go`：复刻 `install_worker_escalation_routing`——只处理 `worker:` 流；记录 `request_id -> {worker_id, colony_id, node_id, reason, context, opened_at}`；构造 `[WORKER_ESCALATION]` 文本并以 `is_client_input=false` 注入 queen；超过 `MAX_PENDING_ESCALATIONS=32` 时向 worker 回 `[QUEEN_REPLY] queue_full …` 解阻塞；queen 缺席时回落为 `CLIENT_OUTPUT_DELTA` + 单条 `CLIENT_INPUT_REQUESTED`；有 colony 运行时时用 `WithFilterColony(stream_id)` 做结构性隔离。
- 测试：`worker_handoff_test.go` 4 个 parity 对应 `core/tests/test_session_manager_worker_handoff.py`（注入并记录、忽略 queen 流、queen 缺席回落、stop_session 退订）。
- 主树测试由 1281 增至 **1285**；refs 由 1333 增至 **1337**，0 missing；`gofmt`、`go vet`、`go test ./...`、`go test -race ./internal/server/`、`make cross`、两道 parity 门禁全绿。

### 更早完成（worker binding 传播 + started_at，基线 1277）

- `internal/tracker/binding.go`：新增 `BindingFromInputData`，复刻 `ColonyBinding.from_dict`——接受 `map[string]string`（queen 写入形态）、JSON 往返后的 `map[string]any`、以及进程内的 `*Binding`/`Binding`；`name` 必须为非空字符串，`dir`/`tracker_db` 必须为字符串（允许空串），其余一律返回 nil。
- `internal/colony/worker.go`：新增 `Worker.TrackerResolver()`，惰性读取 `Context.InputData` 并重水化 binding——Go 无 contextvars，这是 Python worker 把 binding 盖到 ToolRegistry 执行上下文的等价物；无 binding 时返回 `(nil, nil)`，tracker 工具因此以 “no colony context” fail-closed，而不是按事件总线 colony id 合成影子 DB。
- `internal/colony/runtime.go`：`WorkerBuildOptions` 增加 `InputData`；`BuildWorker` 在注入 input_data 时同步填充 `ColonyBindingProvider`，供提示词整形读取 tracker DB；新增 `MarkStarted`/`StartedAt`，`runWorker`/`runOverseer` 在进入运行时记录墙钟时间，`WorkerInfo.StartedAt` 以 epoch 秒暴露（未启动为 0），修正 Python 曾用 `time.monotonic()` 导致的 1970 伪时间。
- 测试：`worker_binding_test.go` 2 个 parity 对应 `core/tests/test_worker_tracker_db_propagation.py`（binding 注入后解析到 colony tracker DB、JSON 往返同形、缺失时两个 tracker 工具均拒绝）；`worker_started_at_test.go` 2 个 parity 对应 `core/tests/test_worker_started_at.py` 与 `WorkerInfo` 默认值。
- 主树测试由 1277 增至 **1281**；refs 由 1329 增至 **1333**，0 missing；`gofmt`、`go vet`、`go test ./...`、两道 parity 门禁全绿。

### 更早完成（worker tool tier spawn + allowlist，基线 1273）

- `internal/colony/runtime.go`：复刻 `colony_runtime._build_worker` 的 spawn 构造路径——`BuildWorker(WorkerBuildOptions)` 接收 `SpawnTools`/`PreloadTools`（nil 时回落到经 allowlist 过滤的 colony 工具），始终写入 `AvailableTools`；keep-set 非空时构造 `tools.ToolTierState`（Pool + AlwaysEnabledNames=keep-set + GateableNames=colony MCP 名集 + `tool_tiers.json` 持久化），先 `RestoreLoadedTools(LoadPersistedTools(), registered)` 再 `Rebuild()`，把 preload 过滤到已注册名字后 `PromoteSearchedTools`；接线 `DynamicToolsProvider`（`[]tools.Tool`→`[]llm.ToolDef`）、`SearchableToolsProvider`（→`[]agentloop.Tool`）、`LoadedToolNamesProvider`。keep-set 为空时保持 dark：无 tier、无 provider、全量 `AvailableTools`。
- `internal/colony/runtime.go`：`SetToolAllowlist(enabled, all)` 复刻 Python 语义——`enabled` 为 nil 表示放行全部（nil 保持），MCP 名集仅在非 nil 时替换；`applyToolAllowlist` 非 MCP 工具无条件保留，MCP 工具仅在 enabled 中才保留；仅在下次 spawn 生效。`SpawnBatch` 改为先在锁内预留 id、锁外 `BuildWorker`、再统一注册与启动，`runWorker` 复用 spawn 时构建的 context（nil 才惰性重建）。
- 测试：`worker_tier_spawn_test.go` 4 个 parity 对应 `core/tests/test_worker_tier_spawn.py` 全部用例（keep-set 接线、空 keep-set 全暗、preload 提升并过滤未知、loaded 集跨重建恢复）。
- 主树测试由 1273 增至 **1277**；refs 由 1325 增至 **1329**，0 missing；`gofmt`、`go vet`、`go test ./...`、`make cross`、两道 parity 门禁全绿。

### 本轮完成（session fork + SSE worker filter，基线 1263）

- `internal/session/fork.go`：复刻 `framework/server/session_fork.py`——`ForkSessionDir` 以 `client_input_received`/`client_input_committed` 的 correlation 对齐用户消息时间戳，按 cut 截断 `conversations/parts`（seq < cut）与 `events.jsonl`（`[:event_index]`）；不携带 tasks/summary/reminder/timeline 文件；cursor 重置为 `next_seq=cut` 并停泊在 `turn_done`（清空 recent_responses / tool fingerprints、user_stopped=false）；meta 剥离 source-only 标志、写入 `forked_from`；随后 `memory.MarkExtracted` 把继承消息标记为已提取。
- `internal/server/worker_filter.go`：复刻 `routes_events._is_worker_noise` 的 SSE 适配层——`IsWorkerNoise` 委托 `eventbus.IsWorkerLocal`，`WorkerEventAllowlist` 暴露 fan-in 事件白名单，nil stream 视为非 worker。
- 测试：`fork_test.go` 5 个 parity 对应 `core/tests/test_session_fork.py` 纯函数用例（按点裁剪、文件继承、整会话复制、未知点拒绝、压缩后点拒绝；2 个 aiohttp 路由用例待 HTTP app）；`worker_filter_test.go` 5 个 parity 对应 `core/tests/test_sse_worker_filter.py` 全部用例。
- 主树测试由 1263 增至 **1273**；refs 由 1315 增至 **1325**，0 missing；`go test ./...`、`go vet`、`gofmt`、两道 parity 门禁全绿。

### 本轮完成（colony 运行时 / overseer 移植，基线 1249）

- `internal/colony/worker.go`：运行时侧 Worker 模型（status/report/result/done 通道、显式报告记录与扁平 reportPayload、batch 元数据、`WorkerInfo`）；`WorkerStatus` 复用调度器定义，`Done()` 判终态。
- `internal/colony/runtime.go`：`ColonyRuntime` 的 spawn/spawn_batch/wait_for_worker_reports/start_overseer/seed_conversation 与 worker 生命周期事件——worker goroutine 跑 `agentloop.ExecuteTurn`，`report_to_parent` 仅对 `worker:` 流开放，终态发布扁平 `SUBAGENT_REPORT` 与 `EXECUTION_COMPLETED/FAILED`；overseer 持久化、幂等、首轮后驻留至 Stop；seed 消息按 `workers/<id>/parts/<n>.json` 落盘。
- `internal/agentloop`：新增 `AgentContext.StopAfterToolBatch`，在工具批记录后立即结束内层循环，使含报告的混合工具批只消耗一次模型调用。
- `internal/colony/runtime_overseer_test.go`：14 个 parity 测试对应 `core/tests/test_colony_runtime_overseer.py` 全部用例（goal 公开、overseer 持久/幂等、报告事件扁平载荷、崩溃合成失败报告、batch 顺序、报告按输入序收集、缺失 id、seed 落盘、报告门控与两类终止场景）。
- 主树测试由 1249 增至 **1263**；refs 由 1301 增至 **1315**，0 missing；`go test ./...`、`go vet`、`gofmt`、两道 parity 门禁全绿。

### 本轮完成（wave 1 五条并行 lane 已合并主树）

- Lane A `internal/skills/installer.go`：技能安装请求执行（12 个 parity 测试）。
- Lane B `internal/triggers/`：`cron.go`（自研 5 段 cron 解析）、`triggers.go`（Definition/View/missed 计算）、`triggers_store.go`（last_fired_at/next_due_at/enabled 持久化），17 个测试。
- Lane C `internal/agentloop/`：`reminders.go`（四类 ReminderPoint）、`tool_skill_reminders.go`（search_tools / skills catalog 源）、`skill_resources.go`（SKILL.md 读取拦截与错误文案）、allowlist 测试。
- Lane E `internal/eventbus/`：`event.go`/`event_type.go`/`event_type_lookup.go`/`bus.go`/`emit.go`/`event_log.go`/`events_policy.go`/`history`/`snapshot.go`/`global.go`，113 个测试。
- Lane D：`00-product-goals.md`、`11-product-requirements.md`、`12-design.md`、`13-cross-platform-and-purego.md`、`14-quality-assurance.md`、`15-test-parity-matrix.md`、README、`Makefile`、`.github/workflows/go.yml`、`scripts/check_parity.sh`。

### 本轮完成（credentials 独立验证）

独立验证代理给出 **PASS**：48/48 credentials 测试通过，gofmt/vet/race 干净；与 Python `cryptography` Fernet 双向互通；加密存储跨语言可读；无 `~/.hive` 越界写入；对抗性探测通过。非阻塞观察：Go 端不接受 utf-8-sig BOM、文件权限 0600（Python 0644）、ID 排序策略不同。

### 本轮完成（storage lane 已合并主树）

- Lane J `internal/storage/`：`session_store.go`、`conversation_store.go`、`checkpoint_store.go`、`concurrent.go`、`session_summary.go`、`migrate_v2.go`、`migrate_v3.go`、`migrate_colony_sessions.go`，55 个测试（会话/检查点 CRUD、路径穿越防护、摘要重建与陈旧判定、缓存生命周期、v2/v3/colony 迁移）。
- Lane J `internal/schemas/`：run 模型与摘要，10 个测试。
- Lane J `internal/globaldb/`：网格查询构建（列白名单、过滤、搜索、排序、分组计数、删除、SQL 转义），23 个测试。
- 主树测试由 664 增至 **752**；两道 parity 门禁与六平台交叉编译全部通过。
- 待决策：`internal/storage` 与既有 `internal/session` 语义重叠，暂并行保留，需在后续里程碑收敛到单一实现。

### 本轮完成（worker tool tiers 对等测试，基线 1230）

- `internal/tools/tiers_test.go`（11 条）：复刻 `core/tests/test_worker_tool_tiers.py` 的 `TestToolTierState`（拆分分区、空 keep-set 关闭拆分、eager 列表指针复用、promote + 持久化 + 二次幂等、restore 丢弃未注册/未授权、持久化往返、allowlist 成员门禁）与 `TestSearchToolsFactory`（select 提升、关键词匹配、已加载上报、`search_tools` schema 与 queen 注册一致）。
- `internal/agentloop/tiers_parity_test.go`（8 条）：复刻 `TestWorkerReminder`（tier worker 绑定/未绑定跳过/queen 仍绑定、worker manifest 渲染、已加载工具离场静默）、`TestDynamicRefreshSynthetics`（worker 合成工具在刷新后保留且无重复、provider 自带 `search_tools` 不重复）、`TestPromptGatingUsesTier`（预激活技能读 eager 集而非 spawn 池；promote 后 `hive.browser-automation` 指南出现）。
- `internal/tools/tiers.go` 新增 `CurrentToolNames() []string`，使 `*ToolTierState` 满足 `agentloop.CurrentToolNamer`，`BuildPromptSpec` 的 tool-gated 技能预激活得以读取当前 eager 集合。
- 主树测试由 1230 增至 **1249**；refs 由 1282 增至 **1301**，0 missing；`go test ./...`、`go vet`、`gofmt`、两道 parity 门禁全绿。

### 本轮完成（colony worker 调度器移植，基线 1230）

- `internal/colony/scheduler.go`：复刻 `framework/host/colony_runtime.py` 的并行 worker 调度——并发上限（`HIVE_MAX_CONCURRENT_WORKERS`，默认 4）、FIFO `pendingQueue`、单一准入点 `Submit`/`enqueueOrAdmit`（准入时排除自身、队列满则 QUEUED）、终态 `SUBAGENT_REPORT` 触发 `DrainPendingQueue` 逐个补位、`colony_id` 过滤（他 colony 的报告不得释放本 colony 槽位）、停止扫描期 `dispatchBlocked` 门禁（拒绝并合成 stopped 报告）、`CancelQueued` 为每个排队 worker 合成带完整 batch 元数据的终态报告、`StopWorkers` 并发停止活动 worker。
- `WorkerStatus` 与上游 StrEnum 值逐字一致（queued/pending/running/completed/failed/stopped）；`IsActive` 含 QUEUED，`IsRunningWorker` 仅 PENDING/RUNNING，与 `_running_worker_count` 的计数口径一致。
- `SpawnBatch`：空 batch id 铸为 `rpw_%Y%m%dT%H%M%SZ_<8 hex>`，`batch_index` 从 1 起、`batch_size = len(tasks)`，`StartFunc`/`StopFunc` 由运行时注入（测试用 gate 驱动）。
- `internal/colony/scheduler_test.go`：6 个 parity 测试对应 `core/tests/test_colony_scheduler.py`（低于上限全部启动、超限入队、终态补位、停止为排队 worker 合成 stopped 报告、排队中仍带 batch 元数据、双 batch 共享 FIFO 队列）；另 4 个 `Parity-exempt`（dispatch 门禁拒绝、跨 colony 报告过滤、drain 防御性跳过已取消 worker、batch id 格式）。
- 主树测试由 1220 增至 **1230**；refs 由 1276 增至 **1282**，0 missing；`go test ./...`、`go test -race ./internal/colony/`、`go vet`、`gofmt` 全绿。

### 本轮完成（colony worker_definition + seed + reap + parallel nudge 移植，基线 1220）

- `internal/colony/worker_definition.go`：复刻 `framework/agents/queen/worker_definition.py`——`DEFAULT_LOOP_CONFIG` 六个默认值、`WORKER_SYSTEM_PROMPT`（与 Python 逐字节一致，含上游拼写 `convenrse` / `no live audience ad no`）、`build_system_prompt`/`build_input_data`/`build_loop_config_dict`/`build_meta`。
- worker.json 序列化：`identity_prompt`/`memory_prompt` 恒空，`goal.success_criteria`/`constraints` 输出 `[]`，`tools`/`skill_dirs` 为独立拷贝，`spawned_at` 按 Python `datetime.isoformat()` 的微秒+`+00:00` 形态，`concurrency_hint` 仅在 >0 时出现；queen loop config 只传播 4 个键，`grace_iterations`/`tool_call_hard_multiple`/`tool_call_lifetime_budget` 保持 worker 默认。
- `internal/hivehome` 补 `Colony.ToDict` / `ColonyFromDict`，复刻 `ColonyBinding.to_dict` / `from_dict`（畸形输入返回 false，等价 Python 返回 None）。
- `internal/colony/worker_definition_test.go`：6 个 Go 测试覆盖 `core/tests/test_worker_definition.py` 的 26 个上游用例；系统提示词与 Python 输出经 `cmp` 校验逐字节相同。
- `internal/colony/seed.go`：复刻 `routes_sessions._seed_colony_queen`，等待 queen loop 启动后把 Create Colony 对话框的 goal 以用户消息（`CLIENT_INPUT_RECEIVED`）投递；无 goal 时投递「Carry on with the work agreed」框架注记；超时告警而不阻断 fork。4 个 parity 测试对应 `core/tests/test_colony_seed.py`。
- `internal/colony/reap.go`：复刻 `ColonyRuntime._reap_worker_browsers`——逐 worker 并发、单次关闭以 5s 上界约束（`_BROWSER_REAP_TIMEOUT_S`），超时即取消并放弃未响应的 bridge；错误吞掉（`return_exceptions=True`）；浏览器模块缺失时静默 no-op；profile 缺省回落 `default`。4 个测试对应 `core/tests/test_colony_reap_timeout.py`。
- `internal/agentloop/colony_parallel_nudge.go`：复刻 `framework/agent_loop/colony_parallel_nudge.py`——pilot（`browser_*`/`web_scrape`）+ 当轮 tracker 写入双门禁、`run_playbook` 后静默、仅 queen 生效、colony phase 以动态工具列表判定（provider 异常回落静态快照）、每轮冷却与会话上限；提醒文案与上游逐字节一致（含 `It it works` 拼写）。9 个测试对应 `core/tests/test_colony_parallel_nudge.py`。
- `internal/agentloop/reminders.go` 的 `ReminderAgentContext` 增加 `AvailableTools` / `DynamicToolsProvider` 两个可选字段，向后兼容。
- 主树测试由 1197 增至 **1220**；refs 由 1238 增至 **1276**，0 missing；`go test`、`go vet`、`gofmt` 全绿。

### 本轮完成（orchestrator safe_eval 沙箱求值测试，基线 1197）

- `internal/orchestrator/safe_eval.go`（纯 Go，无 eval/反射执行外部代码）：Python 表达式子集 lexer/parser/evaluator，白名单内置函数与方法、属性私有性检查、幂次守卫（指数绝对值 ≤ 1000、结果 ≤ 4096 bit）、在每个求值节点轮询截止时间（替代 SIGALRM）；数值/比较/布尔/字典序/元组/字符串切片等语义按 Python 对齐。
- `internal/orchestrator/safe_eval_test.go`：逐类复刻 `core/tests/test_safe_eval.py` 的 15 个测试类，16 个 Go 测试覆盖 120 条上游用例；`TestExecutionTimeout` 的 2 条 SIGALRM/getitimer 内部用例合并为 1 条 `Parity-exempt` 截止时间轮询用例（可观察契约等价）。
- 注解统一为单行逗号分隔 `// Parity:`，使 `check_parity_targets.sh` 真正审计到新引用；refs 由 1118 增至 **1238**，0 missing。
- 主树测试由 1181 增至 **1197**；`go test`、`go vet`、`gofmt`、`make cross` 全绿。

### 本轮完成（Lane F / Lane K / Lane L 合并，基线 1181）

- Lane F `internal/tools/`：`registry.go`、`tiers.go`、`tier_defaults.go`、`capabilities.go`、`coerce.go`、`difflib.go`、`execute.go`、`tool.go`、`mcp.go`、`mcp_registry.go`；MCP 注册半区复刻 `framework/loader/tool_registry.py`（配置解析、重试注册、harness 分组、准入门禁与 verified-manifest 哨兵、凭据快照 diff、resync/probe/kill、清理）。`tool_registry_test.go` 42 条（`core/tests/test_tool_registry.py`）+ `harness_tools_test.go` 10 条（`core/tests/test_harness_tools.py`，其中 3 条 Go 独有行为豁免）。共 52 测试。
- Lane K `internal/llm/`：`catalog.go`（+ `model_catalog.json`）、`capabilities.go`、`keypool.go`、`endpoints.go`、`stream_events.go`、`keycheck.go`、`mock.go`；107 条测试，覆盖模型目录与 token 上限、图像能力过滤与消息提升、密钥池轮转、端点解析、流事件构造、OpenRouter/Anthropic 兼容键检查、LiteLLM mock provider。`llm.go` 仅新增 `ProducesImage`/`ConcurrencySafe`/`SystemDynamicSuffix` 字段，向后兼容。
- Lane L `internal/playbook/`：`runner.go` 复刻 `framework/host/playbook/runner.py`（converge 循环、lane 并发、rate gate、重试/退避、dead-letter、phase/log hooks）；`rows.go` 复刻 `framework/tools/playbook_tools.py` 的 rows 提取、保存/解析、并发声明与批量停止。20 + 12 = 32 条测试。
- 主树测试由 990 增至 **1181**；3 个新包均通过独立测试、`go build ./...`、`go vet ./...`、gofmt 与两道 parity 门禁。

### 本轮完成（orchestrator 幻觉检测移植，基线 990）

- 新建 `internal/orchestrator/`：`buffer.go` 复刻 `framework/orchestrator/node.py` 的 `DataBuffer`（共享 `_data`/锁的权限视图、`>5000` 字符代码检测、`validate=false` 旁路、非字符串跳过、10KB 以上五分位采样）；`validator.go` 复刻 `OutputValidator`（`ValidationResult`、`validate_output_keys`、`validate_no_hallucination`、`validate_schema` 的无依赖 Draft-7 子集、`validate_all`），错误文案与参考一致。
- `hallucination_test.go`：19 个对等测试逐条复刻 `core/tests/test_hallucination_detection.py` 的 DataBuffer（10）、OutputValidator（5）、EdgeCases（4），另 5 个 Go 侧 API 用例标注 `Parity-exempt`（权限视图、键校验、长度上限、schema 子集、validate_all 组合）。
- 主树测试由 966 增至 **990**；`check_parity.sh` 990/990/0，`check_parity_targets.sh` 970 refs / 0 missing / 39 prose。

### 本轮完成（find_json_object 硬化移植，基线 966）

- `internal/agentloop/message.go`：把 `findJSONObject` 重写为逐字复刻 `framework/orchestrator/node.py:find_json_object` 的两段式算法——先取「第一个 `{` 到最后一个 `}`」整段做 `json.Valid` 快路径（百万级输入线性），失败后从第一个 `{` 单次花括号深度扫描（忽略字符串内花括号与转义），不再从每个 `{` 重试。旧实现遇到 100KB 模板花括号会退化为 O(n²)，新实现保持单次扫描。
- `internal/agentloop/find_json_test.go`：44 个对等测试，逐类复刻 `core/tests/test_find_json_hardened.py` 的 BasicCorrectness（19）、LargeOutputRegression（8，含 100KB/1MB/2MB 计时）、Adversarial（13，含参数化边界 9 例）、BehaviourParity（4）。
- 主树测试由 922 增至 **966**；`check_parity.sh` 966/966/0，`check_parity_targets.sh` 951 refs / 0 missing / 39 prose。

### 本轮完成（colony 基础层，基线 922）

- `internal/colony/colony_db.go`：`colony.db`（`colonies/<id>/colony.db`）playbook 运行日志的 upsert/列表，与 domain tracker 分离；4 个 parity 测试对应 `core/tests/test_colony_db.py`。
- `internal/colony/resume.go`：`IterQueenSessionDirs` / `ColonyIDForSessionDir` / `DeriveResumeColonyID`，复刻 `framework/server/session_manager.py` 的 resume colony 推导（DM 影子不得劫持 colony 会话）；5 个 parity 测试对应 `core/tests/test_derive_resume_colony_id.py`。主树测试由 913 增至 **922**，parity 922/922、refs 907。

### 本轮完成（Lane G + Lane I 已合并主树，基线 913）

- Lane G `internal/agentloop/`：并入 `loop.go`（循环核心）、`llm_compact.go`（模型侧压缩）、`context_error.go`（上下文溢出识别与处理）、`reminder_hub.go`、`synthetic_tools.go`；`conversation.go` 增加 `ConversationOptions`（`UnlimitedContext` 显式表达 `max_context_tokens=0`，恢复时还原）；`message.go` 的 `jsonKeyValue` 改为 `json.Valid` + `UseNumber` + Python 风格 `pyJSONDumps`（键有序、`", "`/`": "` 分隔、不转义 HTML）。agentloop 测试由 171 增至 **204**。
- Lane I `internal/tasks/`：`store` / `tools` / `scoping` / `reminders` / `e2e` / `integration`，**128** 个测试，覆盖任务记录、状态流转、归档/恢复、原子写入、hook 门禁、完成后缀、pivot 路由、提醒去重、跨进程持久性。
- 合并方式：worktree 文件回拷贝，无提交；主树两份旧 agentloop 测试文件确认为 lane 重命名/超集后删除（删除前已备份 `/tmp/agentloop_backup_*`）。

## 3. 质量基线

> P0 集成（2026-11-18 晚）新增 `internal/llm/transport.go`、`internal/tools/builtin`、`internal/server/chat_*`、`internal/cli` 装配与 `e2e/p0_chat_test.go`；以下数字已按 P0 集成后的整树门禁更新。

- 主树 `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./... -count=1`：**全绿**（含 P0 新增 `internal/tools/builtin`；agentloop/agents/atomicfs/attachments/cli/colony/credentials/eventbus/globaldb/hivehome/llm/llmjudge/maintenance/mcpregistry/memory/orchestrator/playbook/queen/ratelimit/runtimehealth/schemas/server/session/sessionreport/skills/storage/tasks/tools/tracker/triggers 等）。
- `go vet ./...`、`gofmt -l .`：干净（`make ci` 的 `fmt-check`/`vet` 均无输出）。
- `go build ./...`：通过；6 平台交叉编译由 `make cross` 驱动并通过（darwin/linux/windows × amd64/arm64）。
- 规模：P0 门禁实测 **2,238 个测试函数**，全部纳入 parity 门禁；注解构成明细（P0 前：1,838 个注释块含带 `.py` 路径的 `Parity:` 行、376 个仅 `Parity-exempt`）尚未按 P0 后重算；静态计数 Python 参考测试 `core/tests` 167 个文件、1,724 个 `def test_`（`core/` 共 1,971，`tools/` 3,001，含类方法、参数化展开前），对等测试仍在持续补齐。
- `scripts/check_parity.sh`：**2238/2238 已注解，0 missing，退出码 0**。
- `scripts/check_parity_targets.sh`：**2045 条 `path::test` 引用，0 missing file / 0 missing test**；另有 55 条 shorthand/prose 由人工评审。
- E2E：`go test -tags e2e -count=1 ./e2e/...` 与 `go test -race -tags e2e -count=1 ./e2e/...` 均 ok；凭据门禁 `TestCredentialFlowEnvKeyReachesUpstream` 通过（真实二进制 + 假上游收到 `Authorization: Bearer test-key`）。
- 离线构建：`GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod`。

## 4. 预期交付（里程碑口径）

1. P1 全部包 + parity 测试全绿。→ 已完成
2. P2 AgentLoop/Colony 端到端可跑通 Python 对应的循环测试。→ agentloop 核心与工具注册已完成；colony 基础层（colony.db / resume / worker 定义 / seed / browser reap / worker 调度器）与运行时编排 / overseer / worker 工具分层 spawn 已落地；DM 会话 fork 纯逻辑（parts/events 裁剪、整会话复制、压缩与未知时间点拒绝、meta/timeline 继承）、global-db 数据代理决策层（lead_id 派生 / insert 计划 / changes 游标 / 错误信封）、会话目录解析（DM 优先遍历 / colony 归属推导 / resume 绑定）已对等；fork 的 HTTP 路由、global-db handler 接线、并行 worker 工具与 colony fork 流程待 P3。
3. P3 服务与 CLI 可启动、SSE 可消费。→ 配置类 20 条路由 + 实时会话列表 `GET /api/sessions` + 事件历史分页 `GET /api/sessions/{id}/events/history` 已接线并有 HTTP 级对等测试；CLI 根入口（`serve`/`open`/`queen`/`colony`/`session`/`chat` + 12 个 `skill` 子命令 + `debugger` + `mcp` 14 个子命令 + `janitor` 2 个子命令 + LLMJudge）与独立入口 `hive-cred`（`token`/`browse`/`inspect`）已完成并有 110 条测试（cli 88 + llmjudge 22）；P0 会话切片已接线：`hive serve` 起 HTTP 服务，`POST /api/sessions` 建真实会话、`POST .../chat` 投递、`GET .../events` 提供会话事件 SSE；其余 26 个路由模块、完整模型流式管线与 purego 终端层待接。
4. P4 文档与 parity 门禁完成，CI 在三大平台交叉编译通过。

时间预估属于粗略口径，以里程碑完成度为准，不承诺具体日期。

### 最新一次验证（2026-11-18，P0 集成后整树门禁 PASS）

- `cd /Users/mac/hive/go_hive/go-hive && make ci` → **退出码 0**：`fmt-check`、`vet`、`test`、`cross`（darwin/linux/windows × amd64/arm64）、`parity` 全部通过。
- `scripts/check_parity.sh`：**2,238/2,238 已注解，0 missing**；`scripts/check_parity_targets.sh`：**2,045 条 `path::test` 引用，0 missing files/tests，55 条 shorthand/prose**。
- `go test -tags e2e -count=1 ./e2e/...` → **ok**；`go test -race -tags e2e -count=1 ./e2e/...` → **ok**。
- 凭据门禁：`TestCredentialFlowEnvKeyReachesUpstream` **PASS**——真实二进制 + 假 OpenAI 上游收到 `Authorization: Bearer test-key`。
- 二进制冒烟：`dist/hive-darwin-arm64 --version` → `hive 1.0.14-go.1`；健康检查/启动就绪；`POST /api/sessions` → 201；无 key 时 chat → 503 `{"error":"No LLM configured for this session"}`。
- 下一节的 19:25 整树绿与 2,215/2,035 计数属于 P0 集成**前**基线，仅作历史记录；当前口径以本节 2,238/2,045 为准。

### 最新一次验证（2026-11-18 19:25，agents 发现/删除/元数据路由 + 会话创建/live_tools 合并后整树复跑）

- `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod PY_ROOT=/Users/mac/hive/core make ci`：退出码 0；`fmt-check`、`vet` 无输出；30 个含测试的包全绿；`cross` 六目标（darwin/linux/windows × amd64/arm64）全部编译通过并产出 `dist/hive-*` 六份二进制。
- `check_parity.sh`：**2215/2215、0 missing**（`check_parity: OK — 2215 个测试全部带有 parity 注解或豁免说明。`）；`check_parity_targets.sh`：**2035 refs、0 missing files/tests、46 shorthand/prose**。
- 本轮合并：`internal/agents`（6 个实现文件 / 781 行 / 34 条测试）与 `internal/server` 的 `routes_agents.go`、`routes_session_create.go`、`routes_session_live_tools.go`（12 + 14 + 5 条测试）；测试 2150→**2215**（+65），refs 2007→**2035**，纯 `Parity-exempt` 块 337→**376**，纯 `.py` 引用块 1,812→**1,838**（1 个块两者兼有）。
- 代码规模（本快照，19:25 复测）：223 个非测试 `.go` 文件 / 71,036 行；168 个测试文件 / 56,004 行 / 2,215 条测试；`internal/server` 35 个实现文件 / 9,782 行 / 279 条测试。
- **独立验证**：本快照尚未取得 verifier 结论（19:20 启动的后台 verifier 因内存压力未获得执行槽位、未产生输出），按本文口径标记「验证待跑」，不计为已验收。

### 上一次验证（2026-11-18 19:02，W3 report-bundle + `internal/sessionreport`，本 bundle 12 条新增，另有 colony 路由 18 条同批）

- 作用域实测：`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./internal/sessionreport/... ./internal/server/... -count=1` 全绿；`gofmt -l .` 无输出；`go vet ./...` 无输出；`go build ./...` 通过。
- `check_parity.sh`：本快照 **2150/2150、0 missing**；`check_parity_targets.sh`：**2007 refs、0 missing files/tests、44 shorthand/prose**。
- 本 bundle 新增：`internal/sessionreport/session_report.go`（346 行）与 `sessionreport_test.go`（338 行，8 条豁免）；`internal/server/routes_session_report.go` 与 `routes_session_report_test.go`（246 行，4 条豁免：目录缺失 404、marker+脱敏归档+stats、`queen_resume_from`/`queen_dir` 解析、坏体与未知 severity 回退）；测试 2120→**2150**；纯 `Parity-exempt` 块 307→**337**，纯 `.py` 引用块维持 **1,812**。
- 代码规模（本快照，排除并发写入的 `internal/agents`）：`internal/server` 32 个实现文件 / 8,577 行 / 248 条测试，27 个测试文件 / 9,104 行；`internal/sessionreport` 1 个实现文件 / 346 行 / 8 条测试，1 个测试文件 / 338 行。
- **当时未完成/待复核**：树级 `make test` 曾被 W3 agents 车道的 `internal/agents`（`TestPythonTitleMatchesTitleCasing`：`pythonTitle("my_agent") = "My_Agent", want "My Agent"`）与 W4 create-session 的在途改动阻塞；该问题已在 19:19 快照前修复并整树复跑 `make ci`（退出码 0），本节保留为历史记录。

### 上一次验证（2026-11-18 18:40，会话实时/附件路由组 W2 + 注释清理，本 bundle 14 条新增，合计 19 条）

- `make ci PY_ROOT=/Users/mac/hive/core`（`CGO_ENABLED=0`，离线）：`fmt-check`、`vet`、`test` 全绿；`cross` 六目标（darwin/linux/windows × amd64/arm64）全部编译通过并产出 `dist/hive-*`。
- `check_parity.sh`：**2120/2120、0 missing**；`check_parity_targets.sh`：**2007 refs、0 missing files/tests、44 shorthand/prose**。
- 本 bundle 新增：`internal/server/routes_session_live.go`（会话快照投影 + `/api/sessions/live` SSE 帧）与 `routes_session_attachment.go`（附件上传/回读：MIME 预览表、内容类型强制扩展名、CSV 与 64 KiB 文本预览、候选目录与 colony 根推导）；`internal/colony/runtime.go` 增 `ActiveWorkerCount`（QUEUED/PENDING/RUNNING）；`internal/attachments/` 共享文件名净化工具。测试 14 条：`routes_session_live_test.go` 2、`routes_session_attachment_test.go` 6、`attachments_test.go` 5、`active_worker_count_test.go` 1；上游两个路由模块与 `attachments.py` 均无 Python 测试，故全部 `Parity-exempt` 并写明契约。加上 `internal/runtimehealth`（3 条）与 colony `started_at`（+2，净 +1），主树 2101→**2120**，refs 2004→**2007**；仅含 `Parity-exempt` 的注释块 290→**307**（含豁免内容的块共 308，其中 1 个与 `.py` 引用并存）、仅含 `.py` 引用块 1,810→**1,812**。
- 注释清理：全库删除非注解注释（4,604 + 820 + 3 个区间），`gofmt -l` 归零，编译指令保留；随后重新实测代码规模。
- 代码规模实测（注释清理后）：210 个非测试 `.go` 文件 / 68,261 行；160 个测试文件 / 53,022 行；`internal/server` 30 个实现文件 / 8,208 行 / 226 条测试，25 个测试文件 / 8,441 行。
- **独立验证待跑**：W1/W2 两个 bundle 与注释清理的 verifier 结论返回并复核前，本节数字不视为已验收。

### 上一次验证（2026-11-18 18:13，会话读写路由组 W1，13 条新增）

- `make ci PY_ROOT=/Users/mac/hive/core`（`CGO_ENABLED=0`，离线）：`fmt-check`、`vet`、`test` 全绿；`cross` 六目标（darwin/linux/windows × amd64/arm64）全部编译通过并产出 `dist/hive-*`。
- `check_parity.sh`：**2101/2101、0 missing**（`check_parity: OK — 2101 个测试全部带有 parity 注解或豁免说明。`）；`check_parity_targets.sh`：**2004 refs、0 missing files/tests、43 shorthand/prose**。
- 本轮新增：`internal/server/routes_session_history.go`（262 行，六条路由）与 `routes_session_history_test.go`（441 行，10 条：1 上游对等 + 9 豁免）；`internal/session/cold.go` 配套新增 2 条豁免测试；`session_dirs_test.go` 追加 1 条豁免；`internal/platform/open.go`（39 行）落地。主树由 2088 增至 **2101**，refs 由 2003 增至 **2004**；豁免块 278→**290**、纯 `.py` 引用块 1,809→**1,810**。
- 代码规模实测（本轮重测）：205 个非测试 `.go` 文件 / 76,217 行；154 个测试文件 / 54,596 行；`internal/server` 27 个实现文件 / 9,019 行 / 216 条测试，22 个测试文件 / 8,333 行。
- **独立验证待跑**：本节的数字来自实现方的门禁运行，verifier PASS 之前不得视为已验收。

### 上一次验证（2026-11-18，触发器路由组与运行时，29 条新增）

- `go vet ./...`、`gofmt -l .` 干净；`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./... -count=1` 26 个含测试的包全绿（仅 `cmd/hive`、`cmd/hive-cred`、`internal/version` 无测试文件；`internal/platform`、`internal/sentinel` 目前仍是空目录）。
- `make cross`（`CGO_ENABLED=0`）：darwin/linux/windows × amd64/arm64 六个目标全部编译通过并产出 `dist/hive-*` 二进制。
- `check_parity.sh`：2088/2088、0 missing；`check_parity_targets.sh`：2003 refs、0 missing files/tests、43 shorthand/prose。
- 本轮新增：`internal/server/routes_session_triggers.go`（586 行，六条触发器路由）、`trigger_runtime.go`（708 行，timer/webhook/火统计/resolve_missed/slugify/next-due 运行时）、`webhook_server.go`（270 行，内嵌监听器）与 `routes_session_triggers_test.go`（1,409 行，29 条：8 上游对等 + 21 豁免）。主树由 2059 增至 **2088**，refs 由 1995 增至 **2003**；豁免块 278、纯 `.py` 引用块 1,809。
- 代码规模实测（本轮重测）：203 个非测试 `.go` 文件 / 75,775 行；153 个测试文件 / 54,051 行；`internal/server` 26 个实现文件 / 8,687 行 / 205 条测试，21 个测试文件 / 7,857 行。

### 上一次验证（2026-11-18 17:07，会话详情四路由，9 条新增）

- `go vet ./...`、`gofmt -l .` 干净；`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./... -count=1` 26 个含测试的包全绿（仅 `cmd/hive`、`cmd/hive-cred`、`internal/version` 无测试文件；`internal/platform`、`internal/sentinel` 目前仍是空目录）。
- `make cross`（`CGO_ENABLED=0`）：darwin/linux/windows × amd64/arm64 六个目标全部编译通过并产出 `dist/hive-*` 二进制。
- `check_parity.sh`：2059/2059、0 missing；`check_parity_targets.sh`：1995 refs、0 missing files/tests、43 shorthand/prose。
- 本轮新增：`internal/server/routes_session_detail.go`（254 行，四条会话详情路由）与 `routes_session_detail_test.go`（327 行，9 条：5 上游对等 + 4 豁免）；`session.go` 增触发器视图状态与加载标志，`routes_sessions.go`/`app.go` 完成 `Manager` 接线。主树由 2050 增至 **2059**，refs 由 1990 增至 **1995**；豁免块 257、纯 `.py` 引用块 1,801。
- 代码规模实测：200 个非测试 `.go` 文件 / 74,006 行；152 个测试文件 / 52,659 行；`internal/server` 23 个实现文件 / 6,980 行 / 176 条测试，20 个测试文件 / 6,450 行。

### 上一次验证（2026-11-18 17:08，事件历史端点 Verifier 复审修正）

- `go vet ./...`、`gofmt -l .` 干净；`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go test ./... -count=1` 26 个含测试的包全绿（仅 `cmd/hive`、`cmd/hive-cred`、`internal/platform`、`internal/sentinel`、`internal/version` 无测试文件）。
- `check_parity.sh`：2050/2050、0 missing；`check_parity_targets.sh`：1990 refs、0 missing files/tests、43 shorthand/prose。
- 本轮修正：处理器按 Python 语义补齐四处——ASCII-only 空行过滤、`int()` 查询参数、`json.dumps` 数字尺寸估计、4 条 `test_truncate_*` 上游用例（删除假豁免）。`routes_events_history_test.go` 9→15 条（12 上游对等 + 3 豁免）；`internal/eventbus/events_history_tail_test.go` deferral 13→9 条。主树由 2044 增至 **2050**，refs 由 1986 增至 **1990**；豁免块 253、纯 `.py` 引用块 1,796。
- 代码规模实测（本轮重测）：199 个非测试 `.go` 文件 / 73,553 行；151 个测试文件 / 52,332 行；`internal/server` 22 个实现文件 / 6,527 行 / 167 条测试，19 个测试文件 / 6,123 行。
- 剩余空目录/占位：`internal/platform`（含 `term`）、`internal/sentinel`、`internal/tools/mcp`；`internal/queen` 身份档案已有 10 条直接对等测试，agent 节点与 sentinel 待开始。

### 更早一次验证（2026-11-18 16:34，`hive debugger` 命令）

- `go vet ./...`、`gofmt -l .` 干净；`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...` 通过；`go test ./... -count=1` 26 个含测试的包全绿（仅 `cmd/hive`、`cmd/hive-cred`、`internal/platform`、`internal/sentinel`、`internal/version` 无测试文件）。
- `check_parity.sh`：2035/2035、0 missing；`check_parity_targets.sh`：1978 refs、0 missing files/tests、43 shorthand/prose。
- 本轮新增：`internal/cli/debugger.go`（移植 `framework/debugger/cli.py` 的 `hive debugger`）、`debugger_test.go` 8 条 `Parity-exempt` 测试；`cli.go` 增加 `DebuggerScript`/`DebuggerExec`/`DebuggerPython` seam 与 skill↔mcp 之间的根注册；`args.go` 的 int 解析错误改用 `pyRepr`。主树由 2027 增至 2035，refs 不变（新测试无 `.py` 引用），豁免块由 242 增至 250。
- 代码规模实测（当时）：198 个非测试 `.go` 文件 / 72,847 行；150 个测试文件 / 51,740 行；`internal/cli` 7 个实现文件 / 5,039 行 / 88 条测试，7 个测试文件 / 2,562 行；CLI 汇总（`cmd/hive` + `cmd/hive-cred` + `internal/cli` + `internal/llmjudge`）10 个实现文件 / 5,333 行 / 110 条测试。
- 剩余空目录/占位：`internal/platform`（含 `term`）、`internal/sentinel`、`internal/tools/mcp`；`internal/queen` 身份档案已有 10 条直接对等测试，agent 节点与 sentinel 待开始。

### 更早一次验证（2026-11-18 16:18，`hive-cred` 独立入口 + 对等测试）

- `go vet ./...`、`gofmt -l .` 干净；`CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod go build ./...` 通过；`go test ./... -count=1` 26 个含测试的包全绿（仅 `cmd/hive`、`cmd/hive-cred`、`internal/platform`、`internal/sentinel`、`internal/version` 无测试文件）。
- `check_parity.sh`：2027/2027、0 missing；`check_parity_targets.sh`：1978 refs、0 missing files/tests、43 shorthand/prose。
- 本轮新增：`internal/cli/cred.go`（移植 `framework/credentials/cli.py` 的 `token`/`browse`/`inspect`）、`cmd/hive-cred/main.go`、`cred_test.go` 10 条全部上游对等测试；`internal/tracker/errors.go` 导出 `WriteIndentedJSON`，`internal/credentials/resolve.go` 导出 `LocalRegistryDefault` seam。主树由 2017 增至 2027，refs 由 1968 增至 1978。
- 代码规模实测（本轮重测）：197 个非测试 `.go` 文件 / 72,640 行；149 个测试文件 / 51,425 行；`internal/cli` 6 个实现文件 / 4,832 行 / 80 条测试，6 个测试文件 / 2,247 行；CLI 汇总（`cmd/hive` + `cmd/hive-cred` + `internal/cli` + `internal/llmjudge`）9 个实现文件 / 5,126 行 / 102 条测试。
- 剩余空目录/占位：`internal/platform`（含 `term`）、`internal/sentinel`、`internal/tools/mcp`；`internal/queen` 身份档案已有 10 条直接对等测试，agent 节点与 sentinel 待开始。

### 更早一次验证（2026-11-18 16:05，`hive janitor` 命令组）

- `go vet ./...`、`gofmt -l .` 干净；`go build ./...` 通过；`go test ./...` 26 个含测试的包全绿（仅 `cmd/hive`、`internal/platform`、`internal/sentinel`、`internal/version` 无测试文件）；`make cross` 六平台（darwin/linux/windows × amd64/arm64）全部编译通过。
- `check_parity.sh`：2017/2017、0 missing；`check_parity_targets.sh`：1968 refs、0 missing files/tests、43 shorthand/prose。
- 本轮新增：`internal/cli/janitor.go`（移植 `framework/maintenance/cli.py`）、`cli.go` 的 `janitorCommands()` 根注册、`janitor_test.go` 12 条 `Parity-exempt` 测试（上游 CLI 模块无单元测试）。主树由 2005 增至 2017，refs 不变（新测试无 `.py` 引用）。
- 代码规模实测：190 个非测试 `.go` 文件 / 70,836 行；148 个测试文件 / 50,974 行；`internal/cli` 5 个实现文件 / 4,422 行 / 70 条测试，5 个测试文件 / 1,796 行。

### 更早一次验证（2026-11-18 15:29，`hive mcp` 根入口接线）

- `go vet ./...`、`gofmt -l .` 干净；`go build ./...` 通过；`go test ./...` 26 个含测试的包全绿。
- `check_parity.sh`：2005/2005、0 missing；`check_parity_targets.sh`：1968 refs、0 missing files/tests、43 shorthand/prose。
- 该轮新增：`cli.go` 的 `MCPMain` seam 与 `mcpCommands()` 根注册、`args.go` 的 `Passthrough` 整段透传、`mcp_test.go` 6 条测试（3 对等 + 3 豁免）。主树由 1999 增至 2005，refs 由 1965 增至 1968。
- 代码规模实测：189 个非测试 `.go` 文件 / 70,437 行；147 个测试文件 / 50,664 行；`internal/queen` 1 个实现文件（另有内嵌 YAML）/ 404 行 / 10 条测试，测试文件 174 行；`internal/cli` 4 个实现文件 / 4,023 行 / 58 条测试，4 个测试文件 / 1,486 行；`internal/llmjudge` 1 个实现文件 / 266 行 / 22 条测试，`internal/server` 21 个实现文件 / 5,821 行 / 152 条测试，`internal/session` 3 个实现文件 / 1,087 行 / 15 条测试，`cmd/hive` 14 行。

### 更早一次验证（2026-11-18 15:35，queen 默认档案对等测试）

- 当时 `go test ./...` 26 个含测试的包全绿。
- `check_parity.sh`：1999/1999、0 missing；`check_parity_targets.sh`：1965 refs、0 missing files/tests、43 shorthand/prose。
- 该轮新增：`internal/queen/profiles_test.go`（10 条上游对等）与 `profiles.go` 的 `DefaultSkillsFor`。主树由 1989 增至 1999，refs 由 1955 增至 1965。
- 代码规模实测：189 个非测试 `.go` 文件 / 70,393 行；146 个测试文件 / 50,536 行。

### 更早一次验证（2026-11-18 15:10，会话列表路由 + TTY 语义 + 快照校正）

- 当时 `go test ./...` 25 个含测试的包全绿（`cmd/hive`、`internal/queen`、`internal/version` 无测试文件）。
- `check_parity.sh`：1989/1989、0 missing；`check_parity_targets.sh`：1955 refs、0 missing files/tests、43 shorthand/prose。
- 该轮新增：`internal/server/routes_sessions.go` + `routes_sessions_test.go`（6 条：2 对等 + 4 豁免）；`internal/server/session.go` 增加 `WorkerPath` 与插入序 `SessionManager`；`internal/cli` 的 `stdinIsTTY()` 换用 `mattn/go-isatty` 并新增 3 条测试；`loader_test.go` 4 条、`cold_test.go` 2 条为账本补记。主树由 1974 增至 1989，refs 由 1952 增至 1955。
- 代码规模实测：189 个非测试 `.go` 文件 / 70,375 行；145 个测试文件 / 50,362 行。

## 5. 问题与反馈

- 已解决：worktree 因 `go_hive/` 未跟踪而初始为空 → 每个 lane 创建后先 `rsync` 主树再派工，合并用文件回拷贝，不产生提交。
- 已解决：子代理误领任务 #6/#7 → 已收回为 pending 并重新指派给专用 lane。
- 已解决：parity 门禁 86 个未注解测试 → 当时 664/664、0 missing（Lane H）；storage lane 合并后 752/752、740 refs；Lane G/I 合并后 913/913、897 refs。
- 已解决：`check_parity_targets.sh` 的 `xargs: unterminated quote` → 含撇号的 prose 引用会让一条引用被静默丢弃；改用 `printf`+`sed` 去空白后恢复为 897 refs / 39 prose。
- 风险：Python dict 插入序 → Go 统一用有序切片 + `KeyMap` 复刻。
- 风险：Fernet 双向兼容已由交叉测试锁定。
- 风险：`internal/storage` 与 `internal/session` 职责重叠（会话/检查点各有一套 API），已并行保留；下个里程碑需确定 canonical 实现并让调用方收敛，避免长期双写。
- 已解决：`stdinIsTTY()` 原先用 `os.ModeCharDevice` 判定，会把 `/dev/null` 误报为终端，与 Python `sys.stdin.isatty()` 的 ioctl 语义不符 → 改用 `github.com/mattn/go-isatty`（`IsTerminal || IsCygwinTerminal`），并新增 3 条测试钉住非交互路径。
- 用户反馈（2026-11-18 14:22）：`STATUS.md` 未按要求同步更新 → 已补齐本轮完成条目、`最新一次验证` 标题与时间戳、团队状态与反馈记录；同时纠正 `internal/queen` 仍被写成「目录为空」的过期描述，并重新实测全部数字。此后每轮门禁后都同步刷新本文件与 11/14/15/README/00 的计数。

## 6. 团队状态

- 主执行代理：合并、集成、跑门禁、维护本面板。本轮直接落地 `hive debugger` 命令（`debugger.go`、`DebuggerScript`/`DebuggerExec` seam、skill↔mcp 之间的根注册、`debugger_test.go` 8 条）；上一轮落地 `hive-cred` 独立入口（`cred.go`、`cmd/hive-cred`、`cred_test.go` 10 条）；再上一轮为 `hive janitor` 命令组与 `hive mcp` 根入口接线。
- 并行 lane（worktree）：Lane F 工具注册（52 测试）、Lane K llm 核心（107 测试）、Lane L playbook（32 测试）均已合并主树并转绿；Lane G AgentLoop 核心、Lane I tasks、Lane J storage/schemas/globaldb、Lane H parity 注解均已完成。CLI 子系统由主执行代理直接落地（本轮后 110 条测试：cli 88 + llmjudge 22）；会话列表路由与 TTY 语义修复同由主执行代理完成。
- 下一批候选：剩余 SessionManager 路由（`GET/DELETE /api/sessions/{id}`、`/stats`、triggers、`/events/history`）、credentials 根入口绑定、colony/worker 编排闭环、server 服务装配、purego 终端层。
- 验证子代理：`hive-cred` 独立复核已回填 **PASS**（8 项检查，见上一轮完成条目）；`hive debugger` 的独立复核已派发，结论回填本节。
- 看门狗：每 5 分钟自动汇报；里程碑（测试转绿/失败、合并、阻塞）主动汇报。
