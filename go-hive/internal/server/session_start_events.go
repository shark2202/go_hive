package server

// Session-start events: the opening block of a queen session's events.jsonl.
//
// Upstream's queen writes a fixed five-event opening into her session log as
// soon as the loop starts, before any user turn:
//
//  1. queen_identity_selected  (materialize_queen_identity)
//  2. reminder_injected        point:session_start (the loop's first reminder)
//  3. node_loop_started        (the persistent queen loop begins)
//  4. node_loop_iteration      iteration 0, stamped with the session phase
//  5. context_usage_updated    trigger=iteration_start
//
// Without that block a freshly created session has an empty events.jsonl, so
// /events/history, /snapshot and /stats all answer empty until the first chat
// turn. This port writes the same block when a session registers on an empty
// log segment, so a cold session carries the same history surface upstream
// does.
//
// Parity: framework/server/queen_orchestrator.py::materialize_queen_identity
// Parity: framework/agent_loop/agent_loop.py::_emit_reminder_injected
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
// Parity: framework/agent_loop/agent_loop.py::_publish_iteration

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// emitSessionStartEvents writes the opening block into the session's event log
// when that log is still empty. A segment that already carries events belongs
// to a resumed session or a restart of one that has run; upstream truncates
// events.jsonl when the queen starts a fresh segment, while this port appends,
// so the opening block is written only once per segment. It reports whether the
// block was written, which is also the signal that this session's queen loop
// has not run yet (a fresh create gets the opening model turn).
func emitSessionStartEvents(session *Session) bool {
	if session == nil || session.EventBus == nil || session.QueenDir == "" || session.ID == "" {
		return false
	}
	eventsPath := filepath.Join(session.QueenDir, "events.jsonl")
	if info, err := os.Stat(eventsPath); err == nil && info.Size() > 0 {
		return false
	}
	if err := session.EventBus.SetSessionLog(eventsPath, 0); err != nil {
		log.Printf("session %s: opening event log unavailable: %v", session.ID, err)
		return false
	}

	// Upstream stamps the session id as the opening execution id: the queen's
	// node loop starts as execution <session id> before any turn exists.
	executionID := session.ID
	queenID, queenName, queenTitle := sessionStartQueenIdentity(session)

	identity := eventbus.NewAgentEvent(eventbus.EventQueenIdentitySelected, chatStreamID)
	identity.Data = map[string]any{
		"queen_id": queenID,
		"name":     queenName,
		"title":    queenTitle,
	}
	session.EventBus.Publish(identity)

	session.EventBus.EmitReminderInjected(
		chatStreamID,
		chatNodeID,
		"point:"+string(agentloop.SESSION_START),
		string(agentloop.SESSION_START),
		map[string]any{"point": string(agentloop.SESSION_START)},
		&executionID,
	)

	maxIterations := sessionStartMaxIterations(session)
	session.EventBus.EmitNodeLoopStarted(chatStreamID, chatNodeID, &executionID, &maxIterations)
	session.EventBus.EmitNodeLoopIteration(
		chatStreamID,
		chatNodeID,
		0,
		&executionID,
		map[string]any{"phase": sessionStartPhase(session)},
	)

	// The opening context readout keeps the block's shape for every cold
	// session: a session without a chat runtime has no composed request, so its
	// components are zero rather than omitted.
	contextEvent := eventbus.NewAgentEvent(eventbus.EventContextUsageUpdated, chatStreamID)
	contextEvent.NodeID = eventbus.StrPtr(chatNodeID)
	contextEvent.Data = sessionStartContextUsage(session)
	session.EventBus.Publish(contextEvent)
	return true
}

// sessionStartQueenIdentity reads the bound identity, falling back to the
// session's queen name and then the shared "default" scope (the same fallback
// chatSessionQueenScope uses for storage). Name/title are always present, as
// upstream's profile.get(..., "") payload is.
func sessionStartQueenIdentity(session *Session) (string, string, string) {
	queenID := ""
	var profile map[string]any
	if phase, ok := session.PhaseState.(SessionPhaseState); ok && phase != nil {
		queenID = strings.TrimSpace(phase.QueenID())
		profile = phase.QueenProfile()
	}
	if queenID == "" {
		queenID = strings.TrimSpace(session.QueenName)
	}
	if queenID == "" {
		queenID = "default"
	}
	name, _ := profile["name"].(string)
	title, _ := profile["title"].(string)
	return queenID, name, title
}

