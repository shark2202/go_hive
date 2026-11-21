package integrations

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch7Lane4Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch7Lane4Recorder struct {
	mu       sync.Mutex
	requests []batch7Lane4Request
}

func (rec *batch7Lane4Recorder) wrap(
	next func(http.ResponseWriter, *http.Request, map[string]any),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch7Lane4Request{
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

func (rec *batch7Lane4Recorder) at(t *testing.T, index int) batch7Lane4Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch7Lane4Recorder) last(t *testing.T) batch7Lane4Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch7Lane4Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch7Lane4Fake spins up one fake upstream, points the listed modules at it
// and returns the registry plus the recorded requests and the server URL (the
// Pinecone data plane takes its host per call).
func batch7Lane4Fake(
	t *testing.T,
	handler func(http.ResponseWriter, *http.Request, map[string]any),
	modules []string,
	opts ...Option,
) (*Registry, *batch7Lane4Recorder, string) {
	t.Helper()
	recorder := &batch7Lane4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	options := []Option{
		WithHTTPClient(server.Client()),
		WithGetenv(func(string) string { return "" }),
	}
	for _, module := range modules {
		options = append(options, WithBaseURL(module, server.URL))
	}
	options = append(options, opts...)
	return newBatch7Lane4Registry(options...), recorder, server.URL
}

// batch7Lane4EnvOption installs a fixed environment map.
func batch7Lane4EnvOption(env map[string]string) Option {
	return WithGetenv(func(name string) string { return env[name] })
}

func batch7Lane4Execute(
	t *testing.T,
	registry *Registry,
	tool string,
	input map[string]any,
) map[string]any {
	t.Helper()
	result, err := registry.Execute(t.Context(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

func batch7Lane4WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch7Lane4ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch7Lane4StringField(t *testing.T, source map[string]any, name string) string {
	t.Helper()
	value, ok := source[name].(string)
	if !ok {
		t.Fatalf("field %q = %T, want string (%v)", name, source[name], source)
	}
	return value
}

func batch7Lane4FieldMap(t *testing.T, source map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := source[name].(map[string]any)
	if !ok {
		t.Fatalf("field %q = %T, want map (%v)", name, source[name], source)
	}
	return value
}

func batch7Lane4FieldSlice(t *testing.T, source map[string]any, name string) []any {
	t.Helper()
	value, present := source[name]
	if !present || value == nil {
		t.Fatalf("field %q missing (%v)", name, source)
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		t.Fatalf("field %q = %T, want array (%v)", name, value, source)
	}
	out := make([]any, 0, rv.Len())
	for index := 0; index < rv.Len(); index++ {
		out = append(out, rv.Index(index).Interface())
	}
	return out
}

func batch7Lane4Number(t *testing.T, value any) int64 {
	t.Helper()
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		t.Fatalf("value = %T, want a number", value)
		return 0
	}
}

func batch7Lane4EqualInt(t *testing.T, value any, want int) {
	t.Helper()
	if got := batch7Lane4Number(t, value); got != int64(want) {
		t.Fatalf("value = %d, want %d", got, want)
	}
}

// batch7Lane4StatusHandler fails every request with one raw-text status.
func batch7Lane4StatusHandler(status int) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("provider failure"))
	}
}

