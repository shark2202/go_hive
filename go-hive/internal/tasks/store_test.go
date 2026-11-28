package tasks

// Parity: framework/tasks/tests/test_store.py

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const storeTestSession = "test_session"

func newStoreFixture(t *testing.T) (*TaskStore, string) {
	t.Helper()
	root := t.TempDir()
	ResetSessionDirCache()
	t.Cleanup(ResetSessionDirCache)
	return NewTaskStore(root), root
}

func mustCreate(t *testing.T, store *TaskStore, sessionID string, spec CreateTaskSpec) *TaskRecord {
	t.Helper()
	rec, err := store.CreateTask(sessionID, spec)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return rec
}

func mustEnsure(t *testing.T, store *TaskStore, sessionID string) *TaskListMeta {
	t.Helper()
	meta, err := store.EnsureTaskList(sessionID, nil)
	if err != nil {
		t.Fatalf("EnsureTaskList: %v", err)
	}
	return meta
}

// Parity: framework/tasks/tests/test_store.py::test_create_and_get
func TestCreateAndGet(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "hi"})
	if rec.ID != 1 {
		t.Fatalf("id = %d, want 1", rec.ID)
	}
	fetched := store.GetTask(storeTestSession, 1)
	if fetched == nil {
		t.Fatal("GetTask returned nil")
	}
	if fetched.Subject != "hi" {
		t.Fatalf("subject = %q", fetched.Subject)
	}
	if fetched.Status != TaskStatusPending {
		t.Fatalf("status = %q", fetched.Status)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_get_missing_returns_none
func TestGetMissingReturnsNone(t *testing.T) {
	store, _ := newStoreFixture(t)
	if got := store.GetTask(storeTestSession, 999); got != nil {
		t.Fatalf("GetTask = %v, want nil", got)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_list_ascending
func TestListAscending(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	for _, subject := range []string{"a", "b", "c"} {
		mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: subject})
	}
	records := store.ListTasks(storeTestSession)
	ids := make([]int, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	want := []int{1, 2, 3}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
}

// Parity: framework/tasks/tests/test_store.py::test_list_filters_internal
func TestListFiltersInternal(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "visible"})
	mustCreate(t, store, storeTestSession, CreateTaskSpec{
		Subject:  "hidden",
		Metadata: map[string]any{"_internal": true},
	})
	if public := store.ListTasks(storeTestSession); len(public) != 1 {
		t.Fatalf("public len = %d, want 1", len(public))
	}
	if all := store.ListTasks(storeTestSession, true); len(all) != 2 {
		t.Fatalf("all len = %d, want 2", len(all))
	}
}

