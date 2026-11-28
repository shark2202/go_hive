package cli

// The supervisor package owns the bridge lifecycle but not the control-plane
// keepalive; these cases pin the composition-root seam that wires them.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/cli/browsersupervisor"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// safeBuffer is a bytes.Buffer the supervisor goroutine and the test can use at
// the same time.
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

// TestStartBrowserBridgeWiresControlKeepalive covers the reuse path: a healthy
// bridge is not respawned, but the serve still opens its control connection
// through the injected keepalive with the resolved address and notify sink.
// Parity-exempt: Go-side browser supervisor seam test; upstream has no equivalent.
func TestStartBrowserBridgeWiresControlKeepalive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	addr := strings.TrimPrefix(server.URL, "http://")

	var got browsersupervisor.ControlKeepaliveConfig
	stopped := false
	out := &safeBuffer{}
	stop := startBrowserBridge(browserSupervisorConfig{
		Home:        hivehome.Home{Root: t.TempDir()},
		Out:         out,
		ControlAddr: "control.example:1",
		StartControlKeepalive: func(cfg browsersupervisor.ControlKeepaliveConfig) func() {
			got = cfg
			return func() { stopped = true }
		},
		LookupEnv: func(key string) string {
			switch key {
			case browserBridgeSwitchEnv:
				return "1"
			case "HIVE_BROWSERBRIDGE_HTTP":
				return addr
			default:
				return ""
			}
		},
	})
	defer stop()

	if !strings.Contains(out.String(), "reusing the instance on "+addr) {
		t.Fatalf("expected the reuse notice, got %q", out.String())
	}
	if got.ControlAddr != "control.example:1" {
		t.Fatalf("keepalive control addr = %q", got.ControlAddr)
	}
	if got.Out != out {
		t.Fatal("keepalive did not receive the supervisor output writer")
	}
	if got.OwnerPID != os.Getpid() {
		t.Fatalf("keepalive owner pid = %d, want %d", got.OwnerPID, os.Getpid())
	}
	if got.OnNotify == nil {
		t.Fatal("keepalive did not receive the notify sink")
	}

	stop()
	if !stopped {
		t.Fatal("stopping serve must close the keepalive")
	}
}

// TestStartBrowserBridgeHonorsDisabledSwitch keeps the moved enablement switch
// authoritative: with HIVE_BROWSER_BRIDGE=0 the supervisor never starts and the
// keepalive seam is never touched.
// Parity-exempt: Go-side enablement switch test; upstream has no equivalent.
func TestStartBrowserBridgeHonorsDisabledSwitch(t *testing.T) {
	called := false
	stop := startBrowserBridge(browserSupervisorConfig{
		Home: hivehome.Home{Root: t.TempDir()},
		StartControlKeepalive: func(browsersupervisor.ControlKeepaliveConfig) func() {
			called = true
			return func() {}
		},
		LookupEnv: func(key string) string {
			if key == browserBridgeSwitchEnv {
				return "0"
			}
			return ""
		},
	})
	stop()
	if called {
		t.Fatal("a disabled bridge must not open a control keepalive")
	}
}
