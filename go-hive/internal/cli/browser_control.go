package cli

// browser_control.go is the serve-side half of the bridge control plane
// (port 14831).
//
// Parity: core/framework/loader/cli.py::_bridge_keepalive_loop and
// tools/src/gcu/browser/bridge_rpc.py::BridgeClient.
//
// The Python runtime holds one control-RPC connection open for its lifetime so
// the bridge's idle watchdog can tell "nobody is using the browser" apart from
// "the runtime is gone". The Go port does the same from `hive serve`:
//
//   - connect to ws://127.0.0.1:14831/control
//   - client_hello(owner_pid) so the bridge's reaper can drop zombie clients
//   - subscribe_notifications and route side-panel adopt/release frames
//   - reconnect with backoff if the bridge restarts or idle-exits
//   - close on serve shutdown, which is what lets the bridge exit
//
// The main module deliberately does not import the browserbridge module (it
// carries its own go.mod), so this file carries a minimal RFC 6455 client:
// handshake, masked text frames, ping/pong and the closing handshake — exactly
// the subset the control protocol uses.

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// browserBridgeControlEnv overrides the control-RPC address handed to the
	// child and dialed by the keepalive (default 127.0.0.1:14831).
	browserBridgeControlEnv     = "HIVE_BROWSERBRIDGE_CONTROL"
	browserBridgeDefaultControl = "127.0.0.1:14831"

	browserControlHandshakeTimeout = 3 * time.Second
	browserControlCallTimeout      = 10 * time.Second
	browserControlHoldPoll         = 250 * time.Millisecond
	browserControlMaxBackoff       = 30 * time.Second
)

// browserBridgeControlAddr resolves the control-RPC address.
func browserBridgeControlAddr(lookupEnv func(string) string) string {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if raw := strings.TrimSpace(lookupEnv(browserBridgeControlEnv)); raw != "" {
		return raw
	}
	return browserBridgeDefaultControl
}

// browserControlConn is one client-side WebSocket connection.
type browserControlConn struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex
	closed  atomic.Bool
}

// dialBrowserControl performs the opening handshake against a ws:// URL.
func dialBrowserControl(rawURL string, timeout time.Duration) (*browserControlConn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported control URL scheme %q", parsed.Scheme)
	}
	host := parsed.Host
	path := parsed.Path
	if path == "" {
		path = "/"
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, host, key)
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	response, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("control handshake status %d", response.StatusCode)
	}
	if got := response.Header.Get("Sec-WebSocket-Accept"); got != controlAcceptKey(key) {
		_ = conn.Close()
		return nil, fmt.Errorf("control handshake accept mismatch")
	}
	_ = conn.SetDeadline(time.Time{})
	return &browserControlConn{conn: conn, br: br}, nil
}

func controlAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// writeJSON sends one masked text frame (clients MUST mask).
func (c *browserControlConn) writeJSON(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.writeFrame(0x1, payload)
}

func (c *browserControlConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed.Load() {
		return net.ErrClosed
	}
	header := make([]byte, 0, 14)
	header = append(header, 0x80|opcode)
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length)|0x80)
	case length <= 0xFFFF:
		header = append(header, 126|0x80, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 127|0x80)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header = append(header, mask[:]...)
	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ mask[i&3]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

// readMessage returns the next data message, answering pings and turning a
// peer close into io.EOF.
func (c *browserControlConn) readMessage() ([]byte, error) {
	var (
		buffer     []byte
		fragmented bool
	)
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x9: // ping
			if err := c.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
			continue
		case 0xA: // pong
			continue
		case 0x8: // close
			_ = c.writeFrame(0x8, payload)
			return nil, io.EOF
		case 0x1, 0x2:
			if fin {
				return payload, nil
			}
			fragmented = true
			buffer = payload
		case 0x0:
			if !fragmented {
				return nil, fmt.Errorf("control websocket: stray continuation frame")
			}
			buffer = append(buffer, payload...)
			if fin {
				return buffer, nil
			}
		default:
			return nil, fmt.Errorf("control websocket: unsupported opcode %d", op)
		}
	}
}

func (c *browserControlConn) readFrame() (bool, byte, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin := head[0]&0x80 != 0
	op := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if length < 0 || length > 64<<20 {
		return false, 0, nil, fmt.Errorf("control websocket: frame too large (%d)", length)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return fin, op, payload, nil
}

// Close performs the closing handshake (best effort) and releases the socket.
func (c *browserControlConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, 1000)
	_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = c.writeFrame(0x8, payload)
	return c.conn.Close()
}

// browserControlClient is the RPC client half of the control plane: pipelined
// calls keyed by rpc_id plus a read loop that routes notify frames.
type browserControlClient struct {
	conn *browserControlConn
	url  string

	mu      sync.Mutex
	pending map[string]chan map[string]any
	counter atomic.Uint64
	closed  atomic.Bool
	done    chan struct{}

	onNotify func(profile, text string, extra map[string]any)
}

func newBrowserControlClient(url string) *browserControlClient {
	return &browserControlClient{
		url:     url,
		pending: make(map[string]chan map[string]any),
		done:    make(chan struct{}),
	}
}

