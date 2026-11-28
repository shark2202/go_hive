package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newTestTeam(t *testing.T) *Team {
	t.Helper()
	st, err := Open(Options{Dir: t.TempDir(), StatementTimeout: 5 * time.Second, Now: time.Now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	team, err := st.Team("team_test")
	if err != nil {
		t.Fatalf("Team: %v", err)
	}
	return team
}

func TestCRMCreateQueryGetPatchStageDelete(t *testing.T) {
	team := newTestTeam(t)
	ctx := context.Background()

	page, err := team.CRMCreateRecord(ctx, "people", map[string]any{
		"fields": map[string]any{
			"name":  "Ada Lovelace",
			"email": "ada@example.com",
			"title": "Countess",
			"stage": "new",
		},
		"because": "unit test",
	}, "u_test", "team_test")
	if err != nil {
		t.Fatalf("CRMCreateRecord: %v", err)
	}
	if page.Table != "crm_people" || len(page.Rows) != 1 || page.Total != 1 {
		t.Fatalf("create page = %#v", page)
	}
	record := page.Rows[0]
	id, _ := record["id"].(string)
	if len(id) != 32 {
		t.Fatalf("id = %q, want a 32-char opaque id", id)
	}
	if record["name"] != "Ada Lovelace" || record["stage"] != "new" {
		t.Fatalf("record = %#v", record)
	}

	got, err := team.CRMGetRecord(ctx, "people", id)
	if err != nil {
		t.Fatalf("CRMGetRecord: %v", err)
	}
	if len(got.Rows) != 1 || got.Rows[0]["id"] != id {
		t.Fatalf("get = %#v", got)
	}

	// Filter + search + sort + pagination.
	query, err := team.CRMQueryRecords(ctx, "people", map[string]any{
		"filter":    []any{map[string]any{"column": "stage", "op": "eq", "value": "new"}},
		"search":    "ada",
		"order_by":  "name",
		"order_dir": "asc",
		"limit":     10,
		"offset":    0,
	})
	if err != nil {
		t.Fatalf("CRMQueryRecords: %v", err)
	}
	if query.Total != 1 || len(query.Rows) != 1 {
		t.Fatalf("query = %#v", query)
	}
	if query.Limit != 10 || query.Offset != 0 || query.PrimaryKey[0] != "id" {
		t.Fatalf("query metadata = %#v", query)
	}

	// A LIKE wildcard in the search box matches literally.
	literal, err := team.CRMQueryRecords(ctx, "people", map[string]any{"search": "%"})
	if err != nil {
		t.Fatalf("literal search: %v", err)
	}
	if literal.Total != 0 {
		t.Fatalf("literal %% search matched %d rows", literal.Total)
	}

	groups, err := team.CRMGroupCounts(ctx, "people", map[string]any{"group_by": "stage"})
	if err != nil {
		t.Fatalf("CRMGroupCounts: %v", err)
	}
	if groups.GroupBy != "stage" || len(groups.Groups) != 1 || groups.Groups[0].Value != "new" || groups.Groups[0].Count != 1 {
		t.Fatalf("groups = %#v", groups)
	}

	updated, err := team.CRMUpdateRecord(ctx, "people", id, map[string]any{
		"fields": map[string]any{"title": "Mathematician"},
	}, "u_test", "team_test")
	if err != nil {
		t.Fatalf("CRMUpdateRecord: %v", err)
	}
	if updated.Rows[0]["title"] != "Mathematician" {
		t.Fatalf("updated = %#v", updated.Rows[0])
	}

	staged, err := team.CRMSetStage(ctx, "people", id, map[string]any{"value": "won"}, "u_test", "team_test")
	if err != nil {
		t.Fatalf("CRMSetStage: %v", err)
	}
	if staged.Rows[0]["stage"] != "won" {
		t.Fatalf("staged = %#v", staged.Rows[0])
	}

	deleted, err := team.CRMDeleteRecord(ctx, "people", id, map[string]any{"because": "done"}, "u_test", "team_test")
	if err != nil {
		t.Fatalf("CRMDeleteRecord: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	query, err = team.CRMQueryRecords(ctx, "people", nil)
	if err != nil {
		t.Fatalf("query after delete: %v", err)
	}
	if query.Total != 0 || len(query.Rows) != 0 {
		t.Fatalf("archived row is still visible: %#v", query)
	}
	if _, err := team.CRMGetRecord(ctx, "people", id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get archived err = %v, want ErrNotFound", err)
	}
	if again, err := team.CRMDeleteRecord(ctx, "people", id, nil, "u_test", "team_test"); err != nil || again != 0 {
		t.Fatalf("second delete = %d, %v", again, err)
	}
}

func TestCRMRejectsUnknownEntityAndFields(t *testing.T) {
	team := newTestTeam(t)
	ctx := context.Background()

	if _, err := team.CRMQueryRecords(ctx, "invoices", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown entity err = %v", err)
	}
	if _, err := team.CRMCreateRecord(ctx, "people", map[string]any{
		"fields": map[string]any{"id": "p1"},
	}, "u", "t"); err == nil || !strings.Contains(err.Error(), "field is reserved") {
		t.Fatalf("reserved field err = %v", err)
	}
	if _, err := team.CRMCreateRecord(ctx, "people", map[string]any{
		"fields":     map[string]any{"name": "x"},
		"identities": map[string]any{"email": "x@example.com"},
	}, "u", "t"); err == nil || !strings.Contains(err.Error(), "identities is not supported") {
		t.Fatalf("identities err = %v", err)
	}
	page, err := team.CRMCreateRecord(ctx, "people", map[string]any{"fields": map[string]any{"name": "x"}}, "u", "t")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := page.Rows[0]["id"].(string)
	if _, err := team.CRMUpdateRecord(ctx, "people", id, map[string]any{
		"fields": map[string]any{"unknown_col": "x"},
	}, "u", "t"); err == nil || !strings.Contains(err.Error(), "unknown column") {
		t.Fatalf("unknown patch column err = %v", err)
	}
	if _, err := team.CRMUpdateRecord(ctx, "people", "missing", map[string]any{
		"fields": map[string]any{"name": "x"},
	}, "u", "t"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing patch err = %v", err)
	}
	if _, err := team.CRMSetStage(ctx, "people", id, map[string]any{}, "u", "t"); err == nil || !strings.Contains(err.Error(), "value is required") {
		t.Fatalf("stage without value err = %v", err)
	}
}

func TestCRMFilterInjectionRejected(t *testing.T) {
	team := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.CRMCreateRecord(ctx, "people", map[string]any{"fields": map[string]any{"name": "Ada"}}, "u", "t"); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := team.CRMQueryRecords(ctx, "people", map[string]any{
		"filter": []any{map[string]any{"column": "name; DROP TABLE crm_people", "op": "eq", "value": 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid identifier") {
		t.Fatalf("injection err = %v", err)
	}
	// The table must still exist and hold the row.
	page, err := team.CRMQueryRecords(ctx, "people", nil)
	if err != nil || page.Total != 1 {
		t.Fatalf("table damaged: %#v, %v", page, err)
	}
}

func TestCRMTablesAreHiddenFromGlobalDBListing(t *testing.T) {
	team := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.CRMCreateRecord(ctx, "people", map[string]any{"fields": map[string]any{"name": "Ada"}}, "u", "t"); err != nil {
		t.Fatalf("create: %v", err)
	}
	tables, err := team.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables: %v", err)
	}
	for _, table := range tables {
		if strings.HasPrefix(table.Name, "crm_") {
			t.Fatalf("CRM table leaked into global-db listing: %s", table.Name)
		}
	}
}

func TestCRMChangesHiddenFromGlobalFeed(t *testing.T) {
	team := newTestTeam(t)
	ctx := context.Background()
	if _, err := team.CRMQueryRecords(ctx, "companies", nil); err != nil {
		t.Fatalf("query: %v", err)
	}
	if _, err := team.CRMCreateRecord(ctx, "companies", map[string]any{"fields": map[string]any{"name": "Acme"}}, "u", "t"); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := team.Changes(ctx, "")
	if err != nil {
		t.Fatalf("changes init: %v", err)
	}
	for _, name := range first.Covered {
		if strings.HasPrefix(name, "crm_") {
			t.Fatalf("CRM table in global changes coverage: %v", first.Covered)
		}
	}
	if first.Cursor == nil {
		t.Fatal("changes init returned no cursor")
	}
	after, err := team.Changes(ctx, *first.Cursor)
	if err != nil {
		t.Fatalf("changes since: %v", err)
	}
	for name := range after.Tables {
		if strings.HasPrefix(name, "crm_") {
			t.Fatalf("CRM table in global changes feed: %v", after.Tables)
		}
	}
}

func TestCRMTeamIsolation(t *testing.T) {
	st, err := Open(Options{Dir: t.TempDir(), StatementTimeout: 5 * time.Second, Now: time.Now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	teamA, _ := st.Team("team_a")
	teamB, _ := st.Team("team_b")
	ctx := context.Background()
	if _, err := teamA.CRMCreateRecord(ctx, "companies", map[string]any{"fields": map[string]any{"name": "Acme"}}, "u", "team_a"); err != nil {
		t.Fatalf("create: %v", err)
	}
	page, err := teamB.CRMQueryRecords(ctx, "companies", nil)
	if err != nil {
		t.Fatalf("query B: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("team B saw team A's CRM row: %#v", page)
	}
}
