package testframework

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// freezeClock pins the package clock for one test. Reserve it for tests that
// assert timestamps; concurrent tests must not use it.
func freezeClock(t *testing.T, at time.Time) {
	t.Helper()
	previous := Now
	Now = func() time.Time { return at }
	t.Cleanup(func() { Now = previous })
}

// Parity: core/tests/test_testing_framework.py::test_create_test
func TestCreateTest(t *testing.T) {
	freezeClock(t, time.Date(2026, 11, 26, 10, 20, 30, 0, time.Local))
	test := NewTest("test_001", "goal_001", "constraint_api_limits", TestTypeConstraint,
		"test_constraint_api_limits", "def test_constraint_api_limits(agent): pass", "Tests API rate limits")
	test.Input = map[string]any{"topic": "test"}
	test.ExpectedOutput = map[string]any{"count": 5}

	if test.ID != "test_001" || test.GoalID != "goal_001" {
		t.Fatalf("identity = %q/%q", test.ID, test.GoalID)
	}
	if test.TestType != TestTypeConstraint {
		t.Fatalf("test_type = %q, want constraint", test.TestType)
	}
	if test.ApprovalStatus != ApprovalPending {
		t.Fatalf("approval_status = %q, want pending", test.ApprovalStatus)
	}
	if test.IsApproved() {
		t.Fatal("fresh test must not be approved")
	}
	if test.GeneratedBy != "llm" {
		t.Fatalf("generated_by = %q, want llm", test.GeneratedBy)
	}
	if test.CreatedAt.IsZero() || test.UpdatedAt.IsZero() {
		t.Fatal("construction must stamp created_at/updated_at")
	}
}

// Parity: core/tests/test_testing_framework.py::test_approve_test
func TestApproveTest(t *testing.T) {
	approveAt := time.Date(2026, 11, 26, 11, 0, 0, 0, time.Local)
	freezeClock(t, approveAt)
	test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "pass", "test")

	test.Approve("test_user")

	if test.ApprovalStatus != ApprovalApproved {
		t.Fatalf("approval_status = %q, want approved", test.ApprovalStatus)
	}
	if test.ApprovedBy == nil || *test.ApprovedBy != "test_user" {
		t.Fatalf("approved_by = %v, want test_user", test.ApprovedBy)
	}
	if test.ApprovedAt == nil || !test.ApprovedAt.Equal(approveAt) {
		t.Fatalf("approved_at = %v, want %v", test.ApprovedAt, approveAt)
	}
	if !test.IsApproved() {
		t.Fatal("approved test must report IsApproved")
	}
}

// Parity: core/tests/test_testing_framework.py::test_modify_test
func TestModifyTest(t *testing.T) {
	test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "original code", "test")

	test.Modify("modified code", "test_user")

	if test.ApprovalStatus != ApprovalModified {
		t.Fatalf("approval_status = %q, want modified", test.ApprovalStatus)
	}
	if test.OriginalCode == nil || *test.OriginalCode != "original code" {
		t.Fatalf("original_code = %v, want original code", test.OriginalCode)
	}
	if test.TestCode != "modified code" {
		t.Fatalf("test_code = %q, want modified code", test.TestCode)
	}
	if !test.IsApproved() {
		t.Fatal("modified test must report IsApproved")
	}
}

// Parity: core/tests/test_testing_framework.py::test_reject_test
func TestRejectTest(t *testing.T) {
	test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "pass", "test")

	test.Reject("Not a valid test case")

	if test.ApprovalStatus != ApprovalRejected {
		t.Fatalf("approval_status = %q, want rejected", test.ApprovalStatus)
	}
	if test.RejectionReason == nil || *test.RejectionReason != "Not a valid test case" {
		t.Fatalf("rejection_reason = %v", test.RejectionReason)
	}
	if test.IsApproved() {
		t.Fatal("rejected test must not report IsApproved")
	}
}

// Parity: core/tests/test_testing_framework.py::test_record_result
func TestRecordResult(t *testing.T) {
	test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "pass", "test")

	test.RecordResult(true)
	test.RecordResult(false)

	if test.RunCount != 2 || test.PassCount != 1 || test.FailCount != 1 {
		t.Fatalf("counts = run %d / pass %d / fail %d", test.RunCount, test.PassCount, test.FailCount)
	}
	if test.LastResult == nil || *test.LastResult != "failed" {
		t.Fatalf("last_result = %v, want failed", test.LastResult)
	}
	if test.LastRun == nil {
		t.Fatal("last_run must be stamped")
	}
	rate, ok := test.PassRate()
	if !ok || rate != 0.5 {
		t.Fatalf("pass_rate = %v/%v, want 0.5/true", rate, ok)
	}
}