// batch7Lane4ErrorTable runs one action against each failing status.
func batch7Lane4ErrorTable(
	t *testing.T,
	modules []string,
	tool string,
	opts []Option,
	input map[string]any,
	cases map[int]string,
) {
	t.Helper()
	for status, want := range cases {
		status, want := status, want
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			registry, _, _ := batch7Lane4Fake(
				t, batch7Lane4StatusHandler(status), modules, opts...,
			)
			result := batch7Lane4Execute(t, registry, tool, input)
			if got := batch7Lane4ErrorText(t, result); got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not the registry surface.
func TestBatch7Lane4CatalogIntegrity(t *testing.T) {
	registry := newBatch7Lane4Registry()
	if got := len(registry.Names()); got != 5 {
		t.Fatalf("tools = %d, want 5 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 39 {
		t.Fatalf("actions = %d, want 39", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	expected := map[string]int{
		"pinecone":    9,
		"wandb":       6,
		"langfuse":    6,
		"huggingface": 9,
		"vision":      9,
	}
	for name, want := range expected {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
		if tool.ID == "" || tool.Summary == "" || tool.DisplayName == "" {
			t.Fatalf("%s missing identity: %+v", name, tool)
		}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if registry.Impls(name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch7Lane4(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch7Lane4(shared); installed != 5 {
		t.Fatalf("installed = %d, want 5", installed)
	}
	for _, name := range []string{"pinecone", "wandb", "langfuse", "huggingface", "vision"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("vision")
	if !ok {
		t.Fatal("vision tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("vision missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 9 {
		t.Fatalf("vision action enum = %v", action["enum"])
	}
	if tool.Description == "" {
		t.Fatal("vision empty description")
	}
}

// Parity-exempt: schema shape is a Go-side contract with tools.ToolRegistry.
func TestBatch7Lane4SchemaShape(t *testing.T) {
	registry := newBatch7Lane4Registry()
	for _, tool := range registry.Tools() {
		schema := tool.Schema()
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v", tool.Name, schema["type"])
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties missing", tool.Name)
		}
		action, ok := properties["action"].(map[string]any)
		if !ok {
			t.Fatalf("%s action property missing", tool.Name)
		}
		enum, ok := action["enum"].([]string)
		if !ok || len(enum) != len(tool.Actions) {
			t.Fatalf("%s action enum = %v", tool.Name, action["enum"])
		}
		if required, _ := schema["required"].([]string); len(required) != 1 || required[0] != "action" {
			t.Fatalf("%s required = %v", tool.Name, schema["required"])
		}
	}
}

// Parity-exempt: unknown-action dispatch is a Go-side registry contract.
func TestBatch7Lane4UnknownAction(t *testing.T) {
	registry := newBatch7Lane4Registry()
	_, err := registry.Execute(t.Context(), "vision", map[string]any{"action": "nope"})
	if err == nil {
		t.Fatal("expected unknown action error")
	}
}

// --------------------------------------------------------------- pinecone ---

func batch7Lane4PineconeEnv() map[string]string {
	return map[string]string{"PINECONE_API_KEY": "pc-test-key"}
}

func batch7Lane4PineconeIndex() map[string]any {
	return map[string]any{
		"name":        "my-index",
		"dimension":   1536,
		"metric":      "cosine",
		"host":        "my-index-abc123.svc.pinecone.io",
		"vector_type": "dense",
		"status":      map[string]any{"ready": true, "state": "Ready"},
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeListIndexes::test_successful_list
func TestBatch7Lane4PineconeListIndexes(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"indexes": []any{batch7Lane4PineconeIndex()},
			})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_list_indexes",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodGet || request.Path != "/indexes" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if got := request.Header.Get("Api-Key"); got != "pc-test-key" {
		t.Fatalf("Api-Key = %q", got)
	}
	if got := request.Header.Get("X-Pinecone-Api-Version"); got != "2025-04" {
		t.Fatalf("version = %q", got)
	}
	batch7Lane4EqualInt(t, result["count"], 1)
	index := batch7Lane4FieldSlice(t, result, "indexes")[0].(map[string]any)
	if index["name"] != "my-index" || index["state"] != "Ready" || index["ready"] != true {
		t.Fatalf("index = %v", index)
	}
	batch7Lane4EqualInt(t, index["dimension"], 1536)
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeListIndexes::test_missing_token
func TestBatch7Lane4PineconeMissingToken(t *testing.T) {
	registry := newBatch7Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_list_indexes",
	})
	if got := batch7Lane4ErrorText(t, result); got != "PINECONE_API_KEY not set" {
		t.Fatalf("error = %q", got)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "app.pinecone.io") {
		t.Fatalf("help = %v", result["help"])
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeCreateIndex::test_successful_create
func TestBatch7Lane4PineconeCreateIndex(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusCreated, map[string]any{
				"name": "new-idx", "dimension": 768, "metric": "cosine",
				"host": "new-idx-xyz.svc.pinecone.io",
			})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_create_index", "name": "new-idx", "dimension": 768,
		"cloud": "gcp", "region": "europe-west1",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/indexes" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	spec := batch7Lane4FieldMap(t, request.Body, "spec")
	serverless := batch7Lane4FieldMap(t, spec, "serverless")
	if serverless["cloud"] != "gcp" || serverless["region"] != "europe-west1" {
		t.Fatalf("serverless = %v", serverless)
	}
	if request.Body["metric"] != "cosine" {
		t.Fatalf("metric = %v", request.Body["metric"])
	}
	if result["status"] != "created" || result["name"] != "new-idx" {
		t.Fatalf("result = %v", result)
	}
	batch7Lane4EqualInt(t, result["dimension"], 768)
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeCreateIndex::test_missing_params
func TestBatch7Lane4PineconeCreateIndexValidation(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_create_index", "name": "", "dimension": 0,
	})
	if got := batch7Lane4ErrorText(t, result); got != "name and dimension are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeDescribeIndex::test_successful_describe
func TestBatch7Lane4PineconeDescribeIndex(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			index := batch7Lane4PineconeIndex()
			index["deletion_protection"] = "disabled"
			index["spec"] = map[string]any{
				"serverless": map[string]any{"cloud": "aws", "region": "us-east-1"},
			}
			batch7Lane4WriteJSON(w, http.StatusOK, index)
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_describe_index", "index_name": "my-index",
	})
	if recorder.at(t, 0).Path != "/indexes/my-index" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "my-index" || result["ready"] != true {
		t.Fatalf("result = %v", result)
	}
	if result["deletion_protection"] != "disabled" {
		t.Fatalf("deletion_protection = %v", result["deletion_protection"])
	}
	if _, ok := result["spec"].(map[string]any); !ok {
		t.Fatalf("spec = %v", result["spec"])
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeDeleteIndex::test_successful_delete
func TestBatch7Lane4PineconeDeleteIndex(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusAccepted)
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_delete_index", "index_name": "old-index",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodDelete || request.Path != "/indexes/old-index" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if result["status"] != "deleted" || result["index_name"] != "old-index" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeUpsertVectors::test_successful_upsert
func TestBatch7Lane4PineconeUpsertVectors(t *testing.T) {
	registry, recorder, base := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{"upsertedCount": 2})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_upsert_vectors", "index_host": base,
		"vectors": []any{
			map[string]any{"id": "v1", "values": []any{0.1, 0.2, 0.3}},
			map[string]any{"id": "v2", "values": []any{0.4, 0.5, 0.6}},
		},
		"namespace": "docs",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/vectors/upsert" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if vectors := batch7Lane4FieldSlice(t, request.Body, "vectors"); len(vectors) != 2 {
		t.Fatalf("vectors = %v", vectors)
	}
	if request.Body["namespace"] != "docs" {
		t.Fatalf("namespace = %v", request.Body["namespace"])
	}
	batch7Lane4EqualInt(t, result["upserted_count"], 2)
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeQueryVectors::test_successful_query
func TestBatch7Lane4PineconeQueryVectors(t *testing.T) {
	registry, recorder, base := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"matches": []any{
					map[string]any{"id": "v1", "score": 0.95, "metadata": map[string]any{"topic": "AI"}},
					map[string]any{"id": "v2", "score": 0.82, "metadata": map[string]any{"topic": "ML"}},
				},
				"namespace": "",
			})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_query_vectors", "index_host": base,
		"vector": []any{0.1, 0.2, 0.3}, "top_k": 5,
	})
	request := recorder.at(t, 0)
	if request.Path != "/query" {
		t.Fatalf("path = %s", request.Path)
	}
	batch7Lane4EqualInt(t, request.Body["topK"], 5)
	if request.Body["includeMetadata"] != true || request.Body["includeValues"] != false {
		t.Fatalf("body = %v", request.Body)
	}
	matches := batch7Lane4FieldSlice(t, result, "matches")
	if len(matches) != 2 {
		t.Fatalf("matches = %v", matches)
	}
	first := matches[0].(map[string]any)
	if first["score"] != 0.95 {
		t.Fatalf("score = %v", first["score"])
	}
	if metadata := batch7Lane4FieldMap(t, first, "metadata"); metadata["topic"] != "AI" {
		t.Fatalf("metadata = %v", metadata)
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeFetchVectors::test_successful_fetch
func TestBatch7Lane4PineconeFetchVectors(t *testing.T) {
	registry, recorder, base := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"vectors": map[string]any{
					"v1": map[string]any{"id": "v1", "values": []any{0.1, 0.2}, "metadata": nil},
				},
				"namespace": "",
			})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_fetch_vectors", "index_host": base, "ids": []any{"v1"},
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodGet || request.Path != "/vectors/fetch" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if got := request.Query.Get("ids"); got != "v1" {
		t.Fatalf("ids = %q", got)
	}
	vectors := batch7Lane4FieldMap(t, result, "vectors")
	if _, present := vectors["v1"]; !present {
		t.Fatalf("vectors = %v", vectors)
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeDeleteVectors::test_successful_delete
func TestBatch7Lane4PineconeDeleteVectors(t *testing.T) {
	registry, recorder, base := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_delete_vectors", "index_host": base, "ids": []any{"v1", "v2"},
	})
	request := recorder.at(t, 0)
	if request.Path != "/vectors/delete" {
		t.Fatalf("path = %s", request.Path)
	}
	if ids := batch7Lane4FieldSlice(t, request.Body, "ids"); len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	if result["status"] != "deleted" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeIndexStats::test_successful_stats
func TestBatch7Lane4PineconeIndexStats(t *testing.T) {
	registry, recorder, base := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"namespaces": map[string]any{
					"":     map[string]any{"vectorCount": 100},
					"docs": map[string]any{"vectorCount": 50},
				},
				"dimension":        1536,
				"totalVectorCount": 150,
				"metric":           "cosine",
				"vectorType":       "dense",
			})
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_index_stats", "index_host": base,
	})
	if recorder.at(t, 0).Path != "/describe_index_stats" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	batch7Lane4EqualInt(t, result["total_vector_count"], 150)
	namespaces := batch7Lane4FieldMap(t, result, "namespaces")
	docs := batch7Lane4FieldMap(t, namespaces, "docs")
	batch7Lane4EqualInt(t, docs["vector_count"], 50)
}

// Parity: tools/tests/tools/test_pinecone_tool.py::TestPineconeUpsertVectors::test_missing_params
// Also covers the upstream missing-argument cases for describe_index,
// delete_index, query_vectors, fetch_vectors, delete_vectors and index_stats.
func TestBatch7Lane4PineconeValidation(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	cases := []struct {
		input map[string]any
		want  string
	}{
		{map[string]any{"action": "pinecone_describe_index"}, "index_name is required"},
		{map[string]any{"action": "pinecone_delete_index", "index_name": ""}, "index_name is required"},
		{
			map[string]any{"action": "pinecone_upsert_vectors", "index_host": "", "vectors": []any{}},
			"index_host and vectors are required",
		},
		{
			map[string]any{"action": "pinecone_query_vectors", "index_host": "host.io"},
			"Either vector or id is required",
		},
		{
			map[string]any{"action": "pinecone_fetch_vectors", "index_host": "host.io", "ids": []any{}},
			"index_host and ids are required",
		},
		{
			map[string]any{"action": "pinecone_delete_vectors", "index_host": "host.io"},
			"Provide ids, delete_all=True, or a filter",
		},
		{map[string]any{"action": "pinecone_index_stats"}, "index_host is required"},
	}
	for _, testCase := range cases {
		result := batch7Lane4Execute(t, registry, "pinecone", testCase.input)
		if got := batch7Lane4ErrorText(t, result); got != testCase.want {
			t.Fatalf("%v: error = %q, want %q", testCase.input["action"], got, testCase.want)
		}
	}
}

// Parity-exempt: the upstream suite has no pinecone status-matrix test; the
// strings mirror pinecone_tool._control / _data.
func TestBatch7Lane4PineconeErrorMapping(t *testing.T) {
	batch7Lane4ErrorTable(
		t, []string{"pinecone_tool"}, "pinecone",
		[]Option{batch7Lane4EnvOption(batch7Lane4PineconeEnv())},
		map[string]any{"action": "pinecone_list_indexes"},
		map[int]string{
			http.StatusUnauthorized:    "Unauthorized. Check your PINECONE_API_KEY.",
			http.StatusNotFound:        "Pinecone API error 404: provider failure",
			http.StatusTooManyRequests: "Pinecone API error 429: provider failure",
		},
	)
}

// Parity-exempt: the upstream suite has no pinecone data-plane status test.
func TestBatch7Lane4PineconeDataPlaneErrorMapping(t *testing.T) {
	registry, _, base := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusUnauthorized)
		},
		[]string{"pinecone_tool"},
		batch7Lane4EnvOption(batch7Lane4PineconeEnv()),
	)
	result := batch7Lane4Execute(t, registry, "pinecone", map[string]any{
		"action": "pinecone_index_stats", "index_host": base,
	})
	if got := batch7Lane4ErrorText(t, result); got != "Unauthorized. Check your PINECONE_API_KEY." {
		t.Fatalf("error = %q", got)
	}
}

