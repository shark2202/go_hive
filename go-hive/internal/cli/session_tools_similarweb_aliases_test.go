package cli

// Session assembly guard for the upstream SimilarWeb tool names. Upstream
// registers 29 `similarweb_v5_*` MCP tools; the Go port keeps the aggregate
// `similarweb` grouped tool and publishes the upstream names as compatibility
// aliases. They must reach the session registry/defs (so callers can enumerate
// them) but stay out of the frozen 39-name default model surface.

import (
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// similarWebUpstreamToolNames is the upstream registration order from
// tools/src/aden_tools/tools/similarweb_tool/similarweb_tool.py.
var similarWebUpstreamToolNames = []string{
	"similarweb_v5_traffic_and_engagement",
	"similarweb_v5_website_rank",
	"similarweb_v5_traffic_sources",
	"similarweb_v5_geography",
	"similarweb_v5_demographics",
	"similarweb_v5_company_info",
	"similarweb_v5_top_sites_by_category",
	"similarweb_v5_referrals",
	"similarweb_v5_ppc_spend",
	"similarweb_v5_geography_details",
	"similarweb_v5_similar_sites",
	"similarweb_v5_ad_networks",
	"similarweb_v5_demographics_traffic",
	"similarweb_v5_deduplicated_audience",
	"similarweb_v5_audience_interests",
	"similarweb_v5_audience_overlap",
	"similarweb_v5_technologies",
	"similarweb_v5_leading_folders",
	"similarweb_v5_popular_pages",
	"similarweb_v5_subdomains",
	"similarweb_v5_keyword_competitors",
	"similarweb_v5_keyword_opportunities",
	"similarweb_v5_serp_features",
	"similarweb_v5_organic_keywords",
	"similarweb_v5_paid_keywords",
	"similarweb_v5_serp_players",
	"similarweb_v5_social_referrals",
	"similarweb_v5_segments_list",
	"similarweb_v5_segment_analysis",
}

// Parity-exempt: assembly guard. The Go flat surface has no upstream
// counterpart for this alias layer; it keeps the grouped `similarweb` contract
// for the 39-name default surface and exposes the upstream names on the
// registry/lazy tier.
func TestSessionCatalogueRegistersSimilarWebToolNames(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	defined := map[string]int{}
	for _, def := range catalogue.Defs() {
		defined[def.Name]++
	}
	for _, name := range similarWebUpstreamToolNames {
		if defined[name] != 1 {
			t.Fatalf("similarweb tool %q appears %d times in session defs, want exactly 1", name, defined[name])
		}
		if !catalogue.registry.HasTool(name) {
			t.Fatalf("similarweb tool %q missing from the session registry", name)
		}
	}
	if !catalogue.registry.HasTool("similarweb") {
		t.Fatal("aggregate similarweb tool was removed")
	}

	// The frozen default surface stays at the reference 39 names: neither the
	// aggregate grouped tool nor its upstream aliases may leak into it.
	for _, def := range catalogue.ModelDefs() {
		if def.Name == "similarweb" || strings.HasPrefix(def.Name, "similarweb_v5_") {
			t.Fatalf("%q leaked into the frozen default model surface", def.Name)
		}
	}

	// The lazy/searchable tier is where callers enumerate and load them.
	lazy := map[string]struct{}{}
	for _, def := range catalogue.LazyModelDefs() {
		lazy[def.Name] = struct{}{}
	}
	for _, name := range similarWebUpstreamToolNames {
		if _, ok := lazy[name]; !ok {
			t.Fatalf("similarweb tool %q is not loadable from the lazy tier", name)
		}
	}
}
