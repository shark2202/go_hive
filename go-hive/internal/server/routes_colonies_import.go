package server

// Parity: framework/server/routes_colonies.py::handle_import_colony
// Parity: framework/server/routes_colonies.py::handle_init_upload
// Parity: framework/server/routes_colonies.py::handle_chunk_upload
// Parity: framework/server/routes_colonies.py::handle_upload_status
// Parity: framework/server/routes_colonies.py::handle_finalize_upload
// Parity: framework/server/routes_colonies.py::handle_cancel_upload
// Parity: framework/server/routes_colonies.py::_safe_extract_tar
// Parity: framework/server/routes_colonies.py::_plan_multi_root

import (
	"archive/tar"
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxColonyUploadBytes       int64 = 2 * 1024 * 1024 * 1024
	maxColonyChunkBytes        int64 = 128 * 1024 * 1024
	maxConcurrentColonyUploads       = 32
	colonyUploadTTL                  = 24 * time.Hour
)

type uploadMeta struct {
	Filename        string
	TotalBytes      int64
	SHA256          string
	ReplaceExisting bool
	NameOverride    *string
	CreatedAt       float64
	ReceivedBytes   int64
}

func (m uploadMeta) toMap() map[string]any {
	var nameOverride any
	if m.NameOverride != nil {
		nameOverride = *m.NameOverride
	}
	return map[string]any{
		"filename":         m.Filename,
		"total_bytes":      m.TotalBytes,
		"sha256":           m.SHA256,
		"replace_existing": m.ReplaceExisting,
		"name_override":    nameOverride,
		"created_at":       m.CreatedAt,
		"received_bytes":   m.ReceivedBytes,
	}
}

func uploadMetaFromMap(data map[string]any) uploadMeta {
	meta := uploadMeta{
		Filename:        pythonString(data["filename"]),
		SHA256:          pythonString(data["sha256"]),
		ReplaceExisting: pyTruthy(data["replace_existing"]),
		CreatedAt:       pythonFloat(data["created_at"]),
	}
	meta.TotalBytes, _ = pythonIntLoose(data["total_bytes"])
	meta.ReceivedBytes, _ = pythonIntLoose(data["received_bytes"])
	if override, present := data["name_override"]; present && override != nil {
		text := pythonString(override)
		meta.NameOverride = &text
	}
	return meta
}

func pythonFloat(value any) float64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case float64:
		return typed
	case int:
		return float64(typed)
	case bool:
		if typed {
			return 1
		}
	}
	return 0
}

// pythonIntLoose mirrors the lenient int() calls upstream applies to JSON
// values: numeric strings, floats (truncated), and booleans are all accepted.
func pythonIntLoose(value any) (int64, bool) {
	switch typed := value.(type) {
	case nil:
		return 0, false
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case json.Number:
		text := typed.String()
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed, true
		}
		if parsedFloat, err := strconv.ParseFloat(text, 64); err == nil {
			return int64(parsedFloat), true
		}
		return 0, false
	case float64:
		return int64(typed), true
	case string:
		text := strings.TrimSpace(typed)
		text = strings.ReplaceAll(text, "_", "")
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed, true
		}
		return 0, false
	}
	return 0, false
}

func (d ColonyRouteDeps) maxUploadBytes() int64 {
	if d.MaxUpload > 0 {
		return d.MaxUpload
	}
	return maxColonyUploadBytes
}

func (d ColonyRouteDeps) maxConcurrentUploads() int {
	if d.MaxUploads > 0 {
		return d.MaxUploads
	}
	return maxConcurrentColonyUploads
}

func (d ColonyRouteDeps) uploadTTL() time.Duration {
	if d.UploadTTL > 0 {
		return d.UploadTTL
	}
	return colonyUploadTTL
}

func (d ColonyRouteDeps) stagingDir() string {
	if d.StagingDir != "" {
		return d.StagingDir
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat("/var/tmp"); err == nil && info.IsDir() {
			return filepath.Join("/var/tmp", "hive-colony-uploads")
		}
	}
	return filepath.Join(d.Home.Root, "tmp", "colony_uploads")
}

