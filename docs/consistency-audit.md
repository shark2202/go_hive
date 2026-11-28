# hive ↔ go_hive 逐项一致性核查表

> 生成：2026-11-22 21:45  · HEAD `80c82a08` · 核查快照 = wave-71（`3bea069f`，ci=0 / govuln=0 / qc=0，之后仅文档提交）
> 数据源：`contracts/module-inventory.json`（C12）、`hive-concepts-inventory.json`、`integrations-evidence-audit.json`、`contracts/{api-routes,env-vars,config-keys,tool-definitions,builtin-skills}.json`。

## 1. 一级原语（18，按 runtime_spine 8 层）

| 原语 | 状态 | 核查证据 |
|---|---|---|
| Loop | ✅ | agentloop 执行核：回合/工具/超时/judge/coercion；5,816 测试；SSE 42/42 |
| Colony | ✅ | colony runtime：spawn/kill/report/escalation；崩溃恢复 7/7 |
| Queen | ✅+登记 | prompt 21,584B 逐字；13 人格；模型槽；标题 LLM 槽；pin 语义登记 |
| Worker | ✅ | worker.json 工具面 / 升级 / stall→failed / 转录 resume |
| Plan | ✅ | loop 内规划；长任务计划（chat-app）修复 |
| Tracker | ✅ | schema v3 两侧；C5 PASS；queen DDL/worker 门控 |
| EventBus | ✅+登记 | 77/77 事件；SSE 42/42；events.jsonl append-only；异步派发登记 |
| ReminderHub | ✅ | 15 source 矩阵；reminders-coverage §7 残余登记 |
| Skill | ✅ | 25 内置文件逐字节；双 scope；端点实测 |
| Playbook | ✅ | Python .play.py 120/120；真实 LLM E2E 19/19；900s 守护登记 |
| Trigger | ✅ | timer 真机注入；triggers.json 双跑 0 差异 |
| Sentinel | ✅ 运行时+🔧工具 | 三通道/停车/索引；sentinel_setup 工具已移植（真实投递未验证） |
| Memory | ✅+登记 | v2 + notes 全族；recall 12.7ms；JSON 索引 vs SQLite 登记 |
| Soul | ✅ | 每日反思；反向依赖消除 |
| Tools | ✅+登记 | desc 47/47；schema raw 25/47；输出 golden 12 例外 |
| MCP | ✅+登记 | 协议面双跑；D3 别名层；真实外部 server 未验证 |
| Credentials | ✅+登记 | store/keys/health/cli/check-agent；oauth2 4 模块 + aden/client 未移植 |
| Budgets | ✅ | grace/hard/lifetime 门限（budget_cap 测试） |

## 2. core/framework 模块（C12 逐文件，273）

### ported（220）

