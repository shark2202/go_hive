package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch5Lane4LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

type batch5Lane4Recorder struct {
	mu       sync.Mutex
	requests []batch5Lane4LoggedRequest
}

func (rec *batch5Lane4Recorder) wrap(next func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch5Lane4LoggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   raw,
		})
		rec.mu.Unlock()
		next(w, r, raw)
	}
}

func (rec *batch5Lane4Recorder) last(t *testing.T) batch5Lane4LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch5Lane4Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// JSON decodes a recorded request body as an object.
func (req batch5Lane4LoggedRequest) JSON(t *testing.T) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(req.Body, &object); err != nil {
		t.Fatalf("decode body %q: %v", string(req.Body), err)
	}
	return object
}

// JSONValue decodes a recorded request body into any JSON shape.
func (req batch5Lane4LoggedRequest) JSONValue(t *testing.T) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(req.Body, &value); err != nil {
		t.Fatalf("decode body %q: %v", string(req.Body), err)
	}
	return value
}

func (req batch5Lane4LoggedRequest) text() string { return string(req.Body) }

func batch5Lane4JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch5Lane4Text(w http.ResponseWriter, status int, contentType, text string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

// batch5Lane4TestRegistry builds a batch-5 lane-4 registry pointed at one fake
// upstream, with the module credential supplied out of band.
func batch5Lane4TestRegistry(t *testing.T, module string, handler func(http.ResponseWriter, *http.Request, []byte), opts ...Option) (*Registry, *batch5Lane4Recorder) {
	t.Helper()
	recorder := &batch5Lane4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
	}
	return NewHubSpotObsidianRegistry(append(base, opts...)...), recorder
}

func batch5Lane4HubSpotRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, []byte)) (*Registry, *batch5Lane4Recorder) {
	t.Helper()
	return batch5Lane4TestRegistry(t, "hubspot_tool", handler)
}

func batch5Lane4ObsidianRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, []byte)) (*Registry, *batch5Lane4Recorder) {
	t.Helper()
	return batch5Lane4TestRegistry(t, "obsidian_tool", handler)
}

func batch5Lane4Run(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

func batch5Lane4ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

// batch5Lane4TimeoutTransport makes every request fail like a client timeout.
type batch5Lane4TimeoutTransport struct{}

func (batch5Lane4TimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}

// batch5Lane4NetworkTransport makes every request fail before a response exists.
type batch5Lane4NetworkTransport struct{}

func (batch5Lane4NetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection failed")
}

// batch5Lane4GetRequest is a small helper for asserting the recorded request shape.
func batch5Lane4GetRequest(t *testing.T, recorder *batch5Lane4Recorder, method, path string) batch5Lane4LoggedRequest {
	t.Helper()
	request := recorder.last(t)
	if request.Method != method {
		t.Fatalf("method = %s, want %s", request.Method, method)
	}
	if request.Path != path {
		t.Fatalf("path = %q, want %q", request.Path, path)
	}
	return request
}

// batch5Lane4ActionNames returns the catalog action names for one tool.
func batch5Lane4ActionNames(registry *Registry, tool string) []string {
	entry, ok := registry.Lookup(tool)
	if !ok {
		return nil
	}
	return entry.ActionNames()
}

// batch5Lane4HasAction reports whether names contains action.
func batch5Lane4HasAction(names []string, action string) bool {
	for _, name := range names {
		if name == action {
			return true
		}
	}
	return false
}

// batch5Lane4InstallCount installs the registry into a shared tool registry.
func batch5Lane4InstallCount(t *testing.T, registry *Registry) (int, *tools.ToolRegistry) {
	t.Helper()
	shared := tools.NewToolRegistry()
	return registry.Install(shared), shared
}
