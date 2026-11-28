package server

// Payment-required wiring for the chat stream path.
//
// The llm layer classifies a non-2xx stream as payment_required and calls
// Provider.PaymentRequiredSink, but nothing installed the sink or the
// stream/execution ids, so a 402 reached the client as a generic
// execution_failed (the report's P0-12 residual). These tests pin the runtime
// wiring: the turn's sink/identity reaches the provider, exactly one
// payment_required frame lands on the session bus, and transports that cannot
// carry a provider sink (or the non-streaming fallback) still publish.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func newPaymentRequiredServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(writer, `{"error":{"message":"team has no active plan","type":"payment_required"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func paymentRequiredEvents(bus *eventbus.EventBus) []*eventbus.AgentEvent {
	return bus.GetHistory(eventbus.WithHistoryType(eventbus.EventPaymentRequired))
}

func assertPaymentRequiredEvent(t *testing.T, bus *eventbus.EventBus, wantStream, wantExecution string) {
	t.Helper()
	events := paymentRequiredEvents(bus)
	if len(events) != 1 {
		t.Fatalf("payment_required events = %d, want exactly 1", len(events))
	}
	event := events[0]
	if event.StreamID != wantStream {
		t.Fatalf("payment_required stream = %q, want %q", event.StreamID, wantStream)
	}
	if event.ExecutionID == nil || *event.ExecutionID != wantExecution {
		t.Fatalf("payment_required execution = %v, want %q", event.ExecutionID, wantExecution)
	}
	status, ok := event.Data["upstream_status"].(int)
	if !ok || status != http.StatusPaymentRequired {
		t.Fatalf("payment_required upstream_status = %#v, want 402", event.Data["upstream_status"])
	}
	if message, _ := event.Data["message"].(string); message == "" {
		t.Fatal("payment_required carried no message")
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (the turn's
// stream identity is installed on the provider, so a 402 publishes the
// dedicated billing-gate event instead of only execution_failed).
func TestChatStreamTurnInstallsPaymentRequiredSink(t *testing.T) {
	server := newPaymentRequiredServer(t)
	provider := llm.NewHTTPProvider(llm.ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "test-key",
		APIBase: server.URL,
	})
	bus := eventbus.NewEventBus()
	completer := newChatStreamingCompleter(provider)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}

	completer.beginTurn("You are the queen.", func(string, string, int) {})
	defer completer.endTurn()
	completer.setPaymentRequiredSink(bus, chatStreamID, "exec-1")

	if provider.PaymentRequiredSink == nil {
		t.Fatal("provider.PaymentRequiredSink = nil, want the session bus")
	}
	if provider.PaymentRequiredStreamID != chatStreamID {
		t.Fatalf("provider stream id = %q, want %q", provider.PaymentRequiredStreamID, chatStreamID)
	}
	if provider.PaymentRequiredExecutionID == nil || *provider.PaymentRequiredExecutionID != "exec-1" {
		t.Fatalf("provider execution id = %v, want exec-1", provider.PaymentRequiredExecutionID)
	}

	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{
		System:   "You are the queen.",
		Messages: []llm.ChatMessage{{Role: "user", Content: "hi"}},
	}); err == nil {
		t.Fatal("AComplete succeeded on a 402 response")
	}

	// The provider published from inside its transport; the adapter must not
	// add a second frame.
	assertPaymentRequiredEvent(t, bus, chatStreamID, "exec-1")

	// The execution is over: routing keeps the sink but drops the finished
	// execution id, so a later auxiliary 402 is not misattributed.
	completer.endTurn()
	if provider.PaymentRequiredSink == nil {
		t.Fatal("provider sink cleared at endTurn, want it retained for post-turn calls")
	}
	if provider.PaymentRequiredExecutionID != nil {
		t.Fatalf("provider execution id = %v after endTurn, want nil", provider.PaymentRequiredExecutionID)
	}
}

// paymentStreamProbe is a streaming transport that is not an *llm.Provider, so
// the provider cannot publish the billing gate itself.
type paymentStreamProbe struct {
	err    error
	events []llm.StreamEvent
}

func (p *paymentStreamProbe) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, p.err
}

