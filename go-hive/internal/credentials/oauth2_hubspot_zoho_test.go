package credentials

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Parity: core/framework/credentials/oauth2/hubspot_provider.py::HubSpotOAuth2Provider
func TestHubSpotOAuth2ProviderConfig(t *testing.T) {
	provider := NewHubSpotOAuth2Provider("client-1", "secret-1", nil)
	if provider.ProviderID() != "hubspot_oauth2" {
		t.Fatalf("provider id = %q", provider.ProviderID())
	}
	types := provider.SupportedTypes()
	if len(types) != 1 || types[0] != CredentialTypeOAuth2 {
		t.Fatalf("types = %v", types)
	}
	if !provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeOAuth2}) {
		t.Fatal("oauth2 credential not handled")
	}
	if provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeBearerToken}) {
		t.Fatal("HubSpot must not accept bearer_token credentials")
	}

	if provider.config.TokenURL != HubSpotTokenURL {
		t.Fatalf("token url = %q", provider.config.TokenURL)
	}
	if provider.config.AuthorizationURL != HubSpotAuthorizationURL {
		t.Fatalf("authorization url = %q", provider.config.AuthorizationURL)
	}
	if provider.config.ClientID != "client-1" || provider.config.ClientSecret != "secret-1" {
		t.Fatalf("client = %+v", provider.config)
	}
	scopes := provider.config.DefaultScopes
	want := HubSpotDefaultScopes()
	if len(scopes) != len(want) || len(want) != 6 {
		t.Fatalf("scopes = %v", scopes)
	}
	for index := range want {
		if scopes[index] != want[index] {
			t.Fatalf("scopes = %v", scopes)
		}
	}
	if !strings.Contains(strings.Join(scopes, " "), "crm.objects.deals.write") {
		t.Fatalf("scopes = %v", scopes)
	}

	override := NewHubSpotOAuth2Provider("client-1", "secret-1", []string{"crm.objects.contacts.read"})
	if len(override.config.DefaultScopes) != 1 || override.config.DefaultScopes[0] != "crm.objects.contacts.read" {
		t.Fatalf("override scopes = %v", override.config.DefaultScopes)
	}

	// HubSpotDefaultScopes returns a copy: mutating it cannot poison defaults.
	copied := HubSpotDefaultScopes()
	copied[0] = "mutated"
	if HubSpotDefaultScopes()[0] == "mutated" {
		t.Fatal("HubSpotDefaultScopes is not a copy")
	}
}

// Parity: core/framework/credentials/oauth2/hubspot_provider.py::HubSpotOAuth2Provider
func TestHubSpotOAuth2ProviderValidate(t *testing.T) {
	server := newOAuth2TestServer(t, func(request oauth2TestRequest, _ int) oauth2TestResponse {
		if request.Path != "/crm/v3/objects/contacts" {
			return oauth2TestResponse{Status: http.StatusNotFound, ContentType: "application/json", Body: "{}"}
		}
		if request.Authorization != "Bearer hs-token" {
			return oauth2TestResponse{Status: http.StatusUnauthorized, ContentType: "application/json", Body: "{}"}
		}
		return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: `{"results":[]}`}
	})
	provider := NewHubSpotOAuth2Provider("client-1", "secret-1", nil)
	provider.contactsURL = server.URL("/crm/v3/objects/contacts")

	credential := &CredentialObject{ID: "hubspot", Keys: NewKeyMap(NewCredentialKey("access_token", "hs-token"))}
	if !provider.Validate(credential) {
		t.Fatal("valid HubSpot credential rejected")
	}
	if server.RequestCount() != 1 {
		t.Fatalf("requests = %d", server.RequestCount())
	}
	request := server.Request(0)
	if request.Method != http.MethodGet {
		t.Fatalf("method = %q", request.Method)
	}
	if request.Query.Get("limit") != "1" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Accept != "application/json" {
		t.Fatalf("accept = %q", request.Accept)
	}

	if provider.Validate(&CredentialObject{ID: "hubspot"}) {
		t.Fatal("credential without access_token validated")
	}
	if server.RequestCount() != 1 {
		t.Fatalf("token-less validation made a request: %d", server.RequestCount())
	}

	rejected := &CredentialObject{ID: "hubspot", Keys: NewKeyMap(NewCredentialKey("access_token", "other-token"))}
	if provider.Validate(rejected) {
		t.Fatal("rejected token validated")
	}
}

