package browserbridge

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ContextEntry is one row of the upstream GET /contexts payload.
type ContextEntry struct {
	Profile      string           `json:"profile"`
	GroupID      *int             `json:"groupId"`
	ActiveTab    *int             `json:"activeTab"`
	Name         string           `json:"name"`
	Tabs         []map[string]any `json:"tabs"`
	LastActiveMS *int64           `json:"last_active_ms"`
	Dormant      bool             `json:"dormant"`
	Blockers     []Blocker        `json:"blockers"`
	Status       string           `json:"status"`
}

const (
	waitingWindowMS = 30_000
	dormantAfterMS  = 6 * 60 * 60 * 1000
)

// CreateContext mints a tab group + seed tab in one Chrome profile and
// registers the profile→group mapping. Routing is resolved before anything is
// minted, so a bad/ambiguous browser profile fails fast in the right browser.
func (b *Bridge) CreateContext(ctx context.Context, agentID, displayName, browserProfile string) (map[string]any, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, badRequest("missing required parameter 'agent_id'")
	}
	if strings.TrimSpace(displayName) == "" {
		displayName = agentID
	}
	conn, err := b.resolve(browserProfile, nil)
	if err != nil {
		return nil, err
	}
	label := conn.Label()

	params := map[string]any{"agentId": agentID, "displayName": displayName}
	_, _, _, protocol := conn.identity()
	if recycle := b.firstPersistedGroup(); recycle > 0 && protocol >= 5 {
		params["recycleGroupId"] = recycle
	}

	// Window affinity: serialize per connection so a concurrent first batch
	// establishes one anchor window instead of scattering across windows.
	lock := b.windowLock(label)
	lock.Lock()
	if win := b.hiveWindow(label); win > 0 {
		params["windowId"] = win
	}
	raw, err := conn.Call(ctx, "context.create", params, b.cfg.CommandTimeout)
	lock.Unlock()
	if err != nil {
		return nil, err
	}
	result, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	if win, ok := toInt(result["windowId"]); ok && win > 0 {
		b.setHiveWindow(label, win)
	}
	groupID, _ := toInt(result["groupId"])
	if groupID <= 0 {
		return nil, newError(CodeCreateFailed, "context.create returned no groupId", true)
	}
	b.discardPersistedGroup(groupID)
	b.forgetOrphan(groupID)
	b.contexts.Upsert(ContextMeta{
		Profile:        agentID,
		GroupID:        groupID,
		Name:           displayName,
		BrowserProfile: label,
	})
	tabID, _ := toInt(result["tabId"])
	if tabID > 0 {
		b.rememberTab(tabID, label)
		b.setTabOwner(tabID, agentID)
		b.addHiveTab(tabID)
		b.setContextActiveTab(agentID, tabID)
	}
	result["browser_profile"] = label
	b.logContextEvent("create", agentID, map[string]any{"group_id": groupID, "tab_id": tabID, "browser_profile": label})
	return result, nil
}

// RegisterContext upserts an existing profile→group mapping (idempotent; no
// connection required — a reconnecting gcu may re-publish before hello).
func (b *Bridge) RegisterContext(profile string, groupID int, name, browserProfile string) map[string]any {
	profile = strings.TrimSpace(profile)
	if profile == "" || groupID <= 0 {
		return map[string]any{"ok": false, "error": "profile and positive group_id are required"}
	}
	if strings.TrimSpace(name) == "" {
		name = profile
	}
	if strings.TrimSpace(browserProfile) == "" {
		browserProfile = "default"
	}
	b.contexts.Upsert(ContextMeta{
		Profile:        profile,
		GroupID:        groupID,
		Name:           name,
		BrowserProfile: browserProfile,
	})
	b.logContextEvent("register", profile, map[string]any{"group_id": groupID, "browser_profile": browserProfile})
	return map[string]any{"ok": true}
}

