package server

// Live skills reload fan-out: the SessionManager bridge the /api/skills routes
// walk. Covers the queen filter, the global (shared-library) fan-out, session
// lifecycle, and the real counts/errors the reload endpoints echo.
//
// Parity: tests/test_skill_live_reload.py

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/skills"
)

// countingReloadManager is one session's reloadable catalog. The reload counter
// and the mutation lock are separate so a fan-out can hold the lock while
// Reload runs (the ReloadableSkillsManager contract).
type countingReloadManager struct {
	mu        sync.Mutex
	reloads   int
	reloadErr error
	prompt    string
	lock      sync.Mutex
}

func (m *countingReloadManager) Reload() error {
	m.mu.Lock()
	m.reloads++
	m.mu.Unlock()
	return m.reloadErr
}

func (m *countingReloadManager) SkillsCatalogPrompt() string { return m.prompt }

func (m *countingReloadManager) MutationLock() *sync.Mutex { return &m.lock }

func (m *countingReloadManager) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloads
}

// countingColonyRuntime is a live colony runtime whose only reload-relevant
// behaviour is the catalog rebuild the fan-out triggers.
type countingColonyRuntime struct {
	mu        sync.Mutex
	reloads   int
	reloadErr error
	prompt    string
}

func (c *countingColonyRuntime) StreamID() string { return "colony:test" }

func (c *countingColonyRuntime) SubscribeToEvents(
	eventTypes []eventbus.EventType,
	handler eventbus.EventHandler,
	opts ...eventbus.SubscribeOption,
) (string, error) {
	return "", nil
}

func (c *countingColonyRuntime) InjectInput(workerID, content string) error { return nil }

func (c *countingColonyRuntime) ReloadSkills() (map[string]any, error) {
	c.mu.Lock()
	c.reloads++
	c.mu.Unlock()
	if c.reloadErr != nil {
		return nil, c.reloadErr
	}
	return map[string]any{"catalog_chars": len(c.prompt)}, nil
}

func (c *countingColonyRuntime) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reloads
}

func liveTestSession(id, queenName, colonyID string) *Session {
	session := NewSession(id, eventbus.NewEventBus())
	session.QueenName = queenName
	session.ColonyID = colonyID
	return session
}

// Parity: tests/test_skill_live_reload.py::test_a_queens_skill_reloads_only_her_sessions_and_colonies
func TestLiveSkillsReloadsOnlyTheQueensSessionsAndColonies(t *testing.T) {
	manager := NewSessionManager()

	avaSkills, avaColony := &countingReloadManager{prompt: "ava-catalog"}, &countingColonyRuntime{prompt: "ava-colony"}
	ava := liveTestSession("sess_ava", "queen_ava", "colony_ava")
	ava.LiveSkills = avaSkills
	ava.Colony = avaColony
	manager.AddSession(ava)

	ivySkills, ivyColony := &countingReloadManager{prompt: "ivy-catalog"}, &countingColonyRuntime{prompt: "ivy-colony"}
	ivy := liveTestSession("sess_ivy", "queen_ivy", "colony_ivy")
	ivy.LiveSkills = ivySkills
	ivy.Colony = ivyColony
	manager.AddSession(ivy)

	queen := "queen_ava"
	report := skills.ReloadLiveSkillsReport(manager.LiveSkills(), &queen)

	if avaSkills.count() != 1 || avaColony.count() != 1 {
		t.Fatalf("ava reloads = (%d, %d), want (1, 1)", avaSkills.count(), avaColony.count())
	}
	if ivySkills.count() != 0 || ivyColony.count() != 0 {
		t.Fatalf("ivy reloads = (%d, %d), want (0, 0)", ivySkills.count(), ivyColony.count())
	}
	if report.Runtimes != 2 || report.Reloaded != 2 || report.Failed != 0 {
		t.Fatalf("report = %+v, want 2 runtimes reloaded", report)
	}
	if len(report.Targets) != 2 ||
		report.Targets[0].Target != "colony:colony_ava" ||
		report.Targets[1].Target != "session:sess_ava" {
		t.Fatalf("targets = %+v", report.Targets)
	}
}

