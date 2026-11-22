package cli

// Parity-exempt: tool-contract freezer — report-only.
//
// This is not a parity gate. It re-reads the frozen contract in
// go_hive/contracts/tool-definitions.json (reference = Python hive at the
// commit recorded in the manifest, target = this repo) and reports how far the
// live session tool surface has drifted from it:
//
//   - A. reference vs live catalogue — the alignment backlog (name,
//     description, input schema), the numbers the parent lane tracks.
//   - B. live catalogue vs the Go snapshot inside the manifest — tells the
//     reader when the frozen record itself has gone stale.
//
// It must not fail on either comparison: the surfaces differ today by design
// (the reference reaches 54 tools through the queen allowlist; the Go session
// catalogue carries 137 builtins plus 10 queen lifecycle tools), so a red test
// would block every unrelated change. It fails only when the manifest exists
// but cannot be read or parsed — a broken build artifact, not a contract
// difference.
//
// When the surfaces converge, flip the comparisons below into t.Errorf and drop
// the Parity-exempt tag.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// contractTool is one tool of the frozen manifest. Only the fields the report
// needs are decoded; the manifest keeps the verbatim definitions.
type contractTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
	Layers      []string       `json:"layers"`
	Go          struct {
		Present     bool           `json:"present"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
	} `json:"go"`
}

// contractLayerOutsideSession reports whether every layer a manifest entry
// declares lives outside the queen session catalogue. The reflection memory-file
// tools are handed to the reflection side-agent directly; they never enter the
// session request surface, so comparing them to it would report a fictional gap
// (internal/memory pins them against the same manifest instead).
func contractLayerOutsideSession(layers []string) bool {
	if len(layers) == 0 {
		return false
	}
	for _, layer := range layers {
		switch layer {
		case "llm_request", "mcp_catalog", "lifecycle", "synthetic":
			return false
		}
	}
	return true
}

// Parity-exempt: contract freezer, report-only. It compares the frozen Python
// reference manifest against this repo's live tool catalogue and logs the
// drift; it never fails on a difference, so it cannot gate a parity state that
// is knowingly not reached yet. See the file header for the full rationale.
func TestToolContractFreeze(t *testing.T) {
	path := filepath.Join("..", "..", "..", "contracts", "tool-definitions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("contract manifest not present (%v) — this report needs the committed artifact", err)
	}
	var doc struct {
		SchemaVersion string `json:"schema_version"`
		Reference     struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"reference"`
		Target struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"target"`
		Tools       []contractTool `json:"tools"`
		GoOnlyTools []contractTool `json:"go_only_tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest %s is unreadable: %v", path, err)
	}

	// The memory_timeline feature is off by default, but the contract records
	// search_timeline as an llm_request-layer definition. Turn the feature on
	// for this report so the tool is compared on the surface it is advertised
	// on, instead of being reported as missing from a surface that gates it out.
	t.Setenv("HIVE_MEMORY_TIMELINE", "1")

	// The live session surface: the builtin/integration catalogue plus the
	// queen lifecycle table the chat runtime installs for a queen session.
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	live := map[string]liveTool{}
	for _, def := range catalogue.Defs() {
		live[def.Name] = liveTool{Description: def.Description, Parameters: def.Parameters}
	}
	for _, def := range hiveserver.QueenLifecycleToolDefs() {
		live[def.Name] = liveTool{Description: def.Description, Parameters: def.Parameters}
	}

	t.Logf("contract %s: reference %s %s @ %.8s, target %s %s @ %.8s", doc.SchemaVersion,
		doc.Reference.Name, doc.Reference.Version, doc.Reference.Commit,
		doc.Target.Name, doc.Target.Version, doc.Target.Commit)
	t.Logf("frozen: %d reference tools + %d go-only tools; live catalogue: %d tools",
		len(doc.Tools), len(doc.GoOnlyTools), len(live))

	// --- A. reference vs live -------------------------------------------------
	missing := []string{}
	reflectionLayer := []string{}
	nameOK, descOK, schemaOK := 0, 0, 0
	descDiffs, schemaDiffs := []string{}, []string{}
	frozenNames := map[string]struct{}{}
	for _, tool := range doc.Tools {
		frozenNames[tool.Name] = struct{}{}
		if contractLayerOutsideSession(tool.Layers) {
			reflectionLayer = append(reflectionLayer, tool.Name)
			continue
		}
		def, ok := live[tool.Name]
		if !ok {
			missing = append(missing, tool.Name)
			continue
		}
		nameOK++
		if def.Description == tool.Description {
			descOK++
		} else {
			descDiffs = append(descDiffs, tool.Name)
		}
		if schemaJSON(def.Parameters) == schemaJSON(tool.InputSchema) {
			schemaOK++
		} else {
			schemaDiffs = append(schemaDiffs, tool.Name)
		}
	}
	extra := []string{}
	for name := range live {
		if _, ok := frozenNames[name]; !ok {
			extra = append(extra, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(descDiffs)
	sort.Strings(schemaDiffs)

	t.Logf("A. reference vs live: name %d/%d, description %d/%d, input_schema %d/%d, missing in Go %d, extra in Go %d",
		nameOK, len(doc.Tools), descOK, len(doc.Tools), schemaOK, len(doc.Tools), len(missing), len(extra))
	t.Logf("A. note: live here is the registry union of the session catalogue plus the queen lifecycle tools (search_timeline included via " +
		"HIVE_MEMORY_TIMELINE); the manifest's Go side is the captured REQUEST layer, which is why the name count above differs from the " +
		"manifest's shared count. Entries whose layers all live outside the session catalogue (the reflection side-agent's memory-file " +
		"tools) are excluded from this section.")
	logList(t, "A. missing in Go", missing)
	logList(t, "A. reflection-layer entries (not a session surface)", reflectionLayer)
	logList(t, "A. description differs", descDiffs)
	logList(t, "A. input_schema differs", schemaDiffs)
	logList(t, "A. Go-only (not in reference)", extra)

	// --- C. unified metric: normalized schema over the live intersection -----
	// docs/tool-contract-diff.md §16 makes this section the single
	// authoritative drift metric: reference vs live registry, description
	// verbatim, schema raw and normalized. Normalization matches §14.1:
	// strip CONTEXT_PARAMS, pydantic `title` keys, empty `required` ≡ absent,
	// and numeric equivalence (25.0 ≡ 25).
	normalizedOK := 0
	normalizedDiffs := []string{}
	for _, tool := range doc.Tools {
		def, ok := live[tool.Name]
		if !ok {
			continue
		}
		if recheckSchemaJSON(normalizeRecheckSchema(def.Parameters)) == recheckSchemaJSON(normalizeRecheckSchema(tool.InputSchema)) {
			normalizedOK++
		} else {
			normalizedDiffs = append(normalizedDiffs, tool.Name)
		}
	}
	sort.Strings(normalizedDiffs)
	present := len(doc.Tools) - len(missing)
	descAmongPresent := 0
	for _, tool := range doc.Tools {
		if def, ok := live[tool.Name]; ok && def.Description == tool.Description {
			descAmongPresent++
		}
	}
	t.Logf("C. unified metric (authoritative): present %d/%d, description %d/%d, schema raw %d/%d, schema normalized %d/%d",
		present, len(doc.Tools), descAmongPresent, present, schemaOK, present, normalizedOK, present)
	logList(t, "C. schema differs after normalization", normalizedDiffs)

	// --- B. freeze drift: the manifest's Go snapshot vs live ------------------
	matched, snapMissing, snapChanged := 0, []string{}, []string{}
	snapshotNames := map[string]struct{}{}
	check := func(name, description string, schema map[string]any) {
		snapshotNames[name] = struct{}{}
		def, ok := live[name]
		if !ok {
			snapMissing = append(snapMissing, name)
			return
		}
		matched++
		if def.Description != description || schemaJSON(def.Parameters) != schemaJSON(schema) {
			snapChanged = append(snapChanged, name)
		}
	}
	for _, tool := range doc.Tools {
		if tool.Go.Present && !contractLayerOutsideSession(tool.Layers) {
			check(tool.Name, tool.Go.Description, tool.Go.InputSchema)
		}
	}
	for _, tool := range doc.GoOnlyTools {
		check(tool.Name, tool.Description, tool.InputSchema)
	}
	snapExtra := 0
	for name := range live {
		if _, ok := snapshotNames[name]; !ok {
			snapExtra++
		}
	}
	sort.Strings(snapMissing)
	sort.Strings(snapChanged)
	t.Logf("B. frozen Go snapshot vs live: matched %d, missing %d, text differs %d, live names absent from the freeze %d",
		matched, len(snapMissing), len(snapChanged), snapExtra)
	logList(t, "B. snapshot says present, live lacks it", snapMissing)
	logList(t, "B. description/schema text differs", snapChanged)
	if len(snapChanged) > 0 || snapExtra > 0 {
		t.Logf("B. note: the frozen Go text is the captured REQUEST layer (context params like " +
			"session_cwd stripped, required lists materialized) and was captured with a live home, " +
			"so text drift mixes real surface changes with layer/environment differences; name drift is exact.")
		t.Logf("B. re-run `python3 go_hive/scripts/freeze_tool_contract.py build` after an intended surface change")
	}
}

type liveTool struct {
	Description string
	Parameters  map[string]any
}

// logList prints at most logListCap entries so a wide drift stays readable in
// `go test -v`, then reports how many were withheld.
func logList(t *testing.T, label string, names []string) {
	t.Helper()
	const logListCap = 25
	if len(names) == 0 {
		return
	}
	shown, suffix := names, ""
	if len(shown) > logListCap {
		shown = shown[:logListCap]
		suffix = " …"
	}
	t.Logf("%s (%d): %v%s", label, len(names), shown, suffix)
}

// schemaJSON canonicalizes a JSON Schema for comparison: Go's encoder sorts map
// keys, so byte equality is schema equality for decoded-vs-live maps.
func schemaJSON(schema map[string]any) string {
	if len(schema) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return "!" + err.Error()
	}
	return string(encoded)
}

// recheckContextParams is the framework-injected parameter set §14.1 strips
// before the normalized schema comparison.
var recheckContextParams = map[string]struct{}{
	"agent_id":             {},
	"colony_id":            {},
	"data_dir":             {},
	"principal":            {},
	"profile":              {},
	"profile_display_name": {},
	"session_cwd":          {},
}

// normalizeRecheckSchema applies the §14.1 normalization to one schema node:
// drop pydantic `title` keys, drop CONTEXT_PARAMS from properties/required,
// treat an empty required list as absent, and compare integral numbers as
// integers (25.0 ≡ 25).
func normalizeRecheckSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "title" {
				continue
			}
			out[key] = normalizeRecheckSchema(item)
		}
		if props, ok := out["properties"].(map[string]any); ok {
			for name := range recheckContextParams {
				delete(props, name)
			}
		}
		switch required := out["required"].(type) {
		case []any:
			kept := make([]any, 0, len(required))
			for _, item := range required {
				if name, isString := item.(string); isString {
					if _, context := recheckContextParams[name]; context {
						continue
					}
				}
				kept = append(kept, item)
			}
			if len(kept) == 0 {
				delete(out, "required")
			} else {
				out["required"] = kept
			}
		case []string:
			kept := make([]string, 0, len(required))
			for _, item := range required {
				if _, context := recheckContextParams[item]; context {
					continue
				}
				kept = append(kept, item)
			}
			if len(kept) == 0 {
				delete(out, "required")
			} else {
				out["required"] = kept
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeRecheckSchema(item)
		}
		return out
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) && typed == math.Trunc(typed) {
			return int64(typed)
		}
		return typed
	case json.Number:
		// The parity overlay decodes the contract manifest with UseNumber(),
		// so live defaults arrive as json.Number: 25.0 must compare equal to
		// the reference's float64 25.
		if value, err := typed.Int64(); err == nil {
			return value
		}
		if value, err := typed.Float64(); err == nil {
			if value == math.Trunc(value) {
				return int64(value)
			}
			return value
		}
		return typed.String()
	default:
		return value
	}
}

// recheckSchemaJSON canonicalizes a normalized schema for equality.
func recheckSchemaJSON(normalized any) string {
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "!" + err.Error()
	}
	return string(encoded)
}
