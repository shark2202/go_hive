package server

// Queen lifecycle tools: the queen chat session's agent-facing control surface
// over the bound colony's workers, the worker-profile registry, the worker
// escalation inbox and the session trigger store.
//
// Upstream registers twelve tools in `register_queen_lifecycle_tools`. This
// port carries all of them: `run_worker` lives in internal/colony (it only
// needs the spawner), `write_skill` lives in internal/skills, and the ten
// session-bound tools are implemented here because they read state that only
// a live session owns (pending escalations, triggers, worker profiles, the
// bound runtime's worker registry).
//
// Registration is queen-only: `enableQueenLifecycleTools` is called for queen
// chat sessions, never for a colony worker's catalogue, so a worker's tool
// inventory keeps the upstream worker surface.
//
// Parity: framework/tools/queen_lifecycle_tools.py::register_queen_lifecycle_tools

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

const (
	// QueenGetWorkerStatusToolName reports the bound colony's worker state.
	QueenGetWorkerStatusToolName = "get_worker_status"
	// QueenInjectMessageToolName relays guidance into live workers.
	QueenInjectMessageToolName = "inject_message"
	// QueenListWorkerQuestionsToolName reads the worker escalation inbox.
	QueenListWorkerQuestionsToolName = "list_worker_questions"
	// QueenReplyToWorkerToolName answers one escalation by request_id.
	QueenReplyToWorkerToolName = "reply_to_worker"
	// QueenStopWorkerToolName halts all colony work and pauses timers.
	QueenStopWorkerToolName = "stop_worker"
	// QueenUpdateWorkerProfileToolName inserts/updates one worker profile.
	QueenUpdateWorkerProfileToolName = "update_worker_profile"
	// QueenListBrowserProfilesToolName lists connected browser profiles.
	QueenListBrowserProfilesToolName = "list_browser_profiles"
	// QueenSetTriggerToolName activates (or creates) a trigger.
	QueenSetTriggerToolName = "set_trigger"
	// QueenListTriggersToolName lists the session's triggers.
	QueenListTriggersToolName = "list_triggers"
	// QueenRemoveTriggerToolName deactivates an active trigger.
	QueenRemoveTriggerToolName = "remove_trigger"
)

// Upstream's stop grace window: kept well under the tool-call budget so the
// tool always returns cleanly instead of timing out and being retried.
//
// Parity: framework/tools/queen_lifecycle_tools.py::_DEFAULT_STOP_GRACE_SEC
const (
	defaultStopGraceSeconds = 12.0
	maxStopGraceSeconds     = 25.0
)

// QueenLifecycleBinding is one queen session's lifecycle tool table. It is
// built before the colony attaches (the catalogue is assembled at session
// creation) and resolves the runtime per call, mirroring upstream's
// late-binding session object.
type QueenLifecycleBinding struct {
	session *Session
	home    hivehome.Home

	statusMu    sync.Mutex
	statusCalls map[string]float64 // cooldown tier -> monotonic seconds
}

// NewQueenLifecycleBinding binds the ten lifecycle tools to one session.
func NewQueenLifecycleBinding(session *Session, home hivehome.Home) *QueenLifecycleBinding {
	return &QueenLifecycleBinding{session: session, home: home, statusCalls: map[string]float64{}}
}

// QueenLifecycleToolNames lists the tool names this table executes, in the
// registration order upstream uses for the session-bound subset.
func QueenLifecycleToolNames() []string {
	return []string{
		QueenStopWorkerToolName,
		QueenUpdateWorkerProfileToolName,
		QueenListBrowserProfilesToolName,
		QueenGetWorkerStatusToolName,
		QueenInjectMessageToolName,
		QueenListWorkerQuestionsToolName,
		QueenReplyToWorkerToolName,
		QueenSetTriggerToolName,
		QueenRemoveTriggerToolName,
		QueenListTriggersToolName,
	}
}

// Has reports whether name belongs to this table.
func (b *QueenLifecycleBinding) Has(name string) bool {
	for _, candidate := range QueenLifecycleToolNames() {
		if candidate == name {
			return true
		}
	}
	return false
}

// Execute runs one lifecycle tool call. The bool is the tool-error flag,
// matching the session catalogue's (content, isError) contract.
func (b *QueenLifecycleBinding) Execute(ctx context.Context, name string, input map[string]any) (string, bool) {
	if b == nil || b.session == nil {
		return queenLifecycleJSON(map[string]any{"error": "No runtime on this session."}), true
	}
	switch name {
	case QueenGetWorkerStatusToolName:
		return b.getWorkerStatus(input), false
	case QueenInjectMessageToolName:
		return b.injectMessage(input)
	case QueenListWorkerQuestionsToolName:
		return b.listWorkerQuestions(), false
	case QueenReplyToWorkerToolName:
		return b.replyToWorker(input)
	case QueenStopWorkerToolName:
		return b.stopWorker(ctx, input)
	case QueenUpdateWorkerProfileToolName:
		return b.updateWorkerProfile(input)
	case QueenListBrowserProfilesToolName:
		return b.listBrowserProfiles(ctx), false
	case QueenSetTriggerToolName:
		return b.setTrigger(input)
	case QueenListTriggersToolName:
		return b.listTriggers(), false
	case QueenRemoveTriggerToolName:
		return b.removeTrigger(input)
	}
	return queenLifecycleJSON(map[string]any{"error": "Unknown queen lifecycle tool: " + name}), true
}

