package integrations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localRegistry(t *testing.T, root string) *Registry {
	t.Helper()
	return New(WithRoots(root))
}

// Parity: tools/tests/tools/test_csv_tool.py::TestCsvWrite::test_write_new_csv, tools/tests/tools/test_csv_tool.py::TestCsvRead::test_read_with_limit_and_offset, tools/tests/tools/test_csv_tool.py::TestCsvAppend::test_append_to_existing_csv, tools/tests/tools/test_csv_tool.py::TestCsvInfo::test_get_info_basic_csv
func TestCSVWriteReadAppendInfo(t *testing.T) {
	root := t.TempDir()
	registry := localRegistry(t, root)
	path := filepath.Join(root, "data.csv")

	written, err := registry.Execute(context.Background(), "csv", map[string]any{
		"action":  "csv_write",
		"path":    path,
		"columns": []any{"name", "qty"},
		"rows": []any{
			map[string]any{"name": "apple", "qty": 2},
			map[string]any{"name": "pear"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := written.(map[string]any); result["rows_written"] != 2 || result["success"] != true {
		t.Fatalf("write = %v", result)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "name,qty\napple,2\npear,\n" {
		t.Fatalf("file = %q", raw)
	}

	read, err := registry.Execute(context.Background(), "csv", map[string]any{
		"action": "csv_read", "path": path, "limit": 1, "offset": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	page := read.(map[string]any)
	if page["row_count"] != 1 || page["total_rows"] != 2 || page["limit"] != 1 {
		t.Fatalf("read = %v", page)
	}
	rows := page["rows"].([]map[string]any)
	if rows[0]["name"] != "pear" || rows[0]["qty"] != "" {
		t.Fatalf("rows = %v", rows)
	}

	appended, err := registry.Execute(context.Background(), "csv", map[string]any{
		"action": "csv_append", "path": path,
		"rows": []any{map[string]any{"name": "plum", "qty": 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := appended.(map[string]any); result["rows_appended"] != 1 || result["total_rows"] != 3 {
		t.Fatalf("append = %v", result)
	}

	info, err := registry.Execute(context.Background(), "csv", map[string]any{"action": "csv_info", "path": path})
	if err != nil {
		t.Fatal(err)
	}
	metadata := info.(map[string]any)
	if metadata["column_count"] != 2 || metadata["total_rows"] != 3 {
		t.Fatalf("info = %v", metadata)
	}
	if metadata["file_size_bytes"].(int64) <= 0 {
		t.Fatalf("size = %v", metadata["file_size_bytes"])
	}
}

// Parity: tools/tests/tools/test_csv_tool.py::TestCsvRead::test_path_traversal_blocked, tools/tests/tools/test_csv_tool.py::TestCsvRead::test_non_csv_extension, tools/tests/tools/test_csv_tool.py::TestCsvRead::test_file_not_found, tools/tests/tools/test_csv_tool.py::TestCsvRead::test_empty_csv_file
func TestCSVErrorsAndSandbox(t *testing.T) {
	root := t.TempDir()
	registry := localRegistry(t, root)
	outside := filepath.Join(t.TempDir(), "secret.csv")
	if err := os.WriteFile(outside, []byte("a\n1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"outside root", map[string]any{"action": "csv_read", "path": outside}, "Access denied"},
		{"not csv", map[string]any{"action": "csv_read", "path": filepath.Join(root, "a.txt")}, "must have .csv extension"},
		{"missing", map[string]any{"action": "csv_read", "path": filepath.Join(root, "nope.csv")}, "File not found"},
		{"empty rows", map[string]any{"action": "csv_append", "path": filepath.Join(root, "nope.csv"), "rows": []any{}}, "File not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := registry.Execute(context.Background(), "csv", tc.input)
			if err != nil {
				t.Fatal(err)
			}
			message, _ := result.(map[string]any)["error"].(string)
			if !strings.Contains(message, tc.want) {
				t.Fatalf("error = %q, want %q", message, tc.want)
			}
		})
	}

	empty := filepath.Join(root, "empty.csv")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), "csv", map[string]any{"action": "csv_read", "path": empty})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["error"] != "CSV file is empty or has no headers" {
		t.Fatalf("empty = %v", result)
	}
}

// Parity: tools/tests/tools/test_risk_scorer.py::TestFullScoring::test_headers_results_only, tools/tests/tools/test_risk_scorer.py::TestTopRisks::test_top_risks_generated
func TestRiskScoreGradesAndFindings(t *testing.T) {
	registry := New()

	allGood, err := registry.Execute(context.Background(), "risk_score", map[string]any{
		"headers_results": `{"grade_input":{"hsts":true,"csp":true,"x_frame_options":true,"x_content_type_options":true,"referrer_policy":true,"permissions_policy":true,"no_leaky_headers":true}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := allGood.(map[string]any)
	if result["overall_score"] != 100 || result["overall_grade"] != "A" {
		t.Fatalf("all-good = %v", result)
	}
	headers := result["categories"].(map[string]any)["http_headers"].(map[string]any)
	if headers["score"] != 100 || headers["skipped"] != false {
		t.Fatalf("headers = %v", headers)
	}
	ssl := result["categories"].(map[string]any)["ssl_tls"].(map[string]any)
	if ssl["skipped"] != true || ssl["grade"] != "N/A" {
		t.Fatalf("ssl = %v", ssl)
	}

	missingHSTS, err := registry.Execute(context.Background(), "risk_score", map[string]any{
		"headers_results": `{"hsts":false,"csp":true,"x_frame_options":true,"x_content_type_options":true,"referrer_policy":true,"permissions_policy":true,"no_leaky_headers":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	degraded := missingHSTS.(map[string]any)
	if degraded["overall_score"] != 80 || degraded["overall_grade"] != "B" {
		t.Fatalf("degraded = %v", degraded)
	}
	top := degraded["top_risks"].([]string)
	if len(top) != 1 || top[0] != "Missing Strict-Transport-Security header (Http Headers: B)" {
		t.Fatalf("top risks = %v", top)
	}
}

// Parity: tools/tests/tools/test_risk_scorer.py::TestScoreCategory::test_missing_values_half_credit
func TestRiskScoreMissingDataGivesHalfCredit(t *testing.T) {
	registry := New()
	result, err := registry.Execute(context.Background(), "risk_score", map[string]any{
		"ssl_results": `{"grade_input":{}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	ssl := result.(map[string]any)["categories"].(map[string]any)["ssl_tls"].(map[string]any)
	if ssl["score"] != 49 || ssl["grade"] != "D" {
		t.Fatalf("ssl = %v", ssl)
	}
	if result.(map[string]any)["overall_score"] != 49 {
		t.Fatalf("overall = %v", result.(map[string]any)["overall_score"])
	}
}

// Parity: tools/tests/tools/test_time_tool.py::TestGetCurrentTime::test_returns_required_fields, tools/tests/tools/test_time_tool.py::TestGetCurrentTime::test_asia_timezone, tools/tests/tools/test_time_tool.py::TestGetCurrentTime::test_datetime_is_iso_format
func TestTimeToolFields(t *testing.T) {
	registry := New()
	result, err := registry.Execute(context.Background(), "get_current_time", map[string]any{"timezone": "Asia/Kolkata"})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	for _, key := range []string{"datetime", "date", "time", "timezone", "day_of_week", "unix_timestamp"} {
		if object[key] == nil {
			t.Fatalf("missing %s in %v", key, object)
		}
	}
	if object["timezone"] != "Asia/Kolkata" || !strings.HasSuffix(object["datetime"].(string), "+05:30") {
		t.Fatalf("time = %v", object)
	}
}

type fakeAccounts struct{}

func (fakeAccounts) Accounts(provider string) []map[string]any {
	all := []map[string]any{
		{"account_id": "a1", "provider": "google", "email": "a@example.com"},
		{"account_id": "a2", "provider": "github", "username": "alice"},
	}
	if provider == "" {
		return all
	}
	out := []map[string]any{}
	for _, account := range all {
		if account["provider"] == provider {
			out = append(out, account)
		}
	}
	return out
}

// Parity-exempt: upstream has no Python test; behavior ported from
// tools/src/aden_tools/tools/account_info_tool/account_info_tool.py::get_account_info
func TestAccountInfoSource(t *testing.T) {
	empty, err := New().Execute(context.Background(), "get_account_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.(map[string]any)["message"] != "No credential store configured" {
		t.Fatalf("empty = %v", empty)
	}

	registry := New(WithAccountSource(fakeAccounts{}))
	all, err := registry.Execute(context.Background(), "get_account_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if all.(map[string]any)["count"] != 2 {
		t.Fatalf("all = %v", all)
	}
	filtered, err := registry.Execute(context.Background(), "get_account_info", map[string]any{"provider": "google"})
	if err != nil {
		t.Fatal(err)
	}
	accounts := filtered.(map[string]any)["accounts"].([]map[string]any)
	if len(accounts) != 1 || accounts[0]["account_id"] != "a1" {
		t.Fatalf("filtered = %v", filtered)
	}
}