// DestroyContext closes every tab in the group and removes the registry entry.
func (b *Bridge) DestroyContext(ctx context.Context, groupID int, browserProfile string) (map[string]any, error) {
	if groupID <= 0 {
		return nil, badRequest("'group_id' must be a positive integer")
	}
	meta, _ := b.contexts.ByGroup(groupID)
	label := strings.TrimSpace(browserProfile)
	if label == "" || label == "default" {
		label = meta.BrowserProfile
	}
	conn, err := b.resolve(label, nil)
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "context.destroy", map[string]any{"groupId": groupID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	result, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	if persisted, _ := result["persistedGroup"].(bool); persisted {
		b.addPersistedGroup(groupID)
	} else {
		b.discardPersistedGroup(groupID)
	}
	b.forgetOrphan(groupID)
	if meta.Profile != "" {
		tabs := b.ownedTabs(meta.Profile)
		b.forgetRoutingForTabs(tabs)
	}
	touched := b.pruneGroup(groupID, false)
	b.logContextEvent("destroy", meta.Profile, map[string]any{"group_id": groupID, "closed_tabs": result["closedTabs"], "profiles": touched})
	return result, nil
}

// pruneGroup detaches registry entries pointing at groupID and cleans the
// per-tab attribution/status bookkeeping that belonged to them.
func (b *Bridge) pruneGroup(groupID int, soft bool) int {
	profiles := b.contexts.PruneGroup(groupID, soft)
	for _, profile := range profiles {
		tabs := b.clearTabOwnerIfProfile(profile)
		b.forgetRoutingForTabs(tabs)
		b.stateMu.Lock()
		delete(b.contextInFlight, profile)
		b.stateMu.Unlock()
	}
	return len(profiles)
}

func (b *Bridge) forgetRoutingForTabs(tabs []int) {
	if len(tabs) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tab := range tabs {
		if label, ok := b.tabs[tab]; ok {
			delete(b.tabs, tab)
			if b.counts[label] > 0 {
				b.counts[label]--
			}
		}
	}
}

// ListContexts enumerates the registry with best-effort live tab info.
func (b *Bridge) ListContexts(ctx context.Context) []ContextEntry {
	metas := b.contexts.List()
	out := make([]ContextEntry, 0, len(metas))
	now := time.Now().UnixMilli()
	for _, meta := range metas {
		entry := ContextEntry{
			Profile:  meta.Profile,
			Name:     meta.Name,
			Tabs:     []map[string]any{},
			Blockers: []Blocker{},
		}
		groupID := meta.GroupID
		if groupID > 0 {
			gid := groupID
			entry.GroupID = &gid
		}
		if groupID > 0 && b.IsConnected() {
			if tabs := b.listTabsForGroup(ctx, groupID); tabs != nil {
				entry.Tabs = tabs
			}
		}
		tabIDs := make(map[int]struct{}, len(entry.Tabs))
		var active *int
		for _, tab := range entry.Tabs {
			id, _ := toInt(tab["id"])
			if id <= 0 {
				continue
			}
			tabIDs[id] = struct{}{}
			if isActive, _ := tab["active"].(bool); isActive && active == nil {
				active = &id
			}
		}
		if active == nil {
			if id := b.contextActiveTabFor(meta.Profile); id > 0 {
				if _, ok := tabIDs[id]; ok {
					active = &id
				}
			}
		}
		var lastActive *int64
		var lastActiveTab int
		for id := range tabIDs {
			ts := b.tabActive(id)
			if ts > 0 && (lastActive == nil || ts > *lastActive) {
				v := ts
				lastActive = &v
				lastActiveTab = id
			}
		}
		if active == nil && lastActiveTab > 0 {
			id := lastActiveTab
			active = &id
		}
		entry.ActiveTab = active
		entry.LastActiveMS = lastActive

		effective := int64(0)
		if lastActive != nil {
			effective = *lastActive
		}
		if meta.RegisteredAtMS > effective {
			effective = meta.RegisteredAtMS
		}
		entry.Dormant = effective > 0 && now-effective > dormantAfterMS

		seenKinds := map[string]struct{}{}
		for id := range tabIDs {
			for _, blk := range b.tabBlockersFor(id) {
				if blk.Kind == "" {
					continue
				}
				if _, dup := seenKinds[blk.Kind]; dup {
					continue
				}
				seenKinds[blk.Kind] = struct{}{}
				entry.Blockers = append(entry.Blockers, blk)
			}
		}
		entry.Status = b.classifyContextStatus(meta.Profile, entry.Blockers, lastActive)
		out = append(out, entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out
}

func (b *Bridge) listTabsForGroup(ctx context.Context, groupID int) []map[string]any {
	conn, err := b.resolveForGroup(groupID, "")
	if err != nil {
		return nil
	}
	raw, err := conn.Call(ctx, "tab.list", map[string]any{"groupId": groupID}, 2*time.Second)
	if err != nil {
		return nil
	}
	res, err := resultMap(raw)
	if err != nil {
		return nil
	}
	tabs, _ := res["tabs"].([]any)
	out := make([]map[string]any, 0, len(tabs))
	for _, row := range tabs {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (b *Bridge) classifyContextStatus(profile string, blockers []Blocker, lastActiveMS *int64) string {
	for _, blk := range blockers {
		if blk.Severity == "block" {
			return "blocked"
		}
	}
	if b.contextWorking(profile) {
		return "working"
	}
	if lastActiveMS != nil {
		if time.Now().UnixMilli()-*lastActiveMS < waitingWindowMS {
			return "waiting"
		}
	}
	return "idle"
}

func (b *Bridge) logContextEvent(event, profile string, fields map[string]any) {
	logger, ok := b.telemetry.(EventLogger)
	if !ok {
		return
	}
	entry := map[string]any{
		"type":    "context_event",
		"event":   event,
		"profile": profile,
	}
	for k, v := range fields {
		entry[k] = v
	}
	logger.LogEvent(entry)
}