func (c *browserControlClient) connect() error {
	conn, err := dialBrowserControl(c.url, browserControlHandshakeTimeout)
	if err != nil {
		return err
	}
	c.conn = conn
	go c.readLoop()
	return nil
}

func (c *browserControlClient) readLoop() {
	defer close(c.done)
	for {
		payload, err := c.conn.readMessage()
		if err != nil {
			break
		}
		var frame map[string]any
		if json.Unmarshal(payload, &frame) != nil {
			continue
		}
		if kind, _ := frame["type"].(string); kind == "notify" {
			profile, _ := frame["profile"].(string)
			text, _ := frame["text"].(string)
			extra := make(map[string]any, len(frame))
			for key, value := range frame {
				if key == "type" || key == "profile" || key == "text" {
					continue
				}
				extra[key] = value
			}
			if handler := c.onNotify; handler != nil {
				handler(profile, text, extra)
			}
			continue
		}
		id, _ := frame["rpc_id"].(string)
		if id == "" {
			continue
		}
		c.mu.Lock()
		waiter := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if waiter != nil {
			waiter <- frame
		}
	}
	// The socket dropped: fail every in-flight call so no caller hangs.
	c.closed.Store(true)
	c.mu.Lock()
	waiters := make([]chan map[string]any, 0, len(c.pending))
	for id, waiter := range c.pending {
		waiters = append(waiters, waiter)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
}

// call sends one RPC and waits for its response envelope.
func (c *browserControlClient) call(method string, args ...any) (map[string]any, error) {
	if c.closed.Load() {
		return nil, fmt.Errorf("control connection is closed")
	}
	id := fmt.Sprintf("serve-%d", c.counter.Add(1))
	waiter := make(chan map[string]any, 1)
	c.mu.Lock()
	c.pending[id] = waiter
	c.mu.Unlock()

	if err := c.conn.writeJSON(map[string]any{
		"rpc_id": id, "method": method, "args": args, "params": map[string]any{},
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	timer := time.NewTimer(browserControlCallTimeout)
	defer timer.Stop()
	select {
	case frame, ok := <-waiter:
		if !ok {
			return nil, fmt.Errorf("control connection dropped during %s", method)
		}
		if okFlag, _ := frame["ok"].(bool); !okFlag {
			errObj, _ := frame["error"].(map[string]any)
			message, _ := errObj["message"].(string)
			if message == "" {
				message = "control RPC failed"
			}
			return nil, fmt.Errorf("%s", message)
		}
		result, _ := frame["result"].(map[string]any)
		return result, nil
	case <-timer.C:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("control RPC %s timed out", method)
	}
}

func (c *browserControlClient) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
}

// browserControlKeepalive holds the serve-lifetime control connection.
type browserControlKeepalive struct {
	controlAddr string
	ownerPID    int
	out         io.Writer
	onNotify    func(profile, text string, extra map[string]any)
	// wake asks the supervisor to (re)start a bridge that idle-exited. nil is
	// fine: the loop just retries connecting.
	wake func()

	stopCh chan struct{}
	done   chan struct{}
}

func startBrowserControlKeepalive(cfg browserControlKeepalive) func() {
	if cfg.out == nil {
		cfg.out = io.Discard
	}
	if cfg.stopCh == nil {
		cfg.stopCh = make(chan struct{})
	}
	if cfg.done == nil {
		cfg.done = make(chan struct{})
	}
	go cfg.run()
	var once sync.Once
	return func() {
		once.Do(func() { close(cfg.stopCh) })
		select {
		case <-cfg.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func (k *browserControlKeepalive) run() {
	defer close(k.done)
	backoff := time.Second
	for {
		select {
		case <-k.stopCh:
			return
		default:
		}
		client := newBrowserControlClient("ws://" + k.controlAddr + "/control")
		client.onNotify = k.onNotify
		if err := client.connect(); err != nil {
			if k.wake != nil {
				k.wake()
			}
			if !k.sleep(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		helloArgs := []any{}
		if k.ownerPID > 0 {
			helloArgs = append(helloArgs, k.ownerPID)
		}
		if _, err := client.call("client_hello", helloArgs...); err != nil {
			// An older bridge without client_hello keeps the raw socket open,
			// which still counts as a client during the identify grace
			// (upstream does the same).
			fmt.Fprintf(k.out, "Browser bridge: control hello failed: %v\n", err)
		}
		if _, err := client.call("subscribe_notifications"); err != nil {
			// Best effort: the transport works without push frames.
			fmt.Fprintf(k.out, "Browser bridge: notify subscription unavailable: %v\n", err)
		}
		fmt.Fprintf(k.out, "Browser bridge: control connected on %s\n", k.controlAddr)
		backoff = time.Second
		k.hold(client)
		client.close()
		if !k.sleep(backoff) {
			return
		}
	}
}

// hold keeps the connection until serve stops or the socket drops.
func (k *browserControlKeepalive) hold(client *browserControlClient) {
	ticker := time.NewTicker(browserControlHoldPoll)
	defer ticker.Stop()
	for {
		select {
		case <-k.stopCh:
			return
		case <-client.done:
			return
		case <-ticker.C:
		}
	}
}

func (k *browserControlKeepalive) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-k.stopCh:
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > browserControlMaxBackoff {
		return browserControlMaxBackoff
	}
	return next
}
