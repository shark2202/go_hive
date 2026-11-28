package credentials

// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
//
// TokenLifecycleManager coordinates OAuth2 token refresh with a credential
// store: it loads the stored token, refreshes it before expiry, caches the
// result, and reports re-authorization when the refresh token is gone or
// revoked.
//
// Go has no asyncio twin, so the sync methods are the whole surface and the
// async-only names (get_valid_token, acquire_token_client_credentials) map to
// their sync counterparts.

import (
	"strings"
	"time"
)

// TokenRefreshResult is the outcome of a refresh attempt
// (lifecycle.py::TokenRefreshResult).
type TokenRefreshResult struct {
	Success              bool
	Token                *OAuth2Token
	Error                string
	NeedsReauthorization bool
}

// TokenLifecycleManager manages one OAuth2 credential in a store.
type TokenLifecycleManager struct {
	Provider     *BaseOAuth2Provider
	CredentialID string
	Store        *CredentialStore

	RefreshBuffer    time.Duration
	OnTokenRefreshed func(*OAuth2Token)
	OnRefreshFailed  func(string)

	cachedToken *OAuth2Token
	cacheTime   *time.Time
}

const defaultTokenRefreshBuffer = 5 * time.Minute

// NewTokenLifecycleManager builds a manager; a non-positive refreshBuffer
// keeps upstream's five-minute default. HubSpot and Zoho providers pass their
// embedded BaseOAuth2Provider (e.g. hubSpot.BaseOAuth2Provider).
func NewTokenLifecycleManager(provider *BaseOAuth2Provider, credentialID string, store *CredentialStore, refreshBuffer time.Duration) *TokenLifecycleManager {
	if refreshBuffer <= 0 {
		refreshBuffer = defaultTokenRefreshBuffer
	}
	return &TokenLifecycleManager{
		Provider:      provider,
		CredentialID:  credentialID,
		Store:         store,
		RefreshBuffer: refreshBuffer,
	}
}

// GetValidToken returns a usable token, refreshing when it is within the
// refresh buffer (lifecycle.py::get_valid_token / sync_get_valid_token).
func (m *TokenLifecycleManager) GetValidToken() *OAuth2Token {
	return m.SyncGetValidToken()
}

// SyncGetValidToken is the synchronous token accessor.
func (m *TokenLifecycleManager) SyncGetValidToken() *OAuth2Token {
	if m.cachedToken != nil && !m.needsRefresh(m.cachedToken) {
		return m.cachedToken
	}
	credential, err := m.Store.GetCredential(m.CredentialID, false)
	if err != nil || credential == nil {
		return nil
	}
	token := m.credentialToToken(credential)
	if token == nil {
		return nil
	}
	if m.needsRefresh(token) {
		result := m.syncRefreshToken(credential)
		switch {
		case result.Success && result.Token != nil:
			token = result.Token
		case result.NeedsReauthorization:
			return nil
		default:
			// Keep a still-valid token when the refresh merely failed.
			if token.IsExpired() {
				return nil
			}
		}
	}
	m.cachedToken = token
	now := time.Now().UTC()
	m.cacheTime = &now
	return token
}

// AcquireTokenClientCredentials obtains a service token and saves it
// (lifecycle.py::acquire_token_client_credentials /
// sync_acquire_token_client_credentials).
func (m *TokenLifecycleManager) AcquireTokenClientCredentials(scopes []string) (*OAuth2Token, error) {
	return m.SyncAcquireTokenClientCredentials(scopes)
}

// SyncAcquireTokenClientCredentials is the synchronous client-credentials path.
func (m *TokenLifecycleManager) SyncAcquireTokenClientCredentials(scopes []string) (*OAuth2Token, error) {
	token, err := m.Provider.ClientCredentialsGrant(scopes, nil)
	if err != nil {
		return nil, err
	}
	if err := m.saveTokenToStore(token); err != nil {
		return nil, err
	}
	m.cachedToken = token
	return token, nil
}

// Revoke revokes the stored tokens and deletes the credential
// (lifecycle.py::revoke). It always reports true, like upstream.
func (m *TokenLifecycleManager) Revoke() bool {
	if credential, err := m.Store.GetCredential(m.CredentialID, false); err == nil && credential != nil {
		m.Provider.Revoke(credential)
	}
	_, _ = m.Store.DeleteCredential(m.CredentialID)
	m.cachedToken = nil
	m.cacheTime = nil
	return true
}

