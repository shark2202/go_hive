package testframework

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStorage(t *testing.T) *TestStorage {
	t.Helper()
	storage, err := NewTestStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewTestStorage: %v", err)
	}
	return storage
}

func saveTest(t *testing.T, storage *TestStorage, id, goalID, criteriaID string) *Test {
	t.Helper()
	test := NewTest(id, goalID, criteriaID, TestTypeConstraint, "test_"+id, "pass", "test")
	if err := storage.SaveTest(test); err != nil {
		t.Fatalf("SaveTest(%s): %v", id, err)
	}
	return test
}

func readIndex(t *testing.T, storage *TestStorage, indexType, key string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(storage.BasePath(), "indexes", indexType, key+".json"))
	if err != nil {
		t.Fatalf("read index %s/%s: %v", indexType, key, err)
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err != nil {
		t.Fatalf("parse index %s/%s: %v", indexType, key, err)
	}
	return ids
}

// Parity: core/tests/test_testing_framework.py::test_save_and_load_test
func TestSaveAndLoadTest(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "constraint_001")

	loaded, err := storage.LoadTest("goal_001", "test_001")
	if err != nil {
		t.Fatalf("LoadTest: %v", err)
	}
	if loaded == nil || loaded.ID != "test_001" || loaded.TestName != "test_test_001" {
		t.Fatalf("loaded = %+v", loaded)
	}
	if !loaded.CreatedAt.Equal(test.CreatedAt.Time) {
		t.Fatalf("created_at = %v, want %v", loaded.CreatedAt, test.CreatedAt)
	}
	for _, entry := range []struct{ index, key string }{
		{"by_goal", "goal_001"},
		{"by_approval", "pending"},
		{"by_type", "constraint"},
		{"by_criteria", "constraint_001"},
	} {
		ids := readIndex(t, storage, entry.index, entry.key)
		if len(ids) != 1 || ids[0] != "test_001" {
			t.Fatalf("%s/%s = %v", entry.index, entry.key, ids)
		}
	}
	document, err := os.ReadFile(filepath.Join(storage.BasePath(), "tests", "goal_001", "test_001.json"))
	if err != nil {
		t.Fatalf("read test document: %v", err)
	}
	if !strings.Contains(string(document), "\n  \"id\": \"test_001\"") {
		t.Fatalf("test document is not indented like upstream:\n%s", document)
	}
}

// Parity: core/tests/test_testing_framework.py::test_delete_test
func TestDeleteTest(t *testing.T) {
	storage := newStorage(t)
	saveTest(t, storage, "test_001", "goal_001", "constraint_001")
	result := NewTestResult("test_001", true, 10)
	if err := storage.SaveResult("test_001", result); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}

	deleted, err := storage.DeleteTest("goal_001", "test_001")
	if err != nil || !deleted {
		t.Fatalf("DeleteTest = %v/%v", deleted, err)
	}
	loaded, err := storage.LoadTest("goal_001", "test_001")
	if err != nil || loaded != nil {
		t.Fatalf("LoadTest after delete = %+v/%v", loaded, err)
	}
	for _, entry := range []struct{ index, key string }{
		{"by_goal", "goal_001"},
		{"by_approval", "pending"},
	} {
		if ids := readIndex(t, storage, entry.index, entry.key); len(ids) != 0 {
			t.Fatalf("%s/%s = %v after delete", entry.index, entry.key, ids)
		}
	}
	if _, err := os.Stat(filepath.Join(storage.BasePath(), "results", "test_001")); !os.IsNotExist(err) {
		t.Fatalf("results dir still present: %v", err)
	}
	deleted, err = storage.DeleteTest("goal_001", "test_001")
	if err != nil || deleted {
		t.Fatalf("second DeleteTest = %v/%v, want false/nil", deleted, err)
	}
}

// Parity: core/tests/test_testing_framework.py::test_get_tests_by_goal
func TestGetTestsByGoal(t *testing.T) {
	storage := newStorage(t)
	for index := 0; index < 3; index++ {
		saveTest(t, storage, fmt.Sprintf("test_%d", index), "goal_001", fmt.Sprintf("constraint_%d", index))
	}
	tests, err := storage.GetTestsByGoal("goal_001")
	if err != nil {
		t.Fatalf("GetTestsByGoal: %v", err)
	}
	if len(tests) != 3 {
		t.Fatalf("tests = %d, want 3", len(tests))
	}
	for index, test := range tests {
		if test.ID != fmt.Sprintf("test_%d", index) {
			t.Fatalf("tests[%d] = %s, want insertion order", index, test.ID)
		}
	}
}

