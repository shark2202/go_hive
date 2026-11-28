package testframework

import (
	"bytes"
	"strings"
	"testing"
)

// Parity-exempt: Go-only guard for the interactive display text.
func TestInteractiveApprovalApprove(t *testing.T) {
	storage := newStorage(t)
	test := NewTest("test_001", "goal_001", "c1", TestTypeConstraint, "test_something", "pass", "A test")
	test.Input = map[string]any{"key": "value"}
	test.ExpectedOutput = map[string]any{"result": "expected"}
	test.LLMConfidence = 0.85
	if err := storage.SaveTest(test); err != nil {
		t.Fatalf("SaveTest: %v", err)
	}

	var out bytes.Buffer
	progress := []int{}
	results, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:         strings.NewReader("a\n"),
		Out:        &out,
		OnProgress: func(current, total int) { progress = append(progress, current) },
	})
	if err != nil {
		t.Fatalf("InteractiveApproval: %v", err)
	}
	if len(results) != 1 || !results[0].Success || results[0].Action != ApprovalActionApprove {
		t.Fatalf("results = %+v", results)
	}
	if len(progress) != 1 || progress[0] != 1 {
		t.Fatalf("progress = %v", progress)
	}
	display := out.String()
	for _, want := range []string{
		"[1/1] test_something",
		"Type: constraint",
		"Criteria: c1",
		"Confidence: 85%",
		"Description: A test",
		"Input:",
		"Expected Output:",
		"Test Code:",
		"[a]pprove  [r]eject  [e]dit  [s]kip",
		"Your choice: ",
		"✓ Approved",
	} {
		if !strings.Contains(display, want) {
			t.Fatalf("display missing %q:\n%s", want, display)
		}
	}
	loaded, err := storage.LoadTest("goal_001", "test_001")
	if err != nil || loaded.ApprovalStatus != ApprovalApproved {
		t.Fatalf("stored status = %+v/%v", loaded, err)
	}
}

// Parity-exempt: Go-only guard for reject prompting: an empty reason becomes
// upstream's "No reason provided" default.
func TestInteractiveApprovalReject(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")

	var out bytes.Buffer
	results, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:  strings.NewReader("r\n\n"),
		Out: &out,
	})
	if err != nil {
		t.Fatalf("InteractiveApproval: %v", err)
	}
	if !results[0].Success || results[0].Action != ApprovalActionReject {
		t.Fatalf("result = %+v", results[0])
	}
	loaded, _ := storage.LoadTest("goal_001", "test_001")
	if loaded.ApprovalStatus != ApprovalRejected || loaded.RejectionReason == nil ||
		*loaded.RejectionReason != "No reason provided" {
		t.Fatalf("rejected = %+v", loaded)
	}
	if !strings.Contains(out.String(), "Rejection reason: ") || !strings.Contains(out.String(), "✗ Rejected: No reason provided") {
		t.Fatalf("display = %s", out.String())
	}

	var reasonOut bytes.Buffer
	if _, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:  strings.NewReader("r\nnot useful\n"),
		Out: &reasonOut,
	}); err != nil {
		t.Fatalf("InteractiveApproval reason: %v", err)
	}
	loaded, _ = storage.LoadTest("goal_001", "test_001")
	if loaded.RejectionReason == nil || *loaded.RejectionReason != "not useful" {
		t.Fatalf("reason = %v", loaded.RejectionReason)
	}
}

