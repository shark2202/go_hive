# go-hive-cloud —— 私有化 Hive 云替代（逆向工程 + 自建）

> 状态：**M1（骨架服务）**，2026-11-22。已落地：`serve`/`token`、自签 HS256 JWT + streamToken、
> `/v1/global-db/*` 8 端点、`/v1/inbox/{messages,resolve,runtime/stream}`（SSE `event: reply`）、
> `/v1/gateway/availability`、`/user/refresh-stream-token-from-stream`、内部扩展 `POST /internal/inbox/reply`、
> 每 team SQLite 存储 + 语句门禁 + SQLite 方言翻译层、L1 契约测试、L2a 真客户端冒烟。
> 实现口径与已知近似：`docs/m1-notes.md`；冒烟证据：`evidence/globaldb-smoke.json`。
> 目标：用自建服务**替换 Hive Cloud（hive-backend + llm 代理 + 凭据代理）**，支持私有化部署，
> 让 go_hive / 桌面客户端在 `HIVE_CLOUD_BASE` 指过来后，**客户端可观察契约**（API 路径/方法/字段/状态码/SSE 事件/错误文案）完全一致。
> 证据基础：`go_hive/docs/cloud-dependencies.md`（依赖总账）+ 本目录 `docs/reverse-engineering.md`。

## 1. 可行性判断（诚实口径）

| 层 | 能恢复吗 | 依据 |
| --- | --- | --- |
| **客户端可观察契约**（端点、方法、认证头、请求/响应字段、SSE 事件名与载荷、状态码、错误文案、数据模型线索） | ✅ 可以 | 客户端源码即规格：`internal/globaldb/client.go`、`internal/sentinel/{notifier,listeners}.go`、`core/framework/global_db/*`、`core/framework/server/streamtoken_refresh.py`、`docs/hive-inbox.html`、`contracts/api-contract-diff.md` |
| **服务端内部语义**（授权规则、配额/计费、team 成员模型、迁移/审计、限流、JWT 签名算法与密钥、登录/设备流、streamToken 签发细节、cloud_sync 对账协议、Inbox UI） | ❌ 不能保证 | 后端闭源且不在仓库；无真实云登录态可对照（G3 外部验证被凭据阻塞） |
| **LLM 代理内部**（模型路由、计费、token 校验、图片生成后端） | ❌ 不能保证 | Rust 服务在 hive-backend/llm/，仅有客户端调用面（Anthropic Messages + `/v1/images/generations`） |

**结论**：可以做「**客户端契约兼容的私有化替代**」；"与真实云完全一致"只能在拿到真实云登录态后经 L3 双跑证明——当前不具备该条件，**不做假绿声明**。

## 2. 验收分层

| 层 | 内容 | 状态 |
| --- | --- | --- |
| **L1 契约回放** | 用仓库内夹具/客户端代码驱动自建服务，逐字段断言（golden JSON）；错误文案逐字 | M1 起建立 |
| **L2 真实客户端接入** | `HIVE_CLOUD_BASE` 指向自建：go_hive 的 global-db tracker、inbox escalation、streamToken 刷新真实跑通（落盘证据） | M2/M3 |
| **L3 真实云双跑** | 同一请求打真实云与自建，diff 状态码/字段（需真实登录态，当前**阻塞**） | 待凭据 |

## 3. 非目标（当前阶段）

- 不提供闭源 **Inbox UI**（桌面/网页）——私有部署需要自有前端或复用开源前端；协议层兼容即可。
- 不做计费/配额/对抗滥用；私有部署按信任域处理（仍提供审计与限额开关）。
- 不改 go_hive 主模块行为、不向其 go.mod 增加依赖；本目录是**独立 Go module**。

## 4. 里程碑（估时）

