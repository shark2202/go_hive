package credentials

// Parity: core/framework/credentials/oauth2/base_provider.py::BaseOAuth2Provider
//
// BaseOAuth2Provider is the generic RFC 6749 provider: authorization-code URL
// generation and exchange, client-credentials grant, refresh-token grant,
// RFC 7009 revocation, and the CredentialProvider lifecycle (refresh on
// access, validate, should-refresh, revoke). Provider-specific behaviour is
// layered by embedding plus method override, mirroring the Python subclasses.
//
// Boundary notes where Go cannot mirror Python exactly:
//   - transport failures are returned as ordinary errors, not OAuth2Error,
//     because upstream lets httpx exceptions escape _token_request;
//   - a 200 token response without access_token returns an error, where
//     upstream raises KeyError from OAuth2Token.from_token_response;
//   - request-form/query ordering is deterministic (Go maps are unordered);
//     OAuth2 servers do not observe ordering.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// applyDefaults fills the OAuth2Config defaults that Python's dataclass gets
// from field defaults. The receiver is a private copy owned by the provider.
func (c *OAuth2Config) applyDefaults() {
	if c.TokenPlacement == "" {
		c.TokenPlacement = TokenPlacementHeaderBearer
	}
	if c.QueryParamName == "" {
		c.QueryParamName = "access_token"
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = 30 * time.Second
	}
}

// BaseOAuth2Provider implements CredentialProvider for standard OAuth2 servers.
type BaseOAuth2Provider struct {
	config     OAuth2Config
	providerID string

	mu     sync.Mutex
	client *http.Client
}

var _ CredentialProvider = (*BaseOAuth2Provider)(nil)

// NewBaseOAuth2Provider builds a provider with the upstream default id "oauth2".
func NewBaseOAuth2Provider(config *OAuth2Config) (*BaseOAuth2Provider, error) {
	return NewBaseOAuth2ProviderWithID(config, "oauth2")
}

// NewBaseOAuth2ProviderWithID builds a provider with an explicit provider id.
func NewBaseOAuth2ProviderWithID(config *OAuth2Config, providerID string) (*BaseOAuth2Provider, error) {
	if config == nil {
		return nil, errors.New("token_url is required")
	}
	cfg := *config
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if providerID == "" {
		providerID = "oauth2"
	}
	return &BaseOAuth2Provider{config: cfg, providerID: providerID}, nil
}

func (p *BaseOAuth2Provider) ProviderID() string { return p.providerID }

func (p *BaseOAuth2Provider) SupportedTypes() []CredentialType {
	return []CredentialType{CredentialTypeOAuth2, CredentialTypeBearerToken}
}

func (p *BaseOAuth2Provider) CanHandle(credential *CredentialObject) bool {
	return supports(p.SupportedTypes(), credential.CredentialType)
}

// Close releases idle connections held by the provider HTTP client.
func (p *BaseOAuth2Provider) Close() {
	p.mu.Lock()
	client := p.client
	p.client = nil
	p.mu.Unlock()
	if client != nil {
		client.CloseIdleConnections()
	}
}

func (p *BaseOAuth2Provider) httpClient() *http.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		p.client = &http.Client{Timeout: p.config.RequestTimeout}
	}
	return p.client
}

// --- Grant types ---

// GetAuthorizationURL builds the user-consent URL for the Authorization Code
// flow (base_provider.py::get_authorization_url).
func (p *BaseOAuth2Provider) GetAuthorizationURL(state, redirectURI string, scopes []string, extra map[string]string) (string, error) {
	if p.config.AuthorizationURL == "" {
		return "", errors.New("authorization_url not configured for this provider")
	}
	if len(scopes) == 0 {
		scopes = p.config.DefaultScopes
	}
	params := [][2]string{
		{"client_id", p.config.ClientID},
		{"redirect_uri", redirectURI},
		{"response_type", "code"},
		{"state", state},
		{"scope", strings.Join(scopes, " ")},
	}
	for _, key := range sortedParamKeys(extra) {
		params = append(params, [2]string{key, extra[key]})
	}
	return p.config.AuthorizationURL + "?" + encodeQueryParams(params), nil
}

