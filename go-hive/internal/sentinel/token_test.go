package sentinel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testTokens(t *testing.T) *TokenManager {
	t.Helper()
	tokens := &TokenManager{SecretPath: filepath.Join(t.TempDir(), secretFilename)}
	tokens.SetSecret(bytes.Repeat([]byte{0}, 32))
	return tokens
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_token_round_trips
func TestTokenRoundTrips(t *testing.T) {
	tokens := testTokens(t)
	token := tokens.MakeToken("esc_abc123")
	if !tokens.VerifyToken(token, "esc_abc123") {
		t.Fatalf("token %q should verify", token)
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_token_is_deterministic
func TestTokenIsDeterministic(t *testing.T) {
	tokens := testTokens(t)
	if tokens.MakeToken("esc_x") != tokens.MakeToken("esc_x") {
		t.Fatal("make_token should be deterministic")
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_token_differs_per_escalation
func TestTokenDiffersPerEscalation(t *testing.T) {
	tokens := testTokens(t)
	if tokens.MakeToken("esc_a") == tokens.MakeToken("esc_b") {
		t.Fatal("distinct escalation ids should mint distinct tokens")
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_verify_rejects_wrong_id
func TestVerifyRejectsWrongID(t *testing.T) {
	tokens := testTokens(t)
	token := tokens.MakeToken("esc_a")
	if tokens.VerifyToken(token, "esc_b") {
		t.Fatal("token for esc_a must not verify esc_b")
	}
}

// Parity-exempt: Go-only filesystem hardening; the secret is 0600 and a
// planted symlink must not be written through.
func TestSecretFileIsPrivateAndRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	tokens := &TokenManager{SecretPath: filepath.Join(dir, secretFilename)}
	_ = tokens.MakeToken("esc_perm")
	info, err := os.Stat(tokens.SecretPath)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("secret mode = %o, want 600", perm)
	}

	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir := t.TempDir()
	link := filepath.Join(linkDir, secretFilename)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	linked := &TokenManager{SecretPath: link}
	_ = linked.MakeToken("esc_link")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("secret was written through a symlink: %q", data)
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_verify_rejects_tampered_token
func TestVerifyRejectsTamperedToken(t *testing.T) {
	tokens := testTokens(t)
	token := tokens.MakeToken("esc_a")
	replacement := byte('x')
	if token[0] == 'x' {
		replacement = 'y'
	}
	tampered := string(replacement) + token[1:]
	if tokens.VerifyToken(tampered, "esc_a") {
		t.Fatal("tampered token must not verify")
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_verify_rejects_empty
func TestVerifyRejectsEmpty(t *testing.T) {
	tokens := testTokens(t)
	if tokens.VerifyToken("", "esc_a") {
		t.Fatal("empty token must not verify")
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_extract_token_from_reply
func TestExtractTokenFromReply(t *testing.T) {
	tokens := testTokens(t)
	token := tokens.MakeToken("esc_a")
	reply := "Yes go ahead " + FormatRef(token)
	extracted, ok := ExtractToken(reply)
	if !ok || extracted != token {
		t.Fatalf("extract_token = %q, %t; want %q", extracted, ok, token)
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_extract_token_missing
func TestExtractTokenMissing(t *testing.T) {
	if token, ok := ExtractToken("just a normal reply"); ok || token != "" {
		t.Fatalf("plain reply extracted %q", token)
	}
	if token, ok := ExtractToken(""); ok || token != "" {
		t.Fatalf("empty reply extracted %q", token)
	}
}

// Parity: core/framework/sentinel/token.py, core/tests/test_sentinel_token.py::test_strip_ref_removes_footer
func TestStripRefRemovesFooter(t *testing.T) {
	tokens := testTokens(t)
	token := tokens.MakeToken("esc_a")
	reply := "Continue please " + FormatRef(token)
	stripped := StripRef(reply)
	if stripped != "Continue please" {
		t.Fatalf("strip_ref = %q, want %q", stripped, "Continue please")
	}
}

// Parity: core/framework/sentinel/token.py::make_token
func TestTokenMatchesPythonKnownAnswer(t *testing.T) {
	// Known answer computed from the Python reference with a 32-byte zero
	// secret: base32(HMAC-SHA256(secret, id)) lowercased and truncated to 16.
	tokens := testTokens(t)
	cases := map[string]string{
		"esc_abc123": "xxljbrwdb52qaoq7",
		"esc_x":      "hokz3jljqgqppbrj",
	}
	for escalationID, want := range cases {
		if got := tokens.MakeToken(escalationID); got != want {
			t.Fatalf("make_token(%q) = %q, want %q", escalationID, got, want)
		}
	}
}

// Parity-exempt: token.py has no case-normalisation test.
func TestVerifyAndExtractAreCaseInsensitive(t *testing.T) {
	tokens := testTokens(t)
	token := tokens.MakeToken("esc_a")
	if !tokens.VerifyToken(strings.ToUpper(token), "esc_a") {
		t.Fatal("verify should lower-case the presented token")
	}
	extracted, ok := ExtractToken("REF: " + strings.ToUpper(token))
	if !ok || extracted != token {
		t.Fatalf("extracted = %q, want %q", extracted, token)
	}
}

// Parity-exempt: token.py::_load_or_create_secret has no Python test; this
// asserts the 0600 per-install file lifecycle directly.
func TestSecretCreatedLazilyWith0600AndReloaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), secretFilename)
	first := &TokenManager{SecretPath: path}
	token := first.MakeToken("esc_a")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("secret not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %04o, want 0600", info.Mode().Perm())
	}

	// A fresh manager (simulating a restart) reloads the persisted secret and
	// keeps existing tokens verifiable.
	second := &TokenManager{SecretPath: path}
	if !second.VerifyToken(token, "esc_a") {
		t.Fatal("persisted secret should reproduce the same token")
	}
	if len(token) != TokenLen {
		t.Fatalf("token length = %d, want %d", len(token), TokenLen)
	}
}
