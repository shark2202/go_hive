package testframework

// Approval-type contracts: request validation, result constructors, batch
// request JSON, and action enum validation.

import (
	"errors"
	"testing"
)

// Parity-exempt: Go-only guard for upstream's validate_action messages.
func TestApprovalRequestValidation(t *testing.T) {
	valid := NewApprovalRequest("t1", ApprovalActionApprove)
	if ok, message := valid.ValidateAction(); !ok || message != "" {
		t.Fatalf("approve validation = %v/%q", ok, message)
	}
	modify := NewApprovalRequest("t1", ApprovalActionModify)
	if ok, message := modify.ValidateAction(); ok || message != "modified_code is required for MODIFY action" {
		t.Fatalf("modify validation = %v/%q", ok, message)
	}
	code := ""
	modify.ModifiedCode = &code
	if ok, _ := modify.ValidateAction(); ok {
		t.Fatal("empty modified_code must be rejected")
	}
	reject := NewApprovalRequest("t1", ApprovalActionReject)
	if ok, message := reject.ValidateAction(); ok || message != "reason is required for REJECT action" {
		t.Fatalf("reject validation = %v/%q", ok, message)
	}
	skip := NewApprovalRequest("t1", ApprovalActionSkip)
	if ok, _ := skip.ValidateAction(); !ok {
		t.Fatal("skip needs no extra fields")
	}
}

// Parity-exempt: Go-only guard for the approval result constructors and batch
// summary wording.
func TestApprovalResultAndSummary(t *testing.T) {
	success := ApprovalSuccess("t1", ApprovalActionApprove, "Approved")
	if !success.Success || success.Message == nil || *success.Message != "Approved" || success.Error != nil {
		t.Fatalf("success = %+v", success)
	}
	failure := ApprovalError("t1", ApprovalActionApprove, "boom")
	if failure.Success || failure.Error == nil || *failure.Error != "boom" {
		t.Fatalf("failure = %+v", failure)
	}
	batch := &BatchApprovalResult{Total: 5, Approved: 1, Modified: 1, Rejected: 1, Skipped: 1, Errors: 1}
	want := "Processed 5 tests: 1 approved, 1 modified, 1 rejected, 1 skipped, 1 errors"
	if batch.Summary() != want {
		t.Fatalf("summary = %q, want %q", batch.Summary(), want)
	}
}

// Parity-exempt: Go-only guard for BatchApprovalRequest's JSON round-trip
// (the dev CLI reads it from a file).
func TestBatchApprovalRequestJSON(t *testing.T) {
	document := `{"goal_id":"g1","approvals":[{"test_id":"t1","action":"approve"}]}`
	var request BatchApprovalRequest
	if err := unmarshalJSON([]byte(document), &request); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if request.GoalID != "g1" || len(request.Approvals) != 1 {
		t.Fatalf("request = %+v", request)
	}
	if request.Approvals[0].ApprovedBy != "user" {
		t.Fatalf("approved_by default = %q, want user", request.Approvals[0].ApprovedBy)
	}
	dict := request.ToDict()
	if dict["goal_id"] != "g1" {
		t.Fatalf("to_dict = %+v", dict)
	}
}

// Parity-exempt: Go-only guard that an unknown action in a hand-crafted
// request is rejected at unmarshal time.
func TestApprovalActionValidation(t *testing.T) {
	var request ApprovalRequest
	err := unmarshalJSON([]byte(`{"test_id":"t1","action":"delete"}`), &request)
	if err == nil {
		t.Fatal("unknown action must be rejected")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "action" {
		t.Fatalf("error = %v", err)
	}
}
