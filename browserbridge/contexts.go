package browserbridge

import (
	"sort"
	"sync"
	"time"
)

// ContextMeta is one upstream-style profile→tab-group registry entry.
//
// GroupID is 0 when the entry is soft-pruned: Chrome destroyed the group
// (last tab closed / dragged out) but the agent identity is kept so a later
// adopt_tab can lazily mint a fresh group under the same display name.
type ContextMeta struct {
	Profile        string `json:"profile"`
	GroupID        int    `json:"groupId"`
	Name           string `json:"name,omitempty"`
	BrowserProfile string `json:"browser_profile"`
	RegisteredAtMS int64  `json:"registered_at_ms"`
}

// ContextStore is the profile→tab-group registry contract.
type ContextStore interface {
	Upsert(meta ContextMeta)
	Get(profile string) (ContextMeta, bool)
	ByGroup(groupID int) (ContextMeta, bool)
	Delete(profile string)
	List() []ContextMeta
	// SetGroup rebinds a profile to a freshly minted group (lazy re-create).
	SetGroup(profile string, groupID int)
	// PruneGroup detaches every entry pointing at groupID. soft keeps the
	// agent identity (name + browser profile) and clears GroupID; hard deletes
	// the entry. Returns the touched profile names.
	PruneGroup(groupID int, soft bool) []string
}

// MemoryContextStore is the default process-local implementation.
type MemoryContextStore struct {
	mu     sync.RWMutex
	byName map[string]ContextMeta
}

// NewMemoryContextStore creates an empty registry.
func NewMemoryContextStore() *MemoryContextStore {
	return &MemoryContextStore{byName: make(map[string]ContextMeta)}
}

func (s *MemoryContextStore) Upsert(meta ContextMeta) {
	if meta.RegisteredAtMS == 0 {
		meta.RegisteredAtMS = time.Now().UnixMilli()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byName[meta.Profile] = meta
}

func (s *MemoryContextStore) Get(profile string) (ContextMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.byName[profile]
	return m, ok
}

func (s *MemoryContextStore) ByGroup(groupID int) (ContextMeta, bool) {
	if groupID <= 0 {
		return ContextMeta{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.byName {
		if m.GroupID == groupID {
			return m, true
		}
	}
	return ContextMeta{}, false
}

func (s *MemoryContextStore) Delete(profile string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byName, profile)
}

func (s *MemoryContextStore) List() []ContextMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ContextMeta, 0, len(s.byName))
	for _, m := range s.byName {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Profile < out[j].Profile })
	return out
}

func (s *MemoryContextStore) SetGroup(profile string, groupID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byName[profile]
	if !ok {
		return
	}
	m.GroupID = groupID
	m.RegisteredAtMS = time.Now().UnixMilli()
	s.byName[profile] = m
}

func (s *MemoryContextStore) PruneGroup(groupID int, soft bool) []string {
	if groupID <= 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	var touched []string
	for profile, m := range s.byName {
		if m.GroupID != groupID {
			continue
		}
		touched = append(touched, profile)
		if soft {
			m.GroupID = 0
			m.RegisteredAtMS = now
			s.byName[profile] = m
			continue
		}
		delete(s.byName, profile)
	}
	sort.Strings(touched)
	return touched
}

// Contexts exposes the registry.
func (b *Bridge) Contexts() ContextStore { return b.contexts }
