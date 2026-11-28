# API / SSE 帧级 canonical golden

> 状态：**已实测**（2026-11-21；REST 值级 golden 已扩到 107 端点 / 41 失败路径，
> 未知路径 fallback 双跑记录为待定口径；11 条 GET 端点显式跳过并附原因）
> 产物：
> - `go_hive/contracts/sse-frames/`（reference/target 帧级 golden + `diff.json` + `index.json` + `summary.txt`）
> - `go_hive/contracts/api-responses/value-golden.json`（66 成功端点 + 41 失败路径的值级差异账本）
> - `go_hive/contracts/api-responses/index.json`（端点清单 + `skipped` 显式跳过清单）
> - `go_hive/contracts/api-responses/fallback-probe.json`（未知路径 fallback 双跑证据，不计入比较）
> - `go_hive/docs/api-contract-diff.md`（形状/语义差异报告，含 fallback 小节）
> 生成 / 校验：
> ```bash
> uv run python go_hive/scripts/sse_frames_canonical.py            # 写入帧级 golden
> uv run python go_hive/scripts/sse_frames_canonical.py --check    # 重放校验（不起服务、不触网）
> uv run python go_hive/scripts/sse_frames_canonical.py --refresh  # 双跑探针 + 重建（先删旧 Go 二进制）
> uv run python go_hive/scripts/api_response_values.py            # 写入 REST 值级账本
> uv run python go_hive/scripts/api_response_values.py --check     # 重放校验
> ```
> 关联：`docs/sse-payload-parity.md`（探针剧本 + 逐类型字段矩阵）、
> `docs/api-contract-diff.md`（形状 / 语义差）、外观登记册 `sse` 与 `api-routes` 维度。

## 1. 口径与覆盖

此前 API 侧只比较「单个响应体的形状」（14 端点 0 差异），SSE 侧是脚本报告（无帧级 golden）。
本页冻结的是**帧级/值级**契约：同一剧本下，两侧客户端收到的每一帧、以及全部端点的
canonical 响应值，逐条比对后写入 golden；差异只有三类——已修、已声明例外、未声明（未声明即 FAIL）。

| 覆盖 | 数量 | 说明 |
| --- | --- | --- |
| SSE canonical 帧 | reference 113 / target 148 | 同一剧本：创建 → 开场回合 → 普通消息 → ask_user → 回答 → 重连 replay → DELETE |
| SSE 比较帧（去文档化变换后） | live 34/34、replay 17/17、history 42/42、stop 18/17 | 变换：丢弃 Go-only 每回合信封 36 帧 + 异步 title 位置不参与顺序 |
| REST 端点 | 107/107（全部 PASS；46 条值级已声明差异，0 端点级，0 FAIL） | 66 成功路径 + 41 失败路径；第一批 17 端点 + 第二批 60 条 + 第三批 30 条探针 |
| REST 值路径 | 8716 条 | 0 未声明差异；46 条已声明差异（0 端点级折叠；`session_live_tools` 逐值对照） |
| REST 显式跳过 | 11 条 | SSE 流 / 进程内部读数 / colony 运行时缺口 / SPA fallback；见 `index.json.skipped` 与 §6.6 |
| 未知路径 fallback | 3 路径 × 2 侧 | Python SPA 200 HTML / Go 404；仅证据，不计入比较（§6.4） |
| SSE 例外 | 9 条 | 见 `contracts/sse-frames/diff.json.exceptions` |
| REST 例外规则 | 10 条（0 端点级） | 见 `value-golden.json.exceptions`（§6.3） |

## 2. SSE canonicalizer（`go_hive/scripts/sse_frames_canonical.py`）

探针 `sse_payload_parity.py` 产出 `docs/e2e-assets/sse-payload-parity/capture-{reference,target}.json`；
canonicalizer 把每一帧稳定化为 golden 帧：

- **占位**：`seq` / `timestamp` / `execution_id` / `correlation_id` / `session_id` / `tool_use_id` /
  `run_id` / 所有 `*_at` 时间键 / 时长（`*_ms`）等易变值 → `<键名>`，**存在性与类型保留**；
- **保留**：事件类型与顺序、字段集合、信封字段、枚举值（`stop_reason`/`park_reason`/`activity`/`trigger`/
  `judge_type`…）、消息内容；数字按字面保留（`0.0` 与 `0` 可区分，防止表示回退）；
- 帧形如：`{"stream","index","event","type","envelope":{…},"data":{…}}`（`sort_keys` 落盘）。

比较前施加**文档化变换**（记录在 `diff.json.transforms` 与 `index.json.transforms`）：

1. **Go-only 每回合 loop 信封**：丢弃 target 侧出现在首个 `client_input_received` 之后的
   `execution_started/_completed/_failed/_paused`、`node_loop_started/_completed` 帧；
   会话开始块（在首个 client input 之前）两侧都保留。这就是把
   「剔除 Go-only execution 信封后一致」固化成 golden 断言的规则。
