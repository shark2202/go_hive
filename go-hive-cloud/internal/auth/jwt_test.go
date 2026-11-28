package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var secret = []byte("unit-test-secret")

func TestSignVerifyRoundTrip(t *testing.T) {
	signer := NewSigner(secret, nil)
	token, err := signer.MintUserToken("u1", "team1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(secret, token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "u1" || claims.TeamID != "team1" || claims.Iss != Issuer || claims.Sub != "u1" {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.Typ != "" {
		t.Fatalf("user token typ = %q", claims.Typ)
	}
	stream, err := signer.MintStreamToken("u1", "team1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	streamClaims, err := Verify(secret, stream)
	if err != nil {
		t.Fatalf("Verify stream: %v", err)
	}
	if streamClaims.Typ != TokenTypeStream {
		t.Fatalf("stream typ = %q", streamClaims.Typ)
	}
}

func TestVerifyRejects(t *testing.T) {
	signer := NewSigner(secret, nil)
	token, _ := signer.MintUserToken("u1", "team1", time.Hour)

	expiredSigner := NewSigner(secret, func() time.Time { return time.Now().Add(-2 * time.Hour) })
	expired, _ := expiredSigner.MintUserToken("u1", "team1", time.Hour)

	parts := strings.Split(token, ".")
	// Tampered payload: swap user id.
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	claims["team_id"] = "attacker"
	tamperedPayload, _ := json.Marshal(claims)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[2]

	// alg=none header with a bogus signature.
	noneHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	noneToken := noneHeader + "." + parts[1] + "."

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"garbage", "not-a-token", ErrInvalid},
		{"wrong secret", mustSign(t, []byte("other"), "u1", "team1"), ErrInvalid},
		{"expired", expired, ErrExpired},
		{"tampered", tampered, ErrInvalid},
		{"alg none", noneToken, ErrInvalid},
		{"empty", "", ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Verify(secret, tc.token); !errors.Is(err, tc.want) {
				t.Fatalf("Verify = %v, want %v", err, tc.want)
			}
		})
	}
}

func mustSign(t *testing.T, secret []byte, user, team string) string {
	t.Helper()
	token, err := NewSigner(secret, nil).MintUserToken(user, team, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestVerifyStreamToken(t *testing.T) {
	current := []byte("current-stream-secret")
	previous := []byte("previous-stream-secret")
	signer := NewSigner(current, nil)
	stream, err := signer.MintStreamToken("u1", "team1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyStreamToken([][]byte{current, previous}, stream)
	if err != nil {
		t.Fatalf("VerifyStreamToken: %v", err)
	}
	if claims.Typ != TokenTypeStream || claims.UserID != "u1" || claims.TeamID != "team1" {
		t.Fatalf("claims = %+v", claims)
	}

	old, err := NewSigner(previous, nil).MintStreamToken("u1", "team1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyStreamToken([][]byte{current, previous}, old); err != nil {
		t.Fatalf("previous-secret token rejected inside the window: %v", err)
	}
	if _, err := VerifyStreamToken([][]byte{current}, old); !errors.Is(err, ErrInvalid) {
		t.Fatalf("previous-secret token outside the window = %v, want ErrInvalid", err)
	}

	userToken, _ := signer.MintUserToken("u1", "team1", time.Hour)
	if _, err := VerifyStreamToken([][]byte{current}, userToken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("user token = %v, want ErrInvalid", err)
	}
	noTeam, err := signer.Sign(Claims{UserID: "u1", Typ: TokenTypeStream, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyStreamToken([][]byte{current}, noTeam); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing team = %v, want ErrInvalid", err)
	}
	badTeam, err := signer.Sign(Claims{UserID: "u1", TeamID: "../x", Typ: TokenTypeStream, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyStreamToken([][]byte{current}, badTeam); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad team = %v, want ErrInvalid", err)
	}
}

func TestDecodeExp(t *testing.T) {
	token := mustSign(t, secret, "u1", "team1")
	exp, ok := DecodeExp(token)
	if !ok || time.Until(time.Unix(exp, 0)) < 59*time.Minute {
		t.Fatalf("DecodeExp = (%d, %v)", exp, ok)
	}
	if _, ok := DecodeExp("junk"); ok {
		t.Fatalf("DecodeExp(junk) should fail")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"jwt abc", "abc", true},
		{"JWT abc", "abc", true},
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"abc", "", false},
		{"", "", false},
		{"Basic abc", "", false},
	}
	for _, tc := range cases {
		got, ok := BearerToken(tc.header)
		if got != tc.want || ok != tc.ok {
			t.Errorf("BearerToken(%q) = (%q, %v), want (%q, %v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestValidTeamID(t *testing.T) {
	for _, id := range []string{"team1", "team-1", "team_1", "a.b"} {
		if !ValidTeamID(id) {
			t.Errorf("ValidTeamID(%q) = false", id)
		}
	}
	for _, id := range []string{"", "..", "a/b", "a b", "a;b", strings.Repeat("x", 65)} {
		if ValidTeamID(id) {
			t.Errorf("ValidTeamID(%q) = true", id)
		}
	}
}
