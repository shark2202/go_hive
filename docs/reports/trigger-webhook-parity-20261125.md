# Trigger / Webhook 一致性核对报告（Python Hive ↔ go_hive）

- 日期：2026-11-25
- 复核：2026-11-25 14:35 CST，基线 `5486a5fab`；P1 unload `d5540e86c`（merge `b61abc5a8`）、update 错误传播 `335bb0a38`、AI 校验 `f72af6b3e`、405 `Allow` / 空 methods `7fff8cc3b` 均已修复；`make ci` RC=0，parity 6,511
- 代码基线：`b5e0a1071`（2026-11-25 12:32 +0800）
- 审计范围：trigger 定义与持久化、cron/interval、timer 运行时、webhook HTTP、AI 工具、REST 路由、unload/stop 生命周期、重启恢复、SSE/UI 触发面
- 对照实现：`core/framework/**`、`tools/src/terminal_tools/**`（Python）↔ `go_hive/go-hive/internal/**`（Go）
- 既有证据沿用：`go_hive/docs/reports/triggers-cron-webhook-close-20261125.md`（cron 语法 / webhook 响应字节 / payload 数字类型）、`docs/triggers-webhook.html`
- 本文只补“第二轮”核对：REST、生命周期、重启恢复、校验口径；不重复已闭环的 cron/webhook wire 细节

> 说明：审计期间仓库 HEAD 从 `19282cfcf` 移动到 `b5e0a1071`；触发器相关行为文件在 `d341640b3` 之后没有新的行为变更。本报告以当前工作树代码为准。

---

## 0. 结论

**核心契约一致；第一轮发现的 P1/P2 生命周期与校验差异已于 2026-11-25 修复；剩余共同缺口与文本/SSE/真实网络边界。**

| 面 | 判定 | 说明 |
|---|---|---|
| TriggerDefinition / `triggers.json` | ✅ | 字段、路径、损坏回退、未知字段保留一致 |
| AI 工具 `set_trigger` / `list_triggers` / `remove_trigger` | ✅ | 参数、返回形状、错误文案、colony 阶段可见性一致；`set_trigger` 类型校验已对齐（P2，`f72af6b3e`） |
| cron 语法 | ✅ | Go 已对齐 croniter 6.0.0 接受面；fixture 126 表达式 / 4,911 步 / 82 拒绝语料 |
| timer 运行时 | ✅ | UTC、首触发、next-fire、last_fired/next_due、missed 数学一致 |
| webhook 核心 | ✅ | 端口 8090、path/method、202/404/405/400/401 文案、1MiB、headers/query、raw_body 一致 |
| webhook 405 `Allow` / 空 methods | ✅ | 405 带 aiohttp 风格 `Allow`（大写、排序、逗号无空格）；`methods: []` 返回 404（P3，`7fff8cc3b`） |
| 注入与事件 | ✅ 语义 | `[TRIGGER: type/id]`、批量合并、`webhook_received`/`trigger_fired` 字段一致；文本形态有差 |
| REST 路由 | ✅ 主路径 | 6 条路由两侧都有；状态码/主要错误文案一致 |
| unload / stop 生命周期 | ✅ | Go unload 清空 `AvailableTriggerDefs` 并逐条发 `trigger_removed`；restore 改为 replace（P1，`d5540e86c` / merge `b61abc5a8`） |
| 更新 trigger 的失败路径 | ✅ | Go 传播 timer/webhook 重启错误，返回 500 + `Failed to start trigger: ...`（P2，`335bb0a38`） |
| AI 工具校验口径 | ✅ | 字符串数字、裸 methods、非法 port 已按 REST/Python 拒绝（P2，`f72af6b3e`） |
| `triggers.json` 写入原子性 | ⚠ 有差异（Go 更安全） | Go 原子写；Python `write_text` 非原子（保留的可靠性差异） |
| missed-trigger 握手 | ⚠ 共同缺口 | 两侧都定义 `MISSED_TRIGGERS` 但都不发送；静默 stamp |

---

## 1. 第一轮确认对齐的部分

### 1.1 定义与持久化

