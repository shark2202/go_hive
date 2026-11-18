package integrations

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// batch7Lane9CloudinaryEnv is the three-field credential set used by the tests.
func batch7Lane9CloudinaryEnv() map[string]string {
	return map[string]string{
		"CLOUDINARY_CLOUD_NAME": "test-cloud",
		"CLOUDINARY_API_KEY":    "test-key",
		"CLOUDINARY_API_SECRET": "test-secret",
	}
}

// batch7Lane9CloudinaryFake serves the Cloudinary module and hands the parsed
// form (or raw JSON body) to the handler, since the shared recorder only keeps
// JSON objects.
func batch7Lane9CloudinaryFake(t *testing.T, env map[string]string, handler func(http.ResponseWriter, *http.Request, url.Values, []byte)) *Registry {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		handler(w, r, form, raw)
	}))
	t.Cleanup(server.Close)
	return newBatch7Lane9Registry(
		WithBaseURL("cloudinary_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryUpload::test_successful_upload
func TestBatch7Lane9CloudinaryUpload(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.Method != http.MethodPost || r.URL.Path != "/test-cloud/image/upload" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("test-key:test-secret"))
			if got := r.Header.Get("Authorization"); got != wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, wantAuth)
			}
			if form.Get("file") != "https://example.com/img.jpg" {
				t.Fatalf("file = %q", form.Get("file"))
			}
			if form.Get("public_id") != "sample" || form.Get("folder") != "media" || form.Get("tags") != "nature,trees" {
				t.Fatalf("form = %v", form)
			}
			if form.Get("timestamp") != "1700000000" || form.Get("api_key") != "test-key" {
				t.Fatalf("form = %v", form)
			}
			// The signature covers the signed params + secret, sorted by name.
			digest := sha1.Sum([]byte("folder=media&public_id=sample&tags=nature,trees&timestamp=1700000000test-secret"))
			if want := hex.EncodeToString(digest[:]); form.Get("signature") != want {
				t.Fatalf("signature = %q, want %q", form.Get("signature"), want)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"public_id": "sample", "secure_url": "https://res.cloudinary.com/test-cloud/image/upload/sample.jpg",
				"format": "jpg", "resource_type": "image", "bytes": 12345,
				"width": 800, "height": 600, "created_at": "2024-01-01T00:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_upload", "file_url": "https://example.com/img.jpg",
		"public_id": "sample", "folder": "media", "tags": "nature,trees", "resource_type": "image",
	})
	if result["public_id"] != "sample" || result["format"] != "jpg" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane9Numeric(t, result["bytes"]) != 12345 || batch7Lane9Numeric(t, result["width"]) != 800 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryUpload::test_missing_file_url
func TestBatch7Lane9CloudinaryUploadValidation(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{"action": "cloudinary_upload"})
	if got := batch4ErrorText(t, result); got != "file_url is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryUpload::test_missing_credentials
