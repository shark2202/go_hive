package server

// Parity: framework/server/routes_queens.py::register_routes
// Parity: framework/agents/queen/queen_profiles.py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/queen"
	"gopkg.in/yaml.v3"
)

// QueenSessionCreateParams is the keyword set routes_queens forwards to
// SessionManager.create_session. Nil pointers mirror Python's None; the
// fields are `any` because the Python handlers pass request-body values
// through without coercing them.
type QueenSessionCreateParams struct {
	ColonyID        *string
	QueenResumeFrom *string
	InitialPrompt   any
	QueenName       *string
	InitialPhase    any
	CRMSetup        bool
}

// QueenRouteDeps wires the queen identity/session routes to the runtime.
// Everything that would start a queen, swap a provider or refresh a system
// prompt lives behind a hook: the route owns body validation and the
// status/JSON contract, the hook owns orchestration — the same split the
// session routes use.
type QueenRouteDeps struct {
	Home    hivehome.Home
	Manager *SessionManager

	CreateSession func(QueenSessionCreateParams) (*Session, error)
	StopSession   func(sessionID string) error

	// SchedulePush mirrors framework.cloud_sync_hooks.schedule_push(kind, key).
	SchedulePush func(kind, key string)
	// ValidateProviderKey mirrors routes_config._validate_provider_key and
	// returns (valid, message).
	ValidateProviderKey func(provider, apiKey, apiBase, model string) (bool, string)
	// ApplyQueenModel mirrors queen_models.apply: reconfigure a live
	// session's provider for the queen's queen_llm section (or back to the
	// main model). Reports whether the session was switched.
	ApplyQueenModel func(session *Session, queenID string, defaultModel string) bool
	// RefreshQueenIdentity re-materializes the identity prompt of every live
	// session of the queen after a profile edit.
	RefreshQueenIdentity func(queenID string, profile map[string]any)
	// MemoryDir overrides the queen memory directory resolver.
	MemoryDir func(queenID string) string
	// DefaultModel is the manager's main model, reported by GET .../llm.
	DefaultModel func() string
	// Now overrides the clock (snapshot names, overview timestamps).
	Now func() time.Time
}

func (deps QueenRouteDeps) queensDir() string {
	return deps.Home.QueensDir()
}

func (deps QueenRouteDeps) now() time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

func (deps QueenRouteDeps) schedulePush(kind, key string) {
	if deps.SchedulePush != nil {
		deps.SchedulePush(kind, key)
	}
}

func (deps QueenRouteDeps) memoryDir(queenID string) string {
	if deps.MemoryDir != nil {
		return deps.MemoryDir(queenID)
	}
	return memory.QueenMemoryDir(deps.Home.Root, queenID)
}

func (deps QueenRouteDeps) defaultModel() string {
	if deps.DefaultModel != nil {
		return deps.DefaultModel()
	}
	return ""
}

// queenProfileError maps the profile loader's failure modes onto the route
// contract: a missing profile is a 404, every other read failure a 500.
func queenProfileError(w http.ResponseWriter, err error) {
	var notFound *queen.ProfileNotFoundError
	if errors.As(err, &notFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Queen '" + notFound.ID + "' not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
}

func queenNotFound(w http.ResponseWriter, queenID string) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "Queen '" + queenID + "' not found"})
}

