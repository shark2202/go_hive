package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func NeedsMigrationColonySessions(homeRoot string) bool {
	if fileExists(filepath.Join(homeRoot, "colonies", ".migrations", "colony_sessions_v1")) {
		return false
	}
	return fileExists(filepath.Join(homeRoot, "queens")) || fileExists(filepath.Join(homeRoot, "colonies"))
}

func RunMigrationColonySessions(homeRoot string) error {
	if !NeedsMigrationColonySessions(homeRoot) {
		return nil
	}
	h, err := hivehome.ResolveAt(homeRoot)
	if err != nil {
		return err
	}
	queensDir := h.QueensDir()
	coloniesDir := h.ColoniesDir()

	if fileExists(queensDir) {
		for _, queenID := range readDirNames(queensDir) {
			queenRoot := filepath.Join(queensDir, queenID)
			if info, err := os.Stat(queenRoot); err != nil || !info.IsDir() {
				continue
			}
			sessionsDir := filepath.Join(queenRoot, "sessions")
			if !fileExists(sessionsDir) {
				continue
			}
			for _, sid := range readDirNames(sessionsDir) {
				sessionDir := filepath.Join(sessionsDir, sid)
				if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
					continue
				}
				_ = migrateColonySessionOne(h, sessionDir, queenID)
			}
		}
	}

	migrateLegacySingularQueenDirs(h)

	markerDir := filepath.Join(coloniesDir, ".migrations")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(markerDir, "colony_sessions_v1"), []byte("1\n"), 0o644)
}

func migrateColonySessionOne(h hivehome.Home, sessionDir, queenID string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	if err != nil {
		return "skipped"
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return "skipped"
	}
	if !jsonTruthy(meta["colony_fork"]) {
		return "skipped"
	}
	colonyID := deriveColonyID(meta, h.ColoniesDir())
	if colonyID == "" {
		return "skipped"
	}
	sid := filepath.Base(sessionDir)
	target := filepath.Join(h.ColoniesDir(), colonyID, "queens", queenID, "sessions", sid)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "skipped"
	}

	if !fileExists(target) {
		if err := movePath(sessionDir, target); err != nil {
			return "skipped"
		}
		return "moved"
	}

	targetEvents := filepath.Join(target, "events.jsonl")
	info, statErr := os.Stat(targetEvents)
	if statErr != nil || info.Size() == 0 {
		if err := os.RemoveAll(target); err != nil {
			return "skipped"
		}
		if err := movePath(sessionDir, target); err != nil {
			return "skipped"
		}
		return "overwrote"
	}

	archiveRoot := filepath.Join(h.ColoniesDir(), colonyID, "legacy_fork_snapshots")
	if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
		return "skipped"
	}
	archiveTarget := filepath.Join(archiveRoot, sid)
	suffix := 0
	for fileExists(archiveTarget) {
		suffix++
		archiveTarget = filepath.Join(archiveRoot, sid+"."+itoa(suffix))
	}
	if err := movePath(sessionDir, archiveTarget); err != nil {
		return "skipped"
	}
	return "archived"
}

func migrateLegacySingularQueenDirs(h hivehome.Home) {
	coloniesDir := h.ColoniesDir()
	if !fileExists(coloniesDir) {
		return
	}
	for _, colonyName := range readDirNames(coloniesDir) {
		colonyDir := filepath.Join(coloniesDir, colonyName)
		if info, err := os.Stat(colonyDir); err != nil || !info.IsDir() {
			continue
		}
		legacyRoot := filepath.Join(colonyDir, "queen")
		if info, err := os.Stat(legacyRoot); err != nil || !info.IsDir() {
			continue
		}
		for _, sid := range readDirNames(legacyRoot) {
			sessionDir := filepath.Join(legacyRoot, sid)
			if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
				continue
			}
			if !strings.HasPrefix(sid, "session_") {
				continue
			}
			queenID := "default"
			if data, err := os.ReadFile(filepath.Join(sessionDir, "meta.json")); err == nil {
				var meta map[string]any
				if json.Unmarshal(data, &meta) == nil {
					if raw, ok := meta["queen_id"].(string); ok && raw != "" {
						queenID = raw
					}
				}
			}
			target := filepath.Join(colonyDir, "queens", queenID, "sessions", sid)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				continue
			}
			if fileExists(target) {
				continue
			}
			_ = movePath(sessionDir, target)
		}
		if dirIsEmpty(legacyRoot) {
			_ = os.Remove(legacyRoot)
		}
	}
}

func deriveColonyID(meta map[string]any, coloniesDir string) string {
	raw, ok := meta["agent_path"].(string)
	if !ok || raw == "" {
		return ""
	}
	candidate := resolveExistingPath(raw)
	root := resolveExistingPath(coloniesDir)
	if filepath.Dir(candidate) != root {
		return ""
	}
	return filepath.Base(candidate)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
