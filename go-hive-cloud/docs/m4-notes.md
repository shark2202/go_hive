# M4 实现笔记 —— CRM grid 子集 + `grid_query` SQL 兼容层

> 状态：M4（2026-11-23，重试版）。范围=README §4 的 M4 行：`/v1/crm/*` 10 端点 +
> `grid_query` SQL 兼容层。
>
> 口径：**只实现能从 go_hive/Hive 开源客户端、文档、fixtures 恢复出请求/响应/错误形状的部分**。
> 10 个端点里 7 个（query / group-counts / get / create / patch / delete / stage）落地为
> 通用 grid 行操作；**schema / claim / release 保留 `planned` 且不注册**——它们的响应键、
> 投影形状、租约语义在开源材料里不可恢复。没有用空 200 或猜测字段填充。
>
> 证据基础：`go_hive/go-hive/internal/server/routes_colony_workers_global.go`、
> `core/framework/server/routes_colony_workers.py`、`go_hive/docs/architecture/contracts.md`、
> `core/framework/global_db/grid_query.py`、`go_hive/go-hive/internal/globaldb/grid_query.go`、
> `go_hive/go-hive/frontend/src/api/{colonyData,globalData}.ts`、worker-delegation SKILL。
> 冒烟证据：`evidence/m4-smoke.json`（26/26）。

## 1. 契约恢复表

| # | 端点 | 请求（恢复） | 响应（恢复） | 错误（恢复） | 证据 | 决定 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `GET /v1/crm/grid/schema` | 无 body | **UNKNOWN**：桌面代理纯透传；后端闭源；api-contract 只捕获到未登录 401 | 401 signed-out | Go proxy `:752`；Py proxy `1720-1721`；`contracts/api-responses/reference/crm_grid_schema.json` 仅 401 | **planned，不注册**（无 entity/column/stage 键可依） |
| 2 | `POST /v1/crm/grid/{entity}/query` | `{filter?: [{column, op, value}], search?, order_by?, order_dir?, limit?, offset?}`：`grid_query.py` 的 filter/search 形状 + 通用 grid 文档；Go 代理测试实际转发 `{"filter":[]}` | 通用 grid 行页 `{table, columns, primary_key, rows, total, limit, offset}`（`contracts.md` 明说 CRM grid 行操作返回 rows/columns 通用结构；`TableRowsResponse` 是客户端唯一该类类型） | `invalid JSON body`、`filter must be a list`、`each filter must be an object`、`invalid identifier: ...`、`unknown column: ...`、`unsupported filter op: ...`、`invalid limit/offset`、未知实体 404、401 signed-out | Py `1430-1490`、`1724-1727`；Go `:755`；`contracts.md:175-176`；`globalData.ts` | **implemented**（参数化 SQL） |
| 3 | `POST /v1/crm/grid/{entity}/group-counts` | `{group_by, filter?, search?}` | `{table, group_by, groups:[{value, count}]}`（与通用 grid group-counts 相同） | `group_by is required`、`filter must be a list`、未知列 400、未知实体 404 | Py `1508-1560`、`1730-1733`；Go `:759`；`globalData.ts:128-140` | **implemented** |
| 4 | `GET /v1/crm/grid/{entity}/{id}` | 路径 `entity`/`id` | 单记录；按通用行契约返回单行 `{table, columns, primary_key, rows:[row], total:1, limit:1, offset:0}`；**闭源后端的具体单记录信封 UNKNOWN** | `row not found` 404、未知实体 404 | Go `:763`；Py `1736-1740`；`contracts.md:175` | **implemented**（单行页） |
| 5 | `POST /v1/crm/grid/{entity}` | `{fields, identities?, because?}`（body 形状逐字来自 Py docstring） | 通用行页（创建后的记录）；后端投影响应 UNKNOWN | `invalid JSON body`、`fields must be a non-empty object`、`identities ... UNKNOWN` 400、未知实体 404 | Py `1744-1746`；Go `:768`；`contracts.md:175-176` | **implemented**（fields；identities 仅空值） |
| 6 | `PATCH /v1/crm/grid/{entity}/{id}` | `{fields?, identities?, because?}` | 通用行页（更新后的记录） | `unknown column` 400、`row not found` 404、identities 400、未知实体 404 | Py `1750-1753`；Go `:772` | **implemented** |
| 7 | `DELETE /v1/crm/grid/{entity}/{id}` | body 可选；`{because?}`；**soft-delete (archive)** | `{deleted: int}`（与通用行删除一致） | `because must be a string` 400、未知实体 404 | Py `1757-1760`；Go `:777` | **implemented**（软删除） |
| 8 | `POST /v1/crm/grid/{entity}/{id}/stage` | `{value, because?}`；语义=kanban 阶段移动，后端校验 entity+stage 并记 `stage_changed` | 通用行页（更新后的记录） | `value is required` 400、`row not found` 404、未知实体 404 | Py `1764-1771`；Go `:782`；stage 值由团队配置（`gridUtils.stageLabel.test.ts`） | **implemented**（不做枚举/转移图校验） |
| 9 | `POST /v1/crm/claim` | `{ids: [...], because?}`；CLI 语义：原子抢占未被占用的人，已占用的从 `skipped` 返回 | **UNKNOWN**：胜者列表的键名、租约持有人、租约时长均无开源证据 | 401 | Py `1775-1776`；Go `:787`；SKILL.md:76 | **planned，不注册** |
| 10 | `POST /v1/crm/release` | `{ids: [...], because?}`；语义：释放抢占，供其他 colony 再抢 | **UNKNOWN**：响应键、所有权模型 | 401 | Py `1780-1781`；Go `:790`；SKILL.md:77 | **planned，不注册** |

