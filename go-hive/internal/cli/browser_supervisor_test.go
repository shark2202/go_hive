package cli

// Parity-exempt: the upstream Python bridge is supervised by
// tools/src/gcu/bridge_host.py (a detached subprocess loop with no Python test
// of its own); these cases pin the Go equivalent — spawn, health wait, restart,
// reuse and shutdown — against a stub child, which Python has no analogue for.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// TestMain doubles as the stub bridge child: when the supervisor test spawns
// this test binary with HIVE_TEST_BRIDGE_STUB=1, it behaves like
// cmd/browserbridge (a /health endpoint, a ready line, SIGTERM death) instead
// of running tests.
// Parity-exempt: TestMain 兼作 stub child，是 Go 侧测试夹具，上游没有对应物。
func TestMain(m *testing.M) {
	if os.Getenv("HIVE_TEST_BRIDGE_STUB") == "1" {
		browserBridgeStubMain()
		return
	}
	os.Exit(m.Run())
}

func browserBridgeStubMain() {
	if os.Getenv("HIVE_TEST_STUB_MODE") == "exit-early" {
		fmt.Fprintln(os.Stderr, "stub: refusing to start")
		os.Exit(3)
	}
	addr := os.Getenv("HIVE_TEST_STUB_ADDR")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen http %s: %v\n", addr, err)
		os.Exit(1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeStubJSON(w, map[string]any{"ok": true, "status": "ok", "pid": os.Getpid()})
	})
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		writeStubJSON(w, map[string]any{"ok": true, "result": map[string]any{"ok": true, "stub": true}})
	})
	fmt.Printf("{\"ok\":true,\"ready\":true,\"pid\":%d,\"http\":%q}\n", os.Getpid(), addr)
	if os.Getenv("HIVE_TEST_STUB_MODE") == "idle-exit" {
		// Behave like the real binary's idle watchdog: answer /health, then
		// leave with exit code 0 so the supervisor parks instead of restarting.
		go func() { _ = http.Serve(listener, mux) }()
		time.Sleep(400 * time.Millisecond)
		return
	}
	_ = http.Serve(listener, mux)
}

func writeStubJSON(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// safeBuffer is a bytes.Buffer that the supervisor goroutine and the test can
// use at the same time.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(chunk)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeTCPAddr(t *testing.T) (string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return host + ":" + port, port
}

func waitForOutput(t *testing.T, out *safeBuffer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output never contained %q; got:\n%s", want, out.String())
}

func waitForHealth(t *testing.T, addr string, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 200 * time.Millisecond}
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

// TestBrowserSupervisorStartsAndStopsChild is the serve-side lifecycle: spawn
// the bridge, wait for /health, and take it down when serve exits.
//
// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
func TestBrowserSupervisorStartsAndStopsChild(t *testing.T) {
	addr, port := freeTCPAddr(t)
	t.Setenv("HIVE_BROWSER_BRIDGE", "1")
	t.Setenv("HIVE_BROWSERBRIDGE_BIN", os.Args[0])
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", addr)
	controlAddr, _ := freeTCPAddr(t)
	t.Setenv("HIVE_BROWSERBRIDGE_CONTROL", controlAddr)
	t.Setenv("HIVE_TEST_BRIDGE_STUB", "1")
	t.Setenv("HIVE_TEST_STUB_ADDR", addr)
	t.Setenv("HIVE_TEST_STUB_MODE", "serve")
	t.Setenv("HIVE_TEST_STUB_PORT", port)

	home := hivehome.Home{Root: t.TempDir()}
	out := &safeBuffer{}
	stop := startBrowserBridge(browserSupervisorConfig{Home: home, Out: out})
	defer stop()

	waitForHealth(t, addr, true, 5*time.Second)
	waitForOutput(t, out, "Browser bridge: ready on "+addr, 5*time.Second)

	stop()
	waitForHealth(t, addr, false, 5*time.Second)

	logPath := filepath.Join(home.Root, "browserbridge.log")
	if data, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(data), "browserbridge") {
		t.Fatalf("expected the child's log at %s (err=%v, data=%q)", logPath, err, string(data))
	}
}

