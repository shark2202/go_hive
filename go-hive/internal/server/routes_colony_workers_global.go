package server

// Parity: framework/server/routes_colony_workers.py::handle_global_list_tables

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

const foreignKeyIntrospectSQL = "SELECT tc.table_name AS table_name, kcu.column_name AS column_name, " +
	"ccu.table_name AS ref_table, ccu.column_name AS ref_column " +
	"FROM information_schema.table_constraints tc " +
	"JOIN information_schema.key_column_usage kcu " +
	"ON tc.constraint_name = kcu.constraint_name " +
	"AND tc.constraint_schema = kcu.constraint_schema " +
	"JOIN information_schema.constraint_column_usage ccu " +
	"ON tc.constraint_name = ccu.constraint_name " +
	"AND tc.constraint_schema = ccu.constraint_schema " +
	"WHERE tc.constraint_type = 'FOREIGN KEY' " +
	"AND tc.table_schema NOT IN ('pg_catalog', 'information_schema') " +
	"ORDER BY tc.table_name, kcu.column_name"

const (
	crmExitNotSignedIn = 2
	crmExitPermission  = 3
	crmExitNotFound    = 4
)

type CRMError struct {
	Message  string
	Code     string
	ExitCode int
}

func (e *CRMError) Error() string { return e.Message }