2. **`session_title_updated` 异步位置**：双侧都是后台任务，位置不属契约；按多重集比较
   （类型 + 载荷），位置仅记录（本次 reference 落在 live#11/history#17，target 落在 live#17/history#23）。
3. **已声明例外**（§5）逐路径掩码为 `<exception>`，掩码后若仍不相等即未声明差异。

`--check` 从已提交 capture 在内存中重放整个 canonicalize + 比较流程：逐字节比对
`reference/target/*.json`、`diff.json`、`index.json`（忽略 `generated_at`），并重跑全部断言；
**不需要起服务、不触外网**。`--refresh` 先删除 `dist/hive-sse-parity` 再跑探针，避免复用旧二进制。

## 3. golden 断言（本次结果）

| 断言 | 结果 | 证据 |
| --- | --- | --- |
| `live-core-equal` | PASS | 34/34 帧逐帧一致（类型 + 信封 + 数据） |
| `replay-core-equal` | PASS | 17/17 帧逐帧一致 |
| `history-core-equal` | PASS | 42/42 帧逐帧一致（history 在 DELETE 之前抓取，见 §4.3） |
| `replay-first-frame-snapshot` | PASS | 两侧首帧均为合成 `session_snapshot` |
| `replay-subsequence-of-history-{side}` | PASS | replay 是 history 的有序子序列（允许被抑制项跳过） |
| `judge-verdicts-equal` | PASS | 两侧 history 各 3 条 `judge_verdict{RETRY,"",implicit,0/1/2}`，live/replay 均不可见 |
| `async-title-multiset-equal` | PASS | `session_title_updated` 载荷/数量一致，位置非契约 |
| `exceptions-observed` | PASS | 9 条 SSE 例外全部在 golden 观测到（陈旧例外会 FAIL；`loop-max-iterations` 与 `system-prefix-sha` 已随本车道收敛退役） |
| `stop-tail-informational` | 记录 | DELETE 停机尾巴：reference 永久 loop 可能多发一帧 `loop_state_changed`，Go 的每回合 loop 已收口；帧已存 golden，顺序不作断言 |

## 4. 本车道修复（可修项 → 已修）

### 4.1 `context_usage_updated` 的工具批触发点（补齐 2 帧/回合）

参考在工具批里每个工具结果落库后发 `trigger=post_tool_call`，批尾再发一次
`trigger=post_tool_results`（`agent_loop.py::_execute_impl`）；Go 此前只有 `iteration_start`。
现在 Go 的 `storeToolBatch` 在每次 `AddToolResult` 后按调用序发 `tool_call_completed →
context_usage_updated(post_tool_call)`，批尾发 `context_usage_updated(post_tool_results)`，
再发该次迭代的 `llm_turn_complete`。观测值随之对齐：`message_count` 由 5 → 7 → 7 →
（下一迭代）8，与参考相同；剩余差异只有开场读数（参考有 1 条种子消息，Go 为 0，见 §5）。
回归测试：`internal/server/sse_payload_parity_test.go::TestToolBatchPublishesPostToolContextReadouts`
（`// Parity:` 指向 `_execute_impl` 与 `publish_context_usage`）。

### 4.2 `cost_usd` 数字表示（`0` vs `0.0`）——已修

`eventbus.EmitLLMTurnComplete` 现在用 `pythonFloat` 把 float 按 Python 的口径渲染
（整数浮点补 `.0`，必要时回退定点表示），`credits` 同理；`0` 与 `0.0` 的最后一条类型差消失。
回归测试：`internal/eventbus/event_bus_test.go::TestEmitLLMTurnComplete` 改为断言
JSON 字节（`Marshal(cost_usd) == "0.0042"`）。

### 4.3 history 抓取点前移（探针）

`DELETE /api/sessions/{id}` 的停机尾巴此前会写进 history（reference 多一帧
`loop_state_changed`），污染契约日志。探针现在**先抓 history 再停会话**，停机后的日志另存
`history_after_stop`（仅证据，不参与比较），`stop` 流单独记录为信息性差异。

### 4.4 REST 失败路径与状态码对照

`freeze_api_responses.py` 新增 3 条失败探针（§6），并在 `diff_endpoint` 里把
「状态码相等」「期望失败码」「error 文案相等」加入语义检查；当前 17/17 PASS、状态码不一致 0。

### 4.5 REST 会话挂 PhaseState 工具面（`session_live_tools` 转逐值对照）

会话注册点（`SessionManager.AddSession`）现在把 chat runtime 的 tool tier（`chatToolTier` /
`tools.ToolTierState`）接到会话的 `QueenPhaseState` 上，等价于上游「Queen phase state 自带工具档」：
`/api/sessions/{id}/live_tools` 的 `actual_tools` / `expected_tools` / `framework_added`、
`phase_state_ready`、`mcp_tool_count_registered` 与 `diagnostic.phase_state` 全部按相位真实取值，
不再对 REST 创建会话返回空面。接线只包装既有 runtime（不重复构造），创建/预加载/恢复/分叉路径同源。