| 模块 | Go counterpart |
|---|---|
| `core/framework/__main__.py` | `cmd/hive/main.go` |
| `core/framework/agent_loop/active_workers_reminder.py` | `internal/agentloop/active_workers_reminder.go` |
| `core/framework/agent_loop/colony_parallel_nudge.py` | `internal/agentloop/colony_parallel_nudge.go` |
| `core/framework/agent_loop/colony_worker_snapshot_reminder.py` | `internal/agentloop/colony_worker_snapshot_reminder.go` |
| `core/framework/agent_loop/conversation.py` | `internal/llm/llm.go` |
| `core/framework/agent_loop/idle_nudge.py` | `internal/agentloop/idle_nudge.go` |
| `core/framework/agent_loop/internals/compaction.py` | `internal/agentloop/compaction.go` |
| `core/framework/agent_loop/internals/credential_tool.py` | `internal/server/chat_credential_park.go` |
| `core/framework/agent_loop/internals/cursor_persistence.py` | `internal/server/chat_park.go` |
| `core/framework/agent_loop/internals/event_publishing.py` | `internal/server/chat_turn_emitter.go` |
| `core/framework/agent_loop/internals/sentinel_tool.py` | `internal/sentinel/sentinel_tool.go` |
| `core/framework/agent_loop/internals/synthetic_tools.py` | `internal/agentloop/synthetic_tools.go` |
| `core/framework/agent_loop/internals/tool_input_coercer.py` | `internal/agentloop/tool_coercion_wire.go` |
| `core/framework/agent_loop/internals/tool_result_handler.py` | `internal/agentloop/judge.go` |
| `core/framework/agent_loop/internals/types.py` | `internal/agentloop/types.go` |
| `core/framework/agent_loop/internals/vision_fallback.py` | `internal/visionfallback/visionfallback.go` |
| `core/framework/agent_loop/memory_reminders.py` | `internal/agentloop/memory_reminders.go` |
| `core/framework/agent_loop/past_conversations_reminder.py` | `internal/server/chat_memory_wiring.go` |
| `core/framework/agent_loop/prompting.py` | `internal/agentloop/prompting.go` |
| `core/framework/agent_loop/reminders.py` | `internal/agentloop/reminders.go` |
| `core/framework/agent_loop/stream_stall.py` | `internal/agentloop/stream_stall.go` |
| `core/framework/agent_loop/tool_skill_reminders.py` | `internal/agentloop/tool_skill_reminders.go` |
| `core/framework/agent_loop/tracker_snapshot_reminder.py` | `internal/agentloop/tracker_snapshot_reminder.go` |
| `core/framework/agent_loop/turn_context_reminder.py` | `internal/agentloop/turn_context_reminder.go` |
| `core/framework/agent_loop/types.py` | `internal/agentloop/reminders.go` |
| `core/framework/agent_loop/visible_error.py` | `internal/agentloop/visible_error.go` |
| `core/framework/agents/discovery.py` | `internal/agents/discovery.go` |
| `core/framework/agents/queen/memory_log.py` | `internal/memory/memory_log.go` |
| `core/framework/agents/queen/nodes/__init__.py` | `internal/server/queen_lifecycle_tools.go` |
| `core/framework/agents/queen/queen_memory_v2.py` | `internal/memory/memory.go` |
| `core/framework/agents/queen/queen_profiles.py` | `internal/server/routes_queens.go` |
| `core/framework/agents/queen/queen_tools_config.py` | `internal/server/routes_queens_tools.go` |
| `core/framework/agents/queen/queen_tools_defaults.py` | `internal/tools/category_additions.go` |
| `core/framework/agents/queen/recall_selector.py` | `internal/memory/recall.go` |
| `core/framework/agents/queen/reflection_agent.py` | `internal/memory/reflection.go` |
| `core/framework/agents/queen/soul.py` | `internal/server/chat_runtime_colony.go` |
| `core/framework/agents/queen/soul_probes.py` | `internal/soul/probes.go` |
| `core/framework/agents/queen/soul_reflection.py` | `internal/soul/reflection.go` |
| `core/framework/agents/queen/timeline.py` | `internal/server/chat_memory_wiring.go` |
| `core/framework/agents/queen/tools_ga_migration.py` | `internal/server/tools_ga_migration.go` |
| `core/framework/agents/queen/worker_definition.py` | `internal/colony/worker_definition.go` |
| `core/framework/cli.py` | `internal/cli/cli.go` |
| `core/framework/config.py` | `internal/server/config_slots.go` |
| `core/framework/credentials/aden/provider.py` | `internal/credentials/provider.go` |
| `core/framework/credentials/aden/storage.py` | `internal/credentials/storage.go` |
| `core/framework/credentials/catalog.py` | `internal/credentials/catalog.go` |
| `core/framework/credentials/cli.py` | `internal/cli/cred.go` |
| `core/framework/credentials/key_storage.py` | `internal/credentials/keys.go` |
| `core/framework/credentials/local/models.py` | `internal/credentials/models.go` |
| `core/framework/credentials/local/registry.py` | `internal/credentials/local.go` |
| `core/framework/credentials/models.py` | `internal/credentials/models.go` |
| `core/framework/credentials/oauth2/provider.py` | `internal/credentials/provider.go` |
| `core/framework/credentials/provider.py` | `internal/credentials/provider.go` |
| `core/framework/credentials/resolve.py` | `internal/credentials/resolve.go` |
| `core/framework/credentials/storage.py` | `internal/credentials/storage.go` |
| `core/framework/credentials/store.py` | `internal/credentials/store.go` |
| `core/framework/credentials/template.py` | `internal/credentials/template.go` |
| `core/framework/debugger/cli.py` | `internal/cli/debugger.go` |
| `core/framework/global_db/client.py` | `internal/globaldb/client.go` |
| `core/framework/global_db/count_cache.py` | `internal/globaldb/count_cache.go` |
| `core/framework/global_db/grid_query.py` | `internal/globaldb/grid_query.go` |
| `core/framework/host/colony_binding.py` | `internal/tracker/binding.go` |
| `core/framework/host/colony_db.py` | `internal/colony/colony_db.go` |
| `core/framework/host/colony_metadata.py` | `internal/server/routes_colonies.go` |
| `core/framework/host/colony_runtime.py` | `internal/colony/runtime.go` |
| `core/framework/host/colony_tools_config.py` | `internal/server/e2e_compat_exports.go` |
| `core/framework/host/event_bus.py` | `internal/eventbus/bus.go` |
| `core/framework/host/event_log.py` | `internal/eventbus/event_log.go` |
| `core/framework/host/events_policy.py` | `internal/eventbus/events_policy.go` |
| `core/framework/host/outcome_aggregator.py` | `internal/colony/goal_outcome.go` |
| `core/framework/host/playbook/runner.py` | `internal/playbook/runner.go` |
| `core/framework/host/runtime_health.py` | `internal/runtimehealth/runtime_health.go` |
| `core/framework/host/runtime_resources.py` | `internal/resources/mem_darwin.go` |
| `core/framework/host/tracker_db.py` | `internal/tracker/tracker.go` |
| `core/framework/host/triggers.py` | `internal/triggers/triggers.go` |
| `core/framework/host/webhook_server.py` | `internal/server/webhook_server.go` |
| `core/framework/host/worker.py` | `internal/colony/worker.go` |
| `core/framework/host/worker_profiles.py` | `internal/server/routes_colonies_profiles.go` |
| `core/framework/llm/anthropic.py` | `internal/llm/anthropic.go` |
| `core/framework/llm/antigravity.py` | `internal/llm/antigravity.go` |
| `core/framework/llm/capabilities.py` | `internal/llm/capabilities.go` |
| `core/framework/llm/endpoints.py` | `internal/llm/endpoints.go` |
| `core/framework/llm/key_check.py` | `internal/llm/keycheck.go` |
| `core/framework/llm/key_pool.py` | `internal/llm/keypool.go` |
| `core/framework/llm/litellm.py` | `internal/llm/stream_openai.go` |
| `core/framework/llm/mock.py` | `internal/llm/mock.go` |
| `core/framework/llm/model_catalog.py` | `internal/llm/catalog.go` |
| `core/framework/llm/provider.py` | `internal/agentloop/loop.go` |
| `core/framework/llm/stream_events.py` | `internal/llm/stream_events.go` |
| `core/framework/loader/agent_loader.py` | `internal/agents/agent_spec.go` |
| `core/framework/loader/cli.py` | `internal/cli/loader.go` |
| `core/framework/loader/mcp_cli.py` | `internal/cli/loader.go` |
| `core/framework/loader/mcp_client.py` | `internal/tools/mcp_client.go` |
| `core/framework/loader/mcp_connection_manager.py` | `internal/tools/mcp_connection_manager.go` |
| `core/framework/loader/mcp_errors.py` | `internal/mcpregistry/errors.go` |
| `core/framework/loader/mcp_registry.py` | `internal/tools/mcp_registry.go` |
| `core/framework/loader/mcp_registry_cli.py` | `internal/cli/cli.go` |
| `core/framework/loader/tool_registry.py` | `internal/cli/loader_gate_snapshot.go` |
| `core/framework/maintenance/archive.py` | `internal/maintenance/archive.go` |
| `core/framework/maintenance/cli.py` | `internal/cli/janitor.go` |
| `core/framework/maintenance/janitor.py` | `internal/maintenance/janitor.go` |
| `core/framework/maintenance/retention.py` | `internal/maintenance/retention.go` |
| `core/framework/observability/logging.py` | `internal/logging/logging.go` |
| `core/framework/orchestrator/files.py` | `internal/pipeline/stage_mcp_registry.go` |
| `core/framework/orchestrator/prompting.py` | `internal/queen/static_prompt.go` |
| `core/framework/pipeline/registry.py` | `internal/pipeline/registry.go` |
| `core/framework/pipeline/runner.py` | `internal/pipeline/runner.go` |
| `core/framework/pipeline/stage.py` | `internal/pipeline/stage.go` |
| `core/framework/pipeline/stages/cost_guard.py` | `internal/pipeline/stage_cost_guard.go` |
| `core/framework/pipeline/stages/credential_resolver.py` | `internal/pipeline/stage_credential_resolver.go` |
| `core/framework/pipeline/stages/input_validation.py` | `internal/pipeline/stage_input_validation.go` |
| `core/framework/pipeline/stages/llm_provider.py` | `internal/pipeline/stage_llm_provider.go` |
| `core/framework/pipeline/stages/mcp_registry.py` | `internal/pipeline/stage_mcp_registry.go` |
| `core/framework/pipeline/stages/rate_limit.py` | `internal/pipeline/stage_rate_limit.go` |
| `core/framework/pipeline/stages/skill_registry.py` | `internal/pipeline/stage_skill_registry.go` |
| `core/framework/rate_limiter.py` | `internal/ratelimit/rate_limiter.go` |
| `core/framework/schemas/agent_config.py` | `internal/schemas/agent_config.go` |
| `core/framework/schemas/checkpoint.py` | `internal/schemas/checkpoint.go` |
| `core/framework/schemas/decision.py` | `internal/schemas/decision.go` |
| `core/framework/schemas/run.py` | `internal/schemas/run.go` |
| `core/framework/schemas/session_state.py` | `internal/schemas/session_state.go` |
| `core/framework/sentinel/classifier.py` | `internal/sentinel/classifier.go` |
| `core/framework/sentinel/escalation_source.py` | `internal/sentinel/escalation_source.go` |
| `core/framework/sentinel/listeners.py` | `internal/sentinel/listeners.go` |
| `core/framework/sentinel/manager.py` | `internal/sentinel/manager.go` |
| `core/framework/sentinel/notifier.py` | `internal/sentinel/notifier.go` |
| `core/framework/sentinel/runtime_identity.py` | `internal/sentinel/runtime_identity.go` |
| `core/framework/sentinel/store.py` | `internal/sentinel/store.go` |
| `core/framework/sentinel/token.py` | `internal/sentinel/token.go` |
| `core/framework/server/_session_report.py` | `internal/sessionreport/session_report.go` |
| `core/framework/server/app.py` | `internal/server/app.go` |
| `core/framework/server/boot_status.py` | `internal/server/boot_status.go` |
| `core/framework/server/channels.py` | `internal/server/adapters_qmail.go` |
| `core/framework/server/claude_code_queens.py` | `internal/server/claude_code_queens.go` |
| `core/framework/server/compaction_status.py` | `internal/server/compaction_status.go` |
| `core/framework/server/judge.py` | `internal/server/judge.go` |
| `core/framework/server/queen_mail.py` | `internal/server/adapters_qmail.go` |
| `core/framework/server/queen_mail_mcp.py` | `internal/queenmail/mcp_stdio.go` |
| `core/framework/server/queen_models.py` | `internal/cli/loader_model_slots.go` |
| `core/framework/server/queen_orchestrator.py` | `internal/server/queen_orchestrator.go` |
| `core/framework/server/routes_claude_code_queens.py` | `internal/server/routes_queens_claude_code.go` |
| `core/framework/server/routes_colonies.py` | `internal/server/routes_colonies.go` |
| `core/framework/server/routes_colony_tools.py` | `internal/server/routes_colony_tools.go` |
| `core/framework/server/routes_colony_workers.py` | `internal/server/routes_colony_workers.go` |
| `core/framework/server/routes_config.py` | `internal/server/routes_config.go` |
| `core/framework/server/routes_credentials.py` | `internal/server/routes_credentials.go` |
| `core/framework/server/routes_events.py` | `internal/server/routes_events.go` |
| `core/framework/server/routes_execution.py` | `internal/server/routes_execution.go` |
| `core/framework/server/routes_logs.py` | `internal/server/routes_runtime_logs.go` |
| `core/framework/server/routes_maintenance.py` | `internal/server/routes_maintenance.go` |
| `core/framework/server/routes_mcp.py` | `internal/server/routes_mcp.go` |
| `core/framework/server/routes_memories.py` | `internal/server/routes_memories_extra.go` |
| `core/framework/server/routes_messages.py` | `internal/server/routes_messages_extra.go` |
| `core/framework/server/routes_models.py` | `internal/server/routes_models.go` |
| `core/framework/server/routes_prompts.py` | `internal/server/routes_prompts_extra.go` |
| `core/framework/server/routes_queen_tools.py` | `internal/server/routes_queens_tools.go` |
| `core/framework/server/routes_queens.py` | `internal/server/routes_queens.go` |
| `core/framework/server/routes_sentinel.py` | `internal/server/routes_sentinel.go` |
| `core/framework/server/routes_sessions.py` | `internal/server/routes_sessions.go` |
| `core/framework/server/routes_skills.py` | `internal/server/routes_skills.go` |
| `core/framework/server/routes_soul.py` | `internal/server/routes_credentials_soul.go` |
| `core/framework/server/routes_tasks.py` | `internal/server/routes_tasks.go` |
| `core/framework/server/routes_workers.py` | `internal/server/routes_workers.go` |
| `core/framework/server/session_fork.py` | `internal/session/fork.go` |
| `core/framework/server/session_manager.py` | `internal/server/session.go` |
| `core/framework/server/session_titles.py` | `internal/server/session_titles.go` |
| `core/framework/server/sse.py` | `internal/server/routes_events.go` |
| `core/framework/server/stall_check.py` | `internal/server/stall_check.go` |
| `core/framework/server/turn_context.py` | `internal/server/turn_context.go` |
| `core/framework/skills/catalog.py` | `internal/skills/catalog.go` |
| `core/framework/skills/cli.py` | `internal/cli/skill.go` |
| `core/framework/skills/config.py` | `internal/skills/config.go` |
| `core/framework/skills/defaults.py` | `internal/skills/defaults.go` |
| `core/framework/skills/discovery.py` | `internal/skills/discovery.go` |
| `core/framework/skills/install_requests.py` | `internal/skills/install_requests.go` |
| `core/framework/skills/installer.py` | `internal/skills/installer.go` |
| `core/framework/skills/live_reload.py` | `internal/cli/loader_reminders.go` |
| `core/framework/skills/manager.py` | `internal/skills/manager.go` |
| `core/framework/skills/models.py` | `internal/skills/models.go` |
| `core/framework/skills/overrides.py` | `internal/skills/overrides.go` |
| `core/framework/skills/parser.py` | `internal/skills/parser.go` |
| `core/framework/skills/registry.py` | `internal/skills/registry.go` |
| `core/framework/skills/skill_errors.py` | `internal/skills/errors.go` |
| `core/framework/skills/skill_writer.py` | `internal/skills/skill_writer.go` |
| `core/framework/skills/tool_gating.py` | `internal/skills/tool_gating.go` |
| `core/framework/skills/trust.py` | `internal/skills/trust.go` |
| `core/framework/skills/validator.py` | `internal/skills/validator.go` |
| `core/framework/storage/checkpoint_store.py` | `internal/storage/checkpoint_store.go` |
| `core/framework/storage/concurrent.py` | `internal/storage/concurrent.go` |
| `core/framework/storage/conversation_store.py` | `internal/colony/worker_transcript.go` |
| `core/framework/storage/migrate_colony_sessions.py` | `internal/storage/migrate_colony_sessions.go` |
| `core/framework/storage/migrate_v2.py` | `internal/storage/migrate_v2.go` |
| `core/framework/storage/migrate_v3.py` | `internal/storage/migrate_v3.go` |
| `core/framework/storage/session_store.py` | `internal/storage/session_store.go` |
| `core/framework/storage/session_summary.py` | `internal/storage/session_summary.go` |
| `core/framework/tasks/events.py` | `internal/tasks/events.go` |
| `core/framework/tasks/hooks.py` | `internal/tasks/hooks.go` |
| `core/framework/tasks/models.py` | `internal/tasks/models.go` |
| `core/framework/tasks/reminders.py` | `internal/tasks/reminders.go` |
| `core/framework/tasks/scoping.py` | `internal/tasks/scoping.go` |
| `core/framework/tasks/store.py` | `internal/tasks/store.go` |
| `core/framework/tasks/tools/_context.py` | `internal/tasks/context.go` |
| `core/framework/tasks/tools/colony_tools.py` | `internal/tasks/colony_tools.go` |
| `core/framework/tasks/tools/names.py` | `internal/tasks/names.go` |
| `core/framework/tasks/tools/session_tools.py` | `internal/tasks/session_tools.go` |
| `core/framework/testing/llm_judge.py` | `internal/llmjudge/judge.go` |
| `core/framework/tools/browser_tools.py` | `internal/tools/integrations/browser_setup.go` |
| `core/framework/tools/harness_tools.py` | `internal/tools/mcp.go` |
| `core/framework/tools/playbook_tools.py` | `internal/colony/playbook_tools.go` |
| `core/framework/tools/queen_lifecycle_tools.py` | `internal/server/trigger_runtime.go` |
| `core/framework/tools/queen_mail_tools.py` | `internal/cli/session_tools_queen_mail.go` |
| `core/framework/tools/tool_tiers.py` | `internal/agentloop/search_tools.go` |
| `core/framework/tools/tracker_tools.py` | `internal/tracker/tools.go` |
| `core/framework/tools/worker_monitoring_tools.py` | `internal/server/queen_worker_monitoring.go` |
| `core/framework/tracker/llm_debug_logger.py` | `internal/agentloop/llm_debug_log.go` |
| `core/framework/tracker/runtime_log_schemas.py` | `internal/runtimelogs/runtime_log_schemas.go` |
| `core/framework/tracker/runtime_log_store.py` | `internal/runtimelogs/runtime_log_store.go` |
| `core/framework/tracker/runtime_logger.py` | `internal/runtimelogs/runtime_logger.go` |
| `core/framework/trackerdb/errors.py` | `internal/tracker/errors.go` |
| `core/framework/utils/io.py` | `internal/atomicfs/atomicfs.go` |

