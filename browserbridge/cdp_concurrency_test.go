package browserbridge

// R09 regression: upstream serializes each tab's CDP commands with
// bridge.py::_tab_lock, so two agents accidentally sharing a tab cannot
// interleave attach/sendCommand and corrupt the CDP session. These tests drive
// the real bridge over a real WebSocket against a scripted extension and
// assert the wire-level behavior: on one tab the second command frame must not
// reach the extension before the first result is delivered; different tabs
// stay independent.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/browserbridge/internal/wstest"
)

// cdpTestClient is a scripted extension: frames are read by the test and
// answered only when the test says so.
type cdpTestClient struct {
	t      *testing.T
	client *wstest.Client
}

func startCDPTestClient(t *testing.T, b *Bridge, label string) *cdpTestClient {
	t.Helper()
	client, err := wstest.Dial("ws://"+b.WSAddr()+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatalf("dial fake extension: %v", err)
	}
	t.Cleanup(func() { _ = client.Kill() })
	if err := client.WriteJSON(helloFor(label)); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve(label, nil)
		return err == nil
	}, "hello registration")
	return &cdpTestClient{t: t, client: client}
}

// read returns the next JSON frame or the read error (deadline exceeded).
func (c *cdpTestClient) read(timeout time.Duration) (map[string]any, error) {
	c.t.Helper()
	_ = c.client.SetReadDeadline(time.Now().Add(timeout))
	_, payload, err := c.client.ReadMessage()
	_ = c.client.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, err
	}
	var msg map[string]any
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// reply answers one frame's id with a result.
func (c *cdpTestClient) reply(msg map[string]any, result any) {
	c.t.Helper()
	id, _ := msg["id"].(string)
	if id == "" {
		c.t.Fatalf("frame without id: %v", msg)
	}
	if err := c.client.WriteJSON(map[string]any{"id": id, "result": result}); err != nil {
		c.t.Fatalf("reply: %v", err)
	}
}

// awaitCDP answers attach/audit bookkeeping frames until the next `cdp` frame
// for wantTab arrives, which it returns without answering.
func (c *cdpTestClient) awaitCDP(wantTab int, timeout time.Duration) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.t.Fatalf("no cdp frame for tab %d within %s", wantTab, timeout)
		}
		msg, err := c.read(remaining)
		if err != nil {
			c.t.Fatalf("read waiting for tab %d cdp: %v", wantTab, err)
		}
		switch msg["type"] {
		case "cdp.attach", "tab.audit", "tab.list":
			c.reply(msg, map[string]any{"ok": true})
		case "cdp":
			got, _ := msg["tabId"].(float64)
			if int(got) != wantTab {
				c.t.Fatalf("cdp for tab %v while waiting for tab %d", msg["tabId"], wantTab)
			}
			return msg
		case "ping":
			// app-level heartbeat; the bridge handles the pong itself
		default:
			c.t.Fatalf("unexpected frame type %v: %v", msg["type"], msg)
		}
	}
}

type cdpOutcome struct {
	res map[string]any
	err error
}

// Parity: tools/src/gcu/browser/bridge.py::_cdp
func TestCDPSerializesCommandsOnSameTab(t *testing.T) {
	b := newTestBridge(t, Config{})
	c := startCDPTestClient(t, b, "alpha")
	tabID := 11
	conn, err := b.resolve("alpha", &tabID)
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan cdpOutcome, 1)
	go func() {
		res, err := b.cdp(context.Background(), conn, tabID, "Runtime.evaluate",
			map[string]any{"expression": "first"}, 3*time.Second)
		first <- cdpOutcome{res, err}
	}()

	// The first caller attaches (fresh) and its command frame is held open:
	// the extension never answers it until released below.
	held := c.awaitCDP(tabID, 2*time.Second)

	second := make(chan cdpOutcome, 1)
	go func() {
		res, err := b.cdp(context.Background(), conn, tabID, "Runtime.evaluate",
			map[string]any{"expression": "second"}, 3*time.Second)
		second <- cdpOutcome{res, err}
	}()

	// No frame may reach the extension while the held command is unanswered.
	if msg, err := c.read(250 * time.Millisecond); err == nil {
		t.Fatalf("second command reached the extension before the first finished: %v", msg)
	}

	c.reply(held, map[string]any{"value": "first-result"})
	got1 := <-first
	if got1.err != nil || got1.res["value"] != "first-result" {
		t.Fatalf("first call: res=%v err=%v", got1.res, got1.err)
	}

	// With the lock released, the second command is delivered and completes.
	released := c.awaitCDP(tabID, 2*time.Second)
	c.reply(released, map[string]any{"value": "second-result"})
	got2 := <-second
	if got2.err != nil || got2.res["value"] != "second-result" {
		t.Fatalf("second call: res=%v err=%v", got2.res, got2.err)
	}
}

// Parity: tools/src/gcu/browser/bridge.py::_tab_lock
func TestCDPLockIsPerTabNotGlobal(t *testing.T) {
	b := newTestBridge(t, Config{})
	c := startCDPTestClient(t, b, "alpha")
	conn, err := b.resolve("alpha", nil)
	if err != nil {
		t.Fatal(err)
	}

	slow := make(chan cdpOutcome, 1)
	go func() {
		res, err := b.cdp(context.Background(), conn, 11, "Runtime.evaluate",
			map[string]any{"expression": "slow"}, 3*time.Second)
		slow <- cdpOutcome{res, err}
	}()
	held := c.awaitCDP(11, 2*time.Second)

	// A different tab is not blocked by the held tab-11 command.
	quick := make(chan cdpOutcome, 1)
	go func() {
		res, err := b.cdp(context.Background(), conn, 12, "Runtime.evaluate",
			map[string]any{"expression": "quick"}, 3*time.Second)
		quick <- cdpOutcome{res, err}
	}()
	other := c.awaitCDP(12, 1500*time.Millisecond)
	c.reply(other, map[string]any{"value": "quick-result"})
	gotQuick := <-quick
	if gotQuick.err != nil || gotQuick.res["value"] != "quick-result" {
		t.Fatalf("tab 12 call: res=%v err=%v", gotQuick.res, gotQuick.err)
	}

	c.reply(held, map[string]any{"value": "slow-result"})
	gotSlow := <-slow
	if gotSlow.err != nil || gotSlow.res["value"] != "slow-result" {
		t.Fatalf("tab 11 call: res=%v err=%v", gotSlow.res, gotSlow.err)
	}
}

// Parity: tools/src/gcu/browser/bridge.py::_tab_lock
func TestCDPLockWaitHonoursContextCancellation(t *testing.T) {
	b := newTestBridge(t, Config{})
	c := startCDPTestClient(t, b, "alpha")
	tabID := 11
	conn, err := b.resolve("alpha", &tabID)
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan cdpOutcome, 1)
	go func() {
		res, err := b.cdp(context.Background(), conn, tabID, "Runtime.evaluate",
			map[string]any{"expression": "held"}, 3*time.Second)
		first <- cdpOutcome{res, err}
	}()
	held := c.awaitCDP(tabID, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := b.cdp(ctx, conn, tabID, "Runtime.evaluate", map[string]any{"expression": "waiting"}, 3*time.Second)
	if err == nil {
		t.Fatalf("blocked call returned without error: %v", res)
	}
	if AsError(err).Code != CodeTimeout {
		t.Fatalf("blocked call error code=%s want %s (%v)", AsError(err).Code, CodeTimeout, err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation took too long: %s", elapsed)
	}

	c.reply(held, map[string]any{"value": "held-result"})
	if got := <-first; got.err != nil {
		t.Fatalf("held call: %v", got.err)
	}
}
