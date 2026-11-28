# 08 · 存储与数据

> **本文描述上游 Hive（Python 参考实现）。**
> go_hive 的对应实现：`internal/storage`、`internal/hivehome`、`internal/maintenance`；§2.5 与 §3.4 额外记录两侧的启动/创建时机与单向文件口径。
> 状态：已移植；差异登记见 `docs/alignment-differences.md`。

## 1. 设计原则

Hive 不用外部数据库，全部状态落在 `HIVE_HOME`（默认 `~/.hive`，桌面端指向平台原生 userData 目录）。原则是：

1. **磁盘是事实来源**：进程崩溃/重启后，从磁盘恢复。
2. **一个 Colony 一个目录**：可打包、可导入导出。
3. **原子写**：任何 JSON 写入都先写临时文件再 `os.replace`。
4. **可手动检查**：大部分状态是 JSON/JSONL/Markdown，必要时可以人肉排查。

## 2. `HIVE_HOME` 布局

来自 `framework/config.py` 与 `docs/configuration.md`。**树里出现的文件不等于启动时就会创建**：本节把 scaffold 必建、功能按需建、启动预建分开标注（Go 侧行为见 §2.5）。

### 2.1 顶层

```
$HIVE_HOME/
├── configuration.json          # 全局配置：模型、provider、feature flags
├── queens/<queen_id>/          # Queen 档案 + DM 会话（见 2.2）
├── colonies/<name>/            # 一个自包含的 Colony（见 2.3）
├── sessions/<session_id>/      # Python 的全局会话根：attached_credentials.json 落在这里
├── memories/                   # 分层记忆（见 2.4）
├── credentials/                # 加密凭证库（serve 启动时懒建 key/index）
├── mcp_registry/               # config.json / installed.json
├── registry_cache/             # 技能注册表缓存（懒建）
├── trusted_repos.json          # 技能信任仓库
├── cache/mcp2cli/              # CLI 模式 MCP 工具缓存
├── cache/skills/bundled-<id>/  # Go 内置 skill 的稳定展开目录（见 2.5）
├── event_logs/                 # 事件日志（可清理）
├── llm_logs/                   # LLM 调试日志（opt-in，懒建）
├── compaction_log/             # 懒建
├── tool-artifacts/             # 懒建
├── failed_requests/            # 常量存在，生产路径未写入
├── maintenance/server.lock     # 在线 runtime 的互斥标记
└── .migrated-v2 / .migrated-v3 # 迁移标记
```

### 2.2 `queens/<queen_id>/`

| 路径 | 创建时机 | 说明 |
| --- | --- | --- |
| `profile.yaml` | 首次加载该 queen 时 materialize | Python 原子写；Go 早期懒创建用 `os.WriteFile`，见 §10 风险 |
| `catalog.json` | 首次移除 queen 时 | `removed` 列表，只打标记，保留文件 |
| `sessions/<session_id>/` | 首个 DM 消息 | 会话树，布局同 §3 |
| `skills/` | 首个 queen UI skill 写入时 | `queen_ui` 扫描根，不在 scaffold 中预建 |
| `skills_overrides.json` | UI 覆盖/墓碑时 | 覆盖存储，**不是**扫描根 |
| `soul/` | soul 首次启用时 | `soul.json`、`history/`、`proposals/`、`log.jsonl` |

### 2.3 `colonies/<name>/`：scaffold 必建 vs 功能按需建

| 文件/目录 | 何时创建 | 说明 |
| --- | --- | --- |
| `metadata.json` | **scaffold** | Go 原子写 0600；Python 直写 0644（登记 SC-04） |
| `worker.json` | **scaffold** | Worker 克隆规格 |
| `tools.json` | 保存 Colony 工具配置时 | 不是 scaffold 产物 |
| `triggers.json` | 创建第一个触发器时 | 不是 scaffold 产物 |
| `skills_overrides.json` | UI 覆盖时 | |
| `skills/` | 首个 colony skill 写入时 | 不再由 scaffold 预建（A17 已闭） |
| `tracker/tracker.db` | `ensure_tracker_db` / 首次读 | v3 schema/触发器一致；旧 `data/` 路径已迁移 |
| `colony.db` | 首次 `record_playbook_run` | `playbook_runs` 表；只用于可观测性，不是恢复权威 |
| `playbooks/` | 首个 playbook 保存时 | Go 写 `.play.json` 并生成 `.play.py` 镜像；Python 只认 `.play.py`（见 §3.4） |
| `seed_conversation/` | **fork 时** | Queen 快照，供 Worker 继承；普通 scaffold 不建 |
| `queens/<queen>/sessions/<sid>/` | colony queen 会话创建时 | 迁移与运行期同布局 |
| `workers/<session_id>/` | worker 启动时 | 含 `result.json` 墓碑、`meta/tasks` 保留 |
| `legacy_fork_snapshots/` | 迁移时 | |
| `runs/` | — | Go 旧 scaffold 已移除 |