- 相位工具视图：`internal/server/phase_tool_surface.go`（`PhaseToolSurface` + `Session.attachPhaseToolSurface`）、
  `internal/server/queen_phase_state.go`（字段与转发方法）。
- lazy 语义保持：eager 仍由 24 个 always-on 名字决定（扣除 4 个相位门控）；searchable 按可行
  的 per-queen allowlist 过滤（与上游 `_filtered_tools_for_current_phase` 同序）；平铺面的
  `collect_result` / `credentials` 仍无执行器（advertised-without-executor shim），
  `sentinel_setup` 已实装但按上游相位视图（loop 派发追加、不进 phase state）同样不计入相位工具表，
  `ask_user` 由路由作为 `framework_added` 报告。
- 回归测试：`internal/server/phase_tool_surface_test.go`（注册路径 → `/live_tools` 逐名对照、
  allowlist 过滤、会话列表相位；无 runtime 时的空面）、
  `internal/cli/loader_phase_tool_surface_test.go`（REST 创建路径 + allowlist 解析）。
- 残留（已在 §6.3 声明）：registry 侧 diagnostic 的 MCP 宇宙计数 33 vs 28（excel_* 6 个
  Go 原生实现 - csv_sql 1 个未移植；浏览器工具面已随 §4.7 跟随桥开关，不再计入）。

### 4.6 会话开场与每回合 `node_loop_started.max_iterations`（值级 → 已修）

Go 的开场块是上游永久 queen loop 的逐帧移植，只在会话开始（第一个 client input 之前）发一帧
`node_loop_started`；每个后续回合由 chat runtime 的每回合 loop 各发一帧同名事件。此前开场帧报
Go 每回合 loop 的配置上限（50），per-turn 帧也带同一个 50。w96（`22a77a52`，merge `34a274cc`）
起 `chatLoopConfig` 改用 `agentloop.QueenLoopConfig()`：开场帧由 `sessionStartMaxIterations` 报
上游永久 loop 的 bound，per-turn 帧由每回合 loop 的 `MaxIterations` 报 queen profile 的 bound；
两者默认都是 `999999`，设置 `HIVE_MAX_ITER` 时同取该 cap（与
`queen_orchestrator.py::_queen_loop` 同口径）。per-turn 帧仍属 §5 的有意每回合信封，随信封整体
剔除、不参与帧级比较；它们的值现在也是 999999，不再是旧的 50。
REST 值账本 `events[2].data.max_iterations` 由 999999/50 转为两侧 999999；SSE 帧级 golden
的开场帧同步更新。冻结的 `contracts/sse-frames/target/live.json` 仍是 w96 前的 50（该批次未重刷，
per-turn 帧在比较时整体剔除），下次 `--refresh` 会同步为 999999。回归：
`session_start_events_test.go::TestSessionStartMaxIterationsFollowsQueenLoopBound`、
`loop_config_profiles_test.go::TestQueenLoopProfilesMatchUpstreamBudgets`，
`TestSessionAddWritesOpeningEvents` 收紧为断言 999999。

### 4.7 浏览器工具面跟随桥开关（registry 计数 → 已修）

`InstallBrowserTools`（22 个 gcu 定义）改为按 `browserBridgeRequested`（`HIVE_BROWSER_BRIDGE`
env，其次 `configuration.json` 的 `gcu_enabled`，默认开）安装：桥关闭时既不注册也不登记这批
工具——它们的 executor 只会向桥 POST，没有桥就没有可执行面，上游同样 retired 了 bundled
gcu-tools server；桥开启时行为不变（延迟层 72 保持）。本冻结探针设 `HIVE_BROWSER_BRIDGE=0`：
`registry_mcp_total` 54 → 33、`mcp_tool_names_all_total` 54 → 33、`independent_tools_total`
61 → 40，回到「33 vs 28 = excel_* 6 - csv_sql 1」的既有声明（§6.3）。回归：
`browser_tools_gate_test.go::TestBrowserToolsFollowBridgeSwitch`。


## 5. 例外清单（结构化 + 值级；不进对齐目标）

SSE（`contracts/sse-frames/diff.json.exceptions`，9 条）与 REST
（`contracts/api-responses/value-golden.json.exceptions`，10 条路径规则，0 端点级折叠）的关系：

