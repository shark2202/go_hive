// Package wstest is a tiny RFC 6455 client used by tests to pose as the
// Chrome extension. It is not part of the production API.
package wstest

import (
	"bufio"
	"crypto/rand"
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

// Client is a minimal WebSocket client connection.
type Client struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex

	closeSent bool
	closed    bool
}

// Dial performs the client opening handshake against ws://host/path.
func Dial(rawURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	host := u.Host
	path := u.Path
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
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, host, key)
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("handshake status %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	return &Client{conn: conn, br: br}, nil
}

// WriteJSON marshals and sends v as a text frame.
func (c *Client) WriteJSON(v any) error {
	data, err := jsonMarshal(v)
	if err != nil {
		return err
	}
	return c.WriteText(data)
}

// WriteText sends a masked text frame.
func (c *Client) WriteText(payload []byte) error { return c.WriteFrame(true, 0x1, payload) }

// WriteFrame sends one masked frame.
func (c *Client) WriteFrame(fin bool, op byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	var head []byte
	b0 := op
	if fin {
		b0 |= 0x80
	}
	head = append(head, b0)
	n := len(payload)
	switch {
	case n < 126:
		head = append(head, byte(n)|0x80)
	case n <= 0xFFFF:
		head = append(head, 126|0x80, 0, 0)
		binary.BigEndian.PutUint16(head[len(head)-2:], uint16(n))
	default:
		head = append(head, 127|0x80)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}
	mask := [4]byte{}
	_, _ = rand.Read(mask[:])
	head = append(head, mask[:]...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i&3]
	}
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

// ReadMessage reads the next data frame (control frames are returned too).
func (c *Client) ReadMessage() (byte, []byte, error) {
	for {
		op, payload, fin, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		if op >= 0x8 {
			if op == 0x8 {
				return op, payload, fmt.Errorf("close frame")
			}
			return op, payload, nil
		}
		if !fin {
			// Tests only send tiny fragmented frames; fold continuations.
			all := payload
			for {
				op2, p2, fin2, err := c.readFrame()
				if err != nil {
					return 0, nil, err
				}
				if op2 != 0x0 {
					return 0, nil, fmt.Errorf("unexpected opcode in continuation: %d", op2)
				}
				all = append(all, p2...)
				if fin2 {
					return op, all, nil
				}
			}
		}
		return op, payload, nil
	}
}

func (c *Client) readFrame() (byte, []byte, bool, error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return 0, nil, false, err
	}
	fin := head[0]&0x80 != 0
	op := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, false, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return 0, nil, false, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, false, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return 0, nil, false, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return op, payload, fin, nil
}

// Ping sends a WebSocket ping.
func (c *Client) Ping(payload []byte) error { return c.WriteFrame(true, 0x9, payload) }

// Pong answers a WebSocket ping (a real browser does this from the stack).
func (c *Client) Pong(payload []byte) error { return c.WriteFrame(true, 0xA, payload) }

// Close sends a close frame and tears down TCP.
func (c *Client) Close(code int, reason string) error {
	c.mu.Lock()
	if c.closeSent || c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closeSent = true
	c.mu.Unlock()
	payload := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(payload, uint16(code))
	copy(payload[2:], reason)
	_ = c.WriteFrame(true, 0x8, payload)
	return c.Kill()
}

// Kill closes TCP without a closing handshake (simulates a dropped socket).
func (c *Client) Kill() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.conn.Close()
}

// LocalAddr returns the client-side address.
func (c *Client) LocalAddr() net.Addr { return c.conn.LocalAddr() }

// SetReadDeadline bounds future ReadMessage calls.
func (c *Client) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
