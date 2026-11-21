package server

// Parity: framework/server/routes_sessions.py::handle_session_live_tools

import (
	"net/http"
	"sort"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/tools"
)

// Optional phase-state surfaces. Session.PhaseState is opaque, so the handler
// duck-types the upstream attributes; a missing interface mirrors getattr's
// default (None / empty).
type liveToolsPhaseProvider interface {
	Phase() string
}

type mcpToolNamesProvider interface {
	MCPToolNamesAll() map[string]struct{}
}

type currentToolsProvider interface {
	GetCurrentTools() []tools.Tool
}

type searchableToolsProvider interface {
	GetSearchableTools() []tools.Tool
}

type unregisteredAllowlistProvider interface {
	UnregisteredAllowlistedNames() map[string]struct{}
}

// enabledMCPToolsProvider exposes the phase state's enabled_mcp_tools
// allowlist. isNone reports the Python None case (no allowlist filter); an
// absent interface is also treated as None.
type enabledMCPToolsProvider interface {
	EnabledMCPTools() (names []string, isNone bool)
}

type independentToolsProvider interface {
	IndependentTools() []tools.Tool
}

type filteredIndependentToolsProvider interface {
	FilteredIndependentTools() []tools.Tool
}

// Registry-side diagnostic surfaces (Session.QueenToolRegistry upstream).
type mcpGateSnapshotter interface {
	ComputeMCPGateCredSnapshot() (map[string]string, []string)
}

type mcpServerToolsProvider interface {
	MCPServerTools() map[string][]string
}

type workerToolTierProvider interface {
	WorkerToolTiering() []colony.WorkerToolTiering
}

var syntheticToolNames = map[string]struct{}{
	"ask_user":         {},
	"escalate":         {},
	"report_to_parent": {},
	"suggest_colony":   {},
}

