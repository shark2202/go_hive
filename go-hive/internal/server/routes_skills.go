package server

// Parity: core/framework/server/routes_skills.py::register_routes
//
// HTTP routes for per-queen skill control and the aggregated skills library.
// Paths, payload shapes, status codes and error strings mirror the aiohttp
// originals so the dashboard's skills API behaves the same over the Go
// runtime. The handlers read and write the same on-disk layout the skills
// package already owns: <queen>/skills, <queen>/skills_overrides.json and
// <HIVE_HOME>/skills.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

const skillsRouteMaxUploadBytes = 2 * 1024 * 1024

var (
	skillsRouteZipMagic           = []byte("PK\x03\x04")
	skillsRouteSessionIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	skillsRouteDescriptionLine    = regexp.MustCompile(`(?m)^description:\s*(.+)$`)
	skillsRouteFrontmatterName    = regexp.MustCompile(`(?m)^name:\s*.+$`)
	skillsRouteEditableProvenance = map[skills.Provenance]struct{}{
		skills.ProvenanceUserUICreated: {},
		skills.ProvenanceQueenCreated:  {},
		skills.ProvenanceLearned:       {},
	}
)

// SkillsRouteDeps is everything the skills table needs. Manager supplies the
// live sessions used for reload fan-out and install-request decisions; a nil
// manager leaves those paths at their non-live fallbacks.
type SkillsRouteDeps struct {
	Home    hivehome.Home
	Manager *SessionManager
	// UserHome overrides the home directory discovery scans for ~/.agents
	// skills. Empty means os.UserHomeDir; tests pin it to an isolated dir.
	UserHome string
}

// Parity: core/framework/server/routes_skills.py::register_routes
func RegisterSkillsRoutes(mux *http.ServeMux, deps SkillsRouteDeps) {
	mux.HandleFunc("GET /api/queen/{queen_id}/skills", deps.handleListQueenSkills)
	mux.HandleFunc("POST /api/queen/{queen_id}/skills", deps.handleCreateQueenSkill)
	mux.HandleFunc("PATCH /api/queen/{queen_id}/skills/{skill_name}", deps.handlePatchQueenSkill)
	mux.HandleFunc("GET /api/queen/{queen_id}/skills/{skill_name}/body", deps.handleGetQueenSkillBody)
	mux.HandleFunc("PUT /api/queen/{queen_id}/skills/{skill_name}/body", deps.handlePutQueenSkillBody)
	mux.HandleFunc("POST /api/queen/{queen_id}/skills/{skill_name}/rename", deps.handleRenameQueenSkill)
	mux.HandleFunc("DELETE /api/queen/{queen_id}/skills/{skill_name}", deps.handleDeleteQueenSkill)
	mux.HandleFunc("POST /api/queen/{queen_id}/skills/reload", deps.handleReloadQueenSkills)

	mux.HandleFunc("GET /api/sessions/{session_id}/skill-installs", deps.handleListSkillInstalls)
	mux.HandleFunc("POST /api/sessions/{session_id}/skill-installs/{request_id}", deps.handleDecideSkillInstall)

	mux.HandleFunc("GET /api/skills", deps.handleListAllSkills)
	mux.HandleFunc("GET /api/skills/scopes", deps.handleListScopes)
	mux.HandleFunc("POST /api/skills/reload", deps.handleReloadLibrarySkills)
	mux.HandleFunc("POST /api/skills/upload", deps.handleUploadSkill)
	mux.HandleFunc("GET /api/skills/{skill_name}", deps.handleGetSkillDetail)
}

// ---------------------------------------------------------------------------
// Scope resolution
// ---------------------------------------------------------------------------

type skillsRouteQueenScope struct {
	queenID  string
	writeDir string
	store    *skills.SkillOverrideStore
	manager  *skills.SkillsManager
}

