package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testMainSource = `package main

func Foo() int { return 1 }

func main() {
	Foo()
}
`

type serviceFixture struct {
	service *Service
	root    string
}

func newServiceFixture(t *testing.T, scenario string, mutate func(*Options)) serviceFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/fake\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(testMainSource), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"requestTimeout": 3},
		"servers": map[string]any{
			"fake": map[string]any{
				"command":     os.Args[0],
				"fileTypes":   []string{".go"},
				"rootMarkers": []string{"go.mod"},
				"languageId":  "go",
			},
		},
	})
	opts := Options{
		Root:            root,
		Home:            home,
		ServerEnv:       fakeScenarioEnv(scenario),
		RequestTimeout:  3 * time.Second,
		DiagnosticsWait: 300 * time.Millisecond,
		LookPath:        stubLookPath(nil),
	}
	if mutate != nil {
		mutate(&opts)
	}
	service, err := NewService(opts)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return serviceFixture{service: service, root: root}
}

func (f serviceFixture) execute(t *testing.T, input map[string]any) Result {
	t.Helper()
	req, err := ParseRequest(input)
	if err != nil {
		t.Fatalf("ParseRequest(%v): %v", input, err)
	}
	result, err := f.service.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute(%v): %v", input, err)
	}
	return result
}

func (f serviceFixture) executeErr(t *testing.T, input map[string]any) error {
	t.Helper()
	req, err := ParseRequest(input)
	if err != nil {
		return err
	}
	_, err = f.service.Execute(context.Background(), req)
	return err
}

