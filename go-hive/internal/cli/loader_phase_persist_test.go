package cli

// Parity: framework/server/session_manager.py::SessionManager.create_session
// Parity: framework/server/queen_orchestrator.py::prepare_queen_session (phase cascade + meta.json)

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

func newPhaseTestRunner(t *testing.T) (*Runner, hivehome.Home, *hiveserver.SessionManager, *sync.Mutex, *[]*hiveserver.Session) {
	t.Helper()
	// 用带重试清理的临时目录：会话运行时可能在测试体返回后仍落盘一次。
	home, err := hivehome.ResolveAt(filepath.Join(testTempDir(t), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	runner := &Runner{Stdout: io.Discard, Stderr: io.Discard}
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	return runner, home, hiveserver.NewSessionManager(), &liveMu, &live
}

func readSessionMeta(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	meta := map[string]any{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}
	return meta
}

// Parity: framework/server/session_manager.py::SessionManager.create_session (initial_phase)
func TestCreateSessionPersistsInitialPhase(t *testing.T) {
	runner, home, manager, liveMu, live := newPhaseTestRunner(t)

	sessionID := "sess-independent"
	phase := "independent"
	session, err := runner.createSessionWithRuntime(hiveserver.CreateSessionParams{
		SessionID:    &sessionID,
		InitialPhase: &phase,
	}, manager, liveMu, live, home, "", nil, "")
	if err != nil {
		t.Fatalf("createSessionWithRuntime: %v", err)
	}
	state, ok := session.PhaseState.(*hiveserver.QueenPhaseState)
	if !ok || state == nil {
		t.Fatalf("phase state = %#v, want a live QueenPhaseState", session.PhaseState)
	}
	if state.Phase() != hiveserver.QueenPhaseIndependent {
		t.Fatalf("phase = %q, want independent", state.Phase())
	}
	// The session payload the dashboard reads reports the phase instead of the
	// planning/staging fallback (covered in the server package's
	// TestSessionToLiveDictReportsPhaseState).
	if session.QueenDir == "" {
		t.Fatal("session root was not set")
	}
	if got := hiveserver.ReadSessionPhase(session.QueenDir); got != hiveserver.QueenPhaseIndependent {
		t.Fatalf("meta phase = %q, want independent", got)
	}
	if meta := readSessionMeta(t, session.QueenDir); meta["phase"] != hiveserver.QueenPhaseIndependent {
		t.Fatalf("meta = %#v, want the phase stamped", meta)
	}
}

// Parity: framework/server/queen_orchestrator.py::prepare_queen_session (cold resume)
func TestCreateSessionRestoresPhaseAfterRestart(t *testing.T) {
	runner, home, manager, liveMu, live := newPhaseTestRunner(t)

	sessionID := "sess-restart"
	phase := "independent"
	created, err := runner.createSessionWithRuntime(hiveserver.CreateSessionParams{
		SessionID:    &sessionID,
		InitialPhase: &phase,
	}, manager, liveMu, live, home, "", nil, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	dir := created.QueenDir
	// A restart drops the live session; the transcript tree (and its
	// meta.json) is what survives.
	manager.StopSession(sessionID)

	resumed, err := runner.createSessionWithRuntime(hiveserver.CreateSessionParams{
		QueenResumeFrom: &sessionID,
	}, manager, liveMu, live, home, "", nil, "")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if resumed.ID != sessionID {
		t.Fatalf("resumed id = %q, want %q", resumed.ID, sessionID)
	}
	state, ok := resumed.PhaseState.(*hiveserver.QueenPhaseState)
	if !ok || state == nil || state.Phase() != hiveserver.QueenPhaseIndependent {
		t.Fatalf("resumed phase = %#v, want independent from meta.json", resumed.PhaseState)
	}
	if resumed.QueenDir != dir {
		t.Fatalf("resumed root = %q, want the persisted %q", resumed.QueenDir, dir)
	}
}

// Parity: framework/server/session_manager.py::SessionManager.load_colony (phase follows the binding)
func TestLoadColonySwitchesPhaseAndPersists(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })
	_ = writeColonySpecFixture(t, fixture.home, "honeycomb", lifecycleWorkerSpec)

	session := hiveserver.NewSession("s-phase", eventbus.NewEventBus())
	session.QueenDir = t.TempDir()
	phase := "independent"
	hiveserver.AttachSessionPhaseState(session, &phase, session.QueenDir)
	fixture.manager.AddSession(session)

	loaded, err := fixture.lifecycle.LoadColony("s-phase", "honeycomb", nil)
	if err != nil {
		t.Fatalf("LoadColony: %v", err)
	}
	state, ok := loaded.PhaseState.(*hiveserver.QueenPhaseState)
	if !ok || state == nil || state.Phase() != hiveserver.QueenPhaseColony {
		t.Fatalf("phase after load_colony = %#v, want colony", loaded.PhaseState)
	}
	if got := hiveserver.ReadSessionPhase(loaded.QueenDir); got != hiveserver.QueenPhaseColony {
		t.Fatalf("persisted phase = %q, want colony", got)
	}
	if meta := readSessionMeta(t, loaded.QueenDir); meta["phase"] != hiveserver.QueenPhaseColony {
		t.Fatalf("meta = %#v, want phase colony", meta)
	}
}
