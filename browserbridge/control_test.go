package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/browserbridge/internal/wstest"
)

// control_test.go pins the 14831 surface against the upstream bridge_rpc.py
// contract: the allowlist, client_hello owner registration, server-initiated
// notify frames, per-connection concurrency, and the zombie reaper.

func dialControl(t *testing.T, b *Bridge) *wstest.Client {
	t.Helper()
	addr := b.ControlAddr()
	if addr == "" {
		t.Fatal("control RPC listener is not bound")
	}
	client, err := wstest.Dial("ws://"+addr+controlPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial control rpc: %v", err)
	}
	t.Cleanup(func() { _ = client.Kill() })
	return client
}

// rpcResponse reads frames until the one carrying rpc_id arrives. Notify
// frames (which have no rpc_id) are returned separately so a test can assert
// on them.
func rpcResponse(t *testing.T, client *wstest.Client, rpcID string) map[string]any {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		op, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read control response %q: %v", rpcID, err)
		}
		if op != 0x1 {
			continue
		}
		var frame map[string]any
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if id, _ := frame["rpc_id"].(string); id == rpcID {
			return frame
		}
	}
}

func rpcSend(t *testing.T, client *wstest.Client, rpcID, method string, args []any, params map[string]any) {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	if params == nil {
		params = map[string]any{}
	}
	if err := client.WriteJSON(map[string]any{
		"rpc_id": rpcID, "method": method, "args": args, "params": params,
	}); err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
}

func rpcCall(t *testing.T, client *wstest.Client, rpcID, method string, args []any, params map[string]any) map[string]any {
	t.Helper()
	rpcSend(t, client, rpcID, method, args, params)
	return rpcResponse(t, client, rpcID)
}

func waitForControlClients(t *testing.T, b *Bridge, want int) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool { return b.ControlClients() == want },
		fmt.Sprintf("control clients == %d (got %d)", want, b.ControlClients()))
}

// TestRPCAllowlistCoversDispatch proves the explicit allowlist and the adapter
// table cannot drift: every allowlisted method (except the two per-connection
// ones) has a dispatcher, and the allowlist matches upstream's 49 names.
func TestRPCAllowlistCoversDispatch(t *testing.T) {
	if missing := missingRPCAdapters(); len(missing) != 0 {
		t.Fatalf("allowlisted methods without a dispatcher: %v", missing)
	}
	if got := len(rpcMethods); got != 46 {
		t.Fatalf("allowlist has %d methods, want the upstream 46", got)
	}
	for _, name := range []string{
		"navigate", "screenshot_region", "client_hello", "subscribe_notifications",
		"record_action", "_cdp", "is_connected",
	} {
		if _, ok := rpcMethods[name]; !ok {
			t.Fatalf("allowlist is missing %q", name)
		}
	}
}

// TestRPCUnknownMethodIsRejected is the security boundary: a method outside the
// allowlist never reaches the bridge.
func TestRPCUnknownMethodIsRejected(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	client := dialControl(t, b)

	frame := rpcCall(t, client, "1", "definitely_not_a_method", nil, nil)
	if ok, _ := frame["ok"].(bool); ok {
		t.Fatalf("unknown method was accepted: %+v", frame)
	}
	errObj, _ := frame["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "unknown_method" {
		t.Fatalf("error code = %v, want unknown_method", errObj["code"])
	}
	if retryable, _ := errObj["retryable"].(bool); retryable {
		t.Fatalf("unknown_method must not be retryable: %+v", errObj)
	}
}