// Parity: core/tests/test_testing_framework.py::test_get_approved_tests
func TestGetApprovedTests(t *testing.T) {
	storage := newStorage(t)
	approved := saveTest(t, storage, "test_001", "goal_001", "c1")
	approved.Approve("")
	if err := storage.SaveTest(approved); err != nil {
		t.Fatalf("SaveTest approved: %v", err)
	}
	saveTest(t, storage, "test_002", "goal_001", "c2")
	modified := saveTest(t, storage, "test_003", "goal_001", "c3")
	modified.Modify("modified", "user")
	if err := storage.SaveTest(modified); err != nil {
		t.Fatalf("SaveTest modified: %v", err)
	}

	tests, err := storage.GetApprovedTests("goal_001")
	if err != nil {
		t.Fatalf("GetApprovedTests: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("approved = %d, want 2 (approved + modified)", len(tests))
	}
	pending, err := storage.GetPendingTests("goal_001")
	if err != nil || len(pending) != 1 || pending[0].ID != "test_002" {
		t.Fatalf("pending = %+v/%v", pending, err)
	}
}

// Parity: core/tests/test_testing_framework.py::test_save_and_load_result
func TestSaveAndLoadResult(t *testing.T) {
	storage := newStorage(t)
	freezeClock(t, time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local))
	result := NewTestResult("test_001", true, 100)
	if err := storage.SaveResult("test_001", result); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	loaded, err := storage.GetLatestResult("test_001")
	if err != nil {
		t.Fatalf("GetLatestResult: %v", err)
	}
	if loaded == nil || !loaded.Passed || loaded.DurationMS != 100 {
		t.Fatalf("latest = %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(storage.BasePath(), "results", "test_001", "20261126_120000.json")); err != nil {
		t.Fatalf("timestamped result missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage.BasePath(), "results", "test_001", "latest.json")); err != nil {
		t.Fatalf("latest.json missing: %v", err)
	}
	missing, err := storage.GetLatestResult("none")
	if err != nil || missing != nil {
		t.Fatalf("missing latest = %+v/%v", missing, err)
	}
}

// Parity: core/tests/test_testing_framework.py::test_result_history
func TestResultHistory(t *testing.T) {
	storage := newStorage(t)
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)
	for index := 0; index < 5; index++ {
		result := NewTestResult("test_001", index%2 == 0, 100+index)
		result.Timestamp = NewTimestamp(base.Add(time.Duration(index) * time.Second))
		if err := storage.SaveResult("test_001", result); err != nil {
			t.Fatalf("SaveResult %d: %v", index, err)
		}
	}
	history, err := storage.GetResultHistory("test_001", 3)
	if err != nil {
		t.Fatalf("GetResultHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history = %d, want 3", len(history))
	}
	for index, want := range []int{104, 103, 102} {
		if history[index].DurationMS != want {
			t.Fatalf("history[%d] = %d, want %d (newest first)", index, history[index].DurationMS, want)
		}
	}
	all, err := storage.GetResultHistory("test_001", 10)
	if err != nil || len(all) != 5 {
		t.Fatalf("full history = %d/%v, want 5", len(all), err)
	}
	empty, err := storage.GetResultHistory("none", 10)
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing history = %v/%v", empty, err)
	}
}

// Parity: core/tests/test_testing_framework.py::test_get_stats
func TestGetStats(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")
	test.Approve("")
	if err := storage.UpdateTest(test); err != nil {
		t.Fatalf("UpdateTest: %v", err)
	}
	stats, err := storage.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalTests != 1 || stats.TotalGoals != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.ByApproval["approved"] != 1 || stats.ByApproval["pending"] != 0 {
		t.Fatalf("by_approval = %+v", stats.ByApproval)
	}
	if stats.StoragePath != storage.BasePath() {
		t.Fatalf("storage_path = %q", stats.StoragePath)
	}
}

