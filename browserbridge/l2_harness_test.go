package browserbridge

import (
	"fmt"
	"sync"
)

// l2Harness is a stateful fake extension used by the L2 tests. It models the
// extension's tab/group/audit commands closely enough to drive real bridge
// paths (context lifecycle, handover, health, sweep).
type l2Harness struct {
	mu sync.Mutex

	createGroups []int
	createTabs   []int
	createCalls  int

	groupOf map[int]int // tab id -> Chrome group id
	titleOf map[int]string

	groups       []map[string]any // tabGroup.list payload
	ungrouped    []int
	auditByTab   map[int]map[string]any
	cdpErr       string
	cdpErrMethod string
	releaseStuck bool

	destroyCalls []int
	adoptCalls   []int
	closeCalls   []int
	activateTabs []int
	location     string
}

func newL2Harness() *l2Harness {
	return &l2Harness{
		createGroups: []int{101, 202, 303},
		createTabs:   []int{501, 601, 701},
		groupOf:      map[int]int{},
		titleOf:      map[int]string{},
		auditByTab:   map[int]map[string]any{},
		location:     "https://example.com/",
	}
}

func (h *l2Harness) setGroups(groups ...map[string]any) {
	h.mu.Lock()
	h.groups = groups
	h.mu.Unlock()
}

func (h *l2Harness) setUngrouped(ids ...int) {
	h.mu.Lock()
	h.ungrouped = ids
	h.mu.Unlock()
}

func (h *l2Harness) setGroup(tab, group int) {
	h.mu.Lock()
	h.groupOf[tab] = group
	h.mu.Unlock()
}

func (h *l2Harness) group(tab int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if g, ok := h.groupOf[tab]; ok {
		return g
	}
	return -1
}

func (h *l2Harness) counts() (destroy, adopt, close int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.destroyCalls), len(h.adoptCalls), len(h.closeCalls)
}

func (h *l2Harness) closedTabs() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int(nil), h.closeCalls...)
}

func (h *l2Harness) setReleaseStuck(stuck bool) {
	h.mu.Lock()
	h.releaseStuck = stuck
	h.mu.Unlock()
}

func (h *l2Harness) setCDPError(method, msg string) {
	h.mu.Lock()
	h.cdpErrMethod, h.cdpErr = method, msg
	h.mu.Unlock()
}

func (h *l2Harness) setAudit(tab int, snapshot map[string]any) {
	h.mu.Lock()
	h.auditByTab[tab] = snapshot
	h.mu.Unlock()
}

