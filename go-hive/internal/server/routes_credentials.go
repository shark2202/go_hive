package server

// Parity: framework/server/routes_credentials.py::register_routes

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

type credentialRouteStore interface {
	ListCredentials() ([]string, error)
	GetCredential(string, bool) (*credentials.CredentialObject, error)
	SaveCredential(*credentials.CredentialObject) error
	DeleteCredential(string) (bool, error)
}

type CredentialSpecView struct {
	Name                  string
	CredentialID          string
	EnvVar                string
	Description           string
	HelpURL               string
	APIKeyInstructions    string
	Tools                 []string
	ToolModules           []string
	AdenSupported         bool
	DirectAPIKeySupported bool
	CredentialKey         string
	CredentialGroup       string
	AdenProviderName      string
}

type CredentialRouteDeps struct {
	Home                   hivehome.Home
	Store                  credentialRouteStore
	Specs                  func() []CredentialSpecView
	SpecsStore             func() credentialRouteStore
	Accounts               func() map[string][]map[string]any
	Resync                 func() error
	ValidateKey            func(providerID, apiKey string) (map[string]any, error)
	PublishCredentialEvent func(connected bool, credentialID, provider string)
	// SyncEnv mirrors saved credentials into the process environment after a
	// write or resync. Nil installs the store-backed default, which is armed
	// only when the caller supplied Home.
	SyncEnv func()
}

type CredentialRoutes struct {
	deps      CredentialRouteDeps
	homeGiven bool
	storeOnce sync.Once
	store     credentialRouteStore
	storeErr  error
}

func RegisterCredentialRoutes(mux *http.ServeMux, deps CredentialRouteDeps) {
	homeGiven := deps.Home.Root != ""
	if deps.Home.Root == "" {
		if home, err := hivehome.Resolve(); err == nil {
			deps.Home = home
		}
	}
	routes := &CredentialRoutes{deps: deps, homeGiven: homeGiven}
	mux.HandleFunc("GET /api/credentials/specs", routes.handleListSpecs)
	mux.HandleFunc("GET /api/credentials/oauth-status", routes.handleOAuthStatus)
	mux.HandleFunc("POST /api/credentials/resync", routes.handleResync)
	mux.HandleFunc("POST /api/credentials/validate-key", routes.handleValidateKey)
	mux.HandleFunc("GET /api/credentials", routes.handleListCredentials)
	mux.HandleFunc("POST /api/credentials", routes.handleSaveCredential)
	mux.HandleFunc("GET /api/credentials/{credential_id}", routes.handleGetCredential)
	mux.HandleFunc("DELETE /api/credentials/{credential_id}", routes.handleDeleteCredential)
}

func (rt *CredentialRoutes) credentialStore() (credentialRouteStore, error) {
	if rt.deps.Store != nil {
		return rt.deps.Store, nil
	}
	rt.storeOnce.Do(func() {
		// Pick up a persisted key file before the encrypted storage resolves its
		// Fernet key; otherwise a fresh process generates a throwaway key and the
		// credential it just wrote becomes unreadable on the next request.
		//
		// Parity: framework/server/routes_credentials.py::handle_list_specs
		_, _ = credentials.LoadCredentialKey()
		store, err := credentials.WithEncryptedStorage(rt.deps.Home.CredentialsDir())
		rt.store, rt.storeErr = store, err
	})
	return rt.store, rt.storeErr
}

func (rt *CredentialRoutes) specViews() []CredentialSpecView {
	if rt.deps.Specs == nil {
		return nil
	}
	return rt.deps.Specs()
}

