package colony

// Worker system-prompt assembly: the real worker lifecycle builds its prompt
// through agentloop.BuildSystemPromptPartsForContext, so the base worker prompt,
// the phase-filtered skills catalog and the operational protocols all reach the
// actual LLM request — and the goal rides the first user message, where
// upstream's _build_initial_message puts it.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// promptWiringRuntime builds a live colony whose worker prompt carries the
// test's base prompt, a colony skill, and an operational-protocols block.
func promptWiringRuntime(t *testing.T, colonyDir string, goal *schemas.Goal, provider llm.Completer) *ColonyRuntime {
	t.Helper()
	base := workerPromptBase
	spec := testAgentSpec()
	spec.SystemPrompt = &base
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:       spec,
		Goal:            goal,
		StoragePath:     colonyDir,
		ProtocolsPrompt: "PROTOCOLS SENTINEL: report_to_parent is your terminal channel.",
		LLM:             provider,
		Tools:           []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		StreamID: "worker_prompt_colony",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)
	return runtime
}

const workerPromptBase = "WORKER BASE PROMPT SENTINEL: focused task executor."

// Parity: framework/server/routes_execution.py::fork_session_into_colony +
// framework/agent_loop/agent_loop.py::_restore
// (build_system_prompt_parts_for_context composes the worker system prompt).
func TestWorkerPromptCarriesBaseCatalogAndProtocolsToTheLLM(t *testing.T) {
	colonyDir := t.TempDir()
	writeColonySkill(t, colonyDir, "prompt-lens", "Focus the lens on the task.")
	goalContext := schemas.NewOrderedMap()
	goalContext.Set("audience", "CFO")
	goal := &schemas.Goal{
		ID:   "prompt-wiring",
		Name: "Prompt wiring",
		Constraints: []schemas.Constraint{
			{ID: "cite", Description: "Cite every source", ConstraintType: "hard", Category: "quality"},
		},
		Context: goalContext,
	}
	provider := newScriptedLLM(textResponse("done"))
	runtime := promptWiringRuntime(t, colonyDir, goal, provider)

	if _, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "write the brief"}}, RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	request, ok := provider.firstCall()
	if !ok {
		t.Fatal("no LLM call recorded")
	}

	// The system message is the assembled prompt, not just the worker spec.
	system := request.System
	for _, want := range []string{
		workerPromptBase,
		"<available_skills>",
		"<name>prompt-lens</name>",
		"PROTOCOLS SENTINEL",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("worker system prompt missing %q:\n%s", want, system)
		}
	}
	// Upstream order: catalog and protocols are prefixed to the focus (base)
	// block, so the worker's own prompt closes the static prefix.
	if strings.Index(system, "<available_skills>") > strings.Index(system, workerPromptBase) {
		t.Fatalf("skills catalog must precede the worker base prompt:\n%s", system)
	}
	// The goal is not identity: it rides the first user turn instead.
	for _, unwanted := range []string{"Goal:", "[MUST] Cite every source", "audience: CFO"} {
		if strings.Contains(system, unwanted) {
			t.Fatalf("worker system prompt leaked goal content %q:\n%s", unwanted, system)
		}
	}
	firstUser := firstUserText(request.Messages)
	if !strings.HasPrefix(firstUser, "write the brief") {
		t.Fatalf("first user message = %q, want the task frame first", firstUser)
	}
	for _, want := range []string{"\n\nGoal: ", "[MUST] Cite every source", "audience: CFO"} {
		if !strings.Contains(firstUser, want) {
			t.Fatalf("first user message missing the goal block %q:\n%s", want, firstUser)
		}
	}
}

// refreshScriptLLM runs one hook between its first and second completions, so a
// test can reload the colony catalog while the worker is mid-flight.
type refreshScriptLLM struct {
	mu       sync.Mutex
	calls    []llm.CompletionRequest
	onFirst  func()
	finished bool
}

func (m *refreshScriptLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	index := len(m.calls) - 1
	m.mu.Unlock()
	if index == 0 {
		if m.onFirst != nil {
			m.onFirst()
		}
		// A tool turn keeps the loop alive: the refreshed prompt is visible on
		// the next model call.
		return toCompletion(toolResponse("t1", "task_list", `{}`)), nil
	}
	m.mu.Lock()
	m.finished = true
	m.mu.Unlock()
	return toCompletion(reportResponse("success", "catalog refreshed", nil)), nil
}

func (m *refreshScriptLLM) request(i int) (llm.CompletionRequest, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i >= len(m.calls) {
		return llm.CompletionRequest{}, false
	}
	return m.calls[i], true
}

// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6b3) +
// framework/host/colony_runtime.py::ColonyRuntime.reload_skills — a catalog
// reload (a queen UI override) reaches an in-flight worker's next model call.
func TestWorkerPromptPicksUpCatalogReloadMidRun(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colonies", "prompt-colony")
	queenDir := filepath.Join(root, "queens", "queen_ada")
	writeSkillFile(t, filepath.Join(colonyDir, "skills"), "live-skill", "Live skill description.", "Do the live thing.")

	// The queen's UI override starts enabled, so the first prompt announces it.
	storePath := filepath.Join(queenDir, "skills_overrides.json")
	store := skills.LoadSkillOverrideStore(storePath, "queen:queen_ada")
	enabled := true
	store.Upsert("live-skill", &skills.OverrideEntry{Enabled: &enabled, Provenance: skills.ProvenanceUserUICreated})
	if err := store.Save(); err != nil {
		t.Fatalf("save override store: %v", err)
	}

	provider := &refreshScriptLLM{}
	base := workerPromptBase
	spec := testAgentSpec()
	spec.SystemPrompt = &base
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:       spec,
		Goal:            testGoal(),
		StoragePath:     colonyDir,
		QueenDir:        queenDir,
		ProtocolsPrompt: "PROTOCOLS SENTINEL",
		LLM:             provider,
		Tools:           []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		StreamID: "worker_prompt_reload",
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(runtime.Stop)

	// Mid-run, the queen toggles the skill off and the colony reloads.
	provider.onFirst = func() {
		disabled := false
		store.Upsert("live-skill", &skills.OverrideEntry{Enabled: &disabled, Provenance: skills.ProvenanceUserUICreated})
		if err := store.Save(); err != nil {
			t.Errorf("save override store: %v", err)
			return
		}
		if _, err := runtime.ReloadSkills(); err != nil {
			t.Errorf("ReloadSkills: %v", err)
		}
	}

	if _, err := runtime.RunBatch(context.Background(), []BatchTask{{Task: "do the live thing"}}, RunSpawnOptions{}); err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	first, ok := provider.request(0)
	if !ok {
		t.Fatal("no first LLM call")
	}
	second, ok := provider.request(1)
	if !ok {
		t.Fatal("worker ended before a second model call; the refresh was never observed")
	}
	if !strings.Contains(first.System, "<name>live-skill</name>") {
		t.Fatalf("spawn-time system prompt missing the enabled skill:\n%s", first.System)
	}
	if strings.Contains(second.System, "<name>live-skill</name>") {
		t.Fatalf("in-flight prompt kept the disabled skill after ReloadSkills:\n%s", second.System)
	}
	// Only the catalog changed: the base prompt and protocols prefix stay
	// byte-identical, so the rebuild does not respell unrelated sections.
	for _, want := range []string{workerPromptBase, "<available_skills>", "PROTOCOLS SENTINEL"} {
		if !strings.Contains(second.System, want) {
			t.Fatalf("refreshed prompt missing %q:\n%s", want, second.System)
		}
	}
	if got, want := systemPrefixBeforeCatalog(second.System), systemPrefixBeforeCatalog(first.System); got != want {
		t.Fatalf("prompt prefix churned across the reload:\n got: %q\nwant: %q", got, want)
	}
}

// systemPrefixBeforeCatalog is everything the prompt renders before the skills
// section — identity/accounts, i.e. the parts a catalog reload must not touch.
func systemPrefixBeforeCatalog(prompt string) string {
	if index := strings.Index(prompt, "<available_skills>"); index >= 0 {
		return prompt[:index]
	}
	return prompt
}

// Parity: framework/agent_loop/prompting.py::build_system_prompt_static — an
// absent catalog and empty protocols render the base prompt with no empty
// section and no stray separators, and the worker lifecycle still applies it.
func TestWorkerPromptWithAbsentCatalogReachesTheLLMUnchanged(t *testing.T) {
	goal := &schemas.Goal{
		ID:   "absent-catalog",
		Name: "Absent catalog",
		Constraints: []schemas.Constraint{
			{ID: "cite", Description: "Cite every source", ConstraintType: "hard", Category: "quality"},
		},
	}
	spec := testAgentSpec()
	base := workerPromptBase
	spec.SystemPrompt = &base
	provider := newScriptedLLM(textResponse("done"))
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   spec,
		Goal:        goal,
		StoragePath: t.TempDir(),
		LLM:         provider,
		Tools:       []llm.ToolDef{},
		ToolExecutor: func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
			return agentloop.ToolResult{Content: "ok"}, nil
		},
		StreamID: "worker_prompt_absent",
	})
	worker := runtime.BuildWorker(WorkerBuildOptions{WorkerID: "w_absent", Task: "do the thing"})
	if worker.Context == nil {
		t.Fatal("worker context is nil")
	}
	// No catalog, no protocols: the builder must render the base prompt alone.
	worker.Context.SkillsCatalogPrompt = ""
	worker.Context.DynamicSkillsCatalogProvider = nil
	worker.Context.ProtocolsPrompt = ""

	runtime.runWorker(context.Background(), worker)

	request, ok := provider.firstCall()
	if !ok {
		t.Fatal("no LLM call recorded")
	}
	// Upstream's static builder prefixes the focus block with "\n"; with no
	// identity/binding/accounts/catalog/protocols/memory above it, that is the
	// whole prompt: one separator plus the worker's base prompt, no sections.
	want := "\n" + workerPromptBase
	if request.System != want {
		t.Fatalf("system prompt = %q, want the bare base prompt %q", request.System, want)
	}
	if strings.Contains(request.System, "<available_skills>") || strings.Contains(request.System, "Operational Protocols") {
		t.Fatalf("absent catalog/protocols still rendered a section:\n%s", request.System)
	}
	firstUser := firstUserText(request.Messages)
	if !strings.HasPrefix(firstUser, "do the thing") || !strings.Contains(firstUser, "\n\nGoal: ") {
		t.Fatalf("first user message = %q, want the task frame plus the goal block", firstUser)
	}
}
