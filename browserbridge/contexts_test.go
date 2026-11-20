package browserbridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateContextRegistersGroupAndRoutes(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	// Beta connects first so default routing would pick it; group-id routing
	// must still target alpha, the profile that minted group 101.
	beta := startFakeExtension(t, b.WSAddr(), helloFor("beta"), h.handle)
	alpha := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.conns) == 2
	}, "two connections")

	res, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "display_name": "Queen One", "browser_profile": "alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if got := paramInt(m, "groupId", 0); got != 101 {
		t.Fatalf("groupId=%v", m["groupId"])
	}
	if got := paramInt(m, "tabId", 0); got != 501 {
		t.Fatalf("tabId=%v", m["tabId"])
	}
	if m["browser_profile"] != "alpha" {
		t.Fatalf("browser_profile=%v", m["browser_profile"])
	}
	if b.tabOwnerOf(501) != "queen-1" {
		t.Fatalf("seed tab owner=%q", b.tabOwnerOf(501))
	}

	// Group-scoped listing goes to alpha, not the first-come default beta.
	if _, err := b.Execute(context.Background(), "", "browser_tabs", map[string]any{"group_id": 101}); err != nil {
		t.Fatal(err)
	}
	if alpha.count("tab.list") == 0 {
		t.Fatal("group-routed tab.list never reached alpha")
	}
	if beta.count("tab.list") != 0 {
		t.Fatal("group-routed tab.list leaked to beta")
	}

	entries := b.ListContexts(context.Background())
	if len(entries) != 1 {
		t.Fatalf("contexts=%+v", entries)
	}
	entry := entries[0]
	if entry.Profile != "queen-1" || entry.GroupID == nil || *entry.GroupID != 101 || entry.Name != "Queen One" {
		t.Fatalf("entry=%+v", entry)
	}
	if len(entry.Tabs) != 1 || paramInt(entry.Tabs[0], "id", 0) != 501 {
		t.Fatalf("tabs=%+v", entry.Tabs)
	}
	if entry.Status != "idle" {
		t.Fatalf("status=%q", entry.Status)
	}

	// Per-tab blockers roll up into the context row and flip its status.
	b.setTabBlockers(501, []Blocker{{Kind: "devtools_attached", Severity: "block", Title: "DevTools", Priority: 30}})
	entries = b.ListContexts(context.Background())
	if len(entries[0].Blockers) != 1 || entries[0].Blockers[0].Kind != "devtools_attached" {
		t.Fatalf("blockers=%+v", entries[0].Blockers)
	}
	if entries[0].Status != "blocked" {
		t.Fatalf("status=%q", entries[0].Status)
	}
}

