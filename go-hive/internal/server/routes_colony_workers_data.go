package server

// Parity: framework/server/routes_colony_workers.py::handle_list_tables

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

const colonyDataReadTimeout = 15 * time.Second

const changesRowsCap = 2000

const changeTriggerPrefix = "_tracker_chg_"

const groupCountsCap = 200

type colonyTableColumn struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	NotNull   bool   `json:"notnull"`
	PK        int    `json:"pk"`
	DfltValue any    `json:"dflt_value"`
}

type colonyTableOverview struct {
	Name       string              `json:"name"`
	Columns    []colonyTableColumn `json:"columns"`
	RowCount   int64               `json:"row_count"`
	PrimaryKey []string            `json:"primary_key"`
}

type colonyTableRows struct {
	Table      string              `json:"table"`
	Columns    []colonyTableColumn `json:"columns"`
	PrimaryKey []string            `json:"primary_key"`
	Rows       []map[string]any    `json:"rows"`
	Total      int64               `json:"total"`
	Limit      int                 `json:"limit"`
	Offset     int                 `json:"offset"`
}

type colonyDataError struct {
	Error string `json:"error"`
}

type colonyTrackerSQLError struct{ err error }

func (e *colonyTrackerSQLError) Error() string { return e.err.Error() }

func (e *colonyTrackerSQLError) Unwrap() error { return e.err }

type colonyDataTimeoutBody struct {
	Error    string `json:"error"`
	ColonyID string `json:"colony_id"`
}

type colonyChangeRow struct {
	PK any `json:"pk"`
	Op any `json:"op"`
}

type colonyChangeTable struct {
	Count int               `json:"count"`
	Rows  []colonyChangeRow `json:"rows"`
}

type colonyChanges struct {
	Cursor    int64                        `json:"cursor"`
	Covered   []string                     `json:"covered"`
	Truncated bool                         `json:"truncated"`
	Tables    map[string]colonyChangeTable `json:"tables"`
}

type trackerSnapshotEntry struct {
	at   time.Time
	path string
}

var (
	trackerSnapshotDir     = filepath.Join("/var/tmp", "hive-tracker-snapshots")
	trackerSnapshotTTL     = 2 * time.Second
	trackerSnapshotCacheMu sync.Mutex
	trackerSnapshotCache   = map[string]trackerSnapshotEntry{}
	trackerSnapshotLocksMu sync.Mutex
	trackerSnapshotLocks   = map[string]*sync.Mutex{}
)

func emptyColonyChanges() colonyChanges {
	return colonyChanges{Covered: []string{}, Tables: map[string]colonyChangeTable{}}
}

func (d ColonyWorkerDeps) dataReadTimeout() time.Duration {
	if d.DataReadTimeout > 0 {
		return d.DataReadTimeout
	}
	return colonyDataReadTimeout
}

// Parity: framework/server/routes_colony_workers.py::_resolve_tracker_db_by_name
func (d ColonyWorkerDeps) trackerDB(colonyID string) (string, error) {
	if d.ResolveTrackerDB != nil {
		return d.ResolveTrackerDB(colonyID)
	}
	return resolveTrackerDBByName(d.Home, colonyID)
}

func resolveTrackerDBByName(home hivehome.Home, colonyID string) (string, error) {
	if !colonyNameRe.MatchString(colonyID) {
		return "", nil
	}
	colonyDir := filepath.Join(home.ColoniesDir(), colonyID)
	if !dirExists(colonyDir) {
		return "", nil
	}
	dbPath := filepath.Join(colonyDir, "tracker", "tracker.db")
	if info, err := os.Stat(dbPath); err == nil && info.Mode().IsRegular() {
		return dbPath, nil
	}
	return tracker.EnsureTrackerDB(colonyDir)
}

func trackerSnapshotLock(srcKey string) *sync.Mutex {
	trackerSnapshotLocksMu.Lock()
	defer trackerSnapshotLocksMu.Unlock()
	lock, ok := trackerSnapshotLocks[srcKey]
	if !ok {
		lock = &sync.Mutex{}
		trackerSnapshotLocks[srcKey] = lock
	}
	return lock
}