// Parity-exempt: Go-only guard for the editor branch: changed code -> modified,
// unchanged code -> plain approval with upstream's message.
func TestInteractiveApprovalModify(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")

	var out bytes.Buffer
	results, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:   strings.NewReader("e\n"),
		Out:  &out,
		Edit: func(string) (string, error) { return "edited code", nil },
	})
	if err != nil {
		t.Fatalf("InteractiveApproval: %v", err)
	}
	if results[0].Action != ApprovalActionModify || !results[0].Success {
		t.Fatalf("result = %+v", results[0])
	}
	loaded, _ := storage.LoadTest("goal_001", "test_001")
	if loaded.ApprovalStatus != ApprovalModified || loaded.TestCode != "edited code" ||
		loaded.OriginalCode == nil || *loaded.OriginalCode != "pass" {
		t.Fatalf("modified = %+v", loaded)
	}
	if !strings.Contains(out.String(), "✓ Modified and approved") {
		t.Fatalf("display = %s", out.String())
	}

	test = saveTest(t, storage, "test_002", "goal_001", "c1")
	var unchanged bytes.Buffer
	results, err = InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:   strings.NewReader("e\n"),
		Out:  &unchanged,
		Edit: func(code string) (string, error) { return code, nil },
	})
	if err != nil {
		t.Fatalf("InteractiveApproval unchanged: %v", err)
	}
	if results[0].Action != ApprovalActionApprove || results[0].Message == nil ||
		*results[0].Message != "No modifications made" {
		t.Fatalf("unchanged result = %+v", results[0])
	}
	loaded, _ = storage.LoadTest("goal_001", "test_002")
	if loaded.ApprovalStatus != ApprovalApproved {
		t.Fatalf("unchanged status = %q, want approved", loaded.ApprovalStatus)
	}
}

// Parity-exempt: Go-only guard for skip: it must not update storage and the
// test stays pending.
func TestInteractiveApprovalSkipLeavesPending(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")
	before := test.UpdatedAt

	var out bytes.Buffer
	results, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:  strings.NewReader("s\n"),
		Out: &out,
	})
	if err != nil {
		t.Fatalf("InteractiveApproval: %v", err)
	}
	if !results[0].Success || results[0].Action != ApprovalActionSkip {
		t.Fatalf("result = %+v", results[0])
	}
	loaded, _ := storage.LoadTest("goal_001", "test_001")
	if loaded.ApprovalStatus != ApprovalPending {
		t.Fatalf("status = %q, want pending", loaded.ApprovalStatus)
	}
	if !loaded.UpdatedAt.Equal(before.Time) {
		t.Fatalf("skip rewrote updated_at: %v -> %v", before, loaded.UpdatedAt)
	}
	if !strings.Contains(out.String(), "⏭ Skipped (remains pending)") {
		t.Fatalf("display = %s", out.String())
	}
}

// Parity-exempt: Go-only guard for the invalid-choice loop and its message.
func TestInteractiveApprovalRetriesInvalidChoice(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")

	var out bytes.Buffer
	if _, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:  strings.NewReader("x\n\nA\n"),
		Out: &out,
	}); err != nil {
		t.Fatalf("InteractiveApproval: %v", err)
	}
	display := out.String()
	if strings.Count(display, "Invalid choice. Please enter a, r, e, or s.") != 2 {
		t.Fatalf("invalid-choice message count:\n%s", display)
	}
	loaded, _ := storage.LoadTest("goal_001", "test_001")
	if loaded.ApprovalStatus != ApprovalApproved {
		t.Fatalf("status = %q, want approved", loaded.ApprovalStatus)
	}
}

// Parity-exempt: Go-only guard for EOF behavior (Python input() raises).
func TestInteractiveApprovalEOF(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")
	_, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:  strings.NewReader(""),
		Out: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("empty input must fail")
	}
}

