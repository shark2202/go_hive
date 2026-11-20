package skills

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type countingSkillsManager struct {
	mu      sync.Mutex
	reloads int
	lock    sync.Mutex
	prompt  string
}

func (m *countingSkillsManager) Reload() error {
	m.mu.Lock()
	m.reloads++
	m.mu.Unlock()
	return nil
}

func (m *countingSkillsManager) SkillsCatalogPrompt() string { return m.prompt }

func (m *countingSkillsManager) MutationLock() *sync.Mutex { return &m.lock }

func (m *countingSkillsManager) reloadCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reloads
}

type countingColony struct {
	queenID string
	mu      sync.Mutex
	reloads int
}

func (c *countingColony) ReloadSkills() (map[string]any, error) {
	c.mu.Lock()
	c.reloads++
	c.mu.Unlock()
	return nil, nil
}

func (c *countingColony) reloadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reloads
}

type fakeLiveSession struct {
	queenName string
	manager   *countingSkillsManager
	colony    *countingColony
}

func (s *fakeLiveSession) PhaseSkillsManager() ReloadableSkillsManager {
	if s.manager == nil {
		return nil
	}
	return s.manager
}

type fakeLiveSessionManager struct {
	sessions []*fakeLiveSession
}

func (m *fakeLiveSessionManager) ListSessions() []LiveQueenSession {
	out := make([]LiveQueenSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		out = append(out, session)
	}
	return out
}

func (m *fakeLiveSessionManager) IterQueenSessions(queenID *string) []LiveQueenSession {
	out := []LiveQueenSession{}
	for _, session := range m.sessions {
		if queenID != nil && session.queenName == *queenID {
			out = append(out, session)
		}
	}
	return out
}

func (m *fakeLiveSessionManager) IterColonyRuntimes(queenID *string) []SkillReloader {
	out := []SkillReloader{}
	for _, session := range m.sessions {
		if session.colony == nil {
			continue
		}
		if queenID == nil || session.colony.queenID == *queenID {
			out = append(out, session.colony)
		}
	}
	return out
}

func newLiveSession(queenID string) *fakeLiveSession {
	return &fakeLiveSession{
		queenName: queenID,
		manager:   &countingSkillsManager{},
		colony:    &countingColony{queenID: queenID},
	}
}

// Parity: tests/test_skill_live_reload.py::test_a_queens_skill_reloads_only_her_sessions_and_colonies
func TestQueensSkillReloadsOnlyHerSessionsAndColonies(t *testing.T) {
	ava, ivy := newLiveSession("queen_ava"), newLiveSession("queen_ivy")
	queen := "queen_ava"

	ReloadLiveSkills(&fakeLiveSessionManager{sessions: []*fakeLiveSession{ava, ivy}}, &queen)

	if ava.manager.reloadCount() != 1 || ava.colony.reloadCount() != 1 {
		t.Fatalf("ava = (%d, %d)", ava.manager.reloadCount(), ava.colony.reloadCount())
	}
	if ivy.manager.reloadCount() != 0 || ivy.colony.reloadCount() != 0 {
		t.Fatalf("ivy = (%d, %d)", ivy.manager.reloadCount(), ivy.colony.reloadCount())
	}
}

// Parity: tests/test_skill_live_reload.py::test_a_shared_skill_reloads_everyone
func TestSharedSkillReloadsEveryone(t *testing.T) {
	ava, ivy := newLiveSession("queen_ava"), newLiveSession("queen_ivy")

	ReloadLiveSkills(&fakeLiveSessionManager{sessions: []*fakeLiveSession{ava, ivy}}, nil)

	for _, session := range []*fakeLiveSession{ava, ivy} {
		if session.manager.reloadCount() != 1 || session.colony.reloadCount() != 1 {
			t.Fatalf("%s = (%d, %d)", session.queenName, session.manager.reloadCount(), session.colony.reloadCount())
		}
	}
}

// Parity: tests/test_skill_live_reload.py::test_one_failing_reload_does_not_stop_the_others
func TestOneFailingReloadDoesNotStopTheOthers(t *testing.T) {
	colony := &countingColony{queenID: "queen_ava"}

	ReloadRuntimes([]SkillReloader{brokenReloader{}, colony})

	if colony.reloadCount() != 1 {
		t.Fatalf("colony reloads = %d, want 1", colony.reloadCount())
	}
}

type brokenReloader struct{}

