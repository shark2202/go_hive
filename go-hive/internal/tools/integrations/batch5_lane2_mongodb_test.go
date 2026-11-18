package integrations

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// ---------------------------------------------------------------- test kit ---

type batch5HTTPRecord struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
	Raw    string
}

type batch5HTTPRecorder struct {
	mu      sync.Mutex
	records []batch5HTTPRecord
}

func (rec *batch5HTTPRecorder) add(request *http.Request, body []byte) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var decoded map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &decoded)
	}
	rec.records = append(rec.records, batch5HTTPRecord{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  request.URL.Query(),
		Header: request.Header.Clone(),
		Body:   decoded,
		Raw:    string(body),
	})
}

func (rec *batch5HTTPRecorder) all() []batch5HTTPRecord {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	out := make([]batch5HTTPRecord, len(rec.records))
	copy(out, rec.records)
	return out
}

func (rec *batch5HTTPRecorder) last() batch5HTTPRecord {
	records := rec.all()
	if len(records) == 0 {
		return batch5HTTPRecord{}
	}
	return records[len(records)-1]
}

func batch5JSONServer(t *testing.T, handler func(request *http.Request, record batch5HTTPRecord) (int, string)) (*httptest.Server, *batch5HTTPRecorder) {
	t.Helper()
	recorder := &batch5HTTPRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		recorder.add(r, raw)
		status, body := handler(r, recorder.last())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func batch5MongoRegistry(t *testing.T, baseURL, key string) *Registry {
	t.Helper()
	options := []Option{WithBaseURL("mongodb_tool", baseURL)}
	if key != "" {
		options = append(options, WithCredential("mongodb_tool", key))
	}
	options = append(options, WithGetenv(func(name string) string {
		if name == "MONGODB_DATA_SOURCE" {
			return "Cluster0"
		}
		return ""
	}))
	return newBatch5Lane2Registry(options...)
}

// ------------------------------------------------------------------- tests ---

// Parity: tools/tests/tools/test_mongodb_tool.py::test_successful_find
func TestBatch5Lane2MongoFind(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(request *http.Request, record batch5HTTPRecord) (int, string) {
		if record.Path != "/action/find" {
			return http.StatusNotFound, `{"error":"not found"}`
		}
		return http.StatusOK, `{"documents":[{"_id":"1","name":"Alice"},{"_id":"2","name":"Bob"}]}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action":     "mongodb_find",
		"database":   "app",
		"collection": "users",
		"filter":     `{"status":"active"}`,
		"projection": `{"name":1,"_id":0}`,
		"sort":       `{"created":-1}`,
		"limit":      5,
	})
	batch5AssertNoError(t, result)
	if result["count"] != 2 {
		t.Fatalf("count = %v", result["count"])
	}
	record := recorder.last()
	if record.Method != http.MethodPost || record.Path != "/action/find" {
		t.Fatalf("request = %s %s", record.Method, record.Path)
	}
	if record.Header.Get("api-key") != "test-key" {
		t.Fatalf("api-key = %q", record.Header.Get("api-key"))
	}
	if record.Body["dataSource"] != "Cluster0" || record.Body["database"] != "app" || record.Body["collection"] != "users" {
		t.Fatalf("body = %v", record.Body)
	}
	if record.Body["limit"] != float64(5) {
		t.Fatalf("limit = %v", record.Body["limit"])
	}
	filter, _ := record.Body["filter"].(map[string]any)
	if filter["status"] != "active" {
		t.Fatalf("filter = %v", record.Body["filter"])
	}
	projection, _ := record.Body["projection"].(map[string]any)
	if projection["name"] != float64(1) {
		t.Fatalf("projection = %v", record.Body["projection"])
	}
	sortSpec, _ := record.Body["sort"].(map[string]any)
	if sortSpec["created"] != float64(-1) {
		t.Fatalf("sort = %v", record.Body["sort"])
	}
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_missing_credentials
func TestBatch5Lane2MongoMissingCredentials(t *testing.T) {
	registry := newBatch5Lane2Registry(WithGetenv(func(string) string { return "" }))
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_find", "database": "app", "collection": "users",
	})
	batch5AssertErrorContains(t, result, "MONGODB_DATA_API_URL and MONGODB_API_KEY are required")
	if help, _ := result["help"].(string); help == "" {
		t.Fatalf("help missing: %v", result)
	}
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_invalid_filter_json
func TestBatch5Lane2MongoValidation(t *testing.T) {
	server, _ := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"documents":[]}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_find", "database": "app", "collection": "users", "filter": "{bad",
	})
	batch5AssertErrorContains(t, result, "filter must be valid JSON")
	result = batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_aggregate", "database": "app", "collection": "users", "pipeline": `{"$match":{}}`,
	})
	batch5AssertErrorContains(t, result, "pipeline must be a JSON array")
	result = batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_insert_one", "database": "app", "collection": "users",
	})
	batch5AssertErrorContains(t, result, "document is required")
	result = batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_delete_one", "database": "app", "collection": "users",
	})
	batch5AssertErrorContains(t, result, "filter is required")
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_successful_find_one
func TestBatch5Lane2MongoFindOne(t *testing.T) {
	found := true
	server, _ := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		if found {
			return http.StatusOK, `{"document":{"_id":"1","name":"Alice"}}`
		}
		return http.StatusOK, `{"document":null}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_find_one", "database": "app", "collection": "users", "filter": `{"_id":"1"}`,
	})
	batch5AssertNoError(t, result)
	if result["name"] != "Alice" {
		t.Fatalf("document = %v", result)
	}
	found = false
	missing := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_find_one", "database": "app", "collection": "users",
	})
	batch5AssertErrorContains(t, missing, "no document found matching filter")
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_successful_insert
func TestBatch5Lane2MongoInsertOne(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"insertedId":"abc123"}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_insert_one", "database": "app", "collection": "users",
		"document": `{"name":"Alice","age":30}`,
	})
	batch5AssertNoError(t, result)
	if result["result"] != "inserted" || result["insertedId"] != "abc123" {
		t.Fatalf("result = %v", result)
	}
	document, _ := recorder.last().Body["document"].(map[string]any)
	if document["name"] != "Alice" || document["age"] != float64(30) {
		t.Fatalf("document = %v", recorder.last().Body["document"])
	}
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_successful_update
func TestBatch5Lane2MongoUpdateOne(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"matchedCount":1,"modifiedCount":1,"upsertedId":"new1"}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_update_one", "database": "app", "collection": "users",
		"filter": `{"_id":"1"}`, "update": `{"$set":{"status":"active"}}`, "upsert": true,
	})
	batch5AssertNoError(t, result)
	if result["matchedCount"] != 1 || result["modifiedCount"] != 1 || result["upsertedId"] != "new1" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last().Body
	if body["upsert"] != true {
		t.Fatalf("upsert = %v", body["upsert"])
	}
	update, _ := body["update"].(map[string]any)
	setClause, _ := update["$set"].(map[string]any)
	if setClause["status"] != "active" {
		t.Fatalf("update = %v", body["update"])
	}
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_successful_delete
func TestBatch5Lane2MongoDeleteOne(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"deletedCount":1}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_delete_one", "database": "app", "collection": "users", "filter": `{"_id":"1"}`,
	})
	batch5AssertNoError(t, result)
	if result["deletedCount"] != 1 {
		t.Fatalf("result = %v", result)
	}
	if recorder.last().Path != "/action/deleteOne" {
		t.Fatalf("path = %s", recorder.last().Path)
	}
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_successful_aggregate
func TestBatch5Lane2MongoAggregate(t *testing.T) {
	server, recorder := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusOK, `{"documents":[{"_id":"active","total":3}]}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_aggregate", "database": "app", "collection": "users",
		"pipeline": `[{"$match":{"status":"active"}},{"$count":"total"}]`,
	})
	batch5AssertNoError(t, result)
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	pipeline, _ := recorder.last().Body["pipeline"].([]any)
	if len(pipeline) != 2 {
		t.Fatalf("pipeline = %v", recorder.last().Body["pipeline"])
	}
}

// Parity: tools/tests/tools/test_mongodb_tool.py::test_invalid_pipeline
func TestBatch5Lane2MongoHTTPError(t *testing.T) {
	server, _ := batch5JSONServer(t, func(*http.Request, batch5HTTPRecord) (int, string) {
		return http.StatusBadRequest, `{"error":"bad request detail"}`
	})
	registry := batch5MongoRegistry(t, server.URL, "test-key")
	result := batch5ExecuteRegistry(t, registry, "mongodb", map[string]any{
		"action": "mongodb_find", "database": "app", "collection": "users",
	})
	batch5AssertErrorContains(t, result, "HTTP 400: ")
	batch5AssertErrorContains(t, result, "bad request detail")
}