// Parity: framework/tasks/tests/test_store.py::test_concurrent_create_distinct_ids
func TestConcurrentCreateDistinctIDs(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	const workers = 20
	results := make([]*TaskRecord, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = store.CreateTask(storeTestSession, CreateTaskSpec{
				Subject: "t" + itoa(i),
			})
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("CreateTask %d: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("CreateTask %d returned nil", i)
		}
		if seen[results[i].ID] {
			t.Fatalf("duplicate id %d", results[i].ID)
		}
		seen[results[i].ID] = true
	}
	for id := 1; id <= workers; id++ {
		if !seen[id] {
			t.Fatalf("missing id %d", id)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// Parity: framework/tasks/tests/test_store.py::test_update_returns_changed_fields
func TestUpdateReturnsChangedFields(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "orig"})
	sameSubject := "orig"
	status := TaskStatusInProgress
	newRec, fields := store.UpdateTask(storeTestSession, rec.ID, UpdateTaskOptions{
		Subject: &sameSubject,
		Status:  &status,
	})
	if len(fields) != 1 || fields[0] != "status" {
		t.Fatalf("fields = %v, want [status]", fields)
	}
	if newRec == nil || newRec.Status != TaskStatusInProgress {
		t.Fatalf("new status = %v", newRec)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_update_missing_returns_none
func TestUpdateMissingReturnsNone(t *testing.T) {
	store, _ := newStoreFixture(t)
	subject := "x"
	newRec, fields := store.UpdateTask(storeTestSession, 42, UpdateTaskOptions{Subject: &subject})
	if newRec != nil {
		t.Fatalf("newRec = %v, want nil", newRec)
	}
	if len(fields) != 0 {
		t.Fatalf("fields = %v, want empty", fields)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_metadata_patch_merges_and_deletes
func TestMetadataPatchMergesAndDeletes(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{
		Subject:  "x",
		Metadata: map[string]any{"a": 1, "b": 2},
	})
	newRec, _ := store.UpdateTask(storeTestSession, rec.ID, UpdateTaskOptions{
		MetadataPatch: map[string]any{"a": 10, "b": nil},
	})
	if newRec == nil {
		t.Fatal("UpdateTask returned nil")
	}
	if len(newRec.Metadata) != 1 {
		t.Fatalf("metadata = %v, want {a: 10}", newRec.Metadata)
	}
	if got, _ := newRec.Metadata["a"].(int); got != 10 {
		t.Fatalf("metadata[a] = %v, want 10", newRec.Metadata["a"])
	}
}

// Parity: framework/tasks/tests/test_store.py::test_blocks_bidirectional
func TestBlocksBidirectional(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	a := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "a"})
	b := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "b"})
	newA, _ := store.UpdateTask(storeTestSession, a.ID, UpdateTaskOptions{AddBlocks: []int{b.ID}})
	if newA == nil || !containsInt(newA.Blocks, b.ID) {
		t.Fatalf("a.blocks = %v, want %d", newA, b.ID)
	}
	fetchedB := store.GetTask(storeTestSession, b.ID)
	if fetchedB == nil || !containsInt(fetchedB.BlockedBy, a.ID) {
		t.Fatalf("b.blocked_by = %v, want %d", fetchedB, a.ID)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_blocked_by_bidirectional
func TestBlockedByBidirectional(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	a := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "a"})
	b := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "b"})
	newB, _ := store.UpdateTask(storeTestSession, b.ID, UpdateTaskOptions{AddBlockedBy: []int{a.ID}})
	if newB == nil || !containsInt(newB.BlockedBy, a.ID) {
		t.Fatalf("b.blocked_by = %v, want %d", newB, a.ID)
	}
	fetchedA := store.GetTask(storeTestSession, a.ID)
	if fetchedA == nil || !containsInt(fetchedA.Blocks, b.ID) {
		t.Fatalf("a.blocks = %v, want %d", fetchedA, b.ID)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_delete_increments_highwatermark
func TestDeleteIncrementsHighwatermark(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "a"})
	b := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "b"})
	deleted, _, err := store.DeleteTask(storeTestSession, b.ID)
	if err != nil || !deleted {
		t.Fatalf("DeleteTask = %v, %v", deleted, err)
	}
	newRec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "c"})
	if newRec.ID != b.ID+1 {
		t.Fatalf("new id = %d, want %d (deleted ids must never be reused)", newRec.ID, b.ID+1)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_delete_cascades_blocks
func TestDeleteCascadesBlocks(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	a := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "a"})
	b := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "b"})
	c := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "c"})
	store.UpdateTask(storeTestSession, a.ID, UpdateTaskOptions{AddBlocks: []int{b.ID}})
	store.UpdateTask(storeTestSession, c.ID, UpdateTaskOptions{AddBlockedBy: []int{b.ID}})
	_, cascade, err := store.DeleteTask(storeTestSession, b.ID)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if len(cascade) != 2 || !containsInt(cascade, a.ID) || !containsInt(cascade, c.ID) {
		t.Fatalf("cascade = %v, want [%d %d]", cascade, a.ID, c.ID)
	}
	fetchedA := store.GetTask(storeTestSession, a.ID)
	fetchedC := store.GetTask(storeTestSession, c.ID)
	if containsInt(fetchedA.Blocks, b.ID) {
		t.Fatalf("a.blocks still has %d", b.ID)
	}
	if containsInt(fetchedC.BlockedBy, b.ID) {
		t.Fatalf("c.blocked_by still has %d", b.ID)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_delete_missing_returns_false
func TestDeleteMissingReturnsFalse(t *testing.T) {
	store, _ := newStoreFixture(t)
	deleted, cascade, err := store.DeleteTask(storeTestSession, 42)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if deleted {
		t.Fatal("deleted = true, want false")
	}
	if len(cascade) != 0 {
		t.Fatalf("cascade = %v, want empty", cascade)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_reset_preserves_floor
func TestResetPreservesFloor(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	for i := 0; i < 5; i++ {
		mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "x"})
	}
	if err := store.ResetTaskList(storeTestSession); err != nil {
		t.Fatalf("ResetTaskList: %v", err)
	}
	newRec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "post-reset"})
	if newRec.ID != 6 {
		t.Fatalf("id = %d, want 6", newRec.ID)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_archiving_stamps_batch_markers
func TestArchivingStampsBatchMarkers(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	goal := "Grow pipeline"
	if _, err := store.CreateTasksBatch(storeTestSession, []TaskSpec{{Subject: "x"}}, &goal); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := TaskStatusCompleted
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &completed})
	archived := TaskStatusArchived
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &archived})

	records := store.ListTasks(storeTestSession)
	if len(records) != 1 {
		t.Fatalf("len = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.Status != TaskStatusArchived {
		t.Fatalf("status = %q", rec.Status)
	}
	if got, _ := rec.Metadata["archived_from"].(string); got != "completed" {
		t.Fatalf("archived_from = %v", rec.Metadata["archived_from"])
	}
	if got, _ := rec.Metadata["archived_goal"].(string); got != "Grow pipeline" {
		t.Fatalf("archived_goal = %v", rec.Metadata["archived_goal"])
	}
	if _, ok := rec.Metadata["archived_at"].(float64); !ok {
		t.Fatalf("archived_at is not a float: %T", rec.Metadata["archived_at"])
	}
}

