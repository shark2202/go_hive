package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/browserbridge/ws"
)

const (
	// DefaultWSAddr is the Chrome extension's primary bridge endpoint.
	DefaultWSAddr = "127.0.0.1:14829"
	// DefaultLegacyAddr is the pre-migration endpoint still probed by the
	// shipped extension (offscreen.js rotates 14829 -> 9229).
	DefaultLegacyAddr = "127.0.0.1:9229"
	// DefaultLegacyStatusAddr is the pre-migration status endpoint
	// (LEGACY_BRIDGE_PORT + 1). Upstream's start() binds the same status
	// handler on 9229+1 beside the legacy WS listener, and the shipped side
	// panel plus the runtime's bridge probe still dial it.
	DefaultLegacyStatusAddr = "127.0.0.1:9230"
	// DefaultHTTPAddr is the local-only agent API.
	DefaultHTTPAddr = "127.0.0.1:14830"

	// BridgeVersion is this bridge's own version, reported as /status
	// bridge_version (upstream reports its gcu package version there). Keep
	// it in lockstep with cmd/browserbridge's -version output.
	BridgeVersion = "0.3.0-l3a"

	defaultCommandTimeout = 30 * time.Second
	defaultPingInterval   = 5 * time.Second
	defaultPongTimeout    = 12 * time.Second
	defaultHelloTimeout   = 10 * time.Second
	minProtocolVersion    = 1

	// WS-layer keepalive, the websockets.serve(ping_interval=…,
	// ping_timeout=…) values from bridge.py: _WS_PING_INTERVAL_S /
	// _WS_PING_TIMEOUT_S. The app-level 5s/12s ping stays on top — this one
	// catches a half-open TCP path that the JSON pong cannot.
	DefaultWSPingInterval = 20 * time.Second
	DefaultWSPingTimeout  = 20 * time.Second
)

// Config configures a Bridge. Zero values get the upstream defaults.
type Config struct {
	WSAddr         string
	LegacyAddr     string // empty disables the legacy listener
	HTTPAddr       string
	ControlAddr    string // empty means DefaultControlAddr (127.0.0.1:14831)
	CommandTimeout time.Duration
	PingInterval   time.Duration
	PongTimeout    time.Duration
	HelloTimeout   time.Duration
	MaxMessageSize int64
	// LegacyStatusAddr is the HTTP status endpoint that accompanies
	// LegacyAddr (upstream LEGACY_STATUS_PORT 9230). Empty means
	// DefaultLegacyStatusAddr whenever LegacyAddr is enabled; it is not
	// bound at all when LegacyAddr is empty (the binary's -no-legacy switch).
	LegacyStatusAddr string
	// WSPingInterval / WSPingTimeout are the WS-layer keepalive
	// (websockets ping_interval / ping_timeout). Zero uses 20s/20s.
	WSPingInterval time.Duration
	WSPingTimeout  time.Duration
	Logf           func(format string, args ...any)
	Events         EventSink
	Telemetry      Telemetry
	Health         HealthProvider
	RateLimiter    RateLimiter

	// Control-plane tunables. Zero values use the upstream constants (a fresh
	// control socket counts as a client for 30 s until client_hello, and the
	// reaper runs every 10 s). Tests shorten them.
	ControlIdentifyGrace time.Duration
	ControlReapInterval  time.Duration

	// BridgeVersion is reported as /status bridge_version (upstream's gcu
	// package version). Empty uses the package const BridgeVersion.
	BridgeVersion string
	// RuntimeVersion is reported as /status runtime_version. Nil renders as
	// null, matching upstream's BeelineBridge(runtime_version=None) when the
	// embedding app passes nothing (its production default); an embedder that
	// knows the Hive app version may set it explicitly.
	RuntimeVersion *string

	// ViewportScale is the GCU viewport scale this bridge reports
	// (framework/config.py::get_gcu_viewport_scale via the parent's
	// HIVE_BROWSER_VIEWPORT_SCALE). Zero resolves from the environment.
	ViewportScale float64

	// ShutdownRequest is invoked by POST /shutdown. In the supervised
	// deployment the binary wires it to a graceful exit, which makes the
	// supervisor treat the exit as intentional (upstream SIGTERMs its
	// supervisor instead). nil keeps /shutdown a no-op.
	ShutdownRequest func()

	// L2 runtime policy. Zero values are conservative:
	//   - stale-context sweep runs every 30s;
	//   - telemetry JSONL is off unless TelemetryDir is set;
	//   - the social rate-limit hook is off unless EnableRateLimits is set.
	SweepInterval       time.Duration
	SweepDisabled       bool
	TelemetryDir        string
	EnableRateLimits    bool
	RateLimitPath       string
	RateLimitConfigPath string
	DefaultLabelFile    string
	// Notify is the optional user-initiated hand-over notification hook. The
	// upstream implementation pushes this over the control RPC (L3); callers
	// without RPC can still observe/forward the exact user message.
	Notify func(profile, text string, tabID int, action string)

	// L3 additions.
	//
	// ArtifactDir overrides where browser_snapshot / browser_html spill their
	// raw payloads (default: $HIVE_STORAGE_PATH/data, then
	// $HIVE_HOME/tool-artifacts, then ~/.hive/tool-artifacts).
	ArtifactDir string
	// ExtensionDir is the on-disk path of the bundled Chrome extension. When
	// set it is reported by browser_setup so a user can Load unpacked.
	ExtensionDir string
	// SkillDirs are the roots searched by browser_script for a skill's
	// scripts/<name>.py (default: $HIVE_HOME/skills, ./skills, ./.hive/skills).
	SkillDirs []string
	// ScriptCommand is the interpreter used by browser_script. Empty means
	// ["uv", "run", "python"].
	ScriptCommand []string
}

