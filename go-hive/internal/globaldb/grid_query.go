package globaldb

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type SqlBuildError struct {
	Message string
}

func (e *SqlBuildError) Error() string { return e.Message }

func sqlErr(format string, args ...any) error {
	return &SqlBuildError{Message: fmt.Sprintf(format, args...)}
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var comparisonOps = map[string]string{
	"eq":  "=",
	"ne":  "<>",
	"gt":  ">",
	"gte": ">=",
	"lt":  "<",
	"lte": "<=",
}

var likeOps = map[string][2]string{
	"contains":    {"%", "%"},
	"starts_with": {"", "%"},
	"ends_with":   {"%", ""},
}

var nullaryOps = map[string]struct{}{
	"is_empty":     {},
	"is_not_empty": {},
}

type ColumnSet map[string]struct{}

func NewColumnSet(names ...string) ColumnSet {
	set := make(ColumnSet, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

func QuoteIdent(name string, allowed ColumnSet) (string, error) {
	if !identRe.MatchString(name) {
		return "", sqlErr("invalid identifier: %s", pythonRepr(name))
	}
	if allowed != nil {
		if _, ok := allowed[name]; !ok {
			return "", sqlErr("unknown column: %s", name)
		}
	}
	return `"` + name + `"`, nil
}

func SQLLiteral(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "TRUE", nil
		}
		return "FALSE", nil
	case string:
		return quoteText(v), nil
	case float32:
		return formatFloat(float64(v))
	case float64:
		return formatFloat(v)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return formatInt(v), nil
	default:
		return quoteText(pythonStr(value)), nil
	}
}

func formatFloat(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", sqlErr("non-finite numeric literal")
	}
	return pythonFloatRepr(v), nil
}

func formatInt(v any) string {
	switch n := v.(type) {
	case int:
		return strconv.Itoa(n)
	case int8:
		return strconv.FormatInt(int64(n), 10)
	case int16:
		return strconv.FormatInt(int64(n), 10)
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case uint:
		return strconv.FormatUint(uint64(n), 10)
	case uint8:
		return strconv.FormatUint(uint64(n), 10)
	case uint16:
		return strconv.FormatUint(uint64(n), 10)
	case uint32:
		return strconv.FormatUint(uint64(n), 10)
	case uint64:
		return strconv.FormatUint(n, 10)
	default:
		return fmt.Sprint(v)
	}
}