**清理**：软删只写 `metadata.deleted=true`；`vacate_soft_deleted_colony` 把 `colonies/<id>` 与 `agents/<id>` 改名为 `<id>.deleted.N`；`?purge=true` 才 rmtree 两个目录（含 tracker/colony.db）。两侧逐项一致。

### 2.4 `memories/`

| 作用域 | 路径 | 一致性 |
| --- | --- | --- |
| global | `memories/global/` | ✅ |
| colony | `memories/colonies/<colony_id>/` | ✅ |
| queen | `memories/agents/queens/<queen_id>/` | ✅ |
| worker/agent | `memories/agents/<agent_name>/` | ✅ |

- 每作用域 `log.jsonl`、`timeline.jsonl`、`timeline_meta.json`；文件名 slug、frontmatter（name/description/type/author/learned/confirmed）、200 文件/4KB 上限一致。
- 清理：`retire` 删除正文并追加 `log.jsonl`；`restore` 从 log 恢复；两侧一致。

### 2.5 创建时机：启动预建 vs 懒创建（F3/F4 的选定口径）

两侧对“目录何时存在”的处理不同，但都不承载数据差异。当前**选择保留各自行为并在文档中登记**，不为了目录树完全一致而改动启动链：

| 场景 | Python | Go | 选定行为 |
| --- | --- | --- | --- |
| `HIVE_HOME` 顶层目录 | 按功能懒创建 | `hivehome.EnsureDirs` 启动预建 10 个目录（queens/colonies/memories/credentials/mcp_registry/secrets/cache/event_logs/tool-artifacts） | Go 保留预建：幂等、让写入路径先存在；空目录不是数据，retention/扫描不依赖它们 |
| `hive serve` 启动文件 | `logs/hive-server.log`、`secrets/credential_key`、`credentials/metadata/index.json`、`maintenance/server.lock` | `mcp_registry/config.json`、`maintenance/server.lock`；前三个在首次使用时懒建 | 保留：启动面不同只影响“空 home 的首日树”，不影响迁移/读取 |
| 新会话子目录 | 首次写入时创建 | `session.Store.Create` 预建 `conversations/parts`、`conversations/partials`、`artifacts`、`checkpoints`、`logs` | Go 保留预建：`state.json` 与 `conversations/meta.json` 本来立即写；retention tier 3 按文件判定，空目录不会被当成“有内容” |
| 内置 skill 根 | 包内 `core/framework/skills/_default_skills` | `$HIVE_HOME/cache/skills/bundled-<bundle-id>/_default_skills`（内容哈希寻址，版本变化换目录） | Go 的稳定展开路径：不再使用每次启动都变的 TMPDIR 随机目录（F1 已闭） |

> 结论：F3/F4 是**目录政策**差异，不是数据面差异。Go 侧保留“启动/创建时预建”，Python 侧保留“首次写入时建”；两侧读取都容忍目录缺失，清理都只看文件内容。

## 3. 会话存储

### 3.1 目录结构

`storage/session_store.py` 定义统一会话结构：

```
sessions/session_YYYYMMDD_HHMMSS_{uuid}/
├── state.json              # 单一事实来源（会话状态）
├── attached_credentials.json # 会话固定凭据引用（见 §3.4）
├── runs.jsonl              # Python 运行时追加的 run 生命周期事件（见 §3.4）
├── conversations/          # 对话部分
│   ├── meta.json
│   ├── cursor.json
│   └── parts/
│       ├── 0000000000.json
│       ├── 0000000001.json
│       └── ...
├── artifacts/              # 溢出数据
├── checkpoints/            # 恢复检查点
│   ├── index.json
│   └── cp_{type}_{node}_{ts}.json
└── logs/                   # L1/L2/L3 可观测性
    ├── summary.json
    ├── details.jsonl
    └── tool_logs.jsonl
```

目录里的 `conversations/`、`artifacts/`、`checkpoints/`、`logs/` 是**首次写入时懒建**（Python）；Go 的 `session.Store.Create` 会预建这 5 个子目录，见 §2.5。

