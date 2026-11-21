package sentinel

// Correlation tokens for Sentinel escalations.
//
// An escalation sent to a messaging channel carries a short, opaque
// ``(ref: …)`` footer. When the user replies, the token routes the reply back
// to the exact escalation (and thus the colony/session it belongs to).
//
// The token is an HMAC of the escalation id under a per-install secret, so it
// is opaque, unforgeable and deterministic: the inbound side verifies by
// recompute plus constant-time compare rather than by trusting the wire value.
// The secret lives at ``$HIVE_HOME/sentinel_secret`` (0600), created lazily on
// first use.
//
// Parity: core/framework/sentinel/token.py

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// TokenLen is the number of base32 characters kept from the HMAC digest.
const TokenLen = 16

const secretFilename = "sentinel_secret"

// base32 alphabet is A-Z2-7; the token is lowercased, so match a-z2-7.
var refPattern = regexp.MustCompile(`(?i)ref:\s*([a-z2-7]{6,32})`)

var refStripPattern = regexp.MustCompile(`(?i)\(?\s*ref:\s*[a-z2-7]{6,32}\s*\)?`)

// TokenManager owns the per-install HMAC secret and the token lifecycle.
// A zero-value SecretPath resolves under DefaultHome() lazily.
type TokenManager struct {
	SecretPath string

	mu     sync.Mutex
	cached []byte
}

// NewTokenManager returns a manager whose secret lives under hiveRoot. An
// empty root falls back to HIVE_HOME (or ~/.hive).
func NewTokenManager(hiveRoot string) *TokenManager {
	if hiveRoot == "" {
		hiveRoot = DefaultHome()
	}
	return &TokenManager{SecretPath: filepath.Join(hiveRoot, secretFilename)}
}

// defaultTokens is the process-wide manager used by package-level helpers.
var (
	defaultTokensOnce sync.Once
	defaultTokens     *TokenManager
)

// DefaultTokens returns the process-wide token manager.
func DefaultTokens() *TokenManager {
	defaultTokensOnce.Do(func() { defaultTokens = NewTokenManager("") })
	return defaultTokens
}

// SetSecret pins the HMAC secret in memory. Used by embedders that provision
// the secret out of band and by tests that need deterministic tokens.
func (m *TokenManager) SetSecret(secret []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cached = append([]byte(nil), secret...)
}

// ResetSecretCache drops the cached secret so the next call reloads (or
// regenerates) it from disk.
func (m *TokenManager) ResetSecretCache() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cached = nil
}

func (m *TokenManager) loadOrCreateSecret() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cached != nil {
		return m.cached
	}
	path := m.SecretPath
	if path == "" {
		path = filepath.Join(DefaultHome(), secretFilename)
	}
	if data, err := os.ReadFile(path); err == nil {
		trimmed := strings.TrimSpace(string(data))
		if trimmed != "" {
			if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) > 0 {
				m.cached = decoded
				return decoded
			}
			sentinelLogf("sentinel: could not decode secret at %s, regenerating", path)
		}
	} else if !os.IsNotExist(err) {
		sentinelLogf("sentinel: could not read secret at %s, regenerating: %v", path, err)
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic("sentinel: crypto/rand unavailable: " + err.Error())
	}
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		sentinelLogf("sentinel: refusing to write through symlink at %s; using ephemeral secret", path)
		m.cached = secret
		return secret
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		sentinelLogf("sentinel: could not persist secret; using ephemeral: %v", err)
	} else {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			sentinelLogf("sentinel: could not persist secret; using ephemeral: %v", err)
		} else {
			encoded := base64.StdEncoding.EncodeToString(secret)
			if _, err := f.WriteString(encoded); err != nil {
				sentinelLogf("sentinel: could not persist secret; using ephemeral: %v", err)
			}
			if err := f.Close(); err != nil {
				sentinelLogf("sentinel: could not close secret file: %v", err)
			}
			_ = os.Chmod(path, 0o600)
		}
	}
	m.cached = secret
	return secret
}

// MakeToken returns the deterministic opaque token for an escalation id.
func (m *TokenManager) MakeToken(escalationID string) string {
	mac := hmac.New(sha256.New, m.loadOrCreateSecret())
	_, _ = mac.Write([]byte(escalationID))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	encoded = strings.ToLower(encoded)
	if len(encoded) > TokenLen {
		encoded = encoded[:TokenLen]
	}
	return encoded
}

// VerifyToken is a constant-time check that token matches escalationID.
func (m *TokenManager) VerifyToken(token, escalationID string) bool {
	if token == "" {
		return false
	}
	return hmac.Equal([]byte(strings.ToLower(token)), []byte(m.MakeToken(escalationID)))
}

// ExtractToken pulls a (ref: …) token out of a free-text reply.
func ExtractToken(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	match := refPattern.FindStringSubmatch(text)
	if match == nil {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

// FormatRef renders the footer appended to an outbound escalation message.
func FormatRef(token string) string {
	return fmt.Sprintf("(ref: %s)", token)
}

// StripRef removes the (ref: …) footer from a reply so it is not fed to the
// queen.
func StripRef(text string) string {
	if text == "" {
		return ""
	}
	return strings.TrimSpace(refStripPattern.ReplaceAllString(text, ""))
}

// Package-level helpers backed by DefaultTokens.

// MakeToken returns a token for escalationID under the default install secret.
func MakeToken(escalationID string) string { return DefaultTokens().MakeToken(escalationID) }

// VerifyToken verifies token against the default install secret.
func VerifyToken(token, escalationID string) bool {
	return DefaultTokens().VerifyToken(token, escalationID)
}