| 阶段 | 内容 | 估时 |
| --- | --- | --- |
| **M0** | 逆向规格 + 契约 JSON（本目录） | 2h（已完成初版） |
| **M1** | 骨架服务：JWT 自签 + streamToken、`/v1/global-db/*`、`/v1/inbox/{messages,resolve,runtime/stream}`、`/user/refresh-stream-token-from-stream`、`/v1/gateway/availability`；SQLite（纯 Go）；L1 契约测试 + globaldb 客户端 L2 冒烟 | 10–14h **✅ 已落地（docs/m1-notes.md、evidence/globaldb-smoke.json）** |
| M2 | Inbox L2（go_hive sentinel 真实 escalation→自建→回复回注闭环）+ 审计/限流 | 6h |
| M3 | streamToken 轮换 L2 + `hive` LLM 凭据兼容（仅 token 校验面） | 4h |
| M4 | CRM grid（`/v1/crm/*` 10 端点 + `grid_query` SQL 兼容层） | 8h |
| M5 | LLM 代理（Anthropic Messages 透传 + images generations + 路由配置） | 10h |
| M6 | 凭据代理（api.adenhq.com `/v1/credentials*` 兼容） | 6h |
| M7 | 部署件（systemd/launchd、docker compose、备份/迁移、升级演练） | 6h |

## 5. 目录

```
go_hive/go-hive-cloud/
  README.md                     # 本文件
  go.mod                        # module github.com/shark2202/go_hive/go-hive-cloud（go 1.26.11，无 cgo）
  cmd/hive-cloud/main.go        # serve / token 子命令
  internal/auth/                # HS256 JWT + streamToken（stdlib）
  internal/config/              # 配置与密钥文件（0600）
  internal/store/               # 每 team SQLite、语句门禁、方言翻译、变更/审计、收件箱表
  internal/httpapi/             # 路由、中间件、global-db / inbox / gateway / stream-token 处理器
  internal/httpapi/testdata/    # L1 golden
  contracts/observed-contract.json     # 机器可读契约（契约覆盖测试解析它）
  docs/reverse-engineering.md   # 逆向证据清单（端点/字段/错误/未知项）
  docs/private-deploy-architecture.md  # 自建服务设计（存储/认证/SSE/兼容策略）
  docs/m1-notes.md              # M1 实现规则、方言近似、契约歧义、未知项登记
  harness/globaldb-smoke/       # L2a：真 go_hive globaldb 客户端（独立 go.mod + replace）
  scripts/smoke.sh              # 起服务 → 签 token → 跑 harness → 断言
  evidence/globaldb-smoke.json  # L2a 证据（脚本生成）
```

## 6. 与主仓关系

- 独立 module（`github.com/shark2202/go_hive/go-hive-cloud`），不进入 go_hive `make ci` 的默认门禁（后续可加自己的 CI）。
- 复用已冻结的契约认知，不改动 `go_hive/contracts/*`；主仓行为零变化。
- 私有部署接入点：`HIVE_CLOUD_BASE`（及 `HIVE_CLOUD_JWT` 由自建签发），桌面端需可配置云基址（未知项，见逆向文档 §7）。

## 7. 运行与验证（M1）

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh   # go1.26.11 + 预填模块缓存
cd go_hive/go-hive-cloud

# 签发 operator JWT / streamToken（密钥缺省自动落在 <data_dir>/*_secret，0600）
go run ./cmd/hive-cloud token --data-dir ./data --user-id u1 --team-id team1 --json
go run ./cmd/hive-cloud token --data-dir ./data --user-id u1 --team-id team1 --type stream

# 起服务（--listen 127.0.0.1:0 选随机端口并打印实际地址）
go run ./cmd/hive-cloud serve --data-dir ./data --listen 127.0.0.1:8788

# 门禁
gofmt -l .
go vet ./...
go test ./...            # L1 契约/golden/401/门禁/SSE/streamToken/契约覆盖
./scripts/smoke.sh       # L2a 真客户端 → evidence/globaldb-smoke.json
```

客户端接入：`HIVE_CLOUD_BASE=http://127.0.0.1:8788`、`HIVE_CLOUD_JWT=<token 输出>`。
`docs/m1-notes.md` 记录了 401 文案与契约 JSON 的歧义、SQLite 方言近似、以及 M2+ 的未知项。