func (p *paymentStreamProbe) ACompleteStream(_ context.Context, _ llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	for _, event := range p.events {
		emit(event)
	}
	return llm.Completion{}, p.err
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (transports
// that emit the classified frame themselves still reach the bus).
func TestChatStreamPublishesWhenProviderCannotCarrySink(t *testing.T) {
	errorType := llm.LLMErrorTypePaymentRequired
	status := http.StatusPaymentRequired
	probe := &paymentStreamProbe{
		events: []llm.StreamEvent{llm.StreamErrorEvent{
			Error:          "team has no active plan",
			ErrorType:      &errorType,
			UpstreamStatus: &status,
		}},
		err: &llm.ProviderError{Message: "team has no active plan", StatusCode: http.StatusPaymentRequired},
	}
	bus := eventbus.NewEventBus()
	completer := newChatStreamingCompleter(probe)
	if completer == nil {
		t.Fatal("streaming transport was not wrapped")
	}
	completer.beginTurn("You are the queen.", func(string, string, int) {})
	defer completer.endTurn()
	completer.setPaymentRequiredSink(bus, chatStreamID, "exec-2")

	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "You are the queen."}); err == nil {
		t.Fatal("AComplete succeeded on a 402 stream")
	}
	assertPaymentRequiredEvent(t, bus, chatStreamID, "exec-2")
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (a transport
// that cannot stream falls back, and its 402 still publishes the billing gate).
func TestChatStreamPublishesPaymentRequiredOnNonStreamFallback(t *testing.T) {
	bus := eventbus.NewEventBus()
	// The stream call reports ErrStreamingUnsupported, so the adapter takes
	// the non-streaming fallback, whose call then fails with the 402.
	probe := &unsupportedThenPaymentProbe{
		err: &llm.ProviderError{Message: "team has no active plan", StatusCode: http.StatusPaymentRequired},
	}
	completer := newChatStreamingCompleter(probe)
	if completer == nil {
		t.Fatal("streaming transport was not wrapped")
	}
	completer.beginTurn("You are the queen.", func(string, string, int) {})
	defer completer.endTurn()
	completer.setPaymentRequiredSink(bus, chatStreamID, "exec-3")

	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{System: "You are the queen."}); err == nil {
		t.Fatal("AComplete succeeded when both the stream and the fallback failed")
	}
	assertPaymentRequiredEvent(t, bus, chatStreamID, "exec-3")
}

// unsupportedThenPaymentProbe reports ErrStreamingUnsupported on the streaming
// call and a classified 402 on the non-streaming fallback.
type unsupportedThenPaymentProbe struct {
	err error
}

func (p *unsupportedThenPaymentProbe) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, p.err
}

func (p *unsupportedThenPaymentProbe) ACompleteStream(context.Context, llm.CompletionRequest, func(llm.StreamEvent)) (llm.Completion, error) {
	return llm.Completion{}, llm.ErrStreamingUnsupported
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (a real turn
// publishes payment_required under the execution_started id, before the
// execution_failed the failed turn already emits).
func TestChatRuntimeTurnPublishesPaymentRequiredOn402(t *testing.T) {
	server := newPaymentRequiredServer(t)
	provider := llm.NewHTTPProvider(llm.ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "test-key",
		APIBase: server.URL,
	})
	bus := eventbus.NewEventBus()
	recorder := recordChatEvents(bus)
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       provider,
		Root:      t.TempDir(),
		Bus:       bus,
		SessionID: "session_20260101_000000_pay402",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	defer runtime.Close()

	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	payments := waitForChatEvents(t, recorder, eventbus.EventPaymentRequired, 1)
	if len(payments) != 1 {
		t.Fatalf("payment_required events = %d, want 1", len(payments))
	}
	failed := waitForChatEvents(t, recorder, eventbus.EventExecutionFailed, 1)
	started := recorder.ofType(eventbus.EventExecutionStarted)
	if len(started) != 1 {
		t.Fatalf("execution_started events = %d, want 1", len(started))
	}
	payment := payments[0]
	if payment.StreamID != chatStreamID {
		t.Fatalf("payment_required stream = %q, want %q", payment.StreamID, chatStreamID)
	}
	if payment.ExecutionID == nil || started[0].ExecutionID == nil ||
		*payment.ExecutionID != *started[0].ExecutionID {
		t.Fatalf("payment_required execution = %v, want the turn's %v",
			payment.ExecutionID, started[0].ExecutionID)
	}
	if failed[0].ExecutionID == nil || *failed[0].ExecutionID != *payment.ExecutionID {
		t.Fatalf("execution_failed execution = %v, want %v", failed[0].ExecutionID, payment.ExecutionID)
	}
	status, ok := payment.Data["upstream_status"].(int)
	if !ok || status != http.StatusPaymentRequired {
		t.Fatalf("payment_required upstream_status = %#v, want 402", payment.Data["upstream_status"])
	}
}
