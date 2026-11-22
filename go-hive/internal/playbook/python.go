package playbook

// Python `.play.py` playbook support: an external interpreter + stdio RPC.
//
// Upstream executes playbooks in-process (`exec` in the colony's Python
// environment) with core/framework/host/playbook/runner.py owning the hook
// surface; only two injected callables cross into the colony —
// `dispatch_one` (spawn ONE worker and await its terminal report) and
// `query_rows` (a tracker SELECT). The Go port keeps its pure-Go main binary:
// the same runner semantics live in the embedded bootstrap
// (python_bootstrap.py), and the same two callables cross a newline-delimited
// JSON-RPC bridge over the child's stdin/stdout instead of a closure.
//
// Lifecycle: the interpreter is resolved per run (HIVE_PYTHON override, then
// `python3`, then `python` on PATH); a missing interpreter is a clear
// PythonUnavailableError, never a fake success. The child runs in its own
// process group and is terminated (SIGTERM, then SIGKILL after a grace
// period) when the run's context is cancelled or the run ends. stdout is
// exclusively RPC; the bootstrap points the script's sys.stdout at stderr so
// `print()` cannot corrupt the stream.

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed python_bootstrap.py
var pythonBootstrapSource string

const (
	// pythonLoadTimeout bounds the synchronous load phase (compile + exec at
	// module scope + meta validation). A module-level infinite loop is the case
	// this exists for; a real script's imports finish far inside it.
	pythonLoadTimeout = 120 * time.Second
	// pythonDefaultTimeout is the per-worker dispatch default the bootstrap
	// receives (upstream PlaybookRun(default_timeout=600)). It is NOT a cap on
	// the whole run: upstream never bounds run() as a whole, and a legit batch
	// can outlive any per-worker budget.
	pythonDefaultTimeout = 600 * time.Second
	// pythonStallGrace is added to the worker timeout to form the default
	// no-activity budget. A dispatch_one answer can legitimately stay silent for
	// a full worker timeout, so the silence guard must sit above it; anything
	// beyond that is a stalled bridge, not a slow worker.
	pythonStallGrace = 300 * time.Second
	// pythonMaxLineBytes caps one protocol line. Results carrying a large row
	// batch are legitimate, so this is generous.
	pythonMaxLineBytes = 32 << 20
	// pythonMaxStderrBytes keeps the tail of stderr for diagnostics.
	pythonMaxStderrBytes = 64 << 10
	// pythonKillGrace is how long SIGTERM gets before SIGKILL.
	pythonKillGrace = 2 * time.Second
)

// DefaultStallTimeout is the parent-side silence guard a run gets when
// PythonOptions.StallTimeout is unset: one full worker dispatch
// (pythonDefaultTimeout — upstream PlaybookRun(default_timeout=600)) plus
// pythonStallGrace. This guard watches for a dead bridge; it is not a cap on
// the run's total time, so a slow-but-live batch keeps resetting it.
func DefaultStallTimeout() time.Duration { return pythonDefaultTimeout + pythonStallGrace }

// PythonUnavailableError means the environment cannot run Python playbooks:
// no interpreter on PATH, a bad HIVE_PYTHON, or an interpreter too old to run
// the bootstrap. Callers should report `unsupported` with this reason rather
// than pretending a run happened.
type PythonUnavailableError struct {
	Reason string
}

func (e *PythonUnavailableError) Error() string {
	if e == nil || e.Reason == "" {
		return "no Python 3 interpreter is available"
	}
	return e.Reason
}

// IsPythonUnavailable reports whether err is an environment-capability error
// (as opposed to a script/bridge failure).
func IsPythonUnavailable(err error) bool {
	var target *PythonUnavailableError
	return errors.As(err, &target)
}

// PythonScriptError is a script-level failure reported by the bootstrap: it
// wraps the script's own root cause (SyntaxError, NameError, validation
// errors) with the upstream-style concise line and full traceback.
type PythonScriptError struct {
	Kind      string
	Message   string
	Concise   string
	Traceback string
	Stage     string // "load" or "run"
}

func (e *PythonScriptError) Error() string {
	if e == nil {
		return ""
	}
	if e.Concise != "" {
		return e.Concise
	}
	if e.Kind != "" {
		return e.Kind + ": " + e.Message
	}
	return e.Message
}

