package cli

// Tool-contract parity tests (P1): the model-visible definition of every
// same-name core tool must match the Python registry entry recorded in
// tool_contract_parity.json. The manifest is generated from the live Python
// registries (go_hive/scripts/gen_tool_contract_parity.py); these tests check
// that the assembled session catalogue actually carries it, that the P1
// defect classes named by the audit are fixed, and that the remaining gaps are
// exactly the recorded ones instead of silent drift.

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// parityCatalogueDefs builds the real session catalogue (queen lifecycle table
// included, the way a queen chat session gets it) and indexes its
// model-visible definitions. Duplicate names would make a model-facing surface
// ambiguous, so they fail here.
func parityCatalogueDefs(t *testing.T) map[string]llm.ToolDef {
	t.Helper()
	root := testTempDir(t)
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	home := hivehome.Home{Root: root}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	enableQueenLifecycleTools(catalogue, &hiveserver.Session{}, home)
	defs := make(map[string]llm.ToolDef, len(catalogue.Defs()))
	for _, def := range catalogue.Defs() {
		if _, exists := defs[def.Name]; exists {
			t.Fatalf("duplicate tool definition %q in the session catalogue", def.Name)
		}
		defs[def.Name] = def
	}
	return defs
}

// parityRequired reads a schema's required list as []string.
func parityRequired(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["required"]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			out = append(out, text)
		}
		return out
	default:
		t.Fatalf("required has unexpected type %T", raw)
		return nil
	}
}

// parityProperty returns one property schema from a tool definition.
func parityProperty(t *testing.T, def llm.ToolDef, name string) map[string]any {
	t.Helper()
	props, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s: parameters have no properties map", def.Name)
	}
	prop, ok := props[name].(map[string]any)
	if !ok {
		t.Fatalf("%s: missing parameter %q", def.Name, name)
	}
	return prop
}

// parityAnyOf returns a property's anyOf branches.
func parityAnyOf(t *testing.T, prop map[string]any) []any {
	t.Helper()
	branches, ok := prop["anyOf"].([]any)
	if !ok || len(branches) == 0 {
		t.Fatalf("expected anyOf branches, got %#v", prop)
	}
	return branches
}

// parityEnum reads an enum as a string slice. Catalog params hold []string;
// JSON-decoded manifest params hold []any.
func parityEnum(t *testing.T, prop map[string]any) []string {
	t.Helper()
	switch raw := prop["enum"].(type) {
	case []string:
		return append([]string(nil), raw...)
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			text, _ := item.(string)
			out = append(out, text)
		}
		return out
	default:
		t.Fatalf("expected enum, got %#v", prop)
		return nil
	}
}

// Parity-exempt: the manifest is generated from the Python registries by
// go_hive/scripts/gen_tool_contract_parity.py; Python has no unit test for the
// frozen fixture itself.
func TestToolContractParityManifestShape(t *testing.T) {
	manifest := toolContractParityManifestData()
	if len(manifest.Tools) != 50 {
		t.Fatalf("manifest holds %d tools, want the 50 audited same-name tools (42 core + 8 queen lifecycle)", len(manifest.Tools))
	}
	counts := map[string]int{}
	for name, entry := range manifest.Tools {
		counts[entry.Status]++
		if entry.Description == "" {
			t.Fatalf("%s: empty description in manifest", name)
		}
		if entry.Parameters["type"] != "object" {
			t.Fatalf("%s: parameters are not an object schema: %#v", name, entry.Parameters["type"])
		}
		if entry.Status == "aligned" {
			if !entry.ApplyDescription || !entry.ApplyParameters {
				t.Fatalf("%s: aligned entry must apply description and parameters", name)
			}
			continue
		}
		if entry.ApplyDescription || entry.ApplyParameters {
			t.Fatalf("%s: %s entry must not be applied automatically", name, entry.Status)
		}
		if entry.Reason == "" {
			t.Fatalf("%s: %s entry has no recorded reason", name, entry.Status)
		}
	}
	if counts["aligned"] != 39 || counts["partial"] != 1 || counts["dependency"] != 10 {
		t.Fatalf("manifest status counts = %v, want aligned 39 / partial 1 / dependency 10", counts)
	}
}

// Parity-exempt: the assertion target is the Python contract fixture, not a
// Python unit test; the fixture is generated from the live registries.
func TestToolContractParityAlignedDefsMatchManifest(t *testing.T) {
	defs := parityCatalogueDefs(t)
	manifest := toolContractParityManifestData()
	for name, entry := range manifest.Tools {
		if entry.Status != "aligned" {
			continue
		}
		def, ok := defs[name]
		if !ok {
			t.Fatalf("%s: aligned in the manifest but absent from the session catalogue", name)
		}
		if def.Description != entry.Description {
			t.Fatalf("%s: description is not the Python contract (got %d bytes, want %d)",
				name, len(def.Description), len(entry.Description))
		}
		if !reflect.DeepEqual(def.Parameters, entry.Parameters) {
			t.Fatalf("%s: input schema is not the Python contract\ngot:  %#v\nwant: %#v",
				name, def.Parameters, entry.Parameters)
		}
	}
}

