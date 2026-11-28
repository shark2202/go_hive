package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// CRMEntities is the entity registry recovered from the open-source client
// material: the hive-crm CLI describes the team CRM as "people, companies,
// opportunities" (go_hive internal worker-delegation skill / queen prompts).
// The desktop grid labels them People/Orgs/Deals/Activity; the path slugs for
// those labels are not recoverable, so only the three CLI nouns are accepted.
var CRMEntities = []string{"people", "companies", "opportunities"}

// CRMEntityTable maps a contracted entity name to its private-deploy table.
// Tables live beside the global-db tables but use a crm_ prefix so they never
// appear in GET /v1/global-db/tables.
func CRMEntityTable(entity string) (string, error) {
	for _, name := range CRMEntities {
		if entity == name {
			return "crm_" + entity, nil
		}
	}
	return "", fmt.Errorf("%w: unknown entity: %s", ErrNotFound, entity)
}

// CRMGroupCount is one distinct value of a grouped column.
type CRMGroupCount struct {
	Value any   `json:"value"`
	Count int64 `json:"count"`
}

// CRMGroupCounts mirrors the generic grid group-counts response
// ({table, group_by, groups:[{value, count}]}).
type CRMGroupCounts struct {
	Table   string          `json:"table"`
	GroupBy string          `json:"group_by"`
	Groups  []CRMGroupCount `json:"groups"`
}

const crmGroupCountsCap = 200

var crmReservedFields = map[string]bool{
	"id":          true,
	"created_at":  true,
	"updated_at":  true,
	"archived_at": true,
}

// CRMQueryRecords runs the grid query for an entity. It returns the same
// table/columns/primary_key/rows/total/limit/offset shape as the runtime's
// generic row pages (the shape the architecture contract pins for CRM grid row
// operations).
func (t *Team) CRMQueryRecords(ctx context.Context, entity string, body map[string]any) (*RowPage, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return nil, err
	}
	if err := t.ensureCRMTableSafe(ctx, table); err != nil {
		return nil, err
	}
	opts, err := ParseGridQueryOptions(body)
	if err != nil {
		return nil, err
	}
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return nil, err
	}
	allowed := columnSet(columns)
	selectSQL, args, err := BuildGridSelect(table, allowed, opts, true)
	if err != nil {
		return nil, err
	}
	countSQL, countArgs, err := BuildGridCount(table, allowed, opts, true)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := t.crmTimeoutContext(ctx)
	defer cancel()
	rows, err := t.QueryRawReadOnly(queryCtx, selectSQL, args...)
	if err != nil {
		return nil, err
	}
	countRows, err := t.QueryRawReadOnly(queryCtx, countSQL, countArgs...)
	if err != nil {
		return nil, err
	}
	total := int64(0)
	if len(countRows) > 0 {
		total = asInt(countRows[0]["total"])
	}
	return &RowPage{
		Table:      table,
		Columns:    columns,
		PrimaryKey: PrimaryKey(columns),
		Rows:       rows,
		Total:      total,
		Limit:      opts.Limit,
		Offset:     opts.Offset,
	}, nil
}

// CRMGroupCounts returns one row per distinct group_by value under the active
// filters/search, highest count first. The group set is capped so a
// high-cardinality column cannot return an unbounded response.
func (t *Team) CRMGroupCounts(ctx context.Context, entity string, body map[string]any) (*CRMGroupCounts, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return nil, err
	}
	if err := t.ensureCRMTableSafe(ctx, table); err != nil {
		return nil, err
	}
	rawGroup, ok := body["group_by"]
	if !ok || rawGroup == nil {
		return nil, gridErr("group_by is required")
	}
	groupBy, ok := rawGroup.(string)
	if !ok || strings.TrimSpace(groupBy) == "" {
		return nil, gridErr("group_by is required")
	}
	opts, err := ParseGridQueryOptions(body)
	if err != nil {
		return nil, err
	}
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return nil, err
	}
	allowed := columnSet(columns)
	sqlText, args, err := BuildGridGroupCounts(table, allowed, groupBy, opts, true, crmGroupCountsCap)
	if err != nil {
		return nil, err
	}
	queryCtx, cancel := t.crmTimeoutContext(ctx)
	defer cancel()
	rows, err := t.QueryRawReadOnly(queryCtx, sqlText, args...)
	if err != nil {
		return nil, err
	}
	groups := make([]CRMGroupCount, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, CRMGroupCount{Value: row["value"], Count: asInt(row["count"])})
	}
	return &CRMGroupCounts{Table: table, GroupBy: groupBy, Groups: groups}, nil
}

