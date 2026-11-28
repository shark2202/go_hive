# Memory / Soul 机制对齐审计：全生命周期

- 日期：2026-11-25 11:51 CST（闭环追记：2026-11-25 22:0x CST，lane `w174-memory-soul`）
- 基线：仓库 `f1e1bf97a`（Python `core/framework/**` ↔ Go `go_hive/go-hive/internal/**`）
- 范围：memory notes 的创建/读取/召回/编辑/退休/恢复/关停/压缩/清理，soul 的存储/渲染/每日反思/提案/回滚/探针/关停，及两者的作用域与互操作
- 方法：逐文件核对＋复用既有双跑/真实模型证据；未复跑全量测试
- 结论：**数据面与常规生命周期高度一致。S1/S3 已闭环，S2 经复核判定为平台差（上游在本检出即空转），无未闭环实质差异。**

## 1. 判定总表

| 生命周期阶段 | 判定 | 说明 |
|---|---|---|
| 创建/写入 | ✅ 记忆；✅ soul | memory 反思节奏、4 工具、owner 保护、200/4KB、日志行一致；Go-only sediment 现遵守 `mode`：review 只出提案、pending 阻断，auto 直写（S1 已闭环） |
| 读取/召回 | ✅ | index/wait/background、3/3、pinned、past 6/500/500/3s、timeline live/stop/backfill 一致 |
| 更新/审批 | ✅ | memory 编辑 owner 记账；soul review/auto、decide、rollback、probe、bootstrap 人审一致；sediment 提案走同一 decide 路由 |
| 退休/恢复 | ✅ | retire 留正文于 `log.jsonl`；history 0-based；restore 按 n |
| 关停/压缩 | ✅ | 压缩 → 长反思一致；关停为单一 6s 预算（等于上游 `finish_background_tasks(6s)`），超预算即取消并等待 goroutine 退出（S3 已闭环） |
| 清理/保留 | ✅ | 两侧均不自动删除记忆/soul；会话 retention 不触碰 `memories/`、`queens/<id>/soul/` |
| 迁移/互读 | ✅ | 纯文件、无迁移；soul 版本/history/log 形状一致；Go 附加字段为增量 |
| 云端同步 | ✅（平台差） | 上游 `cloud_sync_hooks.schedule_push` 在本检出为空转（`framework.cloud_sync` 未随包提供，ImportError 被吞），Go 生产钩子留 nil 与上游可观察行为同形（S2） |

## 2. 对齐证据

| 维度 | 两侧实现 | 证据 |
|---|---|---|
| 存储布局/限额 | `memory.go` ↔ `queen_memory_v2.py`（200 文件/4096 字节/30 行 manifest） | `docs/reports/storage-lifecycle-audit-20261125.md` §1.5（memories ✅） |
| 变更日志/owner | `memory_log.go` ↔ `memory_log.py` | 日志行格式 pin、owner guard、retire/restore 双跑 |
| 反思节奏 | 6 回合短、每 4 次短长、压缩长、关停短 | `reflection.go:1108-1218` ↔ `reflection_agent.py:974-1020` |
| 反思工具/提示词 | 4 工具、`build_unified_*` | `memory-management-spec.md` §5（prompt/schema 13/13） |
| 召回三模式 | `recall.go`、`chat_runtime_recall.go` ↔ `recall_selector.py`、`queen_orchestrator.py` | 3/3、pinned、wait ≤10s、background 缓存去重 |
| past conversations | `past_conversations.go` ↔ `tools/src/memory_tools/recall.py` | TOP 6、500/500、8 字符、3s、BM25 |
| timeline | `timeline.go` ↔ `timeline.py` | 12k 批、3 条/300s、schema v1、live/stop/backfill |
| Memory API | 8 路由 + `/api/config/memory` | 两侧路由逐条对应；字段级双跑 0 差 |
| Soul 存储/API | `internal/soul/*` ↔ `agents/queen/soul*.py`、`routes_soul.py` | 6 条路由一致；真实模型 E2E：prompt 逐字节、review/auto/history/state 一致 |
| Soul 反思状态机 | 2 分钟首跑 + 10 分钟 tick、hour、pending 阻断、3 次/天、1 小时间隔 | `reflection_run.go:27-74,236-259` ↔ `soul_reflection.py:612-683` |
| 作用域 | global/colony/queen/worker；自动召回仅 global+queen | 两侧一致 |

