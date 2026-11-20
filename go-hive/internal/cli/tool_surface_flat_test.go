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

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
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
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
			Layers      []string       `json:"layers"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}
	out := map[string]contractDefaultEntry{}
	for _, tool := range doc.Tools {
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
	root := t.TempDir()
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

	want := make([]string, 0, len(contract))
	for name := range contract {
		want = append(want, name)
	}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("default flat surface mismatch:\n got %d: %v\nwant %d: %v",
			len(got), got, len(want), want)
	}
	if len(got) != 39 {
		t.Fatalf("default flat surface has %d tools, want 39 (24 llm_request + 16 lifecycle + 1 synthetic, deduped)", len(got))
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
	// default set minus the four colony-runtime tools (upstream semantics).
	advertised := len(got)
	for _, name := range flatSurfaceColonyPhaseGated {
		if _, ok := present[name]; !ok {
			t.Fatalf("colony-gated tool %q missing from the default surface", name)
		}
		advertised--
	}
	if advertised != 39-len(flatSurfaceColonyPhaseGated) {
		t.Fatalf("expected %d advertised tools before a colony binds, computed %d",
			39-len(flatSurfaceColonyPhaseGated), advertised)
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
	for _, def := range catalogue.ModelDefs() {
		entry, ok := contract[def.Name]
		if !ok {
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
	if verbatim+len(keptP1)+len(keptLifecycle) != 39 {
		t.Fatalf("schema accounting: verbatim %d + keepP1 %d + keepLifecycle %d != 39",
			verbatim, len(keptP1), len(keptLifecycle))
	}
	t.Logf("schema parity: verbatim %d/39, P1-kept %v, lifecycle-kept %v",
		verbatim, keptP1, keptLifecycle)

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
	if len(flat) != 39 {
		t.Fatalf("unknown switch value fell back to %d tools instead of the flat default", len(flat))
	}
	if flatContainsName(flat, "airtable") {
		t.Fatal("unknown switch value exposed the grouped surface")
	}

	// No switch at all (the default) keeps the flat surface.
	t.Setenv(ToolSurfaceEnvVar, "")
	if names := defNames(catalogue.ModelDefs()); len(names) != 39 {
		t.Fatalf("default surface has %d tools, want 39", len(names))
	}
}

// TestFlatSurfaceExecutionRoutes checks that the flat names execute through
// the existing implementations: the playbook-control trio maps onto the
// run_playbook actions (with the actionable no-colony error, not an unknown
// tool), and the four not-yet-ported names answer with an explicit
// not_implemented envelope instead of failing silently.
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

	for _, name := range []string{"collect_result", "credentials", "sentinel_setup", "suggest_colony"} {
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
