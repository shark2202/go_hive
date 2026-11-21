package browserbridge

// commands_tool_surface_test.go pins the tool-surface residuals of
// docs/browser-bridge-surface-audit.html §2.2 that the Python side gets from
// its tool layer (tools/src/gcu/browser/tools/*.py):
//
//   - browser_close / browser_navigate / browser_reload accept an omitted
//     tab_id and fall back to the context's active tab (tabs.py:311,
//     navigation.py:151, navigation.py:266);
//   - browser_tabs reports the active tab (tabs.py:159);
//   - browser_open / browser_navigate append the group tab summary
//     (lifecycle.py:444 summarize_group_tabs);
//   - browser_interact returns the upstream action dict key sets
//     (bridge.py click/type_text/press_key/scroll + interact.py dispatch).

import (
	"context"
	"sort"
	"strings"
	"testing"
)

// withContext registers the agent profile → group mapping the tool layer keeps
// in upstream's `_contexts`, and points its active tab at tabID.
func withContext(t *testing.T, b *Bridge, profile string, groupID, tabID int) {
	t.Helper()
	b.contexts.Upsert(ContextMeta{Profile: profile, GroupID: groupID, Name: profile, BrowserProfile: "alpha"})
	if tabID > 0 {
		b.setContextActiveTab(profile, tabID)
	}
}

// Parity: tools/src/gcu/browser/tools/tabs.py::browser_close (tab_id=None closes
// the context's active tab) and navigation.py::browser_navigate / browser_reload.
func TestBrowserTablessCommandsResolveActiveTab(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	withContext(t, b, "queen-1", 101, 501)

	closed := execToolProfile(t, b, "queen-1", "browser_close", map[string]any{})
	if closed["ok"] != true || paramInt(closed, "closed", 0) != 501 {
		t.Fatalf("tabless close = %+v", closed)
	}

	nav := execToolProfile(t, b, "queen-1", "browser_navigate", map[string]any{
		"url": "https://example.com/next", "wait_until": "commit",
	})
	if nav["ok"] != true || paramInt(nav, "tabId", 0) != 501 {
		t.Fatalf("tabless navigate = %+v", nav)
	}

	reload := execToolProfile(t, b, "queen-1", "browser_reload", map[string]any{})
	if reload["ok"] != true || paramInt(reload, "tabId", 0) != 501 {
		t.Fatalf("tabless reload = %+v", reload)
	}

	// An explicit tab_id still wins over the active tab.
	explicit := execToolProfile(t, b, "queen-1", "browser_close", map[string]any{"tab_id": 777})
	if paramInt(explicit, "closed", 0) != 777 {
		t.Fatalf("explicit close = %+v", explicit)
	}
}

// Parity: tools/src/gcu/browser/tools/tabs.py::browser_close ("No tab to close"
// when there is neither a tab_id nor a tracked active tab).
func TestBrowserTablessCommandsFailWithoutActiveTab(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	withContext(t, b, "queen-1", 101, 0)

	err := execToolProfileErr(t, b, "queen-1", "browser_close", map[string]any{})
	if !strings.Contains(err.Message, "no active tab") {
		t.Fatalf("close error = %q", err.Message)
	}
}

// Parity: tools/src/gcu/browser/tools/tabs.py::browser_tabs (result carries
// activeTabId from the context).
func TestBrowserTabsReportsActiveTabID(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	withContext(t, b, "queen-1", 101, 501)

	res := execToolProfile(t, b, "queen-1", "browser_tabs", map[string]any{})
	if paramInt(res, "activeTabId", 0) != 501 {
		t.Fatalf("activeTabId = %v (want 501): %+v", res["activeTabId"], res)
	}
	if paramInt(res, "total", 0) != 1 {
		t.Fatalf("total = %v", res["total"])
	}
}

// Parity: tools/src/gcu/browser/tools/tabs.py::browser_tabs (a caller with no
// context keeps the profile listing and a null activeTabId).
func TestBrowserTabsWithoutContextKeepsNullActiveTabID(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)

	res := execTool(t, b, "browser_tabs", map[string]any{})
	if value, present := res["activeTabId"]; !present || value != nil {
		t.Fatalf("activeTabId = %v (present=%v), want nil", value, present)
	}
}

// Parity: tools/src/gcu/browser/tools/lifecycle.py::summarize_group_tabs
// (browser_open appends groupTabCount + groupTabs[{id,url,title,active}]).
func TestBrowserOpenAndNavigateAppendGroupSummary(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	withContext(t, b, "queen-1", 101, 501)

	opened := execToolProfile(t, b, "queen-1", "browser_open", map[string]any{
		"url": "https://example.com/page",
	})
	if paramInt(opened, "groupTabCount", -1) != 1 {
		t.Fatalf("open groupTabCount = %v: %+v", opened["groupTabCount"], opened)
	}
	groupTabs, _ := opened["groupTabs"].([]any)
	if len(groupTabs) != 1 {
		t.Fatalf("open groupTabs = %+v", opened["groupTabs"])
	}
	tab, _ := groupTabs[0].(map[string]any)
	if paramInt(tab, "id", 0) != 501 || tab["url"] != "https://example.com/" || tab["title"] != "Example" || tab["active"] != true {
		t.Fatalf("open groupTabs[0] = %+v", tab)
	}

	nav := execToolProfile(t, b, "queen-1", "browser_navigate", map[string]any{
		"url": "https://example.com/next", "wait_until": "commit",
	})
	if paramInt(nav, "groupTabCount", -1) != 1 {
		t.Fatalf("navigate groupTabCount = %v: %+v", nav["groupTabCount"], nav)
	}
}