// ------------------------------------------------------------------ wandb ---

func batch7Lane4WandbEnv() map[string]string {
	return map[string]string{"WANDB_API_KEY": "test-key-abcdefghij"}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_missing_credentials_returns_error
func TestBatch7Lane4WandbMissingCredentials(t *testing.T) {
	registry := newBatch7Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_list_projects", "entity": "test-entity",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Weights & Biases credentials not configured" {
		t.Fatalf("error = %q", got)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "wandb.ai/authorize") {
		t.Fatalf("help = %v", result["help"])
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_list_projects_success
func TestBatch7Lane4WandbListProjects(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"projects": map[string]any{
						"edges": []any{
							map[string]any{"node": map[string]any{
								"name": "proj-a", "description": "Desc A", "createdAt": "2024-01-01",
							}},
							map[string]any{"node": map[string]any{
								"name": "proj-b", "description": "", "createdAt": "2024-02-01",
							}},
						},
					},
				},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_list_projects", "entity": "test-entity",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %s", request.Method)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-key-abcdefghij" {
		t.Fatalf("Authorization = %q", got)
	}
	variables := batch7Lane4FieldMap(t, request.Body, "variables")
	if variables["entity"] != "test-entity" {
		t.Fatalf("variables = %v", variables)
	}
	if result["entity"] != "test-entity" {
		t.Fatalf("entity = %v", result["entity"])
	}
	projects := batch7Lane4FieldSlice(t, result, "projects")
	if len(projects) != 2 {
		t.Fatalf("projects = %v", projects)
	}
	if projects[0].(map[string]any)["name"] != "proj-a" {
		t.Fatalf("projects[0] = %v", projects[0])
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_list_runs_success
func TestBatch7Lane4WandbListRuns(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"project": map[string]any{
						"runs": map[string]any{
							"edges": []any{
								map[string]any{"node": map[string]any{
									"name": "w854ckuu", "id": "ferengi-directive-1",
									"state": "finished", "createdAt": "2024-01-01",
									"config": "{}", "summaryMetrics": "{}",
								}},
							},
						},
					},
				},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_list_runs", "entity": "testentity", "project": "testproject",
		"filters": `{"key": "value"}`, "per_page": 50,
	})
	variables := batch7Lane4FieldMap(t, recorder.at(t, 0).Body, "variables")
	batch7Lane4EqualInt(t, variables["perPage"], 50)
	filters := batch7Lane4FieldMap(t, variables, "filters")
	if filters["key"] != "value" {
		t.Fatalf("filters = %v", filters)
	}
	if result["project"] != "testproject" {
		t.Fatalf("project = %v", result["project"])
	}
	runs := batch7Lane4FieldSlice(t, result, "runs")
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	run := runs[0].(map[string]any)
	if run["id"] != "w854ckuu" || run["display_name"] != "ferengi-directive-1" {
		t.Fatalf("run = %v", run)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_list_runs_invalid_filters_json
func TestBatch7Lane4WandbListRunsInvalidFilters(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_list_runs", "entity": "e", "project": "p", "filters": "not-json",
	})
	if got := batch7Lane4ErrorText(t, result); !strings.Contains(got, "valid JSON") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_get_run_success
