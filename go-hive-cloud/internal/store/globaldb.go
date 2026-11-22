package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RowPage is the GET /v1/global-db/tables/{table}/rows response. Its shape
// mirrors the client's TableRowsResponse (frontend/src/api/colonyData.ts) and
// is also what the runtime's _global_introspect reads (columns + primary_key).
type RowPage struct {
	Table      string           `json:"table"`
	Columns    []ColumnInfo     `json:"columns"`
	PrimaryKey []string         `json:"primary_key"`
	Rows       []map[string]any `json:"rows"`
	Total      int64            `json:"total"`
	Limit      int              `json:"limit"`
	Offset     int              `json:"offset"`
}

// ListRows returns one page of rows. Only limit/offset/order_by/order_dir are
// honoured (the set the runtime forwards).
func (t *Team) ListRows(ctx context.Context, table string, params url.Values) (*RowPage, error) {
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return nil, err
	}
	limit := 100
	offset := 0
	if raw := params.Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	if raw := params.Get("offset"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			offset = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	byName := map[string]bool{}
	for _, c := range columns {
		byName[c.Name] = true
	}
	orderBy := params.Get("order_by")
	if !byName[orderBy] {
		orderBy = ""
	}
	direction := "ASC"
	if strings.EqualFold(params.Get("order_dir"), "desc") {
		direction = "DESC"
	}

	query := "SELECT * FROM " + quoteIdent(table)
	if orderBy != "" {
		query += " ORDER BY " + quoteIdent(orderBy) + " " + direction
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)

	rows, err := t.QueryRaw(ctx, query)
	if err != nil {
		return nil, err
	}
	countRows, err := t.QueryRaw(ctx, "SELECT COUNT(*) AS n FROM "+quoteIdent(table))
	if err != nil {
		return nil, err
	}
	var total int64
	if len(countRows) > 0 {
		total = asInt(countRows[0]["n"])
	}
	return &RowPage{
		Table:      table,
		Columns:    columns,
		PrimaryKey: PrimaryKey(columns),
		Rows:       rows,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
	}, nil
}

// UpsertResult is the /v1/global-db/upsert (and /import) response. The runtime
// reads `inserted` on the mode=insert path (routes_colony_workers.py
// handle_global_insert_row).
type UpsertResult struct {
	Table    string         `json:"table"`
	Inserted int64          `json:"inserted"`
	Updated  int64          `json:"updated"`
	Total    int64          `json:"total,omitempty"`
	PK       map[string]any `json:"pk"`
}

// Upsert inserts or merges one row. mode "insert" means true INSERT semantics:
// a primary-key collision returns ErrConflict (mapped to HTTP 409 with
// {"error":"conflict","pk":...}) instead of overwriting.
func (t *Team) Upsert(ctx context.Context, table string, row map[string]any, mode string) (*UpsertResult, error) {
	if err := ValidateTableName(table); err != nil {
		return nil, err
	}
	if len(row) == 0 {
		return nil, fmt.Errorf("%w: row must be a non-empty object", ErrBadData)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	columns, err := t.ensureTableForRow(ctx, table, row)
	if err != nil {
		return nil, err
	}
	pkCols := PrimaryKey(columns)
	pkValue := pickPK(row, pkCols)

	existed := false
	if len(pkCols) > 0 && len(pkValue) == len(pkCols) {
		existed, err = t.rowExists(ctx, table, pkValue)
		if err != nil {
			return nil, err
		}
	}
	if mode == "insert" && existed {
		return nil, &ConflictError{PK: pkValue}
	}
	if err := t.insertRow(ctx, table, row, columns, pkCols, mode); err != nil {
		if isUniqueViolation(err) {
			return nil, &ConflictError{PK: pkValue}
		}
		return nil, sqlError(err)
	}
	result := &UpsertResult{Table: table, PK: pkValue}
	if existed {
		result.Updated = 1
	} else {
		result.Inserted = 1
	}
	return result, nil
}

// ConflictError carries the attempted primary key for the 409 body.
type ConflictError struct {
	PK map[string]any
}

func (e *ConflictError) Error() string { return "conflict: row already exists" }
func (e *ConflictError) Unwrap() error { return ErrConflict }

// pickPK returns the pk columns present in row.
func pickPK(row map[string]any, pkCols []string) map[string]any {
	out := map[string]any{}
	for _, col := range pkCols {
		if v, ok := row[col]; ok {
			out[col] = v
		}
	}
	return out
}

func (t *Team) rowExists(ctx context.Context, table string, pk map[string]any) (bool, error) {
	where, args := pkWhere(pk)
	rows, err := t.QueryRaw(ctx, "SELECT 1 AS one FROM "+quoteIdent(table)+" WHERE "+where+" LIMIT 1", args...)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// ImportRows inserts a batch, returning how many rows were inserted.
func (t *Team) ImportRows(ctx context.Context, table string, rows []map[string]any) (*UpsertResult, error) {
	if err := ValidateTableName(table); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: rows must be a non-empty array", ErrBadData)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	columns, err := t.ensureTableForRow(ctx, table, rows[0])
	if err != nil {
		return nil, err
	}
	pkCols := PrimaryKey(columns)
	inserted := int64(0)
	var lastPK map[string]any
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		if err := t.insertRow(ctx, table, row, columns, pkCols, "merge"); err != nil {
			return nil, sqlError(err)
		}
		inserted++
		lastPK = pickPK(row, pkCols)
	}
	if lastPK == nil {
		lastPK = map[string]any{}
	}
	return &UpsertResult{Table: table, Inserted: inserted, Total: int64(len(rows)), PK: lastPK}, nil
}

// UpdateRow applies updates to the row identified by pk and returns the number
// of rows changed.
func (t *Team) UpdateRow(ctx context.Context, table string, pk, updates map[string]any) (int64, error) {
	if err := ValidateTableName(table); err != nil {
		return 0, err
	}
	if len(pk) == 0 {
		return 0, fmt.Errorf("%w: pk must be a non-empty object", ErrBadData)
	}
	if len(updates) == 0 {
		return 0, fmt.Errorf("%w: updates must be a non-empty object", ErrBadData)
	}
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return 0, err
	}
	byName := map[string]bool{}
	for _, c := range columns {
		byName[c.Name] = true
	}
	// Deterministic SET order.
	setCols := make([]string, 0, len(updates))
	for name := range updates {
		setCols = append(setCols, name)
	}
	sort.Strings(setCols)
	sets := make([]string, 0, len(setCols))
	args := make([]any, 0, len(setCols)+len(pk))
	for _, name := range setCols {
		if !byName[name] {
			return 0, fmt.Errorf("%w: unknown column: %s", ErrBadData, name)
		}
		value, err := BindValue(updates[name])
		if err != nil {
			return 0, err
		}
		sets = append(sets, quoteIdent(name)+" = ?")
		args = append(args, value)
	}
	whereParts := make([]string, 0, len(pk))
	pkCols := make([]string, 0, len(pk))
	for name := range pk {
		pkCols = append(pkCols, name)
	}
	sort.Strings(pkCols)
	for _, name := range pkCols {
		if !byName[name] {
			return 0, fmt.Errorf("%w: unknown primary key column: %s", ErrBadData, name)
		}
		value, err := BindValue(pk[name])
		if err != nil {
			return 0, err
		}
		whereParts = append(whereParts, quoteIdent(name)+" = ?")
		args = append(args, value)
	}
	query := "UPDATE " + quoteIdent(table) + " SET " + strings.Join(sets, ", ") +
		" WHERE " + strings.Join(whereParts, " AND ")
	res, err := t.rw.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, sqlError(err)
	}
	return res.RowsAffected()
}

