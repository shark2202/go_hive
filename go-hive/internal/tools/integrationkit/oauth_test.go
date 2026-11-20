package integrationkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Parity-exempt: Go-side OAuth2 refresh primitive; upstream implements the
// same grant in core/framework/credentials/oauth2/base_provider.py but its
// pytest suite mocks the provider and never exercises token rotation.
//
// TestTokenRefresherUsesRotatedRefreshToken pins the rotation rule: when a
// provider returns a new refresh_token, the next grant must send it, and the
// forced refresh must replace the normal cache entry.
func TestTokenRefresherUsesRotatedRefreshToken(t *testing.T) {
	var mu sync.Mutex
	var forms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		forms = append(forms, r.PostForm)
		round := len(forms)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch round {
		case 1:
			_, _ = io.WriteString(w, `{"access_token":"access-1","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-2"}`)
		default:
			_, _ = io.WriteString(w, `{"access_token":"access-2","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-3"}`)
		}
	}))
	t.Cleanup(server.Close)

	refresher := NewTokenRefresher(server.Client())
	cfg := &OAuth2Refresh{
		Module:       "google",
		TokenURL:     server.URL,
		ClientID:     "client",
		RefreshToken: "refresh-1",
	}

	first, err := refresher.Refresh(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if first != "access-1" {
		t.Fatalf("first access token = %q", first)
	}
	cached, err := refresher.Token(context.Background(), cfg)
	if err != nil {
		t.Fatalf("cached token: %v", err)
	}
	if cached != "access-1" {
		t.Fatalf("cached token = %q (forced refresh must fill the normal cache)", cached)
	}
	second, err := refresher.Refresh(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if second != "access-2" {
		t.Fatalf("second access token = %q", second)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(forms) != 2 {
		t.Fatalf("grants = %d", len(forms))
	}
	if got := forms[1].Get("refresh_token"); got != "refresh-2" {
		t.Fatalf("second grant refresh_token = %q, want the rotated value", got)
	}
}

// Parity-exempt: upstream raises CredentialRefreshError inside the store; the
// two static-token outcomes (pass-through vs explicit failure) are Go-side.
//
// TestTokenRefresherStaticTokenSemantics pins the two static-token outcomes:
// unknown expiry passes through, a known expired token fails with the env name.
func TestTokenRefresherStaticTokenSemantics(t *testing.T) {
	refresher := NewTokenRefresher(nil)
	unknown := &OAuth2Refresh{Module: "google", AccessToken: "static", TokenEnv: "GOOGLE_ACCESS_TOKEN"}
	token, err := refresher.Token(context.Background(), unknown)
	if err != nil || token != "static" {
		t.Fatalf("unknown expiry = (%q, %v)", token, err)
	}

	expired := &OAuth2Refresh{Module: "google", AccessToken: "static", TokenEnv: "GOOGLE_ACCESS_TOKEN",
		ExpiresAt: timePtr(-1 * 60)}
	_, expiredErr := refresher.Token(context.Background(), expired)
	if expiredErr == nil {
		t.Fatal("expired static token accepted")
	}
	var refreshErr *OAuthRefreshError
	if !errors.As(expiredErr, &refreshErr) || refreshErr.Code != "no_refresh_token" {
		t.Fatalf("expired static token error = %v", expiredErr)
	}
	if refreshErr.TokenEnv != "GOOGLE_ACCESS_TOKEN" {
		t.Fatalf("TokenEnv = %q", refreshErr.TokenEnv)
	}
}

// timePtr builds an expiry that many seconds away from now.
func timePtr(secondsFromNow int) *time.Time {
	at := time.Now().UTC().Add(time.Duration(secondsFromNow) * time.Second)
	return &at
}
