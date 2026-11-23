# Memory 管理机制 —— 需求 / 设计 / 实现（go_hive ⇄ Hive 1.0.15）

> 范围：queen 的**长期记忆**（memory notes）及其读写通道：reflection 写入、变更日志与所有权、三模式召回、past-conversations 摘录、timeline、Memory API/UI，以及它们与上下文压缩、soul 的边界。
> 不在范围：soul（见 `docs/soul-spec.md`）、skills（见 `07-memory-and-skills.md §4`）、tracker/colony 数据库。
> 口径：以代码为准；Python 行号指 `core/framework/...`，Go 行号指 `go_hive/go-hive/internal/...`。文中数字均由代码常量得出，改动后需同步本文件与 `memory-and-soul.html`（生成器会重算部分数字）。

## 1. 需求

### 1.1 功能需求

| ID | 需求 | 验收面 |
|---|---|---|
| M1 | **文件式长期记忆**：一个记忆 = 一个 `.md` 文件，带 `name/description/type` frontmatter；无向量库 | 磁盘布局、`/api/memories` 列表 |
| M2 | **四个作用域**：`global/`、`colonies/<name>/`、`agents/queens/<name>/`、`agents/<worker>/`；只有 global + 本 queen 参与自动召回 | 目录、recall 扫描范围 |
| M3 | **硬上限**：每作用域 ≤200 文件、单文件 ≤4 KB；超限写入被拒绝而不是截断 | 写入错误、扫描截断 |
| M4 | **自动写入**：queen 每 6 个完成回合短反思、每 4 次短反思长反思、压缩后立即长反思、会话关停补短反思 | 触发计数、`state`/日志 |
| M5 | **写入是增量、单主题**：更新已有文件优先于新建；禁止把任务细节/文件路径/临时状态写进记忆；全局身份固定 `user-profile.md` 且保留 `## User Identity` | 反思提示词、文件内容 |
| M6 | **所有权与保护**：每次写入记录 `author(owner\|queen\|reflection)`；owner 写过的文件 queen/reflection 不能覆盖或退休；种子创建不占有 | 写入拒绝文案、`author` 字段 |
| M7 | **全量变更日志**：每次 write/retire/restore 追加 `log.jsonl`（when/who/why/sources/before/after）；history 返回 0-based `n`；可按 `n` 恢复 | `GET /api/memories/history`、`POST .../restore` |
| M8 | **召回三模式**：`index`（默认，全量一行清单+自取）、`wait`（每消息挑选 ≤3/作用域，≤10s）、`background`（回合开始后挑选、缓存注入） | 配置、注入块、时序 |
| M9 | **召回注入是 system-reminder**（不是系统提示、不进缓存前缀）；内容未变不重复注入；压缩后重置 | 注入文本、缓存行为 |
| M10 | **past conversations**：会话开始与每条用户消息前注入最多 6 段相关历史摘录（用户/助手各 500 字符预算；3s 超时；<8 字符消息不触发） | 注入块、超时日志 |
| M11 | **timeline（可选）**：从用户消息抽取带日期条目（kind=event/fact/plan），落 `timeline.jsonl` + `timeline_meta.json`；`search_timeline` 检索；默认关闭 | `memory_timeline` 开关、文件 |
| M12 | **Memory 管理面**：列表/读取/编辑/删除（退休）/历史/恢复/已退休；编辑以 owner 记账；无 UI 新建按钮；API 有幂等种子创建 | `/api/memories*`、Memory 页 |
| M13 | **配置热更新**：`memory_recall`/`memory_reflection`/`memory_timeline` 每条消息读取；`HIVE_MEMORY_RECALL` 覆盖配置 | 改配置不需重启 |
| M14 | **失败隔离**：反思/选择器/历史召回任何失败都不得阻塞主对话；反思错误落 `.reflection_error.txt` | 错误路径、对话继续 |
| M15 | **压缩联动**：压缩完成发 `context_compacted` → 立即 long reflection（在内容被丢弃前沉淀） | 事件、反思触发 |

### 1.2 非功能需求

