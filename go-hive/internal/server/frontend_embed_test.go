package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// Parity-exempt: embedded dashboard serving has no Python counterpart; the
// disk path is covered by TestStaticServingAndIndexFallback.
func TestEmbeddedStaticServingAndIndexFallback(t *testing.T) {
	index := "<!doctype html><title>Hive</title>"
	script := "console.log('hive')"
	assets := fstest.MapFS{
		"index.html":    {Data: []byte(index)},
		"assets/app.js": {Data: []byte(script)},
	}
	app := newAppForTest(t, AppDeps{FrontendDist: t.TempDir()})
	app.distDir = ""
	app.distFS = assets
	app.distFiles = http.FileServer(http.FS(assets))

	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", recorder.Code)
	}
	if recorder.Body.String() != script {
		t.Fatalf("asset body = %q", recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
		t.Fatalf("asset content-type = %q", got)
	}

	for _, target := range []string{"/", "/colonies/colony_a"} {
		recorder = httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", target, recorder.Code)
		}
		if recorder.Body.String() != index {
			t.Fatalf("%s body = %q, want the index page", target, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
			t.Fatalf("%s content-type = %q", target, got)
		}
	}

	// Upstream's fallback has no /api or asset-extension exception: any path
	// that is not a file in the embedded dist answers with index.html.
	for _, target := range []string{"/assets/missing.js", "/api/nonexistent"} {
		recorder = httptest.NewRecorder()
		app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200 (index fallback)", target, recorder.Code)
		}
		if recorder.Body.String() != index {
			t.Fatalf("%s body = %q, want the index page", target, recorder.Body.String())
		}
	}
}

// Parity-exempt: embed tag selection is a Go build concern with no Python
// counterpart; no-tag builds must not register the dashboard catch-all when
// no disk dist exists.
func TestNoFrontendWithoutDiskOrEmbedded(t *testing.T) {
	app := NewApp(AppDeps{
		DesktopToken: new(string),
		Logger:       discardLogger(),
		FrontendDist: "",
	})
	// FrontendDistDir may discover a dist in this checkout; force the
	// no-source shape the standalone no-tag binary has.
	app.distDir = ""
	app.distFS = nil
	if app.HasFrontend() {
		t.Fatal("HasFrontend = true without a disk or embedded dist")
	}
}