// Parity: tests/test_skill_live_reload.py::test_a_shared_skill_reloads_everyone
func TestLiveSkillsGlobalReloadReachesEverySession(t *testing.T) {
	manager := NewSessionManager()
	avaSkills := &countingReloadManager{prompt: "ava"}
	ava := liveTestSession("sess_ava", "queen_ava", "")
	ava.LiveSkills = avaSkills
	manager.AddSession(ava)

	ivySkills := &countingReloadManager{prompt: "ivy"}
	ivy := liveTestSession("sess_ivy", "queen_ivy", "")
	ivy.LiveSkills = ivySkills
	manager.AddSession(ivy)

	// A session without a catalog of its own (no chat provider) is skipped.
	manager.AddSession(liveTestSession("sess_ghost", "queen_ghost", ""))

	report := skills.ReloadLiveSkillsReport(manager.LiveSkills(), nil)

	if avaSkills.count() != 1 || ivySkills.count() != 1 {
		t.Fatalf("reloads = (%d, %d), want (1, 1)", avaSkills.count(), ivySkills.count())
	}
	if report.Runtimes != 2 || report.Reloaded != 2 {
		t.Fatalf("report = %+v, want 2 runtimes", report)
	}
}

// Parity-exempt: upstream's SessionManager owns teardown and the reload walk in
// one event loop, so a stopped session cannot be snapshotted mid-teardown; the
// Go fan-out runs in goroutines and needs the closed flag.
func TestLiveSkillsReloadSkipsStoppedSessions(t *testing.T) {
	manager := NewSessionManager()
	liveSkills, colony := &countingReloadManager{prompt: "catalog"}, &countingColonyRuntime{prompt: "colony"}
	session := liveTestSession("sess_ops", "ops", "colony_ops")
	session.LiveSkills = liveSkills
	session.Colony = colony
	manager.AddSession(session)

	if !manager.StopSession("sess_ops") {
		t.Fatal("StopSession = false, want true")
	}

	report := skills.ReloadLiveSkillsReport(manager.LiveSkills(), nil)
	if report.Runtimes != 0 {
		t.Fatalf("report = %+v, want no live runtimes after stop", report)
	}
	if liveSkills.count() != 0 || colony.count() != 0 {
		t.Fatalf("reloads after stop = (%d, %d), want (0, 0)", liveSkills.count(), colony.count())
	}
}

// Parity-exempt: closing rather than stopping covers the Go-only race where a
// fan-out snapshots the runtimes before StopSession removes the session.
func TestLiveSkillsCollectedRuntimesSkipAClosedSession(t *testing.T) {
	manager := NewSessionManager()
	liveSkills, colony := &countingReloadManager{prompt: "catalog"}, &countingColonyRuntime{prompt: "colony"}
	session := liveTestSession("sess_ops", "ops", "colony_ops")
	session.LiveSkills = liveSkills
	session.Colony = colony
	manager.AddSession(session)

	runtimes := skills.LiveSkillRuntimes(manager.LiveSkills(), nil)
	if len(runtimes) != 2 {
		t.Fatalf("runtimes = %d, want 2", len(runtimes))
	}
	if !manager.StopSession("sess_ops") {
		t.Fatal("StopSession = false, want true")
	}

	report := skills.ReloadRuntimesReport(runtimes)
	if liveSkills.count() != 0 || colony.count() != 0 {
		t.Fatalf("reloads after close = (%d, %d), want (0, 0)", liveSkills.count(), colony.count())
	}
	if report.Reloaded != 2 || report.Failed != 0 {
		t.Fatalf("report = %+v, want the no-ops counted as reloaded", report)
	}
}

