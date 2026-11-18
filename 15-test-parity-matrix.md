# 15 · 测试对等矩阵

> 快照时间：2026-11-18 19:14。数据直接来自 `go_hive/go-hive/internal/**/*_test.go` 的 `// Parity:` / `// Parity-exempt:` 注解。除这两类门禁注解外，测试与实现代码不含任何注释。
> **权威门禁是 `bash go_hive/scripts/check_parity.sh`**，引用可回溯性由 `bash go_hive/scripts/check_parity_targets.sh` 复核；本文只是可读视图。注解变动后请重跑脚本并以脚本输出为准，不要手改数字。

## 1. 读法

- 每组三列：`tests` 为该文件 `func Test...` 数量；`annotated` 为注解块中含 `Parity:` 或 `Parity-exempt:` 的测试数；`missing` 为二者之差。
- `Python 参考` 列来自注解中出现的路径，而不是按名字猜测。注解请写成单行 `// Parity: path::test_a, path::test_b`：`check_parity_targets.sh` 只审计单行注解，多行注释块中的引用不会进入引用检查。
- **弱注解**：注解块中有 `Parity:` 字样，但没有出现任何 `.py` 路径（例如只写 `// Parity: ::test_foo` 或一句散文）。这类测试能通过存在性脚本，却无法按路径回溯；当前为 **0**。
- 一个 Go 测试覆盖多条 Python 用例时，注解逐条列出；本文只按文件聚合，不展开到单条用例。
- `Parity-exempt:` 表示上游没有直接对应物，理由写在注解正文并由人工评审；它不是缺口。

## 2. 总览

| 包 | tests | annotated | missing | 覆盖率 |
| --- | --- | --- | --- | --- |
| `internal/agentloop` | 318 | 318 | 0 | 100% |
| `internal/agents` | 34 | 34 | 0 | 100% |
| `internal/atomicfs` | 4 | 4 | 0 | 100% |
| `internal/attachments` | 5 | 5 | 0 | 100% |
| `internal/cli` | 88 | 88 | 0 | 100% |
| `internal/colony` | 84 | 84 | 0 | 100% |
| `internal/credentials` | 48 | 48 | 0 | 100% |
| `internal/eventbus` | 113 | 113 | 0 | 100% |
| `internal/globaldb` | 23 | 23 | 0 | 100% |
| `internal/hivehome` | 3 | 3 | 0 | 100% |
| `internal/llm` | 215 | 215 | 0 | 100% |
| `internal/llmjudge` | 22 | 22 | 0 | 100% |
| `internal/maintenance` | 42 | 42 | 0 | 100% |
| `internal/mcpregistry` | 160 | 160 | 0 | 100% |
| `internal/memory` | 46 | 46 | 0 | 100% |
| `internal/orchestrator` | 40 | 40 | 0 | 100% |
| `internal/playbook` | 32 | 32 | 0 | 100% |
| `internal/queen` | 10 | 10 | 0 | 100% |
| `internal/ratelimit` | 21 | 21 | 0 | 100% |
| `internal/runtimehealth` | 3 | 3 | 0 | 100% |
| `internal/schemas` | 10 | 10 | 0 | 100% |
| `internal/server` | 279 | 279 | 0 | 100% |
| `internal/session` | 17 | 17 | 0 | 100% |
| `internal/sessionreport` | 8 | 8 | 0 | 100% |
| `internal/skills` | 219 | 219 | 0 | 100% |
| `internal/storage` | 55 | 55 | 0 | 100% |
| `internal/tasks` | 128 | 128 | 0 | 100% |
| `internal/tools` | 136 | 136 | 0 | 100% |
| `internal/tracker` | 35 | 35 | 0 | 100% |
| `internal/triggers` | 17 | 17 | 0 | 100% |
| **合计** | **2,215** | **2,215** | **0** | **100%** |

脚本退出码为 **0**（`check_parity: OK — 2215 个测试全部带有 parity 注解或豁免说明。`）；`check_parity_targets.sh` 审计 **2,035** 条 `path::test` 引用，0 missing，另有 46 条 shorthand/prose。

## 3. 逐包矩阵

### 3.1 agentloop

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `node_conversation_test.go` | 79 | 79 | 0 | `core/tests/test_node_conversation.py`（`TestPersistence`、`TestFileConversationStore`、`TestConversationIntegration`、`TestPartialCheckpoint`、受保护值提取、压缩阈值） |
| `node_conversation_tools_test.go` | 50 | 50 | 0 | `core/tests/test_node_conversation.py`（`TestRepairOrphanedToolCalls`、工具查询、微压缩、消息标记、用户消息截断、图像提升） |
| `phase_compaction_test.go` | 19 | 19 | 0 | `core/tests/test_phase_compaction.py`、`test_agent_loop_prompting_cache.py` |
| `context_error_test.go` | 5 | 5 | 0 | 无（`Parity-exempt`：`context_error.go` 无独立 Python 测试模块） |
| `llm_compact_test.go` | 7 | 7 | 0 | 无（`Parity-exempt`：`llm_compact.go` 无独立 Python 测试模块） |
| `loop_dynamic_tools_test.go` | 2 | 2 | 0 | `core/tests/test_dynamic_tool_refresh_midturn.py` |
| `loop_midturn_test.go` | 3 | 3 | 0 | `core/tests/test_midturn_input.py` |
| `reminder_e2e_test.go` | 5 | 5 | 0 | `core/tests/test_reminder_e2e.py` |
| `idle_nudge_test.go` | 37 | 37 | 0 | `core/tests/test_idle_nudge.py`（ParkReason 属性表与 Activity 映射、六种子态触发/抑制、三套预算、变体封顶与自限速、gate 去重与重登、`Reset` 重装、ticker 停泊唤醒、`TakePending`、默认 `LoopConfig` 值） |
| `stream_stall_test.go` | 8 | 8 | 0 | `core/tests/test_stream_stall.py`（inactive 正文与 meta、ttft 标签、每轮上限与 `ResetTurn`、disabled、零上限、无 reason、`points`；meta.elapsed 与日志行为 `Parity-exempt`） |
| `synthetic_tools_test.go` | 11 | 11 | 0 | `core/tests/test_ask_user_sanitizer.py` |
| `skill_allowlist_test.go` | 7 | 7 | 0 | `core/tests/test_skill_allowlist.py` |
| `skill_resources_test.go` | 4 | 4 | 0 | `core/tests/test_skill_resources.py` |
| `tool_skill_reminders_test.go` | 12 | 12 | 0 | `core/tests/test_tool_skill_reminders.py` |
| `find_json_test.go` | 44 | 44 | 0 | `core/tests/test_find_json_hardened.py`（基础正确性、100KB–2MB 性能回归、对抗输入、行为对等） |
| `colony_parallel_nudge_test.go` | 9 | 9 | 0 | `core/tests/test_colony_parallel_nudge.py`（pilot+tracker 双门禁、run_playbook 后静默、colony phase 外静默、仅 queen、冷却/上限；动态 provider 优先与异常回落为 `Parity-exempt`） |
| `tiers_parity_test.go` | 8 | 8 | 0 | `core/tests/test_worker_tool_tiers.py`（`TestWorkerReminder`、`TestDynamicRefreshSynthetics`、`TestPromptGatingUsesTier`；把 `internal/tools.ToolTierState` 接进循环） |
| `spillover_headers_test.go` | 1 | 1 | 0 | `core/tests/test_janitor_review_fixes.py::test_extractors_match_all_header_formats`（四种 spillover 头部形态全部可解析：当前 truncate 提示、微压缩提示、散文式 saved at、legacy saved-to） |
| `loop_tool_timeout_test.go` | 2 | 2 | 0 | `core/tests/test_tool_timeout_probe_policy.py::test_loopconfig_prefix_timeout_resolution`（180/60 前缀解析、最长前缀、空覆盖回退、agent 预算低于 MCP 上限不变量）；Go 侧 `callExecutor` 真实执行预算为 `Parity-exempt` |
| `budget_cap_test.go` | 5 | 5 | 0 | `core/tests/test_adaptive_tool_budget.py`（setter 收缩成功、拒绝抬升/相等/零/负数/无上限循环、拒绝无宽限轮、`tool_calls_used` 读取、`budget_limited` 读实时 `tool_lifetime_budget_grace`；对应 `AgentLoop.apply_lifetime_budget_cap` 与 `tool_calls_used`） |

