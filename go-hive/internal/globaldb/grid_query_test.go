package globaldb

import (
	"math"
	"strings"
	"testing"
)

var gridCols = NewColumnSet("name", "status", "amount", "email")

func expectSQLBuildError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected SqlBuildError, got nil")
	}
	if _, ok := err.(*SqlBuildError); !ok {
		t.Fatalf("error type = %T, want *SqlBuildError", err)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_quote_ident_allows_known_column
func TestQuoteIdentAllowsKnownColumn(t *testing.T) {
	got, err := QuoteIdent("status", gridCols)
	if err != nil {
		t.Fatalf("QuoteIdent: %v", err)
	}
	if got != `"status"` {
		t.Fatalf("QuoteIdent = %q, want %q", got, `"status"`)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_quote_ident_rejects_malicious
func TestQuoteIdentRejectsMalicious(t *testing.T) {
	bad := []string{
		"status; DROP TABLE leads",
		`name"`,
		"amount OR 1=1",
		"",
		"1col",
		"col-name",
	}
	for _, name := range bad {
		_, err := QuoteIdent(name, nil)
		expectSQLBuildError(t, err)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_quote_ident_rejects_unknown_column
func TestQuoteIdentRejectsUnknownColumn(t *testing.T) {
	_, err := QuoteIdent("secret_col", gridCols)
	expectSQLBuildError(t, err)
}

// Parity: framework/global_db/tests/test_grid_query.py::test_sql_literal_escapes_quote_breakout
func TestSQLLiteralEscapesQuoteBreakout(t *testing.T) {
	got, err := SQLLiteral("a' OR '1'='1")
	if err != nil {
		t.Fatalf("SQLLiteral: %v", err)
	}
	want := "'a'' OR ''1''=''1'"
	if got != want {
		t.Fatalf("SQLLiteral = %q, want %q", got, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_sql_literal_types
func TestSQLLiteralTypes(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{nil, "NULL"},
		{true, "TRUE"},
		{false, "FALSE"},
		{42, "42"},
		{3.5, "3.5"},
	}
	for _, tc := range cases {
		got, err := SQLLiteral(tc.value)
		if err != nil {
			t.Fatalf("SQLLiteral(%v): %v", tc.value, err)
		}
		if got != tc.want {
			t.Fatalf("SQLLiteral(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_sql_literal_rejects_non_finite
func TestSQLLiteralRejectsNonFinite(t *testing.T) {
	if _, err := SQLLiteral(math.Inf(1)); err == nil {
		t.Fatal("SQLLiteral(inf) = nil, want error")
	} else {
		expectSQLBuildError(t, err)
	}
	if _, err := SQLLiteral(math.NaN()); err == nil {
		t.Fatal("SQLLiteral(nan) = nil, want error")
	} else {
		expectSQLBuildError(t, err)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_comparison_filter
func TestComparisonFilter(t *testing.T) {
	clause, err := BuildFilterClause([]map[string]any{{"column": "amount", "op": "gte", "value": 100}}, gridCols)
	if err != nil {
		t.Fatalf("BuildFilterClause: %v", err)
	}
	if want := `"amount" >= 100`; clause != want {
		t.Fatalf("clause = %q, want %q", clause, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_contains_filter_escapes_like_metachars
func TestContainsFilterEscapesLikeMetachars(t *testing.T) {
	clause, err := BuildFilterClause([]map[string]any{{"column": "name", "op": "contains", "value": "50%_off"}}, gridCols)
	if err != nil {
		t.Fatalf("BuildFilterClause: %v", err)
	}
	want := `CAST("name" AS TEXT) ILIKE '%50\%\_off%' ESCAPE '\'`
	if clause != want {
		t.Fatalf("clause = %q, want %q", clause, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_is_empty_filter
func TestIsEmptyFilter(t *testing.T) {
	clause, err := BuildFilterClause([]map[string]any{{"column": "email", "op": "is_empty"}}, gridCols)
	if err != nil {
		t.Fatalf("BuildFilterClause: %v", err)
	}
	want := `("email" IS NULL OR CAST("email" AS TEXT) = '')`
	if clause != want {
		t.Fatalf("clause = %q, want %q", clause, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_unknown_op_rejected
func TestUnknownOpRejected(t *testing.T) {
	_, err := BuildFilterClause([]map[string]any{{"column": "name", "op": "haxx", "value": 1}}, gridCols)
	expectSQLBuildError(t, err)
}

// Parity: framework/global_db/tests/test_grid_query.py::test_filter_on_unknown_column_rejected
func TestFilterOnUnknownColumnRejected(t *testing.T) {
	_, err := BuildFilterClause([]map[string]any{{"column": "evil", "op": "eq", "value": 1}}, gridCols)
	expectSQLBuildError(t, err)
}

// Parity: framework/global_db/tests/test_grid_query.py::test_multiple_filters_anded
func TestMultipleFiltersAnded(t *testing.T) {
	clause, err := BuildFilterClause([]map[string]any{
		{"column": "status", "op": "eq", "value": "won"},
		{"column": "amount", "op": "gt", "value": 0},
	}, gridCols)
	if err != nil {
		t.Fatalf("BuildFilterClause: %v", err)
	}
	want := `"status" = 'won' AND "amount" > 0`
	if clause != want {
		t.Fatalf("clause = %q, want %q", clause, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_search_spans_all_columns
func TestSearchSpansAllColumns(t *testing.T) {
	clause, err := BuildSearchClause("acme", gridCols)
	if err != nil {
		t.Fatalf("BuildSearchClause: %v", err)
	}
	for _, c := range []string{"name", "status", "amount", "email"} {
		if !strings.Contains(clause, `CAST("`+c+`" AS TEXT) ILIKE`) {
			t.Fatalf("clause %q missing column %q", clause, c)
		}
	}
	if !strings.HasPrefix(clause, "(") || !strings.HasSuffix(clause, ")") {
		t.Fatalf("clause not parenthesised: %q", clause)
	}
	if !strings.Contains(clause, " OR ") {
		t.Fatalf("clause has no OR: %q", clause)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_blank_search_is_empty
func TestBlankSearchIsEmpty(t *testing.T) {
	clause, err := BuildSearchClause("   ", gridCols)
	if err != nil {
		t.Fatalf("BuildSearchClause(\"   \"): %v", err)
	}
	if clause != "" {
		t.Fatalf("blank search = %q, want empty", clause)
	}
	clause, err = BuildSearchClause(nil, gridCols)
	if err != nil {
		t.Fatalf("BuildSearchClause(nil): %v", err)
	}
	if clause != "" {
		t.Fatalf("nil search = %q, want empty", clause)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_select_full
func TestBuildSelectFull(t *testing.T) {
	sql, err := BuildSelect("leads", gridCols, SelectOptions{
		Filters:  []map[string]any{{"column": "status", "op": "eq", "value": "new"}},
		OrderBy:  "amount",
		OrderDir: "desc",
		Limit:    50,
		Offset:   100,
	})
	if err != nil {
		t.Fatalf("BuildSelect: %v", err)
	}
	want := `SELECT * FROM "leads" WHERE "status" = 'new' ORDER BY "amount" DESC LIMIT 50 OFFSET 100`
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_select_no_filters
func TestBuildSelectNoFilters(t *testing.T) {
	sql, err := BuildSelect("leads", gridCols, SelectOptions{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("BuildSelect: %v", err)
	}
	if want := `SELECT * FROM "leads" LIMIT 10 OFFSET 0`; sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_select_clamps_order_dir_to_known_keyword
func TestBuildSelectClampsOrderDirToKnownKeyword(t *testing.T) {
	sql, err := BuildSelect("leads", gridCols, SelectOptions{OrderBy: "name", OrderDir: "; DROP", Limit: 100, Offset: 0})
	if err != nil {
		t.Fatalf("BuildSelect: %v", err)
	}
	if want := `ORDER BY "name" ASC LIMIT 100 OFFSET 0`; !strings.HasSuffix(sql, want) {
		t.Fatalf("sql = %q, want suffix %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_count_with_search
func TestBuildCountWithSearch(t *testing.T) {
	sql, err := BuildCount("leads", gridCols, nil, "x")
	if err != nil {
		t.Fatalf("BuildCount: %v", err)
	}
	if want := `SELECT count(*) AS total FROM "leads" WHERE (`; !strings.HasPrefix(sql, want) {
		t.Fatalf("sql = %q, want prefix %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_group_counts_basic
func TestBuildGroupCountsBasic(t *testing.T) {
	sql, err := BuildGroupCounts("leads", gridCols, GroupCountsOptions{GroupBy: "status"})
	if err != nil {
		t.Fatalf("BuildGroupCounts: %v", err)
	}
	want := `SELECT "status" AS value, count(*) AS count FROM "leads" GROUP BY "status" ORDER BY count(*) DESC, "status" ASC`
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_group_counts_with_filters_and_limit
func TestBuildGroupCountsWithFiltersAndLimit(t *testing.T) {
	limit := 200
	sql, err := BuildGroupCounts("leads", gridCols, GroupCountsOptions{
		GroupBy: "status",
		Filters: []map[string]any{{"column": "email", "op": "contains", "value": "rick"}},
		Limit:   &limit,
	})
	if err != nil {
		t.Fatalf("BuildGroupCounts: %v", err)
	}
	if want := `SELECT "status" AS value, count(*) AS count FROM "leads" WHERE `; !strings.HasPrefix(sql, want) {
		t.Fatalf("sql = %q, want prefix %q", sql, want)
	}
	if !strings.Contains(sql, `CAST("email" AS TEXT) ILIKE`) {
		t.Fatalf("sql %q missing email filter", sql)
	}
	if want := `GROUP BY "status" ORDER BY count(*) DESC, "status" ASC LIMIT 200`; !strings.HasSuffix(sql, want) {
		t.Fatalf("sql = %q, want suffix %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_group_counts_rejects_unknown_column
func TestBuildGroupCountsRejectsUnknownColumn(t *testing.T) {
	_, err := BuildGroupCounts("leads", gridCols, GroupCountsOptions{GroupBy: "status; DROP TABLE leads"})
	expectSQLBuildError(t, err)
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_delete
func TestBuildDelete(t *testing.T) {
	sql, err := BuildDelete("leads", map[string]any{"lead_id": "abc'123"}, []string{"lead_id"})
	if err != nil {
		t.Fatalf("BuildDelete: %v", err)
	}
	want := `DELETE FROM "leads" WHERE "lead_id" = 'abc''123'`
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

// Parity: framework/global_db/tests/test_grid_query.py::test_build_delete_composite_pk
func TestBuildDeleteCompositePK(t *testing.T) {
	sql, err := BuildDelete("m2m", map[string]any{"a": 1, "b": 2}, []string{"a", "b"})
	if err != nil {
		t.Fatalf("BuildDelete: %v", err)
	}
	want := `DELETE FROM "m2m" WHERE "a" = 1 AND "b" = 2`
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}
