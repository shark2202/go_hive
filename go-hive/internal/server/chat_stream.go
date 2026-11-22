package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// streamWatchdog trips when a streaming completion produces no event inside
// the TTFT budget, or goes silent for the inter-event budget after the first
// event. It mirrors the upstream loop's LLM stream watchdog
// (agent_loop.py::_run_turn_loop's _watchdog_verdict); the distinction between
// a slow first token and a stalled live stream is preserved because the two
// budgets are separate.
//
// Parity: framework/agent_loop/agent_loop.py (stream watchdog verdicts
// "ttft" / "inactive").
type streamWatchdog struct {
	mu         sync.Mutex
	ttft       time.Duration
	interEvent time.Duration
	firstEvent bool
	lastEvent  time.Time
	startedAt  time.Time
	done       bool
	stalled    *llm.StreamStalledError
	timer      *time.Timer
	cancel     context.CancelFunc
}

func newStreamWatchdog(cancel context.CancelFunc, ttft, interEvent time.Duration) *streamWatchdog {
	return &streamWatchdog{cancel: cancel, ttft: ttft, interEvent: interEvent, startedAt: time.Now()}
}

// start arms the TTFT budget. A no-op when every budget is disabled.
func (w *streamWatchdog) start() {
	w.arm("ttft", w.ttft, w.startedAt)
}

// event records one stream event and re-arms the inter-event budget.
func (w *streamWatchdog) event() {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return
	}
	w.firstEvent = true
	w.lastEvent = time.Now()
	w.mu.Unlock()
	w.arm("inactive", w.interEvent, time.Time{})
}

// finish marks the stream complete: a timer that fires after this is ignored
// (the completion is already in hand).
func (w *streamWatchdog) finish() {
	w.mu.Lock()
	w.done = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
}

// stalledError returns the verdict when the watchdog tripped, else nil.
func (w *streamWatchdog) stalledError() *llm.StreamStalledError {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stalled
}