// queenLoadProfile is the routes' load_queen_profile: the default profile is
// materialized lazily, so only an unknown id is a 404.
func queenLoadProfile(deps QueenRouteDeps, queenID string) (map[string]any, error) {
	profile, err := queen.LoadQueenProfile(deps.queensDir(), queenID)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

// Parity: framework/server/routes_queens.py::handle_list_profiles
func handleQueenListProfiles(w http.ResponseWriter, deps QueenRouteDeps) {
	queen.EnsureDefaultQueens()
	queens := queen.ListQueens(deps.queensDir())
	if queens == nil {
		queens = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queens": queens})
}

// Parity: framework/server/routes_queens.py::handle_list_removed
func handleQueenListRemoved(w http.ResponseWriter, deps QueenRouteDeps) {
	queens := queen.ListRemovedQueens(deps.queensDir())
	if queens == nil {
		queens = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queens": queens})
}

// Parity: framework/server/routes_queens.py::_set_removed
func queenSetRemoved(deps QueenRouteDeps, queenID string, removed bool) (int, map[string]any) {
	if err := setQueenRemoved(deps.queensDir(), queenID, removed); err != nil {
		var notFound *queen.ProfileNotFoundError
		if errors.As(err, &notFound) {
			return http.StatusNotFound, map[string]any{"error": "Queen '" + queenID + "' not found"}
		}
		return http.StatusInternalServerError, map[string]any{"error": internalServerError}
	}
	return http.StatusOK, map[string]any{"queen_id": queenID, "removed": removed}
}

// Parity: framework/agents/queen/queen_profiles.py::set_queen_removed
func setQueenRemoved(queensDir, queenID string, removed bool) error {
	if !queen.IsDefaultQueen(queenID) {
		if _, err := os.Stat(filepath.Join(queensDir, queenID, "profile.yaml")); err != nil {
			return &queen.ProfileNotFoundError{ID: queenID}
		}
	}
	catalogPath := filepath.Join(queensDir, "catalog.json")
	data := map[string]any{}
	if raw, err := os.ReadFile(catalogPath); err == nil {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			if object, ok := decoded.(map[string]any); ok {
				data = object
			}
		}
	}
	ids := queen.RemovedQueenIDs(queensDir)
	if removed {
		ids[queenID] = true
	} else {
		delete(ids, queenID)
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	list := make([]any, 0, len(ordered))
	for _, id := range ordered {
		list = append(list, id)
	}
	data["removed"] = list
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return queenAtomicWrite(catalogPath, string(encoded))
}

// queenAtomicWrite mirrors queen_profiles._atomic_write_text: tempfile in the
// same directory plus os.replace, so readers never see a half-written file.
func queenAtomicWrite(path, data string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".profile.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Parity: framework/server/routes_queens.py::handle_create_queen
func handleQueenCreate(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	var body map[string]any
	if !queenDecodeObject(w, r, "Invalid JSON body", "Body must be an object", &body) {
		return
	}
	entry, status, message := createQueenProfile(deps.queensDir(),
		queenStr(body["name"]),
		queenStr(body["title"]),
		queenStr(body["persona"]),
		queenStr(body["queen_id"]),
	)
	if status != http.StatusCreated {
		if status == http.StatusConflict {
			writeJSON(w, status, map[string]any{"error": "queen id already exists: " + message})
			return
		}
		writeJSON(w, status, map[string]any{"error": message})
		return
	}
	deps.schedulePush("queen_profile", entry["id"].(string))
	writeJSON(w, http.StatusCreated, map[string]any{"queen": entry})
}

// Parity: framework/agents/queen/queen_profiles.py::create_queen_profile
func createQueenProfile(queensDir, name, title, persona, queenID string) (map[string]any, int, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, http.StatusBadRequest, "name is required"
	}
	qid := strings.TrimSpace(queenID)
	if qid == "" {
		qid = slugifyQueenID(name)
	}
	if !queenIDPattern.MatchString(qid) {
		return nil, http.StatusBadRequest, "queen_id must match [a-z0-9][a-z0-9_-]{0,62} (lowercase slug)"
	}
	if queen.IsDefaultQueen(qid) {
		return nil, http.StatusConflict, qid
	}
	if _, err := os.Stat(filepath.Join(queensDir, qid, "profile.yaml")); err == nil {
		return nil, http.StatusConflict, qid
	}
	profile := map[string]any{"name": name, "title": strings.TrimSpace(title)}
	if strings.TrimSpace(persona) != "" {
		profile["core_traits"] = strings.TrimSpace(persona)
	}
	encoded, err := marshalQueenYAML(profile)
	if err != nil {
		return nil, http.StatusInternalServerError, internalServerError
	}
	if err := queenAtomicWrite(filepath.Join(queensDir, qid, "profile.yaml"), string(encoded)); err != nil {
		return nil, http.StatusInternalServerError, internalServerError
	}
	return map[string]any{
		"id":         qid,
		"name":       name,
		"title":      profile["title"],
		"has_avatar": false,
		"custom":     true,
	}, http.StatusCreated, ""
}

var queenIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var queenSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// Parity: framework/agents/queen/queen_profiles.py::_slugify_queen_id
//
// Python falls back to `hash(name)`, which is salted per process; Go uses a
// stable FNV-1a digest so the generated id is deterministic across restarts.
func slugifyQueenID(name string) string {
	slug := strings.Trim(queenSlugPattern.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if len(slug) > 40 {
		slug = slug[:40]
	}
	if slug == "" {
		slug = fmt.Sprintf("custom_%05d", fnv32(name)%100000)
	}
	if !strings.HasPrefix(slug, "queen_") {
		return "queen_" + slug
	}
	return slug
}

func fnv32(text string) int {
	var hash uint32 = 2166136261
	for i := 0; i < len(text); i++ {
		hash ^= uint32(text[i])
		hash *= 16777619
	}
	return int(hash % 100000)
}

// Parity: framework/server/routes_queens.py::_transform_profile_for_api
func transformQueenProfileForAPI(profile map[string]any) map[string]any {
	result := map[string]any{
		"name":  mapString(profile, "name"),
		"title": mapString(profile, "title"),
	}
	summary := []string{}
	if traits := mapString(profile, "core_traits"); traits != "" {
		summary = append(summary, traits)
	}
	if psych, ok := profile["psychological_profile"].(map[string]any); ok {
		if anti := mapString(psych, "anti_stereotype"); anti != "" {
			summary = append(summary, anti)
		}
	}
	if len(summary) > 0 {
		result["summary"] = strings.Join(summary, "\n\n")
	}

	if hidden, ok := profile["hidden_background"].(map[string]any); ok {
		details := []any{}
		if wound := mapString(hidden, "past_wound"); wound != "" {
			details = append(details, "Background: "+wound)
		}
		if motive := mapString(hidden, "deep_motive"); motive != "" {
			details = append(details, "Drive: "+motive)
		}
		if mapping := mapString(hidden, "behavioral_mapping"); mapping != "" {
			details = append(details, "Approach: "+mapping)
		}
		if len(details) > 0 {
			role := mapString(profile, "title")
			if role == "" {
				role = "Executive Advisor"
			}
			result["experience"] = []any{map[string]any{"role": role, "details": details}}
		}
	}
	if skills, ok := profile["skills"]; ok && pyTruthy(skills) {
		result["skills"] = skills
	}
	if world, ok := profile["world_lore"].(map[string]any); ok {
		if habitat := mapString(world, "habitat"); habitat != "" {
			result["signature_achievement"] = strings.TrimSpace(habitat + ". " + mapString(world, "lexicon"))
		}
	}
	if portrait, ok := profile["portrait"]; ok && pyTruthy(portrait) {
		result["portrait"] = portrait
	}
	return result
}

// Parity: framework/server/routes_queens.py::_reverse_transform_for_yaml
func reverseTransformQueenProfile(body map[string]any) map[string]any {
	updates := map[string]any{}
	if value, ok := body["name"]; ok {
		updates["name"] = value
	}
	if value, ok := body["title"]; ok {
		updates["title"] = value
	}
	if value, ok := body["summary"]; ok {
		updates["core_traits"] = value
	}
	if value, ok := body["skills"]; ok {
		updates["skills"] = value
	}
	if value, ok := body["signature_achievement"]; ok {
		world := map[string]any{}
		if existing, ok := updates["world_lore"].(map[string]any); ok {
			world = existing
		}
		world["habitat"] = value
		updates["world_lore"] = world
	}
	if value, ok := body["portrait"]; ok {
		updates["portrait"] = value
	}
	return updates
}

// Parity: framework/server/routes_queens.py::handle_get_profile
func handleQueenGetProfile(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	queen.EnsureDefaultQueens()
	profile, err := queenLoadProfile(deps, queenID)
	if err != nil {
		queenProfileError(w, err)
		return
	}
	body := transformQueenProfileForAPI(profile)
	body["id"] = queenID
	body["has_avatar"] = queen.QueenHasAvatar(deps.queensDir(), queenID)
	writeJSON(w, http.StatusOK, body)
}

// Parity: framework/server/routes_queens.py::handle_update_profile
func handleQueenUpdateProfile(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	var body map[string]any
	if !queenDecodeObject(w, r, "Invalid JSON body", "Body must be a JSON object", &body) {
		return
	}
	updates := reverseTransformQueenProfile(body)
	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "No valid fields to update"})
		return
	}
	updated, err := updateQueenProfile(deps.queensDir(), queenID, updates)
	if err != nil {
		queenProfileError(w, err)
		return
	}
	deps.schedulePush("queen_profile", queenID)
	if deps.RefreshQueenIdentity != nil {
		deps.RefreshQueenIdentity(queenID, updated)
	}
	result := transformQueenProfileForAPI(updated)
	result["id"] = queenID
	result["has_avatar"] = queen.QueenHasAvatar(deps.queensDir(), queenID)
	writeJSON(w, http.StatusOK, result)
}

// Parity: framework/agents/queen/queen_profiles.py::update_queen_profile
func updateQueenProfile(queensDir, queenID string, updates map[string]any) (map[string]any, error) {
	profile, err := queen.LoadQueenProfile(queensDir, queenID)
	if err != nil {
		return nil, err
	}
	for key, value := range updates {
		if incoming, ok := value.(map[string]any); ok {
			if existing, ok := profile[key].(map[string]any); ok {
				for subKey, subValue := range incoming {
					existing[subKey] = subValue
				}
				continue
			}
		}
		profile[key] = value
	}
	if err := writeQueenProfileYAML(filepath.Join(queensDir, queenID, "profile.yaml"), profile); err != nil {
		return nil, err
	}
	return profile, nil
}

func writeQueenProfileYAML(path string, profile map[string]any) error {
	encoded, err := marshalQueenYAML(tidyProfileValue(profile))
	if err != nil {
		return err
	}
	return queenAtomicWrite(path, string(encoded))
}

// marshalQueenYAML renders a profile with two-space indentation, matching
// yaml.safe_dump's readable literal blocks for multi-line persona text.
func marshalQueenYAML(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Parity: framework/agents/queen/queen_profiles.py::_tidy
func tidyProfileValue(value any) any {
	switch typed := value.(type) {
	case string:
		if strings.Contains(typed, "\n") {
			lines := strings.Split(typed, "\n")
			for i, line := range lines {
				lines[i] = strings.TrimRight(line, " \t")
			}
			return strings.Join(lines, "\n")
		}
		return typed
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = tidyProfileValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = tidyProfileValue(item)
		}
		return out
	}
	return value
}

// Parity: framework/server/routes_queens.py::handle_get_queen_llm
func handleQueenGetLLM(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	if _, err := queenLoadProfile(deps, queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	config := HiveConfig(deps.Home)
	var section any
	if sections, ok := config["queen_llm"].(map[string]any); ok {
		if value, ok := sections[queenID]; ok {
			if object, ok := value.(map[string]any); ok {
				section = object
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queen_id":      queenID,
		"section":       section,
		"default_model": deps.defaultModel(),
	})
}

// Parity: framework/server/routes_queens.py::handle_put_queen_llm
func handleQueenPutLLM(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	if _, err := queenLoadProfile(deps, queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, isObject := decoded.(map[string]any)
	if !isObject {
		body = map[string]any{}
	}
	raw, hasSection := body["section"]
	var section map[string]any
	if hasSection && raw != nil {
		object, ok := raw.(map[string]any)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'section' must be a JSON object or null"})
			return
		}
		if strings.TrimSpace(queenStr(object["provider"])) == "" || strings.TrimSpace(queenStr(object["model"])) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'provider' and 'model' are required"})
			return
		}
		section = object
		apiKey := queenStr(object["api_key"])
		if apiKey == "" {
			if envName := queenStr(object["api_key_env_var"]); envName != "" {
				apiKey = os.Getenv(envName)
			}
		}
		apiBase := strings.TrimSpace(queenStr(object["api_base"]))
		validate := true
		if value, ok := body["validate"]; ok {
			validate = pyTruthy(value)
		}
		if validate && apiKey != "" && apiBase != "" && deps.ValidateProviderKey != nil {
			valid, message := deps.ValidateProviderKey(queenStr(object["provider"]), apiKey, apiBase, queenStr(object["model"]))
			if !valid {
				if message == "" {
					message = "unknown error"
				}
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": fmt.Sprintf("Key check failed against %s: %s", apiBase, message),
				})
				return
			}
		}
	}

	config := HiveConfig(deps.Home)
	sections, _ := config["queen_llm"].(map[string]any)
	if sections == nil {
		sections = map[string]any{}
	}
	if section == nil {
		delete(sections, queenID)
	} else {
		sections[queenID] = section
	}
	if len(sections) > 0 {
		config["queen_llm"] = sections
	} else {
		delete(config, "queen_llm")
	}
	if err := WriteHiveConfig(deps.Home, config); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}

	if deps.Manager != nil {
		switched := 0
		for _, session := range deps.Manager.ListSessions() {
			if session.QueenName != queenID {
				continue
			}
			if deps.ApplyQueenModel != nil {
				if deps.ApplyQueenModel(session, queenID, deps.defaultModel()) {
					switched++
				}
				continue
			}
			if applyQueenModelSection(deps.Home, session) {
				switched++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"queen_id":          queenID,
			"section":           section,
			"sessions_switched": switched,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queen_id":          queenID,
		"section":           section,
		"sessions_switched": 0,
	})
}

