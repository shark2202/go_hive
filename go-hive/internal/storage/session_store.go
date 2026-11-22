package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/schemas"
)

type SessionStore struct {
	BasePath    string
	SessionsDir string
}

func NewSessionStore(basePath string) *SessionStore {
	base := filepath.Clean(basePath)
	return &SessionStore{
		BasePath:    base,
		SessionsDir: filepath.Join(base, "sessions"),
	}
}

func (s *SessionStore) GenerateSessionID() string {
	short := make([]byte, 4)
	if _, err := rand.Read(short); err != nil {
		return fmt.Sprintf("session_%s_%08x", time.Now().Format("20060102_150405"),
			uint32(time.Now().UnixNano()))
	}
	return fmt.Sprintf("session_%s_%s", time.Now().Format("20060102_150405"), hex.EncodeToString(short))
}

func (s *SessionStore) GetSessionPath(sessionID string) (string, error) {
	candidate := sessionID
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.SessionsDir, sessionID)
	}
	resolved := resolveExistingPath(candidate)
	if !isRelativeTo(resolved, resolveExistingPath(s.SessionsDir)) {
		return "", fmt.Errorf("Invalid session ID: %s", sessionID)
	}
	return resolved, nil
}

func (s *SessionStore) GetStatePath(sessionID string) (string, error) {
	p, err := s.GetSessionPath(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "state.json"), nil
}

func (s *SessionStore) WriteState(sessionID string, state *schemas.SessionState) error {
	statePath, err := s.GetStatePath(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	// Session artifacts are owner-only (0600), matching the upstream atomic
	// writers and the rest of the session tree (parts/cursor/meta).
	return atomicfs.WriteJSON(statePath, state, 0o600)
}

func (s *SessionStore) ReadState(sessionID string) (*schemas.SessionState, error) {
	statePath, err := s.GetStatePath(sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var state schemas.SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *SessionStore) ListSessions(status, goalID string, limit int) ([]schemas.SessionState, error) {
	sessions := []schemas.SessionState{}
	entries, err := os.ReadDir(s.SessionsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sessions, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statePath := filepath.Join(s.SessionsDir, entry.Name(), "state.json")
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue
		}
		var state schemas.SessionState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if status != "" && state.Status != schemas.SessionStatus(status) {
			continue
		}
		if goalID != "" && state.GoalID != goalID {
			continue
		}
		sessions = append(sessions, state)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].Timestamps.UpdatedAt.Time.After(sessions[j].Timestamps.UpdatedAt.Time)
	})
	if limit >= 0 && len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func (s *SessionStore) DeleteSession(sessionID string) (bool, error) {
	sessionPath, err := s.GetSessionPath(sessionID)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(sessionPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := os.RemoveAll(sessionPath); err != nil {
		return false, err
	}
	return true, nil
}

func (s *SessionStore) SessionExists(sessionID string) (bool, error) {
	statePath, err := s.GetStatePath(sessionID)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(statePath)
	if statErr == nil {
		return true, nil
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	return false, statErr
}

func resolveExistingPath(path string) string {
	path = filepath.Clean(path)
	rest := ""
	cur := path
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Join(cur, rest)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func isRelativeTo(child, base string) bool {
	rel, err := filepath.Rel(base, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
