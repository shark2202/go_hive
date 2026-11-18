package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	terminalOutputTTL          = 300 * time.Second
	terminalOutputStoreCap     = 64 * 1024 * 1024
	terminalOutputDefaultKB    = 64
	terminalOutputHandlePrefix = "out_"
)

// terminalOutputSlice is the paginated view terminal_output_get returns. Field
// order mirrors the Python dict so the reported JSON matches byte for byte.
type terminalOutputSlice struct {
	Data       string `json:"data"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	EOF        bool   `json:"eof"`
	Expired    bool   `json:"expired"`
}

type terminalOutputEntry struct {
	data       []byte
	createdAt  time.Time
	lastAccess time.Time
}

// terminalOutputStore keeps truncated output under a short-lived handle so the
// model can page through it, bounded by a TTL and a total byte cap with LRU
// eviction.
type terminalOutputStore struct {
	mu       sync.Mutex
	ttl      time.Duration
	capBytes int
	entries  map[string]*terminalOutputEntry
	total    int
	now      func() time.Time
}

func newTerminalOutputStore(ttl time.Duration, capBytes int) *terminalOutputStore {
	if ttl <= 0 {
		ttl = terminalOutputTTL
	}
	if capBytes <= 0 {
		capBytes = terminalOutputStoreCap
	}
	return &terminalOutputStore{
		ttl:      ttl,
		capBytes: capBytes,
		entries:  make(map[string]*terminalOutputEntry),
		now:      time.Now,
	}
}

var terminalOutputs = newTerminalOutputStore(terminalOutputTTL, terminalOutputStoreCap)

// Put stores data and returns a fresh handle. Empty payloads get no handle.
func (s *terminalOutputStore) Put(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	handle := terminalOutputHandlePrefix + hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.evictExpiredLocked(now)
	for s.total+len(data) > s.capBytes && len(s.entries) > 0 {
		s.evictLRULocked()
	}
	s.entries[handle] = &terminalOutputEntry{data: data, createdAt: now, lastAccess: now}
	s.total += len(data)
	return handle
}

// Get returns a slice of the stored data, or expired=true when the handle is
// unknown or its TTL lapsed.
func (s *terminalOutputStore) Get(handle string, sinceOffset, maxBytes int) terminalOutputSlice {
	now := s.now()
	s.mu.Lock()
	s.evictExpiredLocked(now)
	entry, ok := s.entries[handle]
	if !ok {
		s.mu.Unlock()
		return terminalOutputSlice{Offset: sinceOffset, NextOffset: sinceOffset, EOF: true, Expired: true}
	}
	entry.lastAccess = now
	buf := entry.data
	s.mu.Unlock()

	since := sinceOffset
	if since < 0 {
		since = 0
	}
	end := since
	if maxBytes > 0 {
		end = since + maxBytes
	}
	if end > len(buf) {
		end = len(buf)
	}
	var data []byte
	if since < len(buf) && end > since {
		data = buf[since:end]
	}
	return terminalOutputSlice{
		Data:       string(data),
		Offset:     since,
		NextOffset: end,
		EOF:        end >= len(buf),
		Expired:    false,
	}
}

func (s *terminalOutputStore) evictExpiredLocked(now time.Time) {
	for handle, entry := range s.entries {
		if now.Sub(entry.createdAt) > s.ttl {
			s.total -= len(entry.data)
			delete(s.entries, handle)
		}
	}
}

func (s *terminalOutputStore) evictLRULocked() {
	var oldest string
	var oldestAt time.Time
	for handle, entry := range s.entries {
		if oldest == "" || entry.lastAccess.Before(oldestAt) {
			oldest = handle
			oldestAt = entry.lastAccess
		}
	}
	if oldest == "" {
		return
	}
	s.total -= len(s.entries[oldest].data)
	delete(s.entries, oldest)
}

// terminalOutputGet implements terminal_output_get: paginate a truncated
// output handle minted by a terminal tool.
func (t *Toolbox) terminalOutputGet(ctx context.Context, input map[string]any) (Result, error) {
	handle, ok := stringArg(input, "output_handle")
	if !ok || handle == "" {
		return terminalErr("terminal_output_get: output_handle is required"), nil
	}
	sinceOffset := 0
	if v, present := input["since_offset"]; present && v != nil {
		parsed, ok := intArg(input, "since_offset")
		if !ok {
			return terminalErr("terminal_output_get: since_offset must be an integer"), nil
		}
		sinceOffset = parsed
	}
	if sinceOffset < 0 {
		sinceOffset = 0
	}
	maxKB := terminalOutputDefaultKB
	if v, present := input["max_kb"]; present && v != nil {
		parsed, ok := intArg(input, "max_kb")
		if !ok {
			return terminalErr("terminal_output_get: max_kb must be an integer"), nil
		}
		maxKB = parsed
	}
	maxBytes := maxKB * 1024
	if maxBytes < 0 {
		maxBytes = 0
	}
	res := terminalOutputs.Get(handle, sinceOffset, maxBytes)
	return terminalEncode(res), nil
}
