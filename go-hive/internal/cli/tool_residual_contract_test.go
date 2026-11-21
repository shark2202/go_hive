package cli

// Residual same-name tool contract (docs/tool-contract-diff.md §13).
//
// The five queen-lifecycle tools were the last same-name definitions without
// a schema-bearing Python reference: the captured Tool Library renders those
// rows as name + description only. The parity manifest now freezes them from
// the registered Python Tool objects (tool_contract_parity.json, source
// queen_lifecycle), and these tests pin the live Go definitions to that
// reference:
//
//   - four tools (remove_trigger / reply_to_worker / set_trigger /
//     update_worker_profile) are byte-identical to the Python source;
//   - run_playbook carries the Python description verbatim and the same four
//     parameters, plus this port's two folded playbook-control parameters
//     (action / run_id) — the one recorded schema exception;
//   - all 41 same-name descriptions equal the frozen Python contract (the
//     desc 41/41 target of the residual lane).
//
// Parity-exempt: the assertion targets are frozen Python artifacts
// (contracts/tool-definitions.json and tool_contract_parity.json), not a
// Python unit test.

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

// queenLifecycleContractNames are the five same-name lifecycle tools frozen in
// the parity manifest from the registered Python Tool objects.
var queenLifecycleContractNames = []string{
	"remove_trigger",
	"reply_to_worker",
	"run_playbook",
	"set_trigger",
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
	return defs
}

// schemaProperties indexes a schema's properties map.
func schemaProperties(t *testing.T, name string, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s: schema has no properties map", name)
	}
	return props
}

// TestQueenLifecycleDefinitionsMatchPythonSource pins the five lifecycle
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
		if entry.Status == "aligned" {
			if !flatJSONEqual(def.Parameters, entry.Parameters) {
				t.Errorf("%s: schema is not the Python source schema\ngot:  %#v\nwant: %#v",
					name, def.Parameters, entry.Parameters)
			}
			continue
		}

		// The recorded partial: every Python parameter must be present,
		// byte-equal, and the only extras are the folded playbook-control
		// parameters this port adds on purpose.
		if name != "run_playbook" {
			t.Errorf("%s: unexpected partial manifest entry", name)
			continue
		}
		refProps := schemaProperties(t, "manifest."+name, entry.Parameters)
		gotProps := schemaProperties(t, "live."+name, def.Parameters)
		for param, want := range refProps {
			got, ok := gotProps[param]
			if !ok {
				t.Errorf("%s.%s: missing from the live schema", name, param)
				continue
			}
			if !flatJSONEqual(got, want) {
				t.Errorf("%s.%s: not the Python source schema\ngot:  %#v\nwant: %#v", name, param, got, want)
			}
		}
		extra := make([]string, 0, 2)
		for param := range gotProps {
			if _, ok := refProps[param]; !ok {
				extra = append(extra, param)
			}
		}
		sort.Strings(extra)
		if want := []string{"action", "run_id"}; !reflect.DeepEqual(extra, want) {
			t.Errorf("%s: extra parameters = %v, want the recorded folded controls %v", name, extra, want)
		}
		refRequired, refHas := entry.Parameters["required"]
		gotRequired, gotHas := def.Parameters["required"]
		if refHas != gotHas || (refHas && !flatJSONEqual(gotRequired, refRequired)) {
			t.Errorf("%s: required = %#v (present %v), want %#v (present %v)",
				name, gotRequired, gotHas, refRequired, refHas)
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
