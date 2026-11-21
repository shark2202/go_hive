// Package ws implements the server side of RFC 6455 WebSockets using only the
// Go standard library. It supports the opening handshake, text/binary messages,
// fragmentation, ping/pong control frames and the closing handshake — enough
// for the Hive Browser Bridge Chrome extension, which speaks JSON text frames.
package ws

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Opcode is a WebSocket frame opcode.
type Opcode byte

const (
	Text   Opcode = 0x1
	Binary Opcode = 0x2
	Close  Opcode = 0x8
	Ping   Opcode = 0x9
	Pong   Opcode = 0xA
)

const (
	acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	// DefaultMaxRead mirrors the Python bridge's websockets max_size
	// (bridge.py: `max_size=50 * 1024 * 1024`) — CDP responses such as a full
	// accessibility tree or a screenshot can be large. The Go port used to cap
	// this at 16 MiB (R10), which a big AX tree can exceed before the
	// extension's own frame ever reaches the bridge.
	DefaultMaxRead int64 = 50 << 20 // 50 MiB
)

// CloseError reports a closing handshake initiated by the peer.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("websocket closed (code %d)", e.Code)
	}
	return fmt.Sprintf("websocket closed (code %d): %s", e.Code, e.Reason)
}

// Conn is one upgraded WebSocket connection. Reads must happen on a single
// goroutine; writes are serialized internally and may be called concurrently.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex

	closeOnce sync.Once
	closeSent atomic.Bool
	closed    atomic.Bool
	maxRead   int64

	// done is closed when the connection is torn down (peer close, server
	// shutdown, or the keepalive timeout). It lets the keepalive goroutine
	// stop without polling.
	done     chan struct{}
	doneOnce sync.Once

	// pongs receives one signal per peer pong when the server runs the
	// keepalive ticker (nil otherwise). Set before the read loop starts.
	pongs chan struct{}
}

// closeDone releases the keepalive goroutine. Safe to call repeatedly and
// from any goroutine.
func (c *Conn) closeDone() { c.doneOnce.Do(func() { close(c.done) }) }

// NetConn exposes the underlying network connection (for deadlines and
// Close). Callers should not read or write raw bytes through it.
func (c *Conn) NetConn() net.Conn { return c.conn }

// SetReadDeadline sets the deadline for future ReadMessage calls.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// Close tears down the TCP connection without sending a close frame.
func (c *Conn) Close() error {
	c.closed.Store(true)
	c.closeDone()
	return c.conn.Close()
}

// WriteText sends a single unmasked text frame.
func (c *Conn) WriteText(payload []byte) error { return c.writeFrame(Text, payload) }

// Ping sends a WebSocket-layer ping.
func (c *Conn) Ping(payload []byte) error { return c.writeFrame(Ping, payload) }

// Pong sends a WebSocket-layer pong.
func (c *Conn) Pong(payload []byte) error { return c.writeFrame(Pong, payload) }

// WriteClose sends a close frame once. Subsequent calls are no-ops so a
// timeout-driven close cannot double-write a frame.
func (c *Conn) WriteClose(code int, reason string) error {
	if !c.closeSent.CompareAndSwap(false, true) {
		return nil
	}
	if len(reason) > 123 {
		reason = reason[:123]
	}
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	return c.writeFrame(Close, payload)
}

func (c *Conn) writeFrame(op Opcode, payload []byte) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	var header [10]byte
	header[0] = 0x80 | byte(op)
	n := 2
	switch {
	case len(payload) < 126:
		header[1] = byte(len(payload))
	case len(payload) <= 0xFFFF:
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(len(payload)))
		n = 4
	default:
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(len(payload)))
		n = 10
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.conn.Write(header[:n]); err != nil {
		return err
	}
	_, err := c.conn.Write(payload)
	return err
}

