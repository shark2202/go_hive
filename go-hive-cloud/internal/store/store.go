// Package store is the persistence layer of go-hive-cloud: one SQLite file per
// team (data/teams/<team_id>.db), the SQL statement gate and the SQLite
// dialect layer.
//
// The real backend is Postgres schema-per-team with free DDL; this private
// deployment keeps the client-observable contract (endpoints, response keys,
// row shapes) on top of SQLite. See docs/m1-notes.md for the fidelity limits.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Errors surfaced to handlers.
var (
	// ErrNotFound marks a missing table/row/database object.
	ErrNotFound = errors.New("not found")
	// ErrConflict marks a primary-key/unique conflict.
	ErrConflict = errors.New("conflict")
	// ErrTimeout marks a statement that hit the per-statement timeout.
	ErrTimeout = errors.New("statement timeout")
	// ErrBadData marks a client-supplied value that cannot be used (unknown
	// column, missing pk, wrong type...).
	ErrBadData = errors.New("bad data")
)

// Options configures a Store.
type Options struct {
	// Dir is the data root (data_dir). Team files live in Dir/teams/.
	Dir string
	// StatementTimeout bounds every user SQL statement (default 5s).
	StatementTimeout time.Duration
	// RowCapDefault applies when a request omits row_cap (<=0 = unlimited).
	RowCapDefault int
	// Now is injectable for tests.
	Now func() time.Time
}

// Store owns the per-team pools.
type Store struct {
	opts  Options
	mu    sync.Mutex
	teams map[string]*Team
}

// Open prepares the data directory. Team databases are opened lazily.
func Open(opts Options) (*Store, error) {
	if opts.Dir == "" {
		return nil, errors.New("store: data dir is required")
	}
	if opts.StatementTimeout <= 0 {
		opts.StatementTimeout = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Join(opts.Dir, "teams"), 0o700); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	return &Store{opts: opts, teams: map[string]*Team{}}, nil
}

// DataDir returns the configured data root.
func (s *Store) DataDir() string { return s.opts.Dir }

// TeamPath returns the on-disk path of a team database (whether or not it has
// been opened yet).
func (s *Store) TeamPath(teamID string) string {
	return filepath.Join(s.opts.Dir, "teams", teamID+".db")
}

