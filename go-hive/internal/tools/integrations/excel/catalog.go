package excel

import (
	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// Catalog returns the Excel module catalog: one tool ("excel") with the six
// upstream actions and their published parameter schemas. Callers must not
// mutate the returned tools; the registry shares these pointers.
func Catalog() []*integrationkit.Tool { return catalog }

// catalog mirrors gen_catalog.py output for excel_tool. Actions are
// in the generator's sorted order; agent_id/workspace_id/session_id are
// runtime identity in the Go port and therefore not model parameters.
var catalog = []*Tool{
	{
		ID:          "excel_tool",
		Name:        "excel",
		DisplayName: "Excel",
		Summary:     "Excel integration (6 actions).",
		Actions: []Action{
			{
				Name:     "excel_append",
				Summary:  "Append rows to an existing Excel file.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path", "rows"},
				Params: map[string]any{
					"path":  integrationkit.Param("string"),
					"rows":  integrationkit.ParamArrayOfObjects(),
					"sheet": integrationkit.Param("string"),
				},
			},
			{
				Name:     "excel_info",
				Summary:  "Get metadata about an Excel file without reading all data.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path"},
				Params: map[string]any{
					"path": integrationkit.Param("string"),
				},
			},
			{
				Name:     "excel_read",
				Summary:  "Read an Excel file and return its contents.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path"},
				Params: map[string]any{
					"path":   integrationkit.Param("string"),
					"sheet":  integrationkit.Param("string"),
					"limit":  integrationkit.Param("integer"),
					"offset": integrationkit.ParamDefault("integer", 0),
				},
			},
			{
				Name:     "excel_search",
				Summary:  "Search for values across Excel sheets.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path", "search_term"},
				Params: map[string]any{
					"path":           integrationkit.Param("string"),
					"search_term":    integrationkit.Param("string"),
					"sheet":          integrationkit.Param("string"),
					"case_sensitive": integrationkit.ParamDefault("boolean", false),
					"match_type":     integrationkit.ParamDefault("string", "contains"),
				},
			},
			{
				Name:     "excel_sheet_list",
				Summary:  "List all sheet names in an Excel file.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path"},
				Params: map[string]any{
					"path": integrationkit.Param("string"),
				},
			},
			{
				Name:     "excel_write",
				Summary:  "Write data to a new Excel file.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path", "columns", "rows"},
				Params: map[string]any{
					"path":    integrationkit.Param("string"),
					"columns": integrationkit.ParamItems("array", "string"),
					"rows":    integrationkit.ParamArrayOfObjects(),
					"sheet":   integrationkit.ParamDefault("string", "Sheet1"),
				},
			},
		},
	},
}