func TestBatch7Lane4WandbGetRun(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"project": map[string]any{
						"run": map[string]any{
							"name": "run-123", "id": "my-run", "state": "finished",
							"createdAt": "2024-01-01", "config": `{"lr": 0.001}`,
							"summaryMetrics": `{"accuracy": 0.9}`,
							"tags":           []any{"v1"}, "notes": "test",
						},
					},
				},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_get_run", "entity": "e", "project": "p", "run_id": "run-123",
	})
	if result["id"] != "run-123" || result["display_name"] != "my-run" {
		t.Fatalf("result = %v", result)
	}
	config := batch7Lane4FieldMap(t, result, "config")
	if config["lr"] != 0.001 {
		t.Fatalf("config = %v", config)
	}
	summary := batch7Lane4FieldMap(t, result, "summary")
	if summary["accuracy"] != 0.9 {
		t.Fatalf("summary = %v", summary)
	}
	if tags := batch7Lane4FieldSlice(t, result, "tags"); len(tags) != 1 || tags[0] != "v1" {
		t.Fatalf("tags = %v", tags)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_get_run_not_found
func TestBatch7Lane4WandbGetRunNotFound(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{"project": map[string]any{"run": nil}},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_get_run", "entity": "e", "project": "p", "run_id": "nope",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Weights & Biases resource not found" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_get_run_metrics_success
func TestBatch7Lane4WandbGetRunMetrics(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"project": map[string]any{
						"run": map[string]any{
							"sampledHistory": []any{
								map[string]any{"loss": 0.5},
								map[string]any{"loss": 0.3},
							},
						},
					},
				},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_get_run_metrics", "entity": "e", "project": "p",
		"run_id": "r1", "metric_keys": "loss, accuracy",
	})
	queryText := batch7Lane4StringField(t, recorder.at(t, 0).Body, "query")
	if !strings.Contains(queryText, "sampledHistory(specs: ") ||
		!strings.Contains(queryText, `"key":"loss"`) {
		t.Fatalf("query = %s", queryText)
	}
	keys := batch7Lane4FieldSlice(t, result, "metric_keys")
	if len(keys) != 2 || keys[0] != "loss" || keys[1] != "accuracy" {
		t.Fatalf("metric_keys = %v", keys)
	}
	if history := batch7Lane4FieldSlice(t, result, "history"); len(history) != 2 {
		t.Fatalf("history = %v", history)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_list_artifacts_success
func TestBatch7Lane4WandbListArtifacts(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"project": map[string]any{
						"run": map[string]any{
							"outputArtifacts": map[string]any{
								"edges": []any{
									map[string]any{"node": map[string]any{
										"name": "model:v0", "type": "model",
										"description": "", "createdAt": "2024-01-01",
									}},
								},
							},
						},
					},
				},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_list_artifacts", "entity": "e", "project": "p", "run_id": "r1",
	})
	if result["run_id"] != "r1" {
		t.Fatalf("run_id = %v", result["run_id"])
	}
	artifacts := batch7Lane4FieldSlice(t, result, "artifacts")
	if len(artifacts) != 1 || artifacts[0].(map[string]any)["name"] != "model:v0" {
		t.Fatalf("artifacts = %v", artifacts)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_get_summary_success
func TestBatch7Lane4WandbGetSummary(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"project": map[string]any{
						"run": map[string]any{
							"summaryMetrics": `{"accuracy": 0.9, "loss": 0.1, "_step": 5}`,
						},
					},
				},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_get_summary", "entity": "e", "project": "p", "run_id": "r1",
	})
	summary := batch7Lane4FieldMap(t, result, "summary")
	if summary["accuracy"] != 0.9 {
		t.Fatalf("summary = %v", summary)
	}
	if _, present := summary["_step"]; present {
		t.Fatalf("summary leaked an internal key: %v", summary)
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_get_run_missing_id
// Also covers the upstream missing run_id / metric_keys cases for
// get_run_metrics, list_artifacts and get_summary.
func TestBatch7Lane4WandbValidation(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	cases := []struct {
		input map[string]any
		want  string
	}{
		{
			map[string]any{"action": "wandb_get_run", "entity": "e", "project": "p", "run_id": ""},
			"run_id is required",
		},
		{
			map[string]any{"action": "wandb_get_run_metrics", "entity": "e", "project": "p", "run_id": ""},
			"run_id is required",
		},
		{
			map[string]any{
				"action": "wandb_get_run_metrics", "entity": "e", "project": "p", "run_id": "r1",
			},
			"metric_keys is required (comma-separated, e.g. 'loss,accuracy')",
		},
		{
			map[string]any{"action": "wandb_list_artifacts", "entity": "e", "project": "p", "run_id": ""},
			"run_id is required",
		},
		{
			map[string]any{"action": "wandb_get_summary", "entity": "e", "project": "p", "run_id": ""},
			"run_id is required",
		},
	}
	for _, testCase := range cases {
		result := batch7Lane4Execute(t, registry, "wandb", testCase.input)
		if got := batch7Lane4ErrorText(t, result); got != testCase.want {
			t.Fatalf("%v: error = %q, want %q", testCase.input["action"], got, testCase.want)
		}
	}
}

// Parity: tools/tests/tools/test_wandb_tool.py::TestWandbTool::test_wandb_list_projects_graphql_error
func TestBatch7Lane4WandbGraphQLError(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"errors": []any{map[string]any{"message": "entity not found"}},
			})
		},
		[]string{"wandb_tool"},
		batch7Lane4EnvOption(batch7Lane4WandbEnv()),
	)
	result := batch7Lane4Execute(t, registry, "wandb", map[string]any{
		"action": "wandb_list_projects", "entity": "e",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Weights & Biases GraphQL error: entity not found" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: the upstream suite covers only the 401 case; the strings for
// 403/404/429 mirror wandb_tool._graphql.
func TestBatch7Lane4WandbErrorMapping(t *testing.T) {
	batch7Lane4ErrorTable(
		t, []string{"wandb_tool"}, "wandb",
		[]Option{batch7Lane4EnvOption(batch7Lane4WandbEnv())},
		map[string]any{"action": "wandb_list_projects", "entity": "e"},
		map[int]string{
			http.StatusUnauthorized:    "Invalid Weights & Biases API key",
			http.StatusForbidden:       "Insufficient permissions for this Weights & Biases resource",
			http.StatusNotFound:        "Weights & Biases API error (HTTP 404): provider failure",
			http.StatusTooManyRequests: "Weights & Biases API error (HTTP 429): provider failure",
		},
	)
}

// --------------------------------------------------------------- langfuse ---

func batch7Lane4LangfuseEnv() map[string]string {
	return map[string]string{
		"LANGFUSE_PUBLIC_KEY": "pk-lf-test",
		"LANGFUSE_SECRET_KEY": "sk-lf-test",
		"LANGFUSE_HOST":       "https://cloud.langfuse.com",
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseListTraces::test_missing_credentials
func TestBatch7Lane4LangfuseMissingCredentials(t *testing.T) {
	registry := newBatch7Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_list_traces",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Langfuse credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseListTraces::test_successful_list
func TestBatch7Lane4LangfuseListTraces(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{
					map[string]any{
						"id": "trace-abc123", "name": "chat-completion",
						"timestamp": "2025-10-16T12:00:00.000Z",
						"userId":    "user_123", "sessionId": "session_456",
						"tags": []any{"production"}, "latency": 1.234, "totalCost": 0.0045,
						"observations": []any{"obs-1", "obs-2"},
					},
				},
				"meta": map[string]any{"page": 1, "limit": 50, "totalItems": 1, "totalPages": 1},
			})
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_list_traces", "name": "chat-completion", "tags": "prod, eu",
	})
	request := recorder.at(t, 0)
	if request.Path != "/api/public/traces" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Header.Get("Authorization"); got !=
		"Basic "+base64.StdEncoding.EncodeToString([]byte("pk-lf-test:sk-lf-test")) {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Query.Get("name"); got != "chat-completion" {
		t.Fatalf("name = %q", got)
	}
	if tags := request.Query["tags"]; len(tags) != 2 || tags[0] != "prod" || tags[1] != "eu" {
		t.Fatalf("tags = %v", tags)
	}
	batch7Lane4EqualInt(t, result["count"], 1)
	batch7Lane4EqualInt(t, result["total_items"], 1)
	trace := batch7Lane4FieldSlice(t, result, "traces")[0].(map[string]any)
	if trace["id"] != "trace-abc123" {
		t.Fatalf("trace = %v", trace)
	}
	batch7Lane4EqualInt(t, trace["observation_count"], 2)
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseGetTrace::test_successful_get
func TestBatch7Lane4LangfuseGetTrace(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "trace-abc123", "name": "chat-completion",
				"timestamp": "2025-10-16T12:00:00.000Z",
				"userId":    "user_123", "sessionId": "session_456",
				"tags": []any{"production"}, "latency": 1.234, "totalCost": 0.0045,
				"input":  map[string]any{"messages": []any{}},
				"output": map[string]any{"response": "Hi there!"},
				"observations": []any{
					map[string]any{
						"id": "obs-1", "type": "GENERATION", "name": "gpt-4-call",
						"model": "gpt-4", "startTime": "2025-10-16T12:00:00.500Z",
						"endTime": "2025-10-16T12:00:01.200Z",
						"usage":   map[string]any{"input": 150, "output": 80, "total": 230},
					},
				},
				"scores": []any{
					map[string]any{
						"id": "score-1", "name": "correctness", "value": 0.9,
						"dataType": "NUMERIC", "source": "API", "comment": "Factually correct",
					},
				},
			})
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_get_trace", "trace_id": "trace-abc123",
	})
	if recorder.at(t, 0).Path != "/api/public/traces/trace-abc123" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["id"] != "trace-abc123" {
		t.Fatalf("id = %v", result["id"])
	}
	observations := batch7Lane4FieldSlice(t, result, "observations")
	if len(observations) != 1 || observations[0].(map[string]any)["model"] != "gpt-4" {
		t.Fatalf("observations = %v", observations)
	}
	scores := batch7Lane4FieldSlice(t, result, "scores")
	if scores[0].(map[string]any)["value"] != 0.9 {
		t.Fatalf("scores = %v", scores)
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseListScores::test_successful_list
func TestBatch7Lane4LangfuseListScores(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{
					map[string]any{
						"id": "score-1", "traceId": "trace-abc123", "observationId": nil,
						"name": "correctness", "value": 0.9, "dataType": "NUMERIC",
						"source": "API", "comment": "Good",
						"timestamp": "2025-10-16T12:01:00.000Z",
					},
				},
				"meta": map[string]any{"page": 1, "limit": 50, "totalItems": 1, "totalPages": 1},
			})
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_list_scores", "trace_id": "trace-abc123", "data_type": "NUMERIC",
	})
	request := recorder.at(t, 0)
	if request.Path != "/api/public/v2/scores" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("traceId"); got != "trace-abc123" {
		t.Fatalf("traceId = %q", got)
	}
	if got := request.Query.Get("dataType"); got != "NUMERIC" {
		t.Fatalf("dataType = %q", got)
	}
	scores := batch7Lane4FieldSlice(t, result, "scores")
	if len(scores) != 1 || scores[0].(map[string]any)["name"] != "correctness" {
		t.Fatalf("scores = %v", scores)
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseCreateScore::test_successful_create
func TestBatch7Lane4LangfuseCreateScore(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{"id": "score-new-123"})
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_create_score", "trace_id": "trace-abc123",
		"name": "helpfulness", "value": 1.0, "data_type": "BOOLEAN",
		"comment": "Very helpful",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/api/public/scores" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["traceId"] != "trace-abc123" || request.Body["dataType"] != "BOOLEAN" {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["value"] != 1.0 || request.Body["comment"] != "Very helpful" {
		t.Fatalf("body = %v", request.Body)
	}
	if result["id"] != "score-new-123" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseListPrompts::test_successful_list