func (c Config) withDefaults() Config {
	if c.WSAddr == "" {
		c.WSAddr = DefaultWSAddr
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = DefaultHTTPAddr
	}
	if c.ControlAddr == "" {
		c.ControlAddr = DefaultControlAddr
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = defaultCommandTimeout
	}
	if c.PingInterval <= 0 {
		c.PingInterval = defaultPingInterval
	}
	if c.PongTimeout <= 0 {
		c.PongTimeout = defaultPongTimeout
	}
	if c.HelloTimeout <= 0 {
		c.HelloTimeout = defaultHelloTimeout
	}
	if c.WSPingInterval <= 0 {
		c.WSPingInterval = DefaultWSPingInterval
	}
	if c.WSPingTimeout <= 0 {
		c.WSPingTimeout = DefaultWSPingTimeout
	}
	// The legacy status listener is part of the legacy migration pair: it is
	// only meaningful (and only bound) while the legacy WS port is enabled.
	if c.LegacyAddr != "" && c.LegacyStatusAddr == "" {
		c.LegacyStatusAddr = DefaultLegacyStatusAddr
	}
	if c.MaxMessageSize <= 0 {
		// Upstream websockets.serve(max_size=50*1024*1024); keep the two
		// listeners (extension relay + control RPC) on that same ceiling.
		c.MaxMessageSize = ws.DefaultMaxRead
	}
	if c.BridgeVersion == "" {
		c.BridgeVersion = BridgeVersion
	}
	if c.Logf == nil {
		c.Logf = log.Printf
	}
	if c.Events == nil {
		c.Events = nopEvents{}
	}
	if c.Health == nil {
		c.Health = nopHealth{}
	}
	if c.SweepInterval <= 0 {
		c.SweepInterval = 30 * time.Second
	}
	if c.DefaultLabelFile == "" {
		c.DefaultLabelFile = defaultLabelPath()
	}
	if c.RateLimitPath == "" {
		c.RateLimitPath = defaultRateLimitPath()
	}
	if c.RateLimitConfigPath == "" {
		c.RateLimitConfigPath = defaultConfigPath()
	}
	if c.ViewportScale <= 0 {
		// get_gcu_viewport_scale's value: the runtime's supervisor normally
		// delivers it through HIVE_BROWSER_VIEWPORT_SCALE, and a hand-started
		// bridge falls back to configuration.json (see viewport.go).
		c.ViewportScale = ResolveViewportScale()
	}
	return c
}

