package agentloop

// Reusable streaming adapter for loops whose LLM seam is Completer.AComplete.
//
// Upstream's _run_turn_loop drives every model call through a streaming task
// guarded by a TTFT / inter-event watchdog; a trip cancels the stream, reports
// the verdict and re-streams the same inner turn behind a continue-nudge. The
// Go agent loop has a single llm.Completer seam, so the watchdog has to live
// inside a completer adapter. The chat queen owns a server-side adapter with a
// client-facing delta sink (internal/server/chat_stream.go); this transport-only
// adapter gives colony workers and the overseer the same watchdog without
// importing the server package.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (the
// "ttft" / "inactive" watchdog verdicts and the LLM stream budget defaults).
// Parity: core/framework/agent_loop/internals/types.py::LoopConfig
// llm_stream_ttft_timeout_seconds / llm_stream_inter_event_idle_seconds.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// LoopConfig defaults for the LLM stream watchdog. A slow first token is a
// different failure than a dead stream, so the TTFT budget is the looser one.
//
// Parity: core/framework/agent_loop/internals/types.py::LoopConfig.llm_stream_ttft_timeout_seconds,
// ::LoopConfig.llm_stream_inter_event_idle_seconds,
// ::LoopConfig.llm_stream_inactivity_timeout_seconds.
const (
	DefaultLLMStreamTTFTTimeoutSeconds       = 600.0
	DefaultLLMStreamInterEventIdleSeconds    = 120.0
	DefaultLLMStreamInactivityTimeoutSeconds = 120.0
)

// StreamWatchdogBudgets resolves a loop config's stream budgets, applying the
// deprecated inactivity alias exactly like the upstream loop: a positive
// `LLMStreamInactivityTimeoutSeconds` below the inter-event budget overrides
// it. Zero disables that budget.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (budget
// resolution with the `_legacy` back-compat rule).
func StreamWatchdogBudgets(config LoopConfig) (time.Duration, time.Duration) {
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

// llmStreamWatchdog trips when a streaming completion produces no event inside
// the TTFT budget, or goes silent for the inter-event budget after the first
// event. It mirrors the upstream loop's verdicts and the chat adapter's
// watchdog; the two budgets stay separate so a slow first token is never
// mistaken for a live stream that went quiet.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (the streaming
// watchdog poll: `_first_event_at is None` -> ttft, else inter-event idle).
type llmStreamWatchdog struct {
	mu         sync.Mutex
	ttft       time.Duration
	interEvent time.Duration
	lastEvent  time.Time
	startedAt  time.Time
	done       bool
	stalled    *llm.StreamStalledError
	timer      *time.Timer
	cancel     context.CancelFunc
}

func newLLMStreamWatchdog(cancel context.CancelFunc, ttft, interEvent time.Duration) *llmStreamWatchdog {
	return &llmStreamWatchdog{cancel: cancel, ttft: ttft, interEvent: interEvent, startedAt: time.Now()}
}

// start arms the TTFT budget. A no-op when every budget is disabled.
func (w *llmStreamWatchdog) start() {
	w.arm("ttft", w.ttft, w.startedAt)
}

// event records one stream event and re-arms the inter-event budget.
func (w *llmStreamWatchdog) event() {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return
	}
	w.lastEvent = time.Now()
	w.mu.Unlock()
	w.arm("inactive", w.interEvent, time.Time{})
}

// finish marks the stream complete: a timer that fires after this is ignored
// (the completion is already in hand).
func (w *llmStreamWatchdog) finish() {
	w.mu.Lock()
	w.done = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
}

// stalledError returns the verdict when the watchdog tripped, else nil.
func (w *llmStreamWatchdog) stalledError() *llm.StreamStalledError {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stalled
}

