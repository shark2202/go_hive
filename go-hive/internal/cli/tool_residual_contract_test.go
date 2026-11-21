package cli

// Residual same-name tool contract (docs/tool-contract-diff.md §13/§14).
//
// The queen-lifecycle tools were the last same-name definitions without a
// schema-bearing Python reference: the captured Tool Library renders those
// rows as name + description only. The parity manifest freezes them from the
// registered Python Tool objects (tool_contract_parity.json, source
// queen_lifecycle), and these tests pin the live Go definitions to that
// reference:
//
//   - all eight lifecycle tools (the five session-bound ones plus Python's
//     three standalone playbook-control tools) are byte-identical to the
//     Python source;
//   - run_playbook carries the Python description and exactly the four Python
//     parameters — the folded action/run_id controls now live on the control
//     tool definitions instead of the fan-out tool (§14);
//   - all 41 same-name descriptions equal the frozen Python contract (the
//     desc 41/41 target of the residual lane).
//
// Parity-exempt: the assertion targets are frozen Python artifacts
// (contracts/tool-definitions.json and tool_contract_parity.json), not a
// Python unit test.

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

// queenLifecycleContractNames are the eight same-name lifecycle tools frozen in
// the parity manifest from the registered Python Tool objects.
var queenLifecycleContractNames = []string{
	"get_playbook_status",
	"list_playbook_runs",
	"remove_trigger",
	"reply_to_worker",
	"run_playbook",
	"set_trigger",
	"stop_playbook",
	"update_worker_profile",
}

// liveQueenLifecycleDefs indexes the Go lifecycle definitions the way the
// session catalogue assembles them (the queen lifecycle table plus the
// colony-owned playbook tool).
func liveQueenLifecycleDefs(t *testing.T) map[string]struct {
	Description string
	Parameters  map[string]any
} {
	t.Helper()
	defs := map[string]struct {
		Description string
		Parameters  map[string]any
	}{}
	for _, def := range hiveserver.QueenLifecycleToolDefs() {
		defs[def.Name] = struct {
			Description string
			Parameters  map[string]any
		}{Description: def.Description, Parameters: def.Parameters}
	}
	playbook := colony.RunPlaybookToolDef()
	defs[playbook.Name] = struct {
		Description string
		Parameters  map[string]any
	}{Description: playbook.Description, Parameters: playbook.Parameters}
	for _, def := range colony.PlaybookControlToolDefs() {
		defs[def.Name] = struct {
			Description string
			Parameters  map[string]any
		}{Description: def.Description, Parameters: def.Parameters}
	}
	return defs
}

// TestQueenLifecycleDefinitionsMatchPythonSource pins the eight lifecycle
// definitions to the Python source contract frozen in the parity manifest.
//
// Parity-exempt: the reference is a generated Python artifact.
func TestQueenLifecycleDefinitionsMatchPythonSource(t *testing.T) {
	manifest := toolContractParityManifestData()
	live := liveQueenLifecycleDefs(t)

	for _, name := range queenLifecycleContractNames {
		entry, ok := manifest.Tools[name]
		if !ok {
			t.Fatalf("%s: missing from the parity manifest", name)
		}
		def, ok := live[name]
		if !ok {
			t.Fatalf("%s: missing from the live lifecycle definitions", name)
		}
		if def.Description != entry.Description {
			t.Errorf("%s: description is not the Python source text (got %d bytes, want %d)",
				name, len(def.Description), len(entry.Description))
		}
		if entry.Status != "aligned" {
			t.Errorf("%s: manifest status = %q, want aligned", name, entry.Status)
			continue
		}
		if !flatJSONEqual(def.Parameters, entry.Parameters) {
			t.Errorf("%s: schema is not the Python source schema\ngot:  %#v\nwant: %#v",
				name, def.Parameters, entry.Parameters)
		}
	}
}

