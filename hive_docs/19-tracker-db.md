# 19 · Tracker DB 机制深入

> Tracker 是 Colony 的**共享账本**，也是 Hive 里唯一一个“让 Agent 直接拥有数据库”的子系统。
> 本章只讲 Tracker：它是什么、schema 怎么长、四个工具怎么协作、写入如何被门禁约束、SQL 安全边界在哪、并发与失败模式是什么。
>
> 关键文件：
> `core/framework/host/tracker_db.py`
> `core/framework/tools/tracker_tools.py`
> `core/framework/host/colony_binding.py`
> `core/framework/agent_loop/tracker_snapshot_reminder.py`
> `core/framework/server/routes_colony_workers.py`
> `core/framework/tools/playbook_tools.py`、`core/framework/host/playbook/runner.py`

---

## 1. 定位：Tracker 是什么，不是什么

### 1.1 它是什么

- **每个 Colony 一个 SQLite 文件**：`colonies/<name>/tracker/tracker.db`（+ `-wal` / `-shm`）。
- **业务事实的唯一来源**：一行 = 一个工作单元（一条线索、一个账户、一篇文档、一个工单）。
- **Queen 设计 schema，Worker 填行，Queen 用 SQL 验收**。
- **Playbook 的 `pending()` 数据源**；重跑即续跑，因为“做完没”永远是新鲜查询。
- **UI Data 页**展示的表；行级变更日志让页面知道“哪一行变了”。

### 1.2 它不是什么

| 不是 | 说明 |
|---|---|
| 不是通用业务数据库 | 没有迁移框架、没有 schema 版本（业务表）、没有外键设计规范 |
| 不是队列/租约系统 | 没有内置 claim/lease；并发切片靠 Queen/Playbook 自己保证 |
| 不是审计日志 | 只有 INSERT/UPDATE 行级变更；没有 DELETE/DDL、没有操作者身份 |
| 不是 `colony.db` | `colony.db` 是框架簿记（playbook run log），Tracker 是业务域 |
| 不是 Plan | Plan 是意图（`tasks.json`），Tracker 是事实（行） |
| 不是沙箱 | Queen 有原生 SQL；denylist 是护栏，不是隔离 |

### 1.3 与其它基底的关系

```text
Queen 的 Plan（tasks.json）      ── 意图：要做什么
        │
        ▼
Queen 用 tracker_sql 建表/种子   ── 设计：一行是什么
        │
        ▼
tracker_register_writable        ── 门禁：Worker 能写哪些列
        │
        ▼
Worker 用 tracker_upsert 填行    ── 事实：每行结果
        │
        ▼
Queen / Playbook 用 tracker_query 查剩余 ── 验收/续跑
        │
        ▼
UI Data 页 / 变更日志             ── 观察：哪一行变了
```

---

## 2. 物理位置与身份

### 2.1 路径

```text
$HIVE_HOME/colonies/<colony_id>/
├── tracker/
│   ├── tracker.db
│   ├── tracker.db-wal
│   └── tracker.db-shm
└── colony.db            # 另一个库：playbook_runs，不是 Tracker
```

v3 迁移把旧 `colonies/<c>/data/tracker.db` 移到 `tracker/`（`storage/migrate_v3.py`）。

### 2.2 `ColonyBinding`：不可变身份

```python
@dataclass(frozen=True)
class ColonyBinding:
    name: str
    dir: Path
    tracker_db: Path
```

- 由 `ColonyBinding.for_name(name)` 从 `config.colony_dir` / `colony_tracker_db_path` 解析；
- Queen 通过 `ToolRegistry` 执行上下文拿到 binding；
- Worker 通过 `input_data["binding"]` 拿到 binding；
- **没有 binding 的 Tracker 工具直接拒绝**，不会从 session id 合成路径（`host/colony_binding.py`、`tools/tracker_tools.py:_require_binding`）。

这解决了三类历史问题：

1. `colony_id` 有时是 session UUID、有时是磁盘 Colony 名，导致 shadow 目录；
2. 工具忘了注入 `tracker_db_path`，落到双义 fallback；
3. 两个 Colony / 一个 Colony 与游离 session 写到同一个库。

### 2.3 启动时的回填

- `SessionManager` 启动时 `ensure_all_colony_tracker_dbs()` 遍历所有 Colony，确保 `tracker/tracker.db` 存在；
- `ensure_tracker_db()` 会 `_patch_worker_configs()`：
  - 扫描 Colony 根目录的 `*.json`；
  - 跳过 `metadata.json` / `triggers.json`；
  - 只处理含 `system_prompt` 的 Worker 配置；
  - 重写 `input_data.binding`，删除 legacy `db_path` / `colony_data_dir` / `tracker_db_path` / `colony_id`。
- **已知缺口**：只 glob 顶层 `*.json`，不会补丁 `profiles/<name>/worker.json`；命名 Worker Profile 的 binding 依赖 fork 时写对。

---

## 3. Schema（v3）

### 3.1 框架表

`SCHEMA_VERSION = 3`，`PRAGMA user_version` 记录。Bootstrap：