| 类别 | 例外 | 归因 |
| --- | --- | --- |
| 有意结构（应当保留） | `node_loop_iteration/llm_turn_complete/client_output_delta.iteration` 会话累计 vs 每回合；`node_loop_completed.iterations`、`execution_*` 的 `input`/`output`（随信封整体剔除） | Python 是永久 loop、Go 是每回合 bounded loop（`sse-payload-parity.md` §4.1/§4.2 既有结论）。快照 `is_executing` 与前端 spinner 依赖该信封，**不硬改**；开场与 per-turn `node_loop_started.max_iterations` 均已按各自 loop 的 bound 修复（§4.6；现同为 999999），per-turn 帧仍随信封整体剔除 |
| 值级（提示词 / 语料车道） | `llm_turn_complete.model`（litellm 前缀 `openai/…`）；`context_usage_updated` 的 `usage_ratio/usage_pct/estimated_tokens/message_count/breakdown.*`；REST 侧同路径 + `queen_tools` 24 条工具描述 | 系统提示词与工具描述文本仍在对齐（prompt / tool-description 车道）。字段集合、类型、发射点、枚举值不受影响 |
| 值级（开场结构） | `context_usage_updated.message_count` 开场 1 vs 0 | 开场块消息结构与提示词车道同源；后续读数已一致（§4.1 钉住 7/7/8） |
| 运行期内部 | REST `session_stats.event_bus.subscriptions`（9 vs 3，逐次运行可变）；`health.resources.*`（比较时忽略） | 进程内读数，不属 HTTP 契约 |
| 非契约 | `session_title_updated` 位置；`stop` 停机尾巴 | 异步任务 / 关停竞态，两侧都可能漂移 |

> 本车道退役 5 条已收敛例外规则（SSE 2 条 + REST 3 条，归并为两类路径）：`system_prefix_sha`（REST + SSE 两侧均 692b774c98c7 逐字相等）与
> `node_loop_started.max_iterations`（开场帧 999999 对齐，§4.6）。
> 这些路径回到严格比较——再发散即 FAIL。
> 未声明差异 = FAIL：`--check`（SSE / REST）都会在出现新差异或旧例外失效时退出 1。

## 6. REST 值级 golden（`api_response_values.py`）

- **输入**：`contracts/api-responses/index.json` + `{reference,target}/<endpoint>.json`（canonical 载荷，已由
  `freeze_api_responses.py` 冻结；值级脚本只读，不抓取）。
- **条件采样字段**：`health.resources.avail_mb` / `resources.renderers` 只在宿主采样恰好在冻结时触发才出现，
  冻结时从 shape 中剔除（`shape_ignore`），canonical 载荷仍保留作为证据，避免快照门禁要求每次运行都出现。
- **展开规则**：递归展开为叶子路径；数组带稳定身份键（`credential_id`/`skill_id`/`tool_name`/`id`/`name`）
  时按该键排序后逐项比较（map 迭代序不算契约变化）；纯标量数组按多重集；其余按位置。
- **路径类易变值**：`location`/`path`/`base_dir`/`home`/`root`/`cwd` 等键按占位符比较（`base_dir` 于本批加入：
  Python 侧读 checkout、Go 侧内置技能物化在进程临时目录，属部署差异而非契约值）。
- **失败路径语义**：状态码 + `error` 文案逐字比较；纯文本错误体（如附件 404 的 `Attachment not found`）
  按文本比较（本次为此修正了 freeze 的非 JSON 体处理）。

### 6.1 第二批端点（60 条新探针：42 成功 + 18 失败）

覆盖范围：sessions 生命周期（delete / live_tools / history / triggers / workers / skill-installs /
tasks / title / chat / attachment / upload）、config GET/PUT、credentials 列表与元数据（仅元数据，
值用占位 `probe-dummy-value`，响应不含任何密钥值）、skills、MCP、browser panel/status、colonies、
queens、sentinel、maintenance。

