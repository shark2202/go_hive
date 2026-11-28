package agentloop

// Reminder-source plumbing: the AgentContext -> ReminderAgentContext view the
// hub binds to, plus the typed provider coercions that view reads through.
// Split out of loop.go to keep the turn driver under its file budget; the
// bodies are unchanged.

import (
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// NewReminderAgentContext is the AgentContext -> reminder-source view the hub
// binds to. Exported so the server session runtime can start the hub's ticker
// with the same context the loop binds at its first turn.
//
// Parity: the AgentContext attributes ReminderHub sources reach for
func NewReminderAgentContext(agentCtx *AgentContext) *ReminderAgentContext {
	if agentCtx == nil {
		return &ReminderAgentContext{}
	}
	out := &ReminderAgentContext{
		IsQueenStream:                  agentCtx.IsQueenStream(),
		SearchableToolsProvider:        searchableToolsFrom(agentCtx.SearchableToolsProvider),
		LoadedToolNamesProvider:        stringSliceProvider(agentCtx.LoadedToolNamesProvider),
		QueenSkillsCatalogProvider:     stringProvider(agentCtx.QueenSkillsCatalogProvider),
		AvailableTools:                 reminderToolsFromDefs(agentCtx.AvailableTools),
		DynamicToolsProvider:           reminderToolsFromProvider(agentCtx.DynamicToolsProvider),
		OpenTasksProvider:              agentCtx.OpenTasksProvider,
		SessionGoalProvider:            agentCtx.SessionGoalProvider,
		SentinelAutopilotProvider:      agentCtx.SentinelAutopilotProvider,
		ColonyBindingProvider:          agentCtx.ColonyBindingProvider,
		ActiveWorkersProvider:          activeWorkersFrom(agentCtx.ActiveWorkersProvider),
		ColonyStatsProvider:            colonyStatsFrom(agentCtx.ColonyStatsProvider),
		TurnContextProvider:            turnContextProviderFrom(agentCtx.TurnContextProvider),
		PastConversationRecallProvider: pastConversationRecallProviderFrom(agentCtx.PastConversationRecallProvider),
		MemoryRecallProvider:           agentCtx.MemoryRecallProvider,
		MemoryIndexProvider:            agentCtx.MemoryIndexProvider,
		MemoryRecallSettingsProvider:   agentCtx.MemoryRecallSettingsProvider,
		ChatAppProvider:                chatAppProviderView(agentCtx.ChatAppProvider),
	}
	if agentCtx.SessionID != nil {
		out.SessionID = *agentCtx.SessionID
	}
	if agentCtx.ColonyID != nil {
		out.ColonyID = *agentCtx.ColonyID
	}
	return out
}

// chatAppProviderView coerces AgentContext.ChatAppProvider (an `any` field
// for upstream parity) onto the reminder view's typed reader. A nil provider,
// or one of an unreadable shape, reads as "no chat app", exactly as upstream's
// callable check does.
func chatAppProviderView(provider any) func() any {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() any:
		return p
	case func() string:
		return func() any { return p() }
	default:
		return nil
	}
}

func reminderToolsFromProvider(provider any) func() []Tool {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() []Tool:
		return p
	case []Tool:
		fixed := append([]Tool(nil), p...)
		return func() []Tool { return fixed }
	case func() []llm.ToolDef:
		return func() []Tool { return reminderToolsFromDefs(p()) }
	case []llm.ToolDef:
		fixed := append([]llm.ToolDef(nil), p...)
		return func() []Tool { return reminderToolsFromDefs(fixed) }
	default:
		return nil
	}
}

func activeWorkersFrom(provider any) func() []ActiveWorker {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() []ActiveWorker:
		return p
	case []ActiveWorker:
		fixed := append([]ActiveWorker(nil), p...)
		return func() []ActiveWorker { return fixed }
	case func() []map[string]any:
		return func() []ActiveWorker {
			rows := p()
			out := make([]ActiveWorker, 0, len(rows))
			for _, row := range rows {
				out = append(out, ActiveWorker{
					ID:     stringValue(row["worker_id"]),
					Status: stringValue(row["status"]),
					Task:   stringValue(row["task"]),
				})
			}
			return out
		}
	default:
		return nil
	}
}

func colonyStatsFrom(provider any) func() ColonyStats {
	switch p := provider.(type) {
	case nil:
		return nil
	case func() ColonyStats:
		return p
	case ColonyStats:
		fixed := p
		return func() ColonyStats { return fixed }
	case func() map[string]any:
		return func() ColonyStats {
			stats := p()
			active, _ := stats["active"].(int)
			total, _ := stats["total"].(int)
			if value, ok := asFloat(stats["active"]); ok {
				active = int(value)
			}
			if value, ok := asFloat(stats["total"]); ok {
				total = int(value)
			}
			return ColonyStats{Active: active, Total: total}
		}
	default:
		return nil
	}
}

// stringValue is the lenient string reader providers share.
func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func searchableToolsFrom(provider any) func() []Tool {
	switch p := provider.(type) {
	case func() []Tool:
		return p
	case []Tool:
		fixed := append([]Tool(nil), p...)
		return func() []Tool { return fixed }
	case func() []llm.ToolDef:
		return func() []Tool {
			return reminderToolsFromDefs(p())
		}
	case []llm.ToolDef:
		fixed := append([]llm.ToolDef(nil), p...)
		return func() []Tool { return reminderToolsFromDefs(fixed) }
	default:
		return nil
	}
}

func reminderToolsFromDefs(defs []llm.ToolDef) []Tool {
	out := make([]Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, Tool{Name: def.Name, Description: def.Description})
	}
	return out
}

func stringSliceProvider(provider any) func() []string {
	switch p := provider.(type) {
	case func() []string:
		return p
	case []string:
		fixed := append([]string(nil), p...)
		return func() []string { return fixed }
	default:
		return nil
	}
}

func stringProvider(provider any) func() string {
	switch p := provider.(type) {
	case func() string:
		return p
	case string:
		return func() string { return p }
	default:
		return nil
	}
}
