package browserbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newTestBridge(t *testing.T, cfg Config) *Bridge {
	t.Helper()
	cfg.WSAddr = "127.0.0.1:0"
	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.LegacyAddr = ""
	if cfg.Logf == nil {
		cfg.Logf = t.Logf
	}
	b := New(cfg)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.Shutdown(ctx)
	})
	return b
}

func testTabListHandler(cmd fakeCommand) (any, string) {
	switch cmd.Type {
	case "tab.list":
		return map[string]any{"tabs": []any{
			map[string]any{"id": 11, "url": "https://a.example/", "title": "A", "groupId": -1},
			map[string]any{"id": 12, "url": "https://b.example/", "title": "B", "groupId": -1},
		}}, ""
	case "tab.activate", "tab.close":
		return map[string]any{"ok": true}, ""
	}
	return map[string]any{"ok": true}, ""
}

func helloFor(label string) map[string]any {
	return map[string]any{
		"type":            "hello",
		"version":         "1.7.4",
		"protocolVersion": 6,
		"extensionId":     "ext-" + label + "-0123456789",
		"profileLabel":    label,
	}
}

func TestHelloRegistersProfileAndStatus(t *testing.T) {
	b := newTestBridge(t, Config{})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)

	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello registration")

	st := b.Status(context.Background())
	if !st.Connected || len(st.Connections) != 1 {
		t.Fatalf("status=%+v", st)
	}
	c := st.Connections[0]
	if c.Label != "alpha" || c.Version != "1.7.4" || c.ProtocolVersion != 6 {
		t.Fatalf("connection=%+v", c)
	}
	if st.TabsTotal != 2 || st.Tabs["alpha"] != 2 {
		t.Fatalf("tab counts=%v total=%d", st.Tabs, st.TabsTotal)
	}
	if f.count("tab.list") == 0 {
		t.Fatal("status did not issue tab.list")
	}
	if st.Port != portOf(b.WSAddr()) || st.Listening.WS != true || st.Listening.Status != true {
		t.Fatalf("listener status=%+v port=%d", st.Listening, st.Port)
	}
}

func TestHelloFallsBackToSyntheticLabel(t *testing.T) {
	b := newTestBridge(t, Config{})
	startFakeExtension(t, b.WSAddr(), map[string]any{
		"type":            "hello",
		"version":         "1.7.4",
		"protocolVersion": 6,
		"extensionId":     "abcdef1234567890",
	}, testTabListHandler)
	want := "profile-abcdef12"
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve(want, nil)
		return err == nil
	}, "synthetic label")
}

func TestAppPingPongKeepsConnectionAlive(t *testing.T) {
	b := newTestBridge(t, Config{PingInterval: 40 * time.Millisecond, PongTimeout: 200 * time.Millisecond})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool { return f.pongs.Load() > 0 }, "first pong")
	time.Sleep(250 * time.Millisecond)
	if _, err := b.resolve("alpha", nil); err != nil {
		t.Fatalf("connection should still be alive: %v", err)
	}
	if f.pongs.Load() < 2 {
		t.Fatalf("expected repeated pings, got %d pongs", f.pongs.Load())
	}
}

func TestPingTimeoutDisconnects(t *testing.T) {
	b := newTestBridge(t, Config{PingInterval: 30 * time.Millisecond, PongTimeout: 100 * time.Millisecond})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	f.ignorePings = true
	f.pongs.Store(0)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err != nil
	}, "ping timeout removal")
	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("fake extension socket was not closed")
	}
}

func TestCommandRoundTrip(t *testing.T) {
	b := newTestBridge(t, Config{})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	res, err := b.Execute(context.Background(), "alpha", "browser_tabs", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["ok"] != true || int(m["total"].(int)) != 2 {
		t.Fatalf("result=%+v", m)
	}
	cmd := f.waitForCommand("tab.list", time.Second)
	if cmd.Params["id"] == "" || cmd.Params["type"] != "tab.list" {
		t.Fatalf("wire shape=%v", cmd.Params)
	}
}

func TestDisconnectFailsInFlightRequest(t *testing.T) {
	received := make(chan struct{}, 1)
	release := make(chan struct{})
	handler := func(cmd fakeCommand) (any, string) {
		if cmd.Type == "tab.list" {
			received <- struct{}{}
			<-release
		}
		return map[string]any{"ok": true}, ""
	}
	b := newTestBridge(t, Config{CommandTimeout: 10 * time.Second})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), handler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")

	errCh := make(chan error, 1)
	go func() {
		_, err := b.Execute(context.Background(), "alpha", "browser_tabs", nil)
		errCh <- err
	}()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("command never reached extension")
	}
	_ = f.client.Kill()
	select {
	case err := <-errCh:
		if err == nil || AsError(err).Code != CodeConnectionLost {
			t.Fatalf("want connection_lost, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not fail on disconnect")
	}
	close(release)
}

func TestDuplicateProfileDisplacesOldConnection(t *testing.T) {
	b := newTestBridge(t, Config{})
	first := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "first hello")
	second := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		select {
		case <-first.done:
			return true
		default:
			return false
		}
	}, "first connection displacement")

	b.mu.RLock()
	active := b.conns["alpha"]
	b.mu.RUnlock()
	if active == nil {
		t.Fatal("no active alpha connection")
	}
	if _, err := b.Execute(context.Background(), "alpha", "browser_tabs", nil); err != nil {
		t.Fatal(err)
	}
	if second.count("tab.list") == 0 {
		t.Fatal("new connection did not receive command")
	}
}

