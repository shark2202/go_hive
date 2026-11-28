package tracker

// Parity: framework/tools/tracker_tools.py::_global_sql
// Parity: framework/tools/tracker_tools.py::_global_upsert
// Parity: framework/tools/tracker_tools.py::_global_register_info

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

// GlobalDBProvider builds the cloud client used by scope='global'. Production
// calls EnvGlobalDB (HIVE_CLOUD_BASE / HIVE_CLOUD_JWT, the same pair the
// Python client reads); tests inject an httptest-backed client.
type GlobalDBProvider func() (*globaldb.Client, error)

// EnvGlobalDB mirrors Python's global_db.client._cloud_config(): an unsigned
// host is an error, never a silently-local database.
func EnvGlobalDB() (*globaldb.Client, error) { return globaldb.NewClientFromEnv() }

// globalSQLTool is the shared body of tracker_sql/tracker_query for
// scope='global'. readOnly selects the backend endpoint (/query vs /sql).
func globalSQLTool(provider GlobalDBProvider, inputs map[string]any, readOnly bool) map[string]any {
	globaldb.NoteGlobalUsed()
	sqlText, _ := asString(inputs["sql"])
	if strings.TrimSpace(sqlText) == "" {
		return failure("'sql' is required.")
	}
	client, err := provider()
	if err != nil {
		return failure(err.Error())
	}
	rowCap := rowCapOf(inputs)
	var raw any
	if readOnly {
		raw, err = client.Query(context.Background(), sqlText, &rowCap)
	} else {
		raw, err = client.SQL(context.Background(), sqlText, &rowCap)
	}
	if err != nil {
		return globalDBFailure(err)
	}
	if !readOnly {
		// A write may have changed row counts — invalidate the reminder cache.
		globaldb.GlobalCountCache.MarkDirty()
	}
	return successWith(shapeGlobalSQL(raw))
}

func globalUpsertTool(provider GlobalDBProvider, b *Binding, inputs map[string]any) map[string]any {
	globaldb.NoteGlobalUsed()
	table, _ := asString(inputs["table"])
	table = strings.TrimSpace(table)
	if table == "" {
		return failure("table is required")
	}
	row, _ := inputs["row"].(map[string]any)
	if len(row) == 0 {
		return failure("row must be a non-empty object of column→value")
	}
	client, err := provider()
	if err != nil {
		return failure(err.Error())
	}
	sourceColony := ""
	if b != nil {
		sourceColony = b.Name
	}
	raw, err := client.Upsert(context.Background(), table, row, sourceColony, "")
	if err != nil {
		return globalDBFailure(err)
	}
	globaldb.GlobalCountCache.MarkDirty()
	inserted := 1
	if body, ok := raw.(map[string]any); ok {
		if n, ok := intValue(body["inserted"]); ok {
			inserted = n
		}
	}
	return successWith(map[string]any{"table": table, "rowcount": inserted})
}

func globalDBFailure(err error) map[string]any {
	var signedOut *globaldb.NotSignedInError
	if errors.As(err, &signedOut) {
		return failure(signedOut.Error())
	}
	var dbErr *globaldb.GlobalDbError
	if errors.As(err, &dbErr) {
		return failure("global DB error: " + dbErr.Error())
	}
	return failure(err.Error())
}

// shapeGlobalSQL maps the backend's {columns, rows(objects), rowCount,
// truncated} onto the colony tracker shape ({kind:'rows'|'exec', columns,
// rows(positional), …}) so the agent sees one contract across scopes.
func shapeGlobalSQL(raw any) map[string]any {
	body, _ := raw.(map[string]any)
	columns := stringValues(body["columns"])
	rawRows, _ := body["rows"].([]any)
	if len(columns) > 0 {
		rows := make([]any, 0, len(rawRows))
		for _, rawRow := range rawRows {
			obj, _ := rawRow.(map[string]any)
			row := make([]any, len(columns))
			for i, column := range columns {
				row[i] = obj[column]
			}
			rows = append(rows, row)
		}
		rowcount := len(rows)
		if n, ok := intValue(body["rowCount"]); ok {
			rowcount = n
		}
		truncated := false
		if v, ok := body["truncated"].(bool); ok {
			truncated = v
		}
		return map[string]any{
			"kind":      "rows",
			"columns":   columns,
			"rows":      rows,
			"rowcount":  rowcount,
			"truncated": truncated,
		}
	}
	rowcount := 0
	if n, ok := intValue(body["rowCount"]); ok {
		rowcount = n
	}
	return map[string]any{"kind": "exec", "rowcount": rowcount}
}

func stringValues(v any) []string {
	items, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}
