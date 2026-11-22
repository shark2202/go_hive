package cli

// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
//
// browser_supervisor.go owns the lifecycle of the standalone Go browser bridge
// (`browserbridge`). Upstream Python has `hive serve` keep the long-lived
// `bridge_host --supervise` process alive; the Go port does the same from the
// serve path, with one deliberate difference: the main module does NOT import
// the browserbridge module (it carries its own go.mod), so the contract is the
// process boundary plus the documented ports — `exec`, wait for /health, stop
// on exit, restart a bounded number of times.
//
// This file exists so loader.go (frozen at 1936 lines by the size gate) only
// needs the three-line call in defaultServe.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

const (
	// browserBridgeSwitchEnv overrides the configuration.json switch.
	// "0"/"off"/"false"/"no" disables the bridge, "1"/"on"/"true"/"yes"
	// forces it on regardless of configuration.json.
	browserBridgeSwitchEnv = "HIVE_BROWSER_BRIDGE"
	// browserBridgeBinEnv points at the browserbridge executable.
	browserBridgeBinEnv = "HIVE_BROWSERBRIDGE_BIN"
	// browserBridgeHTTPEnv overrides the local agent API address
	// (default 127.0.0.1:14830, the documented upstream port).
	browserBridgeHTTPEnv = "HIVE_BROWSERBRIDGE_HTTP"
	// browserBridgeArgsEnv appends extra browserbridge flags (space-split).
	browserBridgeArgsEnv = "HIVE_BROWSERBRIDGE_ARGS"
	// browserBridgeViewportScaleEnv carries the GCU viewport scale to the bridge
	// child. The name is upstream's historical override
	// (tools/src/gcu/browser/session.py::_viewport before the extension-based
	// browser replaced the launched one); here it is the transport the
	// supervisor resolves and the child reports.
	browserBridgeViewportScaleEnv = "HIVE_BROWSER_VIEWPORT_SCALE"

	browserBridgeDefaultHTTP = "127.0.0.1:14830"
	browserBridgeStartWait   = 10 * time.Second
	browserBridgeStopWait    = 5 * time.Second
	browserBridgeMaxRestart  = 3
	browserBridgeHealthEvery = 100 * time.Millisecond
)

// browserSupervisorConfig is the serve-side wiring plus test seams.
type browserSupervisorConfig struct {
	Home hivehome.Home
	Out  io.Writer

	// Notify receives bridge notify frames (side-panel tab hand-over /
	// detach) routed to the live agent whose id matches profile. nil keeps
	// the log-only behavior so the supervisor can run without a session
	// manager (CLI probes, tests).
	Notify func(profile, text string, extra map[string]any)

	// Seams; nil means the real environment.
	LookupEnv  func(string) string
	LookPath   func(string) (string, error)
	Executable func() (string, error)
	HTTPClient *http.Client
}

// browserBridgeHTTPAddr resolves the bridge's local agent API address.
func browserBridgeHTTPAddr(lookupEnv func(string) string) string {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if raw := strings.TrimSpace(lookupEnv(browserBridgeHTTPEnv)); raw != "" {
		return raw
	}
	return browserBridgeDefaultHTTP
}

// browserBridgeBaseURL is the HTTP origin the CLI and the supervisor probe.
func browserBridgeBaseURL(lookupEnv func(string) string) string {
	return "http://" + browserBridgeHTTPAddr(lookupEnv)
}

// browserBridgeRequested applies the switch: an explicit env value wins, then
// configuration.json's `gcu_enabled` (upstream always writes it true when the
// browser feature is installed), then the default — on. The bridge is inert
// without an extension connection, so "on by default" only costs a listener.
func browserBridgeRequested(home hivehome.Home, lookupEnv func(string) string) bool {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(lookupEnv(browserBridgeSwitchEnv))) {
	case "0", "off", "false", "no", "disable", "disabled":
		return false
	case "1", "on", "true", "yes", "enable", "enabled":
		return true
	}
	if strings.TrimSpace(home.Root) == "" {
		return true
	}
	config, ok := readJSONObject(home.ConfigurationFile())
	if !ok {
		return true
	}
	// `gcu_enabled: false` is the upstream opt-out (quickstart.sh writes true).
	if enabled, present := config["gcu_enabled"].(bool); present {
		return enabled
	}
	return true
}

