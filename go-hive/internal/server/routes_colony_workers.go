package server

// Parity: framework/server/routes_colony_workers.py::register_routes

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

var colonyNameRe = regexp.MustCompile(`^[a-z0-9_]+$`)

var workerIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const (
	activeStatusPending = "pending"
	activeStatusRunning = "running"
	activeStatusQueued  = "queued"
)

var systemToolNames = map[string]struct{}{
	"get_account_info": {},
	"get_current_time": {},
}

type ColonyTool struct {
	Name        string
	Description string
}

// Parity: framework/server/routes_colony_workers.py::_resolve_colony_runtime
type ColonyWorkerReadSurface interface {
	ListWorkers() []WorkerInfoView
	Worker(workerID string) (WorkerInfoView, WorkerBatchView, bool)
	ColonyID() string
	StoragePath() string
	Skills() ([]*skills.ParsedSkill, bool)
	Tools() ([]ColonyTool, map[string]string)
}

type ColonyWorkerDeps struct {
	Manager *SessionManager
	Home    hivehome.Home
	Tasks   *tasks.TaskStore
	Runtime func(*Session) ColonyWorkerReadSurface

	ResolveTrackerDB func(colonyID string) (string, error)
	DataReadTimeout  time.Duration
	GlobalDB         func() (*globaldb.Client, error)
	CRMStatus        func() (any, error)
}

type colonyWorkerResultDict struct {
	Status          string  `json:"status"`
	Summary         string  `json:"summary"`
	Error           *string `json:"error"`
	TokensUsed      int     `json:"tokens_used"`
	DurationSeconds float64 `json:"duration_seconds"`
	ToolCallsUsed   int     `json:"tool_calls_used"`
	BudgetLimited   bool    `json:"budget_limited"`
}

type colonyWorkerBatchDict struct {
	BatchID    any `json:"batch_id"`
	BatchIndex any `json:"batch_index"`
	BatchSize  any `json:"batch_size"`
	WorkerSeq  any `json:"worker_seq"`
}

type colonyTaskSummary struct {
	Total      int `json:"total"`
	Completed  int `json:"completed"`
	InProgress int `json:"in_progress"`
	Pending    int `json:"pending"`
}

type colonyWorkerDict struct {
	WorkerID    string                  `json:"worker_id"`
	Task        string                  `json:"task"`
	Status      string                  `json:"status"`
	StartedAt   float64                 `json:"started_at"`
	Result      *colonyWorkerResultDict `json:"result"`
	ProfileName string                  `json:"profile_name"`
	Batch       colonyWorkerBatchDict   `json:"batch"`
	Goal        any                     `json:"goal"`
	TaskSummary *colonyTaskSummary      `json:"task_summary,omitempty"`
}

type colonyDiskWorkerDict struct {
	WorkerID       string                `json:"worker_id"`
	Task           any                   `json:"task"`
	Goal           any                   `json:"goal"`
	Status         string                `json:"status"`
	StartedAt      any                   `json:"started_at"`
	Result         any                   `json:"result"`
	ProfileName    any                   `json:"profile_name"`
	QueenSessionID any                   `json:"queen_session_id"`
	QueenName      any                   `json:"queen_name"`
	Batch          colonyWorkerBatchDict `json:"batch"`
}

type colonyLegacyWorkerDict struct {
	WorkerID  string  `json:"worker_id"`
	Task      string  `json:"task"`
	Status    string  `json:"status"`
	StartedAt float64 `json:"started_at"`
	Result    any     `json:"result"`
	Goal      any     `json:"goal"`
}

type colonyWorkerDetail struct {
	WorkerID    string                  `json:"worker_id"`
	Task        string                  `json:"task"`
	Status      string                  `json:"status"`
	StartedAt   float64                 `json:"started_at"`
	Result      *colonyWorkerResultDict `json:"result"`
	ProfileName string                  `json:"profile_name"`
	Batch       colonyWorkerBatchDict   `json:"batch"`
	Tasks       []*tasks.TaskRecord     `json:"tasks"`
	Goal        any                     `json:"goal"`
}

