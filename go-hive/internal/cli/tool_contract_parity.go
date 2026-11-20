package cli

// Model-visible tool-contract parity for the 36 same-name core tools.
//
// The Python reference (core/framework/tools/harness_tools.py groups plus the
// aden_tools registries) publishes full docstrings as descriptions and richer
// JSON schemas than the Go catalogs kept: Go dropped the Args paragraphs,
// merged newlines, lost the `required` list on single-action tools and
// flattened list item types. Tool-contract parity means the model sees the
// same description and the same input contract on both runtimes.
//
// The manifest (tool_contract_parity.json) is generated from the live Python
// registries by go_hive/scripts/gen_tool_contract_parity.py and frozen here:
// each aligned entry carries the exact upstream description and model-visible
// schema (context params and pydantic titles stripped). applyToolContractParity
// uses it to replace those two fields on the advertised definitions after the
// catalogue has assembled them. Execution is untouched — the registries,
// sandbox and executors keep their own schemas and per-action required lists;
// this layer only fixes what the model is shown.
//
// Terminal tools are owned by the terminal-schema-p0 lane and the Exa module is
// a 7-action aggregate whose flat equivalent would orphan actions; both stay
// out of the override table with their reason recorded (status field), so the
// gap is explicit instead of silently rewritten.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/aden-hive/hive-go/internal/llm"
)

//go:embed tool_contract_parity.json
var toolContractParityJSON []byte

// toolContractParityEntry is one tool's frozen Python contract.
type toolContractParityEntry struct {
	Status           string         `json:"status"`
	Source           string         `json:"source"`
	Reason           string         `json:"reason"`
	ApplyDescription bool           `json:"apply_description"`
	ApplyParameters  bool           `json:"apply_parameters"`
	Description      string         `json:"description"`
	Parameters       map[string]any `json:"parameters"`
}

type toolContractParityManifest struct {
	Provenance map[string]any                     `json:"provenance"`
	Tools      map[string]toolContractParityEntry `json:"tools"`
}

var toolContractParityOnce = sync.OnceValues(func() (*toolContractParityManifest, error) {
	var manifest toolContractParityManifest
	if err := json.Unmarshal(toolContractParityJSON, &manifest); err != nil {
		return nil, fmt.Errorf("tool contract parity manifest: %w", err)
	}
	return &manifest, nil
})

// toolContractParityManifestData returns the embedded manifest. A malformed
// manifest is a check-in bug (the file is compiled in and covered by tests),
// so it fails loudly instead of silently advertising the old schema.
func toolContractParityManifestData() *toolContractParityManifest {
	manifest, err := toolContractParityOnce()
	if err != nil {
		panic(err)
	}
	return manifest
}

// applyToolContractParity rewrites the description and input schema of every
// definition whose manifest entry is marked for application. Definitions not
// in the manifest (and the terminal / exa entries) are returned unchanged.
func applyToolContractParity(defs []llm.ToolDef) []llm.ToolDef {
	manifest := toolContractParityManifestData()
	for index := range defs {
		entry, ok := manifest.Tools[defs[index].Name]
		if !ok {
			continue
		}
		if entry.ApplyDescription {
			defs[index].Description = entry.Description
		}
		if entry.ApplyParameters && entry.Parameters != nil {
			defs[index].Parameters = entry.Parameters
		}
	}
	return defs
}
