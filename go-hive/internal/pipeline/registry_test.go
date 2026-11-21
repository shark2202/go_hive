package pipeline

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Parity-exempt: upstream ships no unit tests for framework/pipeline; this
// pins build_pipeline_from_config's declarative build (type/order/config).
func TestBuildFromConfigBuildsOrderedRunner(t *testing.T) {
	runner, err := BuildFromConfig([]any{
		map[string]any{"type": "cost_guard", "config": map[string]any{"max_cost_per_request": 0.5}},
		map[string]any{"type": "rate_limit", "order": 5, "config": map[string]any{"max_requests_per_minute": 3}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if runner.Len() != 2 {
		t.Fatalf("len = %d, want 2", runner.Len())
	}
	stages := runner.Stages()
	if got := stages[0].Name(); got != "rate_limit" {
		t.Fatalf("first stage = %s, want rate_limit (order 5 override)", got)
	}
	if got := stages[0].Order(); got != 5 {
		t.Fatalf("rate_limit order = %d, want spec override 5", got)
	}
	rateLimit, ok := stages[0].(*RateLimitStage)
	if !ok || rateLimit.MaxRequestsPerMinute != 3 {
		t.Fatalf("rate_limit config not applied: %+v", stages[0])
	}
	costGuard, ok := stages[1].(*CostGuardStage)
	if !ok || costGuard.MaxCostPerRequest != 0.5 {
		t.Fatalf("cost_guard config not applied: %+v", stages[1])
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; an empty
// or absent stages list builds the no-op runner.
func TestBuildFromConfigEmptyIsNoOp(t *testing.T) {
	for _, config := range [][]any{nil, {}} {
		runner, err := BuildFromConfig(config)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if runner.Len() != 0 {
			t.Fatalf("len = %d, want 0", runner.Len())
		}
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; this is
// the anti-silence contract: an unknown stage type fails loudly and lists the
// registered types.
func TestBuildStageUnknownTypeFailsLoudly(t *testing.T) {
	_, err := BuildFromConfig([]any{
		map[string]any{"type": "not_a_stage"},
	})
	if err == nil {
		t.Fatal("unknown stage type must fail")
	}
	message := err.Error()
	if !strings.Contains(message, `unknown pipeline stage type "not_a_stage"`) {
		t.Fatalf("message = %q", message)
	}
	for _, known := range []string{"llm_provider", "credential_resolver", "mcp_registry", "skill_registry", "input_validation", "rate_limit", "cost_guard"} {
		if !strings.Contains(message, known) {
			t.Fatalf("available list missing %s: %q", known, message)
		}
	}
	if !strings.Contains(message, "pipeline stages[0]") {
		t.Fatalf("entry index missing: %q", message)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a spec
// without a type is refused instead of skipped.
func TestBuildStageMissingTypeFails(t *testing.T) {
	if _, err := BuildStage(map[string]any{"order": 10}); err == nil {
		t.Fatal("missing type must fail")
	}
	if _, err := BuildStage(map[string]any{"type": ""}); err == nil {
		t.Fatal("empty type must fail")
	}
	if _, err := BuildStage(map[string]any{"type": 7}); err == nil {
		t.Fatal("non-string type must fail")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// stages list must be a list of objects.
func TestBuildFromConfigRejectsNonObjectEntry(t *testing.T) {
	if _, err := BuildFromConfig([]any{"rate_limit"}); err == nil {
		t.Fatal("string entry must fail")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a bad
// order override is refused (Python would TypeError while sorting).
func TestBuildStageRejectsNonIntegerOrder(t *testing.T) {
	if _, err := BuildStage(map[string]any{"type": "rate_limit", "order": "fast"}); err == nil {
		t.Fatal("string order must fail")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// seven built-ins self-register with their upstream default orders.
func TestBuiltinTypesAndDefaultOrders(t *testing.T) {
	want := map[string]int{
		"llm_provider":        10,
		"credential_resolver": 40,
		"mcp_registry":        50,
		"skill_registry":      60,
		"input_validation":    100,
		"rate_limit":          200,
		"cost_guard":          300,
	}
	types := RegisteredTypes()
	if len(types) != len(want) {
		t.Fatalf("registered types = %v, want %d", types, len(want))
	}
	for name, order := range want {
		stage, err := BuildStage(map[string]any{"type": name})
		if err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
		if stage.Name() != name {
			t.Fatalf("%s Name() = %s", name, stage.Name())
		}
		if stage.Order() != order {
			t.Fatalf("%s Order() = %d, want %d", name, stage.Order(), order)
		}
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline;
// RegisteredTypes is sorted so error lists are deterministic.
func TestRegisteredTypesSorted(t *testing.T) {
	types := RegisteredTypes()
	sorted := append([]string(nil), types...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(types, sorted) {
		t.Fatalf("types not sorted: %v", types)
	}
}
