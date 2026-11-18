package server

// Parity: framework/server/routes_sessions.py::handle_session_attachment
// Parity: framework/server/routes_sessions.py::handle_upload_attachment

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/attachments"
)

// Parity: framework/server/routes_sessions.py::handle_session_attachment (mime_map)
var attachmentMIMEMap = map[string]string{
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".png":   "image/png",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".svg":   "image/svg+xml",
	".pdf":   "application/pdf",
	".csv":   "text/csv",
	".tsv":   "text/tab-separated-values",
	".txt":   "text/plain",
	".md":    "text/markdown",
	".json":  "application/json",
	".jsonl": "application/x-ndjson",
	".yaml":  "application/yaml",
	".yml":   "application/yaml",
	".toml":  "application/toml",
	".xml":   "application/xml",
	".html":  "text/html",
	".htm":   "text/html",
	".css":   "text/css",
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".ts":    "text/plain",
	".tsx":   "text/plain",
	".jsx":   "text/plain",
	".py":    "text/plain",
	".rb":    "text/plain",
	".go":    "text/plain",
	".rs":    "text/plain",
	".java":  "text/plain",
	".c":     "text/plain",
	".cpp":   "text/plain",
	".h":     "text/plain",
	".sh":    "text/plain",
	".sql":   "text/plain",
	".ini":   "text/plain",
	".cfg":   "text/plain",
	".conf":  "text/plain",
	".log":   "text/plain",
}

// Parity: framework/server/routes_sessions.py::handle_upload_attachment (ext_map)
var uploadExtByContentType = map[string]string{
	"application/pdf": ".pdf",
	"text/csv":        ".csv",
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
}

// Parity: framework/server/routes_sessions.py::handle_upload_attachment (_TEXT_PREVIEW_CAP_CHARS)
const textPreviewCapChars = 64 * 1024

// Parity: framework/server/routes_sessions.py::handle_upload_attachment (_LARGE_PDF_THRESHOLD_BYTES)
const largePDFThresholdBytes = 10 * 1024 * 1024

