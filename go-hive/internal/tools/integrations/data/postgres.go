// PostgreSQL integration (upstream postgres_tool, 8 actions). Part of batch 5
// lane 2; the wire protocol lives in pgwire.go.
//
// Parity notes:
//   - The upstream SQL guard is reproduced verbatim: single statement,
//     SELECT-only, forbidden mutation keywords.
//   - Like upstream, the guard is the first pass and a read-only session is the
//     second: every connection issues SET default_transaction_read_only = on
//     (best effort) and SET statement_timeout TO 3000.
//   - Results are capped at 1000 rows and returned as columns/rows arrays with
//     psycopg2-like value conversion (ints stay exact, numeric becomes float64,
//     booleans become bool, json/jsonb are decoded).
//   - Deliberate limit: connections are opened per action (no pool), and only
//     trust/cleartext/MD5/SCRAM-SHA-256 authentication is supported.
package data

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::validate_sql
const (
	pgMaxRows        = 1000
	pgStatementMS    = 3000
	pgMissingMessage = "Missing required credential: PostgreSQL connection string (postgresql://user:***@host:port/db)"
)

const pgHelpText = `Provide a PostgreSQL connection string:

postgresql://user:***@host:port/database

Example:
postgresql://postgres:***@localhost:5432/mydb

The database user should have read-only permissions.`

var pgForbiddenPattern = regexp.MustCompile(
	`(?i)\b(insert|update|delete|merge|upsert|create|alter|drop|truncate|grant|revoke|` +
		`call|execute|prepare|deallocate|vacuum|analyze)\b`,
)

// pgValidateSQL enforces single-statement SELECT-only queries.
func pgValidateSQL(sql string) (string, error) {
	sql = strings.TrimSpace(sql)
	if strings.HasSuffix(sql, ";") {
		sql = strings.TrimSuffix(sql, ";")
	}
	if strings.Contains(sql, ";") {
		return "", fmt.Errorf("Multiple statements are not allowed")
	}
	if !strings.HasPrefix(strings.ToLower(sql), "select") {
		return "", fmt.Errorf("Only SELECT queries are allowed")
	}
	if pgForbiddenPattern.MatchString(sql) {
		return "", fmt.Errorf("Forbidden SQL keyword detected")
	}
	return sql, nil
}

func pgMissingCredential() map[string]any {
	return map[string]any{
		"error":   pgMissingMessage,
		"help":    pgHelpText,
		"success": false,
	}
}

// pgOpen resolves the credential, dials and applies the read-only session
// settings. On failure it returns a ready-to-return error response (when the
// failure is user-visible) or an infrastructure error.
func pgOpen(ctx context.Context, rt *Runtime) (*pgConn, map[string]any, error) {
	dsn := configuredToken(rt, "postgres_tool", "DATABASE_URL")
	if dsn == "" {
		return nil, pgMissingCredential(), nil
	}
	conn, err := pgConnect(ctx, dsn)
	if err != nil {
		return nil, nil, err
	}
	if err := conn.SimpleExec(ctx, fmt.Sprintf("SET statement_timeout TO %d", pgStatementMS)); err != nil {
		conn.Close()
		return nil, nil, err
	}
	// Best effort: some transaction poolers reject session-level SET. The SQL
	// guard above remains the primary read-only enforcement.
	_ = conn.SimpleExec(ctx, "SET default_transaction_read_only = on")
	return conn, nil, nil
}

// pgRead runs one read-only query with no row cap.
func pgRead(ctx context.Context, rt *Runtime, sql string, params map[string]any) (*pgResult, map[string]any, error) {
	conn, response, err := pgOpen(ctx, rt)
	if response != nil || err != nil {
		return nil, response, err
	}
	defer conn.Close()
	result, err := conn.Query(ctx, sql, params, 0)
	if err != nil {
		return nil, nil, err
	}
	return result, nil, nil
}

