package cli

// `hive global-db` is the Go mount of core/framework/global_db/cli.py, the
// standalone `hive-global-db` argparse program. The tests below pin the six
// verbs, their flags, the two exit-code arms (unsigned 2 / backend 1), the
// import batching and export paging loops, and the malformed-input paths.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type globalDBRequest struct {
	Method        string
	Path          string
	Query         string
	Authorization string
	ContentType   string
	Body          map[string]any
}

type globalDBRecorder struct {
	mu       sync.Mutex
	requests []globalDBRequest
}

func (rec *globalDBRecorder) server(t *testing.T, respond func(globalDBRequest) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		request := globalDBRequest{
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.RawQuery,
			Authorization: r.Header.Get("Authorization"),
			ContentType:   r.Header.Get("Content-Type"),
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			if err := json.Unmarshal(raw, &request.Body); err != nil {
				t.Errorf("request body is not JSON (%s): %v", raw, err)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, request)
		rec.mu.Unlock()
		status, body := respond(request)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func (rec *globalDBRecorder) captured() []globalDBRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]globalDBRequest(nil), rec.requests...)
}

func runGlobalDBCLI(t *testing.T, base, jwt string, argv ...string) (int, string, string) {
	t.Helper()
	t.Setenv("HIVE_CLOUD_BASE", base)
	t.Setenv("HIVE_CLOUD_JWT", jwt)
	return runCLI(t, argv...)
}

func assertGlobalDBJSON(t *testing.T, raw string, want any) {
	t.Helper()
	var got any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("stdout is not JSON (%q): %v", raw, err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("stdout JSON = %s, want %s", gotJSON, wantJSON)
	}
}

// Parity: framework/global_db/cli.py::main
func TestGlobalDBCommandsMountUnderHive(t *testing.T) {
	root := NewRunner().rootCommand()
	group := findSubcommand(root, "global-db")
	if group == nil {
		t.Fatal("hive global-db is not resolvable")
	}
	if !group.Hidden {
		t.Fatal("hive global-db must stay hidden: it is a Go mount of a separate upstream entrypoint")
	}
	if group.Prog != "hive-global-db" {
		t.Fatalf("group Prog = %q, want the upstream prog hive-global-db", group.Prog)
	}
	if group.Dest != "cmd" {
		t.Fatalf("group Dest = %q, want argparse's cmd", group.Dest)
	}

	cases := []struct {
		name        string
		flags       []string
		positionals []string
	}{
		{name: "tables"},
		{name: "query", flags: []string{"row-cap"}, positionals: []string{"sql"}},
		{name: "sql", flags: []string{"row-cap"}, positionals: []string{"sql"}},
		{name: "upsert", flags: []string{"table", "row", "source-colony"}},
		{name: "import", flags: []string{"table", "file", "source-colony"}},
		{name: "export", flags: []string{"table", "format"}},
	}
	for _, tc := range cases {
		sub := findSubcommand(group, tc.name)
		if sub == nil {
			t.Fatalf("missing subcommand %q", tc.name)
		}
		longs := make([]string, 0, len(sub.Flags))
		for _, flag := range sub.Flags {
			longs = append(longs, flag.Long)
		}
		if strings.Join(longs, ",") != strings.Join(tc.flags, ",") {
			t.Fatalf("%s flags = %v, want %v", tc.name, longs, tc.flags)
		}
		destinations := make([]string, 0, len(sub.Positionals))
		for _, positional := range sub.Positionals {
			destinations = append(destinations, positional.Dest)
		}
		if strings.Join(destinations, ",") != strings.Join(tc.positionals, ",") {
			t.Fatalf("%s positionals = %v, want %v", tc.name, destinations, tc.positionals)
		}
	}

	for _, required := range []struct{ command, flag string }{
		{"upsert", "table"}, {"upsert", "row"},
		{"import", "table"}, {"import", "file"},
		{"export", "table"},
	} {
		sub := findSubcommand(group, required.command)
		flag := findLongFlag(sub, required.flag)
		if flag == nil || !flag.Required {
			t.Fatalf("%s --%s must be required", required.command, required.flag)
		}
	}
	format := findLongFlag(findSubcommand(group, "export"), "format")
	if format == nil || format.Default != "csv" || strings.Join(format.Choices, ",") != "csv,json" {
		t.Fatalf("export --format = %#v, want default csv with choices csv,json", format)
	}

	// Hidden means the frozen `hive` help surface (C11) is untouched.
	code, stdout, _ := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("hive --help = %d, want 0", code)
	}
	if strings.Contains(stdout, "global-db") {
		t.Fatalf("global-db leaked into `hive --help`:\n%s", stdout)
	}
}

