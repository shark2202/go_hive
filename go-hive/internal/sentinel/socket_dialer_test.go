package sentinel

// The Socket Mode dialer is a Go-only transport: Python uses aiohttp. These
// tests stand a local fake websocket server in for Slack, so no real Slack
// credential or network endpoint is involved.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startWSServer accepts one websocket client, completes the opening handshake
// and hands the upgraded stream to handler. The listener and connection are
// closed when the test finishes.
func startWSServer(t *testing.T, handler func(reader *bufio.Reader, writer *bufio.Writer)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}
		key := request.Header.Get("Sec-WebSocket-Key")
		_, _ = fmt.Fprintf(writer, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"+
			"Connection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", socketAcceptKey(key))
		if err := writer.Flush(); err != nil {
			return
		}
		handler(reader, writer)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	return "ws://" + listener.Addr().String()
}

// writeServerFrame writes one unmasked server frame (servers must not mask).
func writeServerFrame(writer *bufio.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xFFFF:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		var ext [8]byte
		for index := 7; index >= 0; index-- {
			ext[index] = byte(length)
			length >>= 8
		}
		header = append(header, ext[:]...)
	}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writer.Flush()
}

// readClientFrame decodes one masked client frame (clients must mask).
func readClientFrame(reader *bufio.Reader) (byte, bool, []byte, error) {
	var head [2]byte
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		return 0, false, nil, err
	}
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return 0, false, nil, err
		}
		length = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return 0, false, nil, err
		}
		length = 0
		for _, value := range ext {
			length = length<<8 | int64(value)
		}
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return 0, false, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, false, nil, err
	}
	if masked {
		for index := range payload {
			payload[index] ^= mask[index&3]
		}
	}
	return opcode, masked, payload, nil
}

// Parity-exempt: Go-only stdlib websocket transport; a local fake server
// stands in for Slack Socket Mode.
func TestSocketDialerReadsTextAndWritesMaskedJSON(t *testing.T) {
	ackPayload := make(chan []byte, 1)
	serverErr := make(chan error, 1)
	url := startWSServer(t, func(reader *bufio.Reader, writer *bufio.Writer) {
		if err := writeServerFrame(writer, 0x9, []byte("ping")); err != nil {
			serverErr <- err
			return
		}
		if err := writeServerFrame(writer, 0x1, []byte(`{"type":"hello"}`)); err != nil {
			serverErr <- err
			return
		}
		opcode, masked, payload, err := readClientFrame(reader)
		if err != nil {
			serverErr <- err
			return
		}
		if opcode != 0xA || !masked || string(payload) != "ping" {
			serverErr <- fmt.Errorf("pong = opcode %d masked=%v payload=%q", opcode, masked, payload)
			return
		}
		opcode, masked, payload, err = readClientFrame(reader)
		if err != nil {
			serverErr <- err
			return
		}
		if opcode != 0x1 || !masked {
			serverErr <- fmt.Errorf("ack = opcode %d masked=%v", opcode, masked)
			return
		}
		ackPayload <- payload
	})

	conn, err := NewSocketDialer().Dial(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(message) != `{"type":"hello"}` {
		t.Fatalf("message = %q", message)
	}
	if err := conn.WriteJSON(map[string]any{"envelope_id": "env-1"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-serverErr:
		t.Fatalf("server: %v", err)
	case payload := <-ackPayload:
		var decoded map[string]any
		if err := json.Unmarshal(payload, &decoded); err != nil || decoded["envelope_id"] != "env-1" {
			t.Fatalf("ack = %q (err %v)", payload, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the client ack")
	}
}

// Parity-exempt: Go-only transport handshake validation.
func TestSocketDialerRejectsBadHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\n"+
			"Connection: Upgrade\r\nSec-WebSocket-Accept: wrong\r\n\r\n")
	}()
	_, err = NewSocketDialer().Dial(context.Background(), "ws://"+listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "accept mismatch") {
		t.Fatalf("err = %v, want accept mismatch", err)
	}
	<-done
}

// Parity-exempt: Go-only transport shutdown path.
func TestSocketDialerReadUnblocksOnCancel(t *testing.T) {
	url := startWSServer(t, func(reader *bufio.Reader, _ *bufio.Writer) {
		// Hold the connection open until the client's context closes it.
		_, _ = io.Copy(io.Discard, reader)
	})
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := NewSocketDialer().Dial(ctx, url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	readErr := make(chan error, 1)
	go func() {
		_, err := conn.ReadMessage()
		readErr <- err
	}()
	cancel()
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("ReadMessage returned nil after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadMessage did not unblock after cancellation")
	}
}

// Parity-exempt: Go-only transport scheme validation.
func TestSocketDialerRejectsUnsupportedScheme(t *testing.T) {
	_, err := NewSocketDialer().Dial(context.Background(), "http://socket.example")
	if err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
		t.Fatalf("err = %v, want unsupported scheme", err)
	}
}