结论：**7 implemented / 3 planned**。`cloud-surface.json` 的 status 与生成器 `CURATED` 一致；
`TestCloudSurfacePlannedRoutesUnregistered` 断言 schema/claim/release 仍是 404/405。

## 2. 已实现行为（有证据支撑）

### 2.1 实体与存储

- 实体注册表=`people` / `companies` / `opportunities`。来源：worker-delegation SKILL 与 queen prompt
  对 `hive-crm` 的描述“people, companies, opportunities”。桌面视图标签是 People/Orgs/Deals/Activity
  （`routes_colony_workers.py:1869`），但**路径 slug 不可恢复**，因此不把 `orgs`/`deals` 猜成别名。
- 每 team 独立 SQLite（沿用 M1 的 `store.Team`）；CRM 表名 `crm_<entity>`，团队 DB 初始化时建三张基础表，
  与 global-db 表同库但**从 `GET /v1/global-db/tables` 与 `GET /v1/global-db/changes` 排除**（`crm_` 前缀）。
  跨 team 隔离由 team DB 保证。
- 基础列：`id`（服务端生成的不透明 32 hex）、`stage`、`created_at`、`updated_at`、`archived_at`。
  这些是**本实现的存储列**，不是对真实 CRM 字段模型的声明。create 的 `fields` 首次出现时通过
  `ALTER TABLE ADD COLUMN` 建立团队自定义列（真实 CRM 的字段 schema 由闭源后端管理，UNKNOWN）。
- `id`/`created_at`/`updated_at`/`archived_at` 在 `fields` 中保留；`stage` 可写。

### 2.2 query / group-counts

- 请求体逐字采用 `grid_query.py` 的形状：`filter:[{column,op,value}]`、`search`、`order_by`、
  `order_dir`、`limit`、`offset`；`limit` 夹到 1..500，`offset` >=0（与 runtime 的
  `max(1,min(500,...))`/`max(0,...)` 一致）。
- 支持 ops：`eq/ne/gt/gte/lt/lte`、`contains/starts_with/ends_with`、`is_empty/is_not_empty`；
  LIKE 元字符（`\`、`%`、`_`）转义并声明 `ESCAPE '\'`。
- 过滤列、排序列、分组列都必须在实体真实列集合里；越界返回 400（`invalid identifier` /
  `unknown column`），不会进 SQL。
- 所有用户值走 `?` 绑定参数；只有经过白名单校验的标识符进入 SQL 文本。
- 软删除记录默认从 query/get 中排除（`archived_at IS NULL`）。
- group-counts 上限 200 组，按 `count DESC, value ASC` 排序（与 `grid_query.build_group_counts` 一致）。

### 2.3 create / patch / delete / stage

- `create`：要求非空 `fields` 对象；服务端生成 id/时间戳；写 `_cloud_changes`（op=insert）与
  `_cloud_audit`（action=`crm_create`，`because` 入 detail）。