// Parity: tools/src/gcu/browser/tools/interact.py::_dispatch (the action dicts
// are returned verbatim from bridge.py click / click_coordinate / hover /
// type_text / press_key / scroll and interact.py _do_drag / wait).
func TestBrowserInteractReturnKeySets(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
		want   []string
		// extraKeys are Go-side additions documented as intentional: the
		// auto-snapshot payload (upstream _attach_snapshot does the same) and
		// the inline image carrier for coordinate actions (R15).
		extraKeys []string
	}{
		{
			name:   "left_click selector",
			params: map[string]any{"action": "left_click", "selector": ".btn", "tab_id": 501, "auto_snapshot_mode": "off"},
			want:   []string{"ok", "action", "selector", "x", "y", "method"},
		},
		{
			name:      "left_click coordinate",
			params:    map[string]any{"action": "left_click", "coordinate": []any{0.5, 0.5}, "tab_id": 501},
			want:      []string{"ok", "action", "x", "y"},
			extraKeys: []string{"data", "mimeType"},
		},
		{
			name:   "hover selector",
			params: map[string]any{"action": "hover", "selector": ".btn", "tab_id": 501},
			want:   []string{"ok", "action", "selector", "x", "y"},
		},
		{
			name:   "type",
			params: map[string]any{"action": "type", "selector": ".input", "text": "hi", "tab_id": 501, "auto_snapshot_mode": "off"},
			want:   []string{"ok", "action", "selector", "length"},
		},
		{
			name:   "key",
			params: map[string]any{"action": "key", "text": "Enter", "tab_id": 501},
			want:   []string{"ok", "action", "key", "modifiers"},
		},
		{
			name:   "scroll",
			params: map[string]any{"action": "scroll", "tab_id": 501, "auto_snapshot_mode": "off"},
			want:   []string{"ok", "action", "direction", "amount", "method", "container", "steps"},
		},
		{
			name:   "drag",
			params: map[string]any{"action": "drag", "start_selector": ".a", "selector": ".b", "tab_id": 501},
			want:   []string{"ok", "action", "fromCoords", "toCoords"},
		},
		{
			name:   "wait",
			params: map[string]any{"action": "wait", "duration": 0.01, "tab_id": 501},
			want:   []string{"ok", "action", "condition", "seconds"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newL3Fake()
			b, _ := fake.wired(t)
			res := execTool(t, b, "browser_interact", tc.params)
			got := mapKeys(res)
			want := append([]string(nil), tc.want...)
			want = append(want, tc.extraKeys...)
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("key set = %v, want %v (result %+v)", got, want, res)
			}
		})
	}
}

// select's success dict is asserted separately: the key set is the upstream
// bridge.select_option one (drop the image/snapshot carriers select never has).
//
// Parity: tools/src/gcu/browser/tools/interact.py::browser_select
func TestBrowserSelectReturnKeySet(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_select", map[string]any{
		"selector": ".dropdown", "values": []any{"a"}, "tab_id": 501,
	})
	got := mapKeys(res)
	want := []string{"action", "ok", "selected", "selector"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("key set = %v, want %v (result %+v)", got, want, res)
	}
}

// auto_snapshot_mode=off keeps the action dict bare, so the key-set assertions
// above compare against the upstream dicts, not against the snapshot payload
// upstream also attaches by default.
//
// Parity: tools/src/gcu/browser/tools/interact.py::_attach_snapshot
func TestBrowserInteractSnapshotPayloadStillAttachedByDefault(t *testing.T) {
	fake := newL3Fake()
	b, _ := fake.wired(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "left_click", "selector": ".btn", "tab_id": 501,
	})
	if _, present := res["snapshot"]; !present {
		t.Fatalf("default auto_snapshot_mode must attach a snapshot: %+v", res)
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func execToolProfile(t *testing.T, b *Bridge, profile, typ string, params map[string]any) map[string]any {
	t.Helper()
	res, err := b.Execute(context.Background(), profile, typ, params)
	if err != nil {
		t.Fatalf("%s: %v", typ, err)
	}
	return mustMap(t, res)
}

func execToolProfileErr(t *testing.T, b *Bridge, profile, typ string, params map[string]any) *Error {
	t.Helper()
	res, err := b.Execute(context.Background(), profile, typ, params)
	if err == nil {
		t.Fatalf("%s: expected an error, got %+v", typ, res)
	}
	return AsError(err)
}
