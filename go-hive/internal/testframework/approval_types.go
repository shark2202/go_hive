package testframework

// Parity: core/framework/testing/approval_types.py::ApprovalRequest

import (
	"encoding/json"
	"fmt"
)

// ApprovalAction is what a reviewer does with a generated test.
type ApprovalAction string

const (
	// ApprovalActionApprove accepts the test as-is.
	ApprovalActionApprove ApprovalAction = "approve"
	// ApprovalActionModify accepts the test after editing its code.
	ApprovalActionModify ApprovalAction = "modify"
	// ApprovalActionReject declines the test with a reason.
	ApprovalActionReject ApprovalAction = "reject"
	// ApprovalActionSkip leaves the test pending.
	ApprovalActionSkip ApprovalAction = "skip"
)

// ApprovalActions lists every action in upstream declaration order.
var ApprovalActions = []ApprovalAction{ApprovalActionApprove, ApprovalActionModify, ApprovalActionReject, ApprovalActionSkip}

// ApprovalRequest asks to approve, modify, reject, or skip one test. It is
// shared by the interactive CLI and programmatic/MCP approval.
type ApprovalRequest struct {
	TestID       string         `json:"test_id"`
	Action       ApprovalAction `json:"action"`
	ModifiedCode *string        `json:"modified_code"`
	Reason       *string        `json:"reason"`
	ApprovedBy   string         `json:"approved_by"`
}

// NewApprovalRequest builds a request with the upstream approved_by default.
func NewApprovalRequest(testID string, action ApprovalAction) ApprovalRequest {
	return ApprovalRequest{TestID: testID, Action: action, ApprovedBy: "user"}
}

// ValidateAction checks the fields the action requires. The messages match
// upstream token for token.
func (r *ApprovalRequest) ValidateAction() (bool, string) {
	if r.Action == ApprovalActionModify && (r.ModifiedCode == nil || *r.ModifiedCode == "") {
		return false, "modified_code is required for MODIFY action"
	}
	if r.Action == ApprovalActionReject && (r.Reason == nil || *r.Reason == "") {
		return false, "reason is required for REJECT action"
	}
	return true, ""
}

// UnmarshalJSON applies the approved_by default and rejects unknown actions.
func (r *ApprovalRequest) UnmarshalJSON(data []byte) error {
	type alias ApprovalRequest
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	for _, field := range []string{"test_id", "action"} {
		if _, ok := presence[field]; !ok {
			return &ValidationError{Field: field, Problem: "field required"}
		}
	}
	if decoded.ApprovedBy == "" {
		decoded.ApprovedBy = "user"
	}
	if !validApprovalAction(decoded.Action) {
		return &ValidationError{Field: "action", Problem: fmt.Sprintf("invalid value %q", decoded.Action)}
	}
	*r = ApprovalRequest(decoded)
	return nil
}

// ApprovalResult is the outcome of processing one approval request.
type ApprovalResult struct {
	TestID    string         `json:"test_id"`
	Action    ApprovalAction `json:"action"`
	Success   bool           `json:"success"`
	Message   *string        `json:"message"`
	Error     *string        `json:"error"`
	Timestamp Timestamp      `json:"timestamp"`
}

// ApprovalSuccess builds a successful approval result.
func ApprovalSuccess(testID string, action ApprovalAction, message string) ApprovalResult {
	return ApprovalResult{
		TestID:    testID,
		Action:    action,
		Success:   true,
		Message:   &message,
		Timestamp: Timestamp{Now()},
	}
}

// ApprovalError builds a failed approval result.
func ApprovalError(testID string, action ApprovalAction, problem string) ApprovalResult {
	return ApprovalResult{
		TestID:    testID,
		Action:    action,
		Success:   false,
		Error:     &problem,
		Timestamp: Timestamp{Now()},
	}
}

// BatchApprovalRequest approves several tests for one goal at once.
type BatchApprovalRequest struct {
	GoalID    string            `json:"goal_id"`
	Approvals []ApprovalRequest `json:"approvals"`
}

// UnmarshalJSON rejects documents missing the goal or approvals list and
// normalizes a null approvals value to an empty list.
func (b *BatchApprovalRequest) UnmarshalJSON(data []byte) error {
	type alias BatchApprovalRequest
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	for _, field := range []string{"goal_id", "approvals"} {
		if _, ok := presence[field]; !ok {
			return &ValidationError{Field: field, Problem: "field required"}
		}
	}
	if decoded.Approvals == nil {
		decoded.Approvals = []ApprovalRequest{}
	}
	*b = BatchApprovalRequest(decoded)
	return nil
}

// ToDict renders the request for JSON serialization.
func (b *BatchApprovalRequest) ToDict() map[string]any {
	rows := make([]map[string]any, 0, len(b.Approvals))
	for index := range b.Approvals {
		data, err := json.Marshal(b.Approvals[index])
		if err != nil {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(data, &row); err != nil {
			continue
		}
		rows = append(rows, row)
	}
	return map[string]any{"goal_id": b.GoalID, "approvals": rows}
}

// BatchApprovalResult counts the decisions in a batch.
type BatchApprovalResult struct {
	GoalID   string           `json:"goal_id"`
	Total    int              `json:"total"`
	Approved int              `json:"approved"`
	Modified int              `json:"modified"`
	Rejected int              `json:"rejected"`
	Skipped  int              `json:"skipped"`
	Errors   int              `json:"errors"`
	Results  []ApprovalResult `json:"results"`
}

// Summary renders upstream's one-line batch summary.
func (b *BatchApprovalResult) Summary() string {
	return fmt.Sprintf(
		"Processed %d tests: %d approved, %d modified, %d rejected, %d skipped, %d errors",
		b.Total, b.Approved, b.Modified, b.Rejected, b.Skipped, b.Errors,
	)
}

func validApprovalAction(value ApprovalAction) bool {
	for _, candidate := range ApprovalActions {
		if value == candidate {
			return true
		}
	}
	return false
}
