package browserbridge

// control.go ports tools/src/gcu/browser/bridge_rpc.py::BridgeRpcServer — the
// out-of-process control plane on 14831.
//
// Wire protocol (JSON text frames on ws://127.0.0.1:14831/control):
//
//	request   {"rpc_id": "<id>", "method": "navigate", "args": [...], "params": {...}}
//	response  {"rpc_id": "<id>", "ok": true,  "result": <json>}
//	          {"rpc_id": "<id>", "ok": false, "error": {"message","code","retryable"}}
//	notify    {"type": "notify", "profile": ..., "text": ..., ...extra}
//
// The control port exists so a disposable tool process never owns the bridge:
// it can restart and reconnect while every tab, the extension socket and the
// in-memory registry survive. The allowlist (rpcMethods) is the security
// boundary — methods are never resolved reflectively — and client_hello lets
// the server reap zombies whose owner process died, so a crashed runtime
// cannot keep the bridge alive forever.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/browserbridge/ws"
)

const (
	// DefaultControlAddr is the control-RPC port. 14829 = extension relay WS,
	// 14830 = HTTP status, 14831 = this method-call RPC (upstream CONTROL_PORT
	// = BRIDGE_PORT + 2).
	DefaultControlAddr = "127.0.0.1:14831"
	controlPath        = "/control"

	// Identify grace: a fresh control socket that never sends client_hello
	// counts as a client only this long. Real runtimes hello within
	// milliseconds; anything later is an orphan from a previous session.
	controlIdentifyGrace = 30 * time.Second
	// Control-clients reaper cadence (owner PID liveness check).
	controlReapInterval = 10 * time.Second
)

// rpcMethods is the exact upstream RPC_METHODS allowlist. An explicit set —
// never a reflective lookup — so the control port cannot be turned into a
// remote-code lever.
var rpcMethods = map[string]struct{}{
	// contexts / lifecycle
	"create_context":   {},
	"destroy_context":  {},
	"register_context": {},
	"list_contexts":    {},
	// tabs
	"create_tab":   {},
	"close_tab":    {},
	"list_tabs":    {},
	"activate_tab": {},
	// navigation
	"navigate":   {},
	"go_back":    {},
	"go_forward": {},
	"reload":     {},
	// interactions
	"click":            {},
	"click_coordinate": {},
	"type_text":        {},
	"press_key":        {},
	"press_key_at":     {},
	"hover":            {},
	"hover_coordinate": {},
	"scroll":           {},
	"select_option":    {},
	// inspection
	"screenshot":        {},
	"screenshot_region": {},
	"snapshot":          {},
	"evaluate":          {},
	"get_text":          {},
	"get_attribute":     {},
	"shadow_query":      {},
	// advanced
	"wait_for_selector":        {},
	"wait_for_text":            {},
	"resize":                   {},
	"handle_javascript_dialog": {},
	"get_pending_dialog":       {},
	// tab health / action history
	"get_tab_blockers": {},
	"tab_health":       {},
	"audit_tab":        {},
	"record_action":    {},
	// cdp
	"cdp_attach": {},
	"cdp_detach": {},
	"_cdp":       {},
	// introspection
	"status_payload":     {},
	"connected_profiles": {},
	"connection_help":    {},
	"is_connected":       {},
	// identity / notify opt-in (per-connection state, not bridge methods)
	"client_hello":            {},
	"subscribe_notifications": {},
}