```sql
CREATE TABLE IF NOT EXISTS _tracker_registry (
    table_name      TEXT PRIMARY KEY,
    write_columns   TEXT NOT NULL,    -- JSON array of column names
    key_columns     TEXT NOT NULL,    -- JSON array; empty = append mode
    mode            TEXT NOT NULL CHECK (mode IN ('upsert', 'append')),
    registered_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_meta (
    key             TEXT PRIMARY KEY,
    value           TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_changes (
    id              INTEGER PRIMARY KEY,  -- monotonic cursor
    table_name      TEXT NOT NULL,
    pk              TEXT NOT NULL,        -- JSON object key->value
    op              TEXT NOT NULL CHECK (op IN ('insert', 'update')),
    changed_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS _tracker_changes_table_id
    ON _tracker_changes (table_name, id);
```

`_tracker_meta` 目前主要用于：

- `schema_version`；
- 可选的 CRM 水位（`CRM_LINK_KEY` / `CRM_WATERMARK_KEY` / `CRM_PROMOTED_AT_KEY` / `CRM_OPT_OUT` / `CRM_NUDGE_KEY`，见 `tracker_db.py` 的 CRM promote-link 段）。

### 3.2 业务表

业务表**完全由 Queen 创建**，没有固定 schema。典型：

```sql
CREATE TABLE leads (
    id TEXT PRIMARY KEY,
    company TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,
    last_error TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    claimed_by TEXT,
    updated_at TEXT
);
CREATE UNIQUE INDEX idx_leads_id ON leads(id);
```

### 3.3 保护命名空间

- 任何 `_` 前缀表都属于框架；
- `tracker_sql` 拒绝对 `_` 表的 DDL/DML（SELECT 允许）；
- `tracker_upsert` / `tracker_register_writable` 拒绝 `_` 表；
- `PROTECTED_PREFIX = "_"`，`MAX_STATEMENTS_PER_CALL = 20`，`CHANGE_LOG_MAX = 10_000`。

### 3.4 连接参数

`_connect()`：

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

- `isolation_level=None`：autocommit；多语句由调用方显式 `BEGIN`/`COMMIT`；
- WAL 是**文件级 sticky**；其余 per-connection；
- 每个工具调用**新开连接**，没有连接池。

---

## 4. 四个工具

| 工具 | 角色 | 作用 | 并发安全 |
|---|---|---|---|
| `tracker_sql` | **Queen only** | 原生 SQL；DDL/DML/SELECT/脚本 | 否 |
| `tracker_register_writable` | **Queen only** | 把表登记进 `_tracker_registry`，开放 Worker 写入 | 否 |
| `tracker_upsert` | Queen + Worker | 按 key `INSERT ... ON CONFLICT DO UPDATE` | 否 |
| `tracker_query` | Queen + Worker | 只读 SELECT | **是** |

### 4.1 角色注册

`register_tracker_tools(registry, role="queen"|"worker")`：

- Queen：四个都注册；
- Worker：只注册 `tracker_upsert` + `tracker_query`；`tracker_sql` / `tracker_register_writable` 被跳过；
- Worker 的 `tracker_upsert` / `tracker_query` 外面再包一层 `_reject_global_scope`：

```python
if scope == "global":
    return {"success": False, "error": "scope='global' is queen-only …"}
```

**文档漂移**：`register_tracker_tools` 的 docstring 写 “worker: only tracker_upsert”，实际代码同时注册 `tracker_query`。以代码为准。

### 4.2 `tracker_sql`（Queen）

- 输入：`sql`（单条或 `;` 分隔脚本）、`row_cap`（默认 1000，schema 最大 10000）、`scope`（`colony`/`global`）。
- 本地：
  - `ensure_tracker_db(binding.dir)`（防御性；fork 已建）；
  - `execute_sql(binding.tracker_db, sql, row_cap=...)`；
  - `validate_sql` 拒绝 denylist；
  - SQLite 错误原样返回，便于 Queen 调试 schema。
- 返回：
  - 单条 SELECT：`{success, kind:"rows", columns, rows, rowcount, truncated}`；
  - 单条写/DDL：`{success, kind:"exec", rowcount, last_insert_rowid}`；
  - 多语句：`{success, kind:"script", results:[...]}`。
- `scope="global"` → `_global_sql`：把原始 SQL 发到云端 global DB；不经过本地 denylist（由后端负责）。
- **没有查询超时 / 进度回调**；长查询会占住工具线程，直到 60s 工具超时（线程可能继续跑）。

### 4.3 `tracker_register_writable`（Queen）

输入：

```json
{
  "table": "leads",
  "write_columns": ["status", "result_json", "last_error", "attempts", "updated_at"],
  "key_columns": ["id"]
}
```

校验顺序：

1. `table` 非空、不以 `_` 开头；
2. `write_columns` 是字符串列表（schema `minItems: 1`；**executor 本身不拒绝空列表**）；
3. `key_columns` 是非空字符串列表；
4. `ensure_tracker_db`；
5. 表存在（`sqlite_master`）；
6. 所有 `write_columns` / `key_columns` 存在于表中（`PRAGMA table_info`）；
7. `key_columns` 被某个**唯一索引**完整覆盖（`_key_columns_uniquely_indexed`）；
8. `mode` 固定 `"upsert"`；
9. 写 `_tracker_registry`（`INSERT ... ON CONFLICT DO UPDATE`）；
10. `install_change_triggers(table, key_columns)`。