### partial（27）

| 模块 | Go counterpart |
|---|---|
| `core/framework/__init__.py` | `—` |
| `core/framework/agent_loop/__init__.py` | `—` |
| `core/framework/agent_loop/agent_loop.py` | `internal/agentloop/loop.go` |
| `core/framework/agent_loop/internals/judge_pipeline.py` | `internal/agentloop/judge.go` |
| `core/framework/agent_loop/internals/stall_detector.py` | `internal/agentloop/doom_loop.go` |
| `core/framework/agents/__init__.py` | `—` |
| `core/framework/agents/queen/__init__.py` | `—` |
| `core/framework/agents/queen/agent.py` | `internal/agentloop/loop_config.go` |
| `core/framework/agents/queen/config.py` | `—` |
| `core/framework/cloud_sync_hooks.py` | `internal/server/routes_queens.go` |
| `core/framework/credentials/__init__.py` | `—` |
| `core/framework/credentials/setup.py` | `internal/credentials/agent_nodes.go` |
| `core/framework/credentials/validation.py` | `internal/credentials/agent_validation.go` |
| `core/framework/host/__init__.py` | `—` |
| `core/framework/host/execution_manager.py` | `internal/server/chat_turn_emitter.go` |
| `core/framework/host/isolation.py` | `internal/schemas/agent_config.go` |
| `core/framework/llm/__init__.py` | `—` |
| `core/framework/orchestrator/__init__.py` | `—` |
| `core/framework/orchestrator/conversation_judge.py` | `internal/agentloop/llm_judge.go` |
| `core/framework/orchestrator/goal.py` | `internal/schemas/goal.go` |
| `core/framework/orchestrator/node.py` | `internal/agentloop/message.go` |
| `core/framework/orchestrator/safe_eval.py` | `internal/orchestrator/safe_eval.go` |
| `core/framework/orchestrator/validator.py` | `internal/orchestrator/validator.go` |
| `core/framework/schemas/goal.py` | `internal/schemas/goal.go` |
| `core/framework/tasks/tools/register.py` | `internal/tasks/register.go` |
| `core/framework/tools/queen_lifecycle/context.py` | `—` |
| `core/framework/utils/text.py` | `internal/server/routes_colonies.go` |

