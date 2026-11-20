package browserbridge

import (
	"sort"
	"sync"
	"time"
)

// TabAction is one entry in the upstream-style per-tab action ring buffer.
type TabAction struct {
	TSMS   int64  `json:"ts_ms"`
	Verb   string `json:"verb"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
}

const tabActionBufferSize = 200

// state.go owns the process-local registries that the upstream bridge keeps
// beside the context map: tab ownership, activity stamps, action history,
// health snapshots/blockers, pending dialogs, orphan debounce counters, the
// durable "was in a Hive group" tab set, and window affinity.
//
// All fields are guarded by stateMu. It is deliberately separate from b.mu
// (connection/tab-count routing) so extension calls and health calls never
// hold the routing lock for their duration.

func (b *Bridge) setTabOwner(tabID int, profile string) {
	if tabID <= 0 {
		return
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if profile == "" {
		delete(b.tabOwner, tabID)
		return
	}
	b.tabOwner[tabID] = profile
}

func (b *Bridge) tabOwnerOf(tabID int) string {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.tabOwner[tabID]
}

func (b *Bridge) clearTabOwnerIfProfile(profile string) []int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	var tabs []int
	for tab, owner := range b.tabOwner {
		if owner == profile {
			delete(b.tabOwner, tab)
			delete(b.contextActiveTab, profile)
			tabs = append(tabs, tab)
		}
	}
	sort.Ints(tabs)
	return tabs
}

func (b *Bridge) ownedTabs(profile string) []int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	var tabs []int
	for tab, owner := range b.tabOwner {
		if owner == profile {
			tabs = append(tabs, tab)
		}
	}
	sort.Ints(tabs)
	return tabs
}

func (b *Bridge) touchTab(tabID int) {
	if tabID <= 0 {
		return
	}
	b.stateMu.Lock()
	b.tabActiveMS[tabID] = time.Now().UnixMilli()
	b.stateMu.Unlock()
}

func (b *Bridge) tabActive(tabID int) int64 {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.tabActiveMS[tabID]
}

func (b *Bridge) forgetTabState(tabID int) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	delete(b.tabActiveMS, tabID)
	delete(b.tabActions, tabID)
	delete(b.hiveTabs, tabID)
}

// recordAction appends to the bounded per-tab ring buffer. Missing/zero tab
// ids are a no-op, matching upstream telemetry.record_action.
func (b *Bridge) recordAction(tabID int, verb, target string, ok bool, tsMS int64) {
	if tabID <= 0 || verb == "" {
		return
	}
	if tsMS == 0 {
		tsMS = time.Now().UnixMilli()
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	buf := b.tabActions[tabID]
	buf = append(buf, TabAction{TSMS: tsMS, Verb: verb, Target: target, OK: ok})
	if len(buf) > tabActionBufferSize {
		buf = buf[len(buf)-tabActionBufferSize:]
	}
	b.tabActions[tabID] = buf
}

// TabActions returns newest-first entries and the latest timestamp. limit=0
// returns only last_action_ts_ms, matching the side panel's probe.
func (b *Bridge) TabActions(tabID int, limit int, sinceMS *int64) map[string]any {
	if limit < 0 {
		limit = 0
	}
	if limit > tabActionBufferSize {
		limit = tabActionBufferSize
	}
	b.stateMu.Lock()
	buf := append([]TabAction(nil), b.tabActions[tabID]...)
	b.stateMu.Unlock()
	if len(buf) == 0 {
		return map[string]any{"actions": []TabAction{}, "last_action_ts_ms": nil}
	}
	last := buf[len(buf)-1].TSMS
	if limit == 0 {
		return map[string]any{"actions": []TabAction{}, "last_action_ts_ms": last}
	}
	out := make([]TabAction, 0, limit)
	for i := len(buf) - 1; i >= 0 && len(out) < limit; i-- {
		if sinceMS != nil && buf[i].TSMS <= *sinceMS {
			break
		}
		out = append(out, buf[i])
	}
	return map[string]any{"actions": out, "last_action_ts_ms": last}
}

func (b *Bridge) setTabSnapshot(tabID int, snapshot map[string]any) {
	b.stateMu.Lock()
	b.tabSnapshots[tabID] = snapshot
	b.stateMu.Unlock()
}

func (b *Bridge) tabSnapshot(tabID int) map[string]any {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.tabSnapshots[tabID]
}

func (b *Bridge) hasTabSnapshot(tabID int) bool {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	_, ok := b.tabSnapshots[tabID]
	return ok
}

func (b *Bridge) clearTabHealth(tabID int) {
	b.stateMu.Lock()
	delete(b.tabSnapshots, tabID)
	delete(b.tabBlockers, tabID)
	b.stateMu.Unlock()
}

func (b *Bridge) setTabBlockers(tabID int, blockers []Blocker) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if len(blockers) == 0 {
		delete(b.tabBlockers, tabID)
		return
	}
	b.tabBlockers[tabID] = append([]Blocker(nil), blockers...)
}

func (b *Bridge) tabBlockersFor(tabID int) []Blocker {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return append([]Blocker(nil), b.tabBlockers[tabID]...)
}

func (b *Bridge) blockersSnapshot() map[int][]Blocker {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	out := make(map[int][]Blocker, len(b.tabBlockers))
	for tab, blockers := range b.tabBlockers {
		out[tab] = append([]Blocker(nil), blockers...)
	}
	return out
}

func (b *Bridge) setContextActiveTab(profile string, tabID int) {
	if profile == "" {
		return
	}
	b.stateMu.Lock()
	b.contextActiveTab[profile] = tabID
	b.stateMu.Unlock()
}

func (b *Bridge) contextActiveTabFor(profile string) int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.contextActiveTab[profile]
}

func (b *Bridge) beginContextCommand(profile string) func() {
	if profile == "" {
		return func() {}
	}
	b.stateMu.Lock()
	b.contextInFlight[profile]++
	b.stateMu.Unlock()
	return func() {
		b.stateMu.Lock()
		if b.contextInFlight[profile] <= 1 {
			delete(b.contextInFlight, profile)
		} else {
			b.contextInFlight[profile]--
		}
		b.stateMu.Unlock()
	}
}

func (b *Bridge) contextWorking(profile string) bool {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.contextInFlight[profile] > 0
}

func (b *Bridge) addHiveTab(tabID int) {
	if tabID <= 0 {
		return
	}
	b.stateMu.Lock()
	b.hiveTabs[tabID] = struct{}{}
	b.stateMu.Unlock()
}

func (b *Bridge) discardHiveTab(tabID int) {
	b.stateMu.Lock()
	delete(b.hiveTabs, tabID)
	b.stateMu.Unlock()
}

func (b *Bridge) hiveTabsUnion(ids map[int]struct{}) []int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	var out []int
	for tab := range b.hiveTabs {
		if _, ok := ids[tab]; ok {
			out = append(out, tab)
		}
	}
	sort.Ints(out)
	return out
}

func (b *Bridge) hiveTabCount() int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return len(b.hiveTabs)
}

func (b *Bridge) bumpOrphanSeen(groupID int) int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	b.orphanSeen[groupID]++
	return b.orphanSeen[groupID]
}

func (b *Bridge) forgetOrphan(groupID int) {
	b.stateMu.Lock()
	delete(b.orphanSeen, groupID)
	b.stateMu.Unlock()
}

func (b *Bridge) resetOrphansExcept(keep map[int]struct{}) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	for gid := range b.orphanSeen {
		if _, ok := keep[gid]; !ok {
			delete(b.orphanSeen, gid)
		}
	}
}

func (b *Bridge) bumpUngroupedSeen(tabID int) int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	b.ungroupedSeen[tabID]++
	return b.ungroupedSeen[tabID]
}

func (b *Bridge) forgetUngrouped(tabID int) {
	b.stateMu.Lock()
	delete(b.ungroupedSeen, tabID)
	b.stateMu.Unlock()
}

func (b *Bridge) resetUngroupedExcept(keep map[int]struct{}) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	for tid := range b.ungroupedSeen {
		if _, ok := keep[tid]; !ok {
			delete(b.ungroupedSeen, tid)
		}
	}
}

func (b *Bridge) addPersistedGroup(groupID int) {
	b.stateMu.Lock()
	b.persistedGroups[groupID] = struct{}{}
	b.stateMu.Unlock()
}

func (b *Bridge) discardPersistedGroup(groupID int) {
	b.stateMu.Lock()
	delete(b.persistedGroups, groupID)
	b.stateMu.Unlock()
}

func (b *Bridge) persistedGroupIDs() map[int]struct{} {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	out := make(map[int]struct{}, len(b.persistedGroups))
	for gid := range b.persistedGroups {
		out[gid] = struct{}{}
	}
	return out
}

func (b *Bridge) firstPersistedGroup() int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	for gid := range b.persistedGroups {
		return gid
	}
	return 0
}

func (b *Bridge) setHiveWindow(label string, windowID int) {
	b.stateMu.Lock()
	b.hiveWindowByConn[label] = windowID
	b.stateMu.Unlock()
}

func (b *Bridge) hiveWindow(label string) int {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	return b.hiveWindowByConn[label]
}

func (b *Bridge) windowLock(label string) *sync.Mutex {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	mu := b.windowLocks[label]
	if mu == nil {
		mu = &sync.Mutex{}
		b.windowLocks[label] = mu
	}
	return mu
}

func (b *Bridge) setPendingDialog(tabID int, dialog map[string]any) {
	b.stateMu.Lock()
	b.pendingDialogs[tabID] = dialog
	b.stateMu.Unlock()
}

func (b *Bridge) clearPendingDialog(tabID int) {
	b.stateMu.Lock()
	delete(b.pendingDialogs, tabID)
	b.stateMu.Unlock()
}

// PendingDialog returns the tracked native dialog for one tab, if any.
func (b *Bridge) PendingDialog(tabID int) map[string]any {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	d := b.pendingDialogs[tabID]
	if d == nil {
		return nil
	}
	out := make(map[string]any, len(d))
	for k, v := range d {
		out[k] = v
	}
	return out
}

// PendingDialogs returns every tracked dialog keyed by tab id.
func (b *Bridge) PendingDialogs() map[int]map[string]any {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	out := make(map[int]map[string]any, len(b.pendingDialogs))
	for tab, d := range b.pendingDialogs {
		cp := make(map[string]any, len(d))
		for k, v := range d {
			cp[k] = v
		}
		out[tab] = cp
	}
	return out
}