// RPCMethods returns the control-RPC allowlist, sorted.
func RPCMethods() []string {
	out := make([]string, 0, len(rpcMethods))
	for name := range rpcMethods {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// PIDAlive reports whether pid is still running. The process-level watchdogs
// (idle exit, desktop parent) need the same probe the client reaper uses.
func PIDAlive(pid int) bool { return pidAlive(pid) }

// controlClientState is per-control-socket state (upstream _clients).
type controlClientState struct {
	conn        *ws.Conn
	ownerPID    int // 0 until client_hello identifies the owner
	connectedAt time.Time
	wantsNotify bool
}

// controlRPC serves the allowlisted bridge surface on its own port.
type controlRPC struct {
	bridge *Bridge
	logf   func(format string, args ...any)

	mu      sync.Mutex
	clients map[*ws.Conn]*controlClientState
	srv     *ws.Server
	addr    string
	started bool

	identifyGrace time.Duration
	reapInterval  time.Duration

	stop chan struct{}
	done chan struct{}
}

func newControlRPC(b *Bridge) *controlRPC {
	grace := b.cfg.ControlIdentifyGrace
	if grace <= 0 {
		grace = controlIdentifyGrace
	}
	reap := b.cfg.ControlReapInterval
	if reap <= 0 {
		reap = controlReapInterval
	}
	return &controlRPC{
		bridge:        b,
		logf:          b.cfg.Logf,
		clients:       make(map[*ws.Conn]*controlClientState),
		identifyGrace: grace,
		reapInterval:  reap,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

// start binds the control port. A bind failure is logged, not fatal: the
// extension relay and the HTTP API keep working, and a supervised restart gets
// another chance at the singleton port.
func (c *controlRPC) start(addr string) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		c.logf("control rpc %s unavailable (continuing): %v", addr, err)
		return
	}
	srv := &ws.Server{
		Path:    controlPath,
		Handler: c.handleConn,
		MaxRead: c.bridge.cfg.MaxMessageSize,
		Logger:  c.logf,
	}
	c.mu.Lock()
	c.srv = srv
	c.addr = ln.Addr().String()
	c.started = true
	c.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil {
			c.logf("control rpc server stopped: %v", err)
		}
	}()
	go c.reapLoop()
	c.logf("control rpc listening: ws://%s%s", c.addr, controlPath)
}

// stop closes the listener and every client socket, and stops the reaper.
func (c *controlRPC) stopServer(ctx context.Context) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return
	}
	c.started = false
	srv := c.srv
	clients := make([]*controlClientState, 0, len(c.clients))
	for _, state := range c.clients {
		clients = append(clients, state)
	}
	c.mu.Unlock()

	close(c.stop)
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
	for _, state := range clients {
		_ = state.conn.WriteClose(1001, "server_shutdown")
		_ = state.conn.Close()
	}
	select {
	case <-c.done:
	case <-ctx.Done():
	}
}

func (c *controlRPC) address() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addr
}

func (c *controlRPC) listening() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// handleConn reads requests off one control socket and fans each one out into
// its own goroutine, so a slow navigate never head-of-line blocks a screenshot
// multiplexed on the same connection.
func (c *controlRPC) handleConn(conn *ws.Conn) {
	state := &controlClientState{conn: conn, connectedAt: time.Now()}
	c.mu.Lock()
	c.clients[conn] = state
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.clients, conn)
		c.mu.Unlock()
	}()

	for {
		op, data, err := conn.ReadMessage()
		if err != nil {
			if ce, ok := err.(*ws.CloseError); ok {
				c.logf("control client closed: code=%d reason=%q", ce.Code, ce.Reason)
			}
			return
		}
		if op != ws.Text {
			continue
		}
		payload := append([]byte(nil), data...)
		go c.dispatch(conn, payload)
	}
}

func (c *controlRPC) dispatch(conn *ws.Conn, raw []byte) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		c.logf("ignoring non-JSON control frame")
		return
	}
	rpcID := msg["rpc_id"]
	method := rawString(msg["method"])

	var args []any
	if rawArgs, ok := msg["args"]; ok && len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &args)
	}
	params := map[string]any{}
	if rawParams, ok := msg["params"]; ok && len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &params)
	}

	if _, allowed := rpcMethods[method]; !allowed {
		c.sendError(conn, rpcID, newError("unknown_method",
			fmt.Sprintf("Unknown bridge RPC method: %q", method), false))
		return
	}

	switch method {
	case "client_hello":
		var pid any
		if len(args) > 0 {
			pid = args[0]
		} else if v, ok := params["pid"]; ok {
			pid = v
		}
		c.sendResult(conn, rpcID, c.registerOwner(conn, pid))
		return
	case "subscribe_notifications":
		c.mu.Lock()
		if state := c.clients[conn]; state != nil {
			state.wantsNotify = true
		}
		c.mu.Unlock()
		c.sendResult(conn, rpcID, map[string]any{"ok": true})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.bridge.cfg.CommandTimeout*4)
	defer cancel()
	result, err := dispatchRPC(ctx, c.bridge, method, args, params)
	if err != nil {
		c.sendError(conn, rpcID, AsError(err))
		return
	}
	c.sendResult(conn, rpcID, result)
}