### 3.2 atomicfs

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/atomicfs/atomicfs_test.go` | 4 | 4 | 0 | `core/tests/test_skill_trust.py::TestTrustedRepoStore::test_atomic_write`、`core/tests/test_skill_registry.py::TestCacheAtomicWrite::test_atomic_write_uses_tmp_then_replace`；JSONL/`ReadJSON` helper 与权限断言为 `Parity-exempt` |

### 3.3 credentials

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `bootstrap_test.go` | 8 | 8 | 0 | `core/tests/test_credential_bootstrap.py` |
| `fernet_test.go` | 5 | 5 | 0 | 无（`cryptography.fernet` 跨语言向量，全部 `Parity-exempt`） |
| `models_test.go` | 6 | 6 | 0 | `core/framework/credentials/tests/test_credential_store.py` |
| `oauth2_test.go` | 4 | 4 | 0 | `core/framework/credentials/tests/test_credential_store.py` |
| `provider_test.go` | 2 | 2 | 0 | `core/framework/credentials/tests/test_credential_store.py` |
| `shell_test.go` | 5 | 5 | 0 | `tools/tests/test_shell_config.py`；桌面模式门控为 `Parity-exempt` |
| `storage_test.go` | 7 | 7 | 0 | `core/framework/credentials/tests/test_credential_store.py` |
| `store_test.go` | 9 | 9 | 0 | `core/framework/credentials/tests/test_credential_store.py`、`core/tests/test_hive_cred_cli.py` |
| `template_test.go` | 2 | 2 | 0 | `core/framework/credentials/tests/test_credential_store.py` |

安全关键路径（Fernet、shell 配置）已全部注解：可回溯的写 `path::test`，Go 原语独有的写 `Parity-exempt` 并说明理由。

### 3.4 eventbus

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `event_bus_test.go` | 71 | 71 | 0 | `core/tests/test_event_bus.py` |
| `event_type_extension_test.go` | 30 | 30 | 0 | `core/tests/test_event_type_extension.py` |
| `events_history_page_test.go` | 0 | 0 | 0 | 文件级说明：8 条分页用例 `DEFERRED TO THE SERVER LANE`（依赖 `routes_sessions`，不测事件总线） |
| `events_history_tail_test.go` | 0 | 0 | 0 | 文件级说明：9 条尾部读取用例 `DEFERRED TO THE SERVER LANE`（依赖 `routes_sessions`）；其余 4 条 `test_truncate_*` 已落地于 `internal/server/routes_events_history_test.go` |
| `events_policy_test.go` | 12 | 12 | 0 | `core/tests/test_events_policy.py` |

### 3.5 hivehome

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/hivehome/home_test.go` | 3 | 3 | 0 | `core/tests/test_hive_home_isolation.py` |

### 3.6 memory

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `memory_test.go` | 20 | 20 | 0 | `core/tests/test_queen_memory.py` |
| `reflection_test.go` | 17 | 17 | 0 | `core/tests/test_queen_memory.py`；`_write_error` 诊断文件为 `Parity-exempt`（上游无 pytest） |
| `timeline_test.go` | 9 | 9 | 0 | `core/tests/test_memory_timeline_recall.py` |

### 3.7 session

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/session/session_test.go` | 8 | 8 | 0 | `core/tests/test_path_traversal_fix.py`、`core/tests/test_node_conversation.py`、`core/tests/test_session_listing_order.py`；cursor/checkpoint/文件名解析为 `Parity-exempt` |
| `internal/session/fork_test.go` | 5 | 5 | 0 | `core/tests/test_session_fork.py`（按用户消息时间戳裁剪 parts/events、整会话复制、未知时间点拒绝、压缩后时间点拒绝、attachment/meta 继承与 timeline 已提取标记） |
| `internal/session/cold_test.go` | 4 | 4 | 0 | `core/tests/test_session_listing_order.py`（2 条对等：严格按创建时间倒序、跨多个 queen 仍全局有序）；2 条 `Parity-exempt`：colony fork 保留在 colony 列表、active-session 跳过无消息会话 |

### 3.8 skills

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `catalog_test.go` | 14 | 14 | 0 | `core/tests/test_skill_catalog.py` |
| `config_test.go` | 9 | 9 | 0 | `core/tests/test_default_skills.py` |
| `defaults_test.go` | 12 | 12 | 0 | `core/tests/test_default_skills.py` |
| `discovery_test.go` | 8 | 8 | 0 | `core/tests/test_skill_discovery.py` |
| `errors_test.go` | 7 | 7 | 0 | `core/tests/test_skill_errors.py` |
| `install_requests_test.go` | 6 | 6 | 0 | `core/tests/test_skill_install_requests.py` |
| `installer_test.go` | 14 | 14 | 0 | `core/tests/test_skill_installer.py` |
| `live_reload_test.go` | 6 | 6 | 0 | `core/tests/test_skill_live_reload.py` |
| `manager_test.go` | 10 | 10 | 0 | `core/tests/test_skill_integration.py`、`test_skill_overrides.py` |
| `overrides_test.go` | 7 | 7 | 0 | `core/tests/test_skill_overrides.py` |
| `parser_test.go` | 14 | 14 | 0 | `core/tests/test_skill_parser.py` |
| `registry_test.go` | 26 | 26 | 0 | `core/tests/test_skill_registry.py` |
| `skill_writer_test.go` | 14 | 14 | 0 | `core/tests/test_write_skill_tool.py`、`test_skill_overrides.py` |
| `tool_gating_test.go` | 5 | 5 | 0 | `core/tests/test_worker_tool_tiers.py::test_gating_reads_eager_set_not_pool`；其余渲染细节为 `Parity-exempt` |
| `trust_test.go` | 42 | 42 | 0 | `core/tests/test_skill_trust.py` |
| `validator_test.go` | 25 | 25 | 0 | `core/tests/test_skill_validator.py` |

全部 16 个文件都已达到 100%，可作为新增测试的注解样式样板（每条注解都尽可能带 `path::test`）。

### 3.9 tracker

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/tracker/tools_test.go` | 18 | 18 | 0 | `core/tests/test_tracker_tools.py`；queen 全局作用域短路为 `Parity-exempt` |
| `internal/tracker/tracker_test.go` | 17 | 17 | 0 | `core/tests/test_tracker_db.py` |