// CredentialEnvBindings projects the settings spec rows onto the credential
// store → environment mapping used by the process-wide env sync.
func CredentialEnvBindings(specs []CredentialSpecView) []credentials.EnvBinding {
	bindings := make([]credentials.EnvBinding, 0, len(specs))
	seen := map[string]bool{}
	for _, spec := range specs {
		envVar := strings.TrimSpace(spec.EnvVar)
		credentialID := strings.TrimSpace(spec.CredentialID)
		if credentialID == "" {
			credentialID = strings.TrimSpace(spec.Name)
		}
		if envVar == "" || credentialID == "" || seen[envVar] {
			continue
		}
		seen[envVar] = true
		bindings = append(bindings, credentials.EnvBinding{
			CredentialID:  credentialID,
			EnvVar:        envVar,
			CredentialKey: spec.CredentialKey,
		})
	}
	return bindings
}

// SyncCredentialEnv exports every saved credential into the process
// environment under the env names the settings page advertises. Serve calls
// it once at startup; the credential routes call it again after a successful
// save or resync so a new key takes effect without a restart.
func SyncCredentialEnv(home hivehome.Home) credentials.EnvSyncResult {
	if home.Root == "" {
		return credentials.EnvSyncResult{}
	}
	return credentials.SyncEnvFromStore(
		home.CredentialsDir(),
		CredentialEnvBindings(credentialSpecsFromCatalog()),
	)
}

// syncEnv runs the post-write env refresh. Tests and alternate callers can
// replace it through CredentialRouteDeps.SyncEnv; the default only runs when
// the caller supplied Home, so route tests that use in-memory stores never
// touch a real credential directory.
func (rt *CredentialRoutes) syncEnv() {
	if rt.deps.SyncEnv != nil {
		rt.deps.SyncEnv()
		return
	}
	if !rt.homeGiven {
		return
	}
	specs := rt.specViews()
	if len(specs) == 0 {
		return
	}
	credentials.SyncEnvFromStore(rt.deps.Home.CredentialsDir(), CredentialEnvBindings(specs))
}

func (rt *CredentialRoutes) adenKeyPresent() bool {
	key, _ := credentials.LoadAdenAPIKey()
	return key != ""
}

func (rt *CredentialRoutes) specsStore(specs []CredentialSpecView) credentialRouteStore {
	if rt.deps.SpecsStore != nil {
		return rt.deps.SpecsStore()
	}
	// The reference handler calls ensure_credential_key_env() before building
	// the store, so a key persisted on disk is visible to availability. Without
	// this the encrypted fallback below is skipped whenever the caller did not
	// export HIVE_CREDENTIAL_KEY itself.
	//
	// Parity: framework/server/routes_credentials.py::handle_list_specs
	_, _ = credentials.LoadCredentialKey()
	envMapping := map[string]string{}
	for _, spec := range specs {
		credID := spec.CredentialID
		if credID == "" {
			credID = spec.Name
		}
		envMapping[credID] = spec.EnvVar
	}
	envStorage := credentials.NewEnvVarStorage(envMapping)
	if os.Getenv(credentials.CredentialKeyEnvVar) == "" {
		return credentials.NewCredentialStore(credentials.WithStorage(envStorage))
	}
	encrypted, err := credentials.NewEncryptedFileStorage(rt.deps.Home.CredentialsDir(), "")
	if err != nil {
		return credentials.NewCredentialStore(credentials.WithStorage(envStorage))
	}
	return credentials.NewCredentialStore(
		credentials.WithStorage(credentials.NewCompositeStorage(envStorage, encrypted)),
	)
}

func (rt *CredentialRoutes) accountsByProvider() map[string][]map[string]any {
	if rt.deps.Accounts != nil {
		return rt.deps.Accounts()
	}
	specs := rt.specViews()
	named := make([]credentials.NamedSpec, 0, len(specs))
	for _, spec := range specs {
		named = append(named, credentials.NamedSpec{
			Name: spec.Name,
			Spec: credentials.CredentialSpec{
				CredentialID:     spec.CredentialID,
				AdenProviderName: spec.AdenProviderName,
				Description:      spec.Description,
			},
		})
	}
	store, err := credentials.WithEncryptedStorage(rt.deps.Home.CredentialsDir())
	if err != nil {
		return map[string][]map[string]any{}
	}
	adapter := credentials.NewCredentialStoreAdapter(store, named, credentials.DefaultLocalCredentialRegistry())
	return groupAccountsByProvider(adapter.GetAllAccountInfo())
}

