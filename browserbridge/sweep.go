package browserbridge

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// sweep.go ports the stale-context sweep and its two orphan reapers.
//
// The event-driven path (tab_group_event removed → soft prune) is the fast
// path. This loop is the fallback for events lost during a disconnect or an
// extension reload, and the only path that closes Hive groups/tabs orphaned by
// a crashed runtime.

const hiveGroupMarker = "\u200b" // zero-width space, matching extension background.js

func (b *Bridge) sweepLoop() {
	defer close(b.sweepDone)
	ticker := time.NewTicker(b.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.sweepStop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			b.sweepOnce(ctx)
			cancel()
		}
	}
}

func (b *Bridge) sweepOnce(ctx context.Context) {
	b.mu.RLock()
	conns := make(map[string]*Connection, len(b.conns))
	for label, conn := range b.conns {
		conns[label] = conn
	}
	b.mu.RUnlock()
	for label, conn := range conns {
		_, _, _, protocol := conn.identity()
		if protocol < 3 {
			continue
		}
		b.sweepProfile(ctx, label, conn, protocol)
	}
}

func (b *Bridge) sweepProfile(ctx context.Context, label string, conn *Connection, protocol int) {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	raw, err := conn.Call(callCtx, "tabGroup.list", nil, 2*time.Second)
	cancel()
	if err != nil {
		b.cfg.Logf("stale-context sweep skipped for label=%s: %v", label, err)
		return
	}
	groups := parseGroups(raw)
	live := make(map[int]struct{}, len(groups))
	hiveGroups := 0
	for _, g := range groups {
		id, ok := toInt(g["id"])
		if !ok || id <= 0 {
			continue
		}
		live[id] = struct{}{}
		if title, _ := g["title"].(string); strings.Contains(title, hiveGroupMarker) {
			hiveGroups++
		}
	}

	metas := b.contexts.List()
	for _, meta := range metas {
		if meta.BrowserProfile != label || meta.GroupID <= 0 {
			continue
		}
		if _, ok := live[meta.GroupID]; ok {
			continue
		}
		b.pruneGroup(meta.GroupID, true)
		b.cfg.Logf("stale-context sweep soft-pruned profile=%s groupId=%d label=%s", meta.Profile, meta.GroupID, label)
	}

	owned := b.persistedGroupIDs()
	for _, meta := range metas {
		if meta.GroupID > 0 && meta.BrowserProfile == label {
			owned[meta.GroupID] = struct{}{}
		}
	}
	current := map[int]struct{}{}
	for _, g := range groups {
		id, ok := toInt(g["id"])
		if !ok || id <= 0 {
			continue
		}
		if _, isOwned := owned[id]; isOwned {
			continue
		}
		title, _ := g["title"].(string)
		if !strings.Contains(title, hiveGroupMarker) {
			continue
		}
		current[id] = struct{}{}
	}
	b.resetOrphansExcept(current)
	orphansPending, orphansReaped := 0, 0
	for gid := range current {
		orphansPending++
		if b.bumpOrphanSeen(gid) < 2 {
			continue
		}
		if _, err := b.DestroyContext(ctx, gid, label); err != nil {
			b.cfg.Logf("forward orphan reap: destroy_context(%d) failed: %v", gid, err)
		} else {
			orphansReaped++
		}
		b.forgetOrphan(gid)
	}

	ungroupedTotal, ungroupedCandidates, ungroupedReaped := 0, 0, 0
	if protocol >= 6 {
		ungroupedTotal, ungroupedCandidates, ungroupedReaped = b.reapUngrouped(ctx, label, conn)
	}

	b.cfg.Logf("browser-sweep label=%s live_groups=%d hive_groups=%d orphan_groups_pending=%d orphan_groups_reaped=%d ungrouped_total=%d ungrouped_candidates=%d ungrouped_reaped=%d tracked_hive_tabs=%d",
		label, len(live), hiveGroups, orphansPending, orphansReaped,
		ungroupedTotal, ungroupedCandidates, ungroupedReaped, b.hiveTabCount())
}

func (b *Bridge) reapUngrouped(ctx context.Context, label string, conn *Connection) (total, candidates, reaped int) {
	raw, err := conn.Call(ctx, "tab.listUngrouped", nil, 2*time.Second)
	if err != nil {
		return 0, 0, 0
	}
	res, err := resultMap(raw)
	if err != nil {
		return 0, 0, 0
	}
	tabs, _ := res["tabs"].([]any)
	ungrouped := make(map[int]struct{}, len(tabs))
	for _, row := range tabs {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if id, ok := toInt(m["id"]); ok && id > 0 {
			ungrouped[id] = struct{}{}
		}
	}
	pending := map[int]struct{}{}
	for _, tab := range b.hiveTabsUnion(ungrouped) {
		if b.tabOwnerOf(tab) != "" {
			continue
		}
		pending[tab] = struct{}{}
	}
	b.resetUngroupedExcept(pending)
	for tab := range pending {
		candidates++
		if b.bumpUngroupedSeen(tab) < 2 {
			continue
		}
		if _, err := conn.Call(ctx, "tab.close", map[string]any{"tabId": tab}, 5*time.Second); err != nil {
			b.cfg.Logf("ungrouped orphan reap: close_tab(%d) failed: %v", tab, err)
		} else {
			reaped++
		}
		b.forgetUngrouped(tab)
		b.discardHiveTab(tab)
		b.forgetTab(tab)
	}
	return len(ungrouped), candidates, reaped
}

func parseGroups(raw json.RawMessage) []map[string]any {
	res, err := resultMap(raw)
	if err != nil {
		return nil
	}
	rows, _ := res["groups"].([]any)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
