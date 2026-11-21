package pipeline

import (
	"fmt"
	"sync"
	"time"
)

// rateLimitWindow is the sliding window upstream prunes against (60 s).
const rateLimitWindow = 60 * time.Second

// RateLimitStage rejects requests that exceed max_requests_per_minute per
// `entry_point_id:session_id` bucket. Without a session_id in the context's
// session_state every caller shares the "default" bucket.
//
// Parity: framework/pipeline/stages/rate_limit.py::RateLimitStage
type RateLimitStage struct {
	BaseStage
	// MaxRequestsPerMinute is the per-bucket cap (config
	// max_requests_per_minute, default 60).
	MaxRequestsPerMinute int

	mu         sync.Mutex
	timestamps map[string][]time.Time
	now        func() time.Time
}

// NewRateLimitStage builds the stage from its declarative config.
func NewRateLimitStage(spec map[string]any) (Stage, error) {
	config := configMap(spec)
	stage := &RateLimitStage{
		BaseStage:            BaseStage{StageName: "rate_limit", StageOrder: 200},
		MaxRequestsPerMinute: 60,
		timestamps:           map[string][]time.Time{},
	}
	if raw, present := config["max_requests_per_minute"]; present {
		limit, ok := pyInt(raw)
		if !ok {
			return nil, fmt.Errorf("rate_limit max_requests_per_minute must be an integer, got %v", raw)
		}
		stage.MaxRequestsPerMinute = limit
	}
	return stage, nil
}

// Process counts one request into its bucket; at the cap it rejects before
// counting, so a rejected request never extends the window.
//
// Parity: framework/pipeline/stages/rate_limit.py::RateLimitStage.process
func (s *RateLimitStage) Process(ctx *Context) (Result, error) {
	sessionID := "default"
	if ctx.SessionState != nil {
		if raw, present := ctx.SessionState["session_id"]; present {
			if raw == nil {
				// Python str(None) is "None"; the bucket key keeps that.
				sessionID = "None"
			} else {
				sessionID = pyString(raw)
			}
		}
	}
	key := ctx.EntryPointID + ":" + sessionID

	now := s.nowTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	recent := s.timestamps[key][:0]
	for _, stamp := range s.timestamps[key] {
		if now.Sub(stamp) < rateLimitWindow {
			recent = append(recent, stamp)
		}
	}
	if len(recent) >= s.MaxRequestsPerMinute {
		s.timestamps[key] = recent
		return RejectResult(fmt.Sprintf(
			"Rate limit exceeded: %d req/min for session '%s'",
			s.MaxRequestsPerMinute, sessionID,
		)), nil
	}
	s.timestamps[key] = append(recent, now)
	return ContinueResult(), nil
}

func (s *RateLimitStage) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
