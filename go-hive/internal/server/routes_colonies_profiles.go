package server

// Parity: framework/server/routes_colonies.py::handle_list_worker_profiles
// Parity: framework/server/routes_colonies.py::handle_upsert_worker_profile
// Parity: framework/server/routes_colonies.py::handle_delete_worker_profile

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const defaultWorkerProfileName = "default"

const workerProfileNameError = "profile name must be lowercase alphanumeric (with - or _), start with a letter/digit, and be ≤64 characters"

type WorkerProfile struct {
	Name            string
	Task            string
	SkillName       string
	Integrations    map[string]string
	ConcurrencyHint *int
	PromptOverride  any
	ToolFilter      []any
	BrowserProfile  string
}

func (p WorkerProfile) ToDict() map[string]any {
	integrations := map[string]any{}
	for key, value := range p.Integrations {
		integrations[key] = value
	}
	out := map[string]any{
		"name":         p.Name,
		"task":         p.Task,
		"skill_name":   p.SkillName,
		"integrations": integrations,
	}
	if p.ConcurrencyHint != nil {
		out["concurrency_hint"] = *p.ConcurrencyHint
	}
	if p.PromptOverride != nil {
		out["prompt_override"] = p.PromptOverride
	}
	if p.ToolFilter != nil {
		out["tool_filter"] = p.ToolFilter
	}
	if p.BrowserProfile != "" {
		out["browser_profile"] = p.BrowserProfile
	}
	return out
}

func workerProfileFromDict(data map[string]any) WorkerProfile {
	profile := WorkerProfile{
		Name:           strings.TrimSpace(pythonString(data["name"])),
		Task:           pythonString(data["task"]),
		SkillName:      pythonString(data["skill_name"]),
		Integrations:   map[string]string{},
		BrowserProfile: strings.TrimSpace(pythonString(data["browser_profile"])),
	}
	if raw, ok := data["integrations"].(map[string]any); ok {
		for key, value := range raw {
			name := pythonString(key)
			text := pythonString(value)
			if name != "" && text != "" {
				profile.Integrations[name] = text
			}
		}
	}
	switch hint := data["concurrency_hint"].(type) {
	case bool:
		if hint {
			value := 1
			profile.ConcurrencyHint = &value
		}
	case json.Number:
		if value, err := strconv.Atoi(hint.String()); err == nil && value > 0 {
			profile.ConcurrencyHint = &value
		}
	case float64:
		if hint == float64(int(hint)) && hint > 0 {
			value := int(hint)
			profile.ConcurrencyHint = &value
		}
	}
	if override, present := data["prompt_override"]; present && pyTruthy(override) {
		profile.PromptOverride = override
	}
	if filter, ok := data["tool_filter"].([]any); ok {
		profile.ToolFilter = filter
	}
	return profile
}

func validateWorkerProfileName(name string) string {
	if name == "" || len(name) > 64 {
		return workerProfileNameError
	}
	for index, ch := range name {
		valid := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-'
		if index == 0 && !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
			valid = false
		}
		if !valid {
			return workerProfileNameError
		}
	}
	return ""
}

func listWorkerProfilesWithHome(home hivehome.Home, colonyID string) []WorkerProfile {
	metadata := loadColonyMetadata(home, colonyID)
	raw, ok := metadata["worker_profiles"].([]any)
	if !ok || len(raw) == 0 {
		return []WorkerProfile{{Name: defaultWorkerProfileName}}
	}
	profiles := []WorkerProfile{}
	seen := map[string]struct{}{}
	for _, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		profile := workerProfileFromDict(object)
		if profile.Name == "" {
			continue
		}
		if _, duplicate := seen[profile.Name]; duplicate {
			continue
		}
		if validateWorkerProfileName(profile.Name) != "" {
			continue
		}
		seen[profile.Name] = struct{}{}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return []WorkerProfile{{Name: defaultWorkerProfileName}}
	}
	return profiles
}

