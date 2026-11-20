package colony

// Colony skill surface: the `skills/` folder a colony owns, the catalog workers
// see, and the read tools they use. Writing a skill is the queen's job (the
// session catalogue owns write_skill); a worker only reads, so this surface
// adds skill_view/list_skills and an allowlisted SkillDirs root for view_file.

import (
	"unicode/utf8"

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

// workerSkillsCatalogPrompt renders the colony catalog a spawning worker's
// prompt announces, straight off disk (the freshness a new worker always had),
// and primes the memo in-flight workers read. ReloadSkills refreshes the same
// memo when a skill changes while workers are running.
func (r *ColonyRuntime) workerSkillsCatalogPrompt() string {
	dir := r.colonySkillsDir()
	prompt := ""
	if dir != "" {
		prompt = skills.ScopedCatalogPrompt(dir, "colony")
	}
	r.storeSkillsCatalog(prompt)
	return prompt
}

// cachedSkillsCatalogPrompt is the memo an in-flight worker's dynamic provider
// reads each iteration. It starts at the spawn-time render and is swapped by
// ReloadSkills.
func (r *ColonyRuntime) cachedSkillsCatalogPrompt() string {
	r.skillsCatalogMu.RLock()
	defer r.skillsCatalogMu.RUnlock()
	return r.skillsCatalogPrompt
}

// storeSkillsCatalog publishes one rendered catalog memo.
func (r *ColonyRuntime) storeSkillsCatalog(prompt string) {
	r.skillsCatalogMu.Lock()
	r.skillsCatalogPrompt = prompt
	r.skillsCatalogMu.Unlock()
}

// ReloadSkills rebuilds the colony's catalog after a skill changed on disk.
// Every worker already reads the catalog through the dynamic provider its
// context was spawned with, so one reload reaches in-flight workers on their
// next iteration without a respawn. Returns the stats the HTTP fan-out echoes
// (catalog_chars, skill_dirs), mirroring ColonyRuntime.reload_skills upstream.
func (r *ColonyRuntime) ReloadSkills() (map[string]any, error) {
	if r == nil {
		return nil, nil
	}
	dir := r.colonySkillsDir()
	prompt := ""
	if dir != "" {
		prompt = skills.ScopedCatalogPrompt(dir, "colony")
	}
	// Publish even the empty result: a colony whose skills folder disappeared
	// must drop the stale catalog instead of keeping the last one.
	r.storeSkillsCatalog(prompt)
	stats := map[string]any{"catalog_chars": utf8.RuneCountInString(prompt)}
	if dir != "" {
		stats["skill_dirs"] = []string{dir}
	} else {
		stats["skill_dirs"] = []string{}
	}
	return stats, nil
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
