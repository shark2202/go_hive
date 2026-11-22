package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/shark2202/go_hive/browserbridge"
	"github.com/shark2202/go_hive/browserbridge/internal/wstest"
)

func TestParseFlagsDefaults(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if o.ws != browserbridge.DefaultWSAddr || o.httpAddr != browserbridge.DefaultHTTPAddr {
		t.Fatalf("addrs=%+v", o)
	}
	if o.controlAddr != browserbridge.DefaultControlAddr {
		t.Fatalf("control addr=%q, want %q", o.controlAddr, browserbridge.DefaultControlAddr)
	}
	if o.idleGrace != 30*time.Second || o.idleCheck != 10*time.Second || o.noIdleExit {
		t.Fatalf("idle watchdog defaults=%+v", o)
	}
	if o.legacy != browserbridge.DefaultLegacyAddr || o.noLegacy {
		t.Fatalf("legacy defaults=%+v", o)
	}
	if o.legacyStatus != browserbridge.DefaultLegacyStatusAddr {
		t.Fatalf("legacy status default=%q, want %q", o.legacyStatus, browserbridge.DefaultLegacyStatusAddr)
	}
	cfg := o.config()
	if cfg.LegacyAddr != browserbridge.DefaultLegacyAddr {
		t.Fatalf("config legacy=%q", cfg.LegacyAddr)
	}
	if cfg.LegacyStatusAddr != browserbridge.DefaultLegacyStatusAddr {
		t.Fatalf("config legacy status=%q", cfg.LegacyStatusAddr)
	}
	// The WS-layer keepalive is a Config zero-value default (20s/20s in
	// browserbridge.withDefaults); the flags must carry that default through.
	if cfg.WSPingInterval != browserbridge.DefaultWSPingInterval || cfg.WSPingTimeout != browserbridge.DefaultWSPingTimeout {
		t.Fatalf("WS keepalive flags = %v/%v, want %v/%v", cfg.WSPingInterval, cfg.WSPingTimeout,
			browserbridge.DefaultWSPingInterval, browserbridge.DefaultWSPingTimeout)
	}
	if cfg.ArtifactDir != "" || len(cfg.SkillDirs) != 0 || len(cfg.ScriptCommand) != 0 {
		t.Fatalf("unexpected config=%+v", cfg)
	}
}

// TestDesktopParentCheckDefaultMatchesUpstream pins parity item 5: the parent
// death budget is upstream's 5s × 2 confirmations (bridge_host.py
// _PARENT_CHECK_INTERVAL_S / _PARENT_DEATH_CONFIRMATIONS), not the old 400ms
// deliberate acceleration. -parent-check still overrides it.
//
// Parity: tools/src/gcu/bridge_host.py::_watch_parent
func TestDesktopParentCheckDefaultMatchesUpstream(t *testing.T) {
	if desktopParentCheckDefault != 5*time.Second {
		t.Fatalf("desktopParentCheckDefault = %s, want upstream's 5s", desktopParentCheckDefault)
	}
	if desktopParentMisses != 2 {
		t.Fatalf("desktopParentMisses = %d, want upstream's 2 confirmations", desktopParentMisses)
	}
	o, err := parseFlags([]string{"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-no-legacy", "-no-sweep"})
	if err != nil {
		t.Fatal(err)
	}
	if o.parentCheck != desktopParentCheckDefault {
		t.Fatalf("parseFlags default parent-check = %s, want %s", o.parentCheck, desktopParentCheckDefault)
	}
	short, err := parseFlags([]string{"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-no-legacy", "-no-sweep", "-parent-check", "20ms"})
	if err != nil {
		t.Fatal(err)
	}
	if short.parentCheck != 20*time.Millisecond {
		t.Fatalf("-parent-check override = %s, want 20ms", short.parentCheck)
	}
}

