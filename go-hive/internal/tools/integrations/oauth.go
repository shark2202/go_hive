package integrations

// Module-side OAuth2 refresh declarations.
//
// Upstream keeps the OAuth2 refresh material in the credential store
// (client_id / client_secret / refresh_token keys) and refreshes it in
// core/framework/credentials before the tool sees an expired token. The Go
// port resolves module credentials from the environment, so the same three
// fields are read from environment variables here and the grant itself runs
// through integrationkit's TokenRefresher.
//
// Only modules whose upstream credential is OAuth2 declare this:
//
//	google    calendar/gmail/docs/sheets  (credentials/email.py: "Google OAuth2 access token")
//	hubspot   hubspot_tool                (credentials/hubspot.py: "Private App or OAuth2")
//	zoho_crm  zoho_crm_tool               (credentials/zoho.py: client_id/client_secret/refresh_token)
//
// Every other module keeps a static API key and is unaffected.

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// oauthEnv names the environment variables describing one module's OAuth2
// refresh-token credential. The token endpoint is <tokenURLVar> when set, then
// defaultTokenURL (Zoho derives both from its accounts domain).
type oauthEnv struct {
	module          string
	defaultTokenURL string
	clientIDVar     string
	clientSecretVar string
	refreshTokenVar string
	accessTokenVar  string
	tokenURLVar     string
	expiryVar       string
}

// oauthRefreshFromEnv resolves one module's refresh settings. Nil is returned
// when the operator configured no OAuth material at all, so a plain static
// token keeps its historical behaviour (including the provider's own 401
// message); once any OAuth variable is present the refresh path owns the
// credential and its failures are reported explicitly.
func oauthRefreshFromEnv(getenv func(string) string, spec oauthEnv) *integrationkit.OAuth2Refresh {
	if getenv == nil {
		getenv = os.Getenv
	}
	settings := &integrationkit.OAuth2Refresh{
		Module:       spec.module,
		TokenURL:     strings.TrimSpace(getenv(spec.tokenURLVar)),
		ClientID:     strings.TrimSpace(getenv(spec.clientIDVar)),
		ClientSecret: strings.TrimSpace(getenv(spec.clientSecretVar)),
		RefreshToken: strings.TrimSpace(getenv(spec.refreshTokenVar)),
		AccessToken:  strings.TrimSpace(getenv(spec.accessTokenVar)),
		ExpiresAt:    integrationkit.ParseTokenExpiry(getenv(spec.expiryVar)),
		TokenEnv:     spec.accessTokenVar,
		RefreshEnv:   spec.refreshTokenVar,
	}
	if settings.TokenURL == "" {
		settings.TokenURL = spec.defaultTokenURL
	}
	if settings.ClientID == "" && settings.ClientSecret == "" && settings.RefreshToken == "" && settings.ExpiresAt == nil {
		return nil
	}
	return settings
}

// batch5Lane3OAuth is the shared `google` credential used by calendar, gmail,
// docs and sheets.
func batch5Lane3OAuth(getenv func(string) string) *integrationkit.OAuth2Refresh {
	return oauthRefreshFromEnv(getenv, oauthEnv{
		module:          "google",
		defaultTokenURL: "https://oauth2.googleapis.com/token",
		clientIDVar:     "GOOGLE_CLIENT_ID",
		clientSecretVar: "GOOGLE_CLIENT_SECRET",
		refreshTokenVar: "GOOGLE_REFRESH_TOKEN",
		accessTokenVar:  "GOOGLE_ACCESS_TOKEN",
		tokenURLVar:     "GOOGLE_TOKEN_URL",
		expiryVar:       "GOOGLE_ACCESS_TOKEN_EXPIRES_AT",
	})
}

