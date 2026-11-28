package browsercli

// TestMain doubles as the stub bridge child for the CLI ensure test: when the
// test spawns this test binary with HIVE_TEST_BRIDGE_STUB=1, it behaves like
// cmd/browserbridge (a /health endpoint, a ready line, SIGTERM death) instead
// of running tests.
//
// Parity-exempt: TestMain 兼作 stub child，是 Go 侧测试夹具，上游没有对应物。
// The supervisor's own lifecycle tests keep an equivalent fixture in
// internal/cli/browsersupervisor; this copy exists because browsercli's
// ensure test spawns os.Args[0] from this package's test binary.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Parity-exempt: Go-side TestMain only; no Python test fixture.
func TestMain(m *testing.M) {
	if os.Getenv("HIVE_TEST_BRIDGE_STUB") == "1" {
		browserBridgeStubMain()
		return
	}
	os.Exit(m.Run())
}

func browserBridgeStubMain() {
	if os.Getenv("HIVE_TEST_STUB_MODE") == "exit-early" {
		fmt.Fprintln(os.Stderr, "stub: refusing to start")
		os.Exit(3)
	}
	addr := os.Getenv("HIVE_TEST_STUB_ADDR")
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen http %s: %v\n", addr, err)
		os.Exit(1)
	}
	// A detached spawn (the CLI's ensure path) has no supervisor to reap it;
	// the test that started it kills the pid from this file.
	if path := os.Getenv("HIVE_TEST_STUB_PIDFILE"); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeStubJSON(w, map[string]any{"ok": true, "status": "ok", "pid": os.Getpid()})
	})
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		writeStubJSON(w, map[string]any{"ok": true, "result": map[string]any{"ok": true, "stub": true}})
	})
	fmt.Printf("{\"ok\":true,\"ready\":true,\"pid\":%d,\"http\":%q}\n", os.Getpid(), addr)
	if os.Getenv("HIVE_TEST_STUB_MODE") == "idle-exit" {
		// Behave like the real binary's idle watchdog: answer /health, then
		// leave with exit code 0 so the supervisor parks instead of restarting.
		go func() { _ = http.Serve(listener, mux) }()
		time.Sleep(400 * time.Millisecond)
		return
	}
	_ = http.Serve(listener, mux)
}

func writeStubJSON(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

// safeBuffer is a bytes.Buffer that test goroutines and the test can use at the
// same time.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(chunk)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeTCPAddr(t *testing.T) (string, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick free port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	return host + ":" + port, port
}