func (d ColonyRouteDeps) uploadPaths(uploadID string) (string, string) {
	dir := d.stagingDir()
	return filepath.Join(dir, uploadID+".data"), filepath.Join(dir, uploadID+".meta.json")
}

func validUploadID(uploadID string) bool {
	if len(uploadID) != 16 {
		return false
	}
	for _, ch := range uploadID {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return false
	}
	return true
}

func (d ColonyRouteDeps) loadUploadMeta(uploadID string) (uploadMeta, bool) {
	_, metaPath := d.uploadPaths(uploadID)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return uploadMeta{}, false
	}
	decoded, err := decodeJSONBytes(data)
	if err != nil {
		return uploadMeta{}, false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return uploadMeta{}, false
	}
	return uploadMetaFromMap(object), true
}

func (d ColonyRouteDeps) writeUploadMeta(uploadID string, meta uploadMeta) error {
	_, metaPath := d.uploadPaths(uploadID)
	data, err := json.Marshal(meta.toMap())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(metaPath), ".upload-meta-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, metaPath)
}

func (d ColonyRouteDeps) liveUploadCount() int {
	entries, err := os.ReadDir(d.stagingDir())
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".meta.json") {
			count++
		}
	}
	return count
}

func (d ColonyRouteDeps) gcStaleUploads() {
	entries, err := os.ReadDir(d.stagingDir())
	if err != nil {
		return
	}
	now := d.now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		metaPath := filepath.Join(d.stagingDir(), entry.Name())
		meta, ok := d.loadUploadMeta(strings.TrimSuffix(entry.Name(), ".meta.json"))
		if ok && now.Sub(time.Unix(0, int64(meta.CreatedAt*1e9))) < d.uploadTTL() {
			continue
		}
		uploadID := strings.TrimSuffix(entry.Name(), ".meta.json")
		dataPath, _ := d.uploadPaths(uploadID)
		_ = os.Remove(dataPath)
		_ = os.Remove(metaPath)
	}
	entries, err = os.ReadDir(d.stagingDir())
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".data") {
			continue
		}
		uploadID := strings.TrimSuffix(entry.Name(), ".data")
		_, metaPath := d.uploadPaths(uploadID)
		if _, statErr := os.Stat(metaPath); statErr == nil {
			continue
		}
		_ = os.Remove(filepath.Join(d.stagingDir(), entry.Name()))
	}
}

func (d ColonyRouteDeps) newUploadID() (string, error) {
	if d.RandID != nil {
		return d.RandID()
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func handleInitUpload(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	decoded, err := decodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}

	totalBytes, valid := pythonIntLoose(body["total_bytes"])
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "total_bytes must be an integer"})
		return
	}
	if totalBytes <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "total_bytes must be positive"})
		return
	}
	if totalBytes > deps.maxUploadBytes() {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": fmt.Sprintf("total_bytes exceeds %d", deps.maxUploadBytes()),
		})
		return
	}

	sha := strings.ToLower(strings.TrimSpace(pythonString(body["sha256"])))
	if !isHexDigest(sha) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sha256 must be a 64-char hex digest"})
		return
	}

	filename := strings.TrimSpace(pythonString(body["filename"]))
	if filename == "" {
		filename = "upload.tar"
	}
	replaceExisting := pyTruthy(body["replace_existing"])
	var nameOverride *string
	if text := strings.TrimSpace(pythonString(body["name"])); text != "" {
		nameOverride = &text
	}

	if err := os.MkdirAll(deps.stagingDir(), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create staging directory: " + err.Error()})
		return
	}
	deps.gcStaleUploads()
	if deps.liveUploadCount() >= deps.maxConcurrentUploads() {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": "too many concurrent uploads",
			"hint":  fmt.Sprintf("limit is %d; retry after finishing or cancelling one", deps.maxConcurrentUploads()),
		})
		return
	}

	uploadID, err := deps.newUploadID()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not create upload id"})
		return
	}
	meta := uploadMeta{
		Filename:        filename,
		TotalBytes:      totalBytes,
		SHA256:          sha,
		ReplaceExisting: replaceExisting,
		NameOverride:    nameOverride,
		CreatedAt:       float64(deps.now().UnixNano()) / 1e9,
		ReceivedBytes:   0,
	}
	if err := deps.writeUploadMeta(uploadID, meta); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "could not write upload metadata: " + err.Error()})
		return
	}
	dataPath, metaPath := deps.uploadPaths(uploadID)
	file, err := os.OpenFile(dataPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		_ = os.Remove(metaPath)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "could not create staging file: " + err.Error(),
		})
		return
	}
	_ = file.Close()
	writeJSON(w, http.StatusCreated, map[string]any{"upload_id": uploadID, "received": 0})
}

func isHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return false
	}
	return true
}

func handleChunkUpload(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	uploadID := r.PathValue("upload_id")
	if !validUploadID(uploadID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid upload_id"})
		return
	}

	offsetRaw := r.URL.Query().Get("offset")
	if offsetRaw == "" {
		offsetRaw = "-1"
	}
	declaredOffset, offsetErr := strconv.ParseInt(strings.TrimSpace(offsetRaw), 10, 64)
	if offsetErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "offset must be an integer"})
		return
	}
	if declaredOffset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "offset must be >= 0"})
		return
	}

	meta, ok := deps.loadUploadMeta(uploadID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown upload_id"})
		return
	}
	if declaredOffset != meta.ReceivedBytes {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "offset mismatch",
			"declared": declaredOffset,
			"received": meta.ReceivedBytes,
		})
		return
	}

	contentLength := r.ContentLength
	if contentLength < 0 {
		writeJSON(w, http.StatusLengthRequired, map[string]any{"error": "Content-Length required"})
		return
	}
	if contentLength > maxColonyChunkBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "chunk too large"})
		return
	}
	if meta.ReceivedBytes+contentLength > meta.TotalBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":      "chunk would exceed total_bytes",
			"received":   meta.ReceivedBytes,
			"chunk_size": contentLength,
			"total":      meta.TotalBytes,
		})
		return
	}

	body, readErr := io.ReadAll(io.LimitReader(r.Body, contentLength+1))
	if readErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "could not read body"})
		return
	}
	if int64(len(body)) != contentLength {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":    "body length mismatch",
			"expected": contentLength,
			"actual":   len(body),
		})
		return
	}

	dataPath, _ := deps.uploadPaths(uploadID)
	info, statErr := os.Stat(dataPath)
	if errors.Is(statErr, os.ErrNotExist) {
		writeJSON(w, http.StatusGone, map[string]any{"error": "staging file vanished; re-init required"})
		return
	}
	if statErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": statErr.Error()})
		return
	}
	if info.Size() < meta.ReceivedBytes {
		writeJSON(w, http.StatusGone, map[string]any{
			"error":         "staging file shorter than meta says; re-init required",
			"file_size":     info.Size(),
			"meta_received": meta.ReceivedBytes,
		})
		return
	}

	file, openErr := os.OpenFile(dataPath, os.O_RDWR, 0o644)
	if openErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": openErr.Error()})
		return
	}
	if info.Size() > meta.ReceivedBytes {
		if err := file.Truncate(meta.ReceivedBytes); err != nil {
			file.Close()
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}
	if _, err := file.Seek(meta.ReceivedBytes, io.SeekStart); err != nil {
		file.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := file.Sync(); err != nil {
		file.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := file.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	meta.ReceivedBytes += int64(len(body))
	if err := deps.writeUploadMeta(uploadID, meta); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": meta.ReceivedBytes})
}

func handleUploadStatus(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	uploadID := r.PathValue("upload_id")
	if !validUploadID(uploadID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid upload_id"})
		return
	}
	meta, ok := deps.loadUploadMeta(uploadID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown upload_id"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_id":   uploadID,
		"received":    meta.ReceivedBytes,
		"total_bytes": meta.TotalBytes,
		"filename":    meta.Filename,
	})
}

func handleFinalizeUpload(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	uploadID := r.PathValue("upload_id")
	if !validUploadID(uploadID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid upload_id"})
		return
	}
	meta, ok := deps.loadUploadMeta(uploadID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown upload_id"})
		return
	}
	dataPath, metaPath := deps.uploadPaths(uploadID)
	if meta.ReceivedBytes != meta.TotalBytes {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "upload incomplete",
			"received": meta.ReceivedBytes,
			"total":    meta.TotalBytes,
		})
		return
	}

	digest, err := sha256OfFile(dataPath)
	if err != nil {
		_ = os.Remove(metaPath)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "staging file unreadable: " + err.Error(),
		})
		return
	}
	if digest != meta.SHA256 {
		_ = os.Remove(dataPath)
		_ = os.Remove(metaPath)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":    "sha256 mismatch",
			"expected": meta.SHA256,
			"actual":   digest,
		})
		return
	}

	defer func() {
		_ = os.Remove(dataPath)
		_ = os.Remove(metaPath)
	}()
	deps.extractUploadedArchive(w, dataPath, meta.Filename, meta.ReplaceExisting, meta.NameOverride)
}