**唯一索引检查的细节**：

```python
keys_set = set(keys)
for idx in PRAGMA index_list(table):
    if not idx.unique: continue
    cols = [r[2] for r in PRAGMA index_info(idx.name)]
    if set(cols) == keys_set: return True
```

- PRIMARY KEY 算唯一索引；
- 复合 key 顺序无关（set 比较）；
- **忽略 `partial` 标志**：部分唯一索引也会被当成覆盖；`ON CONFLICT(keys)` 可能无法匹配部分索引，导致运行时失败；
- 表达式索引的 `index_info` 列名为 `None`，set 不匹配，通常会被拒绝；
- **不检查 key 列是否 NOT NULL**：SQLite 唯一索引允许多个 NULL，key 为 NULL 时 upsert 可能插入重复行。

**重登记**：

- 同表再次登记会替换 `write_columns/key_columns/mode`；
- 先 `DROP TRIGGER` 再 `CREATE TRIGGER`，按新 key 重建变更日志触发器；
- 不会清理旧 `_tracker_changes`。

### 4.4 `tracker_upsert`（Queen + Worker）

输入：

```json
{"table": "leads", "row": {"id": "acme", "status": "done", "result_json": "…"}}
```

执行：

1. `scope=global` → `_global_upsert`（Queen only；Worker 被 guard 拒绝）；
2. 取 binding；无 binding 拒绝；
3. `table` 非空、不以 `_` 开头；
4. `row` 非空 dict；
5. 查 `_tracker_registry`：

```sql
SELECT write_columns, key_columns, mode FROM _tracker_registry WHERE table_name = ?
```

6. 未登记 → 拒绝；
7. `allowed_for_writes = set(write_columns) | set(key_columns)`；未知列 → 拒绝；
8. `mode="upsert"` 时 row 必须包含所有 `key_columns`；
9. 值编码：
   - `list` / `dict` → `json.dumps(...)` 文本；
   - `bool` → `1/0`；
   - 其他原样（`None` → NULL）；
10. SQL：

```sql
INSERT INTO "leads" ("id","status","result_json")
VALUES (?,?,?)
ON CONFLICT ("id")
DO UPDATE SET "status"=excluded."status", "result_json"=excluded."result_json"
```

- 如果 row 只有 key 列 → `ON CONFLICT DO NOTHING`；
- 标识符用 `_quote_ident`（双引号转义），值全部参数化；
11. `prune_change_log(con)`（机会式裁剪到 10k）；
12. 返回 `{success, table, mode, rowcount, last_insert_rowid}`。

**不改的地方**：

- 不校验列类型、NOT NULL、CHECK、外键（交给 SQLite 约束）；
- 不校验 `write_columns` 是否为空（只校验 row 的列在允许集合内）；
- 不做事务包装（单条 INSERT 本身原子）；
- 不重试 `database is locked`（直接返回 sqlite error）。

### 4.5 `tracker_query`（Queen + Worker）

- `scope=global` → `_global_sql(read_only=True)`；
- 本地：
  - 必须恰好 **1 条语句**（多语句拒绝）；
  - `_effective_statement` 解析有效动词；
  - 只允许 `SELECT` / `EXPLAIN`；
  - `WITH ... INSERT/UPDATE/DELETE` 会解析成写动词并拒绝；
  - 再走 `execute_sql` → `validate_sql`（denylist 兜底）；
  - `row_cap` 默认 1000，schema 最大 10000。
- `concurrency_safe=True`：只读工具可以并行执行（AgentLoop 的并行批次）。

**注意**：`tracker_query` 允许 `EXPLAIN INSERT ...`（动词是 EXPLAIN）；EXPLAIN 不执行写入，安全。它也可以 `SELECT * FROM _tracker_registry` / `_tracker_changes`，即 Worker 可以读框架表结构，但不能写。

---

## 5. 写入协议：从空库到一行结果

### 5.1 阶段 1：Queen 建表与种子

```text
tracker_sql(`
  CREATE TABLE leads (
    id TEXT PRIMARY KEY,
    company TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,
    updated_at TEXT
  );
  CREATE UNIQUE INDEX idx_leads_id ON leads(id);
`)
```

然后种子：

```sql
INSERT INTO leads (id, company, status) VALUES
  ('acme', 'Acme', 'pending'),
  ('globex', 'Globex', 'pending');
```

### 5.2 阶段 2：登记可写列

```text
tracker_register_writable(
  table="leads",
  write_columns=["status","result_json","updated_at"],
  key_columns=["id"]
)
```

从这一刻起：

- `_tracker_registry` 有 `leads`；
- `leads` 上装了 `AFTER INSERT/UPDATE` 触发器；
- Worker 只能写 `status/result_json/updated_at` + key `id`；
- `company` 是 Queen-only（Worker 不能改）。

### 5.3 阶段 3：Worker 填行

