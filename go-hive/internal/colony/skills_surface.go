package colony

// Colony skill surface: the `skills/` folders a worker sees (the colony's own
// plus the owning queen's), the catalog workers read, and the read tools they
// use. Writing a skill is the queen's job (the session catalogue owns
// write_skill); a worker only reads, so this surface adds skill_view/list_skills
// and an allowlisted SkillDirs root for view_file.

import (
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// colonySkillsDir returns the colony's skills/ directory when it exists.
func (r *ColonyRuntime) colonySkillsDir() string {
	if r == nil {
		return ""
	}
	return skills.ExistingColonySkillsDir(r.storagePath)
}

// queenSkillsDir returns the owning queen's skills/ directory when she is
// known and the directory exists. Upstream a queen's skill scope is visible to
// every colony she owns, so her colonies' workers see it too
// (framework/host/colony_runtime.py::_build_default_skills_config wires the
// queen_ui scope into the colony runtime; 07-memory-and-skills.md §4.2).
func (r *ColonyRuntime) queenSkillsDir() string {
	if r == nil {
		return ""
	}
	return skills.ExistingSkillsDir(r.queenDir)
}

// workerSkillScopes is the ordered skill scope list a worker (and the colony's
// own read surface) sees: the owning queen's scope first, then the colony's
// own. Discovery resolves later scopes over earlier ones, so a colony skill
// overrides the queen's copy of the same name — the upstream scan order in
// _build_default_skills_config (queen_ui, then colony_ui).
func (r *ColonyRuntime) workerSkillScopes() []skills.ExtraScope {
	if r == nil {
		return nil
	}
	scopes := make([]skills.ExtraScope, 0, 2)
	if dir := r.queenSkillsDir(); dir != "" {
		scopes = append(scopes, skills.ExtraScope{Directory: dir, Label: "queen_ui", Priority: 2})
	}
	if dir := r.colonySkillsDir(); dir != "" {
		scopes = append(scopes, skills.ExtraScope{Directory: dir, Label: "colony_ui", Priority: 3})
	}
	return scopes
}

// colonySkills returns the skills the colony sees for the read routes (the
// colony's own scope plus the owning queen's). It is only populated when at
// least one of those folders exists; false mirrors upstream's absent
// skills_manager.
func (r *ColonyRuntime) colonySkills() ([]*skills.ParsedSkill, bool) {
	scopes := r.workerSkillScopes()
	if len(scopes) == 0 {
		return nil, false
	}
	discovery := skills.NewSkillDiscovery(skills.DiscoveryConfig{
		SkipFrameworkScope: true,
		SkipUserScope:      true,
		ExtraScopes:        scopes,
	})
	return discovery.Discover(), true
}

// workerSkillDirs is the Tier-3 read allowlist a worker's view_file uses: the
// colony's skills/ folder plus the owning queen's skills/ folder. The queen
// scope is visible to her colonies' workers upstream (queen_ui in
// _build_default_skills_config); a queen the colony does not belong to is
// never exposed.
func (r *ColonyRuntime) workerSkillDirs() []string {
	dirs := make([]string, 0, 2)
	if dir := r.colonySkillsDir(); dir != "" {
		dirs = append(dirs, dir)
	}
	if dir := r.queenSkillsDir(); dir != "" {
		dirs = append(dirs, dir)
	}
	return dirs
}

// workerSkillsCatalogPrompt renders the catalog a spawning worker's prompt
// announces: the owning queen's skills overridden by the colony's own, straight
// off disk (the freshness a new worker always had). It primes the memo
// in-flight workers read; ReloadSkills refreshes the same memo when a skill
// changes while workers are running.
func (r *ColonyRuntime) workerSkillsCatalogPrompt() string {
	prompt := skills.ScopedCatalogPromptForScopes(r.workerSkillScopes())
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
	prompt := r.workerSkillsCatalogPrompt()
	// Publish even the empty result: a colony whose skill folders disappeared
	// must drop the stale catalog instead of keeping the last one.
	stats := map[string]any{"catalog_chars": utf8.RuneCountInString(prompt)}
	dirs := r.workerSkillDirs()
	if dirs == nil {
		dirs = []string{}
	}
	stats["skill_dirs"] = dirs
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
