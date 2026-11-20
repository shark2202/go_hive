package browserbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForTelemetry(t *testing.T, dir string, timeout time.Duration, match func([]map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []map[string]any
	for time.Now().Before(deadline) {
		last = readTelemetry(t, dir)
		if match(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("telemetry never matched; last=%+v", last)
	return nil
}

func readTelemetry(t *testing.T, dir string) []map[string]any {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasPrefix(ent.Name(), "browser-") || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) == nil {
				out = append(out, row)
			}
		}
	}
	return out
}

func TestTabAndGroupEventsUpdateState(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}

	// created -> grouped tab inherits the group's agent.
	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_event", "event": "created", "tabId": 600,
		"groupId": 101, "openerTabId": 501, "active": true,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.tabOwnerOf(600) == "queen-1" }, "created attribution")
	if b.contextActiveTabFor("queen-1") != 600 {
		t.Fatalf("active tab=%d", b.contextActiveTabFor("queen-1"))
	}

	// A popup that hasn't been grouped yet inherits its opener's agent.
	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_event", "event": "created", "tabId": 601,
		"groupId": -1, "openerTabId": 600,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.tabOwnerOf(601) == "queen-1" }, "opener attribution")

	// A tab in no Hive group is not attributed.
	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_event", "event": "created", "tabId": 602, "groupId": -1,
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if owner := b.tabOwnerOf(602); owner != "" {
		t.Fatalf("unowned tab attributed to %q", owner)
	}

	// removed evicts routing and attribution.
	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_event", "event": "removed", "tabId": 600,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.tabOwnerOf(600) == "" }, "removed attribution")

	// tab_group_event removed soft-prunes the registry but keeps identity.
	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_group_event", "event": "removed", "groupId": 101,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		meta, _ := b.contexts.Get("queen-1")
		return meta.GroupID == 0 && meta.Name == "queen-1"
	}, "group soft prune")
}

