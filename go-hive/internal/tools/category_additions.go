package tools

// Generally-available (GA) tool promotions and the sidecar healing helpers.
//
// Parity: framework/agents/queen/queen_tools_defaults.py::_CATEGORY_ADDITIONS
// Parity: framework/agents/queen/queen_tools_defaults.py::_parse_version
// Parity: framework/agents/queen/queen_tools_defaults.py::_category_fully_resolvable
// Parity: framework/agents/queen/queen_tools_defaults.py::infer_category_additions
// Parity: framework/agents/queen/queen_tools_defaults.py::grant_role_default_additions
//
// A saved tools.json sidecar is a frozen flat list of tool names — it never
// picks up a tool added to a category after the save. The helpers below heal
// that: a tool whose GA-promotion version is newer than the sidecar's
// saved_on_version is granted again. Each addition is keyed by the release
// version that promoted the tool to GA (the version that shipped the entry).
//
// Versions compare as tuples of ints split on "." (see ParseAppVersion);
// anything malformed/missing collapses to (0, 0, 0) so legacy sidecars
// without saved_on_version receive every addition on first migration.

import (
	"sort"
	"strconv"
	"strings"
)

var CategoryAdditions = map[string]map[string]string{
	// The browser_* MCP tools were replaced by the terminal-driven hive-browser
	// CLI in 0.3.0; the sole in-process tool is now browser_setup (in
	// browser_core, always-enabled). Heal saved sidecars that predate the
	// switch — whose lists name now-removed browser_* tools.
	"browser_core": {"browser_setup": "0.3.0"},
	"file_ops":     {"attach_file": "0.2.19"},
	// Native HTTP family ships in 1.0.27. A saved sidecar that already covers
	// web_core's baseline (web_scrape) receives the three new tools; fresh
	// role defaults also see them through http_core.
	"web_core": {
		"http_request":  "1.0.27",
		"http_download": "1.0.27",
		"http_upload":   "1.0.27",
	},
	// The dated timeline next to search_messages ships in 0.7.2.
	"context_awareness": {"search_timeline": "0.7.2"},
	// image_generate shipped in 0.2.27 but "media" was only in three visual
	// roles' defaults, so the other queens could see the image-generation
	// skill while the tool it documents was filtered out of their allowlist.
	// 0.3.0 grants "media" to every role, so re-tag the addition to 0.3.0 —
	// sidecars saved on 0.2.27–0.2.x never had media in their role default and
	// must heal. Cost: for the three roles that already had media, a
	// deliberate untick saved on 0.2.27+ is re-granted once.
	"media": {"image_generate": "0.3.0"},
	// Email senders suite ships in 0.2.30; agent-driven sender setup, the
	// team-wide send log, the do-not-contact list and volume tuning ship in
	// 0.3.0. Heal existing explicit-allowlist sidecars of queens whose role
	// default includes "email_senders".
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

// ParseAppVersion mirrors queen_tools_defaults._parse_version: a dotted
// version string becomes a comparable slice of ints ("0.2.19" → [0 2 19]).
// Missing or malformed input returns the (0, 0, 0) floor so legacy sidecars
// without saved_on_version are treated as older than every tracked addition.
func ParseAppVersion(value string) []int {
	floor := []int{0, 0, 0}
	if value == "" {
		return floor
	}
	parts := make([]int, 0, 3)
	for _, chunk := range strings.Split(value, ".") {
		number, err := strconv.Atoi(chunk)
		if err != nil {
			return floor
		}
		parts = append(parts, number)
	}
	if len(parts) == 0 {
		return floor
	}
	return parts
}

// appVersionBefore reports left < right with Python tuple semantics (the
// shorter tuple sorts first when it is a prefix of the longer one).
func appVersionBefore(left, right []int) bool {
	for index := 0; index < len(left) && index < len(right); index++ {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return len(left) < len(right)
}

// CategoryFullyResolvable mirrors queen_tools_defaults._category_fully_resolvable:
// true iff every "@server:" shorthand in the category resolves to at least one
// catalog tool. An unresolved shorthand would expand to a too-small member set,
// so the caller must skip that category rather than risk a false-positive grant.
func CategoryFullyResolvable(category string, mcpCatalog map[string][]map[string]any) bool {
	for _, entry := range ToolCategories[category] {
		if !strings.HasPrefix(entry, "@server:") {
			continue
		}
		serverName := strings.TrimPrefix(entry, "@server:")
		if len(mcpCatalog[serverName]) == 0 {
			return false
		}
	}
	return true
}

// InferCategoryAdditions mirrors queen_tools_defaults.infer_category_additions:
// the legacy fallback used for queens without a role default. For each category
// with tracked additions, when the saved allowlist already covers the whole
// category as it existed *before* any tracked addition (the baseline), grant
// every addition whose GA-promotion version is newer than the sidecar's
// saved_on_version. An empty enabled list (disable-all) is returned unchanged.
func InferCategoryAdditions(enabled []string, savedOnVersion string, mcpCatalog map[string][]map[string]any) []string {
	if len(enabled) == 0 {
		return enabled
	}
	saved := make(map[string]struct{}, len(enabled))
	for _, name := range enabled {
		saved[name] = struct{}{}
	}
	sidecarVersion := ParseAppVersion(savedOnVersion)

	for category, additions := range CategoryAdditions {
		if !CategoryFullyResolvable(category, mcpCatalog) {
			continue
		}
		members := ResolveCategoryTools(category, mcpCatalog)
		baseline := make(map[string]struct{}, len(members))
		for _, name := range members {
			if _, isAddition := additions[name]; isAddition {
				continue
			}
			baseline[name] = struct{}{}
		}
		if len(baseline) == 0 {
			continue
		}
		covered := true
		for name := range baseline {
			if _, ok := saved[name]; !ok {
				covered = false
				break
			}
		}
		if !covered {
			continue
		}
		for name, version := range additions {
			if appVersionBefore(sidecarVersion, ParseAppVersion(version)) {
				saved[name] = struct{}{}
			}
		}
	}

	out := make([]string, 0, len(saved))
	for name := range saved {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// GrantRoleDefaultAdditions mirrors queen_tools_defaults.grant_role_default_additions:
// append every addition whose category sits in the role's default categories.
// Go hoists the QUEEN_DEFAULT_CATEGORIES lookup to the caller (internal/queen
// owns the role table), so categories is the already-resolved role list; an
// empty list (custom queen) returns enabled unchanged. An addition whose
// version is <= saved_on_version is respected as a deliberate untick.
func GrantRoleDefaultAdditions(categories []string, enabled []string, savedOnVersion string) []string {
	if len(enabled) == 0 || len(categories) == 0 {
		return enabled
	}
	saved := make(map[string]struct{}, len(enabled))
	for _, name := range enabled {
		saved[name] = struct{}{}
	}
	sidecarVersion := ParseAppVersion(savedOnVersion)

	additions := make(map[string]struct{})
	for _, category := range categories {
		for name, version := range CategoryAdditions[category] {
			if _, already := saved[name]; already {
				continue
			}
			if appVersionBefore(sidecarVersion, ParseAppVersion(version)) {
				additions[name] = struct{}{}
			}
		}
	}
	if len(additions) == 0 {
		return enabled
	}

	out := make([]string, 0, len(saved)+len(additions))
	for name := range saved {
		out = append(out, name)
	}
	for name := range additions {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
