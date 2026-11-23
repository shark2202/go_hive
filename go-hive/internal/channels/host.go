package channels

// Host: discovers configured channel plugins, starts one subprocess per
// enabled channel, serves their session requests, and streams QueenOutputs to
// their subscriptions. States mirror upstream's running/off/failed map served
// by GET /api/channels.
//
// Parity: framework/server/channels.py::start_channels
// Parity: framework/server/channels.py::stop_channels
// Parity-exempt: per-channel stdio subprocess discovery (the Go command
// extension) and the bounded output queue/seq drop policy.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HostOptions configures the process host.
type HostOptions struct {
	// Config is the parsed configuration.json map; only its channels section
	// is read.
	Config map[string]any
	// StateRoot is $HIVE_HOME/channels: each channel gets StateRoot/<name>.
	StateRoot string
	// BackendFor builds the session backend for one channel. The server
	// adapter passes the channel name so a claimed session records it.
	BackendFor func(name string) SessionBackend
	// Timeouts default to the upstream 30-second start/stop budget.
	StartTimeout     time.Duration
	StopTimeout      time.Duration
	HandshakeTimeout time.Duration
	CallTimeout      time.Duration
	// MaxFrameBytes and QueueSize bound the wire and the per-subscription
	// output queue.
	MaxFrameBytes int
	QueueSize     int
	Logf          func(format string, args ...any)
}

type channelRuntime struct {
	config  PluginConfig
	backend SessionBackend
	proc    atomic.Pointer[pluginProcess]
}

func (r *channelRuntime) process() *pluginProcess {
	if r == nil {
		return nil
	}
	return r.proc.Load()
}

// Host owns the plugin processes and their states. All exported methods are
// safe for concurrent use.
type Host struct {
	options HostOptions

	mu      sync.Mutex
	started bool
	stopped bool
	running map[string]*channelRuntime
	states  map[string]map[string]any
	subs    map[string]*subscription
	nextSub int64
	wg      sync.WaitGroup
}

// NewHost builds a host; Start does the work.
func NewHost(options HostOptions) *Host {
	if options.StartTimeout <= 0 {
		options.StartTimeout = DefaultStartTimeout
	}
	if options.StopTimeout <= 0 {
		options.StopTimeout = DefaultStopTimeout
	}
	if options.HandshakeTimeout <= 0 {
		options.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if options.CallTimeout <= 0 {
		options.CallTimeout = DefaultCallTimeout
	}
	if options.MaxFrameBytes <= 0 {
		options.MaxFrameBytes = DefaultMaxFrameBytes
	}
	if options.QueueSize <= 0 {
		options.QueueSize = DefaultQueueSize
	}
	if options.Logf == nil {
		options.Logf = log.Printf
	}
	return &Host{
		options: options,
		running: map[string]*channelRuntime{},
		states:  map[string]map[string]any{},
		subs:    map[string]*subscription{},
	}
}

// Start spawns every enabled channel, in configuration order. A channel that
// fails to start is recorded failed and never takes the host down.
func (h *Host) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	if h.started || h.stopped {
		h.mu.Unlock()
		return
	}
	h.started = true
	h.mu.Unlock()

	for _, plugin := range Plugins(h.options.Config) {
		h.startOne(ctx, plugin)
	}
}