// Parity-exempt: read-only action goldens for the lsp tool are the Go-only
// acceptance path for docs/lsp-design.json R6/A8; no Python source exists.
func TestServiceReadOnlyActionGoldens(t *testing.T) {
	fixture := newServiceFixture(t, "basic", nil)

	status := fixture.execute(t, map[string]any{"action": "status"})
	for _, want := range []string{
		"lsp status",
		"catalog: 7 embedded (target 55)",
		"settings: enabled=true lazy=true requestTimeout=3s",
		"servers: 8 configured, 1 available, 0 running",
		"fake  available",
	} {
		if !strings.Contains(status.Content, want) {
			t.Fatalf("status missing %q:\n%s", want, status.Content)
		}
	}

	definition := fixture.execute(t, map[string]any{
		"action": "definition", "file": "main.go", "line": 6, "symbol": "Foo",
	})
	wantDefinition := "definition [fake] 2 result(s)\n" +
		"  main.go:3:1  func Foo() int { return 1 }\n" +
		"  main.go:6:2  Foo()"
	if definition.Content != wantDefinition {
		t.Fatalf("definition golden:\n%s\nwant:\n%s", definition.Content, wantDefinition)
	}
	results, ok := definition.Data["results"].([]map[string]any)
	if !ok || len(results) != 2 {
		t.Fatalf("structured results = %#v", definition.Data["results"])
	}

	typeDef := fixture.execute(t, map[string]any{
		"action": "type_definition", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if !strings.HasPrefix(typeDef.Content, "type_definition [fake] 2 result(s)") {
		t.Fatalf("type_definition:\n%s", typeDef.Content)
	}
	implementation := fixture.execute(t, map[string]any{
		"action": "implementation", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if !strings.HasPrefix(implementation.Content, "implementation [fake] 2 result(s)") {
		t.Fatalf("implementation:\n%s", implementation.Content)
	}

	references := fixture.execute(t, map[string]any{
		"action": "references", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if !strings.HasPrefix(references.Content, "references [fake] 2 result(s)") {
		t.Fatalf("references:\n%s", references.Content)
	}

	hover := fixture.execute(t, map[string]any{
		"action": "hover", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	wantHover := "hover [fake]\n```go\nfunc Foo() int\n```\n\nFoo returns 1."
	if hover.Content != wantHover {
		t.Fatalf("hover golden:\n%s\nwant:\n%s", hover.Content, wantHover)
	}

	symbols := fixture.execute(t, map[string]any{
		"action": "symbols", "file": "main.go",
	})
	wantSymbols := "symbols [fake] 2 symbol(s)\n" +
		"  Foo (function) main.go:3:6 — func Foo() int\n" +
		"    Foo.Bar (function) main.go:4:2"
	if symbols.Content != wantSymbols {
		t.Fatalf("symbols golden:\n%s\nwant:\n%s", symbols.Content, wantSymbols)
	}

	diagnostics := fixture.execute(t, map[string]any{
		"action": "diagnostics", "file": "main.go",
	})
	wantDiagnostics := "diagnostics [fake] 2 diagnostic(s)\n" +
		"  main.go:5:1  error  undefined: x  (fake) [E0001]\n" +
		"  main.go:1:1  warning  unused variable  (fake)"
	if diagnostics.Content != wantDiagnostics {
		t.Fatalf("diagnostics golden:\n%s\nwant:\n%s", diagnostics.Content, wantDiagnostics)
	}

	fresh := newServiceFixture(t, "basic", nil)
	overview := fresh.execute(t, map[string]any{"action": "capabilities"})
	if !strings.Contains(overview.Content, "capabilities (8 server(s))") {
		t.Fatalf("capability overview:\n%s", overview.Content)
	}
	if !strings.Contains(overview.Content, "fake: available") {
		t.Fatalf("capability overview missing fake state:\n%s", overview.Content)
	}

	capabilities := fixture.execute(t, map[string]any{
		"action": "capabilities", "file": "main.go",
	})
	for _, want := range []string{
		"capabilities [fake] fake-lsp 1.0",
		"textDocumentSync: full",
		"definition: yes",
		"documentSymbol: yes",
		"workspaceSymbol: yes",
		"rename: no",
	} {
		if !strings.Contains(capabilities.Content, want) {
			t.Fatalf("capabilities missing %q:\n%s", want, capabilities.Content)
		}
	}
	// status ignores file per D4.
	_ = fixture.execute(t, map[string]any{"action": "status", "file": "main.go"})
}

// Parity-exempt: no-match text and workspace forms are frozen by
// docs/lsp-design.json D4/R6/A8; no upstream goldens exist.
func TestServiceNoMatchAndWorkspaceSymbols(t *testing.T) {
	fixture := newServiceFixture(t, "nomatch", nil)
	for _, action := range []string{"definition", "references"} {
		result := fixture.execute(t, map[string]any{
			"action": action, "file": "main.go", "line": 3, "symbol": "Foo",
		})
		want := action + " [fake]: no results"
		if result.Content != want {
			t.Fatalf("%s no-match = %q, want %q", action, result.Content, want)
		}
	}
	hover := fixture.execute(t, map[string]any{
		"action": "hover", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if hover.Content != "hover [fake]: no hover information" {
		t.Fatalf("hover no-match = %q", hover.Content)
	}
	symbols := fixture.execute(t, map[string]any{
		"action": "symbols", "file": "main.go",
	})
	if symbols.Content != "symbols [fake]: no symbols" {
		t.Fatalf("symbols no-match = %q", symbols.Content)
	}

	basic := newServiceFixture(t, "basic", nil)
	workspace := basic.execute(t, map[string]any{
		"action": "symbols", "file": "*", "query": "Fo",
	})
	if !strings.HasPrefix(workspace.Content, "symbols [fake] 2 symbol(s)") {
		t.Fatalf("workspace symbols:\n%s", workspace.Content)
	}
	if !strings.Contains(workspace.Content, "Foo (function)") {
		t.Fatalf("workspace symbols missing Foo:\n%s", workspace.Content)
	}
}

// Parity-exempt: diagnostics freshness/dedupe is the review-focus case in
// docs/lsp-design.json (stale publish before initialize) and A8.
func TestServiceDiagnosticsFreshnessAndDedupe(t *testing.T) {
	dup := newServiceFixture(t, "dup", nil)
	result := dup.execute(t, map[string]any{"action": "diagnostics", "file": "main.go"})
	if !strings.HasPrefix(result.Content, "diagnostics [fake] 2 diagnostic(s)") {
		t.Fatalf("dedupe failed:\n%s", result.Content)
	}
	if fresh, _ := result.Data["fresh"].(bool); !fresh {
		t.Fatalf("duplicate publish should still be fresh: %#v", result.Data)
	}

	stale := newServiceFixture(t, "staleempty", nil)
	_ = stale.execute(t, map[string]any{"action": "diagnostics", "file": "main.go"})
	if err := os.WriteFile(filepath.Join(stale.root, "main.go"), []byte(testMainSource+"\n// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = stale.execute(t, map[string]any{"action": "diagnostics", "file": "main.go"})
	if fresh, _ := result.Data["fresh"].(bool); fresh {
		t.Fatalf("stale empty publish was reported fresh:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "no current diagnostics") {
		t.Fatalf("stale diagnostics text:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "older than the current document version") {
		t.Fatalf("stale note missing:\n%s", result.Content)
	}

	noPublish := newServiceFixture(t, "nopublish", func(opts *Options) {
		opts.DiagnosticsWait = 120 * time.Millisecond
	})
	result = noPublish.execute(t, map[string]any{"action": "diagnostics", "file": "main.go"})
	if fresh, _ := result.Data["fresh"].(bool); fresh {
		t.Fatalf("no publish was reported fresh:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "no diagnostics were published") {
		t.Fatalf("no-publish note missing:\n%s", result.Content)
	}
}

// Parity-exempt: pull diagnostics support is part of docs/lsp-design.json D7
// and has no Python counterpart.
func TestServicePullDiagnostics(t *testing.T) {
	fixture := newServiceFixture(t, "pull", nil)
	result := fixture.execute(t, map[string]any{"action": "diagnostics", "file": "main.go"})
	if !strings.Contains(result.Content, "diagnostics [fake] 2 diagnostic(s)") {
		t.Fatalf("pull diagnostics:\n%s", result.Content)
	}
	if fresh, _ := result.Data["fresh"].(bool); !fresh {
		t.Fatalf("pull diagnostics not fresh: %#v", result.Data)
	}
}

// Parity-exempt: workspace diagnostics/symbols caps are frozen by
// docs/lsp-design.json D4/D9/R8; no upstream test.
func TestServiceWorkspaceDiagnosticsAndFileCap(t *testing.T) {
	fixture := newServiceFixture(t, "basic", nil)
	if err := os.WriteFile(filepath.Join(fixture.root, "other.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := fixture.execute(t, map[string]any{"action": "diagnostics", "file": "*"})
	files, _ := result.Data["files"].([]string)
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	if !strings.HasPrefix(result.Content, "diagnostics [fake] 4 diagnostic(s)") {
		t.Fatalf("workspace diagnostics:\n%s", result.Content)
	}

	// Cap the workspace scan at 20 matching files.
	for i := 0; i < 25; i++ {
		name := filepath.Join(fixture.root, "extra", "f"+itoa2(i)+".go")
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result = fixture.execute(t, map[string]any{"action": "diagnostics", "file": "*"})
	files, _ = result.Data["files"].([]string)
	if len(files) != MaxWorkspaceFiles {
		t.Fatalf("workspace file cap = %d, want %d", len(files), MaxWorkspaceFiles)
	}
}

func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// Parity-exempt: typed failure paths (disabled, no server, path rejection,
// backoff) are R9/R10/A2/A11 acceptance cases with no upstream analogue.
func TestServiceTypedFailures(t *testing.T) {
	fixture := newServiceFixture(t, "basic", nil)
	if err := fixture.executeErr(t, map[string]any{
		"action": "definition", "file": "../outside.go", "line": 1, "symbol": "Foo",
	}); !IsCode(err, CodePathRejected) {
		t.Fatalf("path escape err = %v", err)
	}
	if err := fixture.executeErr(t, map[string]any{
		"action": "definition", "file": "notes.txt", "line": 1, "symbol": "Foo",
	}); !IsCode(err, CodeNoServer) {
		t.Fatalf("no-server err = %v", err)
	}
	if err := fixture.executeErr(t, map[string]any{
		"action": "definition", "file": "main.go", "line": 99, "symbol": "Foo",
	}); !IsCode(err, CodePositionRejected) {
		t.Fatalf("bad line err = %v", err)
	}
	if _, err := ParseAction("rename"); !IsCode(err, CodeUnsupportedAction) {
		t.Fatalf("rename parse err = %v", err)
	}
	if _, err := ParseAction("request"); !IsCode(err, CodeUnsupportedAction) {
		t.Fatalf("request parse err = %v", err)
	}
	if _, err := ParseAction("teleport"); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("unknown action err = %v", err)
	}
	if _, err := ParseRequest(map[string]any{"action": "definition", "file": "*", "line": 1}); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("file=* for definition err = %v", err)
	}
	if _, err := ParseRequest(map[string]any{"action": "status", "file": 1}); !IsCode(err, CodeInvalidArgument) {
		t.Fatalf("non-string file err = %v", err)
	}
}

// Parity-exempt: disable/config-reload behavior is the frozen D1/config
// contract; no Python counterpart.
func TestServiceDisabledAndReload(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	configPath := filepath.Join(home, "lsp.json")
	writeJSON(t, configPath, map[string]any{
		"settings": map[string]any{"enabled": false},
	})
	service, err := NewService(Options{
		Root:            root,
		Home:            home,
		ServerEnv:       fakeScenarioEnv("basic"),
		DiagnosticsWait: 300 * time.Millisecond,
		LookPath:        stubLookPath(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	status, err := service.Execute(context.Background(), Request{Action: ActionStatus})
	if err != nil {
		t.Fatalf("status must work when disabled: %v", err)
	}
	if !strings.Contains(status.Content, "enabled=false") {
		t.Fatalf("status = \n%s", status.Content)
	}
	_, err = service.Execute(context.Background(), Request{Action: ActionDiagnostics, File: "main.go"})
	if !IsCode(err, CodeDisabled) {
		t.Fatalf("disabled diagnostics err = %v", err)
	}

	writeJSON(t, configPath, map[string]any{
		"settings": map[string]any{"enabled": true},
		"servers": map[string]any{
			"fake": map[string]any{
				"command": os.Args[0], "fileTypes": []string{".go"}, "rootMarkers": []string{"go.mod"}, "languageId": "go",
			},
		},
	})
	if _, err := service.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), Request{Action: ActionDiagnostics, File: "main.go"})
	if err != nil {
		t.Fatalf("diagnostics after reload: %v", err)
	}
	if !strings.Contains(result.Content, "diagnostics") {
		t.Fatalf("result = %q", result.Content)
	}
}

// Parity-exempt: timeout/backoff bounds are R8/A10 acceptance cases; no
// upstream test.
func TestServiceTimeoutAndCrashBackoff(t *testing.T) {
	hang := newServiceFixture(t, "hang", func(opts *Options) {
		opts.RequestTimeout = 120 * time.Millisecond
	})
	err := hang.executeErr(t, map[string]any{
		"action": "definition", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if !IsCode(err, CodeTimeout) {
		t.Fatalf("hang err = %v", err)
	}

	crash := newServiceFixture(t, "crash", func(opts *Options) {
		opts.Backoff = time.Minute
	})
	firstErr := crash.executeErr(t, map[string]any{
		"action": "definition", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if !IsCode(firstErr, CodeServerUnavailable) {
		t.Fatalf("crash err = %v", firstErr)
	}
	err = crash.executeErr(t, map[string]any{
		"action": "definition", "file": "main.go", "line": 3, "symbol": "Foo",
	})
	if !strings.Contains(err.Error(), "failure backoff") {
		t.Fatalf("second call did not report backoff: %v", err)
	}
}

// Parity-exempt: document/version tracking through the service is R5/A7 with
// no Python source test.
func TestServiceContextCancellation(t *testing.T) {
	fixture := newServiceFixture(t, "hang", nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := Request{Action: ActionDefinition, File: "main.go", Line: 3, Column: 1, Symbol: "Foo"}
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := fixture.service.Execute(ctx, req)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	var timeout *TimeoutError
	if !errors.As(err, &timeout) && !errors.Is(err, context.Canceled) && !IsCode(err, CodeTimeout) {
		t.Fatalf("err = %v", err)
	}
}

// Parity-exempt: a service request routed through a pinned rootDir from a
// session-like cwd is new Go-only acceptance work for docs/lsp-design.json
// R3/D3; the fake server is this package's test harness.
func TestServicePinnedRootDirRequest(t *testing.T) {
	session := t.TempDir()
	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.test/pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "main.go"), []byte(testMainSource), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	writeJSON(t, filepath.Join(home, "lsp.json"), map[string]any{
		"settings": map[string]any{"requestTimeout": 3},
		"servers": map[string]any{
			"fake": map[string]any{
				"command":     os.Args[0],
				"fileTypes":   []string{".go"},
				"rootMarkers": []string{"go.mod"},
				"rootDir":     module,
				"languageId":  "go",
			},
		},
	})
	realModule, err := filepath.EvalSymlinks(module)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "rootprobe.json")
	service, err := NewService(Options{
		Root:            session,
		Home:            home,
		ServerEnv:       fakeScenarioEnv("rootprobe", fakeMarker+"="+marker),
		RequestTimeout:  3 * time.Second,
		DiagnosticsWait: 300 * time.Millisecond,
		LookPath:        stubLookPath(nil),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	fixture := serviceFixture{service: service, root: session}

	status := fixture.execute(t, map[string]any{"action": "status"})
	if !strings.Contains(status.Content, "fake  available") || !strings.Contains(status.Content, "root=go.mod") {
		t.Fatalf("pinned server status:\n%s", status.Content)
	}

	// A relative file resolves against rootDir, not the session cwd.
	definition := fixture.execute(t, map[string]any{
		"action": "definition", "file": "main.go", "line": 6, "symbol": "Foo",
	})
	if !strings.HasPrefix(definition.Content, "definition [fake] 2 result(s)") {
		t.Fatalf("definition:\n%s", definition.Content)
	}
	if got := definition.Data["file"]; got != "main.go" {
		t.Fatalf("definition file = %#v, want main.go relative to rootDir", got)
	}

	// Workspace diagnostics scan rootDir rather than the empty session dir.
	workspace := fixture.execute(t, map[string]any{"action": "diagnostics", "file": "*"})
	files, ok := workspace.Data["files"].([]string)
	if !ok || len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("workspace files = %#v, want [main.go]", workspace.Data["files"])
	}

	// The server process cwd and initialize rootUri both use rootDir.
	raw, ok := waitForNonEmptyFile(marker, 2*time.Second)
	if !ok {
		t.Fatal("fake server did not record its root")
	}
	var probe struct {
		Cwd     string `json:"cwd"`
		RootURI string `json:"root_uri"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		t.Fatalf("probe %q: %v", raw, err)
	}
	if probe.Cwd != realModule {
		t.Fatalf("server cwd = %q, want %q", probe.Cwd, realModule)
	}
	if probe.RootURI != fileURI(realModule) {
		t.Fatalf("initialize rootUri = %q, want %q", probe.RootURI, fileURI(realModule))
	}
}