// Parity-exempt: the P1 defect list comes from go_hive/docs/tool-consistency-audit.html
// (Python registry comparison), not from a Python unit test.
func TestToolContractParityP1DefectsAreFixed(t *testing.T) {
	defs := parityCatalogueDefs(t)

	t.Run("required-restored", func(t *testing.T) {
		for name, want := range map[string][]string{
			"attach_file":      {"paths"},
			"image_generate":   {"prompt"},
			"web_scrape":       {"url"},
			"web_search":       {"query"},
			"search_wikipedia": {"query"},
		} {
			def, ok := defs[name]
			if !ok {
				t.Fatalf("%s: missing definition", name)
			}
			if got := parityRequired(t, def.Parameters); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: required = %v, want %v", name, got, want)
			}
		}
	})

	t.Run("array-items-narrowed", func(t *testing.T) {
		for name, param := range map[string]string{
			"csv_write":      "columns",
			"csv_append":     "rows",
			"excel_write":    "columns",
			"excel_append":   "rows",
			"pdf_read":       "files",
			"image_generate": "reference_images",
		} {
			def := defs[name]
			prop := parityProperty(t, def, param)
			if name == "pdf_read" || name == "image_generate" {
				branches := parityAnyOf(t, prop)
				array := branches[0].(map[string]any)
				items, _ := array["items"].(map[string]any)
				if array["type"] != "array" || items["type"] != "string" {
					t.Fatalf("%s.%s: want array<string>, got %#v", name, param, prop)
				}
				continue
			}
			items, _ := prop["items"].(map[string]any)
			if prop["type"] != "array" || items == nil {
				t.Fatalf("%s.%s: want typed array, got %#v", name, param, prop)
			}
			if param == "rows" {
				if items["type"] != "object" || items["additionalProperties"] != true {
					t.Fatalf("%s.%s: want array<object additionalProperties=true>, got %#v", name, param, prop)
				}
			} else if items["type"] != "string" {
				t.Fatalf("%s.%s: want array<string>, got %#v", name, param, prop)
			}
		}
	})

	t.Run("enums-restored", func(t *testing.T) {
		if got := parityEnum(t, parityProperty(t, defs["web_search"], "provider")); !reflect.DeepEqual(got, []string{"auto", "google", "brave"}) {
			t.Fatalf("web_search.provider enum = %v", got)
		}
		if got := parityEnum(t, parityProperty(t, defs["exa_search"], "search_type")); !reflect.DeepEqual(got, []string{"auto", "neural", "keyword"}) {
			t.Fatalf("exa_search.search_type enum = %v", got)
		}
		if got := parityEnum(t, parityProperty(t, defs["arxiv"], "sort_by")); !reflect.DeepEqual(got, []string{"relevance", "lastUpdatedDate", "submittedDate"}) {
			t.Fatalf("arxiv.sort_by enum = %v", got)
		}
		if got := parityEnum(t, parityProperty(t, defs["arxiv"], "sort_order")); !reflect.DeepEqual(got, []string{"descending", "ascending"}) {
			t.Fatalf("arxiv.sort_order enum = %v", got)
		}
	})

	t.Run("nullable-union", func(t *testing.T) {
		// attach_file.paths accepts a bare string or a list; the audit counted
		// Go's ARRAY<STRING> as a type mismatch against Python's union.
		paths := parityProperty(t, defs["attach_file"], "paths")
		branches := parityAnyOf(t, paths)
		if len(branches) != 2 {
			t.Fatalf("attach_file.paths anyOf = %#v", paths)
		}
		first, _ := branches[0].(map[string]any)
		second, _ := branches[1].(map[string]any)
		if first["type"] != "string" || second["type"] != "array" {
			t.Fatalf("attach_file.paths branches = %#v", branches)
		}
		items, _ := second["items"].(map[string]any)
		if items["type"] != "string" {
			t.Fatalf("attach_file.paths array items = %#v", second["items"])
		}
	})
}

// Terminal tool names whose request shape is compared against the Python
// contract below.
var terminalContractToolNames = []string{
	"terminal_exec", "terminal_glob", "terminal_rg", "terminal_job_start",
	"terminal_job_logs", "terminal_job_manage", "terminal_output_get",
	"terminal_pty_open", "terminal_pty_run", "terminal_pty_close",
}

