package server

// Parity: framework/server/routes_maintenance.py::register_routes

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/maintenance"
)

// serverProcessStartTS stands in for aiohttp's app["started_at"]: tier 1
// skips log files the live process still holds open. AppDeps has no start
// timestamp yet, so package init is the closest equivalent.
var serverProcessStartTS = float64(time.Now().UnixNano()) / 1e9

// JanitorRunGuard mirrors the aiohttp app["janitor_running"] flag: one
// destructive pass at a time per registered mux.
type JanitorRunGuard struct {
	mu      sync.Mutex
	running bool
}

func (g *JanitorRunGuard) tryStart() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return false
	}
	g.running = true
	return true
}

func (g *JanitorRunGuard) finish() {
	g.mu.Lock()
	g.running = false
	g.mu.Unlock()
}

// MaintenanceRouteDeps carries the janitor seams. Nil functions fall back to
// the maintenance package; tests substitute fakes so no disk is pruned.
type MaintenanceRouteDeps struct {
	Home           hivehome.Home
	Manager        *SessionManager
	Guard          *JanitorRunGuard
	LoadConfig     func(hivehome.Home) maintenance.RetentionConfig
	BuildSafety    func(*SessionManager, maintenance.RetentionConfig) *maintenance.SafetyContext
	RunOnce        func(hivehome.Home, *maintenance.SafetyContext, *maintenance.RetentionConfig, maintenance.RunOptions) *maintenance.JanitorReport
	LoadLastReport func(hivehome.Home) (any, bool)
	ProcessStartTS func() *float64
}

func RegisterMaintenanceRoutes(mux *http.ServeMux, deps MaintenanceRouteDeps) {
	if deps.Guard == nil {
		deps.Guard = &JanitorRunGuard{}
	}
	if deps.LoadConfig == nil {
		deps.LoadConfig = maintenance.LoadRetentionConfig
	}
	if deps.BuildSafety == nil {
		deps.BuildSafety = janitorSafetyFromManager
	}
	if deps.RunOnce == nil {
		deps.RunOnce = maintenance.RunOnce
	}
	if deps.LoadLastReport == nil {
		deps.LoadLastReport = maintenance.LoadLastReport
	}

	mux.HandleFunc("POST /api/maintenance/janitor/run", func(w http.ResponseWriter, r *http.Request) {
		handleJanitorRun(w, r, deps)
	})
	mux.HandleFunc("GET /api/maintenance/janitor/report", func(w http.ResponseWriter, r *http.Request) {
		handleJanitorReport(w, r, deps)
	})
}

// activeWorkerSnapshotter is the exported slice of ColonyRuntime that
// SafetyContext.from_manager needs: enumerate workers, then read each one's
// status. Both methods exist on *colony.ColonyRuntime today.
type activeWorkerSnapshotter interface {
	WorkerToolTiering() []colony.WorkerToolTiering
	GetWorker(id string) *colony.Worker
}

// Parity: framework/maintenance/retention.py::SafetyContext.from_manager
func janitorSafetyFromManager(manager *SessionManager, cfg maintenance.RetentionConfig) *maintenance.SafetyContext {
	protected := map[string]bool{}
	liveDirs := map[string]bool{}
	if manager != nil {
		for _, session := range manager.ListSessions() {
			if session == nil {
				continue
			}
			protected[session.ID] = true
			if session.QueenDir != "" {
				liveDirs[session.QueenDir] = true
				protected[filepath.Base(session.QueenDir)] = true
			}
			if snapshotter, ok := session.Colony.(activeWorkerSnapshotter); ok && snapshotter != nil {
				for _, tier := range snapshotter.WorkerToolTiering() {
					if worker := snapshotter.GetWorker(tier.WorkerID); worker != nil && worker.Status().IsActive() {
						protected[tier.WorkerID] = true
					}
				}
			}
		}
	}
	return &maintenance.SafetyContext{
		ProtectedSessionIDs: protected,
		LiveSessionDirs:     liveDirs,
		GraceSeconds:        float64(cfg.ActiveGraceHours) * 3600.0,
	}
}

func janitorPythonInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	if number, ok := value.(interface{ Int64() (int64, error) }); ok {
		if parsed, err := number.Int64(); err == nil {
			return int(parsed), true
		}
	}
	if number, ok := value.(interface{ Float64() (float64, error) }); ok {
		if parsed, err := number.Float64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func janitorTiers(raw any) (map[int]bool, bool) {
	items, ok := raw.([]any)
	if !ok {
		if direct, ok := raw.([]int); ok {
			items = make([]any, len(direct))
			for i, item := range direct {
				items[i] = item
			}
		} else {
			return nil, false
		}
	}
	tiers := make(map[int]bool, len(items))
	for _, item := range items {
		tier, ok := janitorPythonInt(item)
		if !ok {
			return nil, false
		}
		tiers[tier] = true
	}
	if len(tiers) == 0 {
		return nil, false
	}
	for tier := range tiers {
		if tier < 1 || tier > 3 {
			return nil, false
		}
	}
	return tiers, true
}

// Parity: framework/server/routes_maintenance.py::handle_janitor_run
func handleJanitorRun(w http.ResponseWriter, r *http.Request, deps MaintenanceRouteDeps) {
	body := map[string]any{}
	if value, err := decodeJSONValue(r); err == nil {
		if object, ok := value.(map[string]any); ok {
			body = object
		}
	}

	execute := pyTruthy(body["execute"])
	var rawTiers any = []any{1, 2, 3}
	if value, present := body["tiers"]; present {
		rawTiers = value
	}
	tiers, ok := janitorTiers(rawTiers)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("invalid tiers: %s", pyReprValue(rawTiers)),
		})
		return
	}

	if !deps.Guard.tryStart() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a janitor run is already in progress"})
		return
	}
	defer deps.Guard.finish()

	if deps.Manager == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "session manager not configured"})
		return
	}

	cfg := deps.LoadConfig(deps.Home)
	safety := deps.BuildSafety(deps.Manager, cfg)
	if safety.Refresher == nil {
		safety.Refresher = func() (map[string]bool, map[string]bool) {
			fresh := deps.BuildSafety(deps.Manager, cfg)
			return fresh.ProtectedSessionIDs, fresh.LiveSessionDirs
		}
	}

	var processStart *float64
	if deps.ProcessStartTS != nil {
		processStart = deps.ProcessStartTS()
	} else {
		processStart = &serverProcessStartTS
	}

	report := deps.RunOnce(deps.Home, safety, &cfg, maintenance.RunOptions{
		Tiers:          tiers,
		Execute:        execute,
		IncludeLegacy:  pyTruthy(body["legacy"]),
		IncludeJunk:    pyTruthy(body["junk"]),
		ProcessStartTS: processStart,
	})
	writeJSON(w, http.StatusOK, map[string]any{"report": report.ToMap()})
}

// Parity: framework/server/routes_maintenance.py::handle_janitor_report
func handleJanitorReport(w http.ResponseWriter, r *http.Request, deps MaintenanceRouteDeps) {
	report, ok := deps.LoadLastReport(deps.Home)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"report": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": report})
}
