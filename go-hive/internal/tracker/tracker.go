package tracker

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 3

const ChangeLogMax = 10_000

const ProtectedPrefix = "_"

const MaxStatementsPerCall = 20

const bootstrapSchema = `
CREATE TABLE IF NOT EXISTS _tracker_registry (
    table_name      TEXT PRIMARY KEY,
    write_columns   TEXT NOT NULL,    -- JSON array of column names
    key_columns     TEXT NOT NULL,    -- JSON array; empty = append mode
    mode            TEXT NOT NULL CHECK (mode IN ('upsert', 'append')),
    registered_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_meta (
    key             TEXT PRIMARY KEY,
    value           TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS _tracker_changes (
    id              INTEGER PRIMARY KEY,  -- monotonic cursor for "since" reads
    table_name      TEXT NOT NULL,
    pk              TEXT NOT NULL,        -- JSON object of key column -> value
    op              TEXT NOT NULL CHECK (op IN ('insert', 'update')),
    changed_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS _tracker_changes_table_id
    ON _tracker_changes (table_name, id);
`

var pragmas = []string{
	"PRAGMA journal_mode = WAL;",
	"PRAGMA synchronous = NORMAL;",
	"PRAGMA foreign_keys = ON;",
	"PRAGMA busy_timeout = 5000;",
}

var deniedLeadingKeywords = map[string]bool{
	"ATTACH":  true,
	"DETACH":  true,
	"PRAGMA":  true,
	"VACUUM":  true,
	"REINDEX": true,
}

var deniedFunctions = []string{"load_extension"}

var mutatingKeywords = map[string]bool{
	"CREATE":   true,
	"ALTER":    true,
	"DROP":     true,
	"INSERT":   true,
	"UPDATE":   true,
	"DELETE":   true,
	"REPLACE":  true,
	"TRUNCATE": true,
}

var withMainVerbs = map[string]bool{
	"SELECT":  true,
	"VALUES":  true,
	"INSERT":  true,
	"UPDATE":  true,
	"DELETE":  true,
	"REPLACE": true,
}

type DenylistError struct {
	Message string
}

func (e *DenylistError) Error() string { return e.Message }

func denyf(format string, args ...any) error {
	return &DenylistError{Message: fmt.Sprintf(format, args...)}
}

var ErrNoBinding = errors.New(
	"no colony context — this tool only works inside a colony. The " +
		"queen reaches colony mode by calling suggest_colony and waiting " +
		"for the user to confirm the Create Colony popup; workers receive " +
		"the binding via input_data automatically.",
)