// Parity: framework/global_db/cli.py::main
func TestGlobalDBHelpPagesMatchUpstream(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{
			argv: []string{"global-db", "--help"},
			want: `usage: hive-global-db [-h] {tables,query,sql,upsert,import,export} ...

Shared cloud team global DB CLI.

positional arguments:
  {tables,query,sql,upsert,import,export}
    tables              list tables + row counts
    query               read-only SQL
    sql                 full SQL (DDL/DML/SELECT)
    upsert              upsert one row
    import              bulk upsert from CSV/JSON
    export              stream a whole table out

options:
  -h, --help            show this help message and exit
`,
		},
		{
			argv: []string{"global-db", "query", "--help"},
			want: `usage: hive-global-db query [-h] [--row-cap ROW_CAP] sql

positional arguments:
  sql

options:
  -h, --help         show this help message and exit
  --row-cap ROW_CAP
`,
		},
		{
			argv: []string{"global-db", "upsert", "--help"},
			want: `usage: hive-global-db upsert [-h] --table TABLE --row ROW
                             [--source-colony SOURCE_COLONY]

options:
  -h, --help            show this help message and exit
  --table TABLE
  --row ROW             JSON object of column->value
  --source-colony SOURCE_COLONY
`,
		},
	}
	for _, tc := range cases {
		code, stdout, stderr := runCLI(t, tc.argv...)
		if code != 0 {
			t.Fatalf("%v = %d, want 0 (stderr %q)", tc.argv, code, stderr)
		}
		if stdout != tc.want {
			t.Fatalf("%v stdout:\n%s\nwant:\n%s", tc.argv, stdout, tc.want)
		}
	}
}

// Parity: framework/global_db/cli.py::cmd_tables
func TestGlobalDBTables(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		return 200, `{"tables":[{"name":"leads","row_count":2}]}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt", "global-db", "tables")
	if code != 0 {
		t.Fatalf("tables = %d, want 0 (stderr %q)", code, stderr)
	}
	want := "{\n  \"tables\": [\n    {\n      \"name\": \"leads\",\n      \"row_count\": 2\n    }\n  ]\n}\n"
	if stdout != want {
		t.Fatalf("tables stdout = %q, want %q", stdout, want)
	}
	requests := rec.captured()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodGet || request.Path != "/v1/global-db/tables" || request.Query != "" {
		t.Fatalf("request = %s %s?%s, want GET /v1/global-db/tables", request.Method, request.Path, request.Query)
	}
	if request.Authorization != "jwt test-jwt" {
		t.Fatalf("Authorization = %q, want jwt test-jwt", request.Authorization)
	}
}

// Parity: framework/global_db/cli.py::cmd_query
func TestGlobalDBQueryForwardsRowCap(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantCap any
	}{
		{
			name:    "with row cap",
			argv:    []string{"global-db", "query", "SELECT 1", "--row-cap", "7"},
			wantCap: float64(7),
		},
		{
			name: "without row cap",
			argv: []string{"global-db", "query", "SELECT 1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &globalDBRecorder{}
			server := rec.server(t, func(globalDBRequest) (int, string) {
				return 200, `{"columns":[{"name":"n"}],"rows":[{"n":1}],"rowCount":1}`
			})
			defer server.Close()

			code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt", tc.argv...)
			if code != 0 {
				t.Fatalf("query = %d, want 0 (stderr %q)", code, stderr)
			}
			assertGlobalDBJSON(t, stdout, map[string]any{
				"columns":  []any{map[string]any{"name": "n"}},
				"rows":     []any{map[string]any{"n": float64(1)}},
				"rowCount": float64(1),
			})
			requests := rec.captured()
			if len(requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(requests))
			}
			request := requests[0]
			if request.Method != http.MethodPost || request.Path != "/v1/global-db/query" {
				t.Fatalf("request = %s %s, want POST /v1/global-db/query", request.Method, request.Path)
			}
			if request.Body["sql"] != "SELECT 1" {
				t.Fatalf("sql = %#v, want SELECT 1", request.Body["sql"])
			}
			if request.ContentType != "application/json" {
				t.Fatalf("Content-Type = %q", request.ContentType)
			}
			gotCap, hasCap := request.Body["row_cap"]
			if tc.wantCap == nil {
				if hasCap {
					t.Fatalf("row_cap sent without the flag: %#v", gotCap)
				}
				return
			}
			if gotCap != tc.wantCap {
				t.Fatalf("row_cap = %#v, want %#v", gotCap, tc.wantCap)
			}
		})
	}
}

// Parity: framework/global_db/cli.py::cmd_sql
func TestGlobalDBSQL(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		return 200, `{"columns":[],"rows":[],"rowCount":0}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt",
		"global-db", "sql", "CREATE TABLE leads (id TEXT)")
	if code != 0 {
		t.Fatalf("sql = %d, want 0 (stderr %q)", code, stderr)
	}
	assertGlobalDBJSON(t, stdout, map[string]any{
		"columns": []any{}, "rows": []any{}, "rowCount": float64(0),
	})
	requests := rec.captured()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Path != "/v1/global-db/sql" || requests[0].Method != http.MethodPost {
		t.Fatalf("request = %s %s, want POST /v1/global-db/sql", requests[0].Method, requests[0].Path)
	}
	if requests[0].Body["sql"] != "CREATE TABLE leads (id TEXT)" {
		t.Fatalf("sql = %#v", requests[0].Body["sql"])
	}
	if _, hasCap := requests[0].Body["row_cap"]; hasCap {
		t.Fatal("row_cap sent without the flag")
	}
}

