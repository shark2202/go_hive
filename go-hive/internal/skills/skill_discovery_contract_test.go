package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The contract is checked in at the repository root; go test runs with the
// package directory as the working directory, so walk up until it is found.
// HIVE_SKILL_DISCOVERY_CONTRACT overrides the lookup.
const skillDiscoveryContractEnv = "HIVE_SKILL_DISCOVERY_CONTRACT"

type skillDiscoveryContract struct {
	Schema      int                  `json:"schema"`
	Scopes      []skillContractScope `json:"scopes"`
	Scan        skillContractScan    `json:"scan"`
	Description string               `json:"description"`
	Generator   string               `json:"generator"`
}

type skillContractScope struct {
	Name           string              `json:"name"`
	Priority       int                 `json:"priority"`
	DefaultEnabled bool                `json:"default_enabled"`
	Roots          []skillContractRoot `json:"roots"`
}

type skillContractRoot struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Note string `json:"note"`
}

type skillContractScan struct {
	SkillMarker             string   `json:"skill_marker"`
	MaxDepth                int      `json:"max_depth"`
	MaxDirsPerScope         int      `json:"max_dirs_per_scope"`
	EntryOrder              string   `json:"entry_order"`
	SkipDirs                []string `json:"skip_dirs"`
	FollowSymlinkedDirs     bool     `json:"follow_symlinked_dirs"`
	StopDescendAtMarker     bool     `json:"stop_descend_at_marker"`
	Collision               string   `json:"collision"`
	SameScopeOrder          string   `json:"same_scope_order"`
	OverridesFileIsNotARoot string   `json:"overrides_file_is_not_a_root"`
}

