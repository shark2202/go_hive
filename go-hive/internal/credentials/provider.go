package credentials

import (
	"strings"
	"time"
)

type CredentialProvider interface {
	ProviderID() string
	SupportedTypes() []CredentialType
	Refresh(credential *CredentialObject) (*CredentialObject, error)
	Validate(credential *CredentialObject) bool
	ShouldRefresh(credential *CredentialObject) bool
	Revoke(credential *CredentialObject) bool
	CanHandle(credential *CredentialObject) bool
}

func supports(types []CredentialType, credentialType CredentialType) bool {
	for _, t := range types {
		if t == credentialType {
			return true
		}
	}
	return false
}

func defaultShouldRefresh(credential *CredentialObject) bool {
	deadline := time.Now().UTC().Add(5 * time.Minute)
	for _, key := range credential.Keys.Values() {
		if key.ExpiresAt != nil && !key.ExpiresAt.After(deadline) {
			return true
		}
	}
	return false
}

type StaticProvider struct{}

func (StaticProvider) ProviderID() string { return "static" }

func (StaticProvider) SupportedTypes() []CredentialType {
	return []CredentialType{CredentialTypeAPIKey, CredentialTypeBasicAuth, CredentialTypeCustom}
}

func (StaticProvider) Refresh(credential *CredentialObject) (*CredentialObject, error) {
	return credential, nil
}

func (StaticProvider) Validate(credential *CredentialObject) bool {
	for _, key := range credential.Keys.Values() {
		if strings.TrimSpace(key.GetSecretValue()) != "" {
			return true
		}
	}
	return false
}

func (StaticProvider) ShouldRefresh(*CredentialObject) bool { return false }

func (StaticProvider) Revoke(*CredentialObject) bool { return false }

func (p StaticProvider) CanHandle(credential *CredentialObject) bool {
	return supports(p.SupportedTypes(), credential.CredentialType)
}

type BearerTokenProvider struct{}

func (BearerTokenProvider) ProviderID() string { return "bearer_token" }

func (BearerTokenProvider) SupportedTypes() []CredentialType {
	return []CredentialType{CredentialTypeBearerToken}
}

func (BearerTokenProvider) Refresh(credential *CredentialObject) (*CredentialObject, error) {
	return nil, newCredentialRefreshError(
		"Bearer token '%s' cannot be refreshed. Obtain a new token and save it to the credential store.",
		credential.ID,
	)
}

func (BearerTokenProvider) Validate(credential *CredentialObject) bool {
	key := credential.Keys.Get("access_token")
	if key == nil {
		key = credential.Keys.Get("token")
	}
	if key == nil {
		return false
	}
	return !key.IsExpired()
}

func (BearerTokenProvider) ShouldRefresh(credential *CredentialObject) bool {
	deadline := time.Now().UTC().Add(5 * time.Minute)
	for _, name := range []string{"access_token", "token"} {
		key := credential.Keys.Get(name)
		if key != nil && key.ExpiresAt != nil && !key.ExpiresAt.After(deadline) {
			return true
		}
	}
	return false
}

func (BearerTokenProvider) Revoke(*CredentialObject) bool { return false }

func (p BearerTokenProvider) CanHandle(credential *CredentialObject) bool {
	return supports(p.SupportedTypes(), credential.CredentialType)
}
