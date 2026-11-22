package cli

// Flat default tool surface: contract tests.
//
// These tests pin the model-visible default surface to the frozen Python
// reference (contracts/tool-definitions.json, layers llm_request ∪ lifecycle ∪
// synthetic = 39 unique names) and pin the grouped fallback switch. They are
// Go-side contract checks over a frozen artifact, so they carry the
// Parity-exempt tag (there is no Python unit test with the same scope; the
// Python facts are the captured request/library layers recorded in the
// contract, see go_hive/docs/tool-flat-surface.md).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// flatReadFile keeps the contract read local to the flat-surface tests.
func flatReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// flatJSONEqual compares two decoded JSON schemas by canonical encoding (Go's
// encoder sorts map keys, so byte equality is schema equality).
func flatJSONEqual(left, right any) bool {
	l, lerr := json.Marshal(left)
	r, rerr := json.Marshal(right)
	if lerr != nil || rerr != nil {
		return false
	}
	return string(l) == string(r)
}

// flatContainsName reports whether the name appears in the list.
func flatContainsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// contractPath is the frozen Python⇄Go contract, shared with the report-only
// freezer test.
func contractPath() string {
	return filepath.Join("..", "..", "..", "contracts", "tool-definitions.json")
}

// contractDefaultTools recomputes the Python default set (the three default
// layers) straight from the contract file at test time, so the embedded
// manifest cannot silently drift from the frozen reference.
type contractDefaultEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Layers      []string       `json:"layers"`
}