// TestBrowserSupervisorRestartsAndGivesUp pins the bounded restart loop: a child
// that never becomes healthy is retried browserBridgeMaxRestart times, then the
// supervisor stops instead of looping forever.
//
// Parity-exempt: 上游 bridge_host 的 supervisor 是递归重启 worker，无次数上限（也没有测试）；
// Go 侧改成有界退避，属于本仓库的守护策略。
func TestBrowserSupervisorRestartsAndGivesUp(t *testing.T) {
	addr, _ := freeTCPAddr(t)
	t.Setenv("HIVE_BROWSER_BRIDGE", "1")
	t.Setenv("HIVE_BROWSERBRIDGE_BIN", os.Args[0])
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", addr)
	controlAddr, _ := freeTCPAddr(t)
	t.Setenv("HIVE_BROWSERBRIDGE_CONTROL", controlAddr)
	t.Setenv("HIVE_TEST_BRIDGE_STUB", "1")
	t.Setenv("HIVE_TEST_STUB_MODE", "exit-early")

	out := &safeBuffer{}
	stop := startBrowserBridge(browserSupervisorConfig{Home: hivehome.Home{Root: t.TempDir()}, Out: out})
	defer stop()

	waitForOutput(t, out, fmt.Sprintf("giving up after %d restarts", browserBridgeMaxRestart), 15*time.Second)
	if got := strings.Count(out.String(), "restarting in"); got != browserBridgeMaxRestart {
		t.Fatalf("restart count = %d, want %d; output:\n%s", got, browserBridgeMaxRestart, out.String())
	}
	if !strings.Contains(out.String(), "refusing to start") {
		t.Fatalf("expected the child's own error line in the supervisor output; got:\n%s", out.String())
	}
}

// TestBrowserSupervisorReusesRunningBridge covers the two-serve case: an
// already healthy bridge on the configured address is reused, never doubled.
//
// Parity-exempt: ensure_bridge_host_running 的「已在运行则复用」在 Go 侧改用 /health 探测，
// 上游没有对应测试。
func TestBrowserSupervisorReusesRunningBridge(t *testing.T) {
	addr, _ := freeTCPAddr(t)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStubJSON(w, map[string]any{"ok": true, "status": "ok"})
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	t.Setenv("HIVE_BROWSER_BRIDGE", "1")
	t.Setenv("HIVE_BROWSERBRIDGE_BIN", filepath.Join(t.TempDir(), "missing-browserbridge"))
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", addr)
	controlAddr, _ := freeTCPAddr(t)
	t.Setenv("HIVE_BROWSERBRIDGE_CONTROL", controlAddr)

	out := &safeBuffer{}
	stop := startBrowserBridge(browserSupervisorConfig{Home: hivehome.Home{Root: t.TempDir()}, Out: out})
	defer stop()

	if !strings.Contains(out.String(), "reusing the instance on "+addr) {
		t.Fatalf("expected a reuse notice, got: %q", out.String())
	}
	if strings.Contains(out.String(), "not started") {
		t.Fatalf("a healthy bridge must not trigger a spawn attempt: %q", out.String())
	}
}

// TestBrowserSupervisorPortBusyStops covers the hostile case: another process
// owns 14830, so the child cannot bind and the supervisor must stop instead of
// restart-storming.
//
// Parity-exempt: 上游没有「端口被占」的守护测试；这是 Go 侧守护策略的护栏。
func TestBrowserSupervisorPortBusyStops(t *testing.T) {
	addr, _ := freeTCPAddr(t)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	// A listener that is NOT a bridge: /health is 503, so the supervisor tries
	// to spawn and the child fails to bind.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not a bridge", http.StatusServiceUnavailable)
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	t.Setenv("HIVE_BROWSER_BRIDGE", "1")
	t.Setenv("HIVE_BROWSERBRIDGE_BIN", os.Args[0])
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", addr)
	controlAddr, _ := freeTCPAddr(t)
	t.Setenv("HIVE_BROWSERBRIDGE_CONTROL", controlAddr)
	t.Setenv("HIVE_TEST_BRIDGE_STUB", "1")
	t.Setenv("HIVE_TEST_STUB_ADDR", addr)
	t.Setenv("HIVE_TEST_STUB_MODE", "serve")

	out := &safeBuffer{}
	stop := startBrowserBridge(browserSupervisorConfig{Home: hivehome.Home{Root: t.TempDir()}, Out: out})
	defer stop()

	waitForOutput(t, out, "leaving the existing listener", 15*time.Second)
	if strings.Contains(out.String(), "restarting in") {
		t.Fatalf("a busy port must not be retried; output:\n%s", out.String())
	}
}