// nowISO mirrors Python's `datetime.now(UTC).isoformat(timespec="seconds")`:
// second precision with an explicit +00:00 offset (not RFC3339's "Z").
func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05+00:00") }

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteStr(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func changeTriggerNames(table string) (string, string) {
	return "_tracker_chg__" + table + "__ins", "_tracker_chg__" + table + "__upd"
}

func InstallChangeTriggers(db *sql.DB, table string, keyColumns []string) error {
	parts := make([]string, 0, len(keyColumns))
	for _, c := range keyColumns {
		parts = append(parts, quoteStr(c)+", NEW."+quoteIdent(c))
	}
	pkExpr := "json_object(" + strings.Join(parts, ", ") + ")"
	insName, updName := changeTriggerNames(table)
	for _, t := range []struct {
		name  string
		op    string
		event string
	}{
		{insName, "insert", "INSERT"},
		{updName, "update", "UPDATE"},
	} {
		if _, err := db.Exec("DROP TRIGGER IF EXISTS " + quoteIdent(t.name)); err != nil {
			return err
		}
		stmt := "CREATE TRIGGER " + quoteIdent(t.name) +
			" AFTER " + t.event + " ON " + quoteIdent(table) +
			" BEGIN " +
			"INSERT INTO _tracker_changes (table_name, pk, op, changed_at) " +
			"VALUES (" + quoteStr(table) + ", " + pkExpr + ", " + quoteStr(t.op) + ", " +
			"strftime('%Y-%m-%dT%H:%M:%SZ', 'now')); " +
			"END"
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func backfillChangeTriggers(db *sql.DB) {
	rows, err := db.Query("SELECT table_name, key_columns FROM _tracker_registry")
	if err != nil {
		return
	}
	type entry struct{ table, keys string }
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.table, &e.keys); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	_ = rows.Close()
	for _, e := range entries {
		var raw []any
		if err := json.Unmarshal([]byte(e.keys), &raw); err != nil {
			continue
		}
		var keys []string
		for _, k := range raw {
			if s, ok := k.(string); ok && s != "" {
				keys = append(keys, s)
			}
		}
		if len(keys) == 0 {
			continue
		}
		var one int
		if err := db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", e.table).Scan(&one); err != nil {
			continue
		}
		_ = InstallChangeTriggers(db, e.table, keys)
	}
}

func PruneChangeLog(db *sql.DB) error {
	_, err := db.Exec(
		"DELETE FROM _tracker_changes WHERE id <= (SELECT COALESCE(MAX(id), 0) FROM _tracker_changes) - ?",
		ChangeLogMax,
	)
	return err
}

func GetTrackerMeta(db *sql.DB, key string) (string, bool, error) {
	var value string
	err := db.QueryRow("SELECT value FROM _tracker_meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func SetTrackerMeta(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		"INSERT OR REPLACE INTO _tracker_meta(key, value, updated_at) VALUES (?, ?, ?)",
		key, value, nowISO(),
	)
	return err
}

func EnsureTrackerDB(colonyDir string) (string, error) {
	trackerDir := filepath.Join(colonyDir, "tracker")
	if err := os.MkdirAll(trackerDir, 0o755); err != nil {
		return "", err
	}
	dbPath := filepath.Join(trackerDir, "tracker.db")

	db, err := openDB(dbPath)
	if err != nil {
		return "", err
	}
	migrateErr := func() error {
		var current int
		if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
			return err
		}
		if current < SchemaVersion {
			for _, stmt := range splitStatements(bootstrapSchema) {
				if _, err := db.Exec(stmt); err != nil {
					return err
				}
			}
			if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
				return err
			}
			if err := SetTrackerMeta(db, "schema_version", fmt.Sprintf("%d", SchemaVersion)); err != nil {
				return err
			}
			backfillChangeTriggers(db)
		}
		_ = PruneChangeLog(db)
		return nil
	}()
	closeErr := db.Close()
	if migrateErr != nil {
		return "", migrateErr
	}
	if closeErr != nil {
		return "", closeErr
	}

	resolved, err := filepath.Abs(dbPath)
	if err != nil {
		resolved = dbPath
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	if err := patchWorkerConfigs(colonyDir, resolved); err != nil {
		_ = err
	}
	return resolved, nil
}

func EnsureAllColonyTrackerDBs(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var initialized []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p, err := EnsureTrackerDB(filepath.Join(root, entry.Name()))
		if err != nil {
			continue
		}
		initialized = append(initialized, p)
	}
	return initialized, nil
}

func patchWorkerConfigs(colonyDir, trackerDBPath string) error {
	entries, err := os.ReadDir(colonyDir)
	if err != nil {
		return err
	}
	bindingPayload := map[string]any{
		"name":       filepath.Base(colonyDir),
		"dir":        colonyDir,
		"tracker_db": trackerDBPath,
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if name == "metadata.json" || name == "triggers.json" {
			continue
		}
		path := filepath.Join(colonyDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			continue
		}
		if data == nil {
			continue
		}
		if _, ok := data["system_prompt"]; !ok {
			continue
		}
		inputData, ok := data["input_data"].(map[string]any)
		if !ok {
			inputData = map[string]any{}
		}

		changed := false
		if !reflect.DeepEqual(inputData["binding"], bindingPayload) {
			inputData["binding"] = bindingPayload
			changed = true
		}
		for _, legacy := range []string{"db_path", "colony_data_dir", "tracker_db_path", "colony_id"} {
			if _, ok := inputData[legacy]; ok {
				delete(inputData, legacy)
				changed = true
			}
		}
		if !changed {
			continue
		}
		data["input_data"] = inputData
		if err := writeJSONNoHTML(path, data); err != nil {
			continue
		}
	}
	return nil
}

func writeJSONNoHTML(path string, v any) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	out := strings.TrimSuffix(b.String(), "\n")
	return os.WriteFile(path, []byte(out), 0o644)
}