// Parity-exempt: Go-only index-transition guard. Upstream update_test moves
// the by_approval entry; the port must do the same for every transition.
func TestUpdateTestMovesApprovalIndex(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")
	if ids := readIndex(t, storage, "by_approval", "pending"); len(ids) != 1 {
		t.Fatalf("pending index = %v", ids)
	}

	test.Approve("reviewer")
	if err := storage.UpdateTest(test); err != nil {
		t.Fatalf("UpdateTest: %v", err)
	}
	if ids := readIndex(t, storage, "by_approval", "pending"); len(ids) != 0 {
		t.Fatalf("pending index after approve = %v", ids)
	}
	if ids := readIndex(t, storage, "by_approval", "approved"); len(ids) != 1 || ids[0] != "test_001" {
		t.Fatalf("approved index = %v", ids)
	}

	test.Reject("wrong expectation")
	if err := storage.UpdateTest(test); err != nil {
		t.Fatalf("UpdateTest reject: %v", err)
	}
	if ids := readIndex(t, storage, "by_approval", "approved"); len(ids) != 0 {
		t.Fatalf("approved index after reject = %v (negative regression)", ids)
	}
	if ids := readIndex(t, storage, "by_approval", "rejected"); len(ids) != 1 {
		t.Fatalf("rejected index = %v", ids)
	}
	loaded, err := storage.LoadTest("goal_001", "test_001")
	if err != nil || loaded.ApprovalStatus != ApprovalRejected {
		t.Fatalf("stored status = %+v/%v", loaded, err)
	}
}

// Parity-exempt: Go-only negative regression. A rejected test must never come
// back from GetApprovedTests, even after it was approved first; this is the
// bug the by_approval transition guards against.
func TestRejectedTestStaysOutOfApprovedSet(t *testing.T) {
	storage := newStorage(t)
	test := saveTest(t, storage, "test_001", "goal_001", "c1")
	test.Approve("")
	if err := storage.UpdateTest(test); err != nil {
		t.Fatalf("approve: %v", err)
	}
	test.Reject("no longer valid")
	if err := storage.UpdateTest(test); err != nil {
		t.Fatalf("reject: %v", err)
	}
	approved, err := storage.GetApprovedTests("goal_001")
	if err != nil {
		t.Fatalf("GetApprovedTests: %v", err)
	}
	if len(approved) != 0 {
		t.Fatalf("rejected test leaked into approved set: %+v", approved)
	}
	pending, err := storage.GetPendingTests("goal_001")
	if err != nil || len(pending) != 0 {
		t.Fatalf("rejected test leaked into pending set: %+v/%v", pending, err)
	}
}

// Parity-exempt: Go-only guard for upstream update_test semantics: loading a
// missing document yields None, and the save then creates it.
func TestUpdateTestCreatesMissingDocument(t *testing.T) {
	storage := newStorage(t)
	test := NewTest("test_001", "goal_001", "c1", TestTypeConstraint, "test_1", "pass", "test")
	if err := storage.UpdateTest(test); err != nil {
		t.Fatalf("UpdateTest on missing: %v", err)
	}
	loaded, err := storage.LoadTest("goal_001", "test_001")
	if err != nil || loaded == nil {
		t.Fatalf("created document = %+v/%v", loaded, err)
	}
}