type colonyHistoricalDetail struct {
	WorkerID       string                `json:"worker_id"`
	Task           any                   `json:"task"`
	Goal           any                   `json:"goal"`
	Status         string                `json:"status"`
	StartedAt      any                   `json:"started_at"`
	Result         any                   `json:"result"`
	ProfileName    any                   `json:"profile_name"`
	QueenSessionID any                   `json:"queen_session_id"`
	QueenName      any                   `json:"queen_name"`
	Batch          colonyWorkerBatchDict `json:"batch"`
	Tasks          []*tasks.TaskRecord   `json:"tasks"`
}

type colonySkillDict struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	BaseDir     string `json:"base_dir"`
	SourceScope string `json:"source_scope"`
}

type colonyToolDict struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Provider    *string `json:"provider"`
}

var (
	historicalMetaCacheMu sync.Mutex
	historicalMetaCache   = map[string]map[string]any{}
)

func (d ColonyWorkerDeps) runtimeFor(session *Session) ColonyWorkerReadSurface {
	if d.Runtime != nil {
		return d.Runtime(session)
	}
	if session == nil || session.Colony == nil {
		return nil
	}
	surface, _ := session.Colony.(ColonyWorkerReadSurface)
	return surface
}

func (d ColonyWorkerDeps) taskStore() *tasks.TaskStore {
	if d.Tasks != nil {
		return d.Tasks
	}
	return tasks.GetTaskStore()
}

// Parity: framework/server/routes_colony_workers.py::_worker_info_to_dict
func colonyWorkerResult(info WorkerInfoView) *colonyWorkerResultDict {
	if info.Result == nil {
		return nil
	}
	return &colonyWorkerResultDict{
		Status:          info.Result.Status,
		Summary:         info.Result.Summary,
		Error:           info.Result.Error,
		TokensUsed:      info.Result.TokensUsed,
		DurationSeconds: info.Result.DurationSeconds,
		ToolCallsUsed:   info.Result.ToolCallsUsed,
		BudgetLimited:   info.Result.BudgetLimited,
	}
}

func colonyBatchDict(batch WorkerBatchView) colonyWorkerBatchDict {
	return colonyWorkerBatchDict{
		BatchID:    batch.BatchID,
		BatchIndex: batch.BatchIndex,
		BatchSize:  batch.BatchSize,
		WorkerSeq:  batch.WorkerSeq,
	}
}

func colonyWorkerGoal(store *tasks.TaskStore, workerID string) any {
	if store == nil {
		return nil
	}
	meta := store.GetMeta(workerID)
	if meta == nil || meta.Goal == nil {
		return nil
	}
	return *meta.Goal
}

// Parity: framework/server/routes_colony_workers.py::handle_list_workers
func colonyWorkerTaskSummary(store *tasks.TaskStore, workerID string) *colonyTaskSummary {
	summary := &colonyTaskSummary{}
	for _, record := range tasksOf(store, workerID) {
		if record.Status != tasks.TaskStatusArchived {
			summary.Total++
		}
		switch record.Status {
		case tasks.TaskStatusCompleted:
			summary.Completed++
		case tasks.TaskStatusInProgress:
			summary.InProgress++
		case tasks.TaskStatusPending:
			summary.Pending++
		}
	}
	return summary
}

// Parity: framework/server/routes_colony_workers.py::_worker_tasks
func tasksOf(store *tasks.TaskStore, workerID string) []*tasks.TaskRecord {
	out := []*tasks.TaskRecord{}
	if store == nil {
		return out
	}
	if records := store.ListTasks(workerID); records != nil {
		out = records
	}
	return out
}

func isActiveWorkerStatus(status string) bool {
	return status == activeStatusPending || status == activeStatusRunning || status == activeStatusQueued
}