| ID | 要求 |
|---|---|
| N1 | 召回在关键路径上的开销：`index` 模式**无模型调用**；`wait` 最多等 `wait_seconds`（默认 10s）；`background` 不在关键路径；past recall 超时 3s |
| N2 | 单文件 4 KB / 目录 200 是**硬门**（写入工具与 API 都拒绝），避免上下文膨胀 |
| N3 | 所有写盘原子（temp+rename），进程崩溃不产生半文件 |
| N4 | 日志 append-only，删除可恢复；owner 保护不可被模型绕过 |
| N5 | 路径安全：API 只接受 `memories/` 下、`..`/绝对路径拒绝、仅 `.md`；反思工具文件名禁路径分隔符 |
| N6 | 内容进入 prompt 视为**可信输入**：记忆由 owner/反思产出；terminal 对 `memories/` 无沙箱（登记边界，见 §6） |
| N7 | 跨语言一致：Go 与 Python 的文件格式、日志行、API 字段、注入文案、配置键逐字/逐字节对齐 |
| N8 | 可观测：反思错误文件、日志、`last_run/last_result`、API 计数；不做额外指标上报 |

### 1.3 用户可见验收（缺一不可）

1. 磁盘：文件与 frontmatter 形状、200/4KB 上限；
2. API：字段/错误码/文案（`list/read/create/update/delete/history/restore/retired/config`）；
3. UI：Memory 页分组（Global/Queens）、编辑/删除/历史/恢复、owner 标记；
4. 模型侧：`index` 清单、`wait`/`background` 注入块、past 摘录、timeline 工具输出。

## 2. 设计

### 2.1 组件

```
                  ┌──────────────────────────── 读取（每回合） ─────────────────────────────┐
 用户消息 ──►  Recaller(index: 清单 | wait: LLM 挑选 | background: 后台挑选) ──┐            │
                  │        PastRecall(会话/消息前，≤6 段，3s)                    ├─► <system-reminder>
                  │        Timeline(可选，search_timeline 工具)                 ─┘            │
                  └───────────────────────────────────────────────────────────────────────────┘
                  ┌──────────────────────────── 写入（回合之后） ─────────────────────────────┐
 AgentLoop ──turn_done(每6)──► ReflectionScheduler ──short──► 反思代理（模型槽 worker）
     │            └─context_compacted──► long ──► 工具 write/delete_memory_file
     └─session stop ──► final short                     │
                                                        ▼
                                              memory_log.write/retire
                                              ├─ frontmatter: author/learned/confirmed
                                              ├─ 作用域 log.jsonl（before/after/reason/sources）
                                              └─ 原子写 note 文件
 Owner ──/api/memories──► 同一 memory_log（author=owner）──► owner 保护
```

### 2.2 存储模型

- **目录**：`$HIVE_HOME/memories/{global,colonies/<name>,agents/queens/<name>,agents/<worker>}/`
- **扫描**：每目录按 mtime **从新到旧**取 ≤200 个 `.md`（`ScanMemoryFiles`，`memory.go:145`）；读取 frontmatter + 前 30 行（`HeaderLineLimit=30`）。
- **文件**：`---` frontmatter（`name`、`description`、`type`，以及写入器维护的 `author`、`learned`、`confirmed`）+ 正文；`build_memory_document` 生成 `type` 默认 `profile`。
- **类别**：global 的 `type` 限定 `profile/preference/environment/feedback`（反思工具拒绝其它值；API 不强制枚举，默认 profile）。
- **命名**：kebab-case `.md`；反思工具禁 `/`、`\`、`..`；全局身份固定 `global/user-profile.md`（`## User Identity` 由 Settings 管理，反思不得改写）。

### 2.3 所有权模型

| author | 谁写 | 可否被清理 |
|---|---|---|
| `owner` | Memory 页 / owner API（PUT 编辑后 claim） | **否**：queen/reflection 写入或退休会被拒绝（`… was written by the owner; write what you learned to another file and tell them where the two differ`） |
| `reflection` | 反思代理（write/delete） | 可（长反思可合并/删除） |
| `queen` | 预留（当前无产品写入口） | 可 |
| 无 author | 旧文件或“种子”（POST create，claim=false） | 可；owner 首次编辑时被认领 |