// ExchangeCode exchanges an authorization code for tokens
// (base_provider.py::exchange_code).
func (p *BaseOAuth2Provider) ExchangeCode(code, redirectURI string, extra map[string]string) (*OAuth2Token, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("client_id", p.config.ClientID)
	data.Set("client_secret", p.config.ClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	mergeTokenParams(data, p.config.ExtraTokenParams)
	mergeTokenParams(data, extra)
	return p.tokenRequest(data)
}

// ClientCredentialsGrant obtains a server-to-server token
// (base_provider.py::client_credentials_grant).
func (p *BaseOAuth2Provider) ClientCredentialsGrant(scopes []string, extra map[string]string) (*OAuth2Token, error) {
	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", p.config.ClientID)
	data.Set("client_secret", p.config.ClientSecret)
	mergeTokenParams(data, p.config.ExtraTokenParams)
	mergeTokenParams(data, extra)
	if len(scopes) > 0 || len(p.config.DefaultScopes) > 0 {
		if len(scopes) == 0 {
			scopes = p.config.DefaultScopes
		}
		data.Set("scope", strings.Join(scopes, " "))
	}
	return p.tokenRequest(data)
}

// RefreshAccessToken refreshes an expired access token
// (base_provider.py::refresh_access_token).
func (p *BaseOAuth2Provider) RefreshAccessToken(refreshToken string, scopes []string, extra map[string]string) (*OAuth2Token, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("client_id", p.config.ClientID)
	data.Set("client_secret", p.config.ClientSecret)
	data.Set("refresh_token", refreshToken)
	mergeTokenParams(data, p.config.ExtraTokenParams)
	mergeTokenParams(data, extra)
	if len(scopes) > 0 {
		data.Set("scope", strings.Join(scopes, " "))
	}
	return p.tokenRequest(data)
}

// RevokeToken revokes a token per RFC 7009 (base_provider.py::revoke_token).
// A 200 response is success even when the token was already invalid.
func (p *BaseOAuth2Provider) RevokeToken(token, tokenTypeHint string) bool {
	if p.config.RevocationURL == "" {
		return false
	}
	if tokenTypeHint == "" {
		tokenTypeHint = "access_token"
	}
	data := url.Values{}
	data.Set("token", token)
	data.Set("token_type_hint", tokenTypeHint)
	data.Set("client_id", p.config.ClientID)
	data.Set("client_secret", p.config.ClientSecret)
	request, err := http.NewRequest(http.MethodPost, p.config.RevocationURL, strings.NewReader(data.Encode()))
	if err != nil {
		return false
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for key, value := range p.config.ExtraHeaders {
		request.Header.Set(key, value)
	}
	response, err := p.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}

// --- CredentialProvider interface ---

// Refresh refreshes a credential with its refresh_token
// (base_provider.py::refresh).
func (p *BaseOAuth2Provider) Refresh(credential *CredentialObject) (*CredentialObject, error) {
	if credential == nil {
		return nil, errors.New("credential is required")
	}
	refreshToken, ok := credential.GetKeyValue("refresh_token")
	if !ok || refreshToken == "" {
		return nil, newCredentialRefreshError("Credential '%s' has no refresh_token", credential.ID)
	}

	newToken, err := p.RefreshAccessToken(refreshToken, nil, nil)
	if err != nil {
		var oauthErr *OAuth2Error
		if !errors.As(err, &oauthErr) {
			// Upstream catches only OAuth2Error here; transport, JSON and
			// missing-field failures escape unwrapped.
			return nil, err
		}
		if oauthErr.ErrorCode == "invalid_grant" {
			return nil, newCredentialRefreshError(
				"Refresh token for '%s' is invalid or revoked. Re-authorization required.",
				credential.ID,
			)
		}
		return nil, newCredentialRefreshError("Failed to refresh '%s': %v", credential.ID, err)
	}

	credential.SetKey("access_token", newToken.AccessToken, newToken.ExpiresAt, nil)
	if newToken.RefreshToken != "" && newToken.RefreshToken != refreshToken {
		credential.SetKey("refresh_token", newToken.RefreshToken, nil, nil)
	}
	now := time.Now().UTC()
	credential.LastRefreshed = &now
	return credential, nil
}

// Validate reports whether the credential holds a non-expired access token
// (base_provider.py::validate). Unlike ShouldRefresh there is no skew buffer.
func (p *BaseOAuth2Provider) Validate(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	key := credential.Keys.Get("access_token")
	if key == nil {
		return false
	}
	return !key.IsExpired()
}

// ShouldRefresh reports whether the access token expires within five minutes
// (base_provider.py::should_refresh).
func (p *BaseOAuth2Provider) ShouldRefresh(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	key := credential.Keys.Get("access_token")
	if key == nil || key.ExpiresAt == nil {
		return false
	}
	return !time.Now().UTC().Before(key.ExpiresAt.Add(-5 * time.Minute))
}

// Revoke revokes every token present in the credential
// (base_provider.py::revoke).
func (p *BaseOAuth2Provider) Revoke(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	success := true
	if accessToken, ok := credential.GetKeyValue("access_token"); ok && accessToken != "" {
		if !p.RevokeToken(accessToken, "access_token") {
			success = false
		}
	}
	if refreshToken, ok := credential.GetKeyValue("refresh_token"); ok && refreshToken != "" {
		if !p.RevokeToken(refreshToken, "refresh_token") {
			success = false
		}
	}
	return success
}

// --- Token formatting for requests ---

// FormatForRequest places the token according to the configured placement
// (base_provider.py::format_for_request).
func (p *BaseOAuth2Provider) FormatForRequest(token *OAuth2Token) map[string]any {
	if token == nil {
		return map[string]any{}
	}
	switch p.config.TokenPlacement {
	case TokenPlacementHeaderBearer:
		return map[string]any{"headers": map[string]string{
			"Authorization": token.TokenType + " " + token.AccessToken,
		}}
	case TokenPlacementHeaderCustom:
		headerName := p.config.CustomHeaderName
		if headerName == "" {
			headerName = "X-Access-Token"
		}
		return map[string]any{"headers": map[string]string{headerName: token.AccessToken}}
	case TokenPlacementQueryParam:
		return map[string]any{"params": map[string]string{p.config.QueryParamName: token.AccessToken}}
	case TokenPlacementBodyParam:
		return map[string]any{"data": map[string]string{"access_token": token.AccessToken}}
	}
	return map[string]any{}
}

// FormatCredentialForRequest formats a stored credential for an API request
// (base_provider.py::format_credential_for_request).
func (p *BaseOAuth2Provider) FormatCredentialForRequest(credential *CredentialObject) map[string]any {
	return formatCredentialForRequest(credential, p.FormatForRequest)
}

// formatCredentialForRequest is shared with subclasses that override
// FormatForRequest (Zoho), mirroring Python's polymorphic self.format_for_request.
func formatCredentialForRequest(credential *CredentialObject, format func(*OAuth2Token) map[string]any) map[string]any {
	if credential == nil {
		return map[string]any{}
	}
	accessToken, ok := credential.GetKeyValue("access_token")
	if !ok || accessToken == "" {
		return map[string]any{}
	}
	tokenType := "Bearer"
	if stored, ok := credential.GetKeyValue("token_type"); ok && stored != "" {
		tokenType = stored
	}
	return format(&OAuth2Token{AccessToken: accessToken, TokenType: tokenType})
}

// --- Token request helpers ---

// tokenRequest posts form data to the token endpoint and parses the response
// (base_provider.py::_token_request).
func (p *BaseOAuth2Provider) tokenRequest(data url.Values) (*OAuth2Token, error) {
	request, err := http.NewRequest(http.MethodPost, p.config.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for key, value := range p.config.ExtraHeaders {
		request.Header.Set(key, value)
	}

	response, err := p.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("oauth2 token request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("oauth2 token response read failed: %w", err)
	}

	responseData, err := decodeTokenResponse(response.Header.Get("Content-Type"), body)
	if err != nil {
		return nil, err
	}

	if errorValue, hasError := responseData["error"]; response.StatusCode != http.StatusOK || hasError {
		errorCode := "unknown_error"
		if hasError {
			errorCode = stringValue(errorValue)
		}
		description := string(body)
		if value, ok := responseData["error_description"]; ok {
			description = stringValue(value)
		}
		return nil, NewOAuth2Error(errorCode, description, response.StatusCode)
	}

	if _, ok := responseData["access_token"]; !ok {
		return nil, errors.New("oauth2 token response missing access_token")
	}
	return FromTokenResponse(responseData), nil
}

// decodeTokenResponse accepts JSON token responses and the form-encoded
// responses some providers return (base_provider.py::_parse_form_response).
func decodeTokenResponse(contentType string, body []byte) (map[string]any, error) {
	if strings.Contains(strings.ToLower(contentType), "application/json") {
		var data map[string]any
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("oauth2 token response is not valid JSON: %w", err)
		}
		if data == nil {
			data = map[string]any{}
		}
		return data, nil
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, fmt.Errorf("oauth2 token response is not valid form encoding: %w", err)
	}
	data := make(map[string]any, len(values))
	for key, list := range values {
		if len(list) > 0 {
			// Python keeps the list for repeated keys; token fields are scalar.
			data[key] = list[0]
		}
	}
	return data, nil
}

func mergeTokenParams(data url.Values, extra map[string]string) {
	for key, value := range extra {
		data.Set(key, value)
	}
}

func sortedParamKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// encodeQueryParams encodes in caller order with quote_plus semantics, the
// same encoding Python's urllib.parse.urlencode produces.
func encodeQueryParams(params [][2]string) string {
	var builder strings.Builder
	for index, pair := range params {
		if index > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString(url.QueryEscape(pair[0]))
		builder.WriteByte('=')
		builder.WriteString(url.QueryEscape(pair[1]))
	}
	return builder.String()
}

// rawString returns a non-empty string member of a token response.
func rawString(data map[string]any, key string) (string, bool) {
	value, ok := data[key]
	if !ok {
		return "", false
	}
	text := stringValue(value)
	return text, text != ""
}
