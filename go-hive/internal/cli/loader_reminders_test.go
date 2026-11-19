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