// Parity: core/framework/credentials/oauth2/hubspot_provider.py::HubSpotOAuth2Provider
func TestHubSpotOAuth2ProviderValidateErrors(t *testing.T) {
	server := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{Status: http.StatusUnauthorized, ContentType: "application/json", Body: `{"status":"error"}`}
	})
	provider := NewHubSpotOAuth2Provider("client-1", "secret-1", nil)
	provider.contactsURL = server.URL("/crm/v3/objects/contacts")
	credential := &CredentialObject{ID: "hubspot", Keys: NewKeyMap(NewCredentialKey("access_token", "hs-token"))}
	if provider.Validate(credential) {
		t.Fatal("401 accepted")
	}

	server.Close()
	if provider.Validate(credential) {
		t.Fatal("transport error accepted")
	}
}

// Parity: core/framework/credentials/oauth2/zoho_provider.py::ZohoOAuth2Provider
func TestZohoOAuth2ProviderConfig(t *testing.T) {
	provider := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{AccountsDomain: "https://accounts.zoho.eu/"})
	if provider.ProviderID() != "zoho_crm_oauth2" {
		t.Fatalf("provider id = %q", provider.ProviderID())
	}
	types := provider.SupportedTypes()
	if len(types) != 1 || types[0] != CredentialTypeOAuth2 {
		t.Fatalf("types = %v", types)
	}
	if provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeBearerToken}) {
		t.Fatal("Zoho must not accept bearer_token credentials")
	}
	if provider.AccountsDomain() != "https://accounts.zoho.eu" {
		t.Fatalf("accounts domain = %q", provider.AccountsDomain())
	}
	if provider.config.TokenURL != "https://accounts.zoho.eu/oauth/v2/token" {
		t.Fatalf("token url = %q", provider.config.TokenURL)
	}
	if provider.config.AuthorizationURL != "https://accounts.zoho.eu/oauth/v2/auth" {
		t.Fatalf("authorization url = %q", provider.config.AuthorizationURL)
	}
	if provider.config.TokenPlacement != TokenPlacementHeaderCustom || provider.config.CustomHeaderName != "Authorization" {
		t.Fatalf("placement = %+v", provider.config)
	}
	if len(provider.config.DefaultScopes) != 5 || provider.config.DefaultScopes[0] != "ZohoCRM.modules.leads.ALL" {
		t.Fatalf("scopes = %v", provider.config.DefaultScopes)
	}

	defaults := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{})
	if defaults.AccountsDomain() != ZohoDefaultAccountsDomain {
		t.Fatalf("default accounts domain = %q", defaults.AccountsDomain())
	}
	if defaults.APIDomain() != ZohoDefaultAPIDomain {
		t.Fatalf("default api domain = %q", defaults.APIDomain())
	}

	t.Setenv(ZohoAPIDomainEnv, "https://www.zohoapis.eu/")
	fromEnv := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{})
	if fromEnv.APIDomain() != "https://www.zohoapis.eu" {
		t.Fatalf("env api domain = %q", fromEnv.APIDomain())
	}
	explicit := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{APIDomain: "https://www.zohoapis.in/"})
	if explicit.APIDomain() != "https://www.zohoapis.in" {
		t.Fatalf("explicit api domain = %q", explicit.APIDomain())
	}

	override := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{Scopes: []string{"ZohoCRM.modules.leads.ALL"}})
	if len(override.config.DefaultScopes) != 1 {
		t.Fatalf("override scopes = %v", override.config.DefaultScopes)
	}
	copied := ZohoDefaultScopes()
	copied[0] = "mutated"
	if ZohoDefaultScopes()[0] == "mutated" {
		t.Fatal("ZohoDefaultScopes is not a copy")
	}
}

