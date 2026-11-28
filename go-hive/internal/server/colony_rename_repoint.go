package server

// Colony rename consistency repair.
//
// handle_rename_colony moves colonies/<old> -> colonies/<new> and
// agents/<old> -> agents/<new>, but every JSON document inside the colony tree
// still names the old colony: metadata.json keeps colony_id/name, colony queen
// session metas keep an agent_path under the old directory, run records keep
// colony_id, and worker metas/states keep colony_id. The queen-DM session the
// colony was spawned from keeps agent_path/spawned_colony_id too. After a
// refresh the history cannot resolve the old pointers, so the UI opens a fresh
// session instead of the colony's.
//
// Worker documents matter twice over: resume refuses a worker whose meta.json
// colony_id names another colony, and later writes carrying the stale id
// recreate the old colony directory.
//
// These helpers capture the rewritten contents of every document before the
// move (a parse failure fails the request while the colony is still intact)
// and persist them after it. Only the new colony's own session tree plus the
// one source DM session are walked, never the whole HIVE_HOME.
//
// Parity-exempt: upstream handle_rename_colony leaves these pointers stale;
// this is a Go-only consistency repair for the reported chat-history loss.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/session"
)

// colonyRenameRepoint is one JSON document rewritten after the rename. path is
// the document's post-move location; content is the full map to persist.
type colonyRenameRepoint struct {
	path    string
	content map[string]any
}

// repointColonyPath rewrites value when it names oldDir or a path below it.
func repointColonyPath(value, oldDir, newDir string) (string, bool) {
	if value == "" {
		return value, false
	}
	cleaned := filepath.Clean(value)
	oldClean := filepath.Clean(oldDir)
	if cleaned == oldClean {
		return newDir, true
	}
	prefix := oldClean + string(os.PathSeparator)
	if !strings.HasPrefix(cleaned, prefix) {
		return value, false
	}
	return filepath.Join(newDir, strings.TrimPrefix(cleaned, prefix)), true
}

// readColonyRenameJSON reads one JSON object. ok is false when the document
// does not exist (an optional file); any other failure is returned with the
// path so a preflight error names the document that blocked the rename.
func readColonyRenameJSON(path string) (content map[string]any, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	decoded, err := decodeJSONBytes(data)
	if err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("parse %s: not a JSON object", path)
	}
	return object, true, nil
}

// colonySessionRepoints collects the queen-session metas under the colony's
// tree (queens/<queen>/sessions/<session>/meta.json) whose agent_path names
// the old colony directory. Their post-move paths hang off newColonyDir
// because the whole tree moves with the colony.
func colonySessionRepoints(oldColonyDir, newColonyDir string) ([]colonyRenameRepoint, error) {
	queensDir := filepath.Join(oldColonyDir, "queens")
	queens, err := os.ReadDir(queensDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", queensDir, err)
	}
	var repoints []colonyRenameRepoint
	for _, queen := range queens {
		if !queen.IsDir() {
			continue
		}
		sessionsDir := filepath.Join(queensDir, queen.Name(), "sessions")
		sessions, err := os.ReadDir(sessionsDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", sessionsDir, err)
		}
		for _, sessionEntry := range sessions {
			if !sessionEntry.IsDir() {
				continue
			}
			metaPath := filepath.Join(sessionsDir, sessionEntry.Name(), "meta.json")
			meta, ok, err := readColonyRenameJSON(metaPath)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			repointed, changed := repointColonyPath(pythonString(meta["agent_path"]), oldColonyDir, newColonyDir)
			if !changed {
				continue
			}
			meta["agent_path"] = repointed
			repoints = append(repoints, colonyRenameRepoint{
				path:    filepath.Join(newColonyDir, "queens", queen.Name(), "sessions", sessionEntry.Name(), "meta.json"),
				content: meta,
			})
		}
	}
	return repoints, nil
}