### 3.10 triggers

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/triggers/missed_test.go` | 14 | 14 | 0 | `core/tests/test_missed_triggers.py`、`core/tests/test_sse_trigger_rehydration.py`；cron 解析矩阵为 `Parity-exempt`（Python 用 croniter） |
| `internal/triggers/persistence_test.go` | 3 | 3 | 0 | `core/tests/test_trigger_persistence.py` |

### 3.11 tasks

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/tasks/store_test.go` | 37 | 37 | 0 | `core/framework/tasks/tests/test_store.py`（任务记录、状态流转、归档/恢复、原子写入） |
| `internal/tasks/tools_test.go` | 25 | 25 | 0 | `core/framework/tasks/tests/test_tools.py`（task 工具、批量创建、hook 门禁、完成后缀、pivot 路由） |
| `internal/tasks/scoping_test.go` | 8 | 8 | 0 | `core/framework/tasks/tests/test_scoping.py`（session/colony 解析与环境覆盖） |
| `internal/tasks/reminders_test.go` | 44 | 44 | 0 | `core/framework/tasks/tests/test_reminders.py`（提醒生成、去重、完成/阻塞语义） |
| `internal/tasks/e2e_test.go` | 11 | 11 | 0 | `core/framework/tasks/tests/test_e2e.py`（跨进程持久性、REST 清理、事件发射）；子进程 helper 为 `Parity-exempt` |
| `internal/tasks/integration_test.go` | 3 | 3 | 0 | `core/framework/tasks/tests/test_integration.py` |

### 3.12 globaldb

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/globaldb/grid_query_test.go` | 23 | 23 | 0 | `core/framework/global_db/tests/test_grid_query.py`（过滤、排序白名单、搜索、分组计数、删除、SQL 转义） |

### 3.13 schemas

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/schemas/run_test.go` | 10 | 10 | 0 | `core/tests/test_run.py`（Run 决策/问题/完成/时长、RunSummary、RuntimeMetrics） |

### 3.14 storage

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/storage/concurrent_test.go` | 10 | 10 | 0 | `core/tests/test_storage.py`（生命周期、缓存统计/失效/清理）；批量保存的间接覆盖为 `Parity-exempt` |
| `internal/storage/conversation_test.go` | 9 | 9 | 0 | `core/tests/test_node_conversation.py`（`TestFileConversationStore`、`TestConversationIntegration`） |
| `internal/storage/path_traversal_test.go` | 25 | 25 | 0 | `core/tests/test_path_traversal_fix.py`（键校验、绝对路径/Windows 路径、保留名与真实文件隔离） |
| `internal/storage/session_summary_test.go` | 11 | 11 | 0 | `core/tests/test_session_summary.py`（重建、增量更新、陈旧判定、客户端可见性） |

> 注意：`internal/storage` 与既有 `internal/session` 存在语义重叠（两者都提供会话/检查点存储），目前并行保留；合并策略见 `12-design.md` 与 STATUS 的决策记录。

### 3.15 colony

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/colony/colony_db_test.go` | 4 | 4 | 0 | `core/tests/test_colony_db.py`（playbook 运行记录 upsert / 列表回读 / 空库） |
| `internal/colony/resume_test.go` | 5 | 5 | 0 | `core/tests/test_derive_resume_colony_id.py`（从会话目录反推 colony、DM 影子不劫持、未知会话） |
| `internal/colony/seed_test.go` | 4 | 4 | 0 | `core/tests/test_colony_seed.py`（dialog goal 作为用户消息、无 goal 时的 carry-on 注记、等待 loop 启动、超时告警） |
| `internal/colony/worker_definition_test.go` | 6 | 6 | 0 | `core/tests/test_worker_definition.py`（worker 运行时身份、系统提示词逐字节等价、默认 loop config、input_data 绑定、worker.json 序列化与可选 concurrency_hint） |
| `internal/colony/reap_test.go` | 4 | 4 | 0 | `core/tests/test_colony_reap_timeout.py`（bridge 不响应时放弃关闭、无 worker/无浏览器模块 no-op、错误吞掉、profile 回落 default） |
| `internal/colony/scheduler_test.go` | 10 | 10 | 0 | `core/tests/test_colony_scheduler.py`（并发上限准入、FIFO 排队、终态补位、停止合成 stopped 报告、batch 元数据、双 batch FIFO；dispatch 门禁/跨 colony 过滤/drain 防御跳过/batch id 格式为 `Parity-exempt`） |
| `internal/colony/runtime_overseer_test.go` | 14 | 14 | 0 | `core/tests/test_colony_runtime_overseer.py`（goal 公开属性、overseer 持久且幂等、SUBAGENT_REPORT 扁平载荷、崩溃合成失败报告、batch id 顺序、报告按输入序收集、缺失 id、seed 落盘、report_to_parent 仅 worker: 流、非 worker 流不终止、混合工具批仍终止、worker: 流经报告终止） |
| `internal/colony/worker_tier_spawn_test.go` | 4 | 4 | 0 | `core/tests/test_worker_tier_spawn.py`（keep-set 非空时接线 tier 与三个 provider、空 keep-set 全失效、preload 在 spawn 提升且过滤未知、loaded 集跨重建从 `tool_tiers.json` 恢复） |
| `internal/colony/worker_binding_test.go` | 2 | 2 | 0 | `core/tests/test_worker_tracker_db_propagation.py`（queen 注入的 binding 在 worker 侧重水化到 colony tracker DB、JSON 往返同形；无 binding 时 tracker 工具以 no colony context 拒绝） |
| `internal/colony/worker_started_at_test.go` | 2 | 2 | 0 | `core/tests/test_worker_started_at.py`（started_at 是墙钟 epoch 秒而非单调时钟；未启动的 worker 为 0） |
| `internal/colony/worker_loop_config_test.go` | 14 | 14 | 0 | `core/tests/test_loop_config_overrides.py`（13 条逐条对等：worker profile 基线、max_iterations / max_context_tokens / grace_iterations 覆盖、tool_call_budget 与 lifetime_budget 的 0 哨兵、hard multiple 不可由 queen 调、组合覆盖、未知键静默丢弃、9 组越界、非整数报错、白名单集合；`json.Number` 契约 1 条为 `Parity-exempt`） |
| `internal/colony/adaptive_budget_test.go` | 14 | 14 | 0 | `core/tests/test_adaptive_tool_budget.py`（14 条逐条对等：样本不足不动、收缩到地板并钳在途 worker、公式 max-guard 越过观测峰值留余量、中位数抬升被拒、证据抬升从陈旧低值恢复、抬升封顶 profile base、抬升不波及已钳在途 worker、max-guard 跨窗口淘汰存活、离散度守卫跳过异构样本、常态昂贵时封顶 base 不收缩、样本排除矩阵、kill switch、准入钳制、`get_stats` 名义值；上游 `test_workers_route_serializes_budget_telemetry` 由 `internal/server/worker_list_test.go` 覆盖路由序列化器，与 colony 策略测试分开计数） |

