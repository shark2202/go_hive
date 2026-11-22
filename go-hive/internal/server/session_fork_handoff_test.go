package server

// Parity: core/framework/server/tests/test_session_supersession.py —
// task_create(new_session=true) forking, adapted to the Go fork seam.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// handoffForkFixture wires the fork to a temp hive home and a factory that
// records the params and returns a live-shaped successor.
type handoffForkFixture struct {
	home      hivehome.Home
	old       *Session
	manager   *SessionManager
	created   *Session
	params    CreateSessionParams
	forkedIDs []string
}

func newHandoffForkFixture(t *testing.T) *handoffForkFixture {
	t.Helper()
	root := t.TempDir()
	home := hivehome.Home{Root: root}
	oldDir := home.QueenSessionDir("default", "old-session")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := NewSession("old-session", eventbus.NewEventBus())
	old.QueenDir = oldDir
	old.QueenName = "default"
	fixture := &handoffForkFixture{
		home:    home,
		old:     old,
		manager: NewSessionManager(),
	}
	fixture.manager.AddSession(old)
	return fixture
}

func (f *handoffForkFixture) deps() HandoffForkDeps {
	return HandoffForkDeps{
		Manager: f.manager,
		Home:    f.home,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			f.params = params
			id := ""
			if params.SessionID != nil {
				id = *params.SessionID
			}
			created := NewSession(id, eventbus.NewEventBus())
			created.QueenName = "default"
			created.QueenDir = f.home.QueenSessionDir("default", id)
			f.created = created
			f.forkedIDs = append(f.forkedIDs, id)
			return created, nil
		},
	}
}

// TestForkQueenSessionWithHandoffSeedsSuccessor covers the handoff/tasks mode:
// a fresh directory whose only conversation is the handoff brief, the plan
// seeded into it, a live successor started on the plan, and the old session
// retired and announced.
// Parity: core/framework/server/tests/test_session_supersession.py
// (fork seeds the plan, superseeds the old session, disarms the kickoff turn).
func TestForkQueenSessionWithHandoffSeedsSuccessor(t *testing.T) {
	fixture := newHandoffForkFixture(t)
	deps := fixture.deps()
	deps.GenerateSessionID = func(_ time.Time) (string, error) { return "new-session", nil }

	var mu sync.Mutex
	var published []*eventbus.AgentEvent
	fixture.old.EventBus.Subscribe([]eventbus.EventType{eventbus.EventSessionForked}, func(event *eventbus.AgentEvent) {
		mu.Lock()
		published = append(published, event)
		mu.Unlock()
	})

	specs := []map[string]any{
		{"subject": "crawl target", "description": "first"},
		{"subject": "score leads", "active_form": "Scoring leads"},
	}
	result, err := ForkQueenSessionWithHandoff(fixture.old, deps, "  full context  ", specs, "ship the launch")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if result["new_session_id"] != "new-session" || result["compacted_from"] != "old-session" {
		t.Fatalf("result = %#v", result)
	}
	if ids, ok := result["task_ids"].([]int); !ok || len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("task_ids = %#v, want [1 2]", result["task_ids"])
	}

	newDir := fixture.home.QueenSessionDir("default", "new-session")
	// The handoff brief is the new session's only inherited context.
	parts, err := agentloop.NewFileConversationStore(filepath.Join(newDir, "conversations")).ReadParts()
	if err != nil {
		t.Fatalf("read parts: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("forked session parts = %d, want 1 (the handoff only)", len(parts))
	}
	content, _ := parts[0]["content"].(string)
	if content != "[Session handoff]\nfull context" {
		t.Fatalf("seed content = %q", content)
	}

	// Provenance meta: compacted_from, no compacted_at (no LLM ran).
	var meta map[string]any
	raw, err := os.ReadFile(filepath.Join(newDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if meta["compacted_from"] != "old-session" || meta["queen_id"] != "default" {
		t.Fatalf("meta = %#v", meta)
	}
	if _, compacted := meta["compacted_at"]; compacted {
		t.Fatalf("handoff fork must not stamp compacted_at: %#v", meta)
	}

	// The plan is seeded into the successor, not the old session.
	seeded := tasks.NewTaskStore(fixture.home.Root).ListTasks("new-session")
	if len(seeded) != 2 {
		t.Fatalf("seeded tasks = %d, want 2", len(seeded))
	}
	if seeded[0].Subject != "crawl target" || seeded[1].Subject != "score leads" {
		t.Fatalf("seeded subjects = %q, %q", seeded[0].Subject, seeded[1].Subject)
	}

	// The successor starts on the plan with the kickoff directive.
	if fixture.params.InitialPrompt == nil || !strings.Contains(*fixture.params.InitialPrompt, "ALREADY been set up") {
		t.Fatalf("initial prompt = %v", fixture.params.InitialPrompt)
	}
	if fixture.params.QueenResumeFrom == nil || *fixture.params.QueenResumeFrom != "new-session" {
		t.Fatalf("resume target = %v", fixture.params.QueenResumeFrom)
	}

	// The kickoff turn must not fork again, and the old session is retired.
	if !fixture.created.ForkKickoffPending {
		t.Fatal("successor must disarm the pivot until a real user message")
	}
	if fixture.old.SupersededBy != "new-session" {
		t.Fatalf("old.SupersededBy = %q", fixture.old.SupersededBy)
	}
	oldMetaRaw, err := os.ReadFile(filepath.Join(fixture.old.QueenDir, "meta.json"))
	if err != nil {
		t.Fatalf("read old meta: %v", err)
	}
	var oldMeta map[string]any
	if err := json.Unmarshal(oldMetaRaw, &oldMeta); err != nil {
		t.Fatalf("old meta: %v", err)
	}
	if oldMeta["superseded_by"] != "new-session" {
		t.Fatalf("old meta = %#v", oldMeta)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(published) != 1 {
		t.Fatalf("SESSION_FORKED events = %d, want 1", len(published))
	}
	if published[0].Data["new_session_id"] != "new-session" || published[0].Data["from_session_id"] != "old-session" {
		t.Fatalf("event data = %#v", published[0].Data)
	}
}

// TestForkQueenSessionWithHandoffRequiresHandoff: an empty brief must refuse
// before any directory is created — a blind successor is worse than a retry.
// Parity: core/framework/server/tests/test_session_supersession.py
// (an empty handoff is refused before any fork work happens).
func TestForkQueenSessionWithHandoffRequiresHandoff(t *testing.T) {
	fixture := newHandoffForkFixture(t)
	_, err := ForkQueenSessionWithHandoff(fixture.old, fixture.deps(), "   ", []map[string]any{{"subject": "x"}}, "")
	if err == nil || !strings.Contains(err.Error(), "requires a `handoff`") {
		t.Fatalf("err = %v, want handoff requirement", err)
	}
	if len(fixture.forkedIDs) != 0 {
		t.Fatalf("factory called with a blank handoff: %v", fixture.forkedIDs)
	}
	if fixture.old.SupersededBy != "" {
		t.Fatalf("old session retired on a refused fork: %q", fixture.old.SupersededBy)
	}
}