func (w *llmStreamWatchdog) arm(reason string, limit time.Duration, base time.Time) {
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

func (w *llmStreamWatchdog) trip(reason string, limit time.Duration, base time.Time) {
	w.mu.Lock()
	if w.done || w.stalled != nil {
		w.mu.Unlock()
		return
	}
	w.stalled = &llm.StreamStalledError{
		Reason:         reason,
		ElapsedSeconds: time.Since(base).Seconds(),
		LimitSeconds:   limit.Seconds(),
	}
	w.mu.Unlock()
	// Cancel the in-flight stream so the transport unwinds; the caller reports
	// the verdict instead of the cancellation error.
	w.cancel()
}

// StreamingCompleter adapts a streaming provider to the loop's Completer seam
// while applying the upstream LLM stream watchdog. A trip cancels the stream
// and returns *llm.StreamStalledError, which AgentLoop.injectStreamStallNudge
// turns into the STREAM_STALLED continue-nudge.
//
// A provider that cannot stream is left untouched: NewStreamingCompleter
// returns nil, so the non-streaming path is byte-for-byte unchanged. A
// streaming-capable transport that reports llm.ErrStreamingUnsupported falls
// back to non-streaming for this and every later call, exactly like the chat
// adapter.
type StreamingCompleter struct {
	completer llm.Completer
	streamer  llm.StreamCompleter

	mu               sync.Mutex
	ttftBudget       time.Duration
	interEventBudget time.Duration
	// scoped pins streaming to the conversation call. Auxiliary calls
	// (compaction) share the completer but carry a different system prompt and
	// must not replace the turn's stream state.
	scoped       bool
	systemPrompt string
	// disabled latches after a transport reports ErrStreamingUnsupported.
	disabled bool
	// active is true between the start and end of a streamed call.
	active bool
	// firstEvent is reset when a streamed call starts and set by its first
	// event, mirroring upstream's `_stream_first_event_at`.
	firstEvent bool
	// payment is the billing-gate routing the host installed. A classified
	// 402 is forwarded to it with this stream/execution identity so the SSE
	// feed gets payment_required instead of only a generic turn failure.
	payment PaymentRequiredTarget
}

// NewStreamingCompleter wraps completer when it supports streaming. It returns
// nil otherwise so callers can keep the original completer unchanged. Wrapping
// an existing StreamingCompleter only refreshes its budgets, so repeated
// wiring cannot stack watchdogs.
func NewStreamingCompleter(completer llm.Completer, ttft, interEvent time.Duration) *StreamingCompleter {
	if completer == nil {
		return nil
	}
	if existing, ok := completer.(*StreamingCompleter); ok {
		existing.SetWatchdogBudgets(ttft, interEvent)
		return existing
	}
	streamer, ok := completer.(llm.StreamCompleter)
	if !ok {
		return nil
	}
	return &StreamingCompleter{
		completer:        completer,
		streamer:         streamer,
		ttftBudget:       ttft,
		interEventBudget: interEvent,
	}
}

// NewStreamingCompleterForConfig resolves the loop config's watchdog budgets
// and wraps the completer when it can stream.
func NewStreamingCompleterForConfig(completer llm.Completer, config LoopConfig) *StreamingCompleter {
	ttft, interEvent := StreamWatchdogBudgets(config)
	return NewStreamingCompleter(completer, ttft, interEvent)
}

// SetWatchdogBudgets updates the stream budgets. Zero disables that budget.
func (c *StreamingCompleter) SetWatchdogBudgets(ttft, interEvent time.Duration) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.ttftBudget = ttft
	c.interEventBudget = interEvent
	c.mu.Unlock()
}

// PaymentRequiredTarget is the routing a streaming adapter stamps on the
// billing-gate event: the host's event sink plus the stream and execution the
// failing model call belongs to. A nil execution id is allowed (upstream passes
// `execution_id or None`).
type PaymentRequiredTarget struct {
	Sink        llm.PaymentRequiredEmitter
	StreamID    string
	ExecutionID *string
}

// SetPaymentRequiredSink installs the billing-gate routing for the streams
// this adapter runs. A classified 402 is then forwarded to the session's event
// sink with the adapter's stream/execution identity, so the SSE feed receives
// payment_required instead of only the generic execution failure.
//
// The adapter emits at the stream-event boundary rather than stamping the
// provider: colony workers share one worker provider, so per-worker stream and
// execution ids written onto it would race and could attribute one worker's
// 402 to another. A provider that owns its own sink (the chat queen's
// per-session provider) publishes from inside its transport, and the adapter
// detects that and leaves the frame to it.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)
func (c *StreamingCompleter) SetPaymentRequiredSink(emitter llm.PaymentRequiredEmitter, streamID string, executionID *string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.payment = PaymentRequiredTarget{Sink: emitter, StreamID: streamID, ExecutionID: executionID}
	c.mu.Unlock()
}

// paymentTarget snapshots the installed billing-gate routing.
func (c *StreamingCompleter) paymentTarget() PaymentRequiredTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.payment
}

// ScopeToSystemPrompt pins streaming to the conversation call carrying prompt.
// Calls with any other system prompt (compaction, auxiliary work) go straight
// to the non-streaming path. An empty prompt still scopes: only requests with
// an empty system prompt stream.
func (c *StreamingCompleter) ScopeToSystemPrompt(prompt string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.scoped = true
	c.systemPrompt = prompt
	c.mu.Unlock()
}

