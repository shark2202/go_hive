package skills

import (
	"strings"
	"testing"
)

// Parity-exempt: augment_catalog_for_tools is asserted only indirectly in
func TestAugmentCatalogForToolsNoNames(t *testing.T) {
	for _, names := range [][]string{nil, {}, {""}} {
		if got := AugmentCatalogForTools("<catalog>", names); got != "<catalog>" {
			t.Fatalf("names=%v changed catalog: %q", names, got)
		}
	}
}

// Parity-exempt: the unknown-tool passthrough has no direct pytest; tool gating
func TestAugmentCatalogForToolsUnknownToolUnchanged(t *testing.T) {
	if got := AugmentCatalogForTools("<catalog>", []string{"read_file"}); got != "<catalog>" {
		t.Fatalf("unknown tool changed catalog: %q", got)
	}
}

// Parity: core/tests/test_worker_tool_tiers.py::test_gating_reads_eager_set_not_pool
// Parity-exempt: browser-body rendering of the pre-activated skill has no
func TestAugmentCatalogForToolsBrowserBody(t *testing.T) {
	resetToolGatedBodyCache()
	got := AugmentCatalogForTools("<catalog>", []string{"browser_click"})

	if !strings.HasPrefix(got, "<catalog>\n\n") {
		t.Fatalf("base should come first: %q", got)
	}
	if !strings.Contains(got, "--- Pre-Activated Skill: hive.browser-automation ---\n# GCU Browser Automation") {
		t.Fatalf("missing browser block: %q", got)
	}
	if strings.Contains(got, "name: hive.browser-automation") {
		t.Fatalf("frontmatter should be stripped: %q", got)
	}
}

// Parity-exempt: preset-skill lookup for chart_render has no direct Python
func TestAugmentCatalogForToolsFindsPresetSkill(t *testing.T) {
	resetToolGatedBodyCache()
	got := AugmentCatalogForTools("", []string{"chart_render"})

	if !strings.HasPrefix(got, "--- Pre-Activated Skill: hive.chart-creation-foundations ---\n") {
		t.Fatalf("missing chart block: %q", got)
	}
	if strings.HasPrefix(got, "\n") {
		t.Fatalf("empty base should not leave a leading blank line: %q", got)
	}
}

// Parity-exempt: emitting pre-activated blocks in table order has no direct
func TestAugmentCatalogForToolsTableOrder(t *testing.T) {
	resetToolGatedBodyCache()
	got := AugmentCatalogForTools("<catalog>", []string{"terminal_exec", "browser_click"})

	browser := strings.Index(got, "Pre-Activated Skill: hive.browser-automation")
	terminal := strings.Index(got, "Pre-Activated Skill: hive.terminal-tools-foundations")
	if browser < 0 || terminal < 0 {
		t.Fatalf("missing blocks: %q", got)
	}
	if browser > terminal {
		t.Fatalf("blocks not in table order: browser=%d terminal=%d", browser, terminal)
	}
}