func TestBatch7Lane9CloudinaryMissingCredentials(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_upload", "file_url": "https://example.com/img.jpg",
	})
	want := "CLOUDINARY_CLOUD_NAME, CLOUDINARY_API_KEY, and CLOUDINARY_API_SECRET not set"
	if got := batch4ErrorText(t, result); got != want {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryListResources::test_successful_list
func TestBatch7Lane9CloudinaryListResources(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.Method != http.MethodGet || r.URL.Path != "/test-cloud/resources/image" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if r.URL.Query().Get("max_results") != "500" {
				t.Fatalf("max_results = %q (want clamp to 500)", r.URL.Query().Get("max_results"))
			}
			if r.URL.Query().Get("prefix") != "nature" {
				t.Fatalf("prefix = %q", r.URL.Query().Get("prefix"))
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"resources": []any{map[string]any{
					"public_id": "nature/sample1", "secure_url": "https://res.cloudinary.com/test-cloud/image/upload/sample1.jpg",
					"format": "jpg", "bytes": 5000, "width": 400, "height": 300, "created_at": "2024-01-01T00:00:00Z",
				}},
			})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_list_resources", "max_results": 900, "prefix": "nature",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	first := batch4BodyMap(t, batch4BodySlice(t, result["resources"])[0])
	if first["public_id"] != "nature/sample1" || batch7Lane9Numeric(t, first["bytes"]) != 5000 {
		t.Fatalf("resource = %v", first)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryGetResource::test_successful_get
func TestBatch7Lane9CloudinaryGetResource(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.URL.Path != "/test-cloud/resources/image/upload/nature/sample1" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"public_id": "nature/sample1", "secure_url": "https://res.cloudinary.com/x.jpg",
				"format": "jpg", "resource_type": "image", "bytes": 5000,
				"width": 400, "height": 300, "tags": []any{"nature"}, "status": "active",
				"created_at": "2024-01-01T00:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_get_resource", "public_id": "nature/sample1",
	})
	if result["public_id"] != "nature/sample1" || result["status"] != "active" {
		t.Fatalf("result = %v", result)
	}
	tags := batch4BodySlice(t, result["tags"])
	if len(tags) != 1 || tags[0] != "nature" {
		t.Fatalf("tags = %v", tags)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryGetResource::test_missing_public_id
func TestBatch7Lane9CloudinaryMissingPublicID(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			t.Fatal("unexpected request")
		})
	getResult := batch4Execute(t, registry, "cloudinary", map[string]any{"action": "cloudinary_get_resource"})
	if got := batch4ErrorText(t, getResult); got != "public_id is required" {
		t.Fatalf("error = %q", got)
	}
	deleteResult := batch4Execute(t, registry, "cloudinary", map[string]any{"action": "cloudinary_delete_resource"})
	if got := batch4ErrorText(t, deleteResult); got != "public_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinaryDeleteResource::test_successful_delete
func TestBatch7Lane9CloudinaryDeleteResource(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.Method != http.MethodPost || r.URL.Path != "/test-cloud/image/destroy" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if form.Get("public_id") != "sample1" {
				t.Fatalf("form = %v", form)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"result": "ok"})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_delete_resource", "public_id": "sample1",
	})
	if result["result"] != "ok" || result["public_id"] != "sample1" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_cloudinary_tool.py::TestCloudinarySearch::test_successful_search
func TestBatch7Lane9CloudinarySearch(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.Method != http.MethodPost || r.URL.Path != "/test-cloud/resources/search" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q", got)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("body = %s", raw)
			}
			if body["expression"] != "resource_type:image AND tags=nature" || body["max_results"] != float64(30) {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"total_count": 1,
				"resources": []any{map[string]any{
					"public_id": "nature/sunset", "secure_url": "https://res.cloudinary.com/x.jpg",
					"format": "jpg", "resource_type": "image", "bytes": 8000, "created_at": "2024-01-01T00:00:00Z",
				}},
			})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_search", "expression": "resource_type:image AND tags=nature",
	})
	if batch7Lane9Numeric(t, result["total_count"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	first := batch4BodyMap(t, batch4BodySlice(t, result["resources"])[0])
	if first["public_id"] != "nature/sunset" || first["resource_type"] != "image" {
		t.Fatalf("resource = %v", first)
	}
}

// Parity-exempt: upstream has no usage test; the nested usage shape is pinned.
func TestBatch7Lane9CloudinaryUsage(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.URL.Path != "/test-cloud/usage" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"plan":              "free",
				"storage":           map[string]any{"usage": 1000, "limit": 25000, "used_percent": 4},
				"bandwidth":         map[string]any{"usage": 500, "limit": 25000, "used_percent": 2},
				"transformations":   map[string]any{"usage": 10, "limit": 1000, "used_percent": 1},
				"resources":         7,
				"derived_resources": 3,
				"last_updated":      "2024-01-01T00:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{"action": "cloudinary_get_usage"})
	if result["plan"] != "free" || batch7Lane9Numeric(t, result["resources"]) != 7 {
		t.Fatalf("result = %v", result)
	}
	storage := batch4BodyMap(t, result["storage"])
	if batch7Lane9Numeric(t, storage["used_bytes"]) != 1000 || batch7Lane9Numeric(t, storage["limit_bytes"]) != 25000 {
		t.Fatalf("storage = %v", storage)
	}
	transformations := batch4BodyMap(t, result["transformations"])
	if batch7Lane9Numeric(t, transformations["used_percent"]) != 1 {
		t.Fatalf("transformations = %v", transformations)
	}
}

// Parity-exempt: upstream has no rename test; the form fields are pinned.
func TestBatch7Lane9CloudinaryRename(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.URL.Path != "/test-cloud/video/rename" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if form.Get("from_public_id") != "old" || form.Get("to_public_id") != "new" {
				t.Fatalf("form = %v", form)
			}
			if form.Get("overwrite") != "true" {
				t.Fatalf("form = %v", form)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"public_id": "new", "secure_url": "https://res.cloudinary.com/new.mp4", "format": "mp4",
			})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_rename_resource", "from_public_id": "old", "to_public_id": "new",
		"resource_type": "video", "overwrite": true,
	})
	if result["public_id"] != "new" || result["status"] != "renamed" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream has no add-tag test; the JSON body is pinned.
