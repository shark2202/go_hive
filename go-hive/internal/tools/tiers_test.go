package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tierTool(name, desc string) Tool {
	if desc == "" {
		desc = "desc of " + name
	}
	return Tool{Name: name, Description: desc, Parameters: map[string]any{"type": "object"}}
}

func defaultTierPool() []Tool {
	return []Tool{
		tierTool("browser_interact", ""),
		tierTool("browser_upload", ""),
		tierTool("send_email", ""),
		tierTool("task_update", ""),
	}
}

type tierOpts struct {
	pool        []Tool
	always      map[string]struct{}
	gateable    map[string]struct{}
	allowlist   []string
	persistPath string
}

func defaultTierOpts() tierOpts {
	return tierOpts{
		pool:     defaultTierPool(),
		always:   tierNameSet("browser_interact"),
		gateable: tierNameSet("browser_interact", "browser_upload", "send_email"),
	}
}

func newTier(o tierOpts) *ToolTierState {
	s := &ToolTierState{
		Pool:               o.pool,
		AlwaysEnabledNames: o.always,
		GateableNames:      o.gateable,
		EnabledAllowlist:   o.allowlist,
		PersistPath:        o.persistPath,
	}
	s.Rebuild()
	return s
}

func tierNameSet(names ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

func tierNames(list []Tool) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, tool := range list {
		out[tool.Name] = struct{}{}
	}
	return out
}

