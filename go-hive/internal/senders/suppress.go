package senders

// Suppression list (do-not-contact). Enforced at send time, so it cannot be
// bypassed by choosing another sender or by calling a send path directly.
// Upstream stores this in cloud (sendlog.suppress / suppressions); the local
// fallback is $HIVE_HOME/senders/suppressions.json.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const suppressionsFile = "suppressions.json"

// SuppressionReason values accepted by suppress_recipient.
const (
	ReasonUnsubscribed = "unsubscribed"
	ReasonComplained   = "complained"
	ReasonHardBounce   = "hard_bounce"
	ReasonManual       = "manual"
)

// Suppression is one do-not-contact entry.
type Suppression struct {
	Email     string `json:"email"`
	Reason    string `json:"reason"`
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

type suppressionFile struct {
	Version      int           `json:"version"`
	Suppressions []Suppression `json:"suppressions"`
}

// SuppressionStore is a JSON-backed do-not-contact list.
type SuppressionStore struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
}

// OpenSuppressionStore binds the list to the senders directory.
func OpenSuppressionStore(dir string, now func() time.Time) *SuppressionStore {
	if now == nil {
		now = time.Now
	}
	return &SuppressionStore{dir: dir, now: now}
}

func (s *SuppressionStore) path() string { return filepath.Join(s.dir, suppressionsFile) }

func (s *SuppressionStore) load() ([]Suppression, error) {
	var state suppressionFile
	if err := atomicfs.ReadJSON(s.path(), &state); err != nil {
		if os.IsNotExist(err) {
			return []Suppression{}, nil
		}
		return nil, fmt.Errorf("read suppressions: %w", err)
	}
	if state.Suppressions == nil {
		state.Suppressions = []Suppression{}
	}
	return state.Suppressions, nil
}

func (s *SuppressionStore) persist(rows []Suppression) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create senders dir: %w", err)
	}
	data, err := json.MarshalIndent(suppressionFile{Version: 1, Suppressions: rows}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode suppressions: %w", err)
	}
	data = append(data, '\n')
	return atomicfs.WriteFile(s.path(), data, 0o600)
}

// NormalizeAddress lowercases and trims an address for comparison and storage.
func NormalizeAddress(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Add inserts (or refreshes) an address. Re-adding updates the reason/note
// rather than duplicating the row, so an unsubscribe can be re-recorded after
// a bounce without growing the list.
func (s *SuppressionStore) Add(email, reason, note string) (Suppression, error) {
	address := NormalizeAddress(email)
	if address == "" || !strings.Contains(address, "@") {
		return Suppression{}, fmt.Errorf("invalid email address: %q", email)
	}
	if !ValidSuppressionReason(reason) {
		reason = ReasonManual
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.load()
	if err != nil {
		return Suppression{}, err
	}
	for i := range rows {
		if NormalizeAddress(rows[i].Email) != address {
			continue
		}
		rows[i].Reason = reason
		if note != "" {
			rows[i].Note = note
		}
		if err := s.persist(rows); err != nil {
			return Suppression{}, err
		}
		return rows[i], nil
	}
	row := Suppression{
		Email:     address,
		Reason:    reason,
		Note:      note,
		CreatedAt: s.now().UTC().Format(time.RFC3339),
	}
	rows = append(rows, row)
	if err := s.persist(rows); err != nil {
		return Suppression{}, err
	}
	return row, nil
}

// List returns the list in insertion order.
func (s *SuppressionStore) List() ([]Suppression, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// IsSuppressed reports whether an address must not be contacted.
func (s *SuppressionStore) IsSuppressed(email string) (bool, error) {
	address := NormalizeAddress(email)
	if address == "" {
		return false, nil
	}
	rows, err := s.List()
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if NormalizeAddress(row.Email) == address {
			return true, nil
		}
	}
	return false, nil
}

// ValidSuppressionReason mirrors the upstream allowed set; anything else is
// recorded as a manual decision rather than rejected outright.
func ValidSuppressionReason(reason string) bool {
	switch reason {
	case ReasonUnsubscribed, ReasonComplained, ReasonHardBounce, ReasonManual:
		return true
	}
	return false
}
