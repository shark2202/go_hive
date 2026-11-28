package server

// Parity: framework/server/routes_config.py::handle_get_memory_settings,
// framework/server/routes_config.py::handle_update_memory_settings
// Parity: framework/config.py::get_memory_recall_config, get_memory_recall_env_mode,
// framework/config.py::get_memory_reflection_config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// MemoryRecallModes are the ways memory notes can reach a queen, in the order
// Settings lists them.
var MemoryRecallModes = []string{"index", "wait", "background"}

// MemoryReflectionModels are the models memory reflection can run on.
var MemoryReflectionModels = []string{"worker", "main"}

// readJSONBody decodes the request body the way aiohttp's “request.json()“
// does, keeping numbers as json.Number so integer checks stay exact. It writes
// the 400 itself and reports false when the body is not valid JSON.
func readJSONBody(w http.ResponseWriter, r *http.Request) (any, bool) {
	invalid := func() (any, bool) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return nil, false
	}
	var data []byte
	if r.Body != nil {
		var err error
		data, err = readLimitedBody(r, maxJSONBodyBytes)
		if err != nil {
			return invalid()
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return invalid()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return invalid()
	}
	return decoded, true
}

// memoryRecallEnvMode is the mode the HIVE_MEMORY_RECALL env var sets, when it
// names one: it overrides the configured mode.
//
// Parity: framework/config.py::get_memory_recall_env_mode
func memoryRecallEnvMode() *string {
	raw, ok := os.LookupEnv("HIVE_MEMORY_RECALL")
	if !ok {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range MemoryRecallModes {
		if mode == candidate {
			return &mode
		}
	}
	return nil
}

// MemoryRecallMode is the effective recall mode: the env override, else the
// saved “memory_recall.mode“, else the "index" default.
//
// Parity: framework/config.py::get_memory_recall_config
func MemoryRecallMode(home hivehome.Home) string {
	mode := ""
	if section, ok := HiveConfig(home)["memory_recall"].(map[string]any); ok {
		if raw, ok := section["mode"].(string); ok {
			mode = raw
		}
	}
	if env := memoryRecallEnvMode(); env != nil {
		mode = *env
	}
	for _, candidate := range MemoryRecallModes {
		if mode == candidate {
			return mode
		}
	}
	return MemoryRecallModes[0]
}

// MemoryReflectionModel is the saved “memory_reflection.model“, or the
// "worker" default when missing or invalid.
//
// Parity: framework/config.py::get_memory_reflection_config
func MemoryReflectionModel(home hivehome.Home) string {
	if section, ok := HiveConfig(home)["memory_reflection"].(map[string]any); ok {
		if model, ok := section["model"].(string); ok {
			for _, candidate := range MemoryReflectionModels {
				if model == candidate {
					return model
				}
			}
		}
	}
	return MemoryReflectionModels[0]
}

// MemorySettingsResponse is GET /api/config/memory's “memory“ block.
func MemorySettingsResponse(home hivehome.Home) map[string]any {
	var envValue any
	if env := memoryRecallEnvMode(); env != nil {
		envValue = *env
	}
	return map[string]any{
		"recall_mode":       MemoryRecallMode(home),
		"recall_modes":      append([]string(nil), MemoryRecallModes...),
		"recall_mode_env":   envValue,
		"reflection_model":  MemoryReflectionModel(home),
		"reflection_models": append([]string(nil), MemoryReflectionModels...),
	}
}

type memorySetting struct {
	field   string
	section string
	key     string
	choices []string
}

// memorySettings is the request-field → (configuration.json section, key in
// it) map, in the order Settings lists them.
var memorySettings = []memorySetting{
	{field: "recall_mode", section: "memory_recall", key: "mode", choices: MemoryRecallModes},
	{field: "reflection_model", section: "memory_reflection", key: "model", choices: MemoryReflectionModels},
}

// UpdateMemorySettings ports handle_update_memory_settings: it saves the named
// “memory_recall.mode“ / “memory_reflection.model“ in configuration.json,
// keeping the sections' other keys.
func UpdateMemorySettings(home hivehome.Home, body map[string]any) (int, map[string]any) {
	block := any(body)
	if inner, has := body["memory"]; has {
		block = inner
	}
	memory, isObject := block.(map[string]any)
	if !isObject {
		return 400, map[string]any{"error": "memory must be an object"}
	}

	type update struct {
		setting memorySetting
		value   any
	}
	updates := []update{}
	for _, setting := range memorySettings {
		if value, present := memory[setting.field]; present {
			updates = append(updates, update{setting: setting, value: value})
		}
	}
	if len(updates) == 0 {
		fields := make([]string, 0, len(memorySettings))
		for _, setting := range memorySettings {
			fields = append(fields, setting.field)
		}
		return 400, map[string]any{"error": "No known memory settings. Supported: " + strings.Join(fields, ", ")}
	}
	for _, entry := range updates {
		value, isString := entry.value.(string)
		valid := false
		if isString {
			for _, choice := range entry.setting.choices {
				if value == choice {
					valid = true
					break
				}
			}
		}
		if !valid {
			return 400, map[string]any{
				"error": entry.setting.field + " must be one of: " + strings.Join(entry.setting.choices, ", "),
			}
		}
	}

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		for _, entry := range updates {
			merged := map[string]any{}
			if current, ok := config[entry.setting.section].(map[string]any); ok {
				for key, value := range current {
					merged[key] = value
				}
			}
			merged[entry.setting.key] = entry.value
			config[entry.setting.section] = merged
		}
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}
	return 200, map[string]any{"memory": MemorySettingsResponse(home)}
}

func handleGetMemorySettings(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"memory": MemorySettingsResponse(deps.Home)})
	}
}

func handleUpdateMemorySettings(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		decoded, ok := readJSONBody(w, r)
		if !ok {
			return
		}
		body, isObject := decoded.(map[string]any)
		if !isObject {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "memory must be an object"})
			return
		}
		status, response := UpdateMemorySettings(deps.Home, body)
		writeJSON(w, status, response)
	}
}
