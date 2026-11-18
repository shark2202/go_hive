package schemas

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunStuck     RunStatus = "stuck"
	RunCancelled RunStatus = "cancelled"
)

func (s RunStatus) valid() bool {
	switch s {
	case RunRunning, RunCompleted, RunFailed, RunStuck, RunCancelled:
		return true
	}
	return false
}

type Problem struct {
	ID           string    `json:"id"`
	Severity     string    `json:"severity"`
	Description  string    `json:"description"`
	RootCause    *string   `json:"root_cause"`
	DecisionID   *string   `json:"decision_id"`
	Timestamp    Timestamp `json:"timestamp"`
	SuggestedFix *string   `json:"suggested_fix"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (p Problem) MarshalJSON() ([]byte, error) {
	type plain Problem
	b, err := json.Marshal(plain(p))
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, p.Extra)
}

func (p *Problem) UnmarshalJSON(b []byte) error {
	type plain Problem
	var v plain
	extras, err := unmarshalStruct(b, &v)
	if err != nil {
		return err
	}
	*p = Problem(v)
	p.Extra = extras
	if p.Timestamp.IsZero() {
		p.Timestamp = NowTimestamp()
	}
	return nil
}

type RunMetrics struct {
	TotalDecisions      int      `json:"total_decisions"`
	SuccessfulDecisions int      `json:"successful_decisions"`
	FailedDecisions     int      `json:"failed_decisions"`
	TotalTokens         int      `json:"total_tokens"`
	TotalLatencyMS      int      `json:"total_latency_ms"`
	NodesExecuted       []string `json:"nodes_executed"`
	EdgesTraversed      []string `json:"edges_traversed"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (m *RunMetrics) SuccessRate() float64 {
	if m.TotalDecisions == 0 {
		return 0.0
	}
	return float64(m.SuccessfulDecisions) / float64(m.TotalDecisions)
}

func (m RunMetrics) MarshalJSON() ([]byte, error) {
	type plain RunMetrics
	p := plain(m)
	if p.NodesExecuted == nil {
		p.NodesExecuted = []string{}
	}
	if p.EdgesTraversed == nil {
		p.EdgesTraversed = []string{}
	}
	b, err := json.Marshal(struct {
		plain
		SuccessRate float64 `json:"success_rate"`
	}{plain: p, SuccessRate: m.SuccessRate()})
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, m.Extra)
}

func (m *RunMetrics) UnmarshalJSON(b []byte) error {
	type plain RunMetrics
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	*m = RunMetrics(p)
	m.Extra = extras
	if m.NodesExecuted == nil {
		m.NodesExecuted = []string{}
	}
	if m.EdgesTraversed == nil {
		m.EdgesTraversed = []string{}
	}
	return nil
}