`learned` 首次写入日、不再后移；`confirmed` 在“有对话支持”的更新时移到今天，纯整理（`confirms=false`，长反思）保持不动；召回年龄按 `confirmed`（缺失回退 mtime）。

### 2.4 写入设计（reflection）

- **触发**（`ReflectionScheduler`）：
  - `OnTurnDone`：`stream_id=="queen"` 且 `park_reason ∈ {turn_done, ask_user}` 才计数；`turns % every_turns == 0` 触发 short；`reflections % long_every == 0` 追加 long；
  - `OnCompaction`：压缩完成 → 只跑 long；
  - `Close(grace)`：停止接收新触发，等最多 grace，然后跑一次 final short；
  - 反思进行中到达的触发**跳过**（单飞锁）；失败写 `.reflection_error.txt` 与日志，不标记完成。
- **模型槽**：`memory_reflection.model`：`worker`（默认，`worker_llm` 槽；未配置时用会话模型）或 `main`；每次反思重新解析，改配置即时生效。
- **短反思**（提示词全文在 `reflection.go:555`）：目标 2 回合——先 `list_memory_files` + 读疑似文件，再 `write_memory_file`/`delete_memory_file`；规则：一条学习只做一个作用域决定、全局优先、避免重复、单主题、不写任务细节、`user-profile.md` 特例、≤4KB/200。
- **长反思**（`reflection.go:614`）：housekeeping——去重（跨作用域）、移动错位、合并优先于删除、清理过期、只整理不发明；删除/合并走 `confirms=false`，纯整理不刷新 `confirmed`。
- **工具**（4 个，schema 由 `reflection_tools_contract_test.go` 冻结）：`list_memory_files(scope?)`、`read_memory_file(filename, scope?)`、`write_memory_file(filename, content, scope?)`、`delete_memory_file(filename, scope?)`；**不在 queen 的主工具注册表**。
- **限额**：写工具在目录达 200 时拒绝新建；内容超过 4 KB 直接报错；文件名非法报错。

### 2.5 变更日志设计（memory_log）

- 每次变更由唯一入口 `Write / Retire / Restore` 完成：
  - `before` 读旧文、`after` 写新文（原子），frontmatter 更新 author/learned/confirmed；
  - 追加 `<scope>/log.jsonl` 一行：`{at(local ISO, numeric offset), op, file, author, reason, sources:[{quote,where}], before, after}`，键序固定（Python `json.dumps` 形状）；
  - `History` 在每行加 `n`（0-based 行号，坏行也计数），用于稳定回滚；
  - `Retire` 移出文件但正文留在日志；`Restore(n)` 把该次变更的 `after`（退休则 `before`）原样写回，并作为 owner 变更再记一行。
- 并发：进程级互斥（Go `memoryLogMu`；Python 同等）。

### 2.6 读取设计

| 模式 | 触发点 | 行为 | 上限/超时 |
|---|---|---|---|
| `index`（默认） | SESSION_START、USER_PROMPT_SUBMIT、POST_COMPACT | 不做挑选：给她**全部笔记的一行清单**（`[type] filename: description`），会话开始/压缩后全量、之后只给变化；她自己 `terminal_exec` 读全文 | 无模型调用 |
| `wait` | USER_PROMPT_SUBMIT | 选择器 LLM 只看**用户这句话**，从清单挑笔记，本轮**等待**结果后再回答 | 每作用域 ≤3，整体 ≤10s（`wait_seconds`），超时本轮不带 |
| `background` | USER_PROMPT_SUBMIT | 先投递**缓存的上一次挑选**（未变不重复），再后台刷新缓存供下一条消息使用 | pick 不在关键路径 |

- **选择器**（`recall_selector.py` / `recall.go`）：system prompt 要求“只挑确定有用的、最多 5 条”；输入是 `## User query` + `## Available memories`（manifest）；输出 JSON `{"selected_memories":[…]}`；容忍 `.md` 后缀/裸 slug；**任何失败/非 JSON → 回退到 `type: profile` 的 pinned 笔记**，绝不阻塞。
- **并发**：两个作用域并行挑选（`asyncio.gather` / goroutine），顺序固定 global→queen。
- **注入**：`<system-reminder>` 用户消息；`RECALL_HEADER` 文案“supersedes earlier recall reminders”；`index` 用 `INDEX_HEADER` / `INDEX_CHANGED_HEADER`；内容未变不重复（`_last` 去重），POST_COMPACT 重置。
- **作用域**：只有 global + 本 queen 进入自动召回；colony/agent 目录有布局、可经 API 浏览/编辑，但没有召回消费者。

