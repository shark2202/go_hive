package browserbridge

// Regression: the primary and legacy listeners must not collide with
// themselves. Upstream guards the legacy pair with `if port !=
// LEGACY_BRIDGE_PORT`; the Go bridge used to attempt the legacy binds anyway
// and record a misleading address-in-use bind error.

import (
	"context"
	"net"
	"testing"
	"time"
)

// Parity: tools/src/gcu/browser/bridge.py::start (the port != LEGACY_BRIDGE_PORT guard)
func TestLegacyListenersSkippedWhenPrimarySharesTheAddress(t *testing.T) {
	addrs := freeAddrs(t, 2)
	wsAddr, httpAddr := addrs[0], addrs[1]
	b := New(Config{
		WSAddr:           wsAddr,
		HTTPAddr:         httpAddr,
		ControlAddr:      "127.0.0.1:0",
		LegacyAddr:       wsAddr,
		LegacyStatusAddr: httpAddr,
		Logf:             t.Logf,
	})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.Shutdown(ctx)
	})
	if got := b.LegacyAddr(); got != "" {
		t.Fatalf("legacy WS bound to %q although the primary is there", got)
	}
	if got := b.LegacyStatusAddr(); got != "" {
		t.Fatalf("legacy status bound to %q although the primary API is there", got)
	}
	if errs := b.Status(context.Background()).BindErrors; len(errs) != 0 {
		t.Fatalf("self-collision produced bind errors: %v", errs)
	}
}

func freeAddrs(t *testing.T, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("pick free addr: %v", err)
		}
		out = append(out, ln.Addr().String())
		_ = ln.Close()
	}
	return out
}