// Parity: framework/tasks/tests/test_store.py::test_unarchive_restores_prior_status_and_strips_markers
func TestUnarchiveRestoresPriorStatusAndStripsMarkers(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "was-done"})
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "was-pending"})
	completed := TaskStatusCompleted
	archived := TaskStatusArchived
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &completed})
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &archived})
	store.UpdateTask(storeTestSession, 2, UpdateTaskOptions{Status: &archived})

	restored, err := store.UnarchiveTasks(storeTestSession, []int{1, 2})
	if err != nil {
		t.Fatalf("UnarchiveTasks: %v", err)
	}
	if len(restored) != 2 || !containsInt(restored, 1) || !containsInt(restored, 2) {
		t.Fatalf("restored = %v", restored)
	}
	byID := map[int]*TaskRecord{}
	for _, r := range store.ListTasks(storeTestSession) {
		byID[r.ID] = r
	}
	if byID[1].Status != TaskStatusCompleted {
		t.Fatalf("task 1 status = %q, want completed", byID[1].Status)
	}
	if byID[2].Status != TaskStatusPending {
		t.Fatalf("task 2 status = %q, want pending", byID[2].Status)
	}
	if _, ok := byID[1].Metadata["archived_from"]; ok {
		t.Fatal("archived_from not stripped")
	}
	if _, ok := byID[1].Metadata["archived_at"]; ok {
		t.Fatal("archived_at not stripped")
	}
}

// Parity: framework/tasks/tests/test_store.py::test_unarchive_ignores_non_archived_ids
func TestUnarchiveIgnoresNonArchivedIDs(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "active"})
	restored, err := store.UnarchiveTasks(storeTestSession, []int{1, 999})
	if err != nil {
		t.Fatalf("UnarchiveTasks: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("restored = %v, want empty", restored)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_archive_completed_archives_only_completed
func TestArchiveCompletedArchivesOnlyCompleted(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	goal := "Ship it"
	_, err := store.CreateTasksBatch(storeTestSession, []TaskSpec{
		{Subject: "done-1"}, {Subject: "done-2"}, {Subject: "open"},
	}, &goal)
	if err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := TaskStatusCompleted
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &completed})
	store.UpdateTask(storeTestSession, 2, UpdateTaskOptions{Status: &completed})

	archived, err := store.ArchiveCompletedTasks(storeTestSession)
	if err != nil {
		t.Fatalf("ArchiveCompletedTasks: %v", err)
	}
	if len(archived) != 2 || !containsInt([]int{archived[0].ID, archived[1].ID}, 1) {
		t.Fatalf("archived = %v", archived)
	}
	byID := map[int]*TaskRecord{}
	for _, r := range store.ListTasks(storeTestSession) {
		byID[r.ID] = r
	}
	if byID[1].Status != TaskStatusArchived || byID[2].Status != TaskStatusArchived {
		t.Fatalf("statuses = %q, %q", byID[1].Status, byID[2].Status)
	}
	if byID[3].Status != TaskStatusPending {
		t.Fatalf("open status = %q, want pending", byID[3].Status)
	}
	if got, _ := byID[1].Metadata["archived_from"].(string); got != "completed" {
		t.Fatalf("archived_from = %v", byID[1].Metadata["archived_from"])
	}
	if got, _ := byID[1].Metadata["archived_goal"].(string); got != "Ship it" {
		t.Fatalf("archived_goal = %v", byID[1].Metadata["archived_goal"])
	}
	if _, ok := byID[1].Metadata["archived_at"].(float64); !ok {
		t.Fatalf("archived_at is not a float: %T", byID[1].Metadata["archived_at"])
	}
}

