package memory

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