// Parity: core/framework/credentials/oauth2/zoho_provider.py::ZohoOAuth2Provider
func TestZohoOAuth2ProviderFormatAndValidate(t *testing.T) {
	server := newOAuth2TestServer(t, func(request oauth2TestRequest, _ int) oauth2TestResponse {
		if request.Path != "/crm/v2/users" || request.Query.Get("type") != "CurrentUser" {
			return oauth2TestResponse{Status: http.StatusNotFound, ContentType: "application/json", Body: "{}"}
		}
		if request.Authorization != "Zoho-oauthtoken z-token" {
			return oauth2TestResponse{Status: http.StatusUnauthorized, ContentType: "application/json", Body: "{}"}
		}
		return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: `{"users":[]}`}
	})
	provider := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{APIDomain: server.URL("")})

	headers, ok := provider.FormatForRequest(&OAuth2Token{AccessToken: "z-token", TokenType: "Bearer"})["headers"].(map[string]string)
	if !ok || headers["Authorization"] != "Zoho-oauthtoken z-token" {
		t.Fatalf("headers = %v", headers)
	}
	if headers["Content-Type"] != "application/json" || headers["Accept"] != "application/json" {
		t.Fatalf("headers = %v", headers)
	}

	credential := &CredentialObject{ID: "zoho_crm", Keys: NewKeyMap(
		NewCredentialKey("access_token", "z-token"),
		NewCredentialKey("token_type", "Bearer"),
	)}
	credentialHeaders := provider.FormatCredentialForRequest(credential)["headers"].(map[string]string)
	if credentialHeaders["Authorization"] != "Zoho-oauthtoken z-token" {
		t.Fatalf("credential headers = %v", credentialHeaders)
	}

	if !provider.Validate(credential) {
		t.Fatal("valid Zoho credential rejected")
	}
	request := server.Request(0)
	if request.Method != http.MethodGet || request.Accept != "application/json" {
		t.Fatalf("request = %+v", request)
	}
	if provider.Validate(&CredentialObject{ID: "zoho_crm"}) {
		t.Fatal("credential without access_token validated")
	}
}

// Parity: core/framework/credentials/oauth2/zoho_provider.py::ZohoOAuth2Provider
func TestZohoOAuth2ProviderValidateStatuses(t *testing.T) {
	rateLimited := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{Status: http.StatusTooManyRequests, ContentType: "application/json", Body: `{"code":"TOO_MANY_REQUESTS"}`}
	})
	provider := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{APIDomain: rateLimited.URL("")})
	credential := &CredentialObject{ID: "zoho_crm", Keys: NewKeyMap(NewCredentialKey("access_token", "z-token"))}
	if !provider.Validate(credential) {
		t.Fatal("429 must count as a valid-but-rate-limited token")
	}

	unauthorized := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{Status: http.StatusUnauthorized, ContentType: "application/json", Body: `{"code":"INVALID_TOKEN"}`}
	})
	rejected := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{APIDomain: unauthorized.URL("")})
	if rejected.Validate(credential) {
		t.Fatal("401 accepted")
	}

	unauthorized.Close()
	if rejected.Validate(credential) {
		t.Fatal("transport error accepted")
	}
}