// Bridge owns the extension connections and command routing.
type Bridge struct {
	cfg     Config
	started time.Time

	mu     sync.RWMutex
	conns  map[string]*Connection
	tabs   map[int]string // tab id -> owning profile label
	counts map[string]int // profile label -> last known tab count

	contexts    *MemoryContextStore
	events      EventSink
	telemetry   Telemetry
	health      HealthProvider
	rateLimiter RateLimiter

	// L2 process-local registries (see state.go). stateMu is separate from
	// b.mu so extension calls never hold the routing lock.
	stateMu          sync.Mutex
	tabOwner         map[int]string
	tabActiveMS      map[int]int64
	tabActions       map[int][]TabAction
	tabSnapshots     map[int]map[string]any
	tabBlockers      map[int][]Blocker
	tabHighlights    map[int]map[string]any
	pendingDialogs   map[int]map[string]any
	hiveTabs         map[int]struct{}
	orphanSeen       map[int]int
	ungroupedSeen    map[int]int
	persistedGroups  map[int]struct{}
	contextInFlight  map[string]int
	contextActiveTab map[string]int
	hiveWindowByConn map[string]int
	windowLocks      map[string]*sync.Mutex
	// tabLocks serializes CDP commands per tab id (upstream _tab_locks /
	// bridge.py::_tab_lock). Entries are dropped when a tab closes/reports
	// removed so the keyspace stays bounded by live tabs.
	tabLocks       map[int]chan struct{}
	starredDefault string
	// bindErrors records optional-listener bind failures for /status
	// (upstream _bind_errors). Guarded by stateMu.
	bindErrors map[string]string

	sweepStop    chan struct{}
	sweepDone    chan struct{}
	stopOnce     sync.Once
	sweepRunning atomic.Bool

	serverMu   sync.Mutex
	wss        *ws.Server
	legacyWSS  *ws.Server
	httpServer *http.Server
	// legacyStatusSrv is the 9229+1 status endpoint that mirrors upstream's
	// legacy migration pair. Guarded by serverMu like the other listeners.
	legacyStatusSrv  *http.Server
	control          *controlRPC
	wsAddr           string
	httpAddr         string
	legacyAddr       string
	legacyStatusAddr string
	shutdown         atomic.Bool
}

// New creates a bridge; call Start to bind listeners.
func New(cfg Config) *Bridge {
	cfg = cfg.withDefaults()
	b := &Bridge{
		cfg:              cfg,
		started:          time.Now(),
		conns:            make(map[string]*Connection),
		tabs:             make(map[int]string),
		counts:           make(map[string]int),
		contexts:         NewMemoryContextStore(),
		events:           cfg.Events,
		telemetry:        cfg.Telemetry,
		health:           cfg.Health,
		rateLimiter:      cfg.RateLimiter,
		tabOwner:         make(map[int]string),
		tabActiveMS:      make(map[int]int64),
		tabActions:       make(map[int][]TabAction),
		tabSnapshots:     make(map[int]map[string]any),
		tabBlockers:      make(map[int][]Blocker),
		tabHighlights:    make(map[int]map[string]any),
		pendingDialogs:   make(map[int]map[string]any),
		hiveTabs:         make(map[int]struct{}),
		orphanSeen:       make(map[int]int),
		ungroupedSeen:    make(map[int]int),
		persistedGroups:  make(map[int]struct{}),
		contextInFlight:  make(map[string]int),
		contextActiveTab: make(map[string]int),
		hiveWindowByConn: make(map[string]int),
		windowLocks:      make(map[string]*sync.Mutex),
		tabLocks:         make(map[int]chan struct{}),
		bindErrors:       make(map[string]string),
		sweepStop:        make(chan struct{}),
		sweepDone:        make(chan struct{}),
	}
	if b.telemetry == nil && cfg.TelemetryDir != "" {
		b.telemetry = NewJSONLTelemetry(cfg.TelemetryDir)
	}
	if b.telemetry == nil {
		b.telemetry = nopTelemetry{}
	}
	if b.rateLimiter == nil && cfg.EnableRateLimits {
		b.rateLimiter = NewSocialRateLimiter(cfg.RateLimitPath, cfg.RateLimitConfigPath)
	}
	if b.rateLimiter == nil {
		b.rateLimiter = nopRateLimiter{}
	}
	b.starredDefault = loadStarredDefault(cfg.DefaultLabelFile)
	return b
}

