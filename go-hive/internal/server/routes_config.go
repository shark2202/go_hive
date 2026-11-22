package server

// Parity: framework/server/routes_config.py::register_routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type ConfigRouteDeps struct {
	Home            hivehome.Home
	Sessions        func() []FeatureSession
	Role            RoleWriteDeps
	LLM             LLMRouteDeps
	SentinelRefresh func()
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeConfigBodyRaw(w http.ResponseWriter, r *http.Request) (map[string]any, []byte, bool) {
	invalid := func() (map[string]any, []byte, bool) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return nil, nil, false
	}
	var data []byte
	if r.Body != nil {
		var err error
		data, err = io.ReadAll(r.Body)
		if err != nil {
			return invalid()
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return invalid()
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return invalid()
	}

	body, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return nil, nil, false
	}
	return body, data, true
}

func decodeConfigBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	body, _, ok := decodeConfigBodyRaw(w, r)
	return body, ok
}

func (deps ConfigRouteDeps) sessions() []FeatureSession {
	if deps.Sessions == nil {
		return nil
	}
	return deps.Sessions()
}

// Parity: framework/server/routes_config.py::handle_get_models
func handleGetModels(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ModelsResponse())
	}
}

// Parity: framework/server/routes_config.py::handle_get_sentinel_config
func handleGetSentinel(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, SentinelResponse(HiveConfig(deps.Home)))
	}
}

// Parity: framework/server/routes_config.py::handle_update_sentinel_config
func handleUpdateSentinel(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := UpdateSentinel(deps.Home, body, deps.SentinelRefresh)
		writeJSON(w, status, response)
	}
}

func handleGetFeatures(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, FeaturesResponse(deps.Home))
	}
}

func handleUpdateFeatures(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := UpdateFeatures(deps.Home, body, deps.sessions())
		writeJSON(w, status, response)
	}
}

func handleGetLLMSections(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, LLMSectionsResponse(deps.Home))
	}
}

func handlePutLLMSection(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := PutLLMSection(deps.Home, body, deps.Role)
		writeJSON(w, status, response)
	}
}

func handleGetProviderLibrary(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, ProviderLibraryResponse(deps.Home))
	}
}

func handlePutProviderLibrary(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := PutProviderLibrary(deps.Home, body)
		writeJSON(w, status, response)
	}
}

func handleApplyProviderLibrary(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := ApplyProviderLibrary(deps.Home, body, deps.Role)
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_get_llm_config
func handleGetLLMConfig(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, response := LLMConfigResponse(deps.Home, deps.LLM)
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_update_llm_config
func handleUpdateLLMConfig(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := UpdateLLMConfig(deps.Home, body, deps.LLM)
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_get_rate_limits
func handleGetRateLimits(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, RateLimitsResponse(deps.Home))
	}
}

// Parity: framework/server/routes_config.py::handle_update_rate_limits
func handleUpdateRateLimits(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, raw, ok := decodeConfigBodyRaw(w, r)
		if !ok {
			return
		}
		status, response := UpdateRateLimits(deps.Home, body, orderedLimitsKeys(raw))
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_get_profile
func handleGetProfile(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, response := ProfileResponse(HiveConfig(deps.Home))
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_update_profile
func handleUpdateProfile(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := UpdateProfile(deps.Home, body)
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_upload_user_avatar
func handleUploadAvatar(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, response := UploadUserAvatar(deps.Home, r)
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_get_user_avatar
func handleGetAvatar(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, contentType, ok := GetUserAvatar(deps.Home)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "No avatar found"})
			return
		}
		file, err := os.Open(path)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
	}
}

// Parity: framework/server/routes_config.py::handle_get_external_skills
func handleGetExternalSkills(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, response := ExternalSkillsResponse(deps.Home)
		writeJSON(w, status, response)
	}
}

// Parity: framework/server/routes_config.py::handle_put_external_skills
func handlePutExternalSkills(deps ConfigRouteDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := decodeConfigBody(w, r)
		if !ok {
			return
		}
		status, response := PutExternalSkills(deps.Home, body)
		writeJSON(w, status, response)
	}
}

func RegisterConfigRoutes(mux *http.ServeMux, deps ConfigRouteDeps) {
	mux.HandleFunc("GET /api/config/models", handleGetModels(deps))
	mux.HandleFunc("GET /api/config/sentinel", handleGetSentinel(deps))
	mux.HandleFunc("PUT /api/config/sentinel", handleUpdateSentinel(deps))
	mux.HandleFunc("GET /api/config/features", handleGetFeatures(deps))
	mux.HandleFunc("PUT /api/config/features", handleUpdateFeatures(deps))
	mux.HandleFunc("GET /api/config/memory", handleGetMemorySettings(deps))
	mux.HandleFunc("PUT /api/config/memory", handleUpdateMemorySettings(deps))
	mux.HandleFunc("GET /api/config/llm-sections", handleGetLLMSections(deps))
	mux.HandleFunc("PUT /api/config/llm-sections", handlePutLLMSection(deps))
	mux.HandleFunc("GET /api/config/llm", handleGetLLMConfig(deps))
	mux.HandleFunc("PUT /api/config/llm", handleUpdateLLMConfig(deps))
	mux.HandleFunc("GET /api/config/provider-library", handleGetProviderLibrary(deps))
	mux.HandleFunc("PUT /api/config/provider-library", handlePutProviderLibrary(deps))
	mux.HandleFunc("POST /api/config/provider-library/apply", handleApplyProviderLibrary(deps))
	mux.HandleFunc("GET /api/config/rate-limits", handleGetRateLimits(deps))
	mux.HandleFunc("PUT /api/config/rate-limits", handleUpdateRateLimits(deps))
	mux.HandleFunc("GET /api/config/profile", handleGetProfile(deps))
	mux.HandleFunc("PUT /api/config/profile", handleUpdateProfile(deps))
	mux.HandleFunc("POST /api/config/profile/avatar", handleUploadAvatar(deps))
	mux.HandleFunc("GET /api/config/profile/avatar", handleGetAvatar(deps))
	mux.HandleFunc("GET /api/config/external-skills", handleGetExternalSkills(deps))
	mux.HandleFunc("PUT /api/config/external-skills", handlePutExternalSkills(deps))
}