type Run struct {
	ID              string         `json:"id"`
	GoalID          string         `json:"goal_id"`
	StartedAt       Timestamp      `json:"started_at"`
	Status          RunStatus      `json:"status"`
	CompletedAt     *Timestamp     `json:"completed_at"`
	Decisions       []Decision     `json:"decisions"`
	Problems        []Problem      `json:"problems"`
	Metrics         RunMetrics     `json:"metrics"`
	Narrative       string         `json:"narrative"`
	GoalDescription string         `json:"goal_description"`
	InputData       map[string]any `json:"input_data"`
	OutputData      map[string]any `json:"output_data"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (r *Run) DurationMS() int {
	if r.CompletedAt == nil {
		return 0
	}
	return int(r.CompletedAt.Sub(r.StartedAt.Time) / time.Millisecond)
}

func (r *Run) AddDecision(d Decision) {
	r.Decisions = append(r.Decisions, d)
	r.Metrics.TotalDecisions++
	seen := false
	for _, node := range r.Metrics.NodesExecuted {
		if node == d.NodeID {
			seen = true
			break
		}
	}
	if !seen {
		r.Metrics.NodesExecuted = append(r.Metrics.NodesExecuted, d.NodeID)
	}
}

func (r *Run) RecordOutcome(decisionID string, outcome Outcome) {
	for i := range r.Decisions {
		if r.Decisions[i].ID != decisionID {
			continue
		}
		r.Decisions[i].Outcome = &outcome
		if outcome.Success {
			r.Metrics.SuccessfulDecisions++
		} else {
			r.Metrics.FailedDecisions++
		}
		r.Metrics.TotalTokens += outcome.TokensUsed
		r.Metrics.TotalLatencyMS += outcome.LatencyMS
		return
	}
}

func (r *Run) AddProblem(severity, description string, decisionID, rootCause, suggestedFix *string) string {
	id := fmt.Sprintf("prob_%d", len(r.Problems))
	r.Problems = append(r.Problems, Problem{
		ID:           id,
		Severity:     severity,
		Description:  description,
		DecisionID:   decisionID,
		RootCause:    rootCause,
		SuggestedFix: suggestedFix,
		Timestamp:    NowTimestamp(),
	})
	return id
}

func (r *Run) Complete(status RunStatus, narrative string) {
	r.Status = status
	now := NowTimestamp()
	r.CompletedAt = &now
	if narrative != "" {
		r.Narrative = narrative
	} else {
		r.Narrative = r.generateNarrative()
	}
}

func (r *Run) generateNarrative() string {
	parts := []string{}
	statusText := "failed"
	if r.Status == RunCompleted {
		statusText = "completed successfully"
	}
	parts = append(parts, fmt.Sprintf("Run %s.", statusText))
	parts = append(parts, fmt.Sprintf("Made %d decisions: %d succeeded, %d failed.",
		r.Metrics.TotalDecisions, r.Metrics.SuccessfulDecisions, r.Metrics.FailedDecisions))

	var critical, warnings []string
	for _, p := range r.Problems {
		switch p.Severity {
		case "critical":
			critical = append(critical, p.Description)
		case "warning":
			warnings = append(warnings, p.Description)
		}
	}
	if len(critical) > 0 {
		parts = append(parts, "Critical issues: "+strings.Join(critical, ", "))
	}
	if len(warnings) > 0 {
		parts = append(parts, "Warnings: "+strings.Join(warnings, ", "))
	}

	var failed []string
	for i := range r.Decisions {
		if !r.Decisions[i].WasSuccessful() {
			failed = append(failed, r.Decisions[i].Intent)
			if len(failed) == 3 {
				break
			}
		}
	}
	if len(failed) > 0 {
		parts = append(parts, "Failed on: "+strings.Join(failed, ", "))
	}
	return strings.Join(parts, " ")
}

func (r *Run) Validate() error {
	if r.Status == "" {
		r.Status = RunRunning
	}
	if !r.Status.valid() {
		return fmt.Errorf("invalid status %q", r.Status)
	}
	return nil
}

func (r Run) MarshalJSON() ([]byte, error) {
	type plain Run
	p := plain(r)
	if p.Status == "" {
		p.Status = RunRunning
	}
	if p.Decisions == nil {
		p.Decisions = []Decision{}
	}
	if p.Problems == nil {
		p.Problems = []Problem{}
	}
	if p.InputData == nil {
		p.InputData = map[string]any{}
	}
	if p.OutputData == nil {
		p.OutputData = map[string]any{}
	}
	b, err := json.Marshal(struct {
		plain
		DurationMS int `json:"duration_ms"`
	}{plain: p, DurationMS: r.DurationMS()})
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, r.Extra)
}

func (r *Run) UnmarshalJSON(b []byte) error {
	type plain Run
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	*r = Run(p)
	r.Extra = extras
	if r.Status == "" {
		r.Status = RunRunning
	}
	if r.Decisions == nil {
		r.Decisions = []Decision{}
	}
	if r.Problems == nil {
		r.Problems = []Problem{}
	}
	if r.Metrics.NodesExecuted == nil {
		r.Metrics.NodesExecuted = []string{}
	}
	if r.Metrics.EdgesTraversed == nil {
		r.Metrics.EdgesTraversed = []string{}
	}
	if r.InputData == nil {
		r.InputData = map[string]any{}
	}
	if r.OutputData == nil {
		r.OutputData = map[string]any{}
	}
	return r.Validate()
}

type RunSummary struct {
	RunID            string    `json:"run_id"`
	GoalID           string    `json:"goal_id"`
	Status           RunStatus `json:"status"`
	DurationMS       int       `json:"duration_ms"`
	DecisionCount    int       `json:"decision_count"`
	SuccessRate      float64   `json:"success_rate"`
	ProblemCount     int       `json:"problem_count"`
	Narrative        string    `json:"narrative"`
	KeyDecisions     []string  `json:"key_decisions"`
	CriticalProblems []string  `json:"critical_problems"`
	Warnings         []string  `json:"warnings"`
	Successes        []string  `json:"successes"`

	Extra map[string]json.RawMessage `json:"-"`
}

func RunSummaryFromRun(run *Run) RunSummary {
	keyDecisions := []string{}
	for i := range run.Decisions {
		d := &run.Decisions[i]
		if !d.WasSuccessful() {
			keyDecisions = append(keyDecisions, d.SummaryForBuilder())
		} else if d.Evaluation != nil && d.Evaluation.OutcomeQuality > 0.8 {
			keyDecisions = append(keyDecisions, d.SummaryForBuilder())
		}
	}
	if len(keyDecisions) > 5 {
		keyDecisions = keyDecisions[:5]
	}

	critical := []string{}
	warnings := []string{}
	for _, p := range run.Problems {
		switch p.Severity {
		case "critical":
			critical = append(critical, p.Description)
		case "warning":
			warnings = append(warnings, p.Description)
		}
	}

	successes := []string{}
	for i := range run.Decisions {
		d := &run.Decisions[i]
		if d.WasSuccessful() && d.Outcome != nil && d.Outcome.Summary != "" {
			successes = append(successes, d.Outcome.Summary)
			if len(successes) == 3 {
				break
			}
		}
	}

	return RunSummary{
		RunID:            run.ID,
		GoalID:           run.GoalID,
		Status:           run.Status,
		DurationMS:       run.DurationMS(),
		DecisionCount:    run.Metrics.TotalDecisions,
		SuccessRate:      run.Metrics.SuccessRate(),
		ProblemCount:     len(run.Problems),
		Narrative:        run.Narrative,
		KeyDecisions:     keyDecisions,
		CriticalProblems: critical,
		Warnings:         warnings,
		Successes:        successes,
	}
}

func (s RunSummary) MarshalJSON() ([]byte, error) {
	type plain RunSummary
	p := plain(s)
	if p.KeyDecisions == nil {
		p.KeyDecisions = []string{}
	}
	if p.CriticalProblems == nil {
		p.CriticalProblems = []string{}
	}
	if p.Warnings == nil {
		p.Warnings = []string{}
	}
	if p.Successes == nil {
		p.Successes = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, s.Extra)
}

func (s *RunSummary) UnmarshalJSON(b []byte) error {
	type plain RunSummary
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	*s = RunSummary(p)
	s.Extra = extras
	if s.KeyDecisions == nil {
		s.KeyDecisions = []string{}
	}
	if s.CriticalProblems == nil {
		s.CriticalProblems = []string{}
	}
	if s.Warnings == nil {
		s.Warnings = []string{}
	}
	if s.Successes == nil {
		s.Successes = []string{}
	}
	return nil
}
