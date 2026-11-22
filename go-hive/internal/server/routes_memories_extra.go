package server

// Parity: framework/server/routes_memories.py

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
)

type MemoryRouteDeps struct {
	Home hivehome.Home
}

func RegisterMemoryRoutes(mux *http.ServeMux, deps MemoryRouteDeps) {
	mux.HandleFunc("GET /api/memories", func(w http.ResponseWriter, r *http.Request) {
		handleListMemories(w, r, deps)
	})
	mux.HandleFunc("GET /api/memories/file", func(w http.ResponseWriter, r *http.Request) {
		handleGetMemory(w, r, deps)
	})
	mux.HandleFunc("POST /api/memories/file", func(w http.ResponseWriter, r *http.Request) {
		handleCreateMemory(w, r, deps)
	})
	mux.HandleFunc("PUT /api/memories/file", func(w http.ResponseWriter, r *http.Request) {
		handleUpdateMemory(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/memories/file", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteMemory(w, r, deps)
	})
	mux.HandleFunc("GET /api/memories/history", func(w http.ResponseWriter, r *http.Request) {
		handleMemoryHistory(w, r, deps)
	})
	mux.HandleFunc("POST /api/memories/restore", func(w http.ResponseWriter, r *http.Request) {
		handleRestoreMemory(w, r, deps)
	})
	mux.HandleFunc("GET /api/memories/retired", func(w http.ResponseWriter, r *http.Request) {
		handleRetiredMemories(w, r, deps)
	})
}

func (deps MemoryRouteDeps) memoriesDir() string {
	if deps.Home.Root != "" {
		return hivehome.Home{Root: deps.Home.Root}.MemoriesDir()
	}
	home, err := hivehome.Resolve()
	if err != nil {
		return "memories"
	}
	return home.MemoriesDir()
}

func memoryScopeForRel(parts []string) (string, string) {
	var parent []string
	if len(parts) > 0 {
		parent = parts[:len(parts)-1]
	}
	if len(parent) == 0 {
		return "root", ""
	}
	if parent[0] == "global" {
		return "global", ""
	}
	if len(parent) >= 3 && parent[0] == "agents" && parent[1] == "queens" {
		return "queen", parent[2]
	}
	if parent[0] == "colonies" && len(parent) >= 2 {
		return "colony", parent[1]
	}
	if parent[0] == "agents" && len(parent) >= 2 {
		return "agent", parent[1]
	}
	return strings.Join(parent, "/"), ""
}

func memoryToDict(file memory.MemoryFile, relPath string) map[string]any {
	entry := map[string]any{
		"path":        relPath,
		"filename":    file.Filename,
		"name":        nil,
		"type":        nil,
		"description": nil,
		"author":      nil,
		"learned":     nil,
		"confirmed":   nil,
		"mtime":       file.MTime,
	}
	if file.Name != nil {
		entry["name"] = *file.Name
	}
	if file.Type != nil {
		entry["type"] = *file.Type
	}
	if file.Description != nil {
		entry["description"] = *file.Description
	}
	if file.Author != nil {
		entry["author"] = *file.Author
	}
	if file.Learned != nil {
		entry["learned"] = *file.Learned
	}
	if file.Confirmed != nil {
		entry["confirmed"] = *file.Confirmed
	}
	scope, scopeName := memoryScopeForRel(strings.Split(relPath, "/"))
	entry["scope"] = scope
	entry["scope_name"] = scopeName
	return entry
}

func listMemoryEntries(base string) []map[string]any {
	entries := []map[string]any{}
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		return entries
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		resolvedBase = base
	}
	_ = filepath.WalkDir(resolvedBase, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(resolvedBase, path)
		if err != nil {
			return nil
		}
		relPath := filepath.ToSlash(rel)
		entries = append(entries, memoryToDict(memory.MemoryFileFromPath(path), relPath))
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool {
		leftScope, _ := entries[i]["scope"].(string)
		rightScope, _ := entries[j]["scope"].(string)
		if leftScope != rightScope {
			return leftScope < rightScope
		}
		leftName, _ := entries[i]["scope_name"].(string)
		rightName, _ := entries[j]["scope_name"].(string)
		if leftName != rightName {
			return leftName < rightName
		}
		leftMTime, _ := entries[i]["mtime"].(float64)
		rightMTime, _ := entries[j]["mtime"].(float64)
		return leftMTime > rightMTime
	})
	return entries
}