- 字段：`id`、`name`、`trigger_type`、`trigger_config`、`task`、`enabled`、`last_fired_at`、`next_due_at`。
- 路径：`<colony>/triggers.json`（`session.worker_path` / `WorkerPath`）。
- 读取：文件缺失、JSON 损坏、非数组 → 空列表；未知字段在其他条目上保留。
- 去重：按 `id` 替换；保存时先过滤同 id 再 append。
- 会话加载：`enabled` 缺省按 `true` 处理（Python `tdata.get("enabled", True)`；Go `enabled := true`）。

### 1.2 AI 工具

| 工具 | Python | Go | 判定 |
|---|---|---|---|
| `set_trigger` | 创建/激活；已激活报错；无 task 报错；webhook path/method 校验 | 同 | ✅（主路径） |
| `list_triggers` | `{triggers:[{id,trigger_type,trigger_config,description,task,enabled}]}` | 同；`enabled` 取 `IsTriggerActive` | ✅ |
| `remove_trigger` | 取消 timer/sub、清激活、从 `triggers.json` 删定义 | 同 | ✅ |

可见性：三者都在 colony 阶段工具面（Python `_QUEEN_COLONY_TOOLS`；Go `colony_pivot_lean.go` 的 colony 清单）。

### 1.3 cron / timer

- cron：Go 已支持 5/6/7 字段、秒在尾、宏、月份/星期名称、`L`/`L<dow>`、`#n`、`?`、跨夜区间、年份 1970–2099；croniter 本身拒绝的 `W`/`LW`/`L-3`/裸 DOW `L`/`5L` 两侧都拒绝。
- fixture：真实 croniter 6.0.0 生成，126 表达式 / 4,911 匹配步 / 82 拒绝语料 / 4 无匹配语料。
- timer：UTC；首触发延迟 cron=下一次匹配、interval=值×60s；`next_fire_in/at` 更新；触发后写 `last_fired_at`/`next_due_at`。
- missed：严格晚于 `last_fired_at`、不晚于 `now`；ticks 最多 100；加载时静默 stamp（两侧一致）。

### 1.4 webhook 核心

- 监听 `127.0.0.1`，默认端口 8090，首个 trigger 创建 server，后续 trigger 复用。
- path 精确匹配、区分大小写；methods 默认 `["POST"]`，合法集合 `GET/POST/PUT/PATCH/DELETE/HEAD/OPTIONS`。
- 响应：202 `{"status": "accepted"}`、404 `{"error": "Not found"}`、405 `405: Method Not Allowed`（带 aiohttp 风格 `Allow` 头）、400 `{"error": "Failed to read request body"}`、401 `{"error": "Invalid signature"}`。
- body 上限 1 MiB，`>=` 即 400（无 413）。
- headers/query 重复键取首值；Go 额外补 `Host`；header 名由 Go canonical 化（已登记）。
- JSON：合法任意类型原样保留；解析失败 → `{"raw_body": ...}`；Python 接受 `NaN/Infinity`，Go 走 `raw_body`（已登记）。
- HMAC：`X-Hub-Signature-256` 校验代码两侧都有，但 trigger 创建流程都不传 secret，因此不可达（两侧一致）。

### 1.5 注入与事件

- 注入：`[stamp]\n[TRIGGER: type/id]\nTask: ...\n{payload}`；多条合并为一条 user message，共享一个 stamp。
- `webhook_received`：`path/method/headers/payload/query_params`，`stream_id=source_id`。
- `trigger_fired`：`trigger_id/trigger_type/name/task/last_fired_at/fire_count/next_fire_in/next_fire_at`。
- `trigger_activated` / `trigger_deactivated`：字段与 stream_id 一致。

---

## 2. 第二轮新发现

### P1（已修复 2026-11-25）— Go unload colony 后保留旧 Trigger（Python 清空）

**原缺陷（2026-11-25 12:32 基线 `b5e0a1071`）**

**Python** `SessionManager.unload_colony`：

1. 取消 `active_timer_tasks`；
2. 退订 `active_webhook_subs`；
3. 清空 `active_trigger_ids`；
4. 对 `available_triggers` 逐条发送 `TRIGGER_REMOVED`；
5. 清空 `available_triggers`；
6. 清空 `colony_id` / `binding` / `worker_path`。

