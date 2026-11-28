// Parity: framework/agents/queen/queen_profiles.py
package queen

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"gopkg.in/yaml.v3"
)

//go:embed queen_defaults/*.yaml
var queenDefaultsFS embed.FS

var capabilityKeys = map[string]bool{
	"default_tool_categories": true,
	"default_preset_skills":   true,
}

const catalogFile = "catalog.json"

var supersededTitles = map[string]map[string]bool{
	"queen_sales": {"Head of Sales": true},
}

var (
	defaultQueensOnce sync.Once
	defaultQueens     map[string]map[string]any
)

func DefaultQueens() map[string]map[string]any {
	loadDefaultQueens()
	out := make(map[string]map[string]any, len(defaultQueens))
	for id, profile := range defaultQueens {
		out[id] = profile
	}
	return out
}

func IsDefaultQueen(id string) bool {
	loadDefaultQueens()
	_, ok := defaultQueens[id]
	return ok
}

func DefaultQueenProfile(id string) map[string]any {
	loadDefaultQueens()
	return defaultQueens[id]
}

func loadDefaultQueens() {
	defaultQueensOnce.Do(func() {
		defaultQueens = map[string]map[string]any{}
		entries, err := queenDefaultsFS.ReadDir("queen_defaults")
		if err != nil {
			return
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			data, err := queenDefaultsFS.ReadFile(filepath.ToSlash(filepath.Join("queen_defaults", name)))
			if err != nil {
				continue
			}
			var parsed any
			if err := yaml.Unmarshal(data, &parsed); err != nil {
				continue
			}
			profile, ok := asStringMap(parsed)
			if !ok {
				continue
			}
			defaultQueens[strings.TrimSuffix(name, ".yaml")] = profile
		}
	})
}

func EnsureDefaultQueens() {}

func PersonaOnly(profile map[string]any) map[string]any {
	out := make(map[string]any, len(profile))
	for key, value := range profile {
		if capabilityKeys[key] {
			continue
		}
		out[key] = value
	}
	return out
}

func DefaultProfilePersona(id string) map[string]any {
	loadDefaultQueens()
	profile, ok := defaultQueens[id]
	if !ok {
		return nil
	}
	return PersonaOnly(profile)
}

func DefaultToolsFor(id string) []string {
	loadDefaultQueens()
	profile, ok := defaultQueens[id]
	if !ok {
		return nil
	}
	values, _ := profile["default_tool_categories"].([]any)
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func DefaultSkillsFor(id string) map[string]struct{} {
	loadDefaultQueens()
	return PresetSkillsFromProfile(defaultQueens[id])
}

// PresetSkillsFromProfile extracts a profile's `default_preset_skills` list.
// Custom queens carry the list in their on-disk profile.yaml; built-in queens
// are covered by DefaultSkillsFor, whose embedded profiles are the same shape.
// A missing or malformed list yields an empty (non-nil) set, matching
// queen_profiles.py::default_skills_for.
func PresetSkillsFromProfile(profile map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	if profile == nil {
		return out
	}
	values, _ := profile["default_preset_skills"].([]any)
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			out[text] = struct{}{}
		}
	}
	return out
}

func CurrentTitle(queenID string, onDiskTitle any) any {
	retired, ok := supersededTitles[queenID]
	if !ok {
		return onDiskTitle
	}
	text, isString := onDiskTitle.(string)
	if !isString || !retired[text] {
		return onDiskTitle
	}
	loadDefaultQueens()
	shipped, _ := defaultQueens[queenID]["title"].(string)
	if shipped == "" {
		return onDiskTitle
	}
	return shipped
}

// ValidQueenID reports whether id can be used as one queen directory name.
// Queen ids arrive from URL path values and request bodies; a separator or dot
// segment must never be joined under the queens root.
func ValidQueenID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\\x00")
}

func QueenHasAvatar(queensDir, queenID string) bool {
	if !ValidQueenID(queenID) {
		return false
	}
	dir := filepath.Join(queensDir, queenID)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(dir, "avatar.*"))
	if err != nil {
		return false
	}
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func RemovedQueenIDs(queensDir string) map[string]bool {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(queensDir, catalogFile))
	if err != nil {
		return out
	}
	var catalog any
	if err := json.Unmarshal(data, &catalog); err != nil {
		return out
	}
	object, ok := asStringMap(catalog)
	if !ok {
		return out
	}
	removed, ok := object["removed"].([]any)
	if !ok {
		return out
	}
	for _, value := range removed {
		if text, ok := value.(string); ok {
			out[text] = true
		}
	}
	return out
}

