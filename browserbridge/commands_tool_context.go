package browserbridge

import (
	"context"
	"strconv"
	"strings"
)

// commands_tool_context.go — the agent-profile context view the L3 tool
// handlers share: the active tab (upstream ``ctx["activeTabId"]``), lazy
// context creation (upstream ``lifecycle._ensure_context``) and the group tab
// summary (upstream ``lifecycle.summarize_group_tabs``).
//
// Upstream keeps this state in the tool layer (``_contexts`` in
// tools/tabs.py / navigation.py / lifecycle.py). The Go port keeps the same
// registry in the bridge so the CLI, the desktop panel and the in-process
// browser tools all read one source of truth.

// tabNudgeThreshold mirrors lifecycle._TAB_NUDGE_THRESHOLD: above this many
// tabs in one group the result carries a close-tabs hint.
const tabNudgeThreshold = 3

// tabOverflowHint builds the upstream “tabsHint“ payload. It returns "" below
// the threshold so callers can add it unconditionally.
func tabOverflowHint(count int) string {
	if count <= tabNudgeThreshold {
		return ""
	}
	return "You have " + strconv.Itoa(count) + " tabs open in this group. Close tabs you no " +
		"longer need with browser_close(tab_id=...) before opening or " +
		"navigating more — too many open tabs slow the browser, consume " +
		"memory, and have empirically caused workers to fail their task."
}

// toolProfile normalizes the agent profile: upstream `_resolve_profile` falls
// back to "default" when neither the call nor the session names one.
func toolProfile(profile string) string {
	if trimmed := strings.TrimSpace(profile); trimmed != "" {
		return trimmed
	}
	return "default"
}

// toolContext returns the registered context for a tool call. The agent
// profile (command envelope) is the registry key upstream uses; a caller that
// only names a Chrome profile label is served by the same lookup so mixed
// CLI/tool traffic keeps one context.
func (b *Bridge) toolContext(profile string, params map[string]any) (string, ContextMeta, bool) {
	for _, candidate := range []string{strings.TrimSpace(profile), routeProfile(profile, params)} {
		if candidate == "" {
			continue
		}
		if meta, ok := b.contexts.Get(candidate); ok {
			return candidate, meta, true
		}
	}
	return "", ContextMeta{}, false
}

// toolActiveTabID is the upstream “ctx["activeTabId"]“: the tracked active
// tab for the call's context, or nil when no context/active tab is known.
func (b *Bridge) toolActiveTabID(profile string, params map[string]any) any {
	name, _, ok := b.toolContext(profile, params)
	if !ok {
		return nil
	}
	if id := b.contextActiveTabFor(name); id > 0 {
		return id
	}
	return nil
}

// ensureToolContext ports “lifecycle._ensure_context“: the first URL-taking
// call for a profile lazily mints the tab group + seed tab, so browser_open /
// browser_navigate are the agent's cold-start entry point and later tab-scoped
// calls can omit tab_id. Returns the context key, its meta and the seed tab id
// that was just created (0 when the context already existed).
//
// Unlike upstream this is best-effort: an extension that cannot mint a context
// (or a transport hiccup) keeps the legacy behaviour — tab-scoped calls then
// need an explicit tab_id — instead of failing the open outright.
func (b *Bridge) ensureToolContext(ctx context.Context, profile string, params map[string]any) (string, ContextMeta, int, bool) {
	if name, meta, ok := b.toolContext(profile, params); ok {
		return name, meta, 0, true
	}
	agentID := toolProfile(profile)
	displayName, _ := paramString(params, "profile_display_name")
	browserProfile, _ := paramString(params, "browser_profile")
	result, err := b.CreateContext(ctx, agentID, displayName, browserProfile)
	if err != nil {
		b.logf("tool context ensure failed for %q: %v", agentID, err)
		return "", ContextMeta{}, 0, false
	}
	meta, ok := b.contexts.Get(agentID)
	if !ok {
		return "", ContextMeta{}, 0, false
	}
	seedTab := 0
	if tabID, valid := toInt(result["tabId"]); valid && tabID > 0 {
		seedTab = tabID
	}
	return agentID, meta, seedTab, true
}

// groupSummary ports “lifecycle.summarize_group_tabs“: “{groupTabCount,
// groupTabs:[{id,url,title,active}][, tabsHint]}“ for the call's context.
// Best-effort: nil when there is no context or the tab listing fails, so the
// primary result is never blocked by a listing hiccup.
func (b *Bridge) groupSummary(ctx context.Context, contextName string, meta ContextMeta) map[string]any {
	if contextName == "" || meta.GroupID <= 0 {
		return nil
	}
	result, err := cmdTabsList(ctx, b, meta.BrowserProfile, map[string]any{"group_id": meta.GroupID})
	if err != nil {
		return nil
	}
	m, _ := result.(map[string]any)
	tabs, _ := m["tabs"].([]any)
	active := b.contextActiveTabFor(contextName)
	entries := make([]any, 0, len(tabs))
	for _, item := range tabs {
		tab, _ := item.(map[string]any)
		id, ok := toInt(tab["id"])
		if !ok || id <= 0 {
			id, _ = toInt(tab["tabId"])
		}
		entries = append(entries, map[string]any{
			"id":     id,
			"url":    tab["url"],
			"title":  tab["title"],
			"active": id > 0 && id == active,
		})
	}
	summary := map[string]any{
		"groupTabCount": len(tabs),
		"groupTabs":     entries,
	}
	if hint := tabOverflowHint(len(tabs)); hint != "" {
		summary["tabsHint"] = hint
	}
	return summary
}

// closeActiveTabFollowup ports the active-tab repair browser_close performs
// after closing the tab that was active: the first tab left in the group
// becomes active (nil when the group is empty).
func (b *Bridge) closeActiveTabFollowup(ctx context.Context, contextName string, meta ContextMeta, closedTabID int) {
	if contextName == "" || b.contextActiveTabFor(contextName) != closedTabID {
		return
	}
	result, err := cmdTabsList(ctx, b, meta.BrowserProfile, map[string]any{"group_id": meta.GroupID})
	if err != nil {
		b.setContextActiveTab(contextName, 0)
		return
	}
	m, _ := result.(map[string]any)
	tabs, _ := m["tabs"].([]any)
	next := 0
	for _, item := range tabs {
		tab, _ := item.(map[string]any)
		if id, ok := toInt(tab["id"]); ok && id > 0 {
			next = id
			break
		}
	}
	b.setContextActiveTab(contextName, next)
}
