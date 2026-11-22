# credentials check-agent / resync — 移植与真实验证证据

> lane `cred-check`（worktree `.wt/cred-check`，分支 `cred-check`，基线 `528bdcd6`）。
> 目标：把 `/api/credentials/check-agent`（Go 侧 500 占位）与 `/api/credentials/resync`（`deps.Resync` 生产未赋值）按上游 `core/` 语义实装，并以前端真实调用路径验证。
> 上游参考：`core/framework/server/routes_credentials.py::handle_check_agent / handle_resync_credentials`、`core/framework/credentials/validation.py`、`core/framework/credentials/setup.py::load_agent_nodes`、`tools/src/aden_tools/credentials/health_check.py`。

## 1. 端点字段对齐表

### 1.1 `POST /api/credentials/check-agent`

入参（JSON body）：

| 字段 | 类型 | 默认 | 语义（上游一致） |
| --- | --- | --- | --- |
| `agent_path` | string | — | 必填；`validate_agent_path` 约束在 `$HIVE_HOME/colonies/`、`exports/`、`examples/`、`$HIVE_HOME/agents/` 之下（不含根本身） |
| `verify` | any（Python truthiness） | `true` | 缺省为真；`false`/`null`/`0`/`""`/`[]`/`{}` 关闭第二阶段健康检查；字符串 `"false"` 仍为真（与上游 `bool()` 一致） |

状态码 / 错误文案：

| 情形 | 状态 | Body |
| --- | --- | --- |
| 缺少/空 `agent_path` | 400 | `{"error":"agent_path is required"}` |
| 路径越界 | 400 | `{"error":"agent_path must be inside an allowed directory ($HIVE_HOME/colonies/, exports/, examples/, or $HIVE_HOME/agents/)"}` |
| 非字符串 `agent_path` | 500 | `{"error":"Internal server error"}`（上游 `Path(...)` TypeError 未捕获） |
| body 非法 JSON | 500 | `{"error":"Internal server error"}`（上游 `await request.json()` ValueError 未捕获） |
| 未知但合法目录 | 200 | `{"required":[],"has_aden_key":<bool>}`（`load_agent_nodes` 返回空） |
| 正常 | 200 | `{"required":[...],"has_aden_key":<bool>}` |

响应 envelope：`required`（数组）、`has_aden_key`（bool）。行字段（与 `_status_to_dict` 逐字段一致；JSON 键序也按上游字典序）：

| 字段 | 类型 | 语义 |
| --- | --- | --- |
| `credential_name` | string | `CREDENTIAL_SPECS` 字典键（如 `github`） |
| `credential_id` | string | `spec.credential_id or name`（如 `github`） |
| `env_var` | string | 如 `GITHUB_TOKEN` |
| `description` | string | spec 描述 |
| `help_url` | string | 取 key 的位置 |
| `tools` | string[] | 该凭据影响的、当前 agent 实际声明了的工具（排序） |
| `node_types` | string[] | 同上（当前生成表无 node_types，恒空数组） |
| `available` | bool | 是否可取（env 优先，其次加密 store） |
| `direct_api_key_supported` | bool | spec 字段 |
| `aden_supported` | bool | spec 字段 |
| `credential_key` | string | spec 字段（缺省 `access_token`） |
| `valid` | bool \| null | null=未检查（不可用 / verify=false / 无健康检查端点 / 值为空）；健康检查后为 bool |
| `validation_message` | string \| null | 健康检查返回的 message |
| `alternative_group` | string \| null | 多提供方工具（如 `send_email` → google OR resend）未满足时=工具名；满足的行不带组 |

Aden 平台行（当任一返回凭据 `aden_supported=true` 时**前置**，字段序按上游字面量，且**无** `alternative_group` 键）：
`credential_name="Aden Platform"`, `credential_id="aden_api_key"`, `env_var="ADEN_API_KEY"`, `description="API key from the Developers tab in Settings"`, `help_url="https://hive.adenhq.com/"`, `tools=[]`, `node_types=[]`, `available=<has_aden_key>`, `valid=null`, `validation_message=null`, `direct_api_key_supported=true`, `aden_supported=true`, `credential_key="api_key"`。

检查逻辑（`validate_agent_credentials` 全量移植）：

