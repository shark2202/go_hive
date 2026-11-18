package server

// Parity-exempt: Go-only log hygiene; the upstream access logger writes the
// raw request URI, which can include OAuth codes or webhook tokens.

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity-exempt: Go-only log hygiene; no upstream test pins redaction.
func TestAccessLogRedactsSensitiveQueryValues(t *testing.T) {
	var output bytes.Buffer
	app := &App{logger: log.New(&output, "", 0)}
	handler := app.accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := httptest.NewRequest("GET", "/api/config/callback?code=one-time&state=visible&access_token=secret-value&ok=1", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	logged := output.String()
	for _, secret := range []string{"one-time", "secret-value"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("access log leaked %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, "state=visible") || !strings.Contains(logged, "ok=1") {
		t.Fatalf("non-sensitive query values were dropped: %s", logged)
	}
	if strings.Count(logged, "REDACTED") != 2 {
		t.Fatalf("expected two redactions: %s", logged)
	}
}

// Parity-exempt: Go-only helper coverage for the access-log redaction list.
func TestSensitiveQueryKeyMatchesSuffixes(t *testing.T) {
	for _, name := range []string{"token", "api_key", "client_secret", "refresh_token", "x-api-key", "password"} {
		if !sensitiveQueryKey(name) {
			t.Fatalf("sensitiveQueryKey(%q) = false", name)
		}
	}
	for _, name := range []string{"state", "page", "q", "monkey"} {
		if sensitiveQueryKey(name) {
			t.Fatalf("sensitiveQueryKey(%q) = true", name)
		}
	}
}
