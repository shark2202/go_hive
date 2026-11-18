package colony

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

func IterQueenSessionDirs(home hivehome.Home, sessionID string) []string {
	var out []string

	if queenRoots, err := os.ReadDir(home.QueensDir()); err == nil {
		for _, queen := range sortedDirs(queenRoots) {
			candidate := filepath.Join(home.QueensDir(), queen, "sessions", sessionID)
			if pathExists(candidate) {
				out = append(out, candidate)
			}
		}
	}

	if colonyRoots, err := os.ReadDir(home.ColoniesDir()); err == nil {
		for _, colony := range sortedDirs(colonyRoots) {
			queensRoot := filepath.Join(home.ColoniesDir(), colony, "queens")
			queenRoots, err := os.ReadDir(queensRoot)
			if err != nil {
				continue
			}
			for _, queen := range sortedDirs(queenRoots) {
				candidate := filepath.Join(queensRoot, queen, "sessions", sessionID)
				if pathExists(candidate) {
					out = append(out, candidate)
				}
			}
		}
	}

	return out
}

func ColonyIDForSessionDir(sessionDir string) *string {
	dir := filepath.Clean(sessionDir)
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		if filepath.Base(parent) == "colonies" {
			slug := filepath.Base(dir)
			return &slug
		}
		dir = parent
	}
}

func DeriveResumeColonyID(home hivehome.Home, sessionID string) *string {
	for _, candidate := range IterQueenSessionDirs(home, sessionID) {
		if colonyID := ColonyIDForSessionDir(candidate); colonyID != nil {
			return colonyID
		}
	}
	return nil
}

func sortedDirs(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
