package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// Parity-exempt: adapter-level invariants; upstream has no equivalent test for
// the Go registry surface.
func TestCatalogIntegrity(t *testing.T) {
	registry := New()
	if got := len(registry.Names()); got != 10 {
		t.Fatalf("tools = %d, want 10", got)
	}
	if got := registry.ActionCount(); got != 165 {
		t.Fatalf("actions = %d, want 165", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	seen := map[string]bool{}
	for _, tool := range registry.Tools() {
		if tool.Name == "" || tool.ID == "" || tool.Summary == "" {
			t.Fatalf("tool missing identity: %+v", tool)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		if len(tool.Actions) == 0 {
			t.Fatalf("%s has no actions", tool.Name)
		}
		for _, action := range tool.Actions {
			if _, ok := tool.Action(action.Name); !ok {
				t.Fatalf("%s missing action %s", tool.Name, action.Name)
			}
			_, hasSpec := registry.Specs(tool.Name)[action.Name]
			hasImpl := registry.Impls(tool.Name)[action.Name] != nil
			if !hasSpec && !hasImpl {
				t.Fatalf("%s.%s has neither spec nor implementation", tool.Name, action.Name)
			}
		}
	}
}

// Parity-exempt: schema shape is a Go-side contract with tools.ToolRegistry.
func TestSchemaShape(t *testing.T) {
	registry := New()
	for _, tool := range registry.Tools() {
		schema := tool.Schema()
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v", tool.Name, schema["type"])
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties missing", tool.Name)
		}
		if len(tool.Actions) > 1 {
			action, ok := properties["action"].(map[string]any)
			if !ok {
				t.Fatalf("%s action property missing", tool.Name)
			}
			enum, ok := action["enum"].([]string)
			if !ok || len(enum) != len(tool.Actions) {
				t.Fatalf("%s action enum = %v", tool.Name, action["enum"])
			}
			if required, _ := schema["required"].([]string); len(required) != 1 || required[0] != "action" {
				t.Fatalf("%s required = %v", tool.Name, schema["required"])
			}
		}
		if tool.Description() == "" {
			t.Fatalf("%s empty description", tool.Name)
		}
	}
}

// Parity-exempt: installation into the shared registry is exercised end to end.
func TestInstallIntoSharedRegistry(t *testing.T) {
	shared := tools.NewToolRegistry()
	registry := New()
	installed := registry.Install(shared)
	if installed != len(registry.Names()) {
		t.Fatalf("installed = %d", installed)
	}
	if !shared.HasTool("csv") || !shared.HasTool("get_current_time") {
		t.Fatalf("expected csv and get_current_time in shared registry")
	}
	tool, ok := shared.GetTool("csv")
	if !ok {
		t.Fatal("csv tool missing")
	}
	if tool.Parameters["type"] != "object" {
		t.Fatalf("csv schema = %v", tool.Parameters)
	}
}

// Parity-exempt: unknown tool/action handling is a Go-side error contract.
func TestUnknownToolAndAction(t *testing.T) {
	registry := New()
	if _, err := registry.Execute(context.Background(), "nope", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("err = %v", err)
	}
	if _, err := registry.Execute(context.Background(), "csv", map[string]any{"action": "nope"}); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("err = %v", err)
	}
	result, err := registry.Execute(context.Background(), "csv", nil)
	if err != nil {
		t.Fatal(err)
	}
	problem := result.(map[string]any)
	if problem["error"] == nil || !strings.Contains(problem["error"].(string), "action is required") {
		t.Fatalf("result = %v", problem)
	}
}

// Parity-exempt: implicit single-action dispatch is a Go-side convenience.
func TestSingleActionToolDispatch(t *testing.T) {
	registry := New()
	result, err := registry.Execute(context.Background(), "get_current_time", map[string]any{"timezone": "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	if object["timezone"] != "UTC" || object["datetime"] == "" {
		t.Fatalf("result = %v", object)
	}
	bad, err := registry.Execute(context.Background(), "get_current_time", map[string]any{"timezone": "Not/AZone"})
	if err != nil {
		t.Fatal(err)
	}
	if bad.(map[string]any)["error"] != "Invalid timezone: Not/AZone" {
		t.Fatalf("bad timezone result = %v", bad)
	}
}

// Parity-exempt: JSON round-trip of results is a Go-side contract.
func TestResultJSONRoundTrip(t *testing.T) {
	registry := New()
	result, err := registry.Execute(context.Background(), "risk_score", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
