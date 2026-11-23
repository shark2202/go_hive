# M1 实现笔记 —— 骨架服务落地的规则、近似与未知

> 状态：M1（2026-11-22）。范围=README §4 的 M1 行：`serve`/`token`、自签 JWT + streamToken、
> `/v1/global-db/*` 8 端点、`/v1/inbox/{messages,resolve,runtime/stream}`、`/v1/gateway/availability`、
> `/user/refresh-stream-token-from-stream`、内部扩展 `POST /internal/inbox/reply`、SQLite 存储 + 方言层、
> L1 契约测试、L2a 真客户端冒烟。
>
> 口径：本里程碑只声称**客户端可观察子集**。不声称与真实云内部语义一致；契约未记录的行为一律按
> `contracts/observed-contract.json` 的 `unknown` 处理，不做假绿。

## 1. 交付物与验证路径

| 层 | 实现 | 验证 |
| --- | --- | --- |
| L1 契约 | `internal/httpapi` 15 条路由（13 契约端点 + `/health` + 内部扩展）；路由集合与 `contracts/cloud-surface.json` 的 implemented 集合强制相等 | `go test ./...`：golden（`internal/httpapi/testdata/*.json`）、401 逐字、门禁、SSE、streamToken 往返、surface 覆盖测试 |
| L2a 真客户端 | `harness/globaldb-smoke`（独立 module，`replace github.com/shark2202/go_hive/go-hive => ../../../go-hive`） | `scripts/smoke.sh` → `evidence/globaldb-smoke.json`，14 项检查全过 |
| 门禁 | gofmt / go vet / go test | 见 §10 |

`go test ./...` 覆盖 cloud module 全部包（cmd 无测试）；harness 是嵌套 module，单独 `go vet/go build`。

## 2. 存储

- 每 team 一个 SQLite 文件：`<data_dir>/teams/<team_id>.db`（目录 0700）。team_id 来自 JWT，
  作为文件名前先按 `[A-Za-z0-9._-]{1,64}` 校验（`auth.ValidTeamID`）。
- 连接：`modernc.org/sqlite`（纯 Go，CGO_ENABLED=0 可构建），rw 池 `busy_timeout(5000) + journal_mode(WAL)`；
  `/query` 走独立只读池（`mode=ro&query_only(1)`），双保险（门禁 + 连接属性）。
- 内部表（对客户端不可见、门禁拒访）：
  - `_cloud_changes(cursor, "table", pk, op, at)`：变更游标（自增整数，响应里以字符串回传）。
  - `_cloud_audit(id, at, user_id, team_id, action, table_name, detail)`：所有写操作/收件箱操作审计。
  - `inbox_messages / inbox_message_reads / inbox_replies / runtime_connections`：收件箱表
    （字段依据 reverse-engineering §5；`inbox_replies` 是自建投递队列，真实云端点未知）。
- 全局表清单排除 `_*`、`sqlite_*` 与上述收件箱表（真实云端把 inbox 放在其他 schema，这里同库分表）。

## 3. 语句门禁（`internal/store/gate.go`）

| 规则 | 行为 |
| --- | --- |
| 单语句 | 顶层 `;` 后还有语句 → 400 `only a single SQL statement is allowed` |
| denylist | `ATTACH/DETACH/PRAGMA/VACUUM/REINDEX`、`load_extension(` → 400 `<KEYWORD> is not allowed` |
| 内部对象 | 标识符以 `_` 或 `sqlite_` 开头（含双引号形式）→ 400 `access to internal object "..." is not allowed` |
| 只读 | `/query` 仅允许 `SELECT/WITH/VALUES/EXPLAIN`；数据修改型 CTE（`WITH ... DELETE`）同样拒绝 → 400 `only read-only statements are allowed on /query` |
| 超时 | 默认 5s（`--statement-timeout` 可调）→ 408 `statement exceeded 5s` |
| 行数 | `row_cap` 截断，响应 `truncated: true`；缺省用 `--row-cap`（默认 1000，0=不限） |
| 大小 | SQL 文本 ≤ 4MB；请求体 global-db ≤ 8MB、inbox ≤ 1MB |

