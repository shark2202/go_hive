package data

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Parity-exempt: catalog/registry consistency checks have no Python counterpart
// (the upstream tree has no generated catalog to compare against).
func TestBatch5Lane2RegistrySurface(t *testing.T) {
	registry := NewDatabasesRegistry()
	if got := registry.ActionCount(); got != 32 {
		t.Fatalf("ActionCount = %d, want 32", got)
	}
	if got := registry.ImplementedActions(); got != 32 {
		t.Fatalf("ImplementedActions = %d, want 32", got)
	}
	if names := registry.SortedNames(); strings.Join(names, ",") != "mongodb,postgres,redis,supabase" {
		t.Fatalf("SortedNames = %v", names)
	}
	wantCounts := map[string]int{"postgres": 8, "redis": 11, "mongodb": 6, "supabase": 7}
	for name, want := range wantCounts {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if len(tool.Actions) != want {
			t.Fatalf("%s actions = %d, want %d", name, len(tool.Actions), want)
		}
		schema := tool.Schema()
		properties, _ := schema["properties"].(map[string]any)
		actionProperty, _ := properties["action"].(map[string]any)
		enum, _ := actionProperty["enum"].([]string)
		if len(enum) != want {
			t.Fatalf("%s action enum = %v", name, actionProperty["enum"])
		}
		required, _ := schema["required"].([]string)
		if len(required) != 1 || required[0] != "action" {
			t.Fatalf("%s required = %v", name, schema["required"])
		}
	}
	postgresTool, _ := registry.Lookup("postgres")
	statsAction, ok := postgresTool.Action("pg_get_table_stats")
	if !ok {
		t.Fatal("pg_get_table_stats missing")
	}
	schema, _ := statsAction.Params["schema"].(map[string]any)
	if schema["default"] != "public" {
		t.Fatalf("pg_get_table_stats schema default = %v", schema["default"])
	}
	redisTool, _ := registry.Lookup("redis")
	setAction, _ := redisTool.Action("redis_set")
	if len(setAction.Required) != 2 {
		t.Fatalf("redis_set required = %v", setAction.Required)
	}
	postgres, _ := registry.Lookup("postgres")
	description := postgres.Description()
	for _, want := range []string{"pg_query", "pg_get_foreign_keys", "required: sql"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description missing %q:\n%s", want, description)
		}
	}
}

// Parity-exempt: action routing errors are a port-level concern; upstream
// raises from the MCP layer instead.
func TestBatch5Lane2ActionRouting(t *testing.T) {
	registry := NewDatabasesRegistry(WithGetenv(func(string) string { return "" }))
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{})
	batch5AssertErrorContains(t, result, "action is required")
	actions, _ := result["actions"].([]string)
	if len(actions) != 11 {
		t.Fatalf("actions = %v", result["actions"])
	}
	if _, err := registry.Execute(context.Background(), "redis", map[string]any{"action": "nope"}); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("err = %v, want ErrUnknownAction", err)
	}
	if _, err := registry.Execute(context.Background(), "nope", nil); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("err = %v, want ErrUnknownTool", err)
	}
}
