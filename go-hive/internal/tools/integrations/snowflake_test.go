package integrations

import (
	"net/http"
	"strings"
	"testing"
)

const batch6Lane4SnowflakeEnvAccount = "SNOWFLAKE_ACCOUNT"

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeExecuteSQL::test_missing_credentials
func TestBatch6Lane4SnowflakeMissingCredentials(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "snowflake", map[string]any{"action": "snowflake_execute_sql", "statement": "SELECT 1"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "SNOWFLAKE_ACCOUNT") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeExecuteSQL::test_missing_statement
func TestBatch6Lane4SnowflakeMissingStatement(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{"action": "snowflake_execute_sql", "statement": ""})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeExecuteSQL::test_successful_sync_query
func TestBatch6Lane4SnowflakeExecuteSQLSuccess(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{
		batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1",
		"SNOWFLAKE_DATABASE":           "analytics",
		"SNOWFLAKE_WAREHOUSE":          "compute_wh",
	}, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"statementHandle": "handle-123",
			"resultSetMetaData": map[string]any{
				"numRows": 2,
				"rowType": []any{
					map[string]any{"name": "ID", "type": "fixed"},
					map[string]any{"name": "NAME", "type": "text"},
				},
			},
			"data": []any{[]any{"1", "Alice"}, []any{"2", "Bob"}},
		})
	})
	result := batch4Execute(t, registry, "snowflake", map[string]any{
		"action": "snowflake_execute_sql", "statement": "SELECT * FROM users",
	})
	if result["status"] != "complete" || result["num_rows"] != float64(2) {
		t.Fatalf("result = %v", result)
	}
	columns := batch4BodySlice(t, result["columns"])
	if len(columns) != 2 || columns[0] != "ID" || columns[1] != "NAME" {
		t.Fatalf("columns = %v", columns)
	}
	rows := batch4BodySlice(t, result["rows"])
	if len(rows) != 2 || batch4BodySlice(t, rows[0])[1] != "Alice" {
		t.Fatalf("rows = %v", rows)
	}
	request := recorder.last(t)
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("X-Snowflake-Authorization-Token-Type"); got != "OAUTH" {
		t.Fatalf("token type = %q", got)
	}
	if request.Body["database"] != "analytics" || request.Body["warehouse"] != "compute_wh" {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["timeout"] != float64(60) {
		t.Fatalf("timeout = %v", request.Body["timeout"])
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeExecuteSQL::test_async_query
func TestBatch6Lane4SnowflakeExecuteSQLAsync(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusAccepted, map[string]any{
				"statementHandle": "handle-456",
				"message":         "Asynchronous execution in progress.",
			})
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{
		"action": "snowflake_execute_sql", "statement": "SELECT * FROM big_table",
	})
	if result["status"] != "running" || result["statement_handle"] != "handle-456" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeGetStatementStatus::test_complete_result
func TestBatch6Lane4SnowflakeGetStatementStatus(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v2/statements/handle-123" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"statementHandle": "handle-123",
				"resultSetMetaData": map[string]any{
					"numRows": 1,
					"rowType": []any{map[string]any{"name": "COUNT", "type": "fixed"}},
				},
				"data": []any{[]any{"42"}},
			})
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{
		"action": "snowflake_get_statement_status", "statement_handle": "handle-123",
	})
	if result["status"] != "complete" {
		t.Fatalf("result = %v", result)
	}
	if rows := batch4BodySlice(t, result["rows"]); len(rows) != 1 || batch4BodySlice(t, rows[0])[0] != "42" {
		t.Fatalf("rows = %v", rows)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeGetStatementStatus::test_still_running
func TestBatch6Lane4SnowflakeGetStatementStatusRunning(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusAccepted, map[string]any{
				"statementHandle": "handle-456",
				"message":         "Still executing",
			})
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{
		"action": "snowflake_get_statement_status", "statement_handle": "handle-456",
	})
	if result["status"] != "running" || result["message"] != "Still executing" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeGetStatementStatus::test_query_error
func TestBatch6Lane4SnowflakeGetStatementStatusError(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"statementHandle": "handle-789",
				"message":         "SQL compilation error",
			})
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{
		"action": "snowflake_get_statement_status", "statement_handle": "handle-789",
	})
	if result["status"] != "error" || result["message"] != "SQL compilation error" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeCancelStatement::test_successful_cancel
func TestBatch6Lane4SnowflakeCancelStatement(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements/handle-123/cancel" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"statementHandle": "handle-123"})
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{
		"action": "snowflake_cancel_statement", "statement_handle": "handle-123",
	})
	if result["result"] != "cancelled" || result["statement_handle"] != "handle-123" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_snowflake_tool.py::TestSnowflakeCancelStatement::test_missing_handle
func TestBatch6Lane4SnowflakeCancelMissingHandle(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "snowflake_tool", map[string]string{batch6Lane4SnowflakeEnvAccount: "xy12345.us-east-1"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "snowflake", map[string]any{"action": "snowflake_cancel_statement", "statement_handle": ""})
	batch4ErrorText(t, result)
}
