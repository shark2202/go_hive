package cli

// Tests for the hidden `hive test-approval` dev harness. Upstream ships
// framework/testing as library functions with no CLI entry, so the command's
// parsing and exit codes are Go-side contracts; the workflow itself is pinned
// in internal/testframework.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/testframework"
)

func runCLIWithStdin(t *testing.T, stdin string, argv ...string) (int, string, string) {
	t.Helper()
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	runner.Stdin = strings.NewReader(stdin)
	code := runner.Run(argv)
	return code, stdout.String(), stderr.String()
}

func newTestStore(t *testing.T) (*testframework.TestStorage, string) {
	t.Helper()
	root := t.TempDir()
	storage, err := testframework.NewTestStorage(root)
	if err != nil {
		t.Fatalf("NewTestStorage: %v", err)
	}
	return storage, root
}

func seedTest(t *testing.T, storage *testframework.TestStorage, id, goalID string) *testframework.Test {
	t.Helper()
	test := testframework.NewTest(id, goalID, "c1", testframework.TestTypeConstraint, "test_"+id, "pass", "test")
	if err := storage.SaveTest(test); err != nil {
		t.Fatalf("SaveTest(%s): %v", id, err)
	}
	return test
}

// Parity-exempt: Go-only dev-harness guard. The command is a Go-side mount
// with no Python CLI counterpart; --root is required and missing flags exit 2.
func TestTestApprovalRequiresRoot(t *testing.T) {
	code, _, stderr := runCLIWithStdin(t, "", "test-approval")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "--root") {
		t.Fatalf("stderr = %q, want it to name --root", stderr)
	}
}

// Parity-exempt: Go-only dev-harness guard for conflicting modes (exit 2).
func TestTestApprovalModeConflicts(t *testing.T) {
	root := t.TempDir()
	cases := [][]string{
		{"test-approval", "--root", root, "--batch", "requests.json", "--debug", "t1"},
		{"test-approval", "--root", root, "--debug", "t1"},
	}
	wants := []string{"mutually exclusive", "--debug requires --goal"}
	for index, argv := range cases {
		code, _, stderr := runCLIWithStdin(t, "", argv...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2 (%s)", argv, code, stderr)
		}
		if !strings.Contains(stderr, wants[index]) {
			t.Fatalf("%v stderr = %q, want %q", argv, stderr, wants[index])
		}
	}
}

// Parity-exempt: Go-only dev-harness guard for the approve path and exit 0.
func TestTestApprovalReviewApprove(t *testing.T) {
	storage, root := newTestStore(t)
	seedTest(t, storage, "test_001", "goal_001")

	code, stdout, stderr := runCLIWithStdin(t, "a\n", "test-approval", "--root", root, "--goal", "goal_001")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "test_test_001") || !strings.Contains(stdout, "✓ Approved") {
		t.Fatalf("stdout = %s", stdout)
	}
	loaded, err := storage.LoadTest("goal_001", "test_001")
	if err != nil || loaded.ApprovalStatus != testframework.ApprovalApproved {
		t.Fatalf("stored status = %+v/%v", loaded, err)
	}
}

// Parity-exempt: Go-only dev-harness guard for the skip path (exit 0, still
// pending).
func TestTestApprovalReviewSkip(t *testing.T) {
	storage, root := newTestStore(t)
	seedTest(t, storage, "test_001", "goal_001")

	code, stdout, stderr := runCLIWithStdin(t, "s\n", "test-approval", "--root", root)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "⏭ Skipped (remains pending)") {
		t.Fatalf("stdout = %s", stdout)
	}
	loaded, _ := storage.LoadTest("goal_001", "test_001")
	if loaded.ApprovalStatus != testframework.ApprovalPending {
		t.Fatalf("status = %q, want pending", loaded.ApprovalStatus)
	}
}

// Parity-exempt: Go-only dev-harness guard for EOF: Python input() raises, so
// the review aborts with exit 1 instead of hanging.
func TestTestApprovalReviewEOF(t *testing.T) {
	storage, root := newTestStore(t)
	seedTest(t, storage, "test_001", "goal_001")

	code, _, stderr := runCLIWithStdin(t, "", "test-approval", "--root", root, "--goal", "goal_001")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("expected an error message on stderr")
	}
}

// Parity-exempt: Go-only dev-harness guard for the no-args review scope: with
// no --goal every goal is reviewed.
func TestTestApprovalReviewAllGoals(t *testing.T) {
	storage, root := newTestStore(t)
	seedTest(t, storage, "a", "goal_a")
	seedTest(t, storage, "b", "goal_b")

	code, stdout, stderr := runCLIWithStdin(t, "a\ns\n", "test-approval", "--root", root)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "test_a") || !strings.Contains(stdout, "test_b") {
		t.Fatalf("stdout = %s", stdout)
	}
	first, _ := storage.LoadTest("goal_a", "a")
	second, _ := storage.LoadTest("goal_b", "b")
	if first.ApprovalStatus != testframework.ApprovalApproved || second.ApprovalStatus != testframework.ApprovalPending {
		t.Fatalf("statuses = %q/%q", first.ApprovalStatus, second.ApprovalStatus)
	}
}