- `patch`：`fields` 可省略；只允许已存在列；空 patch 返回当前记录；写 change/audit。
- `delete`：软删除（写 `archived_at`），返回 `{deleted: n}`；重复删除返回 0；body 可选且容忍
  非 JSON（与桌面代理对 DELETE 的 null 透传一致）。
- `stage`：要求非空字符串 `value`；写 `stage` + `updated_at`；不做枚举/转移图校验（团队自定义
  stage 值，`gridUtils.stageLabel.test.ts` 明确“team configures its own pipeline names”）。
- `identities`：请求键已恢复，但形状/去重语义不可恢复；**非空值返回 400**，不静默忽略。
- `because`：字符串审计理由，写 `_cloud_audit`。

### 2.4 响应形状

统一使用客户端已有的通用 grid 类型（`go_hive/go-hive/frontend/src/api/colonyData.ts`
`TableRowsResponse` / `globalData.ts` `GroupCount`）：

```json
{"table":"crm_people","columns":[...],"primary_key":["id"],"rows":[{...}],"total":1,"limit":1,"offset":0}
{"table":"crm_people","group_by":"stage","groups":[{"value":"new","count":1}]}
{"deleted":1}
```

`create/patch/get/stage` 返回单行页（`total=1, limit=1, offset=0`）。闭源后端是否返回更丰富的
单记录信封/投影字段属于 `UNKNOWN`，未伪造。

### 2.5 错误面

| 场景 | 状态 | body |
| --- | --- | --- |
| 无/坏 JWT | 401 | `{"error":"Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)."}` |
| body 非 JSON | 400 | `{"error":"invalid JSON body"}` |
| 未知实体 | 404 | `{"error":"unknown entity: <name>"}` |
| filter 非 list | 400 | `{"error":"filter must be a list"}` |
| filter 元素非对象 | 400 | `{"error":"each filter must be an object"}` |
| 非法/未知列 | 400 | `{"error":"invalid identifier: '...'"}` / `{"error":"unknown column: ..."}` |
| 不支持的 op | 400 | `{"error":"unsupported filter op: '...'"}` |
| limit/offset 非数 | 400 | `{"error":"invalid limit/offset"}` |
| group_by 缺失 | 400 | `{"error":"group_by is required"}` |
| 记录不存在 | 404 | `{"error":"row not found"}` |
| create 缺 fields | 400 | `{"error":"fields must be a non-empty object"}` |
| identities 非空 | 400 | `{"error":"identities is not supported by this private deployment (shape UNKNOWN)"}` |
| stage 缺 value | 400 | `{"error":"value is required"}` |

`invalid identifier: ...`、`unknown column: ...`、`unsupported filter op: ...` 与
`grid_query.py` 的 `SqlBuildError` 文案同族；`filter must be a list`、`group_by is required`、
`invalid limit/offset` 与 runtime 的 global grid 错误文案一致。

## 3. `grid_query` SQL 兼容层

`core/framework/global_db/grid_query.py` 是桌面代理的 SQL 构造信任边界，生成的 Postgres 风格
语句会打到 `/v1/global-db/sql|query`。本服务必须接受这些语句；M4 做了两层：

1. **原始 SQL 兼容**（已有 `internal/store/dialect.go` + M4 回归）：
   - `SELECT * FROM "t" WHERE ... ORDER BY "c" DESC LIMIT n OFFSET m`
   - `SELECT count(*) AS total FROM "t" WHERE ...`
   - `SELECT "c" AS value, count(*) AS count FROM "t" ... GROUP BY "c" ORDER BY count(*) DESC, "c" ASC LIMIT n`
   - `DELETE FROM "t" WHERE "c" = literal`（经 `/v1/global-db/sql`，不是 `/query`）
   - `ILIKE` → `LIKE`、`TRUE/FALSE` → `1/0`、`NOW()` → `CURRENT_TIMESTAMP`、`::text/::varchar/...` 去尾。
   测试直接使用 `test_grid_query.py` 的逐字 SQL（见 `internal/httpapi/m4_contract_test.go:TestGridQuerySQLCompatibility`）。
