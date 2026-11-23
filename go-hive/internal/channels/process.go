package channels

// One plugin subprocess: spawn, frame I/O, request/response matching, and
// shutdown. The reader owns stdout exclusively; stderr is log output only.
//
// Parity-exempt: process transport for the stdio JSON-lines scheme.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errProcessExited   = errors.New("channel plugin process exited")
	errProcessTimedOut = errors.New("channel plugin request timed out")
	errFrameTooLarge   = errors.New("channel plugin frame exceeds the size limit")
)

// requestHandler serves one plugin→host request. The context is bounded by the
// host's call timeout and canceled on shutdown.
type requestHandler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type processOptions struct {
	maxFrame    int
	callTimeout time.Duration
	logf        func(format string, args ...any)
}

type rpcResult struct {
	result  json.RawMessage
	rpcErr  *rpcError
	failure error
}

type pluginProcess struct {
	name    string
	options processOptions

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	ctx    context.Context
	cancel context.CancelFunc

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan rpcResult
	nextID  int64
	closed  bool
	// terminalErr is the protocol/start failure that killed the process; the
	// host reports it instead of the bare exit status.
	terminalErr error

	done     chan struct{}
	doneOnce sync.Once
	intended atomic.Bool
	handler  requestHandler
	requests sync.WaitGroup
	onExit   func(error)
}

func startPluginProcess(name string, argv []string, cwd string, env []string, options processOptions, handler requestHandler, onExit func(error)) (*pluginProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	ctx, cancel := context.WithCancel(context.Background())
	process := &pluginProcess{
		name:    name,
		options: options,
		ctx:     ctx,
		cancel:  cancel,
		pending: map[string]chan rpcResult{},
		done:    make(chan struct{}),
		handler: handler,
		onExit:  onExit,
	}
	if process.options.maxFrame <= 0 {
		process.options.maxFrame = DefaultMaxFrameBytes
	}
	if process.options.callTimeout <= 0 {
		process.options.callTimeout = DefaultCallTimeout
	}
	if process.options.logf == nil {
		process.options.logf = func(format string, args ...any) {}
	}

	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = cwd
	if len(env) > 0 {
		command.Env = mergeEnv(os.Environ(), env)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	process.cmd = command
	process.stdin = stdin
	process.stdout = stdout
	process.stderr = stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	go process.readLoop()
	go process.stderrLoop()
	go process.waitLoop()
	return process, nil
}

// mergeEnv applies the configured KEY=value overrides on top of the inherited
// environment, replacing any inherited entry with the same key.
func mergeEnv(base, overrides []string) []string {
	overridden := map[string]bool{}
	for _, entry := range overrides {
		if key, _, _ := strings.Cut(entry, "="); key != "" {
			overridden[key] = true
		}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		if key, _, _ := strings.Cut(entry, "="); !overridden[key] {
			merged = append(merged, entry)
		}
	}
	for _, entry := range overrides {
		if key, _, _ := strings.Cut(entry, "="); key != "" {
			merged = append(merged, entry)
		}
	}
	return merged
}

func (p *pluginProcess) readLoop() {
	reader := bufio.NewReaderSize(p.stdout, 64*1024)
	for {
		line, err := readLineLimited(reader, p.options.maxFrame)
		if err != nil {
			if errors.Is(err, errFrameTooLarge) {
				p.fail(fmt.Errorf("%s: %w", p.name, err))
			}
			return
		}
		frame, err := decodeEnvelope(line)
		if err != nil {
			// stdout is a protocol channel only. One malformed line means the
			// plugin (or something it printed) cannot be trusted.
			p.fail(fmt.Errorf("%s: plugin wrote a non-protocol line on stdout: %v", p.name, err))
			return
		}
		switch {
		case frame.Method != "" && frame.hasID():
			p.serveRequest(frame)
		case frame.Method != "":
			// Plugin→host notifications are not part of the contract; ignore
			// rather than guessing at a handler.
			p.options.logf("channel %s: ignoring plugin notification %q", p.name, frame.Method)
		case frame.isResponse():
			p.resolve(frame)
		}
	}
}

func (p *pluginProcess) stderrLoop() {
	scanner := bufio.NewScanner(p.stderr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		p.options.logf("channel %s: %s", p.name, scanner.Text())
	}
}

func (p *pluginProcess) waitLoop() {
	err := p.cmd.Wait()
	p.finish(err)
}

func (p *pluginProcess) serveRequest(frame *envelope) {
	rawID := append(json.RawMessage(nil), frame.ID...)
	p.requests.Add(1)
	go func() {
		defer p.requests.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				p.writeError(rawID, CodeInternal, fmt.Sprintf("channel host panic: %v", recovered), KindInternal)
			}
		}()
		ctx, cancel := context.WithTimeout(p.ctx, p.options.callTimeout)
		defer cancel()
		result, err := p.handler(ctx, frame.Method, frame.Params)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			channelErr := AsChannelError(err)
			code := channelErr.Code
			if code == 0 {
				code = CodeChannelError
			}
			p.writeError(rawID, code, channelErr.Message, channelErr.Kind)
			return
		}
		p.writeSuccess(rawID, result)
	}()
}

func (p *pluginProcess) resolve(frame *envelope) {
	key := requestKey(frame.ID)
	p.mu.Lock()
	waiter, ok := p.pending[key]
	if ok {
		delete(p.pending, key)
	}
	p.mu.Unlock()
	if !ok {
		p.options.logf("channel %s: response for unknown request id %s", p.name, key)
		return
	}
	waiter <- rpcResult{result: frame.Result, rpcErr: frame.Error}
}