扫描是 token 化的：字符串字面量/注释里的关键字不触发（测试覆盖 `SELECT 'ATTACH PRAGMA; VACUUM'`）。

## 4. SQLite 方言翻译（`internal/store/dialect.go`）

`grid_query.py` 生成的是 Postgres SQL；M1 的翻译规则：

| Postgres | SQLite | 说明 |
| --- | --- | --- |
| `x ILIKE p ESCAPE '\'` | `x LIKE p ESCAPE '\'` | SQLite `LIKE` 默认对 ASCII 大小写不敏感；**近似**：非 ASCII 不做 Unicode case folding（Postgres 按 locale 折叠）。连接不设 `case_sensitive_like`。 |
| `TRUE` / `FALSE` | `1` / `0` | grid_query 的 `sql_literal` 会发 `TRUE/FALSE`；SQLite 3.23+ 也认，但显式归一避免版本差异。 |
| `NOW()` | `CURRENT_TIMESTAMP` | 空括号一并吞掉（`CURRENT_TIMESTAMP()` 是语法错误）。 |
| `x::text` `::varchar` `::char` `::uuid` `::json` `::jsonb` `::citext` `::timestamp(tz)` | 去掉 cast | SQLite 无静态类型，这些 cast 对 team 表是 no-op。 |
| `x::int` / `::numeric` 等数值 cast | 原样透传 | **未支持**：SQLite 中去掉会改变比较语义；出现时走 400（错误信息来自 SQLite）。记录为已知缺口。 |
| `CAST(x AS TEXT)`、`count(*)`、`<>`、`LIMIT/OFFSET`、`GROUP BY`、`ORDER BY`、双引号标识符 | 原样 | 本来就兼容。 |

翻译同样 token 化：字符串/引号标识符/注释内的 `ILIKE`、`TRUE`、`NOW()` 不会被改写（有测试）。

已知近似（写给后续里程碑）：
1. 大小写折叠仅 ASCII（grid_query 的搜索/contains 场景）。
2. `::int` 等数值 cast 未翻译。
3. Postgres 专有函数（`jsonb_*`、`gen_random_uuid()`、`regexp_match`）未实现 → 400。
4. 复杂 DDL（`SERIAL`、`JSONB` 列、外键、部分索引）会被 SQLite 以宽松类型接受或报错；真实云为
   Postgres 语义，这里只保证 grid_query 生成的形状。

## 5. 响应形状（客户端解析面）

