package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aden-hive/hive-go/browserbridge/ws"
)

type pendingResult struct {
	result json.RawMessage
	err    *Error
}

// Connection is one Chrome extension socket (one Chrome profile).
type Connection struct {
	ws         *ws.Conn
	remoteAddr string

	mu       sync.RWMutex
	label    string
	extID    string
	version  string
	protocol int

	connectedAt time.Time
	lastPong    atomic.Int64 // UnixNano
	helloDone   atomic.Bool

	pendingMu sync.Mutex
	pending   map[string]chan pendingResult
	counter   atomic.Uint64
	inFlight  atomic.Int64

	attachedMu sync.Mutex
	attached   map[int]bool

	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	hbOnce    sync.Once
}

func newConnection(c *ws.Conn) *Connection {
	now := time.Now()
	conn := &Connection{
		ws:          c,
		remoteAddr:  c.NetConn().RemoteAddr().String(),
		connectedAt: now,
		pending:     make(map[string]chan pendingResult),
		attached:    make(map[int]bool),
		done:        make(chan struct{}),
	}
	conn.lastPong.Store(now.UnixNano())
	return conn
}

func (c *Connection) setIdentity(label, extID, version string, protocol int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.label, c.extID, c.version, c.protocol = label, extID, version, protocol
}

func (c *Connection) Label() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.label
}

func (c *Connection) identity() (label, extID, version string, protocol int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.label, c.extID, c.version, c.protocol
}

func (c *Connection) markPong() { c.lastPong.Store(time.Now().UnixNano()) }

func (c *Connection) isClosed() bool { return c.closed.Load() }

func (c *Connection) startHeartbeat(b *Bridge) {
	c.hbOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(b.cfg.PingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-c.done:
					return
				case <-ticker.C:
					last := time.Unix(0, c.lastPong.Load())
					if time.Since(last) > b.cfg.PongTimeout {
						b.cfg.Logf("app-level ping timeout for profile %q — closing extension socket", c.Label())
						c.Close(4001, "ping_timeout")
						return
					}
					if err := c.writeJSON(map[string]any{"type": "ping"}); err != nil {
						return
					}
				}
			}
		}()
	})
}

func (c *Connection) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.ws.WriteText(payload)
}

// Call sends one command frame `{id,type,...params}` and waits for the
// extension's `{id,result}` or `{id,error}` response.
func (c *Connection) Call(ctx context.Context, typ string, params map[string]any, timeout time.Duration) (json.RawMessage, error) {
	if c.isClosed() {
		return nil, newError(CodeConnectionLost, "The browser connection is closed — retry the action.", true)
	}
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	id := strconv.FormatUint(c.counter.Add(1), 10)
	msg := make(map[string]any, len(params)+2)
	for k, v := range params {
		msg[k] = v
	}
	// These two are authoritative; never let params override them.
	msg["id"] = id
	msg["type"] = typ

	ch := make(chan pendingResult, 1)
	c.pendingMu.Lock()
	if c.isClosed() {
		c.pendingMu.Unlock()
		return nil, newError(CodeConnectionLost, "The browser connection is closed — retry the action.", true)
	}
	c.pending[id] = ch
	c.pendingMu.Unlock()

	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)

	if err := c.writeJSON(msg); err != nil {
		c.removePending(id)
		return nil, newError(CodeConnectionLost,
			"Could not send the command to the browser extension — the connection dropped; retry the action.", true)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return res.result, nil
	case <-timer.C:
		c.removePending(id)
		return nil, newError(CodeTimeout,
			"The browser extension did not answer command '"+typ+"' in time", false)
	case <-ctx.Done():
		c.removePending(id)
		return nil, newError(CodeTimeout, "The command was cancelled before the extension answered", false)
	case <-c.done:
		c.removePending(id)
		return nil, newError(CodeConnectionLost,
			"The browser connection dropped before this action finished — retry the action.", true)
	}
}

func (c *Connection) takePending(id string) (chan pendingResult, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	ch, ok := c.pending[id]
	if ok {
		delete(c.pending, id)
	}
	return ch, ok
}

func (c *Connection) removePending(id string) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *Connection) failPending(err *Error) {
	c.pendingMu.Lock()
	chans := make([]chan pendingResult, 0, len(c.pending))
	for id, ch := range c.pending {
		chans = append(chans, ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- pendingResult{err: err}:
		default:
		}
	}
}

// Close performs the closing handshake once and releases the socket.
func (c *Connection) Close(code int, reason string) {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.ws.WriteClose(code, reason)
		_ = c.ws.Close()
		close(c.done)
	})
}

func (c *Connection) ensureAttached(ctx context.Context, tabID int) (bool, error) {
	c.attachedMu.Lock()
	ok := c.attached[tabID]
	c.attachedMu.Unlock()
	if ok {
		return false, nil
	}
	if _, err := c.Call(ctx, "cdp.attach", map[string]any{"tabId": tabID}, 15*time.Second); err != nil {
		return false, err
	}
	c.attachedMu.Lock()
	c.attached[tabID] = true
	c.attachedMu.Unlock()
	return true, nil
}

func (c *Connection) forgetAttached(tabID int) {
	c.attachedMu.Lock()
	delete(c.attached, tabID)
	c.attachedMu.Unlock()
}

func (c *Connection) markAttached(tabID int) {
	c.attachedMu.Lock()
	c.attached[tabID] = true
	c.attachedMu.Unlock()
}

func isNotAttached(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not attached") || strings.Contains(msg, "detached")
}

var errNoResult = errors.New("extension returned no result object")
