package ratelimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock drives the limiter without sleeping in tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return nil
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testLimiter(t *testing.T, clock *fakeClock, maxWait time.Duration, specs ...CallLimitSpec) *CallLimiter {
	t.Helper()
	limiter := NewCallLimiter(specs, CallLimiterOptions{MaxWait: maxWait, Now: clock.Now, Sleep: clock.sleep})
	if limiter == nil {
		t.Fatal("NewCallLimiter returned nil for configured specs")
	}
	return limiter
}

// Parity-exempt: the shipped default is unlimited; upstream has no
// equivalent test because its RateLimitStage is always configured with a
// max_requests_per_minute.
func TestCallLimiterUnconfiguredMeansUnlimited(t *testing.T) {
	var unset *CallLimiter
	for i := 0; i < 100; i++ {
		if err := unset.Acquire(context.Background(), "llm"); err != nil {
			t.Fatalf("nil limiter blocked call %d: %v", i, err)
		}
	}
	if unset.HasBucket("llm") {
		t.Fatal("nil limiter reports a configured bucket")
	}
	if got := CallLimitsFromSection(map[string]any{"linkedin.invite.hourly": 5}); got != nil {
		t.Fatalf("social-only section built a call limiter: %+v", got)
	}
	if got := CallLimitsFromSection(nil); got != nil {
		t.Fatalf("empty section built a call limiter: %+v", got)
	}
}

// Parity: core/framework/pipeline/stages/rate_limit.py::RateLimitStage
func TestCallLimiterPerMinuteReturnsRetryableError(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, 0, CallLimitSpec{Key: CallBucketLLM, PerMinute: 2})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := limiter.Acquire(ctx, CallBucketLLM); err != nil {
			t.Fatalf("call %d inside the limit failed: %v", i, err)
		}
	}
	err := limiter.Acquire(ctx, CallBucketLLM)
	if err == nil {
		t.Fatal("third call in the same minute was allowed")
	}
	var limitErr *CallLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %T, want *CallLimitError", err)
	}
	if !limitErr.Retryable() {
		t.Fatal("limit error is not marked retryable")
	}
	if limitErr.RetryAfter != time.Minute {
		t.Fatalf("RetryAfter = %s, want 1m", limitErr.RetryAfter)
	}
	if limitErr.PerMinute != 2 {
		t.Fatalf("PerMinute = %d, want 2", limitErr.PerMinute)
	}
	if !IsCallLimitError(err) {
		t.Fatal("IsCallLimitError missed the rejection")
	}
	if got := limitErr.RetryAfterSeconds(); got != 60 {
		t.Fatalf("RetryAfterSeconds = %v, want 60", got)
	}
}

// Parity-exempt: bounded waiting is a Go extension; upstream rejects outright.
func TestCallLimiterWaitsWithinBudget(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, 5*time.Second, CallLimitSpec{Key: CallBucketTools, MinInterval: 2 * time.Second})
	ctx := context.Background()

	start := clock.Now()
	if err := limiter.Acquire(ctx, "tools.read_file"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := limiter.Acquire(ctx, "tools.read_file"); err != nil {
		t.Fatalf("second call should have waited: %v", err)
	}
	if elapsed := clock.Now().Sub(start); elapsed != 2*time.Second {
		t.Fatalf("clock advanced %s, want the 2s min interval", elapsed)
	}
}

// Parity-exempt: bounded waiting is a Go extension; this locks the refusal
// once the wait would exceed the configured budget.
func TestCallLimiterRefusesBeyondWaitBudget(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, time.Second, CallLimitSpec{Key: CallBucketTools, MinInterval: 30 * time.Second})
	ctx := context.Background()

	if err := limiter.Acquire(ctx, "tools"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	err := limiter.Acquire(ctx, "tools")
	var limitErr *CallLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("error = %v, want *CallLimitError", err)
	}
	if limitErr.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %s, want 30s", limitErr.RetryAfter)
	}
}

// Parity: core/framework/pipeline/stages/rate_limit.py::RateLimitStage
func TestCallLimiterWindowSlides(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, 0, CallLimitSpec{Key: CallBucketLLM, PerMinute: 2})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := limiter.Acquire(ctx, CallBucketLLM); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	clock.advance(61 * time.Second)
	if err := limiter.Acquire(ctx, CallBucketLLM); err != nil {
		t.Fatalf("call after the window slid: %v", err)
	}
}