// applyQueenModelSection mirrors queen_models.apply for one live session: her
// own queen_llm section when she has one (the body was just written to
// configuration.json), otherwise the main llm section she returns to. The
// session's provider is rebuilt in place; the running turn keeps its pinned
// provider and the next turn resolves the new one.
//
// Parity: framework/server/queen_models.py::apply
func applyQueenModelSection(home hivehome.Home, session *Session) bool {
	if session == nil || session.LLM == nil {
		return false
	}
	section := QueenLLMSection(home, session.QueenName)
	if section == nil {
		main, _ := HiveConfig(home)["llm"].(map[string]any)
		if main == nil {
			return false
		}
		section = main
	}
	switched := ReconfigureSection(session.LLM, home, section)
	// A colony-bound session executes through its colony's overseer, whose
	// transport is separate in Go. Upstream shares the session's provider
	// object with the colony when no worker_llm is set, so her new model must
	// reach the colony too or the running session keeps the old one.
	if holder, ok := session.Colony.(interface{ LLM() any }); ok {
		if ReconfigureSection(holder.LLM(), home, section) {
			switched = true
		}
	}
	return switched
}

var queenPersonaTextFieldSet = map[string]bool{
	"name": true, "title": true, "core_traits": true, "skills": true,
}
var queenPersonaMapFieldSet = map[string]bool{
	"hidden_background": true, "psychological_profile": true, "world_lore": true,
}
var queenPersonaFields = []string{
	"name", "title", "core_traits", "skills",
	"hidden_background", "psychological_profile", "world_lore",
	"behavior_triggers",
}

