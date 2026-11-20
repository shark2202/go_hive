package credentials

// Parity-exempt: Go-only bridge that makes a saved OAuth2 credential
// refreshable by the integration modules. Upstream refreshes inside the
// credential store (core/framework/credentials/oauth2/base_provider.py) and
// the tools read the refreshed access token through CredentialStoreAdapter.
// The port's tools resolve module credentials from the environment, so the
// same credential keys are exported under the variable names the module OAuth
// declarations read (internal/tools/integrations/oauth.go).
//
// The access token itself is already exported by CredentialEnvBindings
// (credential_key access_token). These bindings add the refresh material:
// client id, client secret, refresh token and the access token's expiry.

// OAuthRefreshEnvBindings lists the extra store→environment bindings for the
// providers whose upstream credential is OAuth2. Credential ids match the
// upstream specs: google (calendar/gmail/docs/sheets), hubspot, zoho_crm.
func OAuthRefreshEnvBindings() []EnvBinding {
	providers := []struct {
		credentialID string
		prefix       string
		accessVar    string
	}{
		{credentialID: "google", prefix: "GOOGLE", accessVar: "GOOGLE_ACCESS_TOKEN"},
		{credentialID: "hubspot", prefix: "HUBSPOT", accessVar: "HUBSPOT_ACCESS_TOKEN"},
		{credentialID: "zoho_crm", prefix: "ZOHO", accessVar: "ZOHO_CRM_ACCESS_TOKEN"},
	}
	out := make([]EnvBinding, 0, len(providers)*4)
	for _, provider := range providers {
		out = append(out,
			EnvBinding{CredentialID: provider.credentialID, EnvVar: provider.prefix + "_CLIENT_ID", CredentialKey: "client_id"},
			EnvBinding{CredentialID: provider.credentialID, EnvVar: provider.prefix + "_CLIENT_SECRET", CredentialKey: "client_secret"},
			EnvBinding{CredentialID: provider.credentialID, EnvVar: provider.prefix + "_REFRESH_TOKEN", CredentialKey: "refresh_token"},
			EnvBinding{CredentialID: provider.credentialID, EnvVar: provider.accessVar + "_EXPIRES_AT", ExpiryKey: "access_token"},
		)
	}
	return out
}