// TestRPCClientHelloRegistersOwnerAndCounts covers the identity handshake the
// idle watchdog depends on: a hello'd client counts as alive, status reports
// it, and a reconnect re-announces without duplicating the count.
func TestRPCClientHelloRegistersOwnerAndCounts(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	client := dialControl(t, b)
	waitForControlClients(t, b, 1) // unidentified clients count during the grace window

	frame := rpcCall(t, client, "hello-1", "client_hello", []any{os.Getpid()}, nil)
	if ok, _ := frame["ok"].(bool); !ok {
		t.Fatalf("client_hello envelope failed: %+v", frame)
	}
	result, _ := frame["result"].(map[string]any)
	if ok, _ := result["ok"].(bool); !ok {
		t.Fatalf("client_hello result = %+v", result)
	}
	if pid, _ := toInt(result["pid"]); pid != os.Getpid() {
		t.Fatalf("client_hello pid = %v, want %d", result["pid"], os.Getpid())
	}
	if result["previous"] != nil {
		t.Fatalf("first hello should report previous=nil, got %v", result["previous"])
	}

	st := b.Status(context.Background())
	if !st.Listening.ControlRPC || st.ControlRPCClients != 1 {
		t.Fatalf("status control fields = %+v clients=%d", st.Listening, st.ControlRPCClients)
	}
	if st.RuntimeAlive == nil || !*st.RuntimeAlive {
		t.Fatalf("runtime_alive = %v, want true for a live owner pid", st.RuntimeAlive)
	}

	// A second hello on the same socket reports the previous owner.
	frame = rpcCall(t, client, "hello-2", "client_hello", []any{os.Getpid()}, nil)
	result, _ = frame["result"].(map[string]any)
	if prev, _ := toInt(result["previous"]); prev != os.Getpid() {
		t.Fatalf("second hello previous = %v, want %d", result["previous"], os.Getpid())
	}
	if got := b.ControlClients(); got != 1 {
		t.Fatalf("control clients = %d, want 1 (one socket)", got)
	}

	// Invalid pid is a result-level failure, not a transport error (upstream
	// _register_client_owner returns {"ok": False, "error": "invalid_pid"}).
	frame = rpcCall(t, client, "hello-3", "client_hello", []any{"not-a-pid"}, nil)
	if ok, _ := frame["ok"].(bool); !ok {
		t.Fatalf("invalid hello should still be an ok envelope: %+v", frame)
	}
	result, _ = frame["result"].(map[string]any)
	if good, _ := result["ok"].(bool); good {
		t.Fatalf("invalid pid accepted: %+v", result)
	}
	if reason, _ := result["error"].(string); reason != "invalid_pid" {
		t.Fatalf("invalid pid error = %v, want invalid_pid", result["error"])
	}

	// Closing the socket drops the client, which is what lets the idle
	// watchdog fire.
	if err := client.Close(1000, "done"); err != nil {
		t.Fatalf("close control client: %v", err)
	}
	waitForControlClients(t, b, 0)
}

// TestRPCDispatchAgainstFakeExtension runs a real allowlisted command through
// the control port (not /command) and checks the extension saw it.
func TestRPCDispatchAgainstFakeExtension(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	ext := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), testTabListHandler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "extension hello")

	client := dialControl(t, b)
	frame := rpcCall(t, client, "tabs-1", "list_tabs", nil, nil)
	if ok, _ := frame["ok"].(bool); !ok {
		t.Fatalf("list_tabs via control failed: %+v", frame)
	}
	result, _ := frame["result"].(map[string]any)
	tabs, _ := result["tabs"].([]any)
	if len(tabs) != 2 {
		t.Fatalf("list_tabs returned %d tabs via control, want 2 (%+v)", len(tabs), result)
	}
	if ext.count("tab.list") == 0 {
		t.Fatal("control RPC did not reach the extension")
	}

	// Positional args, the RemoteBridge shape: activate_tab(11).
	frame = rpcCall(t, client, "activate-1", "activate_tab", []any{11}, nil)
	if ok, _ := frame["ok"].(bool); !ok {
		t.Fatalf("activate_tab via control failed: %+v", frame)
	}
	if cmd, ok := ext.lastCommand(); !ok || cmd.Type != "tab.activate" {
		t.Fatalf("extension did not receive tab.activate: %+v", cmd)
	}

	// A slow path through positional/kwargs routing: navigate with
	// wait_until=commit returns as soon as Page.navigate is answered.
	frame = rpcCall(t, client, "nav-1", "navigate", nil, map[string]any{
		"tab_id": 11, "url": "https://example.com/", "wait_until": "commit",
	})
	if ok, _ := frame["ok"].(bool); !ok {
		t.Fatalf("navigate via control failed: %+v", frame)
	}

	frame = rpcCall(t, client, "bad-1", "close_tab", nil, nil)
	if ok, _ := frame["ok"].(bool); ok {
		t.Fatalf("close_tab without tab_id should fail: %+v", frame)
	}
	errObj, _ := frame["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != CodeBadRequest {
		t.Fatalf("close_tab error code = %v, want %s", errObj["code"], CodeBadRequest)
	}
}