### out_of_scope（9）

| 模块 | Go counterpart |
|---|---|
| `core/framework/host/agent_host.py` | `—` |
| `core/framework/host/shared_state.py` | `—` |
| `core/framework/loader/preload_validation.py` | `—` |
| `core/framework/orchestrator/checkpoint_config.py` | `—` |
| `core/framework/orchestrator/context.py` | `—` |
| `core/framework/orchestrator/edge.py` | `—` |
| `core/framework/orchestrator/node_worker.py` | `—` |
| `core/framework/orchestrator/orchestrator.py` | `—` |
| `core/framework/server/streamtoken_refresh.py` | `—` |

### unported（17）

| 模块 | Go counterpart |
|---|---|
| `core/framework/credentials/aden/client.py` | `—` |
| `core/framework/credentials/oauth2/base_provider.py` | `—` |
| `core/framework/credentials/oauth2/hubspot_provider.py` | `—` |
| `core/framework/credentials/oauth2/lifecycle.py` | `—` |
| `core/framework/credentials/oauth2/zoho_provider.py` | `—` |
| `core/framework/global_db/cli.py` | `—` |
| `core/framework/host/stream_runtime.py` | `—` |
| `core/framework/testing/approval_cli.py` | `—` |
| `core/framework/testing/approval_types.py` | `—` |
| `core/framework/testing/categorizer.py` | `—` |
| `core/framework/testing/debug_tool.py` | `—` |
| `core/framework/testing/test_case.py` | `—` |
| `core/framework/testing/test_result.py` | `—` |
| `core/framework/testing/test_storage.py` | `—` |
| `core/framework/tracker/decision_tracker.py` | `—` |
| `core/framework/utils/python_env.py` | `—` |
| `core/framework/utils/task_registry.py` | `—` |

