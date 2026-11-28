package credentials

// Parity: framework/credentials/local/registry.py, framework/credentials/local/models.py

import (
	"fmt"
	"sort"
	"time"
)

const localAccountSeparator = "/"

type LocalAccountInfo struct {
	CredentialID  string
	Alias         string
	Status        string
	Identity      CredentialIdentity
	LastValidated *time.Time
	CreatedAt     *time.Time
	// Health is the health-check result that produced Status/Identity, or nil
	// when the save did not run a check (upstream returns that as a separate
	// tuple element and the route reports valid/message from it).
	Health *HealthCheckResult
}

func (i LocalAccountInfo) StorageID() string {
	return i.CredentialID + localAccountSeparator + i.Alias
}

func (i LocalAccountInfo) ToAccountDict() map[string]any {
	return map[string]any{
		"provider":       i.CredentialID,
		"alias":          i.Alias,
		"identity":       i.Identity.ToDict(),
		"integration_id": nil,
		"source":         "local",
		"status":         i.Status,
	}
}

// LocalHealthChecker validates one local account's primary key against the
// provider. extraKeys carries the submitted form fields (cse_id today), matching
// upstream check_credential_health's **kwargs.
type LocalHealthChecker func(credentialName, credentialValue string, extraKeys map[string]string) HealthCheckResult

type LocalCredentialRegistry struct {
	storage     CredentialStorage
	healthCheck LocalHealthChecker
}

func NewLocalCredentialRegistry(storage CredentialStorage) *LocalCredentialRegistry {
	return &LocalCredentialRegistry{storage: storage, healthCheck: defaultLocalHealthChecker}
}

func defaultLocalHealthChecker(credentialName, credentialValue string, extraKeys map[string]string) HealthCheckResult {
	return CheckCredentialHealthWithExtra(credentialName, credentialValue, extraKeys)
}

func DefaultLocalCredentialRegistry() LocalRegistry {
	storage, err := NewEncryptedFileStorage("", "")
	if err != nil {
		return (*LocalCredentialRegistry)(nil)
	}
	return &LocalCredentialRegistry{storage: storage}
}

func LocalCredentialRegistryAtPath(path string) (*LocalCredentialRegistry, error) {
	storage, err := NewEncryptedFileStorage(path, "")
	if err != nil {
		return nil, err
	}
	return &LocalCredentialRegistry{storage: storage}, nil
}

func (r *LocalCredentialRegistry) store() CredentialStorage {
	if r == nil {
		return nil
	}
	return r.storage
}

func (r *LocalCredentialRegistry) ListAccounts(credentialID string) ([]LocalAccountInfo, error) {
	storage := r.store()
	if storage == nil {
		return nil, nil
	}
	allIDs, err := storage.ListAll()
	if err != nil {
		return nil, err
	}
	accounts := make([]LocalAccountInfo, 0)
	for _, storageID := range allIDs {
		if !containsByte(storageID, '/') {
			continue
		}
		cred, err := storage.Load(storageID)
		if err != nil {
			continue
		}
		if cred == nil {
			continue
		}
		info := toLocalAccountInfo(cred)
		if info == nil {
			continue
		}
		if credentialID != "" && info.CredentialID != credentialID {
			continue
		}
		accounts = append(accounts, *info)
	}
	sort.SliceStable(accounts, func(i, j int) bool {
		if accounts[i].CredentialID != accounts[j].CredentialID {
			return accounts[i].CredentialID < accounts[j].CredentialID
		}
		return accounts[i].Alias < accounts[j].Alias
	})
	return accounts, nil
}

