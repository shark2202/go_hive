package colony

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

var DefaultMaxConcurrentWorkers = colonyEnvInt("HIVE_MAX_CONCURRENT_WORKERS", 4)

func colonyEnvInt(name string, def int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func colonyEnvBool(name string, def bool) bool {
	raw, ok := os.LookupEnv(name)
	if !ok || raw == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

type WorkerStatus string

const (
	WorkerQueued    WorkerStatus = "queued"
	WorkerPending   WorkerStatus = "pending"
	WorkerRunning   WorkerStatus = "running"
	WorkerCompleted WorkerStatus = "completed"
	WorkerFailed    WorkerStatus = "failed"
	WorkerStopped   WorkerStatus = "stopped"
)

func (s WorkerStatus) IsActive() bool {
	return s == WorkerQueued || s == WorkerPending || s == WorkerRunning
}

func (s WorkerStatus) IsRunningWorker() bool {
	return s == WorkerPending || s == WorkerRunning
}

type SchedulerWorker struct {
	ID          string
	Task        string
	Status      WorkerStatus
	BatchID     string
	BatchIndex  int
	BatchSize   int
	WorkerSeq   int
	OutputFile  string
	StreamID    string
	ExecutionID string

	Start func() error
	Stop  func()
}

type WorkerScheduler struct {
	mu              sync.Mutex
	maxConcurrent   int
	colonyID        string
	bus             *eventbus.EventBus
	workers         map[string]*SchedulerWorker
	pendingQueue    []*SchedulerWorker
	dispatchBlocked bool
	idCounter       int
	subscriptionID  string

	StartFunc           func(*SchedulerWorker) error
	StopFunc            func(*SchedulerWorker)
	ApplyAdaptiveBudget func(*SchedulerWorker)
}

func NewWorkerScheduler(maxConcurrent int, colonyID string, bus *eventbus.EventBus) *WorkerScheduler {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrentWorkers
	}
	if colonyID == "" {
		colonyID = "colony"
	}
	return &WorkerScheduler{
		maxConcurrent: maxConcurrent,
		colonyID:      colonyID,
		bus:           bus,
		workers:       map[string]*SchedulerWorker{},
	}
}

func (s *WorkerScheduler) Start() {
	s.mu.Lock()
	if s.subscriptionID != "" || s.bus == nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.subscriptionID = s.bus.Subscribe(
		[]eventbus.EventType{eventbus.EventSubagentReport},
		s.OnTerminalEvent,
	)
}

func (s *WorkerScheduler) Stop() {
	s.mu.Lock()
	subID := s.subscriptionID
	s.subscriptionID = ""
	s.mu.Unlock()
	if subID != "" && s.bus != nil {
		s.bus.Unsubscribe(subID)
	}
}

func (s *WorkerScheduler) OnTerminalEvent(event *eventbus.AgentEvent) {
	if event == nil {
		return
	}
	if colonyID, _ := event.Data["colony_id"].(string); colonyID != "" && colonyID != s.colonyID {
		return
	}
	s.DrainPendingQueue()
}

func (s *WorkerScheduler) RunningWorkerCount(excludeID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runningWorkerCountLocked(excludeID)
}

func (s *WorkerScheduler) runningWorkerCountLocked(excludeID string) int {
	count := 0
	for id, worker := range s.workers {
		if id == excludeID {
			continue
		}
		if worker.Status.IsRunningWorker() {
			count++
		}
	}
	return count
}

func (s *WorkerScheduler) Submit(worker *SchedulerWorker) bool {
	if worker == nil {
		return false
	}
	s.mu.Lock()
	if worker.ID == "" {
		s.idCounter++
		worker.ID = "worker_" + strconv.Itoa(s.idCounter)
	}
	if worker.Status == "" {
		worker.Status = WorkerPending
	}
	s.workers[worker.ID] = worker
	s.mu.Unlock()

	return s.enqueueOrAdmit(worker)
}

func (s *WorkerScheduler) enqueueOrAdmit(worker *SchedulerWorker) bool {
	s.mu.Lock()
	if s.dispatchBlocked {
		worker.Status = WorkerStopped
		s.mu.Unlock()
		s.publishStoppedReport(worker, "Colony was stopped — this task was never started.")
		return false
	}
	if s.runningWorkerCountLocked(worker.ID) < s.maxConcurrent {
		if s.ApplyAdaptiveBudget != nil {
			s.ApplyAdaptiveBudget(worker)
		}
		s.startBackgroundLocked(worker)
		s.mu.Unlock()
		return true
	}
	worker.Status = WorkerQueued
	s.pendingQueue = append(s.pendingQueue, worker)
	s.mu.Unlock()
	return false
}

func (s *WorkerScheduler) startBackgroundLocked(worker *SchedulerWorker) {
	worker.Status = WorkerPending
	if worker.Start == nil {
		worker.Status = WorkerRunning
		return
	}
	start := worker.Start
	go func() {
		s.mu.Lock()
		if worker.Status == WorkerPending {
			worker.Status = WorkerRunning
		}
		s.mu.Unlock()
		_ = start()
	}()
}

func (s *WorkerScheduler) DrainPendingQueue() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	promoted := 0
	for len(s.pendingQueue) > 0 && s.runningWorkerCountLocked("") < s.maxConcurrent {
		worker := s.pendingQueue[0]
		s.pendingQueue = s.pendingQueue[1:]
		if worker.Status != WorkerQueued {
			continue
		}
		if s.ApplyAdaptiveBudget != nil {
			s.ApplyAdaptiveBudget(worker)
		}
		s.startBackgroundLocked(worker)
		promoted++
	}
	return promoted
}