// ------------------------------------------------------------------ actions ---

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_query
func pgQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sql := stringArg(input, "sql")
	params := objectArg(input["params"])
	if configuredToken(rt, "postgres_tool", "DATABASE_URL") == "" {
		return pgMissingCredential(), nil
	}
	cleaned, err := pgValidateSQL(sql)
	if err != nil {
		return databaseErr(err.Error()), nil
	}
	start := time.Now()
	conn, response, openErr := pgOpen(ctx, rt)
	if response != nil {
		return response, nil
	}
	if openErr != nil {
		return databaseErr("Database error while executing query: " + openErr.Error()), nil
	}
	defer conn.Close()
	result, queryErr := conn.Query(ctx, cleaned, params, pgMaxRows)
	if queryErr != nil {
		if pgIsQueryCanceled(queryErr) {
			return databaseErr("Query timed out"), nil
		}
		return databaseErr("Database error while executing query: " + queryErr.Error()), nil
	}
	return map[string]any{
		"columns":     result.Columns,
		"rows":        result.Rows,
		"row_count":   len(result.Rows),
		"max_rows":    pgMaxRows,
		"duration_ms": time.Since(start).Milliseconds(),
		"success":     true,
	}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_list_schemas
func pgListSchemas(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	const sql = `SELECT schema_name
FROM information_schema.schemata
ORDER BY schema_name`
	result, response, err := pgRead(ctx, rt, sql, nil)
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to list schemas: " + err.Error()), nil
	}
	schemas := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) > 0 {
			schemas = append(schemas, row[0])
		}
	}
	return map[string]any{"result": schemas, "success": true}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_list_tables
func pgListTables(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sql := `SELECT table_schema, table_name
FROM information_schema.tables
WHERE table_type = 'BASE TABLE'`
	var params map[string]any
	if schema := stringArg(input, "schema"); schema != "" {
		sql += " AND table_schema = %(schema)s"
		params = map[string]any{"schema": schema}
	}
	result, response, err := pgRead(ctx, rt, sql, params)
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to list tables: " + err.Error()), nil
	}
	tables := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 2 {
			continue
		}
		tables = append(tables, map[string]any{"schema": row[0], "table": row[1]})
	}
	return map[string]any{"result": tables, "success": true}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_describe_table
func pgDescribeTable(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	schema, table := stringArg(input, "schema"), stringArg(input, "table")
	if schema == "" || table == "" {
		return databaseErr("schema and table are required"), nil
	}
	const sql = `SELECT
    column_name,
    data_type,
    is_nullable,
    column_default
FROM information_schema.columns
WHERE table_schema = %(schema)s
  AND table_name = %(table)s
ORDER BY ordinal_position`
	result, response, err := pgRead(ctx, rt, sql, map[string]any{"schema": schema, "table": table})
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to describe table: " + err.Error()), nil
	}
	columns := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 4 {
			continue
		}
		columns = append(columns, map[string]any{
			"column":   row[0],
			"type":     row[1],
			"nullable": row[2],
			"default":  row[3],
		})
	}
	return map[string]any{"result": columns, "success": true}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_explain
func pgExplain(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if configuredToken(rt, "postgres_tool", "DATABASE_URL") == "" {
		return pgMissingCredential(), nil
	}
	cleaned, err := pgValidateSQL(stringArg(input, "sql"))
	if err != nil {
		return databaseErr(err.Error()), nil
	}
	result, response, err := pgRead(ctx, rt, "EXPLAIN "+cleaned, nil)
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to explain query: " + err.Error()), nil
	}
	plan := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) > 0 {
			plan = append(plan, row[0])
		}
	}
	return map[string]any{"result": plan, "success": true}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_get_table_stats