### 3.16 orchestrator

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `internal/orchestrator/hallucination_test.go` | 24 | 24 | 0 | `core/tests/test_hallucination_detection.py`（`TestDataBufferHallucinationDetection` 10、`TestOutputValidatorHallucinationDetection` 5、`TestEdgeCases` 4，共 19 条对等）；权限视图、键校验、长度上限、schema 子集、`validate_all` 组合 5 条为 `Parity-exempt`（对应 `framework/orchestrator/node.py` / `validator.py` 的无 pytest 行为） |
| `internal/orchestrator/safe_eval_test.go` | 16 | 16 | 0 | `core/tests/test_safe_eval.py`（122 条上游用例中 120 条逐类对等：`TestLiterals` 8、`TestDataStructures` 6、`TestArithmetic` 11、`TestExecutionTimeout` 4、`TestUnaryOps` 5、`TestComparisons` 13、`TestBooleanOps` 14、`TestTernary` 4、`TestVariables` 5、`TestSubscript` 4、`TestAttributeAccess` 3、`TestFunctionCalls` 16、`TestMethodCalls` 8、`TestSecurity` 9、`TestEdgeConditionPatterns` 10）；`TestExecutionTimeout` 的 2 条 SIGALRM/getitimer 内部用例合并为 1 条 `Parity-exempt` 截止时间轮询用例 |

### 3.17 tools

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `tool_registry_test.go` | 42 | 42 | 0 | `core/tests/test_tool_registry.py`（注册与发现、MCP 配置解析与重试、harness 分组、准入门禁与 verified-manifest、凭据快照 diff、resync/probe/kill、清理） |
| `harness_tools_test.go` | 10 | 10 | 0 | `core/tests/test_harness_tools.py`（内置 server 不启动子进程、上下文参数隐藏/注入、图像结果提升）；终端参数强制转换、async awaitable、openpyxl 条件性 `hive_tools` 3 条为 `Parity-exempt` |
| `tiers_test.go` | 11 | 11 | 0 | `core/tests/test_worker_tool_tiers.py`（`TestToolTierState` 7 条、`TestSearchToolsFactory` 4 条） |
| `mcp_registry_loader_test.go` | 3 | 3 | 0 | `core/tests/test_mcp_registry_loader.py`（注册表服务器工具名冲突时先到先得、已注册 MCP 服务器优先、`max_tools` 预算与耗尽跳过） |
| `coerce_test.go` | 21 | 21 | 0 | `core/tests/test_tool_input_coercer.py`（空输入/无 schema/未知属性/类型匹配的直通，字符串→整数/数字/布尔与 NaN/inf 拒绝，联合类型先匹配，`[{label}]`/`[{value}]`/单键对象解包，JSON 字符串数组与标量/非法 JSON 包裹，嵌套 `questions[].options`，字符串属性解包 `{label}` 对象） |
| `execute_test.go` | 9 | 9 | 0 | `core/tests/test_tool_executor_isolation.py`（专属 `hive-tool*` 池、hung 工具不饿死默认池）、`core/tests/test_tool_context_propagation.py`（执行上下文传播/未设置）、`core/tests/test_tool_timeout_probe_policy.py` 异步 5 例（alive/resetting 探测 1 次且不 kill，dead/unknown/无探测能力回退 legacy kill） |
| `tier_invariants_test.go` | 7 | 7 | 0 | `core/tests/test_toolset_tier_invariants.py` 全部 7 条（browser core/extended 仅共享 `browser_setup` 且并集为 legacy、terminal core/extended 不相交且并集为 basic、keep-set 类别存在、worker 冷尾排除且非空、queen browser 冷尾排除、worker 上限 24、实测热核保留） |
| `mcp_connection_manager_test.go` | 17 | 17 | 0 | `core/tests/test_mcp_connection_manager.py` 16 条（同名服务器复用与 refcount、最后一个引用断开并清池、并发 acquire/release、cleanup_all、重连替换并保留 refcount、stdio/sse 用 list_tools、http 不可达为 unhealthy、未知服务器、connect 失败清 transition、disconnect 失败被吞、完全释放后重连报错）；`SharedMCPClientPool` 单例为 `Parity-exempt`（上游 `get_instance()` 无直接断言） |
| `mcp_client_test.go` | 16 | 16 | 0 | `core/tests/test_mcp_client.py` 13 条（unix/sse 建连、unix 重试成功与耗尽、http 错误包裹且不重试、内容拆分文本/图片/PDF 五种形状、stdio 握手超时且不泄密）；3 条 `Parity-exempt`：两个子进程夹具与 Go 独有的新行 JSON-RPC 往返（上游经 MCP SDK，不手写帧） |

### 3.18 llm

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `catalog_test.go` | 16 | 16 | 0 | `core/tests/test_model_catalog.py`（各 provider 目录、默认模型存在性、token 上限、preset、重复 ID 拒绝、跨 provider 查找） |
| `capabilities_test.go` | 10 | 10 | 0 | `core/tests/test_capabilities.py`（工具过滤、视觉模型判断、图像提升、返回副本） |
| `endpoints_test.go` | 14 | 14 | 0 | 无（`Parity-exempt`：`framework/llm/endpoints.py` 无独立 Python 测试，按实现行为逐条对等） |
| `keycheck_test.go` | 19 | 19 | 0 | `core/tests/test_check_llm_key_openrouter.py`、`core/tests/test_key_check_packaged.py`（200/401/403/404/429、模型 slug、404 建议、Unicode 清理、自定义 base） |
| `keypool_test.go` | 8 | 8 | 0 | 无（`Parity-exempt`：`framework/llm/key_pool.py` 无独立 Python 测试，按实现行为对等） |
| `mock_test.go` | 11 | 11 | 0 | `core/tests/test_litellm_provider.py::test_mock_provider_acomplete`；mock 内部行为与 Go 测试缝隙为 `Parity-exempt` |
| `stream_events_test.go` | 29 | 29 | 0 | `core/tests/test_stream_events.py`（事件构造、默认值、文本/推理/工具/完成/错误事件） |
| `provider_test.go` | 84 | 84 | 0 | `core/tests/test_litellm_provider.py`（provider 初始化与 api_base 默认、工具格式转换与截断参数修复、Retry-After 解析与指数退避/抖动、minimax 判定、本地/ollama 模型、历史缓存断点、JSON 指令注入、cache-control 支持的模型前缀与 system 分块、缓存 token 提取与流式回退、成本优先级与目录定价、成本从 token 计算） |
| `request_shape_test.go` | 5 | 5 | 0 | `core/tests/test_litellm_provider.py`（`parallel_tool_calls` 的发送/拒绝锁存、OpenRouter tool-compat 消息在 cache-control 支持与 liquid 上的两种形状、ollama `num_ctx` 与空配置）；订阅/extra_body 分支及空 assistant 过滤为 `Parity-exempt` |
| `complete_test.go` | 10 | 10 | 0 | `core/tests/test_litellm_provider.py`（`complete` 基础、系统提示、工具、JSON 模式 4 组共 7 条）；429 重试循环、空响应重试三条短路、kwargs 组装与 `tool_choice` 优先级 3 条为 `Parity-exempt` |
| `anthropic_test.go` | 9 | 9 | 0 | `core/tests/test_litellm_provider.py`（`TestAnthropicProviderBackwardCompatibility` 的 7 条：is_llm_provider、init_defaults、init_custom_model、uses_litellm_internally、complete、passes_response_format、passes_json_mode）；密钥解析优先级与 `AComplete` 类型桥接/system 后缀 2 条为 `Parity-exempt` |

