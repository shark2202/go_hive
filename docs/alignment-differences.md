# 实际差异报告（go_hive ↔ Hive 参考）

> 更新：2026-11-23（§2 工具契约 round 9：归一化残差 0；§5 补触发器登记；MCP 行改记 D3 终选 B′——默认裸名，前缀模式可选；总表与 §1–§4 保持 2026-11-22 快照，wave-76 全量重算）。
> 分类：**一致** / **登记差异（有意保留，带复查条件）** / **已修（本轮闭环）** / **待拍板（需决策）** / **外部未验证**。
>
> **追记（2026-11-24，w96 / `34a274cc`）**：包一致性审计的 A1/A2（queen 工具预算固定 30/50、迭代上限每回合 50）已修复。`agentloop.QueenLoopConfig()` 现为 999999 迭代 + 30/150 工具预算，`ColonyQueenLoopConfig()` 为 999999 + 50/500；`internal/server/chat_memory_wiring.go::chatLoopConfig` 与 colony overseer（`internal/colony/overseer_loop_config.go`）均改用对应 profile，`DefaultLoopConfig()` 保持通用 50 迭代 / 30 软 / 50 硬不变。历史审计正文（含 A1/A2 行与风险清单）保留 2026-11-24 当日快照，不回写；该报告文件在 `go_hive/docs/reports/package-consistency-20261124/report.md`，未随本 worktree 检出。回归：`internal/agentloop/loop_config_profiles_test.go`、`internal/colony/overseer_loop_config_test.go`、`internal/server/config_slots_test.go`。
>
> **追记（2026-11-24，w134 / A5+A39 closeout）**：A5 已闭环——`internal/agentloop/doom_loop_window.go` 的 turn 级批次指纹窗口已接线（`loop.go:1151`），按流形态分流（worker 升级 / queen park / 其他仅警告，`loop_guards.go:211`），并经 `DoomLoopSink`（`loop_guards.go:129`、`colony/worker_doom_loop.go:32`）与 `ParkSink`（`server/chat_loop_guards.go:100`）发布 `node_tool_doom_loop`；检测描述改为事件与 park/升级共用同一份文本。A39 已登记为有意保留的 Go 侧改进（§7.2）：ghost 路由与 i18n 包装不回填 Python（B 树无 i18n 层）。回归：`internal/agentloop/doom_loop_window_test.go`、`internal/colony/worker_escalation_test.go`、`internal/server/chat_doom_window_test.go`。历史审计正文保留 2026-11-24 当日快照，不回写。
>
> **追记（2026-11-25，w131-w133 / D-1+D-2+S-2 closeout）**：`SyncUserProfileMemory` 改走 `memory.Write`（owner/keep-author，恢复 frontmatter、`log.jsonl`、4KB 上限与 owner 守卫）；`memory_reflection.every_turns/long_every` 改为只接受真实 JSON 整数（拒绝 `true`/`"2"`/`2.0`）；`sinkColonyPivot` 恢复 fork-kickoff 与已打开 popup 的状态否决。证据与残余见 §7.3 与 `docs/reports/alignment-closeout-20261125.md`。

> **追记（2026-11-25，colony ownership alignment）**：默认的 colony-bound session 由 **chat queen 执行**，ColonyRuntime 照常启动但 overseer 保持 dormant，回到 Hive 1.0.15 的生产路径；没有生产环境变量或启动开关会启用 live overseer。`/api/sessions/{id}/live_tools` 只报告 chat 执行者的 phase-state 工具面，不新增 `executor` 字段，也不读取另一个 loop 的工具面。证据：`internal/cli/loader_colony_lifecycle_test.go`、`internal/cli/loader_preloaded_chat_test.go`、`internal/server/routes_session_live_tools_test.go`。

> **追记（2026-11-25，skill parity audit）**：修复 colony worker/overseer 忽略 queen `skills_overrides.json` 的差异——`internal/colony/skills_surface.go` 现在每次重建目录时加载 queen store，colony watcher 也监视该文件；回归 `internal/colony/skills_surface_test.go::TestWorkerCatalogHonorsQueenDisableForColonySkills`。其余 skill 侧已知差异（内置 location 随机临时路径、缺 `SKILL_INSTALL_REQUESTED` 事件、写后 reload 时序、无扫描面契约）见 `docs/reports/skill-parity-audit-20261125.md`；Go 独有的 `skill_view`/`list_skills` 与 `POST /api/skills/reload` 维持既有登记。

> **追记（2026-11-25，MCP 边角）**：默认 MCP 路径已与 Python 一致（裸名、工具定义、调用、连接生命周期、注册表）；R5 已修复（`bb8cccb1a`，stdio 回应 server 主动 ping/roots/list/sampling/elicitation，fake + 子进程 fixture 测试），其余登记 R4（Go-only `mime_type` 别名/非 string `data`）、R6（工具定义 `~/.hive` 路径重写仅 Python）、R7（reload / timeout 文件 / `tool_names` / native mcp2cli / 注册表权限等 Go 扩展）。详见 §7.4 与 `docs/reports/mcp-parity-20261125.md`。