| 端点 | 响应（本实现） | 依据 |
| --- | --- | --- |
| `POST /v1/global-db/sql` `/query` | `{"rows":[{...}], "rowCount":N, "truncated":bool}` | Python `result.get("rows")`、`result.get("rowCount", 0)`（routes_colony_workers.py:1477/1684） |
| `POST /v1/global-db/upsert` | `{"table","inserted","updated","pk"}`；`mode=insert` 冲突 → 409 `{"error":"conflict","message":...,"pk":{...}}` | 运行时读 `result.get("inserted")`、`e.status==409`（routes_colony_workers.py 插入路径） |
| `POST /v1/global-db/import` | `{"table","inserted","total","pk"}` | 无客户端字段依赖（任意 JSON），保留可读冗余 |
| `GET /v1/global-db/tables` | `{"tables":[{name, columns:[{name,type,notnull,pk,dflt_value}], row_count, primary_key}]}` | `count_cache.record_global_tables` 读 `name/row_count`；前端 `TableOverview` 同形（globalData.ts:78） |
| `GET /v1/global-db/changes` | `{"cursor": str|null, "covered":[str], "truncated":bool, "tables":{t:{count,rows:[{pk,op}]}}}`；无 `since` 只回 cursor+covered | 前端 `GlobalChangesResponse`（globalData.ts:13-20）；`since` 原样接受 |
| `GET .../tables/{t}/rows` | `{"table","columns","primary_key","rows","total","limit","offset"}` | `_global_introspect` 读 `columns/primary_key`（routes_colony_workers_global.go:139-146）；前端 `TableRowsResponse` |
| `PATCH .../tables/{t}/rows` | `{"updated":N}`；pk 不存在 → 404 `row not found` | 前端 `updateRow` 返回 `{updated}`（globalData.ts:107-111） |
| `POST /v1/inbox/messages` | 200 `{"id": "..."}` | notifier.go:325-329 读 `id` |
| `POST /v1/inbox/resolve` | 200 `{"resolved":N}` | notifier.go 只判 `<300` |
| `GET /v1/inbox/runtime/stream` | SSE：`event: reply` + `data: {"correlation_token","text","colony_id"?,"message_id"?}` | listeners.go:532-554（`colony_id` 可选） |
| `GET /v1/gateway/availability` | 200 `{"available":true,"gateway":"hive-cloud","from":...}` | 客户端不解析（loader.go:1133） |
| `POST /user/refresh-stream-token-from-stream` | 200 `{"streamToken": new}`；无效/过期 → 401 `{"error":"hive_stream_token_invalid"}` | streamtoken_refresh.py:154-186 |
| `POST /internal/inbox/reply` | 200 `{"ok":true,"message_id","reply_id","delivered"}` | **本项目扩展**，不在契约内 |

契约歧义（登记，未解决）：`contracts/observed-contract.json` 把 401 body 记为 `{"error":"signed_out"}`，
而任务/客户端文案是逐字句子。客户端在 401 时根本不读 body（Go 返回固定 `Cloud session rejected (401)`，
Python 同），因此 M1 统一返回
`{"error":"Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)."}`
（逐字句子优先）。真实云端 401 body 仍属 `UNKNOWN`。

## 6. 认证与令牌

- operator JWT：HS256，claims `{sub, user_id, team_id, exp, iss:"hive-cloud"}`；`hive-cloud token` 签发，
  TTL 默认 24h。Header 同时接受 `jwt <token>`（后端约定）与 `Bearer <token>`（运行时约定）。
- streamToken：同 HS256 形状 + `typ:"stream"`，TTL 固定 24h；`/user/refresh-stream-token-from-stream`
  仅凭旧 token 签名验身，拒绝过期/错误签名/非 stream 类型，返回新 token。streamToken **不能**当 operator
  JWT 用于其它端点（401）。
- 401 body 逐字见 §5；`/health`、`/v1/gateway/availability`、stream-token 刷新为公开面。
- 密钥：`--jwt-secret/--stream-secret` 或 env，缺省从 `<data_dir>/jwt_secret`、`<data_dir>/stream_secret`
  读取（首次生成 0600）。双钥轮换窗口：`auth.VerifyAny` 支持多 secret，M3 接入配置。

## 7. 变更流与审计语义

- upsert/import/PATCH 精确记录：`{pk, op in insert|update|import}`。
- `/sql` 写语句按语句分类记录一条：`insert/update/delete/schema`；`pk` 留空（无法从任意 SQL 反解行身份 —
  已知近似）。
- `/sql` 的一次多行 INSERT 记一条变更（不是每行一条），与真实云的行级触发器不同；UI 的
  count 刷新不依赖条数。`changes` 单次最多 500 行，超出 `truncated:true`。
- `since` 必须是本服务返回的十进制 cursor；其它格式 → 400（真实云的 cursor 格式 `UNKNOWN`）。
- 审计：`_cloud_audit` 记录 sql/upsert/import/update/inbox_message/inbox_resolve/inbox_reply。

## 8. 收件箱与 SSE

- 消息 kinds 限定 `blocker|heartbeat|progress|done`（notifier 默认 blocker）。
- `runtime/stream`：无总超时；连接先注册 `runtime_connections` 并订阅 hub，再写 `: connected`；
  keepalive 15s（测试 100ms）。回复先落 `inbox_replies`（`delivered_at IS NULL`），连接建立时补投，
  实时投递成功后标记已投递（避免重连重复）。
