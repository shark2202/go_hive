package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

func NeedsMigrationV3(homeRoot string) bool {
	if fileExists(filepath.Join(homeRoot, ".migrated-v3")) {
		return false
	}
	if fileExists(filepath.Join(homeRoot, "agents", "queens")) {
		return true
	}
	return hasV2TrackerLayout(filepath.Join(homeRoot, "colonies"))
}

func hasV2TrackerLayout(coloniesDir string) bool {
	if !fileExists(coloniesDir) {
		return false
	}
	for _, name := range readDirNames(coloniesDir) {
		if fileExists(filepath.Join(coloniesDir, name, "data", "tracker.db")) {
			return true
		}
	}
	return false
}

func RunMigrationV3(homeRoot string) error {
	if !NeedsMigrationV3(homeRoot) {
		return nil
	}
	h, err := hivehome.ResolveAt(homeRoot)
	if err != nil {
		return err
	}
	migrateV3QueenProfiles(h)
	migrateV3QueenSessions(h)
	migrateV3AgentColonySubtrees(h)
	migrateV3TrackerDirs(h)
	cleanupEmptyV2Dirs(h)

	if err := os.MkdirAll(h.Root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.Root, ".migrated-v3"), []byte("1\n"), 0o644)
}

func v2AgentsDir(h hivehome.Home) string {
	return filepath.Join(h.Root, "agents")
}

func migrateV3QueenProfiles(h hivehome.Home) {
	v2Queens := filepath.Join(v2AgentsDir(h), "queens")
	if !fileExists(v2Queens) {
		return
	}
	for _, queenName := range readDirNames(v2Queens) {
		queenV2 := filepath.Join(v2Queens, queenName)
		if info, err := os.Stat(queenV2); err != nil || !info.IsDir() {
			continue
		}
		queenV3 := filepath.Join(h.QueensDir(), queenName)
		if err := os.MkdirAll(queenV3, 0o755); err != nil {
			continue
		}
		for _, entryName := range readDirNames(queenV2) {
			if entryName == "sessions" {
				continue
			}
			entry := filepath.Join(queenV2, entryName)
			target := filepath.Join(queenV3, entryName)
			if fileExists(target) {
				continue
			}
			_ = movePath(entry, target)
		}
	}
}

func migrateV3QueenSessions(h hivehome.Home) {
	v2Queens := filepath.Join(v2AgentsDir(h), "queens")
	if !fileExists(v2Queens) {
		return
	}
	colonies := h.ColoniesDir()
	resolvedColonies := resolveExistingPath(colonies)
	for _, queenID := range readDirNames(v2Queens) {
		queenV2 := filepath.Join(v2Queens, queenID)
		sessionsDir := filepath.Join(queenV2, "sessions")
		if !fileExists(sessionsDir) {
			continue
		}
		for _, sid := range readDirNames(sessionsDir) {
			sessionDir := filepath.Join(sessionsDir, sid)
			if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
				continue
			}
			colonyID := colonyIDFromAgentPath(filepath.Join(sessionDir, "meta.json"), resolvedColonies)
			var target string
			if colonyID != "" {
				target = filepath.Join(colonies, colonyID, "queens", queenID, "sessions", sid)
			} else {
				target = filepath.Join(h.QueensDir(), queenID, "sessions", sid)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				continue
			}
			if fileExists(target) {
				continue
			}
			_ = movePath(sessionDir, target)
		}
	}
}

func colonyIDFromAgentPath(metaPath, resolvedColonies string) string {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	raw, _ := meta["agent_path"].(string)
	if raw == "" {
		return ""
	}
	info, err := os.Stat(raw)
	if err != nil || !info.IsDir() {
		return ""
	}
	if !strings.HasPrefix(resolveExistingPath(raw), resolvedColonies) {
		return ""
	}
	return filepath.Base(raw)
}

func migrateV3AgentColonySubtrees(h hivehome.Home) {
	agentsDir := v2AgentsDir(h)
	if !fileExists(agentsDir) {
		return
	}
	colonies := h.ColoniesDir()
	for _, agentName := range readDirNames(agentsDir) {
		agentDir := filepath.Join(agentsDir, agentName)
		if info, err := os.Stat(agentDir); err != nil || !info.IsDir() {
			continue
		}
		if agentName == "queens" {
			continue
		}
		colonyRoot := filepath.Join(colonies, agentName)
		if info, err := os.Stat(colonyRoot); err != nil || !info.IsDir() {
			continue
		}
		for _, subName := range readDirNames(agentDir) {
			sub := filepath.Join(agentDir, subName)
			if info, err := os.Stat(sub); err != nil || !info.IsDir() {
				continue
			}
			if subName == "worker" {
				srcConv := filepath.Join(sub, "conversations")
				if !fileExists(srcConv) {
					continue
				}
				dstConv := filepath.Join(colonyRoot, "seed_conversation")
				if fileExists(dstConv) {
					continue
				}
				_ = movePath(srcConv, dstConv)
				continue
			}
			target := filepath.Join(colonyRoot, "workers", subName)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				continue
			}
			if fileExists(target) {
				continue
			}
			_ = movePath(sub, target)
		}
	}
}

func migrateV3TrackerDirs(h hivehome.Home) {
	colonies := h.ColoniesDir()
	if !fileExists(colonies) {
		return
	}
	for _, colonyName := range readDirNames(colonies) {
		colonyDir := filepath.Join(colonies, colonyName)
		if info, err := os.Stat(colonyDir); err != nil || !info.IsDir() {
			continue
		}
		oldPath := filepath.Join(colonyDir, "data")
		newPath := filepath.Join(colonyDir, "tracker")
		if !fileExists(oldPath) || fileExists(newPath) {
			continue
		}
		_ = movePath(oldPath, newPath)
	}
}

func cleanupEmptyV2Dirs(h hivehome.Home) {
	agentsDir := v2AgentsDir(h)
	v2Queens := filepath.Join(agentsDir, "queens")
	if fileExists(agentsDir) && fileExists(v2Queens) {
		for _, queenName := range readDirNames(v2Queens) {
			queenV2 := filepath.Join(v2Queens, queenName)
			sessionsDir := filepath.Join(queenV2, "sessions")
			if dirIsEmpty(sessionsDir) {
				_ = os.Remove(sessionsDir)
			}
			if dirIsEmpty(queenV2) {
				_ = os.Remove(queenV2)
			}
		}
		if dirIsEmpty(v2Queens) {
			_ = os.Remove(v2Queens)
		}
	}

	if !fileExists(agentsDir) {
		return
	}
	for _, agentName := range readDirNames(agentsDir) {
		if agentName == "queens" {
			continue
		}
		rmtreeIfEmpty(filepath.Join(agentsDir, agentName))
	}
	if dirIsEmpty(agentsDir) {
		_ = os.Remove(agentsDir)
	}
}

func dirIsEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}

func rmtreeIfEmpty(root string) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	for _, name := range readDirNames(root) {
		rmtreeIfEmpty(filepath.Join(root, name))
	}
	if dirIsEmpty(root) {
		_ = os.Remove(root)
	}
}
