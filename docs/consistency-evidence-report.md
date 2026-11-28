# Consistency evidence report

- generated: `2026-11-28T03:38:49.101253+00:00`
- commit: `7c3bdba13d32fd1c37673de1752256fa2711bb5b`
- matrix: `go_hive/docs/consistency-matrix.json` (`f589f4b5b08de2b1b4ab8c2c9eedf4e9aaa05b141b81ae308076a5f238aeafb7`)
- status: **OK**

## Totals

| metric | value |
| --- | ---: |
| evidence entries | 321 |
| rows | 34 |
| row drift | 0 |
| dangling refs | 0 |
| evidence ok | 321 |

## Evidence by kind

| kind | ok | drift |
| --- | ---: | ---: |
| artifact | 11 | 0 |
| behavior-registry | 4 | 0 |
| c12-module | 273 | 0 |
| contract | 8 | 0 |
| package | 25 | 0 |

## CodeGraph anchors

Index: 4096 files / 93195 nodes / 281961 edges · state `complete`

| symbol | expectation | Python | Go | status |
| --- | --- | --- | --- | --- |
| `AgentLoop` | both | core/framework/agent_loop/agent_loop.py:722 | go_hive/go-hive/internal/agentloop/loop.go:85 | ok |
| `ToolRegistry` | both | core/framework/loader/tool_registry.py:127 | go_hive/go-hive/internal/tools/registry.go:104 | ok |
| `CredentialStore` | both | core/framework/credentials/store.py:35 | go_hive/go-hive/internal/credentials/store.go:19 | ok |
| `SessionManager` | both | core/framework/server/session_manager.py:412 | go_hive/go-hive/internal/server/session.go:476 | ok |
| `ColonyRuntime` | both | core/framework/host/colony_runtime.py:301 | go_hive/go-hive/internal/colony/runtime.go:123 | ok |
| `SkillsManager` | both | core/framework/skills/manager.py:80 | go_hive/go-hive/internal/skills/manager.go:30 | ok |
| `EventBus` | both | core/framework/host/event_bus.py:388 | go_hive/go-hive/internal/eventbus/bus.go:72 | ok |
| `ReminderHub` | both | core/framework/agent_loop/reminders.py:356 | go_hive/go-hive/internal/agentloop/reminder_hub.go:48 | ok |
| `MCPClient` | both | core/framework/loader/mcp_client.py:142 | go_hive/go-hive/internal/tools/mcp.go:33 | ok |
| `PlaybookRun` | both | core/framework/host/playbook/runner.py:111 | go_hive/go-hive/internal/playbook/runner.go:167 | ok |
| `TriggerDefinition` | both | core/framework/host/triggers.py:32 | go_hive/go-hive/internal/triggers/triggers.go:12 | ok |
| `MemoryFile` | both | core/framework/agents/queen/queen_memory_v2.py:104 | go_hive/go-hive/internal/memory/memory.go:77 | ok |
| `Orchestrator` | python | core/framework/orchestrator/orchestrator.py:115 | — | ok |

## Reproduce

```bash
uv run --no-project python3 go_hive/scripts/gen_consistency_matrix.py
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --write
uv run --no-project python3 go_hive/scripts/verify_consistency_evidence.py --codegraph --check
```
