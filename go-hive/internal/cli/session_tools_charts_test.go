package cli

// Parity-exempt: assembly guard. chart_render is the chart-tools harness
// group's only tool; the session catalogue must advertise it, the group catalog
// must carry its schema for the Tool Library, and dispatch must return the
// structured error envelope instead of panicking or claiming success.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aden-hive/hive-go/internal/charts"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// Parity-exempt: assembly guard (see file comment).
func TestSessionCatalogueExposesChartToolGroup(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	defined := map[string]bool{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = true
	}
	if !defined[charts.ToolName] {
		t.Fatalf("%q missing from the session catalogue", charts.ToolName)
	}

	group := catalogue.registry.GetFullMCPCatalog()["chart-tools"]
	if len(group) != 1 {
		t.Fatalf("chart-tools group = %d tools, want 1: %#v", len(group), group)
	}
	entry := group[0]
	if entry["name"] != charts.ToolName {
		t.Fatalf("chart-tools member = %v", entry["name"])
	}
	schema, _ := entry["input_schema"].(map[string]any)
	if schema == nil || schema["type"] != "object" {
		t.Fatalf("chart_render catalog schema = %#v", entry["input_schema"])
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"kind", "spec", "title", "width", "height", "dpi", "output_path"} {
		if _, ok := properties[want]; !ok {
			t.Fatalf("chart_render schema missing %q: %#v", want, properties)
		}
	}
	required, _ := schema["required"].([]string)
	if len(required) != 2 || required[0] != "kind" || required[1] != "spec" {
		t.Fatalf("chart_render required = %#v", schema["required"])
	}

	// The "charts" tier category points at @server:chart-tools; without the
	// recorded group it would resolve to nothing.
	category := tools.ResolveCategoryTools("charts", catalogue.registry.GetFullMCPCatalog())
	if len(category) != 1 || category[0] != charts.ToolName {
		t.Fatalf("charts category = %#v, want [chart_render]", category)
	}

	// Dispatch: an invalid kind is a tool error (not a transport error), and
	// the envelope echoes kind/spec for the chat's "spec invalid" pill.
	content, isError, err := catalogue.Execute(context.Background(), charts.ToolName, map[string]any{
		"kind": "bogus",
		"spec": map[string]any{"series": []any{}},
	})
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !isError {
		t.Fatalf("invalid kind reported success: %s", content)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		t.Fatalf("tool content is not JSON (%q): %v", content, err)
	}
	if _, ok := envelope["error"]; !ok {
		t.Fatalf("envelope missing error: %#v", envelope)
	}
	if envelope["kind"] != "bogus" {
		t.Fatalf("envelope kind = %v", envelope["kind"])
	}
	if _, ok := envelope["spec"].(map[string]any); !ok {
		t.Fatalf("envelope spec = %#v", envelope["spec"])
	}
}
