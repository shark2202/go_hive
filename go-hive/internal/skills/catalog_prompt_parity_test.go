package skills

// Prompt/skill-catalog parity: render the shared fixture through the same
// SkillsManager a queen chat session builds, canonicalize the machine-specific
// roots, and compare verbatim against the frozen golden text (which is also the
// Python reference's output — see go_hive/scripts/prompt_catalog_parity.py).
//
// Fixture: testdata/prompt-catalog/fixture.json (shared with the Python
// renderer go_hive/scripts/prompt_catalog_render.py).
//
// Regenerate the goldens (after a deliberate rendering change):
//
//	HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1 go test ./internal/skills -run TestPromptCatalogParityGolden
//
// Produce the capture directory the cross-language probe diffs:
//
//	HIVE_PROMPT_CATALOG_CAPTURE_DIR=/tmp/capture go test ./internal/skills -run TestPromptCatalogParityGolden

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const promptCatalogFixturePath = "testdata/prompt-catalog/fixture.json"
const promptCatalogGoldenDir = "testdata/prompt-catalog/golden"
const promptCatalogQueenID = "queen_growth"

type promptCatalogFixture struct {
	Description string                          `json:"description"`
	Profiles    map[string]promptCatalogProfile `json:"profiles"`
	Caps        []promptCatalogCap              `json:"caps"`
	CapsCompact []promptCatalogCap              `json:"caps_compact"`
}

type promptCatalogProfile struct {
	HiveSkills   map[string]promptCatalogSkill `json:"hive_skills"`
	QueenSkills  map[string]promptCatalogSkill `json:"queen_skills"`
	AgentsSkills map[string]promptCatalogSkill `json:"agents_skills"`
}

type promptCatalogSkill struct {
	Description    string         `json:"description"`
	Name           string         `json:"name"`
	Metadata       map[string]any `json:"metadata"`
	RawFrontmatter string         `json:"raw_frontmatter"`
	SymlinkTo      string         `json:"symlink_to"`
}

