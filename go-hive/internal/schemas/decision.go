package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
)

type DecisionType string

const (
	DecisionToolSelection   DecisionType = "tool_selection"
	DecisionParameterChoice DecisionType = "parameter_choice"
	DecisionPathChoice      DecisionType = "path_choice"
	DecisionOutputFormat    DecisionType = "output_format"
	DecisionRetryStrategy   DecisionType = "retry_strategy"
	DecisionDelegation      DecisionType = "delegation"
	DecisionTermination     DecisionType = "termination"
	DecisionCustom          DecisionType = "custom"
)

func (d DecisionType) valid() bool {
	switch d {
	case DecisionToolSelection, DecisionParameterChoice, DecisionPathChoice,
		DecisionOutputFormat, DecisionRetryStrategy, DecisionDelegation,
		DecisionTermination, DecisionCustom:
		return true
	}
	return false
}

type Option struct {
	ID           string         `json:"id"`
	Description  string         `json:"description"`
	ActionType   string         `json:"action_type"`
	ActionParams map[string]any `json:"action_params"`
	Pros         []string       `json:"pros"`
	Cons         []string       `json:"cons"`
	Confidence   float64        `json:"confidence"`

	// confidenceSet records an explicit "confidence" key (including 0). A
	// missing key carries the pydantic default 0.5; an explicit 0 stays 0.
	confidenceSet bool

	Extra map[string]json.RawMessage `json:"-"`
}

// EffectiveConfidence is the confidence the option carries. decision.py
// declares confidence with default=0.5, so only a supplied value (or
// SetConfidence) overrides it.
func (o Option) EffectiveConfidence() float64 {
	if !o.confidenceSet && o.Confidence == 0 {
		return 0.5
	}
	return o.Confidence
}

// SetConfidence records an explicit confidence, including 0.
func (o *Option) SetConfidence(confidence float64) {
	o.Confidence = confidence
	o.confidenceSet = true
}

func (o Option) MarshalJSON() ([]byte, error) {
	type plain Option
	p := plain(o)
	if p.ActionParams == nil {
		p.ActionParams = map[string]any{}
	}
	if p.Pros == nil {
		p.Pros = []string{}
	}
	if p.Cons == nil {
		p.Cons = []string{}
	}
	p.Confidence = o.EffectiveConfidence()
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, o.Extra)
}

func (o *Option) UnmarshalJSON(b []byte) error {
	type plain Option
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	// Pydantic fields without defaults: id, description, action_type.
	if err := requireFields(raw, "option", "id", "description", "action_type"); err != nil {
		return err
	}
	_, confidencePresent := raw["confidence"]
	if !confidencePresent {
		p.Confidence = 0.5
	}
	*o = Option(p)
	o.Extra = extras
	o.confidenceSet = confidencePresent
	if o.ActionParams == nil {
		o.ActionParams = map[string]any{}
	}
	if o.Pros == nil {
		o.Pros = []string{}
	}
	if o.Cons == nil {
		o.Cons = []string{}
	}
	return nil
}

type Outcome struct {
	Success      bool           `json:"success"`
	Result       any            `json:"result"`
	Error        *string        `json:"error"`
	StateChanges map[string]any `json:"state_changes"`
	TokensUsed   int            `json:"tokens_used"`
	LatencyMS    int            `json:"latency_ms"`
	Summary      string         `json:"summary"`
	Timestamp    Timestamp      `json:"timestamp"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (o Outcome) MarshalJSON() ([]byte, error) {
	type plain Outcome
	p := plain(o)
	if p.StateChanges == nil {
		p.StateChanges = map[string]any{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, o.Extra)
}

func (o *Outcome) UnmarshalJSON(b []byte) error {
	type plain Outcome
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	// Pydantic field without a default: success.
	if err := requireFields(raw, "outcome", "success"); err != nil {
		return err
	}
	if p.Timestamp.IsZero() {
		p.Timestamp = NowTimestamp()
	}
	*o = Outcome(p)
	o.Extra = extras
	if o.StateChanges == nil {
		o.StateChanges = map[string]any{}
	}
	return nil
}

type DecisionEvaluation struct {
	GoalAligned          bool    `json:"goal_aligned"`
	AlignmentScore       float64 `json:"alignment_score"`
	BetterOptionExisted  bool    `json:"better_option_existed"`
	BetterOptionID       *string `json:"better_option_id"`
	WhyBetter            *string `json:"why_better"`
	OutcomeQuality       float64 `json:"outcome_quality"`
	ContributedToSuccess *bool   `json:"contributed_to_success"`
	Explanation          string  `json:"explanation"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (e *DecisionEvaluation) Validate() error {
	if e.AlignmentScore < 0 || e.AlignmentScore > 1 {
		return fmt.Errorf("alignment_score must be between 0 and 1")
	}
	if e.OutcomeQuality < 0 || e.OutcomeQuality > 1 {
		return fmt.Errorf("outcome_quality must be between 0 and 1")
	}
	return nil
}

func (e DecisionEvaluation) MarshalJSON() ([]byte, error) {
	type plain DecisionEvaluation
	b, err := json.Marshal(plain(e))
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, e.Extra)
}