### 3.19 playbook

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `runner_test.go` | 20 | 20 | 0 | `core/tests/test_playbook_runner.py`（meta/run 校验、converge 全量收敛、lane 并发、rate gate、重试/退避、dead-letter、profile 轮转、stdlib 导入） |
| `rows_test.go` | 12 | 12 | 0 | `core/tests/test_playbook_tools.py`（rows 提取与尾随分号、错误详情、脚本三来源解析/持久化、queen 并发、批量与按 colony 停止） |

### 3.20 server

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `worker_filter_test.go` | 5 | 5 | 0 | `core/tests/test_sse_worker_filter.py`（queen 流直通、worker 流 LLM/工具事件静默、单数 `worker` 标签仍直通、生命周期事件直通、allowlist 常量存在） |
| `worker_list_test.go` | 1 | 1 | 0 | `core/tests/test_adaptive_tool_budget.py::test_workers_route_serializes_budget_telemetry`（worker 列表序列化器在 result 中输出 `tool_calls_used` / `budget_limited`，并对缺字段的旧结果回退零值） |
| `worker_handoff_test.go` | 4 | 4 | 0 | `core/tests/test_session_manager_worker_handoff.py`（worker 升级记录进 pending_escalations 并以 `[WORKER_ESCALATION]` 注入 queen；queen 流被忽略；queen 缺席时回落为 CLIENT_OUTPUT_DELTA + 单条 CLIENT_INPUT_REQUESTED；stop_session 退订 handoff） |
| `worker_read_test.go` | 1 | 1 | 0 | `core/tests/test_janitor_worker_deepclean.py::test_endpoints_render_after_deep_clean`（深清理后详情端点仍返回 historical 墓碑 status/task/result.summary，会话端点返回空 transcript） |
| `compaction_status_test.go` | 7 | 7 | 0 | `core/tests/test_compaction_sweep.py`（5 条 sweep 对等：缺失根 no-op、in_progress→failed、done/failed 不动、多 queen、无标记目录跳过）；`mark_*`/`get_status` 往返、500 字符截断、`await_completion` 轮询/超时/取消 2 条为 `Parity-exempt` |
| `session_titles_test.go` | 7 | 7 | 0 | `core/tests/test_session_titles.py`（`test_clean_title` 4 个子例、结束回合命名并广播、仅调用工具不命名、用户标题不被覆盖、标题路由设/清 meta 共 5 条对等）；Unicode 空白/行边界与命名失败重试 2 条为 `Parity-exempt` |
| `global_data_test.go` | 8 | 8 | 0 | `core/framework/server/tests/test_global_data_routes.py`（lead_id 优先 LinkedIn slug、email 回退；insert 派生 PK 且 mode="insert"；缺标识 400 且无后端写入；409 冲突信封带 attempted pk；changes 初始化省略 since、游标透传、未登录 401） |
| `session_dirs_test.go` | 14 | 14 | 0 | `core/framework/server/tests/test_session_dir_resolution.py`（8 条对等：从路径读 colony、DM 路径无 colony、生成器找 colony-only 会话、共享 id 产出全部目录且 DM 在前、重复 DM 目录下 resume 仍找到 colony、真实 DM 会话 resume 无 colony、无匹配无 colony、首匹配仍偏好 DM 树）；6 条 `Parity-exempt` 补强：尾分隔符路径规范化、符号链接树跟随、命中后坏目录不上抛、DM 命中即停、坏目录排在匹配前时错误上抛、`QueenDMSessionDir` 只接受 DM 会话 |
| `judge_test.go` | 4 | 4 | 0 | `core/framework/server/tests/test_judge.py`（3 条对等：配置判官收到 model/问题/state/criteria 并返回概率；无槽、无 key、未知协议均无答案且不调用协议；协议失败吞为无答案。上游经 `ChannelHost.judge` 驱动，channel host 尚未移植，Go 直接调用 `AskJudge`，契约相同）；1 条 `Parity-exempt`：用两台真实服务器固定 httpx `follow_redirects=False` + `raise_for_status()` 的 3xx 视为错误语义 |
| `stall_check_test.go` | 7 | 7 | 0 | `core/framework/server/tests/test_stall_check.py`（7 条全部对等：宣布工作无工具→恰一次提醒且 state 逐字段相等、判官认为无碍不提醒、可见错误的失败调用不判定、选项关闭不判定、工具调用后只看最后一次模型调用、纠正上一件作品时携带 `previous_turn`、经 `RegisterConfigRoutes` 驱动 judge 段保存跳过聊天 key 校验并逐字回读且 `stall_check` 开关即时启停判官槽） |
| `config_features_test.go` | 10 | 10 | 0 | `core/tests/test_routes_config_features.py`（开关 getter 的缺省/文件/环境变量优先级、GET 四项缺省、PUT 落盘与回显、无关键保留、三类 400 且不写盘、`email_senders` 同步环境变量、热应用遵守 colony metadata pin） |
| `provider_library_test.go` | 8 | 8 | 0 | `core/tests/test_routes_provider_library.py`（空库、逐字存取、缺名/缺 model/非对象 400、删除并剪空库、apply 到 worker 槽不热切换、apply 到 llm 槽热切换参数、未知名 404 与未知角色 400、保存不校验而 apply 校验失败不改槽） |
| `routes_config_test.go` | 17 | 17 | 0 | `core/tests/test_routes_config_features.py`（7 条）与 `core/tests/test_routes_provider_library.py`（8 条）全部经 `httptest` 服务重放：features 默认/落盘/回显/保留其他键/四类 400/环境变量同步/热应用 pin，provider 库空库/逐字/校验/删除剪空/apply worker 与 llm/404 与 400/health check；另有 2 条 `Parity-exempt`：四个写路由统一返回严格 `Invalid JSON body`（空体、坏 JSON、非对象、尾随垃圾），以及 25 个并发 PUT 下配置文件读改写串行化不丢写 |
| `routes_config_meta_test.go` | 19 | 19 | 0 | `core/framework/server/tests/test_api.py`（1 条对等：`test_get_models_uses_shared_model_catalogue`）；18 条 `Parity-exempt` 覆盖上游未单测的元信息路由：sentinel GET/PUT（缺省块、整段替换、非对象 500 且刷新失败不影响响应）、rate limits（声明缺省、按文档顺序合并覆盖并告警、非对象 limits 400）、profile（缺省/存储值、落盘并镜像 global memory、只替换 Identity 段、非对象 profile 500）、avatar（3 种坏上传、写入并按探测顺序读回、首扩展名优先）、external skills（默认空、路径解析、裁剪列表写回、坏列表 400、非可迭代存储值 500） |
| `llm_config_test.go` | 24 | 24 | 0 | `core/framework/server/tests/test_api.py`（2 条对等：`test_get_llm_config_exposes_subscription_defaults_from_presets`、`test_a_key_written_in_the_config_counts_as_configured`）；22 条 `Parity-exempt` 补齐上游未单测的三条写入分支：custom 端点（空白裁剪、清空订阅旗标、热切换参数、空 model 400）、订阅（preset 限额、旗标置位、`api_key_env_var` 删除、未知/不可哈希 id、缺 token、校验失败不落盘）、API key（目录限额、env var、陈旧 api_base 删除、缺 provider/model、未知模型、缺 key、校验失败、未知 provider 不可哈希、非对象 llm 段 500）与 GET 的 active/detected 过滤；本轮新增数值源拼写透传（配置 provider / API 数值经 `UseNumber` 与 `stringPointer` 保持 Python `str()` 拼写）、provider 别名跳过、无 `api_base` 订阅的陈旧键清理、仅布尔 `false` 拒绝写入、custom 变更中止不落盘、尾随垃圾读空 |
| `app_test.go` | 8 | 8 | 0 | `core/framework/server/tests/test_api.py`（2 条对等：`TestHealth::test_health`、`TestErrorMiddleware::test_unknown_api_route_falls_back_to_frontend`）；6 条 `Parity-exempt`：注入会话的 colony 计数、dist 静态资产与 index 回退、desktop token 401 与 queen-mail 豁免、API no-store、panic 转通用 500、`GET /api/startup-status` 载荷 |
| `boot_status_test.go` | 3 | 3 | 0 | `core/framework/server/boot_status.py`（3 条 `Parity-exempt`：初始状态、report→ready 切换、32 条历史上限；上游无单测文件，接口契约直接由模块实现固定） |
| `routes_sessions_test.go` | 6 | 6 | 0 | `core/framework/server/tests/test_api.py::TestSessionCRUD`（2 条对等：`test_list_sessions_empty`、`test_list_sessions_with_loaded`）；4 条 `Parity-exempt`：`_session_to_live_dict` 全字段、queen-only 缺省、已加载 colony 的默认 phase、`SessionManager._sessions` 插入序 |
| `routes_events_history_test.go` | 15 | 15 | 0 | `core/tests/test_events_history_page.py`（8 条）与 `core/tests/test_events_history_tail.py`（4 条 `test_truncate_*`）全部经 HTTP 重放；3 条 `Parity-exempt`：Python `int()` 查询参数语法、ASCII-only 空白行过滤、`json.dumps` 数值尺寸估计 |
| `routes_session_detail_test.go` | 9 | 9 | 0 | `core/framework/server/tests/test_api.py::TestSessionCRUD`（5 条对等：`test_get_session_found`、`test_get_session_not_found`、`test_stop_session`、`test_stop_session_not_found`、`test_session_stats`）；4 条 `Parity-exempt`：`is_loading` 202 分支、`get_cold_session_info` 磁盘投影、`entry_points` 顶层 next-fire/fire-stat 字段、`handle_list_triggers` 路由包装 |
| `routes_session_triggers_test.go` | 29 | 29 | 0 | `core/framework/server/tests/test_api.py::TestSessionCRUD`（3 条对等：`test_update_trigger_task`、`test_update_trigger_cron_restarts_active_timer`、`test_update_trigger_cron_rejects_invalid_expression`）与 `core/tests/test_missed_triggers.py`（5 条 `test_resolve_missed_*`）；21 条 `Parity-exempt`：创建校验/成功/ID 冲突后缀、坏 JSON、`run` 的 colony/queen 守卫与 forced 注入、activate/deactivate 计时器生命周期、webhook 注册与事件分发、无 webhook server 500、`resolve_missed` 路由包装、PATCH 合并、`EmitTriggerFired` 载荷、`PersistActiveTriggers`、计时器触发与火统计、`NextDueFrom`、`slugifyTriggerID`、webhook server 四种响应 |
| `routes_session_history_test.go` | 10 | 10 | 0 | `core/tests/test_session_titles.py::test_title_route_sets_and_clears_a_users_title`（1 条对等：设置去空白标题、空串清除返回 `null`、`ReadTitle` 读空）；9 条 `Parity-exempt`：非 DM/非字符串标题拒绝、写失败通用 500、history 冷/活 overlay 与 colony fork 排除、colony id 校验、colony 列表保留 fork、active-session 跳过空会话、删除并停止 live、reveal 建目录并调用启动器、删除拒绝符号链接 |
| `routes_session_fork_test.go` | 2 | 2 | 0 | `core/tests/test_session_fork.py`（2 条对等：fork 路由返回新会话且历史列表可见、未知会话 fork 被拒绝） |
| `routes_session_live_test.go` | 2 | 2 | 0 | 2 条 `Parity-exempt`：`handle_get_session_snapshot` 的活会话投影与未知会话 404、`handle_live_sessions_stream` 的 SSE 首帧快照；上游无 Python 测试，契约由模块实现固定 |
| `routes_session_attachment_test.go` | 6 | 6 | 0 | 6 条 `Parity-exempt`：`handle_upload_attachment` 的上传/回读、坏请求与重名后缀、按 Content-Type 强制扩展名、CSV 与 64 KiB 文本预览；`handle_session_attachment` 的路径穿越与未知名拒绝；`_colony_root_for_session_dir` 的 colony 根推导；上游无 Python 测试 |
| `routes_session_colony_test.go` | 18 | 18 | 0 | 18 条 `Parity-exempt`：colony 会话生命周期与 dismiss/pivot 路由；上游 `framework/server/routes_session_colony.py` 无 Python 测试，契约由模块实现固定 |
| `routes_session_report_test.go` | 4 | 4 | 0 | 4 条 `Parity-exempt`：会话目录缺失返回 404；marker 落盘、归档内容脱敏与 stats 信封；`queen_resume_from`/`queen_dir` 的存储 ID 解析；坏 JSON 体与未知 severity 回退 medium；上游 `_session_report.py` 无 Python 测试 |
| `routes_agents_test.go` | 9 | 9 | 0 | `core/tests/test_validate_agent_path.py::TestHTTPEndpointsRejectMaliciousPaths::test_check_agent_credentials_rejects_traversal`（1 条对等）；8 条 `Parity-exempt` 覆盖 `GET /api/discover` 的载荷字段与空组、软删除跳过、`DELETE /api/agents` 的软删元数据（ASCII 转义、键保留）、purge 与缺失目录回显、请求校验（缺参、穿越、框架代理 403）、`PATCH /api/agents/metadata` 的 icon 写回与 `ensure_ascii=False`、四类错误分支；上游这些路由无 Python 测试 |
| `routes_session_create_test.go` | 14 | 14 | 0 | 14 条 `Parity-exempt`：`POST /api/sessions` 普通创建与 fork-into-new-colony 分支——必填校验、路径与代理根校验、worker 配置生成、colony 元数据写盘、来源会话复制、queen 会话建立、响应信封与错误回滚；上游 `framework/server/routes_sessions.py` 的创建分支无 Python 测试 |
| `routes_session_live_tools_test.go` | 5 | 5 | 0 | 5 条 `Parity-exempt`：`GET /api/sessions/{id}/live_tools` 的活会话工具清单、冷会话磁盘投影、未知会话 404、空工具与序列化字段；上游无 Python 测试，契约由模块实现固定 |