`attached_credentials.json` 的位置由 Python 的 `credential_tool.attachments_path()` 决定：`$HIVE_HOME/sessions/<session_id>/attached_credentials.json`，与聊天树实际落在 `queens/<q>/sessions/` 还是 colony 会话目录无关。Go 写入同一全局位置，并把早期写在会话目录旁的旧文件读入后迁移（见 §3.4）。

### 3.2 为什么「一 part 一 JSON」

`storage/conversation_store.py`（`FileConversationStore`）把每条消息部分写成独立文件。好处：

- 追加写不需要重写整个对话；
- 崩溃只可能破坏最后一个 part；
- 恢复时可以逐 part 校验；
- 大对话不会导致单文件巨大。

每个 part 携带 `phase_id`，因此同一目录可以承载多个阶段（Agent 切换）的对话。

### 3.3 写穿与游标

- 每个 part 写穿（write-through）到磁盘。
- `cursor.json` 记录迭代计数、累加输出、停顿状态。
- `checkpoint_store.py` 提供原子写、索引、过期清理。

`agent_loop/internals/cursor_persistence.py` 负责恢复与队列 drain。

### 3.4 跨实现单向文件（可证据化）

这些文件不是统一的双向格式，文档和测试把方向固定下来：

| 文件 | 方向 | 行为 | 测试 |
| --- | --- | --- | --- |
| `attached_credentials.json` | 双向 | 位置固定为 `$HIVE_HOME/sessions/<session_id>/`；Go 读 Python 写的 `[{credential_id, account}]`（`indent=2`，无尾随换行），也写同一形状；单条 malformed 元素被跳过而不是丢弃整份文件；旧 Go 写在会话目录旁的副本读入后迁移并删除 | `internal/server/chat_credentials_tool_test.go`（Python 形状、malformed 容忍、迁移）、`internal/tools/credentials_tool_test.go` |
| `runs.jsonl` | Python 写 → Go 读 | Python `execution_manager._write_run_event()` 按行追加 `{run_id, event, timestamp, created_at[, error]}`；Go 只在 `agents.discovery.CountRuns` 递归读取 `session_*` worker 目录并统计唯一 `run_id`，**不写**该文件（Go 的 chat turn 没有 Python 的 graph run_id；worker 目录名是 `worker_*`，Python 的统计器本就不扫） | `internal/agents/discovery_test.go`（读形状、`session_` 过滤、坏行容忍） |
| `.play.json` | Go 写 → Python 不读 | Go 原生声明式 playbook；Go 同时读 Python 的 `.play.py`。Go 保存/加载原生 playbook 时在旁生成 `.play.py` 镜像（首行 `# Generated by go_hive from ...`），Python 因此能列出并运行同一个 playbook；手写的 `.play.py` 不会被覆盖，`.play.json` 在 Go 侧仍优先 | `internal/playbook/python_mirror_test.go`（形状、字面量转义/compile、不覆盖手写脚本、双扩展优先级） |

镜像是一次**单向转换**：`.play.py` 是派生文件，语义以 `.play.json` 为准；Python 修改镜像后 Go 下次刷新会覆盖它是有意的（只在首行是生成标记时刷新）。

## 4. SQLite：两个库，职责分离

### 4.1 Tracker（`tracker/tracker.db`）

- **域状态**：Queen 与 Worker 的业务数据。
- Queen 拥有 DDL 自由；Worker 只能通过 `tracker_upsert` 写已注册的列。
- 框架表：
  - `_tracker_registry`：哪些表可被 Worker 写、可写列、键列、模式（upsert/append）。
  - `_tracker_meta`：元信息。
  - `_tracker_changes`：行级变更日志（注册时安装触发器）。
- WAL 模式；每次调用新开连接；多语句写用 `BEGIN IMMEDIATE`。
- SQL denylist：`ATTACH/DETACH`、`PRAGMA`、`VACUUM/REINDEX`、`load_extension`、对 `_*` 框架表的 DDL/DML。

> `SCHEMA_VERSION = 3`（`host/tracker_db.py`）。

### 4.2 Colony DB（`colony.db`）

- **框架记账**：playbook 运行日志。
- 刻意与 Tracker 分离，保持 Tracker 是「纯域状态」。
- 明确声明：**仅用于可观测性，绝不是恢复权威**；丢失它不会破坏运行，恢复以域行为准。

