package store

import (
	"errors"
	"testing"
)

func TestCheckStatementAllows(t *testing.T) {
	cases := []string{
		"SELECT 1",
		"SELECT * FROM leads WHERE name = 'x'",
		"CREATE TABLE leads (lead_id TEXT PRIMARY KEY, name TEXT)",
		"INSERT INTO leads (lead_id) VALUES ('l1')",
		"UPDATE leads SET name = 'x' WHERE lead_id = 'l1'",
		"DELETE FROM leads WHERE lead_id = 'l1'",
		"SELECT 'ATTACH PRAGMA; VACUUM' AS note",
		"SELECT 1 -- PRAGMA in a comment\n",
		"SELECT 1 /* DETACH */ ;",
		"WITH x AS (SELECT 1) SELECT * FROM x",
	}
	for _, sqlText := range cases {
		if err := CheckStatement(sqlText, false); err != nil {
			t.Errorf("CheckStatement(%q) = %v, want nil", sqlText, err)
		}
	}
}

func TestCheckStatementRejects(t *testing.T) {
	cases := []struct {
		sqlText   string
		wantCode  string
		wantMatch string
	}{
		{"", GateEmpty, "empty statement"},
		{"   ", GateEmpty, "empty statement"},
		{"SELECT 1; SELECT 2", GateMultiple, "single SQL statement"},
		{"ATTACH DATABASE 'x' AS y", GateForbidden, "ATTACH is not allowed"},
		{"DETACH y", GateForbidden, "DETACH is not allowed"},
		{"PRAGMA table_info(x)", GateForbidden, "PRAGMA is not allowed"},
		{"VACUUM", GateForbidden, "VACUUM is not allowed"},
		{"REINDEX x", GateForbidden, "REINDEX is not allowed"},
		{"SELECT load_extension('x')", GateForbidden, "load_extension is not allowed"},
		{"SELECT * FROM _cloud_changes", GateInternal, "internal object"},
		{"SELECT * FROM \"_cloud_audit\"", GateInternal, "internal object"},
		{"DELETE FROM sqlite_master", GateInternal, "internal object"},
		{"DROP TABLE _cloud_changes", GateInternal, "internal object"},
	}
	for _, tc := range cases {
		err := CheckStatement(tc.sqlText, false)
		var gateErr *GateError
		if !errors.As(err, &gateErr) {
			t.Errorf("CheckStatement(%q) = %v, want GateError", tc.sqlText, err)
			continue
		}
		if gateErr.Code != tc.wantCode {
			t.Errorf("CheckStatement(%q) code = %s, want %s", tc.sqlText, gateErr.Code, tc.wantCode)
		}
		if !contains(gateErr.Message, tc.wantMatch) {
			t.Errorf("CheckStatement(%q) message = %q, want contains %q", tc.sqlText, gateErr.Message, tc.wantMatch)
		}
	}
}

func TestCheckStatementReadOnly(t *testing.T) {
	allowed := []string{"SELECT 1", "WITH x AS (SELECT 1) SELECT 1", "VALUES (1)", "EXPLAIN SELECT 1"}
	for _, sqlText := range allowed {
		if err := CheckStatement(sqlText, true); err != nil {
			t.Errorf("readOnly CheckStatement(%q) = %v, want nil", sqlText, err)
		}
	}
	rejected := []string{
		"INSERT INTO t (id) VALUES (1)",
		"UPDATE t SET x = 1",
		"DELETE FROM t",
		"CREATE TABLE t (id TEXT)",
		"DROP TABLE t",
	}
	for _, sqlText := range rejected {
		err := CheckStatement(sqlText, true)
		var gateErr *GateError
		if !errors.As(err, &gateErr) || gateErr.Code != GateReadOnly {
			t.Errorf("readOnly CheckStatement(%q) = %v, want read_only GateError", sqlText, err)
		}
	}
}

