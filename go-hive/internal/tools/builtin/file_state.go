package builtin

// file_state.go ports aden_tools/file_state_cache.py: a bounded record of the
// files the model has read, so write_file/edit_file can refuse to overwrite
// content that changed on disk since that read.
//
// Upstream keeps one process-global cache keyed by (scope, absolute_path),
// where scope is the agent id (file_ops.py always passes None, so the practical
// scope is "__global__"). A Go Toolbox is already bound to one session root, so
// the cache lives on the Toolbox and the Toolbox is the scope: two sessions
// never share (or corrupt) each other's read-state view. The upstream LRU bound
// (_MAX_ENTRIES_PER_SCOPE = 256) is kept.
//
// Freshness is decided by (size, mtime_ns, sha256): matching metadata is trusted
// without hashing, and only a metadata mismatch pays for a content hash so
// editors that rewrite identical bytes do not read as stale.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
)

// maxFileStateEntries mirrors _MAX_ENTRIES_PER_SCOPE.
const maxFileStateEntries = 256

// fileReadRecord is file_state_cache.FileReadRecord.
type fileReadRecord struct {
	size    int64
	mtimeNs int64
	sha256  string
}

// freshness mirrors file_state_cache.Freshness.
type freshness int

const (
	freshnessUnread freshness = iota
	freshnessFresh
	freshnessStale
)

// fileStateCache is the per-Toolbox read-state cache. The zero value is not
// usable; newFileStateCache initialises the map.
type fileStateCache struct {
	mu      sync.Mutex
	entries map[string]fileReadRecord
	order   []string // touched absolute paths, least-recently-used first
}

func newFileStateCache() *fileStateCache {
	return &fileStateCache{entries: map[string]fileReadRecord{}}
}

// recordRead mirrors file_state_cache.record_read: stat the path, hash the
// bytes the caller just read, and remember the triple. Files that disappear
// between the read and this call are ignored (the concurrent-deletion race).
func (c *fileStateCache) recordRead(absPath string, content []byte) {
	info, err := os.Stat(absPath)
	if err != nil {
		return
	}
	c.remember(absPath, fileReadRecord{
		size:    info.Size(),
		mtimeNs: info.ModTime().UnixNano(),
		sha256:  hashBytes(content),
	})
}

// remember stores one record and evicts the least-recently-used entry once the
// scope grows past the bound.
func (c *fileStateCache) remember(absPath string, rec fileReadRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]fileReadRecord{}
	}
	c.entries[absPath] = rec
	c.touchLocked(absPath)
	for len(c.entries) > maxFileStateEntries && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if _, tracked := c.entries[oldest]; !tracked {
			continue
		}
		delete(c.entries, oldest)
	}
}

// touchLocked moves absPath to the most-recently-used end of order.
func (c *fileStateCache) touchLocked(absPath string) {
	for i, existing := range c.order {
		if existing == absPath {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, absPath)
}

// checkFresh mirrors file_state_cache.check_fresh: FRESH when the on-disk file
// still matches the recorded read, STALE when it was read before and has since
// changed, UNREAD when this scope never read it.
func (c *fileStateCache) checkFresh(absPath string) (freshness, string) {
	c.mu.Lock()
	rec, ok := c.entries[absPath]
	if ok {
		c.touchLocked(absPath)
	}
	c.mu.Unlock()
	if !ok {
		return freshnessUnread, ""
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return freshnessStale, "file has been deleted since it was read"
		}
		return freshnessStale, fmt.Sprintf("stat failed: %v", err)
	}
	if info.Size() == rec.size && info.ModTime().UnixNano() == rec.mtimeNs {
		return freshnessFresh, ""
	}

	// Metadata moved: hash the current bytes before calling it stale, so an
	// editor rewriting identical content does not trip the guard.
	sum, err := hashFile(absPath)
	if err != nil {
		return freshnessStale, fmt.Sprintf("hash failed: %v", err)
	}
	if sum == rec.sha256 {
		c.remember(absPath, fileReadRecord{
			size:    info.Size(),
			mtimeNs: info.ModTime().UnixNano(),
			sha256:  sum,
		})
		return freshnessFresh, ""
	}
	return freshnessStale, "content changed on disk since the last read (sha256 differs)"
}

// forget drops one entry; upstream exposes it as a test hook.
func (c *fileStateCache) forget(absPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, absPath)
	for i, existing := range c.order {
		if existing == absPath {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

// len reports the number of tracked paths (test hook for the LRU bound).
func (c *fileStateCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// hashBytes is _hash_bytes.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashFile is _hash_file: sha256 over the file, streamed in chunks.
func hashFile(absPath string) (string, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