func TestBatch7Lane4LangfuseListPrompts(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{
					map[string]any{
						"name": "movie-critic", "versions": []any{1, 2, 3},
						"labels": []any{"production"}, "tags": []any{"chat"},
						"lastUpdatedAt": "2025-10-15T10:00:00.000Z",
					},
				},
				"meta": map[string]any{"page": 1, "limit": 50, "totalItems": 1, "totalPages": 1},
			})
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_list_prompts", "label": "production",
	})
	request := recorder.at(t, 0)
	if request.Path != "/api/public/v2/prompts" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("label"); got != "production" {
		t.Fatalf("label = %q", got)
	}
	prompts := batch7Lane4FieldSlice(t, result, "prompts")
	if len(prompts) != 1 || prompts[0].(map[string]any)["name"] != "movie-critic" {
		t.Fatalf("prompts = %v", prompts)
	}
	if versions := batch7Lane4FieldSlice(t, prompts[0].(map[string]any), "versions"); len(versions) != 3 {
		t.Fatalf("versions = %v", versions)
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseGetPrompt::test_successful_get
func TestBatch7Lane4LangfuseGetPrompt(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"name": "movie-critic", "version": 3, "type": "chat",
				"prompt": []any{
					map[string]any{"role": "system", "content": "You are a movie critic"},
					map[string]any{"role": "user", "content": "Review {{movie}}"},
				},
				"config": map[string]any{"temperature": 0.7},
				"labels": []any{"production"}, "tags": []any{"chat"},
			})
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	result := batch7Lane4Execute(t, registry, "langfuse", map[string]any{
		"action": "langfuse_get_prompt", "prompt_name": "movie-critic",
		"version": 3, "label": "production",
	})
	request := recorder.at(t, 0)
	if request.Path != "/api/public/v2/prompts/movie-critic" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("version"); got != "3" {
		t.Fatalf("version = %q", got)
	}
	if result["name"] != "movie-critic" || result["type"] != "chat" {
		t.Fatalf("result = %v", result)
	}
	batch7Lane4EqualInt(t, result["version"], 3)
	if prompt := batch7Lane4FieldSlice(t, result, "prompt"); len(prompt) != 2 {
		t.Fatalf("prompt = %v", prompt)
	}
}

// Parity: tools/tests/tools/test_langfuse_tool.py::TestLangfuseGetTrace::test_missing_id
// Also covers the upstream missing-argument cases for create_score and
// get_prompt.
func TestBatch7Lane4LangfuseValidation(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"langfuse_tool"},
		batch7Lane4EnvOption(batch7Lane4LangfuseEnv()),
	)
	cases := []struct {
		input map[string]any
		want  string
	}{
		{map[string]any{"action": "langfuse_get_trace", "trace_id": ""}, "trace_id is required"},
		{
			map[string]any{"action": "langfuse_create_score", "trace_id": "", "name": "", "value": 0},
			"trace_id and name are required",
		},
		{map[string]any{"action": "langfuse_get_prompt", "prompt_name": ""}, "prompt_name is required"},
	}
	for _, testCase := range cases {
		result := batch7Lane4Execute(t, registry, "langfuse", testCase.input)
		if got := batch7Lane4ErrorText(t, result); got != testCase.want {
			t.Fatalf("%v: error = %q, want %q", testCase.input["action"], got, testCase.want)
		}
	}
}

// Parity-exempt: the upstream suite has no status matrix for Langfuse; the
// strings mirror langfuse_tool._handle_response.
func TestBatch7Lane4LangfuseErrorMapping(t *testing.T) {
	batch7Lane4ErrorTable(
		t, []string{"langfuse_tool"}, "langfuse",
		[]Option{batch7Lane4EnvOption(batch7Lane4LangfuseEnv())},
		map[string]any{"action": "langfuse_list_traces"},
		map[int]string{
			http.StatusUnauthorized:    "Invalid Langfuse API keys",
			http.StatusForbidden:       "Insufficient permissions for this Langfuse resource",
			http.StatusNotFound:        "Langfuse resource not found",
			http.StatusTooManyRequests: "Langfuse rate limit exceeded. Try again later.",
		},
	)
}

// ------------------------------------------------------------ huggingface ---

func batch7Lane4HuggingFaceEnv() map[string]string {
	return map[string]string{"HUGGINGFACE_TOKEN": "hf_test_token"}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceSearchModels::test_missing_token
func TestBatch7Lane4HuggingFaceMissingToken(t *testing.T) {
	registry := newBatch7Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_search_models",
	})
	if got := batch7Lane4ErrorText(t, result); got != "HUGGINGFACE_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceSearchModels::test_successful_search
func TestBatch7Lane4HuggingFaceSearchModels(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"id": "meta-llama/Llama-3-8B", "author": "meta-llama",
					"downloads": 1000000, "likes": 5000,
					"pipeline_tag": "text-generation",
					"tags":         []any{"pytorch", "llama"},
					"lastModified": "2024-06-01T00:00:00Z",
				},
			})
		},
		[]string{"huggingface_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_search_models", "query": "llama",
	})
	request := recorder.at(t, 0)
	if request.Path != "/models" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer hf_test_token" {
		t.Fatalf("Authorization = %q", got)
	}
	if request.Query.Get("search") != "llama" || request.Query.Get("direction") != "-1" {
		t.Fatalf("query = %v", request.Query)
	}
	if got := request.Query.Get("limit"); got != "20" {
		t.Fatalf("limit = %q", got)
	}
	models := batch7Lane4FieldSlice(t, result, "models")
	if len(models) != 1 {
		t.Fatalf("models = %v", models)
	}
	model := models[0].(map[string]any)
	if model["id"] != "meta-llama/Llama-3-8B" {
		t.Fatalf("model = %v", model)
	}
	batch7Lane4EqualInt(t, model["downloads"], 1000000)
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceGetModel::test_successful_get
func TestBatch7Lane4HuggingFaceGetModel(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "meta-llama/Llama-3-8B", "author": "meta-llama",
				"downloads": 1000000, "likes": 5000,
				"pipeline_tag": "text-generation", "tags": []any{"pytorch"},
				"library_name": "transformers", "private": false,
				"lastModified": "2024-06-01T00:00:00Z",
				"createdAt":    "2024-04-01T00:00:00Z",
			})
		},
		[]string{"huggingface_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_get_model", "model_id": "meta-llama/Llama-3-8B",
	})
	if recorder.at(t, 0).Path != "/models/meta-llama/Llama-3-8B" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["library_name"] != "transformers" || result["private"] != false {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceSearchDatasets::test_successful_search
func TestBatch7Lane4HuggingFaceSearchDatasets(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"id": "squad", "author": "rajpurkar", "downloads": 500000,
					"likes": 200, "tags": []any{"question-answering"},
					"lastModified": "2024-01-01T00:00:00Z",
				},
			})
		},
		[]string{"huggingface_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_search_datasets", "query": "squad",
	})
	if recorder.at(t, 0).Path != "/datasets" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	datasets := batch7Lane4FieldSlice(t, result, "datasets")
	if len(datasets) != 1 || datasets[0].(map[string]any)["id"] != "squad" {
		t.Fatalf("datasets = %v", datasets)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceGetDataset::test_successful_get
func TestBatch7Lane4HuggingFaceGetDataset(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "openai/gsm8k", "author": "openai", "downloads": 100000,
				"likes": 300, "tags": []any{"math"}, "private": false,
				"lastModified": "2024-01-01T00:00:00Z",
				"createdAt":    "2023-01-01T00:00:00Z",
			})
		},
		[]string{"huggingface_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_get_dataset", "dataset_id": "openai/gsm8k",
	})
	if recorder.at(t, 0).Path != "/datasets/openai/gsm8k" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["id"] != "openai/gsm8k" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceSearchSpaces::test_successful_search
