package server

// Parity: framework/server/routes_colonies.py::handle_delete_colony
// Parity: framework/server/routes_colonies.py::handle_rename_colony
// Parity: framework/server/routes_colonies.py::handle_reveal_colony_folder
// Parity: framework/server/routes_colonies.py::handle_scaffold_colony

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/platform"
)

const colonyNamePattern = "colony name must match [a-z0-9_]+"

// ColonyRouteDeps carries the filesystem home plus the two seams the colony
// lifecycle routes need: the live session registry (to stop sessions before a
// directory moves) and the OS file-manager launcher.
type ColonyRouteDeps struct {
	Home       hivehome.Home
	Manager    *SessionManager
	Reveal     func(string) error
	StagingDir string
	MaxUpload  int64
	MaxUploads int
	UploadTTL  time.Duration
	Clock      func() time.Time
	RandID     func() (string, error)
}

func RegisterColonyRoutes(mux *http.ServeMux, deps ColonyRouteDeps) {
	mux.HandleFunc("POST /api/colonies/import", func(w http.ResponseWriter, r *http.Request) {
		handleImportColony(w, r, deps)
	})
	mux.HandleFunc("POST /api/colonies/import/init", func(w http.ResponseWriter, r *http.Request) {
		handleInitUpload(w, r, deps)
	})
	mux.HandleFunc("PUT /api/colonies/import/chunk/{upload_id}", func(w http.ResponseWriter, r *http.Request) {
		handleChunkUpload(w, r, deps)
	})
	mux.HandleFunc("GET /api/colonies/import/status/{upload_id}", func(w http.ResponseWriter, r *http.Request) {
		handleUploadStatus(w, r, deps)
	})
	mux.HandleFunc("POST /api/colonies/import/finalize/{upload_id}", func(w http.ResponseWriter, r *http.Request) {
		handleFinalizeUpload(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/colonies/import/{upload_id}", func(w http.ResponseWriter, r *http.Request) {
		handleCancelUpload(w, r, deps)
	})
	mux.HandleFunc("POST /api/colonies/{colony_id}/scaffold", func(w http.ResponseWriter, r *http.Request) {
		handleScaffoldColony(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/colonies/{colony_id}", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteColony(w, r, deps)
	})
	mux.HandleFunc("POST /api/colonies/{colony_id}/reveal", func(w http.ResponseWriter, r *http.Request) {
		handleRevealColonyFolder(w, r, deps)
	})
	mux.HandleFunc("POST /api/colonies/{colony_id}/rename", func(w http.ResponseWriter, r *http.Request) {
		handleRenameColony(w, r, deps)
	})
	mux.HandleFunc("GET /api/colonies/{colony_id}/worker_profiles", func(w http.ResponseWriter, r *http.Request) {
		handleListWorkerProfiles(w, r, deps)
	})
	mux.HandleFunc("POST /api/colonies/{colony_id}/worker_profiles", func(w http.ResponseWriter, r *http.Request) {
		handleUpsertWorkerProfile(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/colonies/{colony_id}/worker_profiles/{profile_name}", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteWorkerProfile(w, r, deps)
	})
}

func (d ColonyRouteDeps) now() time.Time {
	if d.Clock != nil {
		return d.Clock()
	}
	return time.Now()
}

func (d ColonyRouteDeps) agentsDir() string {
	return filepath.Join(d.Home.Root, "agents")
}

func validateColonyName(name string) string {
	if name == "" {
		return "colony name is required"
	}
	if len(name) > 64 {
		return "colony name too long (max 64 chars)"
	}
	if !validColonyNameSegment(name) {
		return colonyNamePattern
	}
	return ""
}

func validColonyNameSegment(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func validateColonySessionSegment(segment, label string) string {
	if segment == "" {
		return label + " is required"
	}
	if len(segment) > 128 {
		return label + " too long (max 128 chars)"
	}
	for _, ch := range segment {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			continue
		}
		return label + " must match [a-zA-Z0-9_-]+"
	}
	return ""
}

func colonyMetadataPath(home hivehome.Home, colonyID string) string {
	return filepath.Join(home.ColoniesDir(), colonyID, "metadata.json")
}

// Parity: framework/host/colony_metadata.py::load_colony_metadata
func loadColonyMetadata(home hivehome.Home, colonyID string) map[string]any {
	path := colonyMetadataPath(home, colonyID)
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	decoded, err := decodeJSONBytes(data)
	if err != nil {
		return map[string]any{}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return object
}

// Parity: framework/host/colony_metadata.py::update_colony_metadata
func updateColonyMetadata(home hivehome.Home, colonyID string, updates map[string]any) (map[string]any, error) {
	path := colonyMetadataPath(home, colonyID)
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("Colony '%s' not found", colonyID)
	}
	data := map[string]any{}
	if _, err := os.Stat(path); err == nil {
		data = loadColonyMetadata(home, colonyID)
	}
	for key, value := range updates {
		data[key] = value
	}
	if err := writeJSONObjectAtomic(path, data, ".metadata."); err != nil {
		return nil, err
	}
	return data, nil
}

// Parity: framework/host/colony_metadata.py::is_colony_soft_deleted
func isColonySoftDeleted(home hivehome.Home, colonyID string) bool {
	return pyTruthy(loadColonyMetadata(home, colonyID)["deleted"])
}

// Parity: framework/host/colony_metadata.py::vacate_soft_deleted_colony
func vacateSoftDeletedColony(home hivehome.Home, colonyID string) (string, error) {
	if !isColonySoftDeleted(home, colonyID) {
		return "", nil
	}
	coloniesRoot := home.ColoniesDir()
	agentsRoot := filepath.Join(home.Root, "agents")
	n := 1
	for {
		parked := fmt.Sprintf("%s.deleted.%d", colonyID, n)
		_, colonyErr := os.Stat(filepath.Join(coloniesRoot, parked))
		_, agentErr := os.Stat(filepath.Join(agentsRoot, parked))
		if errors.Is(colonyErr, os.ErrNotExist) && errors.Is(agentErr, os.ErrNotExist) {
			if err := os.Rename(filepath.Join(coloniesRoot, colonyID), filepath.Join(coloniesRoot, parked)); err != nil {
				return "", err
			}
			agentDir := filepath.Join(agentsRoot, colonyID)
			if info, err := os.Stat(agentDir); err == nil && info.IsDir() {
				if err := os.Rename(agentDir, filepath.Join(agentsRoot, parked)); err != nil {
					_ = os.Rename(filepath.Join(coloniesRoot, parked), filepath.Join(coloniesRoot, colonyID))
					return "", err
				}
			}
			return parked, nil
		}
		n++
	}
}

func stopSessionsUnderPaths(deps ColonyRouteDeps, roots ...string) {
	if deps.Manager == nil {
		return
	}
	resolvedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		resolvedRoots = append(resolvedRoots, resolveExistingPath(root))
	}
	for _, session := range deps.Manager.ListSessions() {
		if session == nil || session.WorkerPath == "" {
			continue
		}
		workerPath := resolveExistingPath(session.WorkerPath)
		for _, root := range resolvedRoots {
			if colonyPathWithin(workerPath, root) {
				deps.Manager.StopSession(session.ID)
				break
			}
		}
	}
}

// resolveExistingPath resolves symlinks for the deepest existing ancestor and
// re-joins the missing tail, so a not-yet-created worker path still compares
// against the resolved hive tree.
func resolveExistingPath(path string) string {
	cleaned := filepath.Clean(path)
	existing := cleaned
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		next := filepath.Dir(existing)
		if next == existing {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = next
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		resolved = existing
	}
	for _, segment := range tail {
		resolved = filepath.Join(resolved, segment)
	}
	return resolved
}

func colonyPathWithin(path, root string) bool {
	if path == root {
		return true
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func handleDeleteColony(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if err := validateColonyName(colonyID); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}

	purgeValue := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("purge")))
	purge := purgeValue == "1" || purgeValue == "true" || purgeValue == "yes"

	colonyDir := deps.Home.ColonyDir(colonyID)
	agentDir := filepath.Join(deps.agentsDir(), colonyID)

	colonyInfo, colonyErr := os.Stat(colonyDir)
	agentInfo, agentErr := os.Stat(agentDir)
	colonyIsDir := colonyErr == nil && colonyInfo.IsDir()
	agentIsDir := agentErr == nil && agentInfo.IsDir()
	if !colonyIsDir && !agentIsDir {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": colonyID, "purged": purge})
		return
	}

	stopSessionsUnderPaths(deps, colonyDir, agentDir)

	if purge {
		for _, dir := range []string{colonyDir, agentDir} {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				if removeErr := os.RemoveAll(dir); removeErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]any{
						"error": "failed to delete colony directory: " + removeErr.Error(),
					})
					return
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": colonyID, "purged": true})
		return
	}

	if colonyIsDir {
		if _, err := updateColonyMetadata(deps.Home, colonyID, map[string]any{"deleted": true}); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "failed to mark colony deleted: " + err.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": colonyID, "purged": false})
}

func handleRenameColony(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	oldName := r.PathValue("colony_id")
	if err := validateColonyName(oldName); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}

	decoded, err := decodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	newName := strings.TrimSpace(pythonString(body["new_name"]))
	if err := validateColonyName(newName); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}
	if newName == oldName {
		writeJSON(w, http.StatusOK, map[string]any{"renamed": false, "old_name": oldName, "new_name": newName})
		return
	}

	oldColonyDir := deps.Home.ColonyDir(oldName)
	newColonyDir := deps.Home.ColonyDir(newName)
	agentsRoot := deps.agentsDir()
	oldAgentDir := filepath.Join(agentsRoot, oldName)
	newAgentDir := filepath.Join(agentsRoot, newName)

	if info, statErr := os.Stat(oldColonyDir); statErr != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "colony '" + oldName + "' not found"})
		return
	}
	if _, err := vacateSoftDeletedColony(deps.Home, newName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to clear soft-deleted colony '" + newName + "': " + err.Error(),
		})
		return
	}
	_, newColonyErr := os.Stat(newColonyDir)
	_, newAgentErr := os.Stat(newAgentDir)
	if newColonyErr == nil || newAgentErr == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "colony '" + newName + "' already exists"})
		return
	}

	stopSessionsUnderPaths(deps, oldColonyDir, oldAgentDir)

	if err := os.Rename(oldColonyDir, newColonyDir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to rename colony directory: " + err.Error(),
		})
		return
	}
	if info, statErr := os.Stat(oldAgentDir); statErr == nil && info.IsDir() {
		if err := os.Rename(oldAgentDir, newAgentDir); err != nil {
			_ = os.Rename(newColonyDir, oldColonyDir)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "failed to rename agent directory: " + err.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"renamed": true, "old_name": oldName, "new_name": newName})
}