```text
tracker_upsert(table="leads", row={
  "id": "acme",
  "status": "done",
  "result_json": "{\"employees\": 120}",
  "updated_at": "2026-11-27T…"
})
```

语义：

- 第一次：INSERT；
- 重派同一行：`ON CONFLICT(id) DO UPDATE`，幂等；
- 只带 `id`：`DO NOTHING`；
- 未知列 / 缺 key：拒绝；
- 表未登记：拒绝。

### 5.4 阶段 4：Queen 验收 / Playbook 续跑

```sql
-- 剩余
SELECT id FROM leads WHERE status != 'done';

-- 失败
SELECT id, last_error FROM leads WHERE status = 'failed';

-- 统计
SELECT status, COUNT(*) FROM leads GROUP BY status;
```

Playbook：

```python
await converge(
    pending=lambda: tracker_query("SELECT id FROM leads WHERE status != 'done'"),
    dispatch=lambda row, i: worker(task=f"Research {row['id']}", data=row),
    max_rounds=3,
)
```

### 5.5 阶段 5：UI 行编辑

`PATCH /api/colonies/{cid}/data/tables/{table}/rows`：

- 必须表有 PRIMARY KEY；
- 请求 `{pk, updates}`；
- 校验表 / 列 / pk；
- **不能修改主键列**；
- 直接 `UPDATE` 原 `tracker.db`（不是快照）；
- **只有已注册表**（`_tracker_registry` + `_tracker_chg_` 触发器）的编辑才会进入 change log；UI 也能编辑未注册的用户表，但那类编辑不会出现在 change log；
- 不经过 `tracker_register_writable` 的列门禁：UI 可以改该表任意非主键列（这是“人”的权限，不是 Worker 的权限）。

---

## 6. SQL 安全模型

### 6.1 Denylist

`validate_sql` 是**执行前**的整段校验：要么全部接受，要么全部拒绝（不会部分执行）。

| 拒绝项 | 细节 |
|---|---|
| `ATTACH` / `DETACH` | 禁止逃出当前 DB |
| `PRAGMA` | 禁止改 journal/WAL/外键等；表结构可用 `sqlite_master` 查 |
| `VACUUM` / `REINDEX` | 运维语句，不是业务 |
| `load_extension(...)` | 任意共享库加载 |
| 对 `_` 表的 DDL/DML | 框架命名空间 |
| >20 条语句 | 限制爆炸半径 |

解析细节：

- 先去字符串/注释（`_strip_strings_and_comments`），避免关键字藏在 `'...'` 或 `--` 里；
- `_effective_statement` 处理 `WITH ... INSERT/UPDATE/DELETE`，取真正的写动词；
- `_referenced_tables_for_mutation` 尽力提取 INSERT/UPDATE/DELETE/CREATE/DROP/ALTER 的目标表；
- 提取不到时**保守放行**（defense-in-depth，不是强隔离）。

### 6.2 不是沙箱

- Queen 有完整 SQL：可以 `DROP TABLE`、`DELETE FROM`、`ALTER TABLE`、`CREATE TRIGGER`；
- denylist 只保护框架表和一个很小的逃逸面；
- 没有查询超时、没有成本估算、没有审批；
- SQLite 文件是明文（除 OS 权限外无加密）；
- 任何能读 `tracker.db` 的进程都能读全部业务数据。

### 6.3 已知绕过（审计发现）

`_referenced_tables_for_mutation` 只检查目标表名是否以 `_` 开头，**没有剥离 schema 限定符**。因此：

```sql
INSERT INTO main._tracker_registry ...      -- ALLOWED
UPDATE main._tracker_registry ...           -- ALLOWED
DROP TABLE main._tracker_registry ...       -- ALLOWED
DELETE FROM main._tracker_changes ...       -- ALLOWED
ALTER TABLE main._tracker_registry ...      -- ALLOWED
DROP TRIGGER main._tracker_chg_leads__ins  -- ALLOWED
```

`validate_sql` 对 `main._tracker_registry` 的目标表名是 `main._tracker_registry`，`startswith("_")` 为 false，所以放行。同样的写法对 `"_tracker_registry"`（带 schema 引号）也可能成立。

**影响**：

- Queen（或被提示注入诱导的 Queen）可以破坏 `_tracker_registry`、`_tracker_changes`、变更日志触发器；
- `ensure_tracker_db` 只在 `user_version < 3` 时重建 bootstrap schema；如果版本已是 3 但表被删，**不会自动恢复**；
- 之后 `tracker_upsert` 全部失败（registry 查不到/表不存在），UI change log 失效；
- 这是 **Queen-only** 的路径，Worker 不能 `tracker_sql`，也不能通过 `tracker_upsert` 写 `main._...`（registry 查不到）。

**建议**（属于修复建议，不是现状）：

- 在 `_referenced_tables_for_mutation` 返回前剥离 `schema.` 前缀（`main.` / `temp.` / `"main".` 等），再判断 `_`；
- 或在 `validate_sql` 的 mutating 分支里把 `main._...` 视为受保护；
- 或者干脆拒绝带 schema 限定符的写操作（`INSERT INTO main.x` 在 Hive 里没有合法用途）。