// Parity-exempt: the assertion target is the Python contract fixture
// (tool_contract_parity.json, generated from the live Python registries), not a
// Python unit test. This is the recheck's口径 applied in-process: take the
// definition the catalogue advertises, run it through the exact request
// pipeline (StripContextParams -> ToolToOpenAIFormat), and require the
// serialized JSON Schema to equal the Python one.
func TestTerminalToolsRequestShapeMatchesPythonContract(t *testing.T) {
	catalogue := newSessionToolCatalogue(t.TempDir(), builtin.New(t.TempDir()), hivehome.Home{})
	manifest := toolContractParityManifestData()
	// Index the model-visible defs the way a session runtime publishes them.
	visible := map[string]llm.ToolDef{}
	for _, def := range tools.StripContextParams(catalogue.Defs()) {
		visible[def.Name] = def
	}
	for _, name := range terminalContractToolNames {
		entry, ok := manifest.Tools[name]
		if !ok {
			t.Fatalf("%s: no Python contract entry in the parity manifest", name)
		}
		def, ok := visible[name]
		if !ok {
			t.Fatalf("%s: missing from the session catalogue", name)
		}
		got := requestParametersJSON(t, def)
		want := schemaJSON(entry.Parameters)
		if got != want {
			t.Fatalf("%s: request schema is not the Python contract\ngot:  %s\nwant: %s", name, got, want)
		}
		if def.Description != entry.Description {
			t.Fatalf("%s: description is not the Python contract (got %d bytes, want %d)",
				name, len(def.Description), len(entry.Description))
		}
	}
}

// requestParametersJSON renders the parameters object exactly as the provider
// request carries it.
func requestParametersJSON(t *testing.T, def llm.ToolDef) string {
	t.Helper()
	wire := llm.ToolToOpenAIFormat(def)
	fn, _ := wire["function"].(map[string]any)
	return schemaJSON(fn["parameters"].(map[string]any))
}

// Parity-exempt: same fixture as above; this checks the two serialization
// rules the recheck's 归一化口径 had to paper over — an empty `required` list
// stays absent unless the contract declares it (pydantic omits it; MCP tool
// schemas publish it), and float defaults keep their decimal spelling.
func TestRequestSchemaSerializationMatchesPythonContract(t *testing.T) {
	catalogue := newSessionToolCatalogue(t.TempDir(), builtin.New(t.TempDir()), hivehome.Home{})
	defs := append([]llm.ToolDef{}, catalogue.Defs()...)
	defs = append(defs, hiveserver.QueenLifecycleToolDefs()...)
	visible := map[string]llm.ToolDef{}
	for _, def := range tools.StripContextParams(defs) {
		visible[def.Name] = def
	}

	for _, name := range []string{"list_browser_profiles", "list_triggers", "list_worker_questions", "edit_file", "terminal_pty_open"} {
		def, ok := visible[name]
		if !ok {
			t.Fatalf("%s: missing from the session catalogue", name)
		}
		wire := requestParametersJSON(t, def)
		if strings.Contains(wire, `"required":[]`) {
			t.Fatalf("%s: empty required list must stay absent (Python omits the key): %s", name, wire)
		}
	}

	// write_skill is the mirror case: its Python schema publishes required: [].
	writeSkill, ok := visible["write_skill"]
	if !ok {
		t.Fatal("write_skill: missing from the session catalogue")
	}
	if wire := requestParametersJSON(t, writeSkill); !strings.Contains(wire, `"required":[]`) {
		t.Fatalf("write_skill: Python publishes an explicit empty required list: %s", wire)
	}

	// attach_file / pdf_read publish max_bytes_mb default 25.0 upstream; the
	// decimal spelling must survive the manifest decode.
	for _, name := range []string{"attach_file", "pdf_read"} {
		def, ok := visible[name]
		if !ok {
			t.Fatalf("%s: missing from the session catalogue", name)
		}
		wire := requestParametersJSON(t, def)
		if !strings.Contains(wire, `"default":25.0`) {
			t.Fatalf("%s: max_bytes_mb default must serialize as 25.0: %s", name, wire)
		}
	}
}

// Parity-exempt: the known-gap list is the recorded delivery state, not a
// Python unit test. The comparison runs on the catalogue definitions that
// feed the request pipeline (builtin defs are context-stripped at source,
// mirroring upstream's MCP conversion; queen lifecycle defs are not, because
// upstream registers those as plain Tool objects and never strips them) and on
// serialized JSON, so it measures what the provider request carries rather
// than Go's internal map typing.
func TestToolContractParityRemainingGaps(t *testing.T) {
	defs := parityCatalogueDefs(t)
	manifest := toolContractParityManifestData()
	var gaps []string
	for name, entry := range manifest.Tools {
		def, ok := defs[name]
		if !ok {
			t.Fatalf("%s: manifest tool missing from the session catalogue", name)
		}
		if def.Description == entry.Description && requestParametersJSON(t, def) == schemaJSON(entry.Parameters) {
			continue
		}
		gaps = append(gaps, name)
	}
	sort.Strings(gaps)
	// One recorded partial: exa_search — Go registers Exa as a 7-action module
	// tool (required `action`) while Python exposes a flat tool (required
	// `query`). Flattening is its own lane. The former run_playbook partial was
	// closed when its folded control parameters moved onto real control tool
	// definitions (docs/tool-contract-diff.md §14).
	want := []string{"exa_search"}
	if !reflect.DeepEqual(gaps, want) {
		t.Fatalf("remaining gaps = %v\nwant (recorded) = %v", gaps, want)
	}
}
