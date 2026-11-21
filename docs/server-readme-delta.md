# 上游 server/README 对照清单（graph 时代文档债）

> 对象：`core/framework/server/README.md`（324 行，上游 Python server 的 README）。
> 对照 A：现役 Python 代码 `core/framework/server`（本工作树，基线 main `52672d1c`）。
> 对照 B：现役 Go 路由 `go_hive/go-hive/internal/server`。
> 只记录文档债与覆盖关系，**不修改 Python 源**；所有数字来自 §附录 的实测命令。

## 0. 结论

- README 是 **graph 时代** 的文档：它把「图（graph）」当会话的可加载/卸载附件，执行控制是 trigger/inject/replay，检查面是 graphs/nodes/edges/logs。
- 现役 Python **已没有**这些路由：`/api/sessions/{session_id}/graph` 在 `core/framework/server/*.py` 中 0 命中；`create_session_with_worker_graph` / `load_graph` / `unload_graph` 也 0 命中（`session_manager.py`）。Go 侧同样 0 命中 —— 这是**两侧共同的已退役面**，不是移植缺口。
- README 中仍现行的四类：会话 CRUD（`/api/sessions*`）、SSE（`/api/sessions/{id}/events`）、凭证（`/api/credentials*`）、运行时日志（`/api/runtime-logs`）。但**文件清单与行号全部漂移**：README 说 7 个文件；现役 42 个 `.py`、25 个带路由注册的文件、203 条路由注册。
- Go 对仍现行的路由有完整对应（逐条见主表）；graph 时代路由两侧都没有，无需移植。
- 一句话：**按 README 排期会写出不存在的 graph 路由；按本表排期不会。**

## 1. 主表（段落 → Python 声称 → 现役代码 → Go 对应/差异）

列说明：「时代」= `现役`（README 声称仍成立/已换实现但路由仍在）｜`漂移`（声称的部分内容过时）｜`graph 时代`（两侧现役均无）。

