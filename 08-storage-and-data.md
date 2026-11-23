# 08 · 存储与数据

> **本文描述上游 Hive（Python 参考实现）。**
> go_hive 的对应实现：`internal/storage`、`internal/hivehome`、`internal/maintenance`。
> 状态：已移植；差异登记见 `docs/alignment-differences.md`。

## 1. 设计原则

Hive 不用外部数据库，全部状态落在 `HIVE_HOME`（默认 `~/.hive`，桌面端指向平台原生 userData 目录）。原则是：

1. **磁盘是事实来源**：进程崩溃/重启后，从磁盘恢复。
2. **一个 Colony 一个目录**：可打包、可导入导出。
3. **原子写**：任何 JSON 写入都先写临时文件再 `os.replace`。
4. **可手动检查**：大部分状态是 JSON/JSONL/Markdown，必要时可以人肉排查。

## 2. `HIVE_HOME` 布局

来自 `framework/config.py` 与 `docs/configuration.md`：

```
$HIVE_HOME/
├── configuration.json          # 全局配置：模型、provider、feature flags
├── queens/<queen_id>/          # Queen 档案 + DM 会话
│   └── sessions/<session_id>/
├── colonies/<name>/            # 一个自包含的 Colony
│   ├── metadata.json
│   ├── worker.json             # Worker 克隆规格
│   ├── tools.json              # Colony 工具配置
│   ├── skills_overrides.json
│   ├── skills/                 # Colony 作用域技能
│   ├── tracker/
│   │   └── tracker.db (+ -wal, -shm)
│   ├── colony.db               # 框架记账（playbook 运行日志）
│   ├── triggers.json
│   ├── playbooks/
│   ├── seed_conversation/      # fork 时 Queen 快照，供 Worker 继承
│   ├── queens/<queen>/sessions/<sid>/
│   └── workers/<session_id>/
├── memories/                   # 三层 Queen 记忆（见 07）
├── credentials/                # 加密凭证库
├── mcp_registry/installed.json # MCP 服务器注册
├── registry_cache/             # 技能注册表缓存
├── trusted_repos.json          # 技能信任仓库
├── cache/mcp2cli/              # CLI 模式 MCP 工具缓存
├── event_logs/                 # 事件日志（可清理）
├── llm_logs/                   # LLM 调试日志（opt-in）
├── compaction_log/
├── tool-artifacts/
├── failed_requests/
└── .migrated-v2 / .migrated-v3 # 迁移标记
```

> 注意一处**文档漂移**：`host/tracker_db.py` 的模块 docstring 仍写 `colonies/{name}/data/`，但 `config.py` 与 v3 迁移已把路径改为 `colonies/{name}/tracker/`。以代码为准。

## 3. 会话存储

### 3.1 目录结构

`storage/session_store.py` 定义统一会话结构：

```
sessions/session_YYYYMMDD_HHMMSS_{uuid}/
├── state.json              # 单一事实来源（会话状态）
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
- **凭证明文风险**：`hive-cred token` 会输出真实令牌，若被 Agent 写进日志/对话就泄露；需要明确的使用约束。
- **保留策略需手动触发**：长期运行的用户可能忘记清理，导致磁盘膨胀。

**建议**

- 为会话目录增加一个轻量索引（例如 `index.json` 或 SQLite 元数据），避免冷启动扫描。
- 为 Tracker 增加 schema 版本与迁移钩子，至少在 Colony 升级时做一次结构校验。
- 对 `hive-cred` 输出做脱敏提示，或默认只允许注入环境变量而不是打印。
- 提供一个可选的定期 janitor（默认关闭），并在 UI 中显示磁盘占用。
- 修正过时 docstring，避免 `data/` 与 `tracker/` 混淆。