## 3. tools/src/aden_tools 集成审计（逐模块分级）

- 行数 104 · actions 864（impl 716 / spec 150）
- 上游 103 模块 / 865 actions；Go 102 模块 / 854 actions + senders 9 = 863（唯一缺口 csv_sql/excel_sql，DuckDB/cgo 有意排除）
- 分级：A 12 / B 91 / C 0 / D 1 / E 0；B = 实现完整但无真实第三方凭据（按决策标未验证）
- 逐模块明细：`docs/integrations-evidence-audit.md`（JSON/HTML 同源）

## 4. 契约表面（逐层计数）

| 表面 | 计数 | 参考文档 |
|---|---|---|
| API 路由 | 参考 222 / Go 225 | `docs/api-contract-diff.md`、C6 |
| Env 变量 | Python 90 / Go 125 / 共有 84 / py-only 6 / go-only 41 | `docs/env-config-contract-diff.md`、C1/C2 |
| configuration.json | 137 键 / 22 sections（{'aligned': 131, 'go_only': 4, 'go_partial': 1, 'unread': 1}） | 同上、C3 |
| Tool 定义 | 参考 59 / Go 面 149（llm_request 24 / lifecycle 16 / synthetic 1） | `docs/tool-contract-diff.md`、C8/C10 |
| 内置 Skill | 25 文件逐字节 229354 B | `contracts/builtin-skills.json` |
| CLI | hive 47 路径 / hive-browser 27 / hive-cred 4；usage 错误 134 例 | `docs/cli-surface-diff.md`、C11 |
| Storage | 19 文件类；v1/v2/v3 迁移 57 项 PASS；SC-01…08 | `docs/storage-contract-diff.md`、C4/C5 |
| SSE | 42/42 帧逐字 | `contracts/sse-frames`、`docs/sse-payload-parity.md` |