func (h *l2Harness) handle(cmd fakeCommand) (any, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch cmd.Type {
	case "context.create":
		idx := h.createCalls
		h.createCalls++
		gid, tab := 101, 501
		if idx < len(h.createGroups) {
			gid = h.createGroups[idx]
		}
		if idx < len(h.createTabs) {
			tab = h.createTabs[idx]
		}
		h.groupOf[tab] = gid
		return map[string]any{"groupId": gid, "tabId": tab, "windowId": 7}, ""
	case "context.destroy":
		gid := paramInt(cmd.Params, "groupId", 0)
		h.destroyCalls = append(h.destroyCalls, gid)
		for tab, g := range h.groupOf {
			if g == gid {
				delete(h.groupOf, tab)
			}
		}
		return map[string]any{"ok": true, "closedTabs": 1, "persistedGroup": false}, ""
	case "tab.create":
		gid := paramInt(cmd.Params, "groupId", -1)
		tab := 800 + len(h.groupOf)
		h.groupOf[tab] = gid
		return map[string]any{"tabId": tab}, ""
	case "tab.get":
		tab := paramInt(cmd.Params, "tabId", 0)
		g := -1
		if v, ok := h.groupOf[tab]; ok {
			g = v
		}
		title := h.titleOf[tab]
		if title == "" {
			title = fmt.Sprintf("Tab %d", tab)
		}
		return map[string]any{"id": tab, "groupId": g, "title": title, "url": h.location}, ""
	case "tab.adopt":
		tab := paramInt(cmd.Params, "tabId", 0)
		gid := paramInt(cmd.Params, "groupId", 0)
		h.groupOf[tab] = gid
		h.adoptCalls = append(h.adoptCalls, tab)
		return map[string]any{"ok": true, "tabId": tab, "groupId": gid}, ""
	case "tab.release":
		tab := paramInt(cmd.Params, "tabId", 0)
		if h.releaseStuck {
			if g, ok := h.groupOf[tab]; ok {
				return map[string]any{"ok": true, "tabId": tab, "groupId": g}, ""
			}
			return map[string]any{"ok": true, "tabId": tab, "groupId": 101}, ""
		}
		delete(h.groupOf, tab)
		return map[string]any{"ok": true, "tabId": tab, "groupId": -1}, ""
	case "tab.close":
		tab := paramInt(cmd.Params, "tabId", 0)
		h.closeCalls = append(h.closeCalls, tab)
		delete(h.groupOf, tab)
		return map[string]any{"ok": true}, ""
	case "tab.activate":
		tab := paramInt(cmd.Params, "tabId", 0)
		h.activateTabs = append(h.activateTabs, tab)
		return map[string]any{"ok": true}, ""
	case "tab.list":
		gid := paramInt(cmd.Params, "groupId", -2)
		var tabs []any
		for tab, g := range h.groupOf {
			if gid != -2 && g != gid {
				continue
			}
			tabs = append(tabs, map[string]any{"id": tab, "url": h.location, "title": h.titleOf[tab], "groupId": g})
		}
		return map[string]any{"tabs": tabs}, ""
	case "tab.listUngrouped":
		var tabs []any
		for _, tab := range h.ungrouped {
			tabs = append(tabs, map[string]any{"id": tab, "windowId": 7, "url": h.location})
		}
		return map[string]any{"tabs": tabs}, ""
	case "tabGroup.list":
		if h.groups == nil {
			return map[string]any{"groups": []any{}}, ""
		}
		rows := make([]any, 0, len(h.groups))
		for _, g := range h.groups {
			rows = append(rows, g)
		}
		return map[string]any{"groups": rows}, ""
	case "tab.audit":
		tab := paramInt(cmd.Params, "tabId", 0)
		if audit, ok := h.auditByTab[tab]; ok {
			return audit, ""
		}
		return map[string]any{"tabId": tab, "url": "https://example.com/", "ourExtensionId": "own", "fileAccess": true}, ""
	case "cdp":
		method, _ := cmd.Params["method"].(string)
		if h.cdpErr != "" && (h.cdpErrMethod == "" || h.cdpErrMethod == method) {
			return nil, h.cdpErr
		}
		if method == "Runtime.evaluate" {
			expr, _ := cmd.Params["params"].(map[string]any)
			_ = expr
			params, _ := cmd.Params["params"].(map[string]any)
			expression := ""
			if params != nil {
				expression, _ = params["expression"].(string)
			}
			switch {
			case containsAny(expression, "document.readyState"):
				return map[string]any{"result": map[string]any{"value": "complete"}}, ""
			case containsAny(expression, "window.location.href"):
				return map[string]any{"result": map[string]any{"value": h.location}}, ""
			case containsAny(expression, "document.title"):
				return map[string]any{"result": map[string]any{"value": "Example"}}, ""
			case containsAny(expression, "AppTabBar_Profile_Link", "candidates"):
				return map[string]any{"result": map[string]any{"value": map[string]any{"status": "ok", "account_id": "https://www.linkedin.com/in/me/"}}}, ""
			default:
				return map[string]any{"result": map[string]any{"value": nil}}, ""
			}
		}
		if method == "Page.navigate" {
			return map[string]any{"frameId": "frame-1", "loaderId": "loader-1"}, ""
		}
		return map[string]any{}, ""
	}
	return map[string]any{"ok": true}, ""
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if n != "" && len(s) >= len(n) {
			for i := 0; i+len(n) <= len(s); i++ {
				if s[i:i+len(n)] == n {
					return true
				}
			}
		}
	}
	return false
}