func TestBatch7Lane9CloudinaryAddTag(t *testing.T) {
	registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
		func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
			if r.URL.Path != "/test-cloud/image/tags" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("body = %s", raw)
			}
			if body["tag"] != "nature" || body["command"] != "add" {
				t.Fatalf("body = %v", body)
			}
			ids := batch4BodySlice(t, body["public_ids"])
			if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
				t.Fatalf("public_ids = %v", ids)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"public_ids": []any{"a", "b"}})
		})
	result := batch4Execute(t, registry, "cloudinary", map[string]any{
		"action": "cloudinary_add_tag", "tag": "nature", "public_ids": "a, b",
	})
	if result["status"] != "tagged" {
		t.Fatalf("result = %v", result)
	}
	if ids := batch4BodySlice(t, result["public_ids"]); len(ids) != 2 {
		t.Fatalf("public_ids = %v", result["public_ids"])
	}
}

// Parity-exempt: provider statuses are not exercised upstream; this table pins
// the Cloudinary error vocabulary.
func TestBatch7Lane9CloudinaryStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Unauthorized. Check your Cloudinary credentials."},
		{http.StatusNotFound, "Cloudinary API error 404: missing"},
		{http.StatusTooManyRequests, "Cloudinary API error 429: slow down"},
	}
	bodies := map[int]string{http.StatusNotFound: "missing", http.StatusTooManyRequests: "slow down"}
	for _, testCase := range cases {
		registry := batch7Lane9CloudinaryFake(t, batch7Lane9CloudinaryEnv(),
			func(w http.ResponseWriter, r *http.Request, form url.Values, raw []byte) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(bodies[testCase.status]))
			})
		result := batch4Execute(t, registry, "cloudinary", map[string]any{"action": "cloudinary_get_usage"})
		if got := batch4ErrorText(t, result); got != testCase.want {
			t.Fatalf("status %d: error = %q, want %q", testCase.status, got, testCase.want)
		}
	}
}

// Parity-exempt: the signature helper is internal; its invariants (order
// independence, secret sensitivity, empty-value skipping) are Go-side.
func TestBatch7Lane9CloudinarySignature(t *testing.T) {
	first := batch7Lane9CloudinarySignature(map[string]string{
		"public_id": "sample", "timestamp": "1700000000", "folder": "media",
	}, "secret")
	second := batch7Lane9CloudinarySignature(map[string]string{
		"folder": "media", "timestamp": "1700000000", "public_id": "sample",
	}, "secret")
	if first != second {
		t.Fatalf("signature depends on insertion order: %q != %q", first, second)
	}
	if len(first) != 40 || strings.ContainsAny(first, "GHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("signature = %q, want 40 lowercase hex chars", first)
	}
	if other := batch7Lane9CloudinarySignature(map[string]string{
		"public_id": "sample", "timestamp": "1700000000", "folder": "media",
	}, "other-secret"); other == first {
		t.Fatalf("signature ignores the secret: %q", first)
	}
	withEmpty := batch7Lane9CloudinarySignature(map[string]string{
		"public_id": "sample", "timestamp": "1700000000", "folder": "media", "tags": "",
	}, "secret")
	if withEmpty != first {
		t.Fatalf("empty param changed the signature: %q != %q", withEmpty, first)
	}
	digest := sha1.Sum([]byte("folder=media&public_id=sample&timestamp=1700000000secret"))
	if want := hex.EncodeToString(digest[:]); first != want {
		t.Fatalf("signature = %q, want %q", first, want)
	}
}