func (p *pluginProcess) call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, *rpcError, error) {
	p.mu.Lock()
	if p.closed {
		err := p.terminalErr
		p.mu.Unlock()
		if err == nil {
			err = errProcessExited
		}
		return nil, nil, err
	}
	p.nextID++
	id := strconv.FormatInt(p.nextID, 10)
	key := requestKey(json.RawMessage(strconv.Quote(id)))
	waiter := make(chan rpcResult, 1)
	p.pending[key] = waiter
	p.mu.Unlock()

	cleanup := func() {
		p.mu.Lock()
		delete(p.pending, key)
		p.mu.Unlock()
	}

	if err := p.writeRequest(id, method, params); err != nil {
		cleanup()
		return nil, nil, err
	}
	if timeout <= 0 {
		timeout = p.options.callTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case reply := <-waiter:
		if reply.failure != nil {
			return nil, nil, reply.failure
		}
		return reply.result, reply.rpcErr, nil
	case <-timer.C:
		cleanup()
		return nil, nil, fmt.Errorf("%w: %s after %s", errProcessTimedOut, method, timeout)
	case <-ctx.Done():
		cleanup()
		return nil, nil, ctx.Err()
	case <-p.done:
		// The waiter may have been filled just before the process finished.
		select {
		case reply := <-waiter:
			if reply.failure != nil {
				return nil, nil, reply.failure
			}
			return reply.result, reply.rpcErr, nil
		default:
		}
		cleanup()
		if err := p.terminalError(); err != nil {
			return nil, nil, err
		}
		return nil, nil, errProcessExited
	}
}

func (p *pluginProcess) writeRequest(id, method string, params any) error {
	line, err := marshalRequest(id, method, params)
	if err != nil {
		return fmt.Errorf("encode %s: %w", method, err)
	}
	return p.writeLine(line)
}

func (p *pluginProcess) notify(method string, params any) error {
	line, err := marshalNotification(method, params)
	if err != nil {
		return fmt.Errorf("encode %s: %w", method, err)
	}
	return p.writeLine(line)
}

func (p *pluginProcess) writeSuccess(id json.RawMessage, result any) {
	line, err := marshalSuccess(id, result)
	if err != nil {
		p.options.logf("channel %s: encode response: %v", p.name, err)
		return
	}
	if err := p.writeLine(line); err != nil {
		p.options.logf("channel %s: write response for %s: %v", p.name, string(id), err)
	}
}

func (p *pluginProcess) writeError(id json.RawMessage, code int, message string, kind ErrorKind) {
	line, err := marshalError(id, code, message, kind)
	if err != nil {
		p.options.logf("channel %s: encode error response: %v", p.name, err)
		return
	}
	_ = p.writeLine(line)
}

func (p *pluginProcess) writeLine(line []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if p.isClosed() {
		return errProcessExited
	}
	if len(line) > p.options.maxFrame {
		return errFrameTooLarge
	}
	if _, err := p.stdin.Write(line); err != nil {
		return fmt.Errorf("write to plugin: %w", err)
	}
	return nil
}

func (p *pluginProcess) finish(err error) {
	p.doneOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		pending := make([]chan rpcResult, 0, len(p.pending))
		for key, waiter := range p.pending {
			pending = append(pending, waiter)
			delete(p.pending, key)
		}
		terminal := p.terminalErr
		p.mu.Unlock()
		p.cancel()
		_ = p.stdin.Close()
		for _, waiter := range pending {
			failure := errProcessExited
			if terminal != nil {
				failure = terminal
			} else if err != nil {
				failure = fmt.Errorf("%w: %v", errProcessExited, err)
			}
			waiter <- rpcResult{failure: failure}
		}
		close(p.done)
		if p.onExit != nil {
			p.onExit(err)
		}
	})
}

// fail records a protocol failure, kills the process and lets finish report
// the recorded error to the host.
func (p *pluginProcess) fail(err error) {
	p.mu.Lock()
	if p.terminalErr == nil {
		p.terminalErr = err
	}
	p.mu.Unlock()
	p.kill()
}

func (p *pluginProcess) kill() {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// stop asks the plugin to stop, closes stdin, and waits for the process. A
// plugin that ignores the request is killed after the stop budget.
func (p *pluginProcess) stop(timeout time.Duration) {
	if p == nil {
		return
	}
	p.intended.Store(true)
	if !p.isClosed() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		_, _, _ = p.call(ctx, "stop", map[string]any{}, timeout)
		cancel()
	}
	_ = p.stdin.Close()
	grace := 2 * time.Second
	if timeout > 0 && timeout < grace {
		grace = timeout
	}
	select {
	case <-p.done:
		return
	case <-time.After(grace):
	}
	p.kill()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

// requestKey normalizes a response id to the key call() registered. The host
// sends string ids; a plugin that echoes them unchanged arrives quoted in the
// raw JSON, so a string id is unquoted before matching.
func requestKey(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return "str:" + text
	}
	return "raw:" + string(bytes.TrimSpace(raw))
}

func (p *pluginProcess) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *pluginProcess) terminalError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminalErr
}

// readLineLimited reads one newline-terminated line without letting a plugin
// allocate unbounded memory through a huge stdout line.
func readLineLimited(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, 1024)
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > limit {
			return nil, errFrameTooLarge
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return line, nil
	}
}

// processErrorLine normalizes an exit error for the failed-state message.
func processErrorLine(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return "process exited"
	}
	return text
}