> **追记（2026-11-25，memory/soul parity audit；w174-memory-soul 闭环）**：全生命周期核对结论见 `docs/reports/memory-soul-parity-audit-20261125.md`。原登记三项已处置：① **S1 已修**——Go-only `sediment` 现遵守 soul 的 `mode`：review（默认）只写 pending proposal、已有 pending 时整趟阻断、满段不驱逐，auto 保持直写（`internal/soul/sediment.go::Sediment`/`sedimentReview`、`internal/server/chat_memory_wiring.go::sedimentSoul`；回归 `internal/soul/sediment_review_test.go`、`internal/server/chat_runtime_soul_test.go`、`routes_credentials_soul_sediment_test.go`）。② **S2 判为平台差**——`cloud_sync_hooks.schedule_push` 先 `from framework.cloud_sync import _cloud_config`，本检出无 `core/framework/cloud_sync.py`，ImportError 被 `except Exception` 吞掉即 return，上游每次调用都空转；Go `MemoryRouteDeps.SchedulePush` 生产留 nil 与上游可观察行为同形（不新造云端路径；复查条件见报告 §3 S2；回归 `routes_memories_sync_test.go::TestMemoryPushProductionWiringMatchesUpstreamNoOp`）。③ **S3 已修**——关停收尾改为单一 6s 预算（`chatShutdownGrace`，等于 `_SHUTDOWN_GRACE_S`），`ReflectionScheduler.Shutdown(ctx)` 在预算内等待、超时取消并在 `reflectionCancelDrain` 内确认 goroutine 退出，预算有余量才跑最终短反思（`internal/memory/reflection.go`、`internal/server/chat_runtime.go::Close`；回归 `internal/memory/reflection_shutdown_test.go`、`internal/server/chat_runtime_shutdown_test.go`）。doc 侧同步：`memory-management-spec.md` D1/D2/D8、`soul-spec.md` §6 的 sediment 段。另 `message_inventory` 字节/字符口径见既有 D6（未变）。

> **追记（2026-11-25，park/reminder/budget audit）**：全量核对见 `docs/reports/park-reminder-budget-audit-20261125.md`。预算 profile、15 个 reminder source、park 枚举/闸门、四个 stall 守卫均已对齐；原登记两项已修复：① **P0（已修）** `/api/sessions/{id}/cancel-queen` 的 `CancelCurrentTurn` / `MarkUserStopped` 已在 `*ChatRuntime` 实现（提交 `3425aecf6`，合并 `b5fc7ac23`）：Stop 返回 `cancelled:true` 与 `{cancelled, workers}` 形状，仅取消在飞 turn，被 Stop 的 turn 落到 `user_stopped` park 而非 `execution_failed`，`user_stopped` 闸门自下一个 tick 起抑制 idle nudge，下一次真实客户端输入才解除；回归 `TestCancelQueenStopsRealChatRuntimeTurn`、`TestCancelQueenKeepsQueuedTurnRunning`（真实 runtime + 阻塞模型 + 注册为 queen node，非 fake）。② **P1（已修）** cursor 现持久化 `user_stopped` / `tool_calls_used` / `recent_tool_fingerprints` 并于 loop/runtime 构造时恢复（提交 `53fd28042`，合并 `049738d80`），resume 后 lifetime budget 与 doom 窗口不再重置；回归 `cursor_resume_test.go`、`chat_cursor_restore_test.go`。③ **P1（w177/cursor-replay 已修）** cursor 补齐 `iteration` / `outputs` / `node_id`（按 `cursor_persistence.py` 的 JSON 形状；restore 按 `iteration+1` 续算 `max_iterations`，缺字段/旧形状取默认），重放断路器不再按 stream 升级通道放行跨轮重复（阈值即拒、文本轮断链，与 `agent_loop.py:5477-5492` 一致；只读 `task_list`/`task_get` 仅断路器豁免，保证 worker task reminder 回合可完成）；回归 `cursor_resume_test.go`、`replay_test.go`、`internal/colony` 提醒/升级测试。剩余边界：**P2**（queen doom-loop park 恢复形态，已登记设计差异）与 **P3**（真实 serve 进程 Stop/冷重启 E2E、真实网络 stream stall/Sentinel 投递等验证边界）维持开放。

> **追记（2026-11-26，w182 / pipeline 生产可达性）**：`internal/pipeline` 与 `ColonyRuntime.Trigger` 判定为 **partial（机制/API surface）+ out_of_scope（生产触发路径）**。上游 `ColonyRuntime.trigger`（`core/framework/host/colony_runtime.py:2376`）仅由 `trigger_and_wait`（`:2425`）和 `_timer_loop`（`:2904`）调用；timer 注册 `register_trigger`（`:2823`）全仓 0 调用方，生产 session 的 colony 显式 `pipeline_stages: []`（`core/framework/server/session_manager.py:1777`）。生产触发器走 queen 注入（`routes_sessions.py:1527`/`:1557`；`queen_lifecycle_tools.py:810`/`:886`），与 Go 的 `handleRunTrigger`/`StartTriggerTimer` 同形。图时代 `AgentHost.trigger`（`agent_host.py:918`）不属于 Go 支持的 colony 路径（`routes_sessions.py:1091` 已移除 legacy graph entry points）。接线到 `handleRunTrigger` 会改变 spawn-vs-inject 语义，故不接线；守卫 `internal/pipeline/reachability_test.go`，详见 `docs/pipeline.md §7` 与 §7 A40。