func handleCancelUpload(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	uploadID := r.PathValue("upload_id")
	if !validUploadID(uploadID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid upload_id"})
		return
	}
	dataPath, metaPath := deps.uploadPaths(uploadID)
	_ = os.Remove(dataPath)
	_ = os.Remove(metaPath)
	w.WriteHeader(http.StatusNoContent)
}

func sha256OfFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type multipartUpload struct {
	Path     string
	Filename string
	Form     map[string]string
	TooLarge bool
}

func readMultipartUpload(w http.ResponseWriter, r *http.Request, capBytes int64) (*multipartUpload, bool) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected multipart/form-data"})
		return nil, false
	}
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected multipart/form-data"})
		return nil, false
	}

	upload := &multipartUpload{Form: map[string]string{}}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expected multipart/form-data"})
			return nil, false
		}
		if part.FormName() == "file" {
			filename := part.FileName()
			tmp, createErr := os.CreateTemp("", "colony-upload-*.tar")
			if createErr != nil {
				part.Close()
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": createErr.Error()})
				return nil, false
			}
			tmpName := tmp.Name()
			written, copyErr := io.Copy(tmp, io.LimitReader(part, capBytes+1))
			part.Close()
			if copyErr != nil {
				tmp.Close()
				_ = os.Remove(tmpName)
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": copyErr.Error()})
				return nil, false
			}
			if written > capBytes {
				tmp.Close()
				_ = os.Remove(tmpName)
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
					"error": fmt.Sprintf("upload exceeds %d bytes", capBytes),
				})
				return nil, false
			}
			if err := tmp.Sync(); err != nil {
				tmp.Close()
				_ = os.Remove(tmpName)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return nil, false
			}
			if err := tmp.Close(); err != nil {
				_ = os.Remove(tmpName)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return nil, false
			}
			upload.Path = tmpName
			upload.Filename = filename
			continue
		}
		data, readErr := io.ReadAll(part)
		part.Close()
		if readErr != nil {
			if upload.Path != "" {
				_ = os.Remove(upload.Path)
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": readErr.Error()})
			return nil, false
		}
		name := part.FormName()
		if name == "" {
			name = ""
		}
		upload.Form[name] = strings.TrimSpace(string(data))
	}
	if upload.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing 'file' part"})
		return nil, false
	}
	return upload, true
}

func handleImportColony(w http.ResponseWriter, r *http.Request, deps ColonyRouteDeps) {
	upload, ok := readMultipartUpload(w, r, deps.maxUploadBytes())
	if !ok {
		return
	}
	defer os.Remove(upload.Path)

	replaceExisting := strings.ToLower(upload.Form["replace_existing"]) == "true"
	var nameOverride *string
	if text := strings.TrimSpace(upload.Form["name"]); text != "" {
		nameOverride = &text
	}
	deps.extractUploadedArchive(w, upload.Path, upload.Filename, replaceExisting, nameOverride)
}

type archiveStream struct {
	file *os.File
	gz   *gzip.Reader
}

func (s *archiveStream) Close() error {
	if s.gz != nil {
		_ = s.gz.Close()
	}
	return s.file.Close()
}

