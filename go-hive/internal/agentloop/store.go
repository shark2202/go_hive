package agentloop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type ConversationStore interface {
	WritePart(seq int, data map[string]any) error
	ReadParts() ([]map[string]any, error)
	WriteMeta(data map[string]any) error
	ReadMeta() (map[string]any, error)
	WriteCursor(data map[string]any) error
	ReadCursor() (map[string]any, error)
	DeletePartsBefore(seq int) error
	WritePartial(seq int, data map[string]any) error
	ReadPartial(seq int) (map[string]any, error)
	ReadAllPartials() ([]map[string]any, error)
	ClearPartial(seq int) error
	Close() error
	Destroy() error
}

// CursorMutator is the atomic read-modify-write seam for the conversation
// cursor. Every writer that adds or replaces a cursor field must use it: a
// ReadCursor/WriteCursor pair is two steps, and a concurrent writer (a message
// persist bumping next_seq, a park checkpoint stamping pending_input) that
// lands in between is silently overwritten by the older snapshot.
type CursorMutator interface {
	MutateCursor(fn func(cursor map[string]any)) error
}

type FileConversationStore struct {
	base        string
	partsDir    string
	partialsDir string
}

func NewFileConversationStore(base string) *FileConversationStore {
	return &FileConversationStore{
		base:        base,
		partsDir:    filepath.Join(base, "parts"),
		partialsDir: filepath.Join(base, "partials"),
	}
}

func writeJSONAtomic(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func readJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil
	}
	return parsed, nil
}

func partFileName(seq int) string {
	return formatSeq(seq) + ".json"
}

func formatSeq(seq int) string {
	digits := strconv.Itoa(seq)
	for len(digits) < 10 {
		digits = "0" + digits
	}
	return digits
}

