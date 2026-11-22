# unported 逐条分诊（51 条 → 21 条）

> 范围：`contracts/module-inventory.json`（C12）中 `core/framework` 的 51 条 `unported`（基线 c80dd898）。
> 方法：对每条做 ① Python 顶层符号（ast）↔ Go 树标识符（归一化）对撞；② 同 basename / `// Parity:` 注解 / 调用点 grep；③ 阅读两侧实现确认语义与接线。命令与产物可复现（见文末）。
> 修正只落在 `scripts/gen_module_inventory.py` 的 `CURATED`（不手改 registry），重生后 C12 PASS。

## 结论统计

| 结论 | 数量 | 说明 |
| --- | ---: | --- |
| ported（假 unported） | 11 | Go 已有逐函数对应且被生产代码调用；扫描的「域映射 + 同 basename + Parity 注解」三条路全部漏检（跨名移植） |
| partial | 17 | Go 有对应实现/导出面但不完整、未接线或仅是 Python 包 re-export 面（surface） |
| out_of_scope | 2 | 只服务图执行引擎（Go 不实现的宿主） |
| 真未移植 | 21 | Go 侧确实没有对应实现；按用户可见性排序，前 5 登 delivery-backlog |
| **合计** | **51** | 重生后登记册：ported 216 / partial 27 / out_of_scope 9 / unported 21（273 条） |

误标修正 30 条：11 ported + 17 partial + 2 out_of_scope。`--check` 无 `[WARN]`（无「同名 Go 文件未达 ported」残留）。

## 全表（51 条）

