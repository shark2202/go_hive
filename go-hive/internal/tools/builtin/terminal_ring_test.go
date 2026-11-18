package builtin

import (
	"bytes"
	"testing"
)

// Parity: tools/tests/test_terminal_tools_jobs.py::test_offset_bookkeeping
func TestTerminalRingOffsetsAdvanceAcrossReads(t *testing.T) {
	ring := newTerminalRing(1024)
	for _, line := range []string{"line1\n", "line2\n", "line3\n"} {
		ring.Write([]byte(line))
	}

	first := ring.Read(0, 6)
	if string(first.Data) != "line1\n" {
		t.Fatalf("first read = %q, want %q", first.Data, "line1\n")
	}
	if first.Offset != 0 || first.NextOffset != 6 {
		t.Fatalf("first offsets = %d/%d, want 0/6", first.Offset, first.NextOffset)
	}
	second := ring.Read(first.NextOffset, 6)
	if string(second.Data) != "line2\n" {
		t.Fatalf("second read = %q, want %q", second.Data, "line2\n")
	}
	if second.NextOffset != 12 {
		t.Fatalf("second next_offset = %d, want 12", second.NextOffset)
	}
	tail := ring.Read(second.NextOffset, 4096)
	if string(tail.Data) != "line3\n" {
		t.Fatalf("tail read = %q, want %q", tail.Data, "line3\n")
	}
	if tail.NextOffset != ring.TotalWritten() {
		t.Fatalf("tail next_offset = %d, want total %d", tail.NextOffset, ring.TotalWritten())
	}
}

// Parity: tools/src/terminal_tools/common/ring_buffer.py::RingBuffer
func TestTerminalRingEvictsAndReportsDroppedBytes(t *testing.T) {
	ring := newTerminalRing(8)
	ring.Write([]byte("abcdefgh"))
	ring.Write([]byte("ijkl"))

	got := ring.Read(0, 4096)
	if string(got.Data) != "efghijkl" {
		t.Fatalf("data = %q, want %q", got.Data, "efghijkl")
	}
	if got.TruncatedBytesDropped != 4 {
		t.Fatalf("dropped = %d, want 4", got.TruncatedBytesDropped)
	}
	if got.Offset != 4 {
		t.Fatalf("offset = %d, want 4", got.Offset)
	}
	if ring.Floor() != 4 || ring.TotalWritten() != 12 {
		t.Fatalf("floor/total = %d/%d, want 4/12", ring.Floor(), ring.TotalWritten())
	}
}

// Parity: tools/src/terminal_tools/common/ring_buffer.py::RingBuffer
func TestTerminalRingPartialChunkEvictionKeepsSuffix(t *testing.T) {
	ring := newTerminalRing(4)
	ring.Write([]byte("abcdef"))

	got := ring.Read(0, 100)
	if string(got.Data) != "cdef" {
		t.Fatalf("data = %q, want %q", got.Data, "cdef")
	}
	if got.TruncatedBytesDropped != 2 {
		t.Fatalf("dropped = %d, want 2", got.TruncatedBytesDropped)
	}
}

// Parity: tools/src/terminal_tools/common/ring_buffer.py::RingBuffer
func TestTerminalRingReadPastEndAndEOF(t *testing.T) {
	ring := newTerminalRing(64)
	ring.Write([]byte("abc"))

	empty := ring.Read(10, 10)
	if len(empty.Data) != 0 {
		t.Fatalf("read past end = %q, want empty", empty.Data)
	}
	if empty.NextOffset != 10 {
		t.Fatalf("next_offset = %d, want 10", empty.NextOffset)
	}
	if ring.EOF() {
		t.Fatal("ring reported eof before Close")
	}
	ring.Close()
	if !ring.EOF() {
		t.Fatal("ring did not report eof after Close")
	}
	last := ring.Read(0, 10)
	if string(last.Data) != "abc" {
		t.Fatalf("data after close = %q, want abc", last.Data)
	}
}

// Parity: tools/src/terminal_tools/common/ring_buffer.py::RingBuffer
func TestTerminalRingTailReturnsLastBytes(t *testing.T) {
	ring := newTerminalRing(1024)
	ring.Write([]byte("0123456789"))

	got := ring.Tail(4)
	if !bytes.Equal(got.Data, []byte("6789")) {
		t.Fatalf("tail = %q, want %q", got.Data, "6789")
	}
	if got.NextOffset != 10 {
		t.Fatalf("next_offset = %d, want 10", got.NextOffset)
	}
	all := ring.Tail(4096)
	if string(all.Data) != "0123456789" {
		t.Fatalf("full tail = %q, want %q", all.Data, "0123456789")
	}
}
