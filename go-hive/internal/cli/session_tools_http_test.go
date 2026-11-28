package cli

// Catalogue tests for the native HTTP family. They pin registration, the lazy
// tier, the config gate and per-Hive-session cookie isolation. The transport
// details live in internal/nativehttp; this file proves the join between the
// adapter and sessionToolCatalogue.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

func writeHTTPConfig(t *testing.T, path, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func httpResultBody(t *testing.T, content string) string {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		t.Fatalf("decode result %q: %v", content, err)
	}
	body, _ := parsed["body"].(string)
	return body
}

// Parity-exempt: native HTTP tools are a Go-only extension; no Python catalogue test.
func TestSessionCatalogueHTTPFamily(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	writeHTTPConfig(t, filepath.Join(homeRoot, "http_tools.json"), `{"allow_private_hosts":true}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	defer func() { _ = catalogue.Close() }()

	def, ok := codeIntelDef(t, catalogue, builtin.ToolHTTPRequest)
	if !ok {
		t.Fatal("http_request definition missing from the session catalogue")
	}
	if flatSurfaceGroupedModule(def) {
		t.Fatal("http_request was misclassified as a grouped integration module")
	}
	if names := defNames(catalogue.ModelDefs()); flatContainsName(names, builtin.ToolHTTPRequest) {
		t.Fatal("http_request leaked into the frozen default surface")
	}
	if lazy := defNames(catalogue.LazyModelDefs()); !flatContainsName(lazy, builtin.ToolHTTPRequest) {
		t.Fatalf("http_request missing from the lazy layer: %v", lazy)
	}

	content, isError, err := catalogue.Execute(context.Background(), builtin.ToolHTTPRequest, map[string]any{"url": server.URL})
	if err != nil {
		t.Fatalf("http_request execute: %v", err)
	}
	if isError {
		t.Fatalf("http_request reported a tool error: %s", content)
	}
	if body := httpResultBody(t, content); body != "hello" {
		t.Fatalf("http_request body = %q, want hello", body)
	}
}

// Parity-exempt: Go-only HTTP tools config gate; no Python counterpart.
func TestSessionCatalogueHTTPConfigGate(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	userConfig := filepath.Join(homeRoot, "http_tools.json")
	sessionConfig := filepath.Join(sessionDir, ".hive", "http_tools.json")

	newCatalogue := func() *sessionToolCatalogue {
		return newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	}
	hasRequest := func(catalogue *sessionToolCatalogue) bool {
		_, ok := codeIntelDef(t, catalogue, builtin.ToolHTTPRequest)
		return ok
	}

	// Missing config: default enabled.
	on := newCatalogue()
	if !hasRequest(on) {
		t.Fatal("http tools absent with the default config")
	}
	_ = on.Close()

	// User enabled=false disables the family.
	writeHTTPConfig(t, userConfig, `{"enabled":false}`)
	off := newCatalogue()
	if hasRequest(off) {
		t.Fatal("http tools registered while enabled=false")
	}
	_ = off.Close()

	// The session directory is agent-writable and must not opt the family in
	// or out.
	_ = os.Remove(userConfig)
	writeHTTPConfig(t, sessionConfig, `{"enabled":false}`)
	sessionIgnored := newCatalogue()
	if !hasRequest(sessionIgnored) {
		t.Fatal("session-dir config disabled http tools; only $HIVE_HOME may gate them")
	}
	_ = sessionIgnored.Close()

	writeHTTPConfig(t, userConfig, `{"enabled":false}`)
	writeHTTPConfig(t, sessionConfig, `{"enabled":true}`)
	stillOff := newCatalogue()
	if hasRequest(stillOff) {
		t.Fatal("session-dir config enabled http tools; only $HIVE_HOME may gate them")
	}
	_ = stillOff.Close()

	// HIVE_HTTP_TOOLS=0 is a hard kill switch.
	writeHTTPConfig(t, userConfig, `{"enabled":true}`)
	t.Setenv("HIVE_HTTP_TOOLS", "0")
	killed := newCatalogue()
	if hasRequest(killed) {
		t.Fatal("HIVE_HTTP_TOOLS=0 did not disable the family")
	}
	_ = killed.Close()
}

// Parity-exempt: per-session cookie isolation exists only in the Go catalogue.
func TestSessionCatalogueHTTPCookieIsolation(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionA := testTempDir(t)
	sessionB := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	writeHTTPConfig(t, filepath.Join(homeRoot, "http_tools.json"), `{"allow_private_hosts":true}`)

	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "host", Value: "1", Path: "/"})
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	catalogueA := newSessionToolCatalogue(sessionA, builtin.New(sessionA), home)
	defer func() { _ = catalogueA.Close() }()
	catalogueB := newSessionToolCatalogue(sessionB, builtin.New(sessionB), home)
	defer func() { _ = catalogueB.Close() }()

	run := func(catalogue *sessionToolCatalogue, path string) string {
		t.Helper()
		content, isError, err := catalogue.Execute(context.Background(), builtin.ToolHTTPRequest, map[string]any{"url": server.URL + path})
		if err != nil || isError {
			t.Fatalf("execute %s: err=%v content=%s", path, err, content)
		}
		return httpResultBody(t, content)
	}

	if body := run(catalogueA, "/set"); body != "ok" {
		t.Fatalf("set body = %q", body)
	}
	if body := run(catalogueA, "/echo"); !strings.Contains(body, "host=1") {
		t.Fatalf("session A did not keep its cookie: %q", body)
	}
	if body := run(catalogueB, "/echo"); body != "" {
		t.Fatalf("session B saw session A's cookie: %q", body)
	}

	// A colony worker is a fresh jar, not an inheritance path.
	worker := catalogueA.WithRoot(testTempDir(t))
	defer func() { _ = worker.Close() }()
	if body := run(worker, "/echo"); body != "" {
		t.Fatalf("worker inherited the queen's cookie: %q", body)
	}
}