证据：`core/framework/server/session_manager.py:1284-1303`、`_emit_trigger_events` `:1885-1915`。

**Go** `UnloadColony`：

1. `StopAllTriggers()`：取消 timer/sub、停 webhook server、清 `ActiveTriggers` / `NextFireTimes`；
2. `DetachColony()`：清 `Colony` / `ColonyID` / `WorkerPath`。

**不清 `AvailableTriggerDefs`，也不发 `trigger_removed`。**

证据：`go_hive/go-hive/internal/cli/loader.go:750-763`、`internal/server/session.go:369-395`、`internal/server/session_chat_colony.go:60-68`。

**后果**：

- `DELETE /api/sessions/{id}/colony` 后，`GET /api/sessions/{id}/triggers` 仍返回旧 colony 的 trigger（`enabled=false`）；
- `list_triggers` 仍能看到旧定义；
- 前端收不到 `TRIGGER_REMOVED`，卡片滞留到刷新；
- 若同一 session 再 attach 另一个 colony，`RestoreSessionTriggers` 只对新的 definitions 做 `PutTrigger`，**不先清空旧 map**，旧 colony 的 trigger 可能跨 colony 残留（`internal/server/trigger_runtime.go:221-280`）。

这是本轮最需要修的一条。建议：

- `UnloadColony` 清 `AvailableTriggerDefs`，并按 Python 为每条发 `trigger_removed`；
- `RestoreSessionTriggers` 用新 map 替换（或先清空）再水化，避免 merge 残留；
- 补回归：`unload colony → list empty`；`unload A → attach B → 不残留 A`。

**修复（`d5540e86c`，2026-11-25 12:59 +0800；merge `b61abc5a8`，2026-11-25 13:00 +0800）**

- `UnloadColony` 在停 runtime / detach 前调用 `RemoveAllTriggers()`：先快照 next-fire 与 fire stats，`StopAllTriggers()` 取消 timer/webhook，逐条 `Publish(EventTriggerRemoved)`（payload 对齐 Python 的 `trigger_id/trigger_type/trigger_config/name`，含 `next_fire_in/next_fire_at`、`fire_count/last_fired_at`），再清 `AvailableTriggerDefs`。
- `RestoreSessionTriggers` 改为 replace：先 `StopAllTriggers` + 清 catalog，再读新 colony 的 `triggers.json`，colony 切换不继承旧定义。
- 回归：`internal/server/trigger_wiring_test.go` 的 `TestRemoveAllTriggersPublishesRemovedAndClearsRuntime`、`TestRemoveAllTriggersWithoutBusStillClears`、`TestRestoreSessionTriggersReplacesCatalog`、`TestRestoreSessionTriggersWithoutValidEntriesClearsCatalog`；`internal/cli/loader_colony_lifecycle_test.go` 覆盖 unload 生命周期。

### P2（已修复 2026-11-25）— Go“更新 Trigger”吞掉重启失败

**原缺陷（2026-11-25 12:32 基线 `b5e0a1071`）**

Python `handle_update_trigger_task` 在配置变更、trigger 处于 active 时重启 timer/webhook，**没有 try/except**；启动失败会冒泡成 500（`core/framework/server/routes_sessions.py:1460-1485`）。

Go `handleUpdateTrigger`：

```go
_ = StartTriggerTimer(session, triggerID, &def)
_ = StartTriggerWebhook(session, triggerID, &def)
```

错误被丢弃，随后仍返回 200（`go_hive/go-hive/internal/server/routes_session_triggers.go:300-320`）。端口被占用或 webhook server 起不来时，UI 会显示更新成功，但 trigger 实际没跑。

建议：与 create/activate 一致，启动失败返回 500 + `Failed to start trigger: ...`。

**修复（`335bb0a38`，2026-11-25 12:55 +0800）**