### 3.21 maintenance（janitor / 保留策略）

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `tier1_test.go` | 7 | 7 | 0 | `core/tests/test_janitor_tier1.py`（debug 日志清理、活跃期保留、glob 边界、dry-run 记账） |
| `safety_test.go` | 12 | 12 | 0 | `core/tests/test_janitor_safety.py`（受保护会话、live 目录、fork 压缩中、session lock、idle 门禁、in-flight partials、age 上限） |
| `queen_hygiene_test.go` | 8 | 8 | 0 | `core/tests/test_janitor_queen_hygiene.py`（events 重写、残留文件、配额、message-index 重置、`.pruned.json`、dry-run 不落盘） |
| `worker_deepclean_test.go` | 8 | 8 | 0 | `core/tests/test_janitor_worker_deepclean.py`（tombstone-first、keep-set、stray 清理、message-index 协同删除、幂等、崩溃恢复、dry-run、archive 往返与失败保源） |
| `review_fixes_test.go` | 7 | 7 | 0 | `core/tests/test_janitor_review_fixes.py`（孤儿扫描保护当前头部与 tool_calls 参数、读错误 fail-closed、自有锁不短路活性重检、刷新器阻断运行中 resume、离线执行拒绝 live server marker、marker 生命周期、disposer 建失败不泄漏 2h 全局锁） |

