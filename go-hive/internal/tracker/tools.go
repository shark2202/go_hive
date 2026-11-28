package tracker

// Parity: framework/tools/tracker_tools.py::build_tracker_tools
// Parity: framework/tools/tracker_tools.py::register_tracker_tools

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

type Binding struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	TrackerDB string `json:"tracker_db"`
}

type ExecutionContext struct {
	Binding       *Binding
	TrackerDBPath string
	ColonyID      string
	ColoniesRoot  string
}

type Resolver func() (*Binding, error)

func BindingFromTrackerDBPath(rawPath, colonyID string) (*Binding, error) {
	if rawPath == "" {
		return nil, nil
	}
	dbPath := filepath.Clean(rawPath)
	var colonyDir string
	if filepath.Base(dbPath) == "tracker.db" {
		switch filepath.Base(filepath.Dir(dbPath)) {
		case "data", "tracker":
			colonyDir = filepath.Dir(filepath.Dir(dbPath))
		default:
			colonyDir = filepath.Dir(dbPath)
		}
	} else {
		colonyDir = filepath.Dir(dbPath)
	}
	trackerDB, err := EnsureTrackerDB(colonyDir)
	if err != nil {
		return nil, err
	}
	name := colonyID
	if name == "" {
		name = filepath.Base(colonyDir)
	}
	return &Binding{Name: name, Dir: colonyDir, TrackerDB: trackerDB}, nil
}

