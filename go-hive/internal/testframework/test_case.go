package testframework

// Parity: core/framework/testing/test_case.py::Test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ApprovalStatus is the user-approval state of a generated test.
type ApprovalStatus string

const (
	// ApprovalPending means the test is awaiting user review.
	ApprovalPending ApprovalStatus = "pending"
	// ApprovalApproved means the user accepted the test as-is.
	ApprovalApproved ApprovalStatus = "approved"
	// ApprovalModified means the user edited the test before accepting it.
	ApprovalModified ApprovalStatus = "modified"
	// ApprovalRejected means the user declined the test (with a reason).
	ApprovalRejected ApprovalStatus = "rejected"
)

// ApprovalStatuses lists every valid status in upstream declaration order.
var ApprovalStatuses = []ApprovalStatus{ApprovalPending, ApprovalApproved, ApprovalModified, ApprovalRejected}

// TestType is what a generated test validates.
type TestType string

const (
	// TestTypeConstraint validates constraint boundaries.
	TestTypeConstraint TestType = "constraint"
	// TestTypeSuccessCriteria validates success-criteria achievement.
	// Upstream spells the enum member SUCCESS_CRITERIA but the value is
	// "outcome"; the wire value is what parity preserves.
	TestTypeSuccessCriteria TestType = "outcome"
	// TestTypeEdgeCase validates edge-case handling.
	TestTypeEdgeCase TestType = "edge_case"
)

// TestTypes lists every valid test type in upstream declaration order.
var TestTypes = []TestType{TestTypeConstraint, TestTypeSuccessCriteria, TestTypeEdgeCase}

// Now is the clock seam for approval and execution timestamps. Upstream calls
// datetime.now(); tests freeze this variable so timestamps are deterministic.
var Now = time.Now

// Timestamp is a naive local datetime serialized the way Python's
// datetime.isoformat does: no timezone marker, and microseconds only when
// non-zero. Every upstream constructor uses datetime.now(), so the port keeps
// the same wall-clock shape instead of Go's time.Time RFC3339 form.
type Timestamp struct {
	time.Time
}

// NewTimestamp wraps a time.Time in the upstream wire format.
func NewTimestamp(t time.Time) Timestamp { return Timestamp{Time: t} }

// MarshalJSON renders the timestamp without a timezone, truncating to
// microseconds and omitting the fraction when it is zero.
func (ts Timestamp) MarshalJSON() ([]byte, error) {
	if ts.IsZero() {
		return []byte(`"0001-01-01T00:00:00"`), nil
	}
	base := ts.Format("2006-01-02T15:04:05")
	if micros := ts.Nanosecond() / 1000; micros != 0 {
		base = fmt.Sprintf("%s.%06d", base, micros)
	}
	return json.Marshal(base)
}