func handleRevealColonyFolder(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if err := validateColonyName(colonyID); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}
	folder := deps.Home.ColonyDir(colonyID)
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "colony '" + colonyID + "' not found"})
		return
	}
	reveal := deps.Reveal
	if reveal == nil {
		reveal = platform.OpenPath
	}
	if err := reveal(folder); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": folder})
}

func handleScaffoldColony(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if err := validateColonyName(colonyID); err != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err})
		return
	}

	body := map[string]any{}
	if r.Body != nil && r.ContentLength != 0 {
		if decoded, err := decodeJSONValue(r); err == nil {
			if object, ok := decoded.(map[string]any); ok {
				body = object
			}
		}
	}
	queenName := strings.TrimSpace(pythonString(body["queen_name"]))

	if err := ensureMinimalColony(deps.Home, colonyID, queenName); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if _, err := updateColonyMetadata(deps.Home, colonyID, map[string]any{"scaffolded": true}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to mark colony scaffolded: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"colony_id": colonyID, "scaffolded": true})
}

// Parity: framework/server/session_manager.py::_ensure_minimal_colony
func ensureMinimalColony(home hivehome.Home, colonyID, queenName string) error {
	colonyDir := home.ColonyDir(colonyID)
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		return err
	}

	metadataPath := filepath.Join(colonyDir, "metadata.json")
	if _, err := os.Stat(metadataPath); errors.Is(err, os.ErrNotExist) {
		meta := map[string]any{"name": colonyID, "created_at": float64(time.Now().UnixNano()) / 1e9}
		if queenName != "" {
			meta["queen_name"] = queenName
		}
		if err := writeJSONObjectAtomic(metadataPath, meta, ".metadata."); err != nil {
			return err
		}
	}

	workerPath := filepath.Join(colonyDir, "worker.json")
	if _, err := os.Stat(workerPath); errors.Is(err, os.ErrNotExist) {
		worker := map[string]any{
			"name":        humanizeSlug(colonyID),
			"description": "Colony " + colonyID,
			"tools":       []any{},
			"goal":        map[string]any{"description": "Work on tasks for colony " + colonyID},
		}
		if queenName != "" {
			worker["queen_name"] = queenName
		}
		if err := writeJSONObjectAtomic(workerPath, worker, ".worker."); err != nil {
			return err
		}
	}
	// A scaffolded colony is complete on disk: skills/, runs/ and an empty
	// triggers.json exist before any session opens it.
	return ensureColonyScaffold(colonyDir)
}

// humanizeSlug mirrors framework/utils/text.py::humanize_slug, including
// Python str.title()'s letter/non-letter word boundaries.
func humanizeSlug(slug string) string {
	lowered := strings.ReplaceAll(slug, "_", " ")
	runes := []rune(lowered)
	previousIsLetter := false
	for index, ch := range runes {
		if unicode.IsLetter(ch) {
			if previousIsLetter {
				runes[index] = unicode.ToLower(ch)
			} else {
				runes[index] = unicode.ToUpper(ch)
			}
			previousIsLetter = true
			continue
		}
		previousIsLetter = false
	}
	return string(runes)
}

func pythonString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(typed)
	}
}
