package server

// Parity: framework/server/routes_queens.py (avatar routes)

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/queen"
)

const queenMaxAvatarBytes = 2 * 1024 * 1024

var queenAvatarExtensions = []struct {
	ContentType string
	Ext         string
}{
	{"image/jpeg", ".jpg"},
	{"image/png", ".png"},
}

func queenAvatarExt(contentType string) string {
	for _, entry := range queenAvatarExtensions {
		if entry.ContentType == contentType {
			return entry.Ext
		}
	}
	return ""
}

// Parity: framework/server/routes_queens.py::handle_upload_avatar
func handleQueenUploadAvatar(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	if !queen.ValidQueenID(queenID) {
		queenNotFound(w, queenID)
		return
	}
	queenDir := filepath.Join(deps.queensDir(), queenID)
	if _, err := os.Stat(filepath.Join(queenDir, "profile.yaml")); err != nil {
		queenNotFound(w, queenID)
		return
	}
	reader, err := r.MultipartReader()
	if err != nil {
		// aiohttp raises HTTPUnsupportedMediaType when the request is not
		// multipart/form-data; the error middleware lets it through as 415.
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "Unsupported Media Type"})
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "avatar" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Expected a file field named 'avatar'"})
		return
	}
	defer part.Close()

	contentType := part.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ext := queenAvatarExt(contentType)
	if ext == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("Unsupported image type: %s. Use JPEG or PNG.", contentType),
		})
		return
	}

	data := make([]byte, 0, 64*1024)
	buffer := make([]byte, 8192)
	for {
		n, readErr := part.Read(buffer)
		if n > 0 {
			data = append(data, buffer[:n]...)
			if len(data) > queenMaxAvatarBytes {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": fmt.Sprintf("Image too large. Maximum size is %d MB.", queenMaxAvatarBytes/1024/1024),
				})
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Empty file"})
		return
	}
	if matches, err := filepath.Glob(filepath.Join(queenDir, "avatar.*")); err == nil {
		for _, existing := range matches {
			_ = os.Remove(existing)
		}
	}
	avatarPath := filepath.Join(queenDir, "avatar"+ext)
	if err := os.WriteFile(avatarPath, data, 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	deps.schedulePush("queen_avatar", queenID)
	writeJSON(w, http.StatusOK, map[string]any{
		"avatar_url": "/api/queen/" + queenID + "/avatar",
		"has_avatar": true,
	})
}

// Parity: framework/server/routes_queens.py::handle_get_avatar
func handleQueenGetAvatar(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	if !queen.ValidQueenID(queenID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No avatar found"})
		return
	}
	queenDir := filepath.Join(deps.queensDir(), queenID)
	for _, entry := range queenAvatarExtensions {
		avatarPath := filepath.Join(queenDir, "avatar"+entry.Ext)
		info, err := os.Stat(avatarPath)
		if err != nil || info.IsDir() {
			continue
		}
		file, err := os.Open(avatarPath)
		if err != nil {
			continue
		}
		defer file.Close()
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", mime.TypeByExtension(entry.Ext))
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "No avatar found"})
}

// Parity: framework/server/routes_queens.py::handle_delete_avatar
func handleQueenDeleteAvatar(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	if !queen.ValidQueenID(queenID) {
		writeJSON(w, http.StatusOK, map[string]any{"has_avatar": false})
		return
	}
	queenDir := filepath.Join(deps.queensDir(), queenID)
	if info, err := os.Stat(queenDir); err == nil && info.IsDir() {
		if matches, err := filepath.Glob(filepath.Join(queenDir, "avatar.*")); err == nil {
			for _, existing := range matches {
				_ = os.Remove(existing)
			}
		}
	}
	deps.schedulePush("queen_avatar", queenID)
	writeJSON(w, http.StatusOK, map[string]any{"has_avatar": false})
}

// queenLiveDM reports whether a live session is a DM of this queen: no
// colony, no worker, and (upstream) no channel and no supersession.
