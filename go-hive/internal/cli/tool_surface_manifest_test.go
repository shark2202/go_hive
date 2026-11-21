package cli

// Searchable-tools manifest filter, catalogue side.
//
// The manifest announces the queen's allowlist (saved tools.json, else the
// role-default categories expanded against the MCP catalog) minus the eager
// set. These tests pin the resolution and prove the deferred layer itself is
// untouched: every flat directory tool stays loadable through search_tools.
//
// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
// Parity: framework/agents/queen/queen_tools_defaults.py::resolve_queen_default_tools
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_searchable_tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

func manifestCatalogue(t *testing.T) (*sessionToolCatalogue, hivehome.Home) {
	t.Helper()
	root := t.TempDir()
	home := hivehome.Home{Root: root}
	return newSessionToolCatalogue(root, builtin.New(root), home), home
}

func deferredNames(catalogue *sessionToolCatalogue) []string {
	_, deferred := catalogue.LazySurfaceDeps()
	names := make([]string, 0, len(deferred))
	for _, def := range deferred {
		names = append(names, def.Name)
	}
	return names
}

func manifestHasName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestSearchableManifestNamesFollowQueenRoleDefaults: queen_growth's role
// default expands to the upstream allowlist (25 names, @server:chart-tools
// included), and the deferred tools it leaves outside the always-enabled set
// are exactly chart_render + web_scrape — the reference's two-name manifest.
// Parity: framework/agents/queen/queen_tools_defaults.py::resolve_queen_default_tools
func TestSearchableManifestNamesFollowQueenRoleDefaults(t *testing.T) {
	catalogue, _ := manifestCatalogue(t)
	names, filtered := catalogue.SearchableManifestNames("queen_growth")
	if !filtered {
		t.Fatal("queen_growth has a role default, want a filtered manifest")
	}
	allowed := map[string]struct{}{}
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	// One representative per category the role grants, including the
	// @server:chart-tools shorthand and the research bundle.
	for _, want := range []string{"list_senders", "pdf_read", "terminal_exec", "browser_setup", "web_scrape", "search_messages", "chart_render"} {
		if _, ok := allowed[want]; !ok {
			t.Fatalf("role allowlist is missing %q: %v", want, names)
		}
	}
	// The Go-only loadable remainder stays deferred (filter is text-only).
	deferred := deferredNames(catalogue)
	for _, want := range []string{"csv_read", "edit_file", "read_file", "tracker_upsert", "web_search"} {
		if !manifestHasName(deferred, want) {
			t.Fatalf("deferred layer is missing %q (loadability regressed)", want)
		}
	}
	announced := make([]string, 0, len(deferred))
	for _, name := range deferred {
		if _, ok := allowed[name]; ok {
			announced = append(announced, name)
		}
	}
	if strings.Join(announced, ",") != "chart_render,web_scrape" {
		t.Fatalf("announced deferred tools = %v, want [chart_render web_scrape]", announced)
	}
}

// TestSearchableManifestNamesPreferToolsSidecar: a saved tools.json is
// authoritative, exactly as load_queen_tools_config treats it — including the
// empty list, which announces nothing.
// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
func TestSearchableManifestNamesPreferToolsSidecar(t *testing.T) {
	catalogue, home := manifestCatalogue(t)
	queenDir := filepath.Join(home.QueensDir(), "queen_growth")
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatalf("mkdir queen dir: %v", err)
	}
	sidecar := filepath.Join(queenDir, "tools.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(sidecar, []byte(body), 0o644); err != nil {
			t.Fatalf("write tools.json: %v", err)
		}
	}

	write(`{"enabled_mcp_tools": ["chart_render"]}`)
	names, filtered := catalogue.SearchableManifestNames("queen_growth")
	if !filtered || strings.Join(names, ",") != "chart_render" {
		t.Fatalf("saved allowlist not honoured: filtered=%v names=%v", filtered, names)
	}

	write(`{"enabled_mcp_tools": []}`)
	names, filtered = catalogue.SearchableManifestNames("queen_growth")
	if !filtered {
		t.Fatal("an empty saved allowlist is authoritative (announce nothing)")
	}
	if names == nil || len(names) != 0 {
		t.Fatalf("empty saved allowlist = %v, want an empty non-nil list", names)
	}
}

// TestSearchableManifestNamesAllowAllFallThrough: the shared default queen has
// no role default, so upstream leaves enabled_mcp_tools unset (allow-all) and
// so does Go — the whole deferred layer is announced.
// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
func TestSearchableManifestNamesAllowAllFallThrough(t *testing.T) {
	catalogue, _ := manifestCatalogue(t)
	for _, queenID := range []string{"", "default"} {
		names, filtered := catalogue.SearchableManifestNames(queenID)
		if filtered || names != nil {
			t.Fatalf("%q: filtered=%v names=%v, want the allow-all fall-through", queenID, filtered, names)
		}
	}
}

// TestSearchableManifestNamesUnknownQueenResolveFromCatalog: a queen outside the
// role table allows every catalog tool upstream; Go resolves the same allow-all
// against the live catalog, so the announcement stays bounded to real tools.
// Parity: framework/agents/queen/queen_tools_defaults.py::resolve_queen_default_tools
func TestSearchableManifestNamesUnknownQueenResolveFromCatalog(t *testing.T) {
	catalogue, _ := manifestCatalogue(t)
	names, filtered := catalogue.SearchableManifestNames("queen_custom_role")
	if !filtered {
		t.Fatal("an unknown queen resolves its allowlist from the catalog, like upstream's allow-all")
	}
	allowed := map[string]struct{}{}
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	announced := 0
	for _, name := range deferredNames(catalogue) {
		if _, ok := allowed[name]; ok {
			announced++
		}
	}
	if announced == 0 {
		t.Fatalf("unknown queen announced nothing: names=%v", names)
	}
	if strings.Contains(strings.Join(names, ","), "csv_sql") {
		t.Fatal("csv_sql must not enter the allowlist (upstream lists it as unavailable, not a tool)")
	}
}
