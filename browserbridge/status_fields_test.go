package browserbridge

// R11 regression: /status must carry the fields the shipped extension side
// panel reads (bridge_version, runtime_version) and the per-listener
// bind_errors diagnostic, matching bridge.py::status_payload.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
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
	legacyStatusLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer legacyStatusLn.Close()
	controlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer controlLn.Close()

	// All three optional listeners are held by other processes: the bridge
	// must still start and report exactly which port failed, like upstream's
	// _bind_errors, instead of looking merely "disconnected".
	b := New(Config{
		WSAddr:           "127.0.0.1:0",
		HTTPAddr:         "127.0.0.1:0",
		ControlAddr:      controlLn.Addr().String(),
		LegacyAddr:       legacyLn.Addr().String(),
		LegacyStatusAddr: legacyStatusLn.Addr().String(),
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

	st := b.Status(context.Background())
	if st.BindErrors["legacy_ws"] == "" {
		t.Fatalf("bind_errors missing legacy_ws: %v", st.BindErrors)
	}
	if st.BindErrors["legacy_status"] == "" {
		t.Fatalf("bind_errors missing legacy_status: %v", st.BindErrors)
	}
	if st.BindErrors["control_rpc"] == "" {
		t.Fatalf("bind_errors missing control_rpc: %v", st.BindErrors)
	}
	if len(st.BindErrors) != 3 {
		t.Fatalf("bind_errors has unexpected roles: %v", st.BindErrors)
	}
	if st.Listening.LegacyWS || st.Listening.LegacyStatus || st.Listening.ControlRPC {
		t.Fatalf("listeners reported bound while ports were held: %+v", st.Listening)
	}

	// Healthy bridge: no bind failures at all.
	clean := newTestBridge(t, Config{})
	if got := clean.Status(context.Background()).BindErrors; len(got) != 0 {
		t.Fatalf("healthy bridge bind_errors=%v want empty", got)
	}
}

// TestLegacyStatusListenerServesStatus pins the legacy 9229+1 pair: with the
// legacy listener enabled the bridge also binds the status endpoint there and
// answers the same payload as the primary 14830 API (upstream's
// LEGACY_STATUS_PORT, bridge.py::start).
//
// Parity: tools/src/gcu/browser/bridge.py::start + _http_status_handler
func TestLegacyStatusListenerServesStatus(t *testing.T) {
	b := New(Config{
		WSAddr:           "127.0.0.1:0",
		HTTPAddr:         "127.0.0.1:0",
		ControlAddr:      "127.0.0.1:0",
		LegacyAddr:       "127.0.0.1:0",
		LegacyStatusAddr: "127.0.0.1:0",
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
	addr := b.LegacyStatusAddr()
	if addr == "" {
		t.Fatalf("legacy status listener did not bind (bind_errors=%v)", b.Status(context.Background()).BindErrors)
	}
	if !b.Status(context.Background()).Listening.LegacyStatus {
		t.Fatalf("listening.legacy_status = false while the listener is bound")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{"/health", "/status", "/profiles"} {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s on the legacy status port: %v", path, err)
		}
		var payload map[string]any
		decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || decodeErr != nil {
			t.Fatalf("GET %s = HTTP %d, decode %v", path, resp.StatusCode, decodeErr)
		}
		if path == "/status" {
			if payload["bridge"] != "running" {
				t.Fatalf("/status payload on 9230 = %v", payload)
			}
			listening, _ := payload["listening"].(map[string]any)
			if listening["legacy_status"] != true {
				t.Fatalf("legacy /status listening = %v", payload["listening"])
			}
		}
	}
}

// TestLegacyStatusDisabledWithLegacyWS keeps the -no-legacy contract: no
// legacy WS listener means no 9230 listener either (option defaultValue not
// invented from LegacyAddr when it is disabled).
func TestLegacyStatusDisabledWithLegacyWS(t *testing.T) {
	b := newTestBridge(t, Config{})
	if got := b.LegacyStatusAddr(); got != "" {
		t.Fatalf("legacy status addr = %q with the legacy pair disabled", got)
	}
	if b.Status(context.Background()).Listening.LegacyStatus {
		t.Fatal("listening.legacy_status = true with the legacy pair disabled")
	}
}