// Parity: framework/global_db/cli.py::cmd_upsert
func TestGlobalDBUpsert(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		return 200, `{"inserted":1}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt",
		"global-db", "upsert", "--table", "leads",
		"--row", `{"id":1,"name":"Alice"}`, "--source-colony", "colony-a")
	if code != 0 {
		t.Fatalf("upsert = %d, want 0 (stderr %q)", code, stderr)
	}
	assertGlobalDBJSON(t, stdout, map[string]any{"inserted": float64(1)})
	requests := rec.captured()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/v1/global-db/upsert" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["table"] != "leads" || request.Body["source_colony"] != "colony-a" {
		t.Fatalf("body = %#v", request.Body)
	}
	row, _ := request.Body["row"].(map[string]any)
	if row["id"] != float64(1) || row["name"] != "Alice" {
		t.Fatalf("row = %#v", request.Body["row"])
	}
}

// Parity: framework/global_db/cli.py::cmd_import
func TestGlobalDBImportJSONBatchesAtThousand(t *testing.T) {
	cases := []struct {
		rows      int
		wantCalls int
	}{
		{rows: 0, wantCalls: 0},
		{rows: 1000, wantCalls: 1},
		{rows: 1001, wantCalls: 2},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d rows", tc.rows), func(t *testing.T) {
			dir := t.TempDir()
			rows := make([]map[string]any, tc.rows)
			for index := range rows {
				rows[index] = map[string]any{"id": index}
			}
			encoded, err := json.Marshal(rows)
			if err != nil {
				t.Fatalf("marshal rows: %v", err)
			}
			path := filepath.Join(dir, "rows.json")
			if err := os.WriteFile(path, encoded, 0o644); err != nil {
				t.Fatalf("write rows: %v", err)
			}

			rec := &globalDBRecorder{}
			server := rec.server(t, func(request globalDBRequest) (int, string) {
				batch, _ := request.Body["rows"].([]any)
				return 200, fmt.Sprintf(`{"count":%d}`, len(batch))
			})
			defer server.Close()

			code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt",
				"global-db", "import", "--table", "leads", "--file", path, "--source-colony", "colony-a")
			if code != 0 {
				t.Fatalf("import = %d, want 0 (stderr %q)", code, stderr)
			}
			requests := rec.captured()
			if len(requests) != tc.wantCalls {
				t.Fatalf("requests = %d, want %d", len(requests), tc.wantCalls)
			}
			imported := 0
			for index, request := range requests {
				if request.Path != "/v1/global-db/import" || request.Method != http.MethodPost {
					t.Fatalf("request %d = %s %s", index, request.Method, request.Path)
				}
				if request.Body["table"] != "leads" || request.Body["source_colony"] != "colony-a" {
					t.Fatalf("request %d body = %#v", index, request.Body)
				}
				batch, _ := request.Body["rows"].([]any)
				want := globalDBImportBatch
				if remaining := tc.rows - imported; remaining < want {
					want = remaining
				}
				if len(batch) != want {
					t.Fatalf("batch %d rows = %d, want %d", index, len(batch), want)
				}
				imported += len(batch)
			}
			if imported != tc.rows {
				t.Fatalf("imported = %d, want %d", imported, tc.rows)
			}
			wantStderr := ""
			for done := globalDBImportBatch; done < tc.rows; done += globalDBImportBatch {
				wantStderr += fmt.Sprintf("imported %d/%d\n", done, tc.rows)
			}
			if tc.rows > 0 {
				wantStderr += fmt.Sprintf("imported %d/%d\n", tc.rows, tc.rows)
			}
			if stderr != wantStderr {
				t.Fatalf("stderr = %q, want %q", stderr, wantStderr)
			}
			wantStdout := fmt.Sprintf("{\n  \"success\": true,\n  \"count\": %d\n}\n", tc.rows)
			if stdout != wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout, wantStdout)
			}
		})
	}
}

// Parity: framework/global_db/cli.py::_read_rows
func TestGlobalDBImportCSV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rows.csv")
	if err := os.WriteFile(path, []byte("id,name\n1,Alice\n2,Bob\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}

	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		return 200, `{"count":2}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt",
		"global-db", "import", "--table", "leads", "--file", path)
	if code != 0 {
		t.Fatalf("import = %d, want 0 (stderr %q)", code, stderr)
	}
	if stderr != "imported 2/2\n" {
		t.Fatalf("stderr = %q, want imported 2/2", stderr)
	}
	assertGlobalDBJSON(t, stdout, map[string]any{"success": true, "count": float64(2)})
	requests := rec.captured()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if _, hasSource := requests[0].Body["source_colony"]; hasSource {
		t.Fatalf("source_colony sent without the flag: %#v", requests[0].Body)
	}
	assertGlobalDBJSON(t, mustJSON(t, requests[0].Body["rows"]), []any{
		map[string]any{"id": "1", "name": "Alice"},
		map[string]any{"id": "2", "name": "Bob"},
	})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// Parity: framework/global_db/cli.py::cmd_export
