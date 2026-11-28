package credentials

// Parity: core/framework/credentials/oauth2/hubspot_provider.py::HubSpotOAuth2Provider
//
// HubSpotOAuth2Provider pre-configures HubSpot's OAuth2 endpoints and CRM
// scopes and validates a token against the contacts endpoint.

import (
	"io"
	"net/http"
)

const (
	// HubSpotTokenURL is HubSpot's OAuth2 token endpoint.
	HubSpotTokenURL = "https://api.hubapi.com/oauth/v1/token"
	// HubSpotAuthorizationURL is HubSpot's OAuth2 consent endpoint.
	HubSpotAuthorizationURL = "https://app.hubspot.com/oauth/authorize"
	// HubSpotContactsURL is the lightweight endpoint Validate probes.
	HubSpotContactsURL = "https://api.hubapi.com/crm/v3/objects/contacts"
)

var hubSpotDefaultScopes = []string{
	"crm.objects.contacts.read",
	"crm.objects.contacts.write",
	"crm.objects.companies.read",
	"crm.objects.companies.write",
	"crm.objects.deals.read",
	"crm.objects.deals.write",
}

// HubSpotDefaultScopes returns the default CRM scopes (a copy).
func HubSpotDefaultScopes() []string {
	out := make([]string, len(hubSpotDefaultScopes))
	copy(out, hubSpotDefaultScopes)
	return out
}

// HubSpotOAuth2Provider is the HubSpot-flavoured OAuth2 provider.
type HubSpotOAuth2Provider struct {
	*BaseOAuth2Provider

	// contactsURL is a transport seam so tests can point Validate at a fake
	// server; production keeps HubSpotContactsURL.
	contactsURL string
}

var _ CredentialProvider = (*HubSpotOAuth2Provider)(nil)

// NewHubSpotOAuth2Provider builds the provider; scopes nil keeps HubSpot's
// default CRM scopes (hubspot_provider.py::HubSpotOAuth2Provider).
func NewHubSpotOAuth2Provider(clientID, clientSecret string, scopes []string) *HubSpotOAuth2Provider {
	if len(scopes) == 0 {
		scopes = HubSpotDefaultScopes()
	}
	base, err := NewBaseOAuth2ProviderWithID(&OAuth2Config{
		TokenURL:         HubSpotTokenURL,
		AuthorizationURL: HubSpotAuthorizationURL,
		ClientID:         clientID,
		ClientSecret:     clientSecret,
		DefaultScopes:    scopes,
	}, "hubspot_oauth2")
	if err != nil {
		// The static endpoint configuration above always validates.
		panic(err)
	}
	return &HubSpotOAuth2Provider{BaseOAuth2Provider: base, contactsURL: HubSpotContactsURL}
}

// SupportedTypes narrows HubSpot to OAuth2 only
// (hubspot_provider.py::HubSpotOAuth2Provider.supported_types).
func (p *HubSpotOAuth2Provider) SupportedTypes() []CredentialType {
	return []CredentialType{CredentialTypeOAuth2}
}

// CanHandle must be redeclared so it consults the HubSpot type list rather
// than the promoted base implementation.
func (p *HubSpotOAuth2Provider) CanHandle(credential *CredentialObject) bool {
	return supports(p.SupportedTypes(), credential.CredentialType)
}

// Validate probes the contacts endpoint with limit=1
// (hubspot_provider.py::HubSpotOAuth2Provider.validate).
func (p *HubSpotOAuth2Provider) Validate(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	accessToken, ok := credential.GetKeyValue("access_token")
	if !ok || accessToken == "" {
		return false
	}
	request, err := http.NewRequest(http.MethodGet, p.contactsURL, nil)
	if err != nil {
		return false
	}
	query := request.URL.Query()
	query.Set("limit", "1")
	request.URL.RawQuery = query.Encode()
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")

	response, err := p.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode == http.StatusOK
}