func loadContractDefaultTools(t *testing.T) map[string]contractDefaultEntry {
	t.Helper()
	raw, err := flatReadFile(contractPath())
	if err != nil {
		t.Skipf("contract manifest not present (%v)", err)
	}
	var doc struct {
		Tools []struct {
			Name        string                    `json:"name"`
			Description string                    `json:"description"`
			InputSchema map[string]any            `json:"input_schema"`
			Layers      []string                  `json:"layers"`
			LayerDetail map[string]map[string]any `json:"layer_detail"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}
	out := map[string]contractDefaultEntry{}
	for _, tool := range doc.Tools {
		// A feature-gated llm_request tool (search_timeline behind
		// memory_timeline) is only advertised when its gate is on; the default
		// surface pinned here is the gate-off surface.
		if detail, ok := tool.LayerDetail["llm_request"]; ok {
			if _, gated := detail["feature_gate"]; gated {
				continue
			}
		}
		defaultLayer := false
		for _, layer := range tool.Layers {
			if layer == "llm_request" || layer == "lifecycle" || layer == "synthetic" {
				defaultLayer = true
			}
		}
		if !defaultLayer {
			continue
		}
		out[tool.Name] = contractDefaultEntry{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
			Layers:      tool.Layers,
		}
	}
	return out
}

// queenFlatCatalogue builds the catalogue a queen chat session gets: the
// session catalogue plus the ten queen lifecycle definitions
// (enableQueenSessionTools is called after construction on the real path).
func queenFlatCatalogue(t *testing.T) *sessionToolCatalogue {
	t.Helper()
	root := testTempDir(t)
	home := hivehome.Home{Root: root}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	enableQueenLifecycleTools(catalogue, &hiveserver.Session{}, home)
	return catalogue
}

func defNames(defs []llm.ToolDef) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

// TestFlatSurfaceDefaultMatchesPythonContract pins the model-visible default
// surface to the manifest's three default layers: same names, same count, and
// no grouped integration module in the list.
//
// The per-turn colony gate (server.ChatRuntime.toolsForTurn) drops exactly the
// four colony-phase tools from the advertised array, matching upstream's
// colony-phase list; that behaviour is covered by
// internal/server/chat_runtime_worker_report_test.go and
// queen_lifecycle_actions_test.go.
//
// Parity-exempt: freezes the captured Python request/library layers
// (docs/tool-contract-diff.md §11) onto the Go default surface; no Python test
// asserts the Go catalogue shape.
func TestFlatSurfaceDefaultMatchesPythonContract(t *testing.T) {
	contract := loadContractDefaultTools(t)
	catalogue := queenFlatCatalogue(t)
	got := defNames(catalogue.ModelDefs())

	want := make([]string, 0, len(contract)+len(flatSurfaceColonyPhaseOnly))
	for name := range contract {
		want = append(want, name)
	}
	// Colony-phase-only extensions are not in the frozen default set (the
	// Python capture session had no colony); they still ride the flat surface
	// so a colony-bound turn advertises them, and the server gate hides them
	// otherwise.
	for name := range flatSurfaceColonyPhaseOnly {
		want = append(want, name)
	}
	sort.Strings(want)
	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	if strings.Join(gotSorted, ",") != strings.Join(want, ",") {
		t.Fatalf("default flat surface mismatch:\n got %d: %v\nwant %d: %v",
			len(got), got, len(want), want)
	}
	if len(got) != 39+len(flatSurfaceColonyPhaseOnly) {
		t.Fatalf("default flat surface has %d tools, want %d (24 llm_request + 16 lifecycle + 1 synthetic, deduped, + %d colony-phase-only)",
			len(got), 39+len(flatSurfaceColonyPhaseOnly), len(flatSurfaceColonyPhaseOnly))
	}

	present := map[string]struct{}{}
	for _, name := range got {
		if _, dup := present[name]; dup {
			t.Fatalf("duplicate tool name in the default surface: %s", name)
		}
		present[name] = struct{}{}
	}
	// The grouped integration modules stay registered but never advertised.
	for _, grouped := range []string{"airtable", "slack", "stripe", "csv", "excel", "zoom"} {
		if _, leak := present[grouped]; leak {
			t.Fatalf("grouped tool %q leaked into the default flat surface", grouped)
		}
		if !catalogue.registry.HasTool(grouped) {
			t.Fatalf("grouped tool %q must stay in the internal registry", grouped)
		}
	}
	// Colony-phase gate: the first request of a plain chat session carries the
	// default set minus the colony-runtime tools (upstream semantics).
	advertised := len(got)
	for _, name := range flatSurfaceColonyPhaseGated {
		if _, ok := present[name]; !ok {
			t.Fatalf("colony-gated tool %q missing from the default surface", name)
		}
		advertised--
	}
	if advertised != 39+len(flatSurfaceColonyPhaseOnly)-len(flatSurfaceColonyPhaseGated) {
		t.Fatalf("expected %d advertised tools before a colony binds, computed %d",
			39+len(flatSurfaceColonyPhaseOnly)-len(flatSurfaceColonyPhaseGated), advertised)
	}
	t.Logf("default flat surface: catalogue %d, first request (no colony) %d, grouped pool %d",
		len(got), advertised, len(catalogue.Defs()))
}

// TestFlatSurfaceSchemasMatchFrozenContract checks every advertised default
// tool against the frozen contract: description and input schema are either the
// verbatim Python values or one of the two documented exceptions, each of which
// must keep the live definition.
//
// Parity-exempt: contract check over the frozen artifact.
func TestFlatSurfaceSchemasMatchFrozenContract(t *testing.T) {
	contract := loadContractDefaultTools(t)
	catalogue := queenFlatCatalogue(t)
	live := map[string]llm.ToolDef{}
	for _, def := range catalogue.Defs() {
		if _, dup := live[def.Name]; !dup {
			live[def.Name] = def
		}
	}
	parityManifest := toolContractParityManifestData()

	verbatim, keptP1, keptLifecycle := 0, []string{}, []string{}
	keptColonyOnly := []string{}
	for _, def := range catalogue.ModelDefs() {
		entry, ok := contract[def.Name]
		if !ok {
			// Colony-phase-only extensions are not in the frozen default set;
			// their live definition must be the Python Tool(...) text (the
			// server gate keeps them off unbound sessions).
			if _, colonyOnly := flatSurfaceColonyPhaseOnly[def.Name]; colonyOnly {
				if liveDef, ok := live[def.Name]; !ok || liveDef.Description != def.Description || !flatJSONEqual(liveDef.Parameters, def.Parameters) {
					t.Fatalf("colony-phase-only tool %q drifted from its live definition", def.Name)
				}
				keptColonyOnly = append(keptColonyOnly, def.Name)
				continue
			}
			t.Fatalf("advertised tool %q is not in the frozen default set", def.Name)
		}
		if def.Description == entry.Description && flatJSONEqual(def.Parameters, entry.InputSchema) {
			verbatim++
			continue
		}
		// Documented exception 1: the P1 parity manifest already froze this
		// tool's schema against Python; the flat surface must not rewrite it.
		if _, p1 := parityManifest.Tools[def.Name]; p1 {
			if liveDef, ok := live[def.Name]; !ok || liveDef.Description != def.Description || !flatJSONEqual(liveDef.Parameters, def.Parameters) {
				t.Fatalf("P1-frozen tool %q drifted from its live definition", def.Name)
			}
			keptP1 = append(keptP1, def.Name)
			continue
		}
		// Documented exception 2: lifecycle layer (name + description only in
		// the Python Tool Library); the live Go schema is the executable
		// contract and is kept.
		lifecycleOnly := false
		for _, layer := range entry.Layers {
			if layer == "lifecycle" {
				lifecycleOnly = true
			}
			if layer == "llm_request" || layer == "synthetic" {
				lifecycleOnly = false
				break
			}
		}
		if lifecycleOnly {
			if liveDef, ok := live[def.Name]; !ok || liveDef.Description != def.Description || !flatJSONEqual(liveDef.Parameters, def.Parameters) {
				t.Fatalf("lifecycle tool %q drifted from its live definition", def.Name)
			}
			keptLifecycle = append(keptLifecycle, def.Name)
			continue
		}
		t.Fatalf("tool %q differs from the frozen contract without a documented exception (desc equal=%v)",
			def.Name, def.Description == entry.Description)
	}
	sort.Strings(keptP1)
	sort.Strings(keptLifecycle)
	sort.Strings(keptColonyOnly)
	if verbatim+len(keptP1)+len(keptLifecycle)+len(keptColonyOnly) != 39+len(flatSurfaceColonyPhaseOnly) {
		t.Fatalf("schema accounting: verbatim %d + keepP1 %d + keepLifecycle %d + colonyOnly %d != %d",
			verbatim, len(keptP1), len(keptLifecycle), len(keptColonyOnly), 39+len(flatSurfaceColonyPhaseOnly))
	}
	t.Logf("schema parity: verbatim %d/%d, P1-kept %v, lifecycle-kept %v, colony-phase-only %v",
		verbatim, 39+len(flatSurfaceColonyPhaseOnly), keptP1, keptLifecycle, keptColonyOnly)

	// The runtime's ask_user synthetic must match the frozen contract too:
	// chatToolsWithAskUser keeps the already-advertised definition.
	synthetic := agentloop.BuildAskUserTool()
	entry, ok := contract["ask_user"]
	if !ok {
		t.Fatal("ask_user missing from the frozen default set")
	}
	if synthetic.Description != entry.Description || !flatJSONEqual(synthetic.Parameters, entry.InputSchema) {
		t.Fatal("agentloop.BuildAskUserTool no longer matches the frozen contract")
	}
}

// TestFlatSurfaceGroupedSwitch checks the fallback: HIVE_TOOL_SURFACE=grouped
// returns the complete catalogue (grouped modules included), and any other
// value keeps the flat default so grouping cannot leak in by typo.
//
// Parity-exempt: Go-side surface switch over the frozen contract.
func TestFlatSurfaceGroupedSwitch(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	pool := defNames(catalogue.Defs())

	t.Setenv(ToolSurfaceEnvVar, ToolSurfaceGrouped)
	grouped := defNames(catalogue.ModelDefs())
	if strings.Join(grouped, ",") != strings.Join(pool, ",") {
		t.Fatalf("grouped switch did not restore the full catalogue (%d vs %d names)", len(grouped), len(pool))
	}
	if !flatContainsName(grouped, "airtable") {
		t.Fatal("grouped surface is missing the airtable module")
	}

	t.Setenv(ToolSurfaceEnvVar, "definitely-not-grouped")
	flat := defNames(catalogue.ModelDefs())
	if len(flat) != 39+len(flatSurfaceColonyPhaseOnly) {
		t.Fatalf("unknown switch value fell back to %d tools instead of the flat default", len(flat))
	}
	if flatContainsName(flat, "airtable") {
		t.Fatal("unknown switch value exposed the grouped surface")
	}

	// No switch at all (the default) keeps the flat surface.
	t.Setenv(ToolSurfaceEnvVar, "")
	if names := defNames(catalogue.ModelDefs()); len(names) != 39+len(flatSurfaceColonyPhaseOnly) {
		t.Fatalf("default surface has %d tools, want %d", len(names), 39+len(flatSurfaceColonyPhaseOnly))
	}
}

// TestFlatSurfaceExecutionRoutes checks that the flat names execute through
// the existing implementations: the playbook-control trio maps onto the
// run_playbook actions (with the actionable no-colony error, not an unknown
// tool), and the three not-yet-ported names answer with an explicit
// not_implemented envelope instead of failing silently. sentinel_setup has its
// own execution route now — see TestSentinelSetupExecutesThroughFlatSurface.
//
// Parity-exempt: routing check over the frozen contract; the actions it maps
// onto are covered by their own parity tests.
func TestFlatSurfaceExecutionRoutes(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	ctx := context.Background()

	for _, name := range []string{"get_playbook_status", "list_playbook_runs", "stop_playbook"} {
		content, isError, err := catalogue.Execute(ctx, name, map[string]any{"run_id": "run_x"})
		if err != nil {
			t.Fatalf("%s: unexpected error %v", name, err)
		}
		if !isError || !strings.Contains(content, "colony") {
			t.Fatalf("%s: want the actionable no-colony error, got isError=%v content=%s", name, isError, content)
		}
		if strings.Contains(content, "unknown tool") {
			t.Fatalf("%s: routed to the registry instead of run_playbook", name)
		}
	}

	for _, name := range []string{"collect_result", "credentials", "suggest_colony"} {
		content, isError, err := catalogue.Execute(ctx, name, map[string]any{})
		if err != nil {
			t.Fatalf("%s: unexpected error %v", name, err)
		}
		if !isError || !strings.Contains(content, "not_implemented") {
			t.Fatalf("%s: want a not_implemented envelope, got isError=%v content=%s", name, isError, content)
		}
	}

	content, isError, err := catalogue.Execute(ctx, "search_tools", map[string]any{"query": "inventory"})
	if err != nil || isError {
		t.Fatalf("search_tools inventory fallback: err=%v isError=%v", err, isError)
	}
	if !strings.Contains(content, "\"searchable\"") || !strings.Contains(content, "\"loaded\"") {
		t.Fatalf("search_tools fallback is not an inventory envelope: %s", content)
	}

	// A mapped existing tool still runs: task_list reads the session task list.
	content, isError, err = catalogue.Execute(ctx, "task_list", map[string]any{})
	if err != nil || isError {
		t.Fatalf("task_list through the flat surface: err=%v isError=%v content=%s", err, isError, content)
	}
}

// TestFlatSurfaceManifestMatchesContractFile re-derives the embedded manifest
// from contracts/tool-definitions.json and requires byte-for-byte agreement on
// every name/description/input_schema, so the embedded artifact cannot drift
// from the frozen reference.
//
// Parity-exempt: Go-side freeze check (regenerate with
// go_hive/scripts/gen_tool_surface_flat.py).
func TestFlatSurfaceManifestMatchesContractFile(t *testing.T) {
	contract := loadContractDefaultTools(t)
	manifest := flatSurfaceManifest()
	if got, want := len(manifest.Tools), len(contract); got != want {
		t.Fatalf("embedded manifest has %d tools, contract default set has %d", got, want)
	}
	names := make([]string, 0, len(manifest.Tools))
	for _, entry := range manifest.Tools {
		names = append(names, entry.Name)
	}
	canonical := []string{}
	for name := range contract {
		canonical = append(canonical, name)
	}
	sort.Strings(canonical)
	if strings.Join(names, ",") != strings.Join(canonical, ",") {
		t.Fatalf("embedded manifest order/names diverge from the contract:\n got %v\nwant %v", names, canonical)
	}
	for _, entry := range manifest.Tools {
		ref := contract[entry.Name]
		if entry.Description != ref.Description {
			t.Fatalf("%s: description differs from the contract file", entry.Name)
		}
		if !flatJSONEqual(entry.InputSchema, ref.InputSchema) {
			t.Fatalf("%s: input_schema differs from the contract file", entry.Name)
		}
	}
	if got := manifest.LayerCounts["llm_request"] + manifest.LayerCounts["lifecycle"] + manifest.LayerCounts["synthetic"]; got != 41 {
		t.Fatalf("layer counts %v do not sum to 41 entries (39 unique names)", manifest.LayerCounts)
	}
}

// TestFlatSurfaceEagerOrderMatchesUpstreamCapture pins the flat lazy layer's
// always-on set to the captured Python first request: 24 names, the exact wire
// order of toolresidual/out/py-gateway.jsonl row 0 (2026-11-21), and the same
// name set as the frozen manifest's llm_request layer.
//
// Parity: core/framework/agents/queen/queen_tools_defaults.py::always_enabled_tool_names
// + QueenPhaseState.rebuild_independent_filter (eager list order).
// Parity-exempt at the unit level: the assertion is against the captured
// reference request recorded in go_hive/docs/tool-flat-surface.md §5.2.
func TestFlatSurfaceEagerOrderMatchesUpstreamCapture(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	got := catalogue.EagerModelDefNames()
	want := []string{
		"search_tools", "write_skill", "task_create", "task_update", "task_list", "task_get",
		"suggest_colony", "pdf_read", "attach_file", "get_current_time", "image_generate",
		"get_account_info", "terminal_exec", "terminal_job_logs", "terminal_job_manage",
		"terminal_rg", "terminal_glob", "terminal_output_get", "search_messages",
		"browser_setup", "collect_result", "ask_user", "credentials", "sentinel_setup",
	}
	if len(got) != 24 {
		t.Fatalf("eager surface has %d tools, want 24 (Python first request): %v", len(got), got)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("eager surface order mismatch:\n got %v\nwant %v", got, want)
	}

	// Same set as the frozen llm_request layer, so the order pin cannot drift
	// away from the captured request it reproduces.
	manifest := flatSurfaceManifest()
	layer := append([]string(nil), manifest.Layers["llm_request"]...)
	sortedGot := append([]string(nil), got...)
	sort.Strings(layer)
	sort.Strings(sortedGot)
	if strings.Join(sortedGot, ",") != strings.Join(layer, ",") {
		t.Fatalf("eager set %v != manifest llm_request layer %v", sortedGot, layer)
	}

	// Every eager name is already part of the default flat surface; the lazy
	// layer must not invent names.
	advertised := defNames(catalogue.ModelDefs())
	for _, name := range got {
		if !flatContainsName(advertised, name) {
			t.Fatalf("eager tool %q is not in the flat default surface", name)
		}
	}
}

// TestFlatSurfaceLazyLayerPartition checks the deferred layer: disjoint from
// the eager set, exactly the 15 remaining default-surface names plus the flat
// directory tools, no grouped module, and every entry has an execution route.
//
// Parity: core/framework/tools/tool_tiers.py::ToolTierState.get_searchable_tools
func TestFlatSurfaceLazyLayerPartition(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	eager, deferred := catalogue.LazySurfaceDeps()
	eagerSet := map[string]struct{}{}
	for _, name := range eager {
		eagerSet[name] = struct{}{}
	}
	if len(eager) != 24 {
		t.Fatalf("eager surface = %d, want 24", len(eager))
	}
	if len(deferred) == 0 {
		t.Fatal("deferred layer is empty")
	}

	seen := map[string]struct{}{}
	var leftovers, directory []string
	defaultNames := map[string]struct{}{}
	for _, name := range flatSurfaceDefaultNames() {
		defaultNames[name] = struct{}{}
	}
	for _, def := range deferred {
		if def.Name == "" {
			t.Fatal("deferred layer has an empty name")
		}
		if _, dup := seen[def.Name]; dup {
			t.Fatalf("deferred layer has duplicate %q", def.Name)
		}
		seen[def.Name] = struct{}{}
		if _, isEager := eagerSet[def.Name]; isEager {
			t.Fatalf("deferred tool %q is also eager", def.Name)
		}
		if _, isDefault := defaultNames[def.Name]; isDefault {
			leftovers = append(leftovers, def.Name)
		} else {
			directory = append(directory, def.Name)
		}
	}
	if len(leftovers) != 15 {
		t.Fatalf("default-surface leftovers = %d, want 15 (39 default names - 24 eager): %v", len(leftovers), leftovers)
	}
	if len(directory) == 0 {
		t.Fatal("directory layer is empty")
	}
	for _, want := range []string{
		"chart_render", "web_scrape", "web_search", "read_file", "write_file",
		"edit_file", "search_files", "tracker_sql", "skill_view", "list_skills",
		"credential_status", "terminal_pty_open", "run_playbook", "stop_worker",
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("deferred layer is missing %q", want)
		}
	}
	// Grouped integration modules are module+action tools, not flat names:
	// they stay out of the lazy layer (registered + grouped surface only).
	for _, grouped := range []string{"airtable", "slack", "stripe", "notion", "csv", "excel", "telegram"} {
		if _, leak := seen[grouped]; leak {
			t.Fatalf("grouped module %q leaked into the flat lazy layer", grouped)
		}
		if !catalogue.registry.HasTool(grouped) {
			t.Fatalf("grouped module %q must stay in the internal registry", grouped)
		}
	}
	// Every deferred name must land on a real route: a live catalogue def, a
	// standalone flat name, or a run_playbook action mapping. A deferred name
	// with no route would be advertised-but-unknown (capability loss).
	live := map[string]struct{}{}
	for _, def := range catalogue.defs {
		live[def.Name] = struct{}{}
	}
	for _, def := range deferred {
		_, hasDef := live[def.Name]
		_, standalone := flatSurfaceStandaloneTools[def.Name]
		_, playbook := flatSurfacePlaybookActions[def.Name]
		if !hasDef && !standalone && !playbook {
			t.Fatalf("deferred tool %q has no execution route", def.Name)
		}
	}
}

// TestFlatSurfaceDeferredToolsExecute proves the deferred layer keeps real
// execution: a loadable file tool really reads the file and a lifecycle name
// routes to its existing colony implementation (actionable no-colony error),
// never to an unknown-tool error.
//
// Parity-exempt: routing check over the frozen contract, same spirit as
// TestFlatSurfaceExecutionRoutes.
func TestFlatSurfaceDeferredToolsExecute(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	ctx := context.Background()

	root := catalogue.root
	if err := os.WriteFile(filepath.Join(root, "lazy.txt"), []byte("deferred tool ran"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	content, isError, err := catalogue.Execute(ctx, "read_file", map[string]any{"path": "lazy.txt"})
	if err != nil || isError {
		t.Fatalf("read_file (deferred) failed: err=%v isError=%v content=%s", err, isError, content)
	}
	if !strings.Contains(content, "deferred tool ran") {
		t.Fatalf("read_file (deferred) did not return the file contents: %s", content)
	}

	content, isError, err = catalogue.Execute(ctx, "run_playbook", map[string]any{})
	if err != nil {
		t.Fatalf("run_playbook (deferred): unexpected error %v", err)
	}
	if !isError || !strings.Contains(content, "colony") {
		t.Fatalf("run_playbook (deferred): want the actionable no-colony error, got isError=%v content=%s", isError, content)
	}
	if strings.Contains(content, "unknown tool") {
		t.Fatalf("run_playbook (deferred) routed to the registry: %s", content)
	}
}