这个「关注点分离 + 明确非权威」的设计很值得学习。

## 5. 原子写

`utils/io.py` 的 `atomic_write`：

- 用 `tempfile.mkstemp` 生成**每个写入者唯一**的临时文件；
- 写入后 `fsync`；
- `os.replace` 原子替换；
- 异常时删除临时文件。

注释里记录了一次真实事故：早期固定 `<name>.tmp` 方案在并发写同一文件时会互相截断，导致 `cursor.json` / `summary.json` / `reminder_state.json` 损坏。这是一个典型的「并发写 + 固定临时名」陷阱，修复方式正确。

## 6. 迁移

`storage/migrate_v2.py`、`migrate_v3.py`：

- v3 把 `agents/queens/` 改为 `queens/`；
- Colony overseer 会话迁到 `colonies/<c>/queens/<q>/sessions/`；
- 旧 Worker 目录迁到 `colonies/<c>/workers/`；
- `colonies/<c>/data/tracker.db` → `colonies/<c>/tracker/tracker.db`；
- 记忆布局不变；
- 标记文件 `.migrated-v3`，幂等、可重跑。

迁移在启动时自动执行，这是「升级不破坏用户数据」的关键。

> **Go 侧现状（2026-11-21 复核）**：`storage.RunMigrationV2/V3/RunMigrationColonySessions` 已移植且与 Python 逐项等价（`.wt/storage-verbatim` 双跑，见 `docs/storage-contract-diff.md`），并已接到 `hive serve` 启动链：`defaultServe` → `internal/server/storage_startup.go::RunStorageStartupMigrations`，顺序同 Python（v2 → v3 → colony_sessions → tracker ensure），任一步失败只告警、不阻断启动。真实二进制实测旧 home 迁移与二次启动幂等见 `docs/storage-compat/serve-startup-go.json`。

## 7. 保留与清理

`maintenance/retention.py` + `maintenance/cli.py`：

三档、**手动触发**（无调度器）：

| 档 | 对象 | 策略 |
| --- | --- | --- |
| Tier 1 | 调试存储（event_logs、llm_logs、compaction_log、tool-artifacts、轮转日志） | 直接删除（无消费者） |
| Tier 2 | 已完成的 Colony Worker | 归档+删除 transcript，写 `result.json` 墓碑，保留 `meta.json`/`tasks.json` 以便 UI 继续渲染 |
| Tier 3 | 冷 Queen 会话 | 删除孤立 spillover 文件；重写 `events.jsonl` 去掉遥测/旧完整请求 |

安全模型：

- 运行中的会话被硬阻止；
- 年龄门槛取多个磁盘活动信号的最大值，而不是目录 mtime；
- `KEEP_ALWAYS` 白名单（memories、credentials、marks、mcp_registry 等）永不触碰；
- 默认 `mode="archive"`，可用 `HIVE_RETENTION_*` 环境变量覆盖。

### 7.1 Go 实现（`internal/maintenance`）

纯 Go、无 cgo；平台差异只出现在文件锁与进程探活两处，用 build tag 分离（`lock_unix.go`/`lock_windows.go`、`pid_unix.go`/`pid_windows.go`）。

| 文件 | 职责 |
| --- | --- |
| `config.go` | `RetentionConfig`、`DefaultRetentionConfig`、`HIVE_RETENTION_*` 覆盖、`ForOffline(cfg, now)` 构造安全上下文 |
| `retention.go` | 保留扫描核心：`SafetyContext`/`Refresh`、`IsSafeToPrune`、`IterQueenSessions`/`IterWorkerSessions`、`SpilloverReferenceCorpus`/`FindOrphanSpillovers`、`PruneTier1Logs`、`PruneQueenSession`、`DeepCleanWorker` |
| `janitor.go` | `RunOnce` 三档编排、锁与报告、server marker（`maintenance/server.lock`） |
| `archive.go` | `ArchiveDisposer`（先写 `.tmp` 再 rename，失败保源）与 `DeleteDisposer` |

语义对齐要点：