func colonyWorkersDir(home hivehome.Home, colonyID string) string {
	return filepath.Join(home.ColoniesDir(), colonyID, "workers")
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Parity: framework/server/routes_colony_workers.py::_resolve_worker_dir
func resolveColonyWorkerDir(home hivehome.Home, session *Session, runtime ColonyWorkerReadSurface, workerID string) string {
	if !workerIDRe.MatchString(workerID) {
		return ""
	}
	colonyID := ""
	if runtime != nil {
		colonyID = runtime.ColonyID()
	}
	if colonyID == "" && session != nil {
		colonyID = session.ColonyID
	}
	if colonyID != "" {
		workerDir := filepath.Join(colonyWorkersDir(home, colonyID), workerID)
		if dirExists(workerDir) {
			return workerDir
		}
	}

	storagePath := ""
	if runtime != nil {
		storagePath = runtime.StoragePath()
	}
	if storagePath == "" && session != nil {
		storagePath = session.QueenDir
	}
	if storagePath != "" {
		workerDir := filepath.Join(storagePath, "workers", workerID)
		if dirExists(workerDir) {
			return workerDir
		}
	}
	return ""
}

// Parity: framework/server/routes_colony_workers.py::_read_historical_meta
func readHistoricalMeta(workerID, metaPath string) map[string]any {
	historicalMetaCacheMu.Lock()
	cached, ok := historicalMetaCache[workerID]
	historicalMetaCacheMu.Unlock()
	if ok {
		return cached
	}
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil || meta == nil {
		return nil
	}
	historicalMetaCacheMu.Lock()
	historicalMetaCache[workerID] = meta
	historicalMetaCacheMu.Unlock()
	return meta
}

// Parity: framework/server/routes_colony_workers.py::_walk_colony_workers
func walkColonyWorkers(workersDir, queenSessionID string, knownIDs map[string]struct{}) []any {
	out := []any{}
	if !dirExists(workersDir) {
		return out
	}
	entries, err := os.ReadDir(workersDir)
	if err != nil {
		return out
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return safeMtime(filepath.Join(workersDir, entries[i].Name())) > safeMtime(filepath.Join(workersDir, entries[j].Name()))
	})

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workerID := entry.Name()
		if _, seen := knownIDs[workerID]; seen {
			continue
		}
		metaPath := filepath.Join(workersDir, workerID, "meta.json")
		if _, statErr := os.Stat(metaPath); statErr != nil {
			continue
		}
		meta := readHistoricalMeta(workerID, metaPath)
		if meta == nil {
			continue
		}
		if owner, _ := meta["queen_session_id"].(string); owner != queenSessionID {
			continue
		}
		entryDir := filepath.Join(workersDir, workerID)
		task := meta["task"]
		if !pythonTruthy(task) {
			task = extractHistoricalTask(entryDir)
		}
		goal := meta["goal"]
		if !pythonTruthy(goal) {
			goal = nil
		}
		startedAt := meta["spawned_at"]
		if !pythonTruthy(startedAt) {
			startedAt = safeMtime(entryDir)
		}
		profileName := meta["profile_name"]
		if !pythonTruthy(profileName) {
			profileName = ""
		}
		out = append(out, colonyDiskWorkerDict{
			WorkerID:       workerID,
			Task:           task,
			Goal:           goal,
			Status:         "historical",
			StartedAt:      startedAt,
			Result:         nil,
			ProfileName:    profileName,
			QueenSessionID: orDefault(meta["queen_session_id"], nil),
			QueenName:      orDefault(meta["queen_name"], nil),
			Batch: colonyWorkerBatchDict{
				BatchID:    orDefault(meta["batch_id"], ""),
				BatchIndex: orDefault(meta["batch_index"], 0),
				BatchSize:  orDefault(meta["batch_size"], 0),
				WorkerSeq:  orDefault(meta["worker_seq"], 0),
			},
		})
	}
	return out
}

// Parity: framework/server/routes_colony_workers.py::_walk_historical_workers
func walkHistoricalWorkers(storagePath string, knownIDs map[string]struct{}) []any {
	out := []any{}
	workersDir := filepath.Join(storagePath, "workers")
	if !dirExists(workersDir) {
		return out
	}
	entries, err := os.ReadDir(workersDir)
	if err != nil {
		return out
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return safeMtime(filepath.Join(workersDir, entries[i].Name())) > safeMtime(filepath.Join(workersDir, entries[j].Name()))
	})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		workerID := entry.Name()
		if _, seen := knownIDs[workerID]; seen {
			continue
		}
		entryDir := filepath.Join(workersDir, workerID)
		out = append(out, colonyLegacyWorkerDict{
			WorkerID:  workerID,
			Task:      extractHistoricalTask(entryDir),
			Status:    "historical",
			StartedAt: safeMtime(entryDir),
			Result:    nil,
			Goal:      nil,
		})
	}
	return out
}