1. **节点来源**：`<dir>/agent.json`（声明式；按 Pydantic `AgentConfig` 的必填/类型严格解析，节点 `tools` 支持 `{policy,allowed}` 与 `{policy,tools}` 别名；解析失败=0 节点）→ 否则 `<dir>/agent.py`（Go 无 Python 解释器，按 0 节点处理，已在代码注释声明）→ 否则 Go 扩展：该目录的 worker 配置（`agents.LoadAgentSpec`，与 colony 宿主同一读取规则）的 `tools`。
2. **required_tools/node_types** = 所有节点 `tools`/`node_type` 并集；按工具名排序遍历。
3. **单提供方**：`required=false` 的 spec 标记已查但不输出（如 `finlight`）；正常输出一行。
4. **多提供方**：任一 available 即视为满足，只输出命中的那一行；全缺则每行都带 `alternative_group=<tool>`（与上游一致，包括 jira/x 这类多字段凭据的 any-of 语义）。
5. **node_types** 分支按上游保留（当前生成表 `NodeTypes` 恒空，不会命中）。
6. **可用性 store**：`EnvVarStorage(cred_id→env_var)` 优先；存在 `HIVE_CREDENTIAL_KEY` 时叠加 `EncryptedFileStorage` 回退（每次请求新建，等价上游 per-request store）。
7. **第二阶段健康检查**：仅 available + verify + `health_check_endpoint` 非空；`store.get(cred_id)` 为空则跳过；结果写 `valid`/`validation_message`；`details.identity` 非空时 best-effort 写回 `_identity_*`。
8. **健康检查器**：`health_check.py` 的 42 个注册 checker 全量移植（HubSpot/Zoho/Brave/Google/GoogleSearch/Slack/Calendly/GitHub/Discord/Resend/GoogleMaps/Lusha/Prometheus/Telegram/Intercom + 28 个 BaseHttp 配置类），无专用 checker 时走 `OAuthBearerHealthChecker` 通用回退（`credential_name.replace("_"," ").title()` 服务名）；无 endpoint 时 `"No health checker for '<name>', assuming valid"`。

### 1.2 `POST /api/credentials/resync`

| 情形 | 状态 | Body |
| --- | --- | --- |
| 无 `ADEN_API_KEY`（env/store/shell 均无） | 400 | `{"error":"Aden API key not configured","accounts_by_provider":{}}` |
| hook 报错 | 500 | `{"error":"Internal server error during resync"}` |
| 正常 | 200 | `{"synced":true,"accounts_by_provider":{...}}` |

生产接线：`RegisterCredentialRoutes` 在调用方提供 `Home` 且未注入 `Resync` 时安装本地默认 `resyncLocalCredentials`（`internal/server/routes_credentials.go`）：重读 `HIVE_CREDENTIAL_KEY` 与 `ADEN_API_KEY`（secrets 文件/shell 配置）→ 清 credential store 内存缓存 → 重跑 store→env 桥（`SyncEnvFromStore`）。上游此处额外 force-fetch Aden OAuth token；Aden 客户端属 P2 `credentials/aden/client.py`，本 lane 未移植（端点仍可用、返回快照）。

## 2. 真实验证（可复算）

### 2.1 服务与 curl（`run.sh` + `*.body.json` + `curl-cases.txt`）

`hive serve --port 8798`（8788 被其它 lane 的 `hive-embe` 占用；HIVE_HOME=临时 home，colony `cred_demo/worker.json` 声明 `["github_create_issue"]`，`GITHUB_TOKEN=ghp_fake_e2e_token`）：

| case | 请求 | 实测 |
| --- | --- | --- |
| `modal_default` | `{"agent_path": <colony>}`（前端 modal 原样） | 200；Aden 行 + github 行；`valid=false`，`validation_message="GitHub token is invalid or expired"`（真实访问 `api.github.com/user`，假 token 401） |
| `modal_verify_false` | `{"agent_path": <colony>, "verify": false}` | 200；`valid=null`，其余字段同上 |
| `unknown_agent` | `{"agent_path": <home>/colonies/ghost, "verify": false}` | 200 `{"has_aden_key":false,"required":[]}` |
| `missing_agent_path` | `{}` | 400 `agent_path is required` |
| `traversal_rejected` | `exports/../../etc/passwd` | 400 `agent_path must be inside an allowed directory …` |
| `malformed_body` | `not json` | 500 `Internal server error` |
| `resync_without_aden` | `POST /api/credentials/resync {}` | 400 `Aden API key not configured` |
| `save_aden_key` | `POST /api/credentials {"credential_id":"aden_api_key",…}` | 201 `{"saved":"aden_api_key"}` |
| `resync_with_aden` | `POST /api/credentials/resync {}` | 200 `{"synced":true,"accounts_by_provider":{}}` |

### 2.2 前端真实调用路径（`browser.json` + `dashboard-8798.png` + `serve.log`）