> **追记（2026-11-26，w195 / judge parity）**：移除了 Go-only 的 colony goal criteria judge 激活接缝，colony 路径回到 Hive 1.0.15 口径。`server/chat_runtime_colony.go` 不再在 colony bind 时用 `GoalCriteriaText` 继承 goal criteria，也不再调用 `ConfigureJudge`；`colony/runtime_worker_lifecycle.go`（worker）与 `colony/runtime.go`（overseer）同样不再继承/接线；女王 AgentSpec 固定 `SkipJudge=true`（与上游 `server/queen_orchestrator.py:1665`、`agents/queen/nodes/__init__.py:772` 一致）。显式 `report_to_parent` 与自然停点都不再注入 `[Judge feedback]`；通用 judge 实现/API（`agentloop.Judge`、`ConfigureJudge`、`shouldJudge`、`evaluateJudge`）保留给 graph-era 与显式调用方。回归：`internal/server/chat_runtime_criteria_test.go`（colony-bound 女王真实回合）、`internal/colony/judge_wiring_test.go`（worker 自然停点与显式 report，goal 带 criteria）、`internal/colony/overseer_judge_test.go`。此前 `phase-change-and-judge.html`、`judge-parity.md` 中的 Go-only 激活描述已同步删除。

## 0. 一页总表

| 面 | 实测 | 状态 |
|---|---|---|
| API 路由 | 参考 222 / Go 225：有效缺失 **0**、别名覆盖 2、Go 扩展 5、方法差 1（登记） | 一致（7 条登记差异） |
| REST 端点形状/语义 | 18/18 PASS（107 端点语料） | 一致 |
| SSE 事件帧 | 剔除 Go 独有每回合信封后 **42/42 逐帧一致** | 一致 |
| 工具定义（LLM 侧） | desc **48/48** 逐字；schema 归一 **48/48**（raw 40/48，8 条纯表示差：数值字面 4 + 参考层 `session_cwd` 4）；round 9 把 14 条归一化残差（12 lifecycle + `browser_setup` + `task_create`）归零 | 一致 |
| 工具面扁平 | always-on 24 / deferred 72 / unique 39 | 一致 |
| 触发器（timer/webhook） | 契约页 v3 + 手册 + `session-create.md`；croniter 6.0.0 全语法（126 表达式/4,911 步）；响应码/响应字节/payload 数字类型/批合并/重复键/body 上限**全已修**（3597215d/1e8ef0a1/a91ae182/4978b19c1） | 一致+登记（§5：2 条 wire 残差 + NaN/Infinity） |
| env 变量 | Python 90 / Go 125 / 共有 84 / Python-only 6（GCU）/ Go-only 41（含 runner 与验证脚本开关） | 一致（口径分类明确） |
| configuration.json | 137 键 / 22 sections；aligned 128 / go_only 4 / go_partial 4 / unread 1 | 一致+登记 |
| CLI 外观 | hive 47 路径 / hive-browser 27 / hive-cred 全对齐；4 usage slot 0 漂移 | 一致 |
| 内置 Skill | 25 文件逐字节一致（229,354 B） | 一致 |
| 前端 | 缺失 0；content_diff 63（i18n 包装为主，全归因）；python_only 0；A39 的 ghost 路由/refresh Promise/侧栏加载态/问候 fallback 已登记为 Go 侧改进 | 一致+登记（§7.2） |
| MCP 命名空间 | 默认注册 server 侧裸名（与 Python 同键，不折叠/不消毒）；显式 `mcp.namespace` 才启用前缀模式。13 用例默认模式全一致，前缀模式剩折叠/长名登记；content/server-request/分页边角见 §7.4（R3/R4/R5/R6 已闭合） | **一致（默认）+ 可选模式与边角登记（R1–R2、R7）** |
| 判定语义（judge） | J2 已对齐（瞬态重试/耗尽 fail-open ACCEPT/非瞬态上抛）；边界 5 条登记；colony/queen 已回上游口径（女王 SkipJudge=true，worker/overseer 不接 loop judge） | 已修 |
| memory notes（1.0.15） | notes 推送/recall 索引/log.jsonl/路由/前端全族；探针 51/51、上游 pytest 95 passed | 已修 |
| 恢复与可靠性 | 崩溃恢复 7/7 part 字节一致、events append-only、park 跨重启；浸泡 50/50、SSE 53/53 | 已修（2 项新观察登记） |
| 升级迁移 | v1/v2/v3 三代 home 每 slot 57 项 PASS；旧二进制读回无写入 | 一致 |
| 浏览器渲染 | dpr 注解宽度已修（≤1px）；Skia vs PIL JPEG 字节、字体度量标签盒、alpha 为登记例外 | 已修+例外 |
| exports 基准 | Python=CWD vs Go=$HIVE_HOME（实测矩阵）；`.migrations` 列入 colony 两侧逐字一致 | **待拍板 D2** |
| 版本串 | Go 1.0.26-go.1 / Python 1.0.15（双轨，不进契约） | 登记（既定口径） |
| 安全 | govulncheck 红：stdlib CVE 需 go1.26.6+ | **待拍板 D1** |
| 第三方集成 | 91 个集成「未验证」（无凭据） | 外部未验证 |
| 平台 | darwin/linux 六平台编译+Docker 真跑 20/20；Windows 两平台无实机 | 外部未验证 |

## 1. API / WS

- **路由**：参考 222 条 / Go 225 条。有效缺失 **0**（2 条由别名覆盖：`DELETE /api/sessions/{first}/{second}`、`POST /api/skills/reload` 属 Go 扩展）；Go 扩展 5 条；`/api/sessions/{}/colony` 方法集合差 1（登记）。证据：`contracts/api-routes.json`（reference=python@e5c7603e…）、`docs/api-contract-diff.md`。
- **SSE**：42/42 帧一致（帧级 golden `history-core-equal`）；Go 独有每回合信封 +12 帧为设计差异。证据：`docs/sse-payload-parity.md`。

## 2. 工具 / Skill / MCP