// Parity: framework/server/routes_queens.py::_persona_view
func queenPersonaView(profile map[string]any) map[string]any {
	out := map[string]any{}
	for _, field := range queenPersonaFields {
		if value, ok := profile[field]; ok {
			out[field] = value
		}
	}
	return out
}

// Parity: framework/server/routes_queens.py::_persona_problem
func queenPersonaProblem(fields map[string]any) string {
	for key, value := range fields {
		switch {
		case queenPersonaTextFieldSet[key]:
			text, ok := value.(string)
			if !ok {
				return fmt.Sprintf("'%s' must be text", key)
			}
			if (key == "name" || key == "title") && strings.TrimSpace(text) == "" {
				return fmt.Sprintf("'%s' can't be empty", key)
			}
		case queenPersonaMapFieldSet[key]:
			if value == nil {
				continue
			}
			object, ok := value.(map[string]any)
			if !ok {
				return fmt.Sprintf("'%s' must map names to text", key)
			}
			for _, item := range object {
				if _, ok := item.(string); !ok {
					return fmt.Sprintf("'%s' must map names to text", key)
				}
			}
		case key == "behavior_triggers":
			if value == nil {
				continue
			}
			items, ok := value.([]any)
			if !ok {
				return "'behavior_triggers' must be a list of {trigger, reaction} texts"
			}
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					return "'behavior_triggers' must be a list of {trigger, reaction} texts"
				}
				if _, ok := entry["trigger"].(string); !ok {
					return "'behavior_triggers' must be a list of {trigger, reaction} texts"
				}
				if _, ok := entry["reaction"].(string); !ok {
					return "'behavior_triggers' must be a list of {trigger, reaction} texts"
				}
			}
		default:
			return fmt.Sprintf("Unknown persona field '%s'", key)
		}
	}
	return ""
}