// PythonBridgeError is a transport failure: the child died, wrote non-JSON, or
// could not be started. It is never a script success.
type PythonBridgeError struct {
	Message  string
	Stderr   string
	ExitCode int
	HasExit  bool
}

func (e *PythonBridgeError) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(e.Message)
	if e.HasExit {
		fmt.Fprintf(&b, " (exit code %d)", e.ExitCode)
	}
	if e.Stderr != "" {
		b.WriteString("; stderr: ")
		b.WriteString(e.Stderr)
	}
	return b.String()
}

// ResolvePythonInterpreter finds the interpreter a Python playbook runs on:
// HIVE_PYTHON when set (an explicit deployment override), else `python3`, else
// `python` on PATH. Python 3.8+ is enforced by the bootstrap at startup.
func ResolvePythonInterpreter() (string, error) {
	if override := strings.TrimSpace(os.Getenv("HIVE_PYTHON")); override != "" {
		if filepath.IsAbs(override) || strings.ContainsRune(override, os.PathSeparator) {
			info, err := os.Stat(override)
			if err != nil {
				return "", &PythonUnavailableError{
					Reason: fmt.Sprintf("HIVE_PYTHON=%q is not usable: %v", override, err),
				}
			}
			if info.IsDir() {
				return "", &PythonUnavailableError{
					Reason: fmt.Sprintf("HIVE_PYTHON=%q is a directory, not an interpreter", override),
				}
			}
			return override, nil
		}
		if path, err := exec.LookPath(override); err == nil {
			return path, nil
		}
		return "", &PythonUnavailableError{
			Reason: fmt.Sprintf("HIVE_PYTHON=%q was not found on PATH", override),
		}
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", &PythonUnavailableError{
		Reason: "no Python interpreter found: looked for `python3`, then `python` on PATH. " +
			"Python playbooks need an external Python 3.8+; install it or set HIVE_PYTHON to the interpreter path",
	}
}

// PythonOptions wires one Python playbook run. DispatchOne and QueryRows are
// the same two colony callables the upstream runner receives. ConcurrencyCap
// mirrors upstream's PlaybookRun(concurrency_cap=…) constructor argument: it
// is visible at load time (module-scope lane() calls warn against it) and is
// overridden by Execute when Execute's cap is positive.
type PythonOptions struct {
	DispatchOne    DispatchOne
	QueryRows      QueryRows
	RunID          string
	Interpreter    string
	DefaultTimeout *time.Duration
	// StallTimeout bounds how long the bridge may stay silent (no line from the
	// child and no event) before Execute kills it and reports the stall. Zero
	// means "use DefaultTimeout + pythonStallGrace". Deliberately not an env
	// var: the Go side ships no env surface the Python reference lacks.
	StallTimeout   *time.Duration
	ConcurrencyCap *int
}

// PythonPlaybook is a loaded script whose process is waiting for the run
// command. Call Close when done; it is idempotent.
type PythonPlaybook struct {
	RunID         string
	Meta          map[string]any
	PythonVersion string
	Interpreter   string

	dispatchOne    DispatchOne
	queryRows      QueryRows
	defaultTimeout time.Duration
	stallTimeout   time.Duration
	lastActivity   time.Time

	cmd        *exec.Cmd
	stdin      io.WriteCloser
	workDir    string
	stderr     *pythonTailBuffer
	events     chan bridgeEvent
	closed     chan struct{}
	waitDone   chan struct{}
	readDone   chan struct{}
	stderrDone chan struct{}
	closeOnce  sync.Once
	termOnce   sync.Once
	writeMu    sync.Mutex

	// reqMu guards the bridge-request bookkeeping used by a stall report:
	// served counts answered child requests, reqInFlight names the ones whose
	// answer has not been written yet.
	reqMu       sync.Mutex
	reqServed   int
	reqInFlight map[int64]string

	ctxMu sync.Mutex
	ctx   context.Context
}

type bridgeMessage struct {
	ID            int64           `json:"id"`
	Type          string          `json:"type"`
	Method        string          `json:"method"`
	Params        json.RawMessage `json:"params"`
	Result        json.RawMessage `json:"result"`
	Error         json.RawMessage `json:"error"`
	Meta          map[string]any  `json:"meta"`
	PythonVersion string          `json:"python_version"`
}

type bridgeEvent struct {
	Type          string
	Meta          map[string]any
	PythonVersion string
	Result        json.RawMessage
	Error         pythonWireError
	Err           string
	Line          string
}

