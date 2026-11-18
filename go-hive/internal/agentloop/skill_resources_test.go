package agentloop

import (
	"reflect"
	"slices"
	"testing"

	"github.com/aden-hive/hive-go/internal/skills"
)

func makeParsedSkill(name, baseDir, sourceScope string) *skills.ParsedSkill {
	return &skills.ParsedSkill{
		Name:        name,
		Description: "Skill " + name,
		Location:    baseDir + "/SKILL.md",
		BaseDir:     baseDir,
		SourceScope: sourceScope,
		Body:        "Instructions.",
	}
}

// Parity: tests/test_skill_resources.py::test_allowlisted_dirs_matches_skills
func TestAllowlistedDirsMatchesSkills(t *testing.T) {
	catalog := skills.NewSkillCatalog([]*skills.ParsedSkill{
		makeParsedSkill("a", "/skills/a", "project"),
		makeParsedSkill("b", "/skills/b", "user"),
		makeParsedSkill("c", "/skills/c", "framework"),
	})
	dirs := catalog.AllowlistedDirs()

	for _, want := range []string{"/skills/a", "/skills/b", "/skills/c"} {
		if !slices.Contains(dirs, want) {
			t.Errorf("allowlisted dirs %v missing %q", dirs, want)
		}
	}
}

// Parity: tests/test_skill_resources.py::test_allowlisted_dirs_empty_catalog
func TestAllowlistedDirsEmptyCatalog(t *testing.T) {
	dirs := skills.NewSkillCatalog(nil).AllowlistedDirs()
	if !reflect.DeepEqual(dirs, []string{}) {
		t.Errorf("empty catalog allowlisted dirs = %#v, want empty non-nil slice", dirs)
	}
}

// Parity: tests/test_skill_resources.py::test_node_context_skill_dirs_default
func TestNodeContextSkillDirsDefault(t *testing.T) {
	ctx := NewSkillFileReadInterceptor(nil)
	if !reflect.DeepEqual(ctx.SkillDirs, []string{}) {
		t.Errorf("default skill dirs = %#v, want empty non-nil slice", ctx.SkillDirs)
	}
}

// Parity: tests/test_skill_resources.py::test_node_context_skill_dirs_set
func TestNodeContextSkillDirsSet(t *testing.T) {
	dirs := []string{"/skills/a", "/skills/b"}
	ctx := NewSkillFileReadInterceptor(dirs)
	if !reflect.DeepEqual(ctx.SkillDirs, dirs) {
		t.Errorf("skill dirs = %#v, want %#v", ctx.SkillDirs, dirs)
	}
}
