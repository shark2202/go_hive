package ratelimit

// Call pacing for outbound agent work: LLM requests and tool executions.
//
// The social limiter in rate_limiter.go counts completed actions per platform
// account. This file is the other half: a process-wide pacer for the calls the
// agent itself makes, driven by the optional `rate_limits` section of
// hive.config.json:
//
//	"rate_limits": {
//	  "llm.requests_per_minute": 30,
//	  "llm.min_interval_s": 0.5,
//	  "tools.requests_per_minute": 60,
//	  "tools.run_worker.requests_per_minute": 6,
//	  "calls.max_wait_s": 5
//	}
//
// Unconfigured means unlimited. DefaultCallLimiter returns nil when the section
// names no llm/tools bucket, and every method tolerates a nil receiver, so the
// call paths behave exactly as before until someone opts in.
//
// Parity: core/framework/pipeline/stages/rate_limit.py::RateLimitStage is the
// upstream knob (max_requests_per_minute per session). This port keeps the
// same minute-window rejection, adds min-interval pacing, and waits briefly
// (calls.max_wait_s) before returning a retryable error instead of failing the
// turn outright.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// CallBucketLLM paces every model request the process sends.
	CallBucketLLM = "llm"
	// CallBucketTools is the shared bucket every tool execution falls back to;
	// a tools.<name> bucket overrides it for one tool.
	CallBucketTools = "tools"

	// DefaultCallMaxWait bounds how long one call may sit waiting for its
	// slot. A longer delay returns a retryable error instead.
	DefaultCallMaxWait = 5 * time.Second

	callWindow = time.Minute
)

// CallLimitSpec is one pacing bucket. A zero PerMinute and zero MinInterval
// leaves the bucket disabled.
type CallLimitSpec struct {
	Key         string
	PerMinute   int
	MinInterval time.Duration
}

// CallLimiterOptions customises the pacer. The zero value is production
// behaviour: a real clock and context-aware sleeps.
type CallLimiterOptions struct {
	MaxWait time.Duration
	Now     func() time.Time
	Sleep   func(ctx context.Context, d time.Duration) error
}

// CallLimitError is returned when the next slot is farther away than the
// configured wait budget. It is explicitly retryable: the caller may re-issue
// the same call once RetryAfter has elapsed.
type CallLimitError struct {
	Key         string
	RetryAfter  time.Duration
	PerMinute   int
	MinInterval time.Duration
}

func (e *CallLimitError) Error() string {
	limit := ""
	switch {
	case e.PerMinute > 0 && e.MinInterval > 0:
		limit = fmt.Sprintf("%d requests/minute and one every %.2fs", e.PerMinute, e.MinInterval.Seconds())
	case e.PerMinute > 0:
		limit = fmt.Sprintf("%d requests/minute", e.PerMinute)
	case e.MinInterval > 0:
		limit = fmt.Sprintf("one request every %.2fs", e.MinInterval.Seconds())
	}
	if limit == "" {
		return fmt.Sprintf("rate limit: %s is limited; retry in %.1fs", e.Key, e.RetryAfter.Seconds())
	}
	return fmt.Sprintf("rate limit: %s allows %s; retry in %.1fs", e.Key, limit, e.RetryAfter.Seconds())
}

// Retryable marks the error as safe to re-issue after RetryAfter.
func (e *CallLimitError) Retryable() bool { return true }

// RetryAfterSeconds is the machine-readable delay.
func (e *CallLimitError) RetryAfterSeconds() float64 { return e.RetryAfter.Seconds() }

// IsCallLimitError reports whether err is a pacing rejection.
func IsCallLimitError(err error) bool {
	var limitErr *CallLimitError
	return errors.As(err, &limitErr)
}

// CallLimiter is a per-bucket sliding-window pacer. Safe for concurrent use;
// a nil *CallLimiter is an unlimited pacer.
type CallLimiter struct {
	mu      sync.Mutex
	specs   map[string]CallLimitSpec
	hits    map[string][]time.Time
	maxWait time.Duration
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
}