// ReadMessage returns the next complete data message. Control frames are
// handled transparently: pings are answered, pongs ignored, and a peer close
// is echoed before a *CloseError is returned.
func (c *Conn) ReadMessage() (Opcode, []byte, error) {
	var (
		msgOp   Opcode
		buf     []byte
		fragOn  bool
		maxRead = c.maxRead
	)
	if maxRead <= 0 {
		maxRead = DefaultMaxRead
	}

	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}

		switch op {
		case Ping:
			if err := c.Pong(payload); err != nil {
				return 0, nil, err
			}
			continue
		case Pong:
			// The keepalive ticker waits on this signal; a connection without
			// a ticker (pongs == nil) just drops the frame.
			if c.pongs != nil {
				select {
				case c.pongs <- struct{}{}:
				default:
				}
			}
			continue
		case Close:
			code, reason := parseClosePayload(payload)
			// Echo the close code, then report it to the caller. The caller
			// (bridge read loop) closes the TCP connection.
			_ = c.WriteClose(code, "")
			return 0, nil, &CloseError{Code: code, Reason: reason}
		case Text, Binary:
			if fragOn {
				return 0, nil, c.protocolError("new data frame while fragmented message in progress")
			}
			if fin {
				return op, payload, nil
			}
			msgOp, fragOn = op, true
			buf = payload
			if int64(len(buf)) > maxRead {
				return 0, nil, c.protocolError("message too large")
			}
		case 0x0: // continuation
			if !fragOn {
				return 0, nil, c.protocolError("continuation frame without initial frame")
			}
			buf = append(buf, payload...)
			if int64(len(buf)) > maxRead {
				return 0, nil, c.protocolError("message too large")
			}
			if fin {
				return msgOp, buf, nil
			}
		default:
			return 0, nil, c.protocolError(fmt.Sprintf("unsupported opcode 0x%x", byte(op)))
		}
	}
}

func (c *Conn) protocolError(reason string) error {
	_ = c.WriteClose(1002, reason)
	return fmt.Errorf("websocket protocol error: %s", reason)
}

func (c *Conn) readFrame() (bool, Opcode, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin := head[0]&0x80 != 0
	if head[0]&0x70 != 0 {
		return false, 0, nil, c.protocolError("reserved bits set")
	}
	op := Opcode(head[0] & 0x0F)
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)

	if op >= 0x8 {
		if !fin {
			return false, 0, nil, c.protocolError("fragmented control frame")
		}
		if length > 125 {
			return false, 0, nil, c.protocolError("control frame payload too large")
		}
	}
	if !masked {
		// Clients MUST mask; an unmasked client frame is a protocol error.
		return false, 0, nil, c.protocolError("client frame not masked")
	}

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
		u := binary.BigEndian.Uint64(ext[:])
		if u>>63 != 0 {
			return false, 0, nil, c.protocolError("invalid 64-bit payload length")
		}
		length = int64(u)
	}
	maxRead := c.maxRead
	if maxRead <= 0 {
		maxRead = DefaultMaxRead
	}
	if length > maxRead {
		return false, 0, nil, c.protocolError("frame too large")
	}

	var mask [4]byte
	if _, err := io.ReadFull(c.br, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i&3]
	}
	return fin, op, payload, nil
}

func parseClosePayload(payload []byte) (int, string) {
	if len(payload) < 2 {
		return 1005, ""
	}
	return int(binary.BigEndian.Uint16(payload[:2])), string(payload[2:])
}

// Server accepts WebSocket connections. Path, when set, restricts upgrades to
// that request path (the extension uses /bridge).
type Server struct {
	Addr      string
	Path      string
	Handler   func(*Conn)
	MaxRead   int64
	Logger    func(format string, args ...any)
	Handshake time.Duration

	// PingInterval, when > 0, runs the WS-layer keepalive on every
	// connection: ping at this cadence and give the peer PingTimeout to pong
	// before closing with 1011 "keepalive ping timeout". It mirrors the
	// Python bridge's websockets.serve(ping_interval=…, ping_timeout=…):
	// the WS-layer heartbeat catches a half-open TCP path (NAT reaper,
	// laptop sleep) that the app-level JSON ping cannot see. Zero disables
	// the ticker (the control-RPC listener and unit tests).
	PingInterval time.Duration
	PingTimeout  time.Duration

	mu       sync.Mutex
	ln       net.Listener
	conns    map[*Conn]struct{}
	closed   bool
	serveErr chan error
}