func stripStringsAndComments(sqlText string) string {
	var out strings.Builder
	i, n := 0, len(sqlText)
	for i < n {
		c := sqlText[i]
		switch {
		case c == '-' && i+1 < n && sqlText[i+1] == '-':
			j := strings.IndexByte(sqlText[i:], '\n')
			if j == -1 {
				return out.String()
			}
			out.WriteByte(' ')
			i += j + 1
		case c == '/' && i+1 < n && sqlText[i+1] == '*':
			j := strings.Index(sqlText[i+2:], "*/")
			if j == -1 {
				return out.String()
			}
			out.WriteByte(' ')
			i += 2 + j + 2
		case c == '\'':
			j := i + 1
			for j < n {
				if sqlText[j] == '\'' {
					if j+1 < n && sqlText[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			out.WriteByte(' ')
			i = j + 1
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

func hasExecutableContent(stmt string) bool {
	return strings.TrimSpace(stripStringsAndComments(stmt)) != ""
}

func splitStatements(sqlText string) []string {
	var statements []string
	var current strings.Builder
	i, n := 0, len(sqlText)
	for i < n {
		c := sqlText[i]
		if c == '-' && i+1 < n && sqlText[i+1] == '-' {
			j := strings.IndexByte(sqlText[i:], '\n')
			if j == -1 {
				current.WriteString(sqlText[i:])
				i = n
				continue
			}
			current.WriteString(sqlText[i : i+j+1])
			i += j + 1
			continue
		}
		if c == '/' && i+1 < n && sqlText[i+1] == '*' {
			j := strings.Index(sqlText[i+2:], "*/")
			if j == -1 {
				current.WriteString(sqlText[i:])
				i = n
				continue
			}
			current.WriteString(sqlText[i : i+2+j+2])
			i += 2 + j + 2
			continue
		}
		if c == '\'' {
			current.WriteByte('\'')
			j := i + 1
			for j < n {
				if sqlText[j] == '\'' {
					if j+1 < n && sqlText[j+1] == '\'' {
						current.WriteString("''")
						j += 2
						continue
					}
					current.WriteByte('\'')
					j++
					break
				}
				current.WriteByte(sqlText[j])
				j++
			}
			i = j
			continue
		}
		if c == ';' {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" && hasExecutableContent(stmt) {
				statements = append(statements, stmt)
			}
			current.Reset()
			i++
			continue
		}
		current.WriteByte(c)
		i++
	}
	tail := strings.TrimSpace(current.String())
	if tail != "" && hasExecutableContent(tail) {
		statements = append(statements, tail)
	}
	return statements
}

type wordPos struct {
	word string
	pos  int
}

func topLevelWords(cleaned string) []wordPos {
	var words []wordPos
	depth := 0
	start := -1
	flush := func(end int) {
		if start >= 0 {
			words = append(words, wordPos{word: cleaned[start:end], pos: start})
			start = -1
		}
	}
	for i, ch := range cleaned {
		switch {
		case ch == '(':
			flush(i)
			depth++
		case ch == ')':
			start = -1
			if depth > 0 {
				depth--
			}
		case depth == 0 && (unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_'):
			if start < 0 {
				start = i
			}
		default:
			flush(i)
		}
	}
	flush(len(cleaned))
	return words
}

func effectiveStatement(stmt string) (string, string) {
	cleaned := stripStringsAndComments(stmt)
	words := topLevelWords(cleaned)
	if len(words) == 0 {
		return "", cleaned
	}
	verb := strings.ToUpper(words[0].word)
	if verb != "WITH" {
		return verb, cleaned[words[0].pos:]
	}
	for _, w := range words[1:] {
		up := strings.ToUpper(w.word)
		if withMainVerbs[up] {
			return up, cleaned[w.pos:]
		}
	}
	return verb, cleaned
}

func stripIdentifier(name string) string {
	return strings.Trim(name, "`\"[](),;")
}

func referencedTablesForMutation(stmt string) []string {
	tokens := strings.Fields(stmt)
	if len(tokens) == 0 {
		return nil
	}
	head := strings.ToUpper(tokens[0])
	rest := tokens[1:]
	restUpper := make([]string, len(rest))
	for i, t := range rest {
		restUpper[i] = strings.ToUpper(t)
	}

	switch head {
	case "INSERT", "REPLACE":
		for i, t := range restUpper {
			if t == "INTO" && i+1 < len(rest) {
				return []string{stripIdentifier(rest[i+1])}
			}
		}
		return nil
	case "UPDATE":
		i := 0
		if len(restUpper) >= 2 && restUpper[0] == "OR" {
			i = 2
		}
		if i < len(rest) {
			return []string{stripIdentifier(rest[i])}
		}
		return nil
	case "DELETE":
		for i, t := range restUpper {
			if t == "FROM" && i+1 < len(rest) {
				return []string{stripIdentifier(rest[i+1])}
			}
		}
		return nil
	case "TRUNCATE":
		i := 0
		if len(restUpper) >= 1 && restUpper[0] == "TABLE" {
			i = 1
		}
		if i < len(rest) {
			return []string{stripIdentifier(rest[i])}
		}
		return nil
	case "CREATE", "DROP", "ALTER":
		i := 0
		for i < len(restUpper) {
			switch restUpper[i] {
			case "TEMP", "TEMPORARY", "UNIQUE", "VIRTUAL":
				i++
			default:
				goto modifiersDone
			}
		}
	modifiersDone:
		if i >= len(restUpper) {
			return nil
		}
		kind := restUpper[i]
		if kind != "TABLE" && kind != "INDEX" && kind != "VIEW" && kind != "TRIGGER" {
			return nil
		}
		i++
		if sliceEq(restUpper, i, "IF", "NOT", "EXISTS") {
			i += 3
		} else if sliceEq(restUpper, i, "IF", "EXISTS") {
			i += 2
		}
		var names []string
		if i < len(rest) {
			names = append(names, stripIdentifier(rest[i]))
		}
		if kind == "INDEX" || kind == "TRIGGER" {
			for j := i + 1; j < len(restUpper); j++ {
				if restUpper[j] == "ON" && j+1 < len(rest) {
					names = append(names, stripIdentifier(rest[j+1]))
					break
				}
			}
		}
		out := names[:0]
		for _, n := range names {
			if n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func sliceEq(values []string, start int, want ...string) bool {
	if start+len(want) > len(values) {
		return false
	}
	for i, w := range want {
		if values[start+i] != w {
			return false
		}
	}
	return true
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}

func ValidateSQL(sqlText string) error {
	if strings.TrimSpace(sqlText) == "" {
		return denyf("empty SQL")
	}
	statements := splitStatements(sqlText)
	if len(statements) == 0 {
		return denyf("no executable statements")
	}
	if len(statements) > MaxStatementsPerCall {
		return denyf("too many statements: %d > %d", len(statements), MaxStatementsPerCall)
	}

	cleaned := strings.ToLower(stripStringsAndComments(sqlText))
	for _, fn := range deniedFunctions {
		idx := 0
		for {
			pos := strings.Index(cleaned[idx:], fn)
			if pos == -1 {
				break
			}
			pos += idx
			beforeOK := pos == 0 || !isWordByte(cleaned[pos-1])
			after := strings.TrimLeftFunc(cleaned[pos+len(fn):], unicode.IsSpace)
			if beforeOK && strings.HasPrefix(after, "(") {
				return denyf("forbidden function: %s()", fn)
			}
			idx = pos + len(fn)
		}
	}

	for _, stmt := range statements {
		verb, body := effectiveStatement(stmt)
		if deniedLeadingKeywords[verb] {
			return denyf("forbidden statement: %s", verb)
		}
		if mutatingKeywords[verb] {
			for _, tbl := range referencedTablesForMutation(body) {
				if strings.HasPrefix(tbl, ProtectedPrefix) {
					return denyf("forbidden mutation on framework table: %s", tbl)
				}
			}
		}
	}
	return nil
}

func statementHasWord(stmt, want string) bool {
	cleaned := stripStringsAndComments(stmt)
	for _, w := range topLevelWords(cleaned) {
		if strings.EqualFold(w.word, want) {
			return true
		}
	}
	return false
}

func isReadStatement(verb, stmt string) bool {
	switch verb {
	case "SELECT", "VALUES", "EXPLAIN":
		return true
	}
	return statementHasWord(stmt, "RETURNING")
}

func ExecuteSQL(dbPath, sqlText string, rowCap int) (map[string]any, error) {
	if err := ValidateSQL(sqlText); err != nil {
		return nil, err
	}
	if rowCap <= 0 {
		rowCap = 1000
	}
	statements := splitStatements(sqlText)

	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	results := make([]any, 0, len(statements))
	for _, stmt := range statements {
		result, err := execStatement(db, stmt, rowCap)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	if len(results) == 1 {
		return results[0].(map[string]any), nil
	}
	return map[string]any{"kind": "script", "results": results}, nil
}

func execStatement(db *sql.DB, stmt string, rowCap int) (map[string]any, error) {
	verb, _ := effectiveStatement(stmt)
	if !isReadStatement(verb, stmt) {
		res, err := db.Exec(stmt)
		if err != nil {
			return nil, err
		}
		rowcount, _ := res.RowsAffected()
		lastID, _ := res.LastInsertId()
		return map[string]any{
			"kind":              "exec",
			"rowcount":          rowcount,
			"last_insert_rowid": lastID,
		}, nil
	}

	rows, err := db.Query(stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]any, 0)
	truncated := false
	for {
		if len(out) >= rowCap {
			truncated = rows.Next()
			break
		}
		if !rows.Next() {
			break
		}
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"kind":      "rows",
		"columns":   columns,
		"rows":      out,
		"rowcount":  len(out),
		"truncated": truncated,
	}, nil
}
