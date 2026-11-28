package server

// Parity: framework/server/routes_credentials.py::register_routes
//
// The local-account add/remove pair is split out of routes_credentials.go so
// the Integrations page's manual credential form has its own registration
// seam (the integrator mounts it; this file never edits app.go).

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// LocalCredentialRegistry is the slice of credentials.LocalCredentialRegistry
// these routes need. An interface keeps the handlers testable without an
// encrypted store on disk.
type LocalCredentialRegistry interface {
	SaveAccount(credentialID, alias, apiKey string, runHealthCheck bool, extraKeys map[string]string) (credentials.LocalAccountInfo, error)
	DeleteAccount(credentialID, alias string) (bool, error)
}

type LocalCredentialRouteDeps struct {
	Home hivehome.Home
	// Registry overrides the default encrypted registry under
	// Home.CredentialsDir(). Nil builds it lazily on first request.
	Registry LocalCredentialRegistry
	// PublishCredentialEvent mirrors the main credential routes' event hook.
	// Nil skips publication (embedded hosts that mount this pair alone).
	PublishCredentialEvent func(connected bool, credentialID, provider string)
	// ProviderFor maps a credential id to its provider name for the event.
	ProviderFor func(credentialID string) string
}

type LocalCredentialRoutes struct {
	deps LocalCredentialRouteDeps

	once        sync.Once
	registry    LocalCredentialRegistry
	registryErr error
}

// RegisterLocalCredentialRoutes mounts the manual local-credential surface:
//
//	POST   /api/credentials/local
//	DELETE /api/credentials/local/{credential_id}/{alias}
func RegisterLocalCredentialRoutes(mux *http.ServeMux, deps LocalCredentialRouteDeps) {
	if deps.Home.Root == "" {
		if home, err := hivehome.Resolve(); err == nil {
			deps.Home = home
		}
	}
	routes := &LocalCredentialRoutes{deps: deps}
	mux.HandleFunc("POST /api/credentials/local", routes.handleSaveLocalCredential)
	mux.HandleFunc("DELETE /api/credentials/local/{credential_id}/{alias}", routes.handleDeleteLocalCredential)
}

func (rt *LocalCredentialRoutes) localRegistry() (LocalCredentialRegistry, error) {
	if rt.deps.Registry != nil {
		return rt.deps.Registry, nil
	}
	rt.once.Do(func() {
		if rt.deps.Home.Root == "" {
			rt.registryErr = errLocalCredentialHome
			return
		}
		// Pick up a persisted key file before the encrypted storage resolves
		// its Fernet key, matching the main credential routes' store init.
		_, _ = credentials.LoadCredentialKey()
		rt.registry, rt.registryErr = credentials.LocalCredentialRegistryAtPath(rt.deps.Home.CredentialsDir())
	})
	return rt.registry, rt.registryErr
}

var errLocalCredentialHome = errors.New("credential storage is unavailable: HIVE_HOME is not resolved")

// handleSaveLocalCredential mirrors handle_save_local_credential: any provider
// id (known or custom), an optional account alias for multi-account, and one
// or more key fields. Secrets are stored encrypted and never travel back.
//
// Parity: framework/server/routes_credentials.py::handle_save_local_credential
func (rt *LocalCredentialRoutes) handleSaveLocalCredential(w http.ResponseWriter, r *http.Request) {
	value, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := value.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	credentialID := strings.TrimSpace(localBodyString(body, "credential_id"))
	account := localAccountAlias(body["account"])
	keys, ok := body["keys"].(map[string]any)

	if !validLocalCredentialID(credentialID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a valid credential_id is required"})
		return
	}
	if strings.ContainsAny(account, " /\\\x00") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "account alias must not contain spaces or slashes"})
		return
	}
	if !ok || len(keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "non-empty keys are required"})
		return
	}

	normalized := normalizeLocalKeys(keys)
	if len(normalized) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no non-empty values provided"})
		return
	}
	primary, extra := splitLocalKeys(normalized)

	registry, err := rt.localRegistry()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to store credential: " + err.Error()})
		return
	}
	info, err := saveLocalAccount(registry, credentialID, account, primary, extra)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to store credential: " + err.Error()})
		return
	}

	var valid any
	var message any
	if info.Health != nil {
		valid = info.Health.Valid
		message = info.Health.Message
	}

	rt.publishCredential(true, credentialID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"saved":    credentialID + "/" + account,
		"status":   info.Status,
		"identity": info.Identity.ToDict(),
		"valid":    valid,
		"message":  message,
	})
}

