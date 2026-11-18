package server

import (
	"net/http"
	"sort"
	"testing"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/maintenance"
)

type fakeLiveColony struct {
	workers []*colony.Worker
}

func (f *fakeLiveColony) StreamID() string { return "" }

func (f *fakeLiveColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (f *fakeLiveColony) InjectInput(workerID, content string) error { return nil }

func (f *fakeLiveColony) WorkerToolTiering() []colony.WorkerToolTiering {
	out := make([]colony.WorkerToolTiering, 0, len(f.workers))
	for _, worker := range f.workers {
		out = append(out, colony.WorkerToolTiering{WorkerID: worker.ID})
	}
	return out
}

func (f *fakeLiveColony) GetWorker(id string) *colony.Worker {
	for _, worker := range f.workers {
		if worker.ID == id {
			return worker
		}
	}
	return nil
}

type janitorCapture struct {
	calls  int
	safety *maintenance.SafetyContext
	opts   maintenance.RunOptions
}

func maintenanceTestMux(t *testing.T, deps MaintenanceRouteDeps) (*http.ServeMux, *janitorCapture) {
	t.Helper()
	capture := &janitorCapture{}
	if deps.Home.Root == "" {
		deps.Home = hivehome.Home{Root: t.TempDir()}
	}
	if deps.Manager == nil {
		deps.Manager = NewSessionManager()
	}
	deps.LoadConfig = func(hivehome.Home) maintenance.RetentionConfig {
		return maintenance.DefaultRetentionConfig()
	}
	deps.RunOnce = func(home hivehome.Home, safety *maintenance.SafetyContext, cfg *maintenance.RetentionConfig, opts maintenance.RunOptions) *maintenance.JanitorReport {
		capture.calls++
		capture.safety = safety
		capture.opts = opts
		tiers := make([]int, 0, len(opts.Tiers))
		for tier := range opts.Tiers {
			tiers = append(tiers, tier)
		}
		sort.Ints(tiers)
		return &maintenance.JanitorReport{DryRun: !opts.Execute, Tiers: tiers}
	}
	if deps.LoadLastReport == nil {
		deps.LoadLastReport = maintenance.LoadLastReport
	}
	mux := http.NewServeMux()
	RegisterMaintenanceRoutes(mux, deps)
	return mux, capture
}

// Parity-exempt: framework/server/routes_maintenance.py has no Python HTTP tests; the RunOnce contract is covered by core/tests/test_janitor_safety.py.
func TestJanitorRunDefaultsToDryRun(t *testing.T) {
	mux, capture := maintenanceTestMux(t, MaintenanceRouteDeps{})
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/maintenance/janitor/run", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if capture.calls != 1 {
		t.Fatalf("calls = %d", capture.calls)
	}
	if capture.opts.Execute {
		t.Fatal("execute = true, want dry run")
	}
	if capture.opts.ProcessStartTS == nil || *capture.opts.ProcessStartTS <= 0 {
		t.Fatalf("process_start_ts = %#v", capture.opts.ProcessStartTS)
	}
	for _, tier := range []int{1, 2, 3} {
		if !capture.opts.Tiers[tier] {
			t.Fatalf("missing tier %d in %#v", tier, capture.opts.Tiers)
		}
	}
	report, _ := body["report"].(map[string]any)
	if report["dry_run"] != true {
		t.Fatalf("report = %#v", report)
	}
}

// Parity-exempt: execute/tier/flag plumbing has no Python HTTP test.
func TestJanitorRunPassesExecuteAndFlags(t *testing.T) {
	mux, capture := maintenanceTestMux(t, MaintenanceRouteDeps{})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/maintenance/janitor/run",
		`{"execute":true,"tiers":[1],"legacy":true,"junk":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !capture.opts.Execute || !capture.opts.IncludeLegacy || !capture.opts.IncludeJunk {
		t.Fatalf("opts = %#v", capture.opts)
	}
	if len(capture.opts.Tiers) != 1 || !capture.opts.Tiers[1] {
		t.Fatalf("tiers = %#v", capture.opts.Tiers)
	}
}

// Parity-exempt: tier validation 400s have no Python HTTP test.
func TestJanitorRunRejectsInvalidTiers(t *testing.T) {
	cases := []string{
		`{"tiers":[4]}`,
		`{"tiers":[]}`,
		`{"tiers":"x"}`,
		`{"tiers":[0]}`,
	}
	for _, body := range cases {
		mux, capture := maintenanceTestMux(t, MaintenanceRouteDeps{})
		recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/maintenance/janitor/run", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, recorder.Code)
		}
		if capture.calls != 0 {
			t.Fatalf("body %s ran janitor", body)
		}
	}
}

// Parity-exempt: the concurrent-run 409 guard has no Python HTTP test.
func TestJanitorRunRejectsConcurrentRun(t *testing.T) {
	guard := &JanitorRunGuard{running: true}
	mux, capture := maintenanceTestMux(t, MaintenanceRouteDeps{Guard: guard})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/maintenance/janitor/run", `{}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d", recorder.Code)
	}
	if capture.calls != 0 {
		t.Fatal("janitor ran while guarded")
	}
}

