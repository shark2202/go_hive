# 私有化部署架构（go-hive-cloud）

> 状态：M0 设计（2026-11-22），M1 已按本文 §2–§5 落地（实现细节与近似见 `docs/m1-notes.md`）；M2 完成 Inbox 契约硬化与 L2b 真客户端闭环（见 `docs/m2-notes.md`）。目标：单仓库自建服务，替换 hive-backend + llm 代理 + 凭据代理的客户端契约面。

## 1. 形态

- **服务**：Go 单进程 `hive-cloud`（无 cgo）。子命令：`serve`（HTTP+SSE）、`token`（签发 operator JWT / streamToken）、`admin`（团队/表/成员运维，M2+）。
- **配置**：`config.json`（0600）或环境变量：`listen`、`data_dir`、`jwt_secret`、`stream_secret`、`bootstrap_team`、`features{inbox,globaldb,crm,llmproxy,credentials}`。
- **部署**：单机 systemd/launchd（M7）+ docker compose（本机 Docker.raw 可用）。生产推荐 Postgres；开发/小规模 SQLite。
  M7 的 unit/plist/compose、备份/恢复/升级脚本与演练见 `deploy/README.md`、`docs/m7-notes.md`。

## 2. 存储策略（关键决策）

| 方案 | 优点 | 风险 |
| --- | --- | --- |
| **Postgres（推荐生产）** | `/v1/global-db/sql` 的自由 SQL（`ILIKE`/`ESCAPE`/`CAST…AS TEXT`、DDL）**原样兼容**；schema-per-team 天然 | 依赖外部数据库（compose 提供） |
| **SQLite 纯 Go（M1 起步）** | 零外部依赖、单二进制 | **方言差异**：`ILIKE`、`ESCAPE '\'`、`count(*)` 语义等需翻译层；grid_query 生成的是 Postgres 风格 SQL，必须过方言转换（`ILIKE`→`LIKE` + `COLLATE NOCASE` 或 `lower()` 包装） |

要求：存储层做接口抽象（`store.Store`）；M1 交付 SQLite 实现 + 方言翻译（记录翻译规则与已知近似）；**Postgres 适配器为 M1b**（缓存中如无 pg 驱动则先登记，联网窗口补装）。团队隔离：每 team 独立 schema（PG）或独立文件（SQLite：`data/teams/<team_id>.db`）。

## 3. 认证与令牌

- **JWT**：operator 自签 HS256，claims `{sub, user_id, team_id, exp, iss:"hive-cloud"}`；`hive-cloud token` 生成（运维注入客户端 `HIVE_CLOUD_JWT`）。
  客户端只做两件事：不验签读 `exp`；服务端从 JWT 解析 `team_id/user_id`（客户端从不传）。
- **streamToken**：同样是 HS256 JWT 形状（`{typ:"stream", user_id, team_id, exp:+24h}`），保证客户端 `decode_jwt_exp` 可读；`/user/refresh-stream-token-from-stream` 以**旧 token 签名**为身份证明签发新 token；`/user/refresh-stream-token`（legacy）接受 user JWT。
- 401 文案逐字：`Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).`

## 4. Inbox

- 表：`inbox_messages / inbox_message_reads / runtime_connections`（字段见逆向文档 §5）。
- `POST /v1/inbox/messages`：写入 `kind in {blocker,heartbeat,progress,done}`，返回 `{"id": ...}`；`POST /v1/inbox/resolve`：按 `session_id` 关单。
- `GET /v1/inbox/runtime/stream`：SSE 长连接（无总超时），事件 `event: reply` + `data {"correlation_token","text"}`；按 `runtime_id/kind` 过滤，连接注册进 `runtime_connections`。
- **回复投递方（Inbox UI）闭源、其端点 `UNKNOWN`**：M1 提供**内部管理端点** `POST /internal/inbox/reply {correlation_token,text}`（明确标注为本项目扩展，不算契约兼容面），用于 L2 端到端验证与自建 UI 集成。
- M2：三条已观察端点的字段/错误码/auth/SSE 帧由 `internal/httpapi/inbox_contract_test.go` 固定；每次 inbox 写操作记录 `_cloud_audit`；可选 per-team 限流（`--inbox-rate-limit`，默认关闭，429 + `Retry-After`）。桌面 UI 的列表/回复/已读/未读/心跳与 `GET /v1/inbox/stream` 只有文档路径、无结构契约，登记 `unknown` 且不实现（404/405，绝无空 200）。

