// Package auth implements the self-signed HS256 JWT scheme used by
// go-hive-cloud (operator JWT + streamToken).
//
// Contract notes (see docs/reverse-engineering.md §4):
//   - Clients only decode `exp` from HIVE_CLOUD_JWT without verifying the
//     signature; the server derives team_id/user_id from the token because
//     clients never send them.
//   - streamToken is an opaque Bearer string whose signature is proof of
//     identity; 24h TTL; POST /user/refresh-stream-token-from-stream accepts it
//     without a user JWT.
//
// The activator/issuer of the real cloud is UNKNOWN; this package defines the
// private-deploy scheme: HS256, iss "hive-cloud", claims
// {sub, user_id, team_id, exp, iss} (+ typ:"stream" for stream tokens).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Issuer is the `iss` claim value minted by this service.
const Issuer = "hive-cloud"

// TokenTypeStream marks stream tokens in the `typ` claim.
const TokenTypeStream = "stream"

var (
	// ErrExpired is returned when a token's exp claim is in the past.
	ErrExpired = errors.New("token expired")
	// ErrInvalid covers malformed tokens, unsupported algorithms and bad
	// signatures.
	ErrInvalid = errors.New("invalid token")
)

// Claims is the JWT payload shape. Field order/omitempty keeps the encoded
// form stable for tests.
type Claims struct {
	Sub    string `json:"sub,omitempty"`
	UserID string `json:"user_id,omitempty"`
	TeamID string `json:"team_id,omitempty"`
	Typ    string `json:"typ,omitempty"`
	Iss    string `json:"iss,omitempty"`
	Iat    int64  `json:"iat,omitempty"`
	Exp    int64  `json:"exp,omitempty"`
}

// Signer signs claims with a shared HMAC secret.
type Signer struct {
	secret []byte
	now    func() time.Time
}

// NewSigner returns a signer that signs with secret. A nil now function means
// time.Now.
func NewSigner(secret []byte, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{secret: secret, now: now}
}

// Sign produces a compact JWS (HS256, base64url without padding).
func (s *Signer) Sign(c Claims) (string, error) {
	if len(s.secret) == 0 {
		return "", errors.New("auth: empty signing secret")
	}
	if c.Iss == "" {
		c.Iss = Issuer
	}
	if c.Iat == 0 {
		c.Iat = s.now().Unix()
	}
	if c.Exp == 0 {
		return "", errors.New("auth: claims require an exp")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.mac(body), nil
}

// MintUserToken signs an operator JWT valid for ttl.
func (s *Signer) MintUserToken(userID, teamID string, ttl time.Duration) (string, error) {
	return s.Sign(Claims{
		Sub:    userID,
		UserID: userID,
		TeamID: teamID,
		Exp:    s.now().Add(ttl).Unix(),
	})
}

// MintStreamToken signs a streamToken (`typ:"stream"`) valid for ttl.
func (s *Signer) MintStreamToken(userID, teamID string, ttl time.Duration) (string, error) {
	return s.Sign(Claims{
		Sub:    userID,
		UserID: userID,
		TeamID: teamID,
		Typ:    TokenTypeStream,
		Exp:    s.now().Add(ttl).Unix(),
	})
}

func (s *Signer) mac(body string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Verify checks the signature and exp of token against secret.
func Verify(secret []byte, token string) (*Claims, error) {
	return VerifyAny([]([]byte){secret}, token)
}

// VerifyAny tries each secret in order (supports a dual-key rotation window)
// and returns the claims of the first secret that validates the signature.
func VerifyAny(secrets [][]byte, token string) (*Claims, error) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalid
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrInvalid
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil || header.Alg != "HS256" {
		return nil, ErrInvalid
	}
	body := parts[0] + "." + parts[1]
	gotSig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalid
	}
	verified := false
	seen := false
	for _, secret := range secrets {
		if len(secret) == 0 {
			continue
		}
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(body))
		if hmac.Equal(gotSig, m.Sum(nil)) {
			verified = true
		}
		seen = true
	}
	if !seen || !verified {
		return nil, ErrInvalid
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalid
	}
	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, ErrInvalid
	}
	if claims.Exp == 0 {
		return nil, ErrInvalid
	}
	if time.Unix(claims.Exp, 0).Before(time.Now()) {
		return nil, ErrExpired
	}
	return &claims, nil
}

// DecodeExp reads the exp claim without verifying the signature — the same
// non-verifying decode the clients do to schedule streamToken rotation.
func DecodeExp(token string) (int64, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return 0, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, false
	}
	return claims.Exp, claims.Exp != 0
}

// BearerToken extracts the token from an Authorization header value, accepting
// both the backend scheme ("jwt <token>") and the runtime scheme
// ("Bearer <token>").
func BearerToken(header string) (string, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", false
	}
	lower := strings.ToLower(header)
	switch {
	case strings.HasPrefix(lower, "jwt "):
		return strings.TrimSpace(header[4:]), true
	case strings.HasPrefix(lower, "bearer "):
		return strings.TrimSpace(header[7:]), true
	}
	return "", false
}

// ValidTeamID reports whether id is safe to use as a filesystem component.
func ValidTeamID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := c == '-' || c == '_' || c == '.' ||
			(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return false
		}
	}
	return id != "." && id != ".."
}

// DescribeError renders a user-facing reason for failed verification.
func DescribeError(err error) string {
	switch {
	case errors.Is(err, ErrExpired):
		return "token expired"
	case errors.Is(err, ErrInvalid):
		return "invalid token"
	default:
		return fmt.Sprintf("token rejected: %v", err)
	}
}
