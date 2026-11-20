package browserbridge

import (
	"context"
	"fmt"
	"strings"
)

// commands_lifecycle.go ports tools/lifecycle.py: browser_setup, browser_status
// and browser_stop. They are bridge-side operations (no extension command for
// setup/status; stop reuses `context.destroy`).

const browserBridgeStoreURL = "https://chromewebstore.google.com/detail/hive-browser-bridge/jkpcegnbfimimjodblcemoheedidnppm"

// connectedProfiles mirrors bridge.connected_profiles: one discovery row per
// live extension connection, in the GET /profiles shape the tools expect.
func (b *Bridge) connectedProfiles() []map[string]any {
	rows := b.Profiles()
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"label":            row["label"],
			"is_default":       row["is_default"],
			"starred":          row["starred"],
			"version":          row["version"],
			"protocol_version": row["protocol_version"],
		})
	}
	return out
}

func cmdBrowserSetup(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	profiles := b.connectedProfiles()
	if b.IsConnected() {
		status := "Extension is connected and ready. Call browser_open(url) to begin."
		if len(profiles) > 1 {
			labels := make([]string, 0, len(profiles))
			for _, p := range profiles {
				if label, _ := p["label"].(string); label != "" {
					labels = append(labels, label)
				}
			}
			status += fmt.Sprintf(" %d Chrome profiles are connected (%s); pass browser_profile=<label> to browser_open to choose one.",
				len(profiles), strings.Join(labels, ", "))
		}
		return map[string]any{
			"ok":                 true,
			"connected":          true,
			"status":             status,
			"connected_profiles": profiles,
		}, nil
	}
	out := map[string]any{
		"ok":        false,
		"connected": false,
		"status": "The Hive browser extension isn't connected. Make sure the Hive app is " +
			"running, then follow the steps below to install and enable the Hive " +
			"Browser Bridge extension in Chrome.",
		"install_url": browserBridgeStoreURL,
		"instructions": map[string]any{
			"step_1": fmt.Sprintf("Open the Hive Browser Bridge listing in the Chrome Web Store: %s — or load the unpacked extension from go_hive/browserbridge/extension.", browserBridgeStoreURL),
			"step_2": "Click 'Add to Chrome' and confirm the install prompt (or chrome://extensions → Developer mode → Load unpacked).",
			"step_3": "Pin the extension (puzzle-piece icon → pin) and click its toolbar icon to verify it says 'Connected'.",
			"step_4": "Return here and retry — the bridge will pick up the new connection automatically.",
		},
		"note": "The extension connects to the local Hive runtime via WebSocket on " +
			"ws://127.0.0.1:14829/bridge (older builds use 9229). Chrome must be " +
			"running and the extension must be enabled.",
	}
	if b.cfg.ExtensionDir != "" {
		out["local_extension_path"] = b.cfg.ExtensionDir
	}
	return out, nil
}

func cmdBrowserStatus(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	if !b.IsConnected() {
		// A status probe reports state instead of failing: upstream returns a
		// dict with ok=false plus the actionable disconnect text.
		return map[string]any{
			"ok":                 false,
			"connected":          false,
			"error":              connectionHelp(),
			"connected_profiles": []map[string]any{},
		}, nil
	}
	profileName := strings.TrimSpace(profile)
	if paramProfile, ok := paramString(params, "profile"); ok && strings.TrimSpace(paramProfile) != "" {
		profileName = strings.TrimSpace(paramProfile)
	}
	if profileName == "" {
		profileName = "default"
	}
	connProfiles := b.connectedProfiles()
	meta, hasCtx := b.contexts.Get(profileName)
	if !hasCtx {
		return map[string]any{
			"ok": true, "connected": true, "profile": profileName, "running": false,
			"connected_profiles": connProfiles, "tabs": 0,
		}, nil
	}
	if meta.GroupID <= 0 {
		return map[string]any{
			"ok": true, "connected": true, "profile": profileName, "running": true,
			"groupId": nil, "activeTab": nil, "browser_profile": meta.BrowserProfile,
			"connected_profiles": connProfiles, "tabs": 0,
		}, nil
	}
	tabs := b.listTabsForGroup(ctx, meta.GroupID)
	active := b.contextActiveTabFor(profileName)
	if active == 0 {
		for _, tab := range tabs {
			if isActive, _ := tab["active"].(bool); isActive {
				active, _ = toInt(tab["id"])
				break
			}
		}
	}
	return map[string]any{
		"ok": true, "connected": true, "profile": profileName, "running": true,
		"groupId": meta.GroupID, "activeTab": nilIfZero(active),
		"browser_profile":    meta.BrowserProfile,
		"connected_profiles": connProfiles,
		"tabs":               len(tabs),
	}, nil
}

// cmdBrowserStop closes one profile's tab group. Upstream's
// close_profile_context also drops the local registry entry; in this port the
// bridge IS the registry, so a failed destroy keeps the entry (and reports
// deferred) instead of orphaning the Chrome group.
func cmdBrowserStop(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	if !b.IsConnected() {
		return map[string]any{"ok": false, "error": connectionHelp()}, nil
	}
	profileName := strings.TrimSpace(profile)
	if paramProfile, ok := paramString(params, "profile"); ok && strings.TrimSpace(paramProfile) != "" {
		profileName = strings.TrimSpace(paramProfile)
	}
	if profileName == "" {
		profileName = "default"
	}
	res, err := b.CloseProfileContext(ctx, profileName, "")
	if err != nil {
		e := AsError(err)
		return map[string]any{
			"ok": false, "status": "deferred", "profile": profileName,
			"error": e.Message, "code": e.Code, "retryable": true,
		}, nil
	}
	return res, nil
}

// CloseProfileContext is the Go equivalent of lifecycle.close_profile_context:
// destroy the group this logical profile owns, then drop the registry entry.
func (b *Bridge) CloseProfileContext(ctx context.Context, profileName, browserProfile string) (map[string]any, error) {
	meta, ok := b.contexts.Get(profileName)
	if !ok {
		return map[string]any{"ok": true, "status": "not_running", "profile": profileName}, nil
	}
	if browserProfile == "" {
		browserProfile = meta.BrowserProfile
	}
	if meta.GroupID <= 0 {
		b.contexts.Delete(profileName)
		return map[string]any{"ok": true, "status": "stopped", "profile": profileName, "closedTabs": 0}, nil
	}
	result, err := b.DestroyContext(ctx, meta.GroupID, browserProfile)
	if err != nil {
		return nil, err
	}
	b.contexts.Delete(profileName)
	closedTabs := 0
	if n, ok := toInt(result["closedTabs"]); ok {
		closedTabs = n
	}
	return map[string]any{
		"ok": true, "status": "stopped", "profile": profileName, "closedTabs": closedTabs,
	}, nil
}

// connectionHelp is the upstream actionable disconnect text, condensed to the
// three causes the bridge can distinguish without a control RPC.
func connectionHelp() string {
	return "The Hive browser extension isn't connected. Open Chrome, enable the Hive Browser Bridge " +
		"extension, and confirm it points at ws://127.0.0.1:14829/bridge (older builds use 9229). " +
		"If the port is already taken, stop the process holding it. Then retry."
}

func nilIfZero(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}