func (w *streamWatchdog) arm(reason string, limit time.Duration, base time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done || limit <= 0 {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	if base.IsZero() {
		base = w.lastEvent
	}
	w.timer = time.AfterFunc(limit, func() {
		w.trip(reason, limit, base)
	})
}

func (w *streamWatchdog) trip(reason string, limit time.Duration, base time.Time) {
	w.mu.Lock()
	if w.done || w.stalled != nil {
		w.mu.Unlock()
		return
	}
	if base.IsZero() {
		base = w.startedAt
	}
	w.stalled = &llm.StreamStalledError{
		Reason:         reason,
		ElapsedSeconds: time.Since(base).Seconds(),
		LimitSeconds:   limit.Seconds(),
	}
	w.mu.Unlock()
	// Cancel the in-flight stream so the transport unwinds; the caller
	// reports the verdict instead of the cancellation error.
	w.cancel()
}

// chatStreamingCompleter bridges a streaming provider to the Completer the
// agent loop calls. The loop has one LLM seam (AComplete); when the configured
// provider can stream, this adapter runs the streaming call and forwards each
// text increment to the turn's emitter as a client_output_delta frame.
//
// It only streams the conversation call: compaction and other auxiliary calls
// share the same completer but a different system prompt, and must keep their
// text out of the chat transcript.
type chatStreamingCompleter struct {
	completer llm.Completer
	streamer  llm.StreamCompleter

	mu sync.Mutex
	// streaming is true between beginTurn and endTurn.
	streaming bool
	// disabled latches after the provider reports ErrStreamingUnsupported, so
	// later turns skip the failed streaming attempt.
	disabled bool
	// systemPrompt identifies the conversation call inside a turn.
	systemPrompt string
	// sink publishes one delta; nil means the session has no live event bus.
	sink func(content, snapshot string, iteration int)
	// iteration is set by LoopObserver.IterationStarted, which the loop calls
	// immediately before each LLM request.
	iteration int
	// textIteration is the last iteration whose text was already streamed, so
	// the loop's end-of-call AssistantText does not publish it a second time.
	textIteration int
	// ttftBudget / interEventBudget are the upstream LLM stream watchdog
	// budgets. Zero disables that budget; both zero disables the watchdog and
	// leaves the pre-existing streaming behaviour untouched.
	ttftBudget       time.Duration
	interEventBudget time.Duration
}

// newChatStreamingCompleter returns nil when the provider cannot stream, which
// keeps the non-streaming path byte-for-byte unchanged.
func newChatStreamingCompleter(completer llm.Completer) *chatStreamingCompleter {
	if completer == nil {
		return nil
	}
	streamer, ok := completer.(llm.StreamCompleter)
	if !ok {
		return nil
	}
	return &chatStreamingCompleter{completer: completer, streamer: streamer}
}

// setStreamWatchdog installs the LLM stream watchdog budgets (upstream's
// llm_stream_ttft_timeout_seconds / llm_stream_inter_event_idle_seconds).
// Both zero disables the watchdog.
func (c *chatStreamingCompleter) setStreamWatchdog(ttft, interEvent time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ttftBudget = ttft
	c.interEventBudget = interEvent
	c.mu.Unlock()
}

// streamWatchdogBudgets resolves the loop config's stream budgets, applying
// the legacy inactivity alias exactly like the upstream loop:
// a positive `llm_stream_inactivity_timeout_seconds` below the inter-event
// budget overrides it.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (budget
// resolution with the `_legacy` back-compat rule).
func streamWatchdogBudgets(config agentloop.LoopConfig) (time.Duration, time.Duration) {
	seconds := func(value float64) time.Duration {
		if value <= 0 {
			return 0
		}
		return time.Duration(value * float64(time.Second))
	}
	ttft := seconds(config.LLMStreamTTFTTimeoutSeconds)
	inter := seconds(config.LLMStreamInterEventIdleSeconds)
	if legacy := config.LLMStreamInactivityTimeoutSeconds; legacy > 0 && legacy < config.LLMStreamInterEventIdleSeconds {
		inter = seconds(legacy)
	}
	return ttft, inter
}

// ModelName forwards the provider's model so the loop keeps resolving the same
// model name it did before the adapter existed.
func (c *chatStreamingCompleter) ModelName() string {
	if namer, ok := c.completer.(llm.ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

// beginTurn arms streaming for one turn. systemPrompt is the conversation
// system prompt; only a request carrying it is treated as the model call that
// belongs to the chat transcript.
func (c *chatStreamingCompleter) beginTurn(systemPrompt string, sink func(content, snapshot string, iteration int)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.streaming = true
	c.systemPrompt = systemPrompt
	c.sink = sink
	c.iteration = 0
	c.textIteration = 0
	c.mu.Unlock()
}

func (c *chatStreamingCompleter) endTurn() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.streaming = false
	c.sink = nil
	c.iteration = 0
	c.mu.Unlock()
}

// noteIteration records the loop iteration the next LLM call belongs to.
func (c *chatStreamingCompleter) noteIteration(iteration int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.iteration = iteration
	c.mu.Unlock()
}

// textStreamed reports whether the given iteration's assistant text already
// reached the bus as deltas.
func (c *chatStreamingCompleter) textStreamed(iteration int) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.textIteration != 0 && c.textIteration == iteration
}

// AComplete streams the conversation call and falls back to the non-streaming
// completion when the provider (or an auxiliary call) cannot stream.
func (c *chatStreamingCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if c == nil || c.completer == nil {
		return llm.Completion{}, errors.New("server: chat streaming completer is not initialised")
	}

	c.mu.Lock()
	streamThisCall := !c.disabled && c.streaming && c.sink != nil && req.System == c.systemPrompt
	sink := c.sink
	ttftBudget := c.ttftBudget
	interEventBudget := c.interEventBudget
	c.mu.Unlock()
	if !streamThisCall {
		return c.completer.AComplete(ctx, req)
	}

	// Stream watchdog: a trip cancels the in-flight stream and reports the
	// verdict as a typed error, which the agent loop turns into the
	// STREAM_STALLED reminder point (continue-nudge recovery).
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	watchdog := newStreamWatchdog(cancelStream, ttftBudget, interEventBudget)
	watchdog.start()

	streamed := false
	completion, err := c.streamer.ACompleteStream(streamCtx, req, func(event llm.StreamEvent) {
		watchdog.event()
		delta, ok := event.(llm.TextDeltaEvent)
		if !ok || delta.Content == "" {
			return
		}
		c.mu.Lock()
		iteration := c.iteration
		c.textIteration = iteration
		c.mu.Unlock()
		streamed = true
		sink(delta.Content, delta.Snapshot, iteration)
	})
	watchdog.finish()
	if stalled := watchdog.stalledError(); stalled != nil {
		return llm.Completion{}, stalled
	}
	if err == nil {
		return completion, nil
	}
	if errors.Is(err, llm.ErrStreamingUnsupported) && !streamed {
		// The transport told us up front that it cannot stream; run this call
		// and every later one through the ordinary completion path.
		c.mu.Lock()
		c.disabled = true
		c.mu.Unlock()
		return c.completer.AComplete(ctx, req)
	}
	return llm.Completion{}, err
}

// emitStreamStallDiagnostics publishes the session events upstream emits
// around a continue-nudge: the watchdog verdict (ttft / inactive), the nudge
// itself, and the generic reminder-injected event for the injected block.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (emit_stream_ttft_exceeded / emit_stream_inactive / emit_stream_nudge_sent)
// Parity: framework/agent_loop/agent_loop.py::_emit_reminder_injected.
func (r *ChatRuntime) emitStreamStallDiagnostics(nudge agentloop.StreamStallNudge) {
	if r == nil || r.bus == nil {
		return
	}
	var executionID *string
	if r.agentCtx != nil && r.agentCtx.ExecutionID != "" {
		id := r.agentCtx.ExecutionID
		executionID = &id
	}
	if nudge.Reason == "ttft" {
		r.bus.EmitStreamTTFTExceeded(chatStreamID, chatNodeID, nudge.ElapsedSeconds, nudge.LimitSeconds, executionID)
	} else {
		r.bus.EmitStreamInactive(chatStreamID, chatNodeID, nudge.ElapsedSeconds, nudge.LimitSeconds, executionID)
	}
	r.bus.EmitStreamNudgeSent(chatStreamID, chatNodeID, nudge.Reason, nudge.NudgeCount, executionID)
	r.bus.EmitReminderInjected(chatStreamID, chatNodeID, "stream_stall", nudge.Reason, map[string]any{
		"reason":      nudge.Reason,
		"elapsed":     nudge.ElapsedSeconds,
		"nudge_count": nudge.NudgeCount,
	}, executionID)
}