// resolveBrowserBridgeBinary finds the executable: explicit env, then a sibling
// of the hive binary (how the release layout ships it), then $PATH.
func resolveBrowserBridgeBinary(cfg browserSupervisorConfig) (string, error) {
	lookupEnv := cfg.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	candidates := make([]string, 0, 4)
	if raw := strings.TrimSpace(lookupEnv(browserBridgeBinEnv)); raw != "" {
		candidates = append(candidates, raw)
	}
	executable := cfg.Executable
	if executable == nil {
		executable = os.Executable
	}
	if exe, err := executable(); err == nil && exe != "" {
		dir := filepath.Dir(exe)
		for _, name := range browserBridgeBinaryNames() {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	lookPath := cfg.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if path, err := lookPath(browserBridgeExecutableName()); err == nil && path != "" {
		return path, nil
	}
	return "", fmt.Errorf("no browserbridge executable (set %s or place %s next to the hive binary)",
		browserBridgeBinEnv, browserBridgeBinaryNames()[0])
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if info.Mode()&0o111 != 0 {
		return true
	}
	// Windows has no executable bit; the name check is the permission check.
	return strings.EqualFold(filepath.Ext(path), ".exe")
}

// startBrowserBridge launches the bridge for the lifetime of `hive serve`. The
// returned stop function is always safe to call (also when nothing started).
func startBrowserBridge(cfg browserSupervisorConfig) func() {
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}
	lookupEnv := cfg.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if !browserBridgeRequested(cfg.Home, lookupEnv) {
		return func() {}
	}
	base := browserBridgeBaseURL(lookupEnv)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	if browserBridgeHealthy(client, base) {
		// Another runtime (the Go CLI, a dev bridge, hive-server) already owns
		// the ports; a second listener would only fail to bind. This serve is
		// still a real consumer, so it holds its own control client.
		fmt.Fprintf(out, "Browser bridge: reusing the instance on %s\n", strings.TrimPrefix(base, "http://"))
		return startBrowserControlKeepalive(browserControlKeepalive{
			controlAddr: browserBridgeControlAddr(lookupEnv),
			ownerPID:    browserControlOwnerPID(lookupEnv),
			out:         out,
			onNotify:    browserNotifySink(cfg),
		})
	}
	bin, err := resolveBrowserBridgeBinary(cfg)
	if err != nil {
		fmt.Fprintf(out, "Browser bridge: not started — %v\n", err)
		return func() {}
	}
	supervisor := &browserSupervisor{
		out:           out,
		env:           lookupEnv,
		binary:        bin,
		httpAddr:      browserBridgeHTTPAddr(lookupEnv),
		controlAddr:   browserBridgeControlAddr(lookupEnv),
		base:          base,
		client:        client,
		home:          cfg.Home,
		viewportScale: browserBridgeViewportScale(cfg.Home, lookupEnv),
		ownerPID:      browserControlOwnerPID(lookupEnv),
		stopCh:        make(chan struct{}),
		exited:        make(chan struct{}),
		wake:          make(chan struct{}, 1),
	}
	// The keepalive owns the serve-lifetime control-RPC connection: it keeps
	// the bridge from idle-exiting while the runtime is alive, and its
	// reconnect attempts ask the supervisor to respawn a bridge that exited.
	stopKeepalive := startBrowserControlKeepalive(browserControlKeepalive{
		controlAddr: supervisor.controlAddr,
		ownerPID:    browserControlOwnerPID(lookupEnv),
		out:         out,
		onNotify:    browserNotifySink(cfg),
		wake:        supervisor.ensure,
	})
	go supervisor.run()
	return func() {
		// Close the control connection first: the bridge then sees zero
		// clients, which is the signal the idle watchdog reads. The SIGTERM
		// below is what actually stops the child when serve exits.
		stopKeepalive()
		supervisor.Stop()
	}
}

// browserNotifySink is the keepalive's notify handler: every frame is written
// to the serve log (the pre-session visibility line the handoff documented),
// then handed to cfg.Notify for session injection when serve wired a manager.
// A nil Notify keeps the old log-only behavior.
func browserNotifySink(cfg browserSupervisorConfig) func(profile, text string, extra map[string]any) {
	return func(profile, text string, extra map[string]any) {
		if cfg.Out != nil {
			fmt.Fprintf(cfg.Out, "Browser panel: notify for %s: %s\n", profile, text)
		}
		if cfg.Notify != nil {
			cfg.Notify(profile, text, extra)
		}
	}
}

// browserControlOwnerPID is the PID the runtime's helper processes live and
// die with: the desktop's when it launched serve, else serve itself
// (core/framework/loader/cli.py::_runtime_owner_pid already exported it).
func browserControlOwnerPID(lookupEnv func(string) string) int {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	raw := strings.TrimSpace(lookupEnv("HIVE_DESKTOP_PARENT_PID"))
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return os.Getpid()
	}
	return pid
}

