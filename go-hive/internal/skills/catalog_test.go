package skills

import (
	"fmt"
	"strings"
	"testing"
)

func catalogSkill(name, description, scope, body, location, baseDir string) *ParsedSkill {
	return &ParsedSkill{
		Name:        name,
		Description: description,
		Location:    location,
		BaseDir:     baseDir,
		SourceScope: scope,
		Body:        body,
	}
}

func defaultCatalogSkill(name, description, scope string) *ParsedSkill {
	return catalogSkill(name, description, scope, "Instructions here.",
		"/tmp/skills/my-skill/SKILL.md", "/tmp/skills/my-skill")
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_add_and_get
func TestCatalogAddAndGet(t *testing.T) {
	catalog := NewSkillCatalog(nil)
	skill := defaultCatalogSkill("my-skill", "A test skill.", "project")
	catalog.Add(skill)

	if got := catalog.Get("my-skill"); got != skill {
		t.Fatalf("get returned %+v", got)
	}
	if got := catalog.Get("nonexistent"); got != nil {
		t.Fatalf("get(nonexistent) = %+v", got)
	}
	if catalog.SkillCount() != 1 {
		t.Fatalf("skill_count = %d", catalog.SkillCount())
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_init_with_skills_list
func TestCatalogInitWithSkillsList(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{
		defaultCatalogSkill("a", "Skill A", "project"),
		defaultCatalogSkill("b", "Skill B", "project"),
	})
	if catalog.SkillCount() != 2 {
		t.Fatalf("skill_count = %d", catalog.SkillCount())
	}
	if catalog.Get("a") == nil || catalog.Get("b") == nil {
		t.Fatalf("missing skills")
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_allowlisted_dirs
func TestCatalogAllowlistedDirs(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{
		catalogSkill("a", "A test skill.", "project", "Body", "/skills/a/SKILL.md", "/skills/a"),
		catalogSkill("b", "A test skill.", "project", "Body", "/skills/b/SKILL.md", "/skills/b"),
	})
	dirs := strings.Join(catalog.AllowlistedDirs(), "\x00")
	if !strings.Contains(dirs, "/skills/a") || !strings.Contains(dirs, "/skills/b") {
		t.Fatalf("dirs = %v", catalog.AllowlistedDirs())
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_empty_catalog
func TestCatalogToPromptEmpty(t *testing.T) {
	if prompt := NewSkillCatalog(nil).ToPrompt(nil); prompt != "" {
		t.Fatalf("prompt = %q", prompt)
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_framework_only
func TestCatalogToPromptFrameworkOnly(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{defaultCatalogSkill("my-skill", "A test skill.", "framework")})
	prompt := catalog.ToPrompt(nil)
	if !strings.Contains(prompt, "<available_skills>") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "<name>my-skill</name>") {
		t.Fatalf("prompt = %q", prompt)
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_xml_generation
func TestCatalogToPromptXMLGeneration(t *testing.T) {
	alpha := catalogSkill("alpha", "Alpha skill", "project", "Body", "/p/alpha/SKILL.md", "/p/alpha")
	beta := catalogSkill("beta", "Beta skill", "user", "Body", "/u/beta/SKILL.md", "/tmp/skills/my-skill")
	catalog := NewSkillCatalog([]*ParsedSkill{alpha, beta})
	prompt := catalog.ToPrompt(nil)

	for _, want := range []string{
		"<available_skills>", "</available_skills>",
		"<name>alpha</name>", "<name>beta</name>",
		"<description>Alpha skill</description>",
		"<location>/p/alpha/SKILL.md</location>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "<base_dir>") {
		t.Fatalf("prompt should not emit <base_dir>:\n%s", prompt)
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_sorted_by_name
func TestCatalogToPromptSortedByName(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{
		defaultCatalogSkill("zebra", "Z skill", "project"),
		defaultCatalogSkill("alpha", "A skill", "project"),
	})
	prompt := catalog.ToPrompt(nil)
	if strings.Index(prompt, "alpha") >= strings.Index(prompt, "zebra") {
		t.Fatalf("prompt not sorted:\n%s", prompt)
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_xml_escaping
func TestCatalogToPromptXMLEscaping(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{
		defaultCatalogSkill("test", `Has <special> & "chars"`, "project"),
	})
	prompt := catalog.ToPrompt(nil)
	if !strings.Contains(prompt, "&lt;special&gt;") || !strings.Contains(prompt, "&amp;") {
		t.Fatalf("prompt = %q", prompt)
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_includes_all_scopes
func TestCatalogToPromptIncludesAllScopes(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{
		defaultCatalogSkill("proj", "Project skill", "project"),
		defaultCatalogSkill("usr", "User skill", "user"),
		defaultCatalogSkill("fw", "Framework skill", "framework"),
	})
	prompt := catalog.ToPrompt(nil)
	for _, want := range []string{"<name>proj</name>", "<name>usr</name>", "<name>fw</name>"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_contains_mandatory_header
func TestCatalogToPromptContainsMandatoryHeader(t *testing.T) {
	catalog := NewSkillCatalog([]*ParsedSkill{defaultCatalogSkill("my-skill", "A test skill.", "project")})
	prompt := catalog.ToPrompt(nil)
	for _, want := range []string{
		"## Skills (mandatory)",
		"Before replying: scan <available_skills>",
		"never read more than one skill up front",
		"terminal_exec",
		"SKILL.md",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_to_prompt_compact_fallback_drops_descriptions
func TestCatalogToPromptCompactFallbackDropsDescriptions(t *testing.T) {
	skills := make([]*ParsedSkill, 0, 60)
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("skill-%03d", i)
		skills = append(skills, catalogSkill(
			name,
			strings.Repeat("A reasonably long description ", 4),
			"project",
			"Body",
			"/s/"+name+"/SKILL.md",
			"/s/"+name,
		))
	}
	catalog := NewSkillCatalog(skills)
	prompt := catalog.ToPrompt(nil)

	for _, want := range []string{"## Skills (mandatory)", "scan <available_skills> <name>"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	for i := 0; i < 60; i++ {
		if !strings.Contains(prompt, fmt.Sprintf("<name>skill-%03d</name>", i)) {
			t.Fatalf("missing skill-%03d in compact prompt", i)
		}
	}
	if strings.Contains(prompt, "<description>") {
		t.Fatalf("compact prompt should not render descriptions")
	}
}

// Parity: tests/test_skill_catalog.py::TestSkillCatalog::test_duplicate_add_overwrites
func TestCatalogDuplicateAddOverwrites(t *testing.T) {
	catalog := NewSkillCatalog(nil)
	catalog.Add(defaultCatalogSkill("x", "First", "project"))
	catalog.Add(defaultCatalogSkill("x", "Second", "project"))

	if catalog.SkillCount() != 1 {
		t.Fatalf("skill_count = %d", catalog.SkillCount())
	}
	if got := catalog.Get("x").Description; got != "Second" {
		t.Fatalf("description = %q", got)
	}
}

// Parity-exempt: phase-based catalog visibility is an untested Python path.
func TestCatalogToPromptPhaseVisibility(t *testing.T) {
	always := defaultCatalogSkill("always", "All phases", "project")
	buildOnly := defaultCatalogSkill("build-only", "Build phase", "project")
	phase := "build"
	buildOnly.Visibility = []string{phase}
	catalog := NewSkillCatalog([]*ParsedSkill{always, buildOnly})

	buildPrompt := catalog.ToPrompt(&phase)
	if !strings.Contains(buildPrompt, "<name>always</name>") || !strings.Contains(buildPrompt, "<name>build-only</name>") {
		t.Fatalf("build prompt = %q", buildPrompt)
	}
	other := "plan"
	planPrompt := catalog.ToPrompt(&other)
	if !strings.Contains(planPrompt, "<name>always</name>") {
		t.Fatalf("plan prompt = %q", planPrompt)
	}
	if strings.Contains(planPrompt, "<name>build-only</name>") {
		t.Fatalf("plan prompt should hide build-only:\n%s", planPrompt)
	}
}

// Parity-exempt: the description-cap helper has no direct Python test.
func TestCapDescription(t *testing.T) {
	if got := CapDescription("Short sentence. Second sentence."); got != "Short sentence." {
		t.Fatalf("got = %q", got)
	}
	long := strings.Repeat("x", 400)
	got := CapDescription(long)
	if len([]rune(got)) != DescriptionCapChars {
		t.Fatalf("len = %d, want %d", len([]rune(got)), DescriptionCapChars)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix: %q", got)
	}
}