func openColonyArchive(archivePath string) (*tar.Reader, io.Closer, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, err
	}
	buffered := bufio.NewReader(file)
	stream := &archiveStream{file: file}
	header, _ := buffered.Peek(6)
	switch {
	case len(header) >= 2 && header[0] == 0x1f && header[1] == 0x8b:
		gz, err := gzip.NewReader(buffered)
		if err != nil {
			file.Close()
			return nil, nil, err
		}
		stream.gz = gz
		return tar.NewReader(gz), stream, nil
	case len(header) >= 3 && string(header[:3]) == "BZh":
		return tar.NewReader(bzip2.NewReader(buffered)), stream, nil
	case len(header) >= 6 && header[0] == 0xfd && header[1] == 0x37 && header[2] == 0x7a && header[3] == 0x58 && header[4] == 0x5a && header[5] == 0x00:
		file.Close()
		return nil, nil, errors.New("xz compression is not supported")
	case len(header) >= 4 && header[0] == 0x28 && header[1] == 0xb5 && header[2] == 0x2f && header[3] == 0xfd:
		file.Close()
		return nil, nil, errors.New("zstd compression is not supported")
	default:
		return tar.NewReader(buffered), stream, nil
	}
}

func forEachArchiveMember(archivePath string, fn func(*tar.Header) error) error {
	reader, closer, err := openColonyArchive(archivePath)
	if err != nil {
		return err
	}
	defer closer.Close()
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(header); err != nil {
			return err
		}
	}
}

func normaliseMemberName(name string) string {
	return strings.TrimPrefix(name, "./")
}

func memberPathParts(name string) []string {
	parts := []string{}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." {
			continue
		}
		parts = append(parts, segment)
	}
	return parts
}

func containsDotDot(parts []string) bool {
	for _, part := range parts {
		if part == ".." {
			return true
		}
	}
	return false
}

func hasMultiRootPrefix(archivePath string) bool {
	found := false
	_ = forEachArchiveMember(archivePath, func(header *tar.Header) error {
		name := normaliseMemberName(header.Name)
		if strings.HasPrefix(name, "colonies/") || strings.HasPrefix(name, "agents/") ||
			strings.HasPrefix(name, "queens/") || strings.HasPrefix(name, "memories/") {
			found = true
			return io.EOF
		}
		return nil
	})
	return found
}