- **工具定义**：desc **48/48** 逐字（live recheck，round 9）；schema 归一 **48/48**、raw 40/48（8 条纯表示差：4 条数值字面拼写 + 4 条参考层 `session_cwd`）。round 9 把 12 条 lifecycle 参考改记运行时层（注册 Tool 对象经 `_tool_to_openai_format` 投影），并机械收口 `browser_setup`（去 `additionalProperties`）与 `task_create`（recheck 安装 DM pivot overlay），此前的 14 条归一化残差全部归零。证据：`docs/tool-contract-diff.md` §3.1/§18、`contracts/tool-definitions.json`、`docs/e2e-assets/schema-residuals/REPORT.md`。
- **Skill**：25 个内置文件逐字节一致（229,354 B），有测试门禁。colony worker/overseer 的目录作用域已对齐上游 `_build_default_skills_config`：framework/default → preset → user（`~/.agents/skills` / external / `~/.hive/skills`）→ queen_ui → colony_ui（含 legacy `.hive/skills`），后扫覆盖先扫；目录提示按 phase `colony` 过滤 visibility；preset 默认关闭，按所属 queen profile 的 `default_preset_skills` 在内存启用（自定义 profile 亦生效）。证据：`internal/colony/skills_surface_test.go`（framework/user/[colony] 入、[independent]/[worker] 出、preset 开关、colony 覆盖 framework、overseer 目录）与 `internal/queen/profiles_test.go`。
- **MCP**：D3 终选 B′（2026-11-23）——默认注册 server 侧裸名（不折叠、不消毒），与 Python 同键；`a-b`/`a_b`、`x-y`/`x_y`、70 字符名、跨 server 同名 first-wins、`preserve_existing_tools=False` 覆写全部与 Python 实跑一致（13 冻结用例默认模式全 PASS）。显式 `mcp.namespace` 非空时启用 Go-only 前缀模式（`mcp__<ns>__<srv>__<tool>`，非 `[A-Za-z0-9_]`→`_`），其折叠不可逆（R1）与长名前缀加长（R2）按原样登记；多 content 块逐项一致（R3 关闭：HTTP 首块非 text 已按 server 键序输出，`tools/list` 分页 Go 侧跟随 `nextCursor`；Python agent 客户端仍单页）。默认路径之外的边角（R4 Go-only `mime_type` 别名/非 string `data`、R5 已修——server 主动 ping/roots/list 由 `bb8cccb1a` 应答、R6 工具定义 `~/.hive` 路径重写仅 Python、R7 Go 扩展面）见 §7.4。证据：`docs/mcp-namespace-residuals.md`、`docs/e2e-assets/mcp-bare-default/REPORT.md`、`docs/reports/mcp-parity-20261125.md`。

## 3. env / configuration.json

- **env**：Python 90 / Go 125 / 共有 84；Python-only 6（GCU 平台，无 Go 读取点）；Go-only 41（Browser Bridge 部署面、MCP2CLI、成本限额、runner `HIVE_LME_*` 与验证脚本开关等）。读取点：Python 139 / Go 106。证据：`docs/env-config-contract-diff.md`、`contracts/env-vars.json`。
- **configuration.json**：137 键 / 22 sections；aligned 128 / go_only 4 / go_partial 4 / unread 1（全部在 `contracts/config-keys.json` 带锚点）。1.0.15 新增 `memory_recall`{mode,wait_seconds}、`memory_reflection`{model,every_turns,long_every} 已对齐。

## 4. CLI / 磁盘 / 迁移

- CLI：hive 47 路径全共有；`queen-mail-mcp` 为 Go 隐藏子命令（Python 走模块入口，功能等价，已登记）。
- 磁盘：会话 `meta.json/state.json/events.jsonl/conversations/parts`、凭据、tracker schema v3 两侧一致；存储契约 19 类自洽（C4）。
- 迁移：v1/v2/v3 三代 home 每 slot 验证（57 项）；Go 写 → 旧二进制读回不写入；exports 基准差异见 D2。

## 5. 语义与特性（本轮变化）