func handleListColonyWorkers(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	session := lookupSession(SessionRouteDeps{Manager: deps.Manager}, r.PathValue("session_id"))
	if session == nil {
		sessionNotFound(w, r.PathValue("session_id"))
		return
	}

	runtime := deps.runtimeFor(session)
	store := deps.taskStore()
	workers := []any{}
	knownIDs := map[string]struct{}{}
	storagePath := ""
	colonyID := ""

	if runtime != nil {
		for _, info := range runtime.ListWorkers() {
			status := string(info.Status)
			entry := colonyWorkerDict{
				WorkerID:    info.ID,
				Task:        info.Task,
				Status:      status,
				StartedAt:   info.StartedAt,
				Result:      colonyWorkerResult(info),
				ProfileName: info.ProfileName,
				Batch:       colonyBatchDict(info.Batch),
				Goal:        colonyWorkerGoal(store, info.ID),
			}
			if isActiveWorkerStatus(status) {
				entry.TaskSummary = colonyWorkerTaskSummary(store, info.ID)
			}
			workers = append(workers, entry)
			knownIDs[info.ID] = struct{}{}
		}
		storagePath = runtime.StoragePath()
		colonyID = runtime.ColonyID()
	}

	if storagePath == "" && session.QueenDir != "" {
		storagePath = session.QueenDir
	}
	if colonyID == "" {
		colonyID = session.ColonyID
	}

	if colonyID != "" {
		workers = append(workers, walkColonyWorkers(colonyWorkersDir(deps.Home, colonyID), session.ID, knownIDs)...)
		for _, worker := range workers {
			switch typed := worker.(type) {
			case colonyWorkerDict:
				knownIDs[typed.WorkerID] = struct{}{}
			case colonyDiskWorkerDict:
				knownIDs[typed.WorkerID] = struct{}{}
			case colonyLegacyWorkerDict:
				knownIDs[typed.WorkerID] = struct{}{}
			}
		}
	}

	if storagePath != "" {
		workers = append(workers, walkHistoricalWorkers(storagePath, knownIDs)...)
	}

	writeJSON(w, http.StatusOK, map[string]any{"workers": workers})
}

