# 证据索引

> 本文件列出报告的主要事实来源与复核方式。行号会随代码演进变化，优先按符号名定位。

## 1. 基线与元数据

| 事实 | 来源 |
|---|---|
| 版本 1.0.15 | `core/pyproject.toml:2`、`tools/pyproject.toml:2` |
| Python >=3.11、Apache-2.0 | `core/pyproject.toml` |
| 依赖与 extras | `core/pyproject.toml` |
| 公开 SDK 导出 | `core/hive/__init__.py` |
| 前端技术栈 | `core/frontend/package.json` |
| uv workspace | 根 `pyproject.toml` |

## 2. 执行原语

| 事实 | 来源 |
|---|---|
| `LoopConfig` 全部默认 | `core/framework/agent_loop/internals/types.py:49` |
| 预算/宽限说明 | 同上，字段注释 |
| 工具并发上限 10 | `agent_loop/agent_loop.py`（`_PARALLEL_CAP = 10`） |
| 工具结果 30k 落盘 | `internals/tool_result_handler.py`、`LoopConfig.max_tool_result_chars` |
| 微压缩保留 6 | `internals/compaction.py`（`MICROCOMPACT_KEEP_RECENT`） |
| 压缩缓冲公式 | `LoopConfig.compaction_buffer_*` |
| 流看门狗 | `LoopConfig.llm_stream_*` |
| 停顿/死循环 | `internals/stall_detector.py`、`LoopConfig` |
| 停泊原因 | `agent_loop/reminders.py:113` |
| 游标字段 | `internals/cursor_persistence.py` |
| Judge 分级 | `internals/judge_pipeline.py` |
| 视觉回退 | `llm/capabilities.py`、`internals/vision_fallback.py` |

## 3. 组织与状态

| 事实 | 来源 |
|---|---|
| Queen 两种循环配置 | `agents/queen/agent.py:15` |
| Worker 默认预算 | `agents/queen/worker_definition.py:70` |
| Worker 覆盖白名单/边界 | `host/colony_runtime.py:66`、`:79` |
| 并发默认 4 | `host/colony_runtime.py:141` |
| 自适应预算 | `host/colony_runtime.py:160` 起 |
| Worker 状态 | `host/worker.py:69` |
| 软/硬超时公式 | `tools/queen_lifecycle_tools.py:1366` |
| Queen-only 工具剥离 | `server/routes_execution.py:26`、`:74`、`:86` |
| 阶段工具列表 | `agents/queen/nodes/__init__.py:39`、`:83` |
| Tracker schema/denylist | `host/tracker_db.py:34`、`:79` |
| Tracker 工具 | `tools/tracker_tools.py` |
| 任务模型/状态 | `tasks/models.py` |
| 事件类型 | `host/event_bus.py:102` |
| 提醒点/源 | `agent_loop/reminders.py`、各 `*_reminder.py` |
| 提示词拼装 | `agent_loop/prompting.py`、`orchestrator/prompting.py`、`tools/queen_lifecycle_tools.py:402` |
| 女王身份/选择 | `agents/queen/queen_profiles.py:533`、`:667` |
| Worker 提示 | `agents/queen/worker_definition.py:79`、`:229` |
| 技能目录/工具门控 | `skills/catalog.py:92`、`skills/tool_gating.py:79` |
| 系统提示缓存 | `llm/litellm.py:398`、`:484` |
| 辅助提示词 | `sentinel/classifier.py`、`session_titles.py`、`reflection_agent.py`、`soul_reflection.py`、`timeline.py`、`compaction.py` |
| 会话存储 | `storage/conversation_store.py`、`storage/session_store.py` |
| v3 迁移 | `storage/migrate_v3.py` |

## 4. 记忆与 Soul

| 事实 | 来源 |
|---|---|
| 记忆作用域/限制 | `agents/queen/queen_memory_v2.py` |
| 变更日志/作者/确认日期 | `agents/queen/memory_log.py` |
| 反思节奏 | `config.py:1020` |
| 召回模式 | `config.py:1058` |
| 会话召回 BM25 | `tools/src/memory_tools/recall.py` |
| 召回索引路径 | `tools/src/memory_tools/paths.py` |
| `search_messages` | `tools/src/memory_tools/tool.py` |
| 时间线 | `agents/queen/timeline.py` |
| Soul 模型/配置 | `agents/queen/soul.py`、`config.py:1256` |
| Soul 反思/证据 | `agents/queen/soul_reflection.py` |
| LongMemEval 结果 | `docs/memory-benchmark.md` |

