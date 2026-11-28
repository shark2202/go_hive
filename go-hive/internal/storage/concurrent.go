package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

type CacheEntry struct {
	Value     any
	Timestamp float64
}

func (e CacheEntry) IsExpired(ttl float64) bool {
	return nowSeconds()-e.Timestamp > ttl
}

type CacheStats struct {
	TotalEntries   int
	ExpiredEntries int
	ValidEntries   int
}

type StorageStats struct {
	TotalRuns     int
	StoragePath   string
	Cache         CacheStats
	PendingWrites int
	Running       bool
}

type InvalidKeyError struct {
	Message string
}

func (e *InvalidKeyError) Error() string { return e.Message }

type ConcurrentStorage struct {
	BasePath string

	mu         sync.Mutex
	cache      map[string]CacheEntry
	cacheTTL   float64
	pending    []*schemas.Run
	batchEvery time.Duration
	maxBatch   int
	running    bool
	stopCh     chan struct{}
	doneCh     chan struct{}
	wakeCh     chan struct{}

	ioMu sync.Mutex
}

func NewConcurrentStorage(basePath string, cacheTTL, batchInterval float64, maxBatchSize, maxLocks int) *ConcurrentStorage {
	if batchInterval <= 0 {
		batchInterval = 0.1
	}
	if maxBatchSize <= 0 {
		maxBatchSize = 100
	}
	return &ConcurrentStorage{
		BasePath:   filepath.Clean(basePath),
		cache:      map[string]CacheEntry{},
		cacheTTL:   cacheTTL,
		batchEvery: time.Duration(batchInterval * float64(time.Second)),
		maxBatch:   maxBatchSize,
		wakeCh:     make(chan struct{}, 1),
	}
}

func NewDefaultConcurrentStorage(basePath string) *ConcurrentStorage {
	return NewConcurrentStorage(basePath, 60.0, 0.1, 100, 1000)
}

func ValidateKey(key string) error {
	if key == "" || strings.TrimSpace(key) == "" {
		return &InvalidKeyError{Message: "Key cannot be empty"}
	}
	if strings.ContainsAny(key, `/\`) {
		return &InvalidKeyError{Message: "Invalid key format: path separators not allowed in '" + key + "'"}
	}
	if strings.Contains(key, "..") || strings.HasPrefix(key, ".") {
		return &InvalidKeyError{Message: "Invalid key format: path traversal detected in '" + key + "'"}
	}
	if strings.HasPrefix(key, "/") || (len(key) > 1 && key[1] == ':') {
		return &InvalidKeyError{Message: "Invalid key format: absolute paths not allowed in '" + key + "'"}
	}
	if strings.ContainsRune(key, '\x00') {
		return &InvalidKeyError{Message: "Invalid key format: null bytes not allowed"}
	}
	for _, ch := range "<>|&$`'\"" {
		if strings.ContainsRune(key, ch) {
			return &InvalidKeyError{Message: "Invalid key format: contains dangerous characters in '" + key + "'"}
		}
	}
	return nil
}

func (s *ConcurrentStorage) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	stop, done := s.stopCh, s.doneCh
	s.mu.Unlock()

	go func() {
		defer close(done)
		s.batchWriter(stop)
	}()
}

func (s *ConcurrentStorage) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	stop, done := s.stopCh, s.doneCh
	s.stopCh, s.doneCh = nil, nil
	s.mu.Unlock()

	close(stop)
	<-done
	s.flushPending()
}

func (s *ConcurrentStorage) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *ConcurrentStorage) batchWriter(stop <-chan struct{}) {
	ticker := time.NewTicker(s.batchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			s.flushPending()
			return
		case <-s.wakeCh:
			s.flushPending()
		case <-ticker.C:
			s.flushPending()
		}
	}
}

func (s *ConcurrentStorage) enqueue(run *schemas.Run) {
	s.mu.Lock()
	s.pending = append(s.pending, run)
	s.mu.Unlock()
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *ConcurrentStorage) flushPending() {
	s.mu.Lock()
	items := s.pending
	s.pending = nil
	s.mu.Unlock()

	for start := 0; start < len(items); start += s.maxBatch {
		end := start + s.maxBatch
		if end > len(items) {
			end = len(items)
		}
		for _, run := range items[start:end] {
			if err := s.saveRunLocked(run); err != nil {
				continue
			}
			s.mu.Lock()
			s.cache["run:"+run.ID] = CacheEntry{Value: run, Timestamp: nowSeconds()}
			s.mu.Unlock()
		}
	}
}

func (s *ConcurrentStorage) SaveRun(run *schemas.Run, immediate bool) error {
	s.mu.Lock()
	delete(s.cache, "summary:"+run.ID)
	running := s.running
	s.mu.Unlock()

	if immediate || !running {
		if err := s.saveRunLocked(run); err != nil {
			return err
		}
		s.mu.Lock()
		s.cache["run:"+run.ID] = CacheEntry{Value: run, Timestamp: nowSeconds()}
		s.mu.Unlock()
		return nil
	}
	s.enqueue(run)
	return nil
}

func (s *ConcurrentStorage) LoadRun(runID string, useCache bool) (*schemas.Run, error) {
	if err := ValidateKey(runID); err != nil {
		return nil, err
	}
	cacheKey := "run:" + runID
	if useCache {
		s.mu.Lock()
		cached, ok := s.cache[cacheKey]
		s.mu.Unlock()
		if ok && !cached.IsExpired(s.cacheTTL) {
			if run, ok := cached.Value.(*schemas.Run); ok {
				return run, nil
			}
		}
	}

	run, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, err
	}
	if run != nil {
		s.mu.Lock()
		s.cache[cacheKey] = CacheEntry{Value: run, Timestamp: nowSeconds()}
		s.mu.Unlock()
	}
	return run, nil
}