// Start binds the WebSocket and HTTP listeners. The legacy 9229/9230 listeners
// are best-effort: a port conflict is logged rather than aborting startup.
func (b *Bridge) Start(ctx context.Context) error {
	wsLn, err := net.Listen("tcp", b.cfg.WSAddr)
	if err != nil {
		return fmt.Errorf("listen websocket %s: %w", b.cfg.WSAddr, err)
	}
	wss := &ws.Server{
		Path:         "/bridge",
		Handler:      b.handleWS,
		MaxRead:      b.cfg.MaxMessageSize,
		Logger:       b.cfg.Logf,
		PingInterval: b.cfg.WSPingInterval,
		PingTimeout:  b.cfg.WSPingTimeout,
	}
	httpLn, err := net.Listen("tcp", b.cfg.HTTPAddr)
	if err != nil {
		_ = wsLn.Close()
		return fmt.Errorf("listen http %s: %w", b.cfg.HTTPAddr, err)
	}
	httpSrv := &http.Server{Handler: NewHTTPHandler(b)}

	b.serverMu.Lock()
	b.wss = wss
	b.wsAddr = wsLn.Addr().String()
	b.httpServer = httpSrv
	b.httpAddr = httpLn.Addr().String()
	b.serverMu.Unlock()

	go func() {
		if err := wss.Serve(wsLn); err != nil {
			b.cfg.Logf("websocket server stopped: %v", err)
		}
	}()
	go func() {
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			b.cfg.Logf("http server stopped: %v", err)
		}
	}()

	b.serverMu.Lock()
	primaryWS := b.wsAddr
	b.serverMu.Unlock()
	if b.cfg.LegacyAddr != "" && b.cfg.LegacyAddr != primaryWS {
		// Upstream's `if port != LEGACY_BRIDGE_PORT` guard: when the primary
		// relay already sits on the legacy port there is no second pair to
		// bind (the attempt would just collide with itself).
		b.startLegacy(b.cfg.LegacyAddr)
		// Upstream binds the status handler on LEGACY_STATUS_PORT beside the
		// legacy WS listener (bridge.py start()); the probes that still dial
		// 9230 expect the same payload.
		b.startLegacyStatus(b.cfg.LegacyStatusAddr)
	}
	// Control RPC (14831): the out-of-process method surface. A bind failure
	// is logged, not fatal — the relay and HTTP API stay usable.
	control := newControlRPC(b)
	b.serverMu.Lock()
	b.control = control
	b.serverMu.Unlock()
	if err := control.start(b.cfg.ControlAddr); err != nil {
		b.recordBindError("control_rpc", b.cfg.ControlAddr, err)
	}
	if !b.cfg.SweepDisabled {
		b.sweepRunning.Store(true)
		go b.sweepLoop()
	}
	b.cfg.Logf("browser bridge listening: ws=%s path=/bridge http=%s control=%q legacy=%q viewport_scale=%s",
		b.wsAddr, b.httpAddr, b.ControlAddr(), b.legacyAddr, formatScale(b.cfg.ViewportScale))
	return nil
}

func (b *Bridge) startLegacy(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		b.recordBindError("legacy_ws", addr, err)
		b.cfg.Logf("legacy websocket %s unavailable (continuing): %v", addr, err)
		return
	}
	srv := &ws.Server{
		Path:         "/bridge",
		Handler:      b.handleWS,
		MaxRead:      b.cfg.MaxMessageSize,
		Logger:       b.cfg.Logf,
		PingInterval: b.cfg.WSPingInterval,
		PingTimeout:  b.cfg.WSPingTimeout,
	}
	b.serverMu.Lock()
	b.legacyWSS = srv
	b.legacyAddr = ln.Addr().String()
	b.serverMu.Unlock()
	b.cfg.Logf("legacy websocket listening: %s path=/bridge", ln.Addr())
	go func() {
		if err := srv.Serve(ln); err != nil {
			b.cfg.Logf("legacy websocket stopped: %v", err)
		}
	}()
}

// startLegacyStatus binds the legacy 9229+1 status listener. It serves the
// same handler as the primary 14830 API (upstream reuses one
// _http_status_handler for both ports), so a probe that falls back to 9230
// gets the full status payload instead of a connection refusal. Mirrors
// upstream's `if port != LEGACY_BRIDGE_PORT` guard: a primary API already on
// the legacy status port skips the second bind.
func (b *Bridge) startLegacyStatus(addr string) {
	if addr == "" {
		return
	}
	b.serverMu.Lock()
	primaryHTTP := b.httpAddr
	b.serverMu.Unlock()
	if addr == primaryHTTP {
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		b.recordBindError("legacy_status", addr, err)
		b.cfg.Logf("legacy status %s unavailable (continuing): %v", addr, err)
		return
	}
	srv := &http.Server{Handler: NewHTTPHandler(b)}
	b.serverMu.Lock()
	b.legacyStatusSrv = srv
	b.legacyStatusAddr = ln.Addr().String()
	b.serverMu.Unlock()
	b.cfg.Logf("legacy status listening: http://%s/status", ln.Addr())
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			b.cfg.Logf("legacy status stopped: %v", err)
		}
	}()
}

