package browsercli

// Parity-exempt: the Go CLI's ensure path crosses a process boundary
// (detached spawn + /health wait); upstream bridge_host.py has no equivalent
// test. These cases pin the observable contract of
// ensure_bridge_host_running() for the Go transport.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/cli/browsersupervisor"
)

// killStubFromPidFile reaps the detached stub bridge a test spawned; the CLI's
// ensure path has no supervisor to do it.
func killStubFromPidFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return
	}
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
	}
}

// TestBrowserCLIEnsuresBridgeWithoutFlag is the cold-machine contract: a
// command with no explicit --bridge and nothing listening spawns the bridge
// (detached), waits for /health, and then runs — upstream's
// _run_with_bridge → ensure_bridge_host_running() behavior.
//
// Parity: tools/src/gcu/errors.py::_run_with_bridge
func TestBrowserCLIEnsuresBridgeWithoutFlag(t *testing.T) {
	addr, _ := freeTCPAddr(t)
	home := t.TempDir()
	pidFile := filepath.Join(home, "stub.pid")
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", addr)
	t.Setenv("HIVE_BROWSERBRIDGE_BIN", os.Args[0])
	t.Setenv("HIVE_TEST_BRIDGE_STUB", "1")
	t.Setenv("HIVE_TEST_STUB_ADDR", addr)
	t.Setenv("HIVE_TEST_STUB_MODE", "serve")
	t.Setenv("HIVE_TEST_STUB_PIDFILE", pidFile)
	t.Cleanup(func() { killStubFromPidFile(t, pidFile) })

	stdout, stderr, code := runBrowser(t, home, "status", "--json")
	if code != browserExitOK {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, stdout)
	}
	if payload["stub"] != true {
		t.Fatalf("result = %v, want the spawned bridge's answer", payload)
	}
	// The spawn is logged like upstream's ~/.hive/bridge_host.log.
	if _, err := os.Stat(filepath.Join(home, "bridge_host.log")); err != nil {
		t.Fatalf("expected the spawned bridge's log in HIVE_HOME: %v", err)
	}
	if data, err := os.ReadFile(pidFile); err != nil || len(data) == 0 {
		t.Fatalf("spawned bridge wrote no pid file (err=%v)", err)
	}
}

// TestEnsureBrowserBridgeReusesHealthyEndpoint pins the idempotent fast path:
// a bridge that already answers is never respawned (the binary resolver would
// fail if it were reached).
//
// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
func TestEnsureBrowserBridgeReusesHealthyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	ok := ensureBrowserBridge(server.URL, browserCLIEnsureConfig{
		LookupEnv:  func(string) string { return "" },
		Executable: func() (string, error) { return filepath.Join(t.TempDir(), "missing-hive"), nil },
		LookPath:   func(string) (string, error) { return "", fmt.Errorf("not found") },
	})
	if !ok {
		t.Fatal("a healthy endpoint must be reused")
	}
}

// TestEnsureBrowserBridgeGivesUpWhenSpawnFails bounds the wait: a child that
// dies instantly leaves the command to its normal not-connected exit code
// instead of hanging the CLI.
//
// Parity-exempt: upstream's ensure waits on a control-port probe with the same
// 8s budget but has no test for a child that never comes up.
func TestEnsureBrowserBridgeGivesUpWhenSpawnFails(t *testing.T) {
	started := time.Now()
	ok := ensureBrowserBridge("http://127.0.0.1:1", browserCLIEnsureConfig{
		LookupEnv: func(key string) string {
			if key == browsersupervisor.BinEnv {
				return "/bin/false" // starts and exits 1, never serving
			}
			return ""
		},
		Client: &http.Client{Timeout: 200 * time.Millisecond},
		Wait:   400 * time.Millisecond,
	})
	if ok {
		t.Fatal("ensure reported a bridge that never became healthy")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("ensure waited %s, want the bounded 400ms budget", elapsed)
	}
}

// TestBrowserCLIExplicitBridgeOptsOutOfEnsure keeps the Go-only --bridge
// override honest: pointing at another endpoint must not spawn the default
// bridge.
//
// Parity-exempt: --bridge has no upstream counterpart, so neither does its
// opt-out rule; upstream always ensures its one bridge.
func TestBrowserCLIExplicitBridgeOptsOutOfEnsure(t *testing.T) {
	home := t.TempDir()
	deadAddr, _ := freeTCPAddr(t)
	marker := filepath.Join(home, "spawned")
	script := filepath.Join(home, "fake-browserbridge")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatalf("write fake bridge: %v", err)
	}
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_BROWSERBRIDGE_BIN", script)

	_, _, code := runBrowser(t, home, "status", "--json", "--bridge", "http://"+deadAddr)
	if code != browserExitNotConnected {
		t.Fatalf("exit = %d, want %d for an unreachable explicit bridge", code, browserExitNotConnected)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("an explicit --bridge must not spawn the default bridge")
	}
}