type browserSupervisor struct {
	out         io.Writer
	env         func(string) string
	binary      string
	httpAddr    string
	controlAddr string
	base        string
	client      *http.Client
	home        hivehome.Home
	// viewportScale is the GCU viewport scale the child inherits and reports
	// (upstream get_gcu_viewport_scale, env override first, clamped).
	viewportScale float64

	// ownerPID is the durable runtime the bridge must die with (upstream
	// _runtime_owner_pid). ensure() refuses to respawn once it is gone, so the
	// bridge's own parent watch cannot turn into a restart loop while serve
	// lingers after the desktop quit.
	ownerPID int

	stopCh   chan struct{}
	wake     chan struct{}
	stopOnce sync.Once
	exited   chan struct{}

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool
}

// ensure asks the run loop to (re)start a bridge after an intentional idle
// exit. It never stacks a second child: the signal is only consumed while the
// loop is waiting between children, and any stale signal is drained before a
// spawn.
func (s *browserSupervisor) ensure() {
	if s.runtimeGone() {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// runtimeGone reports that the durable runtime this serve was started for has
// exited. ownerPID == serve's own PID is the standalone `hive serve` case,
// where serve itself is the runtime and is never "gone".
//
// Upstream's supervisor stops respawning for exactly this reason
// (_run_supervisor's parent watchdog): the bridge exits when
// HIVE_DESKTOP_PARENT_PID dies, so a wake — even one queued while the child was
// still starting — must not start a fresh one.
func (s *browserSupervisor) runtimeGone() bool {
	if s.ownerPID <= 0 || s.ownerPID == os.Getpid() {
		return false
	}
	return !browserProcessAlive(s.ownerPID)
}

// errBrowserBridgeIdleExit marks a child that exited 0 on its own (the idle
// watchdog or POST /shutdown). Such an exit is intentional: the supervisor
// waits for the next consumer (the keepalive's ensure) instead of restarting,
// which is what upstream's worker→supervisor SIGTERM handshake achieves.
var errBrowserBridgeIdleExit = errors.New("browser bridge exited on its own")

// run keeps the bridge alive: start, wait for health, wait for exit, restart
// with backoff up to browserBridgeMaxRestart times. An intentional idle exit
// is not a crash: it parks until ensure() is called.
func (s *browserSupervisor) run() {
	defer close(s.exited)
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		if s.isStopped() {
			return
		}
		// A wake queued before the runtime died would otherwise start a child
		// that can only exit again; stop the loop instead.
		if s.runtimeGone() {
			fmt.Fprintf(s.out, "Browser bridge: runtime gone — not restarting\n")
			return
		}
		// Drop a wake request that arrived while a child was already running.
		select {
		case <-s.wake:
		default:
		}
		err := s.runOnce()
		if s.isStopped() {
			return
		}
		if errors.Is(err, errBrowserBridgeIdleExit) {
			fmt.Fprintf(s.out, "Browser bridge: idle — waiting for the next client\n")
			select {
			case <-s.stopCh:
				return
			case <-s.wake:
			}
			attempt = 0
			backoff = 500 * time.Millisecond
			continue
		}
		if err != nil {
			fmt.Fprintf(s.out, "Browser bridge: %v\n", err)
		}
		if errors.Is(err, errBrowserBridgePortBusy) {
			fmt.Fprintf(s.out, "Browser bridge: leaving the existing listener on %s alone\n", s.httpAddr)
			return
		}
		if attempt >= browserBridgeMaxRestart {
			fmt.Fprintf(s.out, "Browser bridge: giving up after %d restarts\n", browserBridgeMaxRestart)
			return
		}
		fmt.Fprintf(s.out, "Browser bridge: restarting in %s\n", backoff)
		select {
		case <-s.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

var errBrowserBridgePortBusy = errors.New("browser bridge ports are busy")

// runOnce starts one child and blocks until it exits (or fails to start).
func (s *browserSupervisor) runOnce() error {
	args := []string{"-http", s.httpAddr, "-control", s.controlAddr}
	if dir := strings.TrimSpace(s.home.ToolArtifactsDir()); dir != "" && s.home.Root != "" {
		args = append(args, "-artifacts", dir)
	}
	if extra := strings.TrimSpace(s.env(browserBridgeArgsEnv)); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	cmd := exec.Command(s.binary, args...)
	cmd.Env = browserBridgeChildEnv(s.home, s.env)
	cmd.SysProcAttr = browserBridgeSysProcAttr()
	output := &tailBuffer{limit: 4096}
	logFile := s.openLog()
	if logFile != nil {
		cmd.Stdout = io.MultiWriter(logFile, output)
		cmd.Stderr = io.MultiWriter(logFile, output)
	} else {
		cmd.Stdout = output
		cmd.Stderr = output
	}
	if err := cmd.Start(); err != nil {
		s.closeLog(logFile)
		return fmt.Errorf("start %s: %w", filepath.Base(s.binary), err)
	}
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	child := &bridgeChild{done: make(chan struct{})}
	go func() {
		child.err = cmd.Wait()
		close(child.done)
	}()

	if err := s.waitHealthy(child); err != nil {
		// Either the child died before serving, or it is alive but deaf: make
		// sure it is gone before returning, so a restart cannot stack two
		// bridges on the same ports.
		_ = browserBridgeSignal(cmd.Process, syscall.SIGTERM)
		select {
		case <-child.done:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
			<-child.done
		}
		s.mu.Lock()
		s.cmd = nil
		s.mu.Unlock()
		s.closeLog(logFile)
		tail := strings.TrimSpace(output.String())
		if strings.Contains(tail, "address already in use") {
			return fmt.Errorf("%w: %v", errBrowserBridgePortBusy, firstLine(tail))
		}
		if tail != "" {
			return fmt.Errorf("%v (%s)", err, firstLine(tail))
		}
		return err
	}
	fmt.Fprintf(s.out, "Browser bridge: ready on %s (pid %d, viewport scale %s)\n",
		s.httpAddr, pid, strconv.FormatFloat(s.viewportScale, 'f', -1, 64))

	<-child.done
	s.mu.Lock()
	s.cmd = nil
	s.mu.Unlock()
	s.closeLog(logFile)
	if s.isStopped() {
		return nil
	}
	if child.err == nil {
		// Exit code 0 is the idle watchdog / POST /shutdown path: the bridge
		// left on purpose, so the supervisor must not treat it as a crash.
		return errBrowserBridgeIdleExit
	}
	return fmt.Errorf("browser bridge exited: %v%s", child.err, lastLine(output.String()))
}

// bridgeChild carries one child's Wait result: the channel closes when the
// process is reaped, so the readiness loop can notice an early exit without
// consuming it (Wait may only be called once).
type bridgeChild struct {
	done chan struct{}
	err  error
}

// waitHealthy polls /health until the child answers, the child exits, or the
// start budget runs out.
func (s *browserSupervisor) waitHealthy(child *bridgeChild) error {
	deadline := time.Now().Add(browserBridgeStartWait)
	for time.Now().Before(deadline) {
		if s.isStopped() {
			return context.Canceled
		}
		select {
		case <-child.done:
			return fmt.Errorf("browser bridge exited before it became healthy")
		default:
		}
		if browserBridgeHealthy(s.client, s.base) {
			return nil
		}
		time.Sleep(browserBridgeHealthEvery)
	}
	return fmt.Errorf("browser bridge did not answer %s/health within %s", s.base, browserBridgeStartWait)
}

// Stop terminates the child (and its process group) and waits for the exit.
func (s *browserSupervisor) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		cmd := s.cmd
		s.mu.Unlock()
		close(s.stopCh)
		if cmd != nil && cmd.Process != nil {
			_ = browserBridgeSignal(cmd.Process, syscall.SIGTERM)
			select {
			case <-s.exited:
				return
			case <-time.After(browserBridgeStopWait):
			}
			_ = browserBridgeSignal(cmd.Process, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
	})
	select {
	case <-s.exited:
	case <-time.After(time.Second):
	}
}

func (s *browserSupervisor) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *browserSupervisor) openLog() *os.File {
	if strings.TrimSpace(s.home.Root) == "" {
		return nil
	}
	path := filepath.Join(s.home.Root, "browserbridge.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	fmt.Fprintf(file, "\n--- %s browserbridge %s ---\n", time.Now().Format(time.RFC3339), s.binary)
	return file
}

func (s *browserSupervisor) closeLog(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

// browserBridgeChildEnv passes HIVE_HOME, the switch and the resolved GCU
// viewport scale through so the child resolves the same home (artifacts,
// telemetry, starred profile) and reports the same viewport.
func browserBridgeChildEnv(home hivehome.Home, lookupEnv func(string) string) []string {
	env := os.Environ()
	if strings.TrimSpace(home.Root) != "" {
		env = withEnv(env, hivehome.EnvHome, home.Root)
	}
	scale := strconv.FormatFloat(browserBridgeViewportScale(home, lookupEnv), 'f', -1, 64)
	return withEnv(env, browserBridgeViewportScaleEnv, scale)
}

// browserBridgeViewportScale resolves the GCU viewport scale the bridge child
// works with, upstream's _viewport() order: an explicit
// HIVE_BROWSER_VIEWPORT_SCALE wins when it parses (upstream warned on junk and
// fell through to config), then configuration.json's `gcu_viewport_scale`, then
// the 0.8 default. The resolved value is clamped to [0.1, 1.0] either way —
// upstream clamped the env path too, so an env 2.0 becomes 1.0 even though a
// config 2.0 falls back to 0.8 in get_gcu_viewport_scale.
func browserBridgeViewportScale(home hivehome.Home, lookupEnv func(string) string) float64 {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if raw := strings.TrimSpace(lookupEnv(browserBridgeViewportScaleEnv)); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(parsed) {
			return clampViewportScale(parsed)
		}
	}
	return clampViewportScale(hiveserver.GcuViewportScale(home))
}

func clampViewportScale(scale float64) float64 {
	if scale < hiveserver.GcuViewportScaleMin {
		return hiveserver.GcuViewportScaleMin
	}
	if scale > hiveserver.GcuViewportScaleMax {
		return hiveserver.GcuViewportScaleMax
	}
	return scale
}

// withEnv sets key in env, replacing any earlier entry so the child reads the
// value this process resolved (the kernel hands getenv the first match).
func withEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return append(out, prefix+value)
}

// browserBridgeHealthy reports whether something answers GET /health with 200.
func browserBridgeHealthy(client *http.Client, base string) bool {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode == http.StatusOK
}

// tailBuffer keeps the last `limit` bytes a child wrote, so a failed start can
// report the child's own error line ("listen http … address already in use")
// without re-reading the log file.
type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		b.limit = 4096
	}
	b.data = append(b.data, chunk...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(chunk), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func firstLine(text string) string {
	lines := nonEmptyLines(text)
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

func lastLine(text string) string {
	lines := nonEmptyLines(text)
	if len(lines) == 0 {
		return ""
	}
	return ": " + lines[len(lines)-1]
}

func nonEmptyLines(text string) []string {
	out := make([]string, 0, 4)
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