| 路径 | 现标记 | 核验结论 | 证据 | 行动 |
| --- | --- | --- | --- | --- |
| `core/framework/__init__.py` | unported | partial（surface） | 顶层惰性 re-export `_EXPORTS`（:31）；Go 包导出面顶替：agentloop/loop.go、colony/runtime.go、agents/agent_spec.go；DecisionTracker 无 Go 实现 | CURATED → partial/surface |
| `core/framework/__main__.py` | unported | **ported** | `cmd/hive/main.go:10`（Parity: framework/cli.py::main；与 console script `hive=framework.cli:main` 同入口） | CURATED → ported |
| `core/framework/agent_loop/__init__.py` | unported | partial（surface） | 类型 re-export（:8）+ `__getattr__`（:17）；`internal/agentloop/types.go`+`loop.go` 导出面 | CURATED → partial/surface |
| `core/framework/agent_loop/internals/sentinel_tool.py` | unported | 真未移植 | `internal/cli/tool_surface_flat.go:174` 明示「not implemented as a model tool (desktop connector)」；:536 返回 not_implemented 文案 | 保留 unported；P2 |
| `core/framework/agent_loop/internals/stall_detector.py` | unported | partial | `internal/agentloop/doom_loop.go:48` ToolCallFingerprint / `:61` DoomLoopDetector（Parity-exempt 替代规则）；n-gram 失速（`ngram_similarity`/`is_stalled`）无 Go 对应 | CURATED → partial/call_chain |
| `core/framework/agent_loop/internals/tool_input_coercer.py` | unported | partial | `internal/tools/coerce.go:12` CoerceToolInput 全量移植，但全库仅 `coerce_test.go` 调用（Python 侧 `agent_loop.py:4805` 派发前调用）——未接线 | CURATED → partial/call_chain；P1 backlog（接线） |
| `core/framework/agents/__init__.py` | unported | partial（surface） | `list_framework_agents`（:8）无运行时调用方；Go 框架 agent 面内嵌在 `internal/queen` + colony worker 定义 | CURATED → partial/surface |
| `core/framework/agents/queen/__init__.py` | unported | partial（surface） | re-export .agent/.config（:3）；Go `internal/queen/identity.go` + colony/worker_definition.go | CURATED → partial/surface |
| `core/framework/agents/queen/agent.py` | unported | partial | queen loop profile（budget 30/ctx 180k）已进 `internal/agentloop/loop_config.go:123` DefaultLoopConfig；colony 相位变体（50×10，`queen_orchestrator.py:1509`）未移植；queen_goal 无 Goal 对象 | CURATED → partial/call_chain |
| `core/framework/agents/queen/config.py` | unported | partial（surface） | `_load_preferred_model` 回退已在 `internal/pipeline/stage_llm_provider.go:15`；RuntimeConfig/AgentMetadata 无运行时调用方（dead surface） | CURATED → partial/surface |
| `core/framework/agents/queen/queen_memory_v2.py` | unported | **ported** | `internal/memory/memory.go:145` ScanMemoryFiles / `:40` ParseFrontmatter / `:196` SlugifyMemoryName / `:204` AllocateMemoryFilename；caller `memory/recall.go:40` | CURATED → ported |
| `core/framework/agents/queen/tools_ga_migration.py` | unported | 真未移植 | `internal/server/routes_queens_tools.go:382` 自述「That versioned healing table has no Go port yet」；`_CATEGORY_ADDITIONS`/`grant_role_default_additions` 无 Go 等价 | 保留 unported；P2 backlog |
| `core/framework/cloud_sync_hooks.py` | unported | partial | `internal/server/routes_queens.go:52` SchedulePush 注记 mirrors schedule_push、`:83` nil 检查调用点；全库无生产代码给 SchedulePush 赋值（cloud sync reconciler 未移植） | CURATED → partial/call_chain |
| `core/framework/credentials/__init__.py` | unported | partial（surface） | 整包 re-export（:43 起）；Go `internal/credentials/` 包导出面；ADEN_AVAILABLE/LOCAL_AVAILABLE 旗标无等价 | CURATED → partial/surface |
| `core/framework/credentials/aden/client.py` | unported | 真未移植 | grep AdenCredentialClient / list_integrations / request_refresh 在 Go = 0；Aden 同步只消费 `ADEN_API_KEY`（keys.go:116），`/api/credentials/resync` 依赖的 deps.Resync 生产未赋值（routes_credentials.go:590） | 保留 unported；P2 |
| `core/framework/credentials/key_storage.py` | unported | **ported** | `internal/credentials/keys.go:27` LoadCredentialKey / `:62` persistKeyIfMissingOrStale / `:105` GenerateAndSaveCredentialKey / `:116` LoadAdenAPIKey；caller `server/routes_credentials.go:98` | CURATED → ported |
| `core/framework/credentials/oauth2/base_provider.py` | unported | 真未移植 | 无 BaseOAuth2Provider（授权 URL/换码/refresh/revoke 流程）；`internal/credentials/oauth2.go` 只有 token/config 类型；全库仅 `oauth2/__init__.py` re-export（OSS 扩展 API） | 保留 unported；P3 |
| `core/framework/credentials/oauth2/hubspot_provider.py` | unported | 真未移植 | 无 HubSpotOAuth2Provider；工具侧仅 token refresh（`internal/tools/integrations/oauth.go:89`），无 provider 类语义 | 保留 unported；P3 |
| `core/framework/credentials/oauth2/lifecycle.py` | unported | 真未移植 | 无 TokenLifecycleManager/TokenRefreshResult；Store 自带 auto-refresh 走 `store.go:448`，不提供该管理器 API | 保留 unported；P3 |
| `core/framework/credentials/oauth2/zoho_provider.py` | unported | 真未移植 | 无 ZohoOAuth2Provider；工具侧仅 `oauth.go:106` oauthZohoOAuth refresh 配置 | 保留 unported；P3 |
| `core/framework/credentials/setup.py` | unported | 真未移植 | CredentialSetupSession/MissingCredential/load_agent_nodes 无 Go 对应；`/api/credentials/check-agent` 是 500 占位（`routes_agents.go:254`）→ 前端 CredentialsModal 打开即报错 | 保留 unported；P1 backlog |
| `core/framework/credentials/validation.py` | unported | partial | `ensure_credential_key_env` → `internal/credentials/bootstrap.go:25`（仅自身测试调用）；validate_agent_credentials（:343）/compute_unavailable_tools（:304）/CredentialStatus 无 Go 对应 | CURATED → partial/call_chain；P1 backlog（与 setup.py 同项） |
| `core/framework/global_db/cli.py` | unported | 真未移植 | 无 `hive-global-db` CLI（cmd/ 只有 hive/hive-browser/hive-cred/hive-longmemeval；internal/cli 无 global-db 子命令）；`internal/globaldb` client 已被 server 路由使用 | 保留 unported；P3 |
| `core/framework/host/__init__.py` | unported | partial（surface） | `_EXPORTS`（:31）懒 re-export；Go colony/runtime.go + eventbus/bus.go + colony/worker.go 导出面 | CURATED → partial/surface |
| `core/framework/host/isolation.py` | unported | partial（surface） | `internal/schemas/agent_config.go:242` IsolationLevel string（默认 shared，无枚举校验）；三值/缓冲语义只在图宿主 | CURATED → partial/surface |
| `core/framework/host/shared_state.py` | unported | out_of_scope | 模块自述 stub（shared state removed）；唯一生产调用方 `host/execution_manager.py:966` cleanup_execution（图宿主） | CURATED → out_of_scope |
| `core/framework/host/stream_runtime.py` | unported | 真未移植 | 无 StreamDecisionTracker/StreamRuntimeAdapter；决策记录子系统（`schemas/decision.go`/`run.go` 数据模型已移植，但无 runtime 写入口） | 保留 unported；P2 |
| `core/framework/llm/__init__.py` | unported | partial（surface） | provider/stream 事件 re-export（:3）；`internal/llm/` 导出面 | CURATED → partial/surface |
| `core/framework/llm/antigravity.py` | unported | 真未移植 | Go 只有 token/config 骨架（`server/llm_config.go:282` antigravityToken）与显式错误（`server/main_slot.go:39` ErrAntigravityUnsupported）；无 provider/Gemini schema/SSE 解析 | 保留 unported；P2 backlog |
| `core/framework/llm/key_check.py` | unported | **ported** | `internal/llm/keycheck.go:491` CheckKey（checkAnthropic:292 / checkOpenRouter:339 / checkGemini:467 等逐函数）；caller `server/routes_credentials.go:628` | CURATED → ported |
| `core/framework/llm/key_pool.py` | unported | **ported** | `internal/llm/keypool.go:16` KeyPool / `:9` KeyHealth / `:24` NewKeyPool；caller `pipeline/stage_llm_provider.go:154`（另 stream_openai.go:64） | CURATED → ported |
| `core/framework/llm/model_catalog.py` | unported | **ported** | `internal/llm/catalog.go:495` LoadModelCatalog / `:18` ModelCatalogError；caller `server/app.go:455`；catalog_test.go 自带 test_model_catalog.py Parity | CURATED → ported |
| `core/framework/loader/mcp_errors.py` | unported | **ported** | `internal/mcpregistry/errors.go:6` 全部 7 个 MCP_* code / `:15` Error(Code/What/Why/Fix)；caller `mcpregistry/registry.go:484` installError | CURATED → ported |
| `core/framework/loader/preload_validation.py` | unported | out_of_scope | `agent_loader.py:1230` run_preload_validation → `preload_validation.py:38` GraphSpec.validate + GCU subagent-only 检查；Go loader 停在图之前（agent_spec.go:9） | CURATED → out_of_scope |
| `core/framework/orchestrator/__init__.py` | unported | partial（surface） | `__getattr__`（:7）再导出图类型（out_of_scope 家族）+ Goal（schemas/goal.go） | CURATED → partial/surface |
| `core/framework/skills/skill_errors.py` | unported | **ported** | `internal/skills/errors.go:8` SkillErrorCode（7 码一致）/ `:35` LogSkillError；caller `skills/discovery.go:281`；errors_test.go 自报上游 Parity | CURATED → ported |
| `core/framework/testing/approval_cli.py` | unported | 真未移植 | 无 interactive/batch approval CLI；全库无 ApprovalAction/ApprovalRequest | 保留 unported；P3 |
| `core/framework/testing/approval_types.py` | unported | 真未移植 | 无审批类型；仅 `framework/testing/__init__.py` re-export | 保留 unported；P3 |
| `core/framework/testing/categorizer.py` | unported | 真未移植 | 无 ErrorCategorizer/错误分类 | 保留 unported；P3 |
| `core/framework/testing/debug_tool.py` | unported | 真未移植 | 无 DebugTool/DebugInfo | 保留 unported；P3 |
| `core/framework/testing/test_case.py` | unported | 真未移植 | 无 Test/ApprovalStatus/TestType 运行时模型 | 保留 unported；P3 |
| `core/framework/testing/test_result.py` | unported | 真未移植 | 无 TestResult/TestSuiteResult/ErrorCategory | 保留 unported；P3 |
| `core/framework/testing/test_storage.py` | unported | 真未移植 | 无 TestStorage 文件存储 | 保留 unported；P3 |
| `core/framework/tools/browser_tools.py` | unported | **ported** | `internal/tools/integrations/browser_setup.go:59` InstallBrowserSetup；caller `cli/session_tools.go:349`；“静态发现载荷”逐字落该文件（browser_setup_test.go:32 自述） | CURATED → ported |
| `core/framework/tools/harness_tools.py` | unported | **ported** | `internal/tools/mcp.go:80` HarnessTool / `:94` HarnessGroups / `:112` ContextParams；caller `tools/mcp_registry.go:632`；组执行器由 builtin 工具箱 + `cli/session_tools_harness_groups.go:80` 承担 | CURATED → ported |
| `core/framework/tools/queen_lifecycle/context.py` | unported | partial（surface） | Python 工具上下文 dataclass（:18，含 update_meta）；Go 工具直取会话：`server/queen_lifecycle_tools.go` + `queen_phase_state.go:132` MetaPath | CURATED → partial/surface |
| `core/framework/tracker/decision_tracker.py` | unported | 真未移植 | 无 DecisionTracker 运行时类（start_run/decide/record_outcome）；schemas/decision.go、run.go、storage/concurrent.go 数据层已移植但无写入驱动器 | 保留 unported；P3 |
| `core/framework/utils/io.py` | unported | **ported** | `internal/atomicfs/atomicfs.go:12` WriteFile（tempfile+rename 原子写）；caller `mcpregistry/registry.go:141`（全库通用） | CURATED → ported |
| `core/framework/utils/python_env.py` | unported | 真未移植 | 无 `UV_INTERNAL__PYTHONHOME` 处理（grep 0）；仅 Python/uv launcher 继承场景需要，Go 宿主不是 uv 启动的 Python 解释器 | 保留 unported；P3（建议 wontfix） |
| `core/framework/utils/task_registry.py` | unported | 真未移植 | 无 TaskRegistry；Python 侧无运行时消费方（仅 `utils/__init__` re-export）；Go goroutine 原语等价 | 保留 unported；P3 |
| `core/framework/utils/text.py` | unported | partial | `internal/server/routes_colonies.go:479` humanizeSlug 注记 mirrors humanize_slug（含 str.title() 词边界）；仅同文件 :460 使用，C12 跨文件调用规则不命中 | CURATED → partial/call_chain |

