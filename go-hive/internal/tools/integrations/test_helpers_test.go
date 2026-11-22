package integrations

// Cross-family test helpers that used to live in the batch-4 test file. The
// notion/linear lane moved to the collab subpackage (int-split-p3), but the
// cross-family suites that stay in this package (account alias routing, OAuth
// refresh, scheduling/chat, marketing/productivity, cloudinary, graph, email)
// still assert against these recorders and JSON helpers, so they stay here
// under the same names.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// notionVersion is the Notion-Version header value the notion provider sends.
const notionVersion = "2022-06-28"

type batch4LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch4Recorder struct {
	mu       sync.Mutex
	requests []batch4LoggedRequest
}

func (rec *batch4Recorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch4LoggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()
		next(w, r, body)
	}
}

func (rec *batch4Recorder) at(t *testing.T, index int) batch4LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch4Recorder) last(t *testing.T) batch4LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch4Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch4TestRegistry builds a notion/linear registry pointed at one fake
// upstream (via the root facade over the collab subpackage).
func batch4TestRegistry(t *testing.T, module string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
	}
	return newBatch4Registry(append(base, opts...)...), recorder
}

func batch4Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

func batch4WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch4ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch4BodyMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map", value)
	}
	return object
}

func batch4BodySlice(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want array", value)
	}
	return items
}

type batch4TimeoutError struct{}

func (batch4TimeoutError) Error() string   { return "i/o timeout" }
func (batch4TimeoutError) Timeout() bool   { return true }
func (batch4TimeoutError) Temporary() bool { return true }

type batch4TimeoutTransport struct{}

func (batch4TimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, batch4TimeoutError{}
}

// ---------------------------------------------------- batch-5 lane-4 kit ---

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

// batch5Lane4TestRegistry builds a hubspot/obsidian registry pointed at one
// fake upstream (via the root facade over the crm subpackage), with the module
// credential supplied out of band.
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
	return newBatch5Lane4Registry(append(base, opts...)...), recorder
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

// --------------------------------------------------------- batch-6 CRM kit ---

type batch6LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch6Recorder struct {
	mu       sync.Mutex
	requests []batch6LoggedRequest
}

func (rec *batch6Recorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch6LoggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()
		next(w, r, body)
	}
}

func (rec *batch6Recorder) at(t *testing.T, index int) batch6LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch6Recorder) last(t *testing.T) batch6LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch6Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch6Execute runs one action and returns the map result.
func batch6Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

// ------------------------------------------- Google Workspace lane-3 kit ---

// Cross-family test helpers for the Google Workspace modules. The workspace
// provider files moved to the gworkspace subpackage (int-split-p3), but the
// account-alias and OAuth-refresh suites stay in this package because they
// assert across families; the helpers below keep those suites compiling
// against the root facade.

// batch5Lane3CalModule is the calendar module id (the provider file lives in
// the gworkspace subpackage now).
const batch5Lane3CalModule = "calendar_tool"

// batch5Lane3TestRegistry points the four Google modules at one fake upstream
// and installs a shared test credential.
func batch5Lane3TestRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL("google_sheets_tool", server.URL),
		WithBaseURL("google_docs_tool", server.URL),
		WithBaseURL("google_docs_drive", server.URL),
		WithBaseURL("gmail_tool", server.URL),
		WithBaseURL("calendar_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithCredential("google_sheets_tool", "test-token"),
		WithCredential("google_docs_tool", "test-token"),
		WithCredential("gmail_tool", "test-token"),
		WithCredential("calendar_tool", "test-token"),
	}
	return newBatch5Lane3Registry(append(base, opts...)...), recorder
}

// batch5Lane3RequireBearer asserts the shared Google access token is sent.
func batch5Lane3RequireBearer(t *testing.T, recorder *batch4Recorder) {
	t.Helper()
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}
