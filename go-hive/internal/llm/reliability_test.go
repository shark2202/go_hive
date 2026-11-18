package llm

// Reliability probes for the completion retry loop: rate limits retry with a
// bounded budget and then propagate the typed error, timeouts abort promptly,
// and an already-cancelled context must not sit through backoff sleeps.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Parity-exempt: fault-injection probe asserting the 429 error type survives
// the retry budget.
func TestCompleteRateLimitPropagatesTypedError(t *testing.T) {
	limited := &ProviderError{Message: "rate limited", StatusCode: 429, Headers: map[string]string{"retry-after": "1"}}
	var sleeps []time.Duration
	transport := &scriptedTransport{replies: []completionAttempt{
		{err: limited}, {err: limited}, {err: limited},
	}}
	provider := newTestProvider(transport, &sleeps)
	retries := 2

	_, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{MaxRetries: &retries})
	if err == nil {
		t.Fatal("Complete succeeded after exhausting rate-limit retries")
	}
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) || providerErr.StatusCode != 429 {
		t.Fatalf("error = %T %v, want *ProviderError status 429", err, err)
	}
	if len(transport.calls) != 3 {
		t.Fatalf("calls = %d, want 3 (initial + 2 retries)", len(transport.calls))
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps = %d, want 2", len(sleeps))
	}
}

// Parity-exempt: fault-injection probe for a cancelled context; backoff must
// not keep sleeping for a request nobody is waiting for.
func TestCompleteCancelledContextStopsRetrying(t *testing.T) {
	t.Run("before the first attempt", func(t *testing.T) {
		var sleeps []time.Duration
		transport := &scriptedTransport{replies: []completionAttempt{{err: &ProviderError{StatusCode: 429}}}}
		provider := newTestProvider(transport, &sleeps)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := provider.Complete(ctx, []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if len(transport.calls) != 0 {
			t.Fatalf("calls = %d, want 0 (a cancelled caller must not hit the transport)", len(transport.calls))
		}
		if len(sleeps) != 0 {
			t.Fatalf("sleeps = %v, want none after cancellation", sleeps)
		}
	})

	t.Run("during a rate-limit backoff", func(t *testing.T) {
		var sleeps []time.Duration
		var calls int
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		provider := &Provider{
			Model:     "gpt-4o-mini",
			SleepFunc: recordingSleep(&sleeps),
			Call: func(callCtx context.Context, kwargs map[string]any) (CompletionResponse, error) {
				calls++
				// The caller's deadline fires while the upstream is rate limited.
				cancel()
				return CompletionResponse{}, &ProviderError{StatusCode: 429, Headers: map[string]string{"retry-after": "5"}}
			},
		}

		_, err := provider.Complete(ctx, []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1 (no retry after cancellation)", calls)
		}
		if len(sleeps) != 0 {
			t.Fatalf("sleeps = %v, want none; the retry loop slept after cancellation", sleeps)
		}
	})
}

// Parity-exempt: fault-injection probe for an HTTP 429 with Retry-After; the
// transport must surface it as a retryable ProviderError and recover.
func TestHTTPRateLimitRetryAfterHeaderRecovers(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-mini","choices":[{"message":{"content":"recovered"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIBase: server.URL, APIKey: "test-key"})
	var sleeps []time.Duration
	provider.SleepFunc = recordingSleep(&sleeps)

	result, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Content != "recovered" {
		t.Fatalf("content = %q, want recovered", result.Content)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if len(sleeps) != 1 {
		t.Fatalf("sleeps = %d, want 1 retry backoff", len(sleeps))
	}
}

// Parity-exempt: fault-injection probe for a hung upstream; the caller's
// deadline must abort the request instead of waiting for the 600s default.
func TestHTTPCompletionHonoursContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(server.Close)

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIBase: server.URL, APIKey: "test-key"})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := provider.Complete(ctx, []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Complete succeeded against a hung server")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline took %s to surface", elapsed)
	}
}

// Parity-exempt: fault-injection probe for a 200 response with an empty
// choices payload; the retry loop is bounded and then returns the empty reply
// instead of spinning forever.
func TestHTTPEmptyResponseRetryIsBounded(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-mini","choices":[]}`))
	}))
	t.Cleanup(server.Close)

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIBase: server.URL, APIKey: "test-key"})
	var sleeps []time.Duration
	provider.SleepFunc = recordingSleep(&sleeps)

	result, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("content = %q, want empty", result.Content)
	}
	if got := requests.Load(); got != int32(EmptyResponseMaxRetries+1) {
		t.Fatalf("requests = %d, want %d (bounded retry)", got, EmptyResponseMaxRetries+1)
	}
	if len(sleeps) != EmptyResponseMaxRetries {
		t.Fatalf("sleeps = %d, want %d", len(sleeps), EmptyResponseMaxRetries)
	}
}

// Parity-exempt: fault-injection probe for a 200 response with an unreadable
// body; the decode error must propagate instead of yielding an empty success.
func TestHTTPCompletionMalformedBodyPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`not-json`))
	}))
	t.Cleanup(server.Close)

	provider := NewHTTPProvider(ProviderOptions{Model: "gpt-4o-mini", APIBase: server.URL, APIKey: "test-key"})
	provider.SleepFunc = func(time.Duration) {}
	_, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, CompleteOptions{})
	if err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("error = %v, want a decode error", err)
	}
}