// ModelName forwards the provider's model so the loop keeps resolving the same
// model name it did before the adapter existed.
func (c *StreamingCompleter) ModelName() string {
	if c == nil {
		return ""
	}
	if namer, ok := c.completer.(llm.ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

// StreamActive reports whether a streamed call is in flight.
func (c *StreamingCompleter) StreamActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// FirstEventSeen reports whether the current/last streamed call has produced
// an event (upstream's `_stream_first_event_at is not None` snapshot).
func (c *StreamingCompleter) FirstEventSeen() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstEvent
}

// Signals snapshots the adapter's live stream state in the shape the reminder
// hub hands to temporal sources (the idle-nudge slow-TTFT branch).
func (c *StreamingCompleter) Signals() *LoopSignals {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return &LoopSignals{StreamActive: c.active, FirstEventSeen: c.firstEvent}
}

// AComplete streams the (scoped) conversation call under the watchdog and
// returns the same Completion the non-streaming path would have produced.
func (c *StreamingCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if c == nil || c.completer == nil {
		return llm.Completion{}, fmt.Errorf("agentloop: streaming completer is not initialised")
	}

	c.mu.Lock()
	streamThisCall := !c.disabled && (!c.scoped || req.System == c.systemPrompt)
	ttftBudget := c.ttftBudget
	interEventBudget := c.interEventBudget
	if streamThisCall {
		c.active = true
		c.firstEvent = false
	}
	c.mu.Unlock()
	if !streamThisCall {
		return c.completer.AComplete(ctx, req)
	}
	defer func() {
		c.mu.Lock()
		c.active = false
		c.mu.Unlock()
	}()

	// A trip cancels the in-flight stream and reports the verdict as a typed
	// error, which the agent loop turns into the STREAM_STALLED reminder point
	// (continue-nudge recovery).
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	watchdog := newLLMStreamWatchdog(cancelStream, ttftBudget, interEventBudget)
	watchdog.start()

	streamed := false
	var streamError *llm.StreamErrorEvent
	completion, err := c.streamer.ACompleteStream(streamCtx, req, func(event llm.StreamEvent) {
		watchdog.event()
		c.mu.Lock()
		c.firstEvent = true
		c.mu.Unlock()
		if delta, ok := event.(llm.TextDeltaEvent); ok && delta.Content != "" {
			streamed = true
		}
		// Keep the terminal frame: it already carries error_type and
		// upstream_status, so the billing gate does not have to re-parse the
		// returned error. The callback runs synchronously in the stream, the
		// same assumption `streamed` above makes.
		if failure, ok := event.(llm.StreamErrorEvent); ok {
			captured := failure
			streamError = &captured
		}
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
		completion, fallbackErr := c.completer.AComplete(ctx, req)
		if fallbackErr != nil {
			// The non-streaming transport never builds a StreamErrorEvent, so
			// this is the only place its 402 can reach the event bus.
			EmitPaymentRequiredForStreamError(c.paymentTarget(), nil, fallbackErr)
		}
		return completion, fallbackErr
	}
	c.emitStreamPaymentRequired(streamError, err)
	return llm.Completion{}, err
}

// emitStreamPaymentRequired publishes a non-recoverable billing gate the
// provider did not already publish from inside its transport.
func (c *StreamingCompleter) emitStreamPaymentRequired(event *llm.StreamErrorEvent, err error) {
	if c == nil || providerOwnsPaymentRequired(c.completer) {
		return
	}
	EmitPaymentRequiredForStreamError(c.paymentTarget(), event, err)
}

// EmitPaymentRequiredForStreamError forwards a terminal provider failure to the
// installed sink when it classifies as the billing gate, so the desktop client
// can reopen the upgrade popup without parsing the failure string. event is the
// stream's classified terminal frame when the transport produced one; err is
// the call's error, used for the non-streaming fallback and for transports that
// do not classify their frames. It reports whether an event was published.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)
func EmitPaymentRequiredForStreamError(target PaymentRequiredTarget, event *llm.StreamErrorEvent, err error) bool {
	if target.Sink == nil {
		return false
	}
	// Upstream only surfaces the billing gate for a non-recoverable frame; a
	// retryable gateway error is not a plan problem.
	if event != nil && event.Recoverable {
		return false
	}
	message := ""
	errorType := ""
	var status *int
	if event != nil {
		message = event.Error
		if event.ErrorType != nil {
			errorType = *event.ErrorType
		}
		status = event.UpstreamStatus
	}
	if errorType != llm.LLMErrorTypePaymentRequired {
		classifiedType, classifiedStatus := llm.ClassifyLLMError(err)
		if classifiedType == nil {
			return false
		}
		errorType = *classifiedType
		if status == nil {
			status = classifiedStatus
		}
	}
	if errorType != llm.LLMErrorTypePaymentRequired {
		return false
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	target.Sink.EmitPaymentRequired(target.StreamID, target.ExecutionID, &message, status)
	return true
}

// InstallProviderPaymentRequiredSink installs the billing-gate routing on the
// llm.Provider behind completer (resolving a model slot), so the provider's
// transport publishes payment_required itself with the given stream/execution
// identity. It reports whether a provider was found; a transport that is not an
// *llm.Provider keeps the adapter-level emission above.
func InstallProviderPaymentRequiredSink(completer llm.Completer, target PaymentRequiredTarget) bool {
	provider, ok := llm.ResolveCompleter(completer).(*llm.Provider)
	if !ok || provider == nil {
		return false
	}
	provider.PaymentRequiredSink = target.Sink
	provider.PaymentRequiredStreamID = target.StreamID
	provider.PaymentRequiredExecutionID = target.ExecutionID
	return true
}

// providerOwnsPaymentRequired reports whether the provider behind completer
// publishes the billing gate itself. Emitting again from the adapter would
// duplicate the SSE frame.
func providerOwnsPaymentRequired(completer llm.Completer) bool {
	provider, ok := llm.ResolveCompleter(completer).(*llm.Provider)
	return ok && provider != nil && provider.PaymentRequiredSink != nil
}
