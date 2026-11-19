package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/aden-hive/hive-go/internal/ratelimit"
)

// TestMain keeps the suite hermetic: the process-wide call limiter would
// otherwise read the developer's hive.config.json on the first HTTP request.
//
// Parity-exempt: Go test harness.
func TestMain(m *testing.M) {
	ratelimit.SetCallLimiter(nil)
	os.Exit(m.Run())
}

func gateServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id": "chatcmpl-gate",
			"model": "gpt-4o-mini",
			"choices": [{"finish_reason": "stop", "message": {"role": "assistant", "content": "paced"}}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}
		}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// Parity-exempt: the Python LLM client is not paced; RateLimitStage runs in the
// upstream request pipeline instead of around the HTTP call.
func TestHTTPProviderGatePacesBeforeRequest(t *testing.T) {
	var hits int32
	server := gateServer(t, &hits)
	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIBase: server.URL + "/v1"})

	blocked := errors.New("gate closed")
	provider.Gate = func(context.Context) error { return blocked }
	_, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	if !errors.Is(err, blocked) {
		t.Fatalf("Complete = %v, want the gate error", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("gate rejection still reached the server (%d requests)", got)
	}

	provider.Gate = func(context.Context) error { return nil }
	completion, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete after the gate opened: %v", err)
	}
	if completion.Content != "paced" {
		t.Fatalf("content = %q", completion.Content)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("requests = %d, want exactly one", got)
	}
}

// Parity-exempt: Go-only wiring check — the real HTTP transport must carry the
// hive-configured limiter while a transport-less provider stays unlimited.
func TestNewHTTPProviderInstallsLLMGate(t *testing.T) {
	if got := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini"}).Gate; got == nil {
		t.Fatal("NewHTTPProvider did not install the configured LLM gate")
	}
	if got := NewProvider(ProviderOptions{Model: "gpt-4o-mini"}).Gate; got != nil {
		t.Fatal("NewProvider must stay unlimited (scripted transports are not paced)")
	}
}

// Parity-exempt: the Python LLM client is not paced (see the non-streaming
// test).
func TestStreamingGatePacesBeforeRequest(t *testing.T) {
	var hits int32
	server := gateServer(t, &hits)
	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIBase: server.URL + "/v1"})

	blocked := errors.New("stream gate closed")
	provider.Gate = func(context.Context) error { return blocked }
	_, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if !errors.Is(err, blocked) {
		t.Fatalf("ACompleteStream = %v, want the gate error", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("gate rejection still opened a stream (%d requests)", got)
	}
}
