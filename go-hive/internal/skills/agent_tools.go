package skills

// Agent-facing skill tools: the queen's write_skill surface plus the
// skill_view / list_skills read surface shared with colony workers.
//
// The writer and validator primitives already live in skill_writer.go; this
// file is the thin agent-tool layer on top of them. It follows upstream's
// write_skill tool (framework/tools/queen_lifecycle_tools.py) for the name,
// scope and parameter contract. The read tools are Go-side additions: upstream
// workers read a skill through a <read_skill> tag and the view_file
// interceptor, while this port exposes the same read as an explicit tool so a
// worker does not have to guess a filesystem path.
//
// Scopes:
//   - colony: <colony dir>/skills, discovered by workers spawned afterwards.
//   - queen:  <queen dir>/skills, the queen's own catalog.
//   - global: a pending skill-install request in the session directory; the
//     user approves or denies it in the dashboard. Nothing is installed here,
//     matching upstream's consent flow.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/llm"
)

const (
	WriteSkillToolName = "write_skill"
	SkillViewToolName  = "skill_view"
	ListSkillsToolName = "list_skills"
)

// ToolDirs is the on-disk context a skill tool call resolves its scope
// against. ColonyDir is set only when a colony is bound; QueenDir is the
// queen's home (…/queens/<id>), and SessionDir is the queen session folder
// that stores global install requests.
type ToolDirs struct {
	ColonyDir  string
	QueenDir   string
	QueenID    string
	SessionDir string
}

// AgentToolDefs returns the three skill tool definitions in installation
// order. The queen session installs all three; a worker surface installs only
// the read pair (write_skill is the queen's authoring tool upstream).
func AgentToolDefs() []llm.ToolDef {
	return []llm.ToolDef{WriteSkillToolDef(), SkillViewToolDef(), ListSkillsToolDef()}
}

// WriteSkillToolDef mirrors the upstream write_skill Tool declaration: the same
// six parameters, the same "exactly one of inline / source_path" mode split,
// and the same model-visible text (write_skill_contract.go is byte-exact
// contract data). The port's scope caveats live in the executor's messages.
//
// Parity: framework/tools/queen_lifecycle_tools.py::write_skill_tool
func WriteSkillToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        WriteSkillToolName,
		Description: writeSkillDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": writeSkillSkillNameDescription,
				},
				"skill_description": map[string]any{
					"type":        "string",
					"description": writeSkillSummaryDescription,
				},
				"skill_body": map[string]any{
					"type":        "string",
					"description": writeSkillSkillBodyDescription,
				},
				"skill_files": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path":    map[string]any{"type": "string"},
							"content": map[string]any{"type": "string"},
						},
						"required": []string{"path", "content"},
					},
					"description": writeSkillSkillFilesDescription,
				},
				"source_path": map[string]any{
					"type":        "string",
					"description": writeSkillSourcePathDescription,
				},
				"scope": map[string]any{
					"type":        "string",
					"enum":        []string{"queen", "colony", "global"},
					"description": writeSkillScopeDescription,
				},
			},
			// The Python contract publishes the key with an empty list (MCP
			// tool schemas materialize `required`); keeping it explicit is what
			// makes the request shape byte-identical.
			"required": []string{},
		},
	}
}

// SkillViewToolDef exposes the read half of the catalog: one skill's SKILL.md
// (or a supporting file under its folder) as text.
//
// Parity-exempt: upstream has no skill_view tool — it reads a skill through a
// <read_skill> tag and the view_file interceptor. This port makes that read an
// explicit tool so workers never need to construct the path themselves.
func SkillViewToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: SkillViewToolName,
		Description: "Read one skill's SKILL.md, or a supporting file inside its folder. Use it after " +
			"list_skills (or when the task names a skill) to load the operational procedure before " +
			"planning. The colony scope is searched first, then the queen's own skills.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"skill_name": map[string]any{
					"type":        "string",
					"description": "The skill name (lowercase, hyphen-separated).",
				},
				"file": map[string]any{
					"type":        "string",
					"description": "Optional path relative to the skill folder. Defaults to SKILL.md.",
				},
			},
			"required": []string{"skill_name"},
		},
	}
}

