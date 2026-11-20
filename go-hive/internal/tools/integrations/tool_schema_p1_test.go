package integrations

// Single-action schema fix (P1): upstream registers each one-action module as
// its own MCP tool, so the published schema carries that action's `required`
// list directly. The Go catalog already recorded Required but Tool.Schema()
// dropped it for single-action tools; these assertions pin the restored
// contract without touching execution.

import (
	"reflect"
	"testing"
)

// Parity-exempt: schema-shape fix against the Python registries; upstream has
// no unit test for Tool.Schema() (a Go-only constructor).
func TestSingleActionSchemaCarriesRequired(t *testing.T) {
	cases := []struct {
		name     string
		registry *Registry
		want     []string
	}{
		{"attach_file", newBatch8FinalRegistry(), []string{"paths"}},
		{"web_scrape", newBatch3Registry(), []string{"url"}},
		{"image_generate", newBatch3MediaRegistry(), []string{"prompt"}},
		{"web_search", newBatch3MediaRegistry(), []string{"query"}},
		{"search_wikipedia", newBatch2Registry(), []string{"query"}},
	}
	for _, tc := range cases {
		tool, ok := tc.registry.Lookup(tc.name)
		if !ok {
			t.Fatalf("%s: not registered", tc.name)
		}
		schema := tool.Schema()
		got, _ := schema["required"].([]string)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: required = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Parity-exempt: schema-shape fix; guards that the single-action rule does not
// leak per-action required arguments into a multi-action tool's shared schema.
func TestMultiActionSchemaOnlyRequiresAction(t *testing.T) {
	schema := batch7Lane5ExaTool.Schema()
	got, _ := schema["required"].([]string)
	if !reflect.DeepEqual(got, []string{"action"}) {
		t.Fatalf("exa_search module required = %v, want [action]", got)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, leaks := props["query"]; !leaks {
		t.Fatalf("exa_search module union schema lost the query parameter")
	}
}
