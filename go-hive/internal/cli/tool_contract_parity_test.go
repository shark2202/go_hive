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
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// parityCatalogueDefs builds the real session catalogue and indexes its
// model-visible definitions. Duplicate names would make a model-facing surface
// ambiguous, so they fail here.
func parityCatalogueDefs(t *testing.T) map[string]llm.ToolDef {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
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
	if len(manifest.Tools) != 36 {
		t.Fatalf("manifest holds %d tools, want the 36 audited same-name tools", len(manifest.Tools))
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
	if counts["aligned"] != 25 || counts["partial"] != 1 || counts["dependency"] != 10 {
		t.Fatalf("manifest status counts = %v, want aligned 25 / partial 1 / dependency 10", counts)
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

// Parity-exempt: the known-gap list is the P1 lane's recorded delivery state,
// not a Python unit test.
func TestToolContractParityRemainingGaps(t *testing.T) {
	defs := parityCatalogueDefs(t)
	manifest := toolContractParityManifestData()
	var gaps []string
	for name, entry := range manifest.Tools {
		def, ok := defs[name]
		if !ok {
			t.Fatalf("%s: manifest tool missing from the session catalogue", name)
		}
		if def.Description == entry.Description && reflect.DeepEqual(def.Parameters, entry.Parameters) {
			continue
		}
		gaps = append(gaps, name)
	}
	sort.Strings(gaps)
	want := []string{
		"exa_search",
		"terminal_exec", "terminal_glob", "terminal_job_logs",
		"terminal_job_manage", "terminal_job_start", "terminal_output_get",
		"terminal_pty_close", "terminal_pty_open", "terminal_pty_run",
		"terminal_rg",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(gaps, want) {
		t.Fatalf("remaining gaps = %v\nwant (recorded) = %v", gaps, want)
	}
}
