package integrations

// Spreadsheet alias guards: the upstream per-tool names (csv_read, ... /
// excel_read, ...) must be independent registry tools that execute exactly the
// aggregate csv/excel actions, while csv_sql/excel_sql stay unsupported.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// aliasProbeRegistry assembles the session view of the spreadsheet surface:
// the csv aggregate, the excel aggregate, then the upstream per-tool aliases.
func aliasProbeRegistry(t *testing.T, root string) *tools.ToolRegistry {
	t.Helper()
	probe := tools.NewToolRegistry()
	csv := New(WithRoots(root))
	if count := csv.Install(probe); count == 0 {
		t.Fatal("csv aggregate installed no tools")
	}
	excel := newBatch3ExcelRegistry(withBatch3ExcelRoots(root))
	if count := excel.Install(probe); count == 0 {
		t.Fatal("excel aggregate installed no tools")
	}
	if count := InstallSpreadsheetAliases(probe); count != 10 {
		t.Fatalf("InstallSpreadsheetAliases registered %d tools, want 10", count)
	}
	return probe
}

// executeAliasTool runs one alias and decodes the JSON envelope.
func executeAliasTool(t *testing.T, registry *tools.ToolRegistry, name string, input map[string]any) (map[string]any, bool) {
	t.Helper()
	result, err := registry.GetExecutor().Execute(context.Background(), tools.ToolUse{Name: name, Input: input})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("%s content %q: %v", name, result.Content, err)
	}
	return payload, result.IsError
}

// Parity-exempt: assembly guard. Upstream registers one MCP tool per action;
// the Go port keeps the aggregate tools and must also expose the upstream names
// so allowlists and spreadsheet_advanced resolve.
func TestSpreadsheetAliasesExposeUpstreamNames(t *testing.T) {
	root := t.TempDir()
	probe := aliasProbeRegistry(t, root)

	upstream := []string{
		"csv_read", "csv_write", "csv_append", "csv_info",
		"excel_read", "excel_write", "excel_append", "excel_info", "excel_sheet_list", "excel_search",
	}
	for _, name := range upstream {
		tool, ok := probe.GetTool(name)
		if !ok {
			t.Fatalf("alias tool %q missing from the registry", name)
		}
		schema, _ := tool.Parameters["properties"].(map[string]any)
		if schema == nil {
			t.Fatalf("%s has no properties schema: %#v", name, tool.Parameters)
		}
		if _, hasAction := schema["action"]; hasAction {
			t.Fatalf("%s is aliased with an action selector; it must be a single-action tool", name)
		}
	}
	// The aggregates stay available for existing callers.
	for _, name := range []string{"csv", "excel"} {
		if !probe.HasTool(name) {
			t.Fatalf("aggregate tool %q was removed", name)
		}
	}
	// Required lists follow the upstream signatures.
	required := map[string][]string{
		"csv_read":         {"path"},
		"csv_write":        {"path", "columns", "rows"},
		"csv_append":       {"path", "rows"},
		"csv_info":         {"path"},
		"excel_read":       {"path"},
		"excel_write":      {"path", "columns", "rows"},
		"excel_append":     {"path", "rows"},
		"excel_info":       {"path"},
		"excel_sheet_list": {"path"},
		"excel_search":     {"path", "search_term"},
	}
	for name, want := range required {
		tool, _ := probe.GetTool(name)
		got, _ := tool.Parameters["required"].([]string)
		if len(got) != len(want) {
			t.Fatalf("%s required = %v, want %v", name, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("%s required = %v, want %v", name, got, want)
			}
		}
	}
}

