package builtin

// The ast_analyze model-visible schema is frozen by
// go_hive/docs/ast-analyze-design.json. This test compares the live definition
// after canonical JSON encoding so a schema edit cannot drift silently.

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAstAnalyzeSchemaMatchesDesign(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/ast-analyze-design.json")
	if err != nil {
		t.Skipf("design artifact not present (%v)", err)
	}
	var doc struct {
		ToolContract struct {
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tool_contract"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode design artifact: %v", err)
	}
	got, err := json.Marshal(AstAnalyzeToolDef().Parameters)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(doc.ToolContract.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("ast_analyze schema drifted:\n got %s\nwant %s", got, want)
	}
}