func TestTranslate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a ILIKE b", "a LIKE b"},
		{"a ilike b", "a LIKE b"},
		{`CAST("name" AS TEXT) ILIKE '%x%' ESCAPE '\'`, `CAST("name" AS TEXT) LIKE '%x%' ESCAPE '\'`},
		{"SELECT TRUE, FALSE", "SELECT 1, 0"},
		{"SELECT NOW(), x", "SELECT CURRENT_TIMESTAMP, x"},
		{"SELECT lead_id::text FROM leads", "SELECT lead_id FROM leads"},
		{"SELECT id::uuid", "SELECT id"},
		{"SELECT x::varchar(64)", "SELECT x"},
		{`SELECT 'ILIKE TRUE NOW()' AS "ilike"`, `SELECT 'ILIKE TRUE NOW()' AS "ilike"`},
		{"SELECT 1 -- ILIKE TRUE\n", "SELECT 1 -- ILIKE TRUE\n"},
		{"SELECT 1 /* ILIKE */", "SELECT 1 /* ILIKE */"},
		{"SELECT x::int", "SELECT x::int"}, // unsupported cast passes through
		{"SELECT now_col FROM t", "SELECT now_col FROM t"},
	}
	for _, tc := range cases {
		if got := Translate(tc.in); got != tc.want {
			t.Errorf("Translate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifyStatement(t *testing.T) {
	cases := []struct {
		sqlText  string
		wantKind StatementKind
		wantTbl  string
	}{
		{"SELECT * FROM t", KindRead, ""},
		{"WITH x AS (SELECT 1) SELECT * FROM x", KindRead, ""},
		{"INSERT INTO leads (a) VALUES (1)", KindInsert, "leads"},
		{"INSERT OR REPLACE INTO leads (a) VALUES (1)", KindInsert, "leads"},
		{"UPDATE leads SET a = 1", KindUpdate, "leads"},
		{"DELETE FROM leads WHERE a = 1", KindDelete, "leads"},
		{"CREATE TABLE IF NOT EXISTS leads (a TEXT)", KindSchema, "leads"},
		{"DROP TABLE leads", KindSchema, "leads"},
		{"CREATE INDEX idx ON leads (a)", KindSchema, "leads"},
		{"ALTER TABLE leads ADD COLUMN b TEXT", KindSchema, "leads"},
		{"REPLACE INTO leads (a) VALUES (1)", KindInsert, "leads"},
	}
	for _, tc := range cases {
		kind, table := ClassifyStatement(tc.sqlText)
		if kind != tc.wantKind || table != tc.wantTbl {
			t.Errorf("ClassifyStatement(%q) = (%v, %q), want (%v, %q)", tc.sqlText, kind, table, tc.wantKind, tc.wantTbl)
		}
	}
}

func TestValidateTableName(t *testing.T) {
	good := []string{"leads", "interactions_2", "T_1"}
	for _, name := range good {
		if err := ValidateTableName(name); err != nil {
			t.Errorf("ValidateTableName(%q) = %v", name, err)
		}
	}
	bad := []string{"", "1abc", "a-b", "a b", "_cloud_changes", "sqlite_master", "a;b"}
	for _, name := range bad {
		if err := ValidateTableName(name); err == nil {
			t.Errorf("ValidateTableName(%q) = nil, want error", name)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestStatementReturnsRows guards the SELECT-vs-EXEC routing.
func TestStatementReturnsRows(t *testing.T) {
	cases := []struct {
		sqlText string
		want    bool
	}{
		{"SELECT 1", true},
		{"WITH x AS (SELECT 1) SELECT * FROM x", true},
		{"EXPLAIN SELECT 1", true},
		{"INSERT INTO t (a) VALUES (1)", false},
		{"INSERT INTO t (a) VALUES (1) RETURNING a", true},
		{"UPDATE t SET a = 1", false},
		{"CREATE TABLE t (a TEXT)", false},
	}
	for _, tc := range cases {
		kind, _ := ClassifyStatement(tc.sqlText)
		if got := statementReturnsRows(Translate(tc.sqlText), kind); got != tc.want {
			t.Errorf("statementReturnsRows(%q) = %v, want %v", tc.sqlText, got, tc.want)
		}
	}
}
