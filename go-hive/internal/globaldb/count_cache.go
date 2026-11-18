package globaldb

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type TableCount struct {
	Name  string
	Count int
}

type CountsFetcher func() ([]TableCount, error)

type CountSnapshot struct {
	Tables     []TableCount
	UpdatedAt  time.Time
	AgeSeconds float64
}

type TableCountCache struct {
	mu      sync.Mutex
	fetcher CountsFetcher
	ttl     time.Duration

	counts  []TableCount
	hasData bool
	mono    time.Time
	wall    time.Time
	dirty   bool
}

func NewTableCountCache(fetcher CountsFetcher, ttlSeconds float64) *TableCountCache {
	return &TableCountCache{
		fetcher: fetcher,
		ttl:     time.Duration(ttlSeconds * float64(time.Second)),
		dirty:   true,
	}
}

func (c *TableCountCache) MarkDirty() {
	c.mu.Lock()
	c.dirty = true
	c.mu.Unlock()
}

func (c *TableCountCache) Record(counts []TableCount) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = append([]TableCount(nil), counts...)
	c.hasData = true
	c.stampLocked()
}

func (c *TableCountCache) stampLocked() {
	c.mono = time.Now()
	c.wall = time.Now().UTC()
	c.dirty = false
}

func (c *TableCountCache) freshLocked() bool {
	return c.hasData && !c.dirty && time.Since(c.mono) <= c.ttl
}

func (c *TableCountCache) Snapshot(force bool) *CountSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if force || !c.freshLocked() {
		if force || !c.freshLocked() {
			counts, err := c.fetcher()
			if err != nil {
				if !c.hasData {
					return nil
				}
			} else {
				c.counts = append([]TableCount(nil), counts...)
				c.hasData = true
				c.stampLocked()
			}
		}
	}
	if !c.hasData {
		return nil
	}
	age := time.Since(c.mono).Seconds()
	if age < 0 {
		age = 0
	}
	return &CountSnapshot{
		Tables:     append([]TableCount(nil), c.counts...),
		UpdatedAt:  c.wall,
		AgeSeconds: age,
	}
}

func FetchGlobalCounts() ([]TableCount, error) {
	client, err := NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	resp, err := client.ListTables(context.Background())
	if err != nil {
		return nil, err
	}
	out := []TableCount{}
	body, ok := resp.(map[string]any)
	if !ok {
		return out, nil
	}
	tables, _ := body["tables"].([]any)
	for _, raw := range tables {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := t["name"].(string)
		if name == "" {
			continue
		}
		out = append(out, TableCount{Name: name, Count: intOrZero(t["row_count"])})
	}
	return out, nil
}

var GlobalCountCache = NewTableCountCache(FetchGlobalCounts, 60.0)

var globalUsed atomic.Bool

func NoteGlobalUsed() { globalUsed.Store(true) }

func GlobalWasUsed() bool { return globalUsed.Load() }

func RecordGlobalTables(tables []map[string]any) {
	counts := make([]TableCount, 0, len(tables))
	for _, t := range tables {
		name, _ := t["name"].(string)
		if name == "" {
			continue
		}
		counts = append(counts, TableCount{Name: name, Count: intOrZero(t["row_count"])})
	}
	GlobalCountCache.Record(counts)
}

func intOrZero(v any) int {
	switch n := v.(type) {
	case nil:
		return 0
	case bool:
		if n {
			return 1
		}
		return 0
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		if parsed, err := strconv.Atoi(n); err == nil {
			return parsed
		}
		return 0
	default:
		return 0
	}
}