### 6.4 其它边界

| 边界 | 行为 |
|---|---|
| 行数 | `tracker_sql` / `tracker_query` 默认 1000，schema 最大 10000；超出 `truncated=true` |
| 语句数 | 最多 20 条/次 |
| 查询时间 | **无**超时/进度回调；依赖外层 60s 工具超时（线程可能继续） |
| 事务 | `execute_sql` autocommit；`BEGIN`/`COMMIT` 允许；错误时未提交事务随连接关闭回滚 |
| 外键 | `PRAGMA foreign_keys=ON`，业务表的外键会被执行 |
| 类型 | SQLite 动态类型；`tracker_upsert` 不校验列类型 |
| 删除 | `tracker_sql` 可以 DELETE 业务行；**变更日志不记录 DELETE** |
| DDL | `tracker_sql` 可以 DROP/ALTER 业务表；**没有 DDL 审计** |

---

## 7. 并发、事务与性能

### 7.1 SQLite 的并发模型

- WAL：多读单写；
- 写者串行；默认 4 个 Worker 并发 upsert 时，实际是串行写；
- `busy_timeout=5000`：写冲突最多等 5s，然后 `database is locked`；
- `tracker_upsert` **不重试**，直接把 sqlite error 返回给 Worker；Worker 可能在下一次工具调用重试（取决于模型）。

### 7.2 长事务风险

`tracker_sql` 允许多语句与 `BEGIN`/`COMMIT`：

```sql
BEGIN IMMEDIATE;
UPDATE leads SET status='running' WHERE ...;
-- 如果模型在这里停止/工具超时，事务未提交
```

- `BEGIN IMMEDIATE` 立刻拿写锁；
- 工具超时 60s 不会强杀 SQLite 查询/事务；线程可能继续持有写锁；
- 其他 Worker 的 upsert 会在 `busy_timeout` 后失败；
- 连接关闭时未提交事务回滚。

**建议**：Queen 的 tracker_sql 多语句应尽量短；不要长时间持有 `BEGIN IMMEDIATE`；大批量写用单条 `INSERT ... SELECT` 或分批。

### 7.3 UI 读取不走原库

Dashboard 的 Data 页：

```text
tracker.db（可能 NFS）
  → shutil.copyfile 到 /var/tmp/hive-tracker-snapshots/
  → 以 mode=ro&immutable=1 打开
  → tables/rows/changes 查询
```

- 快照 TTL **2s**，同一 source 并发读共享；
- 每 source 一把锁，single-flight copy；
- 最多 3 次重试；`sqlite_master` 探针失败则重试；
- 读取超时 **15s**，超时 503 `colony_data_timeout`；
- 行数 **500/页**；change log **2000 条/次**；
- 快照可能落后最后一次 WAL checkpoint 几秒（浏览可接受）。

**为什么不用 `sqlite3.backup()`**：NFS 上的 WAL 需要共享内存，`backup()` 会打开 `-shm/-wal`，在 NFS 上不可靠；代码用 plain `copyfile` + `immutable=1` 绕开。

### 7.4 为什么 Tracker 不放在网络文件系统上

SQLite 官方明确：WAL 模式不适用于网络文件系统。Hive 的 sandbox 里 `HIVE_HOME` 可能在 NFS 上；代码通过 UI 快照规避读取问题，但**写入仍在 NFS 上**。这是部署级风险，不是代码能完全消除的：

- 写锁/共享内存语义在 NFS 上不可靠；
- 多主机同时写会损坏；
- 建议 Tracker 放在本地磁盘（或单机挂载），不要多实例共享。

### 7.5 性能特征

| 操作 | 成本 |
|---|---|
| `tracker_upsert` 单行 | 一次 INSERT + 触发器写 `_tracker_changes` + 机会式 prune |
| 批量 1000 行 | 1000 次 upsert（模型逐个调用）；建议单 worker 处理 5–10 行/批 |
| `tracker_query` | 一次 SELECT；行上限 1000；无索引会全表扫 |
| Playbook `pending()` | 100k 行上限；每轮重新查询 |
| UI tables overview | N × COUNT(*)，在快照上执行 |
| `tracker_snapshot` reminder | 最多 10 表、50ms 预算、read-only |

**索引建议**：

- key 列必须有 UNIQUE（注册要求）；
- `status` / `updated_at` / `claimed_by` 常出现在 pending 查询里，建议建索引；
- 不要对 JSON 大字段建索引；
- 大结果放文件，不塞 Tracker。

---

## 8. 变更日志（`_tracker_changes`）

### 8.1 触发器

注册表时 `install_change_triggers(table, key_columns)`：

```sql
CREATE TRIGGER "_tracker_chg_leads__ins"
AFTER INSERT ON "leads"
BEGIN
  INSERT INTO _tracker_changes (table_name, pk, op, changed_at)
  VALUES ('leads', json_object('id', NEW.id), 'insert',
          strftime('%Y-%m-%dT%H:%M:%SZ','now'));
END;
-- 同理 __upd AFTER UPDATE
```

