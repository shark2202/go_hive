package browserbridge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// handover.go ports upstream adopt_tab / release_tab (Feature 2 / Step 6).

const minHandoverProtocol = 4

func userTabMessage(title string, tabID int, action string) string {
	safe := strings.TrimSpace(title)
	label := fmt.Sprintf("tab #%d", tabID)
	if safe != "" {
		label = `"` + safe + `" (tab #` + itoa(tabID) + ")"
	}
	if action == "handed-over" {
		return "The user handed " + label + " over to you."
	}
	return "The user detached " + label + " from you."
}

func (b *Bridge) notifyHandover(profile, title string, tabID int, action string) {
	if profile == "" {
		return
	}
	text := userTabMessage(title, tabID, action)
	// Control-RPC push: the runtime's keepalive client subscribes and routes
	// the message into the owning agent's conversation (upstream side-panel
	// adopt/release path).
	b.NotifyClients(profile, text, map[string]any{"tabId": tabID, "action": action})
	if b.cfg.Notify != nil {
		b.cfg.Notify(profile, text, tabID, action)
	}
}

func (b *Bridge) closeSeedTab(ctx context.Context, conn *Connection, tabID int) {
	if tabID <= 0 {
		return
	}
	_, _ = conn.Call(ctx, "tab.close", map[string]any{"tabId": tabID}, 5*time.Second)
}

// AdoptTab moves tabID into profile's group. It refuses to steal a tab owned
// by another agent, lazily re-mints a soft-pruned group under the same display
// name, and reports already_owned without a redundant wire move.
func (b *Bridge) AdoptTab(ctx context.Context, profile string, tabID int, fromUser bool) (map[string]any, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil, badRequest("missing required parameter 'profile'")
	}
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	b.touchTab(tabID)
	meta, ok := b.contexts.Get(profile)
	if !ok {
		return nil, newError(CodeNoSuchProfile,
			fmt.Sprintf("No agent named '%s'. Create one first or pick a different agent.", profile), false)
	}
	ownerLabel := meta.BrowserProfile
	conn, err := b.resolve(ownerLabel, nil)
	if err != nil {
		return nil, err
	}
	ownerLabel = conn.Label()
	if _, _, _, protocol := conn.identity(); protocol < minHandoverProtocol {
		return nil, newError(CodeUnsupportedExt,
			"Tab hand-over requires Hive Browser Bridge v1.5+. Update the Chrome extension and retry.", false)
	}

	groupID := meta.GroupID
	seedTab := 0
	if groupID <= 0 {
		params := map[string]any{"agentId": profile, "displayName": meta.Name}
		if meta.Name == "" {
			params["displayName"] = profile
		}
		if win := b.hiveWindow(ownerLabel); win > 0 {
			params["windowId"] = win
		}
		raw, err := conn.Call(ctx, "context.create", params, b.cfg.CommandTimeout)
		if err != nil {
			return nil, err
		}
		created, err := resultMap(raw)
		if err != nil {
			return nil, err
		}
		newGroup, _ := toInt(created["groupId"])
		if newGroup <= 0 {
			return nil, newError(CodeCreateFailed,
				fmt.Sprintf("Could not mint a fresh tab group for agent '%s'.", profile), true)
		}
		b.contexts.SetGroup(profile, newGroup)
		groupID = newGroup
		seedTab, _ = toInt(created["tabId"])
		if seedTab == tabID {
			seedTab = 0
		}
		if win, ok := toInt(created["windowId"]); ok && win > 0 {
			b.setHiveWindow(ownerLabel, win)
		}
	}

	raw, err := conn.Call(ctx, "tab.get", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	info, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	title := ""
	if fromUser {
		title, _ = info["title"].(string)
	}
	if current, ok := toInt(info["groupId"]); ok && current >= 0 {
		if current == groupID {
			b.setTabOwner(tabID, profile)
			b.setContextActiveTab(profile, tabID)
			b.rememberTab(tabID, ownerLabel)
			b.closeSeedTab(ctx, conn, seedTab)
			b.notifyHandover(profile, title, tabID, "handed-over")
			return map[string]any{"ok": true, "tabId": tabID, "groupId": groupID, "already_owned": true}, nil
		}
		for _, other := range b.contexts.List() {
			if other.Profile != profile && other.GroupID == current {
				return nil, newError(CodeConflict,
					fmt.Sprintf("Tab %d is currently owned by agent '%s'. Release it first, then adopt.", tabID, other.Profile), false)
			}
		}
	}

	raw, err = conn.Call(ctx, "tab.adopt", map[string]any{"tabId": tabID, "groupId": groupID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	adopted, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	b.setTabOwner(tabID, profile)
	b.setContextActiveTab(profile, tabID)
	b.rememberTab(tabID, ownerLabel)
	b.addHiveTab(tabID)
	b.closeSeedTab(ctx, conn, seedTab)
	b.notifyHandover(profile, title, tabID, "handed-over")
	outGroup := groupID
	if g, ok := toInt(adopted["groupId"]); ok && g > 0 {
		outGroup = g
	}
	return map[string]any{"ok": true, "tabId": tabID, "groupId": outGroup}, nil
}

// ReleaseTab removes tabID from whatever group it is in. Idempotent at
// Chrome's level; verifies the ungroup actually took so the side panel does
// not keep showing a Release button for a tab that never left.
func (b *Bridge) ReleaseTab(ctx context.Context, tabID int, fromUser bool) (map[string]any, error) {
	if tabID <= 0 {
		return nil, badRequest("missing required parameter 'tab_id'")
	}
	b.touchTab(tabID)
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	if _, _, _, protocol := conn.identity(); protocol < minHandoverProtocol {
		return nil, newError(CodeUnsupportedExt,
			"Tab release requires Hive Browser Bridge v1.5+. Update the Chrome extension and retry.", false)
	}
	notifyProfile := ""
	title := ""
	if fromUser {
		notifyProfile = b.tabOwnerOf(tabID)
		if notifyProfile != "" {
			if raw, err := conn.Call(ctx, "tab.get", map[string]any{"tabId": tabID}, 5*time.Second); err == nil {
				if info, err := resultMap(raw); err == nil {
					title, _ = info["title"].(string)
				}
			}
		}
	}
	raw, err := conn.Call(ctx, "tab.release", map[string]any{"tabId": tabID}, b.cfg.CommandTimeout)
	if err != nil {
		return nil, err
	}
	result, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	if post, ok := toInt(result["groupId"]); ok && post >= 0 {
		return nil, newError(CodeUngroupNoOp,
			fmt.Sprintf("Chrome reported the tab is still in group %d after release — tab-group editing is likely blocked (tab drag in progress, enterprise policy disabling tab groups, or a Chromium fork without full tab-groups support). Retry, or move the tab out of the group manually.", post), true)
	}
	b.setTabOwner(tabID, "")
	b.notifyHandover(notifyProfile, title, tabID, "detached")
	out := map[string]any{"ok": true, "tabId": tabID}
	for k, v := range result {
		out[k] = v
	}
	out["tabId"] = tabID
	return out, nil
}
