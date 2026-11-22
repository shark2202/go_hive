package server

// Parity: framework/server/routes_config.py::handle_get_models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/ratelimit"
	"github.com/shark2202/go_hive/go-hive/internal/skills"
)

// Parity: framework/server/routes_config.py::handle_get_models
func ModelsResponse() map[string]any {
	catalogue := llm.GetModelsCatalogue()
	models := make(map[string][]map[string]any, len(catalogue))
	for provider, entries := range catalogue {
		rows := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			rows = append(rows, entry.ToMap())
		}
		models[provider] = rows
	}
	return map[string]any{"models": models}
}

// Parity: framework/server/routes_config.py::handle_get_sentinel_config
func SentinelResponse(config map[string]any) map[string]any {
	block := config["sentinel"]
	if !pyTruthy(block) {
		block = map[string]any{}
	}
	return map[string]any{"sentinel": block}
}

func safeCall(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// Parity: framework/server/routes_config.py::handle_update_sentinel_config
func UpdateSentinel(home hivehome.Home, body map[string]any, refresh func()) (int, map[string]any) {
	block := any(body)
	if value, ok := body["sentinel"]; ok {
		block = value
	}
	blockMap, ok := block.(map[string]any)
	if !ok {
		return 400, map[string]any{"error": "sentinel must be an object"}
	}
	empty := len(blockMap) == 0
	err := MutateHiveConfig(home, func(config map[string]any) bool {
		if empty {
			delete(config, "sentinel")
		} else {
			config["sentinel"] = blockMap
		}
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}
	if refresh != nil {
		safeCall(refresh)
	}
	return 200, map[string]any{"sentinel": blockMap}
}

func rateLimitsSection(config map[string]any) map[string]any {
	section, _ := config["rate_limits"].(map[string]any)
	return section
}

// Parity: framework/server/routes_config.py::handle_get_rate_limits
func RateLimitsResponse(home hivehome.Home) map[string]any {
	return map[string]any{
		"limits": ratelimit.GetAllLimitsWithOverrides(rateLimitsSection(HiveConfig(home))),
	}
}

func isRateWindow(window string) bool {
	switch window {
	case "hourly", "daily", "weekly":
		return true
	}
	return false
}

// Parity: framework/server/routes_config.py::handle_update_rate_limits
func UpdateRateLimits(home hivehome.Home, body map[string]any, orderedKeys []string) (int, map[string]any) {
	incoming, ok := body["limits"].(map[string]any)
	if !ok {
		return 400, map[string]any{"error": "limits must be an object"}
	}
	keys := orderedKeys
	if keys == nil {
		keys = sortedMapKeys(incoming)
	}

	cleaned := make(map[string]any)
	warnings := make([]string, 0)
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		value, present := incoming[key]
		if !present {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			continue
		}
		platform, action, window := parts[0], parts[1], parts[2]
		if !isRateWindow(window) {
			continue
		}
		if !ratelimit.LimitEntryExists(platform, action) {
			continue
		}
		val, ok := ratelimit.CoerceInt(value)
		if !ok {
			continue
		}
		if val < 1 {
			val = 1
		}
		if ceiling, hasCeiling := ratelimit.LimitCeiling(platform, action, window); hasCeiling && val > ceiling {
			warnings = append(warnings, fmt.Sprintf(
				"%s=%d exceeds recommended max of %d. High values increase the risk of account bans.",
				key, val, ceiling))
		}
		cleaned[key] = val
	}

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		existing, ok := config["rate_limits"].(map[string]any)
		if !ok {
			existing = map[string]any{}
		}
		for key, value := range cleaned {
			existing[key] = value
		}
		config["rate_limits"] = existing
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}

	response := map[string]any{
		"limits": ratelimit.GetAllLimitsWithOverrides(rateLimitsSection(HiveConfig(home))),
	}
	if len(warnings) > 0 {
		response["warnings"] = warnings
	}
	return 200, response
}

func sortedMapKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func orderedLimitsKeys(raw []byte) []string {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil
	}
	var last []string
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil
		}
		key, _ := keyToken.(string)
		if key == "limits" {
			keys, ok := objectKeysViaDecoder(decoder)
			if !ok {
				return nil
			}
			last = keys
			continue
		}
		if err := skipJSONValue(decoder); err != nil {
			return nil
		}
	}
	return last
}

func objectKeysViaDecoder(decoder *json.Decoder) ([]string, bool) {
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, false
	}
	keys := make([]string, 0)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, false
		}
		key, _ := keyToken.(string)
		keys = append(keys, key)
		if err := skipJSONValue(decoder); err != nil {
			return nil, false
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, false
	}
	return keys, true
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected delimiter %v", delim)
	}
	depth := 1
	for depth > 0 {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

var knownAgentSkillDirs = []string{
	"~/.claude/skills",
	"~/.codex/skills",
	"~/.cursor/skills",
	"~/.openclaw/skills",
	"~/.gemini/skills",
}

// Parity: framework/server/routes_config.py::_resolve_external_skills
func ResolveExternalSkills(paths []string) []map[string]any {
	out := make([]map[string]any, 0, len(paths))
	for _, raw := range paths {
		entry := map[string]any{"path": raw}
		expanded, err := skills.ExpandExternalPath(raw)
		if err != nil {
			entry["exists"] = false
			entry["skills"] = 0
			entry["error"] = err.Error()
			out = append(out, entry)
			continue
		}
		entry["resolved"] = expanded
		info, statErr := os.Stat(expanded)
		isDir := statErr == nil && info.IsDir()
		entry["exists"] = isDir
		if isDir {
			entry["skills"] = skills.CountExternalSkills(expanded)
		} else {
			entry["skills"] = 0
		}
		out = append(out, entry)
	}
	return out
}

// Parity: framework/server/routes_config.py::_suggest_external_skills
func SuggestedExternalSkills(configured []string) []map[string]any {
	have := make(map[string]bool, len(configured))
	for _, path := range configured {
		have[skills.NormalizeExternalPath(path)] = true
	}
	out := make([]map[string]any, 0)
	for _, candidate := range knownAgentSkillDirs {
		if have[skills.NormalizeExternalPath(candidate)] {
			continue
		}
		entries := ResolveExternalSkills([]string{candidate})
		entry := entries[0]
		exists, _ := entry["exists"].(bool)
		skillCount, _ := entry["skills"].(int)
		if exists && skillCount > 0 {
			out = append(out, entry)
		}
	}
	return out
}

func pyIterStrings(value any) ([]string, bool) {
	switch v := value.(type) {
	case string:
		out := make([]string, 0, len(v))
		for _, r := range v {
			out = append(out, string(r))
		}
		return out, true
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	case map[string]any:
		return sortedMapKeys(v), true
	}
	return nil, false
}

// Parity: framework/server/routes_config.py::handle_get_external_skills
func ExternalSkillsResponse(home hivehome.Home) (int, map[string]any) {
	raw := HiveConfig(home)["external_skills"]
	paths := []string{}
	if pyTruthy(raw) {
		iterated, ok := pyIterStrings(raw)
		if !ok {
			return 500, map[string]any{"error": internalServerError}
		}
		paths = iterated
	}
	return 200, map[string]any{
		"paths":       paths,
		"resolved":    ResolveExternalSkills(paths),
		"suggestions": SuggestedExternalSkills(paths),
	}
}

// Parity: framework/server/routes_config.py::handle_put_external_skills
func PutExternalSkills(home hivehome.Home, body map[string]any) (int, map[string]any) {
	invalid := func() (int, map[string]any) {
		return 400, map[string]any{"error": "'paths' must be a list of strings"}
	}
	raw, present := body["paths"]
	list, isList := raw.([]any)
	if !present || !isList {
		return invalid()
	}
	paths := make([]string, 0, len(list))
	for _, item := range list {
		value, ok := item.(string)
		if !ok {
			return invalid()
		}
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		if len(paths) > 0 {
			config["external_skills"] = paths
		} else {
			delete(config, "external_skills")
		}
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}
	return 200, map[string]any{"paths": paths, "resolved": ResolveExternalSkills(paths)}
}
