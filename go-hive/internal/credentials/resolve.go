package credentials

// Parity: framework/credentials/resolve.py

import (
	"time"
)

type ResolvedSecret struct {
	CredentialID   string
	Account        *string
	Key            string
	Value          string
	ExpiresAt      *time.Time
	CredentialType *string
}

func (r ResolvedSecret) ExpiresInSeconds() *int {
	if r.ExpiresAt == nil {
		return nil
	}
	delta := r.ExpiresAt.Sub(time.Now().UTC())
	seconds := int(delta / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	return &seconds
}

func (r ResolvedSecret) IsOAuth() bool {
	return r.CredentialType != nil && *r.CredentialType == string(CredentialTypeOAuth2)
}

type SecretNotFound struct{ Message string }

func (e *SecretNotFound) Error() string { return e.Message }

type LocalRegistry interface {
	GetKey(credentialID, alias, keyName string) (string, bool, error)
	ListAccounts(credentialID string) ([]LocalAccountInfo, error)
}

var LocalRegistryDefault = func() LocalRegistry { return DefaultLocalCredentialRegistry() }

type SecretAdapter interface {
	Store() *CredentialStore
}

func defaultKeyName(cred *CredentialObject) *string {
	for _, name := range []string{"value", "api_key", "access_token"} {
		if cred.Keys.Has(name) {
			v := name
			return &v
		}
	}
	for _, name := range cred.Keys.Names() {
		if len(name) == 0 || name[0] != '_' {
			v := name
			return &v
		}
	}
	if first := cred.Keys.First(); first != nil {
		v := first.Name
		return &v
	}
	return nil
}

func fromCredential(cred *CredentialObject, account, key string) *ResolvedSecret {
	keyName := key
	if keyName == "" {
		if name := defaultKeyName(cred); name != nil {
			keyName = *name
		}
	}
	if keyName == "" {
		return nil
	}
	credentialKey := cred.Keys.Get(keyName)
	if credentialKey == nil {
		return nil
	}
	resolved := &ResolvedSecret{
		CredentialID: cred.ID,
		Key:          keyName,
		Value:        credentialKey.GetSecretValue(),
		ExpiresAt:    credentialKey.ExpiresAt,
	}
	if account != "" {
		resolved.Account = &account
	} else {
		resolved.Account = cred.Alias()
	}
	if cred.CredentialType != "" {
		credentialType := string(cred.CredentialType)
		resolved.CredentialType = &credentialType
	}
	return resolved
}

func ResolveSecret(adapter SecretAdapter, credentialID, account, key string) (*ResolvedSecret, error) {
	store := adapter.Store()
	localKey := key
	if localKey == "" {
		localKey = "api_key"
	}

	if account != "" {
		if local, ok := localKeyLookup(credentialID, account, localKey); ok {
			accountCopy := account
			return &ResolvedSecret{
				CredentialID: credentialID, Account: &accountCopy,
				Key: localKey, Value: local,
			}, nil
		}
		cred, err := store.GetCredentialByAlias(credentialID, account)
		if err != nil {
			return nil, err
		}
		if cred == nil {
			return nil, &SecretNotFound{Message: credentialID + "/" + account}
		}
		fresh, err := store.GetCredentialEx(cred.ID, true, true)
		if err != nil {
			return nil, err
		}
		if fresh == nil {
			fresh = cred
		}
		resolved := fromCredential(fresh, account, key)
		if resolved == nil {
			return nil, &SecretNotFound{Message: credentialID + "/" + account + " key " + pythonSingleRepr(key)}
		}
		return resolved, nil
	}

	cred, err := store.GetCredentialEx(credentialID, true, true)
	if err != nil {
		return nil, err
	}
	if cred != nil {
		if resolved := fromCredential(cred, "", key); resolved != nil {
			return resolved, nil
		}
	}

	registry := LocalRegistryDefault()
	accounts, err := registry.ListAccounts(credentialID)
	if err != nil {
		accounts = nil
	}
	target := ""
	if len(accounts) == 1 {
		target = accounts[0].Alias
	} else {
		for _, acct := range accounts {
			if acct.Alias == "default" {
				target = "default"
				break
			}
		}
	}
	if target != "" {
		if local, found, lookupErr := registry.GetKey(credentialID, target, localKey); lookupErr == nil && found {
			accountCopy := target
			return &ResolvedSecret{
				CredentialID: credentialID, Account: &accountCopy,
				Key: localKey, Value: local,
			}, nil
		}
	}
	message := credentialID
	if key != "" {
		message += " key " + pythonSingleRepr(key)
	}
	return nil, &SecretNotFound{Message: message}
}

func localKeyLookup(credentialID, account, keyName string) (string, bool) {
	registry := LocalRegistryDefault()
	if registry == nil {
		return "", false
	}
	value, found, err := registry.GetKey(credentialID, account, keyName)
	if err != nil || !found {
		return "", false
	}
	return value, true
}

func pythonSingleRepr(text string) string {
	return "'" + text + "'"
}
