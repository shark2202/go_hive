package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// GridError is a client-correctable grid query problem. Handlers surface it as
// HTTP 400 with {"error": message}, matching the client's error path.
type GridError struct {
	Message string
}

func (e *GridError) Error() string { return e.Message }
func (e *GridError) Unwrap() error { return ErrBadData }

func gridErr(format string, args ...any) error {
	return &GridError{Message: fmt.Sprintf(format, args...)}
}

// GridQueryOptions is the filter/search/sort/pagination body shared by the
// global grid and the CRM grid. It mirrors framework/global_db/grid_query.py
// (and the Go port in go-hive/internal/globaldb/grid_query.go) at the level the
// desktop proxy exposes:
//
//	{filter?: [{column, op, value}], search?, order_by?, order_dir?,
//	 limit?, offset?}
//
// Unlike grid_query.py this builder never interpolates literals into SQL: every
// user value travels as a bound parameter (?) so the SQLite driver keeps it out
// of statement parsing. Identifiers still have to be validated (they cannot be
// bound as parameters), which is the whitelist boundary below.
type GridQueryOptions struct {
	Filters  []map[string]any
	Search   any
	OrderBy  string
	OrderDir string
	Limit    int
	Offset   int
}

// GridQueryValues carries the bound parameters in statement order.
type GridQueryValues []any

// comparisonOps mirror grid_query.py::_COMPARISON.
var gridComparisonOps = map[string]string{
	"eq":  "=",
	"ne":  "<>",
	"gt":  ">",
	"gte": ">=",
	"lt":  "<",
	"lte": "<=",
}

// gridLikeOps mirror grid_query.py::_LIKE: (prefix, suffix).
var gridLikeOps = map[string][2]string{
	"contains":    {"%", "%"},
	"starts_with": {"", "%"},
	"ends_with":   {"%", ""},
}

// ParseGridQueryOptions validates the request body. The error strings match the
// desktop runtime's documented failures where one exists ("filter must be a
// list", "invalid limit/offset"); the identifier errors come from the builder.
func ParseGridQueryOptions(body map[string]any) (GridQueryOptions, error) {
	opts := GridQueryOptions{OrderDir: "asc", Limit: 100, Offset: 0}
	if body == nil {
		return opts, nil
	}
	if raw, ok := body["filter"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return opts, gridErr("filter must be a list")
		}
		opts.Filters = make([]map[string]any, 0, len(list))
		for _, item := range list {
			object, ok := item.(map[string]any)
			if !ok {
				return opts, gridErr("each filter must be an object")
			}
			opts.Filters = append(opts.Filters, object)
		}
	}
	if raw, ok := body["search"]; ok && raw != nil {
		switch raw.(type) {
		case string, json.Number, float64, bool:
			opts.Search = raw
		default:
			return opts, gridErr("search must be a scalar")
		}
	}
	if raw, ok := body["order_by"]; ok && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return opts, gridErr("order_by must be a string")
		}
		opts.OrderBy = text
	}
	if raw, ok := body["order_dir"]; ok && raw != nil {
		text, ok := raw.(string)
		if !ok {
			return opts, gridErr("order_dir must be a string")
		}
		opts.OrderDir = text
	}
	if raw, ok := body["limit"]; ok && raw != nil {
		n, err := gridInt(raw)
		if err != nil {
			return opts, gridErr("invalid limit/offset")
		}
		opts.Limit = n
	}
	if raw, ok := body["offset"]; ok && raw != nil {
		n, err := gridInt(raw)
		if err != nil {
			return opts, gridErr("invalid limit/offset")
		}
		opts.Offset = n
	}
	opts.Limit = ClampGridLimit(opts.Limit)
	opts.Offset = ClampGridOffset(opts.Offset)
	return opts, nil
}

func gridInt(value any) (int, error) {
	switch v := value.(type) {
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return int(i), nil
		}
		f, err := v.Float64()
		if err != nil {
			return 0, err
		}
		return int(f), nil
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, err
		}
		return i, nil
	default:
		return 0, fmt.Errorf("not a number")
	}
}

