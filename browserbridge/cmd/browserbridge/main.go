// Command browserbridge runs the standalone Hive browser bridge: the
// WebSocket endpoint the Chrome extension dials (14829, legacy 9229) and the
// local agent HTTP API (14830).
//
// It is a thin lifecycle wrapper around the browserbridge package: flags →
// Config, Start, wait for SIGINT/SIGTERM (or a cancelled context), Shutdown.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aden-hive/hive-go/browserbridge"
)

const version = browserbridge.BridgeVersion

type options struct {
	ws           string
	httpAddr     string
	controlAddr  string
	legacy       string
	noLegacy     bool
	sweep        time.Duration
	noSweep      bool
	telemetryDir string
	rateLimits   bool
	artifacts    string
	extension    string
	skills       string
	scriptCmd    string
	probe        string
	showVersion  bool
	shutdownWait time.Duration
	idleGrace    time.Duration
	idleCheck    time.Duration
	noIdleExit   bool
	parentCheck  time.Duration
}

func parseFlags(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("browserbridge", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.ws, "ws", browserbridge.DefaultWSAddr, "extension WebSocket listen address")
	fs.StringVar(&o.httpAddr, "http", browserbridge.DefaultHTTPAddr, "local agent HTTP listen address")
	fs.StringVar(&o.controlAddr, "control", browserbridge.DefaultControlAddr, "control RPC listen address (14831)")
	fs.StringVar(&o.legacy, "legacy", browserbridge.DefaultLegacyAddr, "legacy WebSocket listen address (ignored with -no-legacy)")
	fs.BoolVar(&o.noLegacy, "no-legacy", false, "do not bind the legacy 9229 listener")
	fs.DurationVar(&o.sweep, "sweep", 30*time.Second, "stale-context sweep interval")
	fs.BoolVar(&o.noSweep, "no-sweep", false, "disable the stale-context sweep")
	fs.StringVar(&o.telemetryDir, "telemetry-dir", "", "write daily telemetry JSONL here (default: off)")
	fs.BoolVar(&o.rateLimits, "rate-limits", false, "enable the social profile-view rate-limit hook")
	fs.StringVar(&o.artifacts, "artifacts", "", "artifact directory for snapshot/html spill (default: $HIVE_HOME/tool-artifacts)")
	fs.StringVar(&o.extension, "extension", "", "path to the bundled Chrome extension, reported by browser_setup")
	fs.StringVar(&o.skills, "skills", "", "comma-separated skill roots for browser_script")
	fs.StringVar(&o.scriptCmd, "script-command", "", `interpreter for browser_script (comma-separated, default "uv,run,python")`)
	fs.StringVar(&o.probe, "probe", "", "GET <url>/health and exit (e.g. http://127.0.0.1:14830)")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.DurationVar(&o.shutdownWait, "shutdown-timeout", 5*time.Second, "graceful shutdown budget")
	fs.DurationVar(&o.idleGrace, "idle-grace", 30*time.Second, "exit after this long with no control RPC clients and no desktop parent")
	fs.DurationVar(&o.idleCheck, "idle-check", 10*time.Second, "idle watchdog poll interval")
	fs.BoolVar(&o.noIdleExit, "no-idle-exit", false, "never exit on idle (manual/dev runs)")
	fs.DurationVar(&o.parentCheck, "parent-check", desktopParentCheckDefault,
		"poll cadence for HIVE_DESKTOP_PARENT_PID liveness (2 consecutive misses exit the bridge)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return o, nil
}

func (o options) config() browserbridge.Config {
	cfg := browserbridge.Config{
		WSAddr:           o.ws,
		HTTPAddr:         o.httpAddr,
		ControlAddr:      o.controlAddr,
		LegacyAddr:       o.legacy,
		SweepInterval:    o.sweep,
		SweepDisabled:    o.noSweep,
		TelemetryDir:     o.telemetryDir,
		EnableRateLimits: o.rateLimits,
		ArtifactDir:      o.artifacts,
		ExtensionDir:     o.extension,
		SkillDirs:        splitList(o.skills),
		ScriptCommand:    splitList(o.scriptCmd),
		// /status bridge_version. runtime_version stays null unless an
		// embedder passes one, exactly like upstream's production bridge
		// (BeelineBridge(runtime_version=None)).
		BridgeVersion: version,
	}
	if o.noLegacy {
		cfg.LegacyAddr = ""
	}
	return cfg
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// newBridge builds the bridge without starting it (tests use this directly).
func newBridge(o options) *browserbridge.Bridge {
	return browserbridge.New(o.config())
}

// idleExitRequested implements the upstream _idle_exit_watchdog: exit when no
// control-RPC client is bound AND the durable runtime (HIVE_DESKTOP_PARENT_PID)
// is gone, after a grace window. Two signals, both required — the extension
// being connected says nothing about whether anyone is doing browser work.
// Returns true when the caller should shut the bridge down.
func idleExitRequested(ctx context.Context, b *browserbridge.Bridge, grace, check time.Duration) bool {
	if grace <= 0 {
		grace = 30 * time.Second
	}
	if check <= 0 {
		check = 10 * time.Second
	}
	parentPID := 0
	if raw := strings.TrimSpace(os.Getenv("HIVE_DESKTOP_PARENT_PID")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			parentPID = parsed
		}
	}
	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(check):
		}
		desktopAlive := parentPID > 0 && browserbridge.PIDAlive(parentPID)
		if b.ControlClients() > 0 || desktopAlive {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			fmt.Fprintf(os.Stderr, "browserbridge: no control RPC clients and desktop is gone — starting %s grace before self-exit\n", grace)
			continue
		}
		if time.Since(idleSince) < grace {
			continue
		}
		fmt.Fprintf(os.Stderr, "browserbridge: idle %s with no control RPC clients and no desktop — exiting so an orphaned bridge doesn't outlive the runtime\n", time.Since(idleSince).Round(time.Second))
		return true
	}
}