func TestFirstComeClaimsDefault(t *testing.T) {
	b := newTestBridge(t, Config{})
	alpha := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	beta := startFakeExtension(t, b.WSAddr(), helloFor("beta"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.conns) == 2
	}, "two connections")
	// Upstream default routing is starred-default, else first-come: the
	// earliest-connected profile wins without an explicit star.
	if _, err := b.Execute(context.Background(), "", "browser_tabs", nil); err != nil {
		t.Fatalf("default routing failed: %v", err)
	}
	if alpha.count("tab.list") == 0 {
		t.Fatal("earliest connection did not receive the default-routed command")
	}
	if beta.count("tab.list") != 0 {
		t.Fatal("default routing leaked to the later connection")
	}
	// An explicit unknown label with multiple connected profiles is ambiguous.
	_, err := b.Execute(context.Background(), "gamma", "browser_tabs", nil)
	if err == nil || AsError(err).Code != CodeNoBrowserProfile {
		t.Fatalf("want no_browser_profile, got %v", err)
	}
	// But the sole-connection fallback still applies when only one is live.
	if _, err := b.Execute(context.Background(), "beta", "browser_tabs", nil); err != nil {
		t.Fatal(err)
	}
}

func TestTabRoutingAcrossProfiles(t *testing.T) {
	b := newTestBridge(t, Config{})
	alpha := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), func(cmd fakeCommand) (any, string) {
		if cmd.Type == "tab.list" {
			return map[string]any{"tabs": []any{map[string]any{"id": 11, "groupId": -1}}}, ""
		}
		return map[string]any{"ok": true}, ""
	})
	beta := startFakeExtension(t, b.WSAddr(), helloFor("beta"), func(cmd fakeCommand) (any, string) {
		if cmd.Type == "tab.list" {
			return map[string]any{"tabs": []any{map[string]any{"id": 22, "groupId": -1}}}, ""
		}
		if cmd.Type == "tab.activate" {
			return map[string]any{"ok": true}, ""
		}
		return map[string]any{"ok": true}, ""
	})
	waitFor(t, 2*time.Second, func() bool {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return len(b.conns) == 2
	}, "two connections")
	if _, err := b.Execute(context.Background(), "alpha", "browser_tabs", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "beta", "browser_tabs", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), "", "browser_activate_tab", map[string]any{"tab_id": 22}); err != nil {
		t.Fatal(err)
	}
	if beta.count("tab.activate") != 1 {
		t.Fatalf("beta tab.activate count=%d, alpha=%d", beta.count("tab.activate"), alpha.count("tab.activate"))
	}
	if alpha.count("tab.activate") != 0 {
		t.Fatal("tab command misrouted to alpha")
	}
}

func httpJSON(t *testing.T, method, url string, body string) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("invalid JSON response (%d): %s", resp.StatusCode, data)
	}
	return resp.StatusCode, out
}

func TestHTTPAPI(t *testing.T) {
	b := newTestBridge(t, Config{})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	base := "http://" + b.HTTPAddr()

	code, health := httpJSON(t, "GET", base+"/health", "")
	if code != 200 || health["ok"] != true || health["connections"].(float64) != 1 {
		t.Fatalf("health=%d %+v", code, health)
	}

	code, st := httpJSON(t, "GET", base+"/status", "")
	if code != 200 || st["connected"] != true {
		t.Fatalf("status=%d %+v", code, st)
	}

	code, res := httpJSON(t, "POST", base+"/command",
		`{"type":"browser_tabs","params":{"browser_profile":"alpha"}}`)
	if code != 200 || res["ok"] != true {
		t.Fatalf("command=%d %+v", code, res)
	}
	if f.count("tab.list") == 0 {
		t.Fatal("HTTP command did not reach extension")
	}

	code, errRes := httpJSON(t, "POST", base+"/command", `{"type":"nope"}`)
	if code != 400 || errRes["code"] != CodeUnknownCommand {
		t.Fatalf("unknown command=%d %+v", code, errRes)
	}

	code, errRes = httpJSON(t, "POST", base+"/command", `{not json`)
	if code != 400 || errRes["code"] != CodeBadRequest {
		t.Fatalf("bad body=%d %+v", code, errRes)
	}

	code, errRes = httpJSON(t, "GET", base+"/command", "")
	if code != 400 || errRes["code"] != CodeBadRequest {
		t.Fatalf("method=%d %+v", code, errRes)
	}
}

func TestHTTPNotConnected(t *testing.T) {
	b := newTestBridge(t, Config{})
	base := "http://" + b.HTTPAddr()
	code, res := httpJSON(t, "POST", base+"/command", `{"type":"browser_tabs"}`)
	if code != 503 || res["code"] != CodeNotConnected {
		t.Fatalf("not connected=%d %+v", code, res)
	}
}

func TestUnknownCommandListsUpstreamNames(t *testing.T) {
	b := newTestBridge(t, Config{})
	_, err := b.Execute(context.Background(), "", "not_a_tool", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := AsError(err).Message
	if !strings.Contains(msg, "browser_tabs") || !strings.Contains(msg, "browser_evaluate") {
		t.Fatalf("unhelpful message: %s", msg)
	}
}

func TestHTTPResponseContentType(t *testing.T) {
	b := newTestBridge(t, Config{})
	resp, err := http.Get("http://" + b.HTTPAddr() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte(`"ok":true`)) {
		t.Fatalf("body=%s", body)
	}
}