func pgGetTableStats(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	schema := stringArg(input, "schema")
	if schema == "" {
		schema = "public"
	}
	const sql = `SELECT
    t.tablename AS table_name,
    c.reltuples::bigint AS estimated_rows,
    pg_size_pretty(pg_total_relation_size(
        quote_ident(t.schemaname) || '.' || quote_ident(t.tablename)
    )) AS total_size,
    pg_size_pretty(pg_indexes_size(
        quote_ident(t.schemaname) || '.' || quote_ident(t.tablename)
    )) AS index_size,
    pg_total_relation_size(
        quote_ident(t.schemaname) || '.' || quote_ident(t.tablename)
    ) AS total_bytes
FROM pg_tables t
JOIN pg_class c ON c.relname = t.tablename
JOIN pg_namespace n ON n.oid = c.relnamespace
    AND n.nspname = t.schemaname
WHERE t.schemaname = %(schema)s
ORDER BY pg_total_relation_size(
    quote_ident(t.schemaname) || '.' || quote_ident(t.tablename)
) DESC`
	result, response, err := pgRead(ctx, rt, sql, map[string]any{"schema": schema})
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to get table stats: " + err.Error()), nil
	}
	stats := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 5 {
			continue
		}
		stats = append(stats, map[string]any{
			"table":          row[0],
			"estimated_rows": row[1],
			"total_size":     row[2],
			"index_size":     row[3],
			"total_bytes":    row[4],
		})
	}
	return map[string]any{"schema": schema, "result": stats, "success": true}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_list_indexes
func pgListIndexes(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	schema, table := stringArg(input, "schema"), stringArg(input, "table")
	if schema == "" || table == "" {
		return databaseErr("schema and table are required"), nil
	}
	const sql = `SELECT
    i.relname AS index_name,
    array_to_string(array_agg(a.attname ORDER BY k.n), ', ') AS columns,
    ix.indisunique AS is_unique,
    ix.indisprimary AS is_primary,
    am.amname AS index_type,
    pg_size_pretty(pg_relation_size(i.oid)) AS index_size
FROM pg_index ix
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
JOIN pg_am am ON am.oid = i.relam
CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS k(attnum, n)
JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
WHERE n.nspname = %(schema)s AND t.relname = %(table)s
GROUP BY i.relname, ix.indisunique, ix.indisprimary, am.amname, i.oid
ORDER BY i.relname`
	result, response, err := pgRead(ctx, rt, sql, map[string]any{"schema": schema, "table": table})
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to list indexes: " + err.Error()), nil
	}
	indexes := make([]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if len(row) < 6 {
			continue
		}
		indexes = append(indexes, map[string]any{
			"name":    row[0],
			"columns": row[1],
			"unique":  row[2],
			"primary": row[3],
			"type":    row[4],
			"size":    row[5],
		})
	}
	return map[string]any{"schema": schema, "table": table, "result": indexes, "success": true}, nil
}

// Parity: tools/src/aden_tools/tools/postgres_tool/postgres_tool.py::pg_get_foreign_keys
func pgGetForeignKeys(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	schema, table := stringArg(input, "schema"), stringArg(input, "table")
	if schema == "" || table == "" {
		return databaseErr("schema and table are required"), nil
	}
	outgoingSQL := `SELECT
    tc.constraint_name,
    kcu.column_name,
    ccu.table_schema AS ref_schema,
    ccu.table_name AS ref_table,
    ccu.column_name AS ref_column
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
    ON tc.constraint_name = kcu.constraint_name
    AND tc.table_schema = kcu.table_schema
JOIN information_schema.constraint_column_usage ccu
    ON ccu.constraint_name = tc.constraint_name
WHERE tc.constraint_type = 'FOREIGN KEY'
    AND tc.table_schema = %(schema)s
    AND tc.table_name = %(table)s
ORDER BY tc.constraint_name`
	incomingSQL := `SELECT
    tc.constraint_name,
    tc.table_schema AS source_schema,
    tc.table_name AS source_table,
    kcu.column_name AS source_column,
    ccu.column_name AS referenced_column
FROM information_schema.table_constraints tc
JOIN information_schema.key_column_usage kcu
    ON tc.constraint_name = kcu.constraint_name
    AND tc.table_schema = kcu.table_schema
JOIN information_schema.constraint_column_usage ccu
    ON ccu.constraint_name = tc.constraint_name
WHERE tc.constraint_type = 'FOREIGN KEY'
    AND ccu.table_schema = %(schema)s
    AND ccu.table_name = %(table)s
ORDER BY tc.constraint_name`
	params := map[string]any{"schema": schema, "table": table}
	outgoing, response, err := pgRead(ctx, rt, outgoingSQL, params)
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to get foreign keys: " + err.Error()), nil
	}
	incoming, response, err := pgRead(ctx, rt, incomingSQL, params)
	if response != nil {
		return response, nil
	}
	if err != nil {
		return databaseErr("Failed to get foreign keys: " + err.Error()), nil
	}
	outRows := make([]any, 0, len(outgoing.Rows))
	for _, row := range outgoing.Rows {
		if len(row) < 5 {
			continue
		}
		outRows = append(outRows, map[string]any{
			"constraint":        row[0],
			"column":            row[1],
			"references_schema": row[2],
			"references_table":  row[3],
			"references_column": row[4],
		})
	}
	inRows := make([]any, 0, len(incoming.Rows))
	for _, row := range incoming.Rows {
		if len(row) < 5 {
			continue
		}
		inRows = append(inRows, map[string]any{
			"constraint":        row[0],
			"source_schema":     row[1],
			"source_table":      row[2],
			"source_column":     row[3],
			"referenced_column": row[4],
		})
	}
	return map[string]any{
		"schema":   schema,
		"table":    table,
		"outgoing": outRows,
		"incoming": inRows,
		"success":  true,
	}, nil
}

