package agentloop

// Parity: framework/agent_loop/stream_stall.py.

import (
	"fmt"
	"log"
	"sync"
)

var streamStallLogf = log.Printf

// Parity: framework/agent_loop/stream_stall.py _REASON_LABELS.
var streamStallReasonLabels = map[string]string{
	"ttft":     "no tokens before TTFT budget",
	"inactive": "stream went silent after producing events",
}

// Parity: framework/agent_loop/stream_stall.py StreamStallSource.
type StreamStallSource struct {
	mu             sync.Mutex
	maxPerTurn     int
	enabled        bool
	nudgesThisTurn int
}

// Parity: StreamStallSource.__init__.
func NewStreamStallSource(maxPerTurn int, enabled bool) *StreamStallSource {
	return &StreamStallSource{maxPerTurn: maxPerTurn, enabled: enabled}
}

// Parity: StreamStallSource.name.
func (s *StreamStallSource) Name() string { return "stream_stall" }

// Parity: StreamStallSource.points.
func (s *StreamStallSource) Points() []ReminderPoint { return []ReminderPoint{STREAM_STALLED} }

// Parity: ReminderSource.applies_to default.
func (s *StreamStallSource) AppliesTo(*ReminderAgentContext) bool { return true }

// Parity: StreamStallSource.reset_turn.
func (s *StreamStallSource) ResetTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nudgesThisTurn = 0
}

// Parity: StreamStallSource.render (bare-string branch).
func (s *StreamStallSource) Render(rctx ReminderContext) (string, bool) {
	reminder, ok := s.RenderReminder(rctx)
	if !ok {
		return "", false
	}
	return reminder.Body, true
}

// Parity: StreamStallSource.render.
func (s *StreamStallSource) RenderReminder(rctx ReminderContext) (*Reminder, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.maxPerTurn <= 0 {
		return nil, false
	}
	if s.nudgesThisTurn >= s.maxPerTurn {
		return nil, false
	}
	signals := rctx.Signals
	if signals == nil || signals.StallReason == "" {
		return nil, false
	}
	s.nudgesThisTurn++
	reason := signals.StallReason
	label, known := streamStallReasonLabels[reason]
	if !known {
		label = reason
	}
	body := fmt.Sprintf(
		"The previous stream stalled (%s, %.0fs). Continue from the last tool "+
			"result already in this conversation. Do NOT repeat tool calls "+
			"whose results are visible above — reuse them and move to the next "+
			"step.",
		label, signals.StallElapsed)
	streamStallLogf("[stream_stall] producing nudge (count=%d/%d, reason=%s, elapsed=%.0fs)",
		s.nudgesThisTurn, s.maxPerTurn, reason, signals.StallElapsed)
	return &Reminder{
		Body:   body,
		Source: s.Name(),
		Meta: map[string]any{
			"reason":      reason,
			"elapsed":     signals.StallElapsed,
			"nudge_count": s.nudgesThisTurn,
			"cap":         s.maxPerTurn,
		},
	}, true
}