## 5. 技能与 Playbook

| 事实 | 来源 |
|---|---|
| 技能作用域/优先级 | `skills/discovery.py:33` |
| 默认/预设技能 | `skills/_default_skills/`、`skills/_preset_skills/` |
| 信任门禁 | `skills/trust.py` |
| 工具门控 | `skills/tool_gating.py` |
| `write_skill` | `tools/queen_lifecycle_tools.py:2080` |
| Playbook 契约 | `docs/playbook-spec.md`、`host/playbook/runner.py`、`tools/playbook_tools.py` |
| Playbook 并发/快速窗口 | `runner.py`、`playbook_tools.py` |

## 6. 自治与入口

| 事实 | 来源 |
|---|---|
| Trigger 定义/错过 tick | `host/triggers.py` |
| `set_trigger` | `tools/queen_lifecycle_tools.py:3730` |
| Sentinel 分类 | `sentinel/classifier.py` |
| Sentinel 节奏/默认 | `sentinel/store.py:35` |
| Sentinel 渠道 | `sentinel/notifier.py` |
| Sentinel token | `sentinel/token.py` |
| Sentinel 升级源 | `sentinel/escalation_source.py` |
| Channel 宿主/输出 | `server/channels.py` |
| Queen Mail | `server/queen_mail.py`、`config.py:1137` |
| Claude Code Queen | `server/claude_code_queens.py`、`config.py:1097` |
| Turn Context | `server/turn_context.py` |

## 7. 工具、MCP、浏览器

| 事实 | 来源 |
|---|---|
| 5 个 harness 组 | `tools/harness_tools.py:41` |
| 工具类别 | `agents/queen/queen_tools_defaults.py:51` |
| eager/searchable | 同上、`tools/tool_tiers.py` |
| MCP 模式/传输 | `loader/mcp_registry.py` |
| 注册表路径/索引 | 同上 |
| 工具注册/并发安全/上下文参数 | `loader/tool_registry.py:159`、`:167` |
| 浏览器桥 | `tools/src/gcu/bridge_host.py`、`tools/src/gcu/cli.py` |
| 浏览器技能规则 | `skills/_default_skills/browser-automation/SKILL.md` |
| 集成目录 | `tools/src/aden_tools/tools/*/` |
| 社媒限速 | `framework/rate_limiter.py` |

## 8. 模型与凭据

| 事实 | 来源 |
|---|---|
| Provider 抽象 | `llm/provider.py` |
| 模型目录 | `llm/model_catalog.json`、`llm/model_catalog.py` |
| Provider 数量/预设 | 目录 JSON |
| 端点/Magpie | `llm/endpoints.py` |
| Key 池 | `llm/key_pool.py` |
| 凭据模型 | `credentials/models.py` |
| 加密存储 | `credentials/storage.py` |
| 解析/刷新 | `credentials/resolve.py` |
| 凭据工具 | `agent_loop/internals/credential_tool.py` |
| `hive-cred` | `credentials/cli.py` |
| 订阅 OAuth | `loader/agent_loader.py`、`llm/antigravity.py` |

## 9. 服务端、前端、运维

| 事实 | 来源 |
|---|---|
| 应用工厂/中间件 | `server/app.py` |
| Session 模型 | `server/session_manager.py:219` |
| Queen 装配 | `server/queen_orchestrator.py` |
| 路由清单 | `grep -rhoE 'add_(get|post|put|patch|delete)\("/api/[^"]+"' core/framework/server` |
| SSE | `server/sse.py` |
| 前端路由 | `core/frontend/src/App.tsx` |
| 页面 | `core/frontend/src/pages/` |
| 前端打包 | `core/hatch_build.py` |
| 会话分支 | `server/session_fork.py` |
| 自动标题 | `server/session_titles.py` |
| 报告包 | `server/_session_report.py` |
| 可观测性 | `observability/logging.py` |
| 清理 | `maintenance/retention.py`、`janitor.py` |
| Pipeline | `pipeline/` |
| 测试框架 | `testing/` |

## 10. 生命周期与序列