// Parity-exempt: Go-only guard for storage errors surfacing as result errors
// instead of aborting the review.
func TestInteractiveApprovalStorageError(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")
	// Make the approved index a directory so the atomic write cannot replace
	// it; the approval must come back as an error result.
	if err := makeDir(t, storage.BasePath(), "indexes/by_approval/approved.json"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	results, err := InteractiveApproval([]*Test{test}, storage, InteractiveOptions{
		In:  strings.NewReader("a\n"),
		Out: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("InteractiveApproval: %v", err)
	}
	if results[0].Success || results[0].Error == nil {
		t.Fatalf("result = %+v, want failure", results[0])
	}
}

// Parity-exempt: Go-only guard for batch approval counts, per-action messages
// (including upstream's "modifyd"/"rejectd" quirk), and skip persistence.
func TestBatchApproval(t *testing.T) {
	storage := newStorage(t)
	for _, id := range []string{"a", "m", "r", "s"} {
		saveTest(t, storage, id, "goal_001", "c1")
	}
	code := "edited"
	reason := "wrong"
	requests := []ApprovalRequest{
		NewApprovalRequest("a", ApprovalActionApprove),
		NewApprovalRequest("m", ApprovalActionModify),
		NewApprovalRequest("r", ApprovalActionReject),
		NewApprovalRequest("s", ApprovalActionSkip),
		NewApprovalRequest("missing", ApprovalActionApprove),
	}
	requests[1].ModifiedCode = &code
	requests[2].Reason = &reason

	result := BatchApproval("goal_001", requests, storage)
	if result.Total != 5 || result.Approved != 1 || result.Modified != 1 || result.Rejected != 1 ||
		result.Skipped != 1 || result.Errors != 1 {
		t.Fatalf("counts = %+v", result)
	}
	if len(result.Results) != 5 {
		t.Fatalf("results = %d, want 5", len(result.Results))
	}
	expectMessage := map[int]string{
		0: "Test approved successfully",
		1: "Test modifyd successfully",
		2: "Test rejectd successfully",
		3: "Test skipd successfully",
	}
	for index, want := range expectMessage {
		if result.Results[index].Message == nil || *result.Results[index].Message != want {
			t.Fatalf("result[%d] message = %v, want %q", index, result.Results[index].Message, want)
		}
	}
	if result.Results[4].Success || result.Results[4].Error == nil ||
		!strings.Contains(*result.Results[4].Error, "not found") {
		t.Fatalf("missing test result = %+v", result.Results[4])
	}
	statuses := map[string]ApprovalStatus{}
	for _, id := range []string{"a", "m", "r", "s"} {
		loaded, err := storage.LoadTest("goal_001", id)
		if err != nil {
			t.Fatalf("load %s: %v", id, err)
		}
		statuses[id] = loaded.ApprovalStatus
	}
	if statuses["a"] != ApprovalApproved || statuses["m"] != ApprovalModified ||
		statuses["r"] != ApprovalRejected || statuses["s"] != ApprovalPending {
		t.Fatalf("statuses = %+v", statuses)
	}
	if skipped, _ := storage.GetPendingTests("goal_001"); len(skipped) != 1 || skipped[0].ID != "s" {
		t.Fatalf("pending = %+v", skipped)
	}
}

// Parity-exempt: Go-only guard for batch validation errors (bad request does
// not abort the rest of the batch).
func TestBatchApprovalValidatesRequests(t *testing.T) {
	storage := newStorage(t)
	saveTest(t, storage, "t1", "goal_001", "c1")
	result := BatchApproval("goal_001", []ApprovalRequest{
		NewApprovalRequest("t1", ApprovalActionModify),
		NewApprovalRequest("t1", ApprovalActionReject),
		NewApprovalRequest("t1", ApprovalActionApprove),
	}, storage)
	if result.Errors != 2 || result.Approved != 1 {
		t.Fatalf("counts = %+v", result)
	}
	if result.Results[0].Error == nil || *result.Results[0].Error != "modified_code is required for MODIFY action" {
		t.Fatalf("modify error = %v", result.Results[0].Error)
	}
	if result.Results[1].Error == nil || *result.Results[1].Error != "reason is required for REJECT action" {
		t.Fatalf("reject error = %v", result.Results[1].Error)
	}
}

// Parity-exempt: Go-only guard for a hand-crafted unknown action reaching
// BatchApproval: pydantic would reject the enum at construction, so the port
// must count it as an error instead of silently persisting an unchanged test.
func TestBatchApprovalRejectsUnknownAction(t *testing.T) {
	storage := newStorage(t)
	saveTest(t, storage, "t1", "goal_001", "c1")
	request := ApprovalRequest{TestID: "t1", Action: ApprovalAction("delete"), ApprovedBy: "user"}
	result := BatchApproval("goal_001", []ApprovalRequest{request}, storage)
	if result.Errors != 1 || result.Approved != 0 {
		t.Fatalf("counts = %+v", result)
	}
	if result.Results[0].Error == nil || !strings.Contains(*result.Results[0].Error, "Unknown action") {
		t.Fatalf("error = %v", result.Results[0].Error)
	}
	loaded, _ := storage.LoadTest("goal_001", "t1")
	if loaded.ApprovalStatus != ApprovalPending {
		t.Fatalf("status = %q, want pending", loaded.ApprovalStatus)
	}
}
