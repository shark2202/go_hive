package credentials

// Parity: core/framework/credentials/oauth2/zoho_provider.py::ZohoOAuth2Provider
//
// ZohoOAuth2Provider pre-configures region-aware Zoho accounts endpoints, the
// Zoho CRM scopes, the "Zoho-oauthtoken" authorization header format, a
// CurrentUser validation probe, and a refresh that persists the data-centre
// metadata Zoho returns with new tokens.
//
// Boundary note: upstream also defines _parse_token_response, but
// BaseOAuth2Provider._token_request never calls it; the raw token response is
// already kept verbatim in OAuth2Token.raw_response, so the Go port keeps the
// same observable result without a dead parsing hook.

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	// ZohoDefaultAccountsDomain is Zoho's US accounts server.
	ZohoDefaultAccountsDomain = "https://accounts.zoho.com"
	// ZohoDefaultAPIDomain is Zoho's default CRM API server.
	ZohoDefaultAPIDomain = "https://www.zohoapis.com"
	// ZohoAPIDomainEnv overrides the CRM API domain.
	ZohoAPIDomainEnv = "ZOHO_API_DOMAIN"
)

var zohoDefaultScopes = []string{
	"ZohoCRM.modules.leads.ALL",
	"ZohoCRM.modules.contacts.ALL",
	"ZohoCRM.modules.accounts.ALL",
	"ZohoCRM.modules.deals.ALL",
	"ZohoCRM.modules.notes.CREATE",
}

// ZohoDefaultScopes returns the default CRM scopes (a copy).
func ZohoDefaultScopes() []string {
	out := make([]string, len(zohoDefaultScopes))
	copy(out, zohoDefaultScopes)
	return out
}

// ZohoOAuth2Options carries Zoho-specific configuration. Zero values fall back
// to the upstream defaults (US accounts server, ZOHO_API_DOMAIN or the default
// API domain, default CRM scopes).
type ZohoOAuth2Options struct {
	AccountsDomain string
	APIDomain      string
	Scopes         []string
}

// ZohoOAuth2Provider is the Zoho CRM-flavoured OAuth2 provider.
type ZohoOAuth2Provider struct {
	*BaseOAuth2Provider

	accountsDomain string
	apiDomain      string
}

var _ CredentialProvider = (*ZohoOAuth2Provider)(nil)

// NewZohoOAuth2Provider builds the provider
// (zoho_provider.py::ZohoOAuth2Provider).
func NewZohoOAuth2Provider(clientID, clientSecret string, options ZohoOAuth2Options) *ZohoOAuth2Provider {
	accountsDomain := strings.TrimRight(options.AccountsDomain, "/")
	if accountsDomain == "" {
		accountsDomain = ZohoDefaultAccountsDomain
	}
	apiDomain := strings.TrimRight(options.APIDomain, "/")
	if apiDomain == "" {
		apiDomain = strings.TrimRight(os.Getenv(ZohoAPIDomainEnv), "/")
	}
	if apiDomain == "" {
		apiDomain = ZohoDefaultAPIDomain
	}
	scopes := options.Scopes
	if len(scopes) == 0 {
		scopes = ZohoDefaultScopes()
	}

	base, err := NewBaseOAuth2ProviderWithID(&OAuth2Config{
		TokenURL:         accountsDomain + "/oauth/v2/token",
		AuthorizationURL: accountsDomain + "/oauth/v2/auth",
		ClientID:         clientID,
		ClientSecret:     clientSecret,
		DefaultScopes:    scopes,
		TokenPlacement:   TokenPlacementHeaderCustom,
		CustomHeaderName: "Authorization",
	}, "zoho_crm_oauth2")
	if err != nil {
		// The derived endpoint configuration above always validates.
		panic(err)
	}
	return &ZohoOAuth2Provider{
		BaseOAuth2Provider: base,
		accountsDomain:     accountsDomain,
		apiDomain:          apiDomain,
	}
}

// AccountsDomain is the region-specific Zoho accounts server.
func (p *ZohoOAuth2Provider) AccountsDomain() string { return p.accountsDomain }

