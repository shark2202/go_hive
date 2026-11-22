package storage

import (
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

func createTestRun(runID, goalID string, status schemas.RunStatus, nodes []string) *schemas.Run {
	if runID == "" {
		runID = "test_run_1"
	}
	if goalID == "" {
		goalID = "test_goal"
	}
	if status == "" {
		status = schemas.RunCompleted
	}
	if len(nodes) == 0 {
		nodes = []string{"node_1"}
	}
	return &schemas.Run{
		ID:     runID,
		GoalID: goalID,
		Status: status,
		Metrics: schemas.RunMetrics{
			TotalDecisions:      1,
			SuccessfulDecisions: 1,
			FailedDecisions:     0,
			NodesExecuted:       nodes,
		},
		Narrative: "Test run completed.",
	}
}

// Parity: tests/test_storage.py::TestCacheEntry::test_is_expired_false_when_fresh
func TestCacheEntryIsExpiredFalseWhenFresh(t *testing.T) {
	entry := CacheEntry{Value: "test", Timestamp: nowSeconds()}
	if entry.IsExpired(60.0) {
		t.Fatal("fresh entry reported expired")
	}
}

// Parity: tests/test_storage.py::TestCacheEntry::test_is_expired_true_when_old
func TestCacheEntryIsExpiredTrueWhenOld(t *testing.T) {
	entry := CacheEntry{Value: "test", Timestamp: nowSeconds() - 120}
	if !entry.IsExpired(60.0) {
		t.Fatal("old entry reported fresh")
	}
}

// Parity: tests/test_storage.py::TestConcurrentStorageBasics::test_init
func TestConcurrentStorageInit(t *testing.T) {
	dir := t.TempDir()
	store := NewConcurrentStorage(dir, 60.0, 0.1, 100, 1000)
	if store.BasePath != dir {
		t.Fatalf("base path = %s, want %s", store.BasePath, dir)
	}
	if store.Running() {
		t.Fatal("new storage reports running")
	}
}

// Parity: tests/test_storage.py::TestConcurrentStorageBasics::test_start_and_stop
func TestConcurrentStorageStartAndStop(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	store.Start()
	if !store.Running() {
		t.Fatal("storage not running after Start")
	}
	if store.stopCh == nil || store.doneCh == nil {
		t.Fatal("batch writer channels were not created")
	}
	store.Stop()
	if store.Running() {
		t.Fatal("storage still running after Stop")
	}
}

// Parity: tests/test_storage.py::TestConcurrentStorageBasics::test_double_start_is_idempotent
func TestConcurrentStorageDoubleStartIsIdempotent(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	store.Start()
	store.Start()
	if !store.Running() {
		t.Fatal("storage stopped after second Start")
	}
	store.Stop()
}

// Parity: tests/test_storage.py::TestConcurrentStorageBasics::test_double_stop_is_idempotent
func TestConcurrentStorageDoubleStopIsIdempotent(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	store.Start()
	store.Stop()
	store.Stop()
	if store.Running() {
		t.Fatal("storage still running after double Stop")
	}
}

// Parity: tests/test_storage.py::TestConcurrentStorageCacheManagement::test_clear_cache
func TestConcurrentStorageClearCache(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	store.cache["test_key"] = CacheEntry{Value: "test", Timestamp: nowSeconds()}

	store.ClearCache()

	if len(store.cache) != 0 {
		t.Fatalf("cache not cleared: %d entries", len(store.cache))
	}
}

// Parity: tests/test_storage.py::TestConcurrentStorageCacheManagement::test_invalidate_cache
func TestConcurrentStorageInvalidateCache(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	store.cache["key1"] = CacheEntry{Value: "test1", Timestamp: nowSeconds()}
	store.cache["key2"] = CacheEntry{Value: "test2", Timestamp: nowSeconds()}

	store.InvalidateCache("key1")

	if _, ok := store.cache["key1"]; ok {
		t.Fatal("key1 still cached")
	}
	if _, ok := store.cache["key2"]; !ok {
		t.Fatal("key2 was dropped")
	}
}

// Parity: tests/test_storage.py::TestConcurrentStorageCacheManagement::test_get_cache_stats
func TestConcurrentStorageGetCacheStats(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	store.cache["fresh"] = CacheEntry{Value: "test", Timestamp: nowSeconds()}
	store.cache["expired"] = CacheEntry{Value: "test", Timestamp: nowSeconds() - 120}

	stats := store.GetCacheStats()

	if stats.TotalEntries != 2 {
		t.Fatalf("total = %d, want 2", stats.TotalEntries)
	}
	if stats.ExpiredEntries != 1 {
		t.Fatalf("expired = %d, want 1", stats.ExpiredEntries)
	}
	if stats.ValidEntries != 1 {
		t.Fatalf("valid = %d, want 1", stats.ValidEntries)
	}
}

// Parity-exempt: save/load batching is exercised indirectly above; sleeping on
func TestConcurrentStorageSaveRunRoundTrip(t *testing.T) {
	store := NewConcurrentStorage(t.TempDir(), 60.0, 0.1, 100, 1000)
	run := createTestRun("run_roundtrip", "", "", nil)
	if err := store.SaveRun(run, true); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.LoadRun("run_roundtrip", true)
	if err != nil || loaded == nil {
		t.Fatalf("load: %v, %v", loaded, err)
	}
	if loaded.ID != run.ID || loaded.Narrative != run.Narrative {
		t.Fatalf("loaded run mismatch: %+v", loaded)
	}
	time.Sleep(time.Millisecond)
	if _, err := store.DeleteRun("run_roundtrip"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}