// TestFlatSurfacePlaybookToolsMatchPythonSource pins the four model-visible
// playbook definitions of the default flat surface (run_playbook plus the
// three control tools) to the Python source contract. This is the gate that
// keeps the folded action/run_id parameters off the run_playbook fan-out tool:
// they exist on the control tools instead.
//
// Parity: core/framework/tools/playbook_tools.py::register_playbook_tools
func TestFlatSurfacePlaybookToolsMatchPythonSource(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	manifest := toolContractParityManifestData()
	live := map[string]llm.ToolDef{}
	for _, def := range catalogue.ModelDefs() {
		live[def.Name] = def
	}
	for _, name := range []string{
		"run_playbook", "get_playbook_status", "stop_playbook", "list_playbook_runs",
	} {
		def, ok := live[name]
		if !ok {
			t.Fatalf("%s: missing from the flat model-visible surface", name)
		}
		entry, ok := manifest.Tools[name]
		if !ok {
			t.Fatalf("%s: missing from the parity manifest", name)
		}
		if def.Description != entry.Description {
			t.Errorf("%s: flat description is not the Python text (got %d bytes, want %d)",
				name, len(def.Description), len(entry.Description))
		}
		if !flatJSONEqual(def.Parameters, entry.Parameters) {
			t.Errorf("%s: flat schema is not the Python schema\ngot:  %#v\nwant: %#v",
				name, def.Parameters, entry.Parameters)
		}
	}
	props, ok := live["run_playbook"].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("run_playbook: schema has no properties map")
	}
	for _, folded := range []string{"action", "run_id"} {
		if _, present := props[folded]; present {
			t.Fatalf("run_playbook still advertises the folded control parameter %q to the model", folded)
		}
	}
}

// TestRunPlaybookDescriptionMatchesFrozenContract pins the model-visible
// run_playbook description to the frozen Python contract byte-for-byte. The
// port's previous text claimed Python scripts were not executable and that no
// [PLAYBOOK_COMPLETE] notification existed; both stopped being true when the
// external-interpreter and background-notify lanes landed (docs §13).
//
// Parity-exempt: the reference is the frozen capture artifact.
func TestRunPlaybookDescriptionMatchesFrozenContract(t *testing.T) {
	raw, err := os.ReadFile(contractPath())
	if err != nil {
		t.Skipf("contract manifest not present (%v)", err)
	}
	var doc struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}
	var want string
	for _, tool := range doc.Tools {
		if tool.Name == "run_playbook" {
			want = tool.Description
			break
		}
	}
	if want == "" {
		t.Fatal("run_playbook missing from the frozen contract")
	}
	if got := colony.RunPlaybookToolDef().Description; got != want {
		t.Fatalf("run_playbook description is not the Python contract (got %d bytes, want %d)", len(got), len(want))
	}
}

// TestSameNameToolDescriptionsMatchFrozenContract is the desc 41/41 gate: for
// every same-name tool of the frozen contract (the tools whose Go side was
// captured with the same name), the live description equals the Python text.
//
// Parity-exempt: the reference is the frozen capture artifact.
func TestSameNameToolDescriptionsMatchFrozenContract(t *testing.T) {
	raw, err := os.ReadFile(contractPath())
	if err != nil {
		t.Skipf("contract manifest not present (%v)", err)
	}
	var doc struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Go          struct {
				Present bool `json:"present"`
			} `json:"go"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}

	live := parityCatalogueDefs(t)
	synthetic := agentloop.BuildAskUserTool()
	live[synthetic.Name] = synthetic

	matched := 0
	for _, tool := range doc.Tools {
		if !tool.Go.Present {
			continue
		}
		def, ok := live[tool.Name]
		if !ok {
			t.Errorf("%s: same-name tool missing from the live catalogue", tool.Name)
			continue
		}
		if def.Description != tool.Description {
			t.Errorf("%s: description is not the Python contract (got %d bytes, want %d)",
				tool.Name, len(def.Description), len(tool.Description))
			continue
		}
		matched++
	}
	if matched != 41 {
		t.Fatalf("description parity = %d/41; wanted all 41 same-name tools", matched)
	}
}