func readJSONDir(dir string) ([]map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []map[string]any{}, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	results := make([]map[string]any, 0, len(names))
	for _, name := range names {
		parsed, err := readJSON(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if parsed != nil {
			results = append(results, parsed)
		}
	}
	return results, nil
}

func (s *FileConversationStore) WritePart(seq int, data map[string]any) error {
	return writeJSONAtomic(filepath.Join(s.partsDir, partFileName(seq)), data)
}

func (s *FileConversationStore) ReadParts() ([]map[string]any, error) {
	return readJSONDir(s.partsDir)
}

func (s *FileConversationStore) WriteMeta(data map[string]any) error {
	return writeJSONAtomic(filepath.Join(s.base, "meta.json"), data)
}

func (s *FileConversationStore) ReadMeta() (map[string]any, error) {
	return readJSON(filepath.Join(s.base, "meta.json"))
}

func (s *FileConversationStore) WriteCursor(data map[string]any) error {
	return writeJSONAtomic(filepath.Join(s.base, "cursor.json"), data)
}

func (s *FileConversationStore) ReadCursor() (map[string]any, error) {
	return readJSON(filepath.Join(s.base, "cursor.json"))
}

// writeCursorNextSeq advances the cursor's next_seq without disturbing fields
// another writer owns (the park checkpoint's pending_input). The update is one
// atomic read-modify-write when the store supports it; a ReadCursor/WriteCursor
// pair can land on either side of a concurrent park checkpoint and then carry
// that writer's field away with the older snapshot.
func writeCursorNextSeq(store ConversationStore, nextSeq int) error {
	if store == nil {
		return nil
	}
	if mutator, ok := store.(CursorMutator); ok {
		return mutator.MutateCursor(func(cursor map[string]any) {
			cursor["next_seq"] = nextSeq
		})
	}
	cursor, err := store.ReadCursor()
	if err != nil {
		return err
	}
	updated := UpdateCursorNextSeq(cursor, nextSeq)
	return store.WriteCursor(updated)
}

// cursorDirLocks serialises cursor read-modify-writes per conversation
// directory. The key is the directory, not the store instance: a route may open
// a second store over the same conversation and must share the same lock, or
// the two instances still lose each other's updates.
var cursorDirLocks sync.Map // map[string]*sync.Mutex

func cursorDirLock(base string) *sync.Mutex {
	key := filepath.Clean(base)
	if lock, ok := cursorDirLocks.Load(key); ok {
		return lock.(*sync.Mutex)
	}
	lock := &sync.Mutex{}
	actual, _ := cursorDirLocks.LoadOrStore(key, lock)
	return actual.(*sync.Mutex)
}

// MutateCursor applies fn to the cursor as one atomic read-modify-write. Callers
// that preserve fields they do not own must use it instead of a
// ReadCursor/WriteCursor pair: the pair's snapshot goes stale while another
// writer's update lands in between.
func (s *FileConversationStore) MutateCursor(fn func(cursor map[string]any)) error {
	if fn == nil {
		return nil
	}
	lock := cursorDirLock(s.base)
	lock.Lock()
	defer lock.Unlock()
	cursor, err := readJSON(filepath.Join(s.base, "cursor.json"))
	if err != nil {
		return err
	}
	if cursor == nil {
		cursor = map[string]any{}
	}
	fn(cursor)
	return writeJSONAtomic(filepath.Join(s.base, "cursor.json"), cursor)
}

func (s *FileConversationStore) WritePartial(seq int, data map[string]any) error {
	return writeJSONAtomic(filepath.Join(s.partialsDir, partFileName(seq)), data)
}

func (s *FileConversationStore) ReadPartial(seq int) (map[string]any, error) {
	return readJSON(filepath.Join(s.partialsDir, partFileName(seq)))
}

func (s *FileConversationStore) ReadAllPartials() ([]map[string]any, error) {
	return readJSONDir(s.partialsDir)
}

func (s *FileConversationStore) ClearPartial(seq int) error {
	err := os.Remove(filepath.Join(s.partialsDir, partFileName(seq)))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *FileConversationStore) DeletePartsBefore(seq int) error {
	entries, err := os.ReadDir(s.partsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fileSeq, err := strconv.Atoi(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		if fileSeq < seq {
			if err := os.Remove(filepath.Join(s.partsDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *FileConversationStore) Close() error { return nil }

func (s *FileConversationStore) Clear() error {
	for _, dir := range []string{s.partsDir, s.partialsDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"cursor.json", "meta.json"} {
		err := os.Remove(filepath.Join(s.base, name))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *FileConversationStore) Destroy() error {
	return os.RemoveAll(s.base)
}

type MemoryConversationStore struct {
	mu        sync.Mutex
	parts     map[int]map[string]any
	partials  map[int]map[string]any
	meta      map[string]any
	cursor    map[string]any
	destroyed bool
}

func NewMemoryConversationStore() *MemoryConversationStore {
	return &MemoryConversationStore{
		parts:    map[int]map[string]any{},
		partials: map[int]map[string]any{},
	}
}

func (s *MemoryConversationStore) WritePart(seq int, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts[seq] = copyMap(data)
	return nil
}

func (s *MemoryConversationStore) ReadParts() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seqs := make([]int, 0, len(s.parts))
	for seq := range s.parts {
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)
	out := make([]map[string]any, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, copyMap(s.parts[seq]))
	}
	return out, nil
}

func (s *MemoryConversationStore) WriteMeta(data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = copyMap(data)
	return nil
}

func (s *MemoryConversationStore) ReadMeta() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta == nil {
		return nil, nil
	}
	return copyMap(s.meta), nil
}

func (s *MemoryConversationStore) WriteCursor(data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = copyMap(data)
	return nil
}

func (s *MemoryConversationStore) ReadCursor() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursor == nil {
		return nil, nil
	}
	return copyMap(s.cursor), nil
}

// MutateCursor applies fn under the store lock, so an in-memory cursor gets the
// same atomic update seam as the file store.
func (s *MemoryConversationStore) MutateCursor(fn func(cursor map[string]any)) error {
	if fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor := copyMap(s.cursor)
	fn(cursor)
	s.cursor = cursor
	return nil
}

func (s *MemoryConversationStore) DeletePartsBefore(seq int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for partSeq := range s.parts {
		if partSeq < seq {
			delete(s.parts, partSeq)
		}
	}
	return nil
}

func (s *MemoryConversationStore) WritePartial(seq int, data map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partials[seq] = copyMap(data)
	return nil
}

func (s *MemoryConversationStore) ReadPartial(seq int) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if data, ok := s.partials[seq]; ok {
		return copyMap(data), nil
	}
	return nil, nil
}

func (s *MemoryConversationStore) ReadAllPartials() ([]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seqs := make([]int, 0, len(s.partials))
	for seq := range s.partials {
		seqs = append(seqs, seq)
	}
	sort.Ints(seqs)
	out := make([]map[string]any, 0, len(seqs))
	for _, seq := range seqs {
		out = append(out, copyMap(s.partials[seq]))
	}
	return out, nil
}

func (s *MemoryConversationStore) ClearPartial(seq int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.partials, seq)
	return nil
}

func (s *MemoryConversationStore) Close() error { return nil }

func (s *MemoryConversationStore) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts = map[int]map[string]any{}
	s.partials = map[int]map[string]any{}
	s.meta = nil
	s.cursor = nil
	return nil
}

func (s *MemoryConversationStore) Destroy() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.destroyed = true
	s.parts = map[int]map[string]any{}
	s.partials = map[int]map[string]any{}
	s.meta = nil
	s.cursor = nil
	return nil
}