| 端点 | 方法 | 成功状态 | 值路径 | 备注 |
| --- | --- | --- | --- | --- |
| `/api/sessions/history` | GET | 200 | 14 | 冷/热会话列表 |
| `/api/sessions/{session}/live_tools` | GET | 200 | 433 | 逐值对照；残留为 registry 侧 diagnostic + MCP 宇宙计数（§6.3） |
| `/api/sessions/{session}/triggers` | GET | 200 | 0 | 空列表 |
| `/api/sessions/{session}/workers` | GET | 200 | 0 | 空列表 |
| `/api/sessions/{session}/skill-installs` | GET | 200 | 0 | 空列表 |
| `/api/sessions/{session}/tasks` | GET | 200 | 15 | 探针在两侧 temp home 写入同构 `tasks.json` 后读取 |
| `/api/sessions/{session}/upload-attachment` | POST | 200 | 6 | multipart；两侧返回同名文件 + `extracted_text` |
| `/api/sessions/{session}/attachment/{file}` | GET | 200 | 5 | 下载头（Content-Type/Length/Cache-Control）+ 正文逐字 |
| `/api/sessions/{session}/title` | PATCH | 200 | 2 | 第二会话（临时）写入 |
| `/api/sessions/{session}/chat` | POST | 200 | 2 | `{"status":"queen","delivered":true}` |
| `/api/sessions/{session}`（删除后） | GET | 200 | 9 | 冷会话详情分支 |
| `/api/sessions/{session}` | DELETE | 200 | 2 | `{"session_id","stopped":true}` |
| `/api/config/features` | GET | 200 | 4 | |
| `/api/config/models` | GET | 200 | 268 | 已声明差异：整数定价 `5.0` vs `5`（NV-02） |
| `/api/config/profile` | GET | 200 | 5 | |
| `/api/config/sentinel` | GET | 200 | 0 | |
| `/api/config/rate-limits` | GET | 200 | 78 | |
| `/api/config/llm-sections` | GET | 200 | 7 | |
| `/api/config/external-skills` | GET | 200 | 8 | |
| `/api/config/provider-library` | GET | 200 | 0 | |
| `/api/config/llm` | PUT | 200 | 9 | custom 模式回写同一 model；`sessions_swapped` 已声明差异 |
| `/api/credentials` | GET | 200 | 5 | 仅元数据（id/type/key_names/时间），无密钥值 |
| `/api/credentials` | POST | 201 | 1 | 占位值写入 temp home |
| `/api/credentials/airtable_pat` | GET | 200 | 5 | 单条元数据 |
| `/api/credentials/oauth-status` | GET | 200 | 2 | 在写入凭据之前采样（空 registry） |
| `/api/sentinel/credentials` | GET | 200 | 3 | |
| `/api/skills/scopes` | GET | 200 | 2 | |
| `/api/skills/{name}` | GET | 200 | 7 | |
| `/api/queen/{id}/skills` | GET | 200 | 2732 | 195 条技能（`base_dir` 已占位） |
| `/api/mcp/servers` | GET | 200 | 50 | |
| `/api/browser/status` | GET | 200 | 3 | 探针把两侧 `HIVE_BRIDGE_PORT` 指到关闭端口避免宿主桥串扰 |
| `/api/browser/contexts` | GET | 200 | 3 | 已声明差异（无桥时空态形状，见 §6.3） |
| `/api/browser/tabs` | GET | 200 | 2 | `reason` 文案值级差异（已声明） |
| `/api/colonies/tools-index` | GET | 200 | 0 | |
| `/api/queen/removed` | GET | 200 | 0 | |
| `/api/queen/{id}/overview` | GET | 200 | 6 | |
| `/api/queen/{id}/profile` | GET | 200 | 17 | |
| `/api/queen/{id}/llm` | GET | 200 | 3 | `default_model` 经接线修复后与 reference 一致 |
| `/api/queen/{id}/persona` | GET | 200 | 18 | |
| `/api/queen/{id}/soul` | GET | 200 | 2 | |
| `/api/maintenance/janitor/report` | GET | 200 | 1 | |

### 6.1b 第三批端点（30 条新探针：10 成功/空态 + 20 失败/空对象，+38 条值路径）

覆盖范围：此前只有形状断言、没有值级断言的确定性 GET 面——空列表/空对象、固定错误信封、
无对象读取。全部只读，落在临时 home 的空态上（无 colony、无 worker、无云会话、无桥）。

| 端点 | 方法 | 状态 | 值路径 | 备注 |
| --- | --- | --- | --- | --- |
| `/api/prompts` | GET | 200 | 0 | `{"prompts":[]}`，断言空目录 |
| `/api/memories` | GET | 200 | 0 | `{"memories":[]}` |
| `/api/memories/file?path=…` | GET | 404 | 1 | `Memory not found` |
| `/api/channels` | GET | 200 | 0 | `{"channels":[]}` |
| `/api/claude-code-queens` | GET | 200 | 0 | `{"queens":[]}` |
| `/api/claude-code-queens/{q}/conversations/{s}` | GET | 404 | 1 | `No conversation '<id>' of '<queen>'` |
| `/api/discover` | GET | 200 | 0 | `{}` |
| `/api/queen-mail/queens` | GET | 401 | 1 | 无 mail 凭据：`unauthorized` |
| `/api/config/profile/avatar` | GET | 404 | 1 | `No avatar found` |
| `/api/queen/{id}/avatar` | GET | 404 | 1 | `No avatar found` |
| `/api/models/endpoints/{name}/models` | GET | 404 | 1 | `There's no endpoint '<name>'` |
| `/api/queen/{id}/skills/{skill}/body` | GET | 200 | 8 | `body` 正文 + `base_dir` 占位 + 元数据 |
| `/api/global/crm/status` | GET | 501 | 2 | `crm_unavailable`（本 build 无 CRM） |
| `/api/global/data/tables` | GET | 401 | 2 | `signed_out: true`（无云会话） |
| `/api/global/data/changes` | GET | 401 | 2 | 同上 |
| `/api/global/data/foreign-keys` | GET | 401 | 2 | 同上 |
| `/api/global/data/tables/{table}/rows` | GET | 401 | 2 | 同上 |
| `/api/crm/grid/schema` | GET | 401 | 2 | 同上 |
| `/api/crm/grid/{entity}/{id}` | GET | 401 | 2 | 同上 |
| `/api/colony/{colony}/notifications` | GET | 404 | 1 | `Colony '<id>' not found` |
| `/api/colony/{colony}/tools` | GET | 404 | 1 | 同上 |
| `/api/colonies/{colony}/sessions` | GET | 200 | 0 | `{"sessions":[]}` |
| `/api/colonies/{colony}/active-session` | GET | 200 | 1 | `{"session":null}` |
| `/api/colonies/{colony}/worker_profiles` | GET | 404 | 1 | `colony '<id>' not found` |
| `/api/colonies/{colony}/data/tables` | GET | 200 | 0 | `{"tables":[]}` |
| `/api/colonies/{colony}/data/changes?since=0` | GET | 200 | 2 | `{covered:[],cursor:0,tables:{},truncated:false}` |
| `/api/colonies/{colony}/data/tables/{table}/rows` | GET | 404 | 1 | `no tracker.db` |
| `/api/colonies/import/status/{upload_id}` | GET | 400 | 1 | `invalid upload_id` |
| `/api/sessions/{session}/workers/{worker}` | GET | 404 | 1 | `unknown worker: '<id>'` |
| `/api/sessions/{session}/workers/{worker}/conversation` | GET | 404 | 1 | `unknown worker: '<id>'` |