## 5. 机制面（50 机制 / 21 枚举）

| ID | 层 | 机制 | Python 锚点 |
|---|---|---|---|
| M01 | 1 | AgentLoop | `core/framework/agent_loop/agent_loop.py` |
| M02 | 1 | Turn（回合） | `core/framework/agent_loop/agent_loop.py` |
| M03 | 1 | 工具调用执行 | `core/framework/agent_loop/agent_loop.py` |
| M04 | 1 | 工具预算 | `core/framework/agent_loop/internals/types.py` |
| M05 | 1 | 上下文压缩 | `core/framework/agent_loop/internals/compaction.py` |
| M06 | 1 | 大结果外置 | `core/framework/agent_loop/internals/tool_result_handler.py` |
| M07 | 1 | Park / 冷恢复 | `core/framework/agent_loop/internals/cursor_persistence.py` |
| M08 | 1 | 流停滞守卫 | `core/framework/agent_loop/stream_stall.py` |
| M09 | 1 | 重复 / doom-loop 检测 | `core/framework/agent_loop/internals/stall_detector.py` |
| M10 | 1 | stall check | `core/framework/server/stall_check.py` |
| M11 | 1 | judge 流水线 | `core/framework/agent_loop/internals/judge_pipeline.py` |
| M12 | 1 | 限额 | `core/framework/agent_loop/internals/types.py` |
| M13 | 2 | Colony | `core/framework/host/colony_runtime.py` |
| M14 | 2 | Queen | `core/framework/agents/queen/` |
| M15 | 2 | Worker（ephemeral） | `core/framework/host/worker.py` |
| M16 | 2 | Persistent worker / overseer | `core/framework/host/worker.py、core/framework/host/colony_runtime.py` |
| M17 | 2 | Session | `core/framework/server/session_manager.py` |
| M18 | 2 | ColonyBinding | `core/framework/host/colony_binding.py` |
| M19 | 2 | WorkerProfile | `core/framework/host/worker_profiles.py` |
| M20 | 3 | Plan（tasks.json） | `core/framework/tasks/store.py` |
| M21 | 3 | Tracker（tracker.db） | `core/framework/host/tracker_db.py、core/framework/tools/tracker_tools.py` |
| M22 | 3 | 运行日志 | `core/framework/host/colony_db.py、core/framework/host/playbook/runner.py` |
| M23 | 4 | EventBus | `core/framework/host/event_bus.py` |
| M24 | 4 | ReminderHub | `core/framework/agent_loop/reminders.py` |
| M25 | 4 | SSE / live transcript | `core/framework/server/sse.py` |
| M26 | 4 | Event log | `core/framework/host/event_log.py` |
| M27 | 5 | Skill | `core/framework/skills/` |
| M28 | 5 | Playbook | `core/framework/tools/playbook_tools.py、core/framework/host/playbook/runner.py` |
| M29 | 5 | Tool tiers / search_tools | `core/framework/tools/tool_tiers.py` |
| M30 | 6 | Timer trigger | `core/framework/host/triggers.py` |
| M31 | 6 | Webhook trigger | `core/framework/host/webhook_server.py、core/framework/host/triggers.py` |
| M32 | 6 | Missed-fire 补跑 | `core/framework/host/triggers.py` |
| M33 | 6 | Sentinel 分类器 | `core/framework/sentinel/classifier.py、core/framework/sentinel/manager.py` |
| M34 | 6 | Escalation 记录与回复 | `core/framework/sentinel/store.py、core/framework/sentinel/listeners.py` |
| M35 | 7 | Memory 反思笔记 | `core/framework/agents/queen/queen_memory_v2.py` |
| M36 | 7 | Recall index | `core/framework/agents/queen/recall_selector.py、tools/src/memory_tools` |
| M37 | 7 | Timeline | `core/framework/agents/queen/timeline.py` |
| M38 | 7 | Soul | `core/framework/agents/queen/soul.py、core/framework/agents/queen/soul_reflection.py` |
| M39 | 8 | Harness 工具组 | `core/framework/tools/harness_tools.py` |
| M40 | 8 | MCP 外部工具 | `core/framework/loader/mcp_registry.py` |
| M41 | 8 | Credentials / hive-cred | `core/framework/credentials/` |
| M42 | 8 | Browser bridge | `tools/browser-extension/、tools/src/gcu/` |
| M43 | 8 | 附件 / 多模态 | `core/framework/agent_loop/internals/vision_fallback.py、core/framework/agent_loop/conversation.py` |
| M44 | 8 | Chart / 图表 | `tools/src/chart_tools/、core/framework/tools/harness_tools.py` |
| M45 | 8 | Global DB | `core/framework/global_db/` |
| M46 | HITL | ask_user | `core/framework/agent_loop/internals/synthetic_tools.py、core/framework/agent_loop/agent_loop.py` |
| M47 | HITL | Credential form | `core/framework/agent_loop/internals/credential_tool.py、core/framework/server/routes_credentials.py` |
| M48 | HITL | Colony suggestion / 确认 | `core/framework/agent_loop/internals/synthetic_tools.py、core/framework/host/event_bus.py` |
| M49 | HITL | Queen mail | `core/framework/server/queen_mail.py、core/framework/server/queen_mail_mcp.py、core/framework/server/claude_code_queens.py` |
| M50 | Pipeline | Pipeline 中间件 | `core/framework/pipeline/` |