// ListenAndServe binds Addr and serves until Shutdown.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts connections from ln.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	if s.conns == nil {
		s.conns = make(map[*Conn]struct{})
	}
	if s.Handshake == 0 {
		s.Handshake = 10 * time.Second
	}
	s.mu.Unlock()

	for {
		nc, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if te, ok := err.(net.Error); ok && te.Temporary() {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			return err
		}
		go s.handle(nc)
	}
}

func (s *Server) handle(nc net.Conn) {
	hsDeadline := s.Handshake
	if hsDeadline == 0 {
		hsDeadline = 10 * time.Second
	}
	_ = nc.SetReadDeadline(time.Now().Add(hsDeadline))
	br := bufio.NewReaderSize(nc, 8192)
	req, err := http.ReadRequest(br)
	if err != nil {
		s.logf("handshake read failed from %s: %v", nc.RemoteAddr(), err)
		_ = nc.Close()
		return
	}
	if !validUpgrade(req) {
		writeHTTPError(nc, http.StatusBadRequest, "expected websocket upgrade")
		_ = nc.Close()
		return
	}
	if s.Path != "" && req.URL.Path != s.Path {
		writeHTTPError(nc, http.StatusNotFound, "not found")
		_ = nc.Close()
		return
	}
	key := strings.TrimSpace(req.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		writeHTTPError(nc, http.StatusBadRequest, "missing Sec-WebSocket-Key")
		_ = nc.Close()
		return
	}
	rawKey, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(rawKey) != 16 {
		writeHTTPError(nc, http.StatusBadRequest, "invalid Sec-WebSocket-Key")
		_ = nc.Close()
		return
	}

	accept := acceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	_ = nc.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(nc, resp); err != nil {
		_ = nc.Close()
		return
	}
	_ = nc.SetWriteDeadline(time.Time{})
	_ = nc.SetReadDeadline(time.Time{})

	c := &Conn{conn: nc, br: br, maxRead: s.MaxRead, done: make(chan struct{})}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = c.WriteClose(1001, "server_shutdown")
		_ = nc.Close()
		return
	}
	if s.conns == nil {
		s.conns = make(map[*Conn]struct{})
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			s.logf("handler panic: %v", r)
		}
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.closeDone()
		_ = nc.Close()
	}()
	if s.PingInterval > 0 {
		c.pongs = make(chan struct{}, 1)
		go s.keepalive(c)
	}
	if s.Handler != nil {
		s.Handler(c)
	}
}

// keepalive is the WS-layer heartbeat, websockets' keepalive() ported: sleep
// PingInterval, send a ping, and fail the connection with 1011 "keepalive ping
// timeout" when no pong arrives within PingTimeout (the code and reason the
// Python websockets library uses, so both bridges fail alike).
func (s *Server) keepalive(c *Conn) {
	interval := s.PingInterval
	timeout := s.PingTimeout
	if timeout <= 0 {
		timeout = interval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
		}
		// A pong from before this tick proves nothing about the ping below;
		// drain it so the wait measures "pong after ping".
		select {
		case <-c.pongs:
		default:
		}
		if err := c.Ping(nil); err != nil {
			s.logf("keepalive ping failed: %v", err)
			return
		}
		select {
		case <-c.done:
			return
		case <-c.pongs:
		case <-time.After(timeout):
			s.logf("keepalive ping timeout — closing connection")
			_ = c.WriteClose(1011, "keepalive ping timeout")
			_ = c.Close()
			return
		}
	}
}

func validUpgrade(req *http.Request) bool {
	if req.Method != http.MethodGet {
		return false
	}
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return false
	}
	if req.Header.Get("Sec-WebSocket-Version") != "13" {
		return false
	}
	for _, v := range strings.Split(req.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "upgrade") {
			return true
		}
	}
	return false
}

func acceptKey(key string) string {
	h := sha1.New()
	_, _ = io.WriteString(h, key+acceptGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func writeHTTPError(w io.Writer, status int, msg string) {
	body := msg + "\n"
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(body), body)
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger(format, args...)
	}
}

// Shutdown stops accepting new connections and closes live ones. It returns
// once the listener is closed; handlers observe normal read errors.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.ln
	conns := make([]*Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.WriteClose(1001, "server_shutdown")
		_ = c.Close()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(20 * time.Millisecond):
		return nil
	}
}