func TestBatch7Lane4HuggingFaceSearchSpaces(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"id": "gradio/chatbot", "author": "gradio", "likes": 100,
					"sdk": "gradio", "tags": []any{"chatbot"},
					"lastModified": "2024-01-01T00:00:00Z",
				},
			})
		},
		[]string{"huggingface_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_search_spaces", "query": "chatbot",
	})
	request := recorder.at(t, 0)
	if request.Path != "/spaces" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("sort"); got != "likes" {
		t.Fatalf("sort = %q", got)
	}
	spaces := batch7Lane4FieldSlice(t, result, "spaces")
	if len(spaces) != 1 || spaces[0].(map[string]any)["sdk"] != "gradio" {
		t.Fatalf("spaces = %v", spaces)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceWhoami::test_successful_whoami
func TestBatch7Lane4HuggingFaceWhoami(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{
				"name": "testuser", "fullname": "Test User", "email": "test@example.com",
				"avatarUrl": "https://huggingface.co/avatars/test.png",
				"orgs":      []any{map[string]any{"name": "test-org", "roleInOrg": "admin"}},
				"type":      "user",
			})
		},
		[]string{"huggingface_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_whoami",
	})
	if recorder.at(t, 0).Path != "/whoami-v2" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "testuser" {
		t.Fatalf("result = %v", result)
	}
	orgs := batch7Lane4FieldSlice(t, result, "orgs")
	if len(orgs) != 1 || orgs[0].(map[string]any)["role"] != "admin" {
		t.Fatalf("orgs = %v", orgs)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceRunInference::test_successful_inference
func TestBatch7Lane4HuggingFaceRunInference(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, []any{
				map[string]any{"generated_text": "This is a summary of the input text."},
			})
		},
		[]string{"huggingface_tool", "huggingface_inference_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_run_inference", "model_id": "facebook/bart-large-cnn",
		"inputs":     "Long article text here...",
		"parameters": `{"max_new_tokens": 128, "temperature": 0.7}`,
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/facebook/bart-large-cnn" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	parameters := batch7Lane4FieldMap(t, request.Body, "parameters")
	batch7Lane4EqualInt(t, parameters["max_new_tokens"], 128)
	if result["model_id"] != "facebook/bart-large-cnn" || result["task"] != "auto" {
		t.Fatalf("result = %v", result)
	}
	output := batch7Lane4FieldSlice(t, result, "output")
	if output[0].(map[string]any)["generated_text"] != "This is a summary of the input text." {
		t.Fatalf("output = %v", output)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceRunInference::test_model_loading_503
func TestBatch7Lane4HuggingFaceModelLoading(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
				"estimated_time": 30.5,
			})
		},
		[]string{"huggingface_tool", "huggingface_inference_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_run_inference", "model_id": "bigscience/bloom", "inputs": "Hello",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Model is loading" {
		t.Fatalf("error = %q", got)
	}
	if result["estimated_time"] != 30.5 {
		t.Fatalf("estimated_time = %v", result["estimated_time"])
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceRunEmbedding::test_successful_embedding
func TestBatch7Lane4HuggingFaceRunEmbedding(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, []any{0.1, 0.2, 0.3, -0.4, 0.5})
		},
		[]string{"huggingface_tool", "huggingface_inference_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action":   "huggingface_run_embedding",
		"model_id": "sentence-transformers/all-MiniLM-L6-v2",
		"inputs":   "Hello world",
	})
	if result["model_id"] != "sentence-transformers/all-MiniLM-L6-v2" {
		t.Fatalf("result = %v", result)
	}
	if embedding := batch7Lane4FieldSlice(t, result, "embedding"); len(embedding) != 5 {
		t.Fatalf("embedding = %v", embedding)
	}
	batch7Lane4EqualInt(t, result["dimensions"], 5)
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceListInferenceEndpoints::test_successful_list
func TestBatch7Lane4HuggingFaceListInferenceEndpoints(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, []any{
				map[string]any{
					"name": "my-llama-endpoint",
					"model": map[string]any{
						"repository": "meta-llama/Llama-3.1-8B-Instruct",
					},
					"status": map[string]any{
						"state": "running", "url": "https://xyz.endpoints.huggingface.cloud",
					},
					"type":     "protected",
					"provider": map[string]any{"vendor": "aws", "region": "us-east-1"},
				},
			})
		},
		[]string{"huggingface_tool", "huggingface_endpoints_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_list_inference_endpoints",
	})
	if recorder.at(t, 0).Path != "/api/endpoints" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	endpoints := batch7Lane4FieldSlice(t, result, "endpoints")
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %v", endpoints)
	}
	endpoint := endpoints[0].(map[string]any)
	if endpoint["model"] != "meta-llama/Llama-3.1-8B-Instruct" ||
		endpoint["status"] != "running" || endpoint["provider"] != "aws" {
		t.Fatalf("endpoint = %v", endpoint)
	}
}

// Parity-exempt: upstream never asserts the namespace path segment.
func TestBatch7Lane4HuggingFaceEndpointsNamespace(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		},
		[]string{"huggingface_tool", "huggingface_endpoints_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_list_inference_endpoints", "namespace": "acme",
	})
	if recorder.at(t, 0).Path != "/api/endpoints/acme" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	batch7Lane4EqualInt(t, result["count"], 0)
	if endpoints := batch7Lane4FieldSlice(t, result, "endpoints"); len(endpoints) != 0 {
		t.Fatalf("endpoints = %v", endpoints)
	}
}

// Parity: tools/tests/tools/test_huggingface_tool.py::TestHuggingFaceRunInference::test_missing_model_id
// Also covers the upstream missing-argument and bad-parameters cases for
// run_inference and run_embedding.
func TestBatch7Lane4HuggingFaceValidation(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"huggingface_tool", "huggingface_inference_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	cases := []struct {
		input map[string]any
		want  string
	}{
		{
			map[string]any{"action": "huggingface_get_model", "model_id": ""},
			"model_id is required",
		},
		{
			map[string]any{"action": "huggingface_get_dataset", "dataset_id": ""},
			"dataset_id is required",
		},
		{
			map[string]any{"action": "huggingface_run_inference", "model_id": "", "inputs": "Hello"},
			"model_id is required",
		},
		{
			map[string]any{
				"action": "huggingface_run_inference", "model_id": "facebook/bart-large-cnn", "inputs": "",
			},
			"inputs is required",
		},
		{
			map[string]any{
				"action": "huggingface_run_inference", "model_id": "facebook/bart-large-cnn",
				"inputs": "Hello", "parameters": "not valid json",
			},
			"parameters must be a valid JSON string",
		},
		{
			map[string]any{"action": "huggingface_run_embedding", "model_id": "", "inputs": "Hello"},
			"model_id is required",
		},
		{
			map[string]any{
				"action": "huggingface_run_embedding", "model_id": "sentence-transformers/all-MiniLM-L6-v2",
				"inputs": "",
			},
			"inputs is required",
		},
	}
	for _, testCase := range cases {
		result := batch7Lane4Execute(t, registry, "huggingface", testCase.input)
		if got := batch7Lane4ErrorText(t, result); got != testCase.want {
			t.Fatalf("%v: error = %q, want %q", testCase.input["action"], got, testCase.want)
		}
	}
}

// Parity-exempt: the upstream suite has no status matrix for the Hub API; the
// strings mirror huggingface_tool._get.
func TestBatch7Lane4HuggingFaceErrorMapping(t *testing.T) {
	batch7Lane4ErrorTable(
		t, []string{"huggingface_tool"}, "huggingface",
		[]Option{batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv())},
		map[string]any{"action": "huggingface_search_models"},
		map[int]string{
			http.StatusUnauthorized:    "Unauthorized. Check your HUGGINGFACE_TOKEN.",
			http.StatusNotFound:        "Not found: /models",
			http.StatusTooManyRequests: "HuggingFace API error 429: provider failure",
		},
	)
}