type globalQueryResult struct {
	Table      string `json:"table"`
	Columns    any    `json:"columns"`
	PrimaryKey any    `json:"primary_key"`
	Rows       any    `json:"rows"`
	Total      int    `json:"total"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

type globalGroupCount struct {
	Value any `json:"value"`
	Count int `json:"count"`
}

type globalGroupCounts struct {
	Table   string             `json:"table"`
	GroupBy any                `json:"group_by"`
	Groups  []globalGroupCount `json:"groups"`
}

type globalForeignKey struct {
	Table     any `json:"table"`
	Column    any `json:"column"`
	RefTable  any `json:"ref_table"`
	RefColumn any `json:"ref_column"`
}

func (d ColonyWorkerDeps) globalDB() (*globaldb.Client, error) {
	if d.GlobalDB != nil {
		return d.GlobalDB()
	}
	return globaldb.NewClientFromEnv()
}

func writeGlobalDBFailure(w http.ResponseWriter, err error) {
	status, body := GlobalDBErrorResponse(err)
	writeJSON(w, status, body)
}

func asObject(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}

func asList(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

func asStringList(value any) []string {
	out := []string{}
	for _, item := range asList(value) {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func asObjectList(value any) []map[string]any {
	out := []map[string]any{}
	for _, item := range asList(value) {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func globalColumnSet(columns []map[string]any) globaldb.ColumnSet {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		if name, ok := column["name"].(string); ok {
			names = append(names, name)
		}
	}
	return globaldb.NewColumnSet(names...)
}

func globalTableColumns(columns []map[string]any) []TableColumn {
	out := make([]TableColumn, 0, len(columns))
	for _, column := range columns {
		name, _ := column["name"].(string)
		colType, _ := column["type"].(string)
		notNull, _ := column["notnull"].(bool)
		pk, _ := pyIntValue(column["pk"])
		out = append(out, TableColumn{Name: name, Type: colType, NotNull: notNull, PK: pk})
	}
	return out
}

// Parity: framework/server/routes_colony_workers.py::_global_introspect
func globalIntrospect(ctx context.Context, client *globaldb.Client, table string) ([]map[string]any, []string, error) {
	meta, err := client.ListRows(ctx, table, url.Values{"limit": {"1"}, "offset": {"0"}})
	if err != nil {
		return nil, nil, err
	}
	columns := []map[string]any{}
	primaryKey := []string{}
	if object := asObject(meta); object != nil {
		columns = asObjectList(object["columns"])
		primaryKey = asStringList(object["primary_key"])
	}
	return columns, primaryKey, nil
}

func recordGlobalTablesResult(result any) {
	object := asObject(result)
	if object == nil {
		return
	}
	globaldb.RecordGlobalTables(asObjectList(object["tables"]))
}

func handleGlobalListTables(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	result, err := client.ListTables(r.Context())
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	recordGlobalTablesResult(result)
	writeJSON(w, http.StatusOK, result)
}

func handleGlobalTableRows(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	query := r.URL.Query()
	params := url.Values{}
	for _, key := range []string{"limit", "offset", "order_by", "order_dir"} {
		if values, ok := query[key]; ok && len(values) > 0 {
			params[key] = values
		}
	}
	var forwarded url.Values
	if len(params) > 0 {
		forwarded = params
	}
	result, err := client.ListRows(r.Context(), r.PathValue("table"), forwarded)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Parity: framework/server/routes_colony_workers.py::handle_global_changes
func handleGlobalChanges(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	since := ChangesSinceParam(r.URL.Query().Get("since"))
	sinceValue := ""
	if since != nil {
		sinceValue = *since
	}
	result, err := client.ListChanges(r.Context(), sinceValue)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func handleGlobalUpdateRowRoute(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	decoded, decodeErr := decodeJSONValue(r)
	body, isObject := decoded.(map[string]any)
	if decodeErr != nil || !isObject {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	pk, pkOk := mapField(body, "pk")
	updates, updatesOk := mapField(body, "updates")
	if !pkOk || !updatesOk {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pk and updates must be objects"})
		return
	}
	result, err := client.UpdateRow(r.Context(), r.PathValue("table"), pk, updates)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Parity: framework/server/routes_colony_workers.py::handle_global_table_query
func handleGlobalTableQuery(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	table := r.PathValue("table")
	decoded, decodeErr := decodeJSONValue(r)
	body, isObject := decoded.(map[string]any)
	if decodeErr != nil || !isObject {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}

	filters := []map[string]any{}
	if raw := body["filter"]; pythonTruthy(raw) {
		list, ok := raw.([]any)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "filter must be a list"})
			return
		}
		for _, item := range list {
			if object, ok := item.(map[string]any); ok {
				filters = append(filters, object)
			} else {
				filters = append(filters, map[string]any{})
			}
		}
	}
	search := body["search"]
	orderBy, _ := body["order_by"].(string)
	orderDir := "asc"
	if text, ok := body["order_dir"].(string); ok {
		orderDir = text
	}
	limit, limitOk := pyIntDefault(body["limit"], 100)
	offset, offsetOk := pyIntDefault(body["offset"], 0)
	if !limitOk || !offsetOk {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid limit/offset"})
		return
	}
	limit = clampInt(limit, 1, 500)
	if offset < 0 {
		offset = 0
	}

	columns, primaryKey, err := globalIntrospect(r.Context(), client, table)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	allowed := globalColumnSet(columns)
	if orderBy != "" {
		if _, ok := allowed[orderBy]; !ok {
			orderBy = ""
		}
	}

	selectSQL, err := globaldb.BuildSelect(table, allowed, globaldb.SelectOptions{
		Filters:  filters,
		Search:   search,
		OrderBy:  orderBy,
		OrderDir: orderDir,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	countSQL, err := globaldb.BuildCount(table, allowed, filters, search)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	rowsResult, err := client.Query(r.Context(), selectSQL, &limit)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	countResult, err := client.Query(r.Context(), countSQL, nil)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}

	rows := asObjectList(asObject(rowsResult)["rows"])
	total := 0
	countRows := asObjectList(asObject(countResult)["rows"])
	if len(countRows) > 0 && len(countRows[0]) > 0 {
		if parsed, ok := firstValueAsInt(countRows[0]); ok {
			total = parsed
		} else {
			total = len(rows)
		}
	}
	rowsOut := make([]any, 0, len(rows))
	for _, row := range rows {
		rowsOut = append(rowsOut, row)
	}
	writeJSON(w, http.StatusOK, globalQueryResult{
		Table:      table,
		Columns:    columns,
		PrimaryKey: primaryKey,
		Rows:       rowsOut,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
	})
}

func firstValueAsInt(row map[string]any) (int, bool) {
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return pyIntValue(row[keys[0]])
}

// Parity: framework/server/routes_colony_workers.py::handle_global_table_group_counts
func handleGlobalTableGroupCounts(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	table := r.PathValue("table")
	decoded, decodeErr := decodeJSONValue(r)
	body, isObject := decoded.(map[string]any)
	if decodeErr != nil || !isObject {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}

	groupBy := body["group_by"]
	if !pythonTruthy(groupBy) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "group_by is required"})
		return
	}
	groupByText, _ := groupBy.(string)
	filters := []map[string]any{}
	if raw := body["filter"]; pythonTruthy(raw) {
		list, ok := raw.([]any)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "filter must be a list"})
			return
		}
		for _, item := range list {
			if object, ok := item.(map[string]any); ok {
				filters = append(filters, object)
			} else {
				filters = append(filters, map[string]any{})
			}
		}
	}
	search := body["search"]

	columns, _, err := globalIntrospect(r.Context(), client, table)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	allowed := globalColumnSet(columns)

	limit := groupCountsCap
	sqlText, err := globaldb.BuildGroupCounts(table, allowed, globaldb.GroupCountsOptions{
		GroupBy: groupByText,
		Filters: filters,
		Search:  search,
		Limit:   &limit,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	result, err := client.Query(r.Context(), sqlText, &limit)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}

	groups := []globalGroupCount{}
	for _, row := range asObjectList(asObject(result)["rows"]) {
		count, ok := pyIntValue(row["count"])
		if !ok {
			count = 0
		}
		groups = append(groups, globalGroupCount{Value: row["value"], Count: count})
	}
	writeJSON(w, http.StatusOK, globalGroupCounts{Table: table, GroupBy: groupBy, Groups: groups})
}

// Parity: framework/server/routes_colony_workers.py::handle_global_insert_row
func handleGlobalInsertRow(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	table := r.PathValue("table")
	decoded, decodeErr := decodeJSONValue(r)
	body, isObject := decoded.(map[string]any)
	if decodeErr != nil || !isObject {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	row, rowOk := body["row"].(map[string]any)
	if !rowOk {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "row must be an object"})
		return
	}

	columns, primaryKey, err := globalIntrospect(r.Context(), client, table)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	allowed := globalColumnSet(columns)
	bad := []string{}
	for key := range row {
		if _, ok := allowed[key]; !ok {
			bad = append(bad, key)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown columns: " + pythonListRepr(bad)})
		return
	}

	plan := PlanInsertRow(table, row, globalTableColumns(columns), primaryKey, nil)
	if plan.Status != http.StatusOK {
		writeJSON(w, plan.Status, plan.Body)
		return
	}
	result, err := client.Upsert(r.Context(), table, plan.Upsert.Row, "", plan.Upsert.Mode)
	status, response := MapInsertResult(plan, asObject(result), err)
	writeJSON(w, status, response)
}

// Parity: framework/server/routes_colony_workers.py::handle_global_delete_row
func handleGlobalDeleteRow(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	table := r.PathValue("table")
	decoded, decodeErr := decodeJSONValue(r)
	body, isObject := decoded.(map[string]any)
	if decodeErr != nil || !isObject {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	pkValues, pkOk := body["pk"].(map[string]any)
	if !pkOk || len(pkValues) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pk must be a non-empty object"})
		return
	}

	_, primaryKey, err := globalIntrospect(r.Context(), client, table)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	if len(primaryKey) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "table " + pythonStringRepr(table) + " has no primary key"})
		return
	}
	missing := []string{}
	for _, column := range primaryKey {
		if _, ok := pkValues[column]; !ok {
			missing = append(missing, column)
		}
	}
	if len(missing) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing primary key columns: " + pythonListRepr(missing)})
		return
	}

	deleteSQL, err := globaldb.BuildDelete(table, pkValues, primaryKey)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	result, err := client.SQL(r.Context(), deleteSQL, nil)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	var deleted any = 0
	if object := asObject(result); object != nil {
		if value, ok := object["rowCount"]; ok {
			deleted = value
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// Parity: framework/server/routes_colony_workers.py::handle_global_foreign_keys
func handleGlobalForeignKeys(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	result, err := client.Query(r.Context(), foreignKeyIntrospectSQL, nil)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	fks := []globalForeignKey{}
	for _, row := range asObjectList(asObject(result)["rows"]) {
		fks = append(fks, globalForeignKey{
			Table:     row["table_name"],
			Column:    row["column_name"],
			RefTable:  row["ref_table"],
			RefColumn: row["ref_column"],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"foreign_keys": fks})
}

// Parity: framework/server/routes_colony_workers.py::handle_global_crm_status
func handleGlobalCRMStatus(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	if deps.CRMStatus == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error": "CRM is not available in this build",
			"code":  "crm_unavailable",
		})
		return
	}
	result, err := deps.CRMStatus()
	if err != nil {
		var crmErr *CRMError
		if errors.As(err, &crmErr) {
			status := http.StatusBadRequest
			switch crmErr.ExitCode {
			case crmExitNotSignedIn:
				status = http.StatusUnauthorized
			case crmExitPermission:
				status = http.StatusForbidden
			case crmExitNotFound:
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{
				"error":      crmErr.Message,
				"code":       crmErr.Code,
				"signed_out": crmErr.ExitCode == crmExitNotSignedIn,
			})
			return
		}
		writeGlobalDBFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Parity: framework/server/routes_colony_workers.py::_crm_proxy
func handleCRMProxy(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps, method, path string) {
	client, err := deps.globalDB()
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	var body any
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		var raw []byte
		if r.Body != nil {
			raw, err = readLimitedBody(r, maxJSONBodyBytes)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			decoded, decodeErr := decodeJSONBytes(raw)
			if decodeErr != nil {
				if method != http.MethodDelete {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
					return
				}
			} else {
				body = decoded
			}
		}
	}
	result, err := client.Request(r.Context(), method, path, body, nil)
	if err != nil {
		writeGlobalDBFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func quotePathSegment(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		char := value[i]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '.' || char == '-' || char == '~' {
			builder.WriteByte(char)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexDigits[char>>4])
		builder.WriteByte(hexDigits[char&0x0f])
	}
	return builder.String()
}

func pyIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			asFloat, floatErr := typed.Float64()
			if floatErr != nil {
				return 0, false
			}
			return int(asFloat), true
		}
		return int(parsed), true
	case string:
		parsed, ok := pythonIntParam(typed, 0)
		if !ok {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func pyIntDefault(value any, fallback int) (int, bool) {
	if value == nil {
		return fallback, true
	}
	return pyIntValue(value)
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// Parity: framework/server/routes_colony_workers.py::register_routes
func registerColonyGlobalRoutes(mux *http.ServeMux, deps ColonyWorkerDeps) {
	mux.HandleFunc("GET /api/global/crm/status", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalCRMStatus(w, r, deps)
	})
	mux.HandleFunc("GET /api/global/data/tables", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalListTables(w, r, deps)
	})
	mux.HandleFunc("GET /api/global/data/changes", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalChanges(w, r, deps)
	})
	mux.HandleFunc("GET /api/global/data/foreign-keys", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalForeignKeys(w, r, deps)
	})
	mux.HandleFunc("GET /api/global/data/tables/{table}/rows", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalTableRows(w, r, deps)
	})
	mux.HandleFunc("POST /api/global/data/tables/{table}/query", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalTableQuery(w, r, deps)
	})
	mux.HandleFunc("POST /api/global/data/tables/{table}/group-counts", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalTableGroupCounts(w, r, deps)
	})
	mux.HandleFunc("POST /api/global/data/tables/{table}/rows", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalInsertRow(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/global/data/tables/{table}/rows", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalUpdateRowRoute(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/global/data/tables/{table}/rows", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalDeleteRow(w, r, deps)
	})
	mux.HandleFunc("GET /api/crm/grid/schema", func(w http.ResponseWriter, r *http.Request) {
		handleCRMProxy(w, r, deps, http.MethodGet, "/v1/crm/grid/schema")
	})
	mux.HandleFunc("POST /api/crm/grid/{entity}/query", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		handleCRMProxy(w, r, deps, http.MethodPost, "/v1/crm/grid/"+entity+"/query")
	})
	mux.HandleFunc("POST /api/crm/grid/{entity}/group-counts", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		handleCRMProxy(w, r, deps, http.MethodPost, "/v1/crm/grid/"+entity+"/group-counts")
	})
	mux.HandleFunc("GET /api/crm/grid/{entity}/{id}", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		recordID := quotePathSegment(r.PathValue("id"))
		handleCRMProxy(w, r, deps, http.MethodGet, "/v1/crm/grid/"+entity+"/"+recordID)
	})
	mux.HandleFunc("POST /api/crm/grid/{entity}", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		handleCRMProxy(w, r, deps, http.MethodPost, "/v1/crm/grid/"+entity)
	})
	mux.HandleFunc("PATCH /api/crm/grid/{entity}/{id}", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		recordID := quotePathSegment(r.PathValue("id"))
		handleCRMProxy(w, r, deps, http.MethodPatch, "/v1/crm/grid/"+entity+"/"+recordID)
	})
	mux.HandleFunc("DELETE /api/crm/grid/{entity}/{id}", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		recordID := quotePathSegment(r.PathValue("id"))
		handleCRMProxy(w, r, deps, http.MethodDelete, "/v1/crm/grid/"+entity+"/"+recordID)
	})
	mux.HandleFunc("POST /api/crm/grid/{entity}/{id}/stage", func(w http.ResponseWriter, r *http.Request) {
		entity := quotePathSegment(r.PathValue("entity"))
		recordID := quotePathSegment(r.PathValue("id"))
		handleCRMProxy(w, r, deps, http.MethodPost, "/v1/crm/grid/"+entity+"/"+recordID+"/stage")
	})
	mux.HandleFunc("POST /api/crm/claim", func(w http.ResponseWriter, r *http.Request) {
		handleCRMProxy(w, r, deps, http.MethodPost, "/v1/crm/claim")
	})
	mux.HandleFunc("POST /api/crm/release", func(w http.ResponseWriter, r *http.Request) {
		handleCRMProxy(w, r, deps, http.MethodPost, "/v1/crm/release")
	})
}
