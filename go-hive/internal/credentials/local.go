package credentials

// Parity: framework/credentials/local/registry.py, framework/credentials/local/models.py

import (
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

type LocalCredentialRegistry struct {
	storage CredentialStorage
}

func NewLocalCredentialRegistry(storage CredentialStorage) *LocalCredentialRegistry {
	return &LocalCredentialRegistry{storage: storage}
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

func (r *LocalCredentialRegistry) SaveAccount(
	credentialID, alias, apiKey string,
	runHealthCheck bool,
	extraKeys map[string]string,
) (LocalAccountInfo, error) {
	if alias == "" {
		alias = "default"
	}
	status := "active"
	if runHealthCheck {
		status = "unknown"
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

	var lastRefreshed *time.Time
	if runHealthCheck {
		now := time.Now().UTC()
		cred.LastRefreshed = &now
		lastRefreshed = &now
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
	}, nil
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