// UnmarshalJSON accepts the naive isoformat upstream writes plus the RFC3339
// and space-separated forms pydantic accepts on input. A JSON null leaves the
// zero value.
func (ts *Timestamp) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		ts.Time = time.Time{}
		return nil
	}
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("timestamp: %w", err)
	}
	raw = strings.TrimSpace(raw)
	formats := []string{
		"2006-01-02T15:04:05.999999999",
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range formats {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			ts.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("timestamp: cannot parse %q", raw)
}

// ValidationError reports a pydantic-style validation failure.
type ValidationError struct {
	Field   string
	Problem string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Problem }

// Test is a test case generated from a Goal success criterion or constraint.
// All tests carry approval state; the store never treats an unapproved test as
// runnable.
type Test struct {
	ID               string         `json:"id"`
	GoalID           string         `json:"goal_id"`
	ParentCriteriaID string         `json:"parent_criteria_id"`
	TestType         TestType       `json:"test_type"`
	TestName         string         `json:"test_name"`
	TestCode         string         `json:"test_code"`
	Description      string         `json:"description"`
	Input            map[string]any `json:"input"`
	ExpectedOutput   map[string]any `json:"expected_output"`
	GeneratedBy      string         `json:"generated_by"`
	LLMConfidence    float64        `json:"llm_confidence"`

	ApprovalStatus  ApprovalStatus `json:"approval_status"`
	ApprovedBy      *string        `json:"approved_by"`
	ApprovedAt      *Timestamp     `json:"approved_at"`
	RejectionReason *string        `json:"rejection_reason"`
	OriginalCode    *string        `json:"original_code"`

	LastRun    *Timestamp `json:"last_run"`
	LastResult *string    `json:"last_result"`
	RunCount   int        `json:"run_count"`
	PassCount  int        `json:"pass_count"`
	FailCount  int        `json:"fail_count"`

	CreatedAt Timestamp `json:"created_at"`
	UpdatedAt Timestamp `json:"updated_at"`

	// Extra preserves unknown keys, mirroring model_config extra="allow".
	Extra map[string]json.RawMessage `json:"-"`
}

// NewTest builds a test with upstream defaults (generated_by "llm", pending
// approval, empty input/expected-output maps, fresh timestamps).
func NewTest(id, goalID, parentCriteriaID string, testType TestType, name, code, description string) *Test {
	now := Timestamp{Now()}
	return &Test{
		ID:               id,
		GoalID:           goalID,
		ParentCriteriaID: parentCriteriaID,
		TestType:         testType,
		TestName:         name,
		TestCode:         code,
		Description:      description,
		Input:            map[string]any{},
		ExpectedOutput:   map[string]any{},
		GeneratedBy:      "llm",
		ApprovalStatus:   ApprovalPending,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// Validate checks enum membership and the confidence range, the same checks
// pydantic performs on assignment.
func (t *Test) Validate() error {
	if !validTestType(t.TestType) {
		return &ValidationError{Field: "test_type", Problem: fmt.Sprintf("invalid value %q", t.TestType)}
	}
	if !validApprovalStatus(t.ApprovalStatus) {
		return &ValidationError{Field: "approval_status", Problem: fmt.Sprintf("invalid value %q", t.ApprovalStatus)}
	}
	if t.LLMConfidence < 0 || t.LLMConfidence > 1 {
		return &ValidationError{Field: "llm_confidence", Problem: "must be between 0 and 1"}
	}
	return nil
}

// Approve marks the test approved by the given user.
func (t *Test) Approve(approvedBy string) {
	if approvedBy == "" {
		approvedBy = "user"
	}
	now := Timestamp{Now()}
	t.ApprovalStatus = ApprovalApproved
	t.ApprovedBy = &approvedBy
	t.ApprovedAt = &now
	t.UpdatedAt = now
}

// Modify approves the test with replacement code, retaining the original.
func (t *Test) Modify(newCode, approvedBy string) {
	if approvedBy == "" {
		approvedBy = "user"
	}
	original := t.TestCode
	now := Timestamp{Now()}
	t.OriginalCode = &original
	t.TestCode = newCode
	t.ApprovalStatus = ApprovalModified
	t.ApprovedBy = &approvedBy
	t.ApprovedAt = &now
	t.UpdatedAt = now
}

// Reject declines the test with a reason.
func (t *Test) Reject(reason string) {
	now := Timestamp{Now()}
	t.ApprovalStatus = ApprovalRejected
	t.RejectionReason = &reason
	t.UpdatedAt = now
}

// RecordResult records one execution outcome.
func (t *Test) RecordResult(passed bool) {
	now := Timestamp{Now()}
	result := "failed"
	if passed {
		result = "passed"
		t.PassCount++
	} else {
		t.FailCount++
	}
	t.LastRun = &now
	t.LastResult = &result
	t.RunCount++
	t.UpdatedAt = now
}

// IsApproved reports whether the test was approved, with or without edits.
func (t *Test) IsApproved() bool {
	return t.ApprovalStatus == ApprovalApproved || t.ApprovalStatus == ApprovalModified
}

// PassRate returns the pass ratio and whether the test has run at all
// (upstream returns None for a never-run test).
func (t *Test) PassRate() (float64, bool) {
	if t.RunCount == 0 {
		return 0, false
	}
	return float64(t.PassCount) / float64(t.RunCount), true
}

// ToDict renders the test as the JSON object pydantic's model_dump produces.
func (t *Test) ToDict() (map[string]any, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MarshalJSON emits declared fields in struct order and appends the preserved
// extra keys.
func (t Test) MarshalJSON() ([]byte, error) {
	type alias Test
	return marshalWithExtras(alias(t), t.Extra)
}

// UnmarshalJSON applies pydantic defaults and rejects documents missing a
// required field. Unknown keys are preserved for round-tripping.
func (t *Test) UnmarshalJSON(data []byte) error {
	type alias Test
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	for _, field := range []string{"id", "goal_id", "parent_criteria_id", "test_type", "test_name", "test_code", "description"} {
		if _, ok := presence[field]; !ok {
			return &ValidationError{Field: field, Problem: "field required"}
		}
	}
	extras, err := decodeExtras(data, decoded)
	if err != nil {
		return err
	}
	decoded.Extra = extras
	if decoded.GeneratedBy == "" {
		decoded.GeneratedBy = "llm"
	}
	if decoded.ApprovalStatus == "" {
		decoded.ApprovalStatus = ApprovalPending
	}
	if decoded.Input == nil {
		decoded.Input = map[string]any{}
	}
	if decoded.ExpectedOutput == nil {
		decoded.ExpectedOutput = map[string]any{}
	}
	if decoded.CreatedAt.IsZero() {
		decoded.CreatedAt = Timestamp{Now()}
	}
	if decoded.UpdatedAt.IsZero() {
		decoded.UpdatedAt = Timestamp{Now()}
	}
	*t = Test(decoded)
	return t.Validate()
}

func validTestType(value TestType) bool {
	for _, candidate := range TestTypes {
		if value == candidate {
			return true
		}
	}
	return false
}

func validApprovalStatus(value ApprovalStatus) bool {
	for _, candidate := range ApprovalStatuses {
		if value == candidate {
			return true
		}
	}
	return false
}

// marshalWithExtras serializes base then merges the preserved extra keys.
func marshalWithExtras(base any, extras map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	if len(extras) == 0 {
		return data, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range extras {
		if _, exists := fields[key]; exists {
			// A declared field always wins over an extra with the same name.
			continue
		}
		fields[key] = value
	}
	return json.Marshal(fields)
}

// decodeExtras returns the object keys not covered by base's json tags.
func decodeExtras(data []byte, base any) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	typ := reflect.TypeOf(base)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for index := 0; index < typ.NumField(); index++ {
		tag := typ.Field(index).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		delete(raw, name)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return raw, nil
}