// ListSkillsToolDef lists the skills visible from this colony/queen scope.
//
// Parity-exempt: upstream renders the catalog in the system prompt instead of
// exposing a tool; keeping a tool here makes the catalog reachable even when
// the host did not inject SkillsCatalogPrompt.
func ListSkillsToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        ListSkillsToolName,
		Description: "List the skills visible to this colony/queen scope (name, description, location).",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// ExecuteAgentTool runs one skill tool call. ok is false when name is not a
// skill tool, letting the caller fall through to another executor.
func ExecuteAgentTool(name string, input map[string]any, dirs ToolDirs) (string, bool, bool) {
	var payload map[string]any
	switch name {
	case WriteSkillToolName:
		payload = executeWriteSkill(input, dirs)
	case SkillViewToolName:
		payload = executeSkillView(input, dirs)
	case ListSkillsToolName:
		payload = executeListSkills(dirs)
	default:
		return "", false, false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return `{"error":"skill tool result could not be encoded"}`, true, true
	}
	_, failed := payload["error"]
	return string(encoded), failed, true
}

func executeWriteSkill(input map[string]any, dirs ToolDirs) map[string]any {
	scope := strings.TrimSpace(stringValue(input["scope"]))
	if scope == "" {
		if strings.TrimSpace(dirs.ColonyDir) != "" {
			scope = "colony"
		} else {
			scope = "queen"
		}
	}
	switch scope {
	case "colony", "queen", "global":
	default:
		return map[string]any{"error": fmt.Sprintf("write_skill: scope must be 'colony', 'queen' or 'global', not %q", scope)}
	}
	if scope == "colony" && strings.TrimSpace(dirs.ColonyDir) == "" {
		return map[string]any{"error": "write_skill: no colony is bound to this session, so there is no colony scope. " +
			"Use scope='queen' for a skill of your own, or scope='global' to ask the user to add it to the shared library."}
	}
	if scope == "queen" && strings.TrimSpace(dirs.QueenDir) == "" {
		return map[string]any{"error": "write_skill: this session has no queen skills directory, so scope='queen' is unavailable."}
	}

	var (
		draft       *SkillDraft
		errMsg      string
		sourcePath  = strings.TrimSpace(stringValue(input["source_path"]))
		filesCopied int
	)
	if sourcePath != "" {
		if input["skill_description"] != nil || input["skill_body"] != nil || input["skill_files"] != nil {
			return map[string]any{"error": "write_skill: source_path mode does not accept skill_description / skill_body / skill_files. " +
				"Pass only source_path (and optionally skill_name to rename). Use inline mode if you need to edit content."}
		}
		draft, filesCopied, errMsg = draftFromSourcePath(sourcePath, strings.TrimSpace(stringValue(input["skill_name"])))
	} else {
		skillName := strings.TrimSpace(stringValue(input["skill_name"]))
		skillDescription := strings.TrimSpace(stringValue(input["skill_description"]))
		skillBody := stringValue(input["skill_body"])
		if skillName == "" || skillDescription == "" || strings.TrimSpace(skillBody) == "" {
			return map[string]any{"error": "write_skill: inline mode requires skill_name, skill_description, and skill_body. " +
				"Or pass source_path to copy an existing skill folder."}
		}
		draft, errMsg = BuildSkillDraft(skillName, skillDescription, skillBody, input["skill_files"])
	}
	if draft == nil {
		return map[string]any{
			"error": errMsg,
			"hint": "Provide skill_name (lowercase [a-z0-9.-], ≤64 chars), skill_description (single line, " +
				"1–1024 chars), and skill_body (the operational procedure: schema columns, tool conventions, " +
				"output format, quality bar, gotchas). Use skill_files for optional scripts/references. " +
				"Or pass source_path to copy an existing skill folder.",
		}
	}

	switch scope {
	case "queen":
		return writeQueenScopeSkill(draft, dirs, sourcePath, filesCopied)
	case "global":
		return requestGlobalSkill(draft, dirs)
	default:
		return writeColonyScopeSkill(draft, dirs, sourcePath, filesCopied)
	}
}

