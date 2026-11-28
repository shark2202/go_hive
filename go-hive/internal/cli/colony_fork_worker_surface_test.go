package cli

// The DM fork's worker surface: worker.json inherits the source queen's live
// tools, skills/protocols prompts, skill dirs, loop config and queen phase/id,
// the input_data tracker binding, and the fork provenance metadata. The fork
// also refuses a source whose queen executor/node is gone (upstream 503) and
// stamps the source session with the colony binding + agent meta.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hivecolony "github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// colonyForkPhaseState is the minimal stamped phase state the fork reads
// queen_id from (SessionPhaseState).
type colonyForkPhaseState struct{ queenID string }

func (p colonyForkPhaseState) Phase() string { return hiveserver.QueenPhaseIndependent }
func (p colonyForkPhaseState) QueenID() string {
	return p.queenID
}
func (p colonyForkPhaseState) QueenProfile() map[string]any { return map[string]any{} }

// armColonyForkQueen gives the source session a live chat runtime and returns
// the surface the fork should inherit from it.
func armColonyForkQueen(t *testing.T, source *hiveserver.Session, tools []llm.ToolDef) hiveserver.QueenForkSurface {
	t.Helper()
	runtime, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM:   &lifecycleTestLLM{},
		Tools: tools,
		Execute: func(context.Context, string, map[string]any) (string, bool, error) {
			return "ok", false, nil
		},
		Root:      t.TempDir(),
		Bus:       eventbus.NewEventBus(),
		SessionID: source.ID,
	})
	if err != nil {
		t.Fatalf("build source queen runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	source.Chat = runtime
	return runtime.ForkSurfaceSnapshot()
}

// postCreateColonyWithPrompt lives in colony_fork_seed_test.go (same package).

func readForkWorker(t *testing.T, colonyPath string) map[string]any {
	t.Helper()
	worker, ok := readJSONObject(filepath.Join(colonyPath, "worker.json"))
	if !ok {
		t.Fatalf("read %s/worker.json: unavailable", colonyPath)
	}
	return worker
}

// Parity: framework/server/routes_execution.py::fork_session_into_colony (worker.json live snapshot)
// Parity: framework/agents/queen/worker_definition.py::build_meta
func TestCreateColonyFromSourceDMForkWritesLiveQueenSurface(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{forkMessage(0, "user", "spin up the invoicing colony")}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	source.PhaseState = colonyForkPhaseState{queenID: "queen_alpha"}
	surface := armColonyForkQueen(t, source, []llm.ToolDef{
		{Name: "terminal_exec", Description: "run a command"},
		{Name: "my_mcp_tool", Description: "a curated MCP tool"},
		{Name: "get_worker_status", Description: "queen-only monitor"},
		{Name: "run_worker", Description: "queen-only fan-out"},
		{Name: "suggest_colony", Description: "independent-phase only"},
	})
	if !surface.HasLoop {
		t.Fatal("source queen surface carries no loop config")
	}
	writeQueenSkill(t, fixture.home, "queen_alpha", "fork-surface-probe", "A probe skill.")
	source.LiveSkills = queenSkillsCatalogAt(fixture.home, "queen_alpha", t.TempDir())

	status, payload := postCreateColonyWithPrompt(t, fixture, "forked", "src-dm", "ship the invoice colony")
	if status != http.StatusCreated {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	waitForkBackground(t, fixture)

	colonyPath := fixture.home.ColonyDir("forked")
	worker := readForkWorker(t, colonyPath)
	for _, key := range []string{
		"name", "version", "description", "input_data", "goal", "system_prompt",
		"tools", "searchable_tools", "skills_catalog_prompt", "protocols_prompt",
		"skill_dirs", "identity_prompt", "memory_prompt", "queen_phase", "queen_id",
		"loop_config", "spawned_from", "spawned_at",
	} {
		if _, present := worker[key]; !present {
			t.Fatalf("worker.json is missing %q: %#v", key, worker)
		}
	}
	if _, present := worker["concurrency_hint"]; present {
		t.Fatalf("worker.json invented concurrency_hint the HTTP route never supplies: %#v", worker["concurrency_hint"])
	}
	if worker["name"] != "worker" || worker["version"] != "1.0.0" {
		t.Fatalf("worker identity = %#v/%#v", worker["name"], worker["version"])
	}
	if want := "Worker clone from queen session src-dm"; worker["description"] != want {
		t.Fatalf("worker description = %#v, want %q", worker["description"], want)
	}
	goal, _ := worker["goal"].(map[string]any)
	if goal["description"] != "ship the invoice colony" {
		t.Fatalf("worker goal = %#v", goal)
	}
	if criteria, ok := goal["success_criteria"].([]any); !ok || len(criteria) != 0 {
		t.Fatalf("worker success_criteria = %#v, want []", goal["success_criteria"])
	}
	if constraints, ok := goal["constraints"].([]any); !ok || len(constraints) != 0 {
		t.Fatalf("worker constraints = %#v, want []", goal["constraints"])
	}
	prompt := "ship the invoice colony"
	if worker["system_prompt"] != hivecolony.BuildSystemPrompt(&prompt) {
		t.Fatalf("worker system_prompt is not the worker definition prompt: %#v", worker["system_prompt"])
	}

	rawTools, _ := worker["tools"].([]any)
	inherited := map[string]bool{}
	inheritedOrder := make([]string, 0, len(rawTools))
	for _, entry := range rawTools {
		if name, ok := entry.(string); ok {
			inherited[name] = true
			inheritedOrder = append(inheritedOrder, name)
		}
	}
	if !inherited["terminal_exec"] || !inherited["my_mcp_tool"] {
		t.Fatalf("worker tools = %v, want the queen's work tools inherited", inheritedOrder)
	}
	for _, stripped := range []string{"get_worker_status", "run_worker", "suggest_colony"} {
		if inherited[stripped] {
			t.Fatalf("worker tools = %v, %q should have been stripped", inheritedOrder, stripped)
		}
	}
	// ask_user is a framework synthetic outside the queen phase lists, so the
	// queen-only strip keeps it (upstream _resolve_queen_only_tools).
	if !inherited["ask_user"] {
		t.Fatalf("worker tools = %v, want the framework synthetic ask_user kept", inheritedOrder)
	}
	if len(inheritedOrder) < 2 || inheritedOrder[0] != "terminal_exec" || inheritedOrder[1] != "my_mcp_tool" {
		t.Fatalf("worker tools = %v, want the queen's tool order preserved", inheritedOrder)
	}
	if tools, ok := worker["searchable_tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("worker searchable_tools = %#v, want []", worker["searchable_tools"])
	}
	skillsCatalog, _ := worker["skills_catalog_prompt"].(string)
	if !strings.Contains(skillsCatalog, "<name>fork-surface-probe</name>") {
		t.Fatalf("worker skills_catalog_prompt lost the live queen skill: %q", skillsCatalog)
	}
	if _, ok := worker["protocols_prompt"].(string); !ok {
		t.Fatalf("worker protocols_prompt = %#v, want a string", worker["protocols_prompt"])
	}
	dirs, _ := worker["skill_dirs"].([]any)
	foundDir := false
	for _, entry := range dirs {
		if dir, ok := entry.(string); ok && strings.Contains(dir, "fork-surface-probe") {
			foundDir = true
		}
	}
	if !foundDir {
		t.Fatalf("worker skill_dirs = %#v, want the live queen skill dir", dirs)
	}
	if worker["identity_prompt"] != "" || worker["memory_prompt"] != "" {
		t.Fatalf("worker identity/memory prompts must stay empty: %#v/%#v", worker["identity_prompt"], worker["memory_prompt"])
	}
	if worker["queen_phase"] != hiveserver.QueenPhaseIndependent {
		t.Fatalf("worker queen_phase = %#v", worker["queen_phase"])
	}
	if worker["queen_id"] != "queen_alpha" {
		t.Fatalf("worker queen_id = %#v", worker["queen_id"])
	}
	if worker["spawned_from"] != "src-dm" {
		t.Fatalf("worker spawned_from = %#v", worker["spawned_from"])
	}
	if spawnedAt, _ := worker["spawned_at"].(string); strings.TrimSpace(spawnedAt) == "" {
		t.Fatalf("worker spawned_at = %#v", worker["spawned_at"])
	}
	loopConfig, _ := worker["loop_config"].(map[string]any)
	if loopConfig["max_iterations"] != float64(surface.MaxIterations) ||
		loopConfig["tool_call_budget"] != float64(surface.ToolCallBudget) ||
		loopConfig["max_context_tokens"] != float64(surface.MaxContextTokens) {
		t.Fatalf("worker loop_config = %#v, want the queen loop values %#v", loopConfig, surface)
	}
	if loopConfig["max_iterations"] == float64(hivecolony.DefaultWorkerLoopConfig["max_iterations"]) {
		t.Fatalf("worker loop_config = %#v kept the worker default instead of the queen's config", loopConfig)
	}

	inputData, _ := worker["input_data"].(map[string]any)
	binding, _ := inputData["binding"].(map[string]any)
	if binding["name"] != "forked" || binding["dir"] != colonyPath {
		t.Fatalf("worker input_data binding = %#v", binding)
	}
	trackerDB, _ := binding["tracker_db"].(string)
	if trackerDB == "" {
		t.Fatalf("worker input_data binding has no tracker_db: %#v", binding)
	}
	if _, err := os.Stat(trackerDB); err != nil {
		t.Fatalf("worker tracker_db %q is not on disk: %v", trackerDB, err)
	}

	if _, present := worker["queen_name"]; present {
		t.Fatalf("worker.json kept the Go-only queen_name field: %#v", worker["queen_name"])
	}
	if sessionID == "" {
		t.Fatal("create response carried no session id")
	}
}

// Parity: framework/server/routes_execution.py::fork_session_into_colony (metadata.json)
func TestCreateColonyFromSourceDMFWritesForkMetadata(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	source := hiveserver.NewSession("src-dm", eventbus.NewEventBus())
	source.QueenName = "queen_alpha"
	source.QueenExecutor = &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{"queen": &recordingQueenNode{}}}
	fixture.manager.AddSession(source)

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != http.StatusCreated {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	sessionID, _ := payload["session_id"].(string)
	colonyPath := fixture.home.ColonyDir("forked")

	meta, ok := readJSONObject(filepath.Join(colonyPath, "metadata.json"))
	if !ok {
		t.Fatal("read forked metadata.json: unavailable")
	}
	if meta["colony_id"] != "forked" || meta["name"] != "forked" {
		t.Fatalf("metadata identity = %#v", meta)
	}
	if meta["queen_name"] != "queen_alpha" || meta["source_session_id"] != "src-dm" {
		t.Fatalf("metadata provenance = %#v", meta)
	}
	if meta["queen_session_id"] != sessionID {
		t.Fatalf("metadata queen_session_id = %#v, want %q", meta["queen_session_id"], sessionID)
	}
	if createdAt, ok := meta["created_at"].(float64); !ok || createdAt <= 0 {
		t.Fatalf("metadata created_at = %#v, want a positive seconds float", meta["created_at"])
	}
	if updatedAt, _ := meta["updated_at"].(string); strings.TrimSpace(updatedAt) == "" {
		t.Fatalf("metadata updated_at = %#v", meta["updated_at"])
	}
	workers, _ := meta["workers"].(map[string]any)
	workerSlot, _ := workers["worker"].(map[string]any)
	if workerSlot["task"] != "Work on tasks for colony forked" {
		t.Fatalf("metadata workers.worker.task = %#v", workerSlot["task"])
	}
	if spawnedAt, _ := workerSlot["spawned_at"].(string); strings.TrimSpace(spawnedAt) == "" {
		t.Fatalf("metadata workers.worker.spawned_at = %#v", workerSlot["spawned_at"])
	}

	// A source with no live chat/skills surface contributes empties: no tools,
	// empty prompts, and the default worker loop config (upstream queen_ctx
	// None path).
	worker := readForkWorker(t, colonyPath)
	if tools, ok := worker["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("worker tools = %#v, want [] with no live queen surface", worker["tools"])
	}
	if worker["skills_catalog_prompt"] != "" || worker["protocols_prompt"] != "" {
		t.Fatalf("worker inherited prompts without a live skills manager: %#v", worker)
	}
	if dirs, ok := worker["skill_dirs"].([]any); !ok || len(dirs) != 0 {
		t.Fatalf("worker skill_dirs = %#v, want []", worker["skill_dirs"])
	}
	loopConfig, _ := worker["loop_config"].(map[string]any)
	if loopConfig["max_iterations"] != float64(hivecolony.DefaultWorkerLoopConfig["max_iterations"]) {
		t.Fatalf("worker loop_config = %#v, want the worker defaults", loopConfig)
	}
}

// Parity: framework/server/routes_execution.py::fork_session_into_colony (source meta + binding)
func TestCreateColonyFromSourceDMFPersistsSourceProvenance(t *testing.T) {
	fixture := newLifecycleFixture(t)
	t.Cleanup(func() { fixture.cleanup(t) })

	sourceDir := writeForkSourceSession(t, fixture.home, "queen_alpha", "src-dm",
		[]map[string]any{
			forkMessage(0, "user", "let's fork"),
			forkMessage(1, "assistant", "ready"),
		}, nil)
	source := forkLiveSource(t, fixture, "src-dm", "queen_alpha", sourceDir)
	partsBefore := readForkParts(t, sourceDir)
	eventsBefore, err := os.ReadFile(filepath.Join(sourceDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read source events: %v", err)
	}

	status, payload := postCreateColony(t, fixture, "forked", "src-dm")
	if status != http.StatusCreated {
		t.Fatalf("create colony status = %d payload = %#v, want 201", status, payload)
	}
	waitForkBackground(t, fixture)
	colonyPath := fixture.home.ColonyDir("forked")

	meta := readForkMeta(t, sourceDir)
	if meta["agent_path"] != colonyPath || meta["agent_name"] != "Forked" {
		t.Fatalf("source meta provenance = %#v", meta)
	}
	if meta["colony_spawned"] != true || meta["spawned_colony_id"] != "forked" {
		t.Fatalf("source meta spawn lock = %#v", meta)
	}
	if !source.ColonySpawned || source.SpawnedColonyID != "forked" {
		t.Fatalf("source live spawn lock = %v/%q", source.ColonySpawned, source.SpawnedColonyID)
	}
	if source.ColonyID != "forked" {
		t.Fatalf("source live colony id = %q, want the fork target", source.ColonyID)
	}
	binding, ok := source.Binding.(*tracker.Binding)
	if !ok || binding == nil {
		t.Fatalf("source binding = %#v, want a tracker binding", source.Binding)
	}
	if binding.Name != "forked" || binding.Dir != colonyPath || binding.TrackerDB == "" {
		t.Fatalf("source binding = %#v", binding)
	}
	if _, err := os.Stat(binding.TrackerDB); err != nil {
		t.Fatalf("source binding tracker_db %q is not on disk: %v", binding.TrackerDB, err)
	}

	// The source transcript is only read by the fork: compaction rewrites the
	// copied tree.
	partsAfter := readForkParts(t, sourceDir)
	if len(partsAfter) != len(partsBefore) || partsAfter[0]["content"] != partsBefore[0]["content"] {
		t.Fatalf("source transcript changed: before=%#v after=%#v", partsBefore, partsAfter)
	}
	eventsAfter, err := os.ReadFile(filepath.Join(sourceDir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read source events after fork: %v", err)
	}
	if string(eventsAfter) != string(eventsBefore) {
		t.Fatalf("source events.jsonl changed: before=%q after=%q", eventsBefore, eventsAfter)
	}
}

// Parity: framework/server/routes_execution.py::fork_session_into_colony (queen_executor / node_registry guard)
// Parity: framework/server/routes_sessions.py::_create_colony_from_source (RuntimeError -> 503)
func TestCreateColonyFromSourceDMForkRequiresLiveQueen(t *testing.T) {
	cases := []struct {
		name     string
		executor *hiveserver.QueenExecutor
		want     string
	}{
		{
			name: "queen executor gone",
			want: "queen_executor is None",
		},
		{
			name:     "queen node missing",
			executor: &hiveserver.QueenExecutor{NodeRegistry: map[string]hiveserver.QueenNode{}},
			want:     "queen node is missing",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newLifecycleFixture(t)
			t.Cleanup(func() { fixture.cleanup(t) })

			source := hiveserver.NewSession("src-dm", eventbus.NewEventBus())
			source.QueenName = "queen_alpha"
			source.QueenExecutor = testCase.executor
			fixture.manager.AddSession(source)

			status, payload := postCreateColony(t, fixture, "forked", "src-dm")
			if status != http.StatusServiceUnavailable {
				t.Fatalf("create colony status = %d payload = %#v, want 503", status, payload)
			}
			if message, _ := payload["error"].(string); !strings.Contains(message, testCase.want) {
				t.Fatalf("create colony error = %q, want %q", message, testCase.want)
			}
			if _, err := os.Stat(fixture.home.ColonyDir("forked")); !os.IsNotExist(err) {
				t.Fatalf("refused fork left the colony on disk (stat err = %v)", err)
			}
			if source.ColonySpawned {
				t.Fatal("refused fork still locked the source session")
			}
		})
	}
}