// Parity: tests/test_skill_live_reload.py::test_one_failing_reload_does_not_stop_the_others
func TestLiveSkillsReloadReportsFailuresWithoutBlocking(t *testing.T) {
	manager := NewSessionManager()
	broken := &countingReloadManager{prompt: "broken", reloadErr: errors.New("disk gone")}
	brokenSession := liveTestSession("sess_broken", "ops", "")
	brokenSession.LiveSkills = broken
	manager.AddSession(brokenSession)

	healthy := &countingReloadManager{prompt: "healthy"}
	healthySession := liveTestSession("sess_healthy", "ops", "")
	healthySession.LiveSkills = healthy
	manager.AddSession(healthySession)

	report := skills.ReloadLiveSkillsReport(manager.LiveSkills(), nil)

	if healthy.count() != 1 || broken.count() != 1 {
		t.Fatalf("reloads = (%d, %d), want both attempted", healthy.count(), broken.count())
	}
	if report.Reloaded != 1 || report.Failed != 1 {
		t.Fatalf("report = %+v, want 1 reloaded / 1 failed", report)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "disk gone") {
		t.Fatalf("errors = %v", report.Errors)
	}
}

// Parity-exempt: no upstream route reports live fan-out counts; the Go route
// echoes them so the UI can say how many catalogs a reload reached.
func TestReloadQueenSkillsRouteReportsLiveFanOut(t *testing.T) {
	deps := skillsTestDeps(t)
	skillsTestSeedQueen(t, deps, "ops")

	liveSkills := &countingReloadManager{prompt: "ops-catalog"}
	session := liveTestSession("sess_ops", "ops", "")
	session.LiveSkills = liveSkills
	deps.Manager.AddSession(session)

	otherSkills := &countingReloadManager{prompt: "ivy-catalog"}
	other := liveTestSession("sess_ivy", "ivy", "")
	other.LiveSkills = otherSkills
	deps.Manager.AddSession(other)

	recorder, body := skillsTestDo(t, skillsTestMux(deps), http.MethodPost, "/api/queen/ops/skills/reload", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["ok"] != true || body["kind"] != "queen" || body["target"] != "ops" {
		t.Fatalf("body = %v", body)
	}
	live, ok := body["live"].(map[string]any)
	if !ok {
		t.Fatalf("live = %#v", body["live"])
	}
	if live["runtimes"] != float64(1) || live["reloaded"] != float64(1) || live["failed"] != float64(0) {
		t.Fatalf("live = %v, want the ops session counted", live)
	}
	if liveSkills.count() != 1 || otherSkills.count() != 0 {
		t.Fatalf("reloads = (%d, %d), want (1, 0)", liveSkills.count(), otherSkills.count())
	}
}

// Parity-exempt: the global reload endpoint is Go-only (upstream reloads the
// shared library implicitly through the queen routes); it must report real
// counts and keep a per-runtime failure out of the HTTP status.
func TestReloadLibrarySkillsRouteReportsLiveFanOut(t *testing.T) {
	deps := skillsTestDeps(t)

	broken := &countingReloadManager{prompt: "broken", reloadErr: errors.New("disk gone")}
	brokenSession := liveTestSession("sess_broken", "ops", "")
	brokenSession.LiveSkills = broken
	deps.Manager.AddSession(brokenSession)

	healthy := &countingReloadManager{prompt: "healthy"}
	healthySession := liveTestSession("sess_healthy", "ivy", "")
	healthySession.LiveSkills = healthy
	deps.Manager.AddSession(healthySession)

	recorder, body := skillsTestDo(t, skillsTestMux(deps), http.MethodPost, "/api/skills/reload", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["ok"] != true || body["kind"] != "library" {
		t.Fatalf("body = %v", body)
	}
	live, ok := body["live"].(map[string]any)
	if !ok {
		t.Fatalf("live = %#v", body["live"])
	}
	if live["runtimes"] != float64(2) || live["reloaded"] != float64(1) || live["failed"] != float64(1) {
		t.Fatalf("live = %v, want 2 runtimes / 1 failed", live)
	}
	errorsOut, _ := live["errors"].([]any)
	if len(errorsOut) != 1 || !strings.Contains(errorsOut[0].(string), "disk gone") {
		t.Fatalf("errors = %v", live["errors"])
	}
	if healthy.count() != 1 || broken.count() != 1 {
		t.Fatalf("reloads = (%d, %d), want both attempted", healthy.count(), broken.count())
	}
}