| 事实 | 来源 |
|---|---|
| 顶层状态/停泊原因 | `agent_loop/reminders.py:113` |
| 用户停止与注入语义 | `agent_loop/agent_loop.py:3546`、`:3714` |
| 回合决策顺序 | `agent_loop/agent_loop.py:_run_turn_loop` |
| Worker 状态迁移 | `host/worker.py:69`、`host/colony_runtime.py` 调度段 |
| 会话/阶段/建群 | `server/session_manager.py`、`server/routes_execution.py:1804` |
| Trigger 状态与错过补偿 | `host/triggers.py`、`tools/queen_lifecycle_tools.py:3730` |
| Sentinel 升级 | `sentinel/escalation_source.py`、`sentinel/store.py` |
| Playbook 运行 | `host/playbook/runner.py`、`tools/playbook_tools.py` |
| 凭据表单恢复 | `server/routes_credentials.py:775` |
| 记忆反思/召回 | `agents/queen/reflection_agent.py`、`agent_loop/*_reminder.py` |
| 压缩管线 | `agent_loop/internals/compaction.py` |
| 崩溃恢复 | `agent_loop/internals/cursor_persistence.py`、`storage/conversation_store.py` |

## 12. 文件系统与重启恢复

| 事实 | 来源 |
|---|---|
| HIVE_HOME 布局 | `framework/config.py:80` 起 |
| 原子写 | `framework/utils/io.py:atomic_write` |
| 会话 parts/cursor/meta | `agent_loop/conversation.py`、`internals/cursor_persistence.py` |
| 会话摘要 | `storage/session_summary.py` |
| 任务文档 | `tasks/models.py`、`tasks/store.py` |
| 事件日志 | `host/event_log.py`、`host/event_bus.py` |
| 事件历史分页/seq | `server/routes_sessions.py:2060` 起 |
| 冷恢复/iteration offset | `server/session_manager.py:1550` 起 |
| 冷中断停泊 | `agent_loop/agent_loop.py:1960` 起 |
| 孤儿工具调用修复 | `agent_loop/conversation.py:_repair_orphaned_tool_calls` |
| 压缩写序 | `agent_loop/conversation.py:1513`、`internals/compaction.py` |
| Worker 恢复 | `host/colony_runtime.py:1880` 起 |
| Trigger 恢复/错过 | `server/session_manager.py:1030` 起、`host/triggers.py` |
| Sentinel 启动 | `sentinel/manager.py:103` 起 |
| Playbook run log | `host/colony_db.py`、`tools/playbook_tools.py:_record_run` |
| 记忆反思/时间线恢复 | `agents/queen/reflection_agent.py:901`、`agents/queen/timeline.py:371` |
| Soul 状态 | `agents/queen/soul_reflection.py:171`、`:612` |
| 凭据恢复 | `credentials/key_storage.py`、`credentials/storage.py` |
| 启动迁移 | `server/session_manager.py:425` 起、`storage/migrate_v2.py`、`migrate_v3.py` |
| compaction sweep | `server/app.py:1043`、`server/compaction_status.py:153` |
| 优雅关闭 | `server/app.py:478`、`loader/cli.py:280` 起 |
| 浏览器桥生命周期 | `tools/src/gcu/bridge_host.py` |

## 13. 复核脚本

```bash
# 代码规模
find core/framework -name '*.py' -not -path '*/tests/*' -not -path '*/__pycache__/*' | wc -l
find core/framework -name '*.py' -not -path '*/tests/*' -not -path '*/__pycache__/*' -exec cat {} + | wc -l

# 路由数量
grep -rhoE 'add_(get|post|put|patch|delete)\(\s*"/api/[^"]+"' core/framework/server/*.py | wc -l

# Queen 数量
ls core/framework/agents/queen/queen_defaults/*.yaml | wc -l

# 默认/预设技能
ls core/framework/skills/_default_skills | wc -l
ls core/framework/skills/_preset_skills | wc -l

# 集成目录与注册点
ls -d tools/src/aden_tools/tools/*/ | grep -v __pycache__ | wc -l
grep -rn '@mcp.tool' tools/src/aden_tools/tools --include='*.py' | wc -l

# 模型目录
python3 -c "import json; d=json.load(open('core/framework/llm/model_catalog.json')); print(len(d['providers']), len(d.get('presets',{})))"
```