// Desktop-parent watch — parity with tools/src/gcu/bridge_host.py::_watch_parent.
//
// HIVE_DESKTOP_PARENT_PID is the durable runtime's own PID (the desktop app
// sets it to process.pid; `hive serve` falls back to its own PID). Upstream's
// worker flips its stop event as soon as that PID is gone, because the bridge
// must never outlive the runtime and keep a stale build in memory. The Go port
// used to learn about the death only from the idle watchdog (10 s poll + 30 s
// grace ≈ 30–40 s); this watcher is the upstream behaviour, and the idle
// watchdog stays as the fallback for a bridge started without a parent hint.
//
// Two consecutive misses debounce a one-off probe glitch, like upstream's
// _PARENT_DEATH_CONFIRMATIONS. The 400 ms cadence keeps the observed exit
// comfortably inside the ≤2 s contract.
const (
	desktopParentCheckDefault = 400 * time.Millisecond
	desktopParentMisses       = 2
)

// desktopParentWentAway reports true once parentPID has been missing for
// desktopParentMisses consecutive probes. It returns false when ctx is done.
func desktopParentWentAway(ctx context.Context, parentPID int, check time.Duration) bool {
	if parentPID <= 0 {
		return false
	}
	if check <= 0 {
		check = desktopParentCheckDefault
	}
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(check):
		}
		if browserbridge.PIDAlive(parentPID) {
			misses = 0
			continue
		}
		misses++
		if misses >= desktopParentMisses {
			return true
		}
	}
}

// desktopParentPID parses HIVE_DESKTOP_PARENT_PID; 0 means "no parent hint"
// (a bare/dev bridge, which the idle watchdog still reaps).
func desktopParentPID() int {
	raw := strings.TrimSpace(os.Getenv("HIVE_DESKTOP_PARENT_PID"))
	if raw == "" {
		return 0
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// run starts the listeners, announces the bound addresses on stdout as one
// JSON line, and blocks until ctx is cancelled or the idle watchdog fires.
// Shutdown is always graceful (exit code 0), which tells the supervisor the
// exit was intentional.
func run(ctx context.Context, o options) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg := o.config()
	// POST /shutdown and the idle watchdog share one graceful exit path.
	cfg.ShutdownRequest = cancel
	b := browserbridge.New(cfg)
	if err := b.Start(runCtx); err != nil {
		return err
	}
	if !o.noIdleExit {
		go func() {
			if idleExitRequested(runCtx, b, o.idleGrace, o.idleCheck) {
				cancel()
			}
		}()
	}
	if parentPID := desktopParentPID(); parentPID > 0 {
		go func() {
			if desktopParentWentAway(runCtx, parentPID, o.parentCheck) {
				fmt.Fprintf(os.Stderr,
					"browserbridge: runtime pid %d is gone — shutting down the bridge with the runtime\n", parentPID)
				cancel()
			}
		}()
	}
	legacy := ""
	if !o.noLegacy {
		legacy = b.LegacyAddr()
	}
	ready := map[string]any{
		"ok":      true,
		"ready":   true,
		"version": version,
		"pid":     os.Getpid(),
		"ws":      b.WSAddr(),
		"http":    b.HTTPAddr(),
		"control": b.ControlAddr(),
		"legacy":  legacy,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(ready)

	<-runCtx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), o.shutdownWait)
	defer shutdownCancel()
	if err := b.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	_ = enc.Encode(map[string]any{"ok": true, "stopped": true})
	return nil
}

// probe hits the local HTTP API and prints its payload. Exit code reflects
// reachability so a shell script can wait on it.
func probe(ctx context.Context, base string) error {
	url := strings.TrimRight(base, "/") + "/health"
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Println(strings.TrimSpace(string(body)))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func main() {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "browserbridge:", err)
		os.Exit(2)
	}
	if o.showVersion {
		fmt.Println(version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.probe != "" {
		if err := probe(ctx, o.probe); err != nil {
			fmt.Fprintln(os.Stderr, "browserbridge: probe failed:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "browserbridge:", err)
		os.Exit(1)
	}
}
