package cli

// Harness-group catalog assembly guard: the five upstream harness groups must
// be visible on the session registry even though their tools execute through
// the builtin sandbox, the session tables or the integration registry.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tools/integrations"
)

// harnessGroupCatalogNames returns the recorded catalog names for one group.
func harnessGroupCatalogNames(t *testing.T, catalogue *sessionToolCatalogue, group string) []string {
	t.Helper()
	entries := catalogue.registry.GetFullMCPCatalog()[group]
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		if name == "" {
			t.Fatalf("%s catalog entry has no name: %#v", group, entry)
		}
		schema, _ := entry["input_schema"].(map[string]any)
		if schema == nil || schema["type"] != "object" {
			t.Fatalf("%s.%s input_schema = %#v", group, name, entry["input_schema"])
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func equalNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// Parity: core/tests/test_harness_tools.py::test_every_group_builds
func TestSessionCatalogueRegistersEveryHarnessGroup(t *testing.T) {
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	catalog := catalogue.registry.GetFullMCPCatalog()
	for _, group := range []string{harnessGroupTerminal, "files-tools", harnessGroupChart, harnessGroupMemory, harnessGroupHive} {
		if _, ok := catalog[group]; !ok {
			t.Fatalf("harness group %q is not recorded; catalog = %v", group, catalog)
		}
	}

	wantTerminal := append([]string(nil), terminalHarnessToolNames...)
	sort.Strings(wantTerminal)
	if got := harnessGroupCatalogNames(t, catalogue, harnessGroupTerminal); !equalNames(got, wantTerminal) {
		t.Fatalf("terminal-tools = %v, want %v", got, wantTerminal)
	}

	wantFiles := []string{builtin.ToolEditFile, builtin.ToolReadFile, builtin.ToolSearchFiles, builtin.ToolWriteFile}
	sort.Strings(wantFiles)
	if got := harnessGroupCatalogNames(t, catalogue, "files-tools"); !equalNames(got, wantFiles) {
		t.Fatalf("files-tools = %v, want %v", got, wantFiles)
	}

	if got := harnessGroupCatalogNames(t, catalogue, harnessGroupChart); !equalNames(got, []string{chartRenderToolName}) {
		t.Fatalf("chart-tools = %v, want [%s]", got, chartRenderToolName)
	}

	wantHive := append([]string(nil), hiveCoreHarnessToolNames...)
	sort.Strings(wantHive)
	gotHive := harnessGroupCatalogNames(t, catalogue, harnessGroupHive)
	if !equalNames(gotHive, wantHive) {
		t.Fatalf("hive_tools = %v, want %v", gotHive, wantHive)
	}
	// The Go-only aggregate names and the unported SQL tools are not upstream
	// hive_tools members.
	for _, absent := range []string{"csv", "excel", "csv_sql", "excel_sql"} {
		for _, name := range gotHive {
			if name == absent {
				t.Fatalf("hive_tools catalog unexpectedly holds %q", absent)
			}
		}
	}

	// The registry's server bookkeeping must agree with the tool library view.
	for group, names := range map[string][]string{
		harnessGroupTerminal: terminalHarnessToolNames,
		harnessGroupChart:    {chartRenderToolName},
		harnessGroupHive:     hiveCoreHarnessToolNames,
	} {
		recorded := catalogue.registry.GetServerToolNames(group)
		for _, name := range names {
			if _, ok := recorded[name]; !ok {
				t.Fatalf("%s server bookkeeping misses %q", group, name)
			}
		}
	}

	// @server: category references resolve through the recorded catalog.
	if names := tools.ResolveCategoryTools("charts", catalog); !equalNames(names, []string{chartRenderToolName}) {
		t.Fatalf("charts category = %v, want [%s]", names, chartRenderToolName)
	}
}

// Parity: core/tests/test_harness_tools.py::test_every_group_builds
func TestSessionCatalogueMemoryTimelineGateControlsCatalogAndDefs(t *testing.T) {
	root := t.TempDir()
	defined := func(catalogue *sessionToolCatalogue) map[string]struct{} {
		names := map[string]struct{}{}
		for _, def := range catalogue.Defs() {
			names[def.Name] = struct{}{}
		}
		return names
	}

	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	off := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	if got := harnessGroupCatalogNames(t, off, harnessGroupMemory); !equalNames(got, []string{SearchMessagesToolName}) {
		t.Fatalf("memory-tools (off) = %v, want [%s]", got, SearchMessagesToolName)
	}
	if _, ok := defined(off)[SearchTimelineToolName]; ok {
		t.Fatalf("%s advertised while memory_timeline is off", SearchTimelineToolName)
	}

	t.Setenv("HIVE_MEMORY_TIMELINE", "1")
	on := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	want := []string{SearchMessagesToolName, SearchTimelineToolName}
	sort.Strings(want)
	if got := harnessGroupCatalogNames(t, on, harnessGroupMemory); !equalNames(got, want) {
		t.Fatalf("memory-tools (on) = %v, want %v", got, want)
	}
	if _, ok := defined(on)[SearchTimelineToolName]; !ok {
		t.Fatalf("%s missing from defs while memory_timeline is on", SearchTimelineToolName)
	}
}

// Parity-exempt: chart_render is served by the chart lane; until that renderer
// is linked the catalog entry exists but execution must say so explicitly.
func TestSessionCatalogueChartRenderFallsBackToExplicitNotImplemented(t *testing.T) {
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	implemented := false
	for _, def := range catalogue.Defs() {
		if def.Name == chartRenderToolName {
			implemented = true
			break
		}
	}
	if implemented || catalogue.registry.HasTool(chartRenderToolName) {
		t.Skip("chart_render is implemented in this build; the placeholder path no longer applies")
	}
	content, isError, err := catalogue.Execute(context.Background(), chartRenderToolName, map[string]any{
		"kind": "echarts",
		"spec": map[string]any{"series": []any{}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !isError {
		t.Fatalf("chart_render placeholder reported success: %s", content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload %q: %v", content, err)
	}
	if payload["error"] != "not_implemented" {
		t.Fatalf("payload = %#v, want error=not_implemented", payload)
	}
	if message, _ := payload["message"].(string); message == "" {
		t.Fatalf("payload has no actionable message: %#v", payload)
	}
}

// Parity-exempt: assembly guard for the Go-only aggregate spreadsheet tools —
// the upstream per-tool names must be registered so allowlists and the
// spreadsheet_advanced category resolve.
func TestSessionCatalogueRegistersSpreadsheetToolNames(t *testing.T) {
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	defined := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = struct{}{}
	}
	upstream := []string{
		"csv_read", "csv_write", "csv_append", "csv_info",
		"excel_read", "excel_write", "excel_append", "excel_info", "excel_sheet_list", "excel_search",
	}
	for _, name := range upstream {
		if _, ok := defined[name]; !ok {
			t.Fatalf("spreadsheet tool %q missing from session defs", name)
		}
		if !catalogue.registry.HasTool(name) {
			t.Fatalf("spreadsheet tool %q missing from the session registry", name)
		}
	}
	if _, ok := defined["browser_setup"]; !ok {
		t.Fatal("browser_setup missing from session defs")
	}
	for _, absent := range []string{"csv_sql", "excel_sql"} {
		if _, ok := defined[absent]; ok {
			t.Fatalf("%s must stay unsupported, but it is advertised", absent)
		}
	}
}

// Parity-exempt: assembly guard. The browser discovery tool and the spreadsheet
// aliases ride the declared-tool pool, so colony workers get them too; the base
// surface stays builtin-only.
func TestColonyWorkerToolSurfaceCarriesBrowserAndSpreadsheetTools(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := home.ColonyDir("tools")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}

	base, declared, _ := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	for _, name := range []string{integrations.BrowserSetupToolName, "csv_read", "excel_sheet_list"} {
		if !toolDefNamed(declared, name) {
			t.Fatalf("worker declared-tool pool misses %q", name)
		}
		if toolDefNamed(base, name) {
			t.Fatalf("worker base surface must stay builtin-only; %q leaked into it", name)
		}
	}
}

// Parity-exempt: the browser discovery tool is read-only and executes no
// network calls; both queens and workers build the same catalogue.
func TestSessionCatalogueBrowserSetupDispatch(t *testing.T) {
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	content, isError, err := catalogue.Execute(context.Background(), integrations.BrowserSetupToolName, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if isError {
		t.Fatalf("browser_setup reported an error: %s", content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload %q: %v", content, err)
	}
	if payload["ok"] != true || payload["cli"] != "hive-browser" {
		t.Fatalf("payload = %#v", payload)
	}
}
