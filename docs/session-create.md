# 新建 Session：CLI 与 API（queen / colony 场景）

> 2026-11-23 手写 · 依据 `core/framework/server/routes_sessions.py::handle_create_session`（:117-200）与 Go `internal/server/routes_session_create.go`、`routes_session_colony.go`、`routes_queens_sessions.go`。两侧语义一致。

## 0. 一句话

**CLI 没有“新建 session”命令；新建只能走 HTTP API（前端也是走同一套）。** CLI 的 `hive open/serve --colony` 是“加载并 resume colony 的 queen 会话”，不是通用新建。

## 1. API：`POST /api/sessions`

Body（全部可选）：

| 字段 | 类型 | 含义 |
|---|---|---|
| `colony_id` | string | 绑定/创建 colony（`<COLONIES_DIR>/<id>/`） |
| `session_id` | string | 自定义会话 id（不传则生成） |
| `queen_name` | string | 预绑定 queen profile（不存在 → 404 `Queen '<name>' not found`） |
| `queen_resume_from` | string | 从该会话 resume 同一个 queen |
| `source_session_id` | string | 与 `colony_id` 搭配：把源独立 queen 会话 fork 成新 colony（前端 “Create Colony”） |
| `initial_prompt` | string | 首条用户消息 |
| `initial_phase` | string | 独立 queen 用 `"independent"`；非法值 400（附 valid 列表） |
| `model` | string | 覆盖模型 |

路由矩阵（上游 docstring 逐条）：

| 输入 | 行为 | 结果 |
|---|---|---|
| 无 `colony_id` | **queen-only DM 会话**（独立 queen） | 201 |
| `colony_id` + 目录已存在 | 打开已有 colony（完整 AgentLoader，resume 其 queen 会话） | 201 |
| `colony_id` + 目录不存在 + `source_session_id` | **fork 成新 colony**（源独立会话浓缩为 queen seed 并锁定；重名自动去重 slug） | 201 |
| `colony_id` + 目录不存在（无 source） | **bootstrap 最小 colony**（queen 进入 colony 相位） | 201 |

响应：**201 Created**，live session dict（`session_id`、`colony_id`(无则 null)、`queen_id`、`queen_name`、`phase`、`queen_model`、`queen_supports_images` 等）。
错误：400（Invalid JSON / initial_phase / Invalid colony_id）、404（Queen not found；colony fork 源不存在）、409（会话正在加载/其它冲突）。

```bash
# A. 独立 queen DM（无 colony）
curl -s -X POST http://127.0.0.1:8788/api/sessions \
  -H 'Content-Type: application/json' -d '{}'

# B. 打开/绑定已有 colony（resume 它的 queen 会话）
curl -s -X POST http://127.0.0.1:8788/api/sessions \
  -H 'Content-Type: application/json' -d '{"colony_id":"demo"}'

# C. 新建 colony：从独立 queen 会话 fork（含初始任务）
curl -s -X POST http://127.0.0.1:8788/api/sessions \
  -H 'Content-Type: application/json' \
  -d '{"colony_id":"research-lab","source_session_id":"<独立会话id>","initial_prompt":"目标是…"}'
```

## 2. API：`POST /api/sessions/{session_id}/colony`

把**已有 colony** 加载进一个**已存在**的会话（先建 queen-only 会话、后挂 colony 的场景）：

```bash
curl -s -X POST http://127.0.0.1:8788/api/sessions/<sid>/colony \
  -H 'Content-Type: application/json' -d '{"colony_id":"demo"}'
```

`colony_id` 必填（缺失 400 `colony_id is required`；非法 400 `Invalid colony_id`）。

## 3. API：`POST /api/queen/{queen_id}/session`（面向 queen 的 get-or-create）

该 queen 的**常驻 DM 会话**：有 live DM → 直接返回 `200 {"status":"live"}`；否则 resume 最近一条非 colony 冷会话；都没有才新建。

```bash
curl -s -X POST http://127.0.0.1:8788/api/queen/<queen_id>/session \
  -H 'Content-Type: application/json' -d '{"initial_prompt":"你好"}'
```

## 4. CLI 现状（为什么不能建）

- `hive session {list,stop}` —— 只列举/停止。
- `hive colony {list,info,delete}` —— 没有 create。
- `hive queen {list,show,sessions}` —— 只读。
- `hive open [--colony X]` / `hive serve [--colony X]` —— 启动时加载 colony 并 **resume** 其 queen 会话（新 colony 首次会生成会话目录），但不是通用“新建会话”接口。
- `hive chat <session_id> <message>` —— 只能给**已存在**会话发消息。

结论：脚本化“新建 session（queen/colony）”请用上面的 HTTP API；需要 CLI 的话属于**新增能力**（上游没有），不在当前 parity 范围。