// Parity: tools/tests/tools/test_csv_tool.py::TestCsvWrite::test_write_new_csv, tools/tests/tools/test_csv_tool.py::TestCsvRead::test_read_with_limit_and_offset, tools/tests/tools/test_csv_tool.py::TestCsvAppend::test_append_to_existing_csv, tools/tests/tools/test_csv_tool.py::TestCsvInfo::test_get_info_basic_csv
func TestSpreadsheetAliasesReuseCSVActions(t *testing.T) {
	root := t.TempDir()
	probe := aliasProbeRegistry(t, root)
	path := filepath.Join(root, "alias.csv")

	written, isError := executeAliasTool(t, probe, "csv_write", map[string]any{
		"path":    path,
		"columns": []any{"name", "qty"},
		"rows": []any{
			map[string]any{"name": "apple", "qty": 2},
			map[string]any{"name": "pear"},
		},
	})
	if isError || written["success"] != true || written["rows_written"] != float64(2) {
		t.Fatalf("csv_write = %v (isError=%v)", written, isError)
	}

	page, isError := executeAliasTool(t, probe, "csv_read", map[string]any{
		"path": path, "limit": 1, "offset": 1,
	})
	if isError || page["row_count"] != float64(1) || page["total_rows"] != float64(2) {
		t.Fatalf("csv_read = %v (isError=%v)", page, isError)
	}

	appended, isError := executeAliasTool(t, probe, "csv_append", map[string]any{
		"path": path,
		"rows": []any{map[string]any{"name": "plum", "qty": 5}},
	})
	if isError || appended["rows_appended"] != float64(1) || appended["total_rows"] != float64(3) {
		t.Fatalf("csv_append = %v (isError=%v)", appended, isError)
	}

	info, isError := executeAliasTool(t, probe, "csv_info", map[string]any{"path": path})
	if isError || info["column_count"] != float64(2) || info["total_rows"] != float64(3) {
		t.Fatalf("csv_info = %v (isError=%v)", info, isError)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_then_read, tools/tests/tools/test_excel_tool.py::test_write_append_read, tools/tests/tools/test_excel_tool.py::test_get_info_basic_xlsx, tools/tests/tools/test_excel_tool.py::test_list_sheets_basic
func TestSpreadsheetAliasesReuseExcelActions(t *testing.T) {
	root := t.TempDir()
	probe := aliasProbeRegistry(t, root)
	path := filepath.Join(root, "alias.xlsx")

	written, isError := executeAliasTool(t, probe, "excel_write", map[string]any{
		"path":    path,
		"columns": []any{"name", "qty"},
		"rows": []any{
			map[string]any{"name": "apple", "qty": 2},
			map[string]any{"name": "pear", "qty": 4},
		},
	})
	if isError || written["success"] != true || written["rows_written"] != float64(2) {
		t.Fatalf("excel_write = %v (isError=%v)", written, isError)
	}

	read, isError := executeAliasTool(t, probe, "excel_read", map[string]any{"path": path})
	if isError || read["success"] != true || read["total_rows"] != float64(2) {
		t.Fatalf("excel_read = %v (isError=%v)", read, isError)
	}

	appended, isError := executeAliasTool(t, probe, "excel_append", map[string]any{
		"path": path,
		"rows": []any{map[string]any{"name": "plum", "qty": 5}},
	})
	if isError || appended["rows_appended"] != float64(1) || appended["total_rows"] != float64(3) {
		t.Fatalf("excel_append = %v (isError=%v)", appended, isError)
	}

	info, isError := executeAliasTool(t, probe, "excel_info", map[string]any{"path": path})
	if isError || info["sheet_names"] == nil || info["sheet_count"] != float64(1) {
		t.Fatalf("excel_info = %v (isError=%v)", info, isError)
	}

	sheets, isError := executeAliasTool(t, probe, "excel_sheet_list", map[string]any{"path": path})
	if isError || sheets["sheet_names"] == nil || sheets["sheet_count"] != float64(1) {
		t.Fatalf("excel_sheet_list = %v (isError=%v)", sheets, isError)
	}

	searched, isError := executeAliasTool(t, probe, "excel_search", map[string]any{
		"path": path, "search_term": "plum",
	})
	if isError || searched["success"] != true || searched["match_count"] != float64(1) {
		t.Fatalf("excel_search = %v (isError=%v)", searched, isError)
	}
}

// Parity: tools/tests/tools/test_csv_tool.py::TestCsvRead::test_file_not_found
func TestSpreadsheetAliasesKeepErrorEnvelopes(t *testing.T) {
	root := t.TempDir()
	probe := aliasProbeRegistry(t, root)

	payload, isError := executeAliasTool(t, probe, "csv_read", map[string]any{
		"path": filepath.Join(root, "missing.csv"),
	})
	if isError {
		t.Fatalf("expected an {error} envelope, got a tool error: %v", payload)
	}
	if message, _ := payload["error"].(string); message == "" {
		t.Fatalf("csv_read alias has no error message: %v", payload)
	}
}

// Parity-exempt: assembly guard. csv_sql/excel_sql are not ported (upstream
// gen_catalog.py excludes csv_sql; excel_sql needs DuckDB), so neither the
// aggregates nor the alias installer may expose them.
func TestSpreadsheetSQLNamesStayUnsupported(t *testing.T) {
	root := t.TempDir()
	probe := aliasProbeRegistry(t, root)
	for _, name := range []string{"csv_sql", "excel_sql"} {
		if probe.HasTool(name) {
			t.Fatalf("%s must not be registered as a tool", name)
		}
	}
	for _, aggregate := range spreadsheetAggregates() {
		if _, ok := aggregate.Action("csv_sql"); ok {
			t.Fatalf("%s unexpectedly carries a csv_sql action", aggregate.Name)
		}
		if _, ok := aggregate.Action("excel_sql"); ok {
			t.Fatalf("%s unexpectedly carries an excel_sql action", aggregate.Name)
		}
	}
}

// Parity-exempt: assembly guard. A name an MCP server already registered wins
// over the alias installer (same precedence as the harness-group records).
func TestSpreadsheetAliasesSkipExistingNames(t *testing.T) {
	probe := tools.NewToolRegistry()
	probe.Register("csv_read", tools.Tool{Name: "csv_read", Description: "sentinel"},
		func(map[string]any) (any, error) { return "sentinel", nil })

	if count := InstallSpreadsheetAliases(probe); count != 9 {
		t.Fatalf("installed %d aliases with csv_read taken, want 9", count)
	}
	result, err := probe.GetExecutor().Execute(context.Background(), tools.ToolUse{Name: "csv_read"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "sentinel" {
		t.Fatalf("csv_read was overwritten: %q", result.Content)
	}
}
