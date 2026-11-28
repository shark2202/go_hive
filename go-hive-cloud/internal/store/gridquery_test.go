package store

import (
	"strings"
	"testing"
)

func TestBuildGridSelectUsesBoundParameters(t *testing.T) {
	allowed := map[string]bool{"status": true, "amount": true, "name": true}
	statement, args, err := BuildGridSelect("leads", allowed, GridQueryOptions{
		Filters:  []map[string]any{{"column": "status", "op": "eq", "value": "won' OR '1'='1"}},
		OrderBy:  "amount",
		OrderDir: "desc",
		Limit:    50,
		Offset:   100,
	}, false)
	if err != nil {
		t.Fatalf("BuildGridSelect: %v", err)
	}
	if strings.Contains(statement, "won") {
		t.Fatalf("statement interpolates a literal: %s", statement)
	}
	if !strings.Contains(statement, `"status" = ?`) {
		t.Fatalf("filter is not parameterised: %s", statement)
	}
	if !strings.HasSuffix(statement, "ORDER BY \"amount\" DESC LIMIT ? OFFSET ?") {
		t.Fatalf("statement = %s", statement)
	}
	if len(args) != 3 || args[0] != "won' OR '1'='1" {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildGridWhereComparisonAndNullary(t *testing.T) {
	allowed := map[string]bool{"name": true, "email": true, "amount": true}
	where, args, err := BuildGridWhere(allowed, []map[string]any{
		{"column": "amount", "op": "gte", "value": 100},
		{"column": "email", "op": "is_empty"},
	}, nil, false)
	if err != nil {
		t.Fatalf("BuildGridWhere: %v", err)
	}
	want := ` WHERE "amount" >= ? AND ("email" IS NULL OR CAST("email" AS TEXT) = '')`
	if where != want {
		t.Fatalf("where = %q, want %q", where, want)
	}
	if len(args) != 1 || args[0] != int64(100) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildGridWhereLikeEscapesMetacharacters(t *testing.T) {
	allowed := map[string]bool{"name": true}
	_, args, err := BuildGridWhere(allowed, []map[string]any{
		{"column": "name", "op": "contains", "value": `50%_off\`},
	}, nil, false)
	if err != nil {
		t.Fatalf("BuildGridWhere: %v", err)
	}
	if len(args) != 1 {
		t.Fatalf("args = %#v", args)
	}
	if args[0] != `%50\%\_off\\%` {
		t.Fatalf("pattern = %q", args[0])
	}
}

func TestBuildGridWhereSearchExcludesArchivedAndRejectsUnknownColumn(t *testing.T) {
	allowed := map[string]bool{"name": true, "email": true, "archived_at": true}
	where, args, err := BuildGridWhere(allowed, nil, "acme", true)
	if err != nil {
		t.Fatalf("BuildGridWhere: %v", err)
	}
	if !strings.HasPrefix(where, ` WHERE "archived_at" IS NULL AND (`) {
		t.Fatalf("where = %q", where)
	}
	if strings.Contains(where, `"archived_at" AS TEXT`) {
		t.Fatalf("search spans the archived marker: %q", where)
	}
	if len(args) != 2 {
		t.Fatalf("search args = %#v", args)
	}

	_, _, err = BuildGridWhere(allowed, []map[string]any{
		{"column": "name; DROP TABLE leads", "op": "eq", "value": 1},
	}, nil, false)
	if err == nil {
		t.Fatal("expected an invalid-identifier error")
	}
	if _, ok := err.(*GridError); !ok {
		t.Fatalf("error type = %T, want *GridError", err)
	}
}

func TestBuildGridWhereUnsupportedOp(t *testing.T) {
	_, _, err := BuildGridWhere(map[string]bool{"name": true}, []map[string]any{
		{"column": "name", "op": "haxx", "value": 1},
	}, nil, false)
	if err == nil || !strings.Contains(err.Error(), "unsupported filter op: 'haxx'") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildGridGroupCountsParameterised(t *testing.T) {
	allowed := map[string]bool{"stage": true, "email": true}
	statement, args, err := BuildGridGroupCounts("crm_people", allowed, "stage", GridQueryOptions{
		Filters: []map[string]any{{"column": "email", "op": "contains", "value": "rick"}},
	}, true, 200)
	if err != nil {
		t.Fatalf("BuildGridGroupCounts: %v", err)
	}
	if !strings.HasPrefix(statement, `SELECT "stage" AS value, count(*) AS count FROM "crm_people" WHERE `) {
		t.Fatalf("statement = %s", statement)
	}
	if !strings.HasSuffix(statement, `GROUP BY "stage" ORDER BY count(*) DESC, "stage" ASC LIMIT ?`) {
		t.Fatalf("statement = %s", statement)
	}
	if len(args) != 2 || args[1] != int64(200) {
		t.Fatalf("args = %#v", args)
	}
}

func TestParseGridQueryOptionsDefaultsAndClamps(t *testing.T) {
	opts, err := ParseGridQueryOptions(map[string]any{})
	if err != nil {
		t.Fatalf("ParseGridQueryOptions: %v", err)
	}
	if opts.Limit != 100 || opts.Offset != 0 || opts.OrderDir != "asc" {
		t.Fatalf("defaults = %#v", opts)
	}

	opts, err = ParseGridQueryOptions(map[string]any{"limit": 9999, "offset": -5})
	if err != nil {
		t.Fatalf("ParseGridQueryOptions: %v", err)
	}
	if opts.Limit != 500 || opts.Offset != 0 {
		t.Fatalf("clamped = %#v", opts)
	}

	for _, body := range []map[string]any{
		{"filter": "nope"},
		{"filter": []any{"nope"}},
		{"limit": "abc"},
		{"order_by": 5},
		{"search": map[string]any{"x": 1}},
	} {
		if _, err := ParseGridQueryOptions(body); err == nil {
			t.Fatalf("ParseGridQueryOptions(%v) = nil error", body)
		}
	}
}

func TestGridColumnRejectsInternal(t *testing.T) {
	// Columns come from PRAGMA introspection; identifiers starting with "_"
	// are never part of the CRM grid's allowed set.
	allowed := map[string]bool{"name": true}
	if _, _, err := BuildGridWhere(allowed, []map[string]any{{"column": "_cloud_audit", "op": "eq", "value": 1}}, nil, false); err == nil {
		t.Fatal("expected unknown column error")
	}
}
