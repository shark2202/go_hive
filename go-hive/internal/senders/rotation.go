package senders

// Rotation cursors. The cursor is a fairness heuristic, not a safety rail, so
// it is per-device by design (upstream rotation.py keeps the same split: cloud
// usage governs the cap, local cursors govern whose turn it is). Daily usage
// comes from the send log, not from a second counter that can drift.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

const cursorsFile = "usage.json"

// Rotation policies accepted by pick_sender / send_campaign.
const (
	PolicyRoundRobin = "round_robin"
	PolicyWeighted   = "weighted"
	PolicyLeastUsed  = "least_used"
)

type cursorFileState struct {
	Version int            `json:"version"`
	Date    string         `json:"date,omitempty"`
	Cursors map[string]int `json:"cursors"`
}

// CursorStore persists rotation cursors in $HIVE_HOME/senders/usage.json.
type CursorStore struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// OpenCursorStore binds the cursor file to the senders directory.
func OpenCursorStore(dir string, now func() time.Time) *CursorStore {
	if now == nil {
		now = time.Now
	}
	return &CursorStore{dir: dir, now: now}
}

func (c *CursorStore) path() string { return filepath.Join(c.dir, cursorsFile) }

func (c *CursorStore) load() (cursorFileState, error) {
	state := cursorFileState{Cursors: map[string]int{}}
	if err := atomicfs.ReadJSON(c.path(), &state); err != nil {
		if os.IsNotExist(err) {
			return cursorFileState{Cursors: map[string]int{}}, nil
		}
		return cursorFileState{}, fmt.Errorf("read rotation cursors: %w", err)
	}
	if state.Cursors == nil {
		state.Cursors = map[string]int{}
	}
	return state, nil
}

func (c *CursorStore) persist(state cursorFileState) error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create senders dir: %w", err)
	}
	state.Version = 1
	state.Date = c.now().UTC().Format("2006-01-02")
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode rotation cursors: %w", err)
	}
	data = append(data, '\n')
	return atomicfs.WriteFile(c.path(), data, 0o600)
}

// Next returns the cursor position for key and advances it modulo size.
func (c *CursorStore) Next(key string, size int) (int, error) {
	if size <= 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, err := c.load()
	if err != nil {
		return 0, err
	}
	index := state.Cursors[key] % size
	if index < 0 {
		index = 0
	}
	state.Cursors[key] = (index + 1) % size
	if err := c.persist(state); err != nil {
		return 0, err
	}
	return index, nil
}

// CursorSource is the seam rotation needs; tests can supply a memory cursor.
type CursorSource interface {
	Next(key string, size int) (int, error)
}

func poolKey(pool []Sender) string {
	ids := make([]string, 0, len(pool))
	for _, sender := range pool {
		ids = append(ids, sender.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, "|")
}

// Eligible drops disabled senders and senders that have hit their daily cap.
func Eligible(pool []Sender, usage map[string]int) []Sender {
	out := make([]Sender, 0, len(pool))
	for _, sender := range pool {
		if !sender.Enabled {
			continue
		}
		if sender.DailyLimit != nil && usage[sender.ID] >= *sender.DailyLimit {
			continue
		}
		out = append(out, sender)
	}
	return out
}

// Pick chooses the next sender under policy. It returns nil when nobody is
// eligible (all disabled or over their cap), matching rotation.pick.
func Pick(pool []Sender, policy string, usage map[string]int, cursors CursorSource) (*Sender, error) {
	switch policy {
	case PolicyRoundRobin, PolicyWeighted, PolicyLeastUsed:
	default:
		policy = PolicyRoundRobin
	}
	eligible := Eligible(pool, usage)
	if len(eligible) == 0 {
		return nil, nil
	}

	var chosen Sender
	switch policy {
	case PolicyLeastUsed:
		chosen = eligible[0]
		for _, sender := range eligible[1:] {
			if usage[sender.ID] < usage[chosen.ID] {
				chosen = sender
			}
		}
	case PolicyWeighted:
		expanded := make([]Sender, 0, len(eligible))
		for _, sender := range eligible {
			weight := sender.Weight
			if weight < 1 {
				weight = 1
			}
			for i := 0; i < weight; i++ {
				expanded = append(expanded, sender)
			}
		}
		index := 0
		if cursors != nil {
			next, err := cursors.Next("w:"+poolKey(eligible), len(expanded))
			if err != nil {
				return nil, err
			}
			index = next
		}
		chosen = expanded[index%len(expanded)]
	default: // round_robin
		index := 0
		if cursors != nil {
			next, err := cursors.Next("r:"+poolKey(eligible), len(eligible))
			if err != nil {
				return nil, err
			}
			index = next
		}
		chosen = eligible[index%len(eligible)]
	}
	return &chosen, nil
}
