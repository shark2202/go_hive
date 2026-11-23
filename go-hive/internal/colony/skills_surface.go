package colony

// Colony skill surface: the `skills/` folders a worker sees (framework, preset,
// user, the owning queen's and the colony's own), the catalog workers read, and
// the read tools they use. Writing a skill is the queen's job (the session
// catalogue owns write_skill); a worker only reads, so this surface adds
// skill_view/list_skills and an allowlisted SkillDirs root for view_file.
//
// Parity: framework/host/colony_runtime.py::_build_default_skills_config
// Parity: framework/skills/manager.py::_apply_overrides

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// colonySkillsPhase is the queen phase a bound colony renders its worker
// catalog for. Upstream filters the same manager prompt with "colony" so a
// worker sees what its overseer does: metadata.visibility absent or containing
// "colony" stays, [independent] and [worker] drop out
// (colony_runtime.py::skills_catalog_prompt).
const colonySkillsPhase = "colony"

// colonySkillsDir returns the colony's flat skills/ directory when it exists.
func (r *ColonyRuntime) colonySkillsDir() string {
	if r == nil {
		return ""
	}
	return skills.ExistingColonySkillsDir(r.storagePath)
}

// colonyLegacySkillsDir returns the pre-flatten `colonies/<name>/.hive/skills`
// directory when it exists. Upstream surfaces it as a second colony_ui scope so
// colonies created before the flat layout still resolve with correct
// provenance (_build_default_skills_config).
func (r *ColonyRuntime) colonyLegacySkillsDir() string {
	if r == nil || strings.TrimSpace(r.storagePath) == "" {
		return ""
	}
	return skills.ExistingSkillsDir(filepath.Join(r.storagePath, ".hive"))
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

// workerSkillScopes is the ordered extra scope list a worker (and the colony's
// own read surface) sees beyond framework/preset/user: the owning queen's
// scope, then the colony's own flat and legacy scopes. Discovery resolves later
// scopes over earlier ones, so a colony skill overrides the queen's copy of the
// same name and the legacy layout overrides the flat one on a collision — the
// upstream scan order in _build_default_skills_config.
func (r *ColonyRuntime) workerSkillScopes() []skills.ExtraScope {
	if r == nil {
		return nil
	}
	scopes := make([]skills.ExtraScope, 0, 3)
	if dir := r.queenSkillsDir(); dir != "" {
		scopes = append(scopes, skills.ExtraScope{Directory: dir, Label: "queen_ui", Priority: 2})
	}
	if dir := r.colonySkillsDir(); dir != "" {
		scopes = append(scopes, skills.ExtraScope{Directory: dir, Label: "colony_ui", Priority: 3})
	}
	if dir := r.colonyLegacySkillsDir(); dir != "" {
		scopes = append(scopes, skills.ExtraScope{Directory: dir, Label: "colony_ui", Priority: 3})
	}
	return scopes
}

// defaultPresetSkillsForQueenDir resolves the role-default preset skills of the
// queen that owns a colony: the embedded profile's list (DefaultSkillsFor) plus
// the on-disk profile's `default_preset_skills` (a custom queen). No queen dir,
// an unknown/deleted queen, or a profile without the key yields an empty set —
// the same empty frozenset _build_default_skills_config produces for an unknown
// queen.
//
// Parity: framework/host/colony_runtime.py::_build_default_skills_config
// Parity: framework/agents/queen/queen_profiles.py::default_skills_for
func defaultPresetSkillsForQueenDir(queenDir string) map[string]struct{} {
	out := map[string]struct{}{}
	queenDir = strings.TrimSpace(queenDir)
	if queenDir == "" {
		return out
	}
	clean := filepath.Clean(queenDir)
	queenID := filepath.Base(clean)
	if !queen.ValidQueenID(queenID) {
		return out
	}
	for name := range queen.DefaultSkillsFor(queenID) {
		out[name] = struct{}{}
	}
	profile, err := queen.LoadQueenProfile(filepath.Dir(clean), queenID)
	if err != nil {
		return out
	}
	for name := range queen.PresetSkillsFromProfile(profile) {
		out[name] = struct{}{}
	}
	return out
}

// workerSkillSurface is one consistent view of the worker skill hierarchy:
// the catalog entries after preset/default overrides, the rendered catalog
// prompt filtered for the colony phase, and the roots the view_file
// interceptor may read. Rendering both from one discovery keeps the announced
// catalog, the read allowlist, and the reload stats in step.
type workerSkillSurface struct {
	catalog       []*skills.ParsedSkill
	catalogPrompt string
	dirs          []string
}

// workerSkillSurface discovers the full worker hierarchy: framework defaults,
// preset packs (filtered down to this colony's role defaults), user skills
// (~/.agents/skills, external skills, ~/.hive/skills), the owning queen's
// scope, then the colony's own scopes. Later scopes win on name collisions.
// The prompt is phase-filtered for "colony"; the catalog and read dirs are not,
// because GET /colony/skills and the Tier-3 allowlist expose the full filtered
// manager catalog upstream.
func (r *ColonyRuntime) workerSkillSurface() workerSkillSurface {
	discovery := skills.NewSkillDiscovery(skills.DiscoveryConfig{
		ExtraScopes: r.workerSkillScopes(),
	})
	discovered := discovery.Discover()
	filtered := skills.ApplyManagerOverrides(discovered, skills.NewSkillsConfig(), nil, r.defaultPresetSkills)
	phase := colonySkillsPhase
	return workerSkillSurface{
		catalog:       filtered,
		catalogPrompt: skills.NewSkillCatalog(filtered).ToPrompt(&phase),
		dirs:          discovery.ScannedDirectories(),
	}
}

// attachWorkerSkillSurface wires one worker context to the colony's skill
// hierarchy: the catalog prompt, the Tier-3 read allowlist, and the dynamic
// provider a running worker reads after ReloadSkills. Workers reach it through
// buildWorkerContext; the persistent overseer reaches it in StartOverseer,
// mirroring upstream's start_overseer context (skills_catalog_prompt +
// skill_dirs + dynamic provider).
func (r *ColonyRuntime) attachWorkerSkillSurface(ctx *agentloop.AgentContext) {
	if r == nil || ctx == nil {
		return
	}
	surface := r.workerSkillSurface()
	if len(surface.dirs) == 0 {
		return
	}
	ctx.SkillDirs = surface.dirs
	ctx.SkillsCatalogPrompt = surface.catalogPrompt
	r.storeSkillsCatalog(surface.catalogPrompt)
	ctx.DynamicSkillsCatalogProvider = func() string {
		return r.cachedSkillsCatalogPrompt()
	}
}

// colonySkills returns the skills the colony sees for the read routes: the
// post-override catalog across every scope, without phase filtering (upstream
// GET /colony/skills reads the manager's catalog directly). The bool always
// reports true for a live runtime, matching upstream's always-present
// SkillsManager even when zero skills were discovered.
func (r *ColonyRuntime) colonySkills() ([]*skills.ParsedSkill, bool) {
	if r == nil {
		return nil, false
	}
	return r.workerSkillSurface().catalog, true
}

// workerSkillsCatalogPrompt renders the catalog a spawning worker's prompt
// announces: the full scope hierarchy filtered for phase "colony". The memo is
// primed here; ReloadSkills refreshes the same memo when a skill changes while
// workers are running.
func (r *ColonyRuntime) workerSkillsCatalogPrompt() string {
	surface := r.workerSkillSurface()
	r.storeSkillsCatalog(surface.catalogPrompt)
	return surface.catalogPrompt
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
	surface := r.workerSkillSurface()
	r.storeSkillsCatalog(surface.catalogPrompt)
	// Publish even the empty result: a colony whose skill folders disappeared
	// must drop the stale catalog instead of keeping the last one.
	stats := map[string]any{"catalog_chars": utf8.RuneCountInString(surface.catalogPrompt)}
	dirs := surface.dirs
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