// NewCallLimiter builds a pacer from explicit specs. Specs with no window are
// dropped; when none remain the result is nil (unlimited).
func NewCallLimiter(specs []CallLimitSpec, opts CallLimiterOptions) *CallLimiter {
	byKey := make(map[string]CallLimitSpec, len(specs))
	for _, spec := range specs {
		key := strings.ToLower(strings.TrimSpace(spec.Key))
		if key == "" || (spec.PerMinute <= 0 && spec.MinInterval <= 0) {
			continue
		}
		if spec.PerMinute < 0 {
			spec.PerMinute = 0
		}
		if spec.MinInterval < 0 {
			spec.MinInterval = 0
		}
		spec.Key = key
		byKey[key] = spec
	}
	if len(byKey) == 0 {
		return nil
	}
	limiter := &CallLimiter{
		specs:   byKey,
		hits:    make(map[string][]time.Time, len(byKey)),
		maxWait: opts.MaxWait,
		now:     opts.Now,
		sleep:   opts.Sleep,
	}
	if limiter.now == nil {
		limiter.now = time.Now
	}
	if limiter.sleep == nil {
		limiter.sleep = sleepWithContext
	}
	return limiter
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *CallLimiter) clock() time.Time {
	if l.now == nil {
		return time.Now()
	}
	return l.now()
}

// Acquire reserves one slot for key. It waits when the next slot is within the
// wait budget and returns *CallLimitError when it is farther away, so a caller
// can surface an honest retry hint instead of stalling a turn.
func (l *CallLimiter) Acquire(ctx context.Context, key string) error {
	if l == nil {
		return nil
	}
	spec, ok := l.specFor(key)
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	l.mu.Lock()
	now := l.clock()
	l.pruneLocked(spec.Key, now)
	delay := l.delayLocked(spec, now)
	if delay > l.maxWait {
		l.mu.Unlock()
		return &CallLimitError{
			Key: spec.Key, RetryAfter: delay,
			PerMinute: spec.PerMinute, MinInterval: spec.MinInterval,
		}
	}
	// Reserve the slot before releasing the lock so concurrent callers pace
	// against each other instead of all waking at once.
	l.hits[spec.Key] = append(l.hits[spec.Key], now.Add(delay))
	l.mu.Unlock()

	if delay <= 0 {
		return nil
	}
	return l.sleep(ctx, delay)
}

// specFor resolves the most specific bucket: exact key, then its family
// (tools.read_file -> tools).
func (l *CallLimiter) specFor(key string) (CallLimitSpec, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if spec, ok := l.specs[key]; ok {
		return spec, true
	}
	if index := strings.Index(key, "."); index > 0 {
		if spec, ok := l.specs[key[:index]]; ok {
			return spec, true
		}
	}
	return CallLimitSpec{}, false
}

func (l *CallLimiter) pruneLocked(key string, now time.Time) {
	hits := l.hits[key]
	if len(hits) == 0 {
		return
	}
	cutoff := now.Add(-callWindow)
	kept := hits[:0]
	for _, hit := range hits {
		if !hit.Before(cutoff) {
			kept = append(kept, hit)
		}
	}
	l.hits[key] = kept
}

func (l *CallLimiter) delayLocked(spec CallLimitSpec, now time.Time) time.Duration {
	var delay time.Duration
	hits := l.hits[spec.Key]
	if spec.MinInterval > 0 && len(hits) > 0 {
		if wait := hits[len(hits)-1].Add(spec.MinInterval).Sub(now); wait > delay {
			delay = wait
		}
	}
	if spec.PerMinute > 0 && len(hits) >= spec.PerMinute {
		oldest := hits[len(hits)-spec.PerMinute]
		if wait := oldest.Add(callWindow).Sub(now); wait > delay {
			delay = wait
		}
	}
	if delay < 0 {
		delay = 0
	}
	return delay
}

// HasBucket reports whether key (or its family) is configured.
func (l *CallLimiter) HasBucket(key string) bool {
	if l == nil {
		return false
	}
	_, ok := l.specFor(key)
	return ok
}

