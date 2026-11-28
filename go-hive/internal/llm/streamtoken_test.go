package llm

// Parity: core/framework/llm/litellm.py (hive_stream_token_invalid ->
// try_refresh_now -> retry the same call once)

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Parity: core/framework/llm/litellm.py (hive_stream_token_invalid detection)
func TestIsHiveStreamTokenInvalid(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		expect bool
	}{
		{"nil", nil, false},
		{"plain error", fmt.Errorf("hive_stream_token_invalid"), false},
		{"401 marker", &ProviderError{Message: "llm: proxy returned status 401: hive_stream_token_invalid", StatusCode: http.StatusUnauthorized}, true},
		{"403 marker", &ProviderError{Message: "hive_stream_token_invalid", StatusCode: http.StatusForbidden}, true},
		{"401 other detail", &ProviderError{Message: "llm: proxy returned status 401: invalid api key", StatusCode: http.StatusUnauthorized}, false},
		{"500 marker", &ProviderError{Message: "hive_stream_token_invalid", StatusCode: http.StatusInternalServerError}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsHiveStreamTokenInvalid(testCase.err); got != testCase.expect {
				t.Fatalf("IsHiveStreamTokenInvalid = %v, want %v", got, testCase.expect)
			}
		})
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::try_refresh_now
// (returns None without erroring when refresh is not configured)
func TestRefreshStreamTokenWithoutRefresher(t *testing.T) {
	SetStreamTokenRefresher(nil)
	if token, ok := RefreshStreamToken(context.Background()); ok || token != "" {
		t.Fatalf("RefreshStreamToken = (%q, %v), want (\"\", false)", token, ok)
	}
}

// Parity: core/framework/llm/litellm.py (the hive_stream_token_invalid branch
// retries the streaming call once with the refreshed token)
func TestStreamRetriesOnceAfterHiveStreamTokenInvalid(t *testing.T) {
	var mu sync.Mutex
	authorizations := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		attempt := len(authorizations)
		mu.Unlock()
		if attempt == 1 {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"message":"hive_stream_token_invalid"}}`))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writeSSEFrame(t, writer, `data: {"id":"c1","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
		writeSSEFrame(t, writer, "data: [DONE]")
	}))
	defer server.Close()

	var refreshCalls int
	SetStreamTokenRefresher(func(context.Context) (string, bool) {
		refreshCalls++
		return "refreshed-token", true
	})
	t.Cleanup(func() { SetStreamTokenRefresher(nil) })

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "stale-token", APIBase: server.URL})
	var events []StreamEvent
	completion, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events))
	if err != nil {
		t.Fatalf("ACompleteStream: %v", err)
	}
	if completion.Content != "ok" {
		t.Fatalf("content = %q, want ok", completion.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authorizations) != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry after the refresh)", len(authorizations))
	}
	if authorizations[0] != "Bearer stale-token" || authorizations[1] != "Bearer refreshed-token" {
		t.Fatalf("authorizations = %v, want the retry to carry the refreshed token", authorizations)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
}

// Parity-exempt: negative regression — a plain 401 (bad key, not a stale
// streamToken) must fail immediately instead of triggering a token refresh.
func TestStreamDoesNotRetryOnPlainUnauthorized(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	var refreshCalls int
	SetStreamTokenRefresher(func(context.Context) (string, bool) {
		refreshCalls++
		return "refreshed-token", true
	})
	t.Cleanup(func() { SetStreamTokenRefresher(nil) })

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "stale-token", APIBase: server.URL})
	var events []StreamEvent
	if _, err := provider.ACompleteStream(context.Background(),
		CompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}},
		collectStreamEvents(&events)); err == nil {
		t.Fatal("ACompleteStream succeeded on a 401, want an error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if refreshCalls != 0 {
		t.Fatalf("refresh calls = %d, want 0 for a non-streamToken 401", refreshCalls)
	}
	if NewStreamErrorEvent(&ProviderError{Message: "invalid api key", StatusCode: http.StatusUnauthorized}).Recoverable {
		t.Fatal("fixture sanity: a plain 401 must not be classified recoverable")
	}
}

// Parity: core/framework/llm/litellm.py (the hive_stream_token_invalid branch
// retries the non-streaming call once with the refreshed token)
func TestCompleteRetriesOnceAfterHiveStreamTokenInvalid(t *testing.T) {
	provider := NewProvider(ProviderOptions{Model: "gpt-4o-mini", APIKey: "stale-token"})
	keys := []string{}
	provider.Call = func(ctx context.Context, kwargs map[string]any) (CompletionResponse, error) {
		key, _ := kwargs["api_key"].(string)
		keys = append(keys, key)
		if len(keys) == 1 {
			return CompletionResponse{}, &ProviderError{
				Message:    "llm: proxy returned status 401: hive_stream_token_invalid",
				StatusCode: http.StatusUnauthorized,
			}
		}
		return CompletionResponse{Content: "ok"}, nil
	}

	var refreshCalls int
	SetStreamTokenRefresher(func(context.Context) (string, bool) {
		refreshCalls++
		return "refreshed-token", true
	})
	t.Cleanup(func() { SetStreamTokenRefresher(nil) })

	completion, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.Content != "ok" {
		t.Fatalf("content = %q, want ok", completion.Content)
	}
	if len(keys) != 2 || keys[0] != "stale-token" || keys[1] != "refreshed-token" {
		t.Fatalf("api keys = %v, want the retry to carry the refreshed token", keys)
	}
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
}
