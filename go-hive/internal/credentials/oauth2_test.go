package credentials

import (
	"strings"
	"testing"
	"time"
)

// Parity: core/framework/credentials/tests/test_credential_store.py::TestOAuth2Module::test_oauth2_token_from_response,
func TestOAuth2TokenFromResponse(t *testing.T) {
	token := FromTokenResponse(map[string]any{
		"access_token":  "xxx",
		"token_type":    "Bearer",
		"expires_in":    float64(3600),
		"refresh_token": "yyy",
		"scope":         "read write",
	})
	if token.AccessToken != "xxx" || token.TokenType != "Bearer" {
		t.Fatalf("token = %+v", token)
	}
	if token.RefreshToken != "yyy" || token.Scope != "read write" {
		t.Fatalf("token = %+v", token)
	}
	if token.ExpiresAt == nil {
		t.Fatal("expires_at not set")
	}
	if seconds := token.ExpiresInSeconds(); seconds == nil || *seconds < 3500 || *seconds > 3600 {
		t.Fatalf("expires_in_seconds = %v", seconds)
	}
	if token.IsExpired() {
		t.Fatal("fresh token reported expired")
	}
	if !token.CanRefresh() {
		t.Fatal("refresh token not detected")
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_token_is_expired,
func TestOAuth2TokenExpiry(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	token := NewOAuth2Token("xxx")
	token.ExpiresAt = &future
	if token.IsExpired() {
		t.Fatal("future token reported expired")
	}

	past := time.Now().UTC().Add(-time.Hour)
	expired := NewOAuth2Token("xxx")
	expired.ExpiresAt = &past
	if !expired.IsExpired() {
		t.Fatal("past token not reported expired")
	}

	soon := time.Now().UTC().Add(2 * time.Minute)
	buffered := NewOAuth2Token("xxx")
	buffered.ExpiresAt = &soon
	if !buffered.IsExpired() {
		t.Fatal("clock-skew buffer not applied")
	}

	if NewOAuth2Token("xxx").CanRefresh() {
		t.Fatal("token without refresh_token can refresh")
	}
	blank := NewOAuth2Token("xxx")
	blank.RefreshToken = "   "
	if blank.CanRefresh() {
		t.Fatal("blank refresh_token can refresh")
	}
	if NewOAuth2Token("xxx").ExpiresInSeconds() != nil {
		t.Fatal("token without expiry returned seconds")
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_oauth2_config_validation
func TestOAuth2ConfigValidation(t *testing.T) {
	config, err := NewOAuth2Config("https://example.com/token")
	if err != nil {
		t.Fatal(err)
	}
	if config.TokenURL != "https://example.com/token" {
		t.Fatalf("token url = %q", config.TokenURL)
	}
	if config.TokenPlacement != TokenPlacementHeaderBearer {
		t.Fatalf("placement = %q", config.TokenPlacement)
	}
	if config.QueryParamName != "access_token" || config.RequestTimeout != 30*time.Second {
		t.Fatalf("defaults = %+v", config)
	}

	if _, err := NewOAuth2Config(""); err == nil || !strings.Contains(err.Error(), "token_url is required") {
		t.Fatalf("missing token_url error = %v", err)
	}

	custom := &OAuth2Config{TokenURL: "https://example.com/token", TokenPlacement: TokenPlacementHeaderCustom}
	if err := custom.Validate(); err == nil || !strings.Contains(err.Error(), "custom_header_name is required") {
		t.Fatalf("custom header error = %v", err)
	}
	custom.CustomHeaderName = "X-Access-Token"
	if err := custom.Validate(); err != nil {
		t.Fatalf("valid custom config rejected: %v", err)
	}
}

// Parity-exempt: exact OAuth2 error message formatting has no Python test.
func TestOAuth2Errors(t *testing.T) {
	expired := NewTokenExpiredError("google")
	if expired.Error() != "token_expired: Token for 'google' has expired" {
		t.Fatalf("expired message = %q", expired.Error())
	}
	invalid := NewRefreshTokenInvalidError("google", "revoked")
	if invalid.Error() != "invalid_grant: Refresh token for 'google' is invalid: revoked" {
		t.Fatalf("invalid message = %q", invalid.Error())
	}
}