// Parity: core/framework/credentials/oauth2/zoho_provider.py::ZohoOAuth2Provider
func TestZohoOAuth2ProviderRefresh(t *testing.T) {
	server := newOAuth2TestServerJSON(t, `{"access_token":"z-new","refresh_token":"z-refresh-2","token_type":"Bearer","expires_in":3600,"api_domain":"https://www.zohoapis.eu/","accounts-server":"https://accounts.zoho.eu/","location":"EU"}`)
	provider := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{AccountsDomain: server.URL("")})

	past := time.Now().UTC().Add(-time.Hour)
	credential := &CredentialObject{
		ID:             "zoho_crm",
		CredentialType: CredentialTypeOAuth2,
		Keys: NewKeyMap(
			&CredentialKey{Name: "access_token", Value: "stale", ExpiresAt: &past},
			NewCredentialKey("refresh_token", "z-refresh-1"),
		),
	}

	refreshed, err := provider.Refresh(credential)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if refreshed != credential {
		t.Fatal("refresh did not return the same credential")
	}
	if value, _ := refreshed.GetKeyValue("access_token"); value != "z-new" {
		t.Fatalf("access_token = %q", value)
	}
	if value, _ := refreshed.GetKeyValue("refresh_token"); value != "z-refresh-2" {
		t.Fatalf("refresh_token = %q", value)
	}
	if key := refreshed.Keys.Get("access_token"); key.ExpiresAt == nil || time.Until(*key.ExpiresAt) < 50*time.Minute {
		t.Fatalf("expiry = %v", key.ExpiresAt)
	}
	if value, _ := refreshed.GetKeyValue("api_domain"); value != "https://www.zohoapis.eu" {
		t.Fatalf("api_domain = %q", value)
	}
	if value, _ := refreshed.GetKeyValue("accounts_domain"); value != "https://accounts.zoho.eu" {
		t.Fatalf("accounts_domain = %q", value)
	}
	if value, _ := refreshed.GetKeyValue("location"); value != "eu" {
		t.Fatalf("location = %q", value)
	}
	if refreshed.LastRefreshed != nil {
		t.Fatal("Zoho refresh must leave last_refreshed to the store")
	}

	if server.RequestCount() != 1 {
		t.Fatalf("requests = %d", server.RequestCount())
	}
	form := server.Request(0).Form
	assertFormField(t, form, "grant_type", "refresh_token")
	assertFormField(t, form, "refresh_token", "z-refresh-1")
	assertFormField(t, form, "client_id", "client-1")
	assertFormField(t, form, "client_secret", "secret-1")
}

// Parity: core/framework/credentials/oauth2/zoho_provider.py::ZohoOAuth2Provider
func TestZohoOAuth2ProviderRefreshErrors(t *testing.T) {
	provider := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{AccountsDomain: "https://accounts.zoho.com"})
	if _, err := provider.Refresh(&CredentialObject{ID: "zoho_crm"}); err == nil ||
		err.Error() != "Credential 'zoho_crm' has no refresh_token" {
		t.Fatalf("missing refresh token error = %v", err)
	}

	invalid := newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{
			Status:      http.StatusBadRequest,
			ContentType: "application/json",
			Body:        `{"error":"invalid_grant","error_description":"revoked"}`,
		}
	})
	zohoInvalid := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{AccountsDomain: invalid.URL("")})
	past := time.Now().UTC().Add(-time.Hour)
	credential := &CredentialObject{
		ID: "zoho_crm",
		Keys: NewKeyMap(
			&CredentialKey{Name: "access_token", Value: "stale", ExpiresAt: &past},
			NewCredentialKey("refresh_token", "z-refresh-1"),
		),
	}
	_, err := zohoInvalid.Refresh(credential)
	var refreshErr *CredentialRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	// Unlike the base provider, Zoho maps invalid_grant to the generic message.
	if !strings.HasPrefix(err.Error(), "Failed to refresh 'zoho_crm': invalid_grant: revoked") {
		t.Fatalf("message = %q", err.Error())
	}
	if value, _ := credential.GetKeyValue("access_token"); value != "stale" {
		t.Fatalf("credential mutated on failure: %q", value)
	}

	// Zoho's refresh catches every exception upstream, so transport failures
	// become CredentialRefreshError too.
	unreachable := newOAuth2TestServerJSON(t, oauth2TestDefaultBody)
	zohoUnreachable := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{AccountsDomain: unreachable.URL("")})
	unreachable.Close()
	if _, err := zohoUnreachable.Refresh(credential); err == nil {
		t.Fatal("transport error accepted")
	} else if !errors.As(err, &refreshErr) {
		t.Fatalf("transport error type = %T %v", err, err)
	}
}
