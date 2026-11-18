package server

// Parity: framework/server/routes_sessions.py::handle_report_session_bundle

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aden-hive/hive-go/internal/sessionreport"
)

func handleReportSessionBundle(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := lookupSession(deps, sessionID)

	storageSessionID := sessionID
	if session != nil {
		if session.QueenResumeFrom != "" {
			storageSessionID = session.QueenResumeFrom
		} else {
			storageSessionID = session.ID
		}
	}

	folder := ""
	if session != nil && session.QueenDir != "" {
		folder = session.QueenDir
	} else {
		folder, _ = FindQueenSessionDir(deps.Home, storageSessionID)
	}
	if !pathExists(folder) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session folder not found"})
		return
	}

	body := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body = map[string]any{}
	}
	description := pyStrOrEmpty(body["description"])
	severity := pyStrOrEmpty(body["severity"])
	if severity == "" {
		severity = "medium"
	}
	if !sessionreport.SeverityValid(severity) {
		severity = "medium"
	}

	marker, err := sessionreport.WriteUserReportMarker(folder, description, severity)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	bundle, stats, err := sessionreport.BuildSessionReportBundle(
		folder, storageSessionID, description, severity, &marker)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	reportsDir := filepath.Join(os.TempDir(), "hive-session-reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	filename := fmt.Sprintf("report_%s_%s.tar.gz",
		time.Now().Format("20060102_150405"), storageSessionID)
	bundlePath := filepath.Join(reportsDir, filename)
	if err := os.WriteFile(bundlePath, bundle, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"filename":     filename,
		"bundle_path":  bundlePath,
		"size":         len(bundle),
		"sha256":       sessionreport.Sha256Hex(bundle),
		"content_type": "application/gzip",
		"marker":       marker,
		"stats": map[string]any{
			"files":           stats.Files,
			"text_files":      stats.TextFiles,
			"binary_files":    stats.BinaryFiles,
			"binary_omitted":  stats.BinaryOmitted,
			"images_stripped": stats.ImagesStripped,
		},
	})
}