### 3.22 mcpregistry

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `registry_test.go` | 53 | 53 | 0 | `core/tests/test_mcp_registry.py`（目录结构与初始化、local/registry 安装、transport 覆盖、版本校验、移除/启停、索引搜索、override 类型、索引刷新与陈旧判断、内置服务器退役） |
| `resolve_test.go` | 22 | 22 | 0 | `core/tests/test_mcp_registry.py`（include/exclude/tags/profile、`max_tools` 截断、版本 pin、harness 组合成、manifest→ServerConfig 映射、`load_agent_selection` 及其类型校验） |
| `health_test.go` | 9 | 9 | 0 | `core/tests/test_mcp_registry.py`（单服务器/批量健康检查、池化连接优先、安装 transport 优先、连接与列举失败、不支持 transport、未安装） |
| `version_test.go` | 2 | 2 | 0 | `core/tests/test_mcp_registry.py`（`_get_hive_version` 的 section-aware TOML 回退、缺失 TOML） |
| `cli_test.go` | 62 | 62 | 0 | `core/tests/test_mcp_registry_cli.py`（`hive mcp add/remove/enable/disable/list/search/show/mode`、`--as-cli`、`--json`/`--quiet` 输出形态、索引陈旧提示与刷新、交互式确认、错误与退出码、版本与 agent 选择） |
| `mcp2cli_test.go` | 12 | 12 | 0 | `core/tests/test_mcp_cli.py`（CLI 模式服务器不进工具列表、`CLIServers` 过滤、stdio 单命令行与 env/cwd、header 经环境变量传递、unix transport 拒绝、提示词块、`add --` 与 `run` 转发） |

### 3.23 ratelimit

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `rate_limiter_test.go` | 21 | 21 | 0 | `core/tests/test_social_rate_limiter.py`（20 条对等：建表、空库放行、部分记录、日/周限额、账户与平台隔离、最小间隔、记录与归一化、env 提升/钳制/非法回退/周钳制、日/周计数、未知动作、`min_delay` 值）；配置文件中间优先级与「钳 1 不钳天花板」1 条为 `Parity-exempt` |

### 3.24 cli

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `cli_test.go` | 7 | 7 | 0 | `core/tests/test_cli_entry_point.py`（`--help` 提及 `hive`、`serve` 子命令帮助含 host/port、根帮助含 serve/queen/colony/session/chat、`queen list` / `colony list` 帮助、未知命令非零退出）；Python 的 `sys.path` 引导（`TestConfigurePaths`）无 Go 对应，按文档化缺口记录 |
| `loader_test.go` | 7 | 7 | 0 | `core/tests/test_runner_cli_frontend.py`（前端构建失败保留命令错误文本与 stderr、清理 tsbuildinfo 缓存并在 Windows 使用 `npm.cmd`、`hive open` 先启动 gateway ping 再进入 serve）；另含 4 条会话列表 HTTP 用例 |
| `skill_test.go` | 38 | 38 | 0 | `core/tests/test_skill_cli_commands.py`（`init` 建文件/已存在/缺名、`validate` 合法/目录路径/非法/缺 license 告警、`doctor` 默认技能集与具名技能、`install` URL/注册表缺失/无参、`remove`、`search` 注册表不可用/有结果/无结果、`info` 本地/注册表回退/无处可寻、`--json` 五种输出、`test` 结构校验/调用 provider/eval 套件/JSON 输出/缺 API key 降级） |
| `cred_test.go` | 10 | 10 | 0 | `core/tests/test_hive_cred_cli.py`（`token` 过期先刷新、纯文本只输出值、刷新失败 `reauth_required` 退出 1、未知账号 `not_found` 退出 4、错误消息点名 credential name、已知凭据 ID 提示、控制台脚本注册、`browse` 不泄露密钥、`inspect` 只列 key 名与类型、本地收集账号解析） |
| `mcp_test.go` | 6 | 6 | 0 | `core/tests/test_mcp_registry_cli.py`（3 条对等：14 个子命令全量注册、`mcp list` 空表、`mcp update nonexistent` 退出 1）；3 条 `Parity-exempt`：根帮助列出 `mcp`、`--json`/`--` 参数整段透传、未知子命令退出 2 |
| `janitor_test.go` | 12 | 12 | 0 | 全部 `Parity-exempt`：上游 `framework/maintenance/cli.py` 无单测，逐条钉住 `run`/`status` 的 CLI 线（组帮助、缺子命令退出 2、dry-run 报告、非法 tier 两分支、空白容忍、活服务拒绝、tier1 放行、无记录 status、已存报告、`_print_report` 逐字节、manifest 渲染与坏行截断、`_human` 单位）；依赖 `core/tests/test_janitor_*.py` 覆盖的库层语义 |
| `debugger_test.go` | 8 | 8 | 0 | 全部 `Parity-exempt`：上游 `framework/debugger/cli.py` 无单测，逐条钉住 `cmd_debugger` 的分支（命令形态与注册顺序、日志目录缺失退出 0、无 `*.jsonl` 同分支、脚本缺失退出 1、七 flag 顺序透传与退出码、假值不转发、默认 `HIVE_HOME/llm_logs` 不转发、非法 `--port` 用法错误退出 2） |
| **合计** | **88** | **88** | **0** | **100%** |

### 3.25 llmjudge

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `judge_test.go` | 22 | 22 | 0 | `core/tests/test_llm_judge.py`（注入 provider 与参数、五种响应解析（纯 JSON、json/plain 代码块、空白、缺 explanation / passes 强制布尔）、四类错误处理、向后兼容的懒加载 Anthropic 客户端与模型选择、LiteLLM OpenAI 回退、多判官复用与外键模式） |

### 3.26 queen（身份档案）

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `profiles_test.go` | 10 | 10 | 0 | `core/framework/agents/queen/tests/test_queen_profiles_defaults.py`（13 个默认档案从 YAML 加载且 name/title 非空、capability 默认全空、`default_tools_for` 已知/未知、落盘只写 persona、未改默认的自愈等价性、未知 id 返回空、退役标题仅精确命中时替换、用户自选标题不动、无退役标题原样透传、每条退役标题都指向真实 queen 且当前标题不在退役集合） |

### 3.27 attachments

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `attachments_test.go` | 5 | 5 | 0 | `tools/src/aden_tools/utils/attachments.py`（5 条 `Parity-exempt`：basename 路径剥离与扩展名强制、空白折叠与 200 字符上限、重名 `-2`/`-3` 后缀、二进制与非法 UTF-8 拒绝、文本扩展名 MIME 允许清单；上游无 Python 测试，契约由模块实现固定） |

### 3.28 runtimehealth

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `runtime_health_test.go` | 3 | 3 | 0 | `framework/host/runtime_health.py`（3 条 `Parity-exempt`：网络错误标记分类、上游健康状态生命周期、降级原因 240 字符截断；上游无 Python 测试，契约由模块实现固定） |