func groupAccountsByProvider(accounts []map[string]any) map[string][]map[string]any {
	grouped := map[string][]map[string]any{}
	for _, acct := range accounts {
		provider := accountField(acct, "provider")
		if provider == "" {
			continue
		}
		identity := acct["identity"]
		if !pythonTruthy(identity) {
			identity = map[string]any{}
		}
		source := any("aden")
		if value, present := acct["source"]; present {
			source = value
		}
		credentialID := provider
		if value, present := acct["credential_id"]; present {
			credentialID = pyStr(value)
		}
		grouped[provider] = append(grouped[provider], map[string]any{
			"provider":      provider,
			"alias":         accountField(acct, "alias"),
			"identity":      identity,
			"source":        source,
			"credential_id": credentialID,
		})
	}
	return grouped
}

func accountField(acct map[string]any, key string) string {
	value, present := acct[key]
	if !present || !pythonTruthy(value) {
		return ""
	}
	return strings.TrimSpace(pyStr(value))
}

func credentialMetadata(cred *credentials.CredentialObject) map[string]any {
	var created any
	if cred.CreatedAt != nil {
		created = credentialTime(*cred.CreatedAt)
	}
	var updated any
	if cred.UpdatedAt != nil {
		updated = credentialTime(*cred.UpdatedAt)
	}
	keyNames := cred.Keys.Names()
	if keyNames == nil {
		keyNames = []string{}
	}
	return map[string]any{
		"credential_id":   cred.ID,
		"credential_type": string(cred.CredentialType),
		"key_names":       keyNames,
		"created_at":      created,
		"updated_at":      updated,
	}
}

func credentialTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func isAvailableForSpecs(store credentialRouteStore, credentialID string) bool {
	if store == nil {
		return false
	}
	credential, err := store.GetCredential(credentialID, false)
	if err != nil {
		return false
	}
	return credential != nil
}

func (rt *CredentialRoutes) publishCredential(connected bool, credentialID string) {
	if rt.deps.PublishCredentialEvent == nil {
		return
	}
	rt.deps.PublishCredentialEvent(connected, credentialID, rt.providerForCredential(credentialID))
}

func (rt *CredentialRoutes) providerForCredential(credentialID string) string {
	return specProvider(rt.specViews(), credentialID)
}

func specProvider(specs []CredentialSpecView, credentialID string) string {
	for _, spec := range specs {
		id := spec.CredentialID
		if id == "" {
			id = spec.Name
		}
		if id == credentialID {
			return spec.AdenProviderName
		}
	}
	return ""
}

