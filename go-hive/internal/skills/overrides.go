package skills

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

const OverrideSchemaVersion = 1

type Provenance string

const (
	ProvenanceFramework      Provenance = "framework"
	ProvenancePreset         Provenance = "preset"
	ProvenanceUserDropped    Provenance = "user_dropped"
	ProvenanceUserUICreated  Provenance = "user_ui_created"
	ProvenanceQueenCreated   Provenance = "queen_created"
	ProvenanceLearned        Provenance = "learned_runtime"
	ProvenanceProjectDropped Provenance = "project_dropped"
	ProvenanceOther          Provenance = "other"
)

func validProvenance(p Provenance) bool {
	switch p {
	case ProvenanceFramework, ProvenancePreset, ProvenanceUserDropped,
		ProvenanceUserUICreated, ProvenanceQueenCreated, ProvenanceLearned,
		ProvenanceProjectDropped, ProvenanceOther:
		return true
	}
	return false
}

type OverrideEntry struct {
	Enabled        *bool
	Provenance     Provenance
	Trust          *string
	ParamOverrides map[string]any
	Notes          *string
	CreatedAt      *time.Time
	CreatedBy      *string
}

func (e *OverrideEntry) Clone() *OverrideEntry {
	if e == nil {
		return nil
	}
	params := make(map[string]any, len(e.ParamOverrides))
	for k, v := range e.ParamOverrides {
		params[k] = v
	}
	return &OverrideEntry{
		Enabled:        e.Enabled,
		Provenance:     e.Provenance,
		Trust:          e.Trust,
		ParamOverrides: params,
		Notes:          e.Notes,
		CreatedAt:      e.CreatedAt,
		CreatedBy:      e.CreatedBy,
	}
}

func (e *OverrideEntry) ToDict() map[string]any {
	out := map[string]any{"provenance": string(e.Provenance)}
	if e.Enabled != nil {
		out["enabled"] = *e.Enabled
	}
	if e.Trust != nil {
		out["trust"] = *e.Trust
	}
	if len(e.ParamOverrides) > 0 {
		out["param_overrides"] = e.ParamOverrides
	}
	if e.Notes != nil {
		out["notes"] = *e.Notes
	}
	if e.CreatedAt != nil {
		out["created_at"] = e.CreatedAt.Format(time.RFC3339Nano)
	}
	if e.CreatedBy != nil {
		out["created_by"] = *e.CreatedBy
	}
	return out
}

func EntryFromDict(raw map[string]any) *OverrideEntry {
	entry := &OverrideEntry{Provenance: ProvenanceFramework}
	if enabled, ok := raw["enabled"].(bool); ok {
		entry.Enabled = &enabled
	}
	provRaw := Provenance(pyStr(raw["provenance"]))
	if provRaw == "" {
		provRaw = ProvenanceFramework
	}
	if !validProvenance(provRaw) {
		log.Printf("override: unknown provenance %q; defaulting to framework", string(provRaw))
		provRaw = ProvenanceFramework
	}
	entry.Provenance = provRaw
	if trust, ok := raw["trust"].(string); ok {
		entry.Trust = &trust
	}
	entry.ParamOverrides = map[string]any{}
	if params, ok := raw["param_overrides"].(map[string]any); ok {
		for k, v := range params {
			entry.ParamOverrides[k] = v
		}
	}
	if notes, ok := raw["notes"].(string); ok {
		entry.Notes = &notes
	}
	if createdRaw, ok := raw["created_at"].(string); ok {
		if parsed, err := parsePythonISO(createdRaw); err == nil {
			entry.CreatedAt = &parsed
		}
	}
	if createdBy, ok := raw["created_by"].(string); ok {
		entry.CreatedBy = &createdBy
	}
	return entry
}

func parsePythonISO(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed, nil
	}
	return time.Parse("2006-01-02T15:04:05", raw)
}

type SkillOverrideStore struct {
	Path                string
	ScopeLabel          string
	Version             int
	AllDefaultsDisabled bool
	Overrides           map[string]*OverrideEntry
	DeletedUISkills     map[string]struct{}
}