### 6.2 失败路径矩阵（21 条，全部 PASS）

| 端点 | 方法 | 状态 | 对照物 |
| --- | --- | --- | --- |
| `/api/sessions/session_…deadbeef` | GET | 404/404 | `Session '<id>' not found` |
| `/api/sessions`（body=`[]`） | POST | 400/400 | `Request body must be a JSON object` |
| `/api/sessions/{session}/colony/resolve_missed` | POST | 409/409 | `Colony not loaded` |
| `/api/sessions/{missing}/live_tools` | GET | 404/404 | `session '<id>' not found` |
| `/api/sessions/{missing}/tasks` | GET | 404/404 | `Task list for session … not found` + 空 tasks |
| `/api/sessions/{missing}/triggers` | GET | 404/404 | `Session '<id>' not found` |
| `/api/sessions/{missing}/workers` | GET | 404/404 | `Session '<id>' not found` |
| `/api/sessions/{missing}/skill-installs` | GET | 404/404 | `session not found` |
| `/api/sessions/{missing}/upload-attachment` | POST | 404/404 | `session not found` |
| `/api/sessions/{missing}/chat` | POST | 404/404 | `Session '<id>' not found`（本批修复 Go 文案） |
| `/api/sessions/{missing}/title` | PATCH | 404/404 | `No such queen conversation` |
| `/api/sessions/{missing}` | DELETE | 404/404 | `Session '<id>' not found` |
| `/api/sessions/{session}/attachment/nope.txt` | GET | 404/404 | 纯文本 `Attachment not found` |
| `/api/credentials/nope_probe` | GET | 404/404 | `Credential 'nope_probe' not found` |
| `/api/skills/does-not-exist-probe` | GET | 404/404 | `skill 'does-not-exist-probe' not found` |
| `/api/queen/queen_nope/overview` | GET | 404/404 | `Queen 'queen_nope' not found` |
| `/api/mcp/servers/nope/health` | POST | 404/404 | `{"code","error","what","why","fix"}` 五键错误体 |
| `/api/sentinel/telegram/validate`（空体） | POST | 400/400 | `{"error":"no token provided or saved","ok":false}` |
| `/api/credentials`（空体） | POST | 400/400 | `credential_id and keys are required` |
| `/api/config/llm`（非法 JSON） | PUT | 400/400 | `Invalid JSON body` |
| `/api/config/llm`（body=`[]`） | PUT | 500/500 | 两侧同为 500（非对象体在两侧都抛错）；**记录现状而非期望行为** |

第三批另加 20 条失败/空对象探针（§6.1b：401 无云会话 ×5、501 CRM、404 无对象 ×7、400 非法
upload_id、401 queen-mail、404 memory/avatar/models-endpoint），状态码与 `error` 文案全部逐字
一致；失败路径合计 41 条。

### 6.3 值级/形状差异（已声明，不进对齐目标）