func quoteText(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func likeEscape(value any) string {
	s := strings.ReplaceAll(pythonStr(value), "\x00", "")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

func asText(ident string) string { return "CAST(" + ident + " AS TEXT)" }

func condition(f map[string]any, allowed ColumnSet) (string, error) {
	if f == nil {
		return "", sqlErr("each filter must be an object")
	}
	col, ok := f["column"].(string)
	if !ok {
		return "", sqlErr("invalid identifier: %s", pythonRepr(f["column"]))
	}
	ident, err := QuoteIdent(col, allowed)
	if err != nil {
		return "", err
	}
	op := "eq"
	if raw, ok := f["op"]; ok {
		op = pythonStr(raw)
	}

	if _, ok := nullaryOps[op]; ok {
		if op == "is_empty" {
			return "(" + ident + " IS NULL OR " + asText(ident) + " = '')", nil
		}
		return "(" + ident + " IS NOT NULL AND " + asText(ident) + " <> '')", nil
	}
	if sqlOp, ok := comparisonOps[op]; ok {
		lit, err := SQLLiteral(f["value"])
		if err != nil {
			return "", err
		}
		return ident + " " + sqlOp + " " + lit, nil
	}
	if wrap, ok := likeOps[op]; ok {
		pattern := wrap[0] + likeEscape(f["value"]) + wrap[1]
		lit, err := SQLLiteral(pattern)
		if err != nil {
			return "", err
		}
		return asText(ident) + " ILIKE " + lit + ` ESCAPE '\'`, nil
	}
	return "", sqlErr("unsupported filter op: %s", pythonRepr(op))
}

func BuildFilterClause(filters []map[string]any, allowed ColumnSet) (string, error) {
	if len(filters) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(filters))
	for _, f := range filters {
		part, err := condition(f, allowed)
		if err != nil {
			return "", err
		}
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " AND "), nil
}

func BuildSearchClause(search any, allowed ColumnSet) (string, error) {
	if search == nil {
		return "", nil
	}
	text := pythonStr(search)
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	pattern := "%" + likeEscape(search) + "%"
	lit, err := SQLLiteral(pattern)
	if err != nil {
		return "", err
	}
	cols := sortedColumns(allowed)
	if len(cols) == 0 {
		return "", nil
	}
	ors := make([]string, 0, len(cols))
	for _, c := range cols {
		ident, _ := QuoteIdent(c, nil)
		ors = append(ors, asText(ident)+" ILIKE "+lit+` ESCAPE '\'`)
	}
	return "(" + strings.Join(ors, " OR ") + ")", nil
}

func sortedColumns(allowed ColumnSet) []string {
	cols := make([]string, 0, len(allowed))
	for c := range allowed {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols
}

func combineWhere(clauses ...string) string {
	active := make([]string, 0, len(clauses))
	for _, c := range clauses {
		if c != "" {
			active = append(active, c)
		}
	}
	if len(active) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(active, " AND ")
}

type SelectOptions struct {
	Filters  []map[string]any
	Search   any
	OrderBy  string
	OrderDir string
	Limit    int
	Offset   int
}

func BuildSelect(table string, allowed ColumnSet, opts SelectOptions) (string, error) {
	t, err := QuoteIdent(table, nil)
	if err != nil {
		return "", err
	}
	filters, err := BuildFilterClause(opts.Filters, allowed)
	if err != nil {
		return "", err
	}
	search, err := BuildSearchClause(opts.Search, allowed)
	if err != nil {
		return "", err
	}
	sql := "SELECT * FROM " + t + combineWhere(filters, search)
	if opts.OrderBy != "" {
		direction := "ASC"
		if strings.ToLower(opts.OrderDir) == "desc" {
			direction = "DESC"
		}
		col, err := QuoteIdent(opts.OrderBy, allowed)
		if err != nil {
			return "", err
		}
		sql += " ORDER BY " + col + " " + direction
	}
	sql += " LIMIT " + strconv.Itoa(opts.Limit) + " OFFSET " + strconv.Itoa(opts.Offset)
	return sql, nil
}

func BuildCount(table string, allowed ColumnSet, filters []map[string]any, search any) (string, error) {
	t, err := QuoteIdent(table, nil)
	if err != nil {
		return "", err
	}
	f, err := BuildFilterClause(filters, allowed)
	if err != nil {
		return "", err
	}
	s, err := BuildSearchClause(search, allowed)
	if err != nil {
		return "", err
	}
	return "SELECT count(*) AS total FROM " + t + combineWhere(f, s), nil
}

type GroupCountsOptions struct {
	GroupBy string
	Filters []map[string]any
	Search  any
	Limit   *int
}

func BuildGroupCounts(table string, allowed ColumnSet, opts GroupCountsOptions) (string, error) {
	t, err := QuoteIdent(table, nil)
	if err != nil {
		return "", err
	}
	col, err := QuoteIdent(opts.GroupBy, allowed)
	if err != nil {
		return "", err
	}
	f, err := BuildFilterClause(opts.Filters, allowed)
	if err != nil {
		return "", err
	}
	s, err := BuildSearchClause(opts.Search, allowed)
	if err != nil {
		return "", err
	}
	sql := "SELECT " + col + " AS value, count(*) AS count FROM " + t + combineWhere(f, s) +
		" GROUP BY " + col + " ORDER BY count(*) DESC, " + col + " ASC"
	if opts.Limit != nil {
		sql += " LIMIT " + strconv.Itoa(*opts.Limit)
	}
	return sql, nil
}

func BuildDelete(table string, pk map[string]any, pkCols []string) (string, error) {
	if len(pkCols) == 0 {
		return "", sqlErr("table has no primary key")
	}
	t, err := QuoteIdent(table, nil)
	if err != nil {
		return "", err
	}
	conds := make([]string, 0, len(pkCols))
	for _, c := range pkCols {
		ident, err := QuoteIdent(c, nil)
		if err != nil {
			return "", err
		}
		lit, err := SQLLiteral(pk[c])
		if err != nil {
			return "", err
		}
		conds = append(conds, ident+" = "+lit)
	}
	return "DELETE FROM " + t + " WHERE " + strings.Join(conds, " AND "), nil
}

func pythonStr(value any) string {
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
	case float32:
		return pythonFloatRepr(float64(v))
	case float64:
		return pythonFloatRepr(v)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return formatInt(v)
	default:
		return fmt.Sprint(value)
	}
}

func pythonRepr(value any) string {
	if s, ok := value.(string); ok {
		if !strings.Contains(s, "'") {
			return "'" + s + "'"
		}
		if !strings.Contains(s, `"`) {
			return `"` + s + `"`
		}
		return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
	}
	return pythonStr(value)
}

func pythonFloatRepr(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eEnN") {
		s += ".0"
	}
	return s
}