- `handleUpdateTrigger` 现在检查 `StartTriggerTimer` / `StartTriggerWebhook` 的返回错误；失败时清理半安装的 webhook 订阅与 next-fire，`DeactivateTrigger` 并把 `def.Enabled=false` 持久化，再返回 500 `{"error": "Failed to start trigger: <原因>"}`，不再回 200。
- 回归：`internal/server/routes_session_triggers_test.go` 的 `TestUpdateActiveWebhookRestartOutcomes`（端口占用/启动失败路径）。

### P2（已修复 2026-11-25）— Go 的 AI 工具校验比 REST / Python 宽松

下表为修复前行为（2026-11-25 12:32 基线 `b5e0a1071`）：

| 输入 | Python `set_trigger` | Go `set_trigger`（修复前） | Go REST create/update |
|---|---|---|---|
| `interval_minutes: "5"` | 报错（`isinstance` 失败） | 接受（`floatValue` 解析数字字符串） | 拒绝（`numericValue` 严格数字） |
| `methods: "GET"` | 迭代字符串 → `['G','E','T']` 报错 | 当单个方法 `["GET"]` | 不适用（REST 只建 timer） |
| `methods: 123` | 迭代/`.upper()` 抛异常 | 回落 `["POST"]` | 不适用 |
| `port: "abc"` | `int()` 抛异常 → 500 | 回落 8090 | 不适用 |

证据：`go_hive/go-hive/internal/server/queen_lifecycle_triggers.go:134-146`、`internal/server/trigger_runtime.go:759-770`、`queen_lifecycle_triggers.go:258-276`、`trigger_runtime.go:590-600`；Python `core/framework/tools/queen_lifecycle_tools.py:3810-3830`。

结果：同一个 Go 进程里，UI 创建和 AI 工具创建的行为不同。建议把 `set_trigger` 的类型校验对齐 REST/Python（或明确记录“工具侧宽松”为有意差异）。

**修复（`f72af6b3e`，2026-11-25 12:58 +0800）**

- `queen_lifecycle_triggers.go` 的 `set_trigger` 改为与 REST/Python 相同的严格校验：`interval_minutes` 只接受 JSON 数字、`methods` 只接受字符串数组、`port` 只接受合法端口；类型不符返回工具错误，不再静默回落。
- 回归：`internal/server/queen_lifecycle_triggers_validation_test.go` 的 `TestSetTriggerIntervalRequiresNumber`、`TestSetTriggerWebhookMethodsRequireStringList`、`TestSetTriggerWebhookPortValidation`。

### P2 — `triggers.json` 写入原子性不同

- Python：`_write_agent_triggers_json` 直接 `Path.write_text(...)`，非原子（`core/framework/tools/queen_lifecycle_tools.py:558-564`）。
- Go：`WriteAgentTriggersJSON` 用 `atomicfs.WriteFile`（`go_hive/go-hive/internal/triggers/triggers_store.go:50-55`）。

并发写（timer 更新 + API/tool 写入）时 Python 有截断风险，Go 更安全。这是 Go 侧加固，不是 Go 的 bug；但如果要做“逐字/行为一致”，需要明确这是有意保留的可靠性差异。

### P3 — SSE trigger 重播策略不同（前端补偿）

- Python 后端在 SSE 连接时，除 ring-buffer 重播外，会从 live session 重建完整 trigger 集，补发 `trigger_activated` / `trigger_available`（`core/framework/server/routes_events.py:177-205,411-420`）。
- Go 后端没有这一步；依赖历史 ring + `trigger_available` 事件。
- Go 前端在 colony 加载时主动 `GET /sessions/{id}/triggers` 并合成事件（`go_hive/go-hive/frontend/src/pages/colony-chat.tsx:2006-2043`），Python 前端也做同样的事。

用户可见结果一致；差异在“非 UI 的 SSE 客户端”会看到更少的 trigger 事件。不是阻断项。

### P3（已修复 2026-11-25）— Go 没有 `trigger_removed` 生产者

Go 定义了 `EventTriggerRemoved`，也放进 history 白名单（`internal/eventbus/event_type.go:122`、`internal/server/chat_routes.go:314`）。修复前全仓没有 `Publish(EventTriggerRemoved)`；Python 在 unload 时会发。

