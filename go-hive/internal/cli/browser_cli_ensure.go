package cli

// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
//
// tools/src/gcu/errors.py::_run_with_bridge calls ensure_bridge_host_running()
// before every command, so `hive-browser` on a cold machine starts the
// long-lived bridge instead of failing with exit 2. The Go CLI performs the
// same ensure through the process boundary: probe GET /health on the CLI's
// transport, spawn the `browserbridge` binary detached (its own session, so it
// outlives this one-shot CLI) when nothing answers, then wait up to 8s —
// upstream's wait_s — with upstream's 0.2s poll.
//
// An explicit --bridge (a Go-only transport override upstream has no
// equivalent for) opts out: the caller pointed at another endpoint, and the
// default bridge is not that endpoint's manager. The spawn is best-effort; a
// failure leaves the command to its normal not-connected exit code, exactly
// like ensure_bridge_host_running() returning False.

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const (
	// browserCLIEnsureWait matches ensure_bridge_host_running(wait_s=8.0).
	browserCLIEnsureWait = 8 * time.Second
	// browserCLIEnsurePoll is upstream's 0.2s readiness poll.
	browserCLIEnsurePoll = 200 * time.Millisecond
	// browserCLIHealthTimeout bounds one readiness probe.
	browserCLIHealthTimeout = 2 * time.Second
)

// ensureBridge makes sure the bridge this CLI talks to answers, spawning one
// when it does not. The returned value is advisory: the command proceeds and
// reports its normal exit code either way.
func (c *browserCLI) ensureBridge(args *parsedArgs) bool {
	if args != nil && args.wasProvided("bridge") {
		// Explicit endpoint: someone else manages that bridge.
		return true
	}
	return ensureBrowserBridge(c.bridgeBase(args), browserCLIEnsureConfig{LookupEnv: c.env})
}

// browserCLIEnsureConfig is the spawn/probe wiring plus test seams; nil fields
// mean the real environment.
type browserCLIEnsureConfig struct {
	LookupEnv  func(string) string
	LookPath   func(string) (string, error)
	Executable func() (string, error)
	Client     *http.Client
	// Wait overrides the 8s readiness budget (tests shorten it).
	Wait time.Duration
}

// ensureBrowserBridge is ensure_bridge_host_running's Go shape: reuse a bridge
// that already answers, otherwise start one and wait for it to come up.
func ensureBrowserBridge(base string, cfg browserCLIEnsureConfig) bool {
	lookupEnv := cfg.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: browserCLIHealthTimeout}
	}
	if browserBridgeHealthy(client, base) {
		// Upstream's _control_port_alive fast path: cheap, idempotent no-op.
		return true
	}
	bin, err := resolveBrowserBridgeBinary(browserSupervisorConfig{
		LookupEnv:  lookupEnv,
		LookPath:   cfg.LookPath,
		Executable: cfg.Executable,
	})
	if err != nil {
		return false
	}
	home, homeErr := hivehome.Resolve()
	if homeErr != nil {
		home = hivehome.Home{}
	}

	args := []string{"-http", browserBridgeHTTPAddr(lookupEnv), "-control", browserBridgeControlAddr(lookupEnv)}
	if home.Root != "" {
		args = append(args, "-artifacts", home.ToolArtifactsDir())
	}
	// The same extra-flags hook the serve supervisor honors, so a deployment
	// (or an e2e probe) can pin the spawned child's WS/legacy listeners.
	if extra := strings.TrimSpace(lookupEnv(browserBridgeArgsEnv)); extra != "" {
		args = append(args, strings.Fields(extra)...)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = browserBridgeChildEnv(home, lookupEnv)
	cmd.SysProcAttr = browserBridgeDetachedSysProcAttr()
	if logFile := openBrowserCLIEnsureLog(home); logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		defer func() { _ = logFile.Close() }() // the child holds its own fd
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		return false
	}
	// The CLI exits before the bridge does; releasing the handle keeps the
	// detached child parented to the system, like start_new_session=True.
	_ = cmd.Process.Release()

	wait := cfg.Wait
	if wait <= 0 {
		wait = browserCLIEnsureWait
	}
	deadline := time.Now().Add(wait)
	for {
		if browserBridgeHealthy(client, base) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(browserCLIEnsurePoll)
	}
}

// openBrowserCLIEnsureLog appends the spawned bridge's output to
// <HIVE_HOME>/bridge_host.log — upstream's ~/.hive/bridge_host.log. Nil keeps
// the child's output discarded.
func openBrowserCLIEnsureLog(home hivehome.Home) *os.File {
	if strings.TrimSpace(home.Root) == "" {
		return nil
	}
	path := filepath.Join(home.Root, "bridge_host.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return file
}