type pythonWireError struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Concise   string `json:"concise"`
	Traceback string `json:"traceback"`
}

type pythonTailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func newPythonTailBuffer(max int) *pythonTailBuffer {
	return &pythonTailBuffer{max: max}
}

func (b *pythonTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *pythonTailBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.TrimSpace(string(b.buf))
}

// StartPythonPlaybook resolves the interpreter, starts the bootstrap, sends
// the script and waits for the synchronous load to report meta (or fail). The
// returned handle is ready for Execute.
func StartPythonPlaybook(ctx context.Context, script string, opts PythonOptions) (*PythonPlaybook, error) {
	interpreter := strings.TrimSpace(opts.Interpreter)
	if interpreter == "" {
		resolved, err := ResolvePythonInterpreter()
		if err != nil {
			return nil, err
		}
		interpreter = resolved
	}

	workDir, err := os.MkdirTemp("", "hive-playbook-")
	if err != nil {
		return nil, fmt.Errorf("python playbook: create temp dir: %w", err)
	}
	bootstrapPath := filepath.Join(workDir, "bootstrap.py")
	if err := os.WriteFile(bootstrapPath, []byte(pythonBootstrapSource), 0o600); err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("python playbook: write bootstrap: %w", err)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("python playbook: stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("python playbook: stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = os.RemoveAll(workDir)
		return nil, fmt.Errorf("python playbook: stderr pipe: %w", err)
	}

	defaultTimeout := pythonDefaultTimeout
	if opts.DefaultTimeout != nil && *opts.DefaultTimeout > 0 {
		defaultTimeout = *opts.DefaultTimeout
	}
	stallTimeout := DefaultStallTimeout()
	if defaultTimeout != pythonDefaultTimeout {
		stallTimeout = defaultTimeout + pythonStallGrace
	}
	if opts.StallTimeout != nil && *opts.StallTimeout > 0 {
		stallTimeout = *opts.StallTimeout
	}
	runID := opts.RunID
	if runID == "" {
		runID = "playbook"
	}
	p := &PythonPlaybook{
		RunID:          runID,
		Interpreter:    interpreter,
		dispatchOne:    opts.DispatchOne,
		queryRows:      opts.QueryRows,
		defaultTimeout: defaultTimeout,
		stallTimeout:   stallTimeout,
		lastActivity:   time.Now(),
		workDir:        workDir,
		stdin:          stdinW,
		stderr:         newPythonTailBuffer(pythonMaxStderrBytes),
		events:         make(chan bridgeEvent, 8),
		closed:         make(chan struct{}),
		waitDone:       make(chan struct{}),
		readDone:       make(chan struct{}),
		stderrDone:     make(chan struct{}),
		ctx:            ctx,
	}

	// Manual pipes (instead of cmd.StdoutPipe) so cmd.Wait never races the
	// protocol reader by closing the descriptor it is reading.
	cmd := exec.Command(interpreter, "-u", bootstrapPath)
	cmd.Env = pythonChildEnv()
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		_ = os.RemoveAll(workDir)
		return nil, &PythonUnavailableError{
			Reason: fmt.Sprintf("python playbook: cannot start %s: %v", interpreter, err),
		}
	}
	p.cmd = cmd
	// The child holds duplicated descriptors; drop the parent's copies of the
	// child's ends.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	go p.waitLoop()
	go p.drainStderr(stderrR)
	go p.readLoop(stdoutR)

	// The init write is bounded too: a child that never reads stdin would
	// otherwise park the caller in a full pipe before any timeout can arm.
	if err := p.sendWithin(map[string]any{
		"type":            "init",
		"script":          script,
		"run_id":          runID,
		"default_timeout": defaultTimeout.Seconds(),
		"concurrency_cap": opts.ConcurrencyCap,
	}, pythonLoadTimeout); err != nil {
		p.Close()
		return nil, &PythonBridgeError{
			Message: "python playbook: cannot send the script to the interpreter: " + err.Error(),
			Stderr:  p.stderr.String(),
		}
	}

	event, err := p.waitEvent(ctx, pythonLoadTimeout)
	if err != nil {
		p.Close()
		return nil, err
	}
	switch event.Type {
	case "loaded":
		meta := event.Meta
		if meta == nil {
			meta = map[string]any{}
		}
		if !isTruthy(meta["name"]) {
			p.Close()
			return nil, &PythonScriptError{
				Kind:    "PlaybookScriptError",
				Message: "playbook must define a `meta` dict with at least a 'name'",
				Concise: "PlaybookScriptError: playbook must define a `meta` dict with at least a 'name'",
				Stage:   "load",
			}
		}
		p.Meta = meta
		p.PythonVersion = event.PythonVersion
		return p, nil
	case "load_error":
		p.Close()
		return nil, p.scriptError(event, "load")
	case "fatal":
		p.Close()
		scriptErr := p.scriptError(event, "load")
		var typed *PythonScriptError
		if errors.As(scriptErr, &typed) && typed.Kind == "PythonVersionError" {
			return nil, &PythonUnavailableError{Reason: typed.Error()}
		}
		return nil, scriptErr
	case "protocol_error":
		p.Close()
		return nil, p.protocolError(event)
	default:
		p.Close()
		return nil, &PythonBridgeError{
			Message: fmt.Sprintf("python playbook: unexpected handshake message %q", event.Type),
			Stderr:  p.stderr.String(),
		}
	}
}

