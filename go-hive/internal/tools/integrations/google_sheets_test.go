package integrations

import (
	"net/http"
	"testing"
)

// Parity: tools/tests/tools/test_google_sheets_tool.py::TestSheetsGetSpreadsheet::test_successful_get
func TestBatch5Lane3SheetsGetSpreadsheet(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/v4/spreadsheets/sheet-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("includeGridData") != "true" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"spreadsheetId": "sheet-1",
			"properties":    map[string]any{"title": "My Spreadsheet"},
			"sheets":        []any{map[string]any{"properties": map[string]any{"title": "Sheet1"}}},
		})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_get_spreadsheet", "spreadsheet_id": "sheet-1", "include_grid_data": true,
	})
	if batch4AsMap(result["properties"])["title"] != "My Spreadsheet" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity-exempt: upstream has no create_spreadsheet test; body shape follows
// _GoogleSheetsClient.create_spreadsheet.
func TestBatch5Lane3SheetsCreateSpreadsheet(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"spreadsheetId": "new-sheet"})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_create_spreadsheet", "title": "Q3 Report",
		"sheet_titles": []any{"Summary", "Detail"},
	})
	if result["spreadsheetId"] != "new-sheet" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if batch4AsMap(body["properties"])["title"] != "Q3 Report" {
		t.Fatalf("body = %v", body)
	}
	sheets := batch4BodySlice(t, body["sheets"])
	if len(sheets) != 2 || batch4AsMap(batch4AsMap(sheets[0])["properties"])["title"] != "Summary" {
		t.Fatalf("sheets = %v", sheets)
	}
}

// Parity: tools/tests/tools/test_google_sheets_tool.py::TestSheetsGetValues::test_successful_read
func TestBatch5Lane3SheetsGetValues(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/v4/spreadsheets/sheet-1/values/Sheet1!A1:B3" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("valueRenderOption") != "UNFORMATTED_VALUE" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"range":  "Sheet1!A1:B3",
			"values": []any{[]any{"Name", "Score"}, []any{"Alice", 95}},
		})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_get_values", "spreadsheet_id": "sheet-1",
		"range_name": "Sheet1!A1:B3", "value_render_option": "UNFORMATTED_VALUE",
	})
	values := batch4BodySlice(t, result["values"])
	if len(values) != 2 || batch4BodySlice(t, values[0])[0] != "Name" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity-exempt: upstream has no update_values test; the request mirrors
// _GoogleSheetsClient.update_values, including the JSON-string input form.
func TestBatch5Lane3SheetsUpdateValues(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPut || r.URL.Path != "/v4/spreadsheets/sheet-1/values/Sheet1!A1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("valueInputOption") != "RAW" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"updatedCells": 4})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_update_values", "spreadsheet_id": "sheet-1",
		"range_name": "Sheet1!A1", "values": `[["Alice",95],["Bob",87]]`,
		"value_input_option": "RAW",
	})
	if result["updatedCells"] != float64(4) {
		t.Fatalf("result = %v", result)
	}
	values := batch4BodySlice(t, recorder.last(t).Body["values"])
	if len(values) != 2 || batch4BodySlice(t, values[0])[0] != "Alice" {
		t.Fatalf("values = %v", values)
	}
}

// Parity-exempt: upstream has no append_values test; the ":append" path and
// payload follow _GoogleSheetsClient.append_values.
func TestBatch5Lane3SheetsAppendValues(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets/sheet-1/values/Sheet1:append" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"updates": map[string]any{"updatedRows": 1}})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_append_values", "spreadsheet_id": "sheet-1",
		"range_name": "Sheet1", "values": []any{[]any{"Carol", 78}},
	})
	if batch4AsMap(result["updates"])["updatedRows"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	if values := batch4BodySlice(t, recorder.last(t).Body["values"]); len(values) != 1 {
		t.Fatalf("values = %v", values)
	}
}

// Parity-exempt: upstream has no clear_values test; the ":clear" path follows
// _GoogleSheetsClient.clear_values.
func TestBatch5Lane3SheetsClearValues(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets/sheet-1/values/Sheet1!A1:C5:clear" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"clearedRange": "Sheet1!A1:C5"})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_clear_values", "spreadsheet_id": "sheet-1", "range_name": "Sheet1!A1:C5",
	})
	if result["clearedRange"] != "Sheet1!A1:C5" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity-exempt: upstream has no batch_update_values test; payload follows