- **judge J2 已对齐**：瞬态重试、耗尽 fail-open ACCEPT、非瞬态上抛；上游边界 5 条登记于 `judge-parity.md §6.2`。colony/queen 生产路径已回退 Hive 1.0.15：女王 spec `SkipJudge=true`，worker/overseer 不接 loop judge，goal 即使声明 criteria 也不继承、不注入 `[Judge feedback]`；通用 judge API 仍保留给 graph-era/显式调用方。
- **1.0.15 memory notes 全族**：notes 推送（index/wait）、recall 索引（冷建让路 3s、删除可重建）、`log.jsonl` 历史/恢复、8 条 `/api/memories` 路由、Settings→Memory、用户自有标记、笔记年龄=最后确认日。
- **恢复**：kill -9 后冷恢复 7/7 part 字节一致、events 29→29 append-only、pre-crash 工具结果进入上下文；`ask_user` park 跨两次重启保留。
- **并发**：10 会话 × 5 轮 50/50，0 错误；RSS +12MB；SSE 53/53 无丢帧。
- **长等待/并行工具**：A 表 10 项、B 表 7 项实测；修复 colony worker 等待 60→600s。
- **新登记（非阻断）**：① cold-resume mid-turn guard（`cold_interrupted` 无生产者）② resume 后快照/SSE 首帧空态。两条由 `resume-guards` lane 收口中。
- **凭证校验已闭环（lane `cred-check`）**：`POST /api/credentials/check-agent` 由 500 占位改为上游语义全实现（presence + 健康检查两阶段、Aden 平台行、`alternative_group` 任一满足、14 字段响应 + `has_aden_key`）；`POST /api/credentials/resync` 生产 hook 接线为本地默认（Aden 云端 token 拉取仍缺，P2 `credentials/aden/client.py`）。前端 CredentialsModal 真实打开不再报错。证据 `docs/e2e-assets/credentials-check-agent/`、`docs/unported-triage.md` 末节。
- **`context_compacted` → long reflection 已接线（lane `memory-compaction-reflect`，2026-11-23）**：此前 Go 侧 `ReflectionScheduler.OnCompaction` 全仓无调用点、agentloop 也不发布 `CONTEXT_COMPACTED`，压缩丢内容前不会先沉淀记忆。现由 loop 在 LLM 压缩成功路径产出上游同字段 payload（`level`/`usage_before`/`usage_after`/`tokens_before_k`/`tokens_after_k`，取整为 Python `round` 半偶口径；`ratio_before>=1.0` 时才附 `message_inventory`），chat runtime 以 queen 流订阅 session bus 并调用 `OnCompaction`（反思进行中/Closed 由 scheduler 跳过）。**残余**：① `message_inventory` 的键与形状同上游，但 `content_chars`/`preview` 用 Go 的字节长度（`len(string)`），非 ASCII 内容与上游 Python 的字符长度不同；② Go 的 `compactConversationIfNeeded` 没有 microcompact/prune 阶段（proactive microcompaction 在 `add_tool_result`，上游该路径同样不发事件），因此不存在“仅 microcompact 的 compact() 完成”事件形态；③ worker loop 未接 `OnContextCompacted`（只接 queen，与上游 reflection 订阅口径一致）。
- **触发器（timer/webhook）字段级契约落盘（2026-11-23）**：定义/请求/响应/事件/注入五层字段的类型、必填、默认与语义逐条记录于 `docs/triggers-webhook.html`（v3）；cron 已升级为 croniter 6.0.0 全语法兼容（2026-11-25，`9a9efbe8f`；126 表达式/4,911 步双跑，W 等 croniter 本身不接受的 Quartz 语法两侧仍拒绝）。**登记差异**：① 非 object JSON body payload——**已修**（lane `webhook-payload`，3597215d；数字 int/float 与响应精确字节已于 2026-11-25 补齐，`4978b19c1`；仅剩 NaN/Infinity 走 raw_body）；② 多触发器注入形态——**已修**（lane `trigger-batch`，1e8ef0a1，批合并单 stamp）；③ 重复同名头/query 取首值 & body 上限 1MiB（≥1MiB 回 400，无 413）——**已实测对齐**（lane `webhook-semantics`，a91ae182；残 2 条形态微差：头名拼写/超限 drain）。

## 6. 证据与质量

- parity **5,833/5,833**；C12 **273**（220/27/9/17：四 lane 并集）；config **131 aligned / 4 go_only / 1 go_partial / 1 unread**；QC **9/9**；compat **12/12**；arch **87 包/234 边**（P3 五族拆包 + chat-images）；govulncheck **0 OSV**。
- 性能：`perf-recall-15k`（15,000 轮冷建/warm/搜索/冷启动/RSS）产出中，落 `docs/perf-recall-15k.md`。
- 大样本行为：LongMemEval n=200 四组（`e2e-assets/longmemeval/2026-11-22-200q-real-run.md`）；配对检验均不显著，新增 140 题 126:126。

## 7. 登记差异（有意保留，带复查条件，2026-11-24）

> 本轮 A17/A18/A20 残差收口时，把分包审计标为“未登记”的三项**有意保留**差异登记在此；A17/A18/A20 的已修部分见下节。