func writeColonyScopeSkill(draft *SkillDraft, dirs ToolDirs, sourcePath string, filesCopied int) map[string]any {
	installed, errMsg, replaced := WriteSkill(draft, filepath.Join(dirs.ColonyDir, "skills"), true)
	if errMsg != "" {
		return map[string]any{"error": errMsg}
	}
	colonyID := filepath.Base(filepath.Clean(dirs.ColonyDir))
	return map[string]any{
		"success":      true,
		"scope":        "colony",
		"colony_id":    colonyID,
		"skill_name":   draft.Name,
		"skill_path":   installed,
		"replaced":     replaced,
		"source_path":  sourcePath,
		"files_copied": filesCopied,
		"message": "Skill '" + draft.Name + "' is ready. Workers spawned after this call see it in their " +
			"<available_skills> catalog and can activate it by name — reference it BY NAME in the task " +
			"string instead of repeating the protocol prose.",
	}
}

func writeQueenScopeSkill(draft *SkillDraft, dirs ToolDirs, sourcePath string, filesCopied int) map[string]any {
	root := filepath.Join(dirs.QueenDir, "skills")
	queenID := strings.TrimSpace(dirs.QueenID)
	if queenID == "" {
		queenID = "default"
	}
	store := LoadSkillOverrideStore(filepath.Join(dirs.QueenDir, "skills_overrides.json"), "queen:"+queenID)
	entry := store.Get(draft.Name)
	hers := entry != nil && (entry.Provenance == ProvenanceQueenCreated || entry.Provenance == ProvenanceLearned)
	if _, err := os.Stat(filepath.Join(root, draft.Name)); err == nil && !hers {
		return map[string]any{"error": fmt.Sprintf(
			"write_skill: the user made the skill '%s' in your skills, so you can't replace it. "+
				"Pick another name, or ask the user to change it on the Skills page.", draft.Name)}
	}
	installed, errMsg, replaced := WriteSkill(draft, root, true)
	if errMsg != "" {
		return map[string]any{"error": errMsg}
	}
	if !hers {
		enabled := true
		created := UTCNow()
		createdBy := "queen:" + queenID
		store.Upsert(draft.Name, &OverrideEntry{
			Enabled:    &enabled,
			Provenance: ProvenanceQueenCreated,
			CreatedAt:  &created,
			CreatedBy:  &createdBy,
		})
		if err := store.Save(); err != nil {
			return map[string]any{"error": fmt.Sprintf("skill '%s' was written but its queen override could not be saved: %v", draft.Name, err)}
		}
	}
	// A queen-scope skill is visible to her own sessions AND to the workers of
	// every colony she owns (queen_ui scope), so the old "workers see
	// colony-scope skills only" guidance no longer holds — upstream:
	// "your colonies' workers spawned from now on see it"
	// (_write_queen_skill, framework/tools/queen_lifecycle_tools.py).
	message := fmt.Sprintf("Skill '%s' is in your own skills. It is in your catalog from your next turn, "+
		"and your colonies' workers spawned from now on also see it. "+
		"No other queen sees it: scope='global' asks the user to add it to the shared library.", draft.Name)
	if entry != nil && entry.Enabled != nil && !*entry.Enabled {
		message += " The user has turned this skill off, so it stays off until they turn it back on."
	}
	return map[string]any{
		"success":      true,
		"scope":        "queen",
		"skill_name":   draft.Name,
		"skill_path":   installed,
		"replaced":     replaced,
		"source_path":  sourcePath,
		"files_copied": filesCopied,
		"message":      message,
	}
}

func requestGlobalSkill(draft *SkillDraft, dirs ToolDirs) map[string]any {
	if strings.TrimSpace(dirs.SessionDir) == "" {
		return map[string]any{"error": "write_skill: this session has no folder to keep the request in, so scope='global' isn't available here."}
	}
	request, err := AddRequest(dirs.SessionDir, draft)
	if err != nil {
		return map[string]any{"error": fmt.Sprintf("write_skill: failed to file the global skill request: %v", err)}
	}
	return map[string]any{
		"success":    true,
		"scope":      "global",
		"pending":    true,
		"request_id": request.ID,
		"skill_name": draft.Name,
		"message": "A pending request to add '" + draft.Name + "' to the shared skill library was filed in " +
			"this session. Nothing is installed until the user approves it; if they do, every queen and " +
			"colony reads it. Carry on meanwhile, and don't put it in a skills folder any other way.",
	}
}

