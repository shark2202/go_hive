package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
)

// TestStreamTokenRefreshRoundTrip covers the runtime's rotation protocol:
// POST /user/refresh-stream-token-from-stream {"streamToken": old} ->
// {"streamToken": new}, 24h TTL, verified by signature alone.
func TestStreamTokenRefreshRoundTrip(t *testing.T) {
	e := newEnv(t)
	old := e.mintStream(1 * time.Hour)
	resp, raw := e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": old}, "", "")
	body := e.decodeJSON(resp, raw, http.StatusOK)
	fresh, _ := body["streamToken"].(string)
	if fresh == "" || fresh == old {
		t.Fatalf("streamToken not rotated: %v", body)
	}
	claims, err := auth.Verify([]byte(testStream), fresh)
	if err != nil {
		t.Fatalf("verify fresh token: %v", err)
	}
	if claims.Typ != auth.TokenTypeStream || claims.UserID != testUser || claims.TeamID != testTeam {
		t.Fatalf("claims = %+v", claims)
	}
	// The clients only decode exp (streamtoken_refresh.py decode_jwt_exp).
	exp, ok := auth.DecodeExp(fresh)
	if !ok {
		t.Fatalf("client-style exp decode failed")
	}
	remaining := time.Until(time.Unix(exp, 0))
	if remaining < 23*time.Hour || remaining > 25*time.Hour {
		t.Fatalf("exp = %s away, want ~24h", remaining)
	}
}

func TestStreamTokenRefreshRejectsBadTokens(t *testing.T) {
	e := newEnv(t)
	expiredSigner := auth.NewSigner([]byte(testStream), func() time.Time { return time.Now().Add(-48 * time.Hour) })
	expired, err := expiredSigner.MintStreamToken(testUser, testTeam, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrongSecret, err := auth.NewSigner([]byte("some-other-secret"), nil).MintStreamToken(testUser, testTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		token string
	}{
		{"expired", expired},
		{"wrong secret", wrongSecret},
		{"garbage", "garbage"},
		{"empty", ""},
		{"user jwt (no typ=stream)", e.jwt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := e.do(http.MethodPost, "/user/refresh-stream-token-from-stream", map[string]any{"streamToken": tc.token}, "", "")
			body := e.decodeJSON(resp, raw, http.StatusUnauthorized)
			if body["error"] != "hive_stream_token_invalid" {
				t.Fatalf("body = %v", body)
			}
		})
	}
}

// TestLegacyRefreshEndpointNotRegistered documents M1 scope: the desktop-only
// legacy path (/user/refresh-stream-token) is still UNKNOWN in the contract and
// is deferred to the streamToken milestone.
func TestLegacyRefreshEndpointNotRegistered(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.authed(http.MethodPost, "/user/refresh-stream-token", map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d want 404 (out of M1 scope)", resp.StatusCode)
	}
}