// Parity: framework/server/routes_queens.py::handle_get_persona
func handleQueenGetPersona(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	profile, err := queenLoadProfile(deps, queenID)
	if err != nil {
		queenProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queen_id": queenID, "persona": queenPersonaView(profile)})
}

// Parity: framework/server/routes_queens.py::handle_put_persona
func handleQueenPutPersona(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	var fields map[string]any
	if body, ok := decoded.(map[string]any); ok {
		fields, _ = body["persona"].(map[string]any)
	}
	if len(fields) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be {'persona': {field: value}}"})
		return
	}
	if problem := queenPersonaProblem(fields); problem != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": problem})
		return
	}
	updated, err := replaceQueenPersona(deps, queenID, fields)
	if err != nil {
		queenProfileError(w, err)
		return
	}
	deps.schedulePush("queen_profile", queenID)
	if deps.RefreshQueenIdentity != nil {
		deps.RefreshQueenIdentity(queenID, updated)
	}
	writeJSON(w, http.StatusOK, map[string]any{"queen_id": queenID, "persona": queenPersonaView(updated)})
}

const queenDashboardSnapshots = 30

// Parity: framework/agents/queen/queen_profiles.py::replace_queen_persona
func replaceQueenPersona(deps QueenRouteDeps, queenID string, fields map[string]any) (map[string]any, error) {
	profilePath := filepath.Join(deps.queensDir(), queenID, "profile.yaml")
	if _, err := os.Stat(profilePath); err != nil {
		profile, err := queen.LoadQueenProfile(deps.queensDir(), queenID)
		if err != nil {
			return nil, err
		}
		if err := writeQueenProfileYAML(profilePath, profile); err != nil {
			return nil, err
		}
	}
	original, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	if len(original) > 0 {
		parsed, err := parseQueenYAML(original)
		if err != nil {
			return nil, err
		}
		if parsed != nil {
			data = parsed
		}
	}
	for key, value := range fields {
		if value == nil {
			delete(data, key)
		} else {
			data[key] = value
		}
	}
	snapshots := filepath.Join(filepath.Dir(profilePath), "_profile_snapshots")
	if err := os.MkdirAll(snapshots, 0o755); err != nil {
		return nil, err
	}
	stamp := deps.now().Format("20060102-150405")
	snapshotPath := filepath.Join(snapshots, "profile.yaml."+stamp+".pre-dashboard-edit")
	if err := os.WriteFile(snapshotPath, original, 0o644); err != nil {
		return nil, err
	}
	pruneQueenSnapshots(snapshots)
	if err := writeQueenProfileYAML(profilePath, data); err != nil {
		return nil, err
	}
	return queen.LoadQueenProfile(deps.queensDir(), queenID)
}