func TestAdoptTabHandoverConflictAndAlreadyOwned(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	h.setGroup(600, -1)
	res, err := b.Execute(context.Background(), "", "adopt_tab", map[string]any{
		"profile": "queen-1", "tab_id": 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["ok"] != true || paramInt(m, "groupId", 0) != 101 {
		t.Fatalf("adopt=%+v", m)
	}
	if b.tabOwnerOf(600) != "queen-1" {
		t.Fatalf("owner=%q", b.tabOwnerOf(600))
	}
	destroy, adopt, _ := h.counts()
	if adopt != 1 || destroy != 0 {
		t.Fatalf("adopt calls=%d destroy=%d", adopt, destroy)
	}

	// Already in the target group: success without a second wire move.
	res, err = b.Execute(context.Background(), "", "adopt_tab", map[string]any{
		"profile": "queen-1", "tab_id": 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["already_owned"] != true {
		t.Fatalf("already_owned=%+v", res)
	}
	if _, adopt, _ = h.counts(); adopt != 1 {
		t.Fatalf("already-owned adopt issued a wire move: %d", adopt)
	}

	// Second agent's group: adoption is refused with a conflict.
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-2", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	h.setGroup(700, 202)
	b.setTabOwner(700, "queen-2")
	_, err = b.Execute(context.Background(), "", "adopt_tab", map[string]any{
		"profile": "queen-1", "tab_id": 700,
	})
	if err == nil || AsError(err).Code != CodeConflict {
		t.Fatalf("want conflict, got %v", err)
	}
	if b.tabOwnerOf(700) != "queen-2" {
		t.Fatalf("conflict must not steal ownership: %q", b.tabOwnerOf(700))
	}
	_ = f
}

func TestAdoptTabLazyGroupRecreateAfterSoftPrune(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_group_event", "event": "removed", "groupId": 101, "windowId": 7,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		meta, _ := b.contexts.Get("queen-1")
		return meta.GroupID == 0 && meta.Name == "queen-1"
	}, "soft prune")

	h.setGroup(650, -1)
	res, err := b.Execute(context.Background(), "", "adopt_tab", map[string]any{
		"profile": "queen-1", "tab_id": 650,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if paramInt(m, "groupId", 0) != 202 {
		t.Fatalf("lazy-minted group=%v", m["groupId"])
	}
	if b.tabOwnerOf(650) != "queen-1" {
		t.Fatalf("owner=%q", b.tabOwnerOf(650))
	}
	_, _, closeCalls := h.counts()
	foundSeed := false
	for _, tab := range h.closedTabs() {
		if tab == 601 {
			foundSeed = true
		}
	}
	if !foundSeed || closeCalls == 0 {
		t.Fatalf("seed tab was not closed after lazy adopt: %v", h.closedTabs())
	}
	meta, _ := b.contexts.Get("queen-1")
	if meta.GroupID != 202 {
		t.Fatalf("registry group=%d", meta.GroupID)
	}
}

func TestBrowserOpenWithGroupRoutesAndAttributes(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	beta := startFakeExtension(t, b.WSAddr(), helloFor("beta"), h.handle)
	alpha := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.conns) == 2
	}, "two connections")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := b.Execute(context.Background(), "", "browser_open", map[string]any{
		"url": "https://example.com/page", "group_id": 101,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	tabID := paramInt(m, "tabId", 0)
	if tabID <= 0 || m["ok"] != true {
		t.Fatalf("open=%+v", m)
	}
	if alpha.count("tab.create") == 0 {
		t.Fatal("group-scoped open did not reach alpha")
	}
	if beta.count("tab.create") != 0 {
		t.Fatal("group-scoped open leaked to beta")
	}
	if b.tabOwnerOf(tabID) != "queen-1" {
		t.Fatalf("new tab owner=%q", b.tabOwnerOf(tabID))
	}
}

func TestStarredDefaultWinsOverFirstCome(t *testing.T) {
	labelFile := filepath.Join(t.TempDir(), "browser_default.json")
	b := newTestBridge(t, Config{SweepDisabled: true, DefaultLabelFile: labelFile})
	alpha := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	beta := startFakeExtension(t, b.WSAddr(), helloFor("beta"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.conns) == 2
	}, "two connections")
	b.SetStarredDefault("beta", "ext-beta")
	if _, err := b.Execute(context.Background(), "", "browser_tabs", nil); err != nil {
		t.Fatal(err)
	}
	if beta.count("tab.list") == 0 || alpha.count("tab.list") != 0 {
		t.Fatalf("starred default not used: alpha=%d beta=%d", alpha.count("tab.list"), beta.count("tab.list"))
	}
	// Persisted star survives a new bridge instance.
	reloaded := New(Config{DefaultLabelFile: labelFile})
	if reloaded.StarredDefault() != "beta" {
		t.Fatalf("reloaded star=%q", reloaded.StarredDefault())
	}
}

func TestRegisterContextRehydratesAndAttributesEvents(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	for i := 0; i < 2; i++ {
		if _, err := b.Execute(context.Background(), "", "register_context", map[string]any{
			"profile": "rehydrated", "group_id": 909, "name": "Rehydrated", "browser_profile": "alpha",
		}); err != nil {
			t.Fatal(err)
		}
	}
	metas := b.contexts.List()
	if len(metas) != 1 || metas[0].Profile != "rehydrated" || metas[0].GroupID != 909 {
		t.Fatalf("metas=%+v", metas)
	}

	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_event", "event": "grouped", "tabId": 888, "groupId": 909,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.tabOwnerOf(888) == "rehydrated" }, "rehydrated attribution")
}

func TestDisconnectSoftPrunesContextSlice(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}
	_ = f.client.Kill()
	waitFor(t, 2*time.Second, func() bool {
		meta, _ := b.contexts.Get("queen-1")
		return meta.GroupID == 0 && meta.Name == "queen-1"
	}, "disconnect soft prune")
}

func TestReleaseTabAndUngroupNoOp(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}
	h.setGroup(650, 101)
	b.setTabOwner(650, "queen-1")

	res, err := b.Execute(context.Background(), "", "release_tab", map[string]any{"tab_id": 650})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["ok"] != true || b.tabOwnerOf(650) != "" {
		t.Fatalf("release=%+v owner=%q", res, b.tabOwnerOf(650))
	}

	h.setReleaseStuck(true)
	h.setGroup(651, 101)
	b.setTabOwner(651, "queen-1")
	_, err = b.Execute(context.Background(), "", "release_tab", map[string]any{"tab_id": 651})
	if err == nil || AsError(err).Code != CodeUngroupNoOp {
		t.Fatalf("want ungroup_no_op, got %v", err)
	}
	if b.tabOwnerOf(651) != "queen-1" {
		t.Fatal("failed release must keep attribution")
	}
}

func TestReleaseFromUserEmitsUpstreamMessage(t *testing.T) {
	var gotProfile, gotText, gotAction string
	b := newTestBridge(t, Config{
		SweepDisabled: true,
		Notify: func(profile, text string, tabID int, action string) {
			gotProfile, gotText, gotAction = profile, text, action
		},
	})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}
	h.setGroup(650, 101)
	b.setTabOwner(650, "queen-1")
	if _, err := b.ReleaseTab(context.Background(), 650, true); err != nil {
		t.Fatal(err)
	}
	if gotProfile != "queen-1" || gotAction != "detached" {
		t.Fatalf("notify profile=%q action=%q", gotProfile, gotAction)
	}
	if gotText != `The user detached "Tab 650" (tab #650) from you.` {
		t.Fatalf("text=%q", gotText)
	}
}

func TestHandoverProtocolGate(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), map[string]any{
		"type": "hello", "version": "1.4.0", "protocolVersion": 3,
		"extensionId": "old-extension", "profileLabel": "alpha",
	}, h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}
	_, err := b.Execute(context.Background(), "", "adopt_tab", map[string]any{"profile": "queen-1", "tab_id": 650})
	if err == nil || AsError(err).Code != CodeUnsupportedExt {
		t.Fatalf("want unsupported_extension, got %v", err)
	}
	if !strings.Contains(AsError(err).Message, "v1.5+") {
		t.Fatalf("message=%q", AsError(err).Message)
	}
}
