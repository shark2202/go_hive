package cli

// Registration, gating and routing tests for the independent code-intelligence
// adapters (internal/astgrep, internal/lsp). They are not integration modules:
// both are catalogue-owned adapters, so these tests pin the join between the
// adapter surface and sessionToolCatalogue rather than re-testing the cores.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// writeASTGrepConfig writes one ast_grep.json gate file for catalogue tests.
func writeASTGrepConfig(t *testing.T, path, payload string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// codeIntelDef returns the catalogue definition for name and fails on a
// duplicate, because a duplicate model-facing name is ambiguous.
func codeIntelDef(t *testing.T, catalogue *sessionToolCatalogue, name string) (llm.ToolDef, bool) {
	t.Helper()
	var found llm.ToolDef
	seen := false
	for _, def := range catalogue.Defs() {
		if def.Name != name {
			continue
		}
		if seen {
			t.Fatalf("duplicate tool definition %q in the session catalogue", name)
		}
		found, seen = def, true
	}
	return found, seen
}

// TestSessionCatalogueAstGrepLazyTier pins ast_grep as a post-1.0 opt-in
// lazy-tier tool: with ast_grep.json explicitly enabled its adapter definition
// is in the catalogue, the frozen 39-name default surface is unchanged,
// search_tools can load it from the deferred layer, and catalogue.Execute runs
// the adapter end-to-end.
//
// Parity-exempt: Go-only session-catalogue adapter test; upstream has no
// ast_grep catalogue entry or lazy-tier surface to mirror.
func TestSessionCatalogueAstGrepLazyTier(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	writeASTGrepConfig(t, filepath.Join(homeRoot, "ast_grep.json"), `{"enabled":true}`)
	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	enableQueenLifecycleTools(catalogue, &hiveserver.Session{}, home)
	defer func() { _ = catalogue.Close() }()

	if err := os.WriteFile(filepath.Join(sessionDir, "main.go"),
		[]byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	def, ok := codeIntelDef(t, catalogue, builtin.ToolAstGrep)
	if !ok {
		t.Fatal("ast_grep definition missing from the session catalogue")
	}
	if want := builtin.AstGrepToolDef(); !reflect.DeepEqual(def, want) {
		t.Fatalf("ast_grep catalogue definition drifted from the adapter:\n got %#v\nwant %#v", def, want)
	}
	if flatSurfaceGroupedModule(def) {
		t.Fatal("ast_grep was misclassified as a grouped integration module")
	}

	// The frozen default surface stays at 39 (+ colony-phase-only extension):
	// ast_grep is opt-in and must not be advertised there.
	if names := defNames(catalogue.ModelDefs()); len(names) != 39+len(flatSurfaceColonyPhaseOnly) {
		t.Fatalf("default flat surface has %d tools, want %d", len(names), 39+len(flatSurfaceColonyPhaseOnly))
	} else if flatContainsName(names, builtin.ToolAstGrep) {
		t.Fatal("ast_grep leaked into the frozen 39-tool default surface")
	}

	// The lazy tier is the opt-in path: search_tools can load it on demand.
	if lazy := defNames(catalogue.LazyModelDefs()); !flatContainsName(lazy, builtin.ToolAstGrep) {
		t.Fatalf("ast_grep missing from the lazy layer: %v", lazy)
	}

	content, isError, err := catalogue.Execute(context.Background(), builtin.ToolAstGrep, map[string]any{
		"pat":  "println($A)",
		"path": filepath.Join(sessionDir, "main.go"),
	})
	if err != nil {
		t.Fatalf("ast_grep execute: %v", err)
	}
	if isError {
		t.Fatalf("ast_grep execute reported a tool error: %s", content)
	}
	if !strings.Contains(content, "main.go:") || !strings.Contains(content, "println(\"hi\")") {
		t.Fatalf("ast_grep result did not come from the adapter: %s", content)
	}

	// The adapter's model-visible recoverable errors keep their isError flag.
	content, isError, err = catalogue.Execute(context.Background(), builtin.ToolAstGrep, map[string]any{})
	if err != nil {
		t.Fatalf("ast_grep invalid input execute: %v", err)
	}
	if !isError || !strings.Contains(content, "pat is required") {
		t.Fatalf("ast_grep invalid input did not preserve isError: isError=%v content=%s", isError, content)
	}
}

// TestSessionCatalogueAstGrepConfigGate pins the opt-in registration gate:
// ast_grep is absent by default, an explicit <HIVE_HOME>/ast_grep.json
// enabled=true opts it into the lazy tier, enabled=false or a malformed file
// keeps it out, and an agent-written <sessionDir>/.hive/ast_grep.json is
// ignored.
//
// Parity-exempt: Go-only ast_grep registration gate; upstream has no
// ast_grep.json catalogue gate to mirror.
func TestSessionCatalogueAstGrepConfigGate(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	user := filepath.Join(homeRoot, "ast_grep.json")
	sessionConfig := filepath.Join(sessionDir, ".hive", "ast_grep.json")

	newCatalogue := func() *sessionToolCatalogue {
		return newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	}

	// Missing config: default off, no adapter, no lazy entry.
	off := newCatalogue()
	if _, ok := codeIntelDef(t, off, builtin.ToolAstGrep); ok {
		t.Fatal("ast_grep registered without an opt-in config file")
	}
	if off.astGrepTool != nil {
		t.Fatal("ast_grep adapter built without an opt-in config file")
	}
	if lazy := defNames(off.LazyModelDefs()); flatContainsName(lazy, builtin.ToolAstGrep) {
		t.Fatalf("ast_grep advertised in the lazy layer while disabled: %v", lazy)
	}
	_ = off.Close()

	// User enabled=true opts in; the tool stays lazy-tier only.
	writeASTGrepConfig(t, user, `{"enabled":true}`)
	on := newCatalogue()
	if on.astGrepTool == nil {
		t.Fatal("ast_grep adapter not built with enabled=true")
	}
	if _, ok := codeIntelDef(t, on, builtin.ToolAstGrep); !ok {
		t.Fatal("ast_grep definition missing with enabled=true")
	}
	if names := defNames(on.ModelDefs()); flatContainsName(names, builtin.ToolAstGrep) {
		t.Fatal("ast_grep leaked into the default surface")
	}
	if lazy := defNames(on.LazyModelDefs()); !flatContainsName(lazy, builtin.ToolAstGrep) {
		t.Fatalf("ast_grep missing from the lazy layer while enabled: %v", lazy)
	}
	_ = on.Close()

	// User enabled=false opts out.
	writeASTGrepConfig(t, user, `{"enabled":false}`)
	userOff := newCatalogue()
	if _, ok := codeIntelDef(t, userOff, builtin.ToolAstGrep); ok {
		t.Fatal("user enabled=false did not disable ast_grep")
	}
	_ = userOff.Close()

	// The session directory is agent-writable and must not opt the tool in.
	writeASTGrepConfig(t, user, `{"enabled":false}`)
	writeASTGrepConfig(t, sessionConfig, `{"enabled":true}`)
	sessionOn := newCatalogue()
	if _, ok := codeIntelDef(t, sessionOn, builtin.ToolAstGrep); ok {
		t.Fatal("session-dir config opted ast_grep in; only $HIVE_HOME may gate it")
	}
	_ = sessionOn.Close()

	// A malformed user file fails closed.
	writeASTGrepConfig(t, user, `{"enabled":`)
	broken := newCatalogue()
	if _, ok := codeIntelDef(t, broken, builtin.ToolAstGrep); ok {
		t.Fatal("malformed user config registered ast_grep")
	}
	_ = broken.Close()
}

// TestSessionCatalogueLSPConfigGate pins the lsp registration gate to the
// frozen config chain: settings.enabled=false keeps the tool out of the
// catalogue entirely, and an enabled test lsp.json registers it in the lazy
// tier where catalogue.Execute reaches the adapter.
//
// Parity-exempt: Go-only session-catalogue adapter test; upstream has no
// lsp.json registration gate or lazy-tier catalogue to mirror.
func TestSessionCatalogueLSPConfigGate(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}

	writeLSPConfig := func(enabled bool) {
		t.Helper()
		payload := `{"settings":{"enabled":false}}`
		if enabled {
			payload = `{"settings":{"enabled":true}}`
		}
		if err := os.WriteFile(filepath.Join(homeRoot, "lsp.json"), []byte(payload), 0o600); err != nil {
			t.Fatalf("write lsp.json: %v", err)
		}
	}
	writeSessionLSPConfig := func(enabled bool) {
		t.Helper()
		payload := `{"settings":{"enabled":false}}`
		if enabled {
			payload = `{"settings":{"enabled":true}}`
		}
		path := filepath.Join(sessionDir, ".hive", "lsp.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	newCatalogue := func() *sessionToolCatalogue {
		return newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	}

	// Disabled: no definition anywhere, no lazy advertisement.
	writeLSPConfig(false)
	off := newCatalogue()
	defer func() { _ = off.Close() }()
	if _, ok := codeIntelDef(t, off, builtin.ToolLSP); ok {
		t.Fatal("lsp registered while settings.enabled=false")
	}
	if lazy := defNames(off.LazyModelDefs()); flatContainsName(lazy, builtin.ToolLSP) {
		t.Fatalf("lsp advertised in the lazy layer while disabled: %v", lazy)
	}

	// A session-directory file is agent-writable and must not enable lsp
	// while the operator-level file says disabled.
	writeSessionLSPConfig(true)
	sessionOn := newCatalogue()
	if _, ok := codeIntelDef(t, sessionOn, builtin.ToolLSP); ok {
		t.Fatal("session-dir lsp.json enabled lsp; only $HIVE_HOME may gate it")
	}
	_ = sessionOn.Close()

	// Enabled: registered once, lazy-tier only, and executed through the
	// adapter (status starts no language server). A session-dir disabled file
	// must not override it.
	writeLSPConfig(true)
	writeSessionLSPConfig(false)
	on := newCatalogue()
	defer func() { _ = on.Close() }()
	def, ok := codeIntelDef(t, on, builtin.ToolLSP)
	if !ok {
		t.Fatal("lsp definition missing from an enabled session catalogue")
	}
	if want := builtin.LSPToolDef(); !reflect.DeepEqual(def, want) {
		t.Fatalf("lsp catalogue definition drifted from the adapter:\n got %#v\nwant %#v", def, want)
	}
	if flatSurfaceGroupedModule(def) {
		t.Fatal("lsp was misclassified as a grouped integration module")
	}
	if names := defNames(on.ModelDefs()); flatContainsName(names, builtin.ToolLSP) {
		t.Fatal("lsp leaked into the frozen 39-tool default surface")
	}
	if lazy := defNames(on.LazyModelDefs()); !flatContainsName(lazy, builtin.ToolLSP) {
		t.Fatalf("lsp missing from the lazy layer while enabled: %v", lazy)
	}

	content, isError, err := on.Execute(context.Background(), builtin.ToolLSP, map[string]any{"action": "status"})
	if err != nil {
		t.Fatalf("lsp execute: %v", err)
	}
	if isError {
		t.Fatalf("lsp status reported a tool error: %s", content)
	}
	if !strings.Contains(content, "lsp status") {
		t.Fatalf("lsp result did not come from the adapter: %s", content)
	}
}