func openTrackerReadOnly(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Parity: framework/server/routes_colony_workers.py::_do_fresh_snapshot
func freshTrackerSnapshot(srcPath string) (string, error) {
	if err := os.MkdirAll(trackerSnapshotDir, 0o755); err != nil {
		return "", err
	}
	handle, err := os.CreateTemp(trackerSnapshotDir, "tracker-*.db")
	if err != nil {
		return "", err
	}
	dst := handle.Name()
	if closeErr := handle.Close(); closeErr != nil {
		_ = os.Remove(dst)
		return "", closeErr
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if copyErr := copyFile(srcPath, dst); copyErr != nil {
			lastErr = copyErr
			continue
		}
		db, openErr := openTrackerReadOnly(dst)
		if openErr != nil {
			lastErr = openErr
			continue
		}
		var count int
		probeErr := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&count)
		_ = db.Close()
		if probeErr != nil {
			lastErr = probeErr
			continue
		}
		return dst, nil
	}

	_ = os.Remove(dst)
	if lastErr == nil {
		lastErr = fmt.Errorf("tracker snapshot failed for %s", srcPath)
	}
	return "", lastErr
}

func copyFile(srcPath, dstPath string) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, data, 0o600)
}

// Parity: framework/server/routes_colony_workers.py::_snapshot_tracker_to_local
func snapshotTrackerToLocal(srcPath string) (string, error) {
	now := time.Now()
	trackerSnapshotCacheMu.Lock()
	cached, ok := trackerSnapshotCache[srcPath]
	trackerSnapshotCacheMu.Unlock()
	if ok && now.Sub(cached.at) < trackerSnapshotTTL && fileExists(cached.path) {
		return cached.path, nil
	}

	lock := trackerSnapshotLock(srcPath)
	lock.Lock()
	defer lock.Unlock()

	now = time.Now()
	trackerSnapshotCacheMu.Lock()
	cached, ok = trackerSnapshotCache[srcPath]
	trackerSnapshotCacheMu.Unlock()
	if ok && now.Sub(cached.at) < trackerSnapshotTTL && fileExists(cached.path) {
		return cached.path, nil
	}

	fresh, err := freshTrackerSnapshot(srcPath)
	if err != nil {
		return "", err
	}

	trackerSnapshotCacheMu.Lock()
	previous, hadPrevious := trackerSnapshotCache[srcPath]
	trackerSnapshotCache[srcPath] = trackerSnapshotEntry{at: time.Now(), path: fresh}
	trackerSnapshotCacheMu.Unlock()
	if hadPrevious && previous.path != fresh {
		_ = os.Remove(previous.path)
	}
	return fresh, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Parity: framework/server/routes_colony_workers.py::_q
func quoteSQLiteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// Parity: framework/server/routes_colony_workers.py::_list_user_tables
func listUserTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT LIKE '\_%' ESCAPE '\' ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Parity: framework/server/routes_colony_workers.py::_table_columns
func tableColumns(db *sql.DB, table string) ([]colonyTableColumn, error) {
	rows, err := db.Query("PRAGMA table_info(" + quoteSQLiteIdent(table) + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []colonyTableColumn{}
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   sql.NullString
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		var defaultValue any
		if dfltValue.Valid {
			defaultValue = dfltValue.String
		}
		out = append(out, colonyTableColumn{
			Name:      name,
			Type:      colType.String,
			NotNull:   notNull != 0,
			PK:        pk,
			DfltValue: defaultValue,
		})
	}
	return out, rows.Err()
}

func primaryKeyColumns(columns []colonyTableColumn) []string {
	out := []string{}
	for _, column := range columns {
		if column.PK > 0 {
			out = append(out, column.Name)
		}
	}
	return out
}

func countTableRows(db *sql.DB, table string) (int64, error) {
	var total int64
	err := db.QueryRow("SELECT COUNT(*) AS c FROM " + quoteSQLiteIdent(table)).Scan(&total)
	return total, err
}

// Parity: framework/server/routes_colony_workers.py::_read_tables_overview
func readTablesOverview(dbPath string) ([]colonyTableOverview, error) {
	snapshot, err := snapshotTrackerToLocal(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := openTrackerReadOnly(snapshot)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	names, err := listUserTables(db)
	if err != nil {
		return nil, err
	}
	out := []colonyTableOverview{}
	for _, name := range names {
		columns, columnErr := tableColumns(db, name)
		if columnErr != nil {
			return nil, columnErr
		}
		total, countErr := countTableRows(db, name)
		if countErr != nil {
			return nil, countErr
		}
		out = append(out, colonyTableOverview{
			Name:       name,
			Columns:    columns,
			RowCount:   total,
			PrimaryKey: primaryKeyColumns(columns),
		})
	}
	return out, nil
}

// Parity: framework/server/routes_colony_workers.py::_read_table_rows
func readTableRows(dbPath, table string, limit, offset int, orderBy, orderDir string) (any, error) {
	snapshot, err := snapshotTrackerToLocal(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := openTrackerReadOnly(snapshot)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	names, err := listUserTables(db)
	if err != nil {
		return nil, err
	}
	known := map[string]struct{}{}
	for _, name := range names {
		known[name] = struct{}{}
	}
	if _, ok := known[table]; !ok {
		return colonyDataError{Error: "unknown table: " + table}, nil
	}

	columns, err := tableColumns(db, table)
	if err != nil {
		return nil, err
	}
	columnSet := map[string]struct{}{}
	for _, column := range columns {
		columnSet[column.Name] = struct{}{}
	}

	statement := "SELECT * FROM " + quoteSQLiteIdent(table)
	if orderBy != "" {
		if _, ok := columnSet[orderBy]; ok {
			direction := "ASC"
			if strings.ToLower(orderDir) == "desc" {
				direction = "DESC"
			}
			statement += " ORDER BY " + quoteSQLiteIdent(orderBy) + " " + direction
		}
	}
	statement += " LIMIT ? OFFSET ?"

	rows, err := db.Query(statement, limit, offset)
	if err != nil {
		return nil, err
	}
	columnsOut, scanErr := scanRows(rows)
	if scanErr != nil {
		return nil, scanErr
	}
	total, err := countTableRows(db, table)
	if err != nil {
		return nil, err
	}
	return colonyTableRows{
		Table:      table,
		Columns:    columns,
		PrimaryKey: primaryKeyColumns(columns),
		Rows:       columnsOut,
		Total:      total,
		Limit:      limit,
		Offset:     offset,
	}, nil
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(names))
		targets := make([]any, len(names))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(names))
		for i, name := range names {
			value := values[i]
			if raw, isBytes := value.([]byte); isBytes {
				value = string(raw)
			}
			row[name] = value
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Parity: framework/server/routes_colony_workers.py::_update_table_row
func updateTableRow(dbPath, table string, pk, updates map[string]any) (any, error) {
	if len(updates) == 0 {
		return colonyDataError{Error: "no updates provided"}, nil
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return nil, err
	}

	names, err := listUserTables(db)
	if err != nil {
		return nil, err
	}
	known := map[string]struct{}{}
	for _, name := range names {
		known[name] = struct{}{}
	}
	if _, ok := known[table]; !ok {
		return colonyDataError{Error: "unknown table: " + table}, nil
	}

	columns, err := tableColumns(db, table)
	if err != nil {
		return nil, err
	}
	columnSet := map[string]struct{}{}
	for _, column := range columns {
		columnSet[column.Name] = struct{}{}
	}
	pkColumns := primaryKeyColumns(columns)
	if len(pkColumns) == 0 {
		return colonyDataError{Error: "table " + pythonStringRepr(table) + " has no primary key; cannot edit by row"}, nil
	}

	missing := []string{}
	for _, column := range pkColumns {
		if _, ok := pk[column]; !ok {
			missing = append(missing, column)
		}
	}
	if len(missing) > 0 {
		return colonyDataError{Error: "missing primary key columns: " + pythonListRepr(missing)}, nil
	}

	bad := []string{}
	for column := range updates {
		if _, ok := columnSet[column]; !ok {
			bad = append(bad, column)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		return colonyDataError{Error: "unknown columns: " + pythonListRepr(bad)}, nil
	}

	pkUpdates := []string{}
	for column := range updates {
		for _, pkColumn := range pkColumns {
			if column == pkColumn {
				pkUpdates = append(pkUpdates, column)
			}
		}
	}
	if len(pkUpdates) > 0 {
		sort.Strings(pkUpdates)
		return colonyDataError{Error: "cannot edit primary key columns: " + pythonListRepr(pkUpdates)}, nil
	}

	ordered := make([]string, 0, len(updates))
	for column := range updates {
		ordered = append(ordered, column)
	}
	sort.Strings(ordered)
	setParts := make([]string, 0, len(ordered))
	params := make([]any, 0, len(ordered)+len(pkColumns))
	for _, column := range ordered {
		setParts = append(setParts, quoteSQLiteIdent(column)+" = ?")
		params = append(params, updates[column])
	}
	whereParts := make([]string, 0, len(pkColumns))
	for _, column := range pkColumns {
		whereParts = append(whereParts, quoteSQLiteIdent(column)+" = ?")
		params = append(params, pk[column])
	}

	statement := "UPDATE " + quoteSQLiteIdent(table) + " SET " + strings.Join(setParts, ", ") +
		" WHERE " + strings.Join(whereParts, " AND ")
	result, err := db.Exec(statement, params...)
	if err != nil {
		return nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	return map[string]any{"updated": affected}, nil
}

// Parity: framework/server/routes_colony_workers.py::_read_changes
func readChanges(dbPath string, since int64) (colonyChanges, error) {
	snapshot, err := snapshotTrackerToLocal(dbPath)
	if err != nil {
		return colonyChanges{}, err
	}
	db, err := openTrackerReadOnly(snapshot)
	if err != nil {
		return colonyChanges{}, err
	}
	defer db.Close()
	sqlFailure := func(err error) error { return &colonyTrackerSQLError{err: err} }

	out := emptyColonyChanges()
	if err := db.QueryRow("SELECT COALESCE(MAX(id), 0) AS c FROM _tracker_changes").Scan(&out.Cursor); err != nil {
		return colonyChanges{}, sqlFailure(err)
	}

	coveredPattern := strings.ReplaceAll(changeTriggerPrefix, "_", `\_`) + "%"
	rows, err := db.Query(
		`SELECT DISTINCT tbl_name AS t FROM sqlite_master WHERE type='trigger' AND name LIKE ? ESCAPE '\'`,
		coveredPattern,
	)
	if err != nil {
		return colonyChanges{}, sqlFailure(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return colonyChanges{}, sqlFailure(err)
		}
		out.Covered = append(out.Covered, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return colonyChanges{}, sqlFailure(err)
	}
	rows.Close()

	if since < 0 || since >= out.Cursor {
		return out, nil
	}

	changes, err := db.Query(
		"SELECT table_name, pk, op FROM _tracker_changes WHERE id > ? ORDER BY id LIMIT ?",
		since, changesRowsCap+1,
	)
	if err != nil {
		return colonyChanges{}, sqlFailure(err)
	}
	defer changes.Close()

	perKey := map[string]map[string]colonyChangeRow{}
	seen := 0
	for changes.Next() {
		seen++
		if seen > changesRowsCap {
			out.Truncated = true
			break
		}
		var (
			tableName string
			pkText    sql.NullString
			op        any
		)
		if err := changes.Scan(&tableName, &pkText, &op); err != nil {
			return colonyChanges{}, sqlFailure(err)
		}
		var pk any
		if !pkText.Valid || json.Unmarshal([]byte(pkText.String), &pk) != nil {
			continue
		}
		encoded, err := json.Marshal(pk)
		if err != nil {
			continue
		}
		bucket, ok := perKey[tableName]
		if !ok {
			bucket = map[string]colonyChangeRow{}
			perKey[tableName] = bucket
		}
		if _, seen := bucket[string(encoded)]; seen {
			continue
		}
		bucket[string(encoded)] = colonyChangeRow{PK: pk, Op: op}
	}
	if err := changes.Err(); err != nil {
		return colonyChanges{}, sqlFailure(err)
	}

	out.Tables = map[string]colonyChangeTable{}
	for name, bucket := range perKey {
		entries := make([]colonyChangeRow, 0, len(bucket))
		for _, row := range bucket {
			entries = append(entries, row)
		}
		out.Tables[name] = colonyChangeTable{Count: len(entries), Rows: entries}
	}
	return out, nil
}

func pythonStringRepr(text string) string {
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

func pythonListRepr(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, pythonStringRepr(item))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func runColonyDataWork(timeout time.Duration, work func() (any, error)) (any, bool, error) {
	type outcome struct {
		value any
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := work()
		done <- outcome{value: value, err: err}
	}()
	select {
	case result := <-done:
		return result.value, false, result.err
	case <-time.After(timeout):
		return nil, true, nil
	}
}

func writeColonyDataTimeout(w http.ResponseWriter, colonyID string) {
	writeJSON(w, http.StatusServiceUnavailable, colonyDataTimeoutBody{
		Error:    "colony_data_timeout",
		ColonyID: colonyID,
	})
}

func handleColonyListTables(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	colonyID := r.PathValue("colony_id")
	value, timedOut, err := runColonyDataWork(deps.dataReadTimeout(), func() (any, error) {
		dbPath, resolveErr := deps.trackerDB(colonyID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if dbPath == "" {
			return map[string]any{"tables": []colonyTableOverview{}}, nil
		}
		tables, readErr := readTablesOverview(dbPath)
		if readErr != nil {
			return nil, readErr
		}
		return map[string]any{"tables": tables}, nil
	})
	if timedOut {
		writeColonyDataTimeout(w, colonyID)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func handleColonyTableRows(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	colonyID := r.PathValue("colony_id")
	table := r.PathValue("table")
	query := r.URL.Query()

	limit, ok := pythonQueryInt(query, "limit", 100)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid limit/offset"})
		return
	}
	offset, ok := pythonQueryInt(query, "offset", 0)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid limit/offset"})
		return
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
	orderBy := query.Get("order_by")
	orderDir := query.Get("order_dir")
	if orderDir == "" {
		orderDir = "asc"
	}

	value, timedOut, err := runColonyDataWork(deps.dataReadTimeout(), func() (any, error) {
		dbPath, resolveErr := deps.trackerDB(colonyID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if dbPath == "" {
			return nil, nil
		}
		return readTableRows(dbPath, table, limit, offset, orderBy, orderDir)
	})
	if timedOut {
		writeColonyDataTimeout(w, colonyID)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if value == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no tracker.db"})
		return
	}
	if failure, isError := value.(colonyDataError); isError {
		writeJSON(w, http.StatusBadRequest, failure)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func handleColonyUpdateRow(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	colonyID := r.PathValue("colony_id")
	table := r.PathValue("table")

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

	value, timedOut, err := runColonyDataWork(deps.dataReadTimeout(), func() (any, error) {
		dbPath, resolveErr := deps.trackerDB(colonyID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if dbPath == "" {
			return nil, nil
		}
		return updateTableRow(dbPath, table, pk, updates)
	})
	if timedOut {
		writeColonyDataTimeout(w, colonyID)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if value == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "no tracker.db"})
		return
	}
	if failure, isError := value.(colonyDataError); isError {
		writeJSON(w, http.StatusBadRequest, failure)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

// Parity: framework/server/routes_colony_workers.py::handle_table_changes
func handleColonyTableChanges(w http.ResponseWriter, r *http.Request, deps ColonyWorkerDeps) {
	colonyID := r.PathValue("colony_id")
	since, ok := pythonQueryInt(r.URL.Query(), "since", -1)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid since"})
		return
	}

	value, timedOut, err := runColonyDataWork(deps.dataReadTimeout(), func() (any, error) {
		dbPath, resolveErr := deps.trackerDB(colonyID)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if dbPath == "" {
			return nil, nil
		}
		changes, readErr := readChanges(dbPath, int64(since))
		if readErr != nil {
			var sqlErr *colonyTrackerSQLError
			if errors.As(readErr, &sqlErr) {
				return emptyColonyChanges(), nil
			}
			return nil, readErr
		}
		return changes, nil
	})
	if timedOut {
		writeColonyDataTimeout(w, colonyID)
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if value == nil {
		writeJSON(w, http.StatusOK, emptyColonyChanges())
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func mapField(body map[string]any, key string) (map[string]any, bool) {
	value := body[key]
	if !pythonTruthy(value) {
		return map[string]any{}, true
	}
	typed, ok := value.(map[string]any)
	return typed, ok
}

func pythonIntParam(raw string, fallback int) (int, bool) {
	if raw == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func pythonQueryInt(query url.Values, key string, fallback int) (int, bool) {
	values, present := query[key]
	if !present || len(values) == 0 {
		return fallback, true
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(values[0]))
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// Parity: framework/server/routes_colony_workers.py::register_routes
func registerColonyDataRoutes(mux *http.ServeMux, deps ColonyWorkerDeps) {
	mux.HandleFunc("GET /api/colonies/{colony_id}/data/tables", func(w http.ResponseWriter, r *http.Request) {
		handleColonyListTables(w, r, deps)
	})
	mux.HandleFunc("GET /api/colonies/{colony_id}/data/changes", func(w http.ResponseWriter, r *http.Request) {
		handleColonyTableChanges(w, r, deps)
	})
	mux.HandleFunc("GET /api/colonies/{colony_id}/data/tables/{table}/rows", func(w http.ResponseWriter, r *http.Request) {
		handleColonyTableRows(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/colonies/{colony_id}/data/tables/{table}/rows", func(w http.ResponseWriter, r *http.Request) {
		handleColonyUpdateRow(w, r, deps)
	})
}