// CallLimitsFromSection parses the hive.config.json rate_limits object. It
// returns nil when the section names no llm/tools bucket, which is the shipped
// "unlimited" default.
//
// Parity-exempt: Go-only plumbing. Upstream builds RateLimitStage from its
// pipeline config; this reads the same knob out of hive.config.json.
func CallLimitsFromSection(section map[string]any) *CallLimiter {
	if len(section) == 0 {
		return nil
	}
	buckets := make(map[string]*CallLimitSpec)
	specFor := func(key string) *CallLimitSpec {
		if existing, ok := buckets[key]; ok {
			return existing
		}
		created := &CallLimitSpec{Key: key}
		buckets[key] = created
		return created
	}
	maxWait := DefaultCallMaxWait

	for rawKey, rawValue := range section {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		switch {
		case key == "calls.max_wait_s":
			if seconds, ok := coerceFloat(rawValue); ok && seconds >= 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				maxWait = time.Duration(seconds * float64(time.Second))
			}
		case strings.HasSuffix(key, ".requests_per_minute"):
			bucket := strings.TrimSuffix(key, ".requests_per_minute")
			if !callBucketValid(bucket) {
				continue
			}
			if count, ok := coerceInt(rawValue); ok && count > 0 {
				specFor(bucket).PerMinute = count
			}
		case strings.HasSuffix(key, ".min_interval_s"):
			bucket := strings.TrimSuffix(key, ".min_interval_s")
			if !callBucketValid(bucket) {
				continue
			}
			if seconds, ok := coerceFloat(rawValue); ok && seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				specFor(bucket).MinInterval = time.Duration(seconds * float64(time.Second))
			}
		}
	}

	specs := make([]CallLimitSpec, 0, len(buckets))
	for _, spec := range buckets {
		specs = append(specs, *spec)
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Key < specs[j].Key })
	return NewCallLimiter(specs, CallLimiterOptions{MaxWait: maxWait})
}

// callBucketValid keeps social-limit keys (linkedin.invite.hourly) from being
// mistaken for call buckets.
func callBucketValid(bucket string) bool {
	return bucket == CallBucketLLM || bucket == CallBucketTools || strings.HasPrefix(bucket, CallBucketTools+".")
}

func coerceFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return parsed, err == nil
	}
	return 0, false
}

var (
	callLimiterMu    sync.Mutex
	callLimiterValue *CallLimiter
	callLimiterReady bool
)

// DefaultCallLimiter is the process-wide limiter built from hive.config.json.
// It is read once: rate limits are startup configuration, not live state (the
// memory_timeline flag is the live-reload exception, not the rule).
func DefaultCallLimiter() *CallLimiter {
	callLimiterMu.Lock()
	defer callLimiterMu.Unlock()
	if !callLimiterReady {
		callLimiterValue = CallLimitsFromSection(readHiveConfigOverrides())
		callLimiterReady = true
	}
	return callLimiterValue
}

// SetCallLimiter overrides the process-wide limiter. Tests use it to stay
// hermetic; passing nil means unlimited.
func SetCallLimiter(limiter *CallLimiter) {
	callLimiterMu.Lock()
	callLimiterValue = limiter
	callLimiterReady = true
	callLimiterMu.Unlock()
}

// ResetCallLimiter drops the cached limiter so the next call re-reads config.
func ResetCallLimiter() {
	callLimiterMu.Lock()
	callLimiterValue = nil
	callLimiterReady = false
	callLimiterMu.Unlock()
}

// AcquireLLMRequest paces one model request. Nil limiter (the default) is a
// no-op.
//
// Parity-exempt: Go-only execution point — the Python LLM client is not
// paced; RateLimitStage runs in its request pipeline.
func AcquireLLMRequest(ctx context.Context) error {
	return DefaultCallLimiter().Acquire(ctx, CallBucketLLM)
}

// AcquireToolCall paces one tool execution, preferring a tools.<name> bucket
// over the shared tools bucket.
//
// Parity-exempt: Go-only execution point (see AcquireLLMRequest).
func AcquireToolCall(ctx context.Context, toolName string) error {
	key := CallBucketTools
	if name := strings.ToLower(strings.TrimSpace(toolName)); name != "" {
		key = CallBucketTools + "." + name
	}
	return DefaultCallLimiter().Acquire(ctx, key)
}