// CRMGetRecord returns one unarchived record as a one-row page.
func (t *Team) CRMGetRecord(ctx context.Context, entity, id string) (*RowPage, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return nil, err
	}
	if err := t.ensureCRMTableSafe(ctx, table); err != nil {
		return nil, err
	}
	queryCtx, cancel := t.crmTimeoutContext(ctx)
	defer cancel()
	row, err := t.crmSelectRowReadOnly(queryCtx, table, id, false)
	if err != nil {
		return nil, err
	}
	return t.crmPage(ctx, table, []map[string]any{row}, 1, 1, 0)
}

// CRMCreateRecord inserts one record from {fields, identities?, because?}.
//
// fields is the only write channel this private deployment implements.
// identities is accepted only when empty: its shape and dedupe semantics live
// in the closed backend and are not recoverable, so a non-empty value is
// rejected instead of being guessed at.
func (t *Team) CRMCreateRecord(ctx context.Context, entity string, body map[string]any, userID, teamID string) (*RowPage, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return nil, err
	}
	if err := rejectUnsupportedIdentities(body); err != nil {
		return nil, err
	}
	fields, err := crmFieldsObject(body, true)
	if err != nil {
		return nil, err
	}
	because, err := crmBecause(body)
	if err != nil {
		return nil, err
	}
	for name := range fields {
		if err := validateCRMFieldName(name); err != nil {
			return nil, err
		}
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.ensureCRMTable(ctx, table); err != nil {
		return nil, err
	}
	if err := t.addCRMFields(ctx, table, fields); err != nil {
		return nil, err
	}

	now := t.Now()
	id := NewID()
	row := make(map[string]any, len(fields)+5)
	for name, value := range fields {
		row[name] = value
	}
	row["id"] = id
	row["created_at"] = now
	row["updated_at"] = now
	if _, ok := row["stage"]; !ok {
		row["stage"] = nil
	}

	names := make([]string, 0, len(row))
	for name := range row {
		names = append(names, name)
	}
	sort.Strings(names)
	placeholders := make([]string, 0, len(names))
	args := make([]any, 0, len(names))
	for _, name := range names {
		value, err := BindValue(row[name])
		if err != nil {
			return nil, err
		}
		placeholders = append(placeholders, "?")
		args = append(args, value)
	}
	statement := "INSERT INTO " + quoteIdent(table) + " (" + joinQuoted(names) + ") VALUES (" + strings.Join(placeholders, ", ") + ")"
	if _, err := t.rw.ExecContext(ctx, statement, args...); err != nil {
		if isUniqueViolation(err) {
			return nil, &ConflictError{PK: map[string]any{"id": id}}
		}
		return nil, sqlError(err)
	}
	if err := t.RecordChangeJSON(ctx, table, map[string]any{"id": id}, "insert"); err != nil {
		return nil, err
	}
	if err := t.Audit(ctx, userID, teamID, "crm_create", table, crmAuditDetail(because, len(fields))); err != nil {
		return nil, err
	}
	stored, err := t.crmSelectRow(ctx, table, id, true)
	if err != nil {
		return nil, err
	}
	return t.crmPage(ctx, table, []map[string]any{stored}, 1, 1, 0)
}