// Parity-exempt: Go-only guard for the index file format a Python reader
// consumes (json.dump default separators, no trailing newline).
func TestIndexFileFormat(t *testing.T) {
	storage := newStorage(t)
	saveTest(t, storage, "test_0", "goal_001", "c1")
	saveTest(t, storage, "test_1", "goal_001", "c1")
	data, err := os.ReadFile(filepath.Join(storage.BasePath(), "indexes", "by_goal", "goal_001.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if string(data) != `["test_0", "test_1"]` {
		t.Fatalf("index = %q", data)
	}
}

// Parity-exempt: Go-only corrupt-storage guard. Upstream raises on invalid
// JSON; every ported read path must surface an error instead of a zero value.
func TestCorruptStorage(t *testing.T) {
	t.Run("test document", func(t *testing.T) {
		storage := newStorage(t)
		saveTest(t, storage, "test_001", "goal_001", "c1")
		path := filepath.Join(storage.BasePath(), "tests", "goal_001", "test_001.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("corrupt: %v", err)
		}
		if _, err := storage.LoadTest("goal_001", "test_001"); err == nil {
			t.Fatal("LoadTest must fail on corrupt JSON")
		}
		if _, err := storage.GetTestsByGoal("goal_001"); err == nil {
			t.Fatal("GetTestsByGoal must fail on a corrupt document")
		}
	})
	t.Run("index", func(t *testing.T) {
		storage := newStorage(t)
		saveTest(t, storage, "test_001", "goal_001", "c1")
		path := filepath.Join(storage.BasePath(), "indexes", "by_goal", "goal_001.json")
		if err := os.WriteFile(path, []byte(`["test_001"`), 0o644); err != nil {
			t.Fatalf("corrupt: %v", err)
		}
		if _, err := storage.GetTestsByGoal("goal_001"); err == nil {
			t.Fatal("GetTestsByGoal must fail on a corrupt index")
		}
		if _, err := storage.GetStats(); err == nil {
			t.Fatal("GetStats must fail on a corrupt index")
		}
	})
	t.Run("latest result", func(t *testing.T) {
		storage := newStorage(t)
		dir := filepath.Join(storage.BasePath(), "results", "test_001")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "latest.json"), []byte("null trailing"), 0o644); err != nil {
			t.Fatalf("corrupt: %v", err)
		}
		if _, err := storage.GetLatestResult("test_001"); err == nil {
			t.Fatal("GetLatestResult must fail on corrupt JSON")
		}
	})
}

// Parity-exempt: Go-only concurrency guard. Upstream has no locking; the port
// serializes index read-modify-write, so parallel saves and status flips must
// leave one entry per test and indexes consistent with the stored documents.
func TestConcurrentStorageAccess(t *testing.T) {
	storage := newStorage(t)
	const writers = 16
	var wait sync.WaitGroup
	errs := make(chan error, writers*3)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			test := NewTest(fmt.Sprintf("test_%02d", index), "goal_001", "c1", TestTypeConstraint,
				fmt.Sprintf("test_%02d", index), "pass", "test")
			if err := storage.SaveTest(test); err != nil {
				errs <- err
			}
		}(index)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}
	ids := readIndex(t, storage, "by_goal", "goal_001")
	if len(ids) != writers {
		t.Fatalf("by_goal = %d entries, want %d", len(ids), writers)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate index entry %q", id)
		}
		seen[id] = true
	}
	stats, err := storage.GetStats()
	if err != nil || stats.TotalTests != writers {
		t.Fatalf("stats = %+v/%v", stats, err)
	}

	// Flip one test between approved and rejected from both directions. The
	// final status is nondeterministic, but the index membership must be
	// exactly one category and must match the stored document.
	saveTest(t, storage, "flip", "goal_001", "c1")
	flipErrs := make(chan error, 8)
	var flipWait sync.WaitGroup
	for index := 0; index < 8; index++ {
		flipWait.Add(1)
		go func(index int) {
			defer flipWait.Done()
			loaded, err := storage.LoadTest("goal_001", "flip")
			if err != nil || loaded == nil {
				flipErrs <- fmt.Errorf("load flip: %v", err)
				return
			}
			if index%2 == 0 {
				loaded.Approve("")
			} else {
				loaded.Reject("flip")
			}
			if err := storage.UpdateTest(loaded); err != nil {
				flipErrs <- err
			}
		}(index)
	}
	flipWait.Wait()
	close(flipErrs)
	for err := range flipErrs {
		t.Fatalf("concurrent update: %v", err)
	}
	loaded, err := storage.LoadTest("goal_001", "flip")
	if err != nil || loaded == nil {
		t.Fatalf("reload flip: %+v/%v", loaded, err)
	}
	memberships := 0
	for _, status := range []string{"pending", "approved", "modified", "rejected"} {
		ids, err := storage.GetTestsByApprovalStatus(ApprovalStatus(status))
		if err != nil {
			t.Fatalf("index %s: %v", status, err)
		}
		for _, id := range ids {
			if id == "flip" {
				memberships++
				if string(loaded.ApprovalStatus) != status {
					t.Fatalf("index status %q != stored %q", status, loaded.ApprovalStatus)
				}
			}
		}
	}
	if memberships != 1 {
		t.Fatalf("flip appears in %d approval indexes, want 1", memberships)
	}
}