// ensureTableForRow creates table from row's columns when absent, then returns
// the column metadata.
func (t *Team) ensureTableForRow(ctx context.Context, table string, row map[string]any) ([]ColumnInfo, error) {
	exists, err := t.tableExists(ctx, table)
	if err != nil {
		return nil, err
	}
	if exists {
		return t.TableInfo(ctx, table)
	}
	names := make([]string, 0, len(row))
	for name := range row {
		if err := ValidateTableName(name); err != nil {
			return nil, err
		}
		if strings.HasPrefix(name, "_") {
			return nil, fmt.Errorf("%w: invalid column name: %q", ErrBadData, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	// Put the primary key first for readability.
	pkIndex := -1
	for i, name := range names {
		if name == "id" {
			pkIndex = i
			break
		}
	}
	if pkIndex > 0 {
		names = append([]string{"id"}, append(names[:pkIndex:pkIndex], names[pkIndex+1:]...)...)
	}
	defs := make([]string, 0, len(names))
	for _, name := range names {
		colType := inferSQLType(row[name])
		if name == "id" {
			if colType == "INTEGER" {
				defs = append(defs, quoteIdent(name)+" INTEGER PRIMARY KEY")
			} else {
				defs = append(defs, quoteIdent(name)+" TEXT PRIMARY KEY")
			}
			continue
		}
		defs = append(defs, quoteIdent(name)+" "+colType)
	}
	ddl := "CREATE TABLE IF NOT EXISTS " + quoteIdent(table) + " (" + strings.Join(defs, ", ") + ")"
	if _, err := t.rw.ExecContext(ctx, ddl); err != nil {
		return nil, sqlError(err)
	}
	return t.TableInfo(ctx, table)
}

func inferSQLType(v any) string {
	switch v.(type) {
	case bool:
		return "INTEGER"
	case json.Number:
		text := v.(json.Number).String()
		if !strings.ContainsAny(text, ".eE") {
			return "INTEGER"
		}
		return "REAL"
	case int, int64:
		return "INTEGER"
	case float64:
		return "REAL"
	default:
		return "TEXT"
	}
}

func (t *Team) insertRow(ctx context.Context, table string, row map[string]any, columns []ColumnInfo, pkCols []string, mode string) error {
	byName := map[string]bool{}
	for _, c := range columns {
		byName[c.Name] = true
	}
	names := make([]string, 0, len(row))
	for name := range row {
		if !byName[name] {
			return fmt.Errorf("%w: unknown column: %s", ErrBadData, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("%w: row has no known columns", ErrBadData)
	}
	placeholders := make([]string, 0, len(names))
	args := make([]any, 0, len(names))
	for _, name := range names {
		value, err := BindValue(row[name])
		if err != nil {
			return err
		}
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	query := "INSERT INTO " + quoteIdent(table) + " (" + joinQuoted(names) + ") VALUES (" + strings.Join(placeholders, ", ") + ")"

	if mode != "insert" && len(pkCols) > 0 && len(pickPK(row, pkCols)) == len(pkCols) {
		nonPK := make([]string, 0, len(names))
		for _, name := range names {
			if !containsString(pkCols, name) {
				nonPK = append(nonPK, name)
			}
		}
		conflict := " ON CONFLICT (" + joinQuoted(pkCols) + ") "
		if len(nonPK) == 0 {
			query += conflict + "DO NOTHING"
		} else {
			assignments := make([]string, 0, len(nonPK))
			for _, name := range nonPK {
				assignments = append(assignments, quoteIdent(name)+" = excluded."+quoteIdent(name))
			}
			query += conflict + "DO UPDATE SET " + strings.Join(assignments, ", ")
		}
	}
	_, err := t.rw.ExecContext(ctx, query, args...)
	return err
}

func joinQuoted(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, quoteIdent(name))
	}
	return strings.Join(quoted, ", ")
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") || strings.Contains(message, "constraint failed: unique")
}

// ChangedRow is one entry of the change feed.
type ChangedRow struct {
	PK map[string]any `json:"pk"`
	Op string         `json:"op"`
}

// TableChanges groups the changed rows of one table.
type TableChanges struct {
	Count int          `json:"count"`
	Rows  []ChangedRow `json:"rows"`
}

// ChangesResponse matches the client's GlobalChangesResponse: cursor as a
// string (or null when nothing has changed yet), covered tables, and a map of
// table -> changed rows.
type ChangesResponse struct {
	Cursor    *string                  `json:"cursor"`
	Covered   []string                 `json:"covered"`
	Truncated bool                     `json:"truncated"`
	Tables    map[string]*TableChanges `json:"tables"`
}

// maxChangeRows bounds one changes response.
const maxChangeRows = 500

// Changes returns the row-level change feed. since == "" initialises (cursor +
// covered tables, no rows), matching the client's documented protocol.
func (t *Team) Changes(ctx context.Context, since string) (*ChangesResponse, error) {
	covered, err := t.QueryRaw(ctx, `SELECT DISTINCT "table" AS table_name FROM _cloud_changes ORDER BY table_name`)
	if err != nil {
		return nil, err
	}
	coveredNames := make([]string, 0, len(covered))
	for _, row := range covered {
		coveredNames = append(coveredNames, asString(row["table_name"]))
	}
	cursor, err := t.ChangeCursor(ctx)
	if err != nil {
		return nil, err
	}
	out := &ChangesResponse{
		Covered: coveredNames,
		Tables:  map[string]*TableChanges{},
	}
	if cursor != "" {
		c := cursor
		out.Cursor = &c
	}
	if since == "" {
		return out, nil
	}
	sinceCursor, err := strconv.ParseInt(since, 10, 64)
	if err != nil || sinceCursor < 0 {
		return nil, fmt.Errorf("%w: invalid since cursor: %q", ErrBadData, since)
	}
	rows, err := t.QueryRaw(ctx, `SELECT cursor, "table" AS table_name, pk, op FROM _cloud_changes WHERE cursor > ? ORDER BY cursor LIMIT ?`,
		sinceCursor, maxChangeRows+1)
	if err != nil {
		return nil, err
	}
	if len(rows) > maxChangeRows {
		out.Truncated = true
		rows = rows[:maxChangeRows]
	}
	for _, row := range rows {
		table := asString(row["table_name"])
		group, ok := out.Tables[table]
		if !ok {
			group = &TableChanges{Rows: []ChangedRow{}}
			out.Tables[table] = group
		}
		pk := map[string]any{}
		if raw := asString(row["pk"]); raw != "" {
			_ = json.Unmarshal([]byte(raw), &pk)
		}
		group.Count++
		group.Rows = append(group.Rows, ChangedRow{PK: pk, Op: asString(row["op"])})
	}
	return out, nil
}

// pkWhere builds a WHERE clause for a pk map with sorted columns.
func pkWhere(pk map[string]any) (string, []any) {
	cols := make([]string, 0, len(pk))
	for name := range pk {
		cols = append(cols, name)
	}
	sort.Strings(cols)
	parts := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols))
	for _, name := range cols {
		value, err := BindValue(pk[name])
		if err != nil {
			value = fmt.Sprint(pk[name])
		}
		parts = append(parts, quoteIdent(name)+" = ?")
		args = append(args, value)
	}
	return strings.Join(parts, " AND "), args
}

// Now returns the store clock as RFC3339 (UTC).
func (t *Team) Now() string {
	return t.opts.Now().UTC().Format(time.RFC3339Nano)
}

// DB exposes the rw pool for tests that assert on-disk state.
func (t *Team) DB() *sql.DB { return t.rw }
