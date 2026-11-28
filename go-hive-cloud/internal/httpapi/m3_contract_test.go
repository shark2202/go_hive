package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
	"github.com/shark2202/go_hive/go-hive-cloud/internal/httpapi"
)

const m3CurrentSecret = "m3-current-stream-secret-0123456789"
const m3PreviousSecret = "m3-previous-stream-secret-0123456789"

// m3Env builds a server with a current and a previous streamToken secret, the
// exact configuration of a master-secret rotation window.
func m3Env(t *testing.T) *env {
	t.Helper()
	return newEnv(t, func(cfg *httpapi.Config) {
		cfg.StreamSecrets = [][]byte{[]byte(m3CurrentSecret), []byte(m3PreviousSecret)}
	})
}

// TestStreamTokenRotationWindow pins the M3 rotation protocol:
//   - a token signed with the previous secret still authenticates the refresh
//     endpoint while that secret is configured;
//   - the replacement token is signed with the current (first) secret;
//   - the rotated token keeps user_id/team_id and gets the 24h TTL;
//   - rotation does not revoke the old token before its own exp (there is no
//     revocation ledger in the observed contract).
func TestStreamTokenRotationWindow(t *testing.T) {
	e := m3Env(t)
	previousSigner := auth.NewSigner([]byte(m3PreviousSecret), nil)
	old, err := previousSigner.MintStreamToken(testUser, testTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	resp, raw := e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": old}, "", "")
	body := e.decodeJSON(resp, raw, http.StatusOK)
	fresh, _ := body["streamToken"].(string)
	if fresh == "" || fresh == old {
		t.Fatalf("streamToken not rotated: %v", body)
	}
	claims, err := auth.VerifyStreamToken([][]byte{[]byte(m3CurrentSecret)}, fresh)
	if err != nil {
		t.Fatalf("fresh token must verify with the current secret: %v", err)
	}
	if claims.UserID != testUser || claims.TeamID != testTeam || claims.Typ != auth.TokenTypeStream {
		t.Fatalf("claims = %+v", claims)
	}
	exp, ok := auth.DecodeExp(fresh)
	if !ok {
		t.Fatal("client-style exp decode failed")
	}
	if remaining := time.Until(time.Unix(exp, 0)); remaining < 23*time.Hour || remaining > 25*time.Hour {
		t.Fatalf("exp = %s away, want ~24h", remaining)
	}
	if _, err := auth.VerifyStreamToken([][]byte{[]byte(m3PreviousSecret)}, fresh); err == nil {
		t.Fatal("rotated token must not verify with the previous secret")
	}

	// No revocation ledger: the old token remains valid until exp.
	resp, raw = e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": old}, "", "")
	e.decodeJSON(resp, raw, http.StatusOK)

	// The same old credential is still accepted by the LLM credential face
	// (400 "upstream not configured" — never 401, never a faked 200).
	resp, raw = e.do(http.MethodPost, "/v1/messages", map[string]any{"model": "queen"}, "Bearer", old)
	e.decodeJSON(resp, raw, http.StatusBadRequest)
}

// TestStreamTokenRejectedAfterRotationWindow is the other half of the rotation
// contract: once the previous secret is removed, tokens signed with it are
// rejected by both the refresh endpoint and the LLM validation face.
func TestStreamTokenRejectedAfterRotationWindow(t *testing.T) {
	e := newEnv(t) // only the current testStream secret
	old, err := auth.NewSigner([]byte(m3PreviousSecret), nil).MintStreamToken(testUser, testTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": old}, "", "")
	body := e.decodeJSON(resp, raw, http.StatusUnauthorized)
	if body["error"] != "hive_stream_token_invalid" {
		t.Fatalf("refresh body = %v", body)
	}
	assertLLMTokenInvalid(t, e, old)
}

// TestStreamTokenRefreshRejectsMalformedClaims hardens the refresh check beyond
// signature: typ must be stream and the identity claims must be usable.
func TestStreamTokenRefreshRejectsMalformedClaims(t *testing.T) {
	e := m3Env(t)
	signer := auth.NewSigner([]byte(m3CurrentSecret), nil)
	streamNoTeam, err := signer.Sign(auth.Claims{
		Sub: testUser, UserID: testUser, Typ: auth.TokenTypeStream,
		Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	streamBadTeam, err := signer.Sign(auth.Claims{
		Sub: testUser, UserID: testUser, TeamID: "../escape", Typ: auth.TokenTypeStream,
		Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"no team": streamNoTeam, "bad team": streamBadTeam} {
		t.Run(name, func(t *testing.T) {
			resp, raw := e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": token}, "", "")
			body := e.decodeJSON(resp, raw, http.StatusUnauthorized)
			if body["error"] != "hive_stream_token_invalid" {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

// TestStreamTokenRefreshBodyErrors pins the two JSON body errors documented in
// the surface (400 invalid JSON body; empty token behaves like a bad token).
func TestStreamTokenRefreshBodyErrors(t *testing.T) {
	e := m3Env(t)
	resp, raw := e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", "{not json", "", "")
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "invalid JSON body" {
		t.Fatalf("body = %v", body)
	}
	resp, raw = e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": "   "}, "", "")
	body = e.decodeJSON(resp, raw, http.StatusUnauthorized)
	if body["error"] != "hive_stream_token_invalid" {
		t.Fatalf("body = %v", body)
	}
}

// TestLLMMessagesTokenValidation covers the preserved M3 `hive` LLM credential
// surface: the observed Bearer credential check without an upstream. Valid
// credentials (streamToken or operator JWT) answer 400
// llm_upstream_not_configured — never 401 and never a fake 200 — and
// invalid/expired/missing credentials answer the observed 401
// {"error":{"type":"hive_stream_token_invalid"}}.
func TestLLMMessagesTokenValidation(t *testing.T) {
	e := m3Env(t)
	expiredSigner := auth.NewSigner([]byte(m3CurrentSecret), func() time.Time { return time.Now().Add(-48 * time.Hour) })
	expired, err := expiredSigner.MintStreamToken(testUser, testTeam, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := auth.NewSigner([]byte("not-the-configured-secret"), nil).MintStreamToken(testUser, testTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	invalid := []struct {
		name  string
		token string
	}{
		{"garbage", "garbage"},
		{"wrong signature", wrong},
		{"expired", expired},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			assertLLMTokenInvalid(t, e, tc.token)
		})
	}

	// Missing or non-Bearer auth is a 401 too, with the same observed body.
	for name, header := range map[string][2]string{
		"missing":  {"", ""},
		"basic":    {"Basic", "abc"},
		"no value": {"Bearer", ""},
	} {
		t.Run(name, func(t *testing.T) {
			resp, raw := e.do(http.MethodPost, "/v1/messages", map[string]any{"model": "queen"}, header[0], header[1])
			body := e.decodeJSON(resp, raw, http.StatusUnauthorized)
			assertLLMInvalidBody(t, body)
		})
	}

	// Valid streamToken: credential accepted; no upstream configured.
	stream := e.mintStream(time.Hour)
	resp, raw := e.do(http.MethodPost, "/v1/messages", map[string]any{"model": "queen"}, "Bearer", stream)
	assertLLMNotConfigured(t, resp, raw)

	// Valid operator JWT: the surface documents Bearer streamToken/JWT.
	resp, raw = e.do(http.MethodPost, "/v1/messages", map[string]any{"model": "queen"}, "Bearer", e.jwt)
	assertLLMNotConfigured(t, resp, raw)

	// No configuration ever yields 200 or the retired M3 placeholder from the
	// unconfigured credential face.
	if resp, _ := e.do(http.MethodPost, "/v1/messages", map[string]any{}, "", ""); resp.StatusCode == http.StatusOK {
		t.Fatal("unauthenticated /v1/messages returned 200")
	}
}

func assertLLMNotConfigured(t *testing.T, resp *http.Response, raw []byte) {
	t.Helper()
	body := map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, raw)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d want 400 (body %s)", resp.StatusCode, raw)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["type"] != httpapi.LLMUpstreamNotConfiguredType {
		t.Fatalf("error.type = %v, want %s", errObj["type"], httpapi.LLMUpstreamNotConfiguredType)
	}
	if errObj["type"] == "llm_proxy_not_implemented" {
		t.Fatal("M3 placeholder must not be returned by M5")
	}
}

func assertLLMTokenInvalid(t *testing.T, e *env, token string) {
	t.Helper()
	resp, raw := e.do(http.MethodPost, "/v1/messages", map[string]any{"model": "queen"}, "Bearer", token)
	body := e.decodeJSON(resp, raw, http.StatusUnauthorized)
	assertLLMInvalidBody(t, body)
}

func assertLLMInvalidBody(t *testing.T, body map[string]any) {
	t.Helper()
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error is not an object: %v", body)
	}
	if errObj["type"] != httpapi.LLMTokenInvalidType {
		t.Fatalf("error.type = %v, want %s", errObj["type"], httpapi.LLMTokenInvalidType)
	}
	if len(errObj) != 1 {
		t.Fatalf("observed body has only the type field; got %v", errObj)
	}
	goldenJSON(t, "llm-token-invalid", body)
}
