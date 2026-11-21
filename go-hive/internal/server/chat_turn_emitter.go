package server

// chatTurnEmitter mirrors the upstream agent loop's per-turn event publishing
// onto the session bus: execution / loop lifecycle, assistant text, per-call
// usage, tool calls and the park boundary. It lives beside the chat runtime
// rather than inside it so each file stays within the size budget.

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// sha256Hex12 matches the upstream cache fingerprint: the first 12 hex chars
// of the sha256 over the string, empty string included.
func sha256Hex12(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

// chatTurnEmitter publishes one turn's progress onto the session bus using
// the upstream event names and payload shapes. Nil-receiver methods are
// no-ops so a session without a bus pays no branching cost at each call site.
//
// Parity: framework/agent_loop/internals/event_publishing.py
// Parity: framework/host/execution_manager.py::_run_execution
type chatTurnEmitter struct {
	bus           *eventbus.EventBus
	streamID      string
	nodeID        string
	executionID   string
	model         string
	maxIterations int
	// stream links the emitter to the streaming adapter so the loop's
	// end-of-call AssistantText does not republish text that already went out
	// frame by frame.
	stream *chatStreamingCompleter
	// opening marks the upstream queen loop's first, message-less iteration:
	// the loop envelope (execution / node-loop boundaries) belongs to the
	// session-start block, so only the LLM frames and the park boundary are
	// published, and iterations are reported 0-based as upstream's are.
	opening bool
	// conv supplies the per-turn cache diagnostics (system prefix/suffix sha,
	// request message count) llm_turn_complete carries upstream.
	conv *agentloop.NodeConversation
	// phase is the queen phase stamped on every node_loop_iteration, as the
	// upstream loop's iteration_metadata_provider reports it.
	phase func() string
	// contextUsage builds the context_usage_updated payload for a trigger.
	// Upstream publishes it on every iteration start, after each tool result
	// and once per tool batch; nil skips the readout.
	contextUsage func(trigger string) map[string]any
	// requestMessages is the size of the request the provider received for the
	// current iteration. llm_turn_complete is published after the tool batch
	// (upstream's timing), so the conversation has already grown by then; the
	// diagnostics report this snapshot instead.
	requestMessages int
}

func (r *ChatRuntime) newTurnEmitter(executionID string) *chatTurnEmitter {
	if r == nil || r.bus == nil {
		return nil
	}
	maxIterations := 0
	if r.loop != nil {
		maxIterations = r.loop.Config.MaxIterations
	}
	return &chatTurnEmitter{
		bus:           r.bus,
		streamID:      chatStreamID,
		nodeID:        chatNodeID,
		executionID:   executionID,
		model:         r.model,
		maxIterations: maxIterations,
		stream:        r.streamLLM,
		conv:          r.conv,
		phase:         r.deps.Phase,
		contextUsage:  r.turnContextUsage,
	}
}

// executionStarted opens the turn: execution_started, an EXECUTING loop state
// (the snapshot derives is_executing from it), and the loop-started boundary.
// The opening turn is exempt: upstream's persistent queen loop announces the
// session once (the session-start block) and its first iteration publishes no
// execution envelope.
func (e *chatTurnEmitter) executionStarted(message string) {
	if e == nil || e.opening {
		return
	}
	e.bus.EmitExecutionStarted(e.streamID, e.executionID,
		map[string]any{"message": message}, nil, nil)
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "executing", &e.executionID, nil, nil, nil)
	maxIterations := e.maxIterations
	if maxIterations <= 0 {
		maxIterations = agentloop.DefaultLoopConfig().MaxIterations
	}
	e.bus.EmitNodeLoopStarted(e.streamID, e.nodeID, &e.executionID, &maxIterations)
}

// IterationStarted begins one inner turn: it records the iteration for the
// streaming adapter, announces the node_loop_iteration frame with the queen's
// phase, follows it with the iteration_start context readout, and clears the
// previous request's message count so the diagnostics never reuse it.
//
// Parity: framework/agent_loop/agent_loop.py::_publish_iteration
// Parity: framework/agent_loop/internals/event_publishing.py::publish_context_usage
func (e *chatTurnEmitter) IterationStarted(iteration int) {
	if e == nil {
		return
	}
	e.requestMessages = 0
	if e.stream != nil {
		e.stream.noteIteration(iteration)
	}
	// Upstream stamps every node_loop_iteration with the queen's phase (the
	// loop's iteration_metadata_provider) and follows it with a context readout
	// for the iteration that is about to run.
	extra := map[string]any{}
	if e.phase != nil {
		if phase := e.phase(); phase != "" {
			extra["phase"] = phase
		}
	}
	if e.opening {
		// The session-start block already carries iteration 0, so the first
		// inner turn adds no second announcement; later inner turns of the
		// opening turn (a tool call came back) rebase to upstream's 0-based
		// count like the delta frames do.
		if iteration <= 1 {
			return
		}
		rebased := iteration - 1
		e.bus.EmitNodeLoopIteration(e.streamID, e.nodeID, rebased, &e.executionID, extra)
		e.emitContextUsage("iteration_start")
		return
	}
	e.bus.EmitNodeLoopIteration(e.streamID, e.nodeID, iteration, &e.executionID, extra)
	e.emitContextUsage("iteration_start")
}

