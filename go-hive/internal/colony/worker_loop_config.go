package colony

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

var AllowedWorkerLoopOverrides = []string{
	"grace_iterations",
	"max_context_tokens",
	"max_iterations",
	"tool_call_budget",
	"tool_call_lifetime_budget",
}

var workerLoopOverrideBounds = map[string][2]int{
	"max_iterations":            {1, 1000},
	"grace_iterations":          {0, 3},
	"tool_call_budget":          {0, 200},
	"tool_call_lifetime_budget": {0, 2000},
	"max_context_tokens":        {1000, 1_000_000},
}

type LoopOverrideError struct {
	Key        string
	Value      any
	TypeName   string
	OutOfRange bool
	Lo, Hi     int
}

func (e *LoopOverrideError) Error() string {
	if e.OutOfRange {
		return fmt.Sprintf("loop_config override '%s'=%v out of range [%d, %d]", e.Key, e.Value, e.Lo, e.Hi)
	}
	return fmt.Sprintf("loop_config override '%s' must be int, got %s", e.Key, e.TypeName)
}

// Parity: framework/host/colony_runtime.py::_build_worker_loop_config
func BuildWorkerLoopConfig(overrides map[string]any) (agentloop.LoopConfig, error) {
	cfg := agentloop.DefaultLoopConfig()
	cfg.MaxIterations = DefaultWorkerLoopConfig["max_iterations"]
	cfg.GraceIterations = DefaultWorkerLoopConfig["grace_iterations"]
	cfg.ToolCallBudget = DefaultWorkerLoopConfig["tool_call_budget"]
	cfg.ToolCallHardMultiple = DefaultWorkerLoopConfig["tool_call_hard_multiple"]
	cfg.ToolCallLifetimeBudget = DefaultWorkerLoopConfig["tool_call_lifetime_budget"]
	// The worker hard stop is the multiple rule's 30*3=90, not the queen's
	// explicit 50: clear the default's pinned limit so the worker profile keeps
	// its documented 30/90 budget (audit: worker 30/90, queen 30/50).
	cfg.ToolCallHardLimit = 0
	// Without a spillover directory the loop keeps an oversized tool result in
	// the context verbatim; a worker has the smallest context in the system, so
	// it gives up first. Point every worker loop at its own directory under the
	// hive home (restored by restoreSpillCounter, so a resume cannot overwrite).
	cfg.SpilloverDir = workerSpilloverDir()

	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value := overrides[key]
		if !isAllowedWorkerLoopOverride(key) {
			continue
		}
		n, typeName, ok := pyInt(value)
		if !ok {
			return cfg, &LoopOverrideError{Key: key, Value: value, TypeName: typeName}
		}
		bounds := workerLoopOverrideBounds[key]
		if n < bounds[0] || n > bounds[1] {
			return cfg, &LoopOverrideError{
				Key: key, Value: value, OutOfRange: true,
				Lo: bounds[0], Hi: bounds[1],
			}
		}
		switch key {
		case "max_iterations":
			cfg.MaxIterations = n
		case "grace_iterations":
			cfg.GraceIterations = n
		case "tool_call_budget":
			cfg.ToolCallBudget = n
		case "tool_call_lifetime_budget":
			cfg.ToolCallLifetimeBudget = n
		case "max_context_tokens":
			cfg.MaxContextTokens = n
		}
	}
	return cfg, nil
}

func isAllowedWorkerLoopOverride(key string) bool {
	_, ok := workerLoopOverrideBounds[key]
	return ok
}

// workerSpillSeq disambiguates two loops built in the same process tick.
var workerSpillSeq atomic.Uint64

// workerSpilloverDir is the directory a worker loop writes oversized tool
// results to: one per config build, so two concurrent workers never race on the
// same monotonic spill filename. It lives under the hive home's cache so the
// spill file cited in the transcript survives a process restart. An unresolvable
// home leaves the field empty, which keeps the pre-fix behaviour (no spill)
// rather than writing somewhere arbitrary.
//
// Parity-exempt: upstream has no per-worker spill directory; the Go port's
// spillover producer needs one and the audit pins worker results as its gap.
func workerSpilloverDir() string {
	home, err := hivehome.Resolve()
	if err != nil || home.Root == "" {
		return ""
	}
	name := fmt.Sprintf("%d-%d", os.Getpid(), workerSpillSeq.Add(1))
	return filepath.Join(home.CacheDir(), "worker-tool-results", name)
}

func pyInt(value any) (n int, typeName string, ok bool) {
	switch v := value.(type) {
	case int:
		return v, "int", true
	case int8:
		return int(v), "int", true
	case int16:
		return int(v), "int", true
	case int32:
		return int(v), "int", true
	case int64:
		if v > math.MaxInt || v < math.MinInt {
			return 0, "int", false
		}
		return int(v), "int", true
	case uint, uint8, uint16, uint32, uint64:
		u := uint64(toUint64(v))
		if u > uint64(math.MaxInt) {
			return 0, "int", false
		}
		return int(u), "int", true
	case bool:
		if v {
			return 1, "bool", true
		}
		return 0, "bool", true
	case json.Number:
		if strings.ContainsAny(string(v), ".eE") {
			return 0, "float", false
		}
		parsed, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || parsed > math.MaxInt || parsed < math.MinInt {
			return 0, "int", false
		}
		return int(parsed), "int", true
	case string:
		return 0, "str", false
	case nil:
		return 0, "NoneType", false
	case []any:
		return 0, "list", false
	case map[string]any:
		return 0, "dict", false
	default:
		return 0, pyTypeName(value), false
	}
}

func toUint64(value any) uint64 {
	switch v := value.(type) {
	case uint:
		return uint64(v)
	case uint8:
		return uint64(v)
	case uint16:
		return uint64(v)
	case uint32:
		return uint64(v)
	case uint64:
		return v
	}
	return 0
}

func pyTypeName(value any) string {
	switch value.(type) {
	case float32, float64:
		return "float"
	case []string:
		return "list"
	default:
		return fmt.Sprintf("%T", value)
	}
}