// Execute sends the run command and drives the convergence loop to completion.
// The shim's dispatch_one/query_rows requests are served concurrently, so the
// script's own chunk/lane concurrency composes with the colony scheduler.
func (p *PythonPlaybook) Execute(ctx context.Context, args any, concurrencyCap int) (map[string]any, error) {
	p.setContext(ctx)
	// Parent-side hard limit for the whole run. The bootstrap bounds one
	// worker dispatch; nothing else bounds "the bridge went silent" (a dropped
	// reply, a wedged script, a dead reader thread) — which is exactly how a
	// run used to hang its caller forever. The same budget also bounds the
	// run-command write, so a child that stopped reading stdin cannot park the
	// caller in a full pipe.
	// Parent-side silence guard (not a whole-run cap: upstream run() is
	// unbounded). Any line from the child resets the budget below; a bridge that
	// goes silent for the whole budget is killed and reported, so a dropped
	// reply can never park the caller forever while slow-but-live batches run on.
	stallLimit := p.stallTimeout
	if stallLimit <= 0 {
		stallLimit = p.defaultTimeout + pythonStallGrace
	}
	deadline := time.Now().Add(stallLimit)
	var capPayload any
	if concurrencyCap > 0 {
		capPayload = concurrencyCap
	}
	if err := p.sendWithin(map[string]any{
		"type":            "run",
		"args":            args,
		"concurrency_cap": capPayload,
	}, time.Until(deadline)); err != nil {
		return nil, &PythonBridgeError{
			Message: "python playbook: cannot start the run: " + err.Error(),
			Stderr:  p.stderr.String(),
		}
	}
	runTimer := time.NewTimer(p.stallRemaining(stallLimit))
	defer runTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			p.terminate()
			return nil, ctx.Err()
		case <-runTimer.C:
			// A run with no terminal event inside the budget is over: kill the
			// child's process group, reap it, and report why. Returning while
			// the child lived on is what left queen tool calls and background
			// finishers parked indefinitely.
			p.stopProcess()
			return nil, p.runTimeoutError(stallLimit)
		case event, ok := <-p.events:
			if !ok {
				p.stopProcess()
				return nil, p.exitError()
			}
			switch event.Type {
			case "result":
				if len(event.Result) == 0 {
					return nil, &PythonBridgeError{
						Message: "python playbook: the interpreter returned no result",
						Stderr:  p.stderr.String(),
					}
				}
				var out map[string]any
				if err := json.Unmarshal(event.Result, &out); err != nil {
					return nil, &PythonBridgeError{
						Message: "python playbook: cannot decode the result: " + err.Error(),
						Stderr:  p.stderr.String(),
					}
				}
				if out == nil {
					out = map[string]any{}
				}
				if out["meta"] == nil {
					out["meta"] = p.Meta
				}
				return out, nil
			case "run_error":
				return nil, p.scriptError(event, "run")
			case "fatal":
				return nil, p.scriptError(event, "run")
			case "protocol_error":
				return nil, p.protocolError(event)
			default:
				return nil, &PythonBridgeError{
					Message: fmt.Sprintf("python playbook: unexpected bridge message %q", event.Type),
					Stderr:  p.stderr.String(),
				}
			}
		}
	}
}

