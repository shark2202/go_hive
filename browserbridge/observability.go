package browserbridge

import (
	"context"
	"time"
)

// Telemetry records bridge/tool activity. The JSONL implementation writes
// upstream-shaped rows; callers can inject any implementation. A Telemetry
// that also implements EventLogger receives context/CDP rows.
type Telemetry interface {
	ToolCall(ctx context.Context, profile, tool string, params map[string]any, result any, err error, duration time.Duration)
	ConnectionEvent(event, profile string, fields map[string]any)
}

// EventLogger is the optional richer interface implemented by JSONLTelemetry.
type EventLogger interface {
	LogEvent(entry map[string]any)
}

type nopTelemetry struct{}

func (nopTelemetry) ToolCall(context.Context, string, string, map[string]any, any, error, time.Duration) {
}
func (nopTelemetry) ConnectionEvent(string, string, map[string]any) {}

// Blocker is the upstream health-blocker wire shape surfaced by tab_health,
// /contexts, and /status. field names and semantics match health.py's
// Blocker.to_dict().
type Blocker struct {
	Kind     string         `json:"kind"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Detail   string         `json:"detail"`
	Fix      string         `json:"fix"`
	Priority int            `json:"-"`
	Context  map[string]any `json:"context"`
}

// HealthProvider supplies externally-computed blockers for one tab. The
// built-in health registry is used by default; this seam remains for callers
// that want to augment it.
type HealthProvider interface {
	Blockers(ctx context.Context, tabID int) []Blocker
}

type nopHealth struct{}

func (nopHealth) Blockers(context.Context, int) []Blocker { return nil }

// RateLimiter is the upstream SocialRateLimiter contract used by the
// navigate-time social profile-view hook.
type RateLimiter interface {
	Check(platform, accountID, action string) RateLimitDecision
	Record(platform, accountID, action, targetID string)
}

// RateLimitDecision is one check result. Pointer count fields are omitted
// when the platform/action has no limit for that window.
type RateLimitDecision struct {
	Allowed           bool
	Reason            string
	RetryAfterSeconds float64
	MinDelaySeconds   float64
	HaltCampaign      bool
	HourlyCount       *int
	HourlyLimit       *int
	DailyCount        *int
	DailyLimit        *int
	WeeklyCount       *int
	WeeklyLimit       *int
}

type nopRateLimiter struct{}

func (nopRateLimiter) Check(string, string, string) RateLimitDecision {
	return RateLimitDecision{Allowed: true}
}
func (nopRateLimiter) Record(string, string, string, string) {}