// Parity-exempt: the upstream suite has no inference 401/404/429 matrix.
func TestBatch7Lane4HuggingFaceInferenceErrorMapping(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusNotFound)
		},
		[]string{"huggingface_tool", "huggingface_inference_tool"},
		batch7Lane4EnvOption(batch7Lane4HuggingFaceEnv()),
	)
	result := batch7Lane4Execute(t, registry, "huggingface", map[string]any{
		"action": "huggingface_run_inference", "model_id": "acme/missing", "inputs": "Hello",
	})
	text := batch7Lane4ErrorText(t, result)
	if !strings.HasPrefix(text, "Model not found: ") || !strings.HasSuffix(text, "/acme/missing") {
		t.Fatalf("error = %q", text)
	}
}

// ----------------------------------------------------------------- vision ---

func batch7Lane4VisionEnv() map[string]string {
	return map[string]string{"GOOGLE_CLOUD_VISION_API_KEY": "test-api-key"}
}

// batch7Lane4VisionResponse wraps one annotate result the way the Vision API
// does.
func batch7Lane4VisionResponse(result map[string]any) map[string]any {
	return map[string]any{"responses": []any{result}}
}

// batch7Lane4VisionFeatures reads the features array of the first request.
func batch7Lane4VisionFeatures(t *testing.T, request batch7Lane4Request) []any {
	t.Helper()
	requests := batch7Lane4FieldSlice(t, request.Body, "requests")
	if len(requests) != 1 {
		t.Fatalf("requests = %v", requests)
	}
	first := requests[0].(map[string]any)
	return batch7Lane4FieldSlice(t, first, "features")
}

