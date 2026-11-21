package schemas

import (
	"encoding/json"
	"fmt"
	"strings"
)

type GoalStatus string

const (
	GoalDraft     GoalStatus = "draft"
	GoalReady     GoalStatus = "ready"
	GoalActive    GoalStatus = "active"
	GoalCompleted GoalStatus = "completed"
	GoalFailed    GoalStatus = "failed"
	GoalSuspended GoalStatus = "suspended"
)

func (s GoalStatus) valid() bool {
	switch s {
	case GoalDraft, GoalReady, GoalActive, GoalCompleted, GoalFailed, GoalSuspended:
		return true
	}
	return false
}

type SuccessCriterion struct {
	ID          string  `json:"id,omitempty"`
	Description string  `json:"description"`
	Metric      string  `json:"metric"`
	Type        string  `json:"type"`
	Target      any     `json:"target"`
	Weight      float64 `json:"weight"`
	Met         bool    `json:"met"`

	// weightSet records that the input carried an explicit "weight" key. A
	// missing key means the Python default (1.0); an explicit 0 stays 0.
	weightSet bool

	Extra map[string]json.RawMessage `json:"-"`
}

// EffectiveWeight is the weight the criterion carries. schemas/goal.py declares
// weight with default=1.0, so a criterion whose weight was never supplied is
// weighted 1.0; only an explicit 0 (JSON key present or SetWeight(0)) weighs
// nothing. The Go zero value cannot tell those apart on its own, which is what
// weightSet is for.
//
// Parity: framework/schemas/goal.py::SuccessCriterion (weight field default).
func (c SuccessCriterion) EffectiveWeight() float64 {
	if !c.weightSet && c.Weight == 0 {
		return 1.0
	}
	return c.Weight
}

// SetWeight records an explicit weight, including 0. Constructing a criterion
// in Go with SetWeight(0) is how a caller states "this criterion weighs
// nothing"; leaving Weight unset means the schema default.
func (c *SuccessCriterion) SetWeight(weight float64) {
	c.Weight = weight
	c.weightSet = true
}

func (c *SuccessCriterion) Validate() error {
	if c.Weight < 0 || c.Weight > 1 {
		return fmt.Errorf("weight must be between 0 and 1")
	}
	return nil
}

func (c SuccessCriterion) MarshalJSON() ([]byte, error) {
	type plain SuccessCriterion
	p := plain(c)
	if p.Type == "" {
		p.Type = "success_rate"
	}
	// An absent weight carries the schema default (1.0); an explicit 0 must
	// survive verbatim (schemas/goal.py keeps weight=0.0).
	p.Weight = c.EffectiveWeight()
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, c.Extra)
}

func (c *SuccessCriterion) UnmarshalJSON(b []byte) error {
	type plain SuccessCriterion
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["type"]; !ok {
		p.Type = "success_rate"
	}
	_, weightPresent := raw["weight"]
	if !weightPresent {
		p.Weight = 1.0
	}
	p.weightSet = weightPresent
	*c = SuccessCriterion(p)
	c.Extra = extras
	return c.Validate()
}

type Constraint struct {
	ID             string `json:"id,omitempty"`
	Description    string `json:"description"`
	ConstraintType string `json:"constraint_type"`
	Category       string `json:"category"`
	Check          string `json:"check"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (c Constraint) MarshalJSON() ([]byte, error) {
	type plain Constraint
	p := plain(c)
	if p.Category == "" {
		p.Category = "general"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, c.Extra)
}

func (c *Constraint) UnmarshalJSON(b []byte) error {
	type plain Constraint
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	if p.Category == "" {
		p.Category = "general"
	}
	*c = Constraint(p)
	c.Extra = extras
	return nil
}

type Goal struct {
	ID                   string             `json:"id,omitempty"`
	Name                 string             `json:"name"`
	Description          string             `json:"description"`
	Status               GoalStatus         `json:"status"`
	SuccessCriteria      []SuccessCriterion `json:"success_criteria"`
	Constraints          []Constraint       `json:"constraints"`
	Context              OrderedMap         `json:"context"`
	RequiredCapabilities []string           `json:"required_capabilities"`
	Version              string             `json:"version"`
	ParentVersion        *string            `json:"parent_version"`
	EvolutionReason      *string            `json:"evolution_reason"`
	CreatedAt            Timestamp          `json:"created_at"`
	UpdatedAt            Timestamp          `json:"updated_at"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (g *Goal) IsSuccess() bool {
	if len(g.SuccessCriteria) == 0 {
		return false
	}
	total := 0.0
	met := 0.0
	for _, c := range g.SuccessCriteria {
		weight := c.EffectiveWeight()
		total += weight
		if c.Met {
			met += weight
		}
	}
	return met >= total*0.9
}

func (g *Goal) ToPromptContext() string {
	if len(g.SuccessCriteria) == 0 && len(g.Constraints) == 0 && g.Context.Len() == 0 {
		return ""
	}
	lines := []string{
		"# Goal: " + g.Name,
		g.Description,
		"",
		"## Success Criteria:",
	}
	for _, sc := range g.SuccessCriteria {
		lines = append(lines, "- "+sc.Description)
	}
	if len(g.Constraints) > 0 {
		lines = append(lines, "", "## Constraints:")
		for _, c := range g.Constraints {
			severity := "SHOULD"
			if c.ConstraintType == "hard" {
				severity = "MUST"
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s", severity, c.Description))
		}
	}
	if g.Context.Len() > 0 {
		lines = append(lines, "", "## Context:")
		// Context renders in the order the JSON author wrote it: Python
		// iterates dict insertion order (schemas/goal.py::Goal.
		// to_prompt_context), so sorting here would reorder the block.
		for _, key := range g.Context.Keys() {
			value, _ := g.Context.Get(key)
			lines = append(lines, fmt.Sprintf("- %s: %v", key, value))
		}
	}
	return strings.Join(lines, "\n")
}

func (g *Goal) Validate() error {
	if g.Status == "" {
		g.Status = GoalDraft
	}
	if !g.Status.valid() {
		return fmt.Errorf("invalid status %q", g.Status)
	}
	for i := range g.SuccessCriteria {
		if err := g.SuccessCriteria[i].Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (g Goal) MarshalJSON() ([]byte, error) {
	type plain Goal
	p := plain(g)
	if p.Status == "" {
		p.Status = GoalDraft
	}
	if p.SuccessCriteria == nil {
		p.SuccessCriteria = []SuccessCriterion{}
	}
	if p.Constraints == nil {
		p.Constraints = []Constraint{}
	}
	if p.RequiredCapabilities == nil {
		p.RequiredCapabilities = []string{}
	}
	if p.Version == "" {
		p.Version = "1.0.0"
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, g.Extra)
}

func (g *Goal) UnmarshalJSON(b []byte) error {
	type plain Goal
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	now := NowTimestamp()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}
	*g = Goal(p)
	g.Extra = extras
	if g.Status == "" {
		g.Status = GoalDraft
	}
	if g.SuccessCriteria == nil {
		g.SuccessCriteria = []SuccessCriterion{}
	}
	if g.Constraints == nil {
		g.Constraints = []Constraint{}
	}
	if g.RequiredCapabilities == nil {
		g.RequiredCapabilities = []string{}
	}
	if g.Version == "" {
		g.Version = "1.0.0"
	}
	return g.Validate()
}
