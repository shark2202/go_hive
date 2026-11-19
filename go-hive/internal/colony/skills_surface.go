package colony

// Colony skill surface: the `skills/` folder a colony owns, the catalog workers
// see, and the read tools they use. Writing a skill is the queen's job (the
// session catalogue owns write_skill); a worker only reads, so this surface
// adds skill_view/list_skills and an allowlisted SkillDirs root for view_file.

import (
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/skills"
)

// colonySkillsDir returns the colony's skills/ directory when it exists.
func (r *ColonyRuntime) colonySkillsDir() string {
	if r == nil {
		return ""
	}
	return skills.ExistingColonySkillsDir(r.storagePath)
}

// colonySkills returns the colony catalog for the read routes. It is only
// populated when the colony actually has a skills/ directory; false mirrors
// upstream's absent skills_manager.
func (r *ColonyRuntime) colonySkills() ([]*skills.ParsedSkill, bool) {
	dir := r.colonySkillsDir()
	if dir == "" {
		return nil, false
	}
	discovery := skills.NewSkillDiscovery(skills.DiscoveryConfig{
		SkipFrameworkScope: true,
		SkipUserScope:      true,
		ExtraScopes:        []skills.ExtraScope{{Directory: dir, Label: "colony"}},
	})
	return discovery.Discover(), true
}

// workerSkillDirs is the Tier-3 read allowlist a worker's view_file uses. It
// only names the colony folder: a worker must not read the queen's private
// skills through a filesystem path.
func (r *ColonyRuntime) workerSkillDirs() []string {
	dir := r.colonySkillsDir()
	if dir == "" {
		return nil
	}
	return []string{dir}
}

func (r *ColonyRuntime) workerSkillsCatalogPrompt() string {
	return skills.ScopedCatalogPrompt(r.colonySkillsDir(), "colony")
}

// withoutTool removes one tool definition by name, keeping order otherwise.
func withoutTool(defs []llm.ToolDef, name string) []llm.ToolDef {
	for i, def := range defs {
		if def.Name != name {
			continue
		}
		out := make([]llm.ToolDef, 0, len(defs)-1)
		out = append(out, defs[:i]...)
		out = append(out, defs[i+1:]...)
		return out
	}
	return defs
}