// _GoogleSheetsClient.batch_update_values.
func TestBatch5Lane3SheetsBatchUpdateValues(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets/sheet-1/values:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"totalUpdatedCells": 2})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_batch_update_values", "spreadsheet_id": "sheet-1",
		"data": []any{
			map[string]any{"range": "Sheet1!A1", "values": []any{[]any{"x"}}},
			map[string]any{"range": "Sheet1!B1", "values": []any{[]any{"y"}}},
		},
	})
	if result["totalUpdatedCells"] != float64(2) {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["valueInputOption"] != "USER_ENTERED" {
		t.Fatalf("body = %v", body)
	}
	if data := batch4BodySlice(t, body["data"]); len(data) != 2 {
		t.Fatalf("data = %v", data)
	}
}

// Parity-exempt: upstream has no batch_clear_values test; payload follows
// _GoogleSheetsClient.batch_clear_values.
func TestBatch5Lane3SheetsBatchClearValues(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets/sheet-1/values:batchClear" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"clearedRanges": []any{"Sheet1!A1", "Sheet1!B1"}})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_batch_clear_values", "spreadsheet_id": "sheet-1",
		"ranges": []any{"Sheet1!A1", "Sheet1!B1"},
	})
	if len(batch4BodySlice(t, result["clearedRanges"])) != 2 {
		t.Fatalf("result = %v", result)
	}
	if ranges := batch4BodySlice(t, recorder.last(t).Body["ranges"]); len(ranges) != 2 {
		t.Fatalf("ranges = %v", ranges)
	}
}

// Parity-exempt: upstream has no add_sheet test; the addSheet request follows
// _GoogleSheetsClient.add_sheet with its default grid size.
func TestBatch5Lane3SheetsAddSheet(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets/sheet-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{map[string]any{"addSheet": map[string]any{"properties": map[string]any{"sheetId": 7}}}}})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_add_sheet", "spreadsheet_id": "sheet-1", "title": "New Tab",
	})
	replies := batch4BodySlice(t, result["replies"])
	properties := batch4AsMap(batch4AsMap(replies[0])["addSheet"])["properties"]
	if batch4AsMap(properties)["sheetId"] != float64(7) {
		t.Fatalf("result = %v", result)
	}
	requests := batch4BodySlice(t, recorder.last(t).Body["requests"])
	addSheet := batch4AsMap(batch4AsMap(requests[0])["addSheet"])
	props := batch4AsMap(addSheet["properties"])
	if props["title"] != "New Tab" {
		t.Fatalf("properties = %v", props)
	}
	grid := batch4AsMap(props["gridProperties"])
	if grid["rowCount"] != float64(1000) || grid["columnCount"] != float64(26) {
		t.Fatalf("grid = %v", grid)
	}
}

// Parity-exempt: upstream has no delete_sheet test; the deleteSheet request
// follows _GoogleSheetsClient.delete_sheet.
func TestBatch5Lane3SheetsDeleteSheet(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v4/spreadsheets/sheet-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{map[string]any{}}})
	})
	result := batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_delete_sheet", "spreadsheet_id": "sheet-1", "sheet_id": 42,
	})
	if _, ok := result["replies"]; !ok {
		t.Fatalf("result = %v", result)
	}
	requests := batch4BodySlice(t, recorder.last(t).Body["requests"])
	if batch4AsMap(batch4AsMap(requests[0])["deleteSheet"])["sheetId"] != float64(42) {
		t.Fatalf("requests = %v", requests)
	}
}

// Parity-exempt: Go-side response mapping; upstream tests mock one status at a
// time and do not cover an empty 2xx body.
func TestBatch5Lane3SheetsErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, "Invalid or expired Google Sheets access token"},
		{"forbidden", http.StatusForbidden, `{}`, "Insufficient permissions"},
		{"not found", http.StatusNotFound, `{}`, "Spreadsheet or range not found"},
		{"rate limit", http.StatusTooManyRequests, `{}`, "rate limit exceeded"},
		{"api error", http.StatusBadRequest, `{"error":{"message":"bad range"}}`, "bad range"},
		{"empty", http.StatusOK, ``, "invalid JSON response"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			})
			result := batch4Execute(t, registry, "google_sheets", map[string]any{
				"action": "google_sheets_get_spreadsheet", "spreadsheet_id": "sheet-1",
			})
			if message := batch4ErrorText(t, result); !containsAll(message, testCase.message) {
				t.Fatalf("error = %q, want %q", message, testCase.message)
			}
		})
	}
}
