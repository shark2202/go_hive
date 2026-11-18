package credentials

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const (
	pythonFernetKey   = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	pythonFernetToken = "gAAAAABq_D7giOz2HXg32Gm5vMm3gaLNvM_iOrdHsDme0fe1NF3lnq-X4Ud6PEFa1iJsocMfE2JyvBiDmfX1WNmlutuXPEUD_QVgioLZOrxRrQsjG9O6XB0="
	pythonPayload     = "python-token-payload"
)

// Parity-exempt: Go crypto primitive; Python delegates to cryptography.fernet,
func TestFernetDecryptsPythonToken(t *testing.T) {
	f, err := NewFernet(pythonFernetKey)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.Decrypt(pythonFernetToken)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != pythonPayload {
		t.Fatalf("payload = %q", plain)
	}
}

// Parity-exempt: Go crypto primitive; Python's Fernet round trip is covered by
func TestFernetRoundTrip(t *testing.T) {
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFernet(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"", "hello", strings.Repeat("x", 1000), "非 ASCII 文本"} {
		token, err := f.Encrypt([]byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		plain, err := f.Decrypt(token)
		if err != nil {
			t.Fatal(err)
		}
		if string(plain) != payload {
			t.Fatalf("round trip %q -> %q", payload, plain)
		}
	}
}

// Parity-exempt: Go crypto primitive; HMAC authentication is library behavior
func TestFernetRejectsTampering(t *testing.T) {
	key, _ := GenerateFernetKey()
	f, _ := NewFernet(key)
	token, _ := f.Encrypt([]byte("payload"))

	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := base64.URLEncoding.EncodeToString(raw)
	if _, err := f.Decrypt(tampered); err == nil || !strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("tampered token accepted: %v", err)
	}
}

// Parity-exempt: Go crypto primitive; key validation lives in the cryptography
func TestFernetRejectsBadKey(t *testing.T) {
	if _, err := NewFernet("not-a-key"); err == nil {
		t.Fatal("invalid key accepted")
	}
	if _, err := NewFernet(base64.URLEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short key accepted")
	}
}

// Parity-exempt: Go crypto primitive; Python's Fernet has no built-in TTL check,
func TestFernetTTL(t *testing.T) {
	key, _ := GenerateFernetKey()
	f, _ := NewFernet(key)

	old := time.Now().UTC().Add(-time.Hour)
	token, err := f.EncryptAt(old, []byte("stale"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.DecryptTTL(token, time.Minute); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("stale token accepted: %v", err)
	}
	if _, err := f.DecryptTTL(token, 0); err != nil {
		t.Fatalf("ttl=0 should skip the age check: %v", err)
	}
}
