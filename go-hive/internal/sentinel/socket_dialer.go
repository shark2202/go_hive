package sentinel

// Slack Socket Mode transport.
//
// Socket Mode hands the app a one-shot wss:// URL from apps.connections.open.
// The listener and its parsing surface existed without a production dialer, so
// a configured Slack app token started a listener that backed off forever. This
// file supplies the missing transport: a small RFC 6455 client over the
// standard library, keeping the port's no-third-party-dependency rule while
// leaving SocketDialer injectable for tests.
//
// Parity: framework/sentinel/listeners.py::SlackSocketListener

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	websocketGUID            = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	defaultSocketDialTimeout = 30 * time.Second
	maxSocketFrameBytes      = 64 << 20
)

// WSSocketDialer opens Slack Socket Mode connections over wss:// (or ws:// for
// local test servers).
type WSSocketDialer struct {
	// Timeout bounds the TCP/TLS dial and the opening handshake. Zero uses
	// defaultSocketDialTimeout.
	Timeout time.Duration
	// TLSConfig overrides the default TLS client config. Tests use it to trust
	// a local certificate; production leaves it nil.
	TLSConfig *tls.Config
}

// NewSocketDialer returns the production Socket Mode dialer.
func NewSocketDialer() *WSSocketDialer {
	return &WSSocketDialer{}
}

func (d *WSSocketDialer) timeout() time.Duration {
	if d != nil && d.Timeout > 0 {
		return d.Timeout
	}
	return defaultSocketDialTimeout
}

// Dial performs the websocket opening handshake and returns the connected
// socket. The returned connection closes itself when ctx is cancelled, so a
// blocking ReadMessage cannot wedge Manager.Stop.
func (d *WSSocketDialer) Dial(ctx context.Context, rawURL string) (SocketConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("websocket: parse url: %w", err)
	}
	if parsed.Scheme != "wss" && parsed.Scheme != "ws" {
		return nil, fmt.Errorf("websocket: unsupported scheme %q", parsed.Scheme)
	}
	host := parsed.Host
	if host == "" {
		return nil, fmt.Errorf("websocket: empty host")
	}
	address := host
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		port := "80"
		if parsed.Scheme == "wss" {
			port = "443"
		}
		address = net.JoinHostPort(host, port)
	}

	dialer := &net.Dialer{Timeout: d.timeout()}
	deadline := time.Now().Add(d.timeout())
	var conn net.Conn
	if parsed.Scheme == "wss" {
		tlsConfig := d.TLSConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{ServerName: parsed.Hostname(), MinVersion: tls.VersionTLS12}
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		// The dial target carries only the host, never the ticket in the query
		// string, so this error is safe to log.
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		parsed.RequestURI(), host, key)
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake write: %w", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake read: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake status %d", response.StatusCode)
	}
	if got := response.Header.Get("Sec-WebSocket-Accept"); got != socketAcceptKey(key) {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake accept mismatch")
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return newSocketConn(ctx, conn, reader), nil
}

// socketAcceptKey is the Sec-WebSocket-Accept value for a client key.
func socketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// socketConn is one client-side websocket connection.
type socketConn struct {
	conn   net.Conn
	reader *bufio.Reader

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

func newSocketConn(ctx context.Context, conn net.Conn, reader *bufio.Reader) *socketConn {
	socket := &socketConn{conn: conn, reader: reader, done: make(chan struct{})}
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = socket.Close()
			case <-socket.done:
			}
		}()
	}
	return socket
}

// ReadMessage returns the next finish data message, answering control frames.
// A peer close reads as io.EOF.
func (c *socketConn) ReadMessage() ([]byte, error) {
	var (
		buffer     []byte
		fragmented bool
	)
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x9: // ping
			if err := c.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA: // pong
			continue
		case 0x8: // close
			_ = c.writeFrame(0x8, payload)
			return nil, io.EOF
		case 0x1, 0x2: // text, binary
			if fin {
				return payload, nil
			}
			fragmented = true
			buffer = payload
		case 0x0: // continuation
			if !fragmented {
				return nil, fmt.Errorf("websocket: stray continuation frame")
			}
			buffer = append(buffer, payload...)
			if fin {
				return buffer, nil
			}
		default:
			return nil, fmt.Errorf("websocket: unsupported opcode %d", opcode)
		}
	}
}

// WriteJSON sends one masked text frame (clients must mask).
func (c *socketConn) WriteJSON(value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.writeFrame(0x1, payload)
}

func (c *socketConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
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

func (c *socketConn) readFrame() (bool, byte, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(c.reader, head[:]); err != nil {
		return false, 0, nil, err
	}
	fin := head[0]&0x80 != 0
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if length < 0 || length > maxSocketFrameBytes {
		return false, 0, nil, fmt.Errorf("websocket: frame too large (%d)", length)
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.reader, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return fin, opcode, payload, nil
}

// Close performs the closing handshake (best effort) and releases the socket.
func (c *socketConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		close(c.done)
		payload := make([]byte, 2)
		binary.BigEndian.PutUint16(payload, 1000)
		_ = c.conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = c.writeFrame(0x8, payload)
		closeErr = c.conn.Close()
	})
	return closeErr
}