2. **参数化 grid 构造**（M4 新增 `internal/store/gridquery.go`）：
   - CRM query/group-counts 不接收自由 SQL；从 `{filter,search,order_by,...}` 构造带 `?` 的语句。
   - 标识符 `[A-Za-z_][A-Za-z0-9_]*` + 实体真实列白名单；值全部绑定，LIKE 模式转义。
   - `/v1/global-db/query` 仍只读（gate 在翻译前后各检查一次；数据修改 CTE 也拒绝）。

**SQLite 近似**：SQLite `LIKE` 默认只对 ASCII 大小写不敏感；Postgres `ILIKE` 对 Unicode 也做
大小写折叠。非 ASCII 大小写匹配与真实云的差异记录为已知近似，不冒充完全一致。

## 4. 未实现 / UNKNOWN（保持诚实）

| 项 | 状态 | 本服务行为 |
| --- | --- | --- |
| `GET /v1/crm/grid/schema` 的响应键（entities/columns/options/labels/stages） | `UNKNOWN` | 不注册；GET 得到 405/404，绝不空 200 |
| claim/release 的响应键、租约持有人/时长、同用户多 colony 的锁语义 | `UNKNOWN` | 不注册；404 |
| CRM 投影（FK 解析、custom-field flattening、owner/display 字段、label） | `UNKNOWN` | 返回原始存储行；`ColumnInfo.label` 不伪造 |
| 真实 CRM 实体路径 slug（People/Orgs/Deals/Activity 对应值） | `UNKNOWN` | 只接受 people/companies/opportunities |
| `identities` 的形状与去重规则（email/linkedin 等） | `UNKNOWN` | 非空即 400；不猜 |
| 字段 schema / 必填 / 类型 / 自定义字段管理 | `UNKNOWN` | create 时动态建列；patch 只改已有列 |
| stage 枚举、转移图、`stage_changed` interaction | `UNKNOWN` | 接受任意非空 stage 值，不做转移校验 |
| 记录 id 格式、创建/更新时间戳格式 | `UNKNOWN` | 32 hex 不透明 id；RFC3339Nano UTC |
| 软删除记录的可见性/恢复规则、真实 DELETE 状态码 | `UNKNOWN` | 默认从 query/get 排除；delete 返回 `{deleted:n}` |
| 真实云单条 GET/POST/PATCH 的响应信封与状态码 | `UNKNOWN` | 通用单行页 + 200 |
| 授权/角色（谁可写 CRM）、配额/计费 | `UNKNOWN` | 只校验 operator JWT + team 隔离 |

**没有做的事**：没有实现 schema/claim/release；没有猜 entity slug 别名；没有假装 identities 去重；
没有生成投影字段；没有把 `planned` 端点注册成空 200。

## 5. 验证

```bash
cd go_hive/go-hive-cloud
gofmt -l .
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
python3 scripts/gen_cloud_surface.py --check
./scripts/m4-smoke.sh        # evidence/m4-smoke.json
./scripts/smoke.sh           # M1 + M2 + M3 + M5 + M4（保留各阶段）
```

`evidence/m4-smoke.json`：**26/26**，包括

- 真实 go_hive `globaldb.Client` 跑 `grid_query.py` 逐字 SQL：select/filter/sort、count/search、
  group-counts、delete、`ILIKE` 大小写、只读 gate（DELETE → 400）；
- 原始 operator JWT 跑 CRM：401、create、get、filter+search、group-counts、patch、stage、
  soft-delete、归档后 query/get、未知实体 404、filter 400、identities 400、CRM 表不出现在
  global-db 目录、schema/claim/release 仍 404/405。

L1 测试：

- `internal/store/gridquery_test.go`：参数化、标识符白名单、LIKE 转义、排序/分页夹取、group-counts。
- `internal/store/crm_test.go`：CRUD/软删除/stage/隔离/注入拒绝。
- `internal/httpapi/m4_contract_test.go`：HTTP 契约、错误文案、grid_query SQL 兼容、只读 gate。

## 6. 边界

- 本实现是“**客户端契约兼容的私有化 CRM grid**”，不是闭源 CRM 服务的等价物：投影、schema、
  租约和字段模型都不在开源侧。
- 若拿到真实云登录态/响应，应优先补：schema 响应形状 → claim/release 响应与租约 → 投影字段；
  在此之前保持 `planned`/`UNKNOWN`。
- 不改 go_hive 主模块，不新增第三方依赖；CRM 存储沿用 team SQLite 与既有 gate/dialect/audit。
