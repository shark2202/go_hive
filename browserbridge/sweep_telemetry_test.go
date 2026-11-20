package browserbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaleSweepSoftPrunesAndReapsOrphanGroup(t *testing.T) {
	b := newTestBridge(t, Config{
		SweepInterval:    25 * time.Millisecond,
		DefaultLabelFile: filepath.Join(t.TempDir(), "default.json"),
	})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}

	// Chrome reports no groups: the sweep soft-prunes the registry entry but
	// keeps the agent identity for a later hand-over.
	waitFor(t, 2*time.Second, func() bool {
		meta, _ := b.contexts.Get("queen-1")
		return meta.GroupID == 0
	}, "sweep soft prune")

	// A marked Hive group no context claims is reaped after two sweeps.
	h.setGroups(map[string]any{"id": 900, "title": "Hive Agent" + hiveGroupMarker})
	waitFor(t, 2*time.Second, func() bool {
		destroy, _, _ := h.counts()
		return destroy >= 1
	}, "forward orphan reap")
	if _, ok := b.contexts.ByGroup(900); ok {
		t.Fatal("orphan group was registered")
	}
}

func TestUngroupedOrphanReap(t *testing.T) {
	b := newTestBridge(t, Config{
		SweepInterval:    25 * time.Millisecond,
		DefaultLabelFile: filepath.Join(t.TempDir(), "default.json"),
	})
	h := newL2Harness()
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{"agent_id": "queen-1", "browser_profile": "alpha"}); err != nil {
		t.Fatal(err)
	}
	h.setGroups(map[string]any{"id": 101, "title": "Queen" + hiveGroupMarker})

	if err := f.client.WriteJSON(map[string]any{
		"type": "tab_event", "event": "created", "tabId": 777, "groupId": 101,
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return b.tabOwnerOf(777) == "queen-1" }, "hive tab attribution")
	// Simulate the tab escaping its group: it remains in the durable Hive set
	// but no longer has an agent owner, and Chrome reports it ungrouped.
	b.setTabOwner(777, "")
	h.setUngrouped(777)

	waitFor(t, 2*time.Second, func() bool {
		for _, tab := range h.closedTabs() {
			if tab == 777 {
				return true
			}
		}
		return false
	}, "ungrouped orphan close")
}

func TestRateLimitRejectsRepeatedLinkedInProfileView(t *testing.T) {
	b := newTestBridge(t, Config{
		SweepDisabled:       true,
		EnableRateLimits:    true,
		RateLimitPath:       filepath.Join(t.TempDir(), "limits.json"),
		RateLimitConfigPath: filepath.Join(t.TempDir(), "configuration.json"),
		DefaultLabelFile:    filepath.Join(t.TempDir(), "default.json"),
	})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	nav := func(url string) map[string]any {
		res, err := b.Execute(context.Background(), "alpha", "browser_navigate", map[string]any{
			"tab_id": 11, "url": url, "wait_until": "load",
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.(map[string]any)
	}

	first := nav("https://www.linkedin.com/in/someone/")
	if first["ok"] != true {
		t.Fatalf("first navigation=%+v", first)
	}
	second := nav("https://www.linkedin.com/in/someone-else/")
	if second["ok"] != false || second["error"] != "rate_limited" {
		t.Fatalf("second navigation should be rate limited: %+v", second)
	}
	if second["platform"] != "linkedin" || second["reason"] != "too_fast" {
		t.Fatalf("decision=%+v", second)
	}
	if retry, _ := second["retry_after_seconds"].(float64); retry <= 0 {
		t.Fatalf("retry_after_seconds=%v", second["retry_after_seconds"])
	}
	if _, ok := second["guidance"].(string); !ok {
		t.Fatalf("missing guidance: %+v", second)
	}

	// Non-social navigation is unaffected.
	third := nav("https://example.com/other")
	if third["ok"] != true {
		t.Fatalf("example navigation=%+v", third)
	}
	data, err := os.ReadFile(b.cfg.RateLimitPath)
	if err != nil {
		t.Fatalf("rate limit store not written: %v", err)
	}
	if !strings.Contains(string(data), `"linkedin"`) {
		t.Fatalf("store=%s", data)
	}
}

func TestTelemetryJSONLToolCallRedactionAndContextEvents(t *testing.T) {
	dir := t.TempDir()
	b := newTestBridge(t, Config{SweepDisabled: true, TelemetryDir: dir})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	if _, err := b.Execute(context.Background(), "alpha", "browser_tabs", map[string]any{
		"browser_profile": "alpha", "password": "hunter2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "", "create_context", map[string]any{
		"agent_id": "queen-1", "browser_profile": "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	entries := waitForTelemetry(t, dir, 2*time.Second, func(rows []map[string]any) bool {
		var tool, hello, ctx bool
		for _, row := range rows {
			switch row["type"] {
			case "tool_call":
				if row["tool"] == "browser_tabs" {
					tool = true
				}
			case "connection_event":
				if row["event"] == "hello" {
					hello = true
				}
			case "context_event":
				if row["event"] == "create" {
					ctx = true
				}
			}
		}
		return tool && hello && ctx
	})
	var toolRow map[string]any
	for _, row := range entries {
		if row["type"] == "tool_call" && row["tool"] == "browser_tabs" {
			toolRow = row
			break
		}
	}
	if toolRow == nil {
		t.Fatalf("no tool_call row: %+v", entries)
	}
	params, _ := toolRow["params"].(map[string]any)
	if params["password"] != "***REDACTED***" {
		t.Fatalf("password not redacted: %+v", params)
	}
	if params["browser_profile"] != "alpha" {
		t.Fatalf("params=%+v", params)
	}
}

func TestTabActionsRingBufferAndEndpoint(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	h := newL2Harness()
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), h.handle)
	waitFor(t, 2*time.Second, func() bool { _, err := b.resolve("alpha", nil); return err == nil }, "hello")

	if _, err := b.Execute(context.Background(), "alpha", "browser_navigate", map[string]any{
		"tab_id": 11, "url": "https://example.com/",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "", "record_action", map[string]any{
		"tab_id": 11, "verb": "click", "target": "#go", "ok": true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "", "record_action", map[string]any{
		"tab_id": 11, "verb": "type", "target": "#q", "ok": false,
	}); err != nil {
		t.Fatal(err)
	}

	base := "http://" + b.HTTPAddr()
	code, payload := httpJSON(t, "GET", base+"/tabs/11/actions?limit=2", "")
	if code != 200 || payload["ok"] != true {
		t.Fatalf("actions=%d %+v", code, payload)
	}
	rows, _ := payload["actions"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", payload["actions"])
	}
	first := rows[0].(map[string]any)
	if first["verb"] != "type" || first["ok"] != false {
		t.Fatalf("newest-first violated: %+v", first)
	}
	if payload["last_action_ts_ms"] == nil {
		t.Fatalf("last_action_ts_ms missing: %+v", payload)
	}

	// since_ms filters to strictly newer entries (incremental panel poll).
	last := int64(payload["last_action_ts_ms"].(float64))
	code, payload = httpJSON(t, "GET", base+"/tabs/11/actions?since="+itoa(int(last)), "")
	if code != 200 {
		t.Fatalf("since actions=%d", code)
	}
	if rows, _ := payload["actions"].([]any); len(rows) != 0 {
		t.Fatalf("since filter returned %+v", rows)
	}
}