func TestCDPEventConsumersAndViewportTelemetry(t *testing.T) {
	dir := t.TempDir()
	b := newTestBridge(t, Config{SweepDisabled: true, TelemetryDir: dir})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	if err := f.client.WriteJSON(map[string]any{
		"type": "cdp_event", "tabId": 11, "method": "Page.javascriptDialogOpening",
		"params": map[string]any{"type": "confirm", "message": "Leave?", "url": "https://example.com/"},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.PendingDialog(11) != nil }, "pending dialog")
	if b.PendingDialog(11)["type"] != "confirm" {
		t.Fatalf("dialog=%+v", b.PendingDialog(11))
	}

	// Main-frame navigation invalidates cached health.
	b.setTabBlockers(11, []Blocker{{Kind: "privileged_scheme", Severity: "block", Priority: 10}})
	if err := f.client.WriteJSON(map[string]any{
		"type": "cdp_event", "tabId": 11, "method": "Page.frameNavigated",
		"params": map[string]any{"frame": map[string]any{"url": "https://new.example/"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return len(b.GetTabBlockers(11)) == 0 }, "health invalidation")

	// [hive_vp] console probe becomes a structured viewport_event row.
	if err := f.client.WriteJSON(map[string]any{
		"type": "cdp_event", "tabId": 11, "method": "Runtime.consoleAPICalled",
		"params": map[string]any{"type": "info", "args": []any{
			map[string]any{"type": "string", "value": "[hive_vp]"},
			map[string]any{"type": "string", "value": `{"w":900,"h":700}`},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.client.WriteJSON(map[string]any{
		"type": "cdp_event", "tabId": 11, "method": "Runtime.consoleAPICalled",
		"params": map[string]any{"type": "info", "args": []any{
			map[string]any{"type": "string", "value": "[hive_attach_canary]"},
			map[string]any{"type": "string", "value": `{"tabId":11}`},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	entries := waitForTelemetry(t, dir, 2*time.Second, func(rows []map[string]any) bool {
		var viewport, canary bool
		for _, row := range rows {
			switch row["type"] {
			case "viewport_event":
				viewport = row["w"] == float64(900)
			case "attach_canary":
				canary = true
			}
		}
		return viewport && canary
	})
	if len(entries) == 0 {
		t.Fatal("no telemetry rows")
	}

	if err := f.client.WriteJSON(map[string]any{
		"type": "cdp_event", "tabId": 11, "method": "Page.javascriptDialogClosed",
		"params": map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.PendingDialog(11) == nil }, "dialog closed")
}

func TestTabHealthHTTPAndErrorBlockers(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	h.setAudit(11, map[string]any{
		"tabId": 11, "url": "chrome://settings/", "ourExtensionId": "own",
	})
	h.setAudit(12, map[string]any{
		"tabId": 12, "url": "https://example.com/", "ourExtensionId": "own",
		"domFrames": []any{map[string]any{"src": "chrome-extension://cbhilkcodigmigfbnphipnnmamjfkipp/frame.html"}},
	})
	h.setAudit(13, map[string]any{
		"tabId": 13, "url": "https://example.com/", "ourExtensionId": "own",
	})
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	base := "http://" + b.HTTPAddr()

	code, health := httpJSON(t, "GET", base+"/tabs/11/health?force=1", "")
	if code != 200 || health["ok"] != true || paramInt(health, "tab_id", 0) != 11 {
		t.Fatalf("privileged health=%d %+v", code, health)
	}
	blockers, _ := health["blockers"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["kind"] != "privileged_scheme" {
		t.Fatalf("blockers=%+v", health["blockers"])
	}
	blocker := blockers[0].(map[string]any)
	for _, key := range []string{"kind", "severity", "title", "detail", "fix", "context"} {
		if _, ok := blocker[key]; !ok {
			t.Fatalf("blocker missing %q: %+v", key, blocker)
		}
	}

	code, health = httpJSON(t, "GET", base+"/tabs/12/health?force=1", "")
	blockers, _ = health["blockers"].([]any)
	if code != 200 || len(blockers) != 1 {
		t.Fatalf("foreign health=%d %+v", code, health)
	}
	foreign := blockers[0].(map[string]any)
	if foreign["kind"] != "foreign_extension_frame" || !strings.Contains(foreign["title"].(string), "Calendly") {
		t.Fatalf("foreign blocker=%+v", foreign)
	}

	code, health = httpJSON(t, "GET", base+"/tabs/13/health?force=1", "")
	if code != 200 || len(health["blockers"].([]any)) != 0 {
		t.Fatalf("clean health=%d %+v", code, health)
	}

	// A CDP error populates a cached blocker without a fresh audit clearing it.
	h.setCDPError("Runtime.evaluate", "Another debugger is already attached")
	if _, err := b.Execute(context.Background(), "alpha", "browser_evaluate", map[string]any{
		"tab_id": 14, "script": "1+1",
	}); err == nil {
		t.Fatal("expected CDP error")
	}
	code, health = httpJSON(t, "GET", base+"/tabs/14/health", "")
	blockers, _ = health["blockers"].([]any)
	if code != 200 || len(blockers) != 1 || blockers[0].(map[string]any)["kind"] != "devtools_attached" {
		t.Fatalf("error blocker=%d %+v", code, health)
	}
}

func TestContextsHTTPStarredProfileAndHandoverEndpoints(t *testing.T) {
	labelFile := filepath.Join(t.TempDir(), "browser_default.json")
	b := newTestBridge(t, Config{SweepDisabled: true, DefaultLabelFile: labelFile})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "display_name": "Queen One", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	base := "http://" + b.HTTPAddr()

	code, payload := httpJSON(t, "GET", base+"/contexts", "")
	if code != 200 || payload["ok"] != true {
		t.Fatalf("contexts=%d %+v", code, payload)
	}
	contexts, _ := payload["contexts"].([]any)
	if len(contexts) != 1 {
		t.Fatalf("contexts=%+v", payload)
	}
	row := contexts[0].(map[string]any)
	if row["profile"] != "queen-1" || paramInt(row, "groupId", 0) != 101 || row["name"] != "Queen One" {
		t.Fatalf("row=%+v", row)
	}
	for _, key := range []string{"profile", "groupId", "activeTab", "name", "tabs", "last_active_ms", "dormant", "blockers", "status"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("context row missing %q: %+v", key, row)
		}
	}

	code, payload = httpJSON(t, "POST", base+"/profiles/default", `{"label":"alpha","extensionId":"ext-alpha"}`)
	if code != 200 || payload["ok"] != true {
		t.Fatalf("star=%d %+v", code, payload)
	}
	if b.StarredDefault() != "alpha" {
		t.Fatalf("starred=%q", b.StarredDefault())
	}
	if _, err := os.Stat(labelFile); err != nil {
		t.Fatalf("default label file not persisted: %v", err)
	}
	code, payload = httpJSON(t, "GET", base+"/profiles", "")
	profiles, _ := payload["profiles"].([]any)
	if code != 200 || len(profiles) != 1 {
		t.Fatalf("profiles=%d %+v", code, payload)
	}
	prow := profiles[0].(map[string]any)
	if prow["starred"] != true || prow["is_default"] != true {
		t.Fatalf("profile=%+v", prow)
	}

	h.setGroup(650, -1)
	code, payload = httpJSON(t, "POST", base+"/contexts/queen-1/adopt-tab", `{"tabId":650}`)
	if code != 200 || payload["ok"] != true || b.tabOwnerOf(650) != "queen-1" {
		t.Fatalf("adopt HTTP=%d %+v owner=%q", code, payload, b.tabOwnerOf(650))
	}
	code, payload = httpJSON(t, "POST", base+"/tabs/650/release", `{}`)
	if code != 200 || payload["ok"] != true || b.tabOwnerOf(650) != "" {
		t.Fatalf("release HTTP=%d %+v", code, payload)
	}

	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-2", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}
	h.setGroup(700, 202)
	b.setTabOwner(700, "queen-2")
	code, payload = httpJSON(t, "POST", base+"/contexts/queen-1/adopt-tab", `{"tabId":700}`)
	if code != 409 || payload["code"] != CodeConflict {
		t.Fatalf("conflict HTTP=%d %+v", code, payload)
	}
}

func TestSystemBlockerRollupAndStatus(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), newL2Harness().handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	b.contexts.Upsert(ContextMeta{Profile: "a", GroupID: 1, Name: "a", BrowserProfile: "alpha"})
	b.contexts.Upsert(ContextMeta{Profile: "b", GroupID: 2, Name: "b", BrowserProfile: "alpha"})
	b.setTabOwner(11, "a")
	b.setTabOwner(12, "b")
	devtools := Blocker{Kind: "devtools_attached", Severity: "block", Title: "DevTools", Priority: 30}
	b.setTabBlockers(11, []Blocker{devtools})
	b.setTabBlockers(12, []Blocker{devtools})
	sb := b.SystemBlocker()
	if sb == nil || sb.Kind != "devtools_attached" {
		t.Fatalf("system blocker=%+v", sb)
	}
	if st := b.Status(context.Background()); st.SystemBlocker == nil || st.SystemBlocker.Kind != "devtools_attached" {
		t.Fatalf("status blocker=%+v", st.SystemBlocker)
	}

	// A context with no universal blocker suppresses the connection rollup.
	b.contexts.Upsert(ContextMeta{Profile: "c", GroupID: 3, Name: "c", BrowserProfile: "alpha"})
	b.setTabOwner(13, "c")
	if sb := b.SystemBlocker(); sb != nil {
		t.Fatalf("non-universal blocker promoted: %+v", sb)
	}
}