// CRMUpdateRecord applies {fields?, identities?, because?}. fields may be
// omitted (an identity/because-only write returns the current record).
func (t *Team) CRMUpdateRecord(ctx context.Context, entity, id string, body map[string]any, userID, teamID string) (*RowPage, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return nil, err
	}
	if err := rejectUnsupportedIdentities(body); err != nil {
		return nil, err
	}
	fields, err := crmFieldsObject(body, false)
	if err != nil {
		return nil, err
	}
	because, err := crmBecause(body)
	if err != nil {
		return nil, err
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.ensureCRMTable(ctx, table); err != nil {
		return nil, err
	}
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return nil, err
	}
	if _, err := t.crmSelectRow(ctx, table, id, false); err != nil {
		return nil, err
	}
	byName := columnSet(columns)
	for name := range fields {
		if err := validateCRMFieldName(name); err != nil {
			return nil, err
		}
		if !byName[name] {
			return nil, fmt.Errorf("%w: unknown column: %s", ErrBadData, name)
		}
	}
	if len(fields) > 0 {
		setColumns := make([]string, 0, len(fields)+1)
		args := make([]any, 0, len(fields)+2)
		for name, value := range fields {
			bound, err := BindValue(value)
			if err != nil {
				return nil, err
			}
			setColumns = append(setColumns, quoteIdent(name)+" = ?")
			args = append(args, bound)
		}
		setColumns = append(setColumns, quoteIdent("updated_at")+" = ?")
		args = append(args, t.Now())
		args = append(args, id)
		statement := "UPDATE " + quoteIdent(table) + " SET " + strings.Join(setColumns, ", ") +
			" WHERE " + quoteIdent("id") + " = ? AND " + quoteIdent("archived_at") + " IS NULL"
		result, err := t.rw.ExecContext(ctx, statement, args...)
		if err != nil {
			return nil, sqlError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			return nil, fmt.Errorf("%w: row not found", ErrNotFound)
		}
		if err := t.RecordChangeJSON(ctx, table, map[string]any{"id": id}, "update"); err != nil {
			return nil, err
		}
		if err := t.Audit(ctx, userID, teamID, "crm_update", table, crmAuditDetail(because, len(fields))); err != nil {
			return nil, err
		}
	} else if err := t.Audit(ctx, userID, teamID, "crm_update", table, crmAuditDetail(because, 0)); err != nil {
		return nil, err
	}
	stored, err := t.crmSelectRow(ctx, table, id, true)
	if err != nil {
		return nil, err
	}
	return t.crmPage(ctx, table, []map[string]any{stored}, 1, 1, 0)
}

