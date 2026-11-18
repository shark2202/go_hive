package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TokenPlacement string

const (
	TokenPlacementHeaderBearer TokenPlacement = "header_bearer"
	TokenPlacementHeaderCustom TokenPlacement = "header_custom"
	TokenPlacementQueryParam   TokenPlacement = "query_param"
	TokenPlacementBodyParam    TokenPlacement = "body_param"
)

type OAuth2Token struct {
	AccessToken  string         `json:"access_token"`
	TokenType    string         `json:"token_type"`
	ExpiresAt    *time.Time     `json:"expires_at,omitempty"`
	RefreshToken string         `json:"refresh_token,omitempty"`
	Scope        string         `json:"scope,omitempty"`
	RawResponse  map[string]any `json:"raw_response,omitempty"`
}

func NewOAuth2Token(accessToken string) *OAuth2Token {
	return &OAuth2Token{AccessToken: accessToken, TokenType: "Bearer"}
}

func (t *OAuth2Token) IsExpired() bool {
	if t.ExpiresAt == nil {
		return false
	}
	return !time.Now().UTC().Before(t.ExpiresAt.Add(-5 * time.Minute))
}

func (t *OAuth2Token) CanRefresh() bool {
	return strings.TrimSpace(t.RefreshToken) != ""
}

func (t *OAuth2Token) ExpiresInSeconds() *int {
	if t.ExpiresAt == nil {
		return nil
	}
	seconds := int(time.Until(*t.ExpiresAt).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return &seconds
}

func FromTokenResponse(data map[string]any) *OAuth2Token {
	token := NewOAuth2Token(stringValue(data["access_token"]))
	if tokenType, ok := data["token_type"]; ok {
		token.TokenType = stringValue(tokenType)
	}
	if scope, ok := data["scope"]; ok {
		token.Scope = stringValue(scope)
	}
	if refresh, ok := data["refresh_token"]; ok {
		token.RefreshToken = stringValue(refresh)
	}
	if rawExpires, ok := data["expires_in"]; ok {
		if seconds, ok := numberValue(rawExpires); ok {
			expiresAt := time.Now().UTC().Add(time.Duration(seconds * float64(time.Second)))
			token.ExpiresAt = &expiresAt
		}
	}
	token.RawResponse = data
	return token
}

func stringValue(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func numberValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		var f float64
		if _, err := fmt.Sscan(n, &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

type OAuth2Config struct {
	TokenURL         string `json:"token_url"`
	AuthorizationURL string `json:"authorization_url,omitempty"`
	RevocationURL    string `json:"revocation_url,omitempty"`
	IntrospectionURL string `json:"introspection_url,omitempty"`

	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`

	DefaultScopes []string `json:"default_scopes,omitempty"`

	TokenPlacement   TokenPlacement `json:"token_placement,omitempty"`
	CustomHeaderName string         `json:"custom_header_name,omitempty"`
	QueryParamName   string         `json:"query_param_name,omitempty"`

	ExtraTokenParams map[string]string `json:"extra_token_params,omitempty"`
	RequestTimeout   time.Duration     `json:"request_timeout,omitempty"`
	ExtraHeaders     map[string]string `json:"extra_headers,omitempty"`
}

func NewOAuth2Config(tokenURL string) (*OAuth2Config, error) {
	config := &OAuth2Config{
		TokenURL:       tokenURL,
		TokenPlacement: TokenPlacementHeaderBearer,
		QueryParamName: "access_token",
		RequestTimeout: 30 * time.Second,
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, nil
}

func (c *OAuth2Config) Validate() error {
	if c.TokenURL == "" {
		return errors.New("token_url is required")
	}
	if c.TokenPlacement == TokenPlacementHeaderCustom && c.CustomHeaderName == "" {
		return errors.New("custom_header_name is required when using HEADER_CUSTOM placement")
	}
	return nil
}

type OAuth2Error struct {
	ErrorCode   string
	Description string
	StatusCode  int
	msg         string
}

func NewOAuth2Error(code, description string, statusCode int) *OAuth2Error {
	msg := code
	if description != "" {
		msg = code + ": " + description
	}
	return &OAuth2Error{ErrorCode: code, Description: description, StatusCode: statusCode, msg: msg}
}

func (e *OAuth2Error) Error() string { return e.msg }

type TokenExpiredError struct {
	*OAuth2Error
	CredentialID string
}

func NewTokenExpiredError(credentialID string) *TokenExpiredError {
	return &TokenExpiredError{
		OAuth2Error:  NewOAuth2Error("token_expired", fmt.Sprintf("Token for '%s' has expired", credentialID), 0),
		CredentialID: credentialID,
	}
}

type RefreshTokenInvalidError struct {
	*OAuth2Error
	CredentialID string
}

func NewRefreshTokenInvalidError(credentialID, reason string) *RefreshTokenInvalidError {
	description := fmt.Sprintf("Refresh token for '%s' is invalid", credentialID)
	if reason != "" {
		description += ": " + reason
	}
	return &RefreshTokenInvalidError{
		OAuth2Error:  NewOAuth2Error("invalid_grant", description, 0),
		CredentialID: credentialID,
	}
}