func ListQueens(queensDir string) []map[string]any {
	removed := RemovedQueenIDs(queensDir)
	known := KnownQueens(queensDir)
	out := make([]map[string]any, 0, len(known))
	for _, entry := range known {
		if removed[entry["id"].(string)] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func ListRemovedQueens(queensDir string) []map[string]any {
	removed := RemovedQueenIDs(queensDir)
	known := KnownQueens(queensDir)
	out := make([]map[string]any, 0, len(known))
	for _, entry := range known {
		if removed[entry["id"].(string)] {
			out = append(out, entry)
		}
	}
	return out
}

func KnownQueens(queensDir string) []map[string]any {
	loadDefaultQueens()
	results := map[string]map[string]any{}
	for id, profile := range defaultQueens {
		entry := map[string]any{
			"id":         id,
			"name":       stringOr(profile["name"]),
			"title":      stringOr(profile["title"]),
			"has_avatar": QueenHasAvatar(queensDir, id),
		}
		if portrait, ok := profile["portrait"]; ok && portrait != nil && portrait != "" {
			entry["portrait"] = portrait
		}
		results[id] = entry
	}

	matches, _ := filepath.Glob(filepath.Join(queensDir, "*", "profile.yaml"))
	sort.Strings(matches)
	for _, profilePath := range matches {
		queenID := filepath.Base(filepath.Dir(profilePath))
		data, err := os.ReadFile(profilePath)
		if err != nil {
			continue
		}
		var parsed any
		if err := yaml.Unmarshal(data, &parsed); err != nil {
			continue
		}
		profile, ok := asStringMap(parsed)
		if !ok {
			continue
		}
		entry := map[string]any{
			"id":         queenID,
			"name":       stringOr(profile["name"]),
			"title":      CurrentTitle(queenID, stringOr(profile["title"])),
			"has_avatar": QueenHasAvatar(queensDir, queenID),
			"custom":     !IsDefaultQueen(queenID),
		}
		if portrait, ok := profile["portrait"]; ok && portrait != nil && portrait != "" {
			entry["portrait"] = portrait
		}
		results[queenID] = entry
	}

	ids := make([]string, 0, len(results))
	for id := range results {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, results[id])
	}
	return out
}

func LoadQueenProfile(queensDir, queenID string) (map[string]any, error) {
	if !ValidQueenID(queenID) {
		return nil, &ProfileNotFoundError{ID: queenID}
	}
	profilePath := filepath.Join(queensDir, queenID, "profile.yaml")
	if _, err := os.Stat(profilePath); err != nil {
		materialized, ok := materializeDefaultQueen(queensDir, queenID)
		if !ok {
			return nil, &ProfileNotFoundError{ID: queenID}
		}
		profilePath = materialized
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	parsed, err := parseYAMLMap(data)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, errors.New("queen profile is empty")
	}
	if previous, ok := parsed["title"]; ok {
		parsed["title"] = CurrentTitle(queenID, previous)
	}
	return parsed, nil
}

// ProfileNotFoundError mirrors the FileNotFoundError raised when a queen
// profile is absent and no built-in default can materialize it. The create-
// session route maps it to 404.
type ProfileNotFoundError struct{ ID string }

func (e *ProfileNotFoundError) Error() string { return "Queen profile not found: " + e.ID }

func materializeDefaultQueen(queensDir, queenID string) (string, bool) {
	loadDefaultQueens()
	profile, ok := defaultQueens[queenID]
	if !ok {
		return "", false
	}
	profilePath := filepath.Join(queensDir, queenID, "profile.yaml")
	data, err := yaml.Marshal(PersonaOnly(profile))
	if err != nil {
		return "", false
	}
	// Atomic write: a concurrent reader must never observe a half-written
	// profile.yaml, since "still default" is decided by content equality.
	if err := atomicfs.WriteFile(profilePath, data, 0o644); err != nil {
		return "", false
	}
	return profilePath, true
}

func parseYAMLMap(data []byte) (map[string]any, error) {
	var parsed any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, nil
	}
	object, ok := asStringMap(parsed)
	if !ok {
		return nil, fmt.Errorf("queen profile is not a mapping")
	}
	return object, nil
}

func asStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			text, ok := key.(string)
			if !ok {
				return nil, false
			}
			out[text] = item
		}
		return out, true
	}
	return nil, false
}

func stringOr(value any) string {
	text, _ := value.(string)
	return text
}