// Parity: core/tests/test_testing_framework.py::test_create_test
func TestPassRateWithoutRuns(t *testing.T) {
	test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "pass", "test")
	if _, ok := test.PassRate(); ok {
		t.Fatal("never-run test must not report a pass rate")
	}
}

// Parity-exempt: Go-only serialization guard. Python's pydantic emits a naive
// isoformat datetime; Go's time.Time would emit RFC3339 with a timezone, which
// a Python reader would misread. Pin the wire shape here.
func TestTimestampWireFormat(t *testing.T) {
	freezeClock(t, time.Date(2026, 11, 26, 10, 20, 30, 123456000, time.Local))
	test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "pass", "test")
	data, err := json.Marshal(test)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		`"created_at":"2026-11-26T10:20:30.123456"`,
		`"approval_status":"pending"`,
		`"generated_by":"llm"`,
		`"approved_by":null`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("wire shape missing %s:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "Z\"") || strings.Contains(string(data), "+08:00") {
		t.Fatalf("naive timestamp must not carry a zone:\n%s", data)
	}
}

// Parity-exempt: Go-only round-trip guard for pydantic's extra="allow".
func TestExtraFieldsRoundTrip(t *testing.T) {
	document := `{
	  "id": "test_001",
	  "goal_id": "goal_001",
	  "parent_criteria_id": "constraint_001",
	  "test_type": "constraint",
	  "test_name": "test_something",
	  "test_code": "pass",
	  "description": "test",
	  "input": {},
	  "expected_output": {},
	  "generated_by": "llm",
	  "llm_confidence": 0.5,
	  "approval_status": "pending",
	  "approved_by": null,
	  "approved_at": null,
	  "rejection_reason": null,
	  "original_code": null,
	  "last_run": null,
	  "last_result": null,
	  "run_count": 0,
	  "pass_count": 0,
	  "fail_count": 0,
	  "created_at": "2026-11-26T10:20:30",
	  "updated_at": "2026-11-26T10:20:30",
	  "custom_score": {"value": 3}
	}`
	var test Test
	if err := json.Unmarshal([]byte(document), &test); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if test.Extra["custom_score"] == nil {
		t.Fatalf("extra field dropped: %+v", test.Extra)
	}
	data, err := json.Marshal(&test)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"custom_score":{"value":3}`) {
		t.Fatalf("extra field not preserved: %s", data)
	}
}

// Parity-exempt: Go-only validation guard for the enum/range checks pydantic
// performs at assignment time.
func TestValidationRejectsInvalidEnums(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Test)
		field  string
	}{
		{"type", func(test *Test) { test.TestType = "nonsense" }, "test_type"},
		{"status", func(test *Test) { test.ApprovalStatus = "maybe" }, "approval_status"},
		{"confidence", func(test *Test) { test.LLMConfidence = 1.5 }, "llm_confidence"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			test := NewTest("test_001", "goal_001", "constraint_001", TestTypeConstraint, "test_something", "pass", "test")
			testCase.mutate(test)
			err := test.Validate()
			if err == nil {
				t.Fatal("expected a validation error")
			}
			validation, ok := err.(*ValidationError)
			if !ok || validation.Field != testCase.field {
				t.Fatalf("error = %v, want field %s", err, testCase.field)
			}
		})
	}
}

// Parity-exempt: Go-only presence guard for pydantic's required fields.
func TestUnmarshalRejectsMissingRequiredFields(t *testing.T) {
	var test Test
	err := unmarshalJSON([]byte(`{"goal_id":"g","parent_criteria_id":"c","test_type":"constraint","test_name":"t","test_code":"pass","description":"d"}`), &test)
	if err == nil {
		t.Fatal("missing id must be rejected")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Field != "id" {
		t.Fatalf("error = %v", err)
	}
}

// Parity: framework/testing/test_case.py::ApprovalStatus (class)
func TestApprovalStatusValues(t *testing.T) {
	want := map[ApprovalStatus]string{
		ApprovalPending:  "pending",
		ApprovalApproved: "approved",
		ApprovalModified: "modified",
		ApprovalRejected: "rejected",
	}
	for status, value := range want {
		if string(status) != value {
			t.Fatalf("%v = %q, want %q", status, string(status), value)
		}
	}
	if len(ApprovalStatuses) != 4 {
		t.Fatalf("statuses = %d, want 4", len(ApprovalStatuses))
	}
}
