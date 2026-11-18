package server

// Parity: framework/server/routes_prompts.py

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

const (
	customPromptsFilename = "custom_prompts.json"
	promptMaxItems        = 10
	promptMaxItemLen      = 64
)

type PromptRouteDeps struct {
	Home hivehome.Home
	Now  func() time.Time
}

func (deps PromptRouteDeps) clock() time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}

func (deps PromptRouteDeps) root() string {
	if deps.Home.Root != "" {
		return deps.Home.Root
	}
	home, err := hivehome.Resolve()
	if err != nil {
		return "."
	}
	return home.Root
}

func RegisterPromptRoutes(mux *http.ServeMux, deps PromptRouteDeps) {
	mux.HandleFunc("GET /api/prompts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"prompts": loadCustomPrompts(deps.root())})
	})
	mux.HandleFunc("POST /api/prompts", func(w http.ResponseWriter, r *http.Request) {
		handleCreatePrompt(w, r, deps)
	})
	mux.HandleFunc("PATCH /api/prompts/{prompt_id}", func(w http.ResponseWriter, r *http.Request) {
		handleUpdatePrompt(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/prompts/{prompt_id}", func(w http.ResponseWriter, r *http.Request) {
		handleDeletePrompt(w, r, deps)
	})
}

func customPromptsPath(root string) string {
	return filepath.Join(root, customPromptsFilename)
}

func loadCustomPrompts(root string) []any {
	data, err := os.ReadFile(customPromptsPath(root))
	if err != nil {
		return []any{}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return []any{}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return []any{}
	}
	if list, ok := parsed.([]any); ok {
		return list
	}
	return []any{}
}

func saveCustomPrompts(root string, prompts []any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(prompts); err != nil {
		return err
	}
	return atomicfs.WriteFile(customPromptsPath(root), buffer.Bytes(), 0o644)
}

func promptTextValue(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "", true
	case string:
		return strings.TrimSpace(typed), true
	case bool:
		if !typed {
			return "", true
		}
		return "", false
	case json.Number:
		number, err := typed.Float64()
		if err == nil && number == 0 {
			return "", true
		}
		return "", false
	case float64:
		if typed == 0 {
			return "", true
		}
		return "", false
	case float32:
		if typed == 0 {
			return "", true
		}
		return "", false
	case int:
		if typed == 0 {
			return "", true
		}
		return "", false
	case int64:
		if typed == 0 {
			return "", true
		}
		return "", false
	case []any:
		if len(typed) == 0 {
			return "", true
		}
		return "", false
	case map[string]any:
		if len(typed) == 0 {
			return "", true
		}
		return "", false
	}
	return "", false
}

func normalizePromptStrList(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return []string{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(text))
		runes := []rune(value)
		if len(runes) > promptMaxItemLen {
			value = string(runes[:promptMaxItemLen])
		}
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= promptMaxItems {
			break
		}
	}
	return out
}

func handleCreatePrompt(w http.ResponseWriter, r *http.Request, deps PromptRouteDeps) {
	body, ok := decodeConfigBody(w, r)
	if !ok {
		return
	}

	title, titleOK := promptTextValue(body["title"])
	category, categoryOK := promptTextValue(body["category"])
	content, contentOK := promptTextValue(body["content"])
	if !titleOK || !categoryOK || !contentOK {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if title == "" || content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Title and content are required"})
		return
	}
	if category == "" {
		category = "custom"
	}

	prompts := loadCustomPrompts(deps.root())
	newPrompt := map[string]any{
		"id":       "custom_" + strconv.FormatInt(deps.clock().UnixMilli(), 10),
		"title":    title,
		"category": category,
		"content":  content,
		"skills":   normalizePromptStrList(body["skills"]),
		"tags":     normalizePromptStrList(body["tags"]),
		"custom":   true,
	}
	prompts = append(prompts, newPrompt)
	if err := saveCustomPrompts(deps.root(), prompts); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusCreated, newPrompt)
}

func handleUpdatePrompt(w http.ResponseWriter, r *http.Request, deps PromptRouteDeps) {
	body, ok := decodeConfigBody(w, r)
	if !ok {
		return
	}

	updates := map[string]any{}
	if raw, present := body["title"]; present {
		title, valid := promptTextValue(raw)
		if !valid {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Title cannot be empty"})
			return
		}
		updates["title"] = title
	}
	if raw, present := body["category"]; present {
		category, valid := promptTextValue(raw)
		if !valid {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if category == "" {
			category = "custom"
		}
		updates["category"] = category
	}
	if raw, present := body["content"]; present {
		content, valid := promptTextValue(raw)
		if !valid {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if content == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Content cannot be empty"})
			return
		}
		updates["content"] = content
	}
	if raw, present := body["skills"]; present {
		updates["skills"] = normalizePromptStrList(raw)
	}
	if raw, present := body["tags"]; present {
		updates["tags"] = normalizePromptStrList(raw)
	}
	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "No editable fields provided"})
		return
	}

	promptID := r.PathValue("prompt_id")
	prompts := loadCustomPrompts(deps.root())
	for _, entry := range prompts {
		prompt, isObject := entry.(map[string]any)
		if !isObject {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		id, isString := prompt["id"].(string)
		if !isString || id != promptID {
			continue
		}
		for key, value := range updates {
			prompt[key] = value
		}
		if err := saveCustomPrompts(deps.root(), prompts); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		writeJSON(w, http.StatusOK, prompt)
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "Prompt not found"})
}

func handleDeletePrompt(w http.ResponseWriter, r *http.Request, deps PromptRouteDeps) {
	promptID := r.PathValue("prompt_id")
	prompts := loadCustomPrompts(deps.root())
	before := len(prompts)
	kept := make([]any, 0, len(prompts))
	for _, entry := range prompts {
		prompt, isObject := entry.(map[string]any)
		if !isObject {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if id, isString := prompt["id"].(string); isString && id == promptID {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == before {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Prompt not found"})
		return
	}
	if err := saveCustomPrompts(deps.root(), kept); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": promptID})
}