func sessionStartPhase(session *Session) string {
	return SessionPhaseLabel(session)
}

// SessionPhaseLabel reports the session's queen phase ("independent" /
// "colony") for event payloads, matching the upstream phase state the loop's
// iteration_metadata_provider reads. It is exported so the session loader can
// hand the live chat runtime the same label.
func SessionPhaseLabel(session *Session) string {
	if phase, ok := session.PhaseState.(SessionPhaseState); ok && phase != nil {
		if value := strings.TrimSpace(phase.Phase()); value != "" {
			return value
		}
	}
	if session.ColonyID != "" || session.WorkerPath != "" {
		return QueenPhaseColony
	}
	return QueenPhaseIndependent
}

// sessionStartMaxIterations carries the bound the session's loop actually
// enforces. Upstream advertises 999_999 in its persistent queen loop; the chat
// runtime here runs one bounded 50-iteration loop per turn, so the opening
// event quotes that same configured bound instead of a number the loop would
// never honour.
func sessionStartMaxIterations(session *Session) int {
	if session.Chat != nil && session.Chat.loop != nil && session.Chat.loop.Config.MaxIterations > 0 {
		return session.Chat.loop.Config.MaxIterations
	}
	return agentloop.DefaultLoopConfig().MaxIterations
}

// sessionStartContextUsage mirrors publish_context_usage: a char-based
// projection of everything the next LLM request would carry (conversation,
// rendered system prompt, JSON tool definitions). A session whose runtime could
// not be built (no provider credentials) has composed no request, so every
// component is zero — the event still carries the full payload shape.
func sessionStartContextUsage(session *Session) map[string]any {
	var conv *agentloop.NodeConversation
	var tools []llm.ToolDef
	maxContextTokens := 0
	if session.Chat != nil {
		conv = session.Chat.conv
		tools = session.Chat.toolsForTurn()
		if session.Chat.loop != nil {
			maxContextTokens = session.Chat.loop.Config.MaxContextTokens
		}
	}
	return composeContextUsage(conv, maxContextTokens, tools, "iteration_start")
}

// composeContextUsage is the shared char-based context projection: the
// session-start block and every later readout (iteration_start,
// post_tool_call, post_tool_results) carry the same payload shape; only the
// trigger differs.
//
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
func composeContextUsage(conv *agentloop.NodeConversation, maxContextTokens int, tools []llm.ToolDef, trigger string) map[string]any {
	conversationChars, imageBlocks, systemChars, toolDefsChars := 0, 0, 0, 0
	messageCount := 0
	if conv != nil {
		conversationChars, imageBlocks = conv.ConversationCharsAndImages()
		systemChars = len(conv.SystemPrompt())
		toolDefsChars = chatToolDefsChars(tools)
		messageCount = conv.MessageCount()
	}
	if maxContextTokens <= 0 {
		maxContextTokens = agentloop.DefaultMaxContextTokens
	}
	totalChars := conversationChars + systemChars + toolDefsChars
	// 4/3 safety margin (chars/4 with a 4/3 correction = chars/3).
	charTokens := (totalChars * 4) / (3 * 4)
	imageTokens := imageBlocks * 2000
	estimatedTokens := charTokens + imageTokens
	ratio := 0.0
	if maxContextTokens > 0 {
		ratio = float64(estimatedTokens) / float64(maxContextTokens)
	}
	return map[string]any{
		"usage_ratio":        math.Round(ratio*10000) / 10000,
		"usage_pct":          int(math.Round(ratio * 100)),
		"message_count":      messageCount,
		"estimated_tokens":   estimatedTokens,
		"max_context_tokens": maxContextTokens,
		"trigger":            trigger,
		"breakdown": map[string]any{
			"conversation_chars": conversationChars,
			"system_chars":       systemChars,
			"tool_defs_chars":    toolDefsChars,
			"image_blocks":       imageBlocks,
			"image_tokens":       imageTokens,
		},
	}
}

func chatToolDefsChars(tools []llm.ToolDef) int {
	total := 0
	for _, tool := range tools {
		total += len(tool.Name)
		total += len(tool.Description)
		if tool.Parameters == nil {
			continue
		}
		if encoded, err := json.Marshal(tool.Parameters); err == nil {
			total += len(encoded)
		}
	}
	return total
}
