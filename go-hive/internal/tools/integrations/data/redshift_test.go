package data

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

var batch6Lane4RedshiftEnv = map[string]string{
	"AWS_ACCESS_KEY_ID":     "AKIAIOSFODNN7EXAMPLE",
	"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	"AWS_REGION":            "us-east-1",
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftExecuteSQL::test_missing_credentials
func TestBatch6Lane4RedshiftMissingCredentials(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "redshift_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_execute_sql", "sql": "SELECT 1", "database": "dev", "cluster_identifier": "my-cluster",
	})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "AWS_ACCESS_KEY_ID") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftExecuteSQL::test_missing_sql
func TestBatch6Lane4RedshiftMissingSQL(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_execute_sql", "sql": "", "database": "dev", "cluster_identifier": "my-cluster",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftExecuteSQL::test_missing_cluster
func TestBatch6Lane4RedshiftMissingCluster(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_execute_sql", "sql": "SELECT 1", "database": "dev",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftExecuteSQL::test_successful_execute
func TestBatch6Lane4RedshiftExecuteSQL(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("X-Amz-Target"); got != "RedshiftData.ExecuteStatement" {
				t.Fatalf("X-Amz-Target = %q", got)
			}
			authorization := r.Header.Get("Authorization")
			if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/") ||
				!strings.Contains(authorization, "/us-east-1/redshift-data/aws4_request") {
				t.Fatalf("Authorization = %q", authorization)
			}
			if r.Header.Get("X-Amz-Date") == "" {
				t.Fatal("missing X-Amz-Date")
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"Id":        "stmt-abc123",
				"CreatedAt": 1598323200.0,
				"Database":  "dev",
			})
		}, WithClock(func() time.Time {
			return time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC)
		}))
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_execute_sql", "sql": "SELECT * FROM users", "database": "dev", "cluster_identifier": "my-cluster",
	})
	if result["statement_id"] != "stmt-abc123" || result["status"] != "submitted" || result["database"] != "dev" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Body["Sql"] != "SELECT * FROM users" ||
		request.Body["Database"] != "dev" ||
		request.Body["ClusterIdentifier"] != "my-cluster" {
		t.Fatalf("body = %v", request.Body)
	}
	if got := request.Header.Get("X-Amz-Date"); got != "20261119T010203Z" {
		t.Fatalf("X-Amz-Date = %q", got)
	}
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftDescribeStatement::test_successful_describe
func TestBatch6Lane4RedshiftDescribeStatement(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if got := r.Header.Get("X-Amz-Target"); got != "RedshiftData.DescribeStatement" {
				t.Fatalf("X-Amz-Target = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"Id":           "stmt-abc123",
				"Status":       "FINISHED",
				"HasResultSet": true,
				"ResultRows":   10,
				"Duration":     1500000000,
				"QueryString":  "SELECT * FROM users",
				"Error":        "",
			})
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_describe_statement", "statement_id": "stmt-abc123",
	})
	if result["status"] != "FINISHED" || result["has_result_set"] != true || result["result_rows"] != float64(10) {
		t.Fatalf("result = %v", result)
	}
	if result["error"] != nil {
		t.Fatalf("error should be nil, got %v", result["error"])
	}
	if recorder.last(t).Body["Id"] != "stmt-abc123" {
		t.Fatalf("body = %v", recorder.last(t).Body)
	}
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftGetResults::test_successful_get
func TestBatch6Lane4RedshiftGetResults(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if got := r.Header.Get("X-Amz-Target"); got != "RedshiftData.GetStatementResult" {
				t.Fatalf("X-Amz-Target = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"ColumnMetadata": []any{
					map[string]any{"name": "id", "typeName": "int4"},
					map[string]any{"name": "email", "typeName": "varchar"},
				},
				"Records": []any{
					[]any{map[string]any{"longValue": 1}, map[string]any{"stringValue": "alice@example.com"}},
					[]any{map[string]any{"longValue": 2}, map[string]any{"stringValue": "bob@example.com"}},
				},
				"TotalNumRows": 2,
			})
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_get_results", "statement_id": "stmt-abc123",
	})
	columns := batch4BodySlice(t, result["columns"])
	if len(columns) != 2 || columns[0] != "id" || columns[1] != "email" {
		t.Fatalf("columns = %v", columns)
	}
	rows := batch4BodySlice(t, result["rows"])
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	first := batch4BodySlice(t, rows[0])
	if fmt.Sprint(first[0]) != "1" || first[1] != "alice@example.com" {
		t.Fatalf("first row = %v", first)
	}
	if result["total_rows"] != float64(2) {
		t.Fatalf("total_rows = %v", result["total_rows"])
	}
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftListDatabases::test_missing_cluster
func TestBatch6Lane4RedshiftListDatabasesMissingCluster(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{"action": "redshift_list_databases"})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftListDatabases::test_successful_list
func TestBatch6Lane4RedshiftListDatabases(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if got := r.Header.Get("X-Amz-Target"); got != "RedshiftData.ListDatabases" {
				t.Fatalf("X-Amz-Target = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"Databases": []any{"dev", "staging", "analytics"},
				"NextToken": "",
			})
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_list_databases", "cluster_identifier": "my-cluster",
	})
	if result["count"] != 3 {
		t.Fatalf("result = %v", result)
	}
	databases := batch4BodySlice(t, result["databases"])
	if len(databases) != 3 || databases[0] != "dev" {
		t.Fatalf("databases = %v", databases)
	}
	request := recorder.last(t)
	if request.Body["Database"] != "dev" || request.Body["MaxResults"] != float64(100) {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftListTables::test_missing_database
func TestBatch6Lane4RedshiftListTablesMissingDatabase(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_list_tables", "database": "", "cluster_identifier": "my-cluster",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_redshift_tool.py::TestRedshiftListTables::test_successful_list
func TestBatch6Lane4RedshiftListTables(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "redshift_tool", batch6Lane4RedshiftEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if got := r.Header.Get("X-Amz-Target"); got != "RedshiftData.ListTables" {
				t.Fatalf("X-Amz-Target = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"Tables": []any{
					map[string]any{"name": "users", "schema": "public", "type": "TABLE"},
					map[string]any{"name": "orders", "schema": "public", "type": "TABLE"},
				},
				"NextToken": "",
			})
		})
	result := batch4Execute(t, registry, "redshift", map[string]any{
		"action": "redshift_list_tables", "database": "dev", "cluster_identifier": "my-cluster",
	})
	if result["count"] != 2 {
		t.Fatalf("result = %v", result)
	}
	tables := batch4BodySlice(t, result["tables"])
	if len(tables) != 2 || batch4BodyMap(t, tables[0])["name"] != "users" {
		t.Fatalf("tables = %v", tables)
	}
	if request := recorder.last(t); request.Body["SchemaPattern"] != "public" {
		t.Fatalf("body = %v", request.Body)
	}
}