### 2.7 past conversations

- 数据：每条消息的会话事件（`events.jsonl`）→ turn index（按关键词检索）；一条已命中会话可再带回最多 2 个匹配回合（总数上限 4）。
- 预算：用户/助手各 **500 字符**；窗口选“包含最多查询词”的片段；最多 **6 段**。
- 注入：SESSION_START + 每条 USER_PROMPT_SUBMIT；消息 <8 字符不触发；检索 ≤**3s**，超时本轮不带（后台可继续建索引）。
- 失败/异常一律静默，不阻塞对话。

### 2.8 timeline（可选）

- 开关：`memory_timeline` feature flag，**默认关闭**（`boolConfigFlag(..., false)`）。
- 文件：每会话 `timeline.jsonl`（`schema_version=1`，kind ∈ `event/fact/plan`）+ `timeline_meta.json`（游标/已处理）。
- 抽取：用户消息 → 批（≤**12,000** 字符/批，单消息截 **2,000**）；live 触发=新消息 ≥**3** 或冷却 **300s**；会话 stop flush；可 backfill。
- 检索：`search_timeline` 工具；条目带日期，回答“何时发生”。

### 2.9 与压缩、soul 的边界

- **压缩**：记忆不参与摘要；压缩完成发 `context_compacted` → long reflection 先沉淀；recall 在 POST_COMPACT 重新全量（index）/重置缓存（wait/background）。
- **soul**：独立层（第一人称自述，每日修订，见 `soul-spec.md`）；Go 额外有 `sediment`（memory 反思结论 → soul，`Parity-exempt` 登记）；memory 本身不写 soul。
- **tracker/skills**：不同存储与通道，互不读写。

### 2.10 安全与隐私

- API 路径校验是唯一瓶颈：拒绝绝对路径、`.`/`..`、非 `.md`；解析后必须仍在 `memories/` 根内。
- 反思工具文件名禁路径分隔符；落盘后 frontmatter author 保护 owner 文件。
- **登记边界**：terminal 对 `memories/` 无专门沙箱（技能目录沙箱同样登记为待办），所以“queen 硬写文件”技术上可行但不在设计通道；记忆内容进入 prompt 属可信输入，owner 可用 API 审计/撤销。

## 3. 实现

### 3.1 Python（参考）

| 关注点 | 文件 | 关键点 |
|---|---|---|
| 目录/扫描/限额/清单 | `agents/queen/queen_memory_v2.py` | `MAX_FILES=200`、`MAX_FILE_SIZE_BYTES=4096`、`HeaderLineLimit=30`、`format_memory_manifest` |
| 变更日志/守卫 | `agents/queen/memory_log.py` | `write/retire/restore/history/retired`、`OwnerWritten`、`author_of/confirmed_on` |
| 反思 | `agents/queen/reflection_agent.py` | 短/长提示词、4 工具、触发器 `subscribe_reflection_triggers`、shutdown |
| 选择器 | `agents/queen/recall_selector.py` | LLM 挑选、pinned 回退、`build_scoped_recall_blocks` |
| 注入源 | `agent_loop/memory_reminders.py` | `MemoryIndexReminderSource` / `MemoryRecallReminderSource`、headers |
| Past 召回 | `tools/src/memory_tools/recall.py`、`turn_index.py` | BM25(k1=1.2,b=0.75)、TOP_K、预算、扩展 |
| Timeline | `agents/queen/timeline.py` | 常量与抽取提示词、live/stop/backfill |
| API | `server/routes_memories.py`、`routes_config.py` | 8 路由 + `/api/config/memory` |
| 云同步 | `cloud_sync_hooks.schedule_push("memory", rel)` | 仅 `global/` 与 `agents/queens/` |

### 3.2 Go（本仓库）