func archiveTopLevel(archivePath string) (string, string) {
	tops := map[string]struct{}{}
	var scanErr string
	err := forEachArchiveMember(archivePath, func(header *tar.Header) error {
		name := header.Name
		if name == "" || strings.HasPrefix(name, "/") {
			scanErr = "invalid member path: '" + name + "'"
			return io.EOF
		}
		parts := memberPathParts(name)
		if len(parts) == 0 || parts[0] == ".." {
			scanErr = "invalid member path: '" + name + "'"
			return io.EOF
		}
		first := parts[0]
		if first == "." && len(parts) > 1 {
			first = parts[1]
		}
		if first != "" {
			tops[first] = struct{}{}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "invalid tar archive: " + err.Error()
	}
	if scanErr != "" {
		return "", scanErr
	}
	if len(tops) != 1 {
		return "", "archive must contain exactly one top-level directory"
	}
	for top := range tops {
		return top, ""
	}
	return "", "archive must contain exactly one top-level directory"
}

type multiRootGroup struct {
	Prefix string
	Dest   string
}

type multiRootPlan struct {
	Kinds map[string][]multiRootGroup
}

func classifyMultiRootMember(name string) (string, string, bool) {
	parts := memberPathParts(name)
	if len(parts) == 0 {
		return "", "", false
	}
	switch parts[0] {
	case "colonies":
		if len(parts) >= 2 {
			return "colonies", "colonies/" + parts[1], true
		}
	case "queens":
		if len(parts) >= 2 {
			return "queens", "queens/" + parts[1], true
		}
	case "memories":
		return "memories", "memories", true
	case "agents":
		if len(parts) >= 2 {
			if parts[1] == "queens" {
				if len(parts) >= 5 && parts[3] == "sessions" {
					return "agents_queen", "agents/queens/" + parts[2] + "/sessions/" + parts[4], true
				}
				return "", "", false
			}
			if len(parts) >= 3 && parts[2] == "worker" {
				return "agents_worker", "agents/" + parts[1] + "/worker", true
			}
		}
	}
	return "", "", false
}

func planMultiRoot(deps ColonyRouteDeps, archivePath string) (multiRootPlan, string) {
	plan := multiRootPlan{Kinds: map[string][]multiRootGroup{
		"colonies":      {},
		"queens":        {},
		"memories":      {},
		"agents_worker": {},
		"agents_queen":  {},
	}}
	seenPrefixes := map[string]map[string]struct{}{}
	unrecognised := map[string]struct{}{}
	var planErr string

	err := forEachArchiveMember(archivePath, func(header *tar.Header) error {
		name := normaliseMemberName(header.Name)
		parts := memberPathParts(name)
		if name == "" || strings.HasPrefix(name, "/") || containsDotDot(parts) {
			planErr = "invalid member path: '" + header.Name + "'"
			return io.EOF
		}
		kind, prefix, ok := classifyMultiRootMember(name)
		if !ok {
			if len(parts) > 0 {
				unrecognised[parts[0]] = struct{}{}
			}
			return nil
		}
		if seenPrefixes[kind] == nil {
			seenPrefixes[kind] = map[string]struct{}{}
		}
		if _, duplicate := seenPrefixes[kind][prefix]; duplicate {
			return nil
		}
		prefixParts := memberPathParts(prefix)
		var dest string
		switch kind {
		case "colonies":
			if message := validateColonyName(prefixParts[1]); message != "" {
				planErr = message
				return io.EOF
			}
			dest = filepath.Join(deps.Home.ColoniesDir(), prefixParts[1])
		case "queens":
			if message := validateColonySessionSegment(prefixParts[1], "queen id"); message != "" {
				planErr = message
				return io.EOF
			}
			dest = filepath.Join(deps.Home.Root, "queens", prefixParts[1])
		case "memories":
			dest = deps.Home.MemoriesDir()
		case "agents_worker":
			if message := validateColonyName(prefixParts[1]); message != "" {
				planErr = message
				return io.EOF
			}
			dest = filepath.Join(deps.Home.Root, "agents", prefixParts[1], "worker")
		case "agents_queen":
			queen, sessionID := prefixParts[2], prefixParts[4]
			if message := validateColonySessionSegment(queen, "queen name"); message != "" {
				planErr = message
				return io.EOF
			}
			if message := validateColonySessionSegment(sessionID, "queen session id"); message != "" {
				planErr = message
				return io.EOF
			}
			dest = filepath.Join(deps.Home.Root, "agents", "queens", queen, "sessions", sessionID)
		}
		seenPrefixes[kind][prefix] = struct{}{}
		plan.Kinds[kind] = append(plan.Kinds[kind], multiRootGroup{Prefix: prefix, Dest: dest})
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		return plan, "invalid tar archive: " + err.Error()
	}
	if planErr != "" {
		return plan, planErr
	}
	hasGroups := false
	for _, groups := range plan.Kinds {
		if len(groups) > 0 {
			hasGroups = true
			break
		}
	}
	if !hasGroups {
		roots := make([]string, 0, len(unrecognised))
		for root := range unrecognised {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		got := strings.Join(roots, ", ")
		if got == "" {
			got = "(none)"
		}
		return plan, "tar has no recognised top-level prefix " +
			"(expected colonies/, queens/, memories/, agents/<name>/worker/, " +
			"agents/queens/<queen>/sessions/<sid>/; got: " + got + ")"
	}
	return plan, ""
}

func secureTarget(base, rel string) (string, bool) {
	target := filepath.Join(base, rel)
	if !colonyPathWithin(target, base) {
		return "", false
	}
	if _, err := os.Lstat(target); err == nil {
		resolved, resolveErr := filepath.EvalSymlinks(target)
		if resolveErr != nil || !colonyPathWithin(resolved, base) {
			return "", false
		}
		return resolved, true
	}
	parent := filepath.Dir(target)
	existing := parent
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		next := filepath.Dir(existing)
		if next == existing {
			break
		}
		existing = next
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		resolved = existing
	}
	if !colonyPathWithin(resolved, base) {
		return "", false
	}
	for _, segment := range tail {
		resolved = filepath.Join(resolved, segment)
	}
	return filepath.Join(resolved, filepath.Base(target)), true
}

func safeExtractArchive(archivePath, dest, stripPrefix string, exclude []string, atomic bool) (int, string) {
	base, err := filepath.Abs(dest)
	if err != nil {
		return 0, err.Error()
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return 0, err.Error()
	}
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	prefixWithSep := ""
	if stripPrefix != "" {
		prefixWithSep = stripPrefix + "/"
	}

	reader, closer, openErr := openColonyArchive(archivePath)
	if openErr != nil {
		return 0, "invalid tar archive: " + openErr.Error()
	}
	defer closer.Close()

	filesExtracted := 0
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return filesExtracted, "invalid tar archive: " + nextErr.Error()
		}
		name := normaliseMemberName(header.Name)
		if name == "" {
			continue
		}
		rel := name
		if stripPrefix != "" {
			if name == stripPrefix {
				continue
			}
			if !strings.HasPrefix(name, prefixWithSep) {
				continue
			}
			rel = name[len(prefixWithSep):]
		}
		if rel == "" || rel == "." {
			continue
		}
		skip := false
		for _, excluded := range exclude {
			if rel == excluded || strings.HasPrefix(rel, excluded+"/") {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		parts := memberPathParts(rel)
		if containsDotDot(parts) {
			return filesExtracted, "path traversal in member: '" + header.Name + "'"
		}
		switch header.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return filesExtracted, "symlinks/hardlinks not supported: '" + header.Name + "'"
		case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			return filesExtracted, "device/fifo not supported: '" + header.Name + "'"
		}

		target, ok := secureTarget(base, filepath.FromSlash(rel))
		if !ok {
			return filesExtracted, "member escapes destination: '" + header.Name + "'"
		}
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return filesExtracted, err.Error()
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return filesExtracted, "unsupported member: '" + header.Name + "'"
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return filesExtracted, err.Error()
		}

		mode := os.FileMode(0o644)
		if header.Mode != 0 {
			mode = os.FileMode(header.Mode) & 0o755
		}
		if atomic {
			tmp, err := os.CreateTemp(filepath.Dir(target), ".import.*.tmp")
			if err != nil {
				return filesExtracted, err.Error()
			}
			tmpName := tmp.Name()
			if _, err := io.Copy(tmp, reader); err != nil {
				tmp.Close()
				_ = os.Remove(tmpName)
				return filesExtracted, err.Error()
			}
			if err := tmp.Chmod(mode); err != nil {
				tmp.Close()
				_ = os.Remove(tmpName)
				return filesExtracted, err.Error()
			}
			if err := tmp.Close(); err != nil {
				_ = os.Remove(tmpName)
				return filesExtracted, err.Error()
			}
			if err := os.Rename(tmpName, target); err != nil {
				_ = os.Remove(tmpName)
				return filesExtracted, err.Error()
			}
		} else {
			out, err := os.Create(target)
			if err != nil {
				return filesExtracted, err.Error()
			}
			if _, err := io.Copy(out, reader); err != nil {
				out.Close()
				return filesExtracted, err.Error()
			}
			if err := out.Chmod(mode); err != nil {
				out.Close()
				return filesExtracted, err.Error()
			}
			if err := out.Close(); err != nil {
				return filesExtracted, err.Error()
			}
		}
		filesExtracted++
	}
	return filesExtracted, ""
}

func archiveHasMemberPrefix(archivePath, prefix string) bool {
	found := false
	_ = forEachArchiveMember(archivePath, func(header *tar.Header) error {
		if strings.HasPrefix(normaliseMemberName(header.Name), prefix) {
			found = true
			return io.EOF
		}
		return nil
	})
	return found
}

func removeMatchingGlob(dir, pattern string) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return
	}
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func (d ColonyRouteDeps) extractUploadedArchive(w http.ResponseWriter, uploadPath, uploadFilename string, replaceExisting bool, nameOverride *string) {
	if message, valid := archiveLooksValid(uploadPath); !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid tar archive: " + message})
		return
	}
	if hasMultiRootPrefix(uploadPath) {
		d.importMultiRoot(w, uploadPath, uploadFilename, replaceExisting)
		return
	}
	d.importLegacySingleRoot(w, uploadPath, uploadFilename, replaceExisting, nameOverride)
}