func (e *DecisionEvaluation) UnmarshalJSON(b []byte) error {
	type plain DecisionEvaluation
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if _, ok := raw["goal_aligned"]; !ok {
		p.GoalAligned = true
	}
	if _, ok := raw["alignment_score"]; !ok {
		p.AlignmentScore = 1.0
	}
	if _, ok := raw["outcome_quality"]; !ok {
		p.OutcomeQuality = 1.0
	}
	*e = DecisionEvaluation(p)
	e.Extra = extras
	return e.Validate()
}

type Decision struct {
	ID                string              `json:"id"`
	Timestamp         Timestamp           `json:"timestamp"`
	NodeID            string              `json:"node_id"`
	Intent            string              `json:"intent"`
	DecisionType      DecisionType        `json:"decision_type"`
	Options           []Option            `json:"options"`
	ChosenOptionID    string              `json:"chosen_option_id"`
	Reasoning         string              `json:"reasoning"`
	ActiveConstraints []string            `json:"active_constraints"`
	InputContext      map[string]any      `json:"input_context"`
	Outcome           *Outcome            `json:"outcome"`
	Evaluation        *DecisionEvaluation `json:"evaluation"`

	Extra map[string]json.RawMessage `json:"-"`
}

func (d *Decision) ChosenOption() *Option {
	for i := range d.Options {
		if d.Options[i].ID == d.ChosenOptionID {
			return &d.Options[i]
		}
	}
	return nil
}

func (d *Decision) WasSuccessful() bool {
	return d.Outcome != nil && d.Outcome.Success
}

func (d *Decision) WasGoodDecision() bool {
	if d.Evaluation == nil {
		return d.WasSuccessful()
	}
	return d.Evaluation.GoalAligned && d.Evaluation.OutcomeQuality > 0.5
}

func (d *Decision) SummaryForBuilder() string {
	status := "✗"
	if d.WasSuccessful() {
		status = "✓"
	}
	quality := ""
	if d.Evaluation != nil {
		quality = fmt.Sprintf(" [quality: %.1f]", d.Evaluation.OutcomeQuality)
	}
	action := "unknown action"
	if chosen := d.ChosenOption(); chosen != nil {
		action = chosen.Description
	}
	return fmt.Sprintf("%s [%s] %s → %s%s", status, d.NodeID, d.Intent, action, quality)
}

func (d *Decision) Validate() error {
	if d.DecisionType == "" {
		d.DecisionType = DecisionCustom
	}
	if !d.DecisionType.valid() {
		return fmt.Errorf("invalid decision_type %q", d.DecisionType)
	}
	return nil
}

func (d Decision) MarshalJSON() ([]byte, error) {
	type plain Decision
	p := plain(d)
	if p.DecisionType == "" {
		p.DecisionType = DecisionCustom
	}
	if p.Timestamp.IsZero() {
		p.Timestamp = NowTimestamp()
	}
	if p.Options == nil {
		p.Options = []Option{}
	}
	if p.ActiveConstraints == nil {
		p.ActiveConstraints = []string{}
	}
	if p.InputContext == nil {
		p.InputContext = map[string]any{}
	}
	b, err := json.Marshal(struct {
		plain
		ChosenOption    *Option `json:"chosen_option"`
		WasSuccessful   bool    `json:"was_successful"`
		WasGoodDecision bool    `json:"was_good_decision"`
	}{
		plain:           p,
		ChosenOption:    d.ChosenOption(),
		WasSuccessful:   d.WasSuccessful(),
		WasGoodDecision: d.WasGoodDecision(),
	})
	if err != nil {
		return nil, err
	}
	return mergeExtras(b, d.Extra)
}

func (d *Decision) UnmarshalJSON(b []byte) error {
	type plain Decision
	var p plain
	extras, err := unmarshalStruct(b, &p)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	// Pydantic fields without defaults: id, node_id, intent.
	if err := requireFields(raw, "decision", "id", "node_id", "intent"); err != nil {
		return err
	}
	if p.Timestamp.IsZero() {
		p.Timestamp = NowTimestamp()
	}
	*d = Decision(p)
	d.Extra = extras
	if d.DecisionType == "" {
		d.DecisionType = DecisionCustom
	}
	if d.Options == nil {
		d.Options = []Option{}
	}
	if d.ActiveConstraints == nil {
		d.ActiveConstraints = []string{}
	}
	if d.InputContext == nil {
		d.InputContext = map[string]any{}
	}
	return d.Validate()
}

var ErrInvalidDecision = errors.New("invalid decision")
