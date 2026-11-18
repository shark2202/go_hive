package server

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/globaldb"
)

var leadsColumns = []TableColumn{
	{Name: "lead_id", Type: "text", NotNull: true, PK: 1},
	{Name: "name", Type: "text"},
	{Name: "email", Type: "text"},
	{Name: "linkedin_url", Type: "text"},
}

var leadsPK = []string{"lead_id"}

// Parity: framework/server/tests/test_global_data_routes.py::test_derive_lead_id_prefers_linkedin_slug
func TestDeriveLeadIDPrefersLinkedInSlug(t *testing.T) {
	row := map[string]any{
		"email":        "Jane@Acme.com",
		"linkedin_url": "https://www.LinkedIn.com/in/Jane-Doe/?utm_source=x#top",
	}
	got, ok := DeriveLeadID(row)
	if !ok || got != "linkedin.com/in/jane-doe" {
		t.Errorf("DeriveLeadID = (%q, %v), want (linkedin.com/in/jane-doe, true)", got, ok)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_derive_lead_id_falls_back_to_email
func TestDeriveLeadIDFallsBackToEmail(t *testing.T) {
	got, ok := DeriveLeadID(map[string]any{"email": "  Jane@Acme.com "})
	if !ok || got != "jane@acme.com" {
		t.Errorf("DeriveLeadID(email) = (%q, %v), want (jane@acme.com, true)", got, ok)
	}

	if got, ok := DeriveLeadID(map[string]any{"name": "no identifiers"}); ok || got != "" {
		t.Errorf("DeriveLeadID(no identifiers) = (%q, %v), want (\"\", false)", got, ok)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_insert_lead_derives_pk_and_uses_insert_mode
func TestInsertLeadDerivesPKAndUsesInsertMode(t *testing.T) {
	plan := PlanInsertRow(
		"leads",
		map[string]any{"name": "Jane", "linkedin_url": "https://www.linkedin.com/in/jane/"},
		leadsColumns,
		leadsPK,
		nil,
	)
	if plan.Status != 200 || plan.Upsert == nil {
		t.Fatalf("plan = %+v, want accepted upsert", plan)
	}
	if plan.Upsert.Mode != "insert" {
		t.Errorf("mode = %q, want insert", plan.Upsert.Mode)
	}
	if plan.Upsert.Row["lead_id"] != "linkedin.com/in/jane" {
		t.Errorf("row lead_id = %v, want linkedin.com/in/jane", plan.Upsert.Row["lead_id"])
	}

	status, body := MapInsertResult(plan, map[string]any{"success": true, "inserted": 1}, nil)
	want := map[string]any{
		"inserted": 1,
		"pk":       map[string]any{"lead_id": "linkedin.com/in/jane"},
	}
	if status != 200 || !reflect.DeepEqual(body, want) {
		t.Errorf("MapInsertResult = (%d, %v), want (200, %v)", status, body, want)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_insert_lead_without_identifier_400s_before_backend
func TestInsertLeadWithoutIdentifierRejectsBeforeBackend(t *testing.T) {
	plan := PlanInsertRow("leads", map[string]any{"name": "No Id"}, leadsColumns, leadsPK, nil)
	if plan.Status != 400 {
		t.Fatalf("status = %d, want 400", plan.Status)
	}
	msg, _ := plan.Body["error"].(string)
	if !strings.Contains(msg, "email or LinkedIn") {
		t.Errorf("error = %q, want mention of email or LinkedIn", msg)
	}
	if plan.Upsert != nil {
		t.Errorf("upsert = %+v, want nil (no cloud write attempted)", plan.Upsert)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_insert_conflict_maps_to_409_with_pk
func TestInsertConflictMapsTo409WithPK(t *testing.T) {
	plan := PlanInsertRow(
		"leads",
		map[string]any{"name": "Dup", "email": "dup@x.com"},
		leadsColumns,
		leadsPK,
		nil,
	)
	conflict := &globaldb.GlobalDbError{
		Message: "A row with this key already exists in 'leads'.",
		Status:  409,
	}
	status, body := MapInsertResult(plan, nil, conflict)
	if status != 409 {
		t.Fatalf("status = %d, want 409", status)
	}
	if body["error"] != "conflict" {
		t.Errorf("error = %v, want conflict", body["error"])
	}
	wantPK := map[string]any{"lead_id": "dup@x.com"}
	if !reflect.DeepEqual(body["pk"], wantPK) {
		t.Errorf("pk = %v, want %v", body["pk"], wantPK)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_changes_init_omits_since
func TestChangesInitOmitsSince(t *testing.T) {
	if since := ChangesSinceParam(""); since != nil {
		t.Errorf("ChangesSinceParam(\"\") = %v, want nil", *since)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_changes_passes_cursor_through
func TestChangesPassesCursorThrough(t *testing.T) {
	cursor := "2026-07-20 01:02:03.000004+00"
	since := ChangesSinceParam(cursor)
	if since == nil || *since != cursor {
		t.Errorf("ChangesSinceParam(%q) = %v, want the cursor", cursor, since)
	}
}

// Parity: framework/server/tests/test_global_data_routes.py::test_changes_signed_out_maps_to_401
func TestChangesSignedOutMapsTo401(t *testing.T) {
	status, body := GlobalDBErrorResponse(&globaldb.NotSignedInError{Message: "Not signed in to hive cloud."})
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
	if body["signed_out"] != true {
		t.Errorf("signed_out = %v, want true", body["signed_out"])
	}
}