// registerOwner records the owner (desktop/runtime) PID a control client
// announced. It does not validate liveness — the reaper does that on its own
// schedule, and a client claiming a dead PID is just a faster reap target.
func (c *controlRPC) registerOwner(conn *ws.Conn, pid any) map[string]any {
	value, ok := toInt(pid)
	if !ok || value <= 0 {
		return map[string]any{"ok": false, "error": "invalid_pid", "pid": pid}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.clients[conn]
	if state == nil {
		return map[string]any{"ok": false, "error": "client_gone"}
	}
	previous := state.ownerPID
	state.ownerPID = value
	if previous != value {
		c.logf("control client_hello: owner_pid %d → %d", previous, value)
	}
	return map[string]any{"ok": true, "pid": value, "previous": nilIfZero(previous)}
}

func (c *controlRPC) sendResult(conn *ws.Conn, rpcID json.RawMessage, result any) {
	c.send(conn, map[string]any{"rpc_id": rawJSON(rpcID), "ok": true, "result": result})
}

func (c *controlRPC) sendError(conn *ws.Conn, rpcID json.RawMessage, err *Error) {
	c.send(conn, map[string]any{
		"rpc_id": rawJSON(rpcID),
		"ok":     false,
		"error": map[string]any{
			"message":   err.Message,
			"code":      err.Code,
			"retryable": err.Retryable,
		},
	})
}

func (c *controlRPC) send(conn *ws.Conn, frame map[string]any) {
	payload, err := json.Marshal(frame)
	if err != nil {
		return
	}
	// A client that vanished mid-call simply loses the result; its handler's
	// read loop notices the closed socket.
	_ = conn.WriteText(payload)
}

// rawJSON preserves the caller's rpc_id verbatim (string, number or null).
func rawJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// notify pushes a server-initiated frame to every subscribing client.
// Returns the number of clients the frame reached; dead sockets are skipped
// and cleaned up by their read loops.
func (c *controlRPC) notify(profile, text string, extra map[string]any) int {
	frame := map[string]any{"type": "notify", "profile": profile, "text": text}
	for k, v := range extra {
		if k == "type" || k == "profile" || k == "text" {
			continue
		}
		frame[k] = v
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return 0
	}
	c.mu.Lock()
	targets := make([]*ws.Conn, 0, len(c.clients))
	for conn, state := range c.clients {
		if state.wantsNotify {
			targets = append(targets, conn)
		}
	}
	c.mu.Unlock()
	delivered := 0
	for _, conn := range targets {
		if err := conn.WriteText(payload); err == nil {
			delivered++
		}
	}
	return delivered
}

// activeClientCount counts clients that are currently alive by the reaper's
// rule: identified clients live while their owner PID does; unidentified
// clients only during the identify grace. This is the idle watchdog's signal.
func (c *controlRPC) activeClientCount() int {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, state := range c.clients {
		if c.clientAlive(state, now) {
			count++
		}
	}
	return count
}

// runtimeAlive reports whether any identified client's owner is alive. nil
// means no client has identified yet — no evidence either way.
func (c *controlRPC) runtimeAlive() *bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	identified := make([]int, 0, len(c.clients))
	for _, state := range c.clients {
		if state.ownerPID > 0 {
			identified = append(identified, state.ownerPID)
		}
	}
	if len(identified) == 0 {
		return nil
	}
	alive := false
	for _, pid := range identified {
		if pidAlive(pid) {
			alive = true
			break
		}
	}
	return &alive
}

func (c *controlRPC) clientAlive(state *controlClientState, now time.Time) bool {
	if state.ownerPID > 0 {
		return pidAlive(state.ownerPID)
	}
	return now.Sub(state.connectedAt) < c.identifyGrace
}

// reapLoop force-closes connections whose owner is dead or that never
// identified within the grace window — both signals that the socket is a
// zombie, not a real consumer.
func (c *controlRPC) reapLoop() {
	defer close(c.done)
	ticker := time.NewTicker(c.reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
		}
		now := time.Now()
		type zombie struct {
			conn   *ws.Conn
			reason string
			pid    int
		}
		c.mu.Lock()
		zombies := make([]zombie, 0, len(c.clients))
		for conn, state := range c.clients {
			if c.clientAlive(state, now) {
				continue
			}
			reason := "no_identify"
			if state.ownerPID > 0 {
				reason = "owner_dead"
			}
			zombies = append(zombies, zombie{conn: conn, reason: reason, pid: state.ownerPID})
		}
		c.mu.Unlock()
		for _, z := range zombies {
			c.logf("control reaper: closing zombie client (%s, owner_pid=%d)", z.reason, z.pid)
			_ = z.conn.WriteClose(1011, z.reason)
			_ = z.conn.Close()
		}
	}
}