// emitContextUsage publishes the per-iteration context readout upstream's
// loop emits before each LLM call.
func (e *chatTurnEmitter) emitContextUsage(trigger string) {
	if e == nil || e.contextUsage == nil {
		return
	}
	payload := e.contextUsage(trigger)
	if payload == nil {
		return
	}
	event := eventbus.NewAgentEvent(eventbus.EventContextUsageUpdated, e.streamID)
	event.NodeID = eventbus.StrPtr(e.nodeID)
	event.Data = payload
	e.bus.Publish(event)
}

// ToolResultStored publishes the per-tool context readout upstream emits right
// after one tool result lands in the conversation
// (publish_context_usage trigger=post_tool_call). It is the optional
// agentloop.ToolBatchContextObserver seam; the loop calls it after the
// matching tool_call_completed, so a subscriber sees the pair in call order.
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (post_tool_call)
func (e *chatTurnEmitter) ToolResultStored() {
	e.emitContextUsage("post_tool_call")
}

// ToolBatchStored publishes the batch-end context readout upstream emits once
// every result of a tool batch is stored
// (publish_context_usage trigger=post_tool_results).
//
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (post_tool_results)
func (e *chatTurnEmitter) ToolBatchStored() {
	e.emitContextUsage("post_tool_results")
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_text_delta
// (queen streams publish client_output_delta, not llm_text_delta)
func (e *chatTurnEmitter) AssistantText(content, snapshot string, iteration int) {
	if e == nil {
		return
	}
	// The streaming adapter already published this iteration's text frame by
	// frame; republishing the full message would duplicate the bubble. The
	// adapter reports the loop's raw iteration numbering, so the dedupe check
	// uses it before StreamTextDelta rebases the opening turn to 0-based.
	if e.stream != nil && e.stream.textStreamed(iteration) {
		return
	}
	e.StreamTextDelta(content, snapshot, iteration)
}

// StreamTextDelta publishes one text frame. It is the sink the streaming
// adapter calls for each upstream increment (Content is that increment and
// Snapshot the accumulated answer).
func (e *chatTurnEmitter) StreamTextDelta(content, snapshot string, iteration int) {
	if e == nil {
		return
	}
	if e.opening {
		// Upstream counts the queen loop's first iteration as 0; the Go loop
		// numbers inner turns from 1.
		iteration--
	}
	e.bus.EmitClientOutputDelta(e.streamID, e.nodeID, content, snapshot, &e.executionID, &iteration, 0)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_llm_turn_complete
func (e *chatTurnEmitter) LLMTurnComplete(completion llm.Completion, iteration int) {
	if e == nil {
		return
	}
	if e.opening {
		iteration--
	}
	model := completion.Model
	if model == "" {
		model = e.model
	}
	prefixSHA, suffixSHA, historyAnchor, messageCount := e.turnDiagnostics(model)
	e.bus.EmitLLMTurnComplete(e.streamID, e.nodeID,
		completion.StopReason, model,
		completion.InputTokens, completion.OutputTokens,
		completion.CachedTokens, completion.CacheCreationTokens,
		completion.CostUSD, nil,
		&e.executionID, &iteration, prefixSHA, suffixSHA, historyAnchor, messageCount)
}

// NoteRequestMessages records the size of the request the provider is about to
// receive (the loop's RequestMessageRecorder seam). llm_turn_complete is
// published after the tool batch, so the conversation has already absorbed
// this iteration's assistant and tool messages by then; the diagnostics must
// report the request upstream computed them from.
//
// Parity: framework/agent_loop/agent_loop.py::execute (_compute_turn_diagnostics)
func (e *chatTurnEmitter) NoteRequestMessages(count int) {
	if e == nil {
		return
	}
	e.requestMessages = count
}

// turnDiagnostics mirrors the upstream per-turn cache fingerprints published
// on llm_turn_complete: 12-char sha256 of the static system prefix and the
// dynamic suffix, the rolling history cache breakpoint index (none here, so
// -1), and the number of request messages the provider received.
//
// Parity: framework/agent_loop/agent_loop.py::_compute_turn_diagnostics
func (e *chatTurnEmitter) turnDiagnostics(model string) (prefixSHA, suffixSHA *string, historyAnchor, messageCount *int) {
	if e == nil || e.conv == nil {
		return nil, nil, nil, nil
	}
	prefix := sha256Hex12(e.conv.SystemPromptStatic())
	suffix := sha256Hex12(e.conv.SystemPromptDynamicSuffix())
	anchor := -1
	count := e.requestMessages
	if count <= 0 {
		// A caller that never reported the request (an auxiliary path) falls
		// back to the live conversation size.
		count = len(e.conv.ToLLMMessages(model))
	}
	return &prefix, &suffix, &anchor, &count
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_started
func (e *chatTurnEmitter) ToolCallStarted(call agentloop.ToolCallRequest) {
	if e == nil {
		return
	}
	e.bus.EmitToolCallStarted(e.streamID, e.nodeID, call.ID, call.Name, call.Input, &e.executionID)
}

// Parity: framework/agent_loop/internals/event_publishing.py::publish_tool_completed
func (e *chatTurnEmitter) ToolCallCompleted(call agentloop.ToolCallRequest, result agentloop.ToolResult) {
	if e == nil {
		return
	}
	e.bus.EmitToolCallCompleted(e.streamID, e.nodeID, call.ID, call.Name, result.Content, result.IsError, &e.executionID)
}

// executionCompleted closes a successful turn. loop_state_changed lands last:
// the dashboard clears its "Working…" spinner on it (and on
// execution_completed), and the snapshot then reports idle instead of busy.
// parkReason distinguishes a plain end-of-turn from a turn that parked on an
// ask_user question.
//
// The park announces client_input_requested once, at the turn boundary, as
// upstream's _await_user_input does on every wait: a plain turn_done park
// questionless, an ask_user park carrying the questions it waited on. A
// credential-form park is the exception — upstream announces
// client_credential_form_requested instead, which its producer already
// published.
func (e *chatTurnEmitter) executionCompleted(iterations int, parkReason string, questions []map[string]any) {
	if e == nil {
		return
	}
	if parkReason == "" {
		parkReason = string(agentloop.ParkReasonTurnDone)
	}
	if e.opening {
		// The persistent queen loop publishes no per-iteration execution
		// envelope; its first iteration ends at the same park boundary the
		// dashboard sees on every later turn.
		e.bus.EmitClientInputRequested(e.streamID, e.nodeID, &e.executionID, questions, &parkReason)
		e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "awaiting_user", &e.executionID, &parkReason, nil, nil)
		return
	}
	e.bus.EmitNodeLoopCompleted(e.streamID, e.nodeID, iterations, &e.executionID)
	if parkAnnouncesClientRequest(parkReason) {
		e.bus.EmitClientInputRequested(e.streamID, e.nodeID, &e.executionID, questions, &parkReason)
	}
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "awaiting_user", &e.executionID, &parkReason, nil, nil)
	e.bus.EmitExecutionCompleted(e.streamID, e.executionID,
		map[string]any{"status": "completed"}, nil, nil)
}

// parkAnnouncesClientRequest reports whether the park boundary itself
// announces client_input_requested (upstream's _await_user_input): every wait
// does, except the ones that announce a richer variant in its place. A
// credential_form park announces client_credential_form_requested (published
// by its producer), a colony_suggestion park announces
// colony_suggestion_requested, and a worker's awaiting_queen wait is silent
// (emit_client_request=False upstream).
func parkAnnouncesClientRequest(parkReason string) bool {
	switch parkReason {
	case string(agentloop.ParkReasonCredentialForm),
		string(agentloop.ParkReasonColonySuggestion),
		string(agentloop.ParkReasonAwaitingQueen):
		return false
	}
	return true
}

// executionFailed makes a broken turn visible: execution_failed carries the
// error text (the dashboard renders it as a system bubble from history) and
// the loop state drops out of EXECUTING so the spinner stops. The park reason
// is the one the TurnResult carries when the loop raised its own park (a doom
// loop); an ordinary error defaults to llm_error. Like upstream's broken-park
// wait, it announces client_input_requested first so the snapshot carries the
// unresolved request the user must answer.
func (e *chatTurnEmitter) executionFailed(err error, parkReason agentloop.ParkReason) {
	if e == nil || err == nil {
		return
	}
	if parkReason == "" {
		parkReason = agentloop.ParkReasonLLMError
	}
	message := err.Error()
	reason := string(parkReason)
	e.bus.EmitClientInputRequested(e.streamID, e.nodeID, &e.executionID, nil, &reason)
	e.bus.EmitLoopStateChanged(e.streamID, e.nodeID, "interrupted", &e.executionID, &reason, nil, nil)
	e.bus.EmitExecutionFailed(e.streamID, e.executionID, message, nil, nil)
}