// SaveAccount stores a named local account, optionally validating the key
// first. With runHealthCheck the checker runs against the live API, its
// identity fields are persisted, and a failure still saves with
// status="failed"; an unexpected checker failure saves status="unknown".
//
// Parity: core/framework/credentials/local/registry.py::LocalCredentialRegistry.save_account
func (r *LocalCredentialRegistry) SaveAccount(
	credentialID, alias, apiKey string,
	runHealthCheck bool,
	extraKeys map[string]string,
) (LocalAccountInfo, error) {
	if alias == "" {
		alias = "default"
	}
	status := "active"
	var health *HealthCheckResult
	identityFields := map[string]string{}
	var lastRefreshed *time.Time

	if runHealthCheck {
		now := time.Now().UTC()
		lastRefreshed = &now
		checker := r.healthCheck
		if checker == nil {
			checker = defaultLocalHealthChecker
		}
		result, completed := runLocalHealthCheck(checker, credentialID, apiKey, extraKeys)
		if !completed {
			// Upstream's Exception branch: the account is still saved so the
			// user can retry validation later.
			status = "unknown"
		} else {
			health = &result
			if result.Valid {
				status = "active"
			} else {
				status = "failed"
			}
			identityFields = localHealthIdentity(result.Details)
		}
	}

	storageID := credentialID + localAccountSeparator + alias
	cred := NewCredentialObject(storageID)
	cred.SetKey("api_key", apiKey, nil, nil)
	cred.SetKey("_alias", alias, nil, nil)
	cred.SetKey("_integration_type", credentialID, nil, nil)
	cred.SetKey("_status", status, nil, nil)
	for key, value := range extraKeys {
		cred.SetKey(key, value, nil, nil)
	}
	if len(identityFields) > 0 {
		cred.SetIdentity(identityFields)
	}
	if lastRefreshed != nil {
		cred.LastRefreshed = lastRefreshed
	}

	if storage := r.store(); storage != nil {
		if err := storage.Save(cred); err != nil {
			return LocalAccountInfo{}, err
		}
	}

	return LocalAccountInfo{
		CredentialID:  credentialID,
		Alias:         alias,
		Status:        status,
		Identity:      cred.Identity(),
		LastValidated: lastRefreshed,
		CreatedAt:     cred.CreatedAt,
		Health:        health,
	}, nil
}

// runLocalHealthCheck isolates a checker panic so it degrades to the unknown
// status, the Go equivalent of upstream catching Exception around the check.
func runLocalHealthCheck(
	checker LocalHealthChecker,
	credentialName, credentialValue string,
	extraKeys map[string]string,
) (result HealthCheckResult, completed bool) {
	completed = true
	defer func() {
		if recover() != nil {
			completed = false
		}
	}()
	return checker(credentialName, credentialValue, extraKeys), true
}

// localHealthIdentity keeps only CredentialIdentity's fields from the checker's
// details (health details may also carry team/company/status metadata).
// Parity: core/framework/credentials/local/registry.py::save_account identity filter
func localHealthIdentity(details map[string]any) map[string]string {
	raw, ok := details["identity"].(map[string]any)
	if !ok {
		return nil
	}
	fields := make(map[string]string, len(raw))
	for _, name := range []string{"email", "username", "workspace", "account_id"} {
		if value, ok := raw[name].(string); ok && value != "" {
			fields[name] = value
		}
	}
	return fields
}

// LocalAccountNotFoundError reports a missing local account, the Go projection
// of upstream's KeyError from validate_account.
type LocalAccountNotFoundError struct {
	CredentialID string
	Alias        string
}

func (e *LocalAccountNotFoundError) Error() string {
	return fmt.Sprintf("No local account found: %s/%s", e.CredentialID, e.Alias)
}

