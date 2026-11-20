package browserbridge

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type extensionManifest struct {
	ManifestVersion int    `json:"manifest_version"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	Background      struct {
		ServiceWorker string `json:"service_worker"`
	} `json:"background"`
	SidePanel struct {
		DefaultPath string `json:"default_path"`
	} `json:"side_panel"`
	Action struct {
		DefaultIcon map[string]string `json:"default_icon"`
	} `json:"action"`
	Icons map[string]string `json:"icons"`
}

func loadManifest(t *testing.T) (extensionManifest, string) {
	t.Helper()
	dir := "extension"
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m extensionManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest.json is not valid JSON: %v", err)
	}
	return m, dir
}

func TestExtensionManifestAndFiles(t *testing.T) {
	m, dir := loadManifest(t)
	if m.ManifestVersion != 3 {
		t.Fatalf("manifest_version=%d", m.ManifestVersion)
	}
	if m.Version != "1.7.4" || m.Name != "Hive Browser Bridge" {
		t.Fatalf("unexpected identity: %s %s", m.Name, m.Version)
	}
	refs := map[string]string{
		"background.service_worker": m.Background.ServiceWorker,
		"side_panel.default_path":   m.SidePanel.DefaultPath,
	}
	for size, p := range m.Icons {
		refs["icons."+size] = p
	}
	for size, p := range m.Action.DefaultIcon {
		refs["action.default_icon."+size] = p
	}
	for name, ref := range refs {
		if ref == "" {
			t.Fatalf("%s is empty", name)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(ref))); err != nil {
			t.Fatalf("%s references missing file %q: %v", name, ref, err)
		}
	}
	for _, required := range []string{
		"manifest.json", "background.js", "offscreen.html", "offscreen.js",
		"popup.html", "popup.js", "sidepanel.html", "sidepanel.js", "wordlist.js",
		"README.md", "SOURCE.md", "LICENSE",
	} {
		if _, err := os.Stat(filepath.Join(dir, required)); err != nil {
			t.Fatalf("missing runtime file %q", required)
		}
	}
	src, err := os.ReadFile(filepath.Join(dir, "SOURCE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "tools/browser-extension") || !strings.Contains(string(src), "1.7.4") {
		t.Fatal("SOURCE.md does not record upstream path/version")
	}
}

func TestExtensionPackageZipShape(t *testing.T) {
	if _, err := exec.LookPath("zip"); err != nil {
		t.Skip("zip not installed")
	}
	if _, err := exec.LookPath("unzip"); err != nil {
		t.Skip("unzip not installed")
	}
	cmd := exec.Command("bash", filepath.Join("scripts", "package_extension.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package script failed: %v\n%s", err, out)
	}
	zipPath := filepath.Join("dist", "hive-browser-bridge-1.7.4.zip")
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	entries := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		entries[f.Name] = true
	}
	if !entries["manifest.json"] {
		t.Fatal("manifest.json is not at zip root")
	}
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		packed, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.ReadFile(filepath.Join("extension", "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(packed, source) {
			t.Fatal("packaged manifest differs from extension/manifest.json")
		}
	}
	for name := range entries {
		if strings.HasSuffix(name, "/manifest.json") {
			t.Fatalf("manifest.json nested under %q", name)
		}
	}
	m, _ := loadManifest(t)
	for _, ref := range []string{
		m.Background.ServiceWorker, m.SidePanel.DefaultPath,
	} {
		if !entries[ref] {
			t.Fatalf("manifest reference %q missing from zip", ref)
		}
	}
	for _, ref := range m.Icons {
		if !entries[ref] {
			t.Fatalf("icon %q missing from zip", ref)
		}
	}
	for _, ref := range m.Action.DefaultIcon {
		if !entries[ref] {
			t.Fatalf("action icon %q missing from zip", ref)
		}
	}
}