func archiveLooksValid(archivePath string) (string, bool) {
	reader, closer, err := openColonyArchive(archivePath)
	if err != nil {
		return err.Error(), false
	}
	defer closer.Close()
	_, err = reader.Next()
	if err == nil || errors.Is(err, io.EOF) {
		return "", true
	}
	return err.Error(), false
}

func (d ColonyRouteDeps) importLegacySingleRoot(w http.ResponseWriter, archivePath, uploadFilename string, replaceExisting bool, nameOverride *string) {
	top, topErr := archiveTopLevel(archivePath)
	if topErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": topErr})
		return
	}
	colonyID := top
	if nameOverride != nil && *nameOverride != "" {
		colonyID = *nameOverride
	}
	if message := validateColonyName(colonyID); message != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": message})
		return
	}
	target := filepath.Join(d.Home.ColoniesDir(), colonyID)
	if _, err := os.Stat(target); err == nil {
		if !replaceExisting {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "colony already exists",
				"name":  colonyID,
				"hint":  "set replace_existing=true to overwrite",
			})
			return
		}
		if err := os.RemoveAll(target); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}

	files, extractErr := safeExtractArchive(archivePath, target, top, nil, false)
	if extractErr != "" {
		_ = os.RemoveAll(target)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": extractErr})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":           colonyID,
		"path":           target,
		"files_imported": files,
		"replaced":       replaceExisting,
	})
}

