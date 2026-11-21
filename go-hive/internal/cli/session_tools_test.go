package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// Parity-exempt: assembly guard. The chat session's toolbox must carry the
// ported integration tools next to the builtins, and both dispatch paths must
// stay intact (builtins through the sandbox, integrations through the registry).
func TestSessionToolCatalogueInstallsIntegrations(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	if catalogue.installed == 0 {
		t.Fatal("no integration tools installed")
	}

	defined := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = struct{}{}
	}
	for _, name := range []string{builtin.ToolReadFile, builtin.ToolWriteFile, builtin.ToolEditFile, builtin.ToolSearchFiles, builtin.ToolTerminalExec} {
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
	root := testTempDir(t)
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

// Parity-exempt: assembly guard. The files-tools harness group is served in
// process by the builtin toolbox; its catalog must carry all four tools, and
// every code_editing category name must resolve to a registered tool.
func TestSessionCatalogueExposesFileToolsGroup(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	group := catalogue.registry.GetFullMCPCatalog()["files-tools"]
	entries := map[string]map[string]any{}
	for _, entry := range group {
		name, _ := entry["name"].(string)
		entries[name] = entry
	}
	for _, want := range []string{builtin.ToolReadFile, builtin.ToolWriteFile, builtin.ToolEditFile, builtin.ToolSearchFiles} {
		if _, ok := entries[want]; !ok {
			t.Fatalf("files-tools group missing %q: %#v", want, entries)
		}
	}
	if len(group) != 4 {
		t.Fatalf("files-tools group = %d tools, want 4", len(group))
	}
	if schema, _ := entries[builtin.ToolEditFile]["input_schema"].(map[string]any); schema == nil || schema["type"] != "object" {
		t.Fatalf("edit_file catalog schema = %#v", entries[builtin.ToolEditFile]["input_schema"])
	}

	// code_editing used to name a tool that no surface registered.
	defined := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = struct{}{}
	}
	for _, name := range tools.ResolveCategoryTools("code_editing", nil) {
		if _, ok := defined[name]; !ok {
			t.Fatalf("code_editing member %q is not registered on the session surface", name)
		}
	}
	if _, ok := defined[builtin.ToolEditFile]; !ok {
		t.Fatalf("edit_file missing from session defs")
	}

	// The builtin dispatch path executes edit_file inside the session root.
	write, isError, err := catalogue.Execute(context.Background(), builtin.ToolWriteFile, map[string]any{
		"path":    "edited.txt",
		"content": "alpha\n",
	})
	if err != nil || isError {
		t.Fatalf("write_file: err=%v isError=%v (%s)", err, isError, write)
	}
	edited, isError, err := catalogue.Execute(context.Background(), builtin.ToolEditFile, map[string]any{
		"mode":       "replace",
		"path":       "edited.txt",
		"old_string": "alpha",
		"new_string": "beta",
	})
	if err != nil || isError {
		t.Fatalf("edit_file: err=%v isError=%v (%s)", err, isError, edited)
	}
	if !strings.Contains(edited, "Replaced 1") {
		t.Fatalf("edit_file summary = %q", edited)
	}
	if got := readSessionFile(t, filepath.Join(root, "edited.txt")); got != "beta\n" {
		t.Fatalf("edited content = %q", got)
	}
}

func readSessionFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Parity-exempt: assembly guard. Every installer in the wiring table must put
// its whole tool set into one session catalogue, and the installed counter must
// match the registry so a silent overwrite cannot hide a collision.
func TestSessionToolCatalogueInstallsEveryBatch(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	for _, entry := range sessionIntegrationInstallers {
		probe := tools.NewToolRegistry()
		if count := entry.install(probe); count == 0 {
			t.Fatalf("%s installed no tools", entry.name)
		}
		for _, name := range probe.RegisteredNames() {
			if !catalogue.registry.HasTool(name) {
				t.Fatalf("%s: tool %q never reached the session catalogue", entry.name, name)
			}
		}
	}

	if got, want := catalogue.installed, len(catalogue.registry.RegisteredNames()); got != want {
		t.Fatalf("installed counter = %d, registry holds %d tools (an installer overwrote another?)", got, want)
	}
}