func (h *Host) startOne(ctx context.Context, plugin PluginConfig) {
	if !plugin.Enabled {
		// Upstream still lists an installed-but-off channel; here a configured
		// but disabled one is off, not failed.
		h.setState(plugin.Name, map[string]any{"state": "off"})
		return
	}
	if !validChannelName(plugin.Name) {
		h.failStart(plugin.Name, fmt.Sprintf("invalid channel name %q: a channel name becomes a directory under the state root", plugin.Name))
		return
	}
	if plugin.ConfigError != "" {
		h.failStart(plugin.Name, plugin.ConfigError)
		return
	}
	if len(plugin.Argv()) == 0 {
		h.failStart(plugin.Name, "no command configured (Go host uses stdio plugins; Python hive.channels entry points need the Python adapter)")
		return
	}
	if h.options.BackendFor == nil {
		h.failStart(plugin.Name, "no session backend configured")
		return
	}

	stateDir := plugin.Name
	if strings.TrimSpace(h.options.StateRoot) != "" {
		stateDir = filepath.Join(h.options.StateRoot, plugin.Name)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		h.failStart(plugin.Name, fmt.Sprintf("state_dir %s: %v", stateDir, err))
		return
	}

	backend := h.options.BackendFor(plugin.Name)
	if backend == nil {
		h.failStart(plugin.Name, "no session backend configured")
		return
	}
	runtime := &channelRuntime{config: plugin, backend: backend}

	process, err := startPluginProcess(
		plugin.Name,
		plugin.Argv(),
		plugin.Cwd,
		plugin.Env,
		processOptions{
			maxFrame:    h.options.MaxFrameBytes,
			callTimeout: h.options.CallTimeout,
			logf:        h.options.Logf,
		},
		func(requestCtx context.Context, method string, params json.RawMessage) (any, error) {
			return h.handlePluginRequest(requestCtx, runtime, method, params)
		},
		func(exitErr error) { h.processExited(runtime, exitErr) },
	)
	if err != nil {
		h.failStart(plugin.Name, err.Error())
		return
	}
	runtime.proc.Store(process)

	hello := helloParams{
		Protocol: ProtocolVersion,
		Name:     plugin.Name,
		Config:   plugin.Section,
		StateDir: stateDir,
	}
	helloCtx, cancel := context.WithTimeout(ctx, h.options.HandshakeTimeout)
	result, rpcErr, callErr := process.call(helloCtx, "hello", hello, h.options.HandshakeTimeout)
	cancel()
	if callErr != nil {
		h.abortStart(plugin.Name, process, fmt.Sprintf("handshake failed: %v", callErr))
		return
	}
	if rpcErr != nil {
		h.abortStart(plugin.Name, process, fmt.Sprintf("handshake failed: %s", rpcErr.Message))
		return
	}
	var welcome welcomeResult
	if err := json.Unmarshal(result, &welcome); err != nil || strings.TrimSpace(welcome.PluginVersion) == "" {
		h.abortStart(plugin.Name, process, "handshake failed: welcome must name plugin_version")
		return
	}

	startCtx, startCancel := context.WithTimeout(ctx, h.options.StartTimeout)
	_, startErr, err := process.call(startCtx, "start", map[string]any{}, h.options.StartTimeout)
	startCancel()
	if err != nil {
		h.abortStart(plugin.Name, process, fmt.Sprintf("start failed: %v", err))
		return
	}
	if startErr != nil {
		h.abortStart(plugin.Name, process, fmt.Sprintf("start failed: %s", startErr.Message))
		return
	}

	h.mu.Lock()
	if h.stopped || process.isClosed() {
		h.mu.Unlock()
		h.abortStart(plugin.Name, process, "start failed: channel plugin exited during startup")
		return
	}
	h.running[plugin.Name] = runtime
	h.states[plugin.Name] = map[string]any{"state": "running"}
	h.mu.Unlock()
	h.options.Logf("channel %q started", plugin.Name)
}

func (h *Host) abortStart(name string, process *pluginProcess, message string) {
	process.fail(errors.New(message))
	process.stop(500 * time.Millisecond)
	h.failStart(name, message)
}

func (h *Host) failStart(name, message string) {
	h.setState(name, map[string]any{"state": "failed", "error": message})
	h.options.Logf("channel %q failed to start: %s", name, message)
}

func (h *Host) setState(name string, state map[string]any) {
	h.mu.Lock()
	h.states[name] = state
	h.mu.Unlock()
}

// States returns the GET /api/channels source: one entry per configured
// channel, state running/off/failed, with the error text on failed.
//
// Parity: framework/server/channels.py::handle_list_channels
func (h *Host) States() map[string]map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]map[string]any, len(h.states))
	for name, state := range h.states {
		copied := make(map[string]any, len(state))
		for key, value := range state {
			copied[key] = value
		}
		out[name] = copied
	}
	return out
}

// Stop asks every running plugin to stop, then tears down its subscriptions.
// It is idempotent and bounded by StopTimeout per channel.
//
// Parity: framework/server/channels.py::stop_channels
func (h *Host) Stop() {
	h.mu.Lock()
	if h.stopped || !h.started {
		h.stopped = true
		h.mu.Unlock()
		return
	}
	h.stopped = true
	runtimes := make([]*channelRuntime, 0, len(h.running))
	for _, runtime := range h.running {
		runtimes = append(runtimes, runtime)
	}
	h.mu.Unlock()

	for _, runtime := range runtimes {
		if process := runtime.process(); process != nil {
			process.stop(h.options.StopTimeout)
		}
	}
	h.closeSubscriptions(func(*subscription) bool { return true })
	h.wg.Wait()
}