### 3.29 sessionreport（凭据脱敏诊断包）

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `sessionreport_test.go` | 8 | 8 | 0 | `framework/server/_session_report.py`（8 条 `Parity-exempt`：八类凭据正则脱敏并保留调试上下文、marker 字段缺省与覆盖、归档保留目录树且跳过 `data/reports`、非法 UTF-8 替换为 U+FFFD、severity 精确集合、超大二进制按预算省略并记入 `omitted_paths`、marker 的 `ensure_ascii` 转义与保留 HTML、SHA-256 已知向量；上游无 Python 测试，契约由模块实现固定） |

### 3.30 agents（代理发现与路径校验）

| Go 测试文件 | tests | annotated | missing | Python 参考 |
| --- | --- | --- | --- | --- |
| `discovery_test.go` | 14 | 14 | 0 | `framework/agents/discovery.py`（14 条 `Parity-exempt`：colony 分组与元数据、worker 配置解析、软删除跳过、缺失基目录、目录名 title 回退与 birthtime、`len()` 怪癖、worker+queen 会话计数、`session_` 前缀与递归 `runs.jsonl`、`state.json` 时间戳词序最大值、mtime 分支格式、排序与排除 stem、仅常规 json 文件构成 colony、工具集合语义、`str.title()` 词边界、`WorkerEntry.to_dict`、坏行按文件中止与 `splitlines`/`strip` 怪癖；上游无 Python 测试） |
| `validate_test.go` | 20 | 20 | 0 | `core/tests/test_validate_agent_path.py`（20 条对等：允许根的四元组/解析/相对仓库锚定/缓存、exports 与 examples 及 `$HIVE_HOME/agents` 内路径通过、返回 Path 对象、越界绝对路径与穿越、兄弟目录同名、空串、家目录、根目录、NUL 字节、符号链接逃逸、根自身、`~` 展开、`AllowedRoots` 不可变元组语义） |

## 4. 注解质量审计

### 4.1 曾指向不存在文件的注解（已修复）

| 注解位置 | 旧写法 | 现状 |
| --- | --- | --- |
| `internal/atomicfs/atomicfs_test.go` | `core/framework/utils/tests/test_io.py`（不存在） | 改为 `core/tests/test_skill_trust.py::TestTrustedRepoStore::test_atomic_write` 与 `core/tests/test_skill_registry.py::TestCacheAtomicWrite::test_atomic_write_uses_tmp_then_replace`，并注明 64 writer 并发是 Go-only 回归守卫 |
| `internal/session/session_test.go` | `core/tests/test_session_store.py`（不存在） | 改为 `core/tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_parent_directory_traversal`，并指向真实的对话/持久化用例 |

存在性脚本只检查「有没有 `Parity:` 字样」，不解析路径，所以这类问题必须靠第二道门禁与人工审计发现。当前 `check_parity_targets.sh` 报告 `0 missing file / 0 missing test`。

### 4.2 缺文件路径的弱注解（已清零）

| 包 | 历史弱注解数 | 典型写法 | 现状 |
| --- | --- | --- | --- |
| `credentials` | 19 | `// Parity: TestCredentialStore::test_caching` | 0 |
| `memory` | 22 | `// Parity: ::test_scan_memory_files` | 0（补 `core/tests/test_queen_memory.py` 路径） |
| `tracker` | 20 | `// Parity: test_upsert_insert_then_update` | 0（补 `core/tests/test_tracker_*.py` 路径） |
| `session` | 2 | `// Parity: state.json is the single source of truth...` | 0（改指真实持久化/对话用例） |
| `atomicfs` | 1 | `// Parity: atomic_write preserves permissions...` | 0（改指两条上游 atomic write 用例） |
| **合计** | **44**（清扫末轮实测；文档旧口径为 64，tracker 的 20 条此前已补齐） | — | **0** |

当前 2,215 个测试中，1,838 个的注释块仅含带 `.py` 路径的 `Parity:` 行（另有 1 条同时含 `Parity-exempt` 说明），376 个仅有 `Parity-exempt:` 说明（部分正文提到 `.py` 文件名，但那是散文而非可解析引用）；没有「有 `Parity:` 但无 `.py` 路径」的块。

注：本节是 2026-11-18 19:25 的快照。W3 agents 与 W4 create-session 两条并发车道已合并：`internal/agents`（34 条）与新增服务端路由测试（`routes_agents_test.go` 12 条、`routes_session_create_test.go` 14 条、`routes_session_live_tools_test.go` 5 条）均已纳入上表与总计。

### 4.3 引用可回溯性

`bash go_hive/scripts/check_parity_targets.sh go-hive /Users/mac/hive/core` 实测：

```
parity refs checked: 2035  missing files: 0  missing tests: 0  shorthand/prose: 46
check_parity_targets: OK
```

- 2,035 条 `path::test` 引用全部命中真实文件与真实 `def`；
- 46 条 shorthand/prose 是类名锚点、跨语言向量说明等，需要人工确认语义；
- 存在性不等于语义等价：脚本只能证明引用存在，断言是否真的对等仍需评审。

### 4.4 其他盲区

1. 脚本按 `func Test` 计数；表驱动测试内部的 `t.Run` 子用例不单独计数，这是有意的（parity 以顶层测试为单位）。
2. `Parity-exempt:` 只检查前缀，不检查理由是否充分；评审需要看正文。
3. 脚本扫描全部 `*_test.go`，包括带 `//go:build` 约束的文件；当前没有这类测试文件，若将来新增需要确认是否应纳入统计。
4. 零测试文件（eventbus 的 `events_history_page_test.go` / `events_history_tail_test.go`）只有文件级注解，把页面分页 8 条与 tail 9 条用例显式移交服务端车道；它们在总览中计为 0/0/0。页面分页 8 条与 4 条 `test_truncate_*` 已在 `internal/server/routes_events_history_test.go` 落地，tail 9 条仍是已知缺口。

## 5. 复核方式

```bash
bash go_hive/scripts/check_parity.sh                                  # 存在性：2215/2215/0
bash go_hive/scripts/check_parity_targets.sh go-hive /Users/mac/hive/core   # 可回溯性：2035 refs, 0 missing
```

两条命令都应退出 0。若两者不一致，以脚本输出为准并更新本文。

## 6. 维护流程

1. 新增或修改测试后，先写注解，再跑 `bash go_hive/scripts/check_parity.sh`。
2. 注解格式固定为 `// Parity: <相对 core/ 的 .py 路径>::<测试函数>`；多用例逐行列出，续行可以省略路径但**首行必须带路径**。
3. 上游确实没有对应行为时，用 `// Parity-exempt: <理由>`，理由要能被评审复核。
4. 本文的表格是快照。**不要让数字领先于脚本**：如果脚本与本文不一致，以脚本为准，并更新本文。
5. 本车道只修改 `*_test.go`；实现缺陷由实现车道修复。

## 7. 快照声明

- 生成依据：`/Users/mac/hive/go_hive/go-hive` 合并主树在 2026-11-18 19:14 的文件内容。
- 上游参照：`/Users/mac/hive/core`（提交 `c6c2cdcf`）与 `/Users/mac/hive/tools`。
- 并行开发会持续新增测试与包；当 `go_hive/go-hive/internal` 出现新的 `*_test.go` 时，本表随即过时。
- 权威数据源始终是 `check_parity.sh` 与 `check_parity_targets.sh` 的实时输出。
