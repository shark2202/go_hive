package gworkspace

// Credential-resolution helpers shared by the four Google modules: account
// alias resolution, the module token fallback and the OAuth2 refresh path.
// They were shared root helpers (account_routing.go, oauth.go) reproduced here
// so the subpackage is self-contained; Google's refresh material is the same
// GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET / GOOGLE_REFRESH_TOKEN set as
// upstream core/framework/credentials.

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// resolveAccountToken resolves a module credential for the current call: an
// account alias the action selected wins over the module's single token. A
// selected alias that does not resolve yields "" so the caller reports its own
// auth error, matching upstream returning None from get_by_alias.
func resolveAccountToken(rt *Runtime, moduleID, provider string) string {
	if token, handled := rt.ResolveAccountToken(provider); handled {
		return token
	}
	return moduleToken(rt, moduleID)
}

// moduleToken resolves the module's own credential from WithCredential or the
// runtime environment lookup.
func moduleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	token, _ := cfg.ResolvedToken(getenv)
	return strings.TrimSpace(token)
}

// notionOptionalStrings reads an optional []string parameter. (The helper was
// historically named for the module it was first written for; Calendar and
// Linear share it.)
func notionOptionalStrings(input map[string]any, name string) ([]string, bool) {
	value, present := input[name]
	if !present {
		return nil, false
	}
	list := integrationkit.AsSlice(value)
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, integrationkit.AsString(item))
	}
	return out, true
}

// notionOptionalBool reads an optional boolean parameter (historically named
// for the module it was first written for).
func notionOptionalBool(input map[string]any, name string) (bool, bool) {
	value, present := input[name]
	if !present {
		return false, false
	}
	return integrationkit.AsBool(value), true
}

// oauthEnv names the environment variables describing one module's OAuth2
// refresh-token credential. The token endpoint is <tokenURLVar> when set, then
// defaultTokenURL.
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

// googleOAuth is the shared `google` credential used by calendar, gmail, docs
// and sheets.
func googleOAuth(getenv func(string) string) *integrationkit.OAuth2Refresh {
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