| 例外 | 端点 | 类 | 说明 |
| --- | --- | --- | --- |
| `live-tools-registry-diagnostic` | `session_live_tools` | 已修复 | Go REST 会话已挂 `QueenToolRegistry`；`dropped_by_provider` 与参考逐值一致，仅余 `registry_mcp_total` 及派生计数（见下一行） |
| `live-tools-mcp-universe`（4 条） | `session_live_tools` | value-level | MCP 宇宙计数 33 vs 28 = 6 个 excel_*（Go 原生实现；参考侧要装可选 openpyxl 才注册）减 1 个 csv_sql（Go 未移植 DuckDB）。`independent_tools_total` 随之为 40 vs 35。22 个浏览器 gcu 定义在本探针（`HIVE_BROWSER_BRIDGE=0`）已不进 registry 计数（§4.7）；模型面真正受制的 allowlist 过滤计数一致（22 filtered / 15 allowed） |
| `config-models-pricing-float` | `config_models` | non-verbatim-numeric | 整数定价 `5.0` vs `5`（NV-02） |
| `browser-contexts-empty-state` | `browser_contexts` | empty-state-divergence | Python 读进程内 gcu 注册表（available:true/count:0）；Go 代理桥（available:false/reason） |
| `browser-tabs-reason-text` | `browser_tabs` | env-dependent-value | 同为 available:false，`reason` 文案实现相关 |
| `config-llm-put-swapped-count` | `config_llm_put` | runtime-state-gap | Python 会计入会话的 ColonyRuntime LLM（2），Go REST 会话未绑 colony runtime（1）；colony 绑定面独立对齐项 |

其它既有例外见 §5（工具描述文本、SSE model/sha、`event_bus.subscriptions` 等）。

### 6.4 未知路径 fallback（实测，已对齐）

两侧同输入探测未注册的 `/api/*` GET（证据：`contracts/api-responses/fallback-probe.json`）：

| 路径 | reference（Python） | target（Go） |
| --- | --- | --- |
| `/api/__contract_probe_unknown__` | 404 `text/plain` | 404 `text/plain`（同形） |
| `/api/models` | 404 `text/plain` | 404 `text/plain`（同形） |
| `/api/org-chart` | 404 `text/plain` | 404 `text/plain`（同形） |

- Python 的 SPA catch-all（`app.router.add_get("/{path:.*}", handle_spa)`）只在 `core/frontend/dist/index.html`
  存在时注册；没有 dist 时未注册的 `/api/*` 就是 404 文本（body 前缀 `404: Not Found`）。
- **口径已定（2026-11-21）**：Go 与上游同形——`internal/server/app.go::handleSPA` 去掉 Go 独有的
  `/api/*` JSON 404 与 asset 扩展名守卫：dist 存在时「文件在 dist 内即返回，否则返回 index.html（200）」，
  未注册 `/api/*` 也走同一条 fallback；dist 不存在时两侧都不注册 catch-all（同 404）。
  该口径由上游测试 `framework/server/tests/test_api.py::TestErrorMiddleware::test_unknown_api_route_falls_back_to_frontend`
  钉住，Go 侧回归：`TestUnknownAPIRouteFallsBackToFrontend`、`TestStaticServingAndIndexFallback`。
  本仓库无 dist 的检出里两侧同为 404；建好 `core/frontend/dist` 后两侧同为 200 index.html。
  前端 `api/*.ts` 已核对的路径不受影响（路由面 217/220）。

### 6.5 显式跳过的端点（值级不比较）

`index.json.skipped`（11 条）与 `api-contract-diff.md` 的「显式跳过的端点」表逐条记录方法、
类与原因；这些端点不是遗漏：

| 类 | 端点 | 原因 |
| --- | --- | --- |
| sse-stream | `/api/events/global`、`/api/sessions/live`、`/api/sessions/{session}/workers/stream`、`/api/browser/status/stream` | 长连接流；帧级契约由 `contracts/sse-frames/`（session_events 剧本）覆盖 |
| documented-functional-gap | `/api/health/resources` | Python 渲染器采样器形状 vs Go 资源监控 rollup，两套功能面（§8） |
| runtime-internal | `/api/startup-status`、`/api/runtime-logs` | 各自进程的启动阶段时间戳 / stdout 缓冲，逐次运行不同 |
| runtime-state-gap | `/api/sessions/{session}/colony/tools`、`/colony/skills`、`/workers/{worker_id}/reap-timeline` | Python REST 会话绑了 eager ColonyRuntime，Go 未绑（与 `config_llm_put.sessions_swapped` 同根因）；colony 绑定车道落地后再补探针 |
| covered-elsewhere | `/{path:.*}` | SPA fallback 由 `fallback-probe.json` 证据覆盖（§6.4） |

Go-only 路由（5 条）无 Python 对端，已在 `contracts/api-routes.json` 逐条带 `extension`/`annotation`
标注（`POST /api/mcp/reload`、`/api/skills/reload`、reminders 两条、`DELETE /api/sessions/{a}/{b}` alias），
不进值级账本。

### 6.6 结果

- 107/107（全部 PASS），8716 条值路径，46 条已声明差异（0 端点级折叠），0 未声明。
- 本车道把值级面从 77 端点 / 8678 路径扩到 107 端点 / 8716 路径（+30 探针 / +38 条值路径；
  10 条成功空态 + 20 条失败/空对象，全部 PASS），并给出 11 条显式跳过（§6.5）。