func pruneQueenSnapshots(snapshots string) {
	matches, err := filepath.Glob(filepath.Join(snapshots, "profile.yaml.*.pre-dashboard-edit"))
	if err != nil {
		return
	}
	sort.Strings(matches)
	if len(matches) <= queenDashboardSnapshots {
		return
	}
	for _, old := range matches[:len(matches)-queenDashboardSnapshots] {
		_ = os.Remove(old)
	}
}

func parseQueenYAML(data []byte) (map[string]any, error) {
	var parsed any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, nil
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, errors.New("queen profile is not a mapping")
	}
	return object, nil
}

// Parity: framework/server/routes_queens.py::handle_get_overview
func handleQueenOverview(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	if _, err := queenLoadProfile(deps, queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	memories := memory.ScanMemoryFiles(deps.memoryDir(queenID))
	byType := map[string]int{}
	for _, file := range memories {
		kind := "other"
		if file.Type != nil && *file.Type != "" {
			kind = *file.Type
		}
		byType[kind] = byType[kind] + 1
	}

	sessionsRoot := filepath.Join(deps.queensDir(), queenID, "sessions")
	sessionDirs := []string{}
	if entries, err := os.ReadDir(sessionsRoot); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				sessionDirs = append(sessionDirs, filepath.Join(sessionsRoot, entry.Name()))
			}
		}
	}
	started := []string{}
	var lastActive float64
	hasActive := false
	for _, dir := range sessionDirs {
		if match := queenSessionDatePattern.FindStringSubmatch(filepath.Base(dir)); match != nil {
			started = append(started, match[1])
		}
		events := filepath.Join(dir, "events.jsonl")
		if info, err := os.Stat(events); err == nil && !info.IsDir() {
			mtime := float64(info.ModTime().UnixNano()) / 1e9
			if !hasActive || mtime > lastActive {
				lastActive = mtime
				hasActive = true
			}
		}
	}
	sort.Strings(started)
	var first any
	if len(started) > 0 {
		first = started[0][:4] + "-" + started[0][4:6] + "-" + started[0][6:]
	}
	var lastActiveISO any
	if hasActive {
		lastActiveISO = queenLocalISOMinutes(lastActive)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queen_id": queenID,
		"memory": map[string]any{
			"count":   len(memories),
			"limit":   memory.MaxFiles,
			"by_type": byType,
		},
		"sessions": map[string]any{
			"count":       len(sessionDirs),
			"first":       first,
			"last_active": lastActiveISO,
		},
	})
}