// handleDeleteLocalCredential mirrors handle_delete_local_credential: one
// named local account, addressed by its storage id <credential_id>/<alias>.
//
// Parity: framework/server/routes_credentials.py::handle_delete_local_credential
func (rt *LocalCredentialRoutes) handleDeleteLocalCredential(w http.ResponseWriter, r *http.Request) {
	credentialID := r.PathValue("credential_id")
	alias := r.PathValue("alias")
	// Path values are user input. Reject separators here as well as in the
	// registry so an encoded slash cannot address a different account.
	if !validLocalCredentialID(credentialID) || !validLocalAccountAlias(alias) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "a valid credential_id and alias are required"})
		return
	}

	registry, err := rt.localRegistry()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to delete credential: " + err.Error()})
		return
	}
	deleted, err := registry.DeleteAccount(credentialID, alias)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Failed to delete credential: " + err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "Local account '" + credentialID + "/" + alias + "' not found",
		})
		return
	}

	rt.publishCredential(false, credentialID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// saveLocalAccount mirrors upstream _save_local_credential: providers with a
// registered checker are validated before the route reports success. The
// checker runs inside the registry, matching registry.save_account
// (run_health_check=True); custom providers without a checker keep the
// active/null-health shape.
func saveLocalAccount(
	registry LocalCredentialRegistry,
	credentialID, account, primary string,
	extra map[string]string,
) (credentials.LocalAccountInfo, error) {
	runHealthCheck := credentials.HasHealthChecker(credentialID)
	return registry.SaveAccount(credentialID, account, primary, runHealthCheck, extra)
}

func (rt *LocalCredentialRoutes) publishCredential(connected bool, credentialID string) {
	if rt.deps.PublishCredentialEvent == nil {
		return
	}
	provider := ""
	if rt.deps.ProviderFor != nil {
		provider = rt.deps.ProviderFor(credentialID)
	}
	rt.deps.PublishCredentialEvent(connected, credentialID, provider)
}

func localBodyString(body map[string]any, key string) string {
	value, present := body[key]
	if !present || value == nil {
		return ""
	}
	return pyStrValue(value)
}

func localAccountAlias(value any) string {
	account := ""
	if pythonTruthy(value) {
		account = strings.TrimSpace(pyStrValue(value))
	}
	if account == "" {
		return "default"
	}
	return account
}

func validLocalCredentialID(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, " /\\\x00")
}

func validLocalAccountAlias(value string) bool {
	return value != "" && validLocalCredentialID(value)
}

func normalizeLocalKeys(keys map[string]any) map[string]string {
	normalized := make(map[string]string, len(keys))
	for name, value := range keys {
		text := pyStrValue(value)
		if strings.TrimSpace(text) != "" {
			normalized[name] = text
		}
	}
	return normalized
}

// localPrimaryKeyHints mirrors _PRIMARY_KEY_HINTS: the first field name that
// looks like a primary key supplies the credential's default key.
var localPrimaryKeyHints = []string{"api_key", "token", "access_token", "password", "secret", "value", "key"}

// splitLocalKeys chooses the value stored as api_key and keeps every other
// field under its real name. Map iteration order is not JSON order in Go, so
// when no hint matches the lexicographically first field is used; the result
// is deterministic and every field still survives.
func splitLocalKeys(keys map[string]string) (string, map[string]string) {
	primaryName := ""
	for _, hint := range localPrimaryKeyHints {
		if _, present := keys[hint]; present {
			primaryName = hint
			break
		}
	}
	if primaryName == "" {
		names := make([]string, 0, len(keys))
		for name := range keys {
			names = append(names, name)
		}
		sort.Strings(names)
		primaryName = names[0]
	}

	extra := make(map[string]string, len(keys))
	for name, value := range keys {
		if name == "api_key" {
			continue
		}
		extra[name] = value
	}
	return keys[primaryName], extra
}