// Parity: framework/tasks/tests/test_store.py::test_archive_completed_is_noop_without_completed
func TestArchiveCompletedIsNoopWithoutCompleted(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "open"})
	inProgress := TaskStatusInProgress
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &inProgress})
	archived, err := store.ArchiveCompletedTasks(storeTestSession)
	if err != nil {
		t.Fatalf("ArchiveCompletedTasks: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("archived = %v, want empty", archived)
	}
	if got := store.GetTask(storeTestSession, 1); got.Status != TaskStatusInProgress {
		t.Fatalf("status = %q, want in_progress", got.Status)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_archive_completed_missing_list_returns_empty
func TestArchiveCompletedMissingListReturnsEmpty(t *testing.T) {
	store, _ := newStoreFixture(t)
	archived, err := store.ArchiveCompletedTasks(storeTestSession)
	if err != nil {
		t.Fatalf("ArchiveCompletedTasks: %v", err)
	}
	if len(archived) != 0 {
		t.Fatalf("archived = %v, want empty", archived)
	}
	if store.ListExists(storeTestSession) {
		t.Fatal("ListExists = true, want false")
	}
}

// Parity: framework/tasks/tests/test_store.py::test_archive_completed_second_call_is_noop_and_preserves_stamps
func TestArchiveCompletedSecondCallIsNoopAndPreservesStamps(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "done"})
	completed := TaskStatusCompleted
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &completed})

	first, err := store.ArchiveCompletedTasks(storeTestSession)
	if err != nil {
		t.Fatalf("first ArchiveCompletedTasks: %v", err)
	}
	if len(first) != 1 || first[0].ID != 1 {
		t.Fatalf("first = %v", first)
	}
	stamp := store.GetTask(storeTestSession, 1).Metadata["archived_at"]

	second, err := store.ArchiveCompletedTasks(storeTestSession)
	if err != nil {
		t.Fatalf("second ArchiveCompletedTasks: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second = %v, want empty", second)
	}
	after := store.GetTask(storeTestSession, 1)
	if after.Status != TaskStatusArchived {
		t.Fatalf("status = %q", after.Status)
	}
	if after.Metadata["archived_at"] != stamp {
		t.Fatalf("stamp changed: %v -> %v", stamp, after.Metadata["archived_at"])
	}
}

// Parity: framework/tasks/tests/test_store.py::test_archive_completed_then_unarchive_restores_completed
func TestArchiveCompletedThenUnarchiveRestoresCompleted(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	goal := "Ship it"
	if _, err := store.CreateTasksBatch(storeTestSession, []TaskSpec{
		{Subject: "done"}, {Subject: "open"},
	}, &goal); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	completed := TaskStatusCompleted
	store.UpdateTask(storeTestSession, 1, UpdateTaskOptions{Status: &completed})
	archived, err := store.ArchiveCompletedTasks(storeTestSession)
	if err != nil {
		t.Fatalf("ArchiveCompletedTasks: %v", err)
	}
	if len(archived) != 1 || archived[0].ID != 1 {
		t.Fatalf("archived = %v", archived)
	}
	restored, err := store.UnarchiveTasks(storeTestSession, []int{1})
	if err != nil {
		t.Fatalf("UnarchiveTasks: %v", err)
	}
	if len(restored) != 1 || restored[0] != 1 {
		t.Fatalf("restored = %v", restored)
	}
	rec := store.GetTask(storeTestSession, 1)
	if rec.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", rec.Status)
	}
	if _, ok := rec.Metadata["archived_from"]; ok {
		t.Fatal("archived_from not stripped")
	}
	if _, ok := rec.Metadata["archived_at"]; ok {
		t.Fatal("archived_at not stripped")
	}
}