// InvalidateCache drops the in-memory token (lifecycle.py::invalidate_cache).
func (m *TokenLifecycleManager) InvalidateCache() {
	m.cachedToken = nil
	m.cacheTime = nil
}

// GetRequestHeaders returns the provider-formatted headers, or an empty map
// when no valid token is available (lifecycle.py::get_request_headers).
func (m *TokenLifecycleManager) GetRequestHeaders() map[string]string {
	token := m.SyncGetValidToken()
	if token == nil {
		return map[string]string{}
	}
	result := m.Provider.FormatForRequest(token)
	headers, ok := result["headers"].(map[string]string)
	if !ok {
		return map[string]string{}
	}
	return headers
}

// GetRequestKwargs returns the provider-formatted request fragments, or an
// empty map when no valid token is available (lifecycle.py::get_request_kwargs).
func (m *TokenLifecycleManager) GetRequestKwargs() map[string]any {
	token := m.SyncGetValidToken()
	if token == nil {
		return map[string]any{}
	}
	return m.Provider.FormatForRequest(token)
}

func (m *TokenLifecycleManager) needsRefresh(token *OAuth2Token) bool {
	if token == nil || token.ExpiresAt == nil {
		return false
	}
	return !time.Now().UTC().Before(token.ExpiresAt.Add(-m.RefreshBuffer))
}

func (m *TokenLifecycleManager) credentialToToken(credential *CredentialObject) *OAuth2Token {
	accessToken, ok := credential.GetKeyValue("access_token")
	if !ok || accessToken == "" {
		return nil
	}
	token := &OAuth2Token{
		AccessToken: accessToken,
		TokenType:   "Bearer",
	}
	if key := credential.Keys.Get("access_token"); key != nil {
		token.ExpiresAt = key.ExpiresAt
	}
	if refreshToken, ok := credential.GetKeyValue("refresh_token"); ok {
		token.RefreshToken = refreshToken
	}
	if scope, ok := credential.GetKeyValue("scope"); ok {
		token.Scope = scope
	}
	return token
}

func (m *TokenLifecycleManager) saveTokenToStore(token *OAuth2Token) error {
	credential := NewCredentialObject(m.CredentialID)
	credential.CredentialType = CredentialTypeOAuth2
	credential.ProviderID = m.Provider.ProviderID()
	credential.AutoRefresh = true
	credential.SetKey("access_token", token.AccessToken, token.ExpiresAt, nil)
	if token.RefreshToken != "" {
		credential.SetKey("refresh_token", token.RefreshToken, nil, nil)
	}
	if token.Scope != "" {
		credential.SetKey("scope", token.Scope, nil, nil)
	}
	return m.Store.SaveCredential(credential)
}

// syncRefreshToken refreshes the credential through the provider and persists
// the new token (lifecycle.py::_sync_refresh_token).
func (m *TokenLifecycleManager) syncRefreshToken(credential *CredentialObject) TokenRefreshResult {
	refreshToken, ok := credential.GetKeyValue("refresh_token")
	if !ok || refreshToken == "" {
		return TokenRefreshResult{
			Success:              false,
			Error:                "No refresh token available",
			NeedsReauthorization: true,
		}
	}

	newToken, err := m.Provider.RefreshAccessToken(refreshToken, nil, nil)
	if err != nil {
		message := err.Error()
		if strings.Contains(strings.ToLower(message), "invalid_grant") {
			return TokenRefreshResult{Success: false, Error: message, NeedsReauthorization: true}
		}
		if m.OnRefreshFailed != nil {
			m.OnRefreshFailed(message)
		}
		return TokenRefreshResult{Success: false, Error: message}
	}

	if err := m.saveTokenToStore(newToken); err != nil {
		if m.OnRefreshFailed != nil {
			m.OnRefreshFailed(err.Error())
		}
		return TokenRefreshResult{Success: false, Error: err.Error()}
	}
	if m.OnTokenRefreshed != nil {
		m.OnTokenRefreshed(newToken)
	}
	return TokenRefreshResult{Success: true, Token: newToken}
}