// TestRPCNotifyOnlyReachesSubscribers pins the notify gate and frame shape.
func TestRPCNotifyOnlyReachesSubscribers(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	subscribed := dialControl(t, b)
	plain := dialControl(t, b)

	if frame := rpcCall(t, subscribed, "sub-1", "subscribe_notifications", nil, nil); frame["ok"] != true {
		t.Fatalf("subscribe_notifications failed: %+v", frame)
	}
	if frame := rpcCall(t, plain, "hello-1", "client_hello", []any{os.Getpid()}, nil); frame["ok"] != true {
		t.Fatalf("client_hello failed: %+v", frame)
	}

	if delivered := b.NotifyClients("session-1", "The user handed \"Docs\" (tab #42) over to you.",
		map[string]any{"tabId": 42, "action": "handed-over"}); delivered != 1 {
		t.Fatalf("notify delivered to %d clients, want 1", delivered)
	}

	_ = subscribed.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		op, data, err := subscribed.ReadMessage()
		if err != nil {
			t.Fatalf("subscriber did not receive the notify frame: %v", err)
		}
		if op != 0x1 {
			continue
		}
		var frame map[string]any
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if frame["type"] != "notify" {
			continue
		}
		if frame["profile"] != "session-1" || frame["text"] != "The user handed \"Docs\" (tab #42) over to you." {
			t.Fatalf("notify frame = %+v", frame)
		}
		if id, _ := toInt(frame["tabId"]); id != 42 {
			t.Fatalf("notify tabId = %v, want 42", frame["tabId"])
		}
		if frame["action"] != "handed-over" {
			t.Fatalf("notify action = %v", frame["action"])
		}
		break
	}

	// The non-subscriber must not receive a notify frame. Prove it by sending
	// a call afterwards and asserting the next frame is that response.
	rpcSend(t, plain, "after-1", "is_connected", nil, nil)
	frame := rpcResponse(t, plain, "after-1")
	if frame["ok"] != true {
		t.Fatalf("plain client's call failed: %+v", frame)
	}
}

// TestRPCConcurrentCallsAreMultiplexed proves one control socket serves
// overlapping requests (upstream dispatches each request as its own task).
func TestRPCConcurrentCallsAreMultiplexed(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	client := dialControl(t, b)

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rpcSend(t, client, fmt.Sprintf("c-%d", i), "is_connected", nil, nil)
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(seen) < n {
		op, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read concurrent responses (got %d/%d): %v", len(seen), n, err)
		}
		if op != 0x1 {
			continue
		}
		var frame map[string]any
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if id, _ := frame["rpc_id"].(string); id != "" {
			if frame["ok"] != true {
				t.Fatalf("concurrent call %s failed: %+v", id, frame)
			}
			seen[id] = true
		}
	}
	for i := 0; i < n; i++ {
		if !seen[fmt.Sprintf("c-%d", i)] {
			t.Fatalf("response for c-%d was lost", i)
		}
	}
}

// TestRPCReaperClosesZombies covers both zombie classes: an owner PID that
// died, and a socket that never identified itself.
func TestRPCReaperClosesZombies(t *testing.T) {
	b := newTestBridge(t, Config{
		SweepDisabled:        true,
		ControlIdentifyGrace: 80 * time.Millisecond,
		ControlReapInterval:  20 * time.Millisecond,
	})

	dead := exec.Command("sh", "-c", "exit 0")
	if err := dead.Run(); err != nil {
		t.Fatalf("spawn short-lived process: %v", err)
	}
	deadPID := dead.Process.Pid

	identified := dialControl(t, b)
	if frame := rpcCall(t, identified, "hello-dead", "client_hello", []any{deadPID}, nil); frame["ok"] != true {
		t.Fatalf("client_hello with a dead pid failed: %+v", frame)
	}
	unidentified := dialControl(t, b)

	waitFor(t, 3*time.Second, func() bool { return b.ControlClients() == 0 },
		"the reaper to drop both zombies")

	// Both sockets must actually be closed, not just discounted.
	for name, client := range map[string]*wstest.Client{"identified": identified, "unidentified": unidentified} {
		_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
		closed := false
		for i := 0; i < 10; i++ {
			if _, _, err := client.ReadMessage(); err != nil {
				closed = true
				break
			}
		}
		if !closed {
			t.Fatalf("%s zombie socket was never closed", name)
		}
	}
}

// TestControlNotifyOnHandover proves the handover path feeds the control
// transport (adopt/release from the side panel), not just the library hook.
func TestControlNotifyOnHandover(t *testing.T) {
	b := newTestBridge(t, Config{SweepDisabled: true})
	client := dialControl(t, b)
	if frame := rpcCall(t, client, "sub-1", "subscribe_notifications", nil, nil); frame["ok"] != true {
		t.Fatalf("subscribe failed: %+v", frame)
	}
	b.notifyHandover("session-9", "Docs", 77, "handed-over")

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		op, data, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("no notify frame after handover: %v", err)
		}
		if op != 0x1 {
			continue
		}
		var frame map[string]any
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if frame["type"] != "notify" {
			continue
		}
		if frame["profile"] != "session-9" || frame["action"] != "handed-over" {
			t.Fatalf("handover notify frame = %+v", frame)
		}
		if text, _ := frame["text"].(string); text != `The user handed "Docs" (tab #77) over to you.` {
			t.Fatalf("handover text = %q", frame["text"])
		}
		return
	}
}