// Parity: tools/src/aden_tools/tools/__init__.py::_register_verified
// The seven security tools register unconditionally upstream; the Go session
// catalogue must expose the same set. There is no Go-only opt-in any more, so
// even the old "off" value of HIVE_NETWORK_SCANNERS must not change the
// default surface.
func TestSessionToolCatalogueRegistersSecurityScannersByDefault(t *testing.T) {
	root := testTempDir(t)
	home := hivehome.Home{Root: root}
	t.Setenv("HIVE_NETWORK_SCANNERS", "0")

	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	// The Python registry's security group, verbatim:
	// register_ssl_tls_scanner / register_http_headers_scanner /
	// register_dns_security_scanner / register_port_scanner /
	// register_tech_stack_detector / register_subdomain_enumerator /
	// register_risk_scorer.
	pythonSecurityTools := []string{
		"ssl_tls_scan",
		"http_headers_scan",
		"dns_security_scan",
		"port_scan",
		"tech_stack_detect",
		"subdomain_enumerate",
		"risk_score",
	}
	defined := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = struct{}{}
	}
	for _, name := range pythonSecurityTools {
		if _, ok := defined[name]; !ok {
			t.Fatalf("security tool %q missing from the default catalogue defs", name)
		}
		if !catalogue.registry.HasTool(name) {
			t.Fatalf("security tool %q missing from the default registry", name)
		}
	}
	for _, name := range []string{"arxiv", "duckduckgo", "search_wikipedia", "openmeteo", "yahoo_finance", "youtube_transcript"} {
		if !catalogue.registry.HasTool(name) {
			t.Fatalf("batch-2 tool %q missing from the default catalogue", name)
		}
	}
	if got, want := catalogue.installed, len(catalogue.registry.RegisteredNames()); got != want {
		t.Fatalf("installed counter = %d, registry holds %d tools", got, want)
	}
}