func (s *WorkerScheduler) CancelQueued() int {
	s.mu.Lock()
	queued := append([]*SchedulerWorker(nil), s.pendingQueue...)
	s.pendingQueue = nil
	s.mu.Unlock()

	cancelled := 0
	for _, worker := range queued {
		s.mu.Lock()
		if worker.Status != WorkerQueued {
			s.mu.Unlock()
			continue
		}
		worker.Status = WorkerStopped
		s.mu.Unlock()
		s.publishStoppedReport(worker,
			"Worker was queued behind the concurrency cap and never started — stopped before its slot opened.")
		cancelled++
	}
	return cancelled
}

func (s *WorkerScheduler) BlockDispatch() {
	s.mu.Lock()
	s.dispatchBlocked = true
	s.mu.Unlock()
}

func (s *WorkerScheduler) UnblockDispatch() {
	s.mu.Lock()
	s.dispatchBlocked = false
	s.mu.Unlock()
}

func (s *WorkerScheduler) publishStoppedReport(worker *SchedulerWorker, summary string) {
	if s.bus == nil || worker == nil {
		return
	}
	streamID := worker.StreamID
	if streamID == "" {
		streamID = worker.ID
	}
	executionID := worker.ExecutionID
	if executionID == "" {
		executionID = worker.ID
	}
	event := eventbus.NewAgentEvent(eventbus.EventSubagentReport, streamID)
	event.NodeID = eventbus.StrPtr(worker.ID)
	event.ExecutionID = eventbus.StrPtr(executionID)
	event.Data = map[string]any{
		"worker_id":        worker.ID,
		"colony_id":        s.colonyID,
		"task":             worker.Task,
		"status":           "stopped",
		"summary":          summary,
		"data":             map[string]any{},
		"error":            nil,
		"duration_seconds": 0.0,
		"tokens_used":      0,
		"batch_id":         worker.BatchID,
		"batch_index":      worker.BatchIndex,
		"batch_size":       worker.BatchSize,
		"output_file":      worker.OutputFile,
	}
	s.bus.Publish(event)
}

func (s *WorkerScheduler) StopWorkers() {
	s.BlockDispatch()
	s.CancelQueued()

	s.mu.Lock()
	live := make([]*SchedulerWorker, 0, len(s.workers))
	for _, worker := range s.workers {
		if worker.Status == WorkerPending || worker.Status == WorkerRunning {
			live = append(live, worker)
		}
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, worker := range live {
		wg.Add(1)
		go func(w *SchedulerWorker) {
			defer wg.Done()
			if w.Stop != nil {
				w.Stop()
				return
			}
			s.mu.Lock()
			if w.Status == WorkerPending || w.Status == WorkerRunning {
				w.Status = WorkerStopped
			}
			s.mu.Unlock()
		}(worker)
	}
	wg.Wait()
}

func (s *WorkerScheduler) PendingQueueDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pendingQueue)
}

func (s *WorkerScheduler) QueuedWorkers() []*SchedulerWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*SchedulerWorker(nil), s.pendingQueue...)
}

func (s *WorkerScheduler) StatusOf(id string) (WorkerStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	worker, ok := s.workers[id]
	if !ok {
		return "", false
	}
	return worker.Status, true
}

func (s *WorkerScheduler) BatchIDOf(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	worker, ok := s.workers[id]
	if !ok {
		return "", false
	}
	return worker.BatchID, true
}

func (s *WorkerScheduler) MarkTerminal(id string, status WorkerStatus) {
	s.mu.Lock()
	if worker, ok := s.workers[id]; ok {
		worker.Status = status
	}
	s.mu.Unlock()
}

func (s *WorkerScheduler) SpawnBatch(tasks []string, batchID string) []string {
	if batchID == "" {
		batchID = mintBatchID()
	}
	size := len(tasks)
	ids := make([]string, 0, size)
	for i, task := range tasks {
		worker := &SchedulerWorker{
			Task:       task,
			Status:     WorkerPending,
			BatchID:    batchID,
			BatchIndex: i + 1,
			BatchSize:  size,
		}
		if s.StartFunc != nil {
			worker.Start = func() error { return s.StartFunc(worker) }
		}
		if s.StopFunc != nil {
			worker.Stop = func() { s.StopFunc(worker) }
		}
		wid := s.submitAndID(worker)
		ids = append(ids, wid)
	}
	return ids
}

func (s *WorkerScheduler) submitAndID(worker *SchedulerWorker) string {
	if worker.ID == "" {
		s.mu.Lock()
		s.idCounter++
		worker.ID = "worker_" + strconv.Itoa(s.idCounter)
		s.mu.Unlock()
	}
	s.Submit(worker)
	return worker.ID
}

func mintBatchID() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "rpw_" + time.Now().UTC().Format("20060102T150405Z") + "_00000000"
	}
	return "rpw_" + time.Now().UTC().Format("20060102T150405Z") + "_" + hex.EncodeToString(buf)
}
