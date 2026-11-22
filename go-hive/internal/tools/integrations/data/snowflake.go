// Snowflake SQL REST API v2 (upstream snowflake_tool, 3 actions).
//
// The upstream module posts to
// https://<account>.snowflakecomputing.com/api/v2/statements; this port keeps
// the same endpoint, request body, result projection and status handling.
package data

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const snowflakeMaxRows = 100

var snowflakeTool = &Tool{
	ID:          "snowflake_tool",
	Name:        "snowflake",
	DisplayName: "Snowflake",
	Summary:     "Snowflake SQL REST API integration (3 actions).",
	Actions: []Action{
		{
			Name:     "snowflake_execute_sql",
			Summary:  "Execute a SQL statement on Snowflake and return results.",
			Source:   "snowflake_tool/snowflake_tool.py",
			Required: []string{"statement"},
			Params: map[string]any{
				"statement": param("string"),
				"database":  paramDefault("string", ""),
				"schema":    paramDefault("string", ""),
				"warehouse": paramDefault("string", ""),
				"timeout":   paramDefault("integer", 60),
			},
		},
		{
			Name:     "snowflake_get_statement_status",
			Summary:  "Check the status of a Snowflake SQL statement and fetch results.",
			Source:   "snowflake_tool/snowflake_tool.py",
			Required: []string{"statement_handle"},
			Params: map[string]any{
				"statement_handle": param("string"),
			},
		},
		{
			Name:     "snowflake_cancel_statement",
			Summary:  "Cancel a running Snowflake SQL statement.",
			Source:   "snowflake_tool/snowflake_tool.py",
			Required: []string{"statement_handle"},
			Params: map[string]any{
				"statement_handle": param("string"),
			},
		},
	},
}

func snowflakeImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"snowflake_execute_sql":          snowflakeExecuteSQL,
		"snowflake_get_statement_status": snowflakeGetStatementStatus,
		"snowflake_cancel_statement":     snowflakeCancelStatement,
	}
}

// snowflakeBase resolves the statements endpoint. WithBaseURL wins
// (tests); otherwise the account subdomain builds the public URL, like
// upstream.
func snowflakeBase(rt *Runtime) string {
	if base := moduleBaseURL(rt, "snowflake_tool"); base != "" {
		return base + "/api/v2/statements"
	}
	account := strings.TrimSpace(runtimeEnv(rt, "SNOWFLAKE_ACCOUNT"))
	return "https://" + account + ".snowflakecomputing.com/api/v2/statements"
}

// snowflakeConfig mirrors upstream _get_config: token plus the
// account (unless the base URL is overridden).
func snowflakeConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	token := strings.TrimSpace(moduleToken(rt, "snowflake_tool"))
	account := strings.TrimSpace(runtimeEnv(rt, "SNOWFLAKE_ACCOUNT"))
	base := moduleBaseURL(rt, "snowflake_tool")
	if token == "" || (base == "" && account == "") {
		return "", nil, map[string]any{
			"error": "SNOWFLAKE_ACCOUNT and SNOWFLAKE_TOKEN are required",
			"help":  "Set SNOWFLAKE_ACCOUNT and SNOWFLAKE_TOKEN environment variables",
		}
	}
	tokenType := runtimeEnv(rt, "SNOWFLAKE_TOKEN_TYPE")
	if tokenType == "" {
		tokenType = "OAUTH"
	}
	return snowflakeBase(rt), map[string]string{
		"Authorization":                        "Bearer " + token,
		"Content-Type":                         "application/json",
		"Accept":                               "application/json",
		"User-Agent":                           "hive_tools/1.0",
		"X-Snowflake-Authorization-Token-Type": tokenType,
	}, nil
}

// snowflakeFormat mirrors upstream _format_results.
func snowflakeFormat(data map[string]any) map[string]any {
	meta := asMap(data["resultSetMetaData"])
	columns := make([]any, 0)
	for _, rawColumn := range asSlice(meta["rowType"]) {
		columns = append(columns, asString(asMap(rawColumn)["name"]))
	}
	rows := asSlice(data["data"])
	if rows == nil {
		rows = []any{}
	}
	numRows := any(len(rows))
	if value, present := meta["numRows"]; present {
		numRows = value
	}
	truncated := len(rows) > snowflakeMaxRows
	shown := rows
	if truncated {
		shown = rows[:snowflakeMaxRows]
	}
	return map[string]any{
		"statement_handle": data["statementHandle"],
		"status":           "complete",
		"num_rows":         numRows,
		"columns":          columns,
		"rows":             shown,
		"truncated":        truncated,
	}
}