type promptCatalogCap struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func loadPromptCatalogFixture(t *testing.T) promptCatalogFixture {
	t.Helper()
	raw, err := os.ReadFile(promptCatalogFixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	fixture := promptCatalogFixture{}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return fixture
}

func writePromptCatalogSkill(t *testing.T, dir, dirName string, spec promptCatalogSkill) {
	t.Helper()
	frontmatter := spec.RawFrontmatter
	if frontmatter == "" {
		name := spec.Name
		if name == "" {
			name = dirName
		}
		lines := []string{"name: " + name, "description: " + strconv.Quote(spec.Description)}
		if len(spec.Metadata) > 0 {
			lines = append(lines, "metadata:")
			keys := make([]string, 0, len(spec.Metadata))
			for key := range spec.Metadata {
				keys = append(keys, key)
			}
			sortStrings(keys)
			for _, key := range keys {
				encoded, _ := json.Marshal(spec.Metadata[key])
				lines = append(lines, "  "+key+": "+string(encoded))
			}
		}
		frontmatter = strings.Join(lines, "\n")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "---\n" + frontmatter + "\n---\n\nBody for " + dirName + ".\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// materializePromptCatalogProfile writes one fixture profile as
// <root>/hive (HIVE_HOME) and <root>/home (HOME). Skills carrying
// “symlink_to“ are created as symlinks after every real skill exists (map
// order is random, and a link must not be written before its target).
func materializePromptCatalogProfile(t *testing.T, fixture promptCatalogFixture, profileName, root string) (home, hive string) {
	t.Helper()
	profile, ok := fixture.Profiles[profileName]
	if !ok {
		t.Fatalf("fixture profile %q missing", profileName)
	}
	home = filepath.Join(root, "home")
	hive = filepath.Join(root, "hive")
	type symlink struct {
		path   string
		target string
	}
	var links []symlink
	writeSection := func(base string, skills map[string]promptCatalogSkill) {
		for dirName, spec := range skills {
			dir := filepath.Join(base, dirName)
			if spec.SymlinkTo != "" {
				links = append(links, symlink{path: dir, target: spec.SymlinkTo})
				continue
			}
			writePromptCatalogSkill(t, dir, dirName, spec)
		}
	}
	writeSection(filepath.Join(hive, "skills"), profile.HiveSkills)
	writeSection(filepath.Join(hive, "queens", promptCatalogQueenID, "skills"), profile.QueenSkills)
	writeSection(filepath.Join(home, ".agents", "skills"), profile.AgentsSkills)
	for _, link := range links {
		if err := os.MkdirAll(filepath.Dir(link.path), 0o755); err != nil {
			t.Fatalf("mkdir symlink parent: %v", err)
		}
		if err := os.Symlink(link.target, link.path); err != nil {
			t.Fatalf("symlink %s -> %s: %v", link.path, link.target, err)
		}
	}
	if err := os.WriteFile(filepath.Join(hive, "configuration.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
	return home, hive
}

func promptCatalogManager(t *testing.T, home, hive string) *SkillsManager {
	t.Helper()
	queenHome := filepath.Join(hive, "queens", promptCatalogQueenID)
	manager := NewSkillsManager(&SkillsManagerConfig{
		QueenID:                promptCatalogQueenID,
		QueenOverridesPath:     filepath.Join(queenHome, "skills_overrides.json"),
		ExtraScopeDirs:         []ExtraScope{{Directory: filepath.Join(queenHome, "skills"), Label: "queen_ui", Priority: 2}},
		ProjectRoot:            "",
		SkipCommunityDiscovery: true,
		Interactive:            false,
		HomeDir:                home,
		HiveHome:               hive,
	})
	manager.Load()
	return manager
}

func canonicalizePromptCatalog(text string, roots [][2]string) string {
	for _, pair := range roots {
		if pair[1] != "" {
			text = strings.ReplaceAll(text, pair[1], pair[0])
		}
	}
	return text
}

func renderPromptCatalogProfile(t *testing.T, fixture promptCatalogFixture, profileName string) map[string]string {
	t.Helper()
	home, hive := materializePromptCatalogProfile(t, fixture, profileName, t.TempDir())
	manager := promptCatalogManager(t, home, hive)
	bundled, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundled skills root: %v", err)
	}
	// Resolve the roots first: ParsedSkill.Location mirrors Python's
	// Path.resolve(), so on macOS (/var -> /private/var, /tmp -> /private/tmp)
	// the catalog text carries the resolved path and the replacement must
	// target it before the raw temp path.
	roots := [][2]string{}
	for _, pair := range [][2]string{{"<BUNDLED>", bundled}, {"<HIVE_HOME>", hive}, {"<HOME>", home}} {
		if resolved, resolveErr := filepath.EvalSymlinks(pair[1]); resolveErr == nil && resolved != pair[1] {
			roots = append(roots, [2]string{pair[0], resolved})
		}
		roots = append(roots, pair)
	}

	out := map[string]string{}
	out[profileName+".none"] = canonicalizePromptCatalog(manager.SkillsCatalogPrompt(), roots)
	for _, phase := range []string{"independent", "colony"} {
		value := phase
		out[profileName+"."+phase] = canonicalizePromptCatalog(manager.SkillsCatalogPromptForPhase(&value), roots)
	}
	return out
}

func renderPromptCatalogCaps(t *testing.T, fixture promptCatalogFixture) map[string]string {
	t.Helper()
	render := func(entries []promptCatalogCap) string {
		skills := make([]*ParsedSkill, 0, len(entries))
		for _, entry := range entries {
			location := path.Join("/fixture/skills", entry.Name, "SKILL.md")
			skills = append(skills, &ParsedSkill{
				Name:        entry.Name,
				Description: entry.Description,
				Location:    location,
				BaseDir:     path.Join("/fixture/skills", entry.Name),
				SourceScope: "project",
				Body:        "Body.",
			})
		}
		return NewSkillCatalog(skills).ToPrompt(nil)
	}
	return map[string]string{
		"caps.none":    render(fixture.Caps),
		"caps.compact": render(fixture.CapsCompact),
	}
}

// Parity-exempt: golden fixtures are captured from the Python side by
// scripts/prompt_catalog_parity.py; this test replays them against Go catalog
// rendering (phase filtering, truncation, ordering) as a whole.
func TestPromptCatalogParityGolden(t *testing.T) {
	fixture := loadPromptCatalogFixture(t)

	rendered := map[string]string{}
	for _, profileName := range []string{"small", "edge"} {
		for name, text := range renderPromptCatalogProfile(t, fixture, profileName) {
			rendered[name] = text
		}
	}
	for name, text := range renderPromptCatalogCaps(t, fixture) {
		rendered[name] = text
	}

	if captureDir := strings.TrimSpace(os.Getenv("HIVE_PROMPT_CATALOG_CAPTURE_DIR")); captureDir != "" {
		if err := os.MkdirAll(captureDir, 0o755); err != nil {
			t.Fatalf("capture dir: %v", err)
		}
		for name, text := range rendered {
			if err := os.WriteFile(filepath.Join(captureDir, name+".txt"), []byte(text), 0o644); err != nil {
				t.Fatalf("capture %s: %v", name, err)
			}
		}
	}

	for name, text := range rendered {
		goldenPath := filepath.Join(promptCatalogGoldenDir, name+".txt")
		if os.Getenv("HIVE_PROMPT_CATALOG_WRITE_GOLDEN") == "1" {
			if err := os.MkdirAll(promptCatalogGoldenDir, 0o755); err != nil {
				t.Fatalf("golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, []byte(text), 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}
		golden, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v (regenerate with HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1)", name, err)
		}
		if text != string(golden) {
			t.Fatalf("catalog render %s diverged from golden:\n%s", name, firstPromptCatalogDiff(string(golden), text))
		}
	}
}

// firstPromptCatalogDiff reports the first differing line pair, for a readable
// failure without dumping two multi-KB blobs.
func firstPromptCatalogDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		wantLine, gotLine := "", ""
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine != gotLine {
			return "line " + strconv.Itoa(i+1) + ":\n  want: " + strconv.Quote(wantLine) + "\n  got:  " + strconv.Quote(gotLine)
		}
	}
	return "(no line diff; likely trailing newline)"
}