| # | README 段落 | Python 声称（README 原文要点） | 时代 | 现役代码（core/framework，file:line） | Go 对应/差异（go_hive/go-hive，file:line） |
| --- | --- | --- | --- | --- | --- |
| 1 | Architecture（L7-16） | 「Sessions 是主实体；Graphs 可选，可随时 load/unload 到会话」；Session = event_bus + llm + queen_executor + graph_runtime? | graph 时代 | 无 graph_runtime：会话是 queen 席位（DM）/colony 绑定；`session_manager.py:220`（class Session）、`:224`（`event_bus` 字段）、`:544-559`（EventBus 构造）、`:649 create_session`、`:1205 load_colony` | 同模型、无 graph 层：Go 会话生命周期 `internal/server/routes_sessions.go:45/52/75/78`；colony 绑定 `routes_sessions.go:114`、卸载走 `:61`（两段 DELETE）+ `:68`（colony 分支） |
| 2 | Structure 树（L18-31） | 7 个文件：`app.py`、`session_manager.py`、`sse.py`、`routes_{sessions,execution,events,credentials}.py`、`tests/test_api.py` | 漂移 | 42 个 `.py`；25 个文件含 `router.add_*`；203 条路由注册；`app.py:1096-1119` 导入 24 个 route 模块、`:1121-1157` 逐个注册（含 `channels:1140`、`turn_context:1143`；commercial 可选 `:1180`） | `internal/server` 下 221 处 `mux.HandleFunc`（非测试文件）；路由分文件更细（`routes_session_create.go`、`chat_routes.go` 等） |
| 3 | `app.py` 组件（L35-42） | `create_app(model)`；CORS（localhost）、error middleware、静态托管 + SPA fallback、优雅停机 | 漂移 | `app.py:920 create_app`；中间件链 `:942`（access_log/desktop_auth/no_cache/error）；SPA `:1234`；停机 `:478-488`。**CORS 已不在桌面中间件链**（`:204` 定义仍保留；`:936-940` 注释：桌面走 IPC，无浏览器 origin） | `internal/server/app.go:151 NewApp`、`:174` mux 装配；鉴权/日志中间件为 Go 自有实现 |
| 4 | `session_manager.py` 方法（L44-57） | `create_session()`、`create_session_with_worker_graph()`、`load_graph()`、`unload_graph()`、`stop_session()`；Queen 常驻 + Worker 执行 graph + Judge 后台健康监控 | graph 时代（部分） | `create_session:649`、`load_colony:1205`、`unload_colony:1248`、`stop_session:1291`；`create_session_with_worker_graph`/`load_graph`/`unload_graph` **0 命中**；Queen 常驻 `_start_queen:1440`，worker 执行 `_load_worker_core:981` | `routes_sessions.go:52`（创建）、`:114`（加载 colony）、`:61/:68`（卸载 colony）、`:78`（停止）。Go 无 graph 会话方法 |
| 5 | 三会话模型（L54-57） | 1) Queen 常驻交互执行器 2) Worker=`AgentRuntime` 执行 graphs 3) Judge 定时后台健康监控（graph 加载时激活） | graph 时代（部分） | Queen/Worker 仍成立（`_start_queen:1440`、`_load_worker_core:981`）；**无会话级 Judge**：`judge.py` 是「judge slot 的逐问题概率评委」（`judge.py:1-10` 自述；`stall_check.py:26/60`、`channels.py:123-125` 消费），不做后台健康监控 | Go 的 judge/goal 语义见 `go_hive/docs/queen-worker-colony-parity-audit.html` #7；无会话级 judge 后台循环 |
| 6 | Discovery（L67-73） | `GET /api/discover`：按 category 分组返回 agents 元数据 | 现役 | `routes_sessions.py:3197` | `routes_agents.go:27` |
| 7 | Session Lifecycle（L75-106） | `POST/GET /api/sessions`、`GET/DELETE /api/sessions/{id}`；创建支持 queen-only 或 agent_path+graph（一步建图） | 漂移 | 路由 `routes_sessions.py:3202/3203/3209/3210`；**创建体已换**：`colony_id`/`session_id`/`model`/`initial_prompt`/`initial_phase`/`queen_resume_from`/`queen_name`/`source_session_id`（`handle_create_session:113-140`），无 `agent_path`/`agent_id` | `routes_sessions.go:52`（POST）、`:45`（GET list）、`:75`（GET detail）、`:78`（DELETE） |
| 8 | Graph Lifecycle（L108-126） | `POST/DELETE /api/sessions/{id}/graph`（加载/卸载 graph） | graph 时代 | **0 命中**（`*.py`） | **0 命中**（`internal/server/*.go`） |
| 9 | Execution Control（L128-191） | `trigger`、`inject`、`chat`、`stop`、`pause`（stop 别名）、`resume`、`replay`、`goal-progress` | graph 时代（仅 chat 段现役） | 现役仅 7 条：`chat`/`queen-context`/`record-message`/`cancel-queen`/`presence`/`mark-colony-spawned`/`compact-and-fork`（`routes_execution.py:2338-2350`）；trigger/inject/pause/resume/replay/goal-progress **0 命中** | `routes_execution.go:49-64`（同 7 条）+ `chat_routes.go:33`（chat）；graph 执行控制 0 命中 |
| 10 | SSE Event Streaming（L193-206） | `GET /api/sessions/{id}/events`；15s keepalive；默认事件表含 `WORKER_GRAPH_LOADED` 与 `NODE_LOOP_*` | 漂移 | `routes_events.py:604`；keepalive `KEEPALIVE_INTERVAL = 15.0`（`:97`）；默认事件 `:22` 起（`NODE_*` 仍在；`WORKER_COLONY_LOADED` 在 `:59`，定义 `core/framework/host/event_bus.py:238`） | `chat_routes.go:36`（会话 SSE）、`routes_events.go:70`（`/api/events/global`） |
| 11 | Session Info（L208-214） | `stats`、`entry-points`、`graphs` | graph 时代（stats 现役） | `stats` `routes_sessions.py:3247`；`entry-points`/`graphs` **0 命中** | `stats` `routes_sessions.go:84`；entry-points/graphs 0 命中 |
| 12 | Graph & Node Inspection（L216-229） | nodes/edges/criteria/tools/node-progress 共 4 组路由 | graph 时代 | **0 命中** | **0 命中** |
| 13 | Logs（L231-252） | 会话级 logs + node-scoped logs（`summary/details/tools` 三级） | graph 时代（仅 runtime-logs 现役） | 现役只有 `PATH = "/api/runtime-logs"`（`routes_logs.py:18`，注册 `:32`）；会话/node 日志路由 0 命中 | `routes_runtime_logs.go:170`（`GET /api/runtime-logs`）；node 日志 0 命中 |
| 14 | Credentials（L254-299） | 5 条：GET/POST `/api/credentials`、GET/DELETE `/api/credentials/{id}`、POST `check-agent`；两阶段校验 | 现役（已扩） | 12 条（`routes_credentials.py:926-942`）：local add/delete + specs/oauth-status/check-agent/resync/validate-key + GET/POST `/api/credentials` + GET/DELETE `/{id}` + 会话级 credential-form | `routes_credentials.go:74-85`（specs/oauth/resync/validate/list/save/get/delete）、`routes_agents.go:36`（check-agent） |
| 15 | Key Patterns（L301-308） | session-primary；`request.app["manager"]`；`safe_path_segment()` 防路径穿越；每客户端 1000 事件缓冲 + 15s keepalive；EventBus 共享；凭证不回传 secret | 现役 | `safe_path_segment` `app.py:150`；session-primary 与 EventBus 共享同左（`session_manager.py:224/544`）；SSE 缓冲 `asyncio.Queue(maxsize=1000)`（`routes_events.py:246`）；15s keepalive `routes_events.py:97` | Go 同构（session-primary 路由 + 自己的 path sanitizer）；凭证响应不含 secret（`routes_credentials.go` handler 面） |
| 16 | Storage Paths（L310-318） | `~/.hive/queen/session/{id}`、`judge/session/{id}`、`agents/{agent}/sessions/`、`credentials/` | 漂移 | `queens/<queen>/sessions/<id>`（`core/framework/config.py:110`）、colony 树 `colonies/<c>/queens/<q>/sessions/<id>`（`:170`）、worker `colonies/<c>/workers/...`（`:184`）、`~/.hive/credentials`（`credentials/store.py:49`）；`judge/session` **0 命中** | 同布局（`routes_colonies_import.go:945/964` 读写 `queens/<queen>/sessions`；`compaction_status.go:114`）；无 judge 目录 |
| 17 | Running Tests（L320-324） | `pytest framework/server/tests/ -v`（单个 `test_api.py`） | 漂移 | `core/framework/server/tests/` 下 30 个 `test_*.py`（含 `test_api.py`） | `go test ./internal/server/...`（221 个 handler 所在包） |