func BindingFromColonyID(coloniesRoot, colonyID string) (*Binding, error) {
	if colonyID == "" {
		return nil, nil
	}
	colonyDir := filepath.Join(coloniesRoot, colonyID)
	info, err := os.Stat(colonyDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	trackerDB, err := EnsureTrackerDB(colonyDir)
	if err != nil {
		return nil, err
	}
	return &Binding{Name: colonyID, Dir: colonyDir, TrackerDB: trackerDB}, nil
}

func ResolveBinding(ctx ExecutionContext) (*Binding, error) {
	if ctx.Binding != nil {
		return ctx.Binding, nil
	}
	if b, err := BindingFromTrackerDBPath(ctx.TrackerDBPath, ctx.ColonyID); err != nil || b != nil {
		return b, err
	}
	return BindingFromColonyID(ctx.ColoniesRoot, ctx.ColonyID)
}

func failure(msg string) map[string]any {
	return map[string]any{"success": false, "error": msg}
}

func successWith(result map[string]any) map[string]any {
	out := map[string]any{"success": true}
	for k, v := range result {
		out[k] = v
	}
	return out
}

func pyRepr(items []string) string {
	parts := make([]string, len(items))
	for i, s := range items {
		escaped := strings.ReplaceAll(s, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `'`, `\'`)
		parts[i] = "'" + escaped + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func pyJSONArray(items []string) string {
	parts := make([]string, len(items))
	for i, s := range items {
		b, _ := json.Marshal(s)
		parts[i] = string(b)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// ScopeOf returns the normalized tracker scope for one tool call. Python's
// _scope_of treats a missing/blank scope as "colony" and lowercases the rest;
// callers outside this package (the CLI session tool table) use it to decide
// whether a colony binding is required before dispatch.
func ScopeOf(inputs map[string]any) string {
	s, _ := asString(inputs["scope"])
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "colony"
	}
	return s
}

func rowCapOf(inputs map[string]any) int {
	switch v := inputs["row_cap"].(type) {
	case float64:
		if v <= 0 {
			return 1000
		}
		return int(v)
	case int:
		if v <= 0 {
			return 1000
		}
		return v
	case int64:
		if v <= 0 {
			return 1000
		}
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil || n <= 0 {
			return 1000
		}
		return int(n)
	}
	return 1000
}

func TrackerSQL(b *Binding, sqlText string, rowCap int) map[string]any {
	if b == nil {
		return failure(ErrNoBinding.Error())
	}
	if strings.TrimSpace(sqlText) == "" {
		return failure("tracker_sql: 'sql' is required.")
	}
	if _, err := EnsureTrackerDB(b.Dir); err != nil {
		return failure("tracker_sql: " + err.Error())
	}
	result, err := ExecuteSQL(b.TrackerDB, sqlText, rowCap)
	if err != nil {
		var denied *DenylistError
		if errors.As(err, &denied) {
			return failure("tracker_sql denied: " + denied.Error())
		}
		return failure("tracker_sql sqlite error: " + err.Error())
	}
	return successWith(result)
}

func RegisterWritable(b *Binding, table string, writeColumns, keyColumns []string) map[string]any {
	if b == nil {
		return failure(ErrNoBinding.Error())
	}
	table = strings.TrimSpace(table)
	if table == "" {
		return failure("table is required")
	}
	if strings.HasPrefix(table, ProtectedPrefix) {
		return failure(fmt.Sprintf(
			"table '%s' is in the protected '%s*' namespace and cannot be registered for worker writes.",
			table, ProtectedPrefix,
		))
	}
	if len(keyColumns) == 0 {
		return failure(
			"key_columns is required and must be non-empty. " +
				"Without keys, workers can't upsert idempotently — " +
				"re-dispatched workers would create duplicate rows. " +
				"Add a PRIMARY KEY or UNIQUE INDEX on the table and " +
				"pass those column(s) as key_columns.",
		)
	}
	mode := "upsert"

	if _, err := EnsureTrackerDB(b.Dir); err != nil {
		return failure("tracker_register_writable: " + err.Error())
	}
	db, err := openDB(b.TrackerDB)
	if err != nil {
		return failure("tracker_register_writable sqlite error: " + err.Error())
	}
	defer db.Close()

	if !tableExists(db, table) {
		return failure(fmt.Sprintf(
			"table '%s' does not exist in tracker.db. CREATE the table via tracker_sql first.", table,
		))
	}
	actualCols, err := tableColumns(db, table)
	if err != nil {
		return failure("tracker_register_writable sqlite error: " + err.Error())
	}
	actualSet := make(map[string]bool, len(actualCols))
	for _, c := range actualCols {
		actualSet[c] = true
	}
	var missingWrite []string
	for _, c := range writeColumns {
		if !actualSet[c] {
			missingWrite = append(missingWrite, c)
		}
	}
	if len(missingWrite) > 0 {
		return failure(fmt.Sprintf(
			"write_columns not found on '%s': %s. Existing: %s",
			table, pyRepr(missingWrite), pyRepr(actualCols),
		))
	}
	var missingKey []string
	for _, c := range keyColumns {
		if !actualSet[c] {
			missingKey = append(missingKey, c)
		}
	}
	if len(missingKey) > 0 {
		return failure(fmt.Sprintf(
			"key_columns not found on '%s': %s", table, pyRepr(missingKey),
		))
	}
	indexed, err := keyColumnsUniquelyIndexed(db, table, keyColumns)
	if err != nil {
		return failure("tracker_register_writable sqlite error: " + err.Error())
	}
	if !indexed {
		return failure(fmt.Sprintf(
			"key_columns %s are not covered by a UNIQUE index on '%s'. "+
				"Add PRIMARY KEY or CREATE UNIQUE INDEX before registering for upsert.",
			pyRepr(keyColumns), table,
		))
	}

	if _, err := db.Exec(`
		INSERT INTO _tracker_registry
			(table_name, write_columns, key_columns, mode, registered_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(table_name) DO UPDATE SET
			write_columns = excluded.write_columns,
			key_columns   = excluded.key_columns,
			mode          = excluded.mode,
			registered_at = excluded.registered_at
		`, table, pyJSONArray(writeColumns), pyJSONArray(keyColumns), mode, nowISO(),
	); err != nil {
		return failure("tracker_register_writable sqlite error: " + err.Error())
	}
	if err := InstallChangeTriggers(db, table, keyColumns); err != nil {
		return failure("tracker_register_writable sqlite error: " + err.Error())
	}

	return successWith(map[string]any{
		"table":         table,
		"write_columns": stringSliceAny(writeColumns),
		"key_columns":   stringSliceAny(keyColumns),
		"mode":          mode,
		"message": fmt.Sprintf(
			"Registered '%s' for worker writes (mode=%s, key_columns=%s).",
			table, mode, pyRepr(keyColumns),
		),
	})
}

func stringSliceAny(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func tableExists(db *sql.DB, table string) bool {
	var one int
	err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&one)
	return err == nil
}

func tableColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query("PRAGMA table_info(" + quoteIdent(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var (
			cid       int64
			name      string
			ctype     string
			notNull   int64
			dfltValue any
			pk        int64
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func keyColumnsUniquelyIndexed(db *sql.DB, table string, keys []string) (bool, error) {
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}
	rows, err := db.Query("PRAGMA index_list(" + quoteIdent(table) + ")")
	if err != nil {
		return false, err
	}
	type idx struct {
		name   string
		unique bool
	}
	var indexes []idx
	for rows.Next() {
		var (
			seq     int64
			name    string
			unique  int64
			origin  string
			partial int64
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return false, err
		}
		indexes = append(indexes, idx{name: name, unique: unique != 0})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()

	for _, index := range indexes {
		if !index.unique {
			continue
		}
		infoRows, err := db.Query("PRAGMA index_info(" + quoteIdent(index.name) + ")")
		if err != nil {
			return false, err
		}
		cols := make(map[string]bool)
		for infoRows.Next() {
			var (
				seqno int64
				cid   int64
				name  any
			)
			if err := infoRows.Scan(&seqno, &cid, &name); err != nil {
				infoRows.Close()
				return false, err
			}
			if s, ok := name.(string); ok && s != "" {
				cols[s] = true
			}
		}
		if err := infoRows.Err(); err != nil {
			infoRows.Close()
			return false, err
		}
		infoRows.Close()
		if len(cols) != len(keySet) {
			continue
		}
		match := true
		for k := range keySet {
			if !cols[k] {
				match = false
				break
			}
		}
		if match {
			return true, nil
		}
	}
	return false, nil
}

func Upsert(b *Binding, table string, row map[string]any) map[string]any {
	if b == nil {
		return failure(ErrNoBinding.Error())
	}
	table = strings.TrimSpace(table)
	if table == "" {
		return failure("table is required")
	}
	if strings.HasPrefix(table, ProtectedPrefix) {
		return failure(fmt.Sprintf(
			"refusing to write to framework-owned table '%s' (%s* is reserved).",
			table, ProtectedPrefix,
		))
	}
	if len(row) == 0 {
		return failure("row must be a non-empty object of column→value")
	}

	db, err := openDB(b.TrackerDB)
	if err != nil {
		return failure("tracker_upsert sqlite error: " + err.Error())
	}
	defer db.Close()

	var writeRaw, keyRaw, mode string
	err = db.QueryRow(
		"SELECT write_columns, key_columns, mode FROM _tracker_registry WHERE table_name = ?",
		table,
	).Scan(&writeRaw, &keyRaw, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return failure(fmt.Sprintf(
			"table '%s' is not registered for worker writes. The queen must call tracker_register_writable first.",
			table,
		))
	}
	if err != nil {
		return failure("tracker_upsert sqlite error: " + err.Error())
	}
	writeColumns, ok1 := decodeStringList(writeRaw)
	keyColumns, ok2 := decodeStringList(keyRaw)
	if !ok1 || !ok2 {
		return failure("registry row is corrupt; re-register the table")
	}

	allowed := make(map[string]bool, len(writeColumns)+len(keyColumns))
	for _, c := range writeColumns {
		allowed[c] = true
	}
	for _, c := range keyColumns {
		allowed[c] = true
	}
	var unknown []string
	for c := range row {
		if !allowed[c] {
			unknown = append(unknown, c)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		allowedList := make([]string, 0, len(allowed))
		for c := range allowed {
			allowedList = append(allowedList, c)
		}
		sort.Strings(allowedList)
		return failure(fmt.Sprintf(
			"columns not in write/key list: %s. Allowed: %s",
			pyRepr(unknown), pyRepr(allowedList),
		))
	}

	if mode == "upsert" {
		var missing []string
		for _, k := range keyColumns {
			if _, ok := row[k]; !ok {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			return failure(fmt.Sprintf(
				"row is missing key_columns %s required for upsert", pyRepr(missing),
			))
		}
	}

	cols := make([]string, 0, len(row))
	for c := range row {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	values := make([]any, 0, len(cols))
	for _, c := range cols {
		v, err := encodeUpsertValue(row[c])
		if err != nil {
			return failure("tracker_upsert sqlite error: " + err.Error())
		}
		values = append(values, v)
	}

	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = quoteIdent(c)
		placeholders[i] = "?"
	}
	baseSQL := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table), strings.Join(quotedCols, ", "), strings.Join(placeholders, ", "),
	)
	statement := baseSQL
	if mode == "upsert" {
		keySet := make(map[string]bool, len(keyColumns))
		for _, k := range keyColumns {
			keySet[k] = true
		}
		var updateCols []string
		for _, c := range cols {
			if !keySet[c] {
				updateCols = append(updateCols, c)
			}
		}
		conflict := make([]string, len(keyColumns))
		for i, k := range keyColumns {
			conflict[i] = quoteIdent(k)
		}
		conflictClause := strings.Join(conflict, ", ")
		if len(updateCols) > 0 {
			setParts := make([]string, len(updateCols))
			for i, c := range updateCols {
				setParts[i] = quoteIdent(c) + " = excluded." + quoteIdent(c)
			}
			statement = baseSQL + " ON CONFLICT (" + conflictClause + ") DO UPDATE SET " + strings.Join(setParts, ", ")
		} else {
			statement = baseSQL + " ON CONFLICT (" + conflictClause + ") DO NOTHING"
		}
	}

	res, err := db.Exec(statement, values...)
	if err != nil {
		return failure("tracker_upsert sqlite error: " + err.Error())
	}
	rowcount, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()

	_ = PruneChangeLog(db)

	return successWith(map[string]any{
		"table":             table,
		"mode":              mode,
		"rowcount":          rowcount,
		"last_insert_rowid": lastID,
	})
}

func decodeStringList(raw string) ([]string, bool) {
	var items []any
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func encodeUpsertValue(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if b, ok := v.(bool); ok {
		if b {
			return int64(1), nil
		}
		return int64(0), nil
	}
	kind := reflect.TypeOf(v).Kind()
	if kind == reflect.Slice || kind == reflect.Array || kind == reflect.Map {
		return pyDumps(v)
	}
	return v, nil
}

func pyDumps(v any) (string, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	compact := strings.TrimSuffix(buf.String(), "\n")
	var out strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		if inString {
			out.WriteByte(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out.WriteByte(c)
		case ',':
			out.WriteString(", ")
		case ':':
			out.WriteString(": ")
		default:
			out.WriteByte(c)
		}
	}
	return out.String(), nil
}

var readKeywords = map[string]bool{"SELECT": true, "EXPLAIN": true}

func Query(b *Binding, sqlText string, rowCap int) map[string]any {
	if b == nil {
		return failure(ErrNoBinding.Error())
	}
	if strings.TrimSpace(sqlText) == "" {
		return failure("tracker_query: 'sql' is required.")
	}
	statements := splitStatements(sqlText)
	if len(statements) == 0 {
		return failure("tracker_query: no statements")
	}
	if len(statements) > 1 {
		return failure(fmt.Sprintf(
			"tracker_query accepts ONE statement per call (got %d). Use tracker_sql for scripts.",
			len(statements),
		))
	}
	verb, _ := effectiveStatement(statements[0])
	if !readKeywords[verb] {
		return failure(fmt.Sprintf(
			"tracker_query is SELECT-only; rejected statement verb '%s'. For writes, use tracker_upsert.",
			verb,
		))
	}
	result, err := ExecuteSQL(b.TrackerDB, sqlText, rowCap)
	if err != nil {
		var denied *DenylistError
		if errors.As(err, &denied) {
			return failure("tracker_query denied: " + denied.Error())
		}
		return failure("tracker_query sqlite error: " + err.Error())
	}
	return successWith(result)
}

type Tool struct {
	Name            string
	Description     string
	Parameters      map[string]any
	ConcurrencySafe bool
	QueenOnly       bool
	Execute         func(inputs map[string]any) map[string]any
}

var QueenOnlyTrackerTools = map[string]bool{
	"tracker_sql":               true,
	"tracker_register_writable": true,
}

type Registry struct {
	tools   map[string]Tool
	resolve Resolver
	// role is the registration role ("queen" or "worker") so a per-call
	// rebind keeps the same tool surface. Empty means no role filter.
	role string
}

func NewRegistry(resolve Resolver) *Registry {
	if resolve == nil {
		resolve = func() (*Binding, error) { return nil, nil }
	}
	return &Registry{tools: map[string]Tool{}, resolve: resolve}
}

// StaticResolver always yields b. It is how a binding resolved per call (the
// queen's session colony, a worker's input_data) is handed to the tracker
// tools without rebuilding their argument plumbing by hand.
func StaticResolver(b *Binding) Resolver {
	return func() (*Binding, error) { return b, nil }
}

func (r *Registry) Register(tool Tool) {
	r.tools[tool.Name] = tool
}

func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// Execute runs one registered tool. ok is false when name is not in the
// table, so a caller can fall through to its own executor instead of
// mistaking an unknown tool for a failed call.
func (r *Registry) Execute(name string, inputs map[string]any) (map[string]any, bool) {
	if r == nil {
		return nil, false
	}
	tool, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return tool.Execute(inputs), true
}

// WithResolver rebuilds the same role-filtered table against another binding
// resolver. The tool closures capture the resolver, so a per-call binding
// (queen session colony, worker input_data) requires a rebuilt table rather
// than a mutated one.
func (r *Registry) WithResolver(resolve Resolver) *Registry {
	out := NewRegistry(resolve)
	if r == nil {
		return out
	}
	out.role = r.role
	registerTrackerTools(out)
	return out
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Resolve() (*Binding, error) { return r.resolve() }

func scopeParam() map[string]any {
	return map[string]any{
		"type": "string",
		"enum": []any{"colony", "global"},
		"description": "Which tracker to target. 'colony' (default) is THIS colony's private " +
			"tracker.db (local — your work queue). 'global' targets the team's shared " +
			"cloud database (requires a signed-in cloud session; other colonies read/" +
			"write it too, so scope queries to your own rows and expand the schema only " +
			"additively).",
	}
}

const trackerSQLDescription = "Run raw SQL against this colony's tracker.db. The tracker is your " +
	"queen-owned domain model — design the table(s) that describe progress " +
	"toward the goal, then validate worker output by querying the table.\n\n" +
	"Typical workflow after the colony is created:\n" +
	"  1. CREATE TABLE <name> (...)  -- design columns that describe one " +
	"unit of progress (one row = one company, one paper, one row, etc.).\n" +
	"  2. INSERT INTO <name> (key_col, ...) VALUES (...)  -- seed primary " +
	"keys you already know so workers fan out across disjoint rows.\n" +
	"  3. tracker_register_writable(...)  -- whitelist which columns " +
	"workers may write to; without this they cannot upsert.\n" +
	"  4. run_worker(...)  -- delegate row-fill work.\n" +
	"  5. SELECT ... WHERE <col> IS NULL  -- find gaps; re-dispatch if needed.\n\n" +
	"Returns:\n" +
	"  - SELECT: {kind: 'rows', columns: [...], rows: [[...], ...], " +
	"rowcount, truncated}. Rows past row_cap are dropped (truncated=true); " +
	"paginate with LIMIT/OFFSET.\n" +
	"  - DDL/DML: {kind: 'exec', rowcount, last_insert_rowid}.\n" +
	"  - Multi-statement script (statements separated by ';'): " +
	"{kind: 'script', results: [<per-stmt result>, ...]}.\n\n" +
	"Forbidden: ATTACH, DETACH, PRAGMA, VACUUM, REINDEX, load_extension(). " +
	"Tables starting with '_' are framework-owned (DDL/DML rejected; " +
	"SELECT is fine). Cap of 20 statements per call."

const registerDescription = "Whitelist a tracker table for worker writes. Workers cannot call " +
	"tracker_upsert against an unregistered table — without registration " +
	"the table is invisible to them. Call this after CREATE TABLE.\n\n" +
	"Args:\n" +
	"  table         — the table name in tracker.db (must already exist).\n" +
	"  write_columns — list of columns workers may set on each row.\n" +
	"                  Columns NOT in this list (e.g. an internal " +
	"                  'reviewed_at') stay queen-only.\n" +
	"  key_columns   — REQUIRED. List of columns that uniquely identify " +
	"                  a row (used for ON CONFLICT). Without keys, " +
	"                  re-dispatched workers create duplicate rows " +
	"                  instead of updating in place. Add a PRIMARY KEY " +
	"                  or UNIQUE INDEX on the table before registering.\n\n" +
	"Validation: the table must exist in tracker.db, every named column " +
	"must exist, key_columns must be non-empty, and the key_columns " +
	"must be covered by a UNIQUE index (a PRIMARY KEY counts). " +
	"Re-registering a table is fine — the new spec replaces the old one."

const upsertDescription = "Write a row to a tracker table the queen has registered for worker " +
	"writes. This is your channel for reporting findings — prefer it over " +
	"embedding structured data in your final-message text, because the " +
	"queen reads tracker rows directly and can validate them.\n\n" +
	"Args:\n" +
	"  table — the registered tracker table.\n" +
	"  row   — dict of column→value. Must include all key_columns the " +
	"          queen registered. Columns not in the registered " +
	"          write_columns (or key_columns) are rejected.\n\n" +
	"Behavior: INSERT ... ON CONFLICT(<keys>) DO UPDATE — call again " +
	"with the same key to update the row in place.\n\n" +
	"Refuses unregistered tables and any '_*' framework table."

const queryDescription = "Read rows from the colony's tracker.db. SELECT-only — DDL, INSERT, " +
	"UPDATE, and DELETE are rejected (use tracker_upsert for writes).\n\n" +
	"Workers: use this to read your assignment context (e.g. \"which " +
	"rows still need work\", \"what columns are expected\") instead of " +
	"asking the queen. The queen has already designed the table; you " +
	"can introspect via SELECT against ``sqlite_master`` or the table " +
	"directly.\n\n" +
	"Queen: also fine to use for read-only checks; tracker_sql covers " +
	"the same ground with broader powers.\n\n" +
	"Returns ``{kind: 'rows', columns: [...], rows: [[...], ...], " +
	"rowcount, truncated}``. Rows past row_cap are dropped (truncated=" +
	"true); paginate with LIMIT/OFFSET.\n\n" +
	"Allowed: SELECT, WITH, EXPLAIN. Forbidden: ATTACH, DETACH, PRAGMA, " +
	"VACUUM, REINDEX, load_extension(), and ALL writes/DDL."

const workerGlobalScopeError = "scope='global' is queen-only — workers write only the " +
	"colony tracker. Record your result here (scope='colony') " +
	"and report it to the queen; she owns the shared global DB."

func globalRegisterInfo() map[string]any {
	globaldb.NoteGlobalUsed()
	return successWith(map[string]any{
		"message": "The global DB uses free DDL within your team's schema — no " +
			"registration is needed. Workers may tracker_upsert(scope='global') " +
			"into any existing table; use tracker_sql(scope='global') to " +
			"CREATE/ALTER tables.",
	})
}

func BuildTrackerTools(resolve Resolver) []Tool {
	return BuildTrackerToolsWithGlobalDB(resolve, EnvGlobalDB)
}

// BuildTrackerToolsWithGlobalDB is BuildTrackerTools with an injectable cloud
// client provider, so tests can point scope='global' at an httptest backend.
func BuildTrackerToolsWithGlobalDB(resolve Resolver, globalDB GlobalDBProvider) []Tool {
	if resolve == nil {
		resolve = func() (*Binding, error) { return nil, nil }
	}
	if globalDB == nil {
		globalDB = EnvGlobalDB
	}
	colonyBound := func(core func(*Binding, map[string]any) map[string]any) func(map[string]any) map[string]any {
		return func(inputs map[string]any) map[string]any {
			b, err := resolve()
			if err != nil {
				return failure(err.Error())
			}
			return core(b, inputs)
		}
	}
	// dispatch mirrors Python's executor order: the scope check comes before
	// _require_binding, so a signed-in queen outside a colony still reaches the
	// global DB. A colony binding is only ambient context for source_colony on
	// upserts — its absence (or a resolver failure) must not fail a global call.
	dispatch := func(
		global func(map[string]any) map[string]any,
		colony func(*Binding, map[string]any) map[string]any,
	) func(map[string]any) map[string]any {
		colonyCall := colonyBound(colony)
		return func(inputs map[string]any) map[string]any {
			if ScopeOf(inputs) == "global" {
				return global(inputs)
			}
			return colonyCall(inputs)
		}
	}

	trackerSQL := dispatch(
		func(inputs map[string]any) map[string]any {
			return globalSQLTool(globalDB, inputs, false)
		},
		func(b *Binding, inputs map[string]any) map[string]any {
			sqlText, _ := asString(inputs["sql"])
			return TrackerSQL(b, sqlText, rowCapOf(inputs))
		},
	)

	register := dispatch(
		func(map[string]any) map[string]any {
			return globalRegisterInfo()
		},
		func(b *Binding, inputs map[string]any) map[string]any {
			table, _ := asString(inputs["table"])
			writeColumns, ok := stringListInput(inputs["write_columns"])
			if !ok {
				return failure("write_columns must be a non-empty list of strings")
			}
			keyColumns, ok := stringListInput(inputs["key_columns"])
			if !ok {
				return failure("key_columns must be a list of strings")
			}
			return RegisterWritable(b, table, writeColumns, keyColumns)
		},
	)

	upsert := dispatch(
		func(inputs map[string]any) map[string]any {
			// Python's _ambient_colony_name() reads whatever binding the
			// execution context carries; no binding just means no source colony.
			b, err := resolve()
			if err != nil {
				b = nil
			}
			return globalUpsertTool(globalDB, b, inputs)
		},
		func(b *Binding, inputs map[string]any) map[string]any {
			table, _ := asString(inputs["table"])
			row, _ := inputs["row"].(map[string]any)
			return Upsert(b, table, row)
		},
	)

	query := dispatch(
		func(inputs map[string]any) map[string]any {
			return globalSQLTool(globalDB, inputs, true)
		},
		func(b *Binding, inputs map[string]any) map[string]any {
			sqlText, _ := asString(inputs["sql"])
			return Query(b, sqlText, rowCapOf(inputs))
		},
	)

	return []Tool{
		{
			Name:        "tracker_sql",
			Description: trackerSQLDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sql": map[string]any{
						"type": "string",
						"description": "Raw SQL. May be a single statement or a script of " +
							"statements separated by ';'. CTEs, transactions " +
							"(BEGIN/COMMIT), and views are allowed.",
					},
					"row_cap": map[string]any{
						"type":        "integer",
						"description": "Max rows returned per SELECT (default 1000).",
						"minimum":     1,
						"maximum":     10000,
					},
					"scope": scopeParam(),
				},
				"required": []any{"sql"},
			},
			ConcurrencySafe: false,
			QueenOnly:       true,
			Execute:         trackerSQL,
		},
		{
			Name:        "tracker_register_writable",
			Description: registerDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"table": map[string]any{
						"type":        "string",
						"description": "Tracker table name (must already exist).",
					},
					"write_columns": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"description": "Columns workers may write. Other columns stay queen-only.",
					},
					"key_columns": map[string]any{
						"type":     "array",
						"items":    map[string]any{"type": "string"},
						"minItems": 1,
						"description": "Required. Columns that uniquely identify a row (for " +
							"ON CONFLICT). Must be covered by a PRIMARY KEY or " +
							"UNIQUE index so re-dispatched workers update in " +
							"place instead of creating duplicate rows.",
					},
					"scope": scopeParam(),
				},
				"required": []any{"table", "write_columns", "key_columns"},
			},
			ConcurrencySafe: false,
			QueenOnly:       true,
			Execute:         register,
		},
		{
			Name:        "tracker_upsert",
			Description: upsertDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"table": map[string]any{"type": "string"},
					"row": map[string]any{
						"type": "object",
						"description": "Column→value pairs. Values may be string, number, " +
							"boolean, or null. Lists/objects are JSON-encoded before write.",
					},
					"scope": scopeParam(),
				},
				"required": []any{"table", "row"},
			},
			ConcurrencySafe: false,
			QueenOnly:       false,
			Execute:         upsert,
		},
		{
			Name:        "tracker_query",
			Description: queryDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"sql": map[string]any{
						"type":        "string",
						"description": "SELECT (or WITH … SELECT) statement. Single statement; scripts not allowed.",
					},
					"row_cap": map[string]any{
						"type":        "integer",
						"description": "Max rows returned (default 1000).",
						"minimum":     1,
						"maximum":     10000,
					},
					"scope": scopeParam(),
				},
				"required": []any{"sql"},
			},
			ConcurrencySafe: true,
			QueenOnly:       false,
			Execute:         query,
		},
	}
}

