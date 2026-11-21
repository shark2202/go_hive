package browserbridge

// R11 regression: /status must carry the fields the shipped extension side
// panel reads (bridge_version, runtime_version) and the per-listener
// bind_errors diagnostic, matching bridge.py::status_payload.

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// Parity: tools/src/gcu/browser/bridge.py::status_payload
func TestStatusReportsBridgeAndRuntimeVersion(t *testing.T) {
	runtime := "9.9.9-runtime"
	b := newTestBridge(t, Config{RuntimeVersion: &runtime})
	st := b.Status(context.Background())
	if st.BridgeVersion == nil || *st.BridgeVersion != BridgeVersion {
		t.Fatalf("bridge_version=%v want %q", st.BridgeVersion, BridgeVersion)
	}
	if st.RuntimeVersion == nil || *st.RuntimeVersion != runtime {
		t.Fatalf("runtime_version=%v want %q", st.RuntimeVersion, runtime)
	}

	// Wire shape: the side panel reads these keys out of the JSON body, and
	// runtime_version must render as null (not "") when no embedder set one.
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["bridge_version"] != BridgeVersion {
		t.Fatalf("wire bridge_version=%v want %q", wire["bridge_version"], BridgeVersion)
	}
	if wire["runtime_version"] != runtime {
		t.Fatalf("wire runtime_version=%v want %q", wire["runtime_version"], runtime)
	}
	if _, ok := wire["bind_errors"]; !ok {
		t.Fatalf("wire status has no bind_errors key: %v", wire)
	}

	bare := newTestBridge(t, Config{})
	bareRaw, err := json.Marshal(bare.Status(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bareRaw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["runtime_version"] != nil {
		t.Fatalf("runtime_version=%v want null without an embedder value", wire["runtime_version"])
	}
}

// Parity: tools/src/gcu/browser/bridge.py::start
func TestStatusReportsBindErrorsForOptionalListeners(t *testing.T) {
	legacyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer legacyLn.Close()
	controlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer controlLn.Close()

	// Both optional listeners are held by other processes: the bridge must
	// still start and report exactly which port failed, like upstream's
	// _bind_errors, instead of looking merely "disconnected".
	b := New(Config{
		WSAddr:      "127.0.0.1:0",
		HTTPAddr:    "127.0.0.1:0",
		ControlAddr: controlLn.Addr().String(),
		LegacyAddr:  legacyLn.Addr().String(),
		Logf:        t.Logf,
	})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = b.Shutdown(ctx)
	})

	st := b.Status(context.Background())
	if st.BindErrors["legacy_ws"] == "" {
		t.Fatalf("bind_errors missing legacy_ws: %v", st.BindErrors)
	}
	if st.BindErrors["control_rpc"] == "" {
		t.Fatalf("bind_errors missing control_rpc: %v", st.BindErrors)
	}
	if len(st.BindErrors) != 2 {
		t.Fatalf("bind_errors has unexpected roles: %v", st.BindErrors)
	}
	if st.Listening.LegacyWS || st.Listening.ControlRPC {
		t.Fatalf("listeners reported bound while ports were held: %+v", st.Listening)
	}
	if st.Listening.LegacyStatus {
		t.Fatalf("legacy_status must stay false: the Go bridge has no 9230 listener")
	}

	// Healthy bridge: no bind failures at all.
	clean := newTestBridge(t, Config{})
	if got := clean.Status(context.Background()).BindErrors; len(got) != 0 {
		t.Fatalf("healthy bridge bind_errors=%v want empty", got)
	}
}