// TestBrowserBridgeRequestedSwitch pins the switch precedence: env > gcu_enabled
// > default on.
//
// Parity-exempt: 上游没有开关单测；gcu_enabled 由 quickstart.sh 直接写 configuration.json。
func TestBrowserBridgeRequestedSwitch(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeConfig := func(body string) {
		if err := os.WriteFile(home.ConfigurationFile(), []byte(body), 0o644); err != nil {
			t.Fatalf("write configuration.json: %v", err)
		}
	}

	cases := []struct {
		name  string
		body  string
		env   string
		want  bool
		setup func()
	}{
		{name: "default on without a config file", want: true},
		{name: "gcu_enabled true", body: `{"gcu_enabled": true}`, want: true},
		{name: "gcu_enabled false opts out", body: `{"gcu_enabled": false}`, want: false},
		{name: "env forces on over config", body: `{"gcu_enabled": false}`, env: "1", want: true},
		{name: "env forces off over config", body: `{"gcu_enabled": true}`, env: "0", want: false},
		{name: "env off spelled out", body: `{"gcu_enabled": true}`, env: "off", want: false},
		{name: "garbage env falls through to config", body: `{"gcu_enabled": false}`, env: "maybe", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.body == "" {
				_ = os.Remove(home.ConfigurationFile())
			} else {
				writeConfig(testCase.body)
			}
			lookup := func(key string) string {
				if key == browserBridgeSwitchEnv {
					return testCase.env
				}
				return ""
			}
			if got := browserBridgeRequested(home, lookup); got != testCase.want {
				t.Fatalf("browserBridgeRequested = %v, want %v (env=%q body=%q)", got, testCase.want, testCase.env, testCase.body)
			}
		})
	}
}

// TestResolveBrowserBridgeBinary pins the lookup order: env, sibling of the
// hive binary, $PATH, then a readable error.
//
// Parity-exempt: Go 侧二进制解析（发行布局/环境变量），上游是 Python 包内模块路径，无等价物。
func TestResolveBrowserBridgeBinary(t *testing.T) {
	dir := t.TempDir()
	envBinary := filepath.Join(dir, "from-env")
	if err := os.WriteFile(envBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write env binary: %v", err)
	}
	sibling := filepath.Join(dir, browserBridgeBinaryNames()[0])
	if err := os.WriteFile(sibling, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write sibling binary: %v", err)
	}

	lookupEnv := func(key string) string {
		if key == browserBridgeBinEnv {
			return envBinary
		}
		return ""
	}
	cfg := browserSupervisorConfig{
		LookupEnv:  lookupEnv,
		Executable: func() (string, error) { return filepath.Join(dir, "hive"), nil },
		LookPath:   func(string) (string, error) { return "", fmt.Errorf("not found") },
	}
	if got, err := resolveBrowserBridgeBinary(cfg); err != nil || got != envBinary {
		t.Fatalf("env binary: got (%q, %v), want %q", got, err, envBinary)
	}

	cfg.LookupEnv = func(string) string { return "" }
	if got, err := resolveBrowserBridgeBinary(cfg); err != nil || got != sibling {
		t.Fatalf("sibling binary: got (%q, %v), want %q", got, err, sibling)
	}

	cfg.Executable = func() (string, error) { return filepath.Join(dir, "missing", "hive"), nil }
	cfg.LookPath = func(string) (string, error) { return "/opt/bin/browserbridge", nil }
	if got, err := resolveBrowserBridgeBinary(cfg); err != nil || got != "/opt/bin/browserbridge" {
		t.Fatalf("PATH binary: got (%q, %v)", got, err)
	}

	cfg.LookPath = func(string) (string, error) { return "", fmt.Errorf("not found") }
	if _, err := resolveBrowserBridgeBinary(cfg); err == nil || !strings.Contains(err.Error(), browserBridgeBinEnv) {
		t.Fatalf("expected a build hint in the error, got %v", err)
	}
}