func (brokenReloader) ReloadSkills() (map[string]any, error) {
	return nil, errors.New("disk gone")
}

// Parity-exempt: the adapter's catalog_chars result is a Go-only helper detail;
func TestManagerReloadAdapterReportsCatalogChars(t *testing.T) {
	mgr := &countingSkillsManager{prompt: "abc"}
	adapter := NewManagerReloadAdapter(mgr)

	result, err := adapter.ReloadSkills()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if result["catalog_chars"] != 3 {
		t.Fatalf("result = %+v", result)
	}
	if mgr.reloadCount() != 1 {
		t.Fatalf("reloads = %d", mgr.reloadCount())
	}
}

// Parity-exempt: nil session-manager tolerance in the Go runtime collector has no
func TestLiveSkillRuntimesToleratesNoSessionManager(t *testing.T) {
	if runtimes := LiveSkillRuntimes(nil, nil); len(runtimes) != 0 {
		t.Fatalf("runtimes = %+v", runtimes)
	}
}

// Parity-exempt: skipping sessions that lack a skills manager is a Go-only
func TestLiveSkillRuntimesSkipsSessionsWithoutManager(t *testing.T) {
	session := &fakeLiveSession{queenName: "queen_ava"}
	session.colony = &countingColony{queenID: "queen_ava"}

	runtimes := LiveSkillRuntimes(&fakeLiveSessionManager{sessions: []*fakeLiveSession{session}}, nil)

	if len(runtimes) != 1 {
		t.Fatalf("runtimes = %+v", runtimes)
	}
}

// Parity-exempt: the parity caller returns None; the report is the Go seam the
// HTTP reload endpoints echo back to the UI.
func TestReloadRuntimesReportCountsReloadedAndFailed(t *testing.T) {
	good := &countingColony{queenID: "queen_ava"}
	report := ReloadRuntimesReport([]SkillReloader{brokenReloader{}, good})

	if report.Runtimes != 2 || report.Reloaded != 1 || report.Failed != 1 {
		t.Fatalf("report = %+v", report)
	}
	if good.reloadCount() != 1 {
		t.Fatalf("good reloads = %d, want 1", good.reloadCount())
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "disk gone") {
		t.Fatalf("errors = %v", report.Errors)
	}
}

// Parity-exempt: no upstream equivalent — the report is a Go-side API detail.
func TestReloadRuntimesReportRecoversPanickingRuntime(t *testing.T) {
	good := &countingColony{queenID: "queen_ava"}

	report := ReloadRuntimesReport([]SkillReloader{panickingReloader{}, good})

	if report.Reloaded != 1 || report.Failed != 1 {
		t.Fatalf("report = %+v", report)
	}
	if good.reloadCount() != 1 {
		t.Fatalf("good reloads = %d, want 1", good.reloadCount())
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "boom") {
		t.Fatalf("errors = %v", report.Errors)
	}
}

// Parity-exempt: the report naming contract is a Go-side API detail.
func TestReloadRuntimesReportNamesTargets(t *testing.T) {
	report := ReloadRuntimesReport([]SkillReloader{namedReloader{name: "session:sess_ops"}})

	if len(report.Targets) != 1 || report.Targets[0].Target != "session:sess_ops" {
		t.Fatalf("targets = %+v", report.Targets)
	}
}

// Parity-exempt: the dict shape is the Go HTTP contract; empty collections must
// render as arrays so the dashboard can iterate without null checks.
func TestReloadReportDictKeepsEmptyCollections(t *testing.T) {
	dict := ReloadReport{}.Dict()
	errorsOut, errorsOK := dict["errors"].([]string)
	targetsOut, targetsOK := dict["targets"].([]ReloadTarget)

	if !errorsOK || len(errorsOut) != 0 {
		t.Fatalf("errors = %#v", dict["errors"])
	}
	if !targetsOK || len(targetsOut) != 0 {
		t.Fatalf("targets = %#v", dict["targets"])
	}
	if dict["runtimes"] != 0 || dict["reloaded"] != 0 || dict["failed"] != 0 {
		t.Fatalf("dict = %v", dict)
	}
}

type panickingReloader struct{}

func (panickingReloader) ReloadSkills() (map[string]any, error) { panic("boom") }

type namedReloader struct{ name string }

func (r namedReloader) ReloadSkills() (map[string]any, error) { return nil, nil }

func (r namedReloader) ReloadTarget() string { return r.name }