**修复（同 P1，`d5540e86c` / merge `b61abc5a8`）**：`Session.RemoveAllTriggers()` 对每个可用定义 `Publish(EventTriggerRemoved)`，payload 与 Python `_emit_trigger_events` 对齐；`UnloadColony` 在清 catalog 前调用。回归见 `TestRemoveAllTriggersPublishesRemovedAndClearsRuntime`。

### P3 — 注入 payload / `triggers.json` 文本形态差异

- 注入 payload：Python `json.dumps(t.payload, default=str)` 默认 `", "`/`": "` 分隔符、`ensure_ascii=True`；Go 紧凑 JSON + 原始 UTF-8（`SetEscapeHTML(false)`）。
- `triggers.json`：Go `encoding/json` 按 map key 字典序；Python 按插入顺序。两者都是 2 空格缩进、非 ASCII 不转义（Go `SetEscapeHTML(false)`）。
- 语义等价；若做逐字 prompt/文件 golden，需要登记为文本级差异。

### P3（已修复 2026-11-25）— webhook 405 的 `Allow` 头

Python aiohttp 自动生成的 405 带 `Allow` 头；Go 手写 405 原来不带。状态码和 body 一致，header 不同。

**修复（`7fff8cc3b`，2026-11-25 14:22 +0800）**：Go 405 现在写 `Allow`，值按 aiohttp 3.13.5 实测口径生成——方法大写、排序、逗号分隔、无空格（例如 `GET,POST,PUT`）；body 字节不变。回归：`TestWebhookMethodMismatchAllowAndEmptyMethodsParity`。

### P3（已修复 2026-11-25）— `methods: []` 的 404 vs 405

- Python：`add_route` 只按 methods 注册；空列表 → path 无路由 → 404。
- Go：修复前 path 注册成功但 `allowsMethod` 永远 false → 405。

极端配置才有差。**修复（同 `7fff8cc3b`）**：Go 对 `methods: []` 的已注册 path 直接返回 404 `{"error": "Not found"}`，与 aiohttp 只按 methods 建路由的结果一致；route 仍留在表中，`Start()`/替换语义不变。回归同 `TestWebhookMethodMismatchAllowAndEmptyMethodsParity`。

### P3 — `fire_count` 边界

Python `_emit_trigger_fired` 只在 stats map 存在时带 `fire_count`；Go 总是带。正常会话两侧都有 stats，通常不可观测。

---

## 3. 共同缺口（不是 go_hive 单侧差异）

### 3.1 `MISSED_TRIGGERS` 没有生产者

- 两侧都定义 `missed_triggers` 事件：Python `core/framework/host/event_bus.py:277`；Go `internal/eventbus/event_type.go:124`。
- 前端都监听：`colony-chat.tsx:2435`。
- 但 session load 实际是静默 stamp `last_fired_at = now`，不发事件：
  - Python `core/framework/server/session_manager.py:1078-1098`；
  - Go `go_hive/go-hive/internal/server/trigger_runtime.go:260-285`。
- 结果：文档描述的 “missed-trigger 选择框” 不会出现；`resolve_missed` REST 只能被手动调用。

需要决定：接线（加载时发 `missed_triggers`）或从文档/前端删掉该握手。

### 3.2 `enabled` 默认值的文档漂移

`docs/triggers-webhook.html` 写 `enabled` 默认 `false`（TriggerDefinition 构造默认），但持久化加载时两侧都把缺省 `enabled` 当 `true`。这是文档与代码的差异，不是两侧差异。

---

## 4. 本轮验证命令与结果

在 `19282cfcf`（触发器文件与后续 HEAD 无行为差异）上执行：

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/triggers -run 'Cron|Missed' -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/triggers 0.409s

CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/server -run 'Webhook|Trigger' -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/server 1.210s

cd /Users/mac/hive
PYTHONPATH=/Users/mac/hive/core /Users/mac/hive/.venv/bin/python -m pytest \
  core/tests/test_missed_triggers.py core/tests/test_trigger_persistence.py -q