// QueenLifecycleToolDefs returns the ten tool definitions. The parameter
// schemas mirror the upstream Tool(...) declarations field for field.
func QueenLifecycleToolDefs() []llm.ToolDef {
	defs := queenWorkerControlToolDefs()
	defs = append(defs, queenEscalationToolDefs()...)
	defs = append(defs, queenTriggerToolDefs()...)
	defs = append(defs, queenProfileToolDefs()...)
	return defs
}

// queenWorkerControlToolDefs covers the colony-runtime controls: stop_worker,
// get_worker_status and inject_message.
func queenWorkerControlToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: QueenStopWorkerToolName,
			Description: "Halt ALL colony work and pause timers. Use this whenever the user " +
				"asks to pause, stop, or halt — it is the honest 'stop everything'. It first " +
				"cancels any running playbook convergence loops (so they can't re-dispatch " +
				"fresh workers), then cancels every active worker — including queued ones " +
				"waiting for capacity. Each live worker first receives a [STOP REQUESTED] " +
				"inject asking it to call report_to_parent with its latest progress; workers " +
				"that do not report within `grace_seconds` are force-stopped. This is " +
				"reliable and fast — it always returns within seconds, so call it ONCE and " +
				"report the result; do not retry it in a loop. The collected reports plus " +
				"the list of cancelled playbooks are returned in the tool result, so report " +
				"what ACTUALLY stopped from that result — do not claim everything is " +
				"stopped without it. Leave `grace_seconds` unset for a short reporting " +
				"window; pass 0 for an immediate hard stop.\n" +
				"To halt one specific run_playbook run while leaving others going, use " +
				"stop_playbook(run_id=...) instead.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type": "string",
						"description": "Why the workers are being stopped. Surfaced to each " +
							"worker in the stop inject so its final report_to_parent call can " +
							"reflect the cause.",
					},
					"grace_seconds": map[string]any{
						"type": "number",
						"description": "Seconds to wait for workers to file a report_to_parent " +
							"before force-stopping. Clamped to [0, 25]; leave unset for a short " +
							"window, or 0 for an immediate hard stop.",
					},
				},
				"required": []any{},
			},
		},
		{
			Name: QueenGetWorkerStatusToolName,
			Description: "Check on the colony's workers. Returns a brief prose summary by default. " +
				"Use 'focus' to drill into specifics:\n" +
				"- activity: current node, transitions, latest LLM output\n" +
				"- memory: worker's accumulated buffer state\n" +
				"- tools: running and recent tool calls\n" +
				"- issues: retries, stalls, constraint violations\n" +
				"- progress: goal criteria, token consumption\n" +
				"- full: everything as JSON\n" +
				"For a PLAYBOOK (run_playbook), use get_playbook_status(run_id=...) instead — " +
				"it reports the convergence run's progress by run_id.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"focus": map[string]any{
						"type":        "string",
						"enum":        []any{"activity", "memory", "tools", "issues", "progress", "full"},
						"description": "Aspect to inspect. Omit for a brief summary.",
					},
					"last_n": map[string]any{
						"type":        "integer",
						"description": "Recent events per category (default 20). Only for activity, tools, full.",
					},
				},
				"required": []any{},
			},
		},
		{
			Name: QueenInjectMessageToolName,
			Description: "Send a message to the colony's live workers. The message is injected " +
				"into each active worker's conversation as a user turn. Use this to relay user " +
				"instructions or concerns. At least one worker must be running.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "Message content to send to the workers",
					},
				},
				"required": []any{"content"},
			},
		},
	}
}

// queenEscalationToolDefs covers the worker escalation inbox.
func queenEscalationToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: QueenListWorkerQuestionsToolName,
			Description: "List all worker escalations currently awaiting your reply. " +
				"Each entry has a request_id that you pass to reply_to_worker() " +
				"to unblock the specific worker that asked.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name: QueenReplyToWorkerToolName,
			Description: "Reply to a specific worker escalation. The reply is injected " +
				"into the identified worker's conversation so it can resume. " +
				"Use list_worker_questions() to discover pending request_ids.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"request_id": map[string]any{
						"type":        "string",
						"description": "The escalation request_id from list_worker_questions.",
					},
					"reply": map[string]any{
						"type":        "string",
						"description": "Guidance or answer text to hand back to the worker.",
					},
				},
				"required": []any{"request_id", "reply"},
			},
		},
	}
}

