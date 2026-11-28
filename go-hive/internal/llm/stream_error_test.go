package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type paymentCall struct {
	streamID  string
	execution *string
	message   string
	status    *int
}

type recordingPaymentEmitter struct {
	mu    sync.Mutex
	calls []paymentCall
}

func (r *recordingPaymentEmitter) EmitPaymentRequired(streamID string, executionID *string, message *string, upstreamStatus ...*int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := ""
	if message != nil {
		text = *message
	}
	var status *int
	if len(upstreamStatus) > 0 {
		status = upstreamStatus[0]
	}
	r.calls = append(r.calls, paymentCall{streamID: streamID, execution: executionID, message: text, status: status})
}

func (r *recordingPaymentEmitter) snapshot() []paymentCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]paymentCall(nil), r.calls...)
}

func newErrorStreamServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}))
}

// Parity: core/framework/llm/litellm.py::_classify_llm_error and
// ::LiteLLMProvider.stream (StreamErrorEvent(error_type/upstream_status))
func TestOpenAIStreamPaymentRequiredEmitsClassifiedError(t *testing.T) {
	server := newErrorStreamServer(http.StatusPaymentRequired,
		`{"error":{"message":"team has no active plan","type":"payment_required"}}`)
	defer server.Close()

	emitter := &recordingPaymentEmitter{}
	executionID := "exec-1"
	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	provider.PaymentRequiredSink = emitter
	provider.PaymentRequiredStreamID = "chat"
	provider.PaymentRequiredExecutionID = &executionID

	var events []StreamEvent
	_, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err == nil {
		t.Fatal("ACompleteStream succeeded on a 402 response")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("error = %v, want a 402 ProviderError", err)
	}

	if len(events) != 1 {
		t.Fatalf("events = %#v, want exactly the stream error", events)
	}
	event, ok := events[0].(StreamErrorEvent)
	if !ok {
		t.Fatalf("event = %T, want StreamErrorEvent", events[0])
	}
	if event.ErrorType == nil || *event.ErrorType != LLMErrorTypePaymentRequired {
		t.Fatalf("error_type = %v, want %q", event.ErrorType, LLMErrorTypePaymentRequired)
	}
	if event.UpstreamStatus == nil || *event.UpstreamStatus != http.StatusPaymentRequired {
		t.Fatalf("upstream_status = %v, want 402", event.UpstreamStatus)
	}
	if event.Recoverable {
		t.Fatal("a 402 must not be marked recoverable")
	}
	if event.Error == "" {
		t.Fatal("stream error carried no message")
	}

	calls := emitter.snapshot()
	if len(calls) != 1 {
		t.Fatalf("EmitPaymentRequired calls = %d, want 1", len(calls))
	}
	if calls[0].streamID != "chat" || calls[0].execution == nil || *calls[0].execution != "exec-1" {
		t.Fatalf("payment event routing = %#v, want chat/exec-1", calls[0])
	}
	if calls[0].status == nil || *calls[0].status != http.StatusPaymentRequired {
		t.Fatalf("payment event status = %v, want 402", calls[0].status)
	}
	if calls[0].message == "" {
		t.Fatal("payment event carried no message")
	}
}

