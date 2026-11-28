package memory

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Parity: core/framework/agents/queen/reflection_agent.py::_REFLECTION_TOOLS —
// delete_memory_file carries the model-facing `reason` slot so the reflection
// prompt's "say why it goes" rule has a parameter to fill.
func TestDeleteMemoryFileSchemaExposesReason(t *testing.T) {
	for _, def := range ReflectionTools {
		if def.Name != "delete_memory_file" {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("properties = %#v", def.Parameters["properties"])
		}
		reason, ok := properties["reason"].(map[string]any)
		if !ok {
			t.Fatalf("delete_memory_file has no reason property: %#v", properties)
		}
		if reason["type"] != "string" {
			t.Fatalf("reason type = %#v", reason["type"])
		}
		want := "Why it goes, e.g. 'merged into user-diet.md' or 'the user said it no longer holds'."
		if reason["description"] != want {
			t.Fatalf("reason description = %#v", reason["description"])
		}
		required, _ := def.Parameters["required"].([]string)
		for _, name := range required {
			if name == "reason" {
				t.Fatal("reason must stay optional")
			}
		}
		return
	}
	t.Fatal("delete_memory_file missing from ReflectionTools")
}

// Parity: core/framework/agents/queen/reflection_agent.py::_REFLECTION_TOOLS
func TestReflectionToolDefinitionsMatchUpstreamContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "tool-definitions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract manifest not present (%v) — this check needs the committed artifact", err)
	}
	type contractTool struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	}
	var manifest struct {
		Tools []contractTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}
	references := map[string]contractTool{}
	for _, tool := range manifest.Tools {
		references[tool.Name] = tool
	}

	for _, def := range ReflectionTools {
		reference, ok := references[def.Name]
		if !ok {
			t.Fatalf("%s missing from contracts/tool-definitions.json", def.Name)
		}
		if def.Description != reference.Description {
			t.Fatalf("%s: description is not the upstream reflection_agent text (got %d bytes, want %d bytes)",
				def.Name, len(def.Description), len(reference.Description))
		}
		got, err := json.Marshal(def.Parameters)
		if err != nil {
			t.Fatalf("%s: marshal Go schema: %v", def.Name, err)
		}
		want, err := json.Marshal(reference.InputSchema)
		if err != nil {
			t.Fatalf("%s: marshal upstream schema: %v", def.Name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s: input schema is not the upstream one\n got: %s\nwant: %s", def.Name, got, want)
		}
		sum := sha256.Sum256([]byte(def.Description))
		t.Logf("%s: description %d bytes, sha256 %x", def.Name, len(def.Description), sum[:8])
	}
}