func (d ColonyRouteDeps) importMultiRoot(w http.ResponseWriter, archivePath, uploadFilename string, replaceExisting bool) {
	plan, planErr := planMultiRoot(d, archivePath)
	if planErr != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": planErr})
		return
	}

	primaryColonyID := ""
	primaryColonyTarget := ""
	for _, group := range plan.Kinds["colonies"] {
		parts := memberPathParts(group.Prefix)
		primaryColonyID = parts[1]
		primaryColonyTarget = group.Dest
		if _, err := os.Stat(group.Dest); err == nil {
			if !replaceExisting {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "colony already exists",
					"name":  primaryColonyID,
					"hint":  "set replace_existing=true to overwrite",
				})
				return
			}
			if err := os.RemoveAll(group.Dest); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
		}
	}
	if len(plan.Kinds["colonies"]) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "tar missing required colonies/<name>/ root"})
		return
	}

	for _, group := range plan.Kinds["queens"] {
		if archiveHasMemberPrefix(archivePath, group.Prefix+"/avatar.") {
			removeMatchingGlob(group.Dest, "avatar.*")
		}
	}

	summary := map[string]map[string]int{}
	extractedDests := []string{}
	abort := func(message string) {
		for _, dest := range extractedDests {
			_ = os.RemoveAll(dest)
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": message})
	}

	for _, kind := range []string{"colonies", "queens", "memories", "agents_worker", "agents_queen"} {
		inPlace := kind == "queens" || kind == "memories"
		for _, group := range plan.Kinds[kind] {
			var exclude []string
			if kind == "queens" {
				exclude = []string{"sessions"}
			}
			files, extractErr := safeExtractArchive(archivePath, group.Dest, group.Prefix, exclude, inPlace)
			if extractErr != "" {
				abort(extractErr)
				return
			}
			if _, ok := summary[kind]; !ok {
				summary[kind] = map[string]int{"files": 0}
			}
			summary[kind]["files"] += files
			if !inPlace {
				extractedDests = append(extractedDests, group.Dest)
			}
		}
	}

	totalFiles := 0
	byRoot := map[string]any{}
	for kind, entry := range summary {
		totalFiles += entry["files"]
		byRoot[kind] = map[string]any{"files": entry["files"]}
	}
	var primaryPath any
	if primaryColonyTarget != "" {
		primaryPath = primaryColonyTarget
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"name":           primaryColonyID,
		"path":           primaryPath,
		"files_imported": totalFiles,
		"by_root":        byRoot,
		"replaced":       replaceExisting,
	})
}