var queenSessionDatePattern = regexp.MustCompile(`^session_(\d{8})_`)

func queenLocalISOMinutes(epoch float64) string {
	seconds := int64(math.Floor(epoch))
	nanos := int64((epoch - math.Floor(epoch)) * 1e9)
	return time.Unix(seconds, nanos).In(time.Local).Format("2006-01-02T15:04-07:00")
}

// Parity: framework/server/routes_queens.py::handle_initialize_preferences
func handleQueenInitializePreferences(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	var body map[string]any
	if !queenDecodeObject(w, r, "Invalid JSON body", "Body must be a JSON object", &body) {
		return
	}
	rawQueens, ok := body["queens"].(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must include 'queens' map"})
		return
	}
	initialized := []string{}
	skipped := []string{}
	for queenID, rawOverride := range rawQueens {
		override, ok := rawOverride.(map[string]any)
		if !ok {
			continue
		}
		name, ok := override["n"].(string)
		if !ok || name == "" {
			continue
		}
		if !queen.IsDefaultQueen(queenID) {
			skipped = append(skipped, queenID)
			continue
		}
		profilePath := filepath.Join(deps.queensDir(), queenID, "profile.yaml")
		if _, err := os.Stat(profilePath); err == nil {
			onDisk := map[string]any(nil)
			if data, err := os.ReadFile(profilePath); err == nil {
				if parsed, err := parseQueenYAML(data); err == nil {
					onDisk = parsed
				}
			}
			if !reflect.DeepEqual(onDisk, queen.DefaultProfilePersona(queenID)) {
				skipped = append(skipped, queenID)
				continue
			}
		}
		if _, err := queen.LoadQueenProfile(deps.queensDir(), queenID); err != nil {
			skipped = append(skipped, queenID)
			continue
		}
		updates := map[string]any{"name": name}
		if bio, ok := override["bio"].(string); ok && bio != "" {
			updates["core_traits"] = bio
		}
		if portrait, ok := override["p"].(map[string]any); ok {
			updates["portrait"] = portrait
		}
		if _, err := updateQueenProfile(deps.queensDir(), queenID, updates); err != nil {
			skipped = append(skipped, queenID)
			continue
		}
		initialized = append(initialized, queenID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": initialized, "skipped": skipped})
}

func queenDecodeObject(w http.ResponseWriter, r *http.Request, invalidMessage, notObjectMessage string, target *map[string]any) bool {
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": invalidMessage})
		return false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": notObjectMessage})
		return false
	}
	*target = object
	return true
}

// queenStr is Python's `str(value or "")` for the JSON shapes these routes
// accept: strings pass through, numbers keep their spelling, everything else
// stringifies like Python before the empty-string fallback.
func queenStr(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return pyStr(value)
}

func mapString(object map[string]any, key string) string {
	text, _ := object[key].(string)
	return text
}

// RegisterQueenRoutes mounts the queen identity, avatar and session routes.
//
// Parity: framework/server/routes_queens.py::register_routes
func RegisterQueenRoutes(mux *http.ServeMux, deps QueenRouteDeps) {
	mux.HandleFunc("GET /api/queen/profiles", func(w http.ResponseWriter, r *http.Request) {
		handleQueenListProfiles(w, deps)
	})
	mux.HandleFunc("GET /api/queen/removed", func(w http.ResponseWriter, r *http.Request) {
		handleQueenListRemoved(w, deps)
	})
	mux.HandleFunc("POST /api/queen/create", func(w http.ResponseWriter, r *http.Request) {
		handleQueenCreate(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen/initialize-preferences", func(w http.ResponseWriter, r *http.Request) {
		handleQueenInitializePreferences(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen/{queen_id}/session", func(w http.ResponseWriter, r *http.Request) {
		handleQueenSession(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen/{queen_id}/session/select", func(w http.ResponseWriter, r *http.Request) {
		handleQueenSelectSession(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen/{queen_id}/session/new", func(w http.ResponseWriter, r *http.Request) {
		handleQueenNewSession(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen/{queen_id}/remove", func(w http.ResponseWriter, r *http.Request) {
		status, body := queenSetRemoved(deps, r.PathValue("queen_id"), true)
		writeJSON(w, status, body)
	})
	mux.HandleFunc("POST /api/queen/{queen_id}/restore", func(w http.ResponseWriter, r *http.Request) {
		status, body := queenSetRemoved(deps, r.PathValue("queen_id"), false)
		writeJSON(w, status, body)
	})
	mux.HandleFunc("GET /api/queen/{queen_id}/profile", func(w http.ResponseWriter, r *http.Request) {
		handleQueenGetProfile(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/queen/{queen_id}/profile", func(w http.ResponseWriter, r *http.Request) {
		handleQueenUpdateProfile(w, r, deps)
	})
	mux.HandleFunc("GET /api/queen/{queen_id}/llm", func(w http.ResponseWriter, r *http.Request) {
		handleQueenGetLLM(w, r, deps)
	})
	mux.HandleFunc("PUT /api/queen/{queen_id}/llm", func(w http.ResponseWriter, r *http.Request) {
		handleQueenPutLLM(w, r, deps)
	})
	mux.HandleFunc("GET /api/queen/{queen_id}/persona", func(w http.ResponseWriter, r *http.Request) {
		handleQueenGetPersona(w, r, deps)
	})
	mux.HandleFunc("PUT /api/queen/{queen_id}/persona", func(w http.ResponseWriter, r *http.Request) {
		handleQueenPutPersona(w, r, deps)
	})
	mux.HandleFunc("GET /api/queen/{queen_id}/overview", func(w http.ResponseWriter, r *http.Request) {
		handleQueenOverview(w, r, deps)
	})
	mux.HandleFunc("POST /api/queen/{queen_id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		handleQueenUploadAvatar(w, r, deps)
	})
	mux.HandleFunc("GET /api/queen/{queen_id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		handleQueenGetAvatar(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/queen/{queen_id}/avatar", func(w http.ResponseWriter, r *http.Request) {
		handleQueenDeleteAvatar(w, r, deps)
	})
}