func handleSessionLiveTools(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session '" + sessionID + "' not found"})
		return
	}

	phaseState := session.PhaseState
	var phase any
	if phaseState != nil {
		if provider, ok := phaseState.(liveToolsPhaseProvider); ok {
			phase = provider.Phase()
		}
	}
	mcpNames := map[string]struct{}{}
	if phaseState != nil {
		if provider, ok := phaseState.(mcpToolNamesProvider); ok {
			for name := range provider.MCPToolNamesAll() {
				mcpNames[name] = struct{}{}
			}
		}
	}

	// Build two faithful views — the panel renders them side by side:
	//   * actual_tools   — what the agent loop can LITERALLY call this turn:
	//                      phase_state.get_current_tools() (the eager set)
	//                      plus the ask_user synthetic the loop appends at
	//                      dispatch.
	//   * expected_tools — the configured/allowed surface, each tagged with a
	//                      status so the panel can show WHY an allowed tool
	//                      is not callable yet (searchable / unregistered).
	// When get_current_tools() is unavailable we report actual_tools=[] +
	// phase_state_ready=false rather than falling back to independent_tools
	// (the registry) — that fallback used to misrepresent the allowed surface
	// as the live callable set.
	phaseStateReady := false
	var eagerTools []tools.Tool
	var searchableTools []tools.Tool
	unregisteredNames := map[string]struct{}{}
	if phaseState != nil {
		if provider, ok := phaseState.(currentToolsProvider); ok {
			if fetched, fetchedOK := safeCurrentTools(provider); fetchedOK {
				eagerTools = fetched
				phaseStateReady = true
			}
		}
		if provider, ok := phaseState.(searchableToolsProvider); ok {
			searchableTools = safeSearchableTools(provider)
		}
		if provider, ok := phaseState.(unregisteredAllowlistProvider); ok {
			unregisteredNames = safeUnregisteredNames(provider)
		}
	}

	toolKind := func(name string) string {
		if _, ok := mcpNames[name]; ok {
			return "mcp"
		}
		if _, ok := syntheticToolNames[name]; ok {
			return "synthetic"
		}
		return "lifecycle"
	}

	// actual_tools: the eager (callable-now) set.
	actualTools := []map[string]any{}
	for _, tool := range eagerTools {
		if tool.Name == "" {
			continue
		}
		actualTools = append(actualTools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"kind":        toolKind(tool.Name),
		})
	}

	// ask_user is appended by the agent loop at dispatch for direct-user-io
	// contexts (queens) and is NOT carried in phase_state, so add it to the
	// callable view if it isn't already there.
	frameworkAdded := []map[string]any{}
	if phaseState != nil && !hasToolNamed(actualTools, "ask_user") {
		askUserEntry := map[string]any{
			"name":        "ask_user",
			"description": "Ask the user a question.",
			"kind":        "synthetic",
		}
		frameworkAdded = append(frameworkAdded, askUserEntry)
		actualTools = append(actualTools, askUserEntry)
	}
	sort.SliceStable(actualTools, func(i, j int) bool {
		leftKind, _ := actualTools[i]["kind"].(string)
		rightKind, _ := actualTools[j]["kind"].(string)
		if leftKind != rightKind {
			return leftKind < rightKind
		}
		leftName, _ := actualTools[i]["name"].(string)
		rightName, _ := actualTools[j]["name"].(string)
		return leftName < rightName
	})

	// expected_tools: the configured/allowed surface, status-tagged.
	expectedTools := []map[string]any{}
	for _, tool := range eagerTools {
		if tool.Name == "" {
			continue
		}
		expectedTools = append(expectedTools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"kind":        toolKind(tool.Name),
			"status":      "callable",
		})
	}
	for _, tool := range searchableTools {
		if tool.Name == "" {
			continue
		}
		expectedTools = append(expectedTools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"kind":        toolKind(tool.Name),
			"status":      "searchable",
		})
	}
	sortedUnregistered := make([]string, 0, len(unregisteredNames))
	for name := range unregisteredNames {
		sortedUnregistered = append(sortedUnregistered, name)
	}
	sort.Strings(sortedUnregistered)
	for _, name := range sortedUnregistered {
		expectedTools = append(expectedTools, map[string]any{
			"name":        name,
			"description": "",
			"kind":        toolKind(name),
			"status":      "unregistered",
		})
	}
	statusOrder := map[string]int{"callable": 0, "searchable": 1, "unregistered": 2}
	sort.SliceStable(expectedTools, func(i, j int) bool {
		leftStatus, _ := expectedTools[i]["status"].(string)
		rightStatus, _ := expectedTools[j]["status"].(string)
		if statusOrder[leftStatus] != statusOrder[rightStatus] {
			return statusOrder[leftStatus] < statusOrder[rightStatus]
		}
		leftKind, _ := expectedTools[i]["kind"].(string)
		rightKind, _ := expectedTools[j]["kind"].(string)
		if leftKind != rightKind {
			return leftKind < rightKind
		}
		leftName, _ := expectedTools[i]["name"].(string)
		rightName, _ := expectedTools[j]["name"].(string)
		return leftName < rightName
	})

	connected := safeConnectedProviders(deps)

	diagnostic := buildLiveToolsDiagnostic(session, phaseState)
	workersView := safeWorkersView(session)

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":                sessionID,
		"phase":                     phase,
		"phase_state_ready":         phaseStateReady,
		"workers":                   workersView,
		"tools":                     actualTools,
		"actual_tools":              actualTools,
		"expected_tools":            expectedTools,
		"framework_added":           frameworkAdded,
		"connected_providers":       connected,
		"diagnostic":                diagnostic,
		"mcp_tool_count_registered": len(mcpNames),
	})
}

func hasToolNamed(entries []map[string]any, name string) bool {
	for _, entry := range entries {
		if entry["name"] == name {
			return true
		}
	}
	return false
}

func safeCurrentTools(provider currentToolsProvider) (out []tools.Tool, ok bool) {
	defer func() {
		if recover() != nil {
			out = nil
			ok = false
		}
	}()
	return provider.GetCurrentTools(), true
}

func safeSearchableTools(provider searchableToolsProvider) (out []tools.Tool) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	return provider.GetSearchableTools()
}