## 误标修正清单（30 条）

**→ ported（11）**：`__main__.py`、`agents/queen/queen_memory_v2.py`、`credentials/key_storage.py`、`llm/key_check.py`、`llm/key_pool.py`、`llm/model_catalog.py`、`loader/mcp_errors.py`、`skills/skill_errors.py`、`tools/browser_tools.py`、`tools/harness_tools.py`、`utils/io.py`。

**→ partial（17）**：`__init__.py`（root/agent_loop/agents/agents.queen/credentials/host/llm/orchestrator 共 8 个 surface）、`agents/queen/agent.py`、`agents/queen/config.py`、`cloud_sync_hooks.py`、`credentials/validation.py`、`host/isolation.py`、`agent_loop/internals/stall_detector.py`、`agent_loop/internals/tool_input_coercer.py`、`tools/queen_lifecycle/context.py`、`utils/text.py`。

**→ out_of_scope（2）**：`host/shared_state.py`、`loader/preload_validation.py`。

修正方式：全部写入 `scripts/gen_module_inventory.py` 的 `CURATED`，`python3 scripts/gen_module_inventory.py` 重生 `contracts/module-inventory.json`（同条目 `basis/evidence/note` 带 file:line 证据）；C12 `--check` PASS，且无同名 WARN。

## 真未移植（21 条）按用户可见性排序

