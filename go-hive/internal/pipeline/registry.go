package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Factory builds one stage from a config spec.
type Factory func(spec map[string]any) (Stage, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register wires a stage type name to its factory. Built-in types are
// registered at package init; callers may register additional types.
//
// Parity: framework/pipeline/registry.py::register
func Register(name string, factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
}

// RegisteredTypes returns the sorted type names the registry knows.
func RegisteredTypes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// BuildStage instantiates one stage from a spec: `{"type": ..., "order": N,
// "config": {...}}`. An unknown type is a hard error that lists what is
// available — the stage is never skipped silently.
//
// Parity: framework/pipeline/registry.py::build_stage
func BuildStage(spec map[string]any) (Stage, error) {
	rawType, present := spec["type"]
	if !present {
		return nil, fmt.Errorf("pipeline stage spec is missing %q", "type")
	}
	stageType, ok := rawType.(string)
	if !ok || strings.TrimSpace(stageType) == "" {
		return nil, fmt.Errorf("pipeline stage %q must be a non-empty string", "type")
	}
	registryMu.RLock()
	factory := registry[stageType]
	registryMu.RUnlock()
	if factory == nil {
		available := strings.Join(RegisteredTypes(), ", ")
		if available == "" {
			available = "(none)"
		}
		return nil, fmt.Errorf("unknown pipeline stage type %q. Available: %s", stageType, available)
	}
	stage, err := factory(spec)
	if err != nil {
		return nil, err
	}
	if stage == nil {
		return nil, fmt.Errorf("pipeline stage type %q built nothing", stageType)
	}
	if rawOrder, present := spec["order"]; present {
		order, ok := pyInt(rawOrder)
		if !ok {
			return nil, fmt.Errorf("pipeline stage %q has a non-integer order %v", stageType, rawOrder)
		}
		overrideOrder(stage, order)
	}
	return stage, nil
}

// BuildFromConfig builds a runner from the declarative stages list
// (`configuration.json` → `pipeline.stages`). Each entry is
// `{"type": "...", "order": N, "config": {...}}`. An empty list yields an
// empty runner — the no-op default.
//
// Parity: framework/pipeline/registry.py::build_pipeline_from_config
func BuildFromConfig(stagesConfig []any) (*Runner, error) {
	stages := make([]Stage, 0, len(stagesConfig))
	for index, raw := range stagesConfig {
		spec, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("pipeline stages[%d] must be an object, got %T", index, raw)
		}
		stage, err := BuildStage(spec)
		if err != nil {
			return nil, fmt.Errorf("pipeline stages[%d]: %w", index, err)
		}
		stages = append(stages, stage)
	}
	return NewRunner(stages), nil
}

// overrideOrder applies a spec-level order to a stage. Stages embed BaseStage
// (whose Order is a promoted value method), so a spec override lands on the
// embedded struct when present; otherwise the stage's own Order wins.
func overrideOrder(stage Stage, order int) {
	type orderSetter interface {
		setOrder(int)
	}
	if setter, ok := stage.(orderSetter); ok {
		setter.setOrder(order)
	}
}

// configMap returns the spec's `config` object.
func configMap(spec map[string]any) map[string]any {
	config, _ := spec["config"].(map[string]any)
	if config == nil {
		return map[string]any{}
	}
	return config
}

// pyInt reports a JSON value as an int only when it is integer-shaped:
// Python's int() would accept floats, but configuration values follow the
// JSON types the reader wrote (json.Number arrives via yaml-style callers).
func pyInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
		return 0, false
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			return 0, false
		}
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	}
	return 0, false
}

// pyTruthy mirrors Python truthiness for the container/scalar shapes JSON can
// carry (empty string/list/map and zero numbers are false).
func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		number, err := typed.Float64()
		return err == nil && number != 0
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

// pyString converts a JSON scalar to its Python str() form, returning "" for
// nil/absent values. Used where upstream formats a config value into a
// message (e.g. a rate-limit session id).
func pyString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return trimFloat(typed)
	case bool:
		if typed {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(value)
}