func (s *ConcurrentStorage) LoadSummary(runID string, useCache bool) (*schemas.RunSummary, error) {
	if err := ValidateKey(runID); err != nil {
		return nil, err
	}
	cacheKey := "summary:" + runID
	if useCache {
		s.mu.Lock()
		cached, ok := s.cache[cacheKey]
		s.mu.Unlock()
		if ok && !cached.IsExpired(s.cacheTTL) {
			if summary, ok := cached.Value.(*schemas.RunSummary); ok {
				return summary, nil
			}
		}
	}

	summary, err := s.loadSummaryLocked(runID)
	if err != nil {
		return nil, err
	}
	if summary != nil {
		s.mu.Lock()
		s.cache[cacheKey] = CacheEntry{Value: summary, Timestamp: nowSeconds()}
		s.mu.Unlock()
	}
	return summary, nil
}

func (s *ConcurrentStorage) DeleteRun(runID string) (bool, error) {
	if err := ValidateKey(runID); err != nil {
		return false, err
	}
	deleted, err := s.deleteRunLocked(runID)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	delete(s.cache, "run:"+runID)
	delete(s.cache, "summary:"+runID)
	s.mu.Unlock()
	return deleted, nil
}

func (s *ConcurrentStorage) ListAllRuns() ([]string, error) {
	return s.listAllRunsLocked()
}

func (s *ConcurrentStorage) ClearCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = map[string]CacheEntry{}
}

func (s *ConcurrentStorage) InvalidateCache(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, key)
}

func (s *ConcurrentStorage) GetCacheStats() CacheStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := 0
	for _, entry := range s.cache {
		if entry.IsExpired(s.cacheTTL) {
			expired++
		}
	}
	return CacheStats{
		TotalEntries:   len(s.cache),
		ExpiredEntries: expired,
		ValidEntries:   len(s.cache) - expired,
	}
}

func (s *ConcurrentStorage) GetStats() StorageStats {
	runs, _ := s.listAllRunsLocked()
	s.mu.Lock()
	pending := len(s.pending)
	running := s.running
	s.mu.Unlock()
	return StorageStats{
		TotalRuns:     len(runs),
		StoragePath:   s.BasePath,
		Cache:         s.GetCacheStats(),
		PendingWrites: pending,
		Running:       running,
	}
}

func (s *ConcurrentStorage) SaveRunSync(run *schemas.Run) error {
	if err := ValidateKey(run.ID); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, "summary:"+run.ID)
	s.mu.Unlock()
	if err := s.saveRunSync(run); err != nil {
		return err
	}
	s.mu.Lock()
	s.cache["run:"+run.ID] = CacheEntry{Value: run, Timestamp: nowSeconds()}
	s.mu.Unlock()
	return nil
}

func (s *ConcurrentStorage) LoadRunSync(runID string) (*schemas.Run, error) {
	if err := ValidateKey(runID); err != nil {
		return nil, err
	}
	return s.loadRunSync(runID)
}

func (s *ConcurrentStorage) saveRunLocked(run *schemas.Run) error {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.saveRunSync(run)
}

func (s *ConcurrentStorage) loadRunLocked(runID string) (*schemas.Run, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.loadRunSync(runID)
}

func (s *ConcurrentStorage) loadSummaryLocked(runID string) (*schemas.RunSummary, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.loadSummarySync(runID)
}

func (s *ConcurrentStorage) deleteRunLocked(runID string) (bool, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.deleteRunSync(runID)
}

func (s *ConcurrentStorage) listAllRunsLocked() ([]string, error) {
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.listAllRunsSync()
}

func (s *ConcurrentStorage) saveRunSync(run *schemas.Run) error {
	if err := ValidateKey(run.ID); err != nil {
		return err
	}
	runsDir := filepath.Join(s.BasePath, "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return err
	}
	// Python writes run documents through framework/utils/io.py::atomic_write,
	// whose mkstemp temp starts at 0600; keep the run file owner-only.
	return atomicfs.WriteJSON(filepath.Join(runsDir, run.ID+".json"), run, 0o600)
}

func (s *ConcurrentStorage) loadRunSync(runID string) (*schemas.Run, error) {
	path := filepath.Join(s.BasePath, "runs", runID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var run schemas.Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *ConcurrentStorage) loadSummarySync(runID string) (*schemas.RunSummary, error) {
	if err := ValidateKey(runID); err != nil {
		return nil, err
	}
	path := filepath.Join(s.BasePath, "summaries", runID+".json")
	if data, err := os.ReadFile(path); err == nil {
		var summary schemas.RunSummary
		if err := json.Unmarshal(data, &summary); err != nil {
			return nil, err
		}
		return &summary, nil
	}
	run, err := s.loadRunSync(runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil
	}
	summary := schemas.RunSummaryFromRun(run)
	return &summary, nil
}

func (s *ConcurrentStorage) deleteRunSync(runID string) (bool, error) {
	runPath := filepath.Join(s.BasePath, "runs", runID+".json")
	summaryPath := filepath.Join(s.BasePath, "summaries", runID+".json")
	if _, err := os.Stat(runPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := os.Remove(runPath); err != nil {
		return false, err
	}
	if _, err := os.Stat(summaryPath); err == nil {
		if err := os.Remove(summaryPath); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *ConcurrentStorage) listAllRunsSync() ([]string, error) {
	runsDir := filepath.Join(s.BasePath, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	ids := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}

func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}