| 优先 | 条目 | 用户可见性 | 估算 |
| --- | --- | --- | ---: |
| P1 | `credentials/validation.py` + `credentials/setup.py`（check-agent/resync） | 前端 CredentialsModal 打开即调 `/api/credentials/check-agent`（Go 500 占位）；Aden resync 依赖的 deps.Resync 未接线——用户点开即报错 | 10h |
| P1 | `agent_loop/internals/tool_input_coercer.py` 接线 | 模型发错类型的工具参数不再被强制转换（Python 在 agent_loop.py:4805 派发前调用）——工具调用可靠性 | 2h |
| P2 | `agent_loop/internals/sentinel_tool.py` | 工具面对模型广告 sentinel_setup 但返回 not_implemented；桌面端有替代路径 | 8h |
| P2 | `llm/antigravity.py` | Settings 的订阅列表含 Antigravity；Go 运行期显式报「runtime does not implement」（main_slot.go:39） | 12h |
| P2 | `agents/queen/tools_ga_migration.py` | 升级后已有 sidecar 不会自动获得新 GA 工具（工具库需手动开） | 4h |
| P2 | `credentials/aden/client.py` | Aden 连接的账号不会同步/刷新（UI 有 Aden 入口） | 12h |
| P2 | `host/stream_runtime.py` | 决策记录不落盘（无 UI，Builder 分析面） | 10h |
| P3 | `tracker/decision_tracker.py` | 同上（同一决策记录子系统） | 8h |
| P3 | `credentials/oauth2/{base_provider,hubspot_provider,lifecycle,zoho_provider}.py` | OSS 扩展 API；运行时无调用方（工具侧 refresh 已另行实现） | 12h |
| P3 | `testing/*`（7 个） | Python 测试生成框架；Python 侧 CLI 也未接线（无 `test-run` 命令） | 16h |
| P3 | `global_db/cli.py` | `python -m framework.global_db.cli` 运维 CLI，无入口注册 | 6h |
| P3 | `utils/python_env.py` | Python/uv launcher 专用；Go 宿主不产生该场景（建议 wontfix） | 1h |
| P3 | `utils/task_registry.py` | 无运行时消费方；Go goroutine 原语等价 | 2h |

