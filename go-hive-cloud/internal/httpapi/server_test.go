package httpapi_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
)

// TestAuthMissingToken pins the 401 body byte-for-byte against the client's
// signed-out copy (go_hive/go-hive/internal/globaldb/client.go:45).
func TestAuthMissingToken(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/v1/global-db/sql"},
		{http.MethodPost, "/v1/global-db/query"},
		{http.MethodGet, "/v1/global-db/tables"},
		{http.MethodGet, "/v1/inbox/runtime/stream?runtime_id=rt"},
		{http.MethodPost, "/v1/inbox/messages"},
		{http.MethodPost, "/internal/inbox/reply"},
	}
	for _, tc := range cases {
		resp, raw := e.do(tc.method, tc.path, nil, "", "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s: status %d want 401", tc.method, tc.path, resp.StatusCode)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode 401 body: %v", err)
		}
		if body["error"] != "Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)." {
			t.Fatalf("%s %s: unexpected 401 text %q", tc.method, tc.path, body["error"])
		}
	}
}

func TestAuthRejectsBadTokens(t *testing.T) {
	e := newEnv(t)
	otherSigner := auth.NewSigner([]byte("a-different-secret"), nil)
	wrongSignature, err := otherSigner.MintUserToken(testUser, testTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expiredSigner := auth.NewSigner([]byte(testSecret), func() time.Time { return time.Now().Add(-2 * time.Hour) })
	expired, err := expiredSigner.MintUserToken(testUser, testTeam, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	streamToken := e.mintStream(time.Hour)

	cases := []struct {
		name  string
		token string
	}{
		{"wrong signature", wrongSignature},
		{"expired", expired},
		{"garbage", "not.a.jwt"},
		{"stream token as operator jwt", streamToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := e.do(http.MethodPost, "/v1/global-db/query", map[string]any{"sql": "SELECT 1 AS one"}, "jwt", tc.token)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status %d want 401 (body %s)", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), "Sign in to use the shared global DB") {
				t.Fatalf("401 body missing signed-out text: %s", raw)
			}
		})
	}
}

// TestAuthBearerScheme documents that both Authorization schemes the clients
// use are accepted: `jwt <token>` (global-db) and `Bearer <token>` (inbox,
// listeners.go:455).
func TestAuthBearerScheme(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.do(http.MethodPost, "/v1/global-db/query", map[string]any{"sql": "SELECT 1 AS one"}, "bearer", e.jwt)
	body := e.decodeJSON(resp, raw, http.StatusOK)
	if body["rowCount"].(float64) != 1 {
		t.Fatalf("rowCount = %v", body["rowCount"])
	}
}

func TestHealthAndGatewayAvailability(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.do(http.MethodGet, "/health", nil, "", "")
	body := e.decodeJSON(resp, raw, http.StatusOK)
	if body["status"] != "ok" || body["service"] != "hive-cloud" || body["version"] != "test-m1" {
		t.Fatalf("health body = %v", body)
	}
	goldenJSON(t, "health", body)

	resp, raw = e.do(http.MethodGet, "/v1/gateway/availability?from=hive-open", nil, "", "")
	body = e.decodeJSON(resp, raw, http.StatusOK)
	if body["available"] != true || body["from"] != "hive-open" {
		t.Fatalf("gateway body = %v", body)
	}
	goldenJSON(t, "gateway-availability", body)
}

// TestTeamScoping proves the team comes from the JWT (clients never send one)
// and that each team gets its own SQLite file.
func TestTeamScoping(t *testing.T) {
	e := newEnv(t)
	_, raw := e.authed(http.MethodPost, "/v1/global-db/sql", map[string]any{"sql": "CREATE TABLE t (id TEXT PRIMARY KEY, v TEXT)"})
	if !strings.Contains(string(raw), `"rowCount"`) {
		t.Fatalf("create table: %s", raw)
	}
	otherJWT, err := e.signer.MintUserToken("u_other", "team_other", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw := e.do(http.MethodGet, "/v1/global-db/tables", nil, "jwt", otherJWT)
	body := e.decodeJSON(resp, raw, http.StatusOK)
	if tables := body["tables"].([]any); len(tables) != 0 {
		t.Fatalf("other team sees tables: %v", tables)
	}
	for _, team := range []string{testTeam, "team_other"} {
		path := e.dir + "/teams/" + team + ".db"
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected team db %s: %v", path, err)
		}
	}
}