// Team returns the lazily-opened team database. teamID must already be
// validated (auth.ValidTeamID).
func (s *Store) Team(teamID string) (*Team, error) {
	if !validIdentifier(teamID) {
		return nil, fmt.Errorf("%w: invalid team id %q", ErrBadData, teamID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.teams[teamID]; ok {
		return t, nil
	}
	t, err := s.openTeam(teamID)
	if err != nil {
		return nil, err
	}
	s.teams[teamID] = t
	return t, nil
}

// Close closes every open team pool.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, t := range s.teams {
		if err := t.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.teams = map[string]*Team{}
	return first
}

// Team is one team's SQLite database plus its read-only handle.
type Team struct {
	ID   string
	Path string

	rw *sql.DB
	ro *sql.DB

	writeMu sync.Mutex
	opts    Options
}

func (s *Store) openTeam(teamID string) (*Team, error) {
	path := s.TeamPath(teamID)
	rw, err := openDB(path, true)
	if err != nil {
		return nil, fmt.Errorf("store: open team %s: %w", teamID, err)
	}
	ro, err := openDB(path, false)
	if err != nil {
		_ = rw.Close()
		return nil, fmt.Errorf("store: open team %s (ro): %w", teamID, err)
	}
	t := &Team{ID: teamID, Path: path, rw: rw, ro: ro, opts: s.opts}
	if err := t.ensureSchema(); err != nil {
		_ = rw.Close()
		_ = ro.Close()
		return nil, err
	}
	return t, nil
}

func openDB(path string, writable bool) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	pragmas := []string{"_pragma=busy_timeout(5000)"}
	if writable {
		pragmas = append(pragmas, "_pragma=journal_mode(WAL)")
	} else {
		pragmas = append(pragmas, "mode=ro", "_pragma=query_only(1)")
	}
	u.RawQuery = strings.Join(pragmas, "&")
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	if writable {
		db.SetMaxOpenConns(4)
	} else {
		db.SetMaxOpenConns(4)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

// Close releases the pools.
func (t *Team) Close() error {
	var first error
	if t.ro != nil {
		if err := t.ro.Close(); err != nil {
			first = err
		}
	}
	if t.rw != nil {
		if err := t.rw.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ensureSchema creates the internal bookkeeping tables plus the inbox tables.
// They are invisible to the global-db listing and unreachable through the
// statement gate (identifiers starting with "_" are rejected).
func (t *Team) ensureSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS _cloud_changes (
  cursor     INTEGER PRIMARY KEY AUTOINCREMENT,
  "table"    TEXT NOT NULL,
  pk         TEXT NOT NULL DEFAULT '',
  op         TEXT NOT NULL,
  at         TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS _cloud_audit (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         TEXT NOT NULL,
  user_id    TEXT NOT NULL DEFAULT '',
  team_id    TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  table_name TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS inbox_messages (
  id                TEXT PRIMARY KEY,
  kind              TEXT NOT NULL,
  title             TEXT NOT NULL DEFAULT '',
  body              TEXT NOT NULL DEFAULT '',
  correlation_token TEXT NOT NULL DEFAULT '',
  deep_link         TEXT,
  status            TEXT NOT NULL DEFAULT 'open',
  resolved_by       TEXT NOT NULL DEFAULT '',
  reply_text        TEXT NOT NULL DEFAULT '',
  created_at        TEXT NOT NULL,
  updated_at        TEXT NOT NULL,
  resolved_at       TEXT,
  runtime_id        TEXT NOT NULL DEFAULT '',
  session_id        TEXT NOT NULL DEFAULT '',
  colony_id         TEXT NOT NULL DEFAULT '',
  user_id           TEXT NOT NULL DEFAULT '',
  team_id           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS inbox_messages_session_idx ON inbox_messages (session_id, status);
CREATE INDEX IF NOT EXISTS inbox_messages_token_idx ON inbox_messages (correlation_token);
CREATE TABLE IF NOT EXISTS inbox_message_reads (
  message_id TEXT NOT NULL,
  user_id    TEXT NOT NULL,
  read_at    TEXT NOT NULL,
  PRIMARY KEY (message_id, user_id)
);
CREATE TABLE IF NOT EXISTS inbox_replies (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id        TEXT NOT NULL DEFAULT '',
  correlation_token TEXT NOT NULL DEFAULT '',
  runtime_id        TEXT NOT NULL DEFAULT '',
  session_id        TEXT NOT NULL DEFAULT '',
  text              TEXT NOT NULL,
  created_at        TEXT NOT NULL,
  delivered_at      TEXT
);
CREATE INDEX IF NOT EXISTS inbox_replies_pending_idx ON inbox_replies (runtime_id, delivered_at);
CREATE TABLE IF NOT EXISTS runtime_connections (
  runtime_id   TEXT NOT NULL,
  kind         TEXT NOT NULL DEFAULT '',
  connected_at TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  PRIMARY KEY (runtime_id, kind)
);
`
	if _, err := t.rw.Exec(schema); err != nil {
		return fmt.Errorf("store: init team %s schema: %w", t.ID, err)
	}
	return nil
}

// SQLError marks a statement that reached SQLite and failed: syntax errors,
// missing tables/columns, unsupported Postgres dialect. Handlers surface it as
// HTTP 400 so the client's error path (`{"error": str}`) sees a client error.
type SQLError struct {
	Message string
	cause   error
}

func (e *SQLError) Error() string { return e.Message }
func (e *SQLError) Unwrap() error { return e.cause }

func sqlError(err error) error {
	if err == nil {
		return nil
	}
	return &SQLError{Message: err.Error(), cause: err}
}

// RunResult is the outcome of one gated SQL statement.
type RunResult struct {
	Columns   []string         `json:"-"`
	Rows      []map[string]any `json:"rows"`
	RowCount  int64            `json:"rowCount"`
	Truncated bool             `json:"truncated"`
	Kind      StatementKind    `json:"-"`
	Table     string           `json:"-"`
}

// Run validates, translates and executes one statement. rowCap <= 0 uses the
// store default (0 = unlimited).
func (t *Team) Run(ctx context.Context, sqlText string, rowCap int) (*RunResult, error) {
	return t.run(ctx, sqlText, rowCap, false)
}

// RunReadOnly executes a statement that must not write: the gate requires a
// read-only statement and the read-only pool is used.
func (t *Team) RunReadOnly(ctx context.Context, sqlText string, rowCap int) (*RunResult, error) {
	return t.run(ctx, sqlText, rowCap, true)
}

func (t *Team) run(ctx context.Context, sqlText string, rowCap int, readOnly bool) (*RunResult, error) {
	if err := CheckStatement(sqlText, readOnly); err != nil {
		return nil, err
	}
	translated := Translate(sqlText)
	if err := CheckStatement(translated, readOnly); err != nil {
		return nil, err
	}
	if rowCap <= 0 {
		rowCap = t.opts.RowCapDefault
	}
	kind, table := ClassifyStatement(translated)
	result := &RunResult{Rows: []map[string]any{}, Kind: kind, Table: table}

	runCtx := ctx
	cancel := func() {}
	if t.opts.StatementTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, t.opts.StatementTimeout)
	}
	defer cancel()

	var err error
	if statementReturnsRows(translated, kind) {
		result.Rows, result.Columns, result.Truncated, err = t.queryRows(runCtx, readOnly, translated, rowCap)
		result.RowCount = int64(len(result.Rows))
	} else {
		var res sql.Result
		if readOnly {
			// The pool itself is query_only, so writes fail there; the gate
			// already rejected non-read statements before reaching this path.
			res, err = t.ro.ExecContext(runCtx, translated)
		} else {
			res, err = t.rw.ExecContext(runCtx, translated)
		}
		if err == nil {
			result.RowCount, _ = res.RowsAffected()
		}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: statement exceeded %s", ErrTimeout, t.opts.StatementTimeout)
		}
		return nil, sqlError(err)
	}
	return result, nil
}

func statementReturnsRows(sqlText string, kind StatementKind) bool {
	if kind == KindRead {
		// Guard against data-modifying CTEs surfacing as "with".
		segments := tokenizeSQL(sqlText)
		for _, seg := range segments {
			if seg.kind != segWord {
				continue
			}
			switch strings.ToLower(seg.text) {
			case "insert", "update", "delete", "replace":
				return containsWord(segments, "returning")
			}
		}
		return true
	}
	return containsWord(tokenizeSQL(sqlText), "returning")
}

func containsWord(segments []segment, word string) bool {
	for _, seg := range segments {
		if seg.kind == segWord && strings.EqualFold(seg.text, word) {
			return true
		}
	}
	return false
}

func (t *Team) queryRows(ctx context.Context, readOnly bool, sqlText string, rowCap int) ([]map[string]any, []string, bool, error) {
	db := t.rw
	if readOnly {
		db = t.ro
	}
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	out := []map[string]any{}
	truncated := false
	for rows.Next() {
		if rowCap > 0 && len(out) >= rowCap {
			truncated = true
			break
		}
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = normalizeDriverValue(values[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	return out, columns, truncated, nil
}

// QueryRaw runs an internal (ungated) query on the rw pool and returns rows as
// maps. Used for introspection and the inbox bookkeeping tables.
func (t *Team) QueryRaw(ctx context.Context, sqlText string, args ...any) ([]map[string]any, error) {
	rows, err := t.rw.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, sqlError(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, sqlError(err)
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, sqlError(err)
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			row[name] = normalizeDriverValue(values[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, sqlError(err)
	}
	return out, nil
}

func normalizeDriverValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	default:
		return v
	}
}

// BindValue converts a JSON-decoded value (json.Number-aware) into something
// the driver accepts. Nested objects/arrays are stored as JSON text.
func BindValue(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		return x, nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		f, err := x.Float64()
		if err != nil {
			return nil, fmt.Errorf("%w: bad number %q", ErrBadData, x.String())
		}
		return f, nil
	case string:
		return x, nil
	default:
		raw, err := json.Marshal(x)
		if err != nil {
			return nil, fmt.Errorf("%w: unsupported value %T", ErrBadData, v)
		}
		return string(raw), nil
	}
}

// RecordChange appends a row to _cloud_changes.
func (t *Team) RecordChange(ctx context.Context, table, pk, op string) error {
	at := t.opts.Now().UTC().Format(time.RFC3339Nano)
	_, err := t.rw.ExecContext(ctx,
		`INSERT INTO _cloud_changes ("table", pk, op, at) VALUES (?, ?, ?, ?)`,
		table, pk, op, at)
	return err
}

// RecordChangeJSON is RecordChange with the pk map serialised to JSON (empty
// object/absent map -> empty string).
func (t *Team) RecordChangeJSON(ctx context.Context, table string, pk map[string]any, op string) error {
	payload := ""
	if len(pk) > 0 {
		raw, err := json.Marshal(pk)
		if err != nil {
			return err
		}
		payload = string(raw)
	}
	return t.RecordChange(ctx, table, payload, op)
}

// Audit appends a row to _cloud_audit.
func (t *Team) Audit(ctx context.Context, userID, teamID, action, table, detail string) error {
	at := t.opts.Now().UTC().Format(time.RFC3339Nano)
	_, err := t.rw.ExecContext(ctx,
		`INSERT INTO _cloud_audit (at, user_id, team_id, action, table_name, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		at, userID, teamID, action, table, detail)
	return err
}

// ChangeCursor returns the current max change cursor ("" when the feed is
// empty).
func (t *Team) ChangeCursor(ctx context.Context) (string, error) {
	rows, err := t.QueryRaw(ctx, `SELECT COALESCE(MAX(cursor), 0) AS cursor FROM _cloud_changes`)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	switch v := rows[0]["cursor"].(type) {
	case int64:
		if v == 0 {
			return "", nil
		}
		return strconv.FormatInt(v, 10), nil
	case float64:
		if v == 0 {
			return "", nil
		}
		return strconv.FormatInt(int64(v), 10), nil
	default:
		return "", nil
	}
}

// NewID mints a message/row identifier.
func NewID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

func validIdentifier(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return false
	}
	first := name[0]
	if !(first == '_' || (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
		return false
	}
	return true
}

// ValidateTableName applies the identifier rule from grid_query
// ([A-Za-z_][A-Za-z0-9_]*) and additionally refuses internal tables.
func ValidateTableName(name string) error {
	if !validIdentifier(name) {
		return fmt.Errorf("%w: invalid identifier: %q", ErrBadData, name)
	}
	if strings.HasPrefix(name, "_") || strings.HasPrefix(strings.ToLower(name), "sqlite_") {
		return &GateError{Code: GateInternal, Message: fmt.Sprintf("access to internal object %q is not allowed", name)}
	}
	return nil
}

func quoteIdent(name string) string { return `"` + name + `"` }

// internalTables are excluded from the global-db listing even though they live
// in the same per-team file.
var internalTables = map[string]bool{
	"_cloud_changes":      true,
	"_cloud_audit":        true,
	"inbox_messages":      true,
	"inbox_message_reads": true,
	"inbox_replies":       true,
	"runtime_connections": true,
}

// ColumnInfo mirrors the client's ColumnInfo (frontend/src/api/colonyData.ts).
type ColumnInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	NotNull   bool   `json:"notnull"`
	PK        int    `json:"pk"`
	DfltValue any    `json:"dflt_value"`
}

// TableOverview mirrors the client's TableOverview.
type TableOverview struct {
	Name       string       `json:"name"`
	Columns    []ColumnInfo `json:"columns"`
	RowCount   int64        `json:"row_count"`
	PrimaryKey []string     `json:"primary_key"`
}

func (t *Team) tableExists(ctx context.Context, table string) (bool, error) {
	rows, err := t.QueryRaw(ctx,
		`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, table)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// TableInfo returns column metadata for table.
func (t *Team) TableInfo(ctx context.Context, table string) ([]ColumnInfo, error) {
	if err := ValidateTableName(table); err != nil {
		return nil, err
	}
	exists, err := t.tableExists(ctx, table)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w: unknown table: %s", ErrNotFound, table)
	}
	rows, err := t.QueryRaw(ctx, `PRAGMA table_info(`+quoteIdent(table)+`)`)
	if err != nil {
		return nil, err
	}
	out := make([]ColumnInfo, 0, len(rows))
	for _, row := range rows {
		info := ColumnInfo{
			Name:      asString(row["name"]),
			Type:      asString(row["type"]),
			NotNull:   asInt(row["notnull"]) != 0,
			PK:        int(asInt(row["pk"])),
			DfltValue: row["dflt_value"],
		}
		out = append(out, info)
	}
	return out, nil
}

// PrimaryKey returns the pk column names in key order.
func PrimaryKey(columns []ColumnInfo) []string {
	type ordered struct {
		pos  int
		name string
	}
	orderedPK := []ordered{}
	for _, c := range columns {
		if c.PK > 0 {
			orderedPK = append(orderedPK, ordered{pos: c.PK, name: c.Name})
		}
	}
	sort.Slice(orderedPK, func(i, j int) bool { return orderedPK[i].pos < orderedPK[j].pos })
	out := make([]string, 0, len(orderedPK))
	for _, item := range orderedPK {
		out = append(out, item.name)
	}
	return out
}

// ListTables returns the team's user tables with column metadata and counts.
func (t *Team) ListTables(ctx context.Context) ([]TableOverview, error) {
	rows, err := t.QueryRaw(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	out := []TableOverview{}
	for _, row := range rows {
		name := asString(row["name"])
		if internalTables[name] || strings.HasPrefix(name, "_") || strings.HasPrefix(strings.ToLower(name), "sqlite_") {
			continue
		}
		columns, err := t.TableInfo(ctx, name)
		if err != nil {
			return nil, err
		}
		countRows, err := t.QueryRaw(ctx, `SELECT COUNT(*) AS n FROM `+quoteIdent(name))
		if err != nil {
			return nil, err
		}
		var count int64
		if len(countRows) > 0 {
			count = asInt(countRows[0]["n"])
		}
		out = append(out, TableOverview{
			Name:       name,
			Columns:    columns,
			RowCount:   count,
			PrimaryKey: PrimaryKey(columns),
		})
	}
	return out, nil
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		i, _ := x.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(x, 10, 64)
		return i
	case bool:
		if x {
			return 1
		}
		return 0
	default:
		return 0
	}
}