// Close stops the child (SIGTERM then SIGKILL), reaps it, and removes the
// temporary bootstrap directory. Safe to call multiple times.
func (p *PythonPlaybook) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		close(p.closed)
		p.stopProcess()
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		if p.workDir != "" {
			_ = os.RemoveAll(p.workDir)
		}
	})
}

// stopProcess terminates the child's process group (SIGTERM, then SIGKILL
// after pythonKillGrace), waits for the reaper, and then waits — bounded — for
// the protocol readers to hit EOF. terminate/forceKill are idempotent and
// guarded by waitDone/termOnce, so this is safe to call repeatedly and from
// several goroutines (Execute's deadline path and Close can overlap).
func (p *PythonPlaybook) stopProcess() {
	if p == nil || p.cmd == nil {
		return
	}
	select {
	case <-p.waitDone:
	default:
		p.terminate()
		select {
		case <-p.waitDone:
		case <-time.After(pythonKillGrace):
			p.forceKill()
			select {
			case <-p.waitDone:
			case <-time.After(pythonKillGrace):
			}
		}
	}
	p.waitStreams(pythonKillGrace)
}

// waitStreams waits (bounded) for readLoop and drainStderr to exit. Both end at
// EOF, which stopProcess's kill guarantees; the bound only stops a pathological
// pipe from delaying the caller.
func (p *PythonPlaybook) waitStreams(limit time.Duration) {
	if limit <= 0 {
		return
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for _, done := range []chan struct{}{p.readDone, p.stderrDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-timer.C:
			return
		}
	}
}

func (p *PythonPlaybook) setContext(ctx context.Context) {
	p.ctxMu.Lock()
	p.ctx = ctx
	p.ctxMu.Unlock()
}

func (p *PythonPlaybook) context() context.Context {
	p.ctxMu.Lock()
	defer p.ctxMu.Unlock()
	if p.ctx == nil {
		return context.Background()
	}
	return p.ctx
}

func (p *PythonPlaybook) terminate() {
	p.termOnce.Do(func() {
		select {
		case <-p.waitDone:
			return
		default:
		}
		terminateProcess(p.cmd)
	})
}

func (p *PythonPlaybook) forceKill() {
	select {
	case <-p.waitDone:
		return
	default:
	}
	forceKillProcess(p.cmd)
}

func (p *PythonPlaybook) send(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.stdin == nil {
		return errors.New("bridge is closed")
	}
	_, err = p.stdin.Write(data)
	return err
}

// sendWithin writes one protocol message, giving up after limit. A pipe write
// blocks once the child stops reading; the timer plus the SIGTERM that follows
// keeps that from parking the caller forever (the write fails with a closed
// pipe once the child is gone).
func (p *PythonPlaybook) sendWithin(payload any, limit time.Duration) error {
	if limit <= 0 {
		return p.send(payload)
	}
	done := make(chan error, 1)
	go func() {
		done <- p.send(payload)
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		p.terminate()
		return fmt.Errorf("bridge write did not complete within %s", limit)
	}
}

func (p *PythonPlaybook) respond(id int64, result any) {
	if err := p.send(map[string]any{"id": id, "result": result}); err != nil {
		// The host is gone or the child is dying; the run will surface the
		// exit/protocol error on the event channel.
		_ = err
	}
}

func (p *PythonPlaybook) respondError(id int64, message string) {
	_ = p.send(map[string]any{"id": id, "error": message})
}

func (p *PythonPlaybook) waitLoop() {
	_ = p.cmd.Wait()
	close(p.waitDone)
}

