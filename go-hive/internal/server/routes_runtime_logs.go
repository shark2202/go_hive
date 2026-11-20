package server

// Parity: framework/server/routes_logs.py::handle_runtime_logs
//
// GET /api/runtime-logs feeds the dashboard's Runtime logs drawer. The Go
// server keeps the last lines in a bounded in-memory ring; the handler never
// touches the filesystem, so no query value can become a path. `after` is a
// cursor, not an offset: a boot id that does not match this process starts
// the reader at zero because the old sequence numbers no longer apply.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	// RuntimeLogCapacity is the number of lines kept for the drawer.
	RuntimeLogCapacity = 500
	// maxRuntimeLogTextRunes caps one line so a single huge log record cannot
	// force an unbounded JSON response.
	maxRuntimeLogTextRunes = 4000
	// maxRuntimeLogPayloadBytes is the whole-response budget. Older entries
	// are dropped first; the newest lines are the ones a reader needs.
	maxRuntimeLogPayloadBytes = 512 << 10
)

var ansiEscapePattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

type RuntimeLogEntry struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// RuntimeLogBuffer is a process-local ring of recent log lines. It implements
// io.Writer so a host can tee its logger into it:
//
//	log.SetOutput(io.MultiWriter(os.Stderr, server.DefaultRuntimeLogBuffer()))
type RuntimeLogBuffer struct {
	mu           sync.Mutex
	boot         string
	capacity     int
	maxTextRunes int
	entries      []RuntimeLogEntry
	nextSeq      int64
}

func NewRuntimeLogBuffer(boot string) *RuntimeLogBuffer {
	if strings.TrimSpace(boot) == "" {
		boot = newRuntimeBootID()
	}
	return &RuntimeLogBuffer{boot: boot, capacity: RuntimeLogCapacity, maxTextRunes: maxRuntimeLogTextRunes}
}

// DefaultRuntimeLogBuffer is the process-wide sink. The route uses it when no
// buffer is supplied; a host wires its logger to the same instance.
var defaultRuntimeLogBuffer = NewRuntimeLogBuffer("")

func DefaultRuntimeLogBuffer() *RuntimeLogBuffer {
	return defaultRuntimeLogBuffer
}

func newRuntimeBootID() string {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(raw[:])
}

func (b *RuntimeLogBuffer) Boot() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.boot
}

// Append records one line. stdout/stderr is a label only; it never selects a
// file or stream.
func (b *RuntimeLogBuffer) Append(stream, text string) {
	if b == nil {
		return
	}
	stream = strings.TrimSpace(stream)
	if stream != "stderr" {
		stream = "stdout"
	}
	text = strings.TrimRight(text, "\r\n")
	text = ansiEscapePattern.ReplaceAllString(text, "")
	limit := b.maxTextRunes
	if limit <= 0 {
		limit = maxRuntimeLogTextRunes
	}
	if runes := []rune(text); len(runes) > limit {
		text = string(runes[:limit])
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	capacity := b.capacity
	if capacity <= 0 {
		capacity = RuntimeLogCapacity
	}
	b.nextSeq++
	b.entries = append(b.entries, RuntimeLogEntry{Seq: b.nextSeq, Stream: stream, Text: text})
	if len(b.entries) > capacity {
		b.entries = append([]RuntimeLogEntry(nil), b.entries[len(b.entries)-capacity:]...)
	}
}

// Write records every complete line in p as a stdout line.
func (b *RuntimeLogBuffer) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line != "" {
			b.Append("stdout", line)
		}
	}
	return len(p), nil
}

// WriteStderr records every complete line in p as a stderr line.
func (b *RuntimeLogBuffer) WriteStderr(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if line != "" {
			b.Append("stderr", line)
		}
	}
	return len(p), nil
}

// Since returns the entries after a cursor, oldest first. The returned slice
// is non-nil so an empty payload marshals as [] rather than null.
func (b *RuntimeLogBuffer) Since(after int64) []RuntimeLogEntry {
	entries := make([]RuntimeLogEntry, 0)
	if b == nil {
		return entries
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.entries {
		if entry.Seq > after {
			entries = append(entries, entry)
		}
	}
	return entries
}

type RuntimeLogRouteDeps struct {
	// Buffer is the ring the host tees its logger into. Nil uses the
	// process-wide DefaultRuntimeLogBuffer.
	Buffer *RuntimeLogBuffer
}

type RuntimeLogRoutes struct {
	deps RuntimeLogRouteDeps
}

// RegisterRuntimeLogRoutes mounts GET /api/runtime-logs.
func RegisterRuntimeLogRoutes(mux *http.ServeMux, deps RuntimeLogRouteDeps) {
	routes := &RuntimeLogRoutes{deps: deps}
	mux.HandleFunc("GET /api/runtime-logs", routes.handleRuntimeLogs)
}

func (rt *RuntimeLogRoutes) buffer() *RuntimeLogBuffer {
	if rt != nil && rt.deps.Buffer != nil {
		return rt.deps.Buffer
	}
	return defaultRuntimeLogBuffer
}

// handleRuntimeLogs mirrors handle_runtime_logs. Invalid `after` values fall
// back to 0; a stale or missing `boot` replays the whole buffer.
//
// Parity: framework/server/routes_logs.py::handle_runtime_logs
func (rt *RuntimeLogRoutes) handleRuntimeLogs(w http.ResponseWriter, r *http.Request) {
	buffer := rt.buffer()
	query := r.URL.Query()

	after := int64(0)
	if raw := query.Get("after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		switch {
		case err == nil && parsed > 0:
			after = parsed
		case errors.Is(err, strconv.ErrRange):
			// A cursor beyond int64 is still a valid Python int upstream;
			// it simply means "nothing newer than that".
			after = math.MaxInt64
		}
	}
	if query.Get("boot") != buffer.Boot() {
		after = 0
	}

	entries := trimRuntimeLogPayload(buffer.Since(after))
	writeJSON(w, http.StatusOK, map[string]any{
		"boot":    buffer.Boot(),
		"entries": entries,
	})
}

// trimRuntimeLogPayload keeps the newest entries under the byte budget. It
// never mutates the ring, so a bounded response does not cost the reader the
// lines still in the buffer.
func trimRuntimeLogPayload(entries []RuntimeLogEntry) []RuntimeLogEntry {
	if len(entries) == 0 {
		return []RuntimeLogEntry{}
	}
	total := 0
	start := len(entries)
	for start > 0 {
		size := len(entries[start-1].Text) + 64
		if total+size > maxRuntimeLogPayloadBytes {
			break
		}
		total += size
		start--
	}
	out := make([]RuntimeLogEntry, len(entries)-start)
	copy(out, entries[start:])
	return out
}