// WSAddr returns the bound WebSocket address.
func (b *Bridge) WSAddr() string {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()
	return b.wsAddr
}

// HTTPAddr returns the bound HTTP address.
func (b *Bridge) HTTPAddr() string {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()
	return b.httpAddr
}

// LegacyAddr returns the bound legacy WebSocket address (empty when disabled
// or when the best-effort bind failed).
func (b *Bridge) LegacyAddr() string {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()
	return b.legacyAddr
}

// LegacyStatusAddr returns the bound legacy HTTP status address (empty when the
// legacy pair is disabled or the best-effort bind failed).
func (b *Bridge) LegacyStatusAddr() string {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()
	return b.legacyStatusAddr
}

// ControlAddr returns the bound control-RPC address (empty when the control
// listener failed to bind).
func (b *Bridge) ControlAddr() string {
	control := b.controlRPC()
	if control == nil {
		return ""
	}
	return control.address()
}

// ControlListening reports whether the control-RPC listener is bound.
func (b *Bridge) ControlListening() bool {
	control := b.controlRPC()
	return control != nil && control.listening()
}

// ControlClients is the live count of control-RPC clients by the reaper's rule
// (identified owners alive, or still inside the identify grace). This is the
// idle watchdog's only signal that someone is doing browser work.
func (b *Bridge) ControlClients() int {
	control := b.controlRPC()
	if control == nil {
		return 0
	}
	return control.activeClientCount()
}

// NotifyClients pushes a server-initiated notify frame to every control client
// that subscribed, returning how many were reached. It is the transport behind
// the side-panel adopt/release messages.
func (b *Bridge) NotifyClients(profile, text string, extra map[string]any) int {
	control := b.controlRPC()
	if control == nil {
		return 0
	}
	return control.notify(profile, text, extra)
}

// RuntimeAlive resolves status.runtime_alive across the control clients, then
// falls back to the HIVE_DESKTOP_PARENT_PID hint while no client has
// identified itself (upstream _runtime_alive).
func (b *Bridge) RuntimeAlive() *bool {
	if control := b.controlRPC(); control != nil {
		if fromClients := control.runtimeAlive(); fromClients != nil {
			return fromClients
		}
	}
	raw := os.Getenv("HIVE_DESKTOP_PARENT_PID")
	pid, ok := toInt(raw)
	if !ok || pid <= 0 {
		return nil
	}
	alive := pidAlive(pid)
	return &alive
}

// controlRPC returns the control server under serverMu: Shutdown clears the
// field while the idle watchdog and status readers may still be consulting it.
func (b *Bridge) controlRPC() *controlRPC {
	b.serverMu.Lock()
	defer b.serverMu.Unlock()
	return b.control
}

