package testframework

// Parity: core/framework/testing/test_storage.py::TestStorage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

// TestStorage is the file-based store for tests and results. Layout (upstream):
//
//	{base}/
//	  tests/{goal_id}/{test_id}.json
//	  indexes/by_goal/{goal_id}.json
//	  indexes/by_approval/{status}.json
//	  indexes/by_type/{test_type}.json
//	  indexes/by_criteria/{criteria_id}.json
//	  results/{test_id}/{timestamp}.json
//	  results/{test_id}/latest.json
//	  suites/{goal_id}_suite.json
//
// Upstream has no locking; the Go port serializes every read-modify-write so
// concurrent callers cannot lose an index entry or interleave a partial file.
// Files are written through internal/atomicfs so readers never observe a
// half-written document.
type TestStorage struct {
	basePath string
	mu       sync.RWMutex
	// Now is the clock seam used for UpdatedAt on Update.
	Now func() time.Time
	// Perm is the file mode for written JSON documents.
	Perm os.FileMode
}

// NewTestStorage creates the directory tree and returns a store rooted at base.
func NewTestStorage(base string) (*TestStorage, error) {
	store := &TestStorage{basePath: base, Now: Now, Perm: 0o644}
	if err := store.ensureDirs(); err != nil {
		return nil, err
	}
	return store, nil
}

// BasePath returns the storage root.
func (s *TestStorage) BasePath() string { return s.basePath }

