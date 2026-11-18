package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type SessionStatus string

const (
	SessionActive    SessionStatus = "active"
	SessionPaused    SessionStatus = "paused"
	SessionCompleted SessionStatus = "completed"
	SessionFailed    SessionStatus = "failed"
	SessionCancelled SessionStatus = "cancelled"
)

func (s SessionStatus) valid() bool {
	switch s {
	case SessionActive, SessionPaused, SessionCompleted, SessionFailed, SessionCancelled:
		return true
	}
	return false
}

type SessionTimestamps struct {
	StartedAt    Timestamp  `json:"started_at"`
	UpdatedAt    Timestamp  `json:"updated_at"`
	CompletedAt  *Timestamp `json:"completed_at"`
	PausedAtTime *Timestamp `json:"paused_at_time"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (t SessionTimestamps) MarshalJSON() ([]byte, error) {
	type plain SessionTimestamps
	b, err := json.Marshal(plain(t))
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, t.Extra)
}

func (t *SessionTimestamps) UnmarshalJSON(b []byte) error {
	type plain SessionTimestamps
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	*t = SessionTimestamps(p)
	t.Extra = extras
	return nil
}

type SessionProgress struct {
	CurrentNode        *string        `json:"current_node"`
	PausedAt           *string        `json:"paused_at"`
	ResumeFrom         *string        `json:"resume_from"`
	StepsExecuted      int            `json:"steps_executed"`
	TotalTokens        int            `json:"total_tokens"`
	TotalLatencyMS     int            `json:"total_latency_ms"`
	Path               []string       `json:"path"`
	TotalRetries       int            `json:"total_retries"`
	NodesWithFailures  []string       `json:"nodes_with_failures"`
	RetryDetails       map[string]int `json:"retry_details"`
	HadPartialFailures bool           `json:"had_partial_failures"`
	ExecutionQuality   string         `json:"execution_quality"`
	NodeVisitCounts    map[string]int `json:"node_visit_counts"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (p SessionProgress) MarshalJSON() ([]byte, error) {
	type plain SessionProgress
	v := plain(p)
	if v.Path == nil {
		v.Path = []string{}
	}
	if v.NodesWithFailures == nil {
		v.NodesWithFailures = []string{}
	}
	if v.RetryDetails == nil {
		v.RetryDetails = map[string]int{}
	}
	if v.NodeVisitCounts == nil {
		v.NodeVisitCounts = map[string]int{}
	}
	if v.ExecutionQuality == "" {
		v.ExecutionQuality = "clean"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, p.Extra)
}

func (p *SessionProgress) UnmarshalJSON(b []byte) error {
	type plain SessionProgress
	var v plain
	extras, err := unmarshalStruct(b, &v)
	if err != nil {
		return err
	}
	*p = SessionProgress(v)
	p.Extra = extras
	if p.Path == nil {
		p.Path = []string{}
	}
	if p.NodesWithFailures == nil {
		p.NodesWithFailures = []string{}
	}
	if p.RetryDetails == nil {
		p.RetryDetails = map[string]int{}
	}
	if p.NodeVisitCounts == nil {
		p.NodeVisitCounts = map[string]int{}
	}
	if p.ExecutionQuality == "" {
		p.ExecutionQuality = "clean"
	}
	return nil
}

type SessionResult struct {
	Success *bool          `json:"success"`
	Error   *string        `json:"error"`
	Output  map[string]any `json:"output"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (r SessionResult) MarshalJSON() ([]byte, error) {
	type plain SessionResult
	v := plain(r)
	if v.Output == nil {
		v.Output = map[string]any{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, r.Extra)
}

func (r *SessionResult) UnmarshalJSON(b []byte) error {
	type plain SessionResult
	var v plain
	extras, err := unmarshalStruct(b, &v)
	if err != nil {
		return err
	}
	*r = SessionResult(v)
	r.Extra = extras
	if r.Output == nil {
		r.Output = map[string]any{}
	}
	return nil
}

type SessionMetrics struct {
	DecisionCount     int      `json:"decision_count"`
	ProblemCount      int      `json:"problem_count"`
	TotalInputTokens  int      `json:"total_input_tokens"`
	TotalOutputTokens int      `json:"total_output_tokens"`
	NodesExecuted     []string `json:"nodes_executed"`
	EdgesTraversed    []string `json:"edges_traversed"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (m SessionMetrics) MarshalJSON() ([]byte, error) {
	type plain SessionMetrics
	v := plain(m)
	if v.NodesExecuted == nil {
		v.NodesExecuted = []string{}
	}
	if v.EdgesTraversed == nil {
		v.EdgesTraversed = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, m.Extra)
}

func (m *SessionMetrics) UnmarshalJSON(b []byte) error {
	type plain SessionMetrics
	var v plain
	extras, err := unmarshalStruct(b, &v)
	if err != nil {
		return err
	}
	*m = SessionMetrics(v)
	m.Extra = extras
	if m.NodesExecuted == nil {
		m.NodesExecuted = []string{}
	}
	if m.EdgesTraversed == nil {
		m.EdgesTraversed = []string{}
	}
	return nil
}

type SessionState struct {
	SchemaVersion      string            `json:"schema_version"`
	SessionID          string            `json:"session_id"`
	StreamID           string            `json:"stream_id"`
	CorrelationID      string            `json:"correlation_id"`
	Status             SessionStatus     `json:"status"`
	GoalID             string            `json:"goal_id"`
	AgentID            string            `json:"agent_id"`
	EntryPoint         string            `json:"entry_point"`
	Timestamps         SessionTimestamps `json:"timestamps"`
	Progress           SessionProgress   `json:"progress"`
	Result             SessionResult     `json:"result"`
	DataBuffer         map[string]any    `json:"data_buffer"`
	Metrics            SessionMetrics    `json:"metrics"`
	Problems           []map[string]any  `json:"problems"`
	Decisions          []map[string]any  `json:"decisions"`
	InputData          map[string]any    `json:"input_data"`
	CurrentRunID       *string           `json:"current_run_id"`
	PID                *int              `json:"pid"`
	IsolationLevel     string            `json:"isolation_level"`
	CheckpointEnabled  bool              `json:"checkpoint_enabled"`
	LatestCheckpointID *string           `json:"latest_checkpoint_id"`
	ActiveTriggers     []string          `json:"active_triggers"`
	TriggerTasks       map[string]string `json:"trigger_tasks"`
	WorkerConfigured   bool              `json:"worker_configured"`

	Extra map[string]json.RawMessage `json:"-"`
}

func NewSessionState(sessionID, goalID string, now time.Time) *SessionState {
	return &SessionState{
		SchemaVersion:  "1.1",
		SessionID:      sessionID,
		Status:         SessionActive,
		GoalID:         goalID,
		EntryPoint:     "start",
		Timestamps:     SessionTimestamps{StartedAt: NewTimestamp(now), UpdatedAt: NewTimestamp(now)},
		Progress:       SessionProgress{Path: []string{}, NodesWithFailures: []string{}, RetryDetails: map[string]int{}, NodeVisitCounts: map[string]int{}, ExecutionQuality: "clean"},
		Result:         SessionResult{Output: map[string]any{}},
		DataBuffer:     map[string]any{},
		Metrics:        SessionMetrics{NodesExecuted: []string{}, EdgesTraversed: []string{}},
		Problems:       []map[string]any{},
		Decisions:      []map[string]any{},
		InputData:      map[string]any{},
		IsolationLevel: "shared",
		ActiveTriggers: []string{},
		TriggerTasks:   map[string]string{},
	}
}

func (s *SessionState) Memory() map[string]any { return s.DataBuffer }

func (s *SessionState) SetMemory(v map[string]any) { s.DataBuffer = v }

func (s *SessionState) DurationMS() int {
	if s.Timestamps.CompletedAt == nil || s.Timestamps.CompletedAt.IsZero() {
		return 0
	}
	return int(s.Timestamps.CompletedAt.Sub(s.Timestamps.StartedAt.Time) / time.Millisecond)
}

func (s *SessionState) IsResumableFromCheckpoint() bool {
	return s.CheckpointEnabled && s.LatestCheckpointID != nil
}

func (s *SessionState) IsResumable() bool { return s.IsResumableFromCheckpoint() }

func (s *SessionState) Validate() error {
	if s.SessionID == "" {
		return errors.New("session_id is required")
	}
	if s.GoalID == "" {
		return errors.New("goal_id is required")
	}
	if s.SchemaVersion == "" {
		s.SchemaVersion = "1.1"
	}
	if s.Status == "" {
		s.Status = SessionActive
	}
	if !s.Status.valid() {
		return fmt.Errorf("invalid status %q", s.Status)
	}
	if s.Timestamps.StartedAt.IsZero() {
		return errors.New("timestamps.started_at is required")
	}
	if s.Timestamps.UpdatedAt.IsZero() {
		return errors.New("timestamps.updated_at is required")
	}
	return nil
}

func (s *SessionState) ToSessionStateDict() map[string]any {
	var resumeFrom any
	switch {
	case s.Progress.ResumeFrom != nil:
		resumeFrom = *s.Progress.ResumeFrom
	case s.Progress.PausedAt != nil:
		resumeFrom = *s.Progress.PausedAt
	case len(s.Progress.Path) > 0:
		resumeFrom = s.Progress.Path[len(s.Progress.Path)-1]
	default:
		resumeFrom = nil
	}
	return map[string]any{
		"paused_at":         resumeFrom,
		"resume_from":       resumeFrom,
		"data_buffer":       s.DataBuffer,
		"execution_path":    s.Progress.Path,
		"node_visit_counts": s.Progress.NodeVisitCounts,
	}
}

type ExecutionResult struct {
	PausedAt           *string
	Success            bool
	Error              *string
	Output             map[string]any
	Path               []string
	StepsExecuted      int
	TotalTokens        int
	TotalLatencyMS     int
	TotalRetries       int
	NodesWithFailures  []string
	RetryDetails       map[string]int
	HadPartialFailures bool
	ExecutionQuality   string
	NodeVisitCounts    map[string]int
	SessionState       map[string]any
}

func SessionStateFromExecutionResult(sessionID, goalID string, result ExecutionResult,
	streamID, correlationID, startedAt string, inputData map[string]any,
	agentID, entryPoint string) *SessionState {
	now := time.Now()
	nowTS := NewTimestamp(now)

	status := SessionFailed
	if result.PausedAt != nil && *result.PausedAt != "" {
		status = SessionPaused
	} else if result.Success {
		status = SessionCompleted
	}

	startTS := nowTS
	if startedAt != "" {
		if parsed, err := ParseISOTime(startedAt); err == nil {
			startTS = NewTimestamp(parsed)
		}
	}

	timestamps := SessionTimestamps{StartedAt: startTS, UpdatedAt: nowTS}
	if status != SessionPaused {
		timestamps.CompletedAt = &nowTS
	} else {
		timestamps.PausedAtTime = &nowTS
	}

	var currentNode *string
	if result.PausedAt != nil {
		currentNode = result.PausedAt
	} else if len(result.Path) > 0 {
		last := result.Path[len(result.Path)-1]
		currentNode = &last
	}

	var resumeFrom *string
	if result.SessionState != nil {
		if v, ok := result.SessionState["resume_from"].(string); ok {
			resumeFrom = &v
		}
	}

	dataBuffer := map[string]any{}
	if result.SessionState != nil {
		if v, ok := result.SessionState["data_buffer"].(map[string]any); ok {
			dataBuffer = v
		} else if v, ok := result.SessionState["memory"].(map[string]any); ok {
			dataBuffer = v
		}
	}

	if inputData == nil {
		inputData = map[string]any{}
	}
	if result.Path == nil {
		result.Path = []string{}
	}
	if result.NodesWithFailures == nil {
		result.NodesWithFailures = []string{}
	}
	if result.RetryDetails == nil {
		result.RetryDetails = map[string]int{}
	}
	if result.NodeVisitCounts == nil {
		result.NodeVisitCounts = map[string]int{}
	}
	if result.ExecutionQuality == "" {
		result.ExecutionQuality = "clean"
	}
	if entryPoint == "" {
		entryPoint = "start"
	}

	return &SessionState{
		SchemaVersion: "1.1",
		SessionID:     sessionID,
		StreamID:      streamID,
		CorrelationID: correlationID,
		Status:        status,
		GoalID:        goalID,
		AgentID:       agentID,
		EntryPoint:    entryPoint,
		Timestamps:    timestamps,
		Progress: SessionProgress{
			CurrentNode:        currentNode,
			PausedAt:           result.PausedAt,
			ResumeFrom:         resumeFrom,
			StepsExecuted:      result.StepsExecuted,
			TotalTokens:        result.TotalTokens,
			TotalLatencyMS:     result.TotalLatencyMS,
			Path:               result.Path,
			TotalRetries:       result.TotalRetries,
			NodesWithFailures:  result.NodesWithFailures,
			RetryDetails:       result.RetryDetails,
			HadPartialFailures: result.HadPartialFailures,
			ExecutionQuality:   result.ExecutionQuality,
			NodeVisitCounts:    result.NodeVisitCounts,
		},
		Result: SessionResult{
			Success: boolPtr(result.Success),
			Error:   result.Error,
			Output:  OrEmptyMap(result.Output),
		},
		DataBuffer:     dataBuffer,
		Metrics:        SessionMetrics{NodesExecuted: []string{}, EdgesTraversed: []string{}},
		Problems:       []map[string]any{},
		Decisions:      []map[string]any{},
		InputData:      inputData,
		IsolationLevel: "shared",
		ActiveTriggers: []string{},
		TriggerTasks:   map[string]string{},
	}
}

func SessionStateFromLegacyRun(run *Run, sessionID, streamID string) *SessionState {
	now := NowTimestamp()

	var status SessionStatus
	switch run.Status {
	case RunRunning:
		status = SessionActive
	case RunCompleted:
		status = SessionCompleted
	case RunFailed:
		status = SessionFailed
	case RunCancelled:
		status = SessionCancelled
	case RunStuck:
		status = SessionFailed
	default:
		status = SessionFailed
	}

	decisions := make([]map[string]any, 0, len(run.Decisions))
	for i := range run.Decisions {
		if d, err := ToDict(&run.Decisions[i]); err == nil {
			decisions = append(decisions, d)
		}
	}
	problems := make([]map[string]any, 0, len(run.Problems))
	for _, p := range run.Problems {
		if d, err := ToDict(p); err == nil {
			problems = append(problems, d)
		}
	}

	return &SessionState{
		SchemaVersion: "1.0",
		SessionID:     sessionID,
		StreamID:      streamID,
		Status:        status,
		GoalID:        run.GoalID,
		Timestamps: SessionTimestamps{
			StartedAt:   run.StartedAt,
			UpdatedAt:   now,
			CompletedAt: run.CompletedAt,
		},
		Progress: SessionProgress{
			Path:              []string{},
			NodesWithFailures: []string{},
			RetryDetails:      map[string]int{},
			NodeVisitCounts:   map[string]int{},
			ExecutionQuality:  "clean",
		},
		Result: SessionResult{
			Success: boolPtr(run.Status == RunCompleted),
			Output:  OrEmptyMap(run.OutputData),
		},
		DataBuffer: map[string]any{},
		Metrics: SessionMetrics{
			DecisionCount:     run.Metrics.TotalDecisions,
			ProblemCount:      len(run.Problems),
			TotalInputTokens:  run.Metrics.TotalTokens,
			TotalOutputTokens: 0,
			NodesExecuted:     append([]string{}, run.Metrics.NodesExecuted...),
			EdgesTraversed:    append([]string{}, run.Metrics.EdgesTraversed...),
		},
		Decisions:      decisions,
		Problems:       problems,
		InputData:      OrEmptyMap(run.InputData),
		IsolationLevel: "shared",
		ActiveTriggers: []string{},
		TriggerTasks:   map[string]string{},
	}
}

func boolPtr(b bool) *bool { return &b }

func (s SessionState) MarshalJSON() ([]byte, error) {
	type plain SessionState
	p := plain(s)
	if p.SchemaVersion == "" {
		p.SchemaVersion = "1.1"
	}
	if p.Status == "" {
		p.Status = SessionActive
	}
	if p.EntryPoint == "" {
		p.EntryPoint = "start"
	}
	if p.IsolationLevel == "" {
		p.IsolationLevel = "shared"
	}
	if p.Progress.Path == nil {
		p.Progress.Path = []string{}
	}
	if p.Progress.NodesWithFailures == nil {
		p.Progress.NodesWithFailures = []string{}
	}
	if p.Progress.RetryDetails == nil {
		p.Progress.RetryDetails = map[string]int{}
	}
	if p.Progress.NodeVisitCounts == nil {
		p.Progress.NodeVisitCounts = map[string]int{}
	}
	if p.Progress.ExecutionQuality == "" {
		p.Progress.ExecutionQuality = "clean"
	}
	if p.Result.Output == nil {
		p.Result.Output = map[string]any{}
	}
	if p.DataBuffer == nil {
		p.DataBuffer = map[string]any{}
	}
	if p.Metrics.NodesExecuted == nil {
		p.Metrics.NodesExecuted = []string{}
	}
	if p.Metrics.EdgesTraversed == nil {
		p.Metrics.EdgesTraversed = []string{}
	}
	if p.Problems == nil {
		p.Problems = []map[string]any{}
	}
	if p.Decisions == nil {
		p.Decisions = []map[string]any{}
	}
	if p.InputData == nil {
		p.InputData = map[string]any{}
	}
	if p.ActiveTriggers == nil {
		p.ActiveTriggers = []string{}
	}
	if p.TriggerTasks == nil {
		p.TriggerTasks = map[string]string{}
	}
	b, err := json.Marshal(struct {
		plain
		DurationMS                int  `json:"duration_ms"`
		IsResumable               bool `json:"is_resumable"`
		IsResumableFromCheckpoint bool `json:"is_resumable_from_checkpoint"`
	}{
		plain:                     p,
		DurationMS:                s.DurationMS(),
		IsResumable:               s.IsResumable(),
		IsResumableFromCheckpoint: s.IsResumableFromCheckpoint(),
	})
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, s.Extra)
}

func (s *SessionState) UnmarshalJSON(b []byte) error {
	type plain SessionState
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["data_buffer"]; !ok {
		if mem, ok := raw["memory"]; ok {
			var v map[string]any
			if err := json.Unmarshal(mem, &v); err != nil {
				return err
			}
			p.DataBuffer = v
		}
	}
	for _, k := range []string{"duration_ms", "is_resumable", "is_resumable_from_checkpoint", "memory"} {
		delete(extras, k)
	}
	*s = SessionState(p)
	s.Extra = extras
	if s.SchemaVersion == "" {
		s.SchemaVersion = "1.1"
	}
	if s.Status == "" {
		s.Status = SessionActive
	}
	if s.EntryPoint == "" {
		s.EntryPoint = "start"
	}
	if s.IsolationLevel == "" {
		s.IsolationLevel = "shared"
	}
	if s.Progress.Path == nil {
		s.Progress.Path = []string{}
	}
	if s.Progress.NodesWithFailures == nil {
		s.Progress.NodesWithFailures = []string{}
	}
	if s.Progress.RetryDetails == nil {
		s.Progress.RetryDetails = map[string]int{}
	}
	if s.Progress.NodeVisitCounts == nil {
		s.Progress.NodeVisitCounts = map[string]int{}
	}
	if s.Progress.ExecutionQuality == "" {
		s.Progress.ExecutionQuality = "clean"
	}
	if s.Result.Output == nil {
		s.Result.Output = map[string]any{}
	}
	if s.DataBuffer == nil {
		s.DataBuffer = map[string]any{}
	}
	if s.Metrics.NodesExecuted == nil {
		s.Metrics.NodesExecuted = []string{}
	}
	if s.Metrics.EdgesTraversed == nil {
		s.Metrics.EdgesTraversed = []string{}
	}
	if s.Problems == nil {
		s.Problems = []map[string]any{}
	}
	if s.Decisions == nil {
		s.Decisions = []map[string]any{}
	}
	if s.InputData == nil {
		s.InputData = map[string]any{}
	}
	if s.ActiveTriggers == nil {
		s.ActiveTriggers = []string{}
	}
	if s.TriggerTasks == nil {
		s.TriggerTasks = map[string]string{}
	}
	return s.Validate()
}