// Parity: tools/tests/tools/test_vision_tool.py::test_missing_credentials
func TestBatch7Lane4VisionMissingKey(t *testing.T) {
	registry := newBatch7Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_labels", "image_source": "https://example.com/image.jpg",
	})
	if got := batch7Lane4ErrorText(t, result); got != "GOOGLE_CLOUD_VISION_API_KEY not configured" {
		t.Fatalf("error = %q", got)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "console.cloud.google.com") {
		t.Fatalf("help = %v", result["help"])
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_detect_labels_success
func TestBatch7Lane4VisionDetectLabels(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"labelAnnotations": []any{
					map[string]any{"description": "Dog", "score": 0.97},
					map[string]any{"description": "Animal", "score": 0.95},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_labels", "image_source": "https://example.com/dog.jpg",
		"max_labels": 5,
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %s", request.Method)
	}
	if got := request.Query.Get("key"); got != "test-api-key" {
		t.Fatalf("key = %q", got)
	}
	features := batch7Lane4VisionFeatures(t, request)
	feature := features[0].(map[string]any)
	if feature["type"] != "LABEL_DETECTION" {
		t.Fatalf("feature = %v", feature)
	}
	batch7Lane4EqualInt(t, feature["maxResults"], 5)
	image := batch7Lane4FieldMap(t, request.Body["requests"].([]any)[0].(map[string]any), "image")
	source := batch7Lane4FieldMap(t, image, "source")
	if source["imageUri"] != "https://example.com/dog.jpg" {
		t.Fatalf("source = %v", source)
	}
	labels := batch7Lane4FieldSlice(t, result, "labels")
	if len(labels) != 2 || labels[0].(map[string]any)["description"] != "Dog" {
		t.Fatalf("labels = %v", labels)
	}
	if labels[0].(map[string]any)["score"] != 0.97 {
		t.Fatalf("score = %v", labels[0].(map[string]any)["score"])
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_detect_text_success
func TestBatch7Lane4VisionDetectText(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"textAnnotations": []any{
					map[string]any{"description": "Hello World\nLine 2"},
					map[string]any{
						"description":  "Hello",
						"boundingPoly": map[string]any{"vertices": []any{map[string]any{"x": 0, "y": 0}}},
					},
					map[string]any{
						"description":  "World",
						"boundingPoly": map[string]any{"vertices": []any{map[string]any{"x": 50, "y": 0}}},
					},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_text", "image_source": "https://example.com/text.jpg",
	})
	if got := batch7Lane4VisionFeatures(t, recorder.at(t, 0))[0].(map[string]any)["type"]; got != "TEXT_DETECTION" {
		t.Fatalf("feature type = %v", got)
	}
	if result["text"] != "Hello World\nLine 2" {
		t.Fatalf("text = %v", result["text"])
	}
	blocks := batch7Lane4FieldSlice(t, result, "blocks")
	if len(blocks) != 2 {
		t.Fatalf("blocks = %v", blocks)
	}
	if bounds := batch7Lane4FieldSlice(t, blocks[0].(map[string]any), "bounds"); len(bounds) != 1 {
		t.Fatalf("bounds = %v", bounds)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_detect_text_no_text_found
func TestBatch7Lane4VisionDetectTextEmpty(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"textAnnotations": []any{},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_text", "image_source": "https://example.com/image.jpg",
	})
	if result["text"] != "" {
		t.Fatalf("text = %v", result["text"])
	}
	if blocks := batch7Lane4FieldSlice(t, result, "blocks"); len(blocks) != 0 {
		t.Fatalf("blocks = %v", blocks)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_detect_faces_success
func TestBatch7Lane4VisionDetectFaces(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"faceAnnotations": []any{
					map[string]any{
						"joyLikelihood":       "VERY_LIKELY",
						"sorrowLikelihood":    "VERY_UNLIKELY",
						"angerLikelihood":     "VERY_UNLIKELY",
						"surpriseLikelihood":  "UNLIKELY",
						"detectionConfidence": 0.98,
						"boundingPoly": map[string]any{
							"vertices": []any{map[string]any{"x": 10, "y": 10}},
						},
					},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_faces", "image_source": "https://example.com/face.jpg",
		"max_faces": 10,
	})
	features := batch7Lane4VisionFeatures(t, recorder.at(t, 0))
	if features[0].(map[string]any)["type"] != "FACE_DETECTION" {
		t.Fatalf("features = %v", features)
	}
	faces := batch7Lane4FieldSlice(t, result, "faces")
	if len(faces) != 1 {
		t.Fatalf("faces = %v", faces)
	}
	face := faces[0].(map[string]any)
	if face["joy"] != "VERY_LIKELY" || face["confidence"] != 0.98 {
		t.Fatalf("face = %v", face)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_localize_objects_success
func TestBatch7Lane4VisionLocalizeObjects(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"localizedObjectAnnotations": []any{
					map[string]any{
						"name": "Cat", "score": 0.92,
						"boundingPoly": map[string]any{
							"normalizedVertices": []any{
								map[string]any{"x": 0.1, "y": 0.2},
								map[string]any{"x": 0.9, "y": 0.8},
							},
						},
					},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_localize_objects", "image_source": "https://example.com/cat.jpg",
	})
	features := batch7Lane4VisionFeatures(t, recorder.at(t, 0))
	if features[0].(map[string]any)["type"] != "OBJECT_LOCALIZATION" {
		t.Fatalf("features = %v", features)
	}
	objects := batch7Lane4FieldSlice(t, result, "objects")
	if len(objects) != 1 || objects[0].(map[string]any)["name"] != "Cat" {
		t.Fatalf("objects = %v", objects)
	}
	if bounds := batch7Lane4FieldSlice(t, objects[0].(map[string]any), "bounds"); len(bounds) != 2 {
		t.Fatalf("bounds = %v", bounds)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_detect_logos_success
func TestBatch7Lane4VisionDetectLogos(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"logoAnnotations": []any{
					map[string]any{"description": "Apple", "score": 0.95},
					map[string]any{"description": "Nike", "score": 0.88},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_logos", "image_source": "https://example.com/logos.jpg",
	})
	features := batch7Lane4VisionFeatures(t, recorder.at(t, 0))
	feature := features[0].(map[string]any)
	if feature["type"] != "LOGO_DETECTION" {
		t.Fatalf("feature = %v", feature)
	}
	batch7Lane4EqualInt(t, feature["maxResults"], 5)
	logos := batch7Lane4FieldSlice(t, result, "logos")
	if len(logos) != 2 || logos[0].(map[string]any)["description"] != "Apple" {
		t.Fatalf("logos = %v", logos)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_detect_landmarks_success
func TestBatch7Lane4VisionDetectLandmarks(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"landmarkAnnotations": []any{
					map[string]any{
						"description": "Eiffel Tower", "score": 0.96,
						"locations": []any{
							map[string]any{
								"latLng": map[string]any{"latitude": 48.8584, "longitude": 2.2945},
							},
						},
					},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_landmarks", "image_source": "https://example.com/paris.jpg",
	})
	landmarks := batch7Lane4FieldSlice(t, result, "landmarks")
	if len(landmarks) != 1 || landmarks[0].(map[string]any)["description"] != "Eiffel Tower" {
		t.Fatalf("landmarks = %v", landmarks)
	}
	location := batch7Lane4FieldMap(t, landmarks[0].(map[string]any), "location")
	if location["latitude"] != 48.8584 {
		t.Fatalf("location = %v", location)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_image_properties_success
func TestBatch7Lane4VisionImageProperties(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"imagePropertiesAnnotation": map[string]any{
					"dominantColors": map[string]any{
						"colors": []any{
							map[string]any{
								"color":         map[string]any{"red": 255, "green": 0, "blue": 0},
								"score":         0.5,
								"pixelFraction": 0.3,
							},
						},
					},
				},
				"cropHintsAnnotation": map[string]any{
					"cropHints": []any{
						map[string]any{"boundingPoly": map[string]any{"vertices": []any{}}, "confidence": 0.8},
					},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_image_properties", "image_source": "https://example.com/colorful.jpg",
	})
	features := batch7Lane4VisionFeatures(t, recorder.at(t, 0))
	if len(features) != 2 {
		t.Fatalf("features = %v", features)
	}
	colors := batch7Lane4FieldSlice(t, result, "colors")
	if len(colors) != 1 {
		t.Fatalf("colors = %v", colors)
	}
	batch7Lane4EqualInt(t, colors[0].(map[string]any)["red"], 255)
	if hints := batch7Lane4FieldSlice(t, result, "crop_hints"); len(hints) != 1 {
		t.Fatalf("crop_hints = %v", hints)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_web_detection_success
func TestBatch7Lane4VisionWebDetection(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"webDetection": map[string]any{
					"webEntities": []any{
						map[string]any{"description": "Sunset", "score": 0.9},
					},
					"visuallySimilarImages": []any{
						map[string]any{"url": "https://similar.com/1.jpg"},
					},
					"pagesWithMatchingImages": []any{
						map[string]any{"url": "https://page.com", "pageTitle": "Sunset Photos"},
					},
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_web_detection", "image_source": "https://example.com/sunset.jpg",
	})
	entities := batch7Lane4FieldSlice(t, result, "web_entities")
	if len(entities) != 1 || entities[0].(map[string]any)["description"] != "Sunset" {
		t.Fatalf("web_entities = %v", entities)
	}
	images := batch7Lane4FieldSlice(t, result, "similar_images")
	if len(images) != 1 || images[0] != "https://similar.com/1.jpg" {
		t.Fatalf("similar_images = %v", images)
	}
	pages := batch7Lane4FieldSlice(t, result, "pages_with_image")
	if len(pages) != 1 || pages[0].(map[string]any)["title"] != "Sunset Photos" {
		t.Fatalf("pages = %v", pages)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_safe_search_success
func TestBatch7Lane4VisionSafeSearch(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"safeSearchAnnotation": map[string]any{
					"adult": "VERY_UNLIKELY", "spoof": "UNLIKELY",
					"medical": "VERY_UNLIKELY", "violence": "VERY_UNLIKELY",
					"racy": "POSSIBLE",
				},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_safe_search", "image_source": "https://example.com/photo.jpg",
	})
	if result["adult"] != "VERY_UNLIKELY" || result["violence"] != "VERY_UNLIKELY" ||
		result["racy"] != "POSSIBLE" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_local_file_success
func TestBatch7Lane4VisionLocalFile(t *testing.T) {
	dir := t.TempDir()
	content := []byte("not-a-real-png-but-base64-round-trips")
	path := filepath.Join(dir, "sample.png")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"labelAnnotations": []any{map[string]any{"description": "Image", "score": 0.9}},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
		WithRoots(dir),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_labels", "image_source": path,
	})
	if got := batch7Lane4FieldSlice(t, result, "labels"); len(got) != 1 {
		t.Fatalf("labels = %v", got)
	}
	requests := batch7Lane4FieldSlice(t, recorder.at(t, 0).Body, "requests")
	image := batch7Lane4FieldMap(t, requests[0].(map[string]any), "image")
	if image["content"] != base64.StdEncoding.EncodeToString(content) {
		t.Fatalf("content mismatch")
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_file_not_found
// Also covers the upstream directory and 10MB-limit cases.
func TestBatch7Lane4VisionFileErrors(t *testing.T) {
	dir := t.TempDir()
	large := filepath.Join(dir, "large.png")
	file, err := os.Create(large)
	if err != nil {
		t.Fatalf("create large file: %v", err)
	}
	if err := file.Truncate(int64(batch7Lane4VisionMaxBytes) + 1024*1024); err != nil {
		t.Fatalf("truncate large file: %v", err)
	}
	_ = file.Close()
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
		WithRoots(dir),
	)
	cases := []struct {
		source string
		want   string
	}{
		{filepath.Join(dir, "missing.png"), "File not found: " + filepath.Join(dir, "missing.png")},
		{dir, "Not a file: " + dir},
		{large, "File exceeds 10MB limit (11.0MB)"},
	}
	for _, testCase := range cases {
		result := batch7Lane4Execute(t, registry, "vision", map[string]any{
			"action": "vision_detect_labels", "image_source": testCase.source,
		})
		if got := batch7Lane4ErrorText(t, result); got != testCase.want {
			t.Fatalf("source %s: error = %q, want %q", testCase.source, got, testCase.want)
		}
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_api_error_401
// Also covers the upstream 403 and 429 cases (plus 400).
func TestBatch7Lane4VisionErrorMapping(t *testing.T) {
	batch7Lane4ErrorTable(
		t, []string{"vision_tool"}, "vision",
		[]Option{batch7Lane4EnvOption(batch7Lane4VisionEnv())},
		map[string]any{
			"action": "vision_detect_labels", "image_source": "https://example.com/image.jpg",
		},
		map[int]string{
			http.StatusBadRequest:          "Invalid request. Check image format and size.",
			http.StatusUnauthorized:        "Invalid API key",
			http.StatusForbidden:           "API key not authorized. Enable Vision API in Google Cloud Console.",
			http.StatusTooManyRequests:     "Rate limit exceeded. Try again later.",
			http.StatusNotFound:            "Vision API error (HTTP 404)",
			http.StatusInternalServerError: "Vision API error (HTTP 500)",
		},
	)
}

// Parity: tools/tests/tools/test_vision_tool.py::test_empty_response
func TestBatch7Lane4VisionEmptyResponse(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, map[string]any{"responses": []any{}})
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_labels", "image_source": "https://example.com/image.jpg",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Empty response from API" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_api_error_in_response
func TestBatch7Lane4VisionBodyError(t *testing.T) {
	registry, _, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"error": map[string]any{"message": "Image too small"},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	result := batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_labels", "image_source": "https://example.com/image.jpg",
	})
	if got := batch7Lane4ErrorText(t, result); got != "Image too small" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_vision_tool.py::test_max_labels_clamped
func TestBatch7Lane4VisionClamp(t *testing.T) {
	registry, recorder, _ := batch7Lane4Fake(
		t,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane4WriteJSON(w, http.StatusOK, batch7Lane4VisionResponse(map[string]any{
				"labelAnnotations": []any{},
			}))
		},
		[]string{"vision_tool"},
		batch7Lane4EnvOption(batch7Lane4VisionEnv()),
	)
	_ = batch7Lane4Execute(t, registry, "vision", map[string]any{
		"action": "vision_detect_labels", "image_source": "https://example.com/image.jpg",
		"max_labels": 200,
	})
	feature := batch7Lane4VisionFeatures(t, recorder.at(t, 0))[0].(map[string]any)
	batch7Lane4EqualInt(t, feature["maxResults"], 100)
}
