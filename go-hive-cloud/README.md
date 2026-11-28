# go-hive-cloud —— 私有化 Hive 云替代（逆向工程 + 自建）

> 状态：**M5 + M7（LLM 代理真实透传 + 单机部署件）**，2026-11-23。M1 已落地：`serve`/`token`、自签
> HS256 JWT + streamToken、`/v1/global-db/*` 8 端点、`/v1/inbox/{messages,resolve,runtime/stream}`（SSE
> `event: reply`）、`/v1/gateway/availability`、`/user/refresh-stream-token-from-stream`、内部扩展
> `POST /internal/inbox/reply`、每 team SQLite 存储 + 语句门禁 + SQLite 方言翻译层、L1 契约测试、L2a 真客户端冒烟。
> M2 新增：Inbox 字段/错误码/auth/SSE 帧契约套件、真实 `go_hive` Sentinel escalation→回复注入→SSE 回注闭环
> （L2b）、`_cloud_audit` 审计验证、可选 per-team 限流；桌面 Inbox UI 端点继续登记 `unknown` 且不实现。
> M3 新增：streamToken 双钥轮换窗口（`--stream-secret-previous`）、旧 token/失效边界、真实 Python 运行时
> 刷新闭环（`post_refresh`/`_refresh_loop`/`try_refresh_now`）、`POST /v1/messages` 的 `hive` 凭据校验面、
> 真实 `go_hive` keycheck L2。
> M4 新增：CRM grid 的 **7 个证据充分端点**（query/group-counts/get/create/patch/delete/stage）落地为
> 每 team SQLite 上的通用行页 + 参数化 `grid_query` 过滤/搜索/排序/分页层；schema/claim/release 因响应
> 键与租约语义不可恢复继续 `planned` 且不注册（`docs/m4-notes.md`，26/26 冒烟）。
> M5 新增：`POST /v1/messages` 真实 Anthropic Messages 透传（非流式 + SSE 逐事件 flush、上游 4xx/5xx
> 原样、超时/取消、显式 base URL/凭据/鉴权头）、`POST /v1/images/generations` 客户端 JSON 契约透传、
> 真实 `go_hive` Anthropic 客户端 L2；模型路由/计费/限流/真实图片后端仍 `UNKNOWN` 且不实现。
> M7 新增：单机部署件（systemd/launchd、docker compose、备份/恢复/校验/升级脚本、部署 README），
> 并用临时数据目录完成备份→删除→恢复→健康检查与升级/回滚演练
> （`evidence/m7-deploy-drill.json`，19/19；边界见 `docs/m7-notes.md`）。
> 实现口径与已知近似：`docs/m1-notes.md` + `docs/m2-notes.md` + `docs/m3-notes.md` + `docs/m4-notes.md` + `docs/m5-notes.md` + `docs/m7-notes.md`；
> 冒烟证据：`evidence/globaldb-smoke.json`、`evidence/inbox-smoke.json`、`evidence/m3-smoke.json`、
> `evidence/m4-smoke.json`、`evidence/m5-smoke.json`、`evidence/m7-deploy-drill.json`。
> 目标：用自建服务**替换 Hive Cloud（hive-backend + llm 代理 + 凭据代理）**，支持私有化部署，
> 让 go_hive / 桌面客户端在 `HIVE_CLOUD_BASE` 指过来后，**客户端可观察契约**（API 路径/方法/字段/状态码/SSE 事件/错误文案）完全一致。
> 证据基础：`go_hive/docs/cloud-dependencies.md`（依赖总账）+ 本目录 `docs/reverse-engineering.md`。
> **API/SSE/WS 不变量**：全量端点（含 M2–M7、senders 的 inferred/unknown）登记在 `contracts/cloud-surface.json`，
> 由 `scripts/gen_cloud_surface.py --check` 强制「路由集合 = implemented 集合 / observed 全覆盖 / SSE 事件一致 / WS 显式」；
> 维护说明见 `docs/cloud-surface.md`，可读页 `docs/cloud-surface.html`。

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
| **L2 真实客户端接入** | `HIVE_CLOUD_BASE` 指向自建：go_hive 的 global-db tracker、inbox escalation、streamToken 刷新、Anthropic LLM 客户端真实跑通（落盘证据） | M2/M3/M5 |
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
| M2 | Inbox L2（go_hive sentinel 真实 escalation→自建→回复回注闭环）+ 审计/限流 | 6h **✅ 已落地（docs/m2-notes.md、evidence/inbox-smoke.json；7/7 真客户端检查）** |
| M3 | streamToken 轮换 L2 + `hive` LLM 凭据兼容（仅 token 校验面） | 4h **✅ 已落地（docs/m3-notes.md、evidence/m3-smoke.json；25/25 真客户端检查）** |
| M4 | CRM grid（`/v1/crm/*` 10 端点 + `grid_query` SQL 兼容层） | 8h **部分落地：7/10 通用 grid 端点 + 参数化 grid_query 兼容层（docs/m4-notes.md、evidence/m4-smoke.json 26/26）；schema/claim/release 响应形状 UNKNOWN，保持 planned** |
| M4.5 | senders cloud 兼容（`GET /v1/senders/runtime` + send-log/setup 客户端面的真实端点；先按 `contracts/cloud-surface.json` 的 inferred/unknown 清单补证据） | +6–8h |
| M5 | LLM 代理（Anthropic Messages 透传 + images generations + 路由配置） | 10h **✅ Messages/Images 已落地（docs/m5-notes.md、evidence/m5-smoke.json；11 项真客户端/E2E；模型路由仍 UNKNOWN、未实现）** |
| M6 | 凭据代理（api.adenhq.com `/v1/credentials*` 兼容） | 6h |
| M7 | 部署件（systemd/launchd、docker compose、备份/迁移、升级演练） | 6h **✅ 已落地（deploy/README.md、docs/m7-notes.md、evidence/m7-deploy-drill.json；19/19 检查）** |

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
  contracts/observed-contract.json     # 逆向观察契约（证据快照）
  contracts/cloud-surface.json         # 全量 API/SSE/WS surface（canonical，门禁解析它）
  docs/cloud-surface.md                # surface 维护规则与未知项
  docs/cloud-surface.html              # 上述 JSON 的渲染页（生成物）
  scripts/gen_cloud_surface.py         # 生成/校验 surface（--check）
  scripts/check_cloud_surface.sh       # 门禁包装
  docs/reverse-engineering.md   # 逆向证据清单（端点/字段/错误/未知项）
  docs/private-deploy-architecture.md  # 自建服务设计（存储/认证/SSE/兼容策略）
  docs/m1-notes.md              # M1 实现规则、方言近似、契约歧义、未知项登记
  docs/m2-notes.md              # M2 Inbox 契约硬化、L2b 闭环、审计/限流、未知项
  docs/m3-notes.md              # M3 streamToken 轮换、LLM 凭据边界、未知项
  docs/m4-notes.md              # M4 CRM 契约恢复表、7 端点实现、grid_query 兼容层、未知项
  docs/m5-notes.md              # M5 Messages/Images 透传、配置、错误面、未知项
  docs/m7-notes.md              # M7 部署件、演练、BLOCKED 与剩余风险
  deploy/                       # M7 systemd/launchd、docker compose、备份/恢复/升级脚本与部署 README
  deploy/scripts/deploy-drill.sh # M7 备份→删除→恢复→健康 + 升级/回滚演练
  harness/globaldb-smoke/       # L2a：真 go_hive globaldb 客户端（独立 go.mod + replace）
  harness/inbox-smoke/          # L2b：真 go_hive sentinel 客户端（escalation + SSE 回复闭环）
  harness/m4-smoke/             # L2 M4：真 go_hive globaldb 客户端（grid_query SQL）+ CRM 原始 HTTP
  harness/llm-proxy-smoke/      # L2 M5：真 go_hive Anthropic 客户端 + 真 hive-cloud + fake upstream
  scripts/smoke.sh              # 起服务 → L2a + L2b + M3 + M5 + M4 harness
  scripts/inbox-smoke.sh        # 仅 L2b inbox 闭环
  scripts/m3-smoke.sh           # 仅 M3 streamToken/LLM 凭据
  scripts/m4-smoke.sh           # 仅 M4 grid_query SQL + CRM grid 子集
  scripts/m5-smoke.sh           # 仅 M5 Messages/Images 透传
  evidence/globaldb-smoke.json  # L2a 证据（脚本生成）
  evidence/inbox-smoke.json     # L2b 证据（脚本生成）
  evidence/m3-smoke.json        # M3 证据（脚本生成）
  evidence/m4-smoke.json        # M4 证据（脚本生成）
  evidence/m5-smoke.json        # M5 证据（脚本生成）
  evidence/m7-deploy-drill.json # M7 演练证据（脚本生成，无 token/secret）