// Parity: framework/tasks/tests/test_store.py::test_claim_ok
func TestClaimOk(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "x"})
	result := store.ClaimTaskWithBusyCheck(storeTestSession, rec.ID, "agent_a")
	if result.Kind != "ok" {
		t.Fatalf("kind = %q, want ok", result.Kind)
	}
	if result.Record == nil || result.Record.Owner == nil || *result.Record.Owner != "agent_a" {
		t.Fatalf("owner = %v", result.Record)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_claim_already_owned
func TestClaimAlreadyOwned(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	owner := "agent_a"
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "x", Owner: &owner})
	result := store.ClaimTaskWithBusyCheck(storeTestSession, rec.ID, "agent_b")
	if result.Kind != "already_owned" {
		t.Fatalf("kind = %q, want already_owned", result.Kind)
	}
	if result.By != "agent_a" {
		t.Fatalf("by = %q", result.By)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_claim_not_found
func TestClaimNotFound(t *testing.T) {
	store, _ := newStoreFixture(t)
	result := store.ClaimTaskWithBusyCheck(storeTestSession, 999, "agent_a")
	if result.Kind != "not_found" {
		t.Fatalf("kind = %q, want not_found", result.Kind)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_claim_blocked
func TestClaimBlocked(t *testing.T) {
	store, _ := newStoreFixture(t)
	mustEnsure(t, store, storeTestSession)
	a := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "prereq"})
	b := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "dep"})
	store.UpdateTask(storeTestSession, b.ID, UpdateTaskOptions{AddBlockedBy: []int{a.ID}})
	result := store.ClaimTaskWithBusyCheck(storeTestSession, b.ID, "agent_a")
	if result.Kind != "blocked" {
		t.Fatalf("kind = %q, want blocked", result.Kind)
	}
	if !containsInt(result.BlockedBy, a.ID) {
		t.Fatalf("blocked_by = %v, want %d", result.BlockedBy, a.ID)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_ensure_task_list_idempotent
func TestEnsureTaskListIdempotent(t *testing.T) {
	store, _ := newStoreFixture(t)
	m1 := mustEnsure(t, store, storeTestSession)
	m2 := mustEnsure(t, store, storeTestSession)
	if m1.CreatedAt != m2.CreatedAt {
		t.Fatalf("created_at changed: %v -> %v", m1.CreatedAt, m2.CreatedAt)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_fallback_path_for_unknown_session
func TestFallbackPathForUnknownSession(t *testing.T) {
	store, root := newStoreFixture(t)
	mustEnsure(t, store, "free_floating_session")
	want := filepath.Join(root, "_misc", "free_floating_session", "tasks.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected %s: %v", want, err)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_canonical_queen_session_dir_wins
func TestCanonicalQueenSessionDirWins(t *testing.T) {
	store, root := newStoreFixture(t)
	sid := "session_20260429_test"
	canonical := filepath.Join(root, "queens", "queen_growth", "sessions", sid)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	mustEnsure(t, store, sid)
	rec := mustCreate(t, store, sid, CreateTaskSpec{Subject: "hello"})

	if _, err := os.Stat(filepath.Join(canonical, "tasks.json")); err != nil {
		t.Fatalf("canonical tasks.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "_misc", sid, "tasks.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected fallback tasks.json: %v", err)
	}
	fetched := store.ListTasks(sid)
	if len(fetched) != 1 || fetched[0].ID != rec.ID {
		t.Fatalf("fetched = %v", fetched)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_colony_queen_session_dir_wins
func TestColonyQueenSessionDirWins(t *testing.T) {
	store, root := newStoreFixture(t)
	sid := "session_20260429_overseer"
	canonical := filepath.Join(root, "colonies", "alpha", "queens", "queen_growth", "sessions", sid)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	mustEnsure(t, store, sid)
	rec := mustCreate(t, store, sid, CreateTaskSpec{Subject: "oversee me"})

	if _, err := os.Stat(filepath.Join(canonical, "tasks.json")); err != nil {
		t.Fatalf("canonical tasks.json missing: %v", err)
	}
	fetched := store.ListTasks(sid)
	if len(fetched) != 1 || fetched[0].ID != rec.ID {
		t.Fatalf("fetched = %v", fetched)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_colony_worker_session_dir_wins
func TestColonyWorkerSessionDirWins(t *testing.T) {
	store, root := newStoreFixture(t)
	sid := "session_20260513_worker"
	canonical := filepath.Join(root, "colonies", "alpha", "workers", sid)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(canonical, "events.jsonl"), nil, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	mustEnsure(t, store, sid)
	rec := mustCreate(t, store, sid, CreateTaskSpec{Subject: "worker work"})

	if _, err := os.Stat(filepath.Join(canonical, "tasks.json")); err != nil {
		t.Fatalf("canonical tasks.json missing: %v", err)
	}
	fetched := store.ListTasks(sid)
	if len(fetched) != 1 || fetched[0].ID != rec.ID {
		t.Fatalf("fetched = %v", fetched)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_session_dir_cache_avoids_rescan
func TestSessionDirCacheAvoidsRescan(t *testing.T) {
	root := t.TempDir()
	sid := "session_20260101_000000_abc"
	canonical := filepath.Join(root, "queens", "vision_queen", "sessions", sid)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	orig := findSessionDirFn
	calls := 0
	findSessionDirFn = func(sessionID, hiveRoot string) (string, bool) {
		calls++
		return findSessionDir(sessionID, hiveRoot)
	}
	t.Cleanup(func() { findSessionDirFn = orig })
	ResetSessionDirCache()
	t.Cleanup(ResetSessionDirCache)

	first, err := SessionStorageDir(sid, root)
	if err != nil {
		t.Fatalf("SessionStorageDir: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := SessionStorageDir(sid, root)
		if err != nil {
			t.Fatalf("SessionStorageDir: %v", err)
		}
		if got != canonical {
			t.Fatalf("got %q, want %q", got, canonical)
		}
	}
	if first != canonical {
		t.Fatalf("first = %q, want %q", first, canonical)
	}
	if calls != 1 {
		t.Fatalf("scanned %d times, want 1", calls)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_session_dir_cache_skips_negative
func TestSessionDirCacheSkipsNegative(t *testing.T) {
	root := t.TempDir()
	sid := "session_20260101_000001_def"
	ResetSessionDirCache()
	t.Cleanup(ResetSessionDirCache)

	got, err := SessionStorageDir(sid, root)
	if err != nil {
		t.Fatalf("SessionStorageDir: %v", err)
	}
	if want := filepath.Join(root, "_misc", sid); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	canonical := filepath.Join(root, "queens", "q", "sessions", sid)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err = SessionStorageDir(sid, root)
	if err != nil {
		t.Fatalf("SessionStorageDir: %v", err)
	}
	if got != canonical {
		t.Fatalf("got %q, want %q", got, canonical)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_session_dir_cache_invalidates_on_delete
func TestSessionDirCacheInvalidatesOnDelete(t *testing.T) {
	root := t.TempDir()
	sid := "session_20260101_000002_ghi"
	ResetSessionDirCache()
	t.Cleanup(ResetSessionDirCache)
	canonical := filepath.Join(root, "queens", "q", "sessions", sid)
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := SessionStorageDir(sid, root)
	if err != nil {
		t.Fatalf("SessionStorageDir: %v", err)
	}
	if got != canonical {
		t.Fatalf("got %q, want %q", got, canonical)
	}

	if err := os.RemoveAll(canonical); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	got, err = SessionStorageDir(sid, root)
	if err != nil {
		t.Fatalf("SessionStorageDir: %v", err)
	}
	if want := filepath.Join(root, "_misc", sid); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_set_goal_before_any_tasks
func TestSetGoalBeforeAnyTasks(t *testing.T) {
	store, _ := newStoreFixture(t)
	if err := store.SetGoal(storeTestSession, "Checking 20 Instagram profiles"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	meta := store.GetMeta(storeTestSession)
	if meta == nil || meta.Goal == nil || *meta.Goal != "Checking 20 Instagram profiles" {
		t.Fatalf("meta = %v", meta)
	}
	if tasks := store.ListTasks(storeTestSession); len(tasks) != 0 {
		t.Fatalf("tasks = %v, want empty", tasks)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_task_create_without_goal_keeps_seeded_goal
func TestTaskCreateWithoutGoalKeepsSeededGoal(t *testing.T) {
	store, _ := newStoreFixture(t)
	if err := store.SetGoal(storeTestSession, "Checking 20 Instagram profiles"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := store.CreateTasksBatch(storeTestSession, []TaskSpec{{Subject: "triage batch"}}, nil); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	meta := store.GetMeta(storeTestSession)
	if meta == nil || meta.Goal == nil || *meta.Goal != "Checking 20 Instagram profiles" {
		t.Fatalf("meta = %v", meta)
	}
}

// Parity: framework/tasks/tests/test_store.py::test_task_create_with_goal_overwrites_seeded_goal
func TestTaskCreateWithGoalOverwritesSeededGoal(t *testing.T) {
	store, _ := newStoreFixture(t)
	if err := store.SetGoal(storeTestSession, "Seeded by queen"); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	goal := "Refined by worker"
	if _, err := store.CreateTasksBatch(storeTestSession, []TaskSpec{{Subject: "t"}}, &goal); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	meta := store.GetMeta(storeTestSession)
	if meta == nil || meta.Goal == nil || *meta.Goal != "Refined by worker" {
		t.Fatalf("meta = %v", meta)
	}
}

// Parity: core/framework/tasks/tools/session_tools.py::_make_update_executor —
// an invalid TaskStatus raises before the store is touched. The tool layer
// rejects it first; this store guard is the defence for internal callers that
// bypass the tool layer: drop only the invalid status patch, keep the valid
// fields, and never write a document pydantic would read as corrupt.
func TestUpdateDropsInvalidStatusButKeepsOtherFields(t *testing.T) {
	store, _ := newStoreFixture(t)
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "original"})

	subject := "renamed"
	bogus := TaskStatus("bogus")
	updated, fields := store.UpdateTask(storeTestSession, rec.ID, UpdateTaskOptions{
		Subject: &subject,
		Status:  &bogus,
	})
	if updated == nil {
		t.Fatal("UpdateTask returned nil for a valid subject patch")
	}
	if updated.Subject != subject {
		t.Fatalf("subject = %q, want %q", updated.Subject, subject)
	}
	if updated.Status != TaskStatusPending {
		t.Fatalf("status = %q, want pending", updated.Status)
	}
	if containsString(fields, "status") {
		t.Fatalf("fields = %v, status must not be reported as changed", fields)
	}
	if !containsString(fields, "subject") {
		t.Fatalf("fields = %v, want subject", fields)
	}

	// The document stays readable and every persisted status is valid.
	doc := store.readDoc(storeTestSession)
	if doc == nil {
		t.Fatal("tasks.json unreadable after the rejected status patch")
	}
	for _, r := range doc.Tasks {
		if !ValidTaskStatus(string(r.Status)) {
			t.Fatalf("task #%d persisted invalid status %q", r.ID, r.Status)
		}
	}

	// A subsequent valid status still writes — the guard only drops the
	// invalid value.
	completed := TaskStatusCompleted
	updated, fields = store.UpdateTask(storeTestSession, rec.ID, UpdateTaskOptions{Status: &completed})
	if updated == nil || updated.Status != TaskStatusCompleted {
		t.Fatalf("valid status update = %#v", updated)
	}
	if !containsString(fields, "status") {
		t.Fatalf("fields = %v, want status", fields)
	}
	if got := store.GetTask(storeTestSession, rec.ID); got == nil || got.Status != TaskStatusCompleted {
		t.Fatalf("reloaded = %#v, want completed", got)
	}
}

// Parity: core/framework/tasks/store.py::_read_doc_sync — TaskListDocument.
// model_validate_json rejects an unknown TaskStatus, the reader logs the file
// as corrupt and returns None, and every task in the list disappears. P0-10
// prevents new invalid values at write time; this pins the legacy-file read
// semantics so Go and Python agree on an already-corrupt document instead of
// Go silently listing a record Python cannot deserialize.
func TestReadDocTreatsLegacyInvalidStatusAsCorrupt(t *testing.T) {
	store, _ := newStoreFixture(t)
	rec := mustCreate(t, store, storeTestSession, CreateTaskSpec{Subject: "legacy"})

	path, err := store.DocPath(storeTestSession)
	if err != nil {
		t.Fatalf("DocPath: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tasks.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode tasks.json: %v", err)
	}
	tasks, _ := doc["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %#v", doc["tasks"])
	}
	record, _ := tasks[0].(map[string]any)
	record["status"] = "bogus"
	patched, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode tasks.json: %v", err)
	}
	if err := os.WriteFile(path, patched, 0o600); err != nil {
		t.Fatalf("write tasks.json: %v", err)
	}

	if got := store.GetTask(storeTestSession, rec.ID); got != nil {
		t.Fatalf("GetTask returned %#v for a Python-corrupt document", got)
	}
	if rows := store.ListTasks(storeTestSession); len(rows) != 0 {
		t.Fatalf("ListTasks = %#v, want empty (corrupt document)", rows)
	}
}