- 浏览器打开 `http://127.0.0.1:8798/colony/cred_demo`（官方构建 dashboard，`<title>🐴 Hive</title>`，页面渲染 colony 列表与 Cred Demo）。
- 被服务的前端 bundle `assets/index-Z2alDRTm.js` 含 `/credentials/check-agent` 与 `/credentials/resync` 调用（即 `credentialsApi.checkAgent/resync`）。
- 从该页面发出 modal 的原样请求（`POST /api/credentials/check-agent`, body `{agent_path}`）：**200**，返回 Aden + github 两行；`performance` 记录 `fetch → /api/credentials/check-agent`（537.8ms）；服务端访问日志 `POST /api/credentials/check-agent -> 200 (535ms)`；页面无 JS error。
- 同页 `POST /api/credentials/resync`：**200** `{"synced":true,"accounts_by_provider":{}}`。
- 交叉核对 `GET /api/credentials/specs`：Aden 行在首位、`github.available=true`、`env_var=GITHUB_TOKEN`。

边界（登记，不阻塞）：dashboard 里 CredentialsModal 的**自动弹出**依赖 424 `credentials_required`（session create/load）或同名 SSE 事件；Go 侧已定义该错误类型与 424 映射，但当前没有生产路径抛出它（本轮未改 session 创建语义）。本证据验证的是该弹窗自身发出的请求（路径/体/字段消费完全一致）。

### 2.3 Go 门禁（`go-verify.log`、`merge-postcheck.log`）

```
go build ./...                                  exit=0
go test ./internal/credentials/ ./internal/server/ -count=1   ok / ok
bash go_hive/scripts/merge_postcheck.sh         gofmt clean; wiring 20 files/67 reqs OK;
                                                C12 verdict=PASS (273=216/28/9/20);
                                                arch 7 生成物一致
```

测试清单（新增/更新）：

- `internal/credentials/load_agent_nodes_test.go`：声明式解析（节点级/继承/`none`/别名/默认 node_type）、非法文档 7 例、`agent.py` 分支、空目录、`PyTitle`。
- `internal/credentials/validate_agent_credentials_test.go`：presence/顺序、`required=false` 跳过、多提供方 alternative group（全缺/命中）、健康检查 + identity 回写、空值/verify=false 跳过、`compute_unavailable_tools`、Aden-not-connected + 文案。
- `internal/credentials/health_check_test.go`：回退分发、OAuthBearer 状态映射、BaseHttp 状态/429/identity、五种 auth 形状 + POST body、timeout 文案、42 项注册表。
- `internal/server/routes_credentials_check_test.go`：worker 配置行（有/无 key）、声明式 agent.json、Aden 行前置（无 `alternative_group` 键）、未知 agent 空表、非字符串路径 500。
- `internal/server/routes_credentials_resync_default_test.go`：生产默认 hook（200）、`resyncLocalCredentials` 重读持久化密钥。

## 3. 复算

```bash
# 1) Go 侧
source /Users/mac/.hermes/profiles/omh-tui/cache/scratch/goenv.sh   # go1.26.11 + GOMODCACHE
cd go_hive/go-hive && go build ./... && go test ./internal/credentials/ ./internal/server/ -count=1
cd .. && bash go_hive/scripts/merge_postcheck.sh

# 2) 端点（需要一次 serve；HIVE_HOME 下准备 colonies/cred_demo/worker.json + metadata.json）
HIVE_HOME=… GITHUB_TOKEN=ghp_fake_e2e_token ./dist/hive serve --port 8798
HIVE_HOME_E2E=… bash go_hive/docs/e2e-assets/credentials-check-agent/run.sh 8798

# 3) 前端
#    在 go_hive/go-hive 放好前端 dist（gitignored 构建产物）后打开 http://127.0.0.1:8798/colony/cred_demo，
#    在页面控制台执行与 browser.json 相同的 fetch，或按该文件记录的请求复跑。
```

## 4. 已知边界（登记）

- `credentials/aden/client.py`（Aden 云端 token 拉取/刷新）仍为 P2：resync 走本地默认，不拉云端 token。
- `validation.py::build_setup_session_from_error` / `_status_to_missing` 未移植（依赖交互式 setup session）；`compute_unavailable_tools` 已移植并有测试，但 Go spawn preflight 尚未接线。
- `agent.py` 分支在 Go 侧按 0 节点处理（无 Python 解释器）。
- 多提供方分组顺序沿用生成表（按 credential id 排序）；上游为 `CREDENTIAL_SPECS` 插入序。仅影响“多个都可用时显示哪一个/同组显示顺序”，不影响可用性判定。
- 无持久化 `HIVE_CREDENTIAL_KEY` 的 home 上，resync/specs 走 `WithEncryptedStorage` 时会打 `Generated new encryption key` 警告（既有行为，非本 lane 引入）。