// Parity: framework/server/routes_credentials.py::handle_list_credentials
func (rt *CredentialRoutes) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	store, err := rt.credentialStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	ids, err := store.ListCredentials()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	list := make([]any, 0, len(ids))
	unreadable := make([]string, 0)
	for _, id := range ids {
		credential, err := store.GetCredential(id, false)
		if err != nil {
			var decryption *credentials.CredentialDecryptionError
			if errors.As(err, &decryption) {
				unreadable = append(unreadable, id)
				continue
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		if credential != nil {
			list = append(list, credentialMetadata(credential))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credentials":            list,
		"unreadable_credentials": unreadable,
	})
}

// Parity: framework/server/routes_credentials.py::handle_get_credential
func (rt *CredentialRoutes) handleGetCredential(w http.ResponseWriter, r *http.Request) {
	credentialID := r.PathValue("credential_id")
	store, err := rt.credentialStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	credential, err := store.GetCredential(credentialID, false)
	if err != nil {
		var decryption *credentials.CredentialDecryptionError
		if errors.As(err, &decryption) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":         "Credential '" + credentialID + "' could not be decrypted",
				"credential_id": credentialID,
				"recoverable":   true,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if credential == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Credential '" + credentialID + "' not found"})
		return
	}
	writeJSON(w, http.StatusOK, credentialMetadata(credential))
}

// Parity: framework/server/routes_credentials.py::handle_save_credential
func (rt *CredentialRoutes) handleSaveCredential(w http.ResponseWriter, r *http.Request) {
	body, ok := credentialRequestBody(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	credentialID, hasID := body["credential_id"].(string)
	keys, hasKeys := body["keys"].(map[string]any)
	if !hasID || credentialID == "" || !hasKeys || len(keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "credential_id and keys are required"})
		return
	}

	if credentialID == credentials.AdenCredentialID {
		apiKey := ""
		if value, ok := keys["api_key"].(string); ok {
			apiKey = strings.TrimSpace(value)
		}
		if apiKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "api_key is required"})
			return
		}
		if err := credentials.SaveAdenAPIKey(apiKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		rt.publishCredential(true, credentials.AdenCredentialID)
		rt.syncEnv()
		writeJSON(w, http.StatusCreated, map[string]any{"saved": credentials.AdenCredentialID})
		return
	}

	store, err := rt.credentialStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	credential := credentials.NewCredentialObject(credentialID)
	for name, value := range keys {
		credential.Keys.Set(credentials.NewCredentialKey(name, pyStr(value)))
	}
	if err := store.SaveCredential(credential); err != nil {
		if errors.Is(err, credentials.ErrEnvStorageReadOnlySave) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error":  "credentials_store_locked",
				"detail": "Credential store is read-only (HIVE_CREDENTIAL_KEY missing). Restart hive serve to trigger the self-heal recovery.",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	rt.publishCredential(true, credentialID)
	rt.syncEnv()
	writeJSON(w, http.StatusCreated, map[string]any{"saved": credentialID})
}

// Parity: framework/server/routes_credentials.py::handle_delete_credential
func (rt *CredentialRoutes) handleDeleteCredential(w http.ResponseWriter, r *http.Request) {
	credentialID := r.PathValue("credential_id")

	if credentialID == credentials.AdenCredentialID {
		if !credentials.DeleteAdenAPIKey() {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Credential '" + credentials.AdenCredentialID + "' not found"})
			return
		}
		rt.publishCredential(false, credentials.AdenCredentialID)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
		return
	}

	store, err := rt.credentialStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	deletedFromStore, err := store.DeleteCredential(credentialID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	deletedFromEnv := false
	if envVar, ok := providerEnvVarFor(credentialID); ok {
		if _, present := os.LookupEnv(envVar); present {
			_ = os.Unsetenv(envVar)
			deletedFromEnv = true
		}
	}
	if !deletedFromStore && !deletedFromEnv {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Credential '" + credentialID + "' not found"})
		return
	}
	rt.publishCredential(false, credentialID)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// Parity: framework/server/routes_credentials.py::handle_list_specs
func (rt *CredentialRoutes) handleListSpecs(w http.ResponseWriter, r *http.Request) {
	specs := rt.specViews()
	hasAdenKey := rt.adenKeyPresent()
	accountsByProvider := rt.accountsByProvider()
	store := rt.specsStore(specs)
	rows := make([]any, 0, len(specs)+1)
	anyAden := false
	for _, spec := range specs {
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}
		if spec.AdenSupported {
			anyAden = true
		}
		providerKey := spec.AdenProviderName
		if providerKey == "" {
			providerKey = credentialID
		}
		accounts := accountsByProvider[providerKey]
		if accounts == nil {
			accounts = []map[string]any{}
		}
		available := len(accounts) > 0
		if !spec.AdenSupported || spec.DirectAPIKeySupported {
			available = available || isAvailableForSpecs(store, credentialID)
		}
		tools := spec.Tools
		if tools == nil {
			tools = []string{}
		}
		toolModules := spec.ToolModules
		if toolModules == nil {
			toolModules = []string{}
		}
		rows = append(rows, map[string]any{
			"credential_name":          spec.Name,
			"credential_id":            credentialID,
			"env_var":                  spec.EnvVar,
			"description":              spec.Description,
			"help_url":                 spec.HelpURL,
			"api_key_instructions":     spec.APIKeyInstructions,
			"tools":                    tools,
			"tool_modules":             toolModules,
			"aden_supported":           spec.AdenSupported,
			"direct_api_key_supported": spec.DirectAPIKeySupported,
			"credential_key":           spec.CredentialKey,
			"credential_group":         spec.CredentialGroup,
			"available":                available,
			"accounts":                 accounts,
		})
	}
	if anyAden {
		synthetic := map[string]any{
			"credential_name":          "Aden Platform",
			"credential_id":            credentials.AdenCredentialID,
			"env_var":                  "ADEN_API_KEY",
			"description":              "API key from the Developers tab in Settings",
			"help_url":                 "https://hive.adenhq.com/",
			"api_key_instructions":     "1. Go to hive.adenhq.com\n2. Open Settings > Developers\n3. Copy your API key",
			"tools":                    []string{},
			"tool_modules":             []string{},
			"aden_supported":           true,
			"direct_api_key_supported": true,
			"credential_key":           "api_key",
			"credential_group":         "",
			"available":                hasAdenKey,
			"accounts":                 []map[string]any{},
		}
		rows = append([]any{synthetic}, rows...)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"specs":                rows,
		"has_aden_key":         hasAdenKey,
		"accounts_by_provider": accountsByProvider,
	})
}

