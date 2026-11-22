// Google Sheets catalog entry for the batch-5 lane-3 Google Workspace
// registry (split from google_workspace.go, which keeps the installer,
// module configs and shared helpers).

package gworkspace

// --------------------------------------------------------------- catalog ----

var sheetsTool = &Tool{
	ID:          sheetsModule,
	Name:        "google_sheets",
	DisplayName: "Google Sheets",
	Summary:     "Google Sheets integration (10 actions).",
	Actions: []Action{
		{
			Name:     "google_sheets_get_spreadsheet",
			Summary:  "Get Google Sheets spreadsheet metadata.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id"},
			Params: map[string]any{
				"spreadsheet_id":    param("string"),
				"include_grid_data": paramDefault("boolean", false),
				"account":           paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_create_spreadsheet",
			Summary:  "Create a new Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"title"},
			Params: map[string]any{
				"title":        param("string"),
				"sheet_titles": param("array"),
				"account":      paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_get_values",
			Summary:  "Get values from a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name"},
			Params: map[string]any{
				"spreadsheet_id":      param("string"),
				"range_name":          param("string"),
				"value_render_option": paramDefault("string", "FORMATTED_VALUE"),
				"account":             paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_update_values",
			Summary:  "Update values in a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name", "values"},
			Params: map[string]any{
				"spreadsheet_id":     param("string"),
				"range_name":         param("string"),
				"values":             param("array"),
				"value_input_option": paramDefault("string", "USER_ENTERED"),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_append_values",
			Summary:  "Append values to a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name", "values"},
			Params: map[string]any{
				"spreadsheet_id":     param("string"),
				"range_name":         param("string"),
				"values":             param("array"),
				"value_input_option": paramDefault("string", "USER_ENTERED"),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_clear_values",
			Summary:  "Clear values in a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"range_name":     param("string"),
				"account":        paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_batch_update_values",
			Summary:  "Batch update multiple ranges in a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "data"},
			Params: map[string]any{
				"spreadsheet_id":     param("string"),
				"data":               param("array"),
				"value_input_option": paramDefault("string", "USER_ENTERED"),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_batch_clear_values",
			Summary:  "Batch clear multiple ranges in a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "ranges"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"ranges":         param("array"),
				"account":        paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_add_sheet",
			Summary:  "Add a new sheet/tab to a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "title"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"title":          param("string"),
				"row_count":      paramDefault("integer", 1000),
				"column_count":   paramDefault("integer", 26),
				"account":        paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_delete_sheet",
			Summary:  "Delete a sheet/tab from a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "sheet_id"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"sheet_id":       param("integer"),
				"account":        paramDefault("string", ""),
			},
		},
	},
}
