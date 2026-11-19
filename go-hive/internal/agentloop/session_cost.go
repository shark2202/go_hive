package agentloop

// Session spend hard cap.
//
// The soft budget checkpoint in loop.go nudges the model when a turn makes a
// lot of tool calls; this is the money side. A session accumulates the
// CostUSD of every completion it produces, and once the optional cap is
// reached the next ExecuteTurn is refused before it can make a model call.
// That is deliberately turn-boundary enforcement (not a mid-turn kill): the
// requirement is to block the next turn with an explicit error, and letting
// the current turn finish means its partial work is still persisted.
//
// Default: disabled (0). Upstream carries max_cost_per_run as a schema field
// with no executor; this loop-level cap is the executor.

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

const (
	// SessionCostLimitEnv is the env knob that seeds DefaultLoopConfig.
	SessionCostLimitEnv = "HIVE_SESSION_COST_LIMIT_USD"

	sessionCostBlockedCounter = "session_cost_limit_blocked"
)

// SessionCostLimitFromEnv returns the configured session cap in USD. Unset,
// empty, malformed, NaN/Inf or non-positive values mean disabled (0), which is
// the shipped default.
//
// Parity-exempt: upstream has no session-level spend cap to mirror —
// max_cost_per_run is a schema field and CostGuardStage only rejects a single
// request whose pre-flight estimate exceeds its budget.
func SessionCostLimitFromEnv() float64 {
	raw := strings.TrimSpace(os.Getenv(SessionCostLimitEnv))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return value
}

// SessionCostLimitError is returned by ExecuteTurn when the session's recorded
// spend has already reached the cap. The caller may surface it verbatim: it
// names both amounts and how to lift the cap.
type SessionCostLimitError struct {
	SpentUSD float64
	LimitUSD float64
}

func (e *SessionCostLimitError) Error() string {
	return fmt.Sprintf(
		"agent loop: session cost limit reached ($%.4f spent of $%.4f); refusing the next turn — "+
			"raise %s or start a new session",
		e.SpentUSD, e.LimitUSD, SessionCostLimitEnv)
}

// SessionCostUSD reports the cumulative completion cost this loop has recorded.
func (l *AgentLoop) SessionCostUSD() float64 {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	return l.sessionCostUSD
}

// AddSessionCost records one completion's cost against the session total.
// Non-positive and non-finite amounts are ignored so an unpriced model cannot
// corrupt the running total.
func (l *AgentLoop) AddSessionCost(costUSD float64) {
	if costUSD <= 0 || math.IsNaN(costUSD) || math.IsInf(costUSD, 0) {
		return
	}
	l.stateMu.Lock()
	l.sessionCostUSD += costUSD
	l.stateMu.Unlock()
}

// sessionCostLimitExceeded reports the recorded total and cap when the cap is
// configured and already reached.
func (l *AgentLoop) sessionCostLimitExceeded() (spent, limit float64, exceeded bool) {
	limit = l.Config.SessionCostLimitUSD
	if limit <= 0 {
		return 0, 0, false
	}
	spent = l.SessionCostUSD()
	return spent, limit, spent >= limit
}
