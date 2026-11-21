package browserbridge

// Regression: the WS-layer heartbeat. Upstream's bridge serves the extension
// relay with websockets.serve(ping_interval=_WS_PING_INTERVAL_S,
// ping_timeout=_WS_PING_TIMEOUT_S) (bridge.py: 20s/20s), so a half-open TCP
// path is detected even when the app-level JSON ping cannot help. The Go port
// used to run only the 5s/12s app-level ping; these cases pin the new ticker
// on both extension listeners (primary + legacy).

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

func bridgeConnCount(b *Bridge) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.conns)
}

// newTestBridgeLegacy is newTestBridge without the legacy disable: the two
// pre-migration ports bind ephemeral addresses the test can dial.
func newTestBridgeLegacy(t *testing.T, cfg Config) *Bridge {
	t.Helper()
	cfg.WSAddr = "127.0.0.1:0"
	cfg.HTTPAddr = "127.0.0.1:0"
	cfg.ControlAddr = "127.0.0.1:0"
	if cfg.LegacyAddr == "" {
		cfg.LegacyAddr = "127.0.0.1:0"
	}
	if cfg.LegacyStatusAddr == "" {
		cfg.LegacyStatusAddr = "127.0.0.1:0"
	}
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

func waitBridgeConns(t *testing.T, b *Bridge, want int, timeout time.Duration, what string) {
	t.Helper()
	waitFor(t, timeout, func() bool { return bridgeConnCount(b) == want }, what)
}

// Parity: tools/src/gcu/browser/bridge.py::_serve_ws (ping_interval/ping_timeout)
func TestBridgeWSKeepaliveClosesExtensionThatIgnoresPings(t *testing.T) {
	b := newTestBridge(t, Config{
		WSPingInterval: 40 * time.Millisecond,
		WSPingTimeout:  40 * time.Millisecond,
	})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	f.ignoreWSPings.Store(true)

	waitBridgeConns(t, b, 1, 2*time.Second, "extension registration")
	waitBridgeConns(t, b, 0, 2*time.Second, "WS keepalive close of an extension that never pongs")

	select {
	case <-f.done:
	case <-time.After(2 * time.Second):
		t.Fatal("fake extension socket stayed open after the keepalive close")
	}
}

// Parity: tools/src/gcu/browser/bridge.py::_serve_ws (the pong-answered path)
func TestBridgeWSKeepaliveKeepsPongingExtensionConnected(t *testing.T) {
	b := newTestBridge(t, Config{
		WSPingInterval: 30 * time.Millisecond,
		WSPingTimeout:  30 * time.Millisecond,
	})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitBridgeConns(t, b, 1, 2*time.Second, "extension registration")

	// Five heartbeat cycles: anything that fails to pong would be gone by now.
	time.Sleep(150 * time.Millisecond)
	if f.wsPongs.Load() == 0 {
		t.Fatal("bridge never sent a WS-layer ping")
	}
	waitBridgeConns(t, b, 1, time.Second, "live connection across heartbeat cycles")
	if _, err := b.Execute(context.Background(), "alpha", "browser_tabs", nil); err != nil {
		t.Fatalf("command after heartbeat cycles: %v", err)
	}
}

// The legacy 9229 listener runs the same ticker: the heartbeat is a property
// of the extension relay, not of one port.
//
// Parity: tools/src/gcu/browser/bridge.py::start (legacy _serve_ws)
func TestLegacyListenerRunsWSKeepalive(t *testing.T) {
	b := newTestBridgeLegacy(t, Config{
		WSPingInterval: 40 * time.Millisecond,
		WSPingTimeout:  40 * time.Millisecond,
	})
	f := startFakeExtension(t, b.LegacyAddr(), helloFor("legacy"), testTabListHandler)
	f.ignoreWSPings.Store(true)

	waitBridgeConns(t, b, 1, 2*time.Second, "legacy extension registration")
	waitBridgeConns(t, b, 0, 2*time.Second, "legacy WS keepalive close")
}

// The default ticker exists (20s/20s) — a zero-value Config must not silently
// disable the heartbeat the audit flagged.
func TestBridgeWSKeepaliveDefaultsMatchUpstream(t *testing.T) {
	b := New(Config{})
	if b.cfg.WSPingInterval != 20*time.Second || b.cfg.WSPingTimeout != 20*time.Second {
		t.Fatalf("WS keepalive defaults = %v/%v, want 20s/20s",
			b.cfg.WSPingInterval, b.cfg.WSPingTimeout)
	}
}

// The legacy status payload is the same JSON the primary API serves, and it is
// reachable over a plain HTTP client (the runtime's fallback probe path).
func TestLegacyStatusJSONShape(t *testing.T) {
	b := newTestBridgeLegacy(t, Config{})
	addr := b.LegacyStatusAddr()
	if addr == "" {
		t.Fatal("legacy status listener did not bind")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "" {
		t.Fatalf("legacy status addr = %q (%v)", addr, err)
	}

	resp, err := httpGetJSON("http://" + addr + "/status")
	if err != nil {
		t.Fatal(err)
	}
	if resp["bridge"] != "running" {
		t.Fatalf("legacy /status bridge = %v", resp["bridge"])
	}
	if _, ok := resp["bind_errors"]; !ok {
		t.Fatalf("legacy /status lacks bind_errors: %v", resp)
	}
}

func httpGetJSON(url string) (map[string]any, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}
