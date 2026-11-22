package tools

import (
	"reflect"
	"sort"
	"testing"
)

// The additions table is data mirrored byte-for-byte from the Python
// reference; this snapshot is the drift alarm for a silent edit.
//
// Parity: framework/agents/queen/queen_tools_defaults.py::_CATEGORY_ADDITIONS
func TestCategoryAdditionsTableMirrorsUpstream(t *testing.T) {
	want := map[string]map[string]string{
		"browser_core":      {"browser_setup": "0.3.0"},
		"file_ops":          {"attach_file": "0.2.19"},
		"context_awareness": {"search_timeline": "0.7.2"},
		"media":             {"image_generate": "0.3.0"},
		"email_senders": {
			"list_senders":       "0.2.30",
			"send_from_sender":   "0.2.30",
			"pick_sender":        "0.2.30",
			"send_campaign":      "0.2.30",
			"setup_email_sender": "0.3.0",
			"sender_history":     "0.3.0",
			"suppress_recipient": "0.3.0",
			"list_suppressed":    "0.3.0",
			"adjust_sender":      "0.3.0",
		},
	}
	if !reflect.DeepEqual(CategoryAdditions, want) {
		t.Fatalf("CategoryAdditions = %#v, want %#v", CategoryAdditions, want)
	}
	for category := range CategoryAdditions {
		if _, known := ToolCategories[category]; !known {
			t.Fatalf("addition category %q is not in ToolCategories", category)
		}
	}
}

// Parity: framework/agents/queen/queen_tools_defaults.py::_parse_version
func TestParseAppVersionMirrorsPythonTuple(t *testing.T) {
	cases := []struct {
		value string
		want  []int
	}{
		{"", []int{0, 0, 0}},
		{"0.2.19", []int{0, 2, 19}},
		{"0.3.0", []int{0, 3, 0}},
		{"0.7.2", []int{0, 7, 2}},
		{"1", []int{1}},
		{"1.2", []int{1, 2}},
		{"0.2.x", []int{0, 0, 0}},
		{"v1.0", []int{0, 0, 0}},
	}
	for _, testCase := range cases {
		if got := ParseAppVersion(testCase.value); !reflect.DeepEqual(got, testCase.want) {
			t.Fatalf("ParseAppVersion(%q) = %#v, want %#v", testCase.value, got, testCase.want)
		}
	}
	// Python tuple ordering: a prefix sorts before its extension.
	if !appVersionBefore(ParseAppVersion("0.2.19"), ParseAppVersion("0.2.19.1")) {
		t.Fatal("0.2.19 must sort before 0.2.19.1")
	}
	if !appVersionBefore(ParseAppVersion("0.2.18"), ParseAppVersion("0.2.19")) {
		t.Fatal("0.2.18 must sort before 0.2.19")
	}
	if appVersionBefore(ParseAppVersion("0.3.0"), ParseAppVersion("0.3.0")) {
		t.Fatal("equal versions must not compare before")
	}
	if appVersionBefore(ParseAppVersion("0.10.0"), ParseAppVersion("0.9.0")) {
		t.Fatal("0.10.0 must sort after 0.9.0 (int chunks, not strings)")
	}
}

// Parity: framework/agents/queen/queen_tools_defaults.py::_category_fully_resolvable
func TestCategoryFullyResolvableRequiresCatalogShorthand(t *testing.T) {
	if !CategoryFullyResolvable("file_ops", nil) {
		t.Fatal("file_ops has no @server shorthand; must resolve without a catalog")
	}
	if CategoryFullyResolvable("charts", nil) {
		t.Fatal("charts' @server:chart-tools must not resolve without a catalog")
	}
	catalog := map[string][]map[string]any{"chart-tools": {{"name": "chart_render"}}}
	if !CategoryFullyResolvable("charts", catalog) {
		t.Fatal("charts must resolve when chart-tools has entries")
	}
	if CategoryFullyResolvable("charts", map[string][]map[string]any{"chart-tools": {}}) {
		t.Fatal("an empty server list is not a resolution")
	}
	if _, ok := CategoryAdditions["charts"]; ok {
		t.Fatal("charts must not carry additions (test premise)")
	}
}

