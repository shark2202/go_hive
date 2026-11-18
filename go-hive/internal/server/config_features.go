package server

// Parity: framework/server/routes_config.py::handle_update_features

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

var configMu sync.Mutex

const internalServerError = "Internal server error"

var FeatureKeys = []string{"adaptive_tool_budget", "email_senders", "memory_timeline", "stall_check"}

func envFeatureFlag(name string) (value bool, set bool) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, false
	}
	switch strings.ToLower(trimmed) {
	case "0", "false", "no", "off":
		return false, true
	}
	return true, true
}

func boolConfigFlag(home hivehome.Home, key string, fallback bool) bool {
	value, ok := HiveConfig(home)[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

// Parity: framework/config.py::get_adaptive_tool_budget_enabled
func AdaptiveToolBudgetEnabled(home hivehome.Home) bool {
	if value, ok := envFeatureFlag("HIVE_ADAPTIVE_TOOL_BUDGET"); ok {
		return value
	}
	return boolConfigFlag(home, "adaptive_tool_budget", true)
}

// Parity: framework/config.py::get_email_senders_enabled
func EmailSendersEnabled(home hivehome.Home) bool {
	if value, ok := envFeatureFlag("HIVE_EMAIL_SENDERS"); ok {
		return value
	}
	return boolConfigFlag(home, "email_senders", false)
}

// Parity: framework/config.py::get_memory_timeline_enabled
func MemoryTimelineEnabled(home hivehome.Home) bool {
	if value, ok := envFeatureFlag("HIVE_MEMORY_TIMELINE"); ok {
		return value
	}
	return boolConfigFlag(home, "memory_timeline", false)
}

// Parity: framework/config.py::sync_email_senders_env
func SyncEmailSendersEnv(enabled bool) {
	value := "0"
	if enabled {
		value = "1"
	}
	_ = os.Setenv("HIVE_EMAIL_SENDERS", value)
}

func WriteHiveConfig(home hivehome.Home, config map[string]any) error {
	configMu.Lock()
	defer configMu.Unlock()
	return writeHiveConfigLocked(home, config)
}

func MutateHiveConfig(home hivehome.Home, mutate func(map[string]any) bool) error {
	configMu.Lock()
	defer configMu.Unlock()
	config := HiveConfig(home)
	if !mutate(config) {
		return nil
	}
	return writeHiveConfigLocked(home, config)
}

func writeHiveConfigLocked(home hivehome.Home, config map[string]any) error {
	path := home.ConfigurationFile()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, "*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

type FeatureColony interface {
	AdaptiveToolBudget() (value bool, defined bool)
	SetAdaptiveToolBudget(bool)
}

type FeatureSession struct {
	Colony   FeatureColony
	ColonyID string
}

// Parity: framework/server/routes_config.py::_apply_adaptive_budget_flag
func ApplyAdaptiveBudgetFlag(home hivehome.Home, sessions []FeatureSession, enabled bool) int {
	applied := 0
	for _, session := range sessions {
		if session.Colony == nil {
			continue
		}
		if session.ColonyID != "" && adaptiveBudgetPinned(home, session.ColonyID) {
			continue
		}
		value, defined := session.Colony.AdaptiveToolBudget()
		if defined && value != enabled {
			session.Colony.SetAdaptiveToolBudget(enabled)
			applied++
		}
	}
	return applied
}

func adaptiveBudgetPinned(home hivehome.Home, colonyID string) bool {
	path := filepath.Join(home.ColoniesDir(), colonyID, "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	_, pinned := meta["adaptive_tool_budget"].(bool)
	return pinned
}

// Parity: framework/server/routes_config.py::handle_get_features
func FeaturesResponse(home hivehome.Home) map[string]any {
	return map[string]any{
		"features": map[string]any{
			"adaptive_tool_budget": AdaptiveToolBudgetEnabled(home),
			"email_senders":        EmailSendersEnabled(home),
			"memory_timeline":      MemoryTimelineEnabled(home),
			"stall_check":          StallCheckEnabled(home),
		},
	}
}

// Parity: framework/server/routes_config.py::handle_update_features
func UpdateFeatures(home hivehome.Home, body map[string]any, sessions []FeatureSession) (int, map[string]any) {
	block := any(body)
	if _, ok := body["features"]; ok {
		block = body["features"]
	}
	features, ok := block.(map[string]any)
	if !ok {
		return 400, map[string]any{"error": "features must be an object"}
	}

	updates := map[string]any{}
	for _, key := range FeatureKeys {
		value, present := features[key]
		if !present {
			continue
		}
		flag, isBool := value.(bool)
		if !isBool {
			return 400, map[string]any{"error": key + " must be a boolean"}
		}
		updates[key] = flag
	}
	if len(updates) == 0 {
		return 400, map[string]any{"error": "No known feature keys. Supported: " + strings.Join(FeatureKeys, ", ")}
	}

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		for key, value := range updates {
			config[key] = value
		}
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}

	applied := 0
	if value, ok := updates["adaptive_tool_budget"].(bool); ok {
		applied = ApplyAdaptiveBudgetFlag(home, sessions, value)
	}
	if value, ok := updates["email_senders"].(bool); ok {
		SyncEmailSendersEnv(value)
	}

	return 200, map[string]any{"features": updates, "colonies_applied": applied}
}