// Shutdown closes listeners and live extension sockets.
func (b *Bridge) Shutdown(ctx context.Context) error {
	b.shutdown.Store(true)
	if b.sweepRunning.Load() {
		b.stopOnce.Do(func() { close(b.sweepStop) })
		select {
		case <-b.sweepDone:
		case <-ctx.Done():
		}
	}
	b.mu.RLock()
	conns := make([]*Connection, 0, len(b.conns))
	for _, c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.RUnlock()
	for _, c := range conns {
		c.failPending(newError(CodeConnectionLost, "The browser bridge is shutting down.", false))
		c.Close(1001, "server_shutdown")
	}

	b.serverMu.Lock()
	wss, legacy, legacyStatus, httpSrv := b.wss, b.legacyWSS, b.legacyStatusSrv, b.httpServer
	b.serverMu.Unlock()
	b.serverMu.Lock()
	control := b.control
	b.control = nil
	b.serverMu.Unlock()
	if control != nil {
		control.stopServer(ctx)
	}
	var firstErr error
	if wss != nil {
		if err := wss.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if legacy != nil {
		if err := legacy.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if legacyStatus != nil {
		if err := legacyStatus.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if httpSrv != nil {
		if err := httpSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if closer, ok := b.telemetry.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	return firstErr
}

func (b *Bridge) handleWS(c *ws.Conn) {
	conn := newConnection(c)
	b.events.ConnectionEvent("connect", "", "")
	_ = c.SetReadDeadline(time.Now().Add(b.cfg.HelloTimeout))
	defer b.dropConnection(conn)

	for {
		op, data, err := c.ReadMessage()
		if err != nil {
			if ce, ok := err.(*ws.CloseError); ok {
				b.cfg.Logf("extension websocket closed: label=%q code=%d reason=%q", conn.Label(), ce.Code, ce.Reason)
			}
			return
		}
		if op != ws.Text {
			continue // the bridge protocol is JSON text only
		}
		b.handleMessage(conn, data)
	}
}

func (b *Bridge) handleMessage(conn *Connection, data []byte) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		b.cfg.Logf("ignoring non-JSON frame from %s", conn.remoteAddr)
		return
	}
	typ := rawString(msg["type"])
	switch typ {
	case "hello":
		b.handleHello(conn, msg)
		return
	case "ping":
		// Extension-initiated half of the two-way health check.
		_ = conn.writeJSON(map[string]any{"type": "pong"})
		return
	case "pong":
		conn.markPong()
		return
	case "cdp_event":
		// Unsolicited CDP events forwarded by the extension. L2 consumes
		// the same channels upstream does: viewport/attach telemetry,
		// console rows, pending dialogs, and health-cache invalidation.
		params := map[string]any{}
		if raw, ok := msg["params"]; ok {
			_ = json.Unmarshal(raw, &params)
		}
		tabID := int(rawInt(msg["tabId"]))
		method := rawString(msg["method"])
		b.events.CDPEvent(conn.Label(), tabID, method, params)
		b.handleCDPEvent(conn.Label(), tabID, method, params)
		return
	case "tab_event":
		var active *bool
		if raw, ok := msg["active"]; ok && len(raw) > 0 {
			var v bool
			if json.Unmarshal(raw, &v) == nil {
				active = &v
			}
		}
		b.events.TabEvent(conn.Label(), rawString(msg["event"]), int(rawInt(msg["tabId"])),
			int(rawInt(msg["groupId"])), int(rawInt(msg["openerTabId"])), active)
		b.handleTabEvent(conn, msg)
		return
	case "tab_group_event":
		groupID := int(rawInt(msg["groupId"]))
		b.events.TabGroupEvent(conn.Label(), rawString(msg["event"]), groupID)
		b.handleTabGroupEvent(rawString(msg["event"]), groupID)
		return
	}
	if _, ok := msg["id"]; ok {
		b.deliverResponse(conn, msg)
	}
}

func (b *Bridge) handleHello(conn *Connection, msg map[string]json.RawMessage) {
	if conn.helloDone.Load() {
		// Duplicate hello on the same socket: treat as liveness only so a
		// late duplicate cannot orphan pending requests by re-keying twice.
		conn.markPong()
		return
	}
	protocol := int(rawInt(msg["protocolVersion"]))
	extID := rawString(msg["extensionId"])
	version := rawString(msg["version"])
	label := strings.TrimSpace(rawString(msg["profileLabel"]))
	if label == "" {
		if protocol < 5 || extID == "" {
			label = "default"
		} else {
			prefix := extID
			if len(prefix) > 8 {
				prefix = prefix[:8]
			}
			label = "profile-" + prefix
		}
	}
	if conn.helloDone.Swap(true) {
		return
	}
	conn.setIdentity(label, extID, version, protocol)

	b.mu.Lock()
	old := b.conns[label]
	b.conns[label] = conn
	b.mu.Unlock()

	if old != nil && old != conn {
		b.cfg.Logf("profile %q reconnected; displacing old extension socket", label)
		old.failPending(newError(CodeConnectionLost,
			"The browser connection was replaced by a new connection for the same profile — retry the action.", true))
		old.Close(4002, "displaced_by_reconnect")
	}
	_ = conn.ws.SetReadDeadline(time.Time{})
	conn.startHeartbeat(b)
	if protocol > 0 && protocol < minProtocolVersion {
		b.cfg.Logf("extension protocol %d is below minimum %d", protocol, minProtocolVersion)
	}
	b.cfg.Logf("extension hello: label=%q version=%q protocol=%d id=%q",
		label, version, protocol, shortID(extID))
	b.events.ConnectionEvent("hello", label, "")
	b.telemetry.ConnectionEvent("hello", label, map[string]any{"version": version, "protocol": protocol})
}

func (b *Bridge) dropConnection(conn *Connection) {
	label := conn.Label()
	owner := false
	b.mu.Lock()
	if label != "" && b.conns[label] == conn {
		delete(b.conns, label)
		owner = true
		for tab, ownerLabel := range b.tabs {
			if ownerLabel == label {
				delete(b.tabs, tab)
			}
		}
		delete(b.counts, label)
	}
	b.mu.Unlock()
	if owner {
		conn.failPending(newError(CodeConnectionLost,
			"The browser connection dropped before this action finished — retry the action.", true))
		// Soft-prune only this connection's registry slice. Other Chrome
		// profiles may still be live; keeping the agent identity lets a
		// reconnect/adopt mint a fresh group under the same name.
		for _, meta := range b.contexts.List() {
			if meta.BrowserProfile != label || meta.GroupID == 0 {
				continue
			}
			b.pruneGroup(meta.GroupID, true)
		}
	}
	conn.Close(1000, "")
	b.cfg.Logf("extension disconnected: label=%q", label)
	b.events.ConnectionEvent("disconnect", label, "socket_closed")
	b.telemetry.ConnectionEvent("disconnect", label, nil)
}

func (b *Bridge) deliverResponse(conn *Connection, msg map[string]json.RawMessage) {
	id := rawID(msg["id"])
	ch, ok := conn.takePending(id)
	if !ok {
		return
	}
	if raw, hasErr := msg["error"]; hasErr && len(raw) > 0 && string(raw) != "null" {
		ch <- pendingResult{err: newError(CodeExtensionError, errorFromWire(raw), false)}
		return
	}
	result := json.RawMessage("{}")
	if raw, ok := msg["result"]; ok && string(raw) != "null" {
		result = append(json.RawMessage(nil), raw...)
	}
	ch <- pendingResult{result: result}
}

func (b *Bridge) handleTabEvent(conn *Connection, msg map[string]json.RawMessage) {
	tabID := int(rawInt(msg["tabId"]))
	if tabID == 0 {
		return
	}
	event := rawString(msg["event"])
	label := conn.Label()
	groupID := int(rawInt(msg["groupId"]))
	openerID := int(rawInt(msg["openerTabId"]))
	var active *bool
	if raw, ok := msg["active"]; ok && len(raw) > 0 {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			active = &v
		}
	}

	if event == "removed" {
		b.forgetTab(tabID)
		b.stateMu.Lock()
		delete(b.tabOwner, tabID)
		delete(b.tabActiveMS, tabID)
		delete(b.tabActions, tabID)
		delete(b.hiveTabs, tabID)
		delete(b.ungroupedSeen, tabID)
		// Upstream evicts the per-tab CDP lock on "removed" too: most tabs
		// close via this event, not the close_tab tool.
		delete(b.tabLocks, tabID)
		b.stateMu.Unlock()
		return
	}
	if label == "" {
		return
	}
	b.rememberTab(tabID, label)

	// Mirror upstream _update_tab_profile_from_event plus the lifecycle
	// hook's opener fallback: a background popup that hasn't been grouped
	// yet still belongs to its opener's agent.
	owner := ""
	if groupID >= 0 {
		if meta, ok := b.contexts.ByGroup(groupID); ok {
			owner = meta.Profile
		}
	}
	if owner == "" && openerID > 0 {
		owner = b.tabOwnerOf(openerID)
	}
	if owner != "" {
		b.setTabOwner(tabID, owner)
		b.addHiveTab(tabID)
	} else if groupID >= 0 {
		b.setTabOwner(tabID, "")
	}
	if owner != "" && (event == "activated" || (active != nil && *active)) {
		b.setContextActiveTab(owner, tabID)
	}
}

func (b *Bridge) handleTabGroupEvent(event string, groupID int) {
	if event == "removed" {
		b.pruneGroup(groupID, true)
	}
}

func (b *Bridge) rememberTab(tabID int, label string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if prev, ok := b.tabs[tabID]; ok {
		if prev == label {
			return
		}
		if b.counts[prev] > 0 {
			b.counts[prev]--
		}
	}
	b.tabs[tabID] = label
	b.counts[label]++
}

func (b *Bridge) forgetTab(tabID int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	label, ok := b.tabs[tabID]
	if !ok {
		return
	}
	delete(b.tabs, tabID)
	if b.counts[label] > 0 {
		b.counts[label]--
	}
}

// replaceTabs records the authoritative tab list for one profile.
func (b *Bridge) replaceTabs(label string, ids []int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := make(map[int]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	for tab, owner := range b.tabs {
		if owner == label && !keep[tab] {
			delete(b.tabs, tab)
		}
	}
	for _, id := range ids {
		b.tabs[id] = label
	}
	b.counts[label] = len(ids)
}

func (b *Bridge) countTabs() (map[string]int, int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]int, len(b.conns))
	total := 0
	for label := range b.conns {
		n := b.counts[label]
		out[label] = n
		total += n
	}
	return out, total
}