// CRMDeleteRecord soft-deletes (archives) one record. The DELETE body is
// optional; only {because?} is understood.
func (t *Team) CRMDeleteRecord(ctx context.Context, entity, id string, body map[string]any, userID, teamID string) (int64, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return 0, err
	}
	because, err := crmBecause(body)
	if err != nil {
		return 0, err
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.ensureCRMTable(ctx, table); err != nil {
		return 0, err
	}
	now := t.Now()
	result, err := t.rw.ExecContext(ctx,
		"UPDATE "+quoteIdent(table)+" SET "+quoteIdent("archived_at")+" = ?, "+quoteIdent("updated_at")+" = ? WHERE "+
			quoteIdent("id")+" = ? AND "+quoteIdent("archived_at")+" IS NULL",
		now, now, id)
	if err != nil {
		return 0, sqlError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected > 0 {
		if err := t.RecordChangeJSON(ctx, table, map[string]any{"id": id}, "delete"); err != nil {
			return 0, err
		}
	}
	if err := t.Audit(ctx, userID, teamID, "crm_delete", table, crmAuditDetail(because, 0)); err != nil {
		return 0, err
	}
	return affected, nil
}

// CRMSetStage applies {value, because?} to the entity's stage column. The
// set/transition rules live in the closed backend and are UNKNOWN; this
// deployment accepts any non-empty team-defined stage value (the frontend
// documents stages as team-configured) and does not enforce a transition graph.
func (t *Team) CRMSetStage(ctx context.Context, entity, id string, body map[string]any, userID, teamID string) (*RowPage, error) {
	table, err := CRMEntityTable(entity)
	if err != nil {
		return nil, err
	}
	rawValue, ok := body["value"]
	if !ok || rawValue == nil {
		return nil, gridErr("value is required")
	}
	value, ok := rawValue.(string)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, gridErr("value is required")
	}
	because, err := crmBecause(body)
	if err != nil {
		return nil, err
	}

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if err := t.ensureCRMTable(ctx, table); err != nil {
		return nil, err
	}
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return nil, err
	}
	if !columnSet(columns)["stage"] {
		return nil, fmt.Errorf("%w: entity has no stage column", ErrBadData)
	}
	now := t.Now()
	result, err := t.rw.ExecContext(ctx,
		"UPDATE "+quoteIdent(table)+" SET "+quoteIdent("stage")+" = ?, "+quoteIdent("updated_at")+" = ? WHERE "+
			quoteIdent("id")+" = ? AND "+quoteIdent("archived_at")+" IS NULL",
		value, now, id)
	if err != nil {
		return nil, sqlError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, fmt.Errorf("%w: row not found", ErrNotFound)
	}
	if err := t.RecordChangeJSON(ctx, table, map[string]any{"id": id}, "update"); err != nil {
		return nil, err
	}
	if err := t.Audit(ctx, userID, teamID, "crm_stage", table, "stage="+value+" "+crmAuditDetail(because, 0)); err != nil {
		return nil, err
	}
	stored, err := t.crmSelectRow(ctx, table, id, true)
	if err != nil {
		return nil, err
	}
	return t.crmPage(ctx, table, []map[string]any{stored}, 1, 1, 0)
}

// ensureCRMTableSafe serialises first-use table creation (and its ALTERs) with
// write operations. Read handlers call this; write handlers already hold
// writeMu and call ensureCRMTable directly.
func (t *Team) ensureCRMTableSafe(ctx context.Context, table string) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.ensureCRMTable(ctx, table)
}

// ensureCRMTable creates the entity table on first use. CREATE TABLE IF NOT
// EXISTS keeps an existing table (and its accumulated custom fields) intact.
func (t *Team) ensureCRMTable(ctx context.Context, table string) error {
	if err := ValidateTableName(table); err != nil {
		return err
	}
	exists, err := t.tableExists(ctx, table)
	if err != nil {
		return err
	}
	if !exists {
		statement := `CREATE TABLE IF NOT EXISTS "` + table + `" (
  "id" TEXT PRIMARY KEY,
  "stage" TEXT,
  "created_at" TEXT NOT NULL,
  "updated_at" TEXT NOT NULL,
  "archived_at" TEXT
)`
		if _, err := t.rw.ExecContext(ctx, statement); err != nil {
			return sqlError(err)
		}
	}
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return err
	}
	byName := columnSet(columns)
	if !byName["id"] {
		return fmt.Errorf("%w: crm table %s has no id column", ErrBadData, table)
	}
	for _, name := range []string{"stage", "created_at", "updated_at", "archived_at"} {
		if byName[name] {
			continue
		}
		if _, err := t.rw.ExecContext(ctx, "ALTER TABLE "+quoteIdent(table)+" ADD COLUMN "+quoteIdent(name)+" TEXT"); err != nil {
			return sqlError(err)
		}
	}
	return nil
}

// addCRMFields adds columns for create-time fields. The real CRM schema is
// server-owned; here a first write establishes the team's custom columns.
func (t *Team) addCRMFields(ctx context.Context, table string, fields map[string]any) error {
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return err
	}
	byName := columnSet(columns)
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if byName[name] {
			continue
		}
		statement := "ALTER TABLE " + quoteIdent(table) + " ADD COLUMN " + quoteIdent(name) + " " + inferSQLType(fields[name])
		if _, err := t.rw.ExecContext(ctx, statement); err != nil {
			return sqlError(err)
		}
	}
	return nil
}