// ClampGridLimit mirrors max(1, min(500, n)) from the runtime's grid query.
func ClampGridLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 500 {
		return 500
	}
	return limit
}

// ClampGridOffset mirrors max(0, n).
func ClampGridOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// BuildGridSelect builds the parameterised SELECT for one grid page.
// allowed is the entity/table column set; columns outside it are rejected
// rather than silently dropped. excludeArchived adds the private-deploy
// soft-delete filter when the table carries archived_at.
func BuildGridSelect(table string, allowed map[string]bool, opts GridQueryOptions, excludeArchived bool) (string, GridQueryValues, error) {
	if err := ValidateTableName(table); err != nil {
		return "", nil, err
	}
	where, args, err := BuildGridWhere(allowed, opts.Filters, opts.Search, excludeArchived)
	if err != nil {
		return "", nil, err
	}
	statement := "SELECT * FROM " + quoteIdent(table) + where
	if opts.OrderBy != "" {
		column, err := gridColumn(opts.OrderBy, allowed)
		if err != nil {
			return "", nil, err
		}
		direction := "ASC"
		if strings.EqualFold(opts.OrderDir, "desc") {
			direction = "DESC"
		}
		statement += " ORDER BY " + column + " " + direction
	}
	statement += " LIMIT ? OFFSET ?"
	args = append(args, int64(ClampGridLimit(opts.Limit)), int64(ClampGridOffset(opts.Offset)))
	return statement, args, nil
}

// BuildGridCount builds the parameterised total for the same filters/search.
func BuildGridCount(table string, allowed map[string]bool, opts GridQueryOptions, excludeArchived bool) (string, GridQueryValues, error) {
	if err := ValidateTableName(table); err != nil {
		return "", nil, err
	}
	where, args, err := BuildGridWhere(allowed, opts.Filters, opts.Search, excludeArchived)
	if err != nil {
		return "", nil, err
	}
	return "SELECT count(*) AS total FROM " + quoteIdent(table) + where, args, nil
}

// BuildGridGroupCounts builds the grouped-count query used by board columns.
// The group column is user-chosen, so it is validated against the same column
// set as filters and sort.
func BuildGridGroupCounts(table string, allowed map[string]bool, groupBy string, opts GridQueryOptions, excludeArchived bool, limit int) (string, GridQueryValues, error) {
	if err := ValidateTableName(table); err != nil {
		return "", nil, err
	}
	column, err := gridColumn(groupBy, allowed)
	if err != nil {
		return "", nil, err
	}
	where, args, err := BuildGridWhere(allowed, opts.Filters, opts.Search, excludeArchived)
	if err != nil {
		return "", nil, err
	}
	statement := "SELECT " + column + " AS value, count(*) AS count FROM " + quoteIdent(table) + where +
		" GROUP BY " + column + " ORDER BY count(*) DESC, " + column + " ASC LIMIT ?"
	args = append(args, int64(ClampGridLimit(limit)))
	return statement, args, nil
}

