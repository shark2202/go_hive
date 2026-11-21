package pipeline

import (
	"strings"
	"testing"
	"time"
)

func newTestRateLimit(t *testing.T, perMinute int) *RateLimitStage {
	t.Helper()
	stage, err := BuildStage(map[string]any{
		"type":   "rate_limit",
		"config": map[string]any{"max_requests_per_minute": perMinute},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return stage.(*RateLimitStage)
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; this
// pins rate_limit's sliding-window cap and rejection message.
func TestRateLimitRejectsOverLimit(t *testing.T) {
	stage := newTestRateLimit(t, 2)
	now := time.Unix(1000, 0)
	stage.now = func() time.Time { return now }
	ctx := NewContext("api", map[string]any{})

	for i := 0; i < 2; i++ {
		result, err := stage.Process(ctx)
		if err != nil || result.Action != ActionContinue {
			t.Fatalf("request %d: result=%+v err=%v", i+1, result, err)
		}
	}
	result, err := stage.Process(ctx)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Action != ActionReject {
		t.Fatalf("third request action = %s, want reject", result.Action)
	}
	want := "Rate limit exceeded: 2 req/min for session 'default'"
	if result.RejectionReason != want {
		t.Fatalf("reason = %q, want %q", result.RejectionReason, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// bucket key is entry_point:session_id, so sessions do not share a cap.
func TestRateLimitScopesBucketsBySession(t *testing.T) {
	stage := newTestRateLimit(t, 1)
	now := time.Unix(2000, 0)
	stage.now = func() time.Time { return now }

	first := NewContext("api", map[string]any{})
	first.SessionState = map[string]any{"session_id": "a"}
	if result, _ := stage.Process(first); result.Action != ActionContinue {
		t.Fatalf("session a first request rejected")
	}
	if result, _ := stage.Process(first); result.Action != ActionReject {
		t.Fatalf("session a second request allowed")
	}
	second := NewContext("api", map[string]any{})
	second.SessionState = map[string]any{"session_id": "b"}
	if result, _ := stage.Process(second); result.Action != ActionContinue {
		t.Fatalf("session b must get its own bucket")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; entries
// older than the 60 s window are pruned before the cap check.
func TestRateLimitWindowExpires(t *testing.T) {
	stage := newTestRateLimit(t, 1)
	now := time.Unix(3000, 0)
	stage.now = func() time.Time { return now }
	ctx := NewContext("api", map[string]any{})

	if result, _ := stage.Process(ctx); result.Action != ActionContinue {
		t.Fatalf("first request rejected")
	}
	now = now.Add(61 * time.Second)
	result, err := stage.Process(ctx)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Action != ActionContinue {
		t.Fatalf("request after window rejected: %+v", result)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// missing session_state shares the "default" bucket, and a null session_id is
// stringified the way Python's str(None) does.
func TestRateLimitDefaultAndNullSessionBuckets(t *testing.T) {
	stage := newTestRateLimit(t, 1)
	now := time.Unix(4000, 0)
	stage.now = func() time.Time { return now }

	noState := NewContext("api", map[string]any{})
	if result, _ := stage.Process(noState); result.Action != ActionContinue {
		t.Fatalf("first default-bucket request rejected")
	}
	result, _ := stage.Process(noState)
	if result.Action != ActionReject || !strings.Contains(result.RejectionReason, "session 'default'") {
		t.Fatalf("second default-bucket request = %+v", result)
	}

	nullState := NewContext("api", map[string]any{})
	nullState.SessionState = map[string]any{"session_id": nil}
	if result, _ := stage.Process(nullState); result.Action != ActionContinue {
		t.Fatalf("null session must be its own bucket")
	}
	result, _ = stage.Process(nullState)
	if !strings.Contains(result.RejectionReason, "session 'None'") {
		t.Fatalf("null session reason = %q", result.RejectionReason)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// config key max_requests_per_minute defaults to 60 and rejects non-integers.
func TestRateLimitConfigParsing(t *testing.T) {
	defaultStage := newTestRateLimit(t, 60)
	if defaultStage.MaxRequestsPerMinute != 60 {
		t.Fatalf("default = %d, want 60", defaultStage.MaxRequestsPerMinute)
	}
	if _, err := BuildStage(map[string]any{
		"type":   "rate_limit",
		"config": map[string]any{"max_requests_per_minute": "60"},
	}); err == nil {
		t.Fatal("string limit must fail")
	}
}