## delivery-backlog（已登记前 5）

| id | 优先 | 估算 |
| --- | --- | ---: |
| `unported-credentials-check-agent` | P1 | 10h |
| `unported-tool-input-coercer-wire` | P1 | 2h |
| `unported-sentinel-setup-tool` | P2 | 8h |
| `unported-antigravity-provider` | P2 | 12h |
| `unported-ga-tool-migration` | P2 | 4h |

## 残余观察（不在 51 条范围内，未改动）

- `credentials/aden/provider.py`、`credentials/aden/storage.py` 登记为 ported，但 Go 侧只有通用 `credentials/provider.go`/`storage.go`，没有 Aden 同步 provider/缓存存储（`AdenSyncProvider`/`AdenCachedStorage` grep 0 命中）。属于「文件级 ported 掩盖子系统缺失」的潜在假标，建议下一轮 inventory lane 复核。
- `host/stream_runtime.py` 与 `tracker/decision_tracker.py` 是同一决策记录子系统的两半；若决定不移植，应合并登记为范围外，而不是长期留在 unported。

## 后续变更（2026-11-22 晚，lane `cred-check`）

- **P1 `unported-credentials-check-agent` 已闭环**：`/api/credentials/check-agent` 由 500 占位改为完整实现（`internal/credentials/agent_validation.go` ValidateAgentCredentials / `health_check.go` 42 个 checker / `agent_nodes.go` LoadAgentNodes；路由 `internal/server/routes_agents.go:264`），`/api/credentials/resync` 的生产 hook 已接线为本地默认（`internal/server/routes_credentials.go:194` resyncLocalCredentials：重读密钥 + store→env 桥 + store 缓存清理），Aden 云端 token 拉取仍属 P2 `credentials/aden/client.py`。
- **登记册变化**：`credentials/setup.py` unported → partial（load_agent_nodes 已移植接线）；`credentials/validation.py` 保持 partial（validate/compute/CredentialStatus 已移植，build_setup_session_from_error 未移植）；`credentials/aden/client.py` 保持 unported，basis 注明 resync 已接线。重生后总计 **273 = 217 ported / 27 partial / 9 out_of_scope / 20 unported**（`contracts/module-inventory.json`，C12 PASS；其中 tool_input_coercer 已由 coercer-wire lane 接线升级 ported）。
- 证据与复算：`go_hive/docs/e2e-assets/credentials-check-agent/README.md`。

