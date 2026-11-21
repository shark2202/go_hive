package server

// Searchable-tools manifest parity (prompt-parity P-04).
//
// Upstream announces exactly the queen's allowlist minus the eager set, so a
// queen_growth session's first-turn manifest lists two names — chart_render and
// web_scrape. The Go deferred layer is deliberately wider (everything the build
// can execute stays loadable), so the reminder announces the Python-equivalent
// subset while the tier keeps the whole layer: search_tools still loads the
// rest by select: or keywords, and the "schema is not loaded yet" guard still
// covers every deferred tool.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_searchable_tools
// Parity: framework/agent_loop/tool_skill_reminders.py::_render_searchable_body

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

const (
	// manifestMarker opens the searchable-tools reminder body (both sides).
	manifestMarker = "The following tools are available but their schemas are NOT loaded"
	// manifestFooter is the hub's standard reminder footer; repeated here so
	// the extraction below does not depend on the agentloop constant.
	manifestFooter = "Only act on this if relevant to the current work. NEVER mention this reminder to the user."
	// manifestGolden is the Python capture of the same first-turn manifest
	// (go_hive/docs/e2e-assets/prompt-catalog/reminder-searchable.python.txt).
	manifestGolden = "testdata/prompt/searchable-manifest.python.txt"
)

// manifestDeferredDefs is the loadable layer used below: the colony-gated
// names, the two Python-manifest tools, and flat directory tools that the
// reference's allowlist does not grant queen_growth.
func manifestDeferredDefs() []llm.ToolDef {
	out := append([]llm.ToolDef(nil), lazyDeferredDefs()...)
	out = append(out,
		lazyTestDef("web_scrape"),
		lazyTestDef("csv_read"),
		lazyTestDef("tracker_upsert"),
	)
	return out
}

// manifestAllowlist mirrors the reference's resolved per-queen allowlist for
// queen_growth: the role-default categories, which leave chart_render and
// web_scrape as the only deferred names allowed to be announced.
func manifestAllowlist() []string {
	return []string{"chart_render", "web_scrape"}
}

func newManifestChatRuntime(t *testing.T, model llm.Completer, execute func(context.Context, string, map[string]any) (string, bool, error)) (*ChatRuntime, *chatEventRecorder) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:               model,
		Tools:             append(lazySourceDefs(), lazyColonyDefs()...),
		AlwaysOnToolNames: lazyAlwaysOn,
		DeferredTools:     manifestDeferredDefs(),
		ManifestToolNames: manifestAllowlist(),
		ManifestFiltered:  true,
		Execute:           execute,
		Root:              t.TempDir(),
		Bus:               bus,
		SessionID:         "session_20260101_000000_manifest1",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, recordChatEvents(bus)
}

// searchableManifest extracts the searchable-tools segment from a reminder
// body. Other sources join with a blank line, so the segment ends at the first
// "\n\n"; the manifest itself never contains one.
func searchableManifest(body string) string {
	index := strings.Index(body, manifestMarker)
	if index < 0 {
		return ""
	}
	segment := body[index:]
	if cut := strings.Index(segment, "\n\n"); cut >= 0 {
		segment = segment[:cut]
	}
	return segment
}

// reminderBodies returns every <system-reminder> body carried by a request.
func reminderBodies(req llm.CompletionRequest) []string {
	bodies := make([]string, 0, len(req.Messages))
	for _, message := range req.Messages {
		content := message.Content
		if !strings.Contains(content, "<system-reminder>") {
			continue
		}
		body := content
		if start := strings.Index(body, "<system-reminder>\n"); start >= 0 {
			body = body[start+len("<system-reminder>\n"):]
		}
		if end := strings.LastIndex(body, "\n\n"+manifestFooter+"\n</system-reminder>"); end >= 0 {
			body = body[:end]
		}
		bodies = append(bodies, body)
	}
	return bodies
}

// firstRequestManifest returns the searchable-tools manifest of one request's
// messages, or "".
func firstRequestManifest(req llm.CompletionRequest) string {
	for _, body := range reminderBodies(req) {
		if manifest := searchableManifest(body); manifest != "" {
			return manifest
		}
	}
	return ""
}