- `kind` 查询参数只作连接登记，不参与回复过滤（真实云端过滤语义 `UNKNOWN`）。
- `POST /internal/inbox/reply` 用 operator JWT 决定 team；body `{correlation_token,text,team_id?}`，
  team 不匹配 → 403。这是本项目扩展，不是契约兼容面（真实 Inbox UI 端点 `UNKNOWN`）。
- 桌面 `GET /v1/inbox/stream` 仍为 `UNKNOWN`，未实现（契约覆盖测试跳过 `status:"unknown"`）。

## 9. 依赖

- `modernc.org/sqlite v1.60.1`（与 go_hive 主模块同版本），全部依赖已在 lane 模块缓存中，
  `CGO_ENABLED=0 GOPROXY=off` 可构建；go directive `1.26.11`。
- 其余仅标准库（`crypto/hmac`、`encoding/base64`、`net/http`、`database/sql`…），无 JWT 第三方库。
- 未引入 Postgres 驱动：M1b 的 PG 适配器需要联网窗口补装（缓存中无 pgx）。

## 10. 运行与门禁

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh   # go1.26.11 + 模块缓存
cd go_hive/go-hive-cloud
gofmt -l .            # 期望无输出
go vet ./...
go test ./...         # L1：golden/401/门禁/SSE/streamToken/契约覆盖
./scripts/smoke.sh    # L2a：真客户端 → evidence/globaldb-smoke.json
# golden 更新（仅在有意改变响应形状时）：
go test ./internal/httpapi -update-golden
```

harness 单独构建：`cd harness/globaldb-smoke && go vet ./... && go build ./...`。

## 11. 已知未知 / 后续里程碑

| 项 | 状态 |
| --- | --- |
| 真实云端 401 body（`signed_out` vs 句子） | `UNKNOWN`（§5 登记；按逐字句子实现） |
| JWT 签发方/算法/登录流 | 自建 HS256；真实云 `UNKNOWN` |
| `POST /user/refresh-stream-token`（桌面 legacy） | 未实现（契约 `unknown`；M3） |
| streamToken 轮换/吊销（sid blacklist、master rotation） | M3 |
| `GET /v1/inbox/stream`（桌面 SSE） | `UNKNOWN`，未实现 |
| Inbox UI | 闭源；自建前端走 `/internal/inbox/reply` |
| `/v1/crm/*` 10 端点 + 实体/阶段/claim 语义 | M4；契约覆盖测试显式排除并断言 404 |
| LLM 代理 `/v1/messages`、`/v1/images/generations` | M5（另一 host） |
| 凭据代理 `/v1/credentials*` | M6（另一 host） |
| `cloud_sync` 对账协议 | `UNKNOWN`（不在 M1） |
| 限流/配额/审计格式 | M2；当前只有审计表与请求体/行数上限 |
| `config.json` 的 `features{...}` 开关 | 未实现（配置文件只读 listen/data_dir/secrets/timeout/row-cap） |
| Postgres 存储适配器 | M1b（缓存无驱动，登记待联网） |
| 桌面端能否改云基址 | `UNKNOWN`（M0 已登记） |
| 全量端点/SSE/WS 登记与未知项（含 senders 8 个 unknown-path 方法） | `contracts/cloud-surface.json` + `docs/cloud-surface.md`；`scripts/gen_cloud_surface.py --check` 强制 |

## 12. 证据

- `evidence/globaldb-smoke.json`：`scripts/smoke.sh` 生成，14 项真客户端检查（全过），含时间戳与 base URL。
- L1 golden：`internal/httpapi/testdata/`（health、gateway、global-db 各响应、changes、SSE 帧）。
- 本文件 §5 的契约歧义与 §4 的方言近似是本里程碑的诚实边界。
