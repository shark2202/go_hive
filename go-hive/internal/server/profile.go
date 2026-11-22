package server

// Parity: framework/server/routes_config.py::handle_update_profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

var promptSortKeys = []string{"date", "name", "popular"}

func isPromptSortValue(value string) bool {
	for _, allowed := range promptSortKeys {
		if value == allowed {
			return true
		}
	}
	return false
}

// Parity: framework/server/routes_config.py::_normalize_prompt_sort
func NormalizePromptSort(raw any) map[string]any {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"my", "community"} {
		if value, ok := object[key].(string); ok && isPromptSortValue(value) {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pyStrValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, pyReprValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := sortedMapKeys(v)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, pyReprString(key)+": "+pyReprValue(v[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return pyStr(value)
}

func pyReprValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return pyReprString(v)
	case bool, float64, float32, int, int64:
		return pyStr(v)
	case json.Number:
		return v.String()
	case []any, map[string]any:
		return pyStrValue(v)
	}
	return fmt.Sprintf("%v", value)
}

func pyReprString(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteByte(quote)
	for _, r := range s {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if r == rune(quote) {
				builder.WriteByte('\\')
			}
			builder.WriteRune(r)
		}
	}
	builder.WriteByte(quote)
	return builder.String()
}

func profileGet(profile map[string]any, key string) any {
	if value, ok := profile[key]; ok {
		return value
	}
	return ""
}

func profileBody(profile map[string]any) map[string]any {
	return map[string]any{
		"displayName":         profileGet(profile, "displayName"),
		"about":               profileGet(profile, "about"),
		"theme":               profileGet(profile, "theme"),
		"density":             profileGet(profile, "density"),
		"prompt_library_sort": NormalizePromptSort(profile["prompt_library_sort"]),
	}
}

// Parity: framework/server/routes_config.py::handle_get_profile
func ProfileResponse(config map[string]any) (int, map[string]any) {
	raw, present := config["user_profile"]
	if !present {
		return 200, profileBody(map[string]any{})
	}
	profile, ok := raw.(map[string]any)
	if !ok {
		return 500, map[string]any{"error": internalServerError}
	}
	return 200, profileBody(profile)
}

// Parity: framework/server/routes_config.py::handle_update_profile
func UpdateProfile(home hivehome.Home, body map[string]any) (int, map[string]any) {
	displayValue, hasDisplay := body["displayName"]
	aboutValue, hasAbout := body["about"]
	themeValue, themeIsString := body["theme"].(string)
	setTheme := themeIsString && (themeValue == "light" || themeValue == "dark")
	densityValue, densityIsString := body["density"].(string)
	setDensity := densityIsString && (densityValue == "spacious" || densityValue == "compact")
	sortValue, sortIsObject := body["prompt_library_sort"].(map[string]any)
	wantsMutation := hasDisplay || hasAbout || setTheme || setDensity || sortIsObject

	var (
		saved         map[string]any
		profileNotObj bool
	)

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		raw, present := config["user_profile"]
		profile, isObject := raw.(map[string]any)
		if !present {
			profile = map[string]any{}
		} else if !isObject {
			profileNotObj = true
			if wantsMutation {
				return false
			}
			config["user_profile"] = raw
			return true
		}

		if hasDisplay {
			profile["displayName"] = strings.TrimSpace(pyStrValue(displayValue))
		}
		if hasAbout {
			profile["about"] = strings.TrimSpace(pyStrValue(aboutValue))
		}
		if setTheme {
			profile["theme"] = themeValue
		}
		if setDensity {
			profile["density"] = densityValue
		}
		if sortIsObject {
			current := NormalizePromptSort(profile["prompt_library_sort"])
			if current == nil {
				current = map[string]any{}
			}
			for _, key := range []string{"my", "community"} {
				if value, ok := sortValue[key].(string); ok && isPromptSortValue(value) {
					current[key] = value
				}
			}
			if len(current) > 0 {
				profile["prompt_library_sort"] = current
			}
		}
		config["user_profile"] = profile
		saved = profile
		return true
	})
	if err != nil || profileNotObj {
		return 500, map[string]any{"error": internalServerError}
	}

	SyncUserProfileMemory(home, profileGet(saved, "displayName"), profileGet(saved, "about"))
	return 200, profileBody(saved)
}

