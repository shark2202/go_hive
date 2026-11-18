package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// validSessionDirID rejects ids that would step outside the sessions tree.
// Generated ids are safe, but callers also pass ids from request bodies.
func validSessionDirID(sessionID string) bool {
	if sessionID == "" || len(sessionID) > 255 {
		return false
	}
	if sessionID == "." || sessionID == ".." {
		return false
	}
	return !strings.ContainsAny(sessionID, "/\\\x00")
}

// Parity: framework/server/session_manager.py::_colony_id_for_session_dir
func ColonyIDForSessionDir(sessionDir string) (string, bool) {
	parent := filepath.Dir(filepath.Clean(sessionDir))
	for {
		if filepath.Base(filepath.Dir(parent)) == "colonies" {
			return filepath.Base(parent), true
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", false
		}
		parent = next
	}
}

// Parity: framework/server/session_manager.py::_iter_queen_session_dirs
func IterQueenSessionDirs(home hivehome.Home, sessionID string) ([]string, error) {
	if !validSessionDirID(sessionID) {
		return nil, fmt.Errorf("invalid session id %q", sessionID)
	}
	var out []string
	err := forEachQueenSessionDir(home, sessionID, func(dir string) bool {
		out = append(out, dir)
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Parity: framework/server/session_manager.py::_find_queen_session_dir
func FindQueenSessionDir(home hivehome.Home, sessionID string) (string, error) {
	if !validSessionDirID(sessionID) {
		return "", fmt.Errorf("invalid session id %q", sessionID)
	}
	var first string
	err := forEachQueenSessionDir(home, sessionID, func(dir string) bool {
		first = dir
		return false
	})
	if err != nil {
		return "", err
	}
	if first != "" {
		return first, nil
	}
	return filepath.Join(home.QueensDir(), "default", "sessions", sessionID), nil
}

// Parity: framework/server/routes_sessions.py::_queen_dm_session_dir
func QueenDMSessionDir(home hivehome.Home, sessionID string) (string, bool) {
	if !strings.HasPrefix(sessionID, "session_") ||
		strings.ContainsAny(sessionID, "/\\") {
		return "", false
	}
	var found string
	ok := false
	_ = forEachQueenSessionDir(home, sessionID, func(dir string) bool {
		if entryIsDir(dir) {
			if _, colony := ColonyIDForSessionDir(dir); !colony {
				found, ok = dir, true
				return false
			}
		}
		return true
	})
	return found, ok
}

// Parity: framework/server/session_manager.py::_derive_resume_colony_id
func DeriveResumeColonyID(home hivehome.Home, sessionID string) (string, bool) {
	var colonyID string
	found := false
	err := forEachQueenSessionDir(home, sessionID, func(dir string) bool {
		if id, ok := ColonyIDForSessionDir(dir); ok {
			colonyID, found = id, true
			return false
		}
		return true
	})
	if err != nil {
		return "", false
	}
	return colonyID, found
}

func forEachQueenSessionDir(home hivehome.Home, sessionID string, visit func(string) bool) error {
	queensRoot := home.QueensDir()
	queens, err := listDirIfExists(queensRoot)
	if err != nil {
		return err
	}
	for _, queenRoot := range queens {
		if !entryIsDir(filepath.Join(queensRoot, queenRoot.Name())) {
			continue
		}
		candidate := filepath.Join(queensRoot, queenRoot.Name(), "sessions", sessionID)
		if pathExists(candidate) && !visit(candidate) {
			return nil
		}
	}

	coloniesRoot := home.ColoniesDir()
	colonies, err := listDirIfExists(coloniesRoot)
	if err != nil {
		return err
	}
	for _, colonyRoot := range colonies {
		colonyPath := filepath.Join(coloniesRoot, colonyRoot.Name())
		if !entryIsDir(colonyPath) {
			continue
		}
		queensDir := filepath.Join(colonyPath, "queens")
		queens, err := listDirIfExists(queensDir)
		if err != nil {
			return err
		}
		for _, queenRoot := range queens {
			if !entryIsDir(filepath.Join(queensDir, queenRoot.Name())) {
				continue
			}
			candidate := filepath.Join(queensDir, queenRoot.Name(), "sessions", sessionID)
			if pathExists(candidate) && !visit(candidate) {
				return nil
			}
		}
	}
	return nil
}

func entryIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func listDirIfExists(dir string) ([]os.DirEntry, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}
	return os.ReadDir(dir)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
