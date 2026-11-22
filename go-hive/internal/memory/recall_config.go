package memory

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// MemoryRecall modes: how a queen's memory notes reach her
// (framework.agent_loop.memory_reminders).
const (
	// MemoryRecallIndex: no pick; she sees every note's name and one-line
	// description, and reads the notes she wants herself.
	MemoryRecallIndex = "index"
	// MemoryRecallWait: a model picks the notes for each message, finished
	// before she answers, or skipped for that message past wait_seconds.
	MemoryRecallWait = "wait"
	// MemoryRecallBackground: the same pick after she has started answering.
	MemoryRecallBackground = "background"
)

// MemoryRecallModes lists the ways memory notes can reach a queen, in the
// order Settings lists them.
//
// Parity: core/framework/config.py::MEMORY_RECALL_MODES
var MemoryRecallModes = []string{MemoryRecallIndex, MemoryRecallWait, MemoryRecallBackground}

// MemoryRecallConfig is the resolved memory_recall section.
//
// Parity: core/framework/config.py::MemoryRecallConfig
type MemoryRecallConfig struct {
	Mode string
	// WaitSeconds is the longest "wait" holds a message for its pick.
	WaitSeconds float64
}

// DefaultMemoryRecallConfig returns the defaults for anything missing or
// invalid: "index" and 10 seconds.
//
// Parity: core/framework/config.py::MemoryRecallConfig (field defaults)
func DefaultMemoryRecallConfig() MemoryRecallConfig {
	return MemoryRecallConfig{Mode: MemoryRecallIndex, WaitSeconds: 10.0}
}

// MemoryRecallEnvMode returns the mode HIVE_MEMORY_RECALL sets, when it names
// one: it overrides the configured mode.
//
// Parity: core/framework/config.py::get_memory_recall_env_mode
func MemoryRecallEnvMode() (string, bool) {
	raw, ok := os.LookupEnv("HIVE_MEMORY_RECALL")
	if !ok {
		return "", false
	}
	mode := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range MemoryRecallModes {
		if mode == candidate {
			return mode, true
		}
	}
	return "", false
}

// GetMemoryRecallConfig resolves the memory_recall section of
// <homeRoot>/configuration.json, with defaults for anything missing or
// invalid. The HIVE_MEMORY_RECALL env var, when set to a mode, overrides the
// configured one.
//
// Parity: core/framework/config.py::get_memory_recall_config
func GetMemoryRecallConfig(homeRoot string) MemoryRecallConfig {
	defaults := DefaultMemoryRecallConfig()
	var section map[string]any
	if strings.TrimSpace(homeRoot) != "" {
		section = hiveconf.Section(hivehome.Home{Root: homeRoot}, "memory_recall")
	}
	mode := ""
	if envMode, ok := MemoryRecallEnvMode(); ok {
		mode = envMode
	} else if configured, ok := section["mode"].(string); ok {
		mode = configured
	}
	validMode := false
	for _, candidate := range MemoryRecallModes {
		if mode == candidate {
			validMode = true
			break
		}
	}
	if !validMode {
		mode = defaults.Mode
	}
	wait := defaults.WaitSeconds
	if seconds, ok := recallSeconds(section["wait_seconds"]); ok && seconds > 0 {
		wait = seconds
	}
	return MemoryRecallConfig{Mode: mode, WaitSeconds: wait}
}

// recallSeconds accepts the JSON numbers Python's isinstance(wait, int|float)
// accepts; booleans and strings are rejected, and so are JSON booleans that
// would just be `true`.
func recallSeconds(value any) (float64, bool) {
	switch typed := value.(type) {
	case nil, bool, string:
		return 0, false
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}
