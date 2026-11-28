package colony

// Streaming LLM wiring for colony loops.
//
// Upstream drives every model call through a streaming task guarded by the
// TTFT / inter-event watchdog, so a stalled worker or overseer stream trips a
// STREAM_STALLED continue-nudge that re-streams the same inner turn. The Go
// loop's only LLM seam is Completer.AComplete, so the watchdog rides a
// completer adapter (agentloop.StreamingCompleter) around the provider. This
// file installs it on the worker/overseer AgentContext and exposes its live
// state to the loop's reminder signals.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (stream
// watchdog + continue-nudge recovery).
// Parity: core/framework/host/colony_runtime.py (worker/overseer AgentLoop).

import (
	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// wireStreamingLLM wraps the worker's conversation completer when the provider
// supports streaming, and returns the adapter so the loop's signals provider
// can read live stream state. A non-streaming provider is left untouched.
func (r *ColonyRuntime) wireStreamingLLM(worker *Worker, cfg agentloop.LoopConfig) *agentloop.StreamingCompleter {
	if worker == nil || worker.Context == nil || worker.Context.LLM == nil {
		return nil
	}
	completer := agentloop.NewStreamingCompleterForConfig(worker.Context.LLM, cfg)
	if completer == nil {
		return nil
	}
	// When the conversation is attached, only its call streams: compaction and
	// other auxiliary calls share the completer but carry a different system
	// prompt, and their events must not replace the turn's stream state. A
	// chat-queen completer that already carries the server's stream adapter
	// does not expose StreamCompleter, so it is left un-double-wrapped.
	if worker.conversation != nil {
		completer.ScopeToSystemPrompt(worker.conversation.SystemPrompt())
	}
	// Billing gate: colony workers and the overseer share one provider, so the
	// per-worker stream/execution identity is stamped on this adapter instead
	// of the provider (writing it on the shared provider would race and could
	// attribute one worker's 402 to another). A classified payment_required
	// then reaches the colony bus under this worker's own stream.
	//
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
	// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)
	if r.bus != nil {
		streamID := worker.StreamID
		if streamID == "" {
			streamID = worker.ID
		}
		var executionID *string
		if id := workerExecutionID(worker); id != "" {
			executionID = &id
		}
		completer.SetPaymentRequiredSink(r.bus, streamID, executionID)
	}
	worker.Context.LLM = completer
	return completer
}

// loopSignalsSnapshot overlays the adapter's live stream state on a base
// snapshot. The base values remain the fallback when no streaming adapter is
// installed (or the provider cannot stream).
func loopSignalsSnapshot(base agentloop.LoopSignals, completer *agentloop.StreamingCompleter) *agentloop.LoopSignals {
	if completer != nil {
		base.StreamActive = completer.StreamActive()
		base.FirstEventSeen = completer.FirstEventSeen()
	}
	return &base
}