func handleSessionAttachment(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")
	filename := r.PathValue("filename")

	if strings.ContainsAny(filename, `/\`) || strings.Contains(filename, "..") {
		writePlainText(w, http.StatusBadRequest, "Invalid filename")
		return
	}

	sessionDirs, err := IterQueenSessionDirs(deps.Home, sessionID)
	if err != nil {
		sessionDirs = nil
	}
	if len(sessionDirs) == 0 {
		fallback, _ := FindQueenSessionDir(deps.Home, sessionID)
		sessionDirs = []string{fallback}
	}

	filepathFound := ""
	for _, sessionDir := range sessionDirs {
		for _, candidateDir := range attachmentCandidateDirs(sessionDir) {
			candidate := filepath.Join(candidateDir, filename)
			if regularFile(candidate) {
				filepathFound = candidate
				break
			}
		}
		if filepathFound != "" {
			break
		}
	}

	if filepathFound == "" && deps.Manager != nil {
		if live := deps.Manager.GetSession(sessionID); live != nil && live.QueenDir != "" {
			for _, candidateDir := range attachmentCandidateDirs(live.QueenDir) {
				candidate := filepath.Join(candidateDir, filename)
				if regularFile(candidate) {
					filepathFound = candidate
					break
				}
			}
		}
	}

	if filepathFound == "" {
		writePlainText(w, http.StatusNotFound, "Attachment not found")
		return
	}

	contentType := attachmentMIMEMap[strings.ToLower(filepath.Ext(filepathFound))]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, filepathFound)
}

func handleUploadAttachment(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")

	var session *Session
	if deps.Manager != nil {
		session = deps.Manager.GetSession(sessionID)
	}
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}

	queenDir := session.QueenDir
	if queenDir == "" {
		resumeID := session.QueenResumeFrom
		if resumeID == "" {
			resumeID = sessionID
		}
		queenDir, _ = FindQueenSessionDir(deps.Home, resumeID)
	}

	attachmentsDir := filepath.Join(queenDir, "data", "attachments")
	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create attachment directory"})
		return
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected a 'file' field"})
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected a 'file' field"})
		return
	}
	defer part.Close()

	originalName := part.FileName()
	if originalName == "" {
		originalName = "upload"
	}

	contentType := part.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, fileExt := pythonSplitExt(originalName)
	fileExt = strings.ToLower(fileExt)

	ext, ok := uploadExtByContentType[contentType]
	if !ok {
		if fileExt != "" {
			ext = fileExt
		} else {
			ext = ".bin"
		}
	}

	base := attachments.SanitizeBasename(originalName, ext, "upload")
	filename := attachments.DisambiguateFilename(attachmentsDir, base)
	storedPath := filepath.Join(attachmentsDir, filename)

	destination, err := os.Create(storedPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not save attachment"})
		return
	}
	_, copyErr := io.Copy(destination, part)
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not save attachment"})
		return
	}

	extractedText := ""
	switch {
	case ext == ".pdf":
		_ = largePDFThresholdBytes
	case ext == ".csv":
		extractedText = csvPreview(storedPath, originalName)
	default:
		if _, textShaped := attachments.TextExtToMIME[ext]; textShaped && ext != ".svg" {
			extractedText = textPreview(storedPath)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"filename":       filename,
		"path":           "data/attachments/" + filename,
		"session_id":     sessionID,
		"content_type":   contentType,
		"original_name":  originalName,
		"extracted_text": extractedText,
		"page_images":    []any{},
	})
}

func attachmentCandidateDirs(sessionDir string) []string {
	dirs := []string{
		filepath.Join(sessionDir, "data", "attachments"),
		filepath.Join(sessionDir, "attachments"),
		sessionDir,
	}
	if colonyRoot := colonyRootForSessionDir(sessionDir); colonyRoot != "" {
		dirs = append(dirs, colonyRoot)
	}
	return dirs
}

func colonyRootForSessionDir(sessionDir string) string {
	parent := filepath.Dir(filepath.Clean(sessionDir))
	for {
		if filepath.Base(filepath.Dir(parent)) == "colonies" {
			return parent
		}
		next := filepath.Dir(parent)
		if next == parent {
			return ""
		}
		parent = next
	}
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func csvPreview(path, originalName string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	reader := csv.NewReader(bufio.NewReader(file))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return ""
	}

	dataRows := make([][]string, 0, 200)
	for len(dataRows) < 200 {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}
		dataRows = append(dataRows, row)
	}
	rest := 0
	for {
		_, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ""
		}
		rest++
	}

	rowCount := len(dataRows) + rest
	lines := []string{
		fmt.Sprintf("[CSV file: %s, %d rows, %d columns]", originalName, rowCount, len(header)),
		strings.Join(header, " | "),
	}
	dashes := make([]string, len(header))
	for i := range dashes {
		dashes[i] = "---"
	}
	lines = append(lines, strings.Join(dashes, " | "))
	for _, row := range dataRows {
		lines = append(lines, strings.Join(row, " | "))
	}
	if rowCount > 200 {
		lines = append(lines, fmt.Sprintf("... (%d more rows truncated)", rowCount-200))
	}
	return strings.Join(lines, "\n")
}

func textPreview(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	buffered := bufio.NewReader(file)
	var builder strings.Builder
	read := 0
	for read < textPreviewCapChars+1 {
		r, _, err := buffered.ReadRune()
		if err != nil {
			break
		}
		builder.WriteRune(r)
		read++
	}
	raw := builder.String()
	runes := []rune(raw)
	if len(runes) > textPreviewCapChars {
		return string(runes[:textPreviewCapChars]) + "\n... (preview truncated)"
	}
	return raw
}

func pythonSplitExt(name string) (string, string) {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return name, ""
	}
	for i := 0; i < dot; i++ {
		if name[i] != '.' {
			return name[:dot], name[dot:]
		}
	}
	return name, ""
}

func writePlainText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