// Parity-exempt: per-tool buckets are Go-only; the upstream stage keys on
// entry point + session, not tool name.
func TestCallLimiterSpecificBucketBeatsShared(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, 0,
		CallLimitSpec{Key: CallBucketTools, PerMinute: 1},
		CallLimitSpec{Key: "tools.read_file", PerMinute: 5},
	)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := limiter.Acquire(ctx, "tools.read_file"); err != nil {
			t.Fatalf("read_file call %d: %v", i, err)
		}
	}
	if err := limiter.Acquire(ctx, "tools.read_file"); !IsCallLimitError(err) {
		t.Fatalf("sixth read_file call = %v, want the per-tool limit", err)
	}
	if err := limiter.Acquire(ctx, "tools.write_file"); err != nil {
		t.Fatalf("first write_file call: %v", err)
	}
	if err := limiter.Acquire(ctx, "tools.write_file"); !IsCallLimitError(err) {
		t.Fatalf("second write_file call = %v, want the shared bucket", err)
	}
}

// Parity-exempt: hive.config.json plumbing is Go-only.
func TestCallLimitsFromSectionParsesBuckets(t *testing.T) {
	limiter := CallLimitsFromSection(map[string]any{
		"llm.requests_per_minute":              30,
		"llm.min_interval_s":                   0.25,
		"tools.requests_per_minute":            "60",
		"tools.run_worker.requests_per_minute": 2,
		"calls.max_wait_s":                     1.5,
		"linkedin.invite.hourly":               5,
	})
	if limiter == nil {
		t.Fatal("configured section built no limiter")
	}
	for _, key := range []string{CallBucketLLM, CallBucketTools, "tools.run_worker"} {
		if !limiter.HasBucket(key) {
			t.Fatalf("%s bucket missing: %+v", key, limiter.specs)
		}
	}
	if limiter.HasBucket("linkedin.invite") {
		t.Fatal("social limit leaked into the call buckets")
	}
	if limiter.maxWait != 1500*time.Millisecond {
		t.Fatalf("maxWait = %s, want 1.5s", limiter.maxWait)
	}
	if spec := limiter.specs[CallBucketLLM]; spec.PerMinute != 30 || spec.MinInterval != 250*time.Millisecond {
		t.Fatalf("llm spec = %+v", spec)
	}
	if spec := limiter.specs[CallBucketTools]; spec.PerMinute != 60 {
		t.Fatalf("shared tools spec = %+v", spec)
	}
}

// Parity-exempt: error wording is Go-only.
func TestCallLimitErrorMessageIsActionable(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, 0, CallLimitSpec{Key: CallBucketLLM, PerMinute: 1})
	ctx := context.Background()
	if err := limiter.Acquire(ctx, CallBucketLLM); err != nil {
		t.Fatalf("first call: %v", err)
	}
	err := limiter.Acquire(ctx, CallBucketLLM)
	message := err.Error()
	for _, want := range []string{"rate limit", "1 requests/minute", "retry in"} {
		if !strings.Contains(message, want) {
			t.Fatalf("message %q missing %q", message, want)
		}
	}
}

// Parity-exempt: Go-only default wiring; locks that no config means no pacing.
func TestAcquireLLMRequestNoopWhenUnset(t *testing.T) {
	SetCallLimiter(nil)
	t.Cleanup(ResetCallLimiter)
	for i := 0; i < 10; i++ {
		if err := AcquireLLMRequest(context.Background()); err != nil {
			t.Fatalf("unconfigured LLM call %d was paced: %v", i, err)
		}
	}
}

// Parity-exempt: Go-only wiring; locks that every tool falls back to the
// shared tools bucket.
func TestAcquireToolCallUsesSharedBucket(t *testing.T) {
	clock := newFakeClock()
	SetCallLimiter(testLimiter(t, clock, 0, CallLimitSpec{Key: CallBucketTools, PerMinute: 1}))
	t.Cleanup(ResetCallLimiter)

	if err := AcquireToolCall(context.Background(), "read_file"); err != nil {
		t.Fatalf("first tool call: %v", err)
	}
	err := AcquireToolCall(context.Background(), "write_file")
	var limitErr *CallLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("second tool call = %v, want *CallLimitError", err)
	}
	if limitErr.Key != CallBucketTools {
		t.Fatalf("Key = %q, want the shared bucket", limitErr.Key)
	}
}

// Parity-exempt: Go-only concurrency guarantee for the reservation window.
func TestCallLimiterReservesSlotsForConcurrentCallers(t *testing.T) {
	clock := newFakeClock()
	limiter := testLimiter(t, clock, 0, CallLimitSpec{Key: CallBucketLLM, MinInterval: time.Second})

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = limiter.Acquire(context.Background(), CallBucketLLM)
		}(i)
	}
	wg.Wait()

	allowed, limited := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			allowed++
		case IsCallLimitError(err):
			limited++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if allowed != 1 || limited != 1 {
		t.Fatalf("allowed=%d limited=%d, want exactly one slot", allowed, limited)
	}
}