// BuildGridWhere returns the WHERE clause (with the keyword) and its bound
// values. Filters are AND-joined; search is OR-joined across every allowed
// column, matching grid_query.build_filter_clause/build_search_clause.
func BuildGridWhere(allowed map[string]bool, filters []map[string]any, search any, excludeArchived bool) (string, GridQueryValues, error) {
	clauses := make([]string, 0, len(filters)+2)
	args := make(GridQueryValues, 0, len(filters)+1)
	if excludeArchived && allowed["archived_at"] {
		clauses = append(clauses, quoteIdent("archived_at")+" IS NULL")
	}
	for _, filter := range filters {
		clause, values, err := buildGridCondition(allowed, filter)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		args = append(args, values...)
	}
	searchClause, searchArgs, err := buildGridSearch(allowed, search)
	if err != nil {
		return "", nil, err
	}
	if searchClause != "" {
		clauses = append(clauses, searchClause)
		args = append(args, searchArgs...)
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

func buildGridCondition(allowed map[string]bool, filter map[string]any) (string, GridQueryValues, error) {
	if filter == nil {
		return "", nil, gridErr("each filter must be an object")
	}
	rawColumn, ok := filter["column"]
	if !ok {
		return "", nil, gridErr("invalid identifier: None")
	}
	column, ok := rawColumn.(string)
	if !ok {
		return "", nil, gridErr("invalid identifier: %s", gridRepr(rawColumn))
	}
	ident, err := gridColumn(column, allowed)
	if err != nil {
		return "", nil, err
	}
	op := "eq"
	if raw, ok := filter["op"]; ok && raw != nil {
		op = gridText(raw)
	}

	switch op {
	case "is_empty":
		return "(" + ident + " IS NULL OR CAST(" + ident + " AS TEXT) = '')", nil, nil
	case "is_not_empty":
		return "(" + ident + " IS NOT NULL AND CAST(" + ident + " AS TEXT) <> '')", nil, nil
	}
	if operator, ok := gridComparisonOps[op]; ok {
		value, err := BindValue(filter["value"])
		if err != nil {
			return "", nil, err
		}
		return ident + " " + operator + " ?", GridQueryValues{value}, nil
	}
	if wrap, ok := gridLikeOps[op]; ok {
		pattern := wrap[0] + gridLikeEscape(filter["value"]) + wrap[1]
		return "CAST(" + ident + " AS TEXT) LIKE ? ESCAPE '\\'", GridQueryValues{pattern}, nil
	}
	return "", nil, gridErr("unsupported filter op: '%s'", op)
}

func buildGridSearch(allowed map[string]bool, search any) (string, GridQueryValues, error) {
	if search == nil {
		return "", nil, nil
	}
	text := gridText(search)
	if strings.TrimSpace(text) == "" {
		return "", nil, nil
	}
	columns := make([]string, 0, len(allowed))
	for name := range allowed {
		if name == "archived_at" {
			continue
		}
		columns = append(columns, name)
	}
	sort.Strings(columns)
	if len(columns) == 0 {
		return "", nil, nil
	}
	pattern := "%" + gridLikeEscape(search) + "%"
	clauses := make([]string, 0, len(columns))
	args := make(GridQueryValues, 0, len(columns))
	for _, name := range columns {
		clauses = append(clauses, "CAST("+quoteIdent(name)+" AS TEXT) LIKE ? ESCAPE '\\'")
		args = append(args, pattern)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, nil
}

// gridColumn validates an identifier against the table's real column set.
func gridColumn(name string, allowed map[string]bool) (string, error) {
	if !gridIdentifier(name) {
		return "", gridErr("invalid identifier: '%s'", strings.ReplaceAll(name, "'", "\\'"))
	}
	if !allowed[name] {
		return "", gridErr("unknown column: %s", name)
	}
	return quoteIdent(name), nil
}

func gridIdentifier(name string) bool {
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
	return first == '_' || (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
}

// gridLikeEscape makes LIKE metacharacters match literally. SQLite's LIKE is
// ASCII-case-insensitive by default, which is the closest local equivalent of
// Postgres ILIKE for the identifiers this schema uses.
func gridLikeEscape(value any) string {
	text := strings.ReplaceAll(gridText(value), "\x00", "")
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, "%", `\%`)
	text = strings.ReplaceAll(text, "_", `\_`)
	return text
}

// gridText mirrors Python str() for the JSON scalar types grid filters carry.
func gridText(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprint(v)
	}
}

// gridRepr mirrors the repr() shape used by grid_query.py error messages.
func gridRepr(value any) string {
	if text, ok := value.(string); ok {
		return "'" + strings.ReplaceAll(strings.ReplaceAll(text, `\`, `\\`), "'", `\'`) + "'"
	}
	return gridText(value)
}