func (h *Host) processExited(runtime *channelRuntime, exitErr error) {
	h.mu.Lock()
	process := runtime.process()
	if h.stopped || runtime == nil || process == nil || h.running[runtime.config.Name] != runtime {
		h.mu.Unlock()
		return
	}
	delete(h.running, runtime.config.Name)
	message := ""
	if terminal := process.terminalError(); terminal != nil {
		message = terminal.Error()
	} else if exitErr != nil {
		message = "channel plugin process exited: " + processErrorLine(exitErr)
	} else {
		message = "channel plugin process exited"
	}
	h.states[runtime.config.Name] = map[string]any{"state": "failed", "error": message}
	h.mu.Unlock()
	h.closeSubscriptions(func(sub *subscription) bool { return sub.runtime == runtime })
	h.options.Logf("channel %q failed: %s", runtime.config.Name, message)
}

// handlePluginRequest serves one method from a plugin. The params shape is
// documented in protocol.go; each result mirrors upstream's return value.
//
// Parity: framework/server/channels.py::ChannelHost
func (h *Host) handlePluginRequest(ctx context.Context, runtime *channelRuntime, method string, params json.RawMessage) (any, error) {
	if h.isStopping() {
		return nil, Errorf(KindInternal, "channel host is stopping")
	}
	if runtime == nil || runtime.backend == nil {
		return nil, Errorf(KindInternal, "channel has no session backend")
	}
	switch method {
	case "open_session":
		var request openSessionParams
		if err := decodeParams(params, &request); err != nil {
			return nil, InvalidParamsError("open_session: %v", err)
		}
		if strings.TrimSpace(request.QueenID) == "" {
			return nil, InvalidParamsError("open_session: queen_id is required")
		}
		sessionID, err := runtime.backend.OpenSession(ctx, request.QueenID, request.SessionID, request.Title)
		if err != nil {
			return nil, err
		}
		return openSessionResult{SessionID: sessionID}, nil
	case "send":
		var request sendParams
		if err := decodeParams(params, &request); err != nil {
			return nil, InvalidParamsError("send: %v", err)
		}
		if strings.TrimSpace(request.SessionID) == "" {
			return nil, InvalidParamsError("send: session_id is required")
		}
		images, err := decodeSendImages(request.Images)
		if err != nil {
			return nil, err
		}
		sessionID, err := runtime.backend.Send(ctx, request.SessionID, request.Text, images, request.DisplayText, request.CorrelationID)
		if err != nil {
			return nil, err
		}
		return sendResult{SessionID: sessionID}, nil
	case "subscribe":
		var request subscribeParams
		if err := decodeParams(params, &request); err != nil {
			return nil, InvalidParamsError("subscribe: %v", err)
		}
		if strings.TrimSpace(request.SessionID) == "" {
			return nil, InvalidParamsError("subscribe: session_id is required")
		}
		return h.subscribe(runtime, request.SessionID)
	case "unsubscribe":
		var request unsubscribeParams
		if err := decodeParams(params, &request); err != nil {
			return nil, InvalidParamsError("unsubscribe: %v", err)
		}
		h.unsubscribe(runtime, request.SubscriptionID)
		return map[string]any{}, nil
	case "set_title":
		var request setTitleParams
		if err := decodeParams(params, &request); err != nil {
			return nil, InvalidParamsError("set_title: %v", err)
		}
		if strings.TrimSpace(request.SessionID) == "" {
			return nil, InvalidParamsError("set_title: session_id is required")
		}
		if err := runtime.backend.SetTitle(ctx, request.SessionID, request.Title); err != nil {
			return nil, err
		}
		return map[string]any{}, nil
	case "judge":
		var request judgeParams
		if err := decodeParams(params, &request); err != nil {
			return nil, InvalidParamsError("judge: %v", err)
		}
		if strings.TrimSpace(request.Question) == "" {
			return nil, InvalidParamsError("judge: question is required")
		}
		probability, err := runtime.backend.Judge(ctx, request.Question, request.State, request.Criteria)
		if err != nil {
			return nil, err
		}
		return judgeResult{Probability: probability}, nil
	default:
		return nil, MethodNotFoundError(method)
	}
}

func (h *Host) isStopping() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}
