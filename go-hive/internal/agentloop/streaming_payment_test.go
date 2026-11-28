package agentloop

// Billing-gate wiring for the reusable streaming completer.
//
// Colony workers and the overseer stream through StreamingCompleter with a
// shared provider, so the per-worker stream/execution identity cannot be
// stamped on the provider (one worker's 402 would be attributed to another).
// The adapter owns the identity instead: it forwards the classified terminal
// frame to the installed sink at the event boundary, and the non-streaming
// fallback is classified from the returned error. These tests pin that
// contract.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
// (StreamErrorEvent error_type=="payment_required" -> emit_payment_required)

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type paymentRecord struct {
	streamID  string
	execution *string
	message   string
	status    *int
}

type recordingPaymentSink struct {
	mu    sync.Mutex
	calls []paymentRecord
}

func (r *recordingPaymentSink) EmitPaymentRequired(streamID string, executionID *string, message *string, upstreamStatus ...*int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := paymentRecord{streamID: streamID, execution: executionID}
	if message != nil {
		record.message = *message
	}
	if len(upstreamStatus) > 0 {
		record.status = upstreamStatus[0]
	}
	r.calls = append(r.calls, record)
}

func (r *recordingPaymentSink) snapshot() []paymentRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]paymentRecord(nil), r.calls...)
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (the
// classified terminal frame carries the billing gate to the session bus with
// this stream's identity).
func TestStreamingCompleterForwardsPaymentRequiredStreamError(t *testing.T) {
	errorType := llm.LLMErrorTypePaymentRequired
	status := http.StatusPaymentRequired
	provider := &scriptedStreamCompleter{scenarios: []streamScenario{{
		events: []llm.StreamEvent{llm.StreamErrorEvent{
			Error:          "team has no active plan",
			ErrorType:      &errorType,
			UpstreamStatus: &status,
		}},
		err: &llm.ProviderError{Message: "team has no active plan", StatusCode: http.StatusPaymentRequired},
	}}}
	completer := NewStreamingCompleter(provider, time.Second, time.Second)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}
	sink := &recordingPaymentSink{}
	executionID := "exec-9"
	completer.SetPaymentRequiredSink(sink, "worker:1", &executionID)

	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("AComplete succeeded on a 402 stream")
	}
	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("payment events = %d, want exactly 1", len(calls))
	}
	if calls[0].streamID != "worker:1" || calls[0].execution == nil || *calls[0].execution != "exec-9" {
		t.Fatalf("payment routing = %#v, want worker:1/exec-9", calls[0])
	}
	if calls[0].status == nil || *calls[0].status != http.StatusPaymentRequired {
		t.Fatalf("payment status = %v, want 402", calls[0].status)
	}
	if calls[0].message == "" {
		t.Fatal("payment event carried no message")
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (only a
// non-recoverable payment_required frame reopens the upgrade popup).
func TestStreamingCompleterIgnoresNonPaymentAndRecoverableErrors(t *testing.T) {
	status502 := http.StatusBadGateway
	status401 := http.StatusUnauthorized
	provider := &scriptedStreamCompleter{scenarios: []streamScenario{
		{
			events: []llm.StreamEvent{llm.StreamErrorEvent{
				Error:          "gateway down",
				Recoverable:    true,
				UpstreamStatus: &status502,
			}},
			err: &llm.ProviderError{Message: "gateway down", StatusCode: http.StatusBadGateway},
		},
		{
			events: []llm.StreamEvent{llm.StreamErrorEvent{
				Error:          "bad key",
				UpstreamStatus: &status401,
			}},
			err: &llm.ProviderError{Message: "bad key", StatusCode: http.StatusUnauthorized},
		},
	}}
	completer := NewStreamingCompleter(provider, time.Second, time.Second)
	sink := &recordingPaymentSink{}
	executionID := "exec-9"
	completer.SetPaymentRequiredSink(sink, "worker:1", &executionID)

	for call := 0; call < 2; call++ {
		if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{}); err == nil {
			t.Fatalf("call %d succeeded on an error stream", call+1)
		}
	}
	if calls := sink.snapshot(); len(calls) != 0 {
		t.Fatalf("payment events = %#v, want none for non-billing failures", calls)
	}
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (a transport
// that cannot stream falls back, and its 402 still reaches the bus).
func TestStreamingCompleterForwardsPaymentRequiredOnNonStreamFallback(t *testing.T) {
	provider := &unsupportedPaymentCompleter{
		err: &llm.ProviderError{Message: "team has no active plan", StatusCode: http.StatusPaymentRequired},
	}
	completer := NewStreamingCompleter(provider, time.Second, time.Second)
	if completer == nil {
		t.Fatal("streaming provider was not wrapped")
	}
	sink := &recordingPaymentSink{}
	executionID := "exec-10"
	completer.SetPaymentRequiredSink(sink, "worker:2", &executionID)

	if _, err := completer.AComplete(context.Background(), llm.CompletionRequest{}); err == nil {
		t.Fatal("AComplete succeeded when both the stream and the fallback failed")
	}
	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("payment events = %d, want exactly 1", len(calls))
	}
	if calls[0].streamID != "worker:2" || calls[0].execution == nil || *calls[0].execution != "exec-10" {
		t.Fatalf("payment routing = %#v, want worker:2/exec-10", calls[0])
	}
}

// unsupportedPaymentCompleter reports ErrStreamingUnsupported on the stream and
// a classified failure on the non-streaming fallback.
type unsupportedPaymentCompleter struct {
	err error
}

func (p *unsupportedPaymentCompleter) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, p.err
}

func (p *unsupportedPaymentCompleter) ACompleteStream(context.Context, llm.CompletionRequest, func(llm.StreamEvent)) (llm.Completion, error) {
	return llm.Completion{}, llm.ErrStreamingUnsupported
}

// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (the session
// runtime installs the sink and ids on the provider it owns).
func TestInstallProviderPaymentRequiredSink(t *testing.T) {
	provider := llm.NewHTTPProvider(llm.ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key"})
	sink := &recordingPaymentSink{}
	executionID := "exec-11"
	target := PaymentRequiredTarget{Sink: sink, StreamID: "queen", ExecutionID: &executionID}

	if !InstallProviderPaymentRequiredSink(provider, target) {
		t.Fatal("InstallProviderPaymentRequiredSink reported no provider")
	}
	if provider.PaymentRequiredSink != sink {
		t.Fatalf("provider sink = %#v, want the installed sink", provider.PaymentRequiredSink)
	}
	if provider.PaymentRequiredStreamID != "queen" {
		t.Fatalf("provider stream id = %q, want queen", provider.PaymentRequiredStreamID)
	}
	if provider.PaymentRequiredExecutionID != &executionID {
		t.Fatal("provider execution id did not receive the turn's execution")
	}

	if InstallProviderPaymentRequiredSink(&scriptedCompleter{}, target) {
		t.Fatal("a non-*llm.Provider completer reported an installed sink")
	}
	if InstallProviderPaymentRequiredSink(nil, target) {
		t.Fatal("a nil completer reported an installed sink")
	}
}