## 3. 差异与缺口

### S1（已闭环）：Go-only 的 memory→soul 自动沉淀，现遵守 review/pending

- Python：soul 只会因每日 soul 反思（review 提案 / auto 落地）、owner `PUT`、proposal decide/rollback 而改变；记忆反思不触碰 soul（全仓无 sediment 引用）。bootstrap 只读取记忆作为证据。
- Go（本次修复前）：每次成功的 memory reflection（短反思与长反思都触发）都会 `sedimentSoul`，直接 `Save`（`soul.json` + `history/v<N>.json` + `log.jsonl`）并维护 Go-only 的 `sediment.json`，不检查 `mode: review` 与 pending proposal。
- Go（现状）：`Sediment` 先读 `cfg.Mode`——
  - `review`（`GetSoulConfig` 默认）：不写 soul，把结论写成一条 pending proposal（`proposals/<date>-memory-<HHMMSS>.json`，`edits` 全为 `add`，`sources` 记记忆文件名，`summary` = “N from memory reflection”），由既有 `POST /api/queen/{id}/soul/proposals/{pid}/decide` 以 `owner` 落地；已有 pending proposal 时整趟跳过（`Blocked`），与每日反思 `ReflectionDue` 的 review 闸门同规则。
  - `auto`：保持原直写路径（含 `sediment.json` 记账与“只驱逐自己写过的条目”）。
  - review 下不驱逐：早先提案落地后条目已属 owner，section 满则丢弃该结论（`Dropped`）。
- 证据：`internal/soul/sediment.go`（`Sediment`/`sedimentReview`）、`internal/server/chat_memory_wiring.go::sedimentSoul`；回归 `internal/soul/sediment_review_test.go`（review 提案+decide 落地、pending 阻断并重开、auto/review 双侧、满段不驱逐）、`internal/server/chat_runtime_soul_test.go`（auto 直写 / review 只出提案）、`internal/server/routes_credentials_soul_sediment_test.go`（soul 视图 reporting proposal vs 记账回退）。

### S2（平台差，非缺口）：记忆写入的云端同步

- Python：`routes_memories.py:41-43` 对 `global/`、`agents/queens/` 的写/改/删调用 `schedule_push("memory", rel)`。
- **复核证据**：`cloud_sync_hooks.schedule_push`（`core/framework/cloud_sync_hooks.py:20-37`）先 `from framework.cloud_sync import _cloud_config`；本检出**没有** `core/framework/cloud_sync.py`（只有 `cloud_sync_hooks.py`），ImportError 被 `except Exception` 吞掉并 `return`。即：**上游在本检出每次调用都是空转**，没有任何云推送被执行。
- Go：`MemoryRouteDeps.SchedulePush` 钩子与单测齐备（`routes_memories_extra.go`），生产装配 `app.go` 只传 `Home`（nil 钩子）。这与上游可观察行为**同形**：本地写完成、无推送、无报错。
- 判定：`platform_diff`——两侧都没有 cloud-sync reconciler；Go 不新造一个上游不执行的云端路径。复查条件：上游补回 `framework/cloud_sync`（`_cloud_config()` 可返回非 nil）时，把 reconciler 一起移植并把钩子接进 `RegisterMemoryRoutes`。
- 证据：`routes_memories_extra.go::MemoryRouteDeps` 注释、`app.go` 的 `RegisterMemoryRoutes` 注释、`internal/server/routes_memories_sync_test.go::TestMemoryPushProductionWiringMatchesUpstreamNoOp`（生产 nil 与接线双侧）。

### S3（已闭环）：关停收尾的等待语义