## 后续变更（2026-11-22 晚，lane `sentinel-setup`）

- **P2 `unported-sentinel-setup-tool` 已闭环**：`core/framework/agent_loop/internals/sentinel_tool.py` 全量移植——工具定义（描述 + JSON Schema 逐字，`internal/cli/tool_surface_flat.json` 冻结契约不变）在 `internal/sentinel/sentinel_tool.go::SentinelSetupToolDef`，执行入口 `HandleSentinelSetup`（help/status/store_token/detect_chat/configure/test）；生产接缝在 `internal/cli/sentinel_setup_tool.go`（加密凭据保存 → store→env 桥、进程级 manager、`tool_catalog_refreshed` 事件）。`internal/cli/tool_surface_flat.go` 的 `sentinel_setup` not_implemented 占位已删除，flat 面路由到真实执行；worker 可见面不变（工具不进 catalogue defs，与上游 queen-only 一致）。
- **未验证项**：真实 Telegram/Slack 凭据的校验与投递（单测全部使用注入 HTTP；token 校验以 auth.test/getMe 的响应体为准）；desktop UI 对 `tool_catalog_refreshed` 的刷新链路未起真实前端验证。
- **登记册变化**：`agent_loop/internals/sentinel_tool.py` unported → ported（basis 指向 `internal/sentinel/sentinel_tool.go` 的 `// Parity:` 锚点 + `internal/cli` 生产调用方；evidence `internal/sentinel/sentinel_tool_test.go`）。重生后 unported 20 → 19。
- 验证：`go test ./internal/sentinel/ ./internal/cli/ ./internal/server/`；`bash go_hive/scripts/merge_postcheck.sh` 四关。

## 后续变更（2026-11-22 晚，lane `antigravity`）

- **P2 `unported-antigravity-provider` 已闭环**：`core/framework/llm/antigravity.py` 全量移植为 `internal/llm/antigravity.go`（provider：Gemini contents 转换、JSON Schema → OpenAPI 3.0 清洗、非流式/SSE 解析、端点回退、401/403 refresh 重试、错误文案）+ `internal/llm/antigravity_credentials.go`（`$HIVE_HOME/antigravity-accounts.json` V1–V4 schema、Antigravity IDE SQLite state DB 回退、`refresh_token` 交换；client id/secret 复用 `server.AntigravityClientID` / `AntigravityClientSecret`）。接线：`server.MainSlotProvider`（`use_antigravity_subscription` → `AntigravityProviderFor`，删除 `ErrAntigravityUnsupported`）与 `cli.workerChatProvider`（`worker_llm` 同标志）；Antigravity provider 无 `reconfigure`，`newModelSlot` 对其不设重建工厂（与上游 `hasattr(prov, "reconfigure")` 跳过一致）。
- **测试**：`internal/llm/antigravity_test.go`（mock OAuth+网关：过期 token refresh、401 重试、5xx 端点回退、非重试 4xx 文案、refresh 失败回退旧 token、无凭据文案、SSE 文本/工具调用/thoughtSignature 回传、accounts 文件 V1–V4、IDE DB）+ `internal/llm/antigravity_schema_test.go`（逐条对齐 `core/tests/test_antigravity_schema.py`：union→nullable、递归 properties/items/anyOf、不改输入、纯 null 回退、多类型 union 报错、坏 schema 工具单独剔除）+ server/cli 接线测试。
- **登记册变化**：`llm/antigravity.py` unported → ported（auto 扫描：`internal/llm/antigravity.go` 由 `internal/server/main_slot.go` 引用）；重生后总计 **273 = 218 ported / 27 partial / 9 out_of_scope / 19 unported**（C12 PASS）。`go_hive/docs/env-config-contract-diff.md` / `contracts/config-keys.json` 仍冻结在旧 `go_partial` 结论（`llm.antigravity_client_id` / `_secret` / `use_antigravity_subscription`），需下一次 `freeze_env_config_contract.py` 重生成车道同步；`contracts/config-support-ledger.json` 对应条目的 `partial` 理由同样待该车道更新。
- **未验证**：本 lane 无真实 Google OAuth 凭据与 cloudcode 访问，端到端（真实 refresh/真实模型回合）未验证；单测全部走 httptest 假网关。