// APIDomain is the Zoho CRM API server used by Validate.
func (p *ZohoOAuth2Provider) APIDomain() string { return p.apiDomain }

// SupportedTypes narrows Zoho to OAuth2 only
// (zoho_provider.py::ZohoOAuth2Provider.supported_types).
func (p *ZohoOAuth2Provider) SupportedTypes() []CredentialType {
	return []CredentialType{CredentialTypeOAuth2}
}

// CanHandle must be redeclared so it consults the Zoho type list rather than
// the promoted base implementation.
func (p *ZohoOAuth2Provider) CanHandle(credential *CredentialObject) bool {
	return supports(p.SupportedTypes(), credential.CredentialType)
}

// FormatForRequest uses Zoho's "Zoho-oauthtoken" header
// (zoho_provider.py::ZohoOAuth2Provider.format_for_request).
func (p *ZohoOAuth2Provider) FormatForRequest(token *OAuth2Token) map[string]any {
	if token == nil {
		return map[string]any{}
	}
	return map[string]any{"headers": map[string]string{
		"Authorization": "Zoho-oauthtoken " + token.AccessToken,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}}
}

// FormatCredentialForRequest redeclared so credential formatting goes through
// Zoho's header format, as Python's polymorphic call does.
func (p *ZohoOAuth2Provider) FormatCredentialForRequest(credential *CredentialObject) map[string]any {
	return formatCredentialForRequest(credential, p.FormatForRequest)
}

// Validate probes /crm/v2/users?type=CurrentUser; 429 counts as valid because
// it proves the token was accepted (zoho_provider.py::ZohoOAuth2Provider.validate).
func (p *ZohoOAuth2Provider) Validate(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	accessToken, ok := credential.GetKeyValue("access_token")
	if !ok || accessToken == "" {
		return false
	}
	request, err := http.NewRequest(http.MethodGet, p.apiDomain+"/crm/v2/users", nil)
	if err != nil {
		return false
	}
	query := request.URL.Query()
	query.Set("type", "CurrentUser")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Zoho-oauthtoken "+accessToken)
	request.Header.Set("Accept", "application/json")

	response, err := p.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusTooManyRequests
}

// Refresh refreshes the credential and persists the Zoho data-centre metadata
// (api_domain, accounts_domain, location) returned by the token endpoint
// (zoho_provider.py::ZohoOAuth2Provider.refresh). Unlike the base provider it
// maps every failure to the generic CredentialRefreshError message.
func (p *ZohoOAuth2Provider) Refresh(credential *CredentialObject) (*CredentialObject, error) {
	if credential == nil {
		return nil, errors.New("credential is required")
	}
	refreshToken, ok := credential.GetKeyValue("refresh_token")
	if !ok || refreshToken == "" {
		return nil, newCredentialRefreshError("Credential '%s' has no refresh_token", credential.ID)
	}

	newToken, err := p.RefreshAccessToken(refreshToken, nil, nil)
	if err != nil {
		return nil, newCredentialRefreshError("Failed to refresh '%s': %v", credential.ID, err)
	}

	credential.SetKey("access_token", newToken.AccessToken, newToken.ExpiresAt, nil)
	if newToken.RefreshToken != "" && newToken.RefreshToken != refreshToken {
		credential.SetKey("refresh_token", newToken.RefreshToken, nil, nil)
	}
	if apiDomain, ok := rawString(newToken.RawResponse, "api_domain"); ok {
		credential.SetKey("api_domain", strings.TrimRight(apiDomain, "/"), nil, nil)
	}
	if accountsServer, ok := rawString(newToken.RawResponse, "accounts-server"); ok {
		credential.SetKey("accounts_domain", strings.TrimRight(accountsServer, "/"), nil, nil)
	}
	if location, ok := rawString(newToken.RawResponse, "location"); ok {
		credential.SetKey("location", strings.ToLower(strings.TrimSpace(location)), nil, nil)
	}
	return credential, nil
}
