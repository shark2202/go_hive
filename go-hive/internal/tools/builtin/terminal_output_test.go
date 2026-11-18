package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Parity: tools/src/terminal_tools/common/output_store.py::OutputStore
func TestTerminalOutputStorePaginates(t *testing.T) {
	store := newTerminalOutputStore(time.Minute, 1<<20)
	handle := store.Put([]byte("0123456789"))
	if !strings.HasPrefix(handle, "out_") || len(handle) != len("out_")+16 {
		t.Fatalf("handle = %q, want out_<16 hex>", handle)
	}

	first := store.Get(handle, 0, 4)
	if first.Data != "0123" || first.Offset != 0 || first.NextOffset != 4 || first.EOF || first.Expired {
		t.Fatalf("first slice = %+v", first)
	}
	second := store.Get(handle, first.NextOffset, 4)
	if second.Data != "4567" || second.NextOffset != 8 || second.EOF {
		t.Fatalf("second slice = %+v", second)
	}
	last := store.Get(handle, second.NextOffset, 4)
	if last.Data != "89" || last.NextOffset != 10 || !last.EOF || last.Expired {
		t.Fatalf("last slice = %+v", last)
	}
}

// Parity: tools/src/terminal_tools/common/output_store.py::OutputStore
func TestTerminalOutputStoreUnknownHandleExpires(t *testing.T) {
	store := newTerminalOutputStore(time.Minute, 1<<20)
	got := store.Get("out_doesnotexist", 7, 1024)
	if !got.Expired || !got.EOF {
		t.Fatalf("unknown handle = %+v, want expired+eof", got)
	}
	if got.Offset != 7 || got.NextOffset != 7 || got.Data != "" {
		t.Fatalf("unknown handle slice = %+v", got)
	}
}

// Parity: tools/src/terminal_tools/common/output_store.py::OutputStore
func TestTerminalOutputStoreTTLExpiry(t *testing.T) {
	store := newTerminalOutputStore(time.Minute, 1<<20)
	now := time.Unix(1000, 0)
	store.now = func() time.Time { return now }
	handle := store.Put([]byte("payload"))

	if got := store.Get(handle, 0, 1024); got.Expired {
		t.Fatalf("fresh handle expired: %+v", got)
	}
	now = now.Add(61 * time.Second)
	if got := store.Get(handle, 0, 1024); !got.Expired {
		t.Fatalf("handle survived TTL: %+v", got)
	}
}

// Parity: tools/src/terminal_tools/common/output_store.py::OutputStore
func TestTerminalOutputStoreEvictsLeastRecentlyUsed(t *testing.T) {
	store := newTerminalOutputStore(time.Minute, 8)
	now := time.Unix(1000, 0)
	store.now = func() time.Time { return now }

	first := store.Put([]byte("aaaa"))
	now = now.Add(time.Second)
	second := store.Put([]byte("bbbb"))
	now = now.Add(time.Second)
	_ = store.Get(first, 0, 4)
	now = now.Add(time.Second)
	third := store.Put([]byte("cccc"))

	if got := store.Get(second, 0, 4); !got.Expired {
		t.Fatalf("least recently used entry survived: %+v", got)
	}
	if got := store.Get(first, 0, 4); got.Expired {
		t.Fatalf("recently read entry evicted: %+v", got)
	}
	if got := store.Get(third, 0, 4); got.Expired || got.Data != "cccc" {
		t.Fatalf("newest entry missing: %+v", got)
	}
}

// Parity: tools/tests/test_terminal_tools_lifecycle.py::test_finishes_before_deadline
func TestTerminalOutputGetToolEnvelope(t *testing.T) {
	handle := terminalOutputs.Put([]byte("abcdef"))
	tb := New(t.TempDir())

	res, err := tb.terminalOutputGet(context.Background(), map[string]any{
		"output_handle": handle,
		"since_offset":  0,
		"max_kb":        1,
	})
	if err != nil {
		t.Fatalf("terminalOutputGet: %v", err)
	}
	var decoded struct {
		Data       string `json:"data"`
		Offset     int    `json:"offset"`
		NextOffset int    `json:"next_offset"`
		EOF        bool   `json:"eof"`
		Expired    bool   `json:"expired"`
	}
	if err := json.Unmarshal([]byte(res.Content), &decoded); err != nil {
		t.Fatalf("result %q is not JSON: %v", res.Content, err)
	}
	if decoded.Data != "abcdef" || decoded.NextOffset != 6 || !decoded.EOF || decoded.Expired {
		t.Fatalf("decoded = %+v", decoded)
	}

	missing, err := tb.terminalOutputGet(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("terminalOutputGet missing handle: %v", err)
	}
	if !missing.IsError || !strings.Contains(missing.Content, "output_handle is required") {
		t.Fatalf("missing handle result = %+v", missing)
	}
}