| # | 面 | 差异（Go vs Python） | 保留理由 | 复查条件 | 证据 |
|---|---|---|---|---|---|
| **A15** | Credentials | Go 凭证库目录/文件固定 0700/0600 且写入走 atomicfs；Python 依赖进程 umask（通常 0755/0644）且非原子写。索引损坏时 Go 从 `.enc` 重建并上抛索引写失败；Python `_read_index` 返回空骨架、下一次 upsert 可能丢其他条目。 | 安全与可靠性加固：私密目录不随进程 umask 放宽；派生索引损坏不得导致后续保存覆盖全部凭证。两侧都把 `.enc` 文件视为权威数据。 | 上游改变凭证目录权限口径或把索引改为权威数据源时；共享 HIVE_HOME 的互读冒烟出现权限/索引回归时。 | `internal/credentials/storage.go`（`ensureDirs`/`tightenDirs`/`tightenFileModes`/`readIndexLocked` 的 A15 Parity note）；`internal/credentials/storage_a15_test.go` |
| **A30** | Skills | Go 的 `hive skill install` 额外校验安装名必须是单一路径段（拒绝 `/`、`\`、NUL、`.`、`..`）并把 `--subdir` 限制在 clone 内；Python 直接按调用方给的名称/子目录落盘。 | 路径穿越加固：CLI 安装参数由调用方控制，Go 侧拒绝逃逸目标目录的输入。 | 上游为 skill 安装也加同等校验，或产品要求接受越界名称时。 | `internal/skills/installer.go`（`ValidSkillName` 分支、`resolveInstallSource`）；`internal/skills/installer_test.go`（Parity-exempt 安全用例） |
| **A31** | Skills/Agents | Go worker 定义扩展支持 `.yaml`/`.yml`、对象形 `tools`/`node_type`、顶层 `max_tokens`/`conversation_mode`；Python 的 `AgentLoader.load` 只读 JSON 的既有形状。 | Go 扩展：yaml.v3 已是依赖，手写 agent 包无需先转 JSON；对象形字段沿用上游运行时的宽松读取，不改变 JSON 路径的结果。 | 上游支持 YAML worker 定义，或收紧 worker schema 时。 | `internal/agents/agent_spec.go`（`workerSpecExtensions`、`workerSpecTools.UnmarshalYAML`、顶层 `MaxTokens`/`ConversationMode` 字段） |
| **A39** | Frontend | ghost 路由（Python 永久 Connecting vs Go `Colony not found`）、`ColonyContext.refresh` 返回 `Promise`、侧栏加载态、问候 fallback；7 个 Go `t()` key 在 B 侧以表达式/模板出现。详见 §7.2。 | Go 侧可用性修复：未知 colony 路由必须结束加载态；`refresh(): Promise` 是让「查找已结束」可判定的最小契约扩展。B 树无 i18n 层，且回流会改变 B 的 `ColonyContext` API 与 React 渲染时序，没有安全 backport。 | B 引入 i18n 层、修复 ghost 路由或修改 `refresh` 契约时；届时同步删除本登记。 | `core/frontend/src/pages/colony-chat.tsx:315-322,933,2287`；`core/frontend/src/context/ColonyContext.tsx:51,395`；`go_hive/go-hive/frontend/src/pages/colony-chat.tsx:339-375,2447-2452`；`go_hive/go-hive/frontend/src/context/ColonyContext.tsx:54,432`；`i18n_coverage.py`（missing 0） |
| **A40** | Pipeline / Trigger | `internal/pipeline` 机制与 `ColonyRuntime.Trigger` 已按上游移植且有测试，但两侧都没有生产调用方：上游 `trigger` 只由 `trigger_and_wait`/未注册的 colony timer 调用，生产 server 走 queen 注入；Go 的 `handleRunTrigger`/`StartTriggerTimer` 同样注入 queen。Go `serve --colony` 启动时会读取 `configuration.json` 的 `pipeline.stages`（`internal/colony/runtime.go:348`/`:640`），上游 session colony 显式 `pipeline_stages: []`（`session_manager.py:1777`）——这是 Go-only 启动面差异。 | 保留 API surface：嵌入方仍可使用 `ColonyRuntime.Trigger`；生产行为以 queen 注入为准，避免改变 `handleRunTrigger` 的 spawn-vs-inject 语义。启动面差异保留，因为 `serve --colony` 是 Go-only 装配（`loader_colony_spec_test.go`）。 | 上游把 `register_trigger`/colony timer 或 `AgentHost` 接回生产 server 时；或 Go 新增 colony timer/API 触发面时。 | `docs/pipeline.md §7`；`internal/pipeline/reachability_test.go`；上游锚点见该节 |
| **A41** | Tools (go-only) | Go 新增原生 HTTP 工具家族（http_request/http_download/http_upload），Python 无对应工具；配置只读 `$HIVE_HOME/http_tools.json`，cookie 按 Hive 会话隔离，v1 不落盘。 | 替代 `terminal_exec` + curl：无二进制依赖，内置 SSRF/重定向/上限/脱敏护栏；不改变参考工具面（builtin 14 / flat 39 / 首轮 24 不变），工具只进 lazy 层。 | 上游引入等价工具，或产品要求 Python 侧同面时。 | `internal/nativehttp`、`internal/netguard`、`internal/tools/builtin/http_tools.go`、`docs/http-tools-design.json` |
| **A42** | Tools (go-only) | Go 新增只读仓库 AST 分析 `ast_analyze`（unit/依赖图/wave/风险/评分/start_here），Python 无对应；配置只读 `$HIVE_HOME/ast_analyze.json`，path 绝对且根内。 | Go-only 代码智能能力；不改变参考工具面（builtin 14 / flat 39 / 首轮 24 不变），只进 lazy 层。 | 上游引入等价工具，或产品要求 Python 侧同面时。 | `internal/astanalyze`、`internal/tools/builtin/ast_analyze.go`、`docs/ast-analyze-design.json` |

### 7.1 本轮闭环（A5/A17/A18/A20 残余）

- **A5**：turn 级重复批次指纹窗口已闭环。`internal/agentloop/doom_loop_window.go:17-19` 声明上游 `fingerprint_tool_calls` / `is_tool_doom_loop` / `_execute_impl` 6f' 锚点，`:69 ObserveTurn` 维护 `tool_doom_loop_threshold` 长度的有序批次窗口；`loop.go:1151` 在每批工具结果落盘后观测，`loop_turn_steps.go:153-164` 分流：worker 升级（`loop_guards.go:211 settleDoomLoopWindow` → `escalateDoomLoopDescription`）、queen park（`parkOnDoomLoop`）、其余仅警告。事件面：`loop_guards.go:129 reportDoomLoop` 每 turn 只报一次，`doom_loop.go:213 DoomLoopSink` 携带检测自身描述；worker 由 `colony/worker_doom_loop.go:32-56` 发 `node_tool_doom_loop`（`eventbus/event_type.go:65`），queen 由 `server/chat_loop_guards.go:100-113 publishLoopPark` 发同一事件；`server/chat_routes.go:351` 已在事件历史白名单。证据：`internal/agentloop/doom_loop_window_test.go`（批次重复/顺序敏感/文本回合断链/queen park/事件与 park 描述）、`internal/colony/worker_escalation_test.go`、`internal/server/chat_doom_park_test.go`、`internal/server/chat_doom_window_test.go`。**残余**：queen 的窗口 park 走 server `pending_input` 的有界 turn park/resume，非 Python 的 turn 内 `_await_user_input` 阻塞；窗口不随冷启动 mid-turn resume 恢复（Python 从 cursor 恢复 `recent_tool_fingerprints`）。
- **A17**：colony scaffold 只写 `metadata.json` + `worker.json`（不再多建 `skills/`、`runs/`、`triggers.json`）；`queen_mail.claude_code` 的配置顺序由 `queenmail.LoadConfig` 保留，correspondents 与 `/api/claude-code-queens` 均按配置序渲染（解码后的裸 map 才回落排序）。证据：`internal/server/routes_colonies_test.go`、`internal/queenmail/config_order_test.go`、`internal/server/claude_code_queens_test.go`。
- **A18**：skill watcher 从 pipeline stage 扩到 colony runtime（`internal/colony/skills_surface.go`）与 queen catalog（`internal/server/skills_watcher.go`）；pipeline 的 LLM resolver 接入订阅 token 回退（Claude Code / Codex / Kimi Code / Antigravity，`internal/hiveconf/subscription.go`）；`accounts_prompt` 与 MCP stage 的 worker 工具面已在 `363d04a7` 接线。证据：`internal/colony/skills_watcher_test.go`、`internal/server/skills_watcher_test.go`、`internal/pipeline/subscription_test.go`。
- **A20**：`RuntimeConfig` 聚合落 `internal/server/runtime_config.go`（model / 0.7 temperature / max_tokens / max_context_tokens / api_key / api_base / extra_kwargs），主槽 provider 构造走该聚合；`HIVE_HOME` 展开与 `JSONInt` 的整数/整值浮点/布尔语义已在 `546f5914` 对齐。温度默认值只作为 dataclass 字段保留，不注入请求体——上游 `build_llm` 同样不传，请求体以 provider 默认为准。证据：`internal/server/runtime_config_test.go`、`internal/hiveconf/hiveconf_test.go`。

### 7.2 A39 前端语义差（Go 侧改进，Python 不回流）

**Ghost 路由（未知 `#/colony/<id>`）**

