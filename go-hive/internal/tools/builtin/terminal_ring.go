package builtin

import "sync"

const (
	// terminalRingJobBytes is the per-stream capacity of a job's ring buffer.
	terminalRingJobBytes = 4 * 1024 * 1024
	// terminalRingPTYBytes is the capacity of a PTY session's ring buffer.
	terminalRingPTYBytes = 2 * 1024 * 1024
)

// terminalRingRead is one slice of a ring: the bytes, the absolute offset they
// start at, where the next read should resume, and how many bytes the writer
// had already evicted below the requested offset.
type terminalRingRead struct {
	Data                  []byte
	Offset                int
	NextOffset            int
	TruncatedBytesDropped int
}

// terminalRing is a capacity-bounded byte ring with absolute monotonic
// offsets, so a reader that falls behind learns how much it lost instead of
// silently missing output. Safe for one writer and many readers.
type terminalRing struct {
	mu       sync.Mutex
	capacity int
	chunks   [][]byte
	buffered int
	floor    int
	total    int
	eof      bool
}

func newTerminalRing(capacity int) *terminalRing {
	if capacity <= 0 {
		capacity = terminalRingJobBytes
	}
	return &terminalRing{capacity: capacity}
}

// Write appends a copy of p and evicts the oldest bytes past capacity.
func (r *terminalRing) Write(p []byte) {
	if len(p) == 0 {
		return
	}
	chunk := make([]byte, len(p))
	copy(chunk, p)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks = append(r.chunks, chunk)
	r.buffered += len(chunk)
	r.total += len(chunk)
	for r.buffered > r.capacity && len(r.chunks) > 0 {
		overshoot := r.buffered - r.capacity
		head := r.chunks[0]
		if len(head) <= overshoot {
			r.chunks = r.chunks[1:]
			r.buffered -= len(head)
			r.floor += len(head)
			continue
		}
		r.chunks[0] = head[overshoot:]
		r.buffered -= overshoot
		r.floor += overshoot
	}
}

// Close marks the stream ended; reads catch up to the last write, then eof.
func (r *terminalRing) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eof = true
}

func (r *terminalRing) TotalWritten() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

func (r *terminalRing) Floor() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.floor
}

func (r *terminalRing) EOF() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.eof
}

// Read returns up to maxBytes starting at sinceOffset. Offsets below the
// buffer floor are reported in TruncatedBytesDropped and reading starts at the
// floor; offsets past the last write return empty data with NextOffset pinned
// to the request so the caller can poll again.
func (r *terminalRing) Read(sinceOffset, maxBytes int) terminalRingRead {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if sinceOffset < 0 {
		sinceOffset = 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	since := sinceOffset
	dropped := 0
	if since < r.floor {
		dropped = r.floor - since
		since = r.floor
	}
	available := r.total - since
	if available <= 0 || maxBytes == 0 {
		return terminalRingRead{Data: nil, Offset: since, NextOffset: since, TruncatedBytesDropped: dropped}
	}
	toTake := available
	if toTake > maxBytes {
		toTake = maxBytes
	}
	out := make([]byte, 0, toTake)
	cursor := r.floor
	remaining := toTake
	for _, chunk := range r.chunks {
		chunkEnd := cursor + len(chunk)
		if chunkEnd <= since {
			cursor = chunkEnd
			continue
		}
		start := 0
		if since > cursor {
			start = since - cursor
		}
		end := len(chunk)
		if start+remaining < end {
			end = start + remaining
		}
		out = append(out, chunk[start:end]...)
		remaining -= end - start
		cursor = chunkEnd
		if remaining <= 0 {
			break
		}
	}
	return terminalRingRead{Data: out, Offset: since, NextOffset: since + len(out), TruncatedBytesDropped: dropped}
}

// Tail returns the last maxBytes of buffered output.
func (r *terminalRing) Tail(maxBytes int) terminalRingRead {
	if maxBytes < 0 {
		maxBytes = 0
	}
	r.mu.Lock()
	start := r.floor
	if r.total-maxBytes > start {
		start = r.total - maxBytes
	}
	r.mu.Unlock()
	return r.Read(start, maxBytes)
}