- **默认 dry-run**。`RunOptions{Tiers: nil}` 表示三档全选，空而非 nil 的 map 表示一档都不跑；`Execute=false` 时只记账、写候选到 manifest，不改磁盘（包括不写墓碑）。
- **门禁顺序**（`IsSafeToPrune`）：受保护会话 ID → live 会话目录 → fork 压缩进行中 → 新鲜 session lock（janitor 自有锁在重检时可跳过）→ idle 门槛 `max(ageGate, GraceSeconds)` → 未完成 partial 文件。任一命中即跳过并记录原因。
- **锁**：全局锁 TTL 2 小时，防止反复触发；session 锁 TTL 3600 秒。锁文件在 dispose 全流程结束后释放，disposer 构造失败不得泄漏全局锁（先构造 disposer，再取锁）。
- **孤儿 spillover 扫描 fail-closed**：语料 = `conversations/parts`、`conversations/partials` 的原始 JSON + `cursor.json`；任何一个文件读失败就视为「无法判定」，本会话整轮跳过，绝不误删唯一副本。`tool_calls.arguments` 里的路径同样计入引用。
- **离线与在线互斥**：执行破坏性档位时会写 `maintenance/server.lock`；离线 `RunOnce` 若发现 marker 指向**其他**存活 pid，直接拒绝，文案为 `a live runtime owns this HIVE_HOME (...)`；tier 1（调试日志，且有打开句柄保护）不在此限。`ClearServerMarker` 只删除自己 pid 的 marker，避免误删他人。
- **墓碑先行**：worker 深清理先写 `result.json`（status/summary/`_janitor`），再删 transcript；崩溃后重跑只补删剩余文件，不重写已存在的墓碑。
- **归档**：每个处理目标一个 `*.tar.gz`，成员路径保留 `colonies/<c>/workers/<id>/...` 相对结构；archive 失败时源目录原样保留。
- 尚未落盘：`hive maintenance` CLI 包装（上游 `maintenance/cli.py` 的对等物）与定时调度（上游同样默认关闭）。

## 8. 凭证存储

`framework/credentials/`：

- `CredentialStore`：多后端（file/env/vault）、provider 生命周期（刷新、校验）、模板解析（`{{cred.key}}`）、TTL 缓存、线程安全。
- 加密文件存储：`EncryptedFileStorage("~/.hive/credentials")`，Fernet 加密。
- `OAuth2Provider`：刷新流程。
- `hive-cred` CLI：脚本按需取短期令牌，避免把令牌写进文件。
- 凭证表单：前端独立 POST 直写存储，**不经过对话或事件**（`CLIENT_CREDENTIAL_FORM_REQUESTED` 只携带字段规格）。

## 9. 数据可移植性

- Colony 是单目录，支持 tar 导入导出：`POST /api/colonies/import` 有 init/chunk/finalize/cancel 的完整分片上传协议。
- `Cron`/interval 触发器写入 `triggers.json`，随 Colony 一起移动。
- Tracker 是 SQLite，任何 SQL 工具都能读。
- 记忆是 Markdown，可直接编辑。

这是「本地优先、可审计、可迁移」的典型实现。

## 10. 风险与建议

**风险**

- **文件存储的规模边界**：数万会话、数万 part 文件时，目录遍历与冷启动会变慢；没有看到 LSM/索引层的描述。
- **SQLite 并发**：WAL 支持一写多读，但多个 Worker 同时 `tracker_upsert` 时仍会串行化写锁；高并发 Playbook 可能遇到 `database is locked`（有 `BEGIN IMMEDIATE` 与重试，但需要实测）。
- **schema 由 Queen 运行时创建**：没有框架级迁移；Colony 跨版本升级时，Tracker 表结构可能漂移。
- **文档漂移**：`tracker_db.py` 仍写 `data/`，属于小但会误导维护者的问题。
- **Go 默认 queen profile 懒创建非原子**（F2）：`internal/queen/profiles.go` 用 `os.WriteFile`，崩溃可能留下截断的 `profile.yaml`；Python 用 tempfile+fsync+replace。属未修的原子写孤例。
- **凭证明文风险**：`hive-cred token` 会输出真实令牌，若被 Agent 写进日志/对话就泄露；需要明确的使用约束。
- **保留策略需手动触发**：长期运行的用户可能忘记清理，导致磁盘膨胀。

**建议**

- 为会话目录增加一个轻量索引（例如 `index.json` 或 SQLite 元数据），避免冷启动扫描。
- 为 Tracker 增加 schema 版本与迁移钩子，至少在 Colony 升级时做一次结构校验。
- 对 `hive-cred` 输出做脱敏提示，或默认只允许注入环境变量而不是打印。
- 提供一个可选的定期 janitor（默认关闭），并在 UI 中显示磁盘占用。
- 修正过时 docstring，避免 `data/` 与 `tracker/` 混淆。
