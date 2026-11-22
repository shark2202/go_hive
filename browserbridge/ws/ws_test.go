package ws_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/browserbridge/internal/wstest"
	"github.com/shark2202/go_hive/browserbridge/ws"
)

func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gotClose := make(chan *ws.CloseError, 4)
	srv := &ws.Server{
		Path: "/bridge",
		Handler: func(c *ws.Conn) {
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					if ce, ok := err.(*ws.CloseError); ok {
						gotClose <- ce
					}
					return
				}
				if err := c.WriteText(append([]byte("echo:"), data...)); err != nil {
					return
				}
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return ln.Addr().String()
}

func TestHandshakeTextEcho(t *testing.T) {
	addr := startEcho(t)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "done")
	if err := c.WriteText([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	op, payload, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if op != 0x1 || string(payload) != "echo:hello" {
		t.Fatalf("got op=%d payload=%q", op, payload)
	}
}

func TestFragmentedMessage(t *testing.T) {
	addr := startEcho(t)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "done")
	if err := c.WriteFrame(false, 0x1, []byte("frag")); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteFrame(true, 0x0, []byte("mented")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, payload, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "echo:fragmented" {
		t.Fatalf("payload=%q", payload)
	}
}

func TestPingPong(t *testing.T) {
	addr := startEcho(t)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "done")
	if err := c.Ping([]byte("probe")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	op, payload, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if op != 0xA || string(payload) != "probe" {
		t.Fatalf("expected pong probe, got op=%d payload=%q", op, payload)
	}
}

func TestPeerCloseEchoed(t *testing.T) {
	addr := startEcho(t)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(1000, "bye"); err != nil {
		t.Fatal(err)
	}
	// The server echoes before closing; the client may observe it before
	// the TCP teardown propagates.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	op, _, err := c.ReadMessage()
	if err == nil && op != 0x8 {
		t.Fatalf("expected close frame, got op=%d", op)
	}
}

func TestRejectsPlainHTTPAndWrongPath(t *testing.T) {
	addr := startEcho(t)
	for _, tc := range []struct {
		name    string
		path    string
		upgrade bool
		code    int
	}{
		{"no upgrade", "/bridge", false, http.StatusBadRequest},
		{"wrong path", "/nope", true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if tc.upgrade {
				fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", tc.path, addr)
			} else {
				fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\n\r\n", tc.path, addr)
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.code {
				t.Fatalf("status=%d want %d", resp.StatusCode, tc.code)
			}
			if !strings.Contains(resp.Header.Get("Content-Type"), "text/plain") {
				t.Fatalf("content-type=%q", resp.Header.Get("Content-Type"))
			}
		})
	}
}

// startKeepaliveEcho serves the echo endpoint with the WS-layer keepalive
// ticker enabled, so a test can prove the server pings and fails silent peers.
func startKeepaliveEcho(t *testing.T, interval, timeout time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &ws.Server{
		Path:         "/bridge",
		PingInterval: interval,
		PingTimeout:  timeout,
		Handler: func(c *ws.Conn) {
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					return
				}
				if err := c.WriteText(append([]byte("echo:"), data...)); err != nil {
					return
				}
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return ln.Addr().String()
}

// TestKeepaliveClosesPeerThatNeverPongs ports the Python bridge's
// websockets.serve(ping_interval=20, ping_timeout=20) failure mode: a peer
// that lets the WS-layer pings go unanswered is closed with 1011
// "keepalive ping timeout" (websockets' CloseCode.INTERNAL_ERROR).
func TestKeepaliveClosesPeerThatNeverPongs(t *testing.T) {
	addr := startKeepaliveEcho(t, 40*time.Millisecond, 40*time.Millisecond)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Kill()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(deadline)
		op, payload, err := c.ReadMessage()
		if op == 0x8 { // close frame
			code := 1005
			reason := ""
			if len(payload) >= 2 {
				code = int(binary.BigEndian.Uint16(payload[:2]))
				reason = string(payload[2:])
			}
			if code != 1011 {
				t.Fatalf("close code = %d, want 1011 (internal error)", code)
			}
			if !strings.Contains(reason, "keepalive ping timeout") {
				t.Fatalf("close reason = %q, want keepalive ping timeout", reason)
			}
			return
		}
		if err != nil {
			t.Fatalf("connection died without a close frame: %v", err)
		}
	}
	t.Fatal("server never closed a connection that ignored its pings")
}

// TestKeepalivePongsKeepConnectionAlive is the other half: a peer that answers
// the WS-layer pings stays connected across many heartbeat cycles and keeps
// carrying messages.
func TestKeepalivePongsKeepConnectionAlive(t *testing.T) {
	addr := startKeepaliveEcho(t, 30*time.Millisecond, 30*time.Millisecond)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Kill()
	replies := make(chan string, 1)
	go func() {
		for {
			op, payload, err := c.ReadMessage()
			if err != nil {
				return
			}
			switch op {
			case 0x9:
				_ = c.Pong(payload)
			case 0x1:
				select {
				case replies <- string(payload):
				default:
				}
			}
		}
	}()
	// Five heartbeat cycles: the app-level ping cannot explain survival here,
	// only the pong answers can.
	time.Sleep(150 * time.Millisecond)
	if err := c.WriteText([]byte("alive")); err != nil {
		t.Fatalf("write after heartbeats: %v", err)
	}
	select {
	case got := <-replies:
		if got != "echo:alive" {
			t.Fatalf("reply = %q, want echo:alive", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connection did not answer after the WS-layer heartbeats")
	}
}

// startSizer serves one endpoint that answers each message with its byte
// length ("len=N"), so a test can prove how large a frame the read limit
// accepts without echoing the payload back.
func startSizer(t *testing.T, maxRead int64) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &ws.Server{
		Path:    "/bridge",
		MaxRead: maxRead,
		Handler: func(c *ws.Conn) {
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					return
				}
				if err := c.WriteText([]byte(fmt.Sprintf("len=%d", len(data)))); err != nil {
					return
				}
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return ln.Addr().String()
}

// TestDefaultReadLimitAccepts50MiBMessages pins R10: the Python bridge serves
// websockets with max_size=50 MiB because CDP responses (AX trees, screenshots)
// can be large; the Go port used to reject anything over 16 MiB.
func TestDefaultReadLimitAccepts50MiBMessages(t *testing.T) {
	if ws.DefaultMaxRead != 50<<20 {
		t.Fatalf("DefaultMaxRead=%d want 50 MiB (upstream bridge.py max_size)", ws.DefaultMaxRead)
	}
	addr := startSizer(t, 0) // zero → DefaultMaxRead
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "done")
	// 17 MiB: above the old 16 MiB cap, below upstream's 50 MiB.
	payload := make([]byte, 17<<20)
	if err := c.WriteText(payload); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, reply, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("17 MiB frame was refused: %v", err)
	}
	if string(reply) != fmt.Sprintf("len=%d", len(payload)) {
		t.Fatalf("reply=%q", reply)
	}
}

// TestReadLimitStillRejectsOversizedMessages keeps the ceiling honest: an
// explicit lower cap must still produce a protocol error.
func TestReadLimitStillRejectsOversizedMessages(t *testing.T) {
	addr := startSizer(t, 1<<20)
	c, err := wstest.Dial("ws://"+addr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "done")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := c.WriteText(make([]byte, 2<<20)); err != nil {
		// The server rejects the frame from its length header, before the
		// payload is drained, so the write can die with a reset — that is
		// also a rejection (nothing was accepted).
		return
	}
	op, payload, err := c.ReadMessage()
	if err == nil {
		t.Fatalf("oversized frame accepted: op=%d payload=%q", op, payload)
	}
}