| 关注点 | 文件 | 关键点 |
|---|---|---|
| 数据模型/限额 | `internal/memory/memory.go` | `MaxFiles=200`、`MaxFileSizeBytes=4096`、`ScanMemoryFiles` 按 mtime 取 200 |
| 变更日志 | `internal/memory/memory_log.go` | `Write/Retire/Restore/History`、owner guard、原子写、单互斥 |
| 反思 | `internal/memory/reflection.go` | `ReflectionScheduler`、`OnTurnDone/OnCompaction/Close`、`BuildUnifiedShort/LongReflectSystem`、`ExecuteTool`（4 工具） |
| 反思调度 | `internal/memory/reflection_run.go` | 次数/长反思/错误；`DailyRunner` 属 soul |
| 选择器 | `internal/memory/recall.go` | `SelectMemories`、`BuildScopedRecallBlocks`（3/3）、pinned 回退、相对年龄 |
| 注入 | `internal/agentloop/memory_reminders.go`、`internal/server/{chat_reminders,chat_runtime}.go` | index 清单、wait/background 缓存、`deliverCachedRecall` |
| Past | `internal/memory/past_conversations.go` | TOP_K=6、500/500、8 字符门槛、3s 超时（`HIVE_PAST_RECALL_TIMEOUT`） |
| Timeline | `internal/memory/timeline.go` | 12k/2k 批、3 条/300s、schema v1、`search_timeline` |
| API | `internal/server/routes_memories_extra.go`、`config_memory.go` | 8 路由 + config memory |
| 接线 | `internal/server/chat_memory_wiring.go`、`chat_memory_llm.go` | 触发器、模型槽、timeline 启动、soul sediment（Go-only） |

### 3.3 配置与环境

| 键/变量 | 默认 | 作用 |
|---|---|---|
| `memory_recall.mode` | `index` | `index`/`wait`/`background` |
| `memory_recall.wait_seconds` | `10` | wait 模式等待上限（bool/string/≤0 拒绝） |
| `memory_reflection.every_turns` | `6` | 每 N 个完成回合一次短反思（≥1） |
| `memory_reflection.long_every` | `4` | 每 N 次短反思一次长反思（≥1） |
| `memory_reflection.model` | `worker` | `worker`/`main` |
| `memory_timeline` | `false` | timeline 抽取总开关 |
| `HIVE_MEMORY_RECALL` | – | 覆盖 recall mode |
| `HIVE_PAST_RECALL_TIMEOUT` | `3` | past 召回超时（秒） |
| `HIVE_TIMELINE_BATCH` / `HIVE_TIMELINE_COOLDOWN` | `3` / `300` | timeline live 触发阈值/冷却 |

### 3.4 API 面

| 方法/路径 | 行为 |
|---|---|
| `GET /api/memories` | `{memories:[{path, filename, name, type, description, author, learned, confirmed, mtime, scope, scope_name}]}` |
| `GET /api/memories/file?path=` | + `content`；非法路径 400，不存在 404 |
| `POST /api/memories/file?path=` | body `{name, body, description?, type?}`；幂等种子（存在→`{created:false}`）；`author=owner, claim=false` |
| `PUT /api/memories/file?path=` | body `{content}`；owner 写；>4KB → 400 |
| `DELETE /api/memories/file?path=` | retire（正文留日志）→ `{deleted: path}` |
| `GET /api/memories/history?path=` | `{path, changes:[{n, at, op, file, author, reason, sources, before, after}]}` |
| `POST /api/memories/restore?path=` | body `{n}`；未知 n → 404 |
| `GET /api/memories/retired` | 已退休未恢复列表 |
| `GET/PUT /api/config/memory` | `memory_recall.mode`/`wait_seconds`、`memory_reflection.model`；env 覆盖显示 |

### 3.5 关键算法（伪代码）