func (s *TestStorage) ensureDirs() error {
	for _, dir := range []string{
		filepath.Join(s.basePath, "tests"),
		filepath.Join(s.basePath, "indexes", "by_goal"),
		filepath.Join(s.basePath, "indexes", "by_approval"),
		filepath.Join(s.basePath, "indexes", "by_type"),
		filepath.Join(s.basePath, "indexes", "by_criteria"),
		filepath.Join(s.basePath, "results"),
		filepath.Join(s.basePath, "suites"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("storage: mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// === TEST OPERATIONS ===

// SaveTest writes the full test document and refreshes its index entries.
func (s *TestStorage) SaveTest(test *Test) error {
	if test == nil {
		return errors.New("storage: save test: nil test")
	}
	if err := test.Validate(); err != nil {
		return fmt.Errorf("storage: save test %q: %w", test.ID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveTestLocked(test)
}

func (s *TestStorage) saveTestLocked(test *Test) error {
	goalDir := filepath.Join(s.basePath, "tests", test.GoalID)
	if err := os.MkdirAll(goalDir, 0o755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", goalDir, err)
	}
	if err := s.writeJSON(filepath.Join(goalDir, test.ID+".json"), test); err != nil {
		return err
	}
	if err := s.addToIndexLocked("by_goal", test.GoalID, test.ID); err != nil {
		return err
	}
	if err := s.addToIndexLocked("by_approval", string(test.ApprovalStatus), test.ID); err != nil {
		return err
	}
	if err := s.addToIndexLocked("by_type", string(test.TestType), test.ID); err != nil {
		return err
	}
	return s.addToIndexLocked("by_criteria", test.ParentCriteriaID, test.ID)
}

// LoadTest loads a test document, returning (nil, nil) when it does not exist.
func (s *TestStorage) LoadTest(goalID, testID string) (*Test, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadTestLocked(goalID, testID)
}

func (s *TestStorage) loadTestLocked(goalID, testID string) (*Test, error) {
	path := filepath.Join(s.basePath, "tests", goalID, testID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: load test %s/%s: %w", goalID, testID, err)
	}
	var test Test
	if err := json.Unmarshal(data, &test); err != nil {
		return nil, fmt.Errorf("storage: load test %s/%s: %w", goalID, testID, err)
	}
	return &test, nil
}

// DeleteTest removes a test, its index entries, and its result history. It
// returns false when the test does not exist.
func (s *TestStorage) DeleteTest(goalID, testID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.basePath, "tests", goalID, testID+".json")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("storage: delete test %s/%s: %w", goalID, testID, err)
	}
	test, err := s.loadTestLocked(goalID, testID)
	if err != nil {
		return false, err
	}
	if test != nil {
		for _, entry := range []struct{ index, key string }{
			{"by_goal", test.GoalID},
			{"by_approval", string(test.ApprovalStatus)},
			{"by_type", string(test.TestType)},
			{"by_criteria", test.ParentCriteriaID},
		} {
			if err := s.removeFromIndexLocked(entry.index, entry.key, testID); err != nil {
				return false, err
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("storage: delete test %s/%s: %w", goalID, testID, err)
	}
	if err := os.RemoveAll(filepath.Join(s.basePath, "results", testID)); err != nil {
		return false, fmt.Errorf("storage: delete results for %s: %w", testID, err)
	}
	return true, nil
}

// UpdateTest rewrites an existing test, moving its approval-index entry when
// the status changed and refreshing UpdatedAt. An update for a test that is
// not on disk creates it (upstream loads None and saves).
func (s *TestStorage) UpdateTest(test *Test) error {
	if test == nil {
		return errors.New("storage: update test: nil test")
	}
	if err := test.Validate(); err != nil {
		return fmt.Errorf("storage: update test %q: %w", test.ID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldTest, err := s.loadTestLocked(test.GoalID, test.ID)
	if err != nil {
		return err
	}
	if oldTest != nil && oldTest.ApprovalStatus != test.ApprovalStatus {
		if err := s.removeFromIndexLocked("by_approval", string(oldTest.ApprovalStatus), test.ID); err != nil {
			return err
		}
		if err := s.addToIndexLocked("by_approval", string(test.ApprovalStatus), test.ID); err != nil {
			return err
		}
	}
	test.UpdatedAt = Timestamp{s.now()}
	return s.saveTestLocked(test)
}

// === QUERY OPERATIONS ===

// GetTestsByGoal returns every test registered for the goal.
func (s *TestStorage) GetTestsByGoal(goalID string) ([]*Test, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids, err := s.getIndexLocked("by_goal", goalID)
	if err != nil {
		return nil, err
	}
	tests := make([]*Test, 0, len(ids))
	for _, testID := range ids {
		test, err := s.loadTestLocked(goalID, testID)
		if err != nil {
			return nil, err
		}
		if test != nil {
			tests = append(tests, test)
		}
	}
	return tests, nil
}

// GetTestsByApprovalStatus returns test IDs with the given approval status.
func (s *TestStorage) GetTestsByApprovalStatus(status ApprovalStatus) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getIndexLocked("by_approval", string(status))
}

// GetTestsByType returns test IDs with the given test type.
func (s *TestStorage) GetTestsByType(testType TestType) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getIndexLocked("by_type", string(testType))
}

// GetTestsByCriteria returns test IDs linked to the parent criterion.
func (s *TestStorage) GetTestsByCriteria(criteriaID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getIndexLocked("by_criteria", criteriaID)
}

// GetPendingTests returns the goal's tests still awaiting review.
func (s *TestStorage) GetPendingTests(goalID string) ([]*Test, error) {
	tests, err := s.GetTestsByGoal(goalID)
	if err != nil {
		return nil, err
	}
	pending := make([]*Test, 0, len(tests))
	for _, test := range tests {
		if test.ApprovalStatus == ApprovalPending {
			pending = append(pending, test)
		}
	}
	return pending, nil
}

// GetApprovedTests returns the goal's approved-or-modified tests.
func (s *TestStorage) GetApprovedTests(goalID string) ([]*Test, error) {
	tests, err := s.GetTestsByGoal(goalID)
	if err != nil {
		return nil, err
	}
	approved := make([]*Test, 0, len(tests))
	for _, test := range tests {
		if test.IsApproved() {
			approved = append(approved, test)
		}
	}
	return approved, nil
}

// ListAllGoals lists goal IDs that have at least one indexed test.
func (s *TestStorage) ListAllGoals() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dir := filepath.Join(s.basePath, "indexes", "by_goal")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("storage: list goals: %w", err)
	}
	goals := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		goals = append(goals, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(goals)
	return goals, nil
}

// === RESULT OPERATIONS ===

// SaveResult writes a timestamped result and refreshes latest.json.
func (s *TestStorage) SaveResult(testID string, result *TestResult) error {
	if result == nil {
		return errors.New("storage: save result: nil result")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.basePath, "results", testID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", dir, err)
	}
	stamp := result.Timestamp.Format("20060102_150405")
	if err := s.writeJSON(filepath.Join(dir, stamp+".json"), result); err != nil {
		return err
	}
	return s.writeJSON(filepath.Join(dir, "latest.json"), result)
}

// GetLatestResult returns the most recent result, or (nil, nil) when none.
func (s *TestStorage) GetLatestResult(testID string) (*TestResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	path := filepath.Join(s.basePath, "results", testID, "latest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("storage: latest result %s: %w", testID, err)
	}
	var result TestResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("storage: latest result %s: %w", testID, err)
	}
	return &result, nil
}

// GetResultHistory returns the newest results first, excluding latest.json.
// A negative limit keeps Python's slice semantics (all but the last |limit|).
func (s *TestStorage) GetResultHistory(testID string, limit int) ([]TestResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dir := filepath.Join(s.basePath, "results", testID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []TestResult{}, nil
		}
		return nil, fmt.Errorf("storage: result history %s: %w", testID, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == "latest.json" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if limit < 0 {
		end := len(names) + limit
		if end < 0 {
			end = 0
		}
		names = names[:end]
	} else if limit < len(names) {
		names = names[:limit]
	}
	results := make([]TestResult, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("storage: result history %s/%s: %w", testID, name, err)
		}
		var result TestResult
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("storage: result history %s/%s: %w", testID, name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *TestStorage) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return Now()
}

// === INDEX OPERATIONS ===

func (s *TestStorage) indexPath(indexType, key string) string {
	return filepath.Join(s.basePath, "indexes", indexType, key+".json")
}

func (s *TestStorage) getIndexLocked(indexType, key string) ([]string, error) {
	data, err := os.ReadFile(s.indexPath(indexType, key))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("storage: index %s/%s: %w", indexType, key, err)
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("storage: index %s/%s: %w", indexType, key, err)
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func (s *TestStorage) addToIndexLocked(indexType, key, value string) error {
	values, err := s.getIndexLocked(indexType, key)
	if err != nil {
		return err
	}
	for _, existing := range values {
		if existing == value {
			return nil
		}
	}
	values = append(values, value)
	return s.writeIndexLocked(indexType, key, values)
}

func (s *TestStorage) removeFromIndexLocked(indexType, key, value string) error {
	values, err := s.getIndexLocked(indexType, key)
	if err != nil {
		return err
	}
	for index, existing := range values {
		if existing != value {
			continue
		}
		values = append(values[:index], values[index+1:]...)
		return s.writeIndexLocked(indexType, key, values)
	}
	return nil
}

// writeIndexLocked writes the index with Python's json.dump default
// separators (", " between items, no trailing newline) so a Python reader sees
// the same document shape.
func (s *TestStorage) writeIndexLocked(indexType, key string, values []string) error {
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("storage: index %s/%s: %w", indexType, key, err)
		}
		encoded = append(encoded, string(raw))
	}
	data := []byte("[" + strings.Join(encoded, ", ") + "]")
	path := s.indexPath(indexType, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", filepath.Dir(path), err)
	}
	return atomicfs.WriteFile(path, data, s.Perm)
}

// === UTILITY ===

// StorageStats is the shape of GetStats.
type StorageStats struct {
	TotalGoals  int            `json:"total_goals"`
	TotalTests  int            `json:"total_tests"`
	ByApproval  map[string]int `json:"by_approval"`
	StoragePath string         `json:"storage_path"`
}

// GetStats summarizes the store.
func (s *TestStorage) GetStats() (*StorageStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	dir := filepath.Join(s.basePath, "indexes", "by_goal")
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("storage: list goals: %w", err)
	}
	goals := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		goals = append(goals, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(goals)
	byApproval := map[string]int{}
	total := 0
	for _, goal := range goals {
		ids, err := s.getIndexLocked("by_goal", goal)
		if err != nil {
			return nil, err
		}
		total += len(ids)
	}
	for _, status := range ApprovalStatuses {
		ids, err := s.getIndexLocked("by_approval", string(status))
		if err != nil {
			return nil, err
		}
		byApproval[string(status)] = len(ids)
	}
	return &StorageStats{
		TotalGoals:  len(goals),
		TotalTests:  total,
		ByApproval:  byApproval,
		StoragePath: s.basePath,
	}, nil
}

func (s *TestStorage) writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("storage: mkdir %s: %w", filepath.Dir(path), err)
	}
	return atomicfs.WriteFile(path, data, s.Perm)
}
