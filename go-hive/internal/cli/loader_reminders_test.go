package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// writeQueenSkill materialises one queen-scoped skill under the hive layout.
func writeQueenSkill(t *testing.T, home hivehome.Home, queen, name, description string) {
	t.Helper()
	dir := filepath.Join(home.QueenDir(queen), "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nProbe body.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// writeQueenSkillWithVisibility materialises one queen-scoped skill carrying a
// metadata.visibility list.
func writeQueenSkillWithVisibility(t *testing.T, home hivehome.Home, queen, name, description, visibility string) {
	t.Helper()
	dir := filepath.Join(home.QueenDir(queen), "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\nmetadata:\n  visibility: [" + visibility + "]\n---\n\nProbe body.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.render_skills_catalog
// (the queen's reminder announces the phase-filtered catalog).
func TestQueenSkillsCatalogForPhaseFiltersVisibility(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeQueenSkill(t, home, "queen_alpha", "always-on-skill", "All phases.")
	writeQueenSkillWithVisibility(t, home, "queen_alpha", "colony-only-skill", "Colony only.", "colony")
	writeQueenSkillWithVisibility(t, home, "queen_alpha", "worker-only-skill", "Worker only.", "worker")

	catalog := queenSkillsCatalogAt(home, "queen_alpha", t.TempDir())

	independent := catalog.CatalogForPhase("independent")
	if !strings.Contains(independent, "<name>always-on-skill</name>") {
		t.Fatalf("independent catalog missing the always-on skill: %q", independent)
	}
	if strings.Contains(independent, "colony-only-skill") || strings.Contains(independent, "worker-only-skill") {
		t.Fatalf("independent catalog leaked phase-only skills: %q", independent)
	}

	colony := catalog.CatalogForPhase("colony")
	if !strings.Contains(colony, "<name>colony-only-skill</name>") {
		t.Fatalf("colony catalog missing the colony skill: %q", colony)
	}
	if strings.Contains(colony, "worker-only-skill") {
		t.Fatalf("colony catalog leaked the worker skill: %q", colony)
	}

	// An empty phase keeps the unfiltered catalog (the holder's default).
	if unfiltered := catalog.CatalogForPhase(""); unfiltered != catalog.Catalog() {
		t.Fatal("empty phase should return the unfiltered catalog")
	}
}

// Parity: tests/test_skill_integration.py::test_skills_catalog_in_prompt
func TestQueenSkillsCatalogProviderReadsQueenSkillsDir(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	homeUser := t.TempDir()
	writeQueenSkill(t, home, "queen_alpha", "loader-catalog-probe", "A probe skill.")

	provider := queenSkillsCatalogProviderAt(home, "queen_alpha", homeUser)
	catalog := provider()
	if !strings.Contains(catalog, "<name>loader-catalog-probe</name>") {
		t.Fatalf("catalog = %q, want the queen skill", catalog)
	}
	if again := provider(); again != catalog {
		t.Fatal("catalog provider did not cache the loaded manager")
	}

	other := queenSkillsCatalogProviderAt(home, "queen_beta", t.TempDir())()
	if strings.Contains(other, "loader-catalog-probe") {
		t.Fatalf("another queen's catalog leaked the skill: %q", other)
	}
}

// Parity-exempt: Go loader degrade path; upstream always has a queen home, so
// an unresolved home yields no catalog rather than inventing one.
func TestQueenSkillsCatalogProviderEmptyWithoutHome(t *testing.T) {
	if catalog := queenSkillsCatalogProvider(hivehome.Home{}, "queen_alpha")(); catalog != "" {
		t.Fatalf("catalog = %q, want empty without a hive home", catalog)
	}
}

// Parity: framework/skills/live_reload.py::ManagerReloadAdapter (a live
// session's catalog is rebuilt in place after a skill changed on disk).
func TestQueenSkillsCatalogReloadPicksUpANewSkill(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeQueenSkill(t, home, "queen_alpha", "reload-probe", "First skill.")

	catalog := queenSkillsCatalogAt(home, "queen_alpha", t.TempDir())
	before := catalog.Catalog()
	if !strings.Contains(before, "<name>reload-probe</name>") {
		t.Fatalf("catalog = %q, want the first skill", before)
	}

	writeQueenSkill(t, home, "queen_alpha", "reload-probe-two", "Second skill.")
	if stale := catalog.Catalog(); strings.Contains(stale, "reload-probe-two") {
		t.Fatalf("catalog picked the skill up without a reload: %q", stale)
	}

	// The reload fan-out holds the mutation lock while rebuilding, exactly like
	// skills.ManagerReloadAdapter.
	lock := catalog.MutationLock()
	lock.Lock()
	err := catalog.Reload()
	lock.Unlock()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	after := catalog.Catalog()
	if !strings.Contains(after, "<name>reload-probe</name>") ||
		!strings.Contains(after, "<name>reload-probe-two</name>") {
		t.Fatalf("catalog after reload = %q, want both skills", after)
	}
	if after == before {
		t.Fatal("catalog did not change across the reload")
	}
}

// Parity-exempt: the degrade path has no manager to rebuild, so a reload on an
// unresolved home must no-op instead of building a layout out of nothing.
func TestQueenSkillsCatalogReloadWithoutHomeIsANoop(t *testing.T) {
	catalog := queenSkillsCatalogAt(hivehome.Home{}, "queen_alpha", "")
	lock := catalog.MutationLock()
	lock.Lock()
	err := catalog.Reload()
	lock.Unlock()

	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if catalog := catalog.Catalog(); catalog != "" {
		t.Fatalf("catalog = %q, want empty", catalog)
	}
}