# 18 passed in 0.56s
```

既有 full CI 证据（cron/webhook 收口）：`go_hive/docs/reports/triggers-cron-webhook-close-20261125.md`，基线 `d341640b3`，tests **6,468/6,468**、parity refs **7,310/7,310**、compat **13/13**。

2026-11-25 14:35 CST 复核（基线 `5486a5fab`）补跑：

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test ./internal/server -run 'UpdateActiveWebhookRestartOutcomes|SetTrigger|WebhookMethodMismatchAllowAndEmptyMethodsParity' -count=1
# ok github.com/shark2202/go_hive/go-hive/internal/server

cd /Users/mac/hive/go_hive/go-hive && make ci
# RC=0；parity 6,511
```

unload/restore 的回归由 `internal/server/trigger_wiring_test.go` 与 `internal/cli/loader_colony_lifecycle_test.go` 覆盖（随包级测试执行）。

---

## 5. 修复优先级建议

1. **P1（已完成，`d5540e86c` / merge `b61abc5a8`）**：Go `UnloadColony` 清 `AvailableTriggerDefs` + 发 `trigger_removed`；`RestoreSessionTriggers` 改为 replace；unload/switch 回归已补。
2. **P2（已完成，`335bb0a38`）**：Go `handleUpdateTrigger` 传播 timer/webhook 启动错误（500）。
3. **P2（已完成，`f72af6b3e`）**：Go `set_trigger` 类型校验对齐 REST/Python（interval/methods/port）。
4. **P3（未完成）**：决定 `MISSED_TRIGGERS` 接线或删除；`trigger_removed` 生产者已完成（同 P1）。
5. **P3（部分完成）**：405 `Allow` 头与空 methods 404 已对齐（`7fff8cc3b`）；注入 payload 分隔符/编码、`triggers.json` 键序仍为文本级差异，需登记或做 golden。
6. 若要对齐可靠性，Python `triggers.json` 改原子写；否则在差异登记中注明“Go 侧有意加固”。

---

## 6. 验证边界

- 首轮报告基于当时工作树代码阅读 + 专项测试；2026-11-25 14:35 CST 复核在 `5486a5fab` 补跑了 trigger/webhook 专项测试与全量 `make ci`（RC=0，parity 6,511）。
- 未做真实网络 E2E：webhook 真实外部投递、SSE 非 UI 客户端的 trigger 重播、真实 Telegram/Slack 触发链均未验证；SSE 重播策略差异（P3）与 `MISSED_TRIGGERS` 共同缺口仍开放。
- 触发器 wire 级残差（头名拼写、超限 drain、NaN/Infinity、croniter 运行时缺陷）沿用 `triggers-cron-webhook-close-20261125.md`，本文不重复展开。
- 本报告本身不修改生产代码；修复来自 `d5540e86c`、`335bb0a38`、`f72af6b3e`、`7fff8cc3b` 及对应 merge。

---

## 7. 证据索引

| 主题 | Python | Go |
|---|---|---|
| trigger 定义/持久化 | `core/framework/host/triggers.py`、`queen_lifecycle_tools.py:546-628` | `internal/triggers/triggers.go`、`triggers_store.go` |
| AI 工具 | `queen_lifecycle_tools.py:3730-3985` | `internal/server/queen_lifecycle_triggers.go` |
| cron | `core/framework/host/triggers.py:90-125` | `internal/triggers/cron.go`、`testdata/cron_seconds_parity.json` |
| timer | `queen_lifecycle_tools.py:810-922` | `internal/server/trigger_runtime.go:420-565` |
| webhook | `core/framework/host/webhook_server.py` | `internal/server/webhook_server.go` |
| 注入 | `core/framework/agent_loop/internals/cursor_persistence.py:291-323` | `internal/server/trigger_runtime.go:312-356` |
| REST | `core/framework/server/routes_sessions.py:1231-1745` | `internal/server/routes_session_triggers.go` |
| unload/stop | `session_manager.py:1275-1310,1410-1435` | `cli/loader.go:750-763`、`server/session.go:369-395` |
| 重启恢复 | `session_manager.py:1078-1098` | `internal/server/trigger_runtime.go:221-285` |
| SSE 重播 | `server/routes_events.py:177-205,411-420` | 前端 `colony-chat.tsx:2006-2043` |
| 事件定义 | `host/event_bus.py:226-277` | `internal/eventbus/event_type.go:95-124` |