// ValidateAccount re-runs the health check for a stored account, updates its
// status/identity/last_refreshed in place, and returns the live result. A
// missing account is an error; a stored account without an api_key returns an
// invalid result (upstream's explicit message) rather than an error. Checker
// panics degrade to valid=false with the upstream "Health check error" message.
//
// Upstream exposes this only on LocalCredentialRegistry; routes_credentials.py
// has no validate-account HTTP route, so none is added here.
//
// Parity: core/framework/credentials/local/registry.py::LocalCredentialRegistry.validate_account
func (r *LocalCredentialRegistry) ValidateAccount(credentialID, alias string) (HealthCheckResult, error) {
	credential, err := r.GetAccount(credentialID, alias)
	if err != nil {
		return HealthCheckResult{}, err
	}
	if credential == nil {
		return HealthCheckResult{}, &LocalAccountNotFoundError{CredentialID: credentialID, Alias: alias}
	}

	apiKey, _ := credential.GetKeyValue("api_key")
	if apiKey == "" {
		return HealthCheckResult{Valid: false, Message: "No api_key stored for this account"}, nil
	}

	extraKeys := map[string]string{}
	if cseID, ok := credential.GetKeyValue("cse_id"); ok && cseID != "" {
		extraKeys["cse_id"] = cseID
	}
	checker := r.healthCheck
	if checker == nil {
		checker = defaultLocalHealthChecker
	}

	result, panicValue := runLocalHealthCheckResult(checker, credentialID, apiKey, extraKeys)
	if panicValue != nil {
		message := panicValue.Error()
		result = HealthCheckResult{
			Valid:   false,
			Message: "Health check error: " + message,
			Details: map[string]any{"error": message},
		}
	}

	status := "failed"
	if result.Valid {
		status = "active"
	}
	credential.SetKey("_status", status, nil, nil)
	now := time.Now().UTC()
	credential.LastRefreshed = &now
	if identity := localHealthIdentity(result.Details); len(identity) > 0 {
		credential.SetIdentity(identity)
	}

	if storage := r.store(); storage != nil {
		if err := storage.Save(credential); err != nil {
			return result, err
		}
	}
	return result, nil
}

// runLocalHealthCheckResult captures a checker panic as an error so
// ValidateAccount can report it without crashing the caller.
func runLocalHealthCheckResult(
	checker LocalHealthChecker,
	credentialName, credentialValue string,
	extraKeys map[string]string,
) (result HealthCheckResult, panicValue error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if err, ok := recovered.(error); ok {
				panicValue = err
			} else {
				panicValue = fmt.Errorf("%v", recovered)
			}
		}
	}()
	return checker(credentialName, credentialValue, extraKeys), nil
}

func (r *LocalCredentialRegistry) GetAccount(credentialID, alias string) (*CredentialObject, error) {
	storage := r.store()
	if storage == nil {
		return nil, nil
	}
	return storage.Load(credentialID + localAccountSeparator + alias)
}

func (r *LocalCredentialRegistry) GetKey(credentialID, alias, keyName string) (string, bool, error) {
	cred, err := r.GetAccount(credentialID, alias)
	if err != nil {
		return "", false, err
	}
	if cred == nil {
		return "", false, nil
	}
	value, ok := cred.GetKeyValue(keyName)
	return value, ok, nil
}

func (r *LocalCredentialRegistry) GetAccountInfo(credentialID, alias string) (*LocalAccountInfo, error) {
	cred, err := r.GetAccount(credentialID, alias)
	if err != nil || cred == nil {
		return nil, err
	}
	return toLocalAccountInfo(cred), nil
}

func (r *LocalCredentialRegistry) DeleteAccount(credentialID, alias string) (bool, error) {
	storage := r.store()
	if storage == nil {
		return false, nil
	}
	return storage.Delete(credentialID + localAccountSeparator + alias)
}

func toLocalAccountInfo(cred *CredentialObject) *LocalAccountInfo {
	integrationKey := cred.Keys.Get("_integration_type")
	if integrationKey == nil {
		return nil
	}
	credentialID := integrationKey.GetSecretValue()

	alias := ""
	if aliasKey := cred.Keys.Get("_alias"); aliasKey != nil {
		alias = aliasKey.GetSecretValue()
	} else {
		alias = afterFirstSeparator(cred.ID)
	}

	status := "unknown"
	if statusKey := cred.Keys.Get("_status"); statusKey != nil {
		status = statusKey.GetSecretValue()
	}

	return &LocalAccountInfo{
		CredentialID:  credentialID,
		Alias:         alias,
		Status:        status,
		Identity:      cred.Identity(),
		LastValidated: cred.LastRefreshed,
		CreatedAt:     cred.CreatedAt,
	}
}

func afterFirstSeparator(id string) string {
	for index := 0; index < len(id); index++ {
		if id[index] == '/' {
			return id[index+1:]
		}
	}
	return id
}

func containsByte(text string, want byte) bool {
	for index := 0; index < len(text); index++ {
		if text[index] == want {
			return true
		}
	}
	return false
}