// Parity: framework/server/routes_credentials.py::handle_oauth_status
func (rt *CredentialRoutes) handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts_by_provider": rt.accountsByProvider(),
		"has_aden_key":         rt.adenKeyPresent(),
		"fetched_at":           time.Now().UnixMilli(),
	})
}

// Parity: framework/server/routes_credentials.py::handle_resync_credentials
func (rt *CredentialRoutes) handleResync(w http.ResponseWriter, r *http.Request) {
	if !rt.adenKeyPresent() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":                "Aden API key not configured",
			"accounts_by_provider": map[string][]map[string]any{},
		})
		return
	}
	if rt.deps.Resync == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal server error during resync"})
		return
	}
	if err := rt.deps.Resync(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Internal server error during resync"})
		return
	}
	rt.publishCredential(true, "<aden_resync>")
	rt.syncEnv()
	writeJSON(w, http.StatusOK, map[string]any{
		"synced":               true,
		"accounts_by_provider": rt.accountsByProvider(),
	})
}

// Parity: framework/server/routes_credentials.py::handle_validate_key
func (rt *CredentialRoutes) handleValidateKey(w http.ResponseWriter, r *http.Request) {
	body, ok := credentialRequestBody(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	providerID := ""
	if value, ok := body["provider_id"].(string); ok {
		providerID = strings.TrimSpace(value)
	}
	apiKey := ""
	if value, ok := body["api_key"].(string); ok {
		apiKey = strings.TrimSpace(value)
	}
	if providerID == "" || apiKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider_id and api_key are required"})
		return
	}
	checker := rt.deps.ValidateKey
	if checker == nil {
		checker = func(providerID, apiKey string) (map[string]any, error) {
			return llm.CheckKey(providerID, apiKey, nil, nil)
		}
	}
	result, err := checker(providerID, apiKey)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":   nil,
			"message": "Validation error: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func credentialRequestBody(r *http.Request) (map[string]any, bool) {
	value, err := decodeJSONValue(r)
	if err != nil {
		return nil, false
	}
	body, ok := value.(map[string]any)
	return body, ok
}
