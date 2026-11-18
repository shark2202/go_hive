package schemas

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

type Checkpoint struct {
	CheckpointID       string         `json:"checkpoint_id"`
	CheckpointType     string         `json:"checkpoint_type"`
	SessionID          string         `json:"session_id"`
	RunID              *string        `json:"run_id"`
	CreatedAt          string         `json:"created_at"`
	CurrentNode        *string        `json:"current_node"`
	NextNode           *string        `json:"next_node"`
	ExecutionPath      []string       `json:"execution_path"`
	DataBuffer         map[string]any `json:"data_buffer"`
	AccumulatedOutputs map[string]any `json:"accumulated_outputs"`
	MetricsSnapshot    map[string]any `json:"metrics_snapshot"`
	IsClean            bool           `json:"is_clean"`
	Description        string         `json:"description"`

	Extra map[string]json.RawMessage `json:"-"`
}

func TitleCase(s string) string {
	var b strings.Builder
	newWord := true
	for _, r := range s {
		if unicode.IsLetter(r) {
			if newWord {
				b.WriteRune(unicode.ToUpper(r))
			} else {
				b.WriteRune(unicode.ToLower(r))
			}
			newWord = false
			continue
		}
		b.WriteRune(r)
		newWord = true
	}
	return b.String()
}

func CreateCheckpoint(checkpointType, sessionID string, runID *string, currentNode string,
	executionPath []string, dataBuffer map[string]any, nextNode *string,
	accumulatedOutputs, metricsSnapshot map[string]any, isClean bool, description string) Checkpoint {
	now := time.Now()
	checkpointID := "cp_" + checkpointType + "_" + currentNode + "_" + now.Format("20060102_150405")
	if description == "" {
		description = TitleCase(strings.ReplaceAll(checkpointType, "_", " ")) + ": " + currentNode
	}
	if executionPath == nil {
		executionPath = []string{}
	}
	if dataBuffer == nil {
		dataBuffer = map[string]any{}
	}
	if accumulatedOutputs == nil {
		accumulatedOutputs = map[string]any{}
	}
	if metricsSnapshot == nil {
		metricsSnapshot = map[string]any{}
	}
	return Checkpoint{
		CheckpointID:       checkpointID,
		CheckpointType:     checkpointType,
		SessionID:          sessionID,
		RunID:              runID,
		CreatedAt:          ISOTimeString(now),
		CurrentNode:        &currentNode,
		NextNode:           nextNode,
		ExecutionPath:      executionPath,
		DataBuffer:         dataBuffer,
		AccumulatedOutputs: accumulatedOutputs,
		MetricsSnapshot:    metricsSnapshot,
		IsClean:            isClean,
		Description:        description,
	}
}

func (c Checkpoint) MarshalJSON() ([]byte, error) {
	type plain Checkpoint
	p := plain(c)
	if p.ExecutionPath == nil {
		p.ExecutionPath = []string{}
	}
	if p.DataBuffer == nil {
		p.DataBuffer = map[string]any{}
	}
	if p.AccumulatedOutputs == nil {
		p.AccumulatedOutputs = map[string]any{}
	}
	if p.MetricsSnapshot == nil {
		p.MetricsSnapshot = map[string]any{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, c.Extra)
}

func (c *Checkpoint) UnmarshalJSON(b []byte) error {
	type plain Checkpoint
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["is_clean"]; !ok {
		p.IsClean = true
	}
	*c = Checkpoint(p)
	c.Extra = extras
	if c.ExecutionPath == nil {
		c.ExecutionPath = []string{}
	}
	if c.DataBuffer == nil {
		c.DataBuffer = map[string]any{}
	}
	if c.AccumulatedOutputs == nil {
		c.AccumulatedOutputs = map[string]any{}
	}
	if c.MetricsSnapshot == nil {
		c.MetricsSnapshot = map[string]any{}
	}
	return nil
}

type CheckpointSummary struct {
	CheckpointID   string  `json:"checkpoint_id"`
	CheckpointType string  `json:"checkpoint_type"`
	CreatedAt      string  `json:"created_at"`
	CurrentNode    *string `json:"current_node"`
	NextNode       *string `json:"next_node"`
	IsClean        bool    `json:"is_clean"`
	Description    string  `json:"description"`

	Extra map[string]json.RawMessage `json:"-"`
}

func CheckpointSummaryFrom(c Checkpoint) CheckpointSummary {
	return CheckpointSummary{
		CheckpointID:   c.CheckpointID,
		CheckpointType: c.CheckpointType,
		CreatedAt:      c.CreatedAt,
		CurrentNode:    c.CurrentNode,
		NextNode:       c.NextNode,
		IsClean:        c.IsClean,
		Description:    c.Description,
	}
}

func (s CheckpointSummary) MarshalJSON() ([]byte, error) {
	type plain CheckpointSummary
	b, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, s.Extra)
}

func (s *CheckpointSummary) UnmarshalJSON(b []byte) error {
	type plain CheckpointSummary
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["is_clean"]; !ok {
		p.IsClean = true
	}
	*s = CheckpointSummary(p)
	s.Extra = extras
	return nil
}

type CheckpointIndex struct {
	SessionID          string              `json:"session_id"`
	Checkpoints        []CheckpointSummary `json:"checkpoints"`
	LatestCheckpointID *string             `json:"latest_checkpoint_id"`
	TotalCheckpoints   int                 `json:"total_checkpoints"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (i *CheckpointIndex) AddCheckpoint(c Checkpoint) {
	i.Checkpoints = append(i.Checkpoints, CheckpointSummaryFrom(c))
	id := c.CheckpointID
	i.LatestCheckpointID = &id
	i.TotalCheckpoints = len(i.Checkpoints)
}

func (i *CheckpointIndex) GetCheckpointSummary(id string) *CheckpointSummary {
	for idx := range i.Checkpoints {
		if i.Checkpoints[idx].CheckpointID == id {
			return &i.Checkpoints[idx]
		}
	}
	return nil
}

func (i *CheckpointIndex) FilterByType(checkpointType string) []CheckpointSummary {
	out := []CheckpointSummary{}
	for _, cp := range i.Checkpoints {
		if cp.CheckpointType == checkpointType {
			out = append(out, cp)
		}
	}
	return out
}

func (i *CheckpointIndex) FilterByNode(nodeID string) []CheckpointSummary {
	out := []CheckpointSummary{}
	for _, cp := range i.Checkpoints {
		if cp.CurrentNode != nil && *cp.CurrentNode == nodeID {
			out = append(out, cp)
		}
	}
	return out
}

func (i *CheckpointIndex) GetCleanCheckpoints() []CheckpointSummary {
	out := []CheckpointSummary{}
	for _, cp := range i.Checkpoints {
		if cp.IsClean {
			out = append(out, cp)
		}
	}
	return out
}

func (i *CheckpointIndex) GetLatestCleanCheckpoint() *CheckpointSummary {
	clean := i.GetCleanCheckpoints()
	if len(clean) == 0 {
		return nil
	}
	return &clean[len(clean)-1]
}

func (i CheckpointIndex) MarshalJSON() ([]byte, error) {
	type plain CheckpointIndex
	p := plain(i)
	if p.Checkpoints == nil {
		p.Checkpoints = []CheckpointSummary{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, i.Extra)
}

func (i *CheckpointIndex) UnmarshalJSON(b []byte) error {
	type plain CheckpointIndex
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	*i = CheckpointIndex(p)
	i.Extra = extras
	if i.Checkpoints == nil {
		i.Checkpoints = []CheckpointSummary{}
	}
	return nil
}