// Parity-exempt: aiohttp's request.json() failure is absorbed to {}; this pins the Go equivalent.
func TestJanitorRunIgnoresInvalidJSON(t *testing.T) {
	mux, capture := maintenanceTestMux(t, MaintenanceRouteDeps{})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/maintenance/janitor/run", `not json`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if capture.calls != 1 || capture.opts.Execute {
		t.Fatalf("capture = %#v", capture)
	}
}

// Parity-exempt: report endpoint has no Python HTTP test.
func TestJanitorReportReturnsSavedReport(t *testing.T) {
	saved := map[string]any{"dry_run": true, "total_bytes": float64(12)}
	mux, _ := maintenanceTestMux(t, MaintenanceRouteDeps{
		LoadLastReport: func(hivehome.Home) (any, bool) { return saved, true },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/maintenance/janitor/report", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	report, _ := body["report"].(map[string]any)
	if report["total_bytes"] != float64(12) {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: no-report null payload has no Python HTTP test.
func TestJanitorReportMissingReturnsNull(t *testing.T) {
	mux, _ := maintenanceTestMux(t, MaintenanceRouteDeps{
		LoadLastReport: func(hivehome.Home) (any, bool) { return nil, false },
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/maintenance/janitor/report", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if report, present := body["report"]; !present || report != nil {
		t.Fatalf("body = %#v", body)
	}
}

// Parity: framework/maintenance/retention.py::SafetyContext.from_manager
func TestJanitorSafetyProtectsLiveSessions(t *testing.T) {
	active := colony.NewWorker("w1", "task", "stream")
	finished := colony.NewWorker("w2", "task", "stream")
	finished.Finish(colony.WorkerCompleted, colony.WorkerResult{})

	manager := NewSessionManager()
	manager.AddSession(&Session{
		ID:       "s1",
		QueenDir: "/hive/queens/q1/sessions/s1",
		Colony:   &fakeLiveColony{workers: []*colony.Worker{active, finished}},
	})
	cfg := maintenance.DefaultRetentionConfig()
	safety := janitorSafetyFromManager(manager, cfg)
	for _, key := range []string{"s1", "w1"} {
		if !safety.ProtectedSessionIDs[key] {
			t.Fatalf("%s not protected: %#v", key, safety.ProtectedSessionIDs)
		}
	}
	if safety.ProtectedSessionIDs["w2"] {
		t.Fatalf("finished worker w2 must not be protected: %#v", safety.ProtectedSessionIDs)
	}
	if !safety.LiveSessionDirs["/hive/queens/q1/sessions/s1"] {
		t.Fatalf("live dirs = %#v", safety.LiveSessionDirs)
	}
	if safety.GraceSeconds != float64(cfg.ActiveGraceHours)*3600 {
		t.Fatalf("grace = %v", safety.GraceSeconds)
	}
}

// Parity: framework/maintenance/retention.py::SafetyContext.refresh
func TestJanitorRunInstallsRefresher(t *testing.T) {
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "s1", QueenDir: "/hive/queens/q1/sessions/s1"})
	mux, capture := maintenanceTestMux(t, MaintenanceRouteDeps{Manager: manager})
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/maintenance/janitor/run", `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if capture.safety == nil || capture.safety.Refresher == nil {
		t.Fatal("refresher not installed")
	}
	manager.AddSession(&Session{ID: "s2", QueenDir: "/hive/queens/q1/sessions/s2"})
	protected, live := capture.safety.Refresher()
	if !protected["s2"] || !live["/hive/queens/q1/sessions/s2"] {
		t.Fatalf("protected = %#v, live = %#v", protected, live)
	}
}