// Parity: tools/tests/tools/test_port_scanner.py::TestPortScanning
// The default catalogue must be able to run a real scan. The target is a
// listener bound to 127.0.0.1 — no public host is contacted.
func TestSessionToolCataloguePortScanLocalhost(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	content, isError, err := catalogue.Execute(context.Background(), "port_scan", map[string]any{
		"hostname": "127.0.0.1",
		"ports":    strconv.Itoa(port),
		"timeout":  2.0,
	})
	if err != nil || isError {
		t.Fatalf("port_scan: err=%v isError=%v content=%s", err, isError, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("port_scan output is not JSON: %v (%s)", err, content)
	}
	open, _ := payload["open_ports"].([]any)
	if len(open) != 1 {
		t.Fatalf("open_ports = %#v, want the local listener port", payload["open_ports"])
	}
	entry, _ := open[0].(map[string]any)
	if got, ok := entry["port"].(float64); !ok || int(got) != port {
		t.Fatalf("open port = %#v, want %d", entry["port"], port)
	}
}

// Parity: tools/tests/tools/test_http_headers_scanner.py::TestSecurityHeaders
// Real local call through the session catalogue for the HTTP-header scanner,
// then the risk scorer consumes its output. Both targets are on 127.0.0.1.
func TestSessionToolCatalogueHTTPHeadersScanLocalhost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'")
		w.Header().Set("Permissions-Policy", "geolocation=()")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	content, isError, err := catalogue.Execute(context.Background(), "http_headers_scan", map[string]any{
		"url": server.URL,
	})
	if err != nil || isError {
		t.Fatalf("http_headers_scan: err=%v isError=%v content=%s", err, isError, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("http_headers_scan output is not JSON: %v (%s)", err, content)
	}
	if payload["status_code"] != float64(http.StatusOK) {
		t.Fatalf("status_code = %#v, want 200", payload["status_code"])
	}
	present, _ := payload["headers_present"].([]any)
	found := false
	for _, name := range present {
		if name == "Strict-Transport-Security" {
			found = true
		}
	}
	if !found {
		t.Fatalf("headers_present = %#v, want Strict-Transport-Security", payload["headers_present"])
	}

	riskContent, isError, err := catalogue.Execute(context.Background(), "risk_score", map[string]any{
		"headers_results": content,
	})
	if err != nil || isError {
		t.Fatalf("risk_score: err=%v isError=%v content=%s", err, isError, riskContent)
	}
	var risk map[string]any
	if err := json.Unmarshal([]byte(riskContent), &risk); err != nil {
		t.Fatalf("risk_score output is not JSON: %v (%s)", err, riskContent)
	}
	if _, ok := risk["overall_score"]; !ok {
		t.Fatalf("risk_score payload = %#v", risk)
	}
	if _, ok := risk["overall_grade"]; !ok {
		t.Fatalf("risk_score payload = %#v", risk)
	}
}

// fakeRunWorkerSpawner is the fake colony wire the run_worker executor talks
// to: it records the batch and returns stub worker ids.
type fakeRunWorkerSpawner struct {
	tasks []colony.BatchTask
	opts  colony.SpawnBatchOptions
	ids   []string
}

func (f *fakeRunWorkerSpawner) SpawnBatchWithOptions(_ context.Context, tasks []colony.BatchTask, opts colony.SpawnBatchOptions) ([]string, error) {
	f.tasks = append([]colony.BatchTask(nil), tasks...)
	f.opts = opts
	f.ids = make([]string, 0, len(tasks))
	for index := range tasks {
		f.ids = append(f.ids, "worker:"+string(rune('a'+index)))
	}
	return f.ids, nil
}

func (f *fakeRunWorkerSpawner) GetWorker(string) *colony.Worker { return nil }

func (f *fakeRunWorkerSpawner) MaxConcurrentWorkers() int { return 4 }

// Parity-exempt: assembly guard. The queen catalogue must advertise run_worker
// next to the builtins and integrations; the chat runtime decides per turn
// whether a bound colony makes it callable.
func TestSessionToolCatalogueAdvertisesRunWorker(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	found := false
	for _, def := range catalogue.Defs() {
		if def.Name == colony.RunWorkerToolName {
			found = true
			if def.Parameters["type"] != "object" {
				t.Fatalf("run_worker parameters = %#v", def.Parameters)
			}
		}
	}
	if !found {
		t.Fatalf("run_worker missing from the session catalogue defs")
	}
	if catalogue.registry.HasTool(colony.RunWorkerToolName) {
		t.Fatalf("run_worker should be a catalogue tool, not an integration registry tool")
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_returns_error_when_no_colony
func TestSessionToolCatalogueRunWorkerRejectsWithoutColony(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	content, isError, err := catalogue.Execute(context.Background(), colony.RunWorkerToolName, map[string]any{
		"tasks": []any{map[string]any{"task": "do a thing"}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !isError {
		t.Fatalf("run_worker without a colony reported success: %s", content)
	}
	if !strings.Contains(content, "No colony is bound") {
		t.Fatalf("error = %q, want an actionable no-colony message", content)
	}
}

// Parity: tests/test_run_worker_tool.py::test_run_worker_tool_returns_immediately_and_emits_reports
func TestSessionToolCatalogueRunWorkerDispatchesBoundColony(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	spawner := &fakeRunWorkerSpawner{}
	ctx := colony.WithRunWorkerSpawner(context.Background(), spawner)

	content, isError, err := catalogue.Execute(ctx, colony.RunWorkerToolName, map[string]any{
		"tasks":   []any{map[string]any{"task": "slice one"}, map[string]any{"task": "slice two"}},
		"timeout": 15,
	})
	if err != nil || isError {
		t.Fatalf("Execute: err=%v isError=%v content=%s", err, isError, content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if payload["status"] != "started" || payload["worker_count"] != float64(2) {
		t.Fatalf("payload = %#v", payload)
	}
	if len(spawner.tasks) != 2 || spawner.tasks[0].Task != "slice one" {
		t.Fatalf("spawned tasks = %#v", spawner.tasks)
	}
	if spawner.opts.BatchID == "" {
		t.Fatal("run_worker did not hand the colony a batch id")
	}
}