- 触发器名以 `_tracker_chg_` 开头，属于框架命名空间；
- 重新登记会 DROP + CREATE，按新 key 重建；
- 触发器在 DB 文件里，任何写入者（Worker upsert、Queen SQL、UI PATCH）都会被记录。

### 8.2 日志语义

| 字段 | 含义 |
|---|---|
| `id` | 单调游标；UI 用 `since=<id>` 增量读 |
| `table_name` | 业务表 |
| `pk` | key 列的 JSON 对象 |
| `op` | `insert` / `update` |
| `changed_at` | UTC 秒 |

- **没有 DELETE 触发器**：DELETE 不记录；
- **没有操作者身份**：不知道是 Queen、哪个 Worker、还是 UI；
- **没有 DDL 记录**：建表/改表/删表不记；
- 最多 10,000 条；`prune_change_log` 在 `ensure_tracker_db` 和 `tracker_upsert` 后机会式裁剪。

### 8.3 UI 读取

`GET /api/colonies/{cid}/data/changes?since=<id>`：

- `since < 0`：只返回当前 cursor + covered，不返回历史；
- `covered`：有 `_tracker_chg_` 触发器的表名列表；
- 读取 `id > since`，最多 2000 条；
- 按 `(table, pk)` 去重，first op wins（同一行先 insert 后 update 仍读作 insert）；
- `truncated=true` 时客户端回退到通用刷新；
- 未注册表没有触发器；UI 用 `covered` 判断，回退到行数 diff。

### 8.4 一致性缺口

- 如果注册后表被 `DROP` 并重建，触发器随表消失；`_tracker_registry` 行还在；Worker upsert 会 `no such table`，UI change log 不再覆盖；
- 如果 `DROP TRIGGER`（schema-qualified 绕过）删除触发器，change log 静默停止；
- 重新登记可修复触发器，但不会回填丢失的历史；
- 变更日志不是审计日志，不能用于合规取证。

---

## 9. 与 Playbook、Reminder、Global DB 的集成

### 9.1 Playbook

- `pending=lambda: tracker_query("SELECT id FROM t WHERE status != 'done'")`；
- `tracker_query` / `tracker_count` 是**同步**的；内部 `execute_sql(row_cap=100_000)`；
- `_extract_rows_result` 处理 trailing `;` / `script`，取最后一个 rows 结果；
- 超过 100k 行会 `truncated=true`，日志 warning，但**不会静默丢**；
- `tracker_count` 是 `len(query_rows(...))`，不是 SQL `COUNT(*)`；如果 pending 用 `COUNT(*)`，会返回一行，`converge` 只派发一个 unit——这是 spec 里明确警告的坑；
- Playbook 只读 Tracker；Worker 写入仍走 `tracker_upsert`；runner 不持有业务状态。

### 9.2 Reminder

**Queen 的 `TrackerSnapshotReminderSource`**：

- 仅 Queen、有 binding；
- 在 `POST_TOOL_USE` 触发；
- 首次需要 3 个工具回合；之后冷却：
  - 无表：10 回合；
  - 有表：20 回合；
- 使用任何 `tracker_*` 工具会重置冷却；
- 读取最多 10 张表、50ms 预算、`mode=ro`；
- 目的：提醒 Queen “该建表/该把结果落表”，避免上下文裁剪丢失。

**Worker 的 `WorkerTrackerSnapshotReminderSource`**：

- 仅 Worker、有 binding；
- 每 20 次工具调用触发；
- 只在有表时提醒；
- 使用 `tracker_*` 工具会重置计数；
- 目的：提醒 Worker “先持久化再继续/失败就早报告”。

### 9.3 Global DB

`scope="global"`：

- `tracker_sql(scope=global)` → `global_db.client.sql`（云 Postgres）；
- `tracker_query(scope=global)` → `global_db.client.query`；
- `tracker_upsert(scope=global)` → `global_db.client.upsert`；
- `tracker_register_writable(scope=global)` → 返回“global 无 registry，自由 DDL”；
- Worker 被 `_reject_global_scope` 拒绝；
- 需要 `HIVE_CLOUD_JWT` / `HIVE_CLOUD_BASE`；未登录报 `NotSignedInError`；
- 写入后 `global_count_cache.mark_dirty()`，并对 `leads` 表推进本地 CRM 水位（可选 CRM 包）。

**本地 Tracker 与 Global DB 不是同一个东西**：

- 本地：Colony 私有、SQLite、Queen 任意 SQL、Worker upsert；
- 全局：团队共享、云 Postgres、Queen-only、自由 DDL、无 registry。

---

## 10. 生命周期

```text
建群/fork
  → ensure_tracker_db(colony_dir)
      → 创建 tracker/、tracker.db
      → PRAGMA user_version=3
      → 写 _tracker_meta.schema_version
      → backfill change triggers（schema<3）
      → prune change log
      → _patch_worker_configs（顶层 worker.json）
  → ColonyBinding 写入 worker.json.input_data

Queen 建表/种子
  → tracker_sql

Queen 登记
  → tracker_register_writable
      → _tracker_registry
      → change triggers

Worker 写
  → tracker_upsert
      → 单行 INSERT/UPDATE
      → change log
      → prune

Queen/Playbook 读
  → tracker_query / tracker_sql / playbook query_rows

UI 读
  → /var/tmp snapshot（2s TTL）
  → tables/rows/changes

删除 Colony
  → 整个 colonies/<c>/ 删除（含 tracker.db + wal/shm）

导入 Colony
  → 解包后 ensure_tracker_db 重新绑定路径
```

