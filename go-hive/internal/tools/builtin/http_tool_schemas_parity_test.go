package builtin

// The native HTTP family's model-visible schemas are frozen by
// go_hive/docs/http-tools-design.json. This test reads the design artifact and
// compares the live definitions byte-for-byte after canonical JSON encoding, so
// a schema edit cannot drift silently.

import (
	"encoding/json"
	"os"
	"testing"
)

// Parity-exempt: Go-only tool schemas frozen by docs/http-tools-design.json.
func TestHTTPToolSchemasMatchDesign(t *testing.T) {
	raw, err := os.ReadFile("../../../../docs/http-tools-design.json")
	if err != nil {
		t.Skipf("design artifact not present (%v)", err)
	}
	var doc struct {
		ToolContracts map[string]json.RawMessage `json:"tool_contracts"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode design artifact: %v", err)
	}
	for _, name := range []string{ToolHTTPRequest, ToolHTTPDownload, ToolHTTPUpload} {
		rawContract, ok := doc.ToolContracts[name]
		if !ok {
			t.Fatalf("design artifact has no contract for %q", name)
		}
		var contract struct {
			InputSchema map[string]any `json:"input_schema"`
		}
		if err := json.Unmarshal(rawContract, &contract); err != nil {
			t.Fatalf("decode design %s contract: %v", name, err)
		}
		def, ok := HTTPToolDef(name)
		if !ok {
			t.Fatalf("live definition missing for %q", name)
		}
		got, err := json.Marshal(def.Parameters)
		if err != nil {
			t.Fatalf("encode live %s schema: %v", name, err)
		}
		want, err := json.Marshal(contract.InputSchema)
		if err != nil {
			t.Fatalf("encode design %s schema: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s schema drifted from the design artifact:\n got %s\nwant %s", name, got, want)
		}
	}
}