- Python：`core/frontend/src/pages/colony-chat.tsx:315-322` 的 `refreshColonies()` 是 fire-and-forget，且 `refreshAttemptedRef` 只跑一次；`loading` 初值为 `true`（`:232`）；`loadSession` 的 effect 在 `agentPath` 与 `isNewChat` 都为空时于 `:936` 直接 return，`loadSession` 不会被调用，因此 `:655-661` 的清 `loading` 分支对 ghost route 不可达；not-found 分支又要求 `!agentState.loading`（`:2287`）。结果是页面永久停在 `Connecting to agent...`，直到手动刷新浏览器。
- Go：`go_hive/go-hive/frontend/src/pages/colony-chat.tsx:339-375` 用 `colonyLookupSettledFor` 记录「该路由的查找已 settle」，`colonyMissing` 只在 settle 且仍无 colony 时为真；`:2447-2452` 渲染 `Colony not found: <id>`，`:537-539` 在 missing/pending 之间同步 `loading`，不会永久转圈。`ColonyContext.refresh` 由 `void` 改为 `Promise<void>`（`go_hive/go-hive/frontend/src/context/ColonyContext.tsx:54,432`；Python `core/frontend/src/context/ColonyContext.tsx:51,395` 仍为 `void`），这是判断「查找已结束」的最小契约扩展。
- 侧栏空态：Go 在列表加载中显示 `Loading colonies...`（`components/Sidebar.tsx:527`），Python 只有 `No colonies yet`（`core/frontend/src/components/Sidebar.tsx:522`）。问候 fallback：Go 拆成 `t("Hey {name}, ...")` / `t("Hey there, ...")` 两个 key（`pages/home.tsx:466-467`），Python 直接模板 `Hey {displayName}, ...`（`:463`），语义等价。

**i18n 键**

- B 树（`core/frontend`）没有 i18n 层：0 个 `useI18n` 文件、0 个字典键（`i18n_coverage.py` 的 tree B 证据）；A 树（`go_hive/go-hive/frontend`）当前 1251 个 zh 键。HEAD 重跑 `i18n_coverage.py`：**missing translations 0**（此前的 8 条三元分支键已由 `000fbeca6` 补齐）。
- 刷新报告所称「home/McpServersPanel 7 个 i18n 缺键」不是缺翻译，而是 7 个 Go `t()` key 的英文文案在 B 侧通过 JSX 表达式、模板拼接或多行换行渲染，字面搜索找不到：`Hey {name}, what can I help you with?`、`Hey there, what can I help you with?`（`home.tsx:466-467`）；`Agents run "hive mcp run {name} …" in the terminal`、`Remove MCP server "{name}"?`、`in the terminal. Its tools stay out of every prompt until a command is used: best for servers with many tools.`、`{count} commands`、`{count} tools`（`McpServersPanel.tsx:198,481,639,645`）。这 7 条在 Go zh 字典中均存在；B 没有 zh 字典可补，所以 Python 侧无需（也无法）同步。

**决定**：保留 Go 行为，不向 `core/frontend` 回填。理由：B 无 i18n 层；ghost 修复依赖 `refresh(): Promise` 与 settled/pending 渲染状态机，回流会改变 B 的 `ColonyContext` 公共契约与 React 渲染时序，属于行为性重写而非安全 backport。复查条件：B 引入 i18n 层、修复 ghost 路由或修改 `refresh` 契约时，同步删除本登记。

### 7.3 D-1 / D-2 / S-2 闭环（2026-11-25）