**启动回填**：

- `ensure_all_colony_tracker_dbs()` 遍历所有 Colony；
- 失败只 warning，不阻塞启动；
- 已存在 DB 不会重建缺失的框架表（只有 `user_version < 3` 才 bootstrap）；如果框架表被删而版本仍是 3，需要手工恢复。

**迁移**：

- v3 从 `data/tracker.db` → `tracker/tracker.db`；
- 旧 `ProgressDB` / `tracker_db_path` 字段被 `_patch_worker_configs` 清理；
- 没有业务表迁移框架；Queen 的 `ALTER TABLE` 就是迁移，但没有版本/回滚。

---

## 11. 失败模式与诊断

| 现象 | 原因 | 诊断/修复 |
|---|---|---|
| `no colony context` | 无 binding | 确认 Queen 已 fork 到 Colony / Worker 拿到 `input_data.binding` |
| `table ... does not exist` | 未建表 | `tracker_sql("SELECT name FROM sqlite_master WHERE type='table'")` |
| `key_columns ... not covered by a UNIQUE index` | 缺 PK/UNIQUE | 建 `PRIMARY KEY` 或 `CREATE UNIQUE INDEX` |
| `table ... is not registered for worker writes` | 未登记 | Queen `tracker_register_writable` |
| `columns not in write/key list` | Worker 写了未开放列 | Queen 扩大 `write_columns`，或 Worker 改列 |
| `row is missing key_columns` | Worker 没带 key | 任务文本里给全 key；检查切片 |
| `database is locked` | 写冲突/长事务 | 等 5s 重试；检查 `BEGIN IMMEDIATE`；降低并发 |
| `database disk image is malformed` | 损坏/并发写/NFS | 备份后 `PRAGMA integrity_check`；恢复快照 |
| `no such table` 但 registry 有行 | 表被 DROP/重建 | 重新建表并重新登记；清理 registry |
| change log 不更新 | 触发器被删/表未登记 | 重新 `tracker_register_writable`；检查 `sqlite_master` 的 `_tracker_chg_` |
| change log 没有 DELETE | 设计如此 | 用 `status='deleted'` 软删除，不要物理 DELETE |
| UI 数据旧几秒 | 2s 快照 | 接受；或直接查 DB |
| UI 503 `colony_data_timeout` | 15s 超时/NFS 慢 | 检查快照目录/磁盘；重试 |
| Worker 一直失败 | 列约束/唯一冲突/类型 | 看 sqlite error；检查 NOT NULL/UNIQUE/CHECK |
| 重复行 | key 为 NULL / 唯一索引缺失 / 重派 | 确保 key NOT NULL + UNIQUE；upsert 幂等 |
| 框架表被删 | schema-qualified `main._...` 绕过 | 从备份恢复；修复 validate_sql；重新 bootstrap |
| 磁盘涨 | change log / WAL / 大 JSON | prune 10k；WAL checkpoint；VACUUM（外部 CLI）；大结果落文件 |

### 11.1 诊断 SQL（Queen 可用 `tracker_sql`；外部可用 `sqlite3`）

```sql
-- 表
SELECT name, sql FROM sqlite_master WHERE type='table' ORDER BY name;

-- 注册表
SELECT table_name, write_columns, key_columns, mode, registered_at
FROM _tracker_registry ORDER BY table_name;

-- 唯一索引
PRAGMA index_list('leads');
PRAGMA index_info('idx_leads_id');

-- 变更日志
SELECT * FROM _tracker_changes ORDER BY id DESC LIMIT 20;

-- 触发器
SELECT name, tbl_name FROM sqlite_master WHERE type='trigger';

-- 健康
PRAGMA integrity_check;
PRAGMA journal_mode;
```

> `PRAGMA` 对 Agent 被 denylist 拒绝；以上诊断用外部 `sqlite3` CLI 或 UI 的只读接口。

---

## 12. 设计模式与最佳实践

### 12.1 一张表 = 一个工作单元

```sql
CREATE TABLE units (
    id TEXT PRIMARY KEY,            -- 自然键；NOT NULL
    status TEXT NOT NULL DEFAULT 'pending',
    result_json TEXT,               -- 结构化结果
    last_error TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    claimed_by TEXT,
    updated_at TEXT
);
CREATE UNIQUE INDEX idx_units_id ON units(id);
CREATE INDEX idx_units_status ON units(status);
```

登记：

```text
tracker_register_writable(
  table="units",
  write_columns=["status","result_json","last_error","attempts","claimed_by","updated_at"],
  key_columns=["id"]
)
```

### 12.2 Queen 只开放必要列

- `reviewed_at` / `score` / `owner` 等 Queen-only 列不放进 `write_columns`；
- Worker 只能改自己那一行的结果列；
- key 列自动可写（upsert 需要）。

### 12.3 切片不重叠