## 2. graph 时代路由清单（两侧现役均 0，不要再排期）

| 路由 | README 位置 | 现役 Python | 现役 Go |
| --- | --- | --- | --- |
| `POST/DELETE /api/sessions/{id}/graph` | L110-125 | 0 | 0 |
| `POST /api/sessions/{id}/trigger` | L132 | 0 | 0 |
| `POST /api/sessions/{id}/inject` | L133 | 0 | 0 |
| `POST /api/sessions/{id}/stop`、`/pause` | L135-136 | 0（现役为 `cancel-queen`） | 0（`cancel-queen`：`routes_execution.go:55`） |
| `POST /api/sessions/{id}/resume`、`/replay` | L137-138 | 0 | 0 |
| `GET /api/sessions/{id}/goal-progress` | L139 | 0 | 0 |
| `GET /api/sessions/{id}/entry-points`、`/graphs` | L213-214 | 0 | 0 |
| `GET .../graphs/{gid}/nodes[...]`（4 组检查路由） | L220-223 | 0 | 0 |
| 会话级 `GET /api/sessions/{id}/logs`、node `.../logs` | L235-236 | 0（只有 `/api/runtime-logs`） | 0 |
| 事件 `WORKER_GRAPH_LOADED` | L206 | 0（`WORKER_COLONY_LOADED`，`host/event_bus.py:238`） | 事件枚举见 Go 侧 eventbus/SSE 映射 |

判定口径：以上每一项都在现役两侧做过 grep（见附录 A3/A4），不是「暂未找到」而是**路由已不存在**；README 对应的「graph 执行引擎」在 Go 侧同样是 out_of_scope（`contracts/module-inventory.json` 的 6 条 graph-host 条目）。

## 附录：实测命令与输出

命令均在仓库根执行（`cd <repo root>`；本工作树为 `.wt/audit-docs`）。

```text
# A1 现役 Python 路由注册总数与文件数
$ grep -rc 'router.add_' core/framework/server/*.py | awk -F: '{s+=$2} END {print s}'
203
$ grep -l 'router.add_' core/framework/server/*.py | wc -l
25

# A2 现役 Python / Go 的 graph 路由命中数
$ grep -rn 'sessions/{session_id}/graph' core/framework/server/*.py | wc -l
0
$ grep -rn 'sessions/{session_id}/graph' go_hive/go-hive/internal/server/*.go | wc -l
0

# A3 graph 时代 session_manager 方法
$ grep -rn 'create_session_with_worker_graph\|def load_graph\|def unload_graph' core/framework/server/session_manager.py | wc -l
0

# A4 目录规模
$ ls core/framework/server/*.py | wc -l
42
$ ls core/framework/server/tests/test_*.py | wc -l
30
$ grep -rn 'mux.HandleFunc' go_hive/go-hive/internal/server | wc -l
221

# A5 存储/判官路径漂移
$ grep -rn 'judge/session' core/framework --include='*.py' | wc -l
0

# A6 当前会话创建体字段（README 的 agent_path+graph 模式已无）
$ grep -n 'initial_prompt\|queen_resume_from\|source_session_id' core/framework/server/routes_sessions.py | head -3
121:        "initial_prompt": "..." (optional — first user message for the queen),
123:        "queen_resume_from": "..." (optional — resume from this session's queen),
125:        "source_session_id": "..." (optional — when paired with colony_id and
```

核对日期：2026-11-22（CST）；基线 main `52672d1c`。