// Parity-exempt: Go-only dev-harness guard for --batch (summary + exit 0).
func TestTestApprovalBatch(t *testing.T) {
	storage, root := newTestStore(t)
	seedTest(t, storage, "test_001", "goal_001")
	path := filepath.Join(root, "requests.json")
	document, err := json.Marshal(testframework.BatchApprovalRequest{
		GoalID: "goal_001",
		Approvals: []testframework.ApprovalRequest{
			testframework.NewApprovalRequest("test_001", testframework.ApprovalActionApprove),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("write batch: %v", err)
	}

	code, stdout, stderr := runCLIWithStdin(t, "", "test-approval", "--root", root, "--batch", path)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, "Processed 1 tests: 1 approved") {
		t.Fatalf("stdout = %s", stdout)
	}
	loaded, _ := storage.LoadTest("goal_001", "test_001")
	if loaded.ApprovalStatus != testframework.ApprovalApproved {
		t.Fatalf("status = %q", loaded.ApprovalStatus)
	}
}

// Parity-exempt: Go-only dev-harness guard for --batch error rows (exit 1)
// and unreadable/invalid files.
func TestTestApprovalBatchErrors(t *testing.T) {
	_, root := newTestStore(t)
	path := filepath.Join(root, "requests.json")
	document, err := json.Marshal(testframework.BatchApprovalRequest{
		GoalID: "goal_001",
		Approvals: []testframework.ApprovalRequest{
			testframework.NewApprovalRequest("missing", testframework.ApprovalActionApprove),
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	code, _, stderr := runCLIWithStdin(t, "", "test-approval", "--root", root, "--batch", path)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "not found") {
		t.Fatalf("stderr = %q", stderr)
	}

	code, _, stderr = runCLIWithStdin(t, "", "test-approval", "--root", root, "--batch", filepath.Join(root, "absent.json"))
	if code != 1 || !strings.Contains(stderr, "read batch") {
		t.Fatalf("missing batch exit/stderr = %d/%q", code, stderr)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	code, _, stderr = runCLIWithStdin(t, "", "test-approval", "--root", root, "--batch", path)
	if code != 1 || !strings.Contains(stderr, "parse batch") {
		t.Fatalf("corrupt batch exit/stderr = %d/%q", code, stderr)
	}
}

// Parity-exempt: Go-only dev-harness guard for --goal/--batch mismatch.
func TestTestApprovalBatchGoalMismatch(t *testing.T) {
	_, root := newTestStore(t)
	path := filepath.Join(root, "requests.json")
	document, err := json.Marshal(testframework.BatchApprovalRequest{GoalID: "goal_001"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	code, _, stderr := runCLIWithStdin(t, "", "test-approval", "--root", root, "--goal", "other", "--batch", path)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "does not match batch goal") {
		t.Fatalf("stderr = %q", stderr)
	}
}

// Parity-exempt: Go-only dev-harness guard for --debug: the full report is
// JSON on stdout; a missing test exits 1.
func TestTestApprovalDebug(t *testing.T) {
	storage, root := newTestStore(t)
	seedTest(t, storage, "test_001", "goal_001")
	result := testframework.NewTestResult("test_001", false, 12)
	message := "TypeError: boom"
	result.ErrorMessage = &message
	if err := storage.SaveResult("test_001", result); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	code, stdout, stderr := runCLIWithStdin(t, "", "test-approval", "--root", root, "--goal", "goal_001", "--debug", "test_001")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("debug output is not JSON: %v\n%s", err, stdout)
	}
	if report["test_id"] != "test_001" || report["test_name"] != "test_test_001" {
		t.Fatalf("report = %+v", report)
	}
	if report["error_category"] != "implementation_error" {
		t.Fatalf("error_category = %v", report["error_category"])
	}
	if report["suggested_fix"] == nil {
		t.Fatalf("suggested_fix missing: %+v", report)
	}

	code, _, stderr = runCLIWithStdin(t, "", "test-approval", "--root", root, "--goal", "goal_001", "--debug", "absent")
	if code != 1 {
		t.Fatalf("missing debug exit = %d, want 1", code)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("missing debug stderr = %q", stderr)
	}
}

// Parity-exempt: Go-only guard that the dev harness stays out of the frozen
// CLI surface (hidden commands are excluded from every rendered page).
func TestTestApprovalHiddenFromHelp(t *testing.T) {
	code, stdout, stderr := runCLIWithStdin(t, "", "--help")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if strings.Contains(stdout, "test-approval") {
		t.Fatalf("hidden command leaked into root help:\n%s", stdout)
	}
}