// draftFromSourcePath copies an existing skill folder into a draft: SKILL.md's
// frontmatter supplies the description and body, and every other UTF-8 file is
// carried verbatim. Binary auxiliaries are refused rather than silently
// dropped, matching upstream's copy mode.
func draftFromSourcePath(sourcePath, rename string) (*SkillDraft, int, string) {
	src := expandHomePath(sourcePath)
	info, err := os.Stat(src)
	if err != nil {
		return nil, 0, fmt.Sprintf("source_path '%s' does not exist", sourcePath)
	}
	if info.IsDir() {
		// keep src
	} else if info.Mode().IsRegular() && filepath.Base(src) == "SKILL.md" {
		src = filepath.Dir(src)
	} else {
		return nil, 0, fmt.Sprintf("source_path '%s' must be a skill root directory (containing SKILL.md)", sourcePath)
	}
	skillMD := filepath.Join(src, "SKILL.md")
	if stat, err := os.Stat(skillMD); err != nil || stat.IsDir() {
		return nil, 0, fmt.Sprintf("source_path '%s' has no SKILL.md", sourcePath)
	}
	parsed := ParseSkillMD(skillMD, "user")
	if parsed == nil {
		return nil, 0, fmt.Sprintf("failed to parse SKILL.md at '%s' — check the frontmatter is valid YAML and has a non-empty 'description' field", skillMD)
	}
	name := strings.TrimSpace(rename)
	if name == "" {
		name = parsed.Name
	}

	resolvedSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		resolvedSrc = src
	}
	var files []map[string]any
	var binaries []string
	walkErr := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if path == skillMD {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, part := range strings.Split(rel, "/") {
			if strings.HasPrefix(part, ".") {
				return nil
			}
		}
		// Refuse symlinks escaping the source directory.
		resolvedFile, err := filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		if relToRoot, err := filepath.Rel(resolvedSrc, resolvedFile); err != nil || strings.HasPrefix(relToRoot, "..") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read '%s' under source_path: %w", rel, err)
		}
		if !utf8.Valid(data) {
			binaries = append(binaries, rel)
			return nil
		}
		files = append(files, map[string]any{"path": rel, "content": string(data)})
		return nil
	})
	if walkErr != nil {
		return nil, 0, fmt.Sprintf("failed to walk source_path: %v", walkErr)
	}
	if len(binaries) > 0 {
		sort.Strings(binaries)
		return nil, 0, fmt.Sprintf("write_skill: source skill contains non-UTF8 files which the skill writer can't carry: %v. "+
			"Remove or replace them in the source, or copy by hand.", binaries)
	}
	draft, errMsg := BuildSkillDraft(name, parsed.Description, parsed.Body, files)
	if draft == nil {
		return nil, 0, errMsg
	}
	return draft, len(files), ""
}

func executeSkillView(input map[string]any, dirs ToolDirs) map[string]any {
	rawName := strings.TrimSpace(stringValue(input["skill_name"]))
	if rawName == "" {
		rawName = strings.TrimSpace(stringValue(input["name"]))
	}
	name, errMsg := ValidateSkillName(rawName)
	if errMsg != "" {
		return map[string]any{"error": "skill_view: " + errMsg}
	}
	relFile := strings.TrimSpace(stringValue(input["file"]))
	if relFile == "" {
		relFile = "SKILL.md"
	}
	relFile = filepath.ToSlash(strings.TrimPrefix(relFile, "./"))
	if strings.HasPrefix(relFile, "/") || isUnsafeRelativePath(relFile) {
		return map[string]any{"error": fmt.Sprintf("skill_view: file '%s' must be relative and inside the skill folder", relFile)}
	}

	roots := scopedSkillRoots(dirs)
	for _, root := range roots {
		skillDir := filepath.Join(root.dir, name)
		info, err := os.Stat(skillDir)
		if err != nil || !info.IsDir() {
			continue
		}
		target := filepath.Join(skillDir, filepath.FromSlash(relFile))
		if rel, err := filepath.Rel(skillDir, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return map[string]any{"error": fmt.Sprintf("skill_view: file '%s' must be relative and inside the skill folder", relFile)}
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return map[string]any{"error": fmt.Sprintf("skill_view: '%s' has no readable file '%s': %v", name, relFile, err)}
		}
		if !utf8.Valid(data) {
			return map[string]any{"error": fmt.Sprintf("skill_view: '%s' is not UTF-8 text", relFile)}
		}
		return map[string]any{
			"success":    true,
			"skill_name": name,
			"scope":      root.scope,
			"skill_path": target,
			"content":    string(data),
		}
	}
	return map[string]any{
		"error":     fmt.Sprintf("skill_view: skill '%s' was not found in the colony or queen skills directories", name),
		"available": availableSkillNames(roots),
	}
}

