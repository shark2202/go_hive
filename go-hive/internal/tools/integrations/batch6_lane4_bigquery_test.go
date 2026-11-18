package integrations

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_bigquery_tool.py::TestReadOnlyEnforcement::test_blocks_insert
func TestBatch6Lane4BigQueryBlocksWriteStatements(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	statements := []string{
		"INSERT INTO table VALUES (1, 2)",
		"UPDATE table SET col = 1",
		"DELETE FROM table WHERE id = 1",
		"DROP TABLE my_table",
		"CREATE TABLE my_table (id INT)",
		"ALTER TABLE my_table ADD COLUMN new_col INT",
		"TRUNCATE TABLE my_table",
		"MERGE INTO target USING source ON condition WHEN MATCHED THEN UPDATE",
		"insert into table values (1)",
	}
	for _, statement := range statements {
		result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": statement})
		if got := batch4ErrorText(t, result); got != "Write operations are not allowed" {
			t.Fatalf("%q: error = %q", statement, got)
		}
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestReadOnlyEnforcement::test_allows_select
func TestBatch6Lane4BigQueryAllowsSelect(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT * FROM table"})
	got := batch4ErrorText(t, result)
	if strings.Contains(got, "Write operations") {
		t.Fatalf("error = %q", got)
	}
	if !strings.Contains(got, "authentication failed") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestRowLimits::test_rejects_zero_max_rows
func TestBatch6Lane4BigQueryRejectsZeroRows(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT 1", "max_rows": 0})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "max_rows must be at least 1") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestRowLimits::test_rejects_excessive_max_rows
func TestBatch6Lane4BigQueryRejectsExcessiveRows(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT 1", "max_rows": 10001})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "max_rows cannot exceed 10000") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestQueryExecution::test_successful_query
func TestBatch6Lane4BigQueryQuerySuccess(t *testing.T) {
	env := map[string]string{"BIGQUERY_PROJECT_ID": "test-project"}
	registry, recorder := batch6Lane4TokenRegistry(t, "bigquery_tool", env,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/projects/test-project/queries" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"jobComplete":         true,
				"jobReference":        map[string]any{"jobId": "job-1"},
				"totalRows":           "2",
				"totalBytesProcessed": "1024",
				"schema": map[string]any{"fields": []any{
					map[string]any{"name": "id", "type": "INTEGER", "mode": "REQUIRED"},
					map[string]any{"name": "name", "type": "STRING", "mode": "NULLABLE"},
				}},
				"rows": []any{
					map[string]any{"f": []any{map[string]any{"v": "1"}, map[string]any{"v": "Alice"}}},
					map[string]any{"f": []any{map[string]any{"v": "2"}, map[string]any{"v": "Bob"}}},
				},
			})
		})
	result := batch4Execute(t, registry, "bigquery", map[string]any{
		"action": "run_bigquery_query", "sql": "SELECT id, name FROM users",
	})
	if result["success"] != true || result["total_rows"] != int64(2) || result["rows_returned"] != 2 {
		t.Fatalf("result = %v", result)
	}
	if result["bytes_processed"] != int64(1024) || result["query_truncated"] != false {
		t.Fatalf("result = %v", result)
	}
	rows := batch4BodySlice(t, result["rows"])
	first := batch4BodyMap(t, rows[0])
	if first["id"] != int64(1) || first["name"] != "Alice" {
		t.Fatalf("first row = %v", first)
	}
	if schemas := batch4BodySlice(t, result["schema"]); len(schemas) != 2 {
		t.Fatalf("schema = %v", schemas)
	}
	request := recorder.last(t)
	if request.Body["query"] != "SELECT id, name FROM users" || request.Body["maxResults"] != float64(1000) {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["useLegacySql"] != false {
		t.Fatalf("useLegacySql = %v", request.Body["useLegacySql"])
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestQueryExecution::test_query_truncation
func TestBatch6Lane4BigQueryQueryTruncation(t *testing.T) {
	env := map[string]string{"BIGQUERY_PROJECT_ID": "test-project"}
	rows := make([]any, 0, 10)
	for index := 0; index < 10; index++ {
		rows = append(rows, map[string]any{"f": []any{map[string]any{"v": "1"}}})
	}
	registry, _ := batch6Lane4TokenRegistry(t, "bigquery_tool", env,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"jobComplete":  true,
				"jobReference": map[string]any{"jobId": "job-1"},
				"totalRows":    "10",
				"schema":       map[string]any{"fields": []any{map[string]any{"name": "id", "type": "INTEGER"}}},
				"rows":         rows,
			})
		})
	result := batch4Execute(t, registry, "bigquery", map[string]any{
		"action": "run_bigquery_query", "sql": "SELECT id FROM users", "max_rows": 5,
	})
	if result["total_rows"] != int64(10) || result["rows_returned"] != 5 || result["query_truncated"] != true {
		t.Fatalf("result = %v", result)
	}
	if len(batch4BodySlice(t, result["rows"])) != 5 {
		t.Fatalf("rows = %v", result["rows"])
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestDescribeDataset::test_empty_dataset_id
func TestBatch6Lane4BigQueryDescribeEmptyDatasetID(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	for _, datasetID := range []string{"", "   "} {
		result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "describe_dataset", "dataset_id": datasetID})
		if got := batch4ErrorText(t, result); !strings.Contains(got, "dataset_id is required") {
			t.Fatalf("%q: error = %q", datasetID, got)
		}
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestDescribeDataset::test_successful_describe
func TestBatch6Lane4BigQueryDescribeDataset(t *testing.T) {
	env := map[string]string{"BIGQUERY_PROJECT_ID": "test-project"}
	registry, _ := batch6Lane4TokenRegistry(t, "bigquery_tool", env,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			switch r.URL.Path {
			case "/projects/test-project/datasets/my_dataset/tables":
				batch4WriteJSON(w, http.StatusOK, map[string]any{"tables": []any{
					map[string]any{
						"tableReference": map[string]any{"tableId": "users"},
						"type":           "TABLE",
					},
				}})
			case "/projects/test-project/datasets/my_dataset/tables/users":
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"tableReference": map[string]any{"tableId": "users"},
					"type":           "TABLE",
					"numRows":        "1000",
					"numBytes":       "10240",
					"schema": map[string]any{"fields": []any{
						map[string]any{"name": "id", "type": "INTEGER", "mode": "REQUIRED"},
						map[string]any{"name": "email", "type": "STRING", "mode": "NULLABLE"},
					}},
				})
			default:
				t.Fatalf("path = %s", r.URL.Path)
			}
		})
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "describe_dataset", "dataset_id": "my_dataset"})
	if result["success"] != true || result["dataset_id"] != "my_dataset" || result["project_id"] != "test-project" {
		t.Fatalf("result = %v", result)
	}
	tables := batch4BodySlice(t, result["tables"])
	if len(tables) != 1 {
		t.Fatalf("tables = %v", tables)
	}
	table := batch4BodyMap(t, tables[0])
	if table["table_id"] != "users" || table["row_count"] != int64(1000) || table["size_bytes"] != int64(10240) {
		t.Fatalf("table = %v", table)
	}
	if len(batch4BodySlice(t, table["columns"])) != 2 {
		t.Fatalf("columns = %v", table["columns"])
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestErrorHandling::test_authentication_error
func TestBatch6Lane4BigQueryAuthenticationError(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT 1"})
	if got := batch4ErrorText(t, result); !strings.Contains(strings.ToLower(got), "authentication failed") {
		t.Fatalf("error = %q", got)
	}
	help, _ := result["help"].(string)
	if !strings.Contains(help, "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Fatalf("help = %q", help)
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestErrorHandling::test_permission_error
func TestBatch6Lane4BigQueryPermissionError(t *testing.T) {
	env := map[string]string{"BIGQUERY_PROJECT_ID": "test-project"}
	registry, _ := batch6Lane4TokenRegistry(t, "bigquery_tool", env,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{
				"code": 403, "message": "Permission denied for table project.dataset.table",
			}})
		})
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT 1"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "BigQuery permission denied") {
		t.Fatalf("error = %q", got)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "BigQuery Data Viewer") {
		t.Fatalf("help = %q", help)
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestErrorHandling::test_not_found_error
func TestBatch6Lane4BigQueryNotFoundError(t *testing.T) {
	env := map[string]string{"BIGQUERY_PROJECT_ID": "test-project"}
	registry, _ := batch6Lane4TokenRegistry(t, "bigquery_tool", env,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{
				"code": 404, "message": "Not found: Table project.dataset.nonexistent was not found",
			}})
		})
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT 1"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "not found") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_bigquery_tool.py::TestErrorHandling::test_dataset_not_found_error
func TestBatch6Lane4BigQueryDatasetNotFound(t *testing.T) {
	env := map[string]string{"BIGQUERY_PROJECT_ID": "test-project"}
	registry, _ := batch6Lane4TokenRegistry(t, "bigquery_tool", env,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{
				"code": 404, "message": "Not found: Dataset project:nonexistent was not found",
			}})
		})
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "describe_dataset", "dataset_id": "nonexistent"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Dataset not found: nonexistent") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: the service-account JWT exchange replaces the
// google-cloud-bigquery client's credential loading; upstream tests mock
// _create_bigquery_client and never exercise the token endpoint.
func TestBatch6Lane4BigQueryServiceAccountToken(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	var tokenRequestForm map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			tokenRequestForm = map[string]string{
				"grant_type": r.PostForm.Get("grant_type"),
				"assertion":  r.PostForm.Get("assertion"),
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"access_token": "sa-token", "expires_in": 3600})
		case "/projects/sa-project/queries":
			if got := r.Header.Get("Authorization"); got != "Bearer sa-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"jobComplete":  true,
				"jobReference": map[string]any{"jobId": "job-1"},
				"totalRows":    "0",
				"schema":       map[string]any{"fields": []any{map[string]any{"name": "id", "type": "INTEGER"}}},
				"rows":         []any{},
			})
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	keyPath := filepath.Join(t.TempDir(), "service-account.json")
	keyJSON, err := json.Marshal(map[string]any{
		"type":         "service_account",
		"project_id":   "sa-project",
		"client_email": "svc@sa-project.iam.gserviceaccount.com",
		"private_key":  string(keyPEM),
		"token_uri":    server.URL + "/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": keyPath}
	registry := newBatch6Lane4Registry(
		WithBaseURL("bigquery_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	)
	result := batch4Execute(t, registry, "bigquery", map[string]any{"action": "run_bigquery_query", "sql": "SELECT 1"})
	if result["success"] != true || result["total_rows"] != int64(0) {
		t.Fatalf("result = %v", result)
	}
	if tokenRequestForm["grant_type"] != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Fatalf("grant_type = %q", tokenRequestForm["grant_type"])
	}
	parts := strings.Split(tokenRequestForm["assertion"], ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a JWT: %q", tokenRequestForm["assertion"])
	}
}