func stringListInput(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	items, ok := v.([]any)
	if !ok {
		if ss, ok := v.([]string); ok {
			return ss, true
		}
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func RegisterTrackerTools(reg *Registry, role string) error {
	if role != "queen" && role != "worker" {
		return fmt.Errorf("role must be 'queen' or 'worker', got %s", strconv.Quote(role))
	}
	reg.role = role
	registerTrackerTools(reg)
	return nil
}

// restrictTrackerScopeToColony removes 'global' from a worker-facing tool's
// scope enum. Workers execute colony-only (scope='global' is rejected at the
// executor), so leaving the value in the schema advertises a target they can
// never reach — the P0-2 "usable but always fails" trap. The queen keeps both
// scopes now that scope='global' is wired to the cloud client.
func restrictTrackerScopeToColony(tool *Tool) {
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		return
	}
	scope, ok := properties["scope"].(map[string]any)
	if !ok {
		return
	}
	scope["enum"] = []any{"colony"}
}

// registerTrackerTools fills reg according to its role. It is the shared body
// of RegisterTrackerTools and Registry.WithResolver so a rebind cannot drift
// from the original role filter.
func registerTrackerTools(reg *Registry) {
	for _, tool := range BuildTrackerTools(reg.Resolve) {
		if reg.role == "worker" {
			if QueenOnlyTrackerTools[tool.Name] {
				continue
			}
			restrictTrackerScopeToColony(&tool)
			original := tool.Execute
			tool.Execute = func(inputs map[string]any) map[string]any {
				if ScopeOf(inputs) == "global" {
					return failure(workerGlobalScopeError)
				}
				return original(inputs)
			}
		}
		reg.Register(tool)
	}
}
