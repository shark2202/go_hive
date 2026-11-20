package cli

// browser_control_test.go exercises the serve-side control-RPC client against a
// stub control server: handshake, client_hello(owner_pid), notification
// subscription and push, reconnect-after-drop, and the wake hint that makes the
// supervisor respawn an idle-exited bridge.
//
// Parity: core/framework/loader/cli.py::_bridge_keepalive_loop (the Go side is
// a process-boundary client, so a stub server is the honest equivalent of the
// Python fake-client test in core/tests/test_serve_bridge_owner.py).

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type controlRecord struct {
	Method string
	Args   []any
}

// fakeControlServer accepts control-RPC WebSocket clients and answers like the
// bridge's 14831 listener does for the two methods serve calls.
type fakeControlServer struct {
	t      *testing.T
	ln     net.Listener
	mu     sync.Mutex
	seen   []controlRecord
	conns  []net.Conn
	closed bool

	// dropAfterHello makes the server hang up once the client identified
	// itself, which forces the keepalive to reconnect.
	dropAfterHello bool
	// rejectCall, when set, answers that method with an error envelope.
	rejectCall string

	subscribers int
}

func newFakeControlServer(t *testing.T) *fakeControlServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake control: %v", err)
	}
	s := &fakeControlServer{t: t, ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(conn)
		}
	}()
	t.Cleanup(func() { _ = s.close() })
	return s
}

func (s *fakeControlServer) addr() string { return s.ln.Addr().String() }

func (s *fakeControlServer) close() error {
	s.mu.Lock()
	s.closed = true
	conns := append([]net.Conn(nil), s.conns...)
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return s.ln.Close()
}

func (s *fakeControlServer) records() []controlRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]controlRecord(nil), s.seen...)
}

func (s *fakeControlServer) countMethod(method string) int {
	n := 0
	for _, record := range s.records() {
		if record.Method == method {
			n++
		}
	}
	return n
}

func (s *fakeControlServer) helloArgs() []any {
	for _, record := range s.records() {
		if record.Method == "client_hello" {
			return record.Args
		}
	}
	return nil
}

func (s *fakeControlServer) subscriberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribers
}

func (s *fakeControlServer) handle(conn net.Conn) {
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	key := request.Header.Get("Sec-WebSocket-Key")
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	response := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := io.WriteString(conn, response); err != nil {
		return
	}

	for {
		payload, opcode, err := readClientFrame(reader)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8:
			return
		case 0x9:
			continue
		}
		var frame map[string]any
		if json.Unmarshal(payload, &frame) != nil {
			continue
		}
		id, _ := frame["rpc_id"].(string)
		method, _ := frame["method"].(string)
		args, _ := frame["args"].([]any)
		s.mu.Lock()
		s.seen = append(s.seen, controlRecord{Method: method, Args: args})
		s.mu.Unlock()

		if method == s.rejectCall {
			_ = writeServerFrame(conn, map[string]any{
				"rpc_id": id, "ok": false,
				"error": map[string]any{"message": "nope", "code": "unknown_method", "retryable": false},
			})
			continue
		}
		_ = writeServerFrame(conn, map[string]any{
			"rpc_id": id, "ok": true, "result": map[string]any{"ok": true},
		})
		if method == "client_hello" && s.dropAfterHello {
			return
		}
		if method == "subscribe_notifications" {
			s.mu.Lock()
			s.subscribers++
			s.mu.Unlock()
			_ = writeServerFrame(conn, map[string]any{
				"type": "notify", "profile": "session-1",
				"text":  "The user handed \"Docs\" (tab #7) over to you.",
				"tabId": 7, "action": "handed-over",
			})
		}
	}
}

// readClientFrame reads one client frame (always masked) and returns the
// unmasked payload plus its opcode.
func readClientFrame(reader *bufio.Reader) ([]byte, byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		return nil, 0, err
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return nil, 0, err
		}
		length = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return nil, 0, err
		}
		length = int64(binary.BigEndian.Uint64(ext[:]))
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return nil, 0, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
	}
	return payload, opcode, nil
}

func writeServerFrame(conn net.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	frame := []byte{0x81}
	switch {
	case len(payload) < 126:
		frame = append(frame, byte(len(payload)))
	case len(payload) <= 0xFFFF:
		frame = append(frame, 126, 0, 0)
		binary.BigEndian.PutUint16(frame[len(frame)-2:], uint16(len(payload)))
	default:
		frame = append(frame, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(payload)))
		frame = append(frame, ext[:]...)
	}
	frame = append(frame, payload...)
	_, err = conn.Write(frame)
	return err
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", message)
}

// Parity: tools/src/gcu/browser/bridge_rpc.py::CONTROL_PORT
func TestBrowserBridgeControlAddrPrecedence(t *testing.T) {
	if got := browserBridgeControlAddr(func(string) string { return "" }); got != browserBridgeDefaultControl {
		t.Fatalf("default control addr = %q", got)
	}
	got := browserBridgeControlAddr(func(key string) string {
		if key == browserBridgeControlEnv {
			return " 127.0.0.1:15001 "
		}
		return ""
	})
	if got != "127.0.0.1:15001" {
		t.Fatalf("override control addr = %q", got)
	}
}