func safeUnregisteredNames(provider unregisteredAllowlistProvider) (out map[string]struct{}) {
	defer func() {
		if recover() != nil {
			out = map[string]struct{}{}
		}
	}()
	names := provider.UnregisteredAllowlistedNames()
	if names == nil {
		return map[string]struct{}{}
	}
	out = make(map[string]struct{}, len(names))
	for name := range names {
		out[name] = struct{}{}
	}
	return out
}

func safeConnectedProviders(deps SessionRouteDeps) []string {
	out := []string{}
	if deps.ConnectedProviders == nil {
		return out
	}
	seen := map[string]struct{}{}
	func() {
		defer func() {
			if recover() != nil {
				out = []string{}
				seen = map[string]struct{}{}
			}
		}()
		for _, provider := range deps.ConnectedProviders() {
			if provider == "" {
				continue
			}
			if _, ok := seen[provider]; ok {
				continue
			}
			seen[provider] = struct{}{}
			out = append(out, provider)
		}
	}()
	sort.Strings(out)
	return out
}

// buildLiveToolsDiagnostic re-runs the admission-gate snapshot computation now
// and walks the session's queen registry to count Google-mapped tools dropped
// at registration. It lets an operator tell "the gate didn't see the
// credential" apart from "credential present but tool not in catalog".
func buildLiveToolsDiagnostic(session *Session, phaseState any) map[string]any {
	diagnostic := map[string]any{}
	func() {
		defer func() {
			if recover() != nil {
				// Upstream swallows diagnostic failures and returns whatever
				// was accumulated, which is just the empty dict on panic.
			}
		}()

		registry := session.QueenToolRegistry
		snapshotter, hasSnapshot := registry.(mcpGateSnapshotter)
		if registry != nil && hasSnapshot {
			tpm, liveProviders := snapshotter.ComputeMCPGateCredSnapshot()
			diagnostic["gate_live_providers_now"] = sortedStrings(liveProviders)
			diagnostic["gate_tool_provider_map_size"] = len(tpm)

			registeredMCP := map[string]struct{}{}
			if provider, ok := registry.(mcpServerToolsProvider); ok {
				for _, names := range provider.MCPServerTools() {
					for _, name := range names {
						registeredMCP[name] = struct{}{}
					}
				}
			}

			admittedByProvider := map[string]int{}
			droppedByProvider := map[string][]string{}
			for toolName, prov := range tpm {
				if prov == "" {
					continue
				}
				if _, admitted := registeredMCP[toolName]; admitted {
					admittedByProvider[prov]++
				} else {
					droppedByProvider[prov] = append(droppedByProvider[prov], toolName)
				}
			}
			diagnostic["admitted_by_provider"] = admittedByProvider
			dropped := map[string]any{}
			for provider, names := range droppedByProvider {
				sorted := append([]string(nil), names...)
				sort.Strings(sorted)
				dropped[provider] = map[string]any{
					"count":  len(names),
					"sample": firstN(sorted, 5),
				}
			}
			diagnostic["dropped_by_provider"] = dropped
			diagnostic["registry_mcp_total"] = len(registeredMCP)

			googleInRegistry := []string{}
			for name := range registeredMCP {
				if tpm[name] == "google" {
					googleInRegistry = append(googleInRegistry, name)
				}
			}
			sort.Strings(googleInRegistry)
			diagnostic["google_admitted_sample"] = firstN(googleInRegistry, 8)
		}

		// phase_state-side counts: see whether the queen lifecycle layer
		// carried the registry's google tools into independent_tools and
		// whether the allowlist filter kept them.
		if phaseState == nil {
			return
		}
		var allowed []string
		allowlistIsNone := true
		if provider, ok := phaseState.(enabledMCPToolsProvider); ok {
			names, isNone := provider.EnabledMCPTools()
			if !isNone {
				allowed = names
				allowlistIsNone = false
			}
		}
		mcpNamesPS := map[string]struct{}{}
		if provider, ok := phaseState.(mcpToolNamesProvider); ok {
			for name := range provider.MCPToolNamesAll() {
				mcpNamesPS[name] = struct{}{}
			}
		}
		var indep []tools.Tool
		if provider, ok := phaseState.(independentToolsProvider); ok {
			indep = provider.IndependentTools()
		}
		var filtered []tools.Tool
		if provider, ok := phaseState.(filteredIndependentToolsProvider); ok {
			filtered = provider.FilteredIndependentTools()
		}
		indepNames := toolNameSet(indep)
		filteredNames := toolNameSet(filtered)

		googleAdmittedSet := map[string]struct{}{}
		if sample, ok := diagnostic["google_admitted_sample"].([]string); ok {
			for _, name := range sample {
				googleAdmittedSet[name] = struct{}{}
			}
		}
		// If the registry diagnostic ran, recompute the FULL google set.
		if registry != nil && hasSnapshot {
			tpm2, _ := snapshotter.ComputeMCPGateCredSnapshot()
			googleAdmittedSet = map[string]struct{}{}
			if provider, ok := registry.(mcpServerToolsProvider); ok {
				for _, names := range provider.MCPServerTools() {
					for _, name := range names {
						if tpm2[name] == "google" {
							googleAdmittedSet[name] = struct{}{}
						}
					}
				}
			}
		}

		var allowlistTotal any
		var googleInAllowlist any
		if !allowlistIsNone {
			allowlistTotal = len(allowed)
			allowedSet := make(map[string]struct{}, len(allowed))
			for _, name := range allowed {
				allowedSet[name] = struct{}{}
			}
			googleInAllowlist = intersectionSize(googleAdmittedSet, allowedSet)
		}

		diagnostic["phase_state"] = map[string]any{
			"allowlist_total":             allowlistTotal,
			"allowlist_is_none":           allowlistIsNone,
			"mcp_tool_names_all_total":    len(mcpNamesPS),
			"google_in_mcp_names_all":     intersectionSize(googleAdmittedSet, mcpNamesPS),
			"independent_tools_total":     len(indep),
			"google_in_independent_tools": intersectionSize(googleAdmittedSet, indepNames),
			"filtered_independent_total":  len(filtered),
			"google_in_filtered":          intersectionSize(googleAdmittedSet, filteredNames),
			"google_admitted_total":       len(googleAdmittedSet),
			"google_in_allowlist":         googleInAllowlist,
		}
	}()
	return diagnostic
}

