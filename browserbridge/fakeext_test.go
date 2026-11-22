package browserbridge

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/browserbridge/internal/wstest"
)

type fakeCommand struct {
	ID     string
	Type   string
	Params map[string]any
}

type fakeExtension struct {
	t      *testing.T
	client *wstest.Client
	mu     sync.Mutex
	cmds   []fakeCommand
	pongs  atomic.Int64
	// wsPongs counts WS-layer pong answers; a real Chrome answers them from
	// the socket stack without the page seeing anything.
	wsPongs atomic.Int64

	ignorePings   bool
	ignoreWSPings atomic.Bool
	handler       func(fakeCommand) (any, string)
	ready         chan struct{}
	done          chan struct{}
}

func startFakeExtension(t *testing.T, wsAddr string, hello map[string]any, handler func(fakeCommand) (any, string)) *fakeExtension {
	t.Helper()
	client, err := wstest.Dial("ws://"+wsAddr+"/bridge", 2*time.Second)
	if err != nil {
		t.Fatalf("dial fake extension: %v", err)
	}
	f := &fakeExtension{
		t:       t,
		client:  client,
		handler: handler,
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
	}
	if hello == nil {
		hello = map[string]any{
			"type":            "hello",
			"version":         "1.7.4",
			"protocolVersion": 6,
			"extensionId":     "fake-extension-id",
			"profileLabel":    "default",
		}
	}
	if err := client.WriteJSON(hello); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	close(f.ready)
	go f.loop()
	t.Cleanup(func() { _ = f.client.Kill() })
	return f
}

func (f *fakeExtension) loop() {
	defer close(f.done)
	for {
		op, data, err := f.client.ReadMessage()
		if err != nil {
			return
		}
		if op == 0x9 { // WS-layer ping: the browser answers these itself
			if f.ignoreWSPings.Load() {
				continue
			}
			f.wsPongs.Add(1)
			_ = f.client.Pong(data)
			continue
		}
		if op != 0x1 {
			continue
		}
		var msg map[string]any
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		typ, _ := msg["type"].(string)
		if typ == "ping" {
			if f.ignorePings {
				continue
			}
			f.pongs.Add(1)
			_ = f.client.WriteJSON(map[string]any{"type": "pong"})
			continue
		}
		if typ == "pong" {
			continue
		}
		id, _ := msg["id"].(string)
		if id == "" {
			continue
		}
		cmd := fakeCommand{ID: id, Type: typ, Params: msg}
		f.mu.Lock()
		f.cmds = append(f.cmds, cmd)
		handler := f.handler
		f.mu.Unlock()
		result := any(map[string]any{"ok": true})
		errMsg := ""
		if handler != nil {
			result, errMsg = handler(cmd)
		}
		resp := map[string]any{"id": id}
		if errMsg != "" {
			resp["error"] = errMsg
		} else {
			resp["result"] = result
		}
		_ = f.client.WriteJSON(resp)
	}
}

func (f *fakeExtension) commands() []fakeCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCommand(nil), f.cmds...)
}

func (f *fakeExtension) lastCommand() (fakeCommand, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cmds) == 0 {
		return fakeCommand{}, false
	}
	return f.cmds[len(f.cmds)-1], true
}

func (f *fakeExtension) waitForCommand(typ string, timeout time.Duration) fakeCommand {
	f.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, cmd := range f.commands() {
			if cmd.Type == typ {
				return cmd
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("fake extension did not receive command %q", typ)
	return fakeCommand{}
}

func (f *fakeExtension) count(typ string) int {
	n := 0
	for _, cmd := range f.commands() {
		if cmd.Type == typ {
			n++
		}
	}
	return n
}

func (f *fakeExtension) countCDP(method string) int {
	n := 0
	for _, cmd := range f.commands() {
		if cmd.Type == "cdp" && cmd.Params["method"] == method {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}