// TestSearchableManifestAnnouncesPythonAllowlistOnly pins the first-turn
// manifest text: the reference capture, byte for byte, with the Go-only
// loadable names left out of the announcement.
//
// Parity: framework/agent_loop/tool_skill_reminders.py::_render_searchable_body
func TestSearchableManifestAnnouncesPythonAllowlistOnly(t *testing.T) {
	model := &queenChatLLM{}
	runtime, recorder := newManifestChatRuntime(t, model, func(context.Context, string, map[string]any) (string, bool, error) {
		return "ok", false, nil
	})
	if err := runtime.Deliver("hello"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	requests := model.requestSnapshot()
	if len(requests) == 0 {
		t.Fatal("model never received a completion request")
	}
	manifest := firstRequestManifest(requests[0])
	if manifest == "" {
		t.Fatal("first request carries no searchable-tools manifest")
	}

	golden, err := os.ReadFile(manifestGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	want := strings.TrimSuffix(string(golden), "\n")
	if manifest != want {
		t.Fatalf("searchable manifest mismatch:\n got %q\nwant %q", manifest, want)
	}
	// The reference announces two names; the Go-only loadable remainder must
	// not leak into the text (it stays loadable, just unannounced).
	for _, hidden := range []string{"read_file", "csv_read", "tracker_upsert", "run_playbook"} {
		if strings.Contains(manifest, hidden) {
			t.Fatalf("manifest announced %q, want the Python allowlist only:\n%s", hidden, manifest)
		}
	}
}

// TestSearchableManifestFilterKeepsDeferredToolsLoadable proves the filter is
// text-only: inventory still reports the whole deferred layer, a non-announced
// tool loads by select:, and the load reaches the next request's tools[].
//
// Parity: core/tests/test_worker_tool_tiers.py::TestSearchToolsFactory::test_select_promotes_and_reports
func TestSearchableManifestFilterKeepsDeferredToolsLoadable(t *testing.T) {
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search_tools", Arguments: `{"query":"inventory"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "search_tools", Arguments: `{"query":"select:csv_read"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "c3", Name: "search_tools", Arguments: `{"query":"tracker"}`}}},
		{Content: "loaded"},
	}}
	var mu sync.Mutex
	var executed []string
	runtime, recorder := newManifestChatRuntime(t, model, func(_ context.Context, name string, _ map[string]any) (string, bool, error) {
		mu.Lock()
		executed = append(executed, name)
		mu.Unlock()
		return "ok", false, nil
	})
	if err := runtime.Deliver("load a csv tool and a tracker tool"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	requests := model.requestSnapshot()
	if len(requests) < 4 {
		t.Fatalf("model requests = %d, want inventory + select + keyword + answer", len(requests))
	}
	inventory := ""
	for _, message := range requests[1].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, `"searchable"`) {
			inventory = message.Content
		}
	}
	if inventory == "" {
		t.Fatalf("inventory result not carried into the follow-up request")
	}
	for _, name := range []string{"csv_read", "read_file", "tracker_upsert", "run_playbook"} {
		if !strings.Contains(inventory, name) {
			t.Fatalf("inventory lost deferred tool %q (the filter must not narrow the loadable tier): %s", name, inventory)
		}
	}
	if !hasToolName(requests[2].Tools, "csv_read") {
		t.Fatalf("csv_read missing from the request after search_tools select: %v", toolNamesOf(requests[2].Tools))
	}
	// Keyword search reaches the same unannounced layer: the tracker tools
	// load by free text even though the manifest never named them.
	loaded := toolNamesOf(requests[3].Tools)
	trackerLoaded := false
	for _, name := range loaded {
		if strings.HasPrefix(name, "tracker_") {
			trackerLoaded = true
		}
	}
	if !trackerLoaded {
		t.Fatalf("keyword search loaded no tracker tool: %v", loaded)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 0 {
		t.Fatalf("executed tools = %v, want none (search_tools runs in-loop)", executed)
	}
}

// TestSearchableManifestFilterKeepsDeferredGuard checks the execution guard a
// narrowed announcement must not weaken: calling a deferred tool whose schema
// was never loaded is still refused before dispatch.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop._execute_tool
func TestSearchableManifestFilterKeepsDeferredGuard(t *testing.T) {
	model := &queenChatLLM{responses: []llm.Completion{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"x.txt"}`}}},
		{Content: "could not read"},
	}}
	var mu sync.Mutex
	var executed []string
	runtime, recorder := newManifestChatRuntime(t, model, func(_ context.Context, name string, _ map[string]any) (string, bool, error) {
		mu.Lock()
		executed = append(executed, name)
		mu.Unlock()
		return "ok", false, nil
	})
	if err := runtime.Deliver("read a file"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	requests := model.requestSnapshot()
	if len(requests) < 2 {
		t.Fatalf("model requests = %d, want the refused call and the answer", len(requests))
	}
	refusal := ""
	for _, message := range requests[1].Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "not loaded") {
			refusal = message.Content
		}
	}
	if !strings.Contains(refusal, "read_file") {
		t.Fatalf("deferred-tool guard did not refuse read_file: %q", refusal)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(executed) != 0 {
		t.Fatalf("executed tools = %v, want none (a deferred schema must not run unloaded)", executed)
	}
}