func TestGlobalDBExportCSVPaginates(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(request globalDBRequest) (int, string) {
		switch request.Query {
		case "limit=500&offset=0":
			rows := make([]string, 0, globalDBExportPage)
			for index := 0; index < globalDBExportPage; index++ {
				rows = append(rows, fmt.Sprintf(`{"id":%d,"label":"row-%d"}`, index, index))
			}
			return 200, `{"columns":[{"name":"id"},{"name":"label"}],"rows":[` + strings.Join(rows, ",") + `]}`
		case "limit=500&offset=500":
			return 200, `{"columns":[{"name":"id"},{"name":"label"}],"rows":[{"id":500,"label":"last"}]}`
		default:
			t.Errorf("unexpected page query %q", request.Query)
			return 500, `{"error":{"message":"unexpected page"}}`
		}
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt",
		"global-db", "export", "--table", "leads")
	if code != 0 {
		t.Fatalf("export = %d, want 0 (stderr %q)", code, stderr)
	}
	requests := rec.captured()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2 paged requests", len(requests))
	}
	lines := strings.Split(strings.TrimSuffix(stdout, "\r\n"), "\r\n")
	if len(lines) != globalDBExportPage+2 {
		t.Fatalf("csv lines = %d, want %d", len(lines), globalDBExportPage+2)
	}
	if lines[0] != "id,label" || lines[1] != "0,row-0" || lines[len(lines)-1] != "500,last" {
		t.Fatalf("csv head/tail = %q / %q", lines[:2], lines[len(lines)-1])
	}
	if !strings.HasSuffix(stdout, "\r\n") {
		t.Fatal("csv output must keep the excel dialect's CRLF terminator")
	}
}