func executeListSkills(dirs ToolDirs) map[string]any {
	roots := scopedSkillRoots(dirs)
	entries := map[string]map[string]any{}
	for _, root := range roots {
		if _, err := os.Stat(root.dir); err != nil {
			continue
		}
		discovery := NewSkillDiscovery(DiscoveryConfig{
			SkipFrameworkScope: true,
			SkipUserScope:      true,
			ExtraScopes:        []ExtraScope{{Directory: root.dir, Label: root.scope}},
		})
		for _, skill := range discovery.Discover() {
			if _, exists := entries[skill.Name]; exists {
				continue
			}
			entries[skill.Name] = map[string]any{
				"name":        skill.Name,
				"description": skill.Description,
				"location":    skill.Location,
				"scope":       root.scope,
			}
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, entries[name])
	}
	return map[string]any{"success": true, "skills": out}
}

type skillRoot struct {
	dir   string
	scope string
}

func scopedSkillRoots(dirs ToolDirs) []skillRoot {
	roots := make([]skillRoot, 0, 2)
	if strings.TrimSpace(dirs.ColonyDir) != "" {
		roots = append(roots, skillRoot{dir: filepath.Join(dirs.ColonyDir, "skills"), scope: "colony"})
	}
	if strings.TrimSpace(dirs.QueenDir) != "" {
		roots = append(roots, skillRoot{dir: filepath.Join(dirs.QueenDir, "skills"), scope: "queen"})
	}
	return roots
}

func availableSkillNames(roots []skillRoot) []string {
	seen := map[string]struct{}{}
	for _, root := range roots {
		entries, err := os.ReadDir(root.dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(root.dir, entry.Name(), "SKILL.md")); err == nil {
				seen[entry.Name()] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ScopedCatalogPromptForScopes renders the <available_skills> block for an
// ordered list of extra skill scopes. Later scopes override earlier ones on
// name collisions (discovery scans in order and resolves last-wins), which is
// how the queen's scope (queen_ui) and the colony's own scope (colony_ui)
// combine for a worker: a colony skill overrides the queen's copy of the same
// name, mirroring the upstream scan order in
// framework/host/colony_runtime.py::_build_default_skills_config.
// Non-existent directories are skipped; no usable scope yields "".
func ScopedCatalogPromptForScopes(scopes []ExtraScope) string {
	filtered := make([]ExtraScope, 0, len(scopes))
	for _, scope := range scopes {
		dir := strings.TrimSpace(scope.Directory)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		scope.Directory = dir
		if strings.TrimSpace(scope.Label) == "" {
			scope.Label = "colony"
		}
		filtered = append(filtered, scope)
	}
	if len(filtered) == 0 {
		return ""
	}
	discovery := NewSkillDiscovery(DiscoveryConfig{
		SkipFrameworkScope: true,
		SkipUserScope:      true,
		ExtraScopes:        filtered,
	})
	return NewSkillCatalog(discovery.Discover()).ToPrompt(nil)
}

// ScopedCatalogPrompt renders the <available_skills> block for a single skill
// directory (e.g. the colony's skills/ folder for workers). An empty or
// missing directory yields an empty prompt.
func ScopedCatalogPrompt(dir, scopeLabel string) string {
	if strings.TrimSpace(scopeLabel) == "" {
		scopeLabel = "colony"
	}
	return ScopedCatalogPromptForScopes([]ExtraScope{{Directory: dir, Label: scopeLabel}})
}

// ExistingSkillsDir returns root/skills when it exists, else "".
func ExistingSkillsDir(root string) string {
	if strings.TrimSpace(root) == "" {
		return ""
	}
	dir := filepath.Join(root, "skills")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// ExistingColonySkillsDir returns colonyDir/skills when it exists, else "".
func ExistingColonySkillsDir(colonyDir string) string {
	return ExistingSkillsDir(colonyDir)
}

func expandHomePath(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