func safeWorkersView(session *Session) []map[string]any {
	workersView := []map[string]any{}
	func() {
		defer func() {
			if recover() != nil {
				workersView = []map[string]any{}
			}
		}()
		provider, ok := session.Colony.(workerToolTierProvider)
		if !ok {
			return
		}
		for _, worker := range provider.WorkerToolTiering() {
			if !worker.Enabled {
				workersView = append(workersView, map[string]any{
					"worker_id": worker.WorkerID,
					"tiering": map[string]any{
						"enabled":     false,
						"eager_total": worker.EagerTotal,
					},
				})
				continue
			}
			loaded := worker.LoadedViaSearch
			if loaded == nil {
				loaded = []string{}
			}
			workersView = append(workersView, map[string]any{
				"worker_id": worker.WorkerID,
				"tiering": map[string]any{
					"enabled":           true,
					"eager_total":       worker.EagerTotal,
					"searchable_total":  worker.SearchableTotal,
					"loaded_via_search": loaded,
				},
			})
		}
	}()
	return workersView
}

func sortedStrings(values []string) []string {
	// A nil input still renders as an empty JSON array, matching Python's
	// sorted(set()) — proto3 JSON folds null and unset together, so the
	// diagnostic must not answer null here.
	out := make([]string, len(values))
	copy(out, values)
	sort.Strings(out)
	return out
}

func firstN(values []string, n int) []string {
	if len(values) > n {
		return values[:n]
	}
	return values
}

func toolNameSet(list []tools.Tool) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, tool := range list {
		out[tool.Name] = struct{}{}
	}
	return out
}

func intersectionSize(left, right map[string]struct{}) int {
	count := 0
	for name := range left {
		if _, ok := right[name]; ok {
			count++
		}
	}
	return count
}