// batch5Lane4HubSpotOAuth is the `hubspot` credential. Upstream accepts a
// Private App token or OAuth2; a configured refresh token selects OAuth2.
func batch5Lane4HubSpotOAuth(getenv func(string) string) *integrationkit.OAuth2Refresh {
	return oauthRefreshFromEnv(getenv, oauthEnv{
		module:          "hubspot",
		defaultTokenURL: "https://api.hubapi.com/oauth/v1/token",
		clientIDVar:     "HUBSPOT_CLIENT_ID",
		clientSecretVar: "HUBSPOT_CLIENT_SECRET",
		refreshTokenVar: "HUBSPOT_REFRESH_TOKEN",
		accessTokenVar:  "HUBSPOT_ACCESS_TOKEN",
		tokenURLVar:     "HUBSPOT_TOKEN_URL",
		expiryVar:       "HUBSPOT_ACCESS_TOKEN_EXPIRES_AT",
	})
}

// oauthZohoOAuth is the `zoho_crm` credential. Upstream documents
// ZOHO_CLIENT_ID / ZOHO_CLIENT_SECRET / ZOHO_REFRESH_TOKEN and derives the
// token endpoint from ZOHO_ACCOUNTS_DOMAIN (or ZOHO_REGION); the default is
// the US accounts host.
func oauthZohoOAuth(getenv func(string) string) *integrationkit.OAuth2Refresh {
	return oauthRefreshFromEnv(getenv, oauthEnv{
		module:          "zoho_crm",
		defaultTokenURL: batch6ZohoTokenURL(getenv),
		clientIDVar:     "ZOHO_CLIENT_ID",
		clientSecretVar: "ZOHO_CLIENT_SECRET",
		refreshTokenVar: "ZOHO_REFRESH_TOKEN",
		accessTokenVar:  "ZOHO_CRM_ACCESS_TOKEN",
		tokenURLVar:     "ZOHO_TOKEN_URL",
		expiryVar:       "ZOHO_CRM_ACCESS_TOKEN_EXPIRES_AT",
	})
}

// batch6ZohoTokenURL builds the regional token endpoint. ZOHO_ACCOUNTS_DOMAIN
// is an explicit base; ZOHO_REGION maps the documented region codes onto
// accounts.zoho.<code> (us/com is the .com host).
func batch6ZohoTokenURL(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if domain := strings.TrimRight(strings.TrimSpace(getenv("ZOHO_ACCOUNTS_DOMAIN")), "/"); domain != "" {
		return domain + "/oauth/v2/token"
	}
	region := strings.ToLower(strings.TrimSpace(getenv("ZOHO_REGION")))
	switch region {
	case "us", "com":
		return "https://accounts.zoho.com/oauth/v2/token"
	case "in", "eu", "au", "jp", "uk", "sg", "ca":
		return "https://accounts.zoho." + region + "/oauth/v2/token"
	}
	return "https://accounts.zoho.com/oauth/v2/token"
}

// oauthValueError renders a refresh failure as the tool's value payload.
func oauthValueError(err error) map[string]any {
	var refreshErr *integrationkit.OAuthRefreshError
	if errors.As(err, &refreshErr) {
		return refreshErr.ValueError()
	}
	if err == nil {
		return nil
	}
	return map[string]any{"error": err.Error()}
}

// oauthModuleToken resolves a module credential through the refresh-aware
// path and reports whether a 401 can be retried with a freshly minted token.
func oauthModuleToken(ctx context.Context, rt *Runtime, moduleID string) (string, bool, map[string]any) {
	cfg := moduleConfig(rt, moduleID)
	token, _, err := rt.ModuleToken(ctx, cfg)
	if err != nil {
		return "", false, oauthValueError(err)
	}
	return token, rt.OAuthRefreshable(moduleID), nil
}

// moduleConfig returns one module's config from the runtime.
func moduleConfig(rt *Runtime, moduleID string) *ModuleConfig {
	if rt == nil || rt.Modules == nil {
		return nil
	}
	return rt.Modules[moduleID]
}
