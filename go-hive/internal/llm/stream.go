package llm

import (
	"context"
	"errors"
	"fmt"
)

// ErrStreamingUnsupported marks a provider/model pair that has no streaming
// transport (or whose upstream stream is known to be unreliable). Callers fall
// back to Completer.AComplete so the turn behaves exactly like the
// non-streaming path instead of failing.
var ErrStreamingUnsupported = errors.New("llm: streaming is not supported for this provider")

// StreamStalledError reports a completion call whose stream watchdog tripped:
// no event arrived inside the TTFT budget ("ttft"), or the stream went silent
// after producing events ("inactive"). The two budgets mirror the upstream
// agent loop's LLM stream watchdog; the loop turns this error into the
// STREAM_STALLED reminder point instead of failing the turn outright.
//
// Parity: framework/agent_loop/stream_stall.py (the source consumes
// LoopSignals.stall_reason / stall_elapsed; the watchdog that detects the
// condition lives in agent_loop.py's _run_turn_loop).
type StreamStalledError struct {
	Reason         string
	ElapsedSeconds float64
	LimitSeconds   float64
}

func (e *StreamStalledError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "stalled"
	}
	return fmt.Sprintf("llm: stream %s for %.0fs (limit %.0fs)", reason, e.ElapsedSeconds, e.LimitSeconds)
}

// StreamCompleter is the streaming counterpart of Completer. A provider that
// implements it can deliver a completion as incremental events while the
// upstream is still generating.
//
// emit is called synchronously on the calling goroutine, in wire order:
//   - TextDeltaEvent for every text chunk (Content is the increment, Snapshot
//     the accumulated answer so far);
//   - ToolCallEvent once per tool call after its argument fragments have been
//     assembled (upstream sends them one JSON object at the end);
//   - TextEndEvent with the full text before the finish when any text arrived;
//   - FinishEvent with stop reason, usage and cost last.
//
// The returned Completion is the same value the non-streaming path would have
// produced, so callers can persist history from it. An error means the
// completion is incomplete: partial text must not be persisted as a finished
// answer.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.stream
type StreamCompleter interface {
	ACompleteStream(ctx context.Context, req CompletionRequest, emit func(StreamEvent)) (Completion, error)
}

// ACompleteStream lets an AnthropicProvider stream through its underlying
// Messages transport. The provider's own model/base decide routing, so an
// Anthropic-compatible proxy streams exactly like the public API.
//
// Parity: core/framework/llm/anthropic.py::AnthropicProvider.acomplete
func (a *AnthropicProvider) ACompleteStream(ctx context.Context, req CompletionRequest, emit func(StreamEvent)) (Completion, error) {
	if a == nil || a.Provider == nil {
		return Completion{}, fmt.Errorf("llm: anthropic provider is not initialised")
	}
	return a.Provider.ACompleteStream(ctx, req, emit)
}