func (d SkillsRouteDeps) userHomeDir() string {
	if d.UserHome != "" {
		return d.UserHome
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// queenScope mirrors _queen_scope: an existing queen directory resolves to her
// write dir, override store and a freshly loaded admin manager. Unknown ids
// return nil so the caller can answer 404.
func (d SkillsRouteDeps) queenScope(queenID string) *skillsRouteQueenScope {
	queenHome, ok := d.queenHome(queenID)
	if !ok {
		return nil
	}
	info, err := os.Stat(queenHome)
	if err != nil || !info.IsDir() {
		return nil
	}
	overridesPath := filepath.Join(queenHome, "skills_overrides.json")
	writeDir := filepath.Join(queenHome, "skills")
	store := skills.LoadSkillOverrideStore(overridesPath, "queen:"+queenID)
	manager := d.buildAdminManager(queenID, overridesPath, []skills.ExtraScope{
		{Directory: writeDir, Label: "queen_ui", Priority: 2},
	})
	return &skillsRouteQueenScope{
		queenID:  queenID,
		writeDir: writeDir,
		store:    store,
		manager:  manager,
	}
}

// queenHome keeps an id from stepping outside the queens tree. Upstream joins
// the raw path segment; the Go mux hands us the same segment, so an extra
// containment check costs nothing and rejects ".."/absolute ids.
func (d SkillsRouteDeps) queenHome(queenID string) (string, bool) {
	if queenID == "" {
		return "", false
	}
	queensDir := d.Home.QueensDir()
	candidate := filepath.Join(queensDir, queenID)
	rel, err := filepath.Rel(queensDir, candidate)
	if err != nil || rel == "." || rel == "" || filepath.IsAbs(rel) ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
}

func (d SkillsRouteDeps) buildAdminManager(queenID, overridesPath string, extras []skills.ExtraScope) *skills.SkillsManager {
	manager := skills.NewSkillsManager(&skills.SkillsManagerConfig{
		SkillsConfig:           skills.NewSkillsConfig(),
		ProjectRoot:            "",
		SkipCommunityDiscovery: true,
		Interactive:            false,
		QueenID:                queenID,
		QueenOverridesPath:     overridesPath,
		ExtraScopeDirs:         extras,
		HomeDir:                d.userHomeDir(),
		HiveHome:               d.Home.Root,
	})
	manager.Load()
	return manager
}

func (d SkillsRouteDeps) queenIDs() []string {
	ids := []string{}
	entries, err := os.ReadDir(d.Home.QueensDir())
	if err != nil {
		return ids
	}
	for _, entry := range entries {
		info, statErr := os.Stat(filepath.Join(d.Home.QueensDir(), entry.Name()))
		if statErr != nil || !info.IsDir() {
			continue
		}
		profile := filepath.Join(d.Home.QueensDir(), entry.Name(), "profile.yaml")
		if profileInfo, profileErr := os.Stat(profile); profileErr == nil && !profileInfo.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

func (d SkillsRouteDeps) colonyIDs() []string {
	ids := []string{}
	entries, err := os.ReadDir(d.Home.ColoniesDir())
	if err != nil {
		return ids
	}
	for _, entry := range entries {
		info, statErr := os.Stat(filepath.Join(d.Home.ColoniesDir(), entry.Name()))
		if statErr == nil && info.IsDir() {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids
}

// colonyQueenID reads metadata.json's queen_name, treating "" as absent.
func (d SkillsRouteDeps) colonyQueenID(colonyName string) *string {
	data, err := os.ReadFile(filepath.Join(d.Home.ColoniesDir(), colonyName, "metadata.json"))
	if err != nil {
		return nil
	}
	var meta map[string]any
	if json.Unmarshal(data, &meta) != nil || meta == nil {
		return nil
	}
	queenName, _ := meta["queen_name"].(string)
	if queenName == "" {
		return nil
	}
	return &queenName
}

// Parity: core/framework/server/routes_skills.py::_all_scope_extras
func (d SkillsRouteDeps) allScopeExtras() []skills.ExtraScope {
	extras := []skills.ExtraScope{}
	for _, queenID := range d.queenIDs() {
		extras = append(extras, skills.ExtraScope{
			Directory: filepath.Join(d.Home.QueenDir(queenID), "skills"),
			Label:     "queen_ui",
			Priority:  2,
		})
	}
	for _, colony := range d.colonyIDs() {
		colonyDir := filepath.Join(d.Home.ColoniesDir(), colony)
		extras = append(extras, skills.ExtraScope{
			Directory: filepath.Join(colonyDir, "skills"),
			Label:     "colony_ui",
			Priority:  3,
		})
		extras = append(extras, skills.ExtraScope{
			Directory: filepath.Join(colonyDir, ".hive", "skills"),
			Label:     "colony_ui",
			Priority:  3,
		})
	}
	return extras
}

// ---------------------------------------------------------------------------
// Serializers
// ---------------------------------------------------------------------------

// Parity: core/framework/server/routes_skills.py::_resolve_provenance
func skillsRouteResolveProvenance(skill *skills.ParsedSkill, store *skills.SkillOverrideStore) (skills.Provenance, *skills.OverrideEntry) {
	var storeEntry *skills.OverrideEntry
	if store != nil {
		if entry := store.Get(skill.Name); entry != nil {
			storeEntry = entry
			stamped := entry.Provenance
			if stamped == skills.ProvenanceFramework && skill.SourceScope != "framework" {
				stamped = skills.ProvenanceOther
			}
			if stamped != skills.ProvenanceOther {
				return stamped, entry
			}
		}
	}
	switch skill.SourceScope {
	case "framework":
		return skills.ProvenanceFramework, storeEntry
	case "preset":
		return skills.ProvenancePreset, storeEntry
	case "user":
		return skills.ProvenanceUserDropped, storeEntry
	case "queen_ui":
		return skills.ProvenanceUserUICreated, storeEntry
	case "colony_ui":
		return skills.ProvenanceQueenCreated, storeEntry
	default:
		return skills.ProvenanceProjectDropped, storeEntry
	}
}

// Parity: core/framework/server/routes_skills.py::_effective_enabled
func skillsRouteEffectiveEnabled(skill *skills.ParsedSkill, store *skills.SkillOverrideStore) bool {
	if store != nil {
		if entry := store.Get(skill.Name); entry != nil && entry.Enabled != nil {
			return *entry.Enabled
		}
	}
	if skill.SourceScope == "preset" {
		return false
	}
	if store != nil && store.AllDefaultsDisabled && skill.SourceScope == "framework" {
		return false
	}
	return true
}

// Parity: core/framework/server/routes_skills.py::_serialize_skill
func skillsRouteSerializeSkill(skill *skills.ParsedSkill, store *skills.SkillOverrideStore) map[string]any {
	provenance, entry := skillsRouteResolveProvenance(skill, store)
	_, editable := skillsRouteEditableProvenance[provenance]
	paramOverrides := map[string]any{}
	var trust any
	var createdAt any
	var createdBy any
	var notes any
	if entry != nil {
		for key, value := range entry.ParamOverrides {
			paramOverrides[key] = value
		}
		trust = entry.Trust
		if entry.CreatedAt != nil {
			createdAt = skillsRoutePythonISO(*entry.CreatedAt)
		}
		createdBy = entry.CreatedBy
		notes = entry.Notes
	}
	return map[string]any{
		"name":            skill.Name,
		"description":     skill.Description,
		"source_scope":    skill.SourceScope,
		"provenance":      string(provenance),
		"enabled":         skillsRouteEffectiveEnabled(skill, store),
		"editable":        editable,
		"deletable":       editable,
		"location":        skill.Location,
		"base_dir":        skill.BaseDir,
		"visibility":      skill.Visibility,
		"trust":           trust,
		"created_at":      createdAt,
		"created_by":      createdBy,
		"notes":           notes,
		"param_overrides": paramOverrides,
	}
}

// skillsRoutePythonISO renders a timestamp the way datetime.isoformat() does
// for an aware UTC value: +00:00, and microseconds only when non-zero.
func skillsRoutePythonISO(value time.Time) string {
	utc := value.UTC()
	out := utc.Format("2006-01-02T15:04:05")
	if micros := utc.Nanosecond() / 1000; micros != 0 {
		out += fmt.Sprintf(".%06d", micros)
	}
	return out + "+00:00"
}

type skillsRouteOwner struct {
	Type string
	ID   string
}

func (o *skillsRouteOwner) dict() map[string]any {
	if o == nil {
		return nil
	}
	return map[string]any{"type": o.Type, "id": o.ID, "name": o.ID}
}

// Parity: core/framework/server/routes_skills.py::handle_list_all_skills
func skillsRouteOwnerFor(skill *skills.ParsedSkill) *skillsRouteOwner {
	parts := strings.Split(filepath.ToSlash(skill.BaseDir), "/")
	if skill.SourceScope == "queen_ui" {
		for index, part := range parts {
			if part == "queens" && index+1 < len(parts) {
				return &skillsRouteOwner{Type: "queen", ID: parts[index+1]}
			}
		}
	}
	if skill.SourceScope == "colony_ui" {
		for index, part := range parts {
			if part == "colonies" && index+1 < len(parts) {
				return &skillsRouteOwner{Type: "colony", ID: parts[index+1]}
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Per-queen handlers
// ---------------------------------------------------------------------------

// Parity: core/framework/server/routes_skills.py::handle_list_queen_skills
func (d SkillsRouteDeps) handleListQueenSkills(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, fmt.Sprintf("queen '%s' not found", queenID))
		return
	}
	rows := []map[string]any{}
	for _, skill := range scope.manager.EnumerateSkillsWithSource() {
		rows = append(rows, skillsRouteSerializeSkill(skill, scope.store))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, _ := rows[i]["name"].(string)
		right, _ := rows[j]["name"].(string)
		return left < right
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"queen_id":              queenID,
		"all_defaults_disabled": scope.store.AllDefaultsDisabled,
		"skills":                rows,
	})
}

// Parity: core/framework/server/routes_skills.py::handle_create_queen_skill
func (d SkillsRouteDeps) handleCreateQueenSkill(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	payload, ok := skillsRouteDecodeJSONBody(r)
	if !ok {
		skillsRouteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status, body := d.createQueenSkill(scope, payload, skillsRouteRequesterID(r))
	writeJSON(w, status, body)
}

func (d SkillsRouteDeps) createQueenSkill(scope *skillsRouteQueenScope, payload map[string]any, userID string) (int, map[string]any) {
	draft, errMsg := skills.BuildSkillDraft(
		skillsRouteStringValue(payload["name"]),
		skillsRouteStringValue(payload["description"]),
		skillsRouteStringValue(payload["body"]),
		payload["files"],
	)
	if errMsg != "" || draft == nil {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	replaceExisting := false
	if raw, present := payload["replace_existing"]; present {
		replaceExisting = skillsRoutePyTruthy(raw)
	}
	installed, writeErr, replaced := skills.WriteSkill(draft, scope.writeDir, replaceExisting)
	if writeErr != "" || installed == "" {
		status := http.StatusInternalServerError
		if strings.Contains(writeErr, "already exists") {
			status = http.StatusConflict
		}
		return status, skillsRouteErrorBody(writeErr)
	}
	enabled := true
	if raw, present := payload["enabled"]; present {
		enabled = skillsRoutePyTruthy(raw)
	}
	var notes *string
	if text, ok := payload["notes"].(string); ok {
		notes = &text
	}
	now := skills.UTCNow()
	scope.store.Upsert(draft.Name, &skills.OverrideEntry{
		Enabled:    &enabled,
		Provenance: skills.ProvenanceUserUICreated,
		CreatedAt:  &now,
		CreatedBy:  &userID,
		Notes:      notes,
	})
	if err := scope.store.Save(); err != nil {
		return http.StatusInternalServerError, skillsRouteErrorBody(err.Error())
	}
	d.reloadQueenSkills(scope)
	return http.StatusCreated, map[string]any{
		"name":           draft.Name,
		"installed_path": installed,
		"replaced":       replaced,
		"enabled":        enabled,
		"provenance":     string(skills.ProvenanceUserUICreated),
	}
}

// Parity: core/framework/server/routes_skills.py::handle_patch_queen_skill
func (d SkillsRouteDeps) handlePatchQueenSkill(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	payload, ok := skillsRouteDecodeJSONBody(r)
	if !ok {
		skillsRouteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status, body := d.patchQueenSkill(scope, r.PathValue("skill_name"), payload)
	writeJSON(w, status, body)
}

func (d SkillsRouteDeps) patchQueenSkill(scope *skillsRouteQueenScope, skillName string, payload map[string]any) (int, map[string]any) {
	name, errMsg := skills.ValidateSkillName(skillName)
	if errMsg != "" || name == "" {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	existing := scope.store.Get(name)
	if existing == nil {
		existing = &skills.OverrideEntry{Provenance: skills.ProvenanceOther}
	}
	if raw, present := payload["enabled"]; present {
		enabled, ok := raw.(bool)
		if !ok {
			return http.StatusBadRequest, skillsRouteErrorBody("'enabled' must be a bool")
		}
		existing.Enabled = &enabled
	}
	if raw, present := payload["param_overrides"]; present {
		params, ok := raw.(map[string]any)
		if !ok {
			return http.StatusBadRequest, skillsRouteErrorBody("'param_overrides' must be an object")
		}
		copied := make(map[string]any, len(params))
		for key, value := range params {
			copied[key] = value
		}
		existing.ParamOverrides = copied
	}
	if raw, present := payload["notes"]; present {
		switch typed := raw.(type) {
		case nil:
			existing.Notes = nil
		case string:
			existing.Notes = &typed
		}
	}
	if raw, present := payload["all_defaults_disabled"]; present {
		if value, ok := raw.(bool); ok {
			scope.store.AllDefaultsDisabled = value
		}
	}
	scope.store.Upsert(name, existing)
	if err := scope.store.Save(); err != nil {
		return http.StatusInternalServerError, skillsRouteErrorBody(err.Error())
	}
	d.reloadQueenSkills(scope)
	var enabledOut any
	if existing.Enabled != nil {
		enabledOut = *existing.Enabled
	}
	return http.StatusOK, map[string]any{"name": name, "enabled": enabledOut, "ok": true}
}

// Parity: core/framework/server/routes_skills.py::handle_get_queen_skill_body
func (d SkillsRouteDeps) handleGetQueenSkillBody(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	status, body := d.getQueenSkillBody(scope, r.PathValue("skill_name"))
	writeJSON(w, status, body)
}

func (d SkillsRouteDeps) getQueenSkillBody(scope *skillsRouteQueenScope, skillName string) (int, map[string]any) {
	name, errMsg := skills.ValidateSkillName(skillName)
	if errMsg != "" || name == "" {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	for _, skill := range scope.manager.EnumerateSkillsWithSource() {
		if skill.Name != name {
			continue
		}
		provenance, _ := skillsRouteResolveProvenance(skill, scope.store)
		_, editable := skillsRouteEditableProvenance[provenance]
		return http.StatusOK, map[string]any{
			"name":         skill.Name,
			"description":  skill.Description,
			"source_scope": skill.SourceScope,
			"location":     skill.Location,
			"base_dir":     skill.BaseDir,
			"body":         skill.Body,
			"visibility":   skill.Visibility,
			"editable":     editable,
		}
	}
	return http.StatusNotFound, skillsRouteErrorBody(fmt.Sprintf("skill '%s' not found", name))
}

// Parity: core/framework/server/routes_skills.py::handle_put_queen_skill_body
func (d SkillsRouteDeps) handlePutQueenSkillBody(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	payload, ok := skillsRouteDecodeJSONBody(r)
	if !ok {
		skillsRouteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status, body := d.putQueenSkillBody(scope, r.PathValue("skill_name"), payload)
	writeJSON(w, status, body)
}

func (d SkillsRouteDeps) putQueenSkillBody(scope *skillsRouteQueenScope, skillName string, payload map[string]any) (int, map[string]any) {
	name, errMsg := skills.ValidateSkillName(skillName)
	if errMsg != "" || name == "" {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	entry := scope.store.Get(name)
	provenance := skills.ProvenanceFramework
	if entry != nil {
		provenance = entry.Provenance
	}
	if _, editable := skillsRouteEditableProvenance[provenance]; !editable {
		return http.StatusForbidden, skillsRouteErrorBody(fmt.Sprintf("skill '%s' is not editable (provenance=%s)", name, provenance))
	}
	body, _ := payload["body"].(string)
	if strings.TrimSpace(body) == "" {
		return http.StatusBadRequest, skillsRouteErrorBody("'body' is required")
	}
	description, hasDescription := payload["description"].(string)
	finalDescription := description
	if !hasDescription {
		currentPath := filepath.Join(scope.writeDir, name, "SKILL.md")
		if data, readErr := os.ReadFile(currentPath); readErr == nil {
			if match := skillsRouteDescriptionLine.FindStringSubmatch(string(data)); match != nil {
				finalDescription = strings.Trim(strings.TrimSpace(match[1]), "\"'")
			}
		}
	}
	draft, draftErr := skills.BuildSkillDraft(name, finalDescription, body, payload["files"])
	if draftErr != "" || draft == nil {
		return http.StatusBadRequest, skillsRouteErrorBody(draftErr)
	}
	installed, writeErr, _ := skills.WriteSkill(draft, scope.writeDir, true)
	if writeErr != "" || installed == "" {
		return http.StatusInternalServerError, skillsRouteErrorBody(writeErr)
	}
	d.reloadQueenSkills(scope)
	return http.StatusOK, map[string]any{"name": name, "installed_path": installed}
}

// Parity: core/framework/server/routes_skills.py::handle_rename_queen_skill
func (d SkillsRouteDeps) handleRenameQueenSkill(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	payload, ok := skillsRouteDecodeJSONBody(r)
	if !ok {
		skillsRouteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	status, body := d.renameQueenSkill(scope, r.PathValue("skill_name"), payload)
	writeJSON(w, status, body)
}

func (d SkillsRouteDeps) renameQueenSkill(scope *skillsRouteQueenScope, skillName string, payload map[string]any) (int, map[string]any) {
	oldName, errMsg := skills.ValidateSkillName(skillName)
	if errMsg != "" || oldName == "" {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	newName, errMsg := skills.ValidateSkillName(skillsRouteStringValue(payload["new_name"]))
	if errMsg != "" || newName == "" {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	if newName == oldName {
		return http.StatusBadRequest, skillsRouteErrorBody("new_name matches the current name")
	}
	entry := scope.store.Get(oldName)
	provenance := skills.ProvenanceFramework
	if entry != nil {
		provenance = entry.Provenance
	}
	if _, editable := skillsRouteEditableProvenance[provenance]; !editable {
		return http.StatusForbidden, skillsRouteErrorBody(fmt.Sprintf("skill '%s' is not editable (provenance=%s)", oldName, provenance))
	}
	oldDir := filepath.Join(scope.writeDir, oldName)
	newDir := filepath.Join(scope.writeDir, newName)
	if info, statErr := os.Stat(oldDir); statErr != nil || !info.IsDir() {
		return http.StatusNotFound, skillsRouteErrorBody(fmt.Sprintf("skill '%s' has no editable copy for this queen", oldName))
	}
	if _, statErr := os.Stat(newDir); statErr == nil {
		return http.StatusConflict, skillsRouteErrorBody(fmt.Sprintf("skill '%s' already exists", newName))
	}
	if err := os.Rename(oldDir, newDir); err != nil {
		return http.StatusInternalServerError, skillsRouteErrorBody(fmt.Sprintf("failed to rename skill folder: %v", err))
	}
	skillMD := filepath.Join(newDir, "SKILL.md")
	if data, readErr := os.ReadFile(skillMD); readErr != nil {
		_ = os.Rename(newDir, oldDir)
		return http.StatusInternalServerError, skillsRouteErrorBody(fmt.Sprintf("failed to update SKILL.md: %v", readErr))
	} else if location := skillsRouteFrontmatterName.FindStringIndex(string(data)); location != nil {
		patched := string(data)[:location[0]] + "name: " + newName + string(data)[location[1]:]
		if writeErr := os.WriteFile(skillMD, []byte(patched), 0o644); writeErr != nil {
			_ = os.Rename(newDir, oldDir)
			return http.StatusInternalServerError, skillsRouteErrorBody(fmt.Sprintf("failed to update SKILL.md: %v", writeErr))
		}
	}
	if entry != nil {
		scope.store.Upsert(newName, entry.Clone())
		scope.store.Remove(oldName, false)
		if err := scope.store.Save(); err != nil {
			return http.StatusInternalServerError, skillsRouteErrorBody(err.Error())
		}
	}
	d.reloadQueenSkills(scope)
	return http.StatusOK, map[string]any{"old_name": oldName, "new_name": newName, "ok": true}
}

// Parity: core/framework/server/routes_skills.py::handle_delete_queen_skill
func (d SkillsRouteDeps) handleDeleteQueenSkill(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	status, body := d.deleteQueenSkill(scope, r.PathValue("skill_name"))
	writeJSON(w, status, body)
}

func (d SkillsRouteDeps) deleteQueenSkill(scope *skillsRouteQueenScope, skillName string) (int, map[string]any) {
	name, errMsg := skills.ValidateSkillName(skillName)
	if errMsg != "" || name == "" {
		return http.StatusBadRequest, skillsRouteErrorBody(errMsg)
	}
	if entry := scope.store.Get(name); entry != nil {
		if _, editable := skillsRouteEditableProvenance[entry.Provenance]; !editable {
			return http.StatusForbidden, skillsRouteErrorBody(fmt.Sprintf("skill '%s' is not deletable (provenance=%s)", name, entry.Provenance))
		}
	}
	removed, removeErr := skills.RemoveSkill(scope.writeDir, name)
	if removeErr != "" {
		return http.StatusInternalServerError, skillsRouteErrorBody(removeErr)
	}
	scope.store.Remove(name, true)
	if err := scope.store.Save(); err != nil {
		return http.StatusInternalServerError, skillsRouteErrorBody(err.Error())
	}
	d.reloadQueenSkills(scope)
	return http.StatusOK, map[string]any{"name": name, "removed": removed}
}

// Parity: core/framework/server/routes_skills.py::handle_reload_queen_skills
func (d SkillsRouteDeps) handleReloadQueenSkills(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	scope := d.queenScope(queenID)
	if scope == nil {
		skillsRouteError(w, http.StatusNotFound, "queen not found")
		return
	}
	report := d.reloadQueenSkills(scope)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"target": scope.queenID,
		"kind":   "queen",
		"live":   report.Dict(),
	})
}

// Parity-exempt: upstream has no library-wide reload endpoint. A shared-library
// change reaches the live managers through their own reload (they rescan
// HIVE_HOME/skills), so this endpoint is the unfiltered fan-out — upstream's
// reload_live_skills(session_manager, queen_id=None). There is no request-scoped
// admin manager to rebuild here: the library has none.
func (d SkillsRouteDeps) handleReloadLibrarySkills(w http.ResponseWriter, r *http.Request) {
	report := d.reloadLiveSkills(nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"kind": "library",
		"live": report.Dict(),
	})
}

// ---------------------------------------------------------------------------
// Aggregated library
// ---------------------------------------------------------------------------

// Parity: core/framework/server/routes_skills.py::handle_list_all_skills
func (d SkillsRouteDeps) handleListAllSkills(w http.ResponseWriter, r *http.Request) {
	queenIDs := d.queenIDs()
	colonyIDs := d.colonyIDs()
	stores := make(map[string]*skills.SkillOverrideStore, len(queenIDs))
	for _, queenID := range queenIDs {
		stores[queenID] = skills.LoadSkillOverrideStore(
			filepath.Join(d.Home.QueenDir(queenID), "skills_overrides.json"),
			"queen:"+queenID,
		)
	}
	colonyQueens := make(map[string]*string, len(colonyIDs))
	for _, colony := range colonyIDs {
		colonyQueens[colony] = d.colonyQueenID(colony)
	}
	discovered := skills.NewSkillDiscovery(skills.DiscoveryConfig{
		HomeDir:     d.userHomeDir(),
		HiveHome:    d.Home.Root,
		ExtraScopes: d.allScopeExtras(),
	}).Discover()

	rows := []map[string]any{}
	for _, skill := range discovered {
		visibleQueens := []string{}
		for _, queenID := range queenIDs {
			if skillsRouteEffectiveEnabled(skill, stores[queenID]) {
				visibleQueens = append(visibleQueens, queenID)
			}
		}
		visibleColonies := []string{}
		for _, colony := range colonyIDs {
			var store *skills.SkillOverrideStore
			if queenID := colonyQueens[colony]; queenID != nil {
				store = stores[*queenID]
			}
			if skillsRouteEffectiveEnabled(skill, store) {
				visibleColonies = append(visibleColonies, colony)
			}
		}
		sort.Strings(visibleQueens)
		sort.Strings(visibleColonies)

		owner := skillsRouteOwnerFor(skill)
		var provenanceStore *skills.SkillOverrideStore
		if owner != nil && owner.Type == "queen" {
			provenanceStore = stores[owner.ID]
		}
		provenance, _ := skillsRouteResolveProvenance(skill, provenanceStore)
		_, editable := skillsRouteEditableProvenance[provenance]
		rows = append(rows, map[string]any{
			"name":               skill.Name,
			"description":        skill.Description,
			"source_scope":       skill.SourceScope,
			"provenance":         string(provenance),
			"owner":              owner.dict(),
			"visible_to":         map[string]any{"queens": visibleQueens, "colonies": visibleColonies},
			"enabled_by_default": true,
			"editable":           editable,
			"deletable":          editable,
			"location":           skill.Location,
			"visibility":         skill.Visibility,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, _ := rows[i]["name"].(string)
		right, _ := rows[j]["name"].(string)
		return left < right
	})

	queens := make([]map[string]any, 0, len(queenIDs))
	for _, queenID := range queenIDs {
		queens = append(queens, map[string]any{"id": queenID, "name": queenID})
	}
	colonies := make([]map[string]any, 0, len(colonyIDs))
	for _, colony := range colonyIDs {
		var queenID any
		if value := colonyQueens[colony]; value != nil {
			queenID = *value
		}
		colonies = append(colonies, map[string]any{"name": colony, "queen_id": queenID})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"skills":   rows,
		"queens":   queens,
		"colonies": colonies,
	})
}

// Parity: core/framework/server/routes_skills.py::handle_list_scopes
func (d SkillsRouteDeps) handleListScopes(w http.ResponseWriter, r *http.Request) {
	queens := []map[string]any{}
	for _, queenID := range d.queenIDs() {
		queens = append(queens, map[string]any{"id": queenID, "name": queenID})
	}
	colonies := []map[string]any{}
	for _, colony := range d.colonyIDs() {
		var queenID any
		if value := d.colonyQueenID(colony); value != nil {
			queenID = *value
		}
		colonies = append(colonies, map[string]any{"name": colony, "queen_id": queenID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"queens": queens, "colonies": colonies})
}

// Parity: core/framework/server/routes_skills.py::handle_get_skill_detail
func (d SkillsRouteDeps) handleGetSkillDetail(w http.ResponseWriter, r *http.Request) {
	skillName := r.PathValue("skill_name")
	name, errMsg := skills.ValidateSkillName(skillName)
	if errMsg != "" || name == "" {
		skillsRouteError(w, http.StatusBadRequest, errMsg)
		return
	}
	manager := d.buildAdminManager("", "", d.allScopeExtras())
	for _, skill := range manager.EnumerateSkillsWithSource() {
		if skill.Name != name {
			continue
		}
		body, readErr := os.ReadFile(skill.Location)
		if readErr != nil {
			skillsRouteError(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to read %s: %v", skill.Location, readErr))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":         skill.Name,
			"description":  skill.Description,
			"source_scope": skill.SourceScope,
			"location":     skill.Location,
			"base_dir":     skill.BaseDir,
			"body":         string(body),
			"visibility":   skill.Visibility,
		})
		return
	}
	skillsRouteError(w, http.StatusNotFound, fmt.Sprintf("skill '%s' not found", name))
}

// ---------------------------------------------------------------------------
// Upload (multipart: SKILL.md OR .zip)
// ---------------------------------------------------------------------------

// Parity: core/framework/server/routes_skills.py::handle_upload_skill
func (d SkillsRouteDeps) handleUploadSkill(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		skillsRouteError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		skillsRouteError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	var uploadBytes []byte
	uploadFilename := ""
	form := map[string]string{}
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil {
			skillsRouteError(w, http.StatusBadRequest, "invalid multipart body")
			return
		}
		fieldName := part.FormName()
		if fieldName == "file" {
			data, readErr := io.ReadAll(io.LimitReader(part, skillsRouteMaxUploadBytes+1))
			uploadFilename = part.FileName()
			_ = part.Close()
			if readErr != nil {
				skillsRouteError(w, http.StatusBadRequest, readErr.Error())
				return
			}
			if len(data) > skillsRouteMaxUploadBytes {
				skillsRouteError(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("upload exceeds %d bytes", skillsRouteMaxUploadBytes))
				return
			}
			uploadBytes = data
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(part, 1<<20))
		_ = part.Close()
		if readErr != nil {
			skillsRouteError(w, http.StatusBadRequest, readErr.Error())
			return
		}
		form[fieldName] = strings.TrimSpace(string(data))
	}
	if uploadBytes == nil {
		skillsRouteError(w, http.StatusBadRequest, "missing 'file' part")
		return
	}

	scopeKind := strings.ToLower(form["scope"])
	if scopeKind != "user" && scopeKind != "queen" {
		skillsRouteError(w, http.StatusBadRequest, "scope must be user|queen")
		return
	}
	targetID := strings.TrimSpace(form["target_id"])
	if scopeKind == "queen" && targetID == "" {
		skillsRouteError(w, http.StatusBadRequest, "target_id required for scope=queen")
		return
	}
	enabledValue, present := form["enabled"]
	if !present {
		enabledValue = "true"
	}
	enabled := strings.ToLower(enabledValue) != "false"
	replaceExisting := strings.ToLower(form["replace_existing"]) == "true"
	var nameOverride string
	if trimmed := strings.TrimSpace(form["name"]); trimmed != "" {
		nameOverride = trimmed
	}

	writeDir := ""
	var targetScope *skillsRouteQueenScope
	if scopeKind == "user" {
		writeDir = filepath.Join(d.Home.Root, "skills")
	} else {
		targetScope = d.queenScope(targetID)
		if targetScope == nil {
			skillsRouteError(w, http.StatusNotFound, "queen not found")
			return
		}
		writeDir = targetScope.writeDir
	}

	var draftName, draftDescription, draftBody string
	var draftFiles []map[string]any
	var extractErr string
	if bytes.HasPrefix(uploadBytes, skillsRouteZipMagic) || strings.HasSuffix(uploadFilename, ".zip") {
		draftName, draftDescription, draftBody, draftFiles, extractErr = skillsRouteExtractFromZip(uploadBytes, nameOverride)
	} else {
		draftName, draftDescription, draftBody, draftFiles, extractErr = skillsRouteExtractFromMD(uploadBytes, uploadFilename, nameOverride)
	}
	if extractErr != "" {
		skillsRouteError(w, http.StatusBadRequest, extractErr)
		return
	}
	draft, draftErr := skills.BuildSkillDraft(draftName, draftDescription, draftBody, draftFiles)
	if draftErr != "" || draft == nil {
		skillsRouteError(w, http.StatusBadRequest, draftErr)
		return
	}
	installed, writeErr, replaced := skills.WriteSkill(draft, writeDir, replaceExisting)
	if writeErr != "" || installed == "" {
		status := http.StatusInternalServerError
		if strings.Contains(writeErr, "already exists") {
			status = http.StatusConflict
		}
		skillsRouteError(w, status, writeErr)
		return
	}
	if targetScope != nil {
		now := skills.UTCNow()
		createdBy := skillsRouteRequesterID(r)
		targetScope.store.Upsert(draft.Name, &skills.OverrideEntry{
			Enabled:    &enabled,
			Provenance: skills.ProvenanceUserUICreated,
			CreatedAt:  &now,
			CreatedBy:  &createdBy,
		})
		if err := targetScope.store.Save(); err != nil {
			skillsRouteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d.reloadLiveSkills(&targetID)
	}
	var targetOut any
	if targetID != "" {
		targetOut = targetID
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":           draft.Name,
		"installed_path": installed,
		"replaced":       replaced,
		"scope":          scopeKind,
		"target_id":      targetOut,
		"enabled":        enabled,
	})
}

// skillsRouteExtractFromMD parses a lone SKILL.md upload. It mirrors
// _extract_from_md: (name, description, body, files, error string).
func skillsRouteExtractFromMD(raw []byte, filename, nameHint string) (string, string, string, []map[string]any, string) {
	if !utf8.Valid(raw) {
		return "", "", "", nil, "upload is not valid UTF-8"
	}
	name, description, body, errMsg := skillsRouteParseSkillMDText(string(raw))
	if errMsg != "" {
		return "", "", "", nil, errMsg
	}
	if nameHint != "" {
		name = nameHint
	}
	if name == "" && filename != "" {
		base := path.Base(strings.ReplaceAll(filename, "\\", "/"))
		stem := strings.TrimSuffix(base, path.Ext(base))
		if stem != "" && strings.ToLower(stem) != "skill" {
			name = strings.ToLower(stem)
		}
	}
	if name == "" {
		return "", "", "", nil, "could not determine skill name (pass 'name' form field)"
	}
	return name, description, body, []map[string]any{}, ""
}

// skillsRouteExtractFromZip parses a .zip bundle. SKILL.md must be inside the
// archive (root or a single enclosing folder), mirroring _extract_from_zip.
func skillsRouteExtractFromZip(raw []byte, nameHint string) (string, string, string, []map[string]any, string) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", "", "", nil, "invalid zip file"
	}
	names := make([]string, 0, len(reader.File))
	byName := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
		byName[file.Name] = file
	}
	skillMDs := []string{}
	for _, name := range names {
		if name == "SKILL.md" || strings.HasSuffix(name, "/SKILL.md") {
			skillMDs = append(skillMDs, name)
		}
	}
	if len(skillMDs) == 0 {
		return "", "", "", nil, "zip must contain SKILL.md at root"
	}
	sort.SliceStable(skillMDs, func(i, j int) bool {
		return strings.Count(skillMDs[i], "/") < strings.Count(skillMDs[j], "/")
	})
	rootSkillMD := skillMDs[0]
	rootPrefix := ""
	if rootSkillMD != "SKILL.md" {
		rootPrefix = rootSkillMD[:len(rootSkillMD)-len("SKILL.md")]
	}
	skillFile := byName[rootSkillMD]
	data, readErr := skillsRouteReadZipFile(skillFile)
	if readErr != nil {
		return "", "", "", nil, fmt.Sprintf("invalid zip file: %v", readErr)
	}
	name, description, body, errMsg := skillsRouteParseSkillMDText(strings.ToValidUTF8(string(data), "\uFFFD"))
	if errMsg != "" {
		return "", "", "", nil, errMsg
	}
	if nameHint != "" {
		name = nameHint
	}
	if name == "" && rootPrefix != "" {
		name = strings.Trim(rootPrefix, "/")
	}
	if name == "" {
		return "", "", "", nil, "could not determine skill name from zip"
	}
	files := []map[string]any{}
	for _, entryName := range names {
		if entryName == rootSkillMD || strings.HasSuffix(entryName, "/") {
			continue
		}
		if !strings.HasPrefix(entryName, rootPrefix) {
			continue
		}
		rel := entryName[len(rootPrefix):]
		if rel == "" || rel == "SKILL.md" {
			continue
		}
		entry := byName[entryName]
		content, contentErr := skillsRouteReadZipFile(entry)
		if contentErr != nil {
			return "", "", "", nil, fmt.Sprintf("invalid zip file: %v", contentErr)
		}
		if !utf8.Valid(content) {
			return "", "", "", nil, fmt.Sprintf("binary file '%s' not supported yet", rel)
		}
		files = append(files, map[string]any{"path": rel, "content": string(content)})
	}
	return name, description, body, files, ""
}

func skillsRouteReadZipFile(file *zip.File) ([]byte, error) {
	if file == nil {
		return nil, errors.New("missing archive entry")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, skillsRouteMaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > skillsRouteMaxUploadBytes {
		return nil, fmt.Errorf("entry exceeds %d bytes", skillsRouteMaxUploadBytes)
	}
	return data, nil
}

// skillsRouteParseSkillMDText is the lightweight frontmatter split used by
// uploads, mirroring _parse_skill_md_text: only name/description/body.
func skillsRouteParseSkillMDText(text string) (string, string, string, string) {
	if !strings.HasPrefix(text, "---") {
		return "", "", "", "SKILL.md must start with '---' frontmatter"
	}
	parts := strings.SplitN(text, "---", 3)
	if len(parts) < 3 {
		return "", "", "", "SKILL.md frontmatter is malformed"
	}
	frontmatter := parts[1]
	body := strings.TrimLeft(parts[2], "\n")
	name := ""
	description := ""
	for _, line := range strings.Split(strings.ReplaceAll(frontmatter, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name = strings.Trim(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), "\"'")
		} else if strings.HasPrefix(line, "description:") {
			description = strings.Trim(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), "\"'")
		}
	}
	return name, description, body, ""
}

// ---------------------------------------------------------------------------
// A queen's requests to add a skill to the shared library
// ---------------------------------------------------------------------------

// Parity: core/framework/server/routes_skills.py::handle_list_skill_installs
func (d SkillsRouteDeps) handleListSkillInstalls(w http.ResponseWriter, r *http.Request) {
	sessionDir, ok := d.installSessionDir(r.PathValue("session_id"))
	if !ok {
		skillsRouteError(w, http.StatusNotFound, "session not found")
		return
	}
	library := filepath.Join(d.Home.Root, "skills")
	rows := []map[string]any{}
	for _, pending := range skills.PendingRequests(sessionDir) {
		replaces := false
		if _, err := os.Stat(filepath.Join(library, pending.Name)); err == nil {
			replaces = true
		}
		rows = append(rows, map[string]any{
			"id":           pending.ID,
			"name":         pending.Name,
			"description":  pending.Description,
			"body":         pending.Body,
			"files":        pending.Files,
			"requested_at": pending.RequestedAt,
			"replaces":     replaces,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

// Parity: core/framework/server/routes_skills.py::handle_decide_skill_install
func (d SkillsRouteDeps) handleDecideSkillInstall(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	sessionDir, ok := d.installSessionDir(sessionID)
	if !ok {
		skillsRouteError(w, http.StatusNotFound, "session not found")
		return
	}
	payload, ok := skillsRouteDecodeJSONBody(r)
	if !ok {
		skillsRouteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	decision, _ := payload["decision"].(string)
	if decision != "approve" && decision != "deny" {
		skillsRouteError(w, http.StatusBadRequest, "decision must be 'approve' or 'deny'")
		return
	}
	requestID := r.PathValue("request_id")
	pending, err := skills.PendingRequest(sessionDir, requestID)
	if err != nil {
		var notFound *skills.RequestNotFound
		var decided *skills.RequestDecided
		switch {
		case errors.As(err, &notFound):
			skillsRouteError(w, http.StatusNotFound, err.Error())
		case errors.As(err, &decided):
			skillsRouteError(w, http.StatusConflict, err.Error())
		default:
			skillsRouteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	name := pending.Name
	result := map[string]any{"name": name, "decision": decision}
	note := ""
	if decision == "approve" {
		draft, draftErr := skills.DraftOf(pending)
		if draftErr != nil {
			skillsRouteError(w, http.StatusUnprocessableEntity, draftErr.Error())
			return
		}
		installed, writeErr, replaced := skills.WriteSkill(draft, filepath.Join(d.Home.Root, "skills"), true)
		if writeErr != "" || installed == "" {
			message := writeErr
			if message == "" {
				message = "failed to write skill folder"
			}
			skillsRouteError(w, http.StatusInternalServerError, message)
			return
		}
		if err := skills.MarkDecided(sessionDir, requestID, skills.InstallStatusApproved); err != nil {
			skillsRouteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		d.reloadLiveSkills(nil)
		result["installed_path"] = installed
		result["replaced"] = replaced
		replacedText := ""
		if replaced {
			replacedText = ", replacing the shared skill of that name"
		}
		note = "[The user approved adding your skill '" + name + "' to the shared skill library" +
			replacedText + ". Every queen and colony can use it now, and it's in your skills catalog from your next turn.]"
	} else {
		if err := skills.MarkDecided(sessionDir, requestID, skills.InstallStatusDenied); err != nil {
			skillsRouteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		note = "[The user declined adding your skill '" + name + "' to the shared skill library, so it isn't installed. Don't install it another way.]"
	}

	live := d.getSession(sessionID)
	queenTold := false
	if live != nil {
		queenTold = skillsRouteTellQueen(live, note)
	}
	result["queen_told"] = queenTold
	if live != nil && live.EventBus != nil {
		nodeID := "queen"
		executionID := live.ID
		event := eventbus.NewAgentEvent(eventbus.EventSkillInstallDecided, "queen")
		event.NodeID = &nodeID
		event.ExecutionID = &executionID
		event.Data = map[string]any{"request_id": requestID, "name": name, "decision": decision}
		live.EventBus.Publish(event)
	}
	writeJSON(w, http.StatusOK, result)
}

func (d SkillsRouteDeps) getSession(sessionID string) *Session {
	if d.Manager == nil {
		return nil
	}
	return d.Manager.GetSession(sessionID)
}

// installSessionDir resolves the folder of the URL's session, running or not.
// A live session with a queen dir wins; otherwise the stored session tree is
// searched and must exist.
func (d SkillsRouteDeps) installSessionDir(sessionID string) (string, bool) {
	if !skillsRouteSessionIDPattern.MatchString(sessionID) {
		return "", false
	}
	if live := d.getSession(sessionID); live != nil && live.QueenDir != "" {
		return live.QueenDir, true
	}
	sessionDir, err := FindQueenSessionDir(d.Home, sessionID)
	if err != nil {
		return "", false
	}
	info, statErr := os.Stat(sessionDir)
	if statErr != nil || !info.IsDir() {
		return "", false
	}
	return sessionDir, true
}

func skillsRouteTellQueen(session *Session, message string) bool {
	if session == nil || session.QueenExecutor == nil {
		return false
	}
	node := session.QueenExecutor.QueenNode()
	if node == nil {
		return false
	}
	if err := node.InjectEvent(message, true); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Reload fan-out
// ---------------------------------------------------------------------------

// reloadQueenSkills reloads the request-scoped admin manager and every live
// runtime the session manager exposes, mirroring _reload_scope. The returned
// report describes the live fan-out (how many catalogs rebuilt, which failed);
// per-runtime failures are logged and never fail the caller.
func (d SkillsRouteDeps) reloadQueenSkills(scope *skillsRouteQueenScope) skills.ReloadReport {
	if scope == nil {
		return skills.ReloadReport{}
	}
	if scope.manager != nil {
		lock := scope.manager.MutationLock()
		lock.Lock()
		if err := scope.manager.Reload(); err != nil {
			log.Printf("skills: primary manager reload failed: %v", err)
		}
		lock.Unlock()
	}
	return d.reloadLiveSkills(&scope.queenID)
}

// reloadLiveSkills reloads the live catalogs a skill change reaches through the
// session manager's skills-facing view. The SessionManager cannot satisfy
// skills.LiveSessionManager itself (its ListSessions returns *Session), so the
// bridge in live_skills.go adapts it; a nil manager yields an empty report.
func (d SkillsRouteDeps) reloadLiveSkills(queenID *string) skills.ReloadReport {
	if d.Manager == nil {
		return skills.ReloadReport{}
	}
	return skills.ReloadLiveSkillsReport(d.Manager.LiveSkills(), queenID)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func skillsRouteError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, skillsRouteErrorBody(message))
}

func skillsRouteErrorBody(message string) map[string]any {
	return map[string]any{"error": message}
}

func skillsRouteRequesterID(r *http.Request) string {
	if value := r.Header.Get("X-User"); value != "" {
		return value
	}
	return "ui"
}

func skillsRouteStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func skillsRoutePyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

func skillsRouteDecodeJSONBody(r *http.Request) (map[string]any, bool) {
	var data []byte
	if r.Body != nil {
		var err error
		data, err = io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			return nil, false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, false
	}
	payload, ok := decoded.(map[string]any)
	if !ok {
		return nil, false
	}
	return payload, true
}