// postgresImpls maps the upstream action names to handlers.
func postgresImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pg_query":            pgQuery,
		"pg_list_schemas":     pgListSchemas,
		"pg_list_tables":      pgListTables,
		"pg_describe_table":   pgDescribeTable,
		"pg_explain":          pgExplain,
		"pg_get_table_stats":  pgGetTableStats,
		"pg_list_indexes":     pgListIndexes,
		"pg_get_foreign_keys": pgGetForeignKeys,
	}
}

// postgresTool is the model-facing catalog entry.
var postgresTool = &Tool{
	ID:          "postgres_tool",
	Name:        "postgres",
	DisplayName: "PostgreSQL",
	Summary:     "PostgreSQL read-only integration (8 actions).",
	Actions: []Action{
		{
			Name:     "pg_query",
			Summary:  "Execute a read-only SELECT query.",
			Source:   "postgres_tool/postgres_tool.py",
			Required: []string{"sql"},
			Params: map[string]any{
				"sql":    param("string"),
				"params": param("object"),
			},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_list_schemas",
			Summary:  "List all schemas in the database.",
			Source:   "postgres_tool/postgres_tool.py",
			Params:   map[string]any{},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_list_tables",
			Summary:  "List tables, optionally filtered by schema.",
			Source:   "postgres_tool/postgres_tool.py",
			Params:   map[string]any{"schema": paramDefault("string", "")},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_describe_table",
			Summary:  "Describe the columns of a table.",
			Source:   "postgres_tool/postgres_tool.py",
			Required: []string{"schema", "table"},
			Params: map[string]any{
				"schema": param("string"),
				"table":  param("string"),
			},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_explain",
			Summary:  "Explain the execution plan of a query.",
			Source:   "postgres_tool/postgres_tool.py",
			Required: []string{"sql"},
			Params:   map[string]any{"sql": param("string")},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_get_table_stats",
			Summary:  "Get row counts and size statistics for tables in a schema.",
			Source:   "postgres_tool/postgres_tool.py",
			Params:   map[string]any{"schema": paramDefault("string", "public")},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_list_indexes",
			Summary:  "List indexes on a table.",
			Source:   "postgres_tool/postgres_tool.py",
			Required: []string{"schema", "table"},
			Params: map[string]any{
				"schema": param("string"),
				"table":  param("string"),
			},
			Requires: []string{"DATABASE_URL"},
		},
		{
			Name:     "pg_get_foreign_keys",
			Summary:  "Get outgoing and incoming foreign keys for a table.",
			Source:   "postgres_tool/postgres_tool.py",
			Required: []string{"schema", "table"},
			Params: map[string]any{
				"schema": param("string"),
				"table":  param("string"),
			},
			Requires: []string{"DATABASE_URL"},
		},
	},
}