```

## 6. 与主仓关系

- 独立 module（`github.com/shark2202/go_hive/go-hive-cloud`），不进入 go_hive `make ci` 的默认门禁（后续可加自己的 CI）。
- 复用已冻结的契约认知，不改动 `go_hive/contracts/*`；主仓行为零变化。
- 私有部署接入点：`HIVE_CLOUD_BASE`（及 `HIVE_CLOUD_JWT` 由自建签发），桌面端需可配置云基址（未知项，见逆向文档 §7）。

## 7. 运行与验证（M5）

```bash
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh   # go1.26.11 + 预填模块缓存
cd go_hive/go-hive-cloud

# 签发 operator JWT / streamToken（密钥缺省自动落在 <data_dir>/*_secret，0600）
go run ./cmd/hive-cloud token --data-dir ./data --user-id u1 --team-id team1 --json
go run ./cmd/hive-cloud token --data-dir ./data --user-id u1 --team-id team1 --type stream

# 起服务（--listen 127.0.0.1:0 选随机端口并打印实际地址）
# LLM 透传需要显式上游；不配置时 /v1/messages 返回 400 llm_upstream_not_configured（绝不空 200）
go run ./cmd/hive-cloud serve --data-dir ./data --listen 127.0.0.1:8788 \
  --llm-upstream-url https://api.anthropic.com \
  --llm-upstream-key "$ANTHROPIC_API_KEY" \
  --llm-upstream-auth x-api-key

# 门禁
gofmt -l .
go vet ./...
go test ./...            # L1 契约/golden/401/门禁/SSE/streamToken/inbox/LLM 透传契约
python3 scripts/gen_cloud_surface.py --check   # 或 ./scripts/check_cloud_surface.sh
./scripts/m5-smoke.sh    # L2 M5：真 go_hive Anthropic 客户端 + 真二进制 + fake upstream
./scripts/m4-smoke.sh    # L2 M4：真 go_hive globaldb 客户端（grid_query SQL）+ CRM grid 子集
./scripts/smoke.sh       # M1 + M2 + M3 + M5 + M4 → evidence/*.json
./scripts/inbox-smoke.sh # 仅 L2b（真实 sentinel 闭环）
./deploy/scripts/lint-deploy.sh   # M7 部署静态门禁（脚本/unit/plist/compose）
./deploy/scripts/deploy-drill.sh  # M7 备份→删除→恢复→健康 + 升级/回滚演练
```

harness 单独构建：`cd harness/globaldb-smoke && go vet ./... && go build ./...`、
`cd harness/inbox-smoke && go vet ./... && go build ./...`、
`cd harness/llm-proxy-smoke && go vet ./... && go build ./...`。
M2 限流默认关闭；需要时 `--inbox-rate-limit 60 --inbox-rate-window 1m`（或
`HIVE_CLOUD_INBOX_RATE_LIMIT`/`HIVE_CLOUD_INBOX_RATE_WINDOW_MS`）。
M5 images 透传另需 `--images-upstream-url`/`--images-upstream-key`；未配置时返回 503。

客户端接入：`HIVE_CLOUD_BASE=http://127.0.0.1:8788`、`HIVE_CLOUD_JWT=<token 输出>`；
LLM 调用方用 `HIVE_LLM_BASE_URL` 指向同一 base。
`docs/m1-notes.md` / `docs/m2-notes.md` / `docs/m3-notes.md` / `docs/m5-notes.md` 记录了 401 文案与契约
JSON 的歧义、SQLite 方言近似、Inbox 未知项、streamToken 轮换边界与 LLM 上游透传/未知项。
