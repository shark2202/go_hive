package tools

import (
	"sort"
	"strings"
	"testing"
)

var measuredCold = map[string]struct{}{
	"browser_upload":         {},
	"browser_dialog_respond": {},
	"browser_console":        {},
	"browser_html":           {},
	"browser_get_text":       {},
	"browser_select":         {},
	"browser_resize":         {},
	"search_messages":        {},
	"terminal_rg":            {},
	"terminal_glob":          {},
	"pdf_read":               {},
}

const workerKeepSetMax = 24

// Parity: core/tests/test_toolset_tier_invariants.py::test_browser_split_partitions_legacy_categories
func TestBrowserSplitPartitionsLegacyCategories(t *testing.T) {
	core := stringSet(ToolCategories["browser_core"]...)
	extended := stringSet(ToolCategories["browser_extended"]...)
	legacy := unionSets(stringSet(ToolCategories["browser_basic"]...), stringSet(ToolCategories["browser_interaction"]...))

	intersection := intersectSets(core, extended)
	if len(intersection) != 1 {
		t.Fatalf("browser_core ∩ browser_extended = %v, want {browser_setup}", sortedSetNames(intersection))
	}
	if _, ok := intersection["browser_setup"]; !ok {
		t.Fatalf("browser_core ∩ browser_extended = %v, want {browser_setup}", sortedSetNames(intersection))
	}
	if diff := symmetricDifference(unionSets(core, extended), legacy); len(diff) != 0 {
		t.Fatalf("browser_core ∪ browser_extended != browser_basic ∪ browser_interaction; symmetric diff = %v", sortedSetNames(diff))
	}
}

// Parity: core/tests/test_toolset_tier_invariants.py::test_terminal_split_partitions_basic
func TestTerminalSplitPartitionsBasic(t *testing.T) {
	core := stringSet(ToolCategories["terminal_core"]...)
	extended := stringSet(ToolCategories["terminal_extended"]...)
	basic := stringSet(ToolCategories["terminal_basic"]...)

	if overlap := intersectSets(core, extended); len(overlap) != 0 {
		t.Fatalf("terminal_core ∩ terminal_extended = %v, want ∅", sortedSetNames(overlap))
	}
	if diff := symmetricDifference(unionSets(core, extended), basic); len(diff) != 0 {
		t.Fatalf("terminal_core ∪ terminal_extended != terminal_basic; symmetric diff = %v", sortedSetNames(diff))
	}
}

// Parity: core/tests/test_toolset_tier_invariants.py::test_keep_set_categories_exist
func TestKeepSetCategoriesExist(t *testing.T) {
	for cat := range WorkerAlwaysEnabledCategories {
		if _, ok := ToolCategories[cat]; !ok {
			t.Errorf("unknown category in worker keep-set: %s", cat)
		}
	}
	for cat := range AlwaysEnabledCategories {
		if _, ok := ToolCategories[cat]; !ok {
			t.Errorf("unknown category in queen keep-set: %s", cat)
		}
	}
}

// Parity: core/tests/test_toolset_tier_invariants.py::test_worker_keep_set_excludes_measured_cold_tail
func TestWorkerKeepSetExcludesMeasuredColdTail(t *testing.T) {
	eager := WorkerAlwaysEnabledToolNames(nil)
	if len(eager) == 0 {
		t.Fatal("worker keep-set unexpectedly empty (split accidentally dark?)")
	}
	overlap := intersectSets(eager, measuredCold)
	if len(overlap) != 0 {
		t.Fatalf("measured-cold tools re-entered the worker eager tier: %v", sortedSetNames(overlap))
	}
}

// Parity: core/tests/test_toolset_tier_invariants.py::test_queen_eager_tier_excludes_browser_cold_tail
func TestQueenEagerTierExcludesBrowserColdTail(t *testing.T) {
	eager := AlwaysEnabledToolNames(nil)
	browserCold := map[string]struct{}{}
	for name := range measuredCold {
		if strings.HasPrefix(name, "browser_") {
			browserCold[name] = struct{}{}
		}
	}
	overlap := intersectSets(eager, browserCold)
	if len(overlap) != 0 {
		t.Fatalf("cold browser tools re-entered the queen eager tier: %v", sortedSetNames(overlap))
	}
}

// Parity: core/tests/test_toolset_tier_invariants.py::test_worker_keep_set_size_budget
func TestWorkerKeepSetSizeBudget(t *testing.T) {
	eager := WorkerAlwaysEnabledToolNames(nil)
	if len(eager) > workerKeepSetMax {
		t.Fatalf("worker keep-set grew to %d names (max %d): %v", len(eager), workerKeepSetMax, sortedSetNames(eager))
	}
}

// Parity: core/tests/test_toolset_tier_invariants.py::test_worker_keep_set_keeps_the_measured_hot_core
func TestWorkerKeepSetKeepsMeasuredHotCore(t *testing.T) {
	eager := WorkerAlwaysEnabledToolNames(nil)
	for _, outlier := range []string{"browser_setup", "terminal_exec", "attach_file", "web_scrape"} {
		if _, ok := eager[outlier]; !ok {
			t.Errorf("measured hot tool missing from worker keep-set: %s (have %v)", outlier, sortedSetNames(eager))
		}
	}
}

func stringSet(items ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[item] = struct{}{}
	}
	return out
}

func intersectSets(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for name := range a {
		if _, ok := b[name]; ok {
			out[name] = struct{}{}
		}
	}
	return out
}

func unionSets(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for name := range a {
		out[name] = struct{}{}
	}
	for name := range b {
		out[name] = struct{}{}
	}
	return out
}

func symmetricDifference(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for name := range a {
		if _, ok := b[name]; !ok {
			out[name] = struct{}{}
		}
	}
	for name := range b {
		if _, ok := a[name]; !ok {
			out[name] = struct{}{}
		}
	}
	return out
}

func sortedSetNames(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