// TestControlKeepaliveHelloSubscribeNotify is the upstream keepalive contract:
// identify the owner PID, subscribe, receive the pushed frame.
// Parity: core/framework/loader/cli.py::_bridge_keepalive_loop
func TestControlKeepaliveHelloSubscribeNotify(t *testing.T) {
	server := newFakeControlServer(t)
	ownerPID := os.Getpid()
	notifications := make(chan string, 4)
	out := &safeBuffer{}

	stop := startBrowserControlKeepalive(browserControlKeepalive{
		controlAddr: server.addr(),
		ownerPID:    ownerPID,
		out:         out,
		onNotify: func(profile, text string, extra map[string]any) {
			action, _ := extra["action"].(string)
			notifications <- profile + "|" + text + "|" + action
		},
	})
	defer stop()

	waitForCondition(t, 5*time.Second, func() bool { return server.countMethod("client_hello") > 0 }, "client_hello")
	args := server.helloArgs()
	if len(args) != 1 {
		t.Fatalf("client_hello args = %v, want one positional owner pid", args)
	}
	if pid, ok := args[0].(float64); !ok || int(pid) != ownerPID {
		t.Fatalf("client_hello pid = %v, want %d", args[0], ownerPID)
	}
	waitForCondition(t, 5*time.Second, func() bool { return server.countMethod("subscribe_notifications") > 0 },
		"subscribe_notifications")
	waitForCondition(t, 5*time.Second, func() bool { return server.subscriberCount() > 0 }, "server to register the subscriber")

	select {
	case got := <-notifications:
		want := "session-1|The user handed \"Docs\" (tab #7) over to you.|handed-over"
		if got != want {
			t.Fatalf("notify = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notify frame reached the keepalive")
	}
	if !strings.Contains(out.String(), "control connected on "+server.addr()) {
		t.Fatalf("keepalive did not log the connection; output:\n%s", out.String())
	}

	stop()
	// Stopping closes the control socket, which is the signal the bridge's
	// idle watchdog reads after serve exits.
	waitForCondition(t, 5*time.Second, func() bool {
		return strings.Count(out.String(), "control connected on") == 1
	}, "no reconnect after stop")
	time.Sleep(300 * time.Millisecond)
	if got := server.countMethod("subscribe_notifications"); got != 1 {
		t.Fatalf("keepalive reconnected after stop (subscribe count=%d)", got)
	}
}

// TestControlKeepaliveReconnectsAfterDrop covers bridge restarts: the socket
// drops, the loop redials and re-identifies.
// Parity: core/framework/loader/cli.py::_bridge_keepalive_loop
func TestControlKeepaliveReconnectsAfterDrop(t *testing.T) {
	server := newFakeControlServer(t)
	server.dropAfterHello = true
	stop := startBrowserControlKeepalive(browserControlKeepalive{
		controlAddr: server.addr(),
		ownerPID:    os.Getpid(),
		out:         io.Discard,
	})
	defer stop()
	waitForCondition(t, 8*time.Second, func() bool { return server.countMethod("client_hello") >= 2 },
		"the keepalive to reconnect after the socket dropped")
}

// TestControlKeepaliveWakesSupervisorOnUnreachableBridge is the respawn path:
// while no bridge answers 14831, the loop keeps retrying and asks the
// supervisor to bring one up.
// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
func TestControlKeepaliveWakesSupervisorOnUnreachableBridge(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing is listening now

	woken := make(chan struct{}, 8)
	stop := startBrowserControlKeepalive(browserControlKeepalive{
		controlAddr: addr,
		ownerPID:    os.Getpid(),
		out:         io.Discard,
		wake:        func() { woken <- struct{}{} },
	})
	defer stop()
	select {
	case <-woken:
	case <-time.After(5 * time.Second):
		t.Fatal("the keepalive never asked the supervisor to start a bridge")
	}
}

// TestBrowserControlClientCallSurfacesError pins the error envelope handling:
// the bridge's structured failure becomes a Go error with its message.
// Parity: tools/src/gcu/browser/bridge_rpc.py::BridgeClient.call
func TestBrowserControlClientCallSurfacesError(t *testing.T) {
	server := newFakeControlServer(t)
	server.rejectCall = "is_connected"
	client := newBrowserControlClient("ws://" + server.addr() + "/control")
	if err := client.connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.close()
	_, err := client.call("is_connected")
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want the bridge's message", err)
	}
}

// TestControlClientRejectsNonUpgradeResponse keeps the handshake honest: an
// HTTP error response must fail the dial instead of producing a half-open
// "connected" client.
// Parity-exempt: 上游 websockets 客户端库自己校验 101 握手；Go 侧是手写最小客户端，需单测。
func TestControlClientRejectsNonUpgradeResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = conn.Read(make([]byte, 4096))
		_, _ = io.WriteString(conn, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
	}()
	client := newBrowserControlClient("ws://" + ln.Addr().String() + "/control")
	if err := client.connect(); err == nil {
		client.close()
		t.Fatal("connect accepted a non-101 handshake response")
	}
}