func LoadSkillOverrideStore(path, scopeLabel string) *SkillOverrideStore {
	store := &SkillOverrideStore{
		Path:            path,
		ScopeLabel:      scopeLabel,
		Version:         OverrideSchemaVersion,
		Overrides:       map[string]*OverrideEntry{},
		DeletedUISkills: map[string]struct{}{},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return store
	}
	var rawAny any
	if err := json.Unmarshal(data, &rawAny); err != nil {
		log.Printf("override: failed to read %s (%v); starting empty", path, err)
		return store
	}
	raw, ok := rawAny.(map[string]any)
	if !ok {
		log.Printf("override: %s is not an object; starting empty", path)
		return store
	}
	store.Version = pyInt(raw["version"], OverrideSchemaVersion)
	store.AllDefaultsDisabled = pyBool(raw["all_defaults_disabled"])
	if rawOverrides, ok := raw["overrides"].(map[string]any); ok {
		for name, item := range rawOverrides {
			entryRaw, ok := item.(map[string]any)
			if !ok {
				continue
			}
			store.Overrides[name] = EntryFromDict(entryRaw)
		}
	}
	if deleted, ok := raw["deleted_ui_skills"].([]any); ok {
		for _, item := range deleted {
			if name, ok := item.(string); ok {
				store.DeletedUISkills[name] = struct{}{}
			}
		}
	}
	return store
}

func (s *SkillOverrideStore) Upsert(skillName string, entry *OverrideEntry) {
	s.Overrides[skillName] = entry
	delete(s.DeletedUISkills, skillName)
}

func (s *SkillOverrideStore) SetEnabled(skillName string, enabled bool, provenance *Provenance) {
	existing := s.Overrides[skillName]
	if existing == nil {
		prov := ProvenanceFramework
		if provenance != nil {
			prov = *provenance
		}
		existing = &OverrideEntry{Enabled: &enabled, Provenance: prov}
	} else {
		existing.Enabled = &enabled
		if provenance != nil {
			existing.Provenance = *provenance
		}
	}
	s.Overrides[skillName] = existing
}

func (s *SkillOverrideStore) Remove(skillName string, tombstone bool) {
	delete(s.Overrides, skillName)
	if tombstone {
		s.DeletedUISkills[skillName] = struct{}{}
	}
}

func (s *SkillOverrideStore) IsDisabled(skillName string, defaultEnabled bool) bool {
	if s.AllDefaultsDisabled && defaultEnabled {
		if entry := s.Overrides[skillName]; entry != nil && entry.Enabled != nil && *entry.Enabled {
			return false
		}
		return true
	}
	entry := s.Overrides[skillName]
	if entry == nil || entry.Enabled == nil {
		return !defaultEnabled
	}
	return !*entry.Enabled
}

func (s *SkillOverrideStore) EffectiveEnabled(skillName string, defaultEnabled bool) bool {
	return !s.IsDisabled(skillName, defaultEnabled)
}

func (s *SkillOverrideStore) Get(skillName string) *OverrideEntry {
	return s.Overrides[skillName]
}

func (s *SkillOverrideStore) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(s.Overrides))
	for name := range s.Overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	overrides := make(map[string]any, len(names))
	for _, name := range names {
		overrides[name] = s.Overrides[name].ToDict()
	}
	payload := map[string]any{
		"version":               s.Version,
		"all_defaults_disabled": s.AllDefaultsDisabled,
		"overrides":             overrides,
	}
	if len(s.DeletedUISkills) > 0 {
		deleted := make([]string, 0, len(s.DeletedUISkills))
		for name := range s.DeletedUISkills {
			deleted = append(deleted, name)
		}
		sort.Strings(deleted)
		payload["deleted_ui_skills"] = deleted
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(s.Path, data, 0o644)
}

func UTCNow() time.Time { return time.Now().UTC() }

func pyInt(v any, fallback int) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	return fallback
}

func pyBool(v any) bool {
	switch typed := v.(type) {
	case bool:
		return typed
	case nil:
		return false
	case float64:
		return typed != 0
	case string:
		return typed != ""
	}
	return false
}