// Parity: core/framework/llm/litellm.py::_classify_llm_error and
// ::LiteLLMProvider.stream (StreamErrorEvent(error_type/upstream_status))
func TestAnthropicStreamNon2xxEmitsClassifiedError(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		wantType    *string
		recoverable bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, wantType: nil, recoverable: false},
		{name: "bad gateway", status: http.StatusBadGateway, wantType: nil, recoverable: true},
	}
	for _, scenario := range tests {
		t.Run(scenario.name, func(t *testing.T) {
			server := newErrorStreamServer(scenario.status, `{"error":{"message":"upstream rejected"}}`)
			defer server.Close()

			provider, err := NewAnthropicProviderWithBase("test-key", "claude-sonnet-4", server.URL)
			if err != nil {
				t.Fatalf("NewAnthropicProviderWithBase: %v", err)
			}
			var events []StreamEvent
			_, err = provider.ACompleteStream(context.Background(),
				CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
				collectStreamEvents(&events))
			if err == nil {
				t.Fatalf("ACompleteStream succeeded on status %d", scenario.status)
			}
			if len(events) != 1 {
				t.Fatalf("events = %#v, want exactly the stream error", events)
			}
			event, ok := events[0].(StreamErrorEvent)
			if !ok {
				t.Fatalf("event = %T, want StreamErrorEvent", events[0])
			}
			if event.UpstreamStatus == nil || *event.UpstreamStatus != scenario.status {
				t.Fatalf("upstream_status = %v, want %d", event.UpstreamStatus, scenario.status)
			}
			if scenario.wantType == nil {
				if event.ErrorType != nil {
					t.Fatalf("error_type = %q, want nil", *event.ErrorType)
				}
			} else if event.ErrorType == nil || *event.ErrorType != *scenario.wantType {
				t.Fatalf("error_type = %v, want %q", event.ErrorType, *scenario.wantType)
			}
			if event.Recoverable != scenario.recoverable {
				t.Fatalf("recoverable = %v, want %v", event.Recoverable, scenario.recoverable)
			}
		})
	}
}

// Parity: core/framework/llm/litellm.py::_classify_llm_error (exhausted 429s
// are reported as rate_limit with the upstream status)
func TestOpenAIStreamExhaustedRateLimitIsClassified(t *testing.T) {
	server := newErrorStreamServer(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)
	defer server.Close()

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL})
	provider.SleepFunc = func(time.Duration) {}

	var events []StreamEvent
	_, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err == nil {
		t.Fatal("ACompleteStream succeeded after exhausted 429 retries")
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want exactly the terminal stream error", events)
	}
	event, ok := events[0].(StreamErrorEvent)
	if !ok {
		t.Fatalf("event = %T, want StreamErrorEvent", events[0])
	}
	if event.ErrorType == nil || *event.ErrorType != LLMErrorTypeRateLimit {
		t.Fatalf("error_type = %v, want %q", event.ErrorType, LLMErrorTypeRateLimit)
	}
	if event.UpstreamStatus == nil || *event.UpstreamStatus != http.StatusTooManyRequests {
		t.Fatalf("upstream_status = %v, want 429", event.UpstreamStatus)
	}
	if event.Recoverable {
		t.Fatal("an exhausted 429 must not be marked recoverable")
	}
}

// Parity: core/framework/llm/litellm.py::_classify_llm_error
func TestClassifyLLMErrorTable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType string
		wantCode int
	}{
		{name: "payment required", err: &ProviderError{StatusCode: 402}, wantType: LLMErrorTypePaymentRequired, wantCode: 402},
		{name: "rate limit", err: &ProviderError{StatusCode: 429}, wantType: LLMErrorTypeRateLimit, wantCode: 429},
		{name: "server error", err: &ProviderError{StatusCode: 500}, wantType: "", wantCode: 500},
		{name: "no status", err: errors.New("transport blew up"), wantType: "", wantCode: 0},
	}
	for _, scenario := range tests {
		t.Run(scenario.name, func(t *testing.T) {
			errorType, status := ClassifyLLMError(scenario.err)
			if scenario.wantType == "" {
				if errorType != nil {
					t.Fatalf("error_type = %q, want nil", *errorType)
				}
			} else if errorType == nil || *errorType != scenario.wantType {
				t.Fatalf("error_type = %v, want %q", errorType, scenario.wantType)
			}
			if scenario.wantCode == 0 {
				if status != nil {
					t.Fatalf("status = %d, want nil", *status)
				}
			} else if status == nil || *status != scenario.wantCode {
				t.Fatalf("status = %v, want %d", status, scenario.wantCode)
			}
		})
	}
}
