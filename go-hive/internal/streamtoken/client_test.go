package streamtoken

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity: core/tests/test_streamtoken_refresh.py::TestPostRefresh::test_happy_path_returns_new_token
func TestClientRefreshPostsCurrentToken(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		payload, _ := io.ReadAll(request.Body)
		gotBody = string(payload)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"success":true,"streamToken":"new-token-xyz"}`)
	}))
	defer server.Close()

	token, err := NewClient().Refresh(context.Background(), server.URL+"/", "old-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if token != "new-token-xyz" {
		t.Fatalf("token = %q, want new-token-xyz", token)
	}
	if gotPath != RefreshPath {
		t.Fatalf("path = %q, want %q", gotPath, RefreshPath)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("decode body %q: %v", gotBody, err)
	}
	if body["streamToken"] != "old-token" {
		t.Fatalf("request body = %#v, want streamToken=old-token", body)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestPostRefresh::test_non_200_returns_none
func TestClientRefreshRejectsNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"success":false,"msg":"expired"}`)
	}))
	defer server.Close()

	token, err := NewClient().Refresh(context.Background(), server.URL, "old-token")
	if err == nil || token != "" {
		t.Fatalf("Refresh = (%q, %v), want (\"\", error)", token, err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want the status in the message", err)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestPostRefresh::test_200_with_missing_streamToken_returns_none
func TestClientRefreshRejectsMissingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, `{"success":true}`)
	}))
	defer server.Close()

	token, err := NewClient().Refresh(context.Background(), server.URL, "old-token")
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("err = %v, want ErrMissingToken", err)
	}
	if token != "" {
		t.Fatalf("token = %q, want empty", token)
	}
}

// Parity: core/tests/test_streamtoken_refresh.py::TestPostRefresh::test_network_error_returns_none
func TestClientRefreshSurfacesTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	baseURL := server.URL
	server.Close()

	if token, err := NewClient().Refresh(context.Background(), baseURL, "old-token"); err == nil || token != "" {
		t.Fatalf("Refresh = (%q, %v), want (\"\", error) after close", token, err)
	}
}
