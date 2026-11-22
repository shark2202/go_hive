package colony

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const resumeSessionID = "session_20260719_121546_afe89313"

func makeHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return home
}

func makeColonySession(t *testing.T, home hivehome.Home, colonyID, queen, sid string) string {
	t.Helper()
	dir := filepath.Join(home.ColoniesDir(), colonyID, "queens", queen, "sessions", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir colony session: %v", err)
	}
	return dir
}

func makeDMSession(t *testing.T, home hivehome.Home, queen, sid string) string {
	t.Helper()
	dir := filepath.Join(home.QueensDir(), queen, "sessions", sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir dm session: %v", err)
	}
	return dir
}

// Parity: core/tests/test_derive_resume_colony_id.py::test_colony_session_resolves_to_its_colony
func TestDeriveResumeColonyIDFromColonySession(t *testing.T) {
	home := makeHome(t)
	makeColonySession(t, home, "email_reply", "queen_outbound", resumeSessionID)
	got := DeriveResumeColonyID(home, resumeSessionID)
	if got == nil || *got != "email_reply" {
		t.Fatalf("colony id = %v, want email_reply", got)
	}
}

// Parity: core/tests/test_derive_resume_colony_id.py::test_dm_session_yields_none
func TestDeriveResumeColonyIDDMSessionYieldsNone(t *testing.T) {
	home := makeHome(t)
	makeDMSession(t, home, "queen_outbound", resumeSessionID)
	if got := DeriveResumeColonyID(home, resumeSessionID); got != nil {
		t.Fatalf("colony id = %v, want nil", *got)
	}
}

// Parity: core/tests/test_derive_resume_colony_id.py::test_unknown_session_yields_none
func TestDeriveResumeColonyIDUnknownSessionYieldsNone(t *testing.T) {
	home := makeHome(t)
	if got := DeriveResumeColonyID(home, resumeSessionID); got != nil {
		t.Fatalf("colony id = %v, want nil", *got)
	}
}

// Parity: core/tests/test_derive_resume_colony_id.py::test_colony_wins_even_when_a_dm_shadow_exists
func TestDeriveResumeColonyIDColonyWinsOverDMShadow(t *testing.T) {
	home := makeHome(t)
	makeDMSession(t, home, "queen_outbound", resumeSessionID)
	makeColonySession(t, home, "email_reply", "queen_outbound", resumeSessionID)
	got := DeriveResumeColonyID(home, resumeSessionID)
	if got == nil || *got != "email_reply" {
		t.Fatalf("colony id = %v, want email_reply", got)
	}
}

// Parity: core/tests/test_derive_resume_colony_id.py::test_colony_session_resolves_to_its_colony, core/tests/test_derive_resume_colony_id.py::test_dm_session_yields_none
func TestColonyIDForSessionDirNested(t *testing.T) {
	dir := filepath.Join("hive", "colonies", "slug", "queens", "q", "sessions", "sid")
	got := ColonyIDForSessionDir(dir)
	if got == nil || *got != "slug" {
		t.Fatalf("colony id = %v, want slug", got)
	}
	if got := ColonyIDForSessionDir(filepath.Join("hive", "queens", "q", "sessions", "sid")); got != nil {
		t.Fatalf("DM dir colony id = %v, want nil", *got)
	}
}