func handleListMemories(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	writeJSON(w, http.StatusOK, map[string]any{"memories": listMemoryEntries(deps.memoriesDir())})
}

func resolveSafeMemoryPath(base, rel string) (string, bool) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, `\`) {
		return "", false
	}
	normalized := strings.ReplaceAll(rel, `\`, "/")
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if part == "" || part == ".." || part == "." {
			return "", false
		}
	}

	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		resolvedBase, err = filepath.Abs(base)
		if err != nil {
			return "", false
		}
	}
	target := filepath.Join(resolvedBase, filepath.FromSlash(normalized))

	resolvedTarget := target
	if evaluated, err := filepath.EvalSymlinks(target); err == nil {
		resolvedTarget = evaluated
	} else if parent, err := filepath.EvalSymlinks(filepath.Dir(target)); err == nil {
		resolvedTarget = filepath.Join(parent, filepath.Base(target))
	}

	relative, err := filepath.Rel(resolvedBase, resolvedTarget)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	if !strings.EqualFold(filepath.Ext(resolvedTarget), ".md") {
		return "", false
	}
	return resolvedTarget, true
}

func memoryQueryPath(r *http.Request) string {
	return r.URL.Query().Get("path")
}

func handleGetMemory(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	rel := memoryQueryPath(r)
	target, ok := resolveSafeMemoryPath(deps.memoriesDir(), rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid path"})
		return
	}
	info, statErr := os.Stat(target)
	if statErr != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Memory not found"})
		return
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not read memory"})
		return
	}
	payload := memoryToDict(memory.MemoryFileFromPath(target), rel)
	payload["content"] = string(data)
	writeJSON(w, http.StatusOK, payload)
}

func handleCreateMemory(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	rel := memoryQueryPath(r)
	target, ok := resolveSafeMemoryPath(deps.memoriesDir(), rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid path"})
		return
	}

	body, ok := decodeConfigBody(w, r)
	if !ok {
		return
	}

	name, nameIsString := body["name"].(string)
	if !nameIsString || strings.TrimSpace(name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "name must be a non-empty string"})
		return
	}
	memoryBody, bodyIsString := body["body"].(string)
	if !bodyIsString || strings.TrimSpace(memoryBody) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "body must be a non-empty string"})
		return
	}
	description := ""
	if value, isString := body["description"].(string); isString {
		description = value
	}
	memType := "profile"
	if value, isString := body["type"].(string); isString && strings.TrimSpace(value) != "" {
		memType = value
	}

	if _, err := os.Stat(target); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"created": false, "path": rel})
		return
	}

	content := memory.BuildMemoryDocument(name, description, memType, memoryBody)
	if len([]byte(content)) > memory.MaxFileSizeBytes {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Memory exceeds the " + strconv.Itoa(memory.MaxFileSizeBytes) + "-byte limit",
		})
		return
	}

	// A seed from onboarding: nobody's, so her reflection can build on it.
	stamped, err := memory.Write(filepath.Dir(target), filepath.Base(target), content, memory.WriteOptions{
		Author:     memory.AuthorOwner,
		Reason:     "seeded",
		KeepAuthor: true,
	})
	if err != nil {
		if memory.IsTooLarge(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not create memory"})
		return
	}

	payload := memoryToDict(memory.MemoryFileFromPath(target), rel)
	payload["content"] = stamped
	payload["created"] = true
	writeJSON(w, http.StatusOK, payload)
}

func handleUpdateMemory(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	rel := memoryQueryPath(r)
	target, ok := resolveSafeMemoryPath(deps.memoriesDir(), rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid path"})
		return
	}
	info, statErr := os.Stat(target)
	if statErr != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Memory not found"})
		return
	}

	body, ok := decodeConfigBody(w, r)
	if !ok {
		return
	}
	content, contentIsString := body["content"].(string)
	if !contentIsString || strings.TrimSpace(content) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Content must be a non-empty string"})
		return
	}
	if len([]byte(content)) > memory.MaxFileSizeBytes {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "Memory exceeds the " + strconv.Itoa(memory.MaxFileSizeBytes) + "-byte limit",
		})
		return
	}
	stamped, err := memory.Write(filepath.Dir(target), filepath.Base(target), content, memory.WriteOptions{
		Author: memory.AuthorOwner,
		Reason: "edited on the Memory page",
	})
	if err != nil {
		if memory.IsTooLarge(err) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not write memory"})
		return
	}

	payload := memoryToDict(memory.MemoryFileFromPath(target), rel)
	payload["content"] = stamped
	writeJSON(w, http.StatusOK, payload)
}

func handleDeleteMemory(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	rel := memoryQueryPath(r)
	target, ok := resolveSafeMemoryPath(deps.memoriesDir(), rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid path"})
		return
	}
	info, statErr := os.Stat(target)
	if statErr != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Memory not found"})
		return
	}
	if err := memory.Retire(filepath.Dir(target), filepath.Base(target), memory.AuthorOwner, "deleted on the Memory page", time.Time{}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not delete memory"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": rel})
}

// handleMemoryHistory ports routes_memories.py::handle_memory_history:
// GET /api/memories/history?path=… — every change to one memory file, oldest
// first, each with its n in that folder's log.
func handleMemoryHistory(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	rel := memoryQueryPath(r)
	target, ok := resolveSafeMemoryPath(deps.memoriesDir(), rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid path"})
		return
	}
	name := filepath.Base(target)
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    rel,
		"changes": memory.History(filepath.Dir(target), &name),
	})
}

// handleRestoreMemory ports routes_memories.py::handle_restore_memory:
// POST /api/memories/restore?path=… — body {"n": <change>}: the file as that
// change left it.
func handleRestoreMemory(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	rel := memoryQueryPath(r)
	target, ok := resolveSafeMemoryPath(deps.memoriesDir(), rel)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid path"})
		return
	}
	decoded, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	var raw any
	if mapping, isMap := decoded.(map[string]any); isMap {
		raw = mapping["n"]
	}
	// Python: isinstance(n, int) and not isinstance(n, bool) — a JSON number
	// with no fraction, and never `true`/`false` or a numeric string.
	number, valid := 0, false
	if value, isNumber := raw.(json.Number); isNumber {
		if parsed, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			number, valid = int(parsed), true
		}
	}
	if !valid {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "n must be the number of a change in the file's history"})
		return
	}
	content, err := memory.Restore(filepath.Dir(target), filepath.Base(target), number, time.Time{})
	if err != nil {
		if memory.IsNoChange(err) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	payload := memoryToDict(memory.MemoryFileFromPath(target), rel)
	payload["content"] = content
	writeJSON(w, http.StatusOK, payload)
}

// handleRetiredMemories ports routes_memories.py::handle_retired_memories:
// GET /api/memories/retired — files retired and not restored, across scopes,
// with who retired them and why.
func handleRetiredMemories(w http.ResponseWriter, r *http.Request, deps MemoryRouteDeps) {
	entries := []map[string]any{}
	base := deps.memoriesDir()
	if info, err := os.Stat(base); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusOK, map[string]any{"retired": entries})
		return
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		resolvedBase = base
	}
	_ = filepath.WalkDir(resolvedBase, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != memory.LogFile {
			return nil
		}
		logDir := filepath.Dir(path)
		dirRel, relErr := filepath.Rel(resolvedBase, logDir)
		if relErr != nil {
			return nil
		}
		for _, retired := range memory.Retired(logDir) {
			relPath := filepath.ToSlash(filepath.Join(dirRel, retired.File))
			scope, scopeName := memoryScopeForRel(strings.Split(relPath, "/"))
			entries = append(entries, map[string]any{
				"path":       relPath,
				"filename":   retired.File,
				"n":          retired.N,
				"at":         retired.At,
				"author":     retired.Author,
				"reason":     retired.Reason,
				"scope":      scope,
				"scope_name": scopeName,
			})
		}
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool {
		left, _ := entries[i]["at"].(string)
		right, _ := entries[j]["at"].(string)
		return left > right
	})
	writeJSON(w, http.StatusOK, map[string]any{"retired": entries})
}