// Parity: framework/global_db/cli.py::cmd_export
func TestGlobalDBExportJSON(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		return 200, `{"columns":[{"name":"id"},{"name":"name"}],"rows":[{"id":1,"name":"Alice"},{"id":2,"name":"Bob"}]}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt",
		"global-db", "export", "--table", "leads", "--format", "json")
	if code != 0 {
		t.Fatalf("export = %d, want 0 (stderr %q)", code, stderr)
	}
	assertGlobalDBJSON(t, stdout, []any{
		map[string]any{"id": float64(1), "name": "Alice"},
		map[string]any{"id": float64(2), "name": "Bob"},
	})
	requests := rec.captured()
	if len(requests) != 1 || requests[0].Query != "limit=500&offset=0" {
		t.Fatalf("requests = %#v", requests)
	}
}

// Parity: framework/global_db/cli.py::_run
func TestGlobalDBErrorExitCodes(t *testing.T) {
	t.Run("unsigned exits 2", func(t *testing.T) {
		code, stdout, stderr := runGlobalDBCLI(t, "http://127.0.0.1:1", "", "global-db", "tables")
		if code != 2 {
			t.Fatalf("unsigned tables = %d, want 2", code)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		want := "error: Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset).\n"
		if stderr != want {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	})

	t.Run("backend 401 exits 2", func(t *testing.T) {
		rec := &globalDBRecorder{}
		server := rec.server(t, func(globalDBRequest) (int, string) {
			return 401, `{"error":{"message":"expired"}}`
		})
		defer server.Close()
		code, _, stderr := runGlobalDBCLI(t, server.URL, "stale-jwt", "global-db", "tables")
		if code != 2 {
			t.Fatalf("401 = %d, want 2", code)
		}
		if stderr != "error: Cloud session rejected (401) — sign in again.\n" {
			t.Fatalf("stderr = %q", stderr)
		}
	})

	t.Run("backend error exits 1", func(t *testing.T) {
		rec := &globalDBRecorder{}
		server := rec.server(t, func(globalDBRequest) (int, string) {
			return 500, `{"error":{"message":"boom"}}`
		})
		defer server.Close()
		code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt", "global-db", "sql", "SELECT 1")
		if code != 1 {
			t.Fatalf("500 = %d, want 1", code)
		}
		if stdout != "" || stderr != "error: boom\n" {
			t.Fatalf("stdout/stderr = %q/%q", stdout, stderr)
		}
	})

	t.Run("backend code-only error exits 1", func(t *testing.T) {
		rec := &globalDBRecorder{}
		server := rec.server(t, func(globalDBRequest) (int, string) {
			return 404, `{"error":{"code":"table_not_found"}}`
		})
		defer server.Close()
		code, _, stderr := runGlobalDBCLI(t, server.URL, "test-jwt", "global-db", "tables")
		if code != 1 || stderr != "error: table_not_found\n" {
			t.Fatalf("code/stderr = %d/%q", code, stderr)
		}
	})
}

// Parity: framework/global_db/cli.py::main
func TestGlobalDBMalformedInput(t *testing.T) {
	dir := t.TempDir()
	notArray := filepath.Join(dir, "rows.json")
	if err := os.WriteFile(notArray, []byte(`{"id":1}`), 0o644); err != nil {
		t.Fatalf("write json: %v", err)
	}

	cases := []struct {
		name       string
		argv       []string
		wantCode   int
		wantStderr string
	}{
		{
			name:       "missing subcommand",
			argv:       []string{"global-db"},
			wantCode:   2,
			wantStderr: "hive-global-db: error: the following arguments are required: cmd\n",
		},
		{
			name:       "row is not JSON",
			argv:       []string{"global-db", "upsert", "--table", "leads", "--row", "{"},
			wantCode:   1,
			wantStderr: "error: --row is not valid JSON: ",
		},
		{
			name:       "missing sql positional",
			argv:       []string{"global-db", "query"},
			wantCode:   2,
			wantStderr: "hive-global-db query: error: the following arguments are required: sql\n",
		},
		{
			name:       "invalid format choice",
			argv:       []string{"global-db", "export", "--table", "leads", "--format", "yaml"},
			wantCode:   2,
			wantStderr: "hive-global-db export: error: argument --format: invalid choice: 'yaml' (choose from 'csv', 'json')\n",
		},
		{
			name:       "invalid row cap",
			argv:       []string{"global-db", "query", "SELECT 1", "--row-cap", "nope"},
			wantCode:   2,
			wantStderr: "hive-global-db query: error: argument --row-cap: invalid int value: 'nope'\n",
		},
		{
			name:       "missing required table",
			argv:       []string{"global-db", "export"},
			wantCode:   2,
			wantStderr: "hive-global-db export: error: the following arguments are required: --table\n",
		},
		{
			name:       "import JSON is not an array",
			argv:       []string{"global-db", "import", "--table", "leads", "--file", notArray},
			wantCode:   1,
			wantStderr: "error: JSON import file must be an array of row objects\n",
		},
		{
			name:       "import file is missing",
			argv:       []string{"global-db", "import", "--table", "leads", "--file", filepath.Join(dir, "absent.csv")},
			wantCode:   1,
			wantStderr: "error: ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &globalDBRecorder{}
			server := rec.server(t, func(globalDBRequest) (int, string) {
				t.Error("malformed input reached the backend")
				return 500, `{"error":{"message":"should not be called"}}`
			})
			defer server.Close()
			code, _, stderr := runGlobalDBCLI(t, server.URL, "test-jwt", tc.argv...)
			if code != tc.wantCode {
				t.Fatalf("%v = %d, want %d (stderr %q)", tc.argv, code, tc.wantCode, stderr)
			}
			if !strings.Contains(stderr, tc.wantStderr) {
				t.Fatalf("%v stderr = %q, want it to contain %q", tc.argv, stderr, tc.wantStderr)
			}
			if requests := rec.captured(); len(requests) != 0 {
				t.Fatalf("malformed input made %d request(s)", len(requests))
			}
		})
	}
}

// Parity: framework/global_db/cli.py::main
func TestGlobalDBUnknownVerbRejected(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		t.Error("unknown verb reached the backend")
		return 500, `{"error":{"message":"should not be called"}}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "test-jwt", "global-db", "drop", "--table", "leads")
	if code != 2 {
		t.Fatalf("drop = %d, want 2", code)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q", stdout)
	}
	want := "hive-global-db: error: argument cmd: invalid choice: 'drop' (choose from 'tables', 'query', 'sql', 'upsert', 'import', 'export')\n"
	if !strings.HasSuffix(stderr, want) {
		t.Fatalf("stderr = %q, want it to end with %q", stderr, want)
	}
	if requests := rec.captured(); len(requests) != 0 {
		t.Fatalf("unknown verb made %d request(s)", len(requests))
	}
}

// Parity-exempt: negative regression for the Go mount. The earlier audit found
// no `hive-global-db` surface; the risk of adding one is a silent local
// fallback (or an unauthenticated request) when the cloud session is missing.
// The CLI must fail before touching the network, not degrade to a local DB.
func TestGlobalDBUnsignedNeverSendsRequest(t *testing.T) {
	rec := &globalDBRecorder{}
	server := rec.server(t, func(globalDBRequest) (int, string) {
		t.Error("unsigned CLI contacted the backend")
		return 200, `{"tables":[]}`
	})
	defer server.Close()

	code, stdout, stderr := runGlobalDBCLI(t, server.URL, "", "global-db", "tables")
	if code != 2 {
		t.Fatalf("unsigned tables = %d, want 2", code)
	}
	if stdout != "" || !strings.HasPrefix(stderr, "error: Sign in to use the shared global DB") {
		t.Fatalf("stdout/stderr = %q/%q", stdout, stderr)
	}
	if requests := rec.captured(); len(requests) != 0 {
		t.Fatalf("unsigned CLI made %d request(s)", len(requests))
	}
}