func handleGetColonyWorker(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(SessionRouteDeps{Manager: deps.Manager}, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	workerID := r.PathValue("worker_id")
	runtime := deps.runtimeFor(session)
	store := deps.taskStore()

	if runtime != nil {
		if info, batch, ok := runtime.Worker(workerID); ok {
			detail := colonyWorkerDetail{
				WorkerID:    info.ID,
				Task:        info.Task,
				Status:      string(info.Status),
				StartedAt:   info.StartedAt,
				Result:      colonyWorkerResult(info),
				ProfileName: info.ProfileName,
				Batch:       colonyBatchDict(batch),
				Tasks:       tasksOf(store, workerID),
				Goal:        colonyWorkerGoal(store, workerID),
			}
			writeJSON(w, http.StatusOK, map[string]any{"worker": detail})
			return
		}
	}

	if workerDir := resolveColonyWorkerDir(deps.Home, session, runtime, workerID); workerDir != "" {
		detail := historicalColonyWorkerDetail(workerID, workerDir, store)
		writeJSON(w, http.StatusOK, map[string]any{"worker": detail})
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown worker: " + workerID})
}

// Parity: framework/server/routes_colony_workers.py::_read_worker_from_disk
func historicalColonyWorkerDetail(workerID, workerDir string, store *tasks.TaskStore) colonyHistoricalDetail {
	disk := ReadWorkerFromDisk(workerID, workerDir)
	batch, _ := disk["batch"].(map[string]any)
	if batch == nil {
		batch = map[string]any{}
	}
	return colonyHistoricalDetail{
		WorkerID:       workerID,
		Task:           disk["task"],
		Goal:           disk["goal"],
		Status:         "historical",
		StartedAt:      disk["started_at"],
		Result:         disk["result"],
		ProfileName:    disk["profile_name"],
		QueenSessionID: disk["queen_session_id"],
		QueenName:      disk["queen_name"],
		Batch: colonyWorkerBatchDict{
			BatchID:    orDefault(batch["batch_id"], ""),
			BatchIndex: orDefault(batch["batch_index"], 0),
			BatchSize:  orDefault(batch["batch_size"], 0),
			WorkerSeq:  orDefault(batch["worker_seq"], 0),
		},
		Tasks: tasksOf(store, workerID),
	}
}

func handleGetColonyWorkerConversation(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(SessionRouteDeps{Manager: deps.Manager}, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	workerID := r.PathValue("worker_id")
	runtime := deps.runtimeFor(session)
	workerDir := resolveColonyWorkerDir(deps.Home, session, runtime, workerID)
	if workerDir == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown worker: " + workerID})
		return
	}

	result := ReadWorkerConversation(workerDir)
	result["worker_id"] = workerID
	writeJSON(w, http.StatusOK, result)
}

func handleListColonySkills(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(SessionRouteDeps{Manager: deps.Manager}, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	catalog := []*skills.ParsedSkill{}
	if runtime := deps.runtimeFor(session); runtime != nil {
		if listed, ok := runtime.Skills(); ok {
			catalog = listed
		}
	}

	skillsOut := make([]colonySkillDict, 0, len(catalog))
	for _, skill := range catalog {
		if skill == nil {
			continue
		}
		skillsOut = append(skillsOut, colonySkillDict{
			Name:        skill.Name,
			Description: skill.Description,
			Location:    skill.Location,
			BaseDir:     skill.BaseDir,
			SourceScope: skill.SourceScope,
		})
	}
	sort.SliceStable(skillsOut, func(i, j int) bool { return skillsOut[i].Name < skillsOut[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"skills": skillsOut})
}

// Parity: framework/server/routes_colony_workers.py::_tool_to_dict
func colonyToolToDict(tool ColonyTool, providerMap map[string]string) colonyToolDict {
	var provider *string
	if mapped, ok := providerMap[tool.Name]; ok {
		value := mapped
		provider = &value
	}
	if provider == nil {
		if _, isSystem := systemToolNames[tool.Name]; isSystem {
			value := "system"
			provider = &value
		}
	}
	return colonyToolDict{Name: tool.Name, Description: tool.Description, Provider: provider}
}

func handleListColonyTools(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(SessionRouteDeps{Manager: deps.Manager}, sessionID)
	if session == nil {
		sessionNotFound(w, sessionID)
		return
	}

	var tools []ColonyTool
	var providerMap map[string]string
	if runtime := deps.runtimeFor(session); runtime != nil {
		tools, providerMap = runtime.Tools()
	}

	toolsOut := make([]colonyToolDict, 0, len(tools))
	for _, tool := range tools {
		toolsOut = append(toolsOut, colonyToolToDict(tool, providerMap))
	}
	sort.SliceStable(toolsOut, func(i, j int) bool { return toolsOut[i].Name < toolsOut[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"tools": toolsOut})
}

// Parity: framework/server/routes_colony_workers.py::register_routes
func RegisterColonyWorkerRoutes(mux *http.ServeMux, deps ColonyWorkerDeps) {
	mux.HandleFunc("GET /api/sessions/{session_id}/workers", func(w http.ResponseWriter, r *http.Request) {
		handleListColonyWorkers(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/workers/{worker_id}", func(w http.ResponseWriter, r *http.Request) {
		handleGetColonyWorker(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/workers/{worker_id}/conversation", func(w http.ResponseWriter, r *http.Request) {
		handleGetColonyWorkerConversation(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/colony/skills", func(w http.ResponseWriter, r *http.Request) {
		handleListColonySkills(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/colony/tools", func(w http.ResponseWriter, r *http.Request) {
		handleListColonyTools(w, r, deps)
	})
	registerColonyDataRoutes(mux, deps)
	registerColonyGlobalRoutes(mux, deps)
}