// Parity: framework/server/routes_config.py::_update_user_profile_memory
func SyncUserProfileMemory(home hivehome.Home, displayName, about any) {
	defer func() { _ = recover() }()

	memDir := memory.GlobalMemoryDir(home.Root)
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		return
	}
	memoryPath := filepath.Join(memDir, "user-profile.md")

	existingBody := ""
	if data, err := os.ReadFile(memoryPath); err == nil {
		existingText := string(data)
		if strings.Contains(existingText, "---\n") {
			parts := strings.SplitN(existingText, "---\n", 3)
			if len(parts) >= 3 {
				existingBody = strings.TrimSpace(parts[2])
			}
		}
	}

	identityLines := make([]string, 0, 2)
	if pyTruthy(displayName) {
		identityLines = append(identityLines, "- **Name:** "+pyStrValue(displayName))
	}
	if pyTruthy(about) {
		identityLines = append(identityLines, "- **About:** "+pyStrValue(about))
	}
	identitySection := ""
	if len(identityLines) > 0 {
		identitySection = "## Identity\n" + strings.Join(identityLines, "\n")
	}

	var newBody string
	switch {
	case existingBody != "" && strings.Contains(existingBody, "## Identity"):
		before := strings.TrimRightFunc(
			strings.SplitN(existingBody, "## Identity", 2)[0], unicode.IsSpace)
		rest := strings.SplitN(existingBody, "## Identity", 2)[1]
		afterParts := strings.SplitN(rest, "\n## ", 2)
		after := ""
		if len(afterParts) > 1 {
			after = "\n## " + afterParts[1]
		}
		newBody = strings.TrimSpace(before + "\n" + identitySection + after)
	case existingBody != "":
		newBody = strings.TrimSpace(identitySection + "\n\n" + existingBody)
	default:
		newBody = identitySection
	}

	body := newBody
	if body == "" {
		body = "No profile information yet."
	}
	description := "User profile information"
	if pyTruthy(displayName) {
		description = "User identity: " + pyStrValue(displayName)
	}
	content := memory.BuildMemoryDocument("User Profile", description, "profile", body)
	_ = os.WriteFile(memoryPath, []byte(content), 0o644)
}

const maxAvatarBytes = 2 * 1024 * 1024

var avatarTypes = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

var avatarProbeOrder = []string{".jpg", ".png", ".webp"}

var avatarContentTypes = map[string]string{
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
}

// Parity-exempt: framework/server/routes_config.py::handle_upload_user_avatar
func UploadUserAvatar(home hivehome.Home, r *http.Request) (int, map[string]any) {
	missingField := func() (int, map[string]any) {
		return 400, map[string]any{"error": "Expected a file field named 'avatar'"}
	}
	reader, err := r.MultipartReader()
	if err != nil {
		return missingField()
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "avatar" {
		return missingField()
	}

	contentType := part.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	ext, allowed := avatarTypes[contentType]
	if !allowed {
		return 400, map[string]any{
			"error": fmt.Sprintf("Unsupported image type: %s. Use JPEG, PNG, or WebP.", contentType),
		}
	}

	var data bytes.Buffer
	chunk := make([]byte, 8192)
	for {
		n, readErr := part.Read(chunk)
		if n > 0 {
			data.Write(chunk[:n])
			if data.Len() > maxAvatarBytes {
				return 400, map[string]any{"error": "Image too large. Maximum size is 2 MB."}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 500, map[string]any{"error": internalServerError}
		}
	}
	if data.Len() == 0 {
		return 400, map[string]any{"error": "Empty file"}
	}

	existing, _ := filepath.Glob(filepath.Join(home.Root, "avatar.*"))
	for _, path := range existing {
		_ = os.Remove(path)
	}
	avatarPath := filepath.Join(home.Root, "avatar"+ext)
	if err := os.WriteFile(avatarPath, data.Bytes(), 0o644); err != nil {
		return 500, map[string]any{"error": internalServerError}
	}
	return 200, map[string]any{"avatar_url": "/api/config/profile/avatar"}
}

// Parity: framework/server/routes_config.py::handle_get_user_avatar
func GetUserAvatar(home hivehome.Home) (path, contentType string, ok bool) {
	for _, ext := range avatarProbeOrder {
		candidate := filepath.Join(home.Root, "avatar"+ext)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, avatarContentTypes[ext], true
		}
	}
	return "", "", false
}