// colonyRunRepoints collects the run records (runs/*.json) that still name
// the old colony id.
func colonyRunRepoints(oldColonyDir, newColonyDir, oldName, newName string) ([]colonyRenameRepoint, error) {
	runsDir := filepath.Join(oldColonyDir, "runs")
	paths, err := filepath.Glob(filepath.Join(runsDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", runsDir, err)
	}
	var repoints []colonyRenameRepoint
	for _, path := range paths {
		run, ok, err := readColonyRenameJSON(path)
		if err != nil {
			return nil, err
		}
		if !ok || pythonString(run["colony_id"]) != oldName {
			continue
		}
		run["colony_id"] = newName
		repoints = append(repoints, colonyRenameRepoint{
			path:    filepath.Join(newColonyDir, "runs", filepath.Base(path)),
			content: run,
		})
	}
	return repoints, nil
}

// colonyWorkerRepoints collects the worker documents (workers/<id>/meta.json
// and workers/<id>/state.json) that still name the old colony id. Resume
// validates meta.json colony_id against the running colony and refuses a
// mismatch, so a rename that leaves it stale makes every saved worker
// unresumable.
func colonyWorkerRepoints(oldColonyDir, newColonyDir, oldName, newName string) ([]colonyRenameRepoint, error) {
	workersDir := filepath.Join(oldColonyDir, "workers")
	workers, err := os.ReadDir(workersDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workersDir, err)
	}
	documents := []string{"meta.json", "state.json"}
	var repoints []colonyRenameRepoint
	for _, worker := range workers {
		if !worker.IsDir() {
			continue
		}
		for _, document := range documents {
			path := filepath.Join(workersDir, worker.Name(), document)
			content, ok, err := readColonyRenameJSON(path)
			if err != nil {
				return nil, err
			}
			if !ok || pythonString(content["colony_id"]) != oldName {
				continue
			}
			content["colony_id"] = newName
			repoints = append(repoints, colonyRenameRepoint{
				path:    filepath.Join(newColonyDir, "workers", worker.Name(), document),
				content: content,
			})
		}
	}
	return repoints, nil
}

// sourceSessionRepoints repoints the queen-DM session the colony was spawned
// from. The session id comes from the colony metadata, so only that exact
// session under the queens root is touched; every other queen session keeps
// its pointers.
func sourceSessionRepoints(home hivehome.Home, sourceSessionID, oldColonyDir, newColonyDir, oldName, newName string) ([]colonyRenameRepoint, error) {
	if sourceSessionID == "" || !session.ValidSegment(sourceSessionID) {
		return nil, nil
	}
	queensRoot := home.QueensDir()
	queens, err := os.ReadDir(queensRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", queensRoot, err)
	}
	var repoints []colonyRenameRepoint
	for _, queen := range queens {
		if !queen.IsDir() {
			continue
		}
		metaPath := filepath.Join(queensRoot, queen.Name(), "sessions", sourceSessionID, "meta.json")
		meta, ok, err := readColonyRenameJSON(metaPath)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		changed := false
		if repointed, ok := repointColonyPath(pythonString(meta["agent_path"]), oldColonyDir, newColonyDir); ok {
			meta["agent_path"] = repointed
			changed = true
		}
		if pythonString(meta["spawned_colony_id"]) == oldName {
			meta["spawned_colony_id"] = newName
			changed = true
		}
		if changed {
			repoints = append(repoints, colonyRenameRepoint{path: metaPath, content: meta})
		}
	}
	return repoints, nil
}

// preflightColonyRenameRepoints parses every document that carries a pointer
// to the old colony and returns its rewritten contents. Nothing is written
// here, so a malformed document fails the request before any directory moves.
func preflightColonyRenameRepoints(home hivehome.Home, oldName, newName string) ([]colonyRenameRepoint, error) {
	oldColonyDir := home.ColonyDir(oldName)
	newColonyDir := home.ColonyDir(newName)

	repoints := make([]colonyRenameRepoint, 0, 4)

	metadataPath := filepath.Join(oldColonyDir, "metadata.json")
	metadata, ok, err := readColonyRenameJSON(metadataPath)
	if err != nil {
		return nil, err
	}
	sourceSessionID := ""
	if ok {
		sourceSessionID = strings.TrimSpace(pythonString(metadata["source_session_id"]))
		if pythonString(metadata["colony_id"]) != newName || pythonString(metadata["name"]) != newName {
			metadata["colony_id"] = newName
			metadata["name"] = newName
			repoints = append(repoints, colonyRenameRepoint{
				path:    filepath.Join(newColonyDir, "metadata.json"),
				content: metadata,
			})
		}
	}

	sessions, err := colonySessionRepoints(oldColonyDir, newColonyDir)
	if err != nil {
		return nil, err
	}
	repoints = append(repoints, sessions...)

	runs, err := colonyRunRepoints(oldColonyDir, newColonyDir, oldName, newName)
	if err != nil {
		return nil, err
	}
	repoints = append(repoints, runs...)

	workers, err := colonyWorkerRepoints(oldColonyDir, newColonyDir, oldName, newName)
	if err != nil {
		return nil, err
	}
	repoints = append(repoints, workers...)

	source, err := sourceSessionRepoints(home, sourceSessionID, oldColonyDir, newColonyDir, oldName, newName)
	if err != nil {
		return nil, err
	}
	repoints = append(repoints, source...)

	return repoints, nil
}

// applyColonyRenameRepoints persists the preflighted documents. A failure
// names the document that could not be written so the route can answer a 500
// instead of claiming a clean rename.
func applyColonyRenameRepoints(repoints []colonyRenameRepoint) error {
	for _, repoint := range repoints {
		if err := writeJSONObjectAtomic(repoint.path, repoint.content, ".rename."); err != nil {
			return fmt.Errorf("%s: %w", repoint.path, err)
		}
	}
	return nil
}