// resolve picks the extension connection for a command. Routing order mirrors
// upstream _send: explicit browser profile, then tab ownership, then the
// default rule (starred default, else earliest-connected).
func (b *Bridge) resolve(profile string, tabID *int) (*Connection, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	label := strings.TrimSpace(profile)
	if label != "" && label != "default" {
		if conn := b.conns[label]; conn != nil {
			return conn, nil
		}
		// A pinned label is volatile (extension reinstall). With exactly one
		// live connection there is no other browser to run in, so use it.
		if len(b.conns) == 1 {
			for _, conn := range b.conns {
				return conn, nil
			}
		}
		return nil, newError(CodeNoBrowserProfile,
			fmt.Sprintf("No Chrome profile labelled '%s' is connected (connected: %s). Open that Chrome profile, enable the Hive Browser Bridge extension, and set its side-panel label, then retry.", label, b.labelsLocked()), false)
	}
	if tabID != nil {
		if owner, ok := b.tabs[*tabID]; ok {
			if conn := b.conns[owner]; conn != nil {
				return conn, nil
			}
		}
	}
	if b.starredDefault != "" {
		if conn := b.conns[b.starredDefault]; conn != nil {
			return conn, nil
		}
	}
	if len(b.conns) == 0 {
		return nil, newError(CodeNotConnected,
			"no browser extension is connected — install/enable the Hive Browser Bridge extension and open Chrome", true)
	}
	// First-come claims default: the earliest-connected profile.
	var earliest *Connection
	for _, conn := range b.conns {
		if earliest == nil || conn.connectedAt.Before(earliest.connectedAt) {
			earliest = conn
		}
	}
	return earliest, nil
}