## 后续变更（2026-11-22 晚，lane `ga-migration`）

- **P2 `unported-ga-tool-migration` 已闭环**：GA（generally-available）工具 sidecar 愈合迁移全量移植——`internal/tools/category_additions.go`（`_CATEGORY_ADDITIONS` 表 + `_parse_version`/`_category_fully_resolvable`/`infer_category_additions`/`grant_role_default_additions`）、`internal/server/tools_ga_migration.go`（`_apply_to_sidecar`/`migrate_queen_sidecars`/`migrate_colony_sidecars`/`run_ga_tool_migration` + `colony_metadata.list_colony_ids`）。启动接线 `internal/cli/loader.go` `runGAToolMigration`（storage 迁移之后、任何会话/preloaded colony 装配之前，对齐 create_app）；`load_queen_tools_config` 侧车读取同步愈合（已知 queen 走角色默认 grant、自定义 queen 走 baseline 启发式），替换 routes_queens_tools.go 原「no Go port yet」注记。幂等性按侧车 `saved_on_version`；写回 payload 与 Python 一致（enabled_mcp_tools/updated_at/saved_on_version）。
- **登记册变化**：`tools_ga_migration.py` unported → ported（kind=parity，counterpart `internal/server/tools_ga_migration.go`，caller `internal/cli/tools_ga_migration.go`，证据 `tools_ga_migration_test.go`）；`queen_tools_defaults.py`/`colony_metadata.py` 的 go_counterparts 各 +1（distributed）。重生后总计 **273 = 218 ported / 27 partial / 9 out_of_scope / 19 unported**（`contracts/module-inventory.json`，C12 PASS）。
- 验证：`go test -count=1 ./internal/{tools,server,cli,queen}` 全 `ok`；`make parity`（5,769/5,769）/`make parity-targets`（6,299 refs，0 missing）；`bash go_hive/scripts/merge_postcheck.sh` 四关全过（gofmt / wiring 21 文件 68 项 / C12 verdict=PASS / arch `--check`）；`make tool-surface-check`/`make tool-output-check` 无漂移。
- 说明：Google Analytics 工具族（7 action）早已由 batch-7 移植（`internal/tools/integrations/google_analytics*.go`，冻结契约 `go_hive/contracts/tool-definitions.json` go_only_tools[35] 逐字一致），本项中「GA」指 generally-available 工具晋升，非 Google Analytics。

## 复现

```sh
# 符号对撞（本 lane 探针）：ast 取 Python 顶层 def/class → 归一化（lower/去非字母数字）
# ↔ go_hive/go-hive 非测试 Go 标识符集合；输出同 basename / 共享符号候选，再逐条读调用点确认。
python3 scripts/gen_module_inventory.py --dry-run     # 矩阵：files=273 ported=216 partial=28 out_of_scope=9 unported=20（本分支实测）
python3 scripts/check_module_inventory.py --check     # verdict=PASS，无 WARN
python3 scripts/check_module_inventory.py --report    # 按领域矩阵 + Top unported
```