枚举面：一级原语 18；概念页 8；Queen 生命周期工具 12；Reminder 源 15；Reminder 注入点 9；Event 类型 77；Queen 人格 YAML 13；Park 原因 11；Task 状态 5；Worker 状态 6；Judge 家族 3 活跃 + 1 legacy；Stall 家族 3；Sentinel 通道 3；Harness 工具组 5；Skill 扫描面 9 类根 / 6 scope；四基底 4；Worker 模式 2；Queen 阶段 2；触发类型 2；停滞预算 2；人机确认点 4

## 6. 登记例外与外部未验证（不藏）

- 设计性例外：浏览器渲染字节（Skia vs PIL）、字体度量、dpr 注解、父死时序、EventBus 异步派发、版本双轨、recall JSON 索引、playbook 900s 守护、工具输出 golden 12 例外
- 功能缺口：oauth2 平台 provider 4 模块（base/lifecycle/hubspot/zoho）、aden/client 云端 token 拉取、channels 宿主
- 外部未验证：Windows 实机、GitHub Actions release job、Sentinel 真实投递、91 个第三方集成、真实 MCP server
- 详见 `docs/trial/KNOWN-GAPS.md`、`docs/alignment-differences.md`、`docs/unported-triage.md`

## 7. 核查命令与最新结果（wave-71）

| 门禁 | 结果 |
|---|---|
| make ci（9 门禁） | **0** |
| govulncheck | **0 OSV** |
| QC | **9/9** |
| parity / refs | **5,816 / 6,391，0 missing** |
| C12 | **273 = 220/27/9/17** |
| compat | **12/12** |
| publish | 镜像同步（`7c2cccb`） |

