package cli

// browser_tool_surface_test.go pins the model-visible browser surface this lane
// adds: the 22 gcu tools are registered in the session catalogue, but they stay
// in the search_tools lazy layer — the first request keeps the Python
// reference's 24 eager names. Execution routes through the bridge's /command
// envelope, the same transport the hive-browser CLI uses.
//
// Parity: core/framework/tools/tool_tiers.py (eager vs searchable)
// Parity: tools/src/gcu/browser/tools/tabs.py (22 active tool definitions)

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools/integrations"
)

// TestBrowserToolsAreDeferredNotFirstRequest is the important regression: the
// Python reference default session advertises 24 tools in its first LLM
// request. The new browser definitions must not join that set.
//
// Parity: core/framework/tools/tool_tiers.py::ToolTierState.get_current_tools
func TestBrowserToolsAreDeferredNotFirstRequest(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	eager, deferred := catalogue.LazySurfaceDeps()
	if len(eager) != 24 {
		t.Fatalf("first request tools = %d, want 24", len(eager))
	}
	eagerSet := map[string]struct{}{}
	for _, name := range eager {
		eagerSet[name] = struct{}{}
	}
	deferredSet := map[string]struct{}{}
	for _, def := range deferred {
		deferredSet[def.Name] = struct{}{}
	}
	live := map[string]struct{}{}
	for _, def := range catalogue.defs {
		live[def.Name] = struct{}{}
	}
	browserNames := integrations.BrowserToolNames()
	if len(browserNames) != 22 {
		t.Fatalf("browser definitions = %d, want 22", len(browserNames))
	}
	for _, name := range browserNames {
		if name == integrations.BrowserSetupToolName {
			// browser_setup is part of the Python default surface: the eager
			// 24 carry the core/framework discovery tool of that name, not the
			// gcu copy (see InstallBrowserTools' skip rule).
			if _, eager := eagerSet[name]; !eager {
				t.Fatalf("browser_setup must stay in the first request (framework port)")
			}
			continue
		}
		if _, eager := eagerSet[name]; eager {
			t.Fatalf("browser tool %q leaked into the first request", name)
		}
		if _, ok := deferredSet[name]; !ok {
			t.Fatalf("browser tool %q missing from the lazy layer", name)
		}
		if _, ok := live[name]; !ok {
			t.Fatalf("browser tool %q has no catalogue definition (no execution route)", name)
		}
	}
	t.Logf("browser surface: %d eager (first request) + %d deferred (incl. %d browser tools)",
		len(eager), len(deferred), len(browserNames))
}

// bridgeCommand is the POST /command envelope the executor must send.
type capturedBrowserCommand struct {
	Profile string         `json:"profile"`
	Type    string         `json:"type"`
	Params  map[string]any `json:"params"`
}

// fakeBrowserBridge answers one recorded /command call.
func fakeBrowserBridge(t *testing.T, status int, response map[string]any) (*httptest.Server, *capturedBrowserCommand) {
	t.Helper()
	captured := &capturedBrowserCommand{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/command" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// TestBrowserToolExecutorSendsBridgeCommand proves the registry executor is a
// real bridge client: type/profile envelope plus the tool's arguments.
//
// Parity: tools/src/gcu/browser/tools/tabs.py::browser_tabs (result forwarded
// verbatim through the bridge /command transport).
func TestBrowserToolExecutorSendsBridgeCommand(t *testing.T) {
	server, captured := fakeBrowserBridge(t, http.StatusOK, map[string]any{
		"ok": true,
		"result": map[string]any{
			"ok": true, "total": 1, "activeTabId": 7,
			"tabs": []any{map[string]any{"id": 7, "url": "https://example.com/", "title": "Example"}},
		},
	})
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", server.URL)
	t.Setenv("HIVE_BROWSER_SESSION", "queen-1")
	catalogue := queenFlatCatalogue(t)

	content, isError, err := catalogue.Execute(context.Background(), "browser_tabs", map[string]any{})
	if err != nil {
		t.Fatalf("browser_tabs: %v", err)
	}
	if isError {
		t.Fatalf("browser_tabs reported an error: %s", content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("result is not JSON: %q (%v)", content, err)
	}
	if payload["activeTabId"] != float64(7) || payload["total"] != float64(1) {
		t.Fatalf("result = %+v", payload)
	}
	if captured.Type != "browser_tabs" || captured.Profile != "queen-1" {
		t.Fatalf("command envelope = %+v", captured)
	}
}

// TestBrowserToolExecutorInlinesImageBlocks covers the screenshot family: the
// bridge's JPEG payload becomes an image block the chat runtime attaches to the
// next model request (Python returns TextContent + ImageContent), and the text
// block carries no base64.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::render_screenshot
func TestBrowserToolExecutorInlinesImageBlocks(t *testing.T) {
	server, captured := fakeBrowserBridge(t, http.StatusOK, map[string]any{
		"ok": true,
		"result": map[string]any{
			"ok": true, "tabId": 7, "url": "https://example.com/", "title": "Example",
			"imageType": "jpeg", "data": "aGVsbG8=", "mimeType": "image/jpeg",
		},
	})
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", server.URL)
	t.Setenv("HIVE_BROWSER_SESSION", "queen-1")
	catalogue := queenFlatCatalogue(t)

	content, isError, blocks, err := catalogue.ExecuteBlocks(context.Background(), "browser_screenshot", map[string]any{
		"intent": "view the page",
	})
	if err != nil || isError {
		t.Fatalf("browser_screenshot: err=%v isError=%v content=%s", err, isError, content)
	}
	if strings.Contains(content, "aGVsbG8=") {
		t.Fatalf("text block leaked the base64 image: %s", content)
	}
	if len(blocks) != 1 {
		t.Fatalf("image blocks = %d, want 1", len(blocks))
	}
	imageURL, _ := blocks[0]["image_url"].(map[string]any)
	if url, _ := imageURL["url"].(string); !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("image block = %+v", blocks[0])
	}
	// Python's tool layer always encodes 800px JPEG q75; the executor pins the
	// same defaults when the model only supplies the schema fields.
	if captured.Params["format"] != "jpeg" || captured.Params["quality"] != float64(75) || captured.Params["max_width"] != float64(800) {
		t.Fatalf("screenshot params = %+v", captured.Params)
	}
}

// TestBrowserToolExecutorReportsBridgeRefusal maps a bridge error envelope onto
// the Python tool shape {ok:false, error:<message>} with is_error set.
//
// Parity: tools/src/gcu/browser/tools/tabs.py::browser_tabs (connection-error
// branch returns {"ok": False, "error": connection_error(bridge)}).
func TestBrowserToolExecutorReportsBridgeRefusal(t *testing.T) {
	server, _ := fakeBrowserBridge(t, http.StatusServiceUnavailable, map[string]any{
		"ok": false, "code": "not_connected", "message": "no browser extension is connected",
	})
	t.Setenv("HIVE_BROWSERBRIDGE_HTTP", server.URL)
	catalogue := queenFlatCatalogue(t)

	content, isError, err := catalogue.Execute(context.Background(), "browser_open", map[string]any{
		"url": "https://example.com/",
	})
	if err != nil {
		t.Fatalf("browser_open: %v", err)
	}
	if !isError {
		t.Fatalf("bridge refusal must be a tool error: %s", content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("error result is not JSON: %q", content)
	}
	if payload["ok"] != false || payload["error"] != "no browser extension is connected" {
		t.Fatalf("error payload = %+v", payload)
	}
}
