package ws_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/browserbridge/internal/wstest"
	"github.com/aden-hive/hive-go/browserbridge/ws"
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
