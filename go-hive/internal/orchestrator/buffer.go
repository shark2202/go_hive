package orchestrator

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

type DataBufferWriteError struct {
	Key    string
	Length int
}

func (e *DataBufferWriteError) Error() string {
	return fmt.Sprintf(
		"Rejected suspicious content for key '%s': appears to be hallucinated code (%d chars). "+
			"If this is intentional, use validate=False.",
		e.Key, e.Length,
	)
}

type PermissionError struct {
	Message string
}

func (e *PermissionError) Error() string { return e.Message }

type bufferState struct {
	mu       sync.RWMutex
	keyMu    sync.Mutex
	data     map[string]any
	keyLocks map[string]*sync.Mutex
}

type DataBuffer struct {
	initOnce     sync.Once
	state        *bufferState
	allowedRead  map[string]struct{}
	allowedWrite map[string]struct{}
}

func NewDataBuffer() *DataBuffer {
	b := &DataBuffer{}
	b.ensure()
	return b
}

func (b *DataBuffer) ensure() *bufferState {
	b.initOnce.Do(func() {
		if b.state == nil {
			b.state = &bufferState{
				data:     map[string]any{},
				keyLocks: map[string]*sync.Mutex{},
			}
		}
	})
	return b.state
}

func (b *DataBuffer) canRead(key string) bool {
	return len(b.allowedRead) == 0 || hasKey(b.allowedRead, key)
}

func (b *DataBuffer) canWrite(key string) bool {
	return len(b.allowedWrite) == 0 || hasKey(b.allowedWrite, key)
}

func (b *DataBuffer) Read(key string) (any, error) {
	if !b.canRead(key) {
		return nil, &PermissionError{Message: "Node not allowed to read key: " + key}
	}
	s := b.ensure()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data[key], nil
}

func (b *DataBuffer) Write(key string, value any, validate ...bool) error {
	if !b.canWrite(key) {
		return &PermissionError{Message: "Node not allowed to write key: " + key}
	}
	if err := screenWrite(key, value, validate); err != nil {
		return err
	}
	s := b.ensure()
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
	return nil
}

func (b *DataBuffer) WriteAsync(key string, value any, validate ...bool) error {
	if !b.canWrite(key) {
		return &PermissionError{Message: "Node not allowed to write key: " + key}
	}
	s := b.ensure()
	lock := s.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	if err := screenWrite(key, value, validate); err != nil {
		return err
	}
	s.mu.Lock()
	s.data[key] = value
	s.mu.Unlock()
	return nil
}

func (s *bufferState) lockFor(key string) *sync.Mutex {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	lock := s.keyLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		s.keyLocks[key] = lock
	}
	return lock
}

func screenWrite(key string, value any, validate []bool) error {
	if len(validate) > 0 && !validate[0] {
		return nil
	}
	str, ok := value.(string)
	if !ok {
		return nil
	}
	length := utf8.RuneCountInString(str)
	if length <= 5000 {
		return nil
	}
	if !containsCodeIndicator(str, dataBufferCodeIndicators) {
		return nil
	}
	return &DataBufferWriteError{Key: key, Length: length}
}

func (b *DataBuffer) ReadAll() map[string]any {
	s := b.ensure()
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(b.allowedRead) == 0 {
		out := make(map[string]any, len(s.data))
		for k, v := range s.data {
			out[k] = v
		}
		return out
	}
	out := make(map[string]any, len(b.allowedRead))
	for k, v := range s.data {
		if hasKey(b.allowedRead, k) {
			out[k] = v
		}
	}
	return out
}

func (b *DataBuffer) WithPermissions(readKeys, writeKeys []string) *DataBuffer {
	return &DataBuffer{
		state:        b.ensure(),
		allowedRead:  keySet(readKeys),
		allowedWrite: keySet(writeKeys),
	}
}

var dataBufferCodeIndicators = []string{
	"```python",
	"def ",
	"class ",
	"import ",
	"async def ",
	"from ",
	"function ",
	"const ",
	"let ",
	"=> {",
	"require(",
	"export ",
	"SELECT ",
	"INSERT ",
	"UPDATE ",
	"DELETE ",
	"DROP ",
	"<script",
	"<?php",
	"<%",
}

func containsCodeIndicator(value string, indicators []string) bool {
	runes := []rune(value)
	n := len(runes)
	if n < 10000 {
		return anyIndicatorIn(value, indicators)
	}
	positions := []int{
		0,
		n / 4,
		n / 2,
		3 * n / 4,
		maxInt(0, n-2000),
	}
	for _, pos := range positions {
		end := pos + 2000
		if end > n {
			end = n
		}
		if anyIndicatorIn(string(runes[pos:end]), indicators) {
			return true
		}
	}
	return false
}

func anyIndicatorIn(text string, indicators []string) bool {
	for _, indicator := range indicators {
		if indicator != "" && strings.Contains(text, indicator) {
			return true
		}
	}
	return false
}

func keySet(keys []string) map[string]struct{} {
	if len(keys) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}

func hasKey(set map[string]struct{}, key string) bool {
	_, ok := set[key]
	return ok
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