// queenTriggerToolDefs covers set/list/remove_trigger.
func queenTriggerToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: QueenSetTriggerToolName,
			Description: "Activate a trigger (timer) so it fires periodically. " +
				"Use trigger_id of an available trigger, or provide trigger_type + trigger_config" +
				" to create a custom one. " +
				"A task must be configured before activation —" +
				" either pre-set on the trigger or provided here.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"trigger_id": map[string]any{
						"type":        "string",
						"description": "ID of the trigger to activate (from list_triggers) or a new custom ID",
					},
					"trigger_type": map[string]any{
						"type":        "string",
						"description": "Type of trigger ('timer'). Only needed for custom triggers.",
					},
					"trigger_config": map[string]any{
						"type": "object",
						"description": "Config for the trigger. Timer: {cron: '*/5 * * * *'} or " +
							"{interval_minutes: 5}. Only needed for custom triggers.",
					},
					"task": map[string]any{
						"type": "string",
						"description": "The task/instructions for the worker when this trigger fires" +
							" (e.g. 'Process inbox emails using saved rules')." +
							" Required if not already configured on the trigger.",
					},
				},
				"required": []any{"trigger_id"},
			},
		},
		{
			Name:        QueenRemoveTriggerToolName,
			Description: "Deactivate an active trigger. The trigger stops firing but remains available for re-activation.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"trigger_id": map[string]any{
						"type":        "string",
						"description": "ID of the trigger to deactivate",
					},
				},
				"required": []any{"trigger_id"},
			},
		},
		{
			Name:        QueenListTriggersToolName,
			Description: "List all available triggers (from the loaded worker) and their active/inactive status.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// queenProfileToolDefs covers the worker-profile registry tools.
func queenProfileToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: QueenUpdateWorkerProfileToolName,
			Description: "Insert or update one worker profile on an existing colony. " +
				"Use this to swap a profile's account alias (e.g. 'switch " +
				"slack-work to use alias work-2'), bind it to a specific Chrome " +
				"profile via browser_profile (a connected label from " +
				"list_browser_profiles), or add a profile after the colony was " +
				"forked. Existing siblings are preserved. Pass only the fields you " +
				"want to change.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"colony_id":    map[string]any{"type": "string"},
					"profile_name": map[string]any{"type": "string"},
					"integrations": map[string]any{
						"type":                 "object",
						"additionalProperties": map[string]any{"type": "string"},
					},
					"task":             map[string]any{"type": "string"},
					"skill_name":       map[string]any{"type": "string"},
					"concurrency_hint": map[string]any{"type": "integer", "minimum": 1},
					"prompt_override":  map[string]any{"type": "string"},
					"tool_filter": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
					"browser_profile": map[string]any{
						"type": "string",
						"description": "Chrome profile label this profile's browser tools target " +
							"(from list_browser_profiles). Empty clears it.",
					},
				},
				"required": []any{"colony_id", "profile_name"},
			},
		},
		{
			Name: QueenListBrowserProfilesToolName,
			Description: "List the Chrome profiles whose Hive extension is currently connected, each with the " +
				"label to pass as browser_profile in update_worker_profile. Call this before binding " +
				"workers to browser profiles so you bind to real, connected labels.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// colonyBoundQueenTool reports whether name belongs to the colony-phase tool
// surface: while no colony runtime is bound, the def is not advertised (a
// direct call still returns the tool's own actionable no-colony error). This
// mirrors the upstream phase split, where these tools exist only in the
// COLONY list.
//
// Parity: framework/agents/queen/nodes/__init__.py::_QUEEN_COLONY_TOOLS
func colonyBoundQueenTool(name string) bool {
	switch name {
	case QueenGetWorkerStatusToolName, QueenInjectMessageToolName, QueenStopWorkerToolName:
		return true
	}
	return false
}

// queenLifecycleJSON encodes a tool payload the way Python's json.dumps does
// for the parts that matter here: no HTML escaping and no trailing newline.
func queenLifecycleJSON(value any) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `{"error":"result could not be encoded"}`
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

// charPrefix truncates text to n characters (runes), so multi-byte content is
// never cut mid-symbol, matching Python's str slicing.
func charPrefix(text string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

// roundHalfEvenTo1 rounds to one decimal place, matching Python's round() for
// the sub-second values reported here (waiting_seconds, elapsed).
func roundTo1(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return math.Round(value*10) / 10
}

func queenInputString(input map[string]any, key string) string {
	value, ok := input[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// queenInputPresent reports whether the caller supplied key with a non-null
// value — the Go spelling of upstream's "argument is not None" merge rule.
func queenInputPresent(input map[string]any, key string) bool {
	value, ok := input[key]
	return ok && value != nil
}

// queenStringSlice reads a JSON array of strings, keeping only string items
// (the schema declares items.type=string).
func queenStringSlice(raw any) ([]string, bool) {
	switch typed := raw.(type) {
	case nil:
		return nil, false
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out, true
	default:
		return nil, false
	}
}

// queenListValue normalises a JSON array payload to []any without allocating
// when it already has that shape.
func queenListValue(raw any) []any {
	items, _ := raw.([]any)
	return items
}