- 四条已知值级残差逐项实测：
  - `registry_mcp_total` 及派生计数：54/54/61 → 33/33/40（Python 28/28/35）。22 个浏览器 gcu
    定义改为跟随桥开关（§4.7），剩余 33 vs 28 = excel_* 6 - csv_sql 1（§6.3 已声明）。
  - `system_prefix_sha`：REST 与 SSE 两侧均 `692b774c98c7`（16/16 SSE 帧），逐字相等；例外退役。
  - `node_loop_started.max_iterations`：开场帧 999999 vs 50 → 两侧 999999（§4.6）；per-turn
    Go-only 信封在 w96 后同为 999999（此前 50），仍随信封整体剔除。
  - `node_loop_completed.iterations`：Go-only 每回合帧（3 个 live / 3 个 history，值 1），Python
    永久 loop 不发；保留为有意结构例外（§5），REST 值账本不覆盖该 Go-only 面。
- 本批把 `session_live_tools` 从端点级折叠（418 条）转为逐值对照：433 条路径全部参与比较，
  工具面（21 条 `actual_tools` / 32 条 `expected_tools` 含描述与状态、`framework_added`、
  `phase_state_ready`、`phase_state` 诊断半边）逐值一致；余下 4 个值差异属 `live-tools-mcp-universe` 声明族（§6.3）。
- `pythonFloat` 修复后，`session_events` / `session_events_history` 的 `cost_usd` 已从差异清单消失。
- 本批顺带修复：Go `GET /api/queen/{id}/llm` 的 `default_model` 接线（serve 未传 `DefaultModel` 钩子，
  恒为空串）；Go `POST /api/sessions/{id}/chat` 未知会话 404 文案改为与 `resolve_session` 一致的
  `Session '<id>' not found`；canonicalizer 把 `base_dir` 纳入占位键；freeze 失败语义支持纯文本错误体。

## 7. 复现与门禁

```bash
# 刷新双跑证据 + 帧级 golden（先删旧二进制；脚本会自动删）
uv run python go_hive/scripts/sse_frames_canonical.py --refresh
# 刷新 REST 形状/语义冻结 + 值级探针证据（先删旧二进制）
rm -f go_hive/go-hive/dist/hive-contract && uv run python go_hive/scripts/freeze_api_responses.py
# 写/验值级账本与帧级 golden
uv run python go_hive/scripts/api_response_values.py
uv run python go_hive/scripts/api_response_values.py --check
uv run python go_hive/scripts/sse_frames_canonical.py --check
```

- `freeze_api_responses.py` 起两个临时实例（各自 temp `HIVE_HOME`、空闲端口、本地 stub LLM），
  探针含写操作（删会话、写凭据、写配置、上传附件）全部落在临时 home；浏览器探针把两侧
  `HIVE_BRIDGE_PORT` 指到关闭端口并关掉 Go 桥 supervisor，避免宿主上的 14830 桥串扰。
- fallback 小节要求 Python 侧存在 `core/frontend/dist`（构建前端后）；没有 dist 时两侧都回 404 文本。
- 探针与 golden 均只监听 127.0.0.1、只用本地 stub，不访问外部服务；**不纳入 `make ci`**，
  按需手动跑。本车道门禁只跑受影响范围（gofmt / vet / 目标包测试 / parity），不跑全量 `make ci`。

## 8. 未覆盖 / 后续

- 真实 provider 冒烟：当前剧本与 stub 固定内容，值级差异在提示词车道收敛后需再复跑。
- 多工具批的金句序：单工具批已被 golden + 单测钉住；多工具批的 `tool_call_completed →
  post_tool_call` 交替顺序由同一段代码保证，但未纳入本剧本。
- `model` 前缀 / 工具描述等提示词语料的追平（prompt / tool-description 车道）。`system_prefix_sha`
  本车道实测两侧逐字相等（REST + SSE），例外已退役。
- **显式跳过（11 条，`index.json.skipped`，不是遗漏）**：4 条 SSE 流、`/api/health/resources`
  （两套资源面）、`/api/startup-status`、`/api/runtime-logs`、colony 运行时缺口的 3 条
  （`colony/tools`、`colony/skills`、`workers/{id}/reap-timeline`）、SPA fallback。colony 绑定车道
  落地后把 3 条 runtime-state-gap 补进值账本。
- `api.proto` 只映射 17 个端点；本批新增 30 个端点只进 shape/值账本（`check_api_proto.py` 报
  `unmapped-endpoint` 警告，0 error，不失败）。下批可批量补 proto message 或维持该口径。
- 会话运行时构造：PhaseState 工具面本批已挂（§4.5），`session_live_tools` 已转逐值对照；
  剩余为 registry 侧 admission-gate 诊断（需挂载 live queen tool registry）与 ColonyRuntime 绑定面
  （`config_llm_put.sessions_swapped`、colony 工具/技能面），对齐后可再升级为严格比较。