// resolveForGroup routes group-scoped commands to the connection that minted
// the group, matching upstream _conn_label_for_group / _send.
func (b *Bridge) resolveForGroup(groupID int, profile string) (*Connection, error) {
	label := strings.TrimSpace(profile)
	if label != "" && label != "default" {
		return b.resolve(label, nil)
	}
	if meta, ok := b.contexts.ByGroup(groupID); ok && meta.BrowserProfile != "" {
		// The group belongs to a specific extension. Do NOT fall back to an
		// arbitrary default connection: querying another profile's group id
		// would silently inspect the wrong browser.
		return b.resolve(meta.BrowserProfile, nil)
	}
	return b.resolve("", nil)
}

func (b *Bridge) labelsLocked() string {
	labels := make([]string, 0, len(b.conns))
	for label := range b.conns {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

func (b *Bridge) defaultLabel() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.starredDefault != "" {
		if _, ok := b.conns[b.starredDefault]; ok {
			return b.starredDefault
		}
	}
	if len(b.conns) == 0 {
		return ""
	}
	var earliest *Connection
	for _, conn := range b.conns {
		if earliest == nil || conn.connectedAt.Before(earliest.connectedAt) {
			earliest = conn
		}
	}
	if earliest == nil {
		return ""
	}
	return earliest.Label()
}

// IsConnected reports whether at least one extension socket is registered.
func (b *Bridge) IsConnected() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.conns) > 0
}

// ConnectionHelp is reserved for the upstream actionable disconnect text.
// L0/L1 callers read the structured not_connected error instead.

func (b *Bridge) logf(format string, args ...any) { b.cfg.Logf(format, args...) }

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func rawInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int64(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	return 0
}

func rawID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}