func loadSkillDiscoveryContract(t *testing.T) skillDiscoveryContract {
	t.Helper()
	if override := os.Getenv(skillDiscoveryContractEnv); override != "" {
		return parseSkillDiscoveryContract(t, override)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; {
		for _, rel := range []string{"contracts/skill-discovery.json", "go_hive/contracts/skill-discovery.json"} {
			candidate := filepath.Join(dir, rel)
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
				return parseSkillDiscoveryContract(t, candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("cannot locate contracts/skill-discovery.json above %s (set %s to override)", wd, skillDiscoveryContractEnv)
	return skillDiscoveryContract{}
}

func parseSkillDiscoveryContract(t *testing.T, path string) skillDiscoveryContract {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var contract skillDiscoveryContract
	if err := decoder.Decode(&contract); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return contract
}

// Parity-exempt: the machine-readable discovery contract is a Go/Python
// cross-check added after the audit; upstream has no contract file.
func TestSkillDiscoveryContractMatchesConstants(t *testing.T) {
	contract := loadSkillDiscoveryContract(t)
	if contract.Schema != 1 {
		t.Fatalf("contract schema = %d, want 1", contract.Schema)
	}

	expectedOrder := []string{"framework", "preset", "user", "queen_ui", "colony_ui", "project"}
	gotOrder := make([]string, 0, len(contract.Scopes))
	priorities := map[string]int{}
	for _, scope := range contract.Scopes {
		gotOrder = append(gotOrder, scope.Name)
		priorities[scope.Name] = scope.Priority
		if len(scope.Roots) == 0 {
			t.Fatalf("scope %q has no roots", scope.Name)
		}
		if scope.Name == "preset" && scope.DefaultEnabled {
			t.Fatal("preset must be disabled by default")
		}
		for _, root := range scope.Roots {
			if strings.TrimSpace(root.Kind) == "" || strings.TrimSpace(root.Path) == "" {
				t.Fatalf("scope %q has an empty root kind/path: %#v", scope.Name, root)
			}
		}
	}
	if !reflect.DeepEqual(gotOrder, expectedOrder) {
		t.Fatalf("contract scopes = %v, want %v", gotOrder, expectedOrder)
	}
	if !reflect.DeepEqual(priorities, ScopePriority) {
		t.Fatalf("contract priorities = %v, Go ScopePriority = %v", priorities, ScopePriority)
	}

	if contract.Scan.SkillMarker != "SKILL.md" {
		t.Fatalf("contract skill_marker = %q", contract.Scan.SkillMarker)
	}
	if contract.Scan.MaxDepth != 4 || contract.Scan.MaxDirsPerScope != 2000 {
		t.Fatalf("contract limits = depth %d / dirs %d, want 4 / 2000", contract.Scan.MaxDepth, contract.Scan.MaxDirsPerScope)
	}
	defaults := NewSkillDiscovery(DiscoveryConfig{})
	if defaults.config.MaxDepth != contract.Scan.MaxDepth || defaults.config.MaxDirs != contract.Scan.MaxDirsPerScope {
		t.Fatalf("Go defaults = depth %d / dirs %d, contract says %d / %d",
			defaults.config.MaxDepth, defaults.config.MaxDirs, contract.Scan.MaxDepth, contract.Scan.MaxDirsPerScope)
	}
	if contract.Scan.EntryOrder != "name_ascending" {
		t.Fatalf("contract entry_order = %q", contract.Scan.EntryOrder)
	}
	if !contract.Scan.FollowSymlinkedDirs || !contract.Scan.StopDescendAtMarker {
		t.Fatalf("contract must follow symlinked dirs and stop at the marker: %#v", contract.Scan)
	}
	if contract.Scan.Collision != "last_wins" {
		t.Fatalf("contract collision = %q", contract.Scan.Collision)
	}
	if contract.Scan.SameScopeOrder != "cross_client_before_hive_specific" {
		t.Fatalf("contract same_scope_order = %q", contract.Scan.SameScopeOrder)
	}
	if contract.Scan.OverridesFileIsNotARoot != "skills_overrides.json" {
		t.Fatalf("contract overrides_file_is_not_a_root = %q", contract.Scan.OverridesFileIsNotARoot)
	}

	contractSkip := append([]string(nil), contract.Scan.SkipDirs...)
	goSkip := make([]string, 0, len(skipDirs))
	for name := range skipDirs {
		goSkip = append(goSkip, name)
	}
	sort.Strings(contractSkip)
	sort.Strings(goSkip)
	if !reflect.DeepEqual(contractSkip, goSkip) {
		t.Fatalf("contract skip_dirs = %v, Go skipDirs = %v", contractSkip, goSkip)
	}

	wantKinds := map[string][]string{
		"framework": {"bundled"},
		"preset":    {"bundled"},
		"user":      {"home", "external_skills", "hive_home"},
		"queen_ui":  {"extra_scope"},
		"colony_ui": {"extra_scope", "extra_scope"},
		"project":   {"project", "project"},
	}
	for _, scope := range contract.Scopes {
		kinds := make([]string, 0, len(scope.Roots))
		for _, root := range scope.Roots {
			kinds = append(kinds, root.Kind)
		}
		if !reflect.DeepEqual(kinds, wantKinds[scope.Name]) {
			t.Fatalf("scope %q root kinds = %v, want %v", scope.Name, kinds, wantKinds[scope.Name])
		}
	}
}

func writeContractSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: contract fixture\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Parity-exempt: cross-implementation scan-order pin for the contract; Python
// validates the same order in core/tests/test_skill_discovery_contract.py.
func TestSkillDiscoveryContractRootOrder(t *testing.T) {
	userHome := t.TempDir()
	hiveHome := t.TempDir()
	t.Setenv("HIVE_HOME", hiveHome)

	userAgents := filepath.Join(userHome, ".agents", "skills")
	writeContractSkill(t, userAgents, "user-cross")
	external := t.TempDir()
	writeContractSkill(t, external, "external")
	hiveSkills := filepath.Join(hiveHome, "skills")
	writeContractSkill(t, hiveSkills, "user-hive")
	queen := filepath.Join(hiveHome, "queens", "queen_growth", "skills")
	writeContractSkill(t, queen, "queen-skill")
	colony := filepath.Join(hiveHome, "colonies", "probe", "skills")
	writeContractSkill(t, colony, "colony-skill")
	colonyLegacy := filepath.Join(hiveHome, "colonies", "probe", ".hive", "skills")
	writeContractSkill(t, colonyLegacy, "colony-legacy")
	project := t.TempDir()
	projectAgents := filepath.Join(project, ".agents", "skills")
	writeContractSkill(t, projectAgents, "project-cross")
	projectHive := filepath.Join(project, ".hive", "skills")
	writeContractSkill(t, projectHive, "project-hive")

	discovery := NewSkillDiscovery(DiscoveryConfig{
		ProjectRoot:        project,
		HomeDir:            userHome,
		HiveHome:           hiveHome,
		SkipFrameworkScope: true,
		ExternalSkills:     []string{external},
		ExtraScopes: []ExtraScope{
			{Directory: queen, Label: "queen_ui", Priority: 3},
			{Directory: colony, Label: "colony_ui", Priority: 4},
			{Directory: colonyLegacy, Label: "colony_ui", Priority: 4},
		},
	})
	found := discovery.Discover()
	dirs := discovery.ScannedDirectories()
	if len(dirs) != 8 {
		t.Fatalf("scanned %d directories, want 8: %v", len(dirs), dirs)
	}
	wantTail := []string{userAgents, external, hiveSkills, queen, colony, colonyLegacy, projectAgents, projectHive}
	if !reflect.DeepEqual(dirs, wantTail) {
		t.Fatalf("scan order = %v, want %v", dirs, wantTail)
	}
	if len(found) != 8 {
		t.Fatalf("found %d skills, want 8", len(found))
	}
	for _, want := range []string{"queen-skill", "colony-skill", "colony-legacy", "project-hive"} {
		foundName := false
		for _, skill := range found {
			if skill.Name == want {
				foundName = true
			}
		}
		if !foundName {
			t.Fatalf("skill %q was not discovered", want)
		}
	}
}

// Parity: core/framework/skills/discovery.py::SkillDiscovery._find_skill_files
//
// Pins the scan rules the contract records: sorted entries, skipped dirs, a
// SKILL.md stops descent, and max_depth 4 counts subdirectory levels.
func TestSkillDiscoveryContractScanRules(t *testing.T) {
	root := t.TempDir()
	writeContractSkill(t, root, "a")
	writeContractSkill(t, filepath.Join(root, "a"), "nested") // under a marker: not scanned
	writeContractSkill(t, filepath.Join(root, ".git", "hidden"), "hidden")
	writeContractSkill(t, filepath.Join(root, "b", "c", "d", "e"), "deep")
	// Six levels below the root: recursion is cut at depth 4.
	writeContractSkill(t, filepath.Join(root, "x", "y", "z", "w", "v", "u"), "too-deep")

	discovery := NewSkillDiscovery(DiscoveryConfig{})
	files := discovery.findSkillFiles(root, 0)
	want := []string{
		filepath.Join(root, "a", "SKILL.md"),
		filepath.Join(root, "b", "c", "d", "e", "deep", "SKILL.md"),
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("findSkillFiles = %v, want %v", files, want)
	}
}
