package agentloop

import (
	"sort"
	"strings"
)

var toolSkillFirePoints = []ReminderPoint{
	SESSION_START,
	USER_PROMPT_SUBMIT,
	POST_TOOL_USE,
	POST_COMPACT,
}

const searchableToolsAddedContract = "The following tools are available but their schemas are NOT loaded — calling them directly will fail. Use search_tools with query \"select:<name>[,<name>...]\" to load their schemas before calling them (or pass keywords to search). Loaded tools stay loaded for the rest of the session:\n"

const searchableToolsRemovedContract = "The following tools are no longer available:\n"

type SearchableToolsReminderSource struct {
	announced map[string]struct{}
}

func NewSearchableToolsReminderSource() *SearchableToolsReminderSource {
	return &SearchableToolsReminderSource{announced: map[string]struct{}{}}
}

func (s *SearchableToolsReminderSource) Name() string { return "searchable_tools" }

func (s *SearchableToolsReminderSource) Points() []ReminderPoint {
	return append([]ReminderPoint(nil), toolSkillFirePoints...)
}

func (s *SearchableToolsReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	if agentCtx.IsQueenStream {
		return true
	}
	return agentCtx.SearchableToolsProvider != nil
}

func (s *SearchableToolsReminderSource) Render(rctx ReminderContext) (string, bool) {
	agentCtx := rctx.AgentCtx
	if agentCtx == nil || agentCtx.SearchableToolsProvider == nil {
		return "", false
	}
	if s.announced == nil {
		s.announced = map[string]struct{}{}
	}

	current := make(map[string]struct{})
	for _, tool := range agentCtx.SearchableToolsProvider() {
		if tool.Name != "" {
			current[tool.Name] = struct{}{}
		}
	}

	if rctx.Point == POST_COMPACT {
		s.announced = copyNameSet(current)
		if len(current) == 0 {
			return "", false
		}
		return renderSearchableBody(sortedNames(current), nil), true
	}

	added := difference(current, s.announced)
	departed := difference(s.announced, current)
	loaded := loadedNames(agentCtx)
	removed := make(map[string]struct{})
	for name := range departed {
		if _, isLoaded := loaded[name]; !isLoaded {
			removed[name] = struct{}{}
		}
	}
	s.announced = copyNameSet(current)
	if len(added) == 0 && len(removed) == 0 {
		return "", false
	}
	return renderSearchableBody(sortedNames(added), sortedNames(removed)), true
}

func loadedNames(agentCtx *ReminderAgentContext) map[string]struct{} {
	out := map[string]struct{}{}
	if agentCtx == nil || agentCtx.LoadedToolNamesProvider == nil {
		return out
	}
	for _, name := range agentCtx.LoadedToolNamesProvider() {
		out[name] = struct{}{}
	}
	return out
}

func renderSearchableBody(added, removed []string) string {
	parts := make([]string, 0, 2)
	if len(added) > 0 {
		parts = append(parts, searchableToolsAddedContract+joinLines(added))
	}
	if len(removed) > 0 {
		parts = append(parts, searchableToolsRemovedContract+joinLines(removed))
	}
	return joinBlocks(parts)
}

type SkillsCatalogReminderSource struct {
	lastSig string
	hasSig  bool
}

func NewSkillsCatalogReminderSource() *SkillsCatalogReminderSource {
	return &SkillsCatalogReminderSource{}
}

func (s *SkillsCatalogReminderSource) Name() string { return "skills_catalog" }

func (s *SkillsCatalogReminderSource) Points() []ReminderPoint {
	return append([]ReminderPoint(nil), toolSkillFirePoints...)
}

func (s *SkillsCatalogReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	return agentCtx != nil && agentCtx.IsQueenStream
}

func (s *SkillsCatalogReminderSource) Render(rctx ReminderContext) (string, bool) {
	agentCtx := rctx.AgentCtx
	if agentCtx == nil || agentCtx.QueenSkillsCatalogProvider == nil {
		return "", false
	}
	catalog := strings.TrimSpace(agentCtx.QueenSkillsCatalogProvider())
	if catalog == "" {
		s.lastSig = ""
		s.hasSig = true
		return "", false
	}
	if rctx.Point != POST_COMPACT && s.hasSig && catalog == s.lastSig {
		return "", false
	}
	s.lastSig = catalog
	s.hasSig = true
	return catalog, true
}

func copyNameSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for name := range in {
		out[name] = struct{}{}
	}
	return out
}

func difference(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for name := range a {
		if _, found := b[name]; !found {
			out[name] = struct{}{}
		}
	}
	return out
}

func sortedNames(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func joinLines(items []string) string { return strings.Join(items, "\n") }

func joinBlocks(parts []string) string { return strings.Join(parts, "\n\n") }
