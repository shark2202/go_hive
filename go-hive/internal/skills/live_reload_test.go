package skills

import (
	"errors"
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