// Parity: framework/agents/queen/queen_tools_defaults.py::infer_category_additions
func TestInferCategoryAdditionsGrantsWhenBaselineCovered(t *testing.T) {
	// file_ops baseline (members minus additions) = {pdf_read}.
	got := InferCategoryAdditions([]string{"pdf_read"}, "0.2.18", nil)
	want := []string{"attach_file", "pdf_read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("infer = %#v, want %#v", got, want)
	}

	// A sidecar saved on or after the promotion version respects the untick.
	unchanged := InferCategoryAdditions([]string{"pdf_read"}, "0.2.19", nil)
	if !reflect.DeepEqual(unchanged, []string{"pdf_read"}) {
		t.Fatalf("infer at promotion version = %#v, want unchanged", unchanged)
	}

	// Covering only the addition (not the baseline) must not grant.
	notCovered := InferCategoryAdditions([]string{"attach_file"}, "0.2.18", nil)
	if !reflect.DeepEqual(notCovered, []string{"attach_file"}) {
		t.Fatalf("infer without baseline coverage = %#v, want unchanged", notCovered)
	}

	// Empty (disable-all) stays empty; nil stays nil.
	if got := InferCategoryAdditions([]string{}, "0.2.18", nil); len(got) != 0 {
		t.Fatalf("empty enabled = %#v, want empty", got)
	}
	if got := InferCategoryAdditions(nil, "0.2.18", nil); got != nil {
		t.Fatalf("nil enabled = %#v, want nil", got)
	}
}

// Parity: framework/agents/queen/queen_tools_defaults.py::infer_category_additions
func TestInferCategoryAdditionsGrantsEveryCoveredCategory(t *testing.T) {
	// context_awareness baseline = members minus search_timeline.
	enabled := []string{"get_current_time", "get_account_info", "search_messages"}
	got := InferCategoryAdditions(enabled, "0.7.1", nil)
	want := append(append([]string{}, enabled...), "search_timeline")
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("infer = %#v, want %#v", got, want)
	}
	// media's baseline is empty (image_generate is the whole category): the
	// heuristic must skip it rather than grant on a coincidental match.
	if containsString(got, "image_generate") {
		t.Fatalf("infer granted an empty-baseline category: %#v", got)
	}
}

// Parity: framework/agents/queen/queen_tools_defaults.py::grant_role_default_additions
func TestGrantRoleDefaultAdditionsGrantsByRole(t *testing.T) {
	categories := []string{"file_ops", "browser_core"}
	got := GrantRoleDefaultAdditions(categories, []string{"pdf_read"}, "0.2.18")
	want := []string{"attach_file", "browser_setup", "pdf_read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grant = %#v, want %#v", got, want)
	}

	// A deliberate untick saved on or after the promotion is respected.
	unchanged := GrantRoleDefaultAdditions(categories, []string{"pdf_read"}, "0.3.0")
	if !reflect.DeepEqual(unchanged, []string{"pdf_read"}) {
		t.Fatalf("grant at current version = %#v, want unchanged", unchanged)
	}

	// A custom queen (no role categories) is skipped.
	if got := GrantRoleDefaultAdditions(nil, []string{"pdf_read"}, "0.2.18"); !reflect.DeepEqual(got, []string{"pdf_read"}) {
		t.Fatalf("grant without categories = %#v, want unchanged", got)
	}

	// Already-granted tools are not double-listed.
	already := GrantRoleDefaultAdditions(categories, []string{"attach_file", "browser_setup", "pdf_read"}, "0.2.18")
	if !reflect.DeepEqual(already, []string{"attach_file", "browser_setup", "pdf_read"}) {
		t.Fatalf("grant with additions already present = %#v", already)
	}

	// Empty/nil enabled returns as-is.
	if got := GrantRoleDefaultAdditions(categories, nil, "0.2.18"); got != nil {
		t.Fatalf("grant nil enabled = %#v", got)
	}
	// Unknown categories contribute nothing.
	if got := GrantRoleDefaultAdditions([]string{"spreadsheet_advanced"}, []string{"pdf_read"}, "0.2.18"); !reflect.DeepEqual(got, []string{"pdf_read"}) {
		t.Fatalf("grant unknown category = %#v", got)
	}
}

// A malformed sidecar version floors at (0,0,0), so a legacy sidecar receives
// every tracked addition (Python `_parse_version` docstring).
//
// Parity: framework/agents/queen/queen_tools_defaults.py::_parse_version
func TestGrantRoleDefaultAdditionsFloorsMalformedVersion(t *testing.T) {
	got := GrantRoleDefaultAdditions([]string{"file_ops"}, []string{"pdf_read"}, "not-a-version")
	want := []string{"attach_file", "pdf_read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grant with malformed version = %#v, want %#v", got, want)
	}
}
