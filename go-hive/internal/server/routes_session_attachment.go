package server

// Parity: framework/server/routes_sessions.py::handle_session_attachment
// Parity: framework/server/routes_sessions.py::handle_upload_attachment

import (
	"bufio"
	"encoding/csv"
	"errors"
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
		writePlainText(w, http.StatusBadRequest, "Invalid session id")
		return
	}
	if len(sessionDirs) == 0 {
		fallback, fallbackErr := FindQueenSessionDir(deps.Home, sessionID)
		if fallbackErr != nil || fallback == "" {
			writePlainText(w, http.StatusNotFound, "Attachment not found")
			return
		}
		sessionDirs = []string{fallback}
	}

	filepathFound := ""
	for _, sessionDir := range sessionDirs {
		if resolved, ok := findAttachmentFile(deps.Home.Root, sessionDir, filename); ok {
			filepathFound = resolved
			break
		}
	}

	if filepathFound == "" && deps.Manager != nil {
		if live := deps.Manager.GetSession(sessionID); live != nil && live.QueenDir != "" {
			if resolved, ok := findAttachmentFile(deps.Home.Root, live.QueenDir, filename); ok {
				filepathFound = resolved
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
		queenDir, resumeErr := FindQueenSessionDir(deps.Home, resumeID)
		if resumeErr != nil || queenDir == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
			return
		}
	}

	// The session tree is the only place a stored attachment may land.
	// os.Root refuses to traverse a symlink that leaves the resolved queen
	// directory, so a symlinked data/ or attachments/ can no longer redirect
	// the write outside HIVE_HOME.
	storageRoot, ok := resolveAttachmentStorageRoot(deps.Home.Root, queenDir)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create attachment directory"})
		return
	}
	root, err := os.OpenRoot(storageRoot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create attachment directory"})
		return
	}
	defer root.Close()

	const attachmentsRel = "data/attachments"
	if err := root.MkdirAll(attachmentsRel, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create attachment directory"})
		return
	}
	attachmentsDir := filepath.Join(storageRoot, attachmentsRel)

	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentUploadBytes)
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
	storedRel := filepath.Join(attachmentsRel, filename)

	destination, err := root.OpenFile(storedRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		filename = attachments.DisambiguateFilename(attachmentsDir, base)
		storedRel = filepath.Join(attachmentsRel, filename)
		destination, err = root.OpenFile(storedRel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not save attachment"})
		return
	}
	_, copyErr := io.Copy(destination, part)
	closeErr := destination.Close()
	if copyErr != nil || closeErr != nil {
		_ = root.Remove(storedRel)
		var tooLarge *http.MaxBytesError
		if errors.As(copyErr, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "attachment too large"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not save attachment"})
		return
	}

	extractedText := ""
	switch {
	case ext == ".pdf":
		_ = largePDFThresholdBytes
	case ext == ".csv":
		extractedText = withStoredAttachment(root, storedRel, func(content io.Reader) string {
			return csvPreview(content, originalName)
		})
	default:
		if _, textShaped := attachments.TextExtToMIME[ext]; textShaped && ext != ".svg" {
			extractedText = withStoredAttachment(root, storedRel, textPreview)
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

// findAttachmentFile resolves filename under every candidate directory of one
// session tree and returns the first regular file that stays inside HIVE_HOME.
func findAttachmentFile(homeRoot, sessionDir, filename string) (string, bool) {
	for _, candidateDir := range attachmentCandidateDirs(sessionDir) {
		if resolved, ok := resolveAttachmentRead(homeRoot, candidateDir, filename); ok {
			return resolved, true
		}
	}
	return "", false
}

// resolveAttachmentRead returns the on-disk path to serve, or false when the
// file is missing, is not a regular file, or resolves outside HIVE_HOME.
// Resolving the directory (not just the final component) is what stops a
// symlinked data/ or attachments/ directory from redirecting the read.
// Parity-exempt: Go-only symlink hardening; the Python reference serves the
// joined path directly, so no upstream assertion covers this.
func resolveAttachmentRead(homeRoot, dir, filename string) (string, bool) {
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	if !withinHomeRoot(homeRoot, resolvedDir) {
		return "", false
	}
	resolved := filepath.Join(resolvedDir, filename)
	// Lstat, not Stat: an attachment symlink must not serve its target, even
	// when that target stays inside the home.
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

// resolveAttachmentStorageRoot resolves a session directory and reports
// whether it stays inside HIVE_HOME. The resolved path anchors os.Root on a
// real directory, so the later creation of data/attachments cannot start from
// a symlink chain that already left the tree.
// Parity-exempt: Go-only symlink hardening.
func resolveAttachmentStorageRoot(homeRoot, dir string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", false
	}
	if !withinHomeRoot(homeRoot, resolved) {
		return "", false
	}
	return resolved, true
}

// withinHomeRoot reports whether path stays inside the configured HIVE_HOME.
// Both sides are resolved so a symlinked home (macOS /var, for instance) does
// not reject every in-tree path. An unset home leaves no boundary to enforce.
func withinHomeRoot(homeRoot, path string) bool {
	if homeRoot == "" {
		return true
	}
	base, err := filepath.EvalSymlinks(homeRoot)
	if err != nil {
		base = filepath.Clean(homeRoot)
	}
	return pathWithin(path, base)
}

// withStoredAttachment reopens a just-written attachment through the same
// os.Root handle, so the preview cannot read a file outside the session tree
// even if the stored path is swapped for a symlink mid-request.
func withStoredAttachment(root *os.Root, rel string, read func(io.Reader) string) string {
	file, err := root.Open(rel)
	if err != nil {
		return ""
	}
	defer file.Close()
	return read(file)
}

func csvPreview(content io.Reader, originalName string) string {
	text, _ := csvPreviewCounted(content, originalName)
	return text
}

// csvPreviewCounted is csvPreview plus the parsed data-row count, which the
// chat attachment listing needs for its meta string.
//
// Parity: framework/server/routes_execution.py::prepare_queen_input (_parse_csv)
func csvPreviewCounted(content io.Reader, originalName string) (string, int) {
	reader := csv.NewReader(bufio.NewReader(content))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return "", 0
	}

	dataRows := make([][]string, 0, 200)
	for len(dataRows) < 200 {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0
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
			return "", 0
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
	return strings.Join(lines, "\n"), rowCount
}

func textPreview(content io.Reader) string {
	buffered := bufio.NewReader(content)
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