func TestParseFlagsDisablesLegacyAndParsesLists(t *testing.T) {
	o, err := parseFlags([]string{
		"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-no-legacy",
		"-skills", "/a/skills, /b/skills", "-script-command", "python3",
		"-telemetry-dir", "/tmp/tel", "-rate-limits", "-no-sweep",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := o.config()
	if cfg.LegacyAddr != "" {
		t.Fatalf("legacy should be disabled, got %q", cfg.LegacyAddr)
	}
	if cfg.LegacyStatusAddr != "" {
		t.Fatalf("legacy status should be disabled, got %q", cfg.LegacyStatusAddr)
	}
	if len(cfg.SkillDirs) != 2 || cfg.SkillDirs[1] != "/b/skills" {
		t.Fatalf("skills=%+v", cfg.SkillDirs)
	}
	if len(cfg.ScriptCommand) != 1 || cfg.ScriptCommand[0] != "python3" {
		t.Fatalf("script command=%+v", cfg.ScriptCommand)
	}
	if cfg.TelemetryDir != "/tmp/tel" || !cfg.EnableRateLimits || !cfg.SweepDisabled {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	if _, err := parseFlags([]string{"-not-a-flag"}); err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
	if _, err := parseFlags([]string{"stray"}); err == nil {
		t.Fatal("expected an error for a positional argument")
	}
}

func TestBridgeBindsRequestedListeners(t *testing.T) {
	o, err := parseFlags([]string{"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-control", "127.0.0.1:0", "-no-legacy", "-no-sweep"})
	if err != nil {
		t.Fatal(err)
	}
	b := newBridge(o)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.Shutdown(ctx)
	}()
	if b.WSAddr() == "" || b.HTTPAddr() == "" {
		t.Fatalf("addrs ws=%q http=%q", b.WSAddr(), b.HTTPAddr())
	}
	if b.LegacyAddr() != "" {
		t.Fatalf("legacy bound despite -no-legacy: %q", b.LegacyAddr())
	}
}

func TestRunExitsCleanlyOnContextCancel(t *testing.T) {
	o, err := parseFlags([]string{"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-control", "127.0.0.1:0", "-no-legacy", "-no-sweep"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, o) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after context cancellation")
	}
}

func TestProbeHealthEndpoint(t *testing.T) {
	o, err := parseFlags([]string{"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-control", "127.0.0.1:0", "-no-legacy", "-no-sweep"})
	if err != nil {
		t.Fatal(err)
	}
	b := newBridge(o)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := probe(context.Background(), "http://"+b.HTTPAddr()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.Shutdown(ctx)
	if err := probe(context.Background(), "http://"+b.HTTPAddr()); err == nil {
		t.Fatal("probe succeeded against a stopped bridge")
	}
}

// freeAddr reserves a loopback port and releases it, so a test can hand the
// bridge a concrete address to probe.
func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return addr
}

func waitHealth(t *testing.T, addr string, want bool, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + addr + "/health")
		alive := err == nil && resp.StatusCode == http.StatusOK
		if resp != nil {
			_ = resp.Body.Close()
		}
		if alive == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("bridge on %s: health=%v never reached %v", addr, !want, want)
}

// TestIdleWatchdogExits pins the upstream idle contract: with no control-RPC
// client and no desktop parent, the bridge exits on its own after the grace.
func TestIdleWatchdogExits(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_PARENT_PID", "")
	o, err := parseFlags([]string{
		"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-control", "127.0.0.1:0",
		"-no-legacy", "-no-sweep", "-idle-grace", "150ms", "-idle-check", "25ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), o) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the idle watchdog never fired")
	}
}

// TestIdleWatchdogHoldsWhileDesktopAlive covers the side-panel case: the
// desktop consumes the bridge even with no tool client attached.
func TestIdleWatchdogHoldsWhileDesktopAlive(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_PARENT_PID", strconv.Itoa(os.Getpid()))
	o, err := parseFlags([]string{
		"-ws", "127.0.0.1:0", "-http", "127.0.0.1:0", "-control", "127.0.0.1:0",
		"-no-legacy", "-no-sweep", "-idle-grace", "60ms", "-idle-check", "20ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, o) }()
	select {
	case err := <-done:
		t.Fatalf("bridge exited while the desktop parent was alive: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
}

// TestIdleWatchdogHoldsWhileControlClientConnected proves a connected control
// client suppresses the idle exit even with no desktop parent.
func TestIdleWatchdogHoldsWhileControlClientConnected(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_PARENT_PID", "")
	httpAddr := freeAddr(t)
	controlAddr := freeAddr(t)
	o, err := parseFlags([]string{
		"-ws", "127.0.0.1:0", "-http", httpAddr, "-control", controlAddr,
		"-no-legacy", "-no-sweep", "-idle-grace", "80ms", "-idle-check", "20ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, o) }()
	waitHealth(t, httpAddr, true, 5*time.Second)

	// /status must report the control listener.
	resp, err := http.Get("http://" + httpAddr + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var status map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decode status: %v", err)
	}
	_ = resp.Body.Close()
	listening, _ := status["listening"].(map[string]any)
	if listening["control_rpc"] != true {
		t.Fatalf("control rpc not listening: %+v", status["listening"])
	}

	// Connect a control client; its presence alone keeps the bridge up.
	client, err := wstest.Dial("ws://"+controlAddr+"/control", 2*time.Second)
	if err != nil {
		t.Fatalf("dial control: %v", err)
	}
	defer func() { _ = client.Kill() }()
	if err := client.WriteJSON(map[string]any{
		"rpc_id": "h1", "method": "client_hello", "args": []any{os.Getpid()}, "params": map[string]any{},
	}); err != nil {
		t.Fatalf("client_hello: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("bridge exited despite a live control client: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancellation")
	}
}

// TestShutdownRouteEndsTheProcess pins POST /shutdown: it answers 202 and the
// graceful exit path runs, which the supervisor reads as intentional.
func TestShutdownRouteEndsTheProcess(t *testing.T) {
	t.Setenv("HIVE_DESKTOP_PARENT_PID", strconv.Itoa(os.Getpid()))
	httpAddr := freeAddr(t)
	o, err := parseFlags([]string{
		"-ws", "127.0.0.1:0", "-http", httpAddr, "-control", "127.0.0.1:0",
		"-no-legacy", "-no-sweep",
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), o) }()
	waitHealth(t, httpAddr, true, 5*time.Second)

	resp, err := http.Post("http://"+httpAddr+"/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || body["ok"] != true {
		t.Fatalf("shutdown = %d %+v", resp.StatusCode, body)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("POST /shutdown did not end the bridge")
	}
	waitHealth(t, httpAddr, false, 5*time.Second)
}

// parentStandIn launches a child that only sleeps, so a test can kill it and
// stand in for a dying desktop runtime. It is this test binary itself, which
// makes the fixture portable across the cross-build platforms.
// Parity-exempt: test-only stand-in process; upstream kills a live desktop pid.
func parentStandIn(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestParentStandInHelper")
	cmd.Env = append(os.Environ(), "BROWSERBRIDGE_PARENT_STANDIN=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start parent stand-in: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// TestParentStandInHelper is the sleeping child behind parentStandIn.
// Parity-exempt: test-only stand-in process; it asserts nothing.
func TestParentStandInHelper(t *testing.T) {
	if os.Getenv("BROWSERBRIDGE_PARENT_STANDIN") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

// TestDesktopParentWatchExits pins the upstream _watch_parent contract: when
// HIVE_DESKTOP_PARENT_PID goes away the bridge exits on its own, well inside
// the ≤2 s budget, instead of waiting out the idle grace. The idle watchdog is
// disabled here so the exit can only come from the parent watch.
//
// Parity: tools/src/gcu/bridge_host.py::_watch_parent
func TestDesktopParentWatchExits(t *testing.T) {
	parent := parentStandIn(t)
	httpAddr := freeAddr(t)
	o, err := parseFlags([]string{
		"-ws", "127.0.0.1:0", "-http", httpAddr, "-control", "127.0.0.1:0",
		"-no-legacy", "-no-sweep", "-no-idle-exit", "-parent-check", "20ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_DESKTOP_PARENT_PID", strconv.Itoa(parent.Process.Pid))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, o) }()
	waitHealth(t, httpAddr, true, 5*time.Second)

	// Reap the stand-in first: a zombie still answers signal 0, so an
	// unreaped child would (correctly) count as alive for the bridge.
	started := time.Now()
	_ = parent.Process.Kill()
	_ = parent.Wait()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit after its desktop parent died")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("bridge took %s to exit after the parent died, want ≤ 2s", elapsed)
	}
}