- Python：`stop_session` 把关停短反思与 timeline flush 作为 fire-and-forget task 启动，per-session stop 不等待；server 退出时 `finish_background_tasks(6s)` 统一给 6 秒再取消。
- Go（本次修复前）：`ChatRuntime.Close` 阻塞收尾——reflection `Close(2s grace)`（排空后再跑最终短反思，共可达 4s）+ timeline flush 30s 超时并等待 `timelineWG`，最坏约 34s。
- Go（现状）：`Close` 用**一个** `chatShutdownGrace = 6s` 预算（= `_SHUTDOWN_GRACE_S`）同时约束最终短反思与 timeline flush：`ReflectionScheduler.Shutdown(ctx)` 在预算内等待在途反思，超时即取消（`reflectionCancelDrain` 1s 内确认 goroutine 退出，不再产生 waiter goroutine），预算有余量才跑最终短反思；两条路径都返回后 `Close` 才返回，因此“Close 之后不再写”的承诺保持。
- 证据：`internal/server/chat_memory_wiring.go::chatShutdownGrace`、`internal/server/chat_runtime.go::Close`、`internal/memory/reflection.go`（`Shutdown`/`waitIdle`/`trackLocked`/`settle`）；回归 `internal/memory/reflection_shutdown_test.go`（预算内跑最终短反思、超预算取消且 goroutine 退出、快 pass 不被取消 + teardown pass 仍跑）、`internal/server/chat_runtime_shutdown_test.go`（两条 leg 共享预算；idle 时仍完成两条 leg）。残余：预算耗尽的极端路径下最终短反思被跳过（Python 会再起一个 task、同样在 6s 处被取消）。

### S4（已登记）：压缩事件 `message_inventory` 的口径

Go 的 `content_chars`/`preview` 用字节长度，Python 用字符长度；非 ASCII 内容不同（`agentloop/compaction.go:304`；`alignment-differences.md:74` D6）。不影响记忆/召回本身。

### S5（验证边界，非机制差异）

Timeline 在 Go 侧有完整实现，但真实数据仅 fixture/脚本验证；Python 有生产路径历史。记忆以双跑脚本为主，soul 有真实模型 E2E；两者验证强度不同。

## 4. 文档漂移

| 文档 | 漂移 | 实际 |
|---|---|---|
| `docs/soul-spec.md:11` | `$HIVE_HOME/agents/queens/<id>/soul/` | 两侧均为 `$HIVE_HOME/queens/<id>/soul/`（`soul.py:132` ↔ `store.go:20`） |
| `docs/soul-daily-reflection.md:203-217` | 称 `model: worker` 不启用 worker 模型；称 boot 后立即首跑 | 已支持 `worker_llm` 优先（`loader_soul_model.go`、`loader_model_slots.go:65`）；首跑延迟 2 分钟（`loader_soul_daily.go:18-19`） |
| `docs/memory-management-spec.md:175` | 调度实现指向 `internal/memory/reflection_run.go`（不存在） | 本检出该行已指向 `internal/memory/reflection.go`；soul `DailyRunner` 在 `internal/soul/reflection_run.go`（无漂移） |
| `docs/memory-management-spec.md` D1/D2/D8 | D1 称生产未接线、D2 称 sediment 绕过 review、D8 称 2s/30s 关停 | 已按 S1/S2/S3 结论改写（本轮）；soul 文档 §6 的 sediment 段同步 |

## 5. 建议（本轮执行结果）

1. S1 → **已修**：review 是硬约束，`Sediment` 在 review 下只写 pending proposal、pending 阻断、满段不驱逐；auto 保持直写。
2. S2 → **登记为平台差**：上游 `schedule_push` 在本检出因缺 `framework.cloud_sync` 而空转，Go 生产钩子留 nil 即同形；不做假接线（复查条件见 §3 S2）。
3. S3 → **已修**：关停改为单一 6s 预算，等于上游 `finish_background_tasks(6s)`；超预算取消并等待 goroutine 退出，保持“Close 后不再写”。
4. §4 的文档漂移：memory spec 的 D 表与调度锚点本轮已更正；soul-spec / soul-daily-reflection 两条仍在原文档，未在本 lane 改动（跨文档，留待文档 lane）。

## 6. 验证边界

- 复用证据：`storage-lifecycle-audit-20261125.md`、`memory-management-spec.md` §5、`soul-daily-reflection.md` §8.1（真实模型 E2E）。
- 本轮运行：`CGO_ENABLED=0 go test ./internal/memory ./internal/soul ./internal/server ./internal/agentloop -count=1`（全绿）、`make parity`（6745/6745）、`gofmt -l .`（空）、`go vet ./internal/soul ./internal/memory ./internal/server`（空）。
- 本轮未运行：Python memory/soul pytest、真实模型联调、真实 serve 进程的 stop 压测（S3 的时间预算以单元测试的缩小 grace 观测，非 6s 实跑）。
