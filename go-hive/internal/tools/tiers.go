package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func FirstLine(text string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 160
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) <= maxLen {
			return line
		}
		return strings.TrimRight(string(runes[:maxLen-1]), " \t\r\n") + "…"
	}
	return ""
}

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

func tokenizeQuery(q string) []string {
	parts := nonAlnumRe.Split(strings.ToLower(q), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func MatchSearchableTools(query string, searchable []Tool, limit int) []string {
	if limit == 0 {
		limit = 5
	}
	byName := make(map[string]Tool, len(searchable))
	for _, t := range searchable {
		byName[t.Name] = t
	}
	q := strings.TrimSpace(query)
	if strings.HasPrefix(strings.ToLower(q), "select:") {
		var wanted []string
		for _, n := range strings.Split(q[len("select:"):], ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				wanted = append(wanted, n)
			}
		}
		seen := map[string]struct{}{}
		out := make([]string, 0, len(wanted))
		for _, n := range wanted {
			if _, found := byName[n]; !found {
				continue
			}
			if _, dup := seen[n]; dup {
				continue
			}
			seen[n] = struct{}{}
			out = append(out, n)
		}
		return out
	}

	tokens := tokenizeQuery(q)
	if len(tokens) == 0 {
		return []string{}
	}
	type scored struct {
		score int
		name  string
	}
	var hits []scored
	for _, t := range searchable {
		haystack := strings.ToLower(t.Name + " " + t.Description)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(haystack, tok) {
				score++
			}
		}
		if score > 0 {
			hits = append(hits, scored{score: score, name: t.Name})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].name < hits[j].name
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

func MatchNames(query string, names map[string]struct{}) []string {
	q := strings.TrimSpace(query)
	if q == "" || len(names) == 0 {
		return []string{}
	}
	if strings.HasPrefix(strings.ToLower(q), "select:") {
		var out []string
		for _, n := range strings.Split(q[len("select:"):], ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			if _, ok := names[n]; ok {
				out = append(out, n)
			}
		}
		return out
	}
	tokens := tokenizeQuery(q)
	if len(tokens) == 0 {
		return []string{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var out []string
	for _, n := range sorted {
		lower := strings.ToLower(n)
		for _, tok := range tokens {
			if strings.Contains(lower, tok) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

type ToolTierState struct {
	Pool               []Tool
	AlwaysEnabledNames map[string]struct{}
	GateableNames      map[string]struct{}
	EnabledAllowlist   []string
	LoadedToolNames    []string
	PersistPath        string

	filteredPool []Tool
	eagerPool    []Tool
	rebuilt      bool
	memo         []Tool
}

func NewToolTierState() *ToolTierState {
	return &ToolTierState{
		AlwaysEnabledNames: map[string]struct{}{},
		GateableNames:      map[string]struct{}{},
	}
}

func (s *ToolTierState) PassesAllowlist(name string) bool {
	if _, ok := s.AlwaysEnabledNames[name]; ok {
		return true
	}
	if _, gateable := s.GateableNames[name]; !gateable {
		return true
	}
	if s.EnabledAllowlist == nil {
		return true
	}
	for _, n := range s.EnabledAllowlist {
		if n == name {
			return true
		}
	}
	return false
}

func (s *ToolTierState) IsEager(name string) bool {
	if len(s.AlwaysEnabledNames) == 0 {
		return true
	}
	if _, ok := s.AlwaysEnabledNames[name]; ok {
		return true
	}
	if _, gateable := s.GateableNames[name]; !gateable {
		return true
	}
	for _, n := range s.LoadedToolNames {
		if n == name {
			return true
		}
	}
	return false
}

func (s *ToolTierState) Rebuild() {
	s.filteredPool = make([]Tool, 0, len(s.Pool))
	for _, t := range s.Pool {
		if s.PassesAllowlist(t.Name) {
			s.filteredPool = append(s.filteredPool, t)
		}
	}
	s.eagerPool = make([]Tool, 0, len(s.filteredPool))
	for _, t := range s.filteredPool {
		if s.IsEager(t.Name) {
			s.eagerPool = append(s.eagerPool, t)
		}
	}
	s.memo = s.eagerPool
	s.rebuilt = true
}

func (s *ToolTierState) GetCurrentTools() []Tool {
	if !s.rebuilt && len(s.Pool) > 0 {
		s.Rebuild()
	}
	return s.eagerPool
}

func (s *ToolTierState) CurrentToolNames() []string {
	current := s.GetCurrentTools()
	out := make([]string, 0, len(current))
	for _, t := range current {
		out = append(out, t.Name)
	}
	return out
}

func (s *ToolTierState) GetSearchableTools() []Tool {
	if !s.rebuilt && len(s.Pool) > 0 {
		s.Rebuild()
	}
	out := make([]Tool, 0)
	for _, t := range s.filteredPool {
		if !s.IsEager(t.Name) {
			out = append(out, t)
		}
	}
	return out
}

func (s *ToolTierState) SearchableNames() map[string]struct{} {
	out := map[string]struct{}{}
	for _, t := range s.GetSearchableTools() {
		out[t.Name] = struct{}{}
	}
	return out
}

func (s *ToolTierState) UnregisteredAllowlistedNames() map[string]struct{} {
	out := map[string]struct{}{}
	if len(s.EnabledAllowlist) == 0 {
		return out
	}
	for _, n := range s.EnabledAllowlist {
		if _, ok := s.GateableNames[n]; !ok {
			out[n] = struct{}{}
		}
	}
	return out
}

func (s *ToolTierState) ToolInventory() map[string][]string {
	loaded := make([]string, 0)
	for _, t := range s.GetCurrentTools() {
		loaded = append(loaded, t.Name)
	}
	searchable := make([]string, 0)
	for _, t := range s.GetSearchableTools() {
		searchable = append(searchable, t.Name)
	}
	disabled := make([]string, 0)
	for _, t := range s.Pool {
		if !s.PassesAllowlist(t.Name) {
			disabled = append(disabled, t.Name)
		}
	}
	unavailable := make([]string, 0)
	for n := range s.UnregisteredAllowlistedNames() {
		unavailable = append(unavailable, n)
	}
	sort.Strings(loaded)
	sort.Strings(searchable)
	sort.Strings(disabled)
	sort.Strings(unavailable)
	return map[string][]string{
		"loaded":      loaded,
		"searchable":  searchable,
		"disabled":    disabled,
		"unavailable": unavailable,
	}
}

func (s *ToolTierState) PromoteSearchedTools(names []string) []string {
	newly := []string{}
	for _, name := range names {
		if !containsString(s.LoadedToolNames, name) {
			s.LoadedToolNames = append(s.LoadedToolNames, name)
			newly = append(newly, name)
		}
	}
	if len(newly) > 0 {
		s.PersistLoadedTools()
		s.Rebuild()
	}
	return newly
}

func (s *ToolTierState) RestoreLoadedTools(persisted []string, registeredNames map[string]struct{}) {
	s.LoadedToolNames = nil
	for _, n := range persisted {
		if _, ok := registeredNames[n]; ok && s.PassesAllowlist(n) {
			s.LoadedToolNames = append(s.LoadedToolNames, n)
		}
	}
}

func (s *ToolTierState) PersistLoadedTools() {
	if s.PersistPath == "" {
		return
	}
	existing := map[string]any{}
	if data, err := os.ReadFile(s.PersistPath); err == nil {
		var parsed map[string]any
		if json.Unmarshal(data, &parsed) == nil && parsed != nil {
			existing = parsed
		}
	}
	existing["loaded_tools"] = append([]string(nil), s.LoadedToolNames...)
	if err := os.MkdirAll(filepath.Dir(s.PersistPath), 0o755); err != nil {
		return
	}
	data, err := json.Marshal(existing)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.PersistPath, data, 0o644)
}

func (s *ToolTierState) LoadPersistedTools() []string {
	if s.PersistPath == "" {
		return nil
	}
	data, err := os.ReadFile(s.PersistPath)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil
	}
	raw, ok := parsed["loaded_tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

type SearchToolsProvider interface {
	GetSearchableTools() []Tool
	GetCurrentTools() []Tool
	UnregisteredAllowlistedNames() map[string]struct{}
	PromoteSearchedTools(names []string) []string
	ToolInventory() map[string][]string
}

const inventoryNote = "Current session configuration only. Loaded tools are callable; searchable tools need loading; " +
	"disabled tools are excluded by the allowlist; unavailable tools are configured but not registered. " +
	"This does not verify credentials or service connectivity."

const searchToolsLoadedNote = "Loaded. These tools are callable from your next step and stay loaded for the rest of this session."

const searchToolsDescription = "Loads tool schemas so you can call them. Tools in the " +
	"<searchable_tools> block of your system prompt are listed by name only — " +
	"their schemas are not loaded, so they cannot be called until you load them " +
	"here. Once loaded, a tool is callable on your next step exactly like an " +
	"always-on tool, and stays loaded for the rest of the session.\n\n" +
	"Query forms:\n" +
	"- \"inventory\" — report loaded, searchable, disabled, and unavailable tools without loading anything. " +
	"Use before describing your capabilities.\n" +
	"- \"select:name_a,name_b\" — load these exact tools by name (no limit). " +
	"Preferred: the <searchable_tools> list already gives you the names, so pass " +
	"all the ones this task needs in a single call.\n" +
	"- \"send gmail email\" — keyword search; loads up to max_results best matches."

func BuildSearchTools(provider SearchToolsProvider) (Tool, func(inputs map[string]any) (string, error)) {
	handler := func(inputs map[string]any) (string, error) {
		query, _ := inputs["query"].(string)
		maxResults := 5
		if v, ok := inputs["max_results"]; ok && v != nil {
			if n, ok := intFromAny(v); ok {
				maxResults = n
			}
		}
		return RunSearchTools(provider, query, maxResults)
	}

	tool := Tool{
		Name:        "search_tools",
		Description: searchToolsDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "\"select:name_a,name_b\" to load exact tools by name, or keywords to search.",
				},
				"max_results": map[string]any{
					"type":        "number",
					"description": "Max tools a keyword search loads (default 5). Ignored by select:, which loads every name you pass.",
					"default":     5,
				},
			},
			"required": []any{"query"},
		},
	}
	return tool, handler
}

func RunSearchTools(provider SearchToolsProvider, query string, maxResults int) (string, error) {
	if strings.ToLower(strings.TrimSpace(query)) == "inventory" {
		out := map[string]any{"note": inventoryNote}
		for k, v := range provider.ToolInventory() {
			out[k] = v
		}
		return marshalJSON(out)
	}

	searchable := provider.GetSearchableTools()
	unavailable := MatchNames(query, provider.UnregisteredAllowlistedNames())

	currentNames := map[string]struct{}{}
	for _, t := range provider.GetCurrentTools() {
		currentNames[t.Name] = struct{}{}
	}
	alreadyLoaded := MatchNames(query, currentNames)
	alreadyLoadedNote := strings.Join(alreadyLoaded, ", ") + " is already loaded and callable right now — " +
		"invoke it directly on your next step. No search_tools call is needed."

	if len(searchable) == 0 {
		switch {
		case len(alreadyLoaded) > 0:
			return marshalJSON(map[string]any{
				"loaded":         []string{},
				"already_loaded": alreadyLoaded,
				"note":           alreadyLoadedNote,
			})
		case len(unavailable) > 0:
			return marshalJSON(map[string]any{
				"loaded":      []string{},
				"unavailable": unavailable,
				"note": strings.Join(unavailable, ", ") + " is configured but unavailable: it was not registered " +
					"in this session. Check runtime configuration, credentials, and server status. " +
					"Every other allowed tool is already loaded.",
			})
		default:
			return marshalJSON(map[string]any{
				"loaded": []string{},
				"note":   "Nothing to load — every tool you are allowed to use is already loaded.",
			})
		}
	}

	limit := maxResults
	if limit < 1 {
		limit = 1
	}
	matches := MatchSearchableTools(query, searchable, limit)
	if len(matches) == 0 {
		names := make([]string, 0, len(searchable))
		for _, t := range searchable {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		available := strings.Join(names, ", ")
		switch {
		case len(alreadyLoaded) > 0:
			return marshalJSON(map[string]any{
				"loaded":         []string{},
				"already_loaded": alreadyLoaded,
				"note":           alreadyLoadedNote,
			})
		case len(unavailable) > 0:
			return marshalJSON(map[string]any{
				"loaded":      []string{},
				"unavailable": unavailable,
				"note": strings.Join(unavailable, ", ") + " is configured but unavailable: it was not registered " +
					"in this session. Check runtime configuration, credentials, and server status. " +
					"Other searchable tools: " + available + ".",
			})
		default:
			return marshalJSON(map[string]any{
				"loaded": []string{},
				"note": "No searchable tool matched " + pythonRepr(query) + ". Searchable tools: " + available + ". " +
					"Retry with different keywords or search_tools(query=\"select:exact_name\").",
			})
		}
	}

	byName := make(map[string]Tool, len(searchable))
	for _, t := range searchable {
		byName[t.Name] = t
	}
	loaded := provider.PromoteSearchedTools(matches)
	already := []string{}
	for _, n := range matches {
		if !containsString(loaded, n) {
			already = append(already, n)
		}
	}
	toolsOut := make([]map[string]any, 0, len(matches))
	for _, n := range matches {
		desc := ""
		if t, ok := byName[n]; ok {
			desc = t.Description
		}
		toolsOut = append(toolsOut, map[string]any{
			"name":        n,
			"description": FirstLine(desc, 160),
		})
	}
	return marshalJSON(map[string]any{
		"loaded":         loaded,
		"already_loaded": already,
		"tools":          toolsOut,
		"note":           searchToolsLoadedNote,
	})
}

func marshalJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func intFromAny(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case float32:
		return int(n), true
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i), true
		}
		if f, err := n.Float64(); err == nil {
			return int(f), true
		}
	}
	return 0, false
}