func (t *Team) crmSelectRow(ctx context.Context, table, id string, includeArchived bool) (map[string]any, error) {
	statement := "SELECT * FROM " + quoteIdent(table) + " WHERE " + quoteIdent("id") + " = ?"
	if !includeArchived {
		statement += " AND " + quoteIdent("archived_at") + " IS NULL"
	}
	statement += " LIMIT 1"
	rows, err := t.QueryRaw(ctx, statement, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: row not found", ErrNotFound)
	}
	return rows[0], nil
}

// crmTimeoutContext bounds a read query by the configured statement timeout.
func (t *Team) crmTimeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if t.opts.StatementTimeout > 0 {
		return context.WithTimeout(ctx, t.opts.StatementTimeout)
	}
	return ctx, func() {}
}

func (t *Team) crmSelectRowReadOnly(ctx context.Context, table, id string, includeArchived bool) (map[string]any, error) {
	statement := "SELECT * FROM " + quoteIdent(table) + " WHERE " + quoteIdent("id") + " = ?"
	if !includeArchived {
		statement += " AND " + quoteIdent("archived_at") + " IS NULL"
	}
	statement += " LIMIT 1"
	rows, err := t.QueryRawReadOnly(ctx, statement, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: row not found", ErrNotFound)
	}
	return rows[0], nil
}

func (t *Team) crmPage(ctx context.Context, table string, rows []map[string]any, total int64, limit, offset int) (*RowPage, error) {
	columns, err := t.TableInfo(ctx, table)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []map[string]any{}
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

func columnSet(columns []ColumnInfo) map[string]bool {
	set := make(map[string]bool, len(columns))
	for _, column := range columns {
		set[column.Name] = true
	}
	return set
}

func validateCRMFieldName(name string) error {
	if !gridIdentifier(name) {
		return &GridError{Message: fmt.Sprintf("invalid identifier: '%s'", strings.ReplaceAll(name, "'", "\\'"))}
	}
	if strings.HasPrefix(name, "_") || strings.HasPrefix(strings.ToLower(name), "sqlite_") {
		return &GateError{Code: GateInternal, Message: fmt.Sprintf("access to internal object %q is not allowed", name)}
	}
	if crmReservedFields[name] {
		return fmt.Errorf("%w: field is reserved: %s", ErrBadData, name)
	}
	return nil
}

func crmFieldsObject(body map[string]any, required bool) (map[string]any, error) {
	raw, ok := body["fields"]
	if !ok || raw == nil {
		if required {
			return nil, fmt.Errorf("%w: fields must be a non-empty object", ErrBadData)
		}
		return map[string]any{}, nil
	}
	fields, ok := raw.(map[string]any)
	if !ok || (required && len(fields) == 0) {
		return nil, fmt.Errorf("%w: fields must be a non-empty object", ErrBadData)
	}
	return fields, nil
}

func crmBecause(body map[string]any) (string, error) {
	raw, ok := body["because"]
	if !ok || raw == nil {
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%w: because must be a string", ErrBadData)
	}
	return text, nil
}

func rejectUnsupportedIdentities(body map[string]any) error {
	raw, ok := body["identities"]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case map[string]any:
		if len(value) == 0 {
			return nil
		}
	case []any:
		if len(value) == 0 {
			return nil
		}
	}
	return fmt.Errorf("%w: identities is not supported by this private deployment (shape UNKNOWN)", ErrBadData)
}

func crmAuditDetail(because string, fieldCount int) string {
	detail := fmt.Sprintf("fields=%d", fieldCount)
	if because != "" {
		detail += " because=" + because
	}
	return detail
}