// snowflakeDecode decodes a response body, reporting invalid JSON.
func snowflakeDecode(resp *platformResponse) (map[string]any, map[string]any) {
	data, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Snowflake API error: invalid JSON response"}
	}
	return data, nil
}

func snowflakeExecuteSQL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := snowflakeConfig(rt)
	if failure != nil {
		return failure, nil
	}
	statement := stringArg(input, "statement")
	if strings.TrimSpace(statement) == "" {
		return map[string]any{"error": "statement is required"}, nil
	}
	timeout := intArg(input, "timeout", 60)
	body := map[string]any{
		"statement": statement,
		"timeout":   timeout,
	}
	database := stringArg(input, "database")
	if database == "" {
		database = runtimeEnv(rt, "SNOWFLAKE_DATABASE")
	}
	if database != "" {
		body["database"] = database
	}
	schema := stringArg(input, "schema")
	if schema == "" {
		schema = runtimeEnv(rt, "SNOWFLAKE_SCHEMA")
	}
	if schema != "" {
		body["schema"] = schema
	}
	warehouse := stringArg(input, "warehouse")
	if warehouse == "" {
		warehouse = runtimeEnv(rt, "SNOWFLAKE_WAREHOUSE")
	}
	if warehouse != "" {
		body["warehouse"] = warehouse
	}

	// Upstream waits timeout+10s, never less than 30s.
	requestTimeout := time.Duration(timeout+10) * time.Second
	if requestTimeout < 30*time.Second {
		requestTimeout = 30 * time.Second
	}
	resp, err := doJSON(ctx, rt, http.MethodPost, base, headers, body, requestTimeout)
	if err != nil {
		return transportError(ctx, "Snowflake", err), nil
	}
	switch resp.Status {
	case http.StatusOK:
		data, failure := snowflakeDecode(resp)
		if failure != nil {
			return failure, nil
		}
		return snowflakeFormat(data), nil
	case http.StatusAccepted:
		data, failure := snowflakeDecode(resp)
		if failure != nil {
			return failure, nil
		}
		return map[string]any{
			"statement_handle": data["statementHandle"],
			"status":           "running",
			"message":          stringOr(data, "message", "Asynchronous execution in progress"),
		}, nil
	default:
		return httpError(resp), nil
	}
}

func snowflakeGetStatementStatus(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := snowflakeConfig(rt)
	if failure != nil {
		return failure, nil
	}
	handle := stringArg(input, "statement_handle")
	if handle == "" {
		return map[string]any{"error": "statement_handle is required"}, nil
	}
	resp, err := doJSON(ctx, rt, http.MethodGet, base+"/"+handle, headers, nil, 30*time.Second)
	if err != nil {
		return transportError(ctx, "Snowflake", err), nil
	}
	switch resp.Status {
	case http.StatusOK:
		data, failure := snowflakeDecode(resp)
		if failure != nil {
			return failure, nil
		}
		return snowflakeFormat(data), nil
	case http.StatusAccepted:
		data, failure := snowflakeDecode(resp)
		if failure != nil {
			return failure, nil
		}
		return map[string]any{
			"statement_handle": data["statementHandle"],
			"status":           "running",
			"message":          stringOr(data, "message", "Still executing"),
		}, nil
	case http.StatusUnprocessableEntity:
		data, failure := snowflakeDecode(resp)
		if failure != nil {
			return failure, nil
		}
		return map[string]any{
			"statement_handle": data["statementHandle"],
			"status":           "error",
			"message":          stringOr(data, "message", "Query failed"),
		}, nil
	default:
		return httpError(resp), nil
	}
}

func snowflakeCancelStatement(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := snowflakeConfig(rt)
	if failure != nil {
		return failure, nil
	}
	handle := stringArg(input, "statement_handle")
	if handle == "" {
		return map[string]any{"error": "statement_handle is required"}, nil
	}
	resp, err := doJSON(ctx, rt, http.MethodPost, base+"/"+handle+"/cancel", headers, nil, 30*time.Second)
	if err != nil {
		return transportError(ctx, "Snowflake", err), nil
	}
	if resp.Status != http.StatusOK {
		return httpError(resp), nil
	}
	return map[string]any{"result": "cancelled", "statement_handle": handle}, nil
}
