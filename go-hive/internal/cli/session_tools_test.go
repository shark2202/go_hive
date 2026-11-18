package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// Parity-exempt: assembly guard. The chat session's toolbox must carry the
// ported integration tools next to the builtins, and both dispatch paths must
// stay intact (builtins through the sandbox, integrations through the registry).
func TestSessionToolCatalogueInstallsIntegrations(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	if catalogue.installed == 0 {
		t.Fatal("no integration tools installed")
	}

	defined := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = struct{}{}
	}
	for _, name := range []string{builtin.ToolReadFile, builtin.ToolWriteFile, builtin.ToolTerminalExec} {
		if _, ok := defined[name]; !ok {
			t.Fatalf("builtin %q missing from the catalogue", name)
		}
	}
	integrations := catalogue.registry.RegisteredNames()
	if len(integrations) == 0 {
		t.Fatal("registry has no tools")
	}
	for _, name := range integrations {
		if _, ok := defined[name]; !ok {
			t.Fatalf("integration tool %q missing from the catalogue defs", name)
		}
	}
	if len(catalogue.Defs()) != len(defined) {
		t.Fatalf("duplicate tool definitions: %d defs, %d names", len(catalogue.Defs()), len(defined))
	}

	// Builtin dispatch still runs inside the sandbox root.
	target := filepath.Join("notes", "hello.txt")
	if _, isError, err := catalogue.Execute(context.Background(), builtin.ToolWriteFile, map[string]any{
		"path":    target,
		"content": "hello hive",
	}); err != nil || isError {
		t.Fatalf("write_file: err=%v isError=%v", err, isError)
	}
	content, isError, err := catalogue.Execute(context.Background(), builtin.ToolReadFile, map[string]any{"path": target})
	if err != nil || isError {
		t.Fatalf("read_file: err=%v isError=%v", err, isError)
	}
	if !strings.Contains(content, "hello hive") {
		t.Fatalf("read_file content = %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, target)); err != nil {
		t.Fatalf("builtin wrote outside the session root: %v", err)
	}

	// Integration tools dispatch through the registry. With no credentials the
	// call must report a structured tool error, not panic or claim success.
	name := integrations[0]
	output, isError, err := catalogue.Execute(context.Background(), name, map[string]any{"action": "not-a-real-action"})
	if err != nil {
		t.Fatalf("%s: unexpected transport error: %v", name, err)
	}
	if !isError {
		t.Fatalf("%s: unknown action reported success: %s", name, output)
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil {
		t.Fatalf("%s: error output is not JSON (%q): %v", name, output, jsonErr)
	}
	if _, ok := payload["error"]; !ok {
		t.Fatalf("%s: error payload has no error field: %v", name, payload)
	}
}

// Parity-exempt: assembly guard. The wave-4 hand-written modules register on
// the same catalogue as the catalog-backed integrations, and the senders tool
// follows its feature gate instead of being always-on.
func TestSessionToolCatalogueInstallsWaveFourModules(t *testing.T) {
	root := t.TempDir()
	home := hivehome.Home{Root: root}
	names := func() map[string]struct{} {
		defined := map[string]struct{}{}
		for _, def := range newSessionToolCatalogue(root, builtin.New(root), home).Defs() {
			defined[def.Name] = struct{}{}
		}
		return defined
	}

	got := names()
	for _, name := range []string{"pdf_read", "web_scrape", "excel"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("batch-3 tool %q missing from the catalogue", name)
		}
	}
	if _, ok := got["senders"]; ok {
		t.Fatal("senders registered while email_senders is off")
	}

	t.Setenv("HIVE_EMAIL_SENDERS", "1")
	if _, ok := names()["senders"]; !ok {
		t.Fatal("senders missing from the catalogue while email_senders is on")
	}
}
