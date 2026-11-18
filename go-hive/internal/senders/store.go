package senders

// Local sender store. Upstream keeps the pool in Aden cloud; this build keeps
// the identical rows in $HIVE_HOME/senders/senders.json so the tool surface
// works offline. The file holds mailbox passwords, so it is written 0600 inside
// a 0700 directory and never included in a public view.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

const sendersFile = "senders.json"

type senderFile struct {
	Version int      `json:"version"`
	Senders []Sender `json:"senders"`
}

// Store is a process-safe (not cross-process-locked) JSON sender pool.
type Store struct {
	dir string
	mu  sync.Mutex
}

// OpenStore binds a store to the senders directory (usually $HIVE_HOME/senders).
func OpenStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path() string { return filepath.Join(s.dir, sendersFile) }

func (s *Store) load() ([]Sender, error) {
	var state senderFile
	if err := atomicfs.ReadJSON(s.path(), &state); err != nil {
		if os.IsNotExist(err) {
			return []Sender{}, nil
		}
		return nil, fmt.Errorf("read senders: %w", err)
	}
	if state.Senders == nil {
		state.Senders = []Sender{}
	}
	for i := range state.Senders {
		if state.Senders[i].Tags == nil {
			state.Senders[i].Tags = []string{}
		}
	}
	return state.Senders, nil
}

func (s *Store) persist(rows []Sender) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create senders dir: %w", err)
	}
	data, err := json.MarshalIndent(senderFile{Version: 1, Senders: rows}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode senders: %w", err)
	}
	data = append(data, '\n')
	return atomicfs.WriteFile(s.path(), data, 0o600)
}

// List returns the pool in creation order. includeDisabled mirrors
// SenderRegistry.list(include_disabled=...).
func (s *Store) List(includeDisabled bool) ([]Sender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Sender, 0, len(rows))
	for _, row := range rows {
		if !includeDisabled && !row.Enabled {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

// Get resolves a sender by exact id or case-insensitive name, matching
// SenderRegistry.get.
func (s *Store) Get(idOrName string) (*Sender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ID == idOrName {
			row := rows[i]
			return &row, nil
		}
	}
	wanted := strings.ToLower(strings.TrimSpace(idOrName))
	if wanted == "" {
		return nil, nil
	}
	for i := range rows {
		if strings.ToLower(strings.TrimSpace(rows[i].Name)) == wanted {
			row := rows[i]
			return &row, nil
		}
	}
	return nil, nil
}

// Create inserts one sender and returns the stored row (id assigned here).
func (s *Store) Create(sender Sender) (Sender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return Sender{}, err
	}
	if sender.ID == "" {
		sender.ID = newSenderID()
	}
	for _, row := range rows {
		if row.ID == sender.ID {
			return Sender{}, fmt.Errorf("sender id %q already exists", sender.ID)
		}
	}
	if sender.Weight <= 0 {
		sender.Weight = 1
	}
	if sender.Tags == nil {
		sender.Tags = []string{}
	}
	rows = append(rows, sender)
	if err := s.persist(rows); err != nil {
		return Sender{}, err
	}
	return sender, nil
}

// Update applies mutate to the row with the given id inside the store lock and
// returns the row as it was before the change plus the stored result.
func (s *Store) Update(id string, mutate func(*Sender)) (Sender, Sender, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return Sender{}, Sender{}, err
	}
	for i := range rows {
		if rows[i].ID != id {
			continue
		}
		before := rows[i]
		after := rows[i]
		mutate(&after)
		after.ID = before.ID // identifiers are immutable
		if after.Tags == nil {
			after.Tags = []string{}
		}
		rows[i] = after
		if err := s.persist(rows); err != nil {
			return Sender{}, Sender{}, err
		}
		return before, after, nil
	}
	return Sender{}, Sender{}, fmt.Errorf("sender %q not found", id)
}