- 每个 Worker 只处理一组 key；
- 任务文本写清 `id` 列表/范围；
- 不要两个 Worker 写同一行；
- Playbook 的 `pending()` 返回未完成行；每轮重新查，不缓存。

### 12.4 幂等

- key 必须唯一且 NOT NULL；
- 重派同一行用 `tracker_upsert` 覆盖，不追加；
- 不要用 `COUNT(*)` 作为 pending 列表；
- Worker 的副作用（发邮件、提交表单）要单独幂等，Tracker 只能保证行不重复。

### 12.5 大结果放文件

- Tracker 存结果摘要/路径/状态，不存大二进制；
- 大 JSON 可以存 `result_json`，但注意 1000 行 × 大字段的查询成本；
- 需要完整内容时写 `data/` 文件，Tracker 存路径。

### 12.6 软删除

- 变更日志不记录 DELETE；
- 用 `status='deleted'` / `archived_at`，不要物理 DELETE；
- 需要真正清理时由 Queen 外部运维，不走 Agent。

### 12.7 不要用 Tracker 当事件日志

- `_tracker_changes` 只有 insert/update，没有 actor；
- 运行日志放 `colony.db` / `events.jsonl`；
- Tracker 只放业务事实。

### 12.8 备份

- 复制 `tracker.db` + `-wal` + `-shm`，或先 `PRAGMA wal_checkpoint(TRUNCATE)`；
- 不要只复制 `.db` 而丢 `-wal`；
- UI 的 `/var/tmp` 快照不是备份，可能落后几秒；
- 导入 Colony 时路径会重新绑定。

---

## 13. 评价

### 13.1 优点

- **极简但正确的协调模型**：一张表、一个 key、一个 upsert，就能让 N 个 Worker 并行填行。
- **Queen 拥有 schema**：业务形状未知时，运行时建模比预先写死 DAG 更灵活。
- **写入门禁清晰**：注册表 + 可写列 + key 唯一索引，Worker 不能改结构。
- **变更日志**：触发器在 DB 内，任何写入者都被记录，UI 能显示行级变化。
- **Playbook 天然续跑**：pending 是新鲜查询，不需要额外游标。
- **UI 快照**：把 NFS 读取问题隔离到 `/var/tmp`，避免阻塞主库。
- **安全边界分层**：Queen 原生 SQL、Worker 窄 upsert、global 仅 Queen、`_` 表保护。

### 13.2 风险

| 风险 | 说明 |
|---|---|
| **schema-qualified 绕过** | `main._tracker_registry` 可被 `tracker_sql` 修改/删除；影响 registry/change log |
| **无查询超时** | 长查询/长事务占住写锁；60s 工具超时不一定杀线程 |
| **无 DELETE/DDL 审计** | 物理删除、DROP/ALTER 无记录；变更日志不完整 |
| **无内置 claim/lease** | 并发切片靠 Queen/Playbook；重复派发可能双写 |
| **partial unique index** | 注册检查忽略 `partial`，可能运行时 ON CONFLICT 失败 |
| **NULL key** | 唯一索引允许多 NULL；upsert 可能重复 |
| **NFS 上的 WAL** | 部署风险；UI 快照只解决读，不解决写 |
| **单写者瓶颈** | 4 Worker 并发 upsert 实际串行；5s busy_timeout 后报错 |
| **无迁移/版本** | 业务表 schema 变了只能 ALTER，没有回滚 |
| **明文存储** | 无加密；文件权限即边界 |
| **Worker profile binding** | `profiles/*/worker.json` 不被启动回填 |

### 13.3 一句话

> **Tracker 是 Hive 的“业务真相层”：Queen 用 SQL 设计一行是什么，Worker 用 upsert 填行，Playbook 用 pending 查询续跑，UI 用变更日志看哪一行动了。**
> 它的简单性是最大优点，也是最大风险：schema 质量、key 设计、切片、幂等、SQL 边界，全靠 Queen 的 SQL 判断和部署纪律。

---

## 14. 源码地图

| 关注点 | 文件 |
|---|---|
| Schema / 连接 / denylist / execute | `core/framework/host/tracker_db.py` |
| 四个工具 / 角色 / global scope | `core/framework/tools/tracker_tools.py` |
| Binding 身份 | `core/framework/host/colony_binding.py` |
| Colony 路径 / tracker 路径 | `core/framework/config.py` |
| 建群时创建 tracker | `core/framework/server/routes_execution.py`、`routes_sessions.py` |
| 启动回填 | `core/framework/server/session_manager.py` |
| UI tables/rows/changes | `core/framework/server/routes_colony_workers.py` |
| 变更日志触发器 | `core/framework/host/tracker_db.py:install_change_triggers` |
| Tracker 提醒 | `core/framework/agent_loop/tracker_snapshot_reminder.py` |
| Playbook pending/count | `core/framework/tools/playbook_tools.py`、`host/playbook/runner.py` |
| Global DB | `core/framework/global_db/client.py`、`tracker_tools.py:_global_*` |
| 测试 | `core/tests/test_colony_db.py`、`test_colony_fork_flow.py`、`test_playbook_*` |