func (p *PythonPlaybook) drainStderr(pipe io.ReadCloser) {
	defer close(p.stderrDone)
	defer pipe.Close()
	buf := make([]byte, 4096)
	for {
		n, err := pipe.Read(buf)
		if n > 0 {
			_, _ = p.stderr.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (p *PythonPlaybook) readLoop(pipe io.ReadCloser) {
	defer close(p.readDone)
	defer pipe.Close()
	defer close(p.events)
	reader := bufio.NewReaderSize(pipe, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && !p.handleLine(trimmed) {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.emitEvent(bridgeEvent{Type: "protocol_error", Err: err.Error()})
			}
			return
		}
	}
}

func (p *PythonPlaybook) handleLine(line []byte) bool {
	p.touchActivity()
	if len(line) > pythonMaxLineBytes {
		p.emitEvent(bridgeEvent{
			Type: "protocol_error",
			Err:  fmt.Sprintf("protocol line exceeds %d bytes", pythonMaxLineBytes),
		})
		return false
	}
	var message bridgeMessage
	if err := json.Unmarshal(line, &message); err != nil {
		p.emitEvent(bridgeEvent{
			Type: "protocol_error",
			Err:  "cannot parse interpreter output as JSON: " + err.Error(),
			Line: truncateProtocolLine(line),
		})
		return false
	}
	if message.Type != "" {
		event := bridgeEvent{
			Type:          message.Type,
			Meta:          message.Meta,
			PythonVersion: message.PythonVersion,
			Result:        message.Result,
		}
		if len(message.Error) > 0 {
			_ = json.Unmarshal(message.Error, &event.Error)
		}
		p.emitEvent(event)
		return true
	}
	if message.ID != 0 {
		go p.handleRequest(message)
		return true
	}
	p.emitEvent(bridgeEvent{
		Type: "protocol_error",
		Err:  "interpreter output has neither a type nor an id",
		Line: truncateProtocolLine(line),
	})
	return false
}

func (p *PythonPlaybook) emitEvent(event bridgeEvent) {
	select {
	case p.events <- event:
	case <-p.closed:
	}
}

func (p *PythonPlaybook) handleRequest(message bridgeMessage) {
	p.trackRequest(message.ID, message.Method)
	defer p.untrackRequest(message.ID)
	switch message.Method {
	case "query_rows":
		var params struct {
			SQL string `json:"sql"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			p.respondError(message.ID, "invalid query_rows params: "+err.Error())
			return
		}
		rows := []map[string]any{}
		if p.queryRows != nil {
			if got := p.queryRows(params.SQL); got != nil {
				rows = got
			}
		}
		p.respond(message.ID, rows)
	case "dispatch_one":
		var params struct {
			Task    string         `json:"task"`
			Data    map[string]any `json:"data"`
			Profile string         `json:"profile"`
			Timeout *float64       `json:"timeout"`
			Schema  map[string]any `json:"schema"`
			Goal    *string        `json:"goal"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			p.respondError(message.ID, "invalid dispatch_one params: "+err.Error())
			return
		}
		if p.dispatchOne == nil {
			p.respondError(message.ID, "no dispatch_one configured")
			return
		}
		options := DispatchOptions{
			Data:    params.Data,
			Profile: params.Profile,
			Schema:  params.Schema,
			Goal:    params.Goal,
		}
		if params.Timeout != nil && *params.Timeout > 0 {
			options.Timeout = time.Duration(*params.Timeout * float64(time.Second))
		}
		report, err := p.dispatchOne(p.context(), params.Task, options)
		if err != nil {
			p.respondError(message.ID, err.Error())
			return
		}
		p.respond(message.ID, report)
	default:
		p.respondError(message.ID, "unknown bridge method "+message.Method)
	}
}

// waitEvent waits for the next control event, the context, the process exit,
// or the load timeout — whichever comes first. It always terminates the child
// on the error paths so a hung load cannot leak a process.
func (p *PythonPlaybook) waitEvent(ctx context.Context, timeout time.Duration) (bridgeEvent, error) {
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case event, ok := <-p.events:
		if !ok {
			return bridgeEvent{}, p.exitError()
		}
		return event, nil
	case <-ctx.Done():
		p.terminate()
		return bridgeEvent{}, ctx.Err()
	case <-timer:
		p.terminate()
		return bridgeEvent{}, &PythonBridgeError{
			Message: fmt.Sprintf("python playbook did not finish loading within %s", timeout),
			Stderr:  p.stderr.String(),
		}
	}
}

func (p *PythonPlaybook) exitError() error {
	reaped := true
	select {
	case <-p.waitDone:
	case <-time.After(pythonKillGrace):
		// stopProcess was already called (or the child ignored SIGKILL); do
		// not wait forever for a reaper that will not come.
		reaped = false
	}
	code := -1
	// ProcessState is written by cmd.Wait; only read it once waitDone proves
	// the write happened (otherwise this would race the reaper).
	if reaped && p.cmd != nil && p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	message := "python playbook exited before producing a result"
	if !reaped {
		message = "python playbook stopped producing output and did not exit"
	}
	return &PythonBridgeError{
		Message:  message,
		Stderr:   p.stderr.String(),
		ExitCode: code,
		HasExit:  code >= 0,
	}
}

// runTimeoutError is the terminal answer when a run exceeds the parent-side
// limit. It names the budget, what the host last did on the wire, and the
// child's stderr tail, so a stall is diagnosable from the tool payload alone.
func (p *PythonPlaybook) runTimeoutError(limit time.Duration) error {
	served, inFlight := p.requestSnapshot()
	message := fmt.Sprintf("python playbook: no bridge activity for %s (bridge stalled)", limit)
	message += "; the interpreter did not answer and was terminated"
	message += fmt.Sprintf("; bridge requests served=%d", served)
	if len(inFlight) > 0 {
		message += "; in-flight=[" + strings.Join(inFlight, ", ") + "]"
	} else {
		message += "; in-flight=[]"
	}
	return &PythonBridgeError{
		Message: message,
		Stderr:  tailLines(p.stderr.String(), 20),
	}
}

// touchActivity records that the bridge produced a line; the silence guard
// measures from here, so a healthy long batch keeps resetting the budget.
func (p *PythonPlaybook) touchActivity() {
	p.reqMu.Lock()
	p.lastActivity = time.Now()
	p.reqMu.Unlock()
}

// stallRemaining is the time left before the silence budget expires.
func (p *PythonPlaybook) stallRemaining(limit time.Duration) time.Duration {
	p.reqMu.Lock()
	last := p.lastActivity
	p.reqMu.Unlock()
	if last.IsZero() {
		return limit
	}
	left := limit - time.Since(last)
	if left < time.Second {
		left = time.Second
	}
	return left
}

// trackRequest records an in-flight bridge request (the child asked, the host
// has not answered yet) so a stall report can name what was outstanding.
func (p *PythonPlaybook) trackRequest(id int64, method string) {
	p.reqMu.Lock()
	if p.reqInFlight == nil {
		p.reqInFlight = map[int64]string{}
	}
	p.reqInFlight[id] = method
	p.reqMu.Unlock()
}

// untrackRequest marks a bridge request answered (whatever the answer was).
func (p *PythonPlaybook) untrackRequest(id int64) {
	p.reqMu.Lock()
	if _, ok := p.reqInFlight[id]; ok {
		delete(p.reqInFlight, id)
		p.reqServed++
	}
	p.reqMu.Unlock()
}

func (p *PythonPlaybook) requestSnapshot() (int, []string) {
	p.reqMu.Lock()
	defer p.reqMu.Unlock()
	inFlight := make([]string, 0, len(p.reqInFlight))
	for id, method := range p.reqInFlight {
		inFlight = append(inFlight, fmt.Sprintf("%d:%s", id, method))
	}
	sort.Strings(inFlight)
	return p.reqServed, inFlight
}

// tailLines returns the last n lines of text for a one-message diagnosis.
func tailLines(text string, n int) string {
	if text == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (p *PythonPlaybook) scriptError(event bridgeEvent, stage string) error {
	wire := event.Error
	concise := wire.Concise
	if concise == "" {
		if wire.Kind != "" {
			concise = wire.Kind + ": " + wire.Message
		} else {
			concise = wire.Message
		}
	}
	return &PythonScriptError{
		Kind:      wire.Kind,
		Message:   wire.Message,
		Concise:   concise,
		Traceback: wire.Traceback,
		Stage:     stage,
	}
}

func (p *PythonPlaybook) protocolError(event bridgeEvent) error {
	message := "python playbook: cannot parse interpreter output"
	if event.Err != "" {
		message += ": " + event.Err
	}
	if event.Line != "" {
		message += " (line: " + event.Line + ")"
	}
	return &PythonBridgeError{Message: message, Stderr: p.stderr.String()}
}

func truncateProtocolLine(line []byte) string {
	text := strings.TrimSpace(string(line))
	const max = 400
	if len(text) > max {
		return text[:max] + "…"
	}
	return text
}

// pythonChildEnv builds the deliberately small environment the child sees:
// interpreter plumbing plus locale/temp essentials, never HIVE_* secrets or
// provider credentials from the host process.
func pythonChildEnv() []string {
	env := []string{
		"PYTHONUNBUFFERED=1",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONIOENCODING=utf-8",
	}
	for _, name := range []string{
		"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE",
		"TMPDIR", "TMP", "TEMP", "TZ",
		// Windows essentials for starting a console program / Python bootstrap.
		"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT",
		"USERPROFILE", "HOMEDRIVE", "HOMEPATH", "NUMBER_OF_PROCESSORS",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}