```
reflection_tick(turn_done):
  if stream != queen or park_reason not in {turn_done, ask_user}: return
  turns += 1
  if turns % every_turns != 0: return
  if running: return                    # 单飞
  reflections += 1
  spawn(short; if reflections % long_every == 0: long)
  on success: OnReflect(result)          # Go: soul sediment 钩子

recall_pick(query, mode):
  if mode == index: return manifest(global)+manifest(queen)   # 无模型
  pick = selector(query, manifest)  # 只看 query；失败→profile pinned
  if mode == wait: return pick      # ≤wait_seconds
  if mode == background: cache = pick; deliver cached (dedupe) next drain

memory_write(dir, file, content, author, confirms):
  guard(before, author)                # owner 保护
  stamp author/learned/confirmed
  atomic_write(file)
  append(log.jsonl, {at, op, file, author, reason, sources, before, after})
```

## 4. 运维与可观测

- **日志**：每个作用域 `log.jsonl` 是审计源；`History`/`restore` 均基于它。
- **错误**：反思失败写 `<global>/.reflection_error.txt`（含 traceback），并计入当天 attempts（3 次/天，1 小时间隔）。
- **状态**：soul 页/记忆页展示 `last_run`/`last_result`（soul 用 state.json；memory 反思没有独立 status API，错误文件与日志是唯一痕迹）。
- **备份/迁移**：记忆是纯文件，可直接打包 `memories/`；`$HIVE_HOME` 迁移标记 `.migrated-v2/.migrated-v3` 与记忆无关；**没有自动保留/清理**（200 上限靠写入拒绝，不自动删）。
- **热更新**：`memory_recall.mode`、`wait_seconds`、`memory_reflection.model` 每条消息/每次反思读取；`HIVE_MEMORY_RECALL` 优先。

## 5. 验证与证据

| 面 | 证据 |
|---|---|
| Go 单测 | `go test ./internal/memory ./internal/server` 全绿；`internal/memory` 当前 140+ 用例（含日志行格式 pin、owner guard、pinned 回退、注入去重、timeline、past、reflection 工具契约） |
| Python 参考 | `docs/e2e-assets/memory-notes/`（95 个上游 pytest 全绿 + golden log/history/API/config/guard/confirm） |
| 双跑 | `docs/memory-e2e.md`：Go 真实 serve 3/3 子测试、Python 12 阶段、prompt/schema 13/13、门禁 4741 parity |
| 负向 | 删除 404、非法路径 400、>4KB 400、owner 保护文案、超时降级、非 JSON 回退 pinned |
| 性能 | recall term-count 进程内缓存（失效信号 = `events.jsonl` 大小，append-only）：warm p50 123.8→12.7ms、p95 171→13.5ms；720 记录/7,194 轮逐字节相同；RSS +12%（~17MB@15k 轮）；重启首查 ~90ms（6k 轮） |

## 6. 已知差异与风险（登记）

| # | 差异/风险 | 状态 |
|---|---|---|
| D1 | Go 的 `/api/memories` 写入未调用云同步 `SchedulePush`（Python 对 `global/` 与 `agents/queens/` 会 push） | 待补（低风险，云启用时才有感） |
| D2 | Go-only `sediment`：memory 反思结论额外写 soul（`Parity-exempt`） | 已登记（soul 文档） |
| D3 | `timeline` 默认关闭（两侧一致）；Go 有完整实现，但真实数据仅脚本化 fixture 验证 | 已登记 |
| D4 | worker/colony 作用域有布局与 API，但不参与自动召回（两侧一致） | 设计如此 |
| D5 | terminal 对 `memories/` 无沙箱（技能目录沙箱同待办） | 已知边界 |
| D6 | `message_inventory`（压缩事件）用字节口径 vs Python 字符口径 | 已登记（压缩文档） |
| D7 | Memory 页无新建入口（API 有幂等创建） | 与上游一致；若加按钮属新增功能，需拍板 |

## 7. 维护

- 数字来源：`memory.go` 常量、`config.py` 默认值、`timeline.py`/`past_conversations.go` 常量；改一处要同步本文件与 `docs/memory-and-soul.html`（该 HTML 由 `scripts/gen_memory_doc.py` 重算上游数字）。
- 契约：`contracts/api-routes.json`（8 路由）、`contracts/config-keys.json`（memory_* 键）、`contracts/tool-definitions.json`（reflection 工具 schema 不在模型面）。
- 变更流程：任何 memory 行为改动 → 单测 + 双跑脚本（`memory-e2e.md` 命令）+ 本文件与差异表更新。
