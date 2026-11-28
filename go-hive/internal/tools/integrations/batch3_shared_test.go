package integrations

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestBatch3ImageAndScrapeShareArtifactDir proves the two writers resolve
// through the same helper: the same environment produces the same directory,
// including "~" expansion.
// Parity-exempt: cross-tool invariant this change introduces; upstream tests
// cover each resolver separately.
func TestBatch3ImageAndScrapeShareArtifactDir(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, "hive-root", "tool-artifacts")

	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + batch3TinyPNGBase64 + `"}]}`))
	}))
	defer imageServer.Close()
	imageRegistry := batch3MediaTestRegistry(t, imageServer, map[string]string{
		"HIVE_API_KEY": "stream-token",
		"HIVE_HOME":    "~/hive-root",
	}, withBatch3UserHomeDir(func() (string, error) { return home, nil }))
	imageResult := batch3ImageSummary(t, batch3Run(t, imageRegistry, "image_generate", map[string]any{"prompt": "x"}))
	imageImages, _ := imageResult["images"].([]any)
	if len(imageImages) != 1 {
		t.Fatalf("images = %v", imageResult["images"])
	}
	imagePath, _ := imageImages[0].(map[string]any)["path"].(string)
	if got := filepath.Dir(imagePath); got != want {
		t.Fatalf("image dir = %q, want %q", got, want)
	}

	t.Setenv("HIVE_STORAGE_PATH", "")
	t.Setenv("HIVE_HOME", "~/hive-root")
	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><main><p>Body</p></main></body></html>`))
	}))
	defer htmlServer.Close()
	scrapeRegistry := batch3TestRegistry(t, htmlServer.URL, withBatch3ScrapeHomeDir(func() (string, error) { return home, nil }))
	scrapeResult := batch3Execute(t, scrapeRegistry, "web_scrape", map[string]any{"url": "https://example.com/page"})
	if message, failed := scrapeResult["error"]; failed {
		t.Fatalf("web_scrape = %v", message)
	}
	savedTo, _ := scrapeResult["saved_to"].(string)
	if got := filepath.Dir(savedTo); got != want {
		t.Fatalf("scrape dir = %q, want %q", got, want)
	}
}