// TestBrowserBridgeBaseURL pins the address precedence the CLI and the
// supervisor share.
//
// Parity-exempt: 端口/地址覆盖是 Go 侧的部署开关，上游没有对应测试。
func TestBrowserBridgeBaseURL(t *testing.T) {
	if got := browserBridgeBaseURL(func(string) string { return "" }); got != "http://127.0.0.1:14830" {
		t.Fatalf("default base = %q", got)
	}
	got := browserBridgeBaseURL(func(key string) string {
		if key == browserBridgeHTTPEnv {
			return "127.0.0.1:15000"
		}
		return ""
	})
	if got != "http://127.0.0.1:15000" {
		t.Fatalf("override base = %q", got)
	}
}

// TestBrowserSupervisorParksOnIdleExit pins the idle-exit contract: exit code 0
// is intentional (idle watchdog / POST /shutdown), so the supervisor waits for
// the next client instead of counting it as a crash. ensure() then brings a
// fresh child up.
//
// Parity: tools/src/gcu/bridge_host.py::_idle_exit_watchdog +
// _run_supervisor's worker→supervisor SIGTERM handshake.
func TestBrowserSupervisorParksOnIdleExit(t *testing.T) {
	addr, _ := freeTCPAddr(t)
	controlAddr, _ := freeTCPAddr(t)
	t.Setenv("HIVE_TEST_BRIDGE_STUB", "1")
	t.Setenv("HIVE_TEST_STUB_ADDR", addr)
	t.Setenv("HIVE_TEST_STUB_MODE", "idle-exit")

	out := &safeBuffer{}
	s := &browserSupervisor{
		out:         out,
		env:         os.Getenv,
		binary:      os.Args[0],
		httpAddr:    addr,
		controlAddr: controlAddr,
		base:        "http://" + addr,
		client:      &http.Client{Timeout: 300 * time.Millisecond},
		home:        hivehome.Home{Root: t.TempDir()},
		stopCh:      make(chan struct{}),
		exited:      make(chan struct{}),
		wake:        make(chan struct{}, 1),
	}
	go s.run()
	defer s.Stop()

	waitForOutput(t, out, "Browser bridge: idle — waiting for the next client", 10*time.Second)
	if strings.Contains(out.String(), "restarting in") {
		t.Fatalf("an intentional idle exit must not restart; output:\n%s", out.String())
	}
	// No child is running while parked.
	waitForHealth(t, addr, false, 5*time.Second)

	// The next consumer wakes the supervisor into starting a fresh child.
	s.ensure()
	waitForHealth(t, addr, true, 10*time.Second)
}

// deadProcessPID returns the PID of a process that has already been reaped, so
// "the runtime owner is gone" can be tested without racing a real desktop.
func deadProcessPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dead-pid probe: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reap dead-pid probe: %v", err)
	}
	return pid
}

// TestBrowserSupervisorRefusesRespawnAfterRuntimeDeath pins the dead-runtime
// respawn guard: a wake arriving after the desktop parent died must not start a
// fresh bridge (upstream's supervisor stops for the same reason, so a stale
// host can never outlive the app). ownerPID == serve's own PID is the
// standalone case, where a wake must still respawn.
//
// Parity: tools/src/gcu/bridge_host.py::_run_supervisor
func TestBrowserSupervisorRefusesRespawnAfterRuntimeDeath(t *testing.T) {
	dead := deadProcessPID(t)

	deadOwner := &browserSupervisor{ownerPID: dead, wake: make(chan struct{}, 1)}
	deadOwner.ensure()
	select {
	case <-deadOwner.wake:
		t.Fatal("ensure() woke the supervisor after its runtime owner died")
	default:
	}

	// A wake queued while the previous child was still starting must not
	// restart the loop after that child exits either.
	stale := &browserSupervisor{
		out:      io.Discard,
		ownerPID: dead,
		stopCh:   make(chan struct{}),
		exited:   make(chan struct{}),
		wake:     make(chan struct{}, 1),
	}
	stale.wake <- struct{}{}
	done := make(chan struct{})
	go func() { stale.run(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor kept restarting after the runtime owner died")
	}

	serveOwns := &browserSupervisor{ownerPID: os.Getpid(), wake: make(chan struct{}, 1)}
	serveOwns.ensure()
	select {
	case <-serveOwns.wake:
	default:
		t.Fatal("ensure() did not wake the supervisor while serve owns the runtime")
	}
}