func stringValues(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func runSearch(t *testing.T, handler func(map[string]any) (string, error), inputs map[string]any) map[string]any {
	t.Helper()
	out, err := handler(inputs)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v (raw: %s)", err, out)
	}
	return payload
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_split_partitions_pool
func TestTierSplitPartitionsPool(t *testing.T) {
	s := newTier(defaultTierOpts())

	eager := tierNames(s.GetCurrentTools())
	wantEager := tierNameSet("browser_interact", "task_update")
	searchable := tierNames(s.GetSearchableTools())
	wantSearchable := tierNameSet("browser_upload", "send_email")

	if !sameNameSet(eager, wantEager) {
		t.Errorf("eager = %v, want %v", eager, wantEager)
	}
	if !sameNameSet(searchable, wantSearchable) {
		t.Errorf("searchable = %v, want %v", searchable, wantSearchable)
	}
	if !sameNameSet(s.SearchableNames(), searchable) {
		t.Errorf("SearchableNames() = %v, want %v", s.SearchableNames(), searchable)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_empty_keep_set_disables_split
func TestTierEmptyKeepSetDisablesSplit(t *testing.T) {
	opts := defaultTierOpts()
	opts.always = map[string]struct{}{}
	s := newTier(opts)

	eager := tierNames(s.GetCurrentTools())
	want := tierNameSet("browser_interact", "browser_upload", "send_email", "task_update")
	if !sameNameSet(eager, want) {
		t.Errorf("eager = %v, want %v", eager, want)
	}
	if got := s.GetSearchableTools(); len(got) != 0 {
		t.Errorf("searchable = %v, want empty", tierNames(got))
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_eager_list_is_memoized_object
func TestTierEagerListIsMemoizedObject(t *testing.T) {
	s := newTier(defaultTierOpts())
	a := s.GetCurrentTools()
	b := s.GetCurrentTools()
	if len(a) == 0 {
		t.Fatal("eager list is empty; fixture changed")
	}
	if &a[0] != &b[0] {
		t.Error("GetCurrentTools returned a different backing array across calls")
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_promote_moves_to_eager_and_persists
func TestTierPromoteMovesToEagerAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_tiers.json")
	opts := defaultTierOpts()
	opts.persistPath = path
	s := newTier(opts)

	newly := s.PromoteSearchedTools([]string{"send_email", "send_email"})
	if len(newly) != 1 || newly[0] != "send_email" {
		t.Fatalf("newly promoted = %v, want [send_email]", newly)
	}
	if _, ok := tierNames(s.GetCurrentTools())["send_email"]; !ok {
		t.Error("send_email not in the eager set after promotion")
	}
	if _, ok := s.SearchableNames()["send_email"]; ok {
		t.Error("send_email still searchable after promotion")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persisted file missing: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("persisted file is not JSON: %v", err)
	}
	loaded := stringValues(parsed["loaded_tools"])
	if len(loaded) != 1 || loaded[0] != "send_email" {
		t.Errorf("persisted loaded_tools = %v, want [send_email]", loaded)
	}

	if again := s.PromoteSearchedTools([]string{"send_email"}); len(again) != 0 {
		t.Errorf("second promote returned %v, want []", again)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_restore_drops_unknown_and_disallowed
func TestTierRestoreDropsUnknownAndDisallowed(t *testing.T) {
	opts := defaultTierOpts()
	opts.allowlist = []string{"browser_upload"}
	s := newTier(opts)

	s.RestoreLoadedTools(
		[]string{"browser_upload", "gone_tool", "send_email"},
		tierNameSet("browser_upload", "send_email"),
	)
	if len(s.LoadedToolNames) != 1 || s.LoadedToolNames[0] != "browser_upload" {
		t.Errorf("loaded = %v, want [browser_upload]", s.LoadedToolNames)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_persist_roundtrip
func TestTierPersistRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_tiers.json")
	opts := defaultTierOpts()
	opts.persistPath = path
	s := newTier(opts)
	s.PromoteSearchedTools([]string{"browser_upload"})

	opts2 := defaultTierOpts()
	opts2.persistPath = path
	s2 := newTier(opts2)
	s2.RestoreLoadedTools(s2.LoadPersistedTools(), tierNames(s2.Pool))
	s2.Rebuild()

	if _, ok := tierNames(s2.GetCurrentTools())["browser_upload"]; !ok {
		t.Errorf("browser_upload not eager after roundtrip; loaded = %v", s2.LoadedToolNames)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestToolTierState::test_allowlist_gates_membership
func TestTierAllowlistGatesMembership(t *testing.T) {
	opts := defaultTierOpts()
	opts.allowlist = []string{"send_email"}
	s := newTier(opts)

	allowed := tierNames(s.filteredPool)
	if _, ok := allowed["browser_upload"]; ok {
		t.Errorf("browser_upload survived a restrictive allowlist: %v", allowed)
	}
	for _, name := range []string{"browser_interact", "send_email", "task_update"} {
		if _, ok := allowed[name]; !ok {
			t.Errorf("%s missing from the filtered pool: %v", name, allowed)
		}
	}
}

func sameNameSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for name := range a {
		if _, ok := b[name]; !ok {
			return false
		}
	}
	return true
}

// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_select_promotes_and_reports
func TestSearchToolsSelectPromotesAndReports(t *testing.T) {
	s := newTier(defaultTierOpts())
	_, handler := BuildSearchTools(s)

	payload := runSearch(t, handler, map[string]any{"query": "select:send_email"})
	loaded := stringValues(payload["loaded"])
	if len(loaded) != 1 || loaded[0] != "send_email" {
		t.Fatalf("loaded = %v, want [send_email]", loaded)
	}
	if _, ok := tierNames(s.GetCurrentTools())["send_email"]; !ok {
		t.Error("send_email not eager after select: promotion")
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_keyword_match
func TestSearchToolsKeywordMatch(t *testing.T) {
	opts := tierOpts{
		pool: []Tool{
			tierTool("send_email", "Send an email via the configured sender"),
			tierTool("browser_interact", ""),
		},
		always:   tierNameSet("browser_interact"),
		gateable: tierNameSet("send_email", "browser_interact"),
	}
	s := newTier(opts)
	_, handler := BuildSearchTools(s)

	payload := runSearch(t, handler, map[string]any{"query": "send email"})
	loaded := stringValues(payload["loaded"])
	if len(loaded) != 1 || loaded[0] != "send_email" {
		t.Fatalf("loaded = %v, want [send_email]", loaded)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_already_loaded_is_reported
func TestSearchToolsAlreadyLoadedIsReported(t *testing.T) {
	s := newTier(defaultTierOpts())
	_, handler := BuildSearchTools(s)

	payload := runSearch(t, handler, map[string]any{"query": "select:browser_interact"})
	if loaded := stringValues(payload["loaded"]); len(loaded) != 0 {
		t.Errorf("loaded = %v, want []", loaded)
	}
	found := false
	for _, name := range stringValues(payload["already_loaded"]) {
		if name == "browser_interact" {
			found = true
		}
	}
	if !found {
		t.Errorf("already_loaded = %v, want browser_interact", payload["already_loaded"])
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_schema_matches_queen_registration
func TestSearchToolsSchemaMatchesQueenRegistration(t *testing.T) {
	s := newTier(defaultTierOpts())
	schema, _ := BuildSearchTools(s)

	if schema.Name != "search_tools" {
		t.Errorf("name = %q, want search_tools", schema.Name)
	}
	if !strings.Contains(schema.Description, "select:name_a,name_b") {
		t.Errorf("description lost the select form: %q", schema.Description)
	}
	props, ok := schema.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties = %#v, want an object", schema.Parameters["properties"])
	}
	if len(props) != 2 {
		t.Errorf("property count = %d, want 2", len(props))
	}
	for _, key := range []string{"query", "max_results"} {
		if _, ok := props[key]; !ok {
			t.Errorf("missing property %q: %v", key, props)
		}
	}
}
