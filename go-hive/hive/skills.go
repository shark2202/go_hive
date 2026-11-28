package hive

import (
	"os"

	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// Skill is one parsed skill: name, description, location and body.
//
// Parity: framework/skills/parser.py::ParsedSkill
type Skill = skills.ParsedSkill

// ListSkills discovers every skill visible to this Hive home: framework
// defaults, presets, the user scope and project scopes.
//
// Parity: framework/skills/discovery.py::SkillDiscovery.discover
func ListSkills(home Home) ([]*Skill, error) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	discovery := skills.NewSkillDiscovery(skills.DiscoveryConfig{
		HomeDir:  userHome,
		HiveHome: home.Root,
	})
	return discovery.Discover(), nil
}

// ParseSkillFile parses one SKILL.md file. scope labels the source scope
// ("user", "framework", "colony_ui", ...).
//
// Parity: framework/skills/parser.py::parse_skill_md
func ParseSkillFile(path, scope string) *Skill {
	return skills.ParseSkillMD(path, scope)
}

// SkillCatalog is an ordered, de-duplicated skill set that renders the model
// catalog prompt.
//
// Parity: framework/skills/catalog.py::SkillCatalog
type SkillCatalog struct {
	inner *skills.SkillCatalog
}

// NewSkillCatalog builds a catalog over the given skills.
//
// Parity: framework/skills/catalog.py::SkillCatalog
func NewSkillCatalog(list []*Skill) *SkillCatalog {
	return &SkillCatalog{inner: skills.NewSkillCatalog(list)}
}

// Get returns one skill by name.
//
// Parity: framework/skills/catalog.py::SkillCatalog.get
func (c *SkillCatalog) Get(name string) (*Skill, bool) {
	if c == nil || c.inner == nil {
		return nil, false
	}
	entry := c.inner.Get(name)
	if entry == nil {
		return nil, false
	}
	return entry, true
}

// Count is the number of skills in the catalog.
//
// Parity: framework/skills/catalog.py::SkillCatalog.skill_count
func (c *SkillCatalog) Count() int {
	if c == nil || c.inner == nil {
		return 0
	}
	return c.inner.SkillCount()
}

// Prompt renders the catalog block injected into an agent's system prompt.
// An empty phase renders the phase-neutral catalog.
//
// Parity: framework/skills/catalog.py::SkillCatalog.to_prompt
func (c *SkillCatalog) Prompt(phase string) string {
	if c == nil || c.inner == nil {
		return ""
	}
	if phase == "" {
		return c.inner.ToPrompt(nil)
	}
	return c.inner.ToPrompt(&phase)
}

// AllowlistedDirs lists the directories a skill-scoped file read may touch.
//
// Parity: framework/skills/tool_gating.py
func (c *SkillCatalog) AllowlistedDirs() []string {
	if c == nil || c.inner == nil {
		return nil
	}
	return c.inner.AllowlistedDirs()
}
