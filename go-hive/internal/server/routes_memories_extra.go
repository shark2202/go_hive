package server

// Parity: framework/server/routes_memories.py

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not create memory"})
		return
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not create memory"})
		return
	}

	payload := memoryToDict(memory.MemoryFileFromPath(target), rel)
	payload["content"] = content
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
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not write memory"})
		return
	}

	payload := memoryToDict(memory.MemoryFileFromPath(target), rel)
	payload["content"] = content
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
	if err := os.Remove(target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Could not delete memory"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": rel})
}