## 5. Global DB / CRM

- `/v1/global-db/*` 8 端点按逆向文档实现；`/sql` 与 `/query` 过**语句门禁**：单语句、denylist（`ATTACH/DETACH/PRAGMA/VACUUM/REINDEX/load_extension` 及 `_*` 表，镜像 tracker 语义）、statement timeout（默认 5s）、`row_cap` 截断、只读连接执行 `/query`。
- `changes`：服务端维护 `_cloud_changes(cursor, table, pk, op, at)`，返回 `{cursor, tables}`，`since` 原样接受；写路径（upsert/import/rows PATCH/sql-NOSUPERUSER 写）自动记变更。
- CRM（M4）：`people/companies/opportunities` 三实体落在每 team SQLite 的 `crm_*` 表；query/group-counts/get/create/patch/delete/stage 使用通用行页 + 参数化 `grid_query` 过滤层。schema/claim/release 的响应键与租约语义 `UNKNOWN`，保持 `planned` 不注册（`docs/m4-notes.md`）。

## 6. LLM 代理与凭据代理（M5/M6）

- LLM 代理：Anthropic Messages 透传 + `/v1/images/generations`（上游供应商键由部署方配置）；streamToken 校验本地签发即可。
- 凭据代理：`/v1/credentials*` 兼容层（存储/刷新/OAuth 回调流程需按 M6 设计；字段以 Python dataclass 为准）。

## 7. 验证（不假绿）

| 层 | 手段 | 证据落盘 |
| --- | --- | --- |
| L1 | 契约 golden 测试：每个端点形状/状态码/错误文案；`contracts/observed-contract.json` 驱动 | `testdata/` + 测试报告 |
| L2a | **真实客户端**：小 Go 程序 `harness/globaldb-smoke`（module replace → `../go-hive`）驱动 `internal/globaldb` 全方法打自建服务 | `evidence/globaldb-smoke.json` |
| L2b | **真实 go_hive**：`harness/inbox-smoke`（module replace → `../go-hive`）用 `sentinel.HTTPNotifier` 上报 escalation、经 `/internal/inbox/reply` 注入答复、`sentinel.HiveInboxListener` 收 `event: reply` 并 `OnInbound`；含 streamToken 401 与桌面 SSE 404 负向检查 | `evidence/inbox-smoke.json` |
| L3 | 真实云双跑 diff（阻塞：无登录态） | 待凭据 |

## 8. 安全

- 密钥仅环境/0600 文件；JWT/streamToken 密钥轮换双钥窗口（M3+）。
- 请求体上限（global-db 8MB、inbox 1MB）；`/sql` 结果行数上限；审计：所有写操作记录 `_cloud_audit`。
- 可选 inbox 写限流：per-team 固定窗口（默认关闭），超限 429 + `Retry-After`；SSE/读操作不计。
- 团队隔离默认拒绝跨 team 访问（JWT team_id → schema/file 选择）。

## 9. 里程碑与主仓关系

见 `README.md` §4/§6。M1 交付即包含：`serve` 骨架、SQLite 存储 + 方言翻译、全部 3.1/3.2/3.4 端点、L1 测试、L2a 冒烟；M2 交付 Inbox 契约套件 + L2b 真客户端闭环 + 审计/限流；CRM/LLM/凭据/部署件后续里程碑。