func saveWorkerProfiles(home hivehome.Home, colonyID string, profiles []WorkerProfile) ([]WorkerProfile, error) {
	if info, err := os.Stat(filepath.Dir(colonyMetadataPath(home, colonyID))); err != nil || !info.IsDir() {
		return nil, errors.New("Colony '" + colonyID + "' not found")
	}
	canonical := []WorkerProfile{}
	seen := map[string]struct{}{}
	for _, profile := range profiles {
		if err := validateWorkerProfileName(profile.Name); err != "" {
			return nil, errors.New(err)
		}
		if _, duplicate := seen[profile.Name]; duplicate {
			return nil, errors.New("duplicate worker profile name: '" + profile.Name + "'")
		}
		seen[profile.Name] = struct{}{}
		canonical = append(canonical, profile)
	}
	if len(canonical) == 0 {
		canonical = []WorkerProfile{{Name: defaultWorkerProfileName}}
	}
	encoded := make([]any, 0, len(canonical))
	for _, profile := range canonical {
		encoded = append(encoded, profile.ToDict())
	}
	if _, err := updateColonyMetadata(home, colonyID, map[string]any{"worker_profiles": encoded}); err != nil {
		return nil, err
	}
	return canonical, nil
}

func upsertWorkerProfile(home hivehome.Home, colonyID string, profile WorkerProfile) ([]WorkerProfile, error) {
	if err := validateWorkerProfileName(profile.Name); err != "" {
		return nil, errors.New(err)
	}
	existing := listWorkerProfilesWithHome(home, colonyID)
	out := make([]WorkerProfile, 0, len(existing)+1)
	for _, candidate := range existing {
		if candidate.Name != profile.Name {
			out = append(out, candidate)
		}
	}
	out = append(out, profile)
	return saveWorkerProfiles(home, colonyID, out)
}

func deleteWorkerProfile(home hivehome.Home, colonyID, profileName string) (bool, error) {
	if profileName == defaultWorkerProfileName {
		return false, errors.New("cannot delete the default worker profile")
	}
	existing := listWorkerProfilesWithHome(home, colonyID)
	out := make([]WorkerProfile, 0, len(existing))
	for _, profile := range existing {
		if profile.Name != profileName {
			out = append(out, profile)
		}
	}
	if len(out) == len(existing) {
		return false, nil
	}
	if _, err := saveWorkerProfiles(home, colonyID, out); err != nil {
		return false, err
	}
	return true, nil
}

// findWorkersBoundToProfile uses the optional runtime seam for live worker
// bindings; runtimes that do not expose it report no bindings.
func findWorkersBoundToProfile(deps ColonyRouteDeps, colonyID, profileName string) []string {
	if deps.Manager == nil {
		return []string{}
	}
	bound := []string{}
	for _, session := range deps.Manager.ListSessions() {
		if session == nil || session.ColonyID != colonyID || session.Colony == nil {
			continue
		}
		source, ok := session.Colony.(interface {
			BoundWorkersForProfile(string) []string
		})
		if !ok {
			continue
		}
		for _, workerID := range source.BoundWorkersForProfile(profileName) {
			if workerID != "" {
				bound = append(bound, workerID)
			}
		}
	}
	sort.Strings(bound)
	return bound
}

func workerProfilesToJSON(profiles []WorkerProfile) []any {
	out := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, profile.ToDict())
	}
	return out
}

func handleListWorkerProfiles(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if err := validateColonyName(colonyID); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}
	if info, err := os.Stat(deps.Home.ColonyDir(colonyID)); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "colony '" + colonyID + "' not found"})
		return
	}
	profiles := listWorkerProfilesWithHome(deps.Home, colonyID)
	writeJSON(w, http.StatusOK, map[string]any{"worker_profiles": workerProfilesToJSON(profiles)})
}

func handleUpsertWorkerProfile(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if err := validateColonyName(colonyID); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}
	if info, err := os.Stat(deps.Home.ColonyDir(colonyID)); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "colony '" + colonyID + "' not found"})
		return
	}
	decoded, err := decodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body must be a JSON object"})
		return
	}

	profile := workerProfileFromDict(body)
	if err := validateWorkerProfileName(profile.Name); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}
	saved, err := upsertWorkerProfile(deps.Home, colonyID, profile)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"worker_profiles": workerProfilesToJSON(saved)})
}

func handleDeleteWorkerProfile(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	colonyID := r.PathValue("colony_id")
	profileName := r.PathValue("profile_name")
	if err := validateColonyName(colonyID); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}
	if info, err := os.Stat(deps.Home.ColonyDir(colonyID)); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "colony '" + colonyID + "' not found"})
		return
	}
	if bound := findWorkersBoundToProfile(deps, colonyID, profileName); len(bound) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":         "profile is bound to live workers; stop them first",
			"bound_workers": bound,
		})
		return
	}
	removed, err := deleteWorkerProfile(deps.Home, colonyID, profileName)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !removed {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "profile '" + profileName + "' not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "profile_name": profileName})
}