- **D-1**：`internal/server/profile.go::SyncUserProfileMemory` 从 `os.WriteFile` 改为 `memory.Write(memDir, "user-profile.md", content, memory.WriteOptions{Author: memory.AuthorOwner, Reason: "profile settings", KeepAuthor: true})`。恢复 frontmatter、`log.jsonl`、`MaxFileSizeBytes` 与 owner 守卫；镜像失败仍被吞掉，profile PUT 不因此失败。测试覆盖 owner 保留、日志条目、超限不落盘且 PUT 200。提交 `bebedf60d`，合并 `deaa5ff7a`。
- **D-2**：`internal/server/chat_memory_wiring.go::chatMemoryReflectionConfig` 改用 `reflectionPositiveInt` + `strictJSONInteger`；只接受整数形 `json.Number`（`Int64()` 成功）与 Go `int/int64`，拒绝 bool、字符串、`2.0`、`1e1`、0、负数和溢出。提交 `f63947afe`，合并 `89a5d3ff5`。
- **S-2**：`internal/server/chat_colony_dispatch.go::sinkColonyPivot` 恢复上游状态检查：`ForkKickoffPending` 返回 fork-kickoff 否决文本；runtime 或 session 已有 pending pivot 返回 concurrent-popup 否决文本；否则 stash。文案逐字对齐 `_make_pivot_payload_sink`。提交 `bf930f68b`，合并 `1a125476c`。
- 回归：`go test ./internal/server ./internal/memory` PASS；`make parity` 6,453/6,453；全量 CI 见 `docs/reports/alignment-closeout-20261125.md`。
### 7.4 MCP 边角项（2026-11-25，第二轮复核）

> 默认 MCP 路径（裸名、工具定义、调用、连接生命周期、注册表）与 Python 一致；以下 R4/R5/R6 已闭合、R7 为有意扩展且已通过测试验证，保留行内记录供追溯，详见 `docs/reports/mcp-parity-20261125.md`。

| # | 面 | 差异（Go vs Python） | 保留理由 | 复查条件 | 证据 |
|---|---|---|---|---|---|
| **R4** | MCP content | **已修复（2026-11-25，w173）**：`SplitMCPContent` 改为 Python `_split_mcp_content` 的逐行移植——只认 camelCase `mimeType`（删除 `mime_type` 别名）、按字段存在性（`hasattr` 语义）判定 image 分支、非 string 值按 Python `str()` 拼写（`None`/`True`/`False`/数字）。`{data, mimeType: null}` → `data:None;base64,…`，与探针 `split.null_mime` 逐字一致；合法 `resource_link` 仍两侧丢弃。 | 已对齐，不再作为差异保留。 | MCP 规范引入 `mime_type` 别名，或真实 server 返回非 string `data` 时。 | `internal/tools/mcp_client.go::SplitMCPContent`；`internal/tools/mcp_namespace_parity_test.go`（`TestMCPSplitContentNullMimeTypeMatchesPython` 等 5 条） |
| **R5** | MCP 客户端 | **已修复（2026-11-25，`bb8cccb1a`）**：Go stdio `readLoop` 现区分 server 主动请求并回复——`ping` → `{}`，`roots/list` → `-32600 "List roots not supported"`，sampling/elicitation → `not supported` ErrorData，未知方法 → `-32602`；与 Python mcp 1.26.0 `ClientSession` 默认行为一致（旧行为：无 pending 的 server request 被丢弃）。 | 已对齐，不再作为差异保留。 | 真实第三方 server 主动发 ping/roots/list，或 MCP capability 协商变更时复查。 | `internal/tools/mcp_client.go`；`internal/tools/mcp_server_requests_test.go`（单元 + read-loop + 子进程 fixture，`TestMCPStdioServerRequestsEndToEnd`） |
| **R6** | 工具定义 | **已修复（2026-11-25，w173）**：Go 在 `ToolToOpenAIFormat`（与 Python `_tool_to_openai_format` 同一 LLM 边界）对 OpenAI function 字典做 `resolve_hive_paths_deep`，把每个字符串值里的 `~/.hive` 替换成实际 `HIVE_HOME`；`HIVE_HOME` 不可解析时原样返回，且不原地修改 registry 的 `ToolDef`。 | 已对齐，不再作为差异保留。 | Go 工具定义层改变投影边界时。 | `internal/llm/tool_descriptions.go`、`internal/llm/provider.go::ToolToOpenAIFormat`、`internal/llm/tool_descriptions_test.go` |
| **R7** | Go 扩展 | `POST /api/mcp/reload`、`HIVE_MCP_CALL_RESULT_TIMEOUT > configuration.json mcp.timeout_seconds > 240s`、`GET /api/mcp/servers` 的 `tool_names`、native mcp2cli、注册表 0700/0600。**2026-11-25（w173）补齐可执行证据**：权限扩展新增 `TestInitUsesPrivatePermissions`，其余四项沿用既有测试并在 `docs/reports/mcp-parity-20261125.md` §5 的验证命令里实跑。 | 可用性/安全加固；不改变默认契约。 | 上游引入对应能力，或产品要求统一 API 形状时。 | `internal/server/routes_mcp.go`；`internal/cli/mcp_config.go`；`internal/mcpregistry/nativecli`；`internal/mcpregistry/registry.go`；`internal/mcpregistry/registry_test.go::TestInitUsesPrivatePermissions` |

- **仍未对齐**：S-1（无公共 SDK）、S-3（channels 仅 stdio；Python/Go 语义对照见 `docs/channel-plugin-semantics.html`）、V-1/V-2 验证缺口、契约快照/未移植面仍按该报告列出的范围开放。

