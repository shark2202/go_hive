package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const (
	sessionDirTestQueen  = "queen_growth"
	sessionDirTestColony = "icp_outreach"
	sessionDirTestID     = "session_20260728_202550_96d649af"
)

func sessionDirTestDMPath(home hivehome.Home) string {
	return filepath.Join(home.QueensDir(), sessionDirTestQueen, "sessions", sessionDirTestID)
}

func sessionDirTestColonyPath(home hivehome.Home) string {
	return filepath.Join(home.ColoniesDir(), sessionDirTestColony, "queens", sessionDirTestQueen, "sessions", sessionDirTestID)
}

func mkdirTestDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_colony_id_read_off_the_session_path
func TestColonyIDReadOffTheSessionPath(t *testing.T) {
	dir := filepath.Join("/h", "colonies", "acme", "queens", sessionDirTestQueen, "sessions", "s1")
	if got, ok := ColonyIDForSessionDir(dir); !ok || got != "acme" {
		t.Fatalf("ColonyIDForSessionDir(%q) = %q, %v; want acme, true", dir, got, ok)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_queen_dm_session_path_has_no_colony
func TestQueenDMSessionPathHasNoColony(t *testing.T) {
	dir := filepath.Join("/h", "queens", sessionDirTestQueen, "sessions", "s1")
	if got, ok := ColonyIDForSessionDir(dir); ok {
		t.Fatalf("ColonyIDForSessionDir(%q) = %q, true; want no colony", dir, got)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_generator_finds_a_colony_only_session
func TestGeneratorFindsAColonyOnlySession(t *testing.T) {
	home := serverTestHome(t)
	want := sessionDirTestColonyPath(home)
	mkdirTestDir(t, want)

	got, err := IterQueenSessionDirs(home, sessionDirTestID)
	if err != nil {
		t.Fatalf("IterQueenSessionDirs: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("IterQueenSessionDirs = %#v; want [%q]", got, want)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_generator_yields_every_dir_sharing_an_id
func TestGeneratorYieldsEveryDirSharingAnID(t *testing.T) {
	home := serverTestHome(t)
	dm := sessionDirTestDMPath(home)
	colony := sessionDirTestColonyPath(home)
	mkdirTestDir(t, dm)
	mkdirTestDir(t, colony)

	got, err := IterQueenSessionDirs(home, sessionDirTestID)
	if err != nil {
		t.Fatalf("IterQueenSessionDirs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("IterQueenSessionDirs = %#v; want two dirs", got)
	}
	seen := map[string]bool{got[0]: true, got[1]: true}
	if !seen[dm] || !seen[colony] {
		t.Fatalf("IterQueenSessionDirs = %#v; want %q and %q", got, dm, colony)
	}
	if got[0] != dm {
		t.Fatalf("first candidate = %q; want the DM tree dir %q", got[0], dm)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_resume_colony_is_found_despite_a_duplicate_dm_dir
func TestResumeColonyIsFoundDespiteADuplicateDMDir(t *testing.T) {
	home := serverTestHome(t)
	mkdirTestDir(t, sessionDirTestDMPath(home))
	mkdirTestDir(t, sessionDirTestColonyPath(home))

	got, ok := DeriveResumeColonyID(home, sessionDirTestID)
	if !ok || got != sessionDirTestColony {
		t.Fatalf("DeriveResumeColonyID = %q, %v; want %q, true", got, ok, sessionDirTestColony)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_resume_colony_is_none_for_a_real_dm_session
func TestResumeColonyIsNoneForARealDMSession(t *testing.T) {
	home := serverTestHome(t)
	mkdirTestDir(t, sessionDirTestDMPath(home))

	if got, ok := DeriveResumeColonyID(home, sessionDirTestID); ok {
		t.Fatalf("DeriveResumeColonyID = %q, true; want no colony", got)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_resume_colony_is_none_when_nothing_matches
func TestResumeColonyIsNoneWhenNothingMatches(t *testing.T) {
	home := serverTestHome(t)
	if got, ok := DeriveResumeColonyID(home, sessionDirTestID); ok {
		t.Fatalf("DeriveResumeColonyID = %q, true; want no colony", got)
	}
}

// Parity: core/framework/server/tests/test_session_dir_resolution.py::test_first_match_lookup_still_prefers_the_dm_tree
func TestFirstMatchLookupStillPrefersTheDMTree(t *testing.T) {
	home := serverTestHome(t)
	dm := sessionDirTestDMPath(home)
	mkdirTestDir(t, dm)
	mkdirTestDir(t, sessionDirTestColonyPath(home))

	got, err := FindQueenSessionDir(home, sessionDirTestID)
	if err != nil {
		t.Fatalf("FindQueenSessionDir: %v", err)
	}
	if got != dm {
		t.Fatalf("FindQueenSessionDir = %q; want %q", got, dm)
	}
}

// Parity-exempt: 上游 _queen_dm_session_dir 只在改名路由里被间接调用，没有
func TestQueenDMSessionDirAcceptsOnlyDMSessions(t *testing.T) {
	home := serverTestHome(t)
	dm := sessionDirTestDMPath(home)
	mkdirTestDir(t, dm)
	mkdirTestDir(t, sessionDirTestColonyPath(home))

	if got, ok := QueenDMSessionDir(home, sessionDirTestID); !ok || got != dm {
		t.Fatalf("QueenDMSessionDir = %q, %v; want the DM dir %q", got, ok, dm)
	}

	colonyOnly := "session_20260728_202551_aaaaaaaa"
	mkdirTestDir(t, filepath.Join(home.ColoniesDir(), sessionDirTestColony, "queens", sessionDirTestQueen, "sessions", colonyOnly))
	if got, ok := QueenDMSessionDir(home, colonyOnly); ok {
		t.Fatalf("colony-only id = %q, true; want no DM session", got)
	}

	for _, bad := range []string{"", "not-a-session", "session_/../escape", `session_\escape`} {
		if got, ok := QueenDMSessionDir(home, bad); ok {
			t.Fatalf("QueenDMSessionDir(%q) = %q, true; want rejection", bad, got)
		}
	}
}

// Parity-exempt: pathlib normalizes at construction, so Path("/colonies/acme/")
func TestColonyRootWithTrailingSeparatorHasNoColony(t *testing.T) {
	if got, ok := ColonyIDForSessionDir("/colonies/acme/"); ok {
		t.Fatalf("ColonyIDForSessionDir(/colonies/acme/) = %q, true; want no colony", got)
	}
	if got, ok := ColonyIDForSessionDir("/h/colonies/acme/queens/q/sessions/s1/"); !ok || got != "acme" {
		t.Fatalf("trailing-separator session path = %q, %v; want acme, true", got, ok)
	}
}

// Parity-exempt: is_dir() follows symlinks, so a symlinked queen tree is a
func TestSessionDirTraversalFollowsSymlinkedTrees(t *testing.T) {
	home := serverTestHome(t)
	realQueen := filepath.Join(home.Root, "real_queen")
	mkdirTestDir(t, filepath.Join(realQueen, "sessions", sessionDirTestID))
	mkdirTestDir(t, home.QueensDir())
	if err := os.Symlink(realQueen, filepath.Join(home.QueensDir(), "link")); err != nil {
		t.Fatalf("symlink queen: %v", err)
	}

	wantQueenDir := filepath.Join(home.QueensDir(), "link", "sessions", sessionDirTestID)
	got, err := FindQueenSessionDir(home, sessionDirTestID)
	if err != nil {
		t.Fatalf("FindQueenSessionDir: %v", err)
	}
	if got != wantQueenDir {
		t.Fatalf("FindQueenSessionDir = %q; want %q", got, wantQueenDir)
	}

	realColony := filepath.Join(home.Root, "real_colony")
	mkdirTestDir(t, filepath.Join(realColony, "queens", sessionDirTestQueen, "sessions", sessionDirTestID))
	mkdirTestDir(t, home.ColoniesDir())
	if err := os.Symlink(realColony, filepath.Join(home.ColoniesDir(), "clink")); err != nil {
		t.Fatalf("symlink colony: %v", err)
	}
	if got, ok := DeriveResumeColonyID(home, sessionDirTestID); !ok || got != "clink" {
		t.Fatalf("DeriveResumeColonyID = %q, %v; want clink, true", got, ok)
	}
}

// Parity-exempt: the upstream traversal is a lazy generator, so a listing
func TestLookupsStopBeforeALaterListingError(t *testing.T) {
	home := serverTestHome(t)
	want := sessionDirTestColonyPath(home)
	mkdirTestDir(t, want)
	mkdirTestDir(t, filepath.Join(home.ColoniesDir(), "z_bad"))
	if err := os.WriteFile(filepath.Join(home.ColoniesDir(), "z_bad", "queens"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write queens file: %v", err)
	}

	got, err := FindQueenSessionDir(home, sessionDirTestID)
	if err != nil || got != want {
		t.Fatalf("FindQueenSessionDir = %q, %v; want %q, nil", got, err, want)
	}
	if got, ok := DeriveResumeColonyID(home, sessionDirTestID); !ok || got != sessionDirTestColony {
		t.Fatalf("DeriveResumeColonyID = %q, %v; want %q, true", got, ok, sessionDirTestColony)
	}
	if _, err := IterQueenSessionDirs(home, sessionDirTestID); err == nil {
		t.Fatal("IterQueenSessionDirs = nil error; want the listing failure")
	}
}

// Parity-exempt: same laziness contract for a DM match, which the generator
func TestFirstMatchStopsBeforeAnUnreadableColonyTree(t *testing.T) {
	home := serverTestHome(t)
	dm := sessionDirTestDMPath(home)
	mkdirTestDir(t, dm)
	mkdirTestDir(t, filepath.Join(home.ColoniesDir(), "z_bad"))
	if err := os.WriteFile(filepath.Join(home.ColoniesDir(), "z_bad", "queens"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write queens file: %v", err)
	}

	got, err := FindQueenSessionDir(home, sessionDirTestID)
	if err != nil || got != dm {
		t.Fatalf("FindQueenSessionDir = %q, %v; want %q, nil", got, err, dm)
	}
	if got, ok := DeriveResumeColonyID(home, sessionDirTestID); ok {
		t.Fatalf("DeriveResumeColonyID = %q, true; want no colony", got)
	}
}

// Parity-exempt: an error before any match reaches both lookup helpers, which
func TestLookupsPropagateAnEarlyListingError(t *testing.T) {
	home := serverTestHome(t)
	mkdirTestDir(t, sessionDirTestColonyPath(home))
	mkdirTestDir(t, filepath.Join(home.ColoniesDir(), "a_bad"))
	if err := os.WriteFile(filepath.Join(home.ColoniesDir(), "a_bad", "queens"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write queens file: %v", err)
	}

	if got, err := FindQueenSessionDir(home, sessionDirTestID); err == nil {
		t.Fatalf("FindQueenSessionDir = %q, nil; want an error", got)
	}
	if got, ok := DeriveResumeColonyID(home, sessionDirTestID); ok {
		t.Fatalf("DeriveResumeColonyID = %q, true; want no colony", got)
	}
}
