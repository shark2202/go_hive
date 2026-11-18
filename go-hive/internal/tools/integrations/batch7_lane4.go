// Batch 7 lane 4 of the integration port: the AI-platform group — Pinecone
// (9 actions), Weights & Biases (6), Langfuse (6), Hugging Face (9) and Google
// Cloud Vision (9), 39 actions total.
//
// Upstream reference:
// tools/src/aden_tools/tools/{pinecone,wandb,langfuse,huggingface,vision}_tool.
// Action names, parameter defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Credentials resolve from WithCredential (single-token modules) or the
//     upstream environment variables (PINECONE_API_KEY, WANDB_API_KEY,
//     LANGFUSE_PUBLIC_KEY + LANGFUSE_SECRET_KEY + LANGFUSE_HOST,
//     HUGGINGFACE_TOKEN, GOOGLE_CLOUD_VISION_API_KEY).
//   - Every request runs through Runtime.HTTP and every base URL is injectable
//     with WithBaseURL, so each action is exercisable against an httptest
//     server without real credentials. The Pinecone data plane takes its host
//     per call (index_host); a full http(s) URL there is used verbatim.
//   - HTTP failures mirror the upstream helpers, including their exact
//     401/403/404/429 messages and the 500-character body slices upstream
//     applies to provider errors.
//
// Vision accepts a public URL or a local image path; local paths go through
// Runtime.resolveSafePath so the tool stays inside the configured roots.
package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane4MaxBody  = 8 << 20
	batch7Lane4MaxError = 500

	batch7Lane4PineconeVersion = "2025-04"
	batch7Lane4PineconeBase    = "https://api.pinecone.io"

	batch7Lane4WandbEndpoint = "https://api.wandb.ai/graphql"

	batch7Lane4LangfuseHost = "https://cloud.langfuse.com"

	batch7Lane4HuggingFaceBase      = "https://huggingface.co/api"
	batch7Lane4HuggingFaceInference = "https://api-inference.huggingface.co/models"
	batch7Lane4HuggingFaceEndpoints = "https://api.endpoints.huggingface.cloud"

	batch7Lane4VisionURL      = "https://vision.googleapis.com/v1/images:annotate"
	batch7Lane4VisionMaxBytes = 10 * 1024 * 1024
)

// InstallBatch7Lane4 publishes the batch-7 lane-4 AI-platform tools into a
// shared registry and returns the number of tools registered.
func InstallBatch7Lane4(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	return newBatch7Lane4Registry().Install(target)
}

// newBatch7Lane4Registry builds a registry containing only the lane-4 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane4Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane4Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range []*Tool{
		batch7Lane4PineconeTool,
		batch7Lane4WandbTool,
		batch7Lane4LangfuseTool,
		batch7Lane4HuggingFaceTool,
		batch7Lane4VisionTool,
	} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"pinecone":    batch7Lane4PineconeImpls(),
		"wandb":       batch7Lane4WandbImpls(),
		"langfuse":    batch7Lane4LangfuseImpls(),
		"huggingface": batch7Lane4HuggingFaceImpls(),
		"vision":      batch7Lane4VisionImpls(),
	} {
		for action, impl := range actions {
			registry.addImpl(tool, action, impl)
		}
	}
	return registry
}

// batch7Lane4Modules declares the public endpoints and credential variables.
// Langfuse derives its host from LANGFUSE_HOST (or WithBaseURL) because cloud
// tenants live in different regions; the Hugging Face inference and Endpoints
// APIs are separate hosts from the Hub.
func batch7Lane4Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"pinecone_tool": {
			ID: "pinecone_tool", BaseURL: batch7Lane4PineconeBase,
			EnvVars: []string{"PINECONE_API_KEY"},
		},
		"wandb_tool": {
			ID: "wandb_tool", BaseURL: batch7Lane4WandbEndpoint,
			EnvVars: []string{"WANDB_API_KEY"},
		},
		"langfuse_tool": {
			ID: "langfuse_tool",
			EnvVars: []string{
				"LANGFUSE_PUBLIC_KEY",
				"LANGFUSE_SECRET_KEY",
				"LANGFUSE_HOST",
			},
		},
		"huggingface_tool": {
			ID: "huggingface_tool", BaseURL: batch7Lane4HuggingFaceBase,
			EnvVars: []string{"HUGGINGFACE_TOKEN"},
		},
		"huggingface_inference_tool": {
			ID: "huggingface_inference_tool", BaseURL: batch7Lane4HuggingFaceInference,
		},
		"huggingface_endpoints_tool": {
			ID: "huggingface_endpoints_tool", BaseURL: batch7Lane4HuggingFaceEndpoints,
		},
		"vision_tool": {
			ID: "vision_tool", BaseURL: batch7Lane4VisionURL,
			EnvVars: []string{"GOOGLE_CLOUD_VISION_API_KEY"},
		},
	}
}

// ------------------------------------------------------------------ shared ---

// batch7Lane4Getenv resolves the runtime environment lookup.
func batch7Lane4Getenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// batch7Lane4ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch7Lane4ModuleURL(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch7Lane4ModuleToken resolves a single-token module credential from
// WithCredential first, then the configured environment lookup.
func batch7Lane4ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.resolveToken(batch7Lane4Getenv(rt))
	return strings.TrimSpace(token)
}

// batch7Lane4HTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func batch7Lane4HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch7Lane4Reply is the status/body pair every module helper inspects.
type batch7Lane4Reply struct {
	Status      int
	Body        []byte
	ContentType string
}

// batch7Lane4Do performs one JSON request and reads a bounded body.
func batch7Lane4Do(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (batch7Lane4Reply, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return batch7Lane4Reply{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return batch7Lane4Reply{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := batch7Lane4HTTP(rt).Do(req)
	if err != nil {
		return batch7Lane4Reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane4MaxBody))
	if err != nil {
		return batch7Lane4Reply{}, err
	}
	return batch7Lane4Reply{
		Status:      resp.StatusCode,
		Body:        raw,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// batch7Lane4IsTimeout reports whether err is a deadline or a net timeout.
func batch7Lane4IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch7Lane4BasicAuth renders an HTTP Basic header value.
func batch7Lane4BasicAuth(user, password string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
	return "Basic " + encoded
}

// batch7Lane4TruncateText caps provider error/description text the same way
// upstream slices response.text[:500].
func batch7Lane4TruncateText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// batch7Lane4Clamp bounds an integer to [low, high].
func batch7Lane4Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch7Lane4AsMap safely views a decoded JSON object.
func batch7Lane4AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch7Lane4AsSlice safely views a decoded JSON array.
func batch7Lane4AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch7Lane4AnyList renders a decoded JSON array, normalising a missing field
// to an empty (non-nil) slice so it encodes as [].
func batch7Lane4AnyList(value any) []any {
	items, ok := value.([]any)
	if !ok || items == nil {
		return []any{}
	}
	return items
}

// batch7Lane4AsString reads a decoded JSON string.
func batch7Lane4AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch7Lane4BoolOr reads a boolean with the upstream `.get(key, fallback)`
// default applied when the field is absent.
func batch7Lane4BoolOr(value any, fallback bool) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}

// batch7Lane4FloatOr reads a number with a fallback.
func batch7Lane4FloatOr(value any, fallback float64) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	}
	return fallback
}

// batch7Lane4IntOr reads an integer with a fallback.
func batch7Lane4IntOr(value any, fallback int) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

// batch7Lane4JSONNumber turns integral float64 values (the form
// encoding/json produces) back into integers so responses render like Python
// ints instead of 456.0.
func batch7Lane4JSONNumber(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
		return typed
	default:
		return value
	}
}

// batch7Lane4StringID renders an identifier the way Python's str() does.
func batch7Lane4StringID(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

// batch7Lane4StringList renders a decoded JSON array of names as []string. A
// missing field produces an empty (non-nil) slice so it encodes as [].
func batch7Lane4StringList(value any) []string {
	items := batch7Lane4AsSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, batch7Lane4AsString(item))
	}
	return out
}

// batch7Lane4FirstN bounds a name list the way upstream tags[:10] does.
func batch7Lane4FirstN(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

// batch7Lane4DecodeAny decodes a JSON body of any shape.
func batch7Lane4DecodeAny(raw []byte) (any, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// batch7Lane4DecodeObject decodes a JSON object body.
func batch7Lane4DecodeObject(raw []byte) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, true
}

// batch7Lane4Round3 mirrors Python round(value, 3).
func batch7Lane4Round3(value float64) float64 {
	return math.Round(value*1000) / 1000
}

// batch7Lane4SplitCSV splits a comma-separated argument, trimming and dropping
// blanks.
func batch7Lane4SplitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ---------------------------------------------------------------- pinecone ---

var batch7Lane4PineconeTool = &Tool{
	ID:          "pinecone_tool",
	Name:        "pinecone",
	DisplayName: "Pinecone",
	Summary:     "Pinecone vector database: indexes and vector operations (9 actions).",
	Actions: []Action{
		{
			Name:    "pinecone_list_indexes",
			Summary: "List all indexes in your Pinecone project.",
			Source:  "pinecone_tool/pinecone_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "pinecone_create_index",
			Summary:  "Create a new serverless Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"name", "dimension"},
			Params: map[string]any{
				"name":      param("string"),
				"dimension": param("integer"),
				"metric":    paramDefault("string", "cosine"),
				"cloud":     paramDefault("string", "aws"),
				"region":    paramDefault("string", "us-east-1"),
			},
		},
		{
			Name:     "pinecone_describe_index",
			Summary:  "Get details about a specific Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_name"},
			Params: map[string]any{
				"index_name": param("string"),
			},
		},
		{
			Name:     "pinecone_delete_index",
			Summary:  "Delete a Pinecone index. This is irreversible.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_name"},
			Params: map[string]any{
				"index_name": param("string"),
			},
		},
		{
			Name:     "pinecone_upsert_vectors",
			Summary:  "Upsert vectors into a Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host", "vectors"},
			Params: map[string]any{
				"index_host": param("string"),
				"vectors":    param("array"),
				"namespace":  paramDefault("string", ""),
			},
		},
		{
			Name:     "pinecone_query_vectors",
			Summary:  "Query a Pinecone index for similar vectors.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host"},
			Params: map[string]any{
				"index_host":       param("string"),
				"vector":           param("array"),
				"id":               paramDefault("string", ""),
				"top_k":            paramDefault("integer", 10),
				"namespace":        paramDefault("string", ""),
				"filter":           param("object"),
				"include_metadata": paramDefault("boolean", true),
				"include_values":   paramDefault("boolean", false),
			},
		},
		{
			Name:     "pinecone_fetch_vectors",
			Summary:  "Fetch vectors by ID from a Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host", "ids"},
			Params: map[string]any{
				"index_host": param("string"),
				"ids":        param("array"),
				"namespace":  paramDefault("string", ""),
			},
		},
		{
			Name:     "pinecone_delete_vectors",
			Summary:  "Delete vectors from a Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host"},
			Params: map[string]any{
				"index_host": param("string"),
				"ids":        param("array"),
				"namespace":  paramDefault("string", ""),
				"delete_all": paramDefault("boolean", false),
				"filter":     param("object"),
			},
		},
		{
			Name:     "pinecone_index_stats",
			Summary:  "Get statistics for a Pinecone index, including namespace vector counts.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host"},
			Params: map[string]any{
				"index_host": param("string"),
			},
		},
	},
}

func batch7Lane4PineconeImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pinecone_list_indexes":   batch7Lane4PineconeListIndexes,
		"pinecone_create_index":   batch7Lane4PineconeCreateIndex,
		"pinecone_describe_index": batch7Lane4PineconeDescribeIndex,
		"pinecone_delete_index":   batch7Lane4PineconeDeleteIndex,
		"pinecone_upsert_vectors": batch7Lane4PineconeUpsertVectors,
		"pinecone_query_vectors":  batch7Lane4PineconeQueryVectors,
		"pinecone_fetch_vectors":  batch7Lane4PineconeFetchVectors,
		"pinecone_delete_vectors": batch7Lane4PineconeDeleteVectors,
		"pinecone_index_stats":    batch7Lane4PineconeIndexStats,
	}
}

// batch7Lane4PineconeAuth returns the token or the upstream missing-key error.
func batch7Lane4PineconeAuth(rt *Runtime) (string, map[string]any) {
	token := batch7Lane4ModuleToken(rt, "pinecone_tool")
	if token == "" {
		return "", map[string]any{
			"error": "PINECONE_API_KEY not set",
			"help":  "Get an API key at https://app.pinecone.io/ under API Keys",
		}
	}
	return token, nil
}

// batch7Lane4PineconeHeaders mirrors _headers.
func batch7Lane4PineconeHeaders(token string) map[string]string {
	return map[string]string{
		"Api-Key":                token,
		"Content-Type":           "application/json",
		"X-Pinecone-Api-Version": batch7Lane4PineconeVersion,
	}
}

// batch7Lane4PineconeControl performs one control-plane request and maps HTTP
// failures the way _control does. The boolean reports success.
func batch7Lane4PineconeControl(
	ctx context.Context,
	rt *Runtime,
	token, method, path string,
	payload any,
) (map[string]any, bool) {
	target := batch7Lane4ModuleURL(rt, "pinecone_tool")
	if target == "" {
		target = batch7Lane4PineconeBase
	}
	reply, err := batch7Lane4Do(
		ctx, rt, method, target+path, batch7Lane4PineconeHeaders(token), payload,
	)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Pinecone timed out"}, false
		}
		return map[string]any{"error": "Pinecone request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your PINECONE_API_KEY."}, false
	}
	if reply.Status == http.StatusAccepted {
		return map[string]any{"status": "accepted"}, true
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Pinecone API error %d: %s",
				reply.Status,
				batch7Lane4TruncateText(string(reply.Body), batch7Lane4MaxError),
			),
		}, false
	}
	if len(bytes.TrimSpace(reply.Body)) == 0 {
		return map[string]any{"status": "ok"}, true
	}
	value, ok := batch7Lane4DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Pinecone request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane4PineconeData performs one data-plane request against the index
// host and maps HTTP failures the way _data does.
func batch7Lane4PineconeData(
	ctx context.Context,
	rt *Runtime,
	token, method, host, path string,
	query url.Values,
	payload any,
) (map[string]any, bool) {
	target := host
	if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
		target = "https://" + target
	}
	target = strings.TrimRight(target, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	reply, err := batch7Lane4Do(
		ctx, rt, method, target, batch7Lane4PineconeHeaders(token), payload,
	)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Pinecone timed out"}, false
		}
		return map[string]any{"error": "Pinecone request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your PINECONE_API_KEY."}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Pinecone API error %d: %s",
				reply.Status,
				batch7Lane4TruncateText(string(reply.Body), batch7Lane4MaxError),
			),
		}, false
	}
	if len(bytes.TrimSpace(reply.Body)) == 0 {
		return map[string]any{}, true
	}
	value, ok := batch7Lane4DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Pinecone request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane4PineconeStatus reads `(idx.get("status") or {})`.
func batch7Lane4PineconeStatus(source map[string]any) map[string]any {
	status, ok := source["status"].(map[string]any)
	if !ok || status == nil {
		return map[string]any{}
	}
	return status
}

func batch7Lane4PineconeListIndexes(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := batch7Lane4PineconeControl(ctx, rt, token, http.MethodGet, "/indexes", nil)
	if !ok {
		return data, nil
	}
	indexes := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["indexes"]) {
		index := batch7Lane4AsMap(raw)
		status := batch7Lane4PineconeStatus(index)
		indexes = append(indexes, map[string]any{
			"name":        batch7Lane4AsString(index["name"]),
			"dimension":   batch7Lane4JSONNumber(batch7Lane4IntOr(index["dimension"], 0)),
			"metric":      batch7Lane4AsString(index["metric"]),
			"host":        batch7Lane4AsString(index["host"]),
			"vector_type": batch7Lane4StringDefault(index["vector_type"], "dense"),
			"state":       batch7Lane4AsString(status["state"]),
			"ready":       batch7Lane4BoolOr(status["ready"], false),
		})
	}
	return map[string]any{"indexes": indexes, "count": len(indexes)}, nil
}

func batch7Lane4PineconeCreateIndex(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	name := stringArg(input, "name")
	dimension := intArg(input, "dimension", 0)
	if name == "" || dimension == 0 {
		return map[string]any{"error": "name and dimension are required"}, nil
	}
	metric := batch7Lane4StringDefault(input["metric"], "cosine")
	cloud := batch7Lane4StringDefault(input["cloud"], "aws")
	region := batch7Lane4StringDefault(input["region"], "us-east-1")
	body := map[string]any{
		"name":      name,
		"dimension": dimension,
		"metric":    metric,
		"spec": map[string]any{
			"serverless": map[string]any{"cloud": cloud, "region": region},
		},
	}
	data, ok := batch7Lane4PineconeControl(ctx, rt, token, http.MethodPost, "/indexes", body)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"name":      batch7Lane4StringDefault(data["name"], name),
		"dimension": batch7Lane4JSONNumber(batch7Lane4IntOr(data["dimension"], dimension)),
		"metric":    batch7Lane4StringDefault(data["metric"], metric),
		"host":      batch7Lane4AsString(data["host"]),
		"status":    "created",
	}, nil
}

func batch7Lane4PineconeDescribeIndex(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexName := stringArg(input, "index_name")
	if indexName == "" {
		return map[string]any{"error": "index_name is required"}, nil
	}
	data, ok := batch7Lane4PineconeControl(
		ctx, rt, token, http.MethodGet, "/indexes/"+url.PathEscape(indexName), nil,
	)
	if !ok {
		return data, nil
	}
	status := batch7Lane4PineconeStatus(data)
	return map[string]any{
		"name":                batch7Lane4AsString(data["name"]),
		"dimension":           batch7Lane4JSONNumber(batch7Lane4IntOr(data["dimension"], 0)),
		"metric":              batch7Lane4AsString(data["metric"]),
		"host":                batch7Lane4AsString(data["host"]),
		"vector_type":         batch7Lane4StringDefault(data["vector_type"], "dense"),
		"state":               batch7Lane4AsString(status["state"]),
		"ready":               batch7Lane4BoolOr(status["ready"], false),
		"deletion_protection": batch7Lane4StringDefault(data["deletion_protection"], "disabled"),
		"spec":                batch7Lane4AnyOrMap(data["spec"]),
	}, nil
}

func batch7Lane4PineconeDeleteIndex(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexName := stringArg(input, "index_name")
	if indexName == "" {
		return map[string]any{"error": "index_name is required"}, nil
	}
	data, ok := batch7Lane4PineconeControl(
		ctx, rt, token, http.MethodDelete, "/indexes/"+url.PathEscape(indexName), nil,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{"index_name": indexName, "status": "deleted"}, nil
}

func batch7Lane4PineconeUpsertVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	vectors := batch7Lane4AsSlice(input["vectors"])
	if indexHost == "" || len(vectors) == 0 {
		return map[string]any{"error": "index_host and vectors are required"}, nil
	}
	body := map[string]any{"vectors": vectors}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		body["namespace"] = namespace
	}
	data, ok := batch7Lane4PineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/vectors/upsert", nil, body,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"upserted_count": batch7Lane4JSONNumber(batch7Lane4IntOr(data["upsertedCount"], 0)),
	}, nil
}

func batch7Lane4PineconeQueryVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	if indexHost == "" {
		return map[string]any{"error": "index_host is required"}, nil
	}
	vector := batch7Lane4AsSlice(input["vector"])
	id := stringArg(input, "id")
	if len(vector) == 0 && id == "" {
		return map[string]any{"error": "Either vector or id is required"}, nil
	}
	topK := batch7Lane4Clamp(intArg(input, "top_k", 10), 1, 10000)
	includeMetadata := boolArg(input, "include_metadata", true)
	includeValues := boolArg(input, "include_values", false)
	body := map[string]any{
		"topK":            topK,
		"includeMetadata": includeMetadata,
		"includeValues":   includeValues,
	}
	if len(vector) > 0 {
		body["vector"] = vector
	}
	if id != "" {
		body["id"] = id
	}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		body["namespace"] = namespace
	}
	if filter, ok := input["filter"].(map[string]any); ok && len(filter) > 0 {
		body["filter"] = filter
	}
	data, ok := batch7Lane4PineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/query", nil, body,
	)
	if !ok {
		return data, nil
	}
	matches := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["matches"]) {
		source := batch7Lane4AsMap(raw)
		match := map[string]any{
			"id":    batch7Lane4AsString(source["id"]),
			"score": batch7Lane4FloatOr(source["score"], 0),
		}
		if metadata := batch7Lane4AsMap(source["metadata"]); includeMetadata && len(metadata) > 0 {
			match["metadata"] = metadata
		}
		if values := batch7Lane4AsSlice(source["values"]); includeValues && len(values) > 0 {
			match["values"] = values
		}
		matches = append(matches, match)
	}
	return map[string]any{
		"matches":   matches,
		"namespace": batch7Lane4AsString(data["namespace"]),
	}, nil
}

func batch7Lane4PineconeFetchVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	ids := batch7Lane4ToStrings(batch7Lane4AsSlice(input["ids"]))
	if indexHost == "" || len(ids) == 0 {
		return map[string]any{"error": "index_host and ids are required"}, nil
	}
	query := url.Values{"ids": ids}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		query.Set("namespace", namespace)
	}
	data, ok := batch7Lane4PineconeData(
		ctx, rt, token, http.MethodGet, indexHost, "/vectors/fetch", query, nil,
	)
	if !ok {
		return data, nil
	}
	vectors := map[string]any{}
	for id, raw := range batch7Lane4AsMap(data["vectors"]) {
		source := batch7Lane4AsMap(raw)
		vectors[id] = map[string]any{
			"id":       batch7Lane4StringDefault(source["id"], id),
			"values":   batch7Lane4AnyList(source["values"]),
			"metadata": source["metadata"],
		}
	}
	return map[string]any{
		"vectors":   vectors,
		"namespace": batch7Lane4AsString(data["namespace"]),
	}, nil
}

func batch7Lane4PineconeDeleteVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	if indexHost == "" {
		return map[string]any{"error": "index_host is required"}, nil
	}
	ids := batch7Lane4ToStrings(batch7Lane4AsSlice(input["ids"]))
	deleteAll := boolArg(input, "delete_all", false)
	filter, hasFilter := input["filter"].(map[string]any)
	if len(ids) == 0 && !deleteAll && (!hasFilter || len(filter) == 0) {
		return map[string]any{"error": "Provide ids, delete_all=True, or a filter"}, nil
	}
	body := map[string]any{}
	if len(ids) > 0 {
		body["ids"] = ids
	}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		body["namespace"] = namespace
	}
	if deleteAll {
		body["deleteAll"] = true
	}
	if hasFilter && len(filter) > 0 {
		body["filter"] = filter
	}
	data, ok := batch7Lane4PineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/vectors/delete", nil, body,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{"status": "deleted"}, nil
}

func batch7Lane4PineconeIndexStats(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4PineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	if indexHost == "" {
		return map[string]any{"error": "index_host is required"}, nil
	}
	data, ok := batch7Lane4PineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/describe_index_stats", nil,
		map[string]any{},
	)
	if !ok {
		return data, nil
	}
	namespaces := map[string]any{}
	for name, raw := range batch7Lane4AsMap(data["namespaces"]) {
		namespaces[name] = map[string]any{
			"vector_count": batch7Lane4JSONNumber(
				batch7Lane4IntOr(batch7Lane4AsMap(raw)["vectorCount"], 0),
			),
		}
	}
	return map[string]any{
		"namespaces":         namespaces,
		"dimension":          batch7Lane4JSONNumber(batch7Lane4IntOr(data["dimension"], 0)),
		"total_vector_count": batch7Lane4JSONNumber(batch7Lane4IntOr(data["totalVectorCount"], 0)),
		"metric":             batch7Lane4AsString(data["metric"]),
		"vector_type":        batch7Lane4AsString(data["vectorType"]),
	}, nil
}

// ------------------------------------------------------------------- wandb ---

var batch7Lane4WandbTool = &Tool{
	ID:          "wandb_tool",
	Name:        "wandb",
	DisplayName: "Weights & Biases",
	Summary:     "Weights & Biases experiment tracking over the GraphQL API (6 actions).",
	Actions: []Action{
		{
			Name:     "wandb_list_projects",
			Summary:  "List all projects for a Weights & Biases entity (user or organization).",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity"},
			Params: map[string]any{
				"entity": param("string"),
			},
		},
		{
			Name:     "wandb_list_runs",
			Summary:  "List runs in a Weights & Biases project.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project"},
			Params: map[string]any{
				"entity":   param("string"),
				"project":  param("string"),
				"filters":  paramDefault("string", ""),
				"per_page": paramDefault("integer", 50),
			},
		},
		{
			Name:     "wandb_get_run",
			Summary:  "Get details of a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":  param("string"),
				"project": param("string"),
				"run_id":  param("string"),
			},
		},
		{
			Name:     "wandb_get_run_metrics",
			Summary:  "Get sampled metrics history for a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":      param("string"),
				"project":     param("string"),
				"run_id":      param("string"),
				"metric_keys": paramDefault("string", ""),
			},
		},
		{
			Name:     "wandb_list_artifacts",
			Summary:  "List artifacts logged by a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":  param("string"),
				"project": param("string"),
				"run_id":  param("string"),
			},
		},
		{
			Name:     "wandb_get_summary",
			Summary:  "Get summary metrics for a specific Weights & Biases run.",
			Source:   "wandb_tool/wandb_tool.py",
			Required: []string{"entity", "project", "run_id"},
			Params: map[string]any{
				"entity":  param("string"),
				"project": param("string"),
				"run_id":  param("string"),
			},
		},
	},
}

func batch7Lane4WandbImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"wandb_list_projects":   batch7Lane4WandbListProjects,
		"wandb_list_runs":       batch7Lane4WandbListRuns,
		"wandb_get_run":         batch7Lane4WandbGetRun,
		"wandb_get_run_metrics": batch7Lane4WandbGetRunMetrics,
		"wandb_list_artifacts":  batch7Lane4WandbListArtifacts,
		"wandb_get_summary":     batch7Lane4WandbGetSummary,
	}
}

// batch7Lane4WandbAuth returns the API key or the upstream credentials error.
func batch7Lane4WandbAuth(rt *Runtime) (string, map[string]any) {
	token := batch7Lane4ModuleToken(rt, "wandb_tool")
	if token == "" {
		return "", map[string]any{
			"error": "Weights & Biases credentials not configured",
			"help":  "Set WANDB_API_KEY environment variable or configure via credential store. Get your API key at https://wandb.ai/authorize",
		}
	}
	return token, nil
}

// batch7Lane4WandbCall executes one GraphQL query and unwraps the data
// envelope the way _graphql does. The boolean reports success.
func batch7Lane4WandbCall(
	ctx context.Context,
	rt *Runtime,
	token, query string,
	variables map[string]any,
) (map[string]any, bool) {
	if variables == nil {
		variables = map[string]any{}
	}
	target := batch7Lane4ModuleURL(rt, "wandb_tool")
	if target == "" {
		target = batch7Lane4WandbEndpoint
	}
	reply, err := batch7Lane4Do(
		ctx, rt, http.MethodPost, target,
		map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		},
		map[string]any{"query": query, "variables": variables},
	)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Weights & Biases API key"}, false
	case http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient permissions for this Weights & Biases resource",
		}, false
	}
	if reply.Status >= 400 {
		detail := string(reply.Body)
		if payload, ok := batch7Lane4DecodeObject(reply.Body); ok {
			if errorsList := batch7Lane4AsSlice(payload["errors"]); len(errorsList) > 0 {
				if message := batch7Lane4AsString(batch7Lane4AsMap(errorsList[0])["message"]); message != "" {
					detail = message
				}
			}
		}
		return map[string]any{
			"error": fmt.Sprintf(
				"Weights & Biases API error (HTTP %d): %s", reply.Status, detail,
			),
		}, false
	}
	payload, ok := batch7Lane4DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Network error: invalid JSON response"}, false
	}
	if errorsList := batch7Lane4AsSlice(payload["errors"]); len(errorsList) > 0 {
		message := batch7Lane4AsString(batch7Lane4AsMap(errorsList[0])["message"])
		if message == "" {
			message = fmt.Sprint(payload["errors"])
		}
		return map[string]any{"error": "Weights & Biases GraphQL error: " + message}, false
	}
	data, ok := payload["data"].(map[string]any)
	if !ok || data == nil {
		return map[string]any{}, true
	}
	return data, true
}

// batch7Lane4WandbObject decodes a JSONString field, defaulting to {}.
func batch7Lane4WandbObject(value any) map[string]any {
	text, ok := value.(string)
	if !ok {
		return map[string]any{}
	}
	decoded, ok := batch7Lane4DecodeObject([]byte(text))
	if !ok {
		return map[string]any{}
	}
	return decoded
}

func batch7Lane4WandbListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4WandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	entity := stringArg(input, "entity")
	query := `
        query ListProjects($entity: String!) {
          projects(entityName: $entity) {
            edges {
              node {
                name
                description
                createdAt
              }
            }
          }
        }
        `
	data, ok := batch7Lane4WandbCall(ctx, rt, token, query, map[string]any{"entity": entity})
	if !ok {
		return data, nil
	}
	edges := batch7Lane4AsSlice(batch7Lane4AsMap(data["projects"])["edges"])
	projects := make([]map[string]any, 0, len(edges))
	for _, raw := range edges {
		node := batch7Lane4AsMap(batch7Lane4AsMap(raw)["node"])
		projects = append(projects, map[string]any{
			"name":        batch7Lane4AsString(node["name"]),
			"description": batch7Lane4AsString(node["description"]),
			"created_at":  batch7Lane4AsString(node["createdAt"]),
		})
	}
	return map[string]any{"entity": entity, "projects": projects}, nil
}

func batch7Lane4WandbListRuns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	var parsedFilters any
	if raw := stringArg(input, "filters"); raw != "" {
		decoded, ok := batch7Lane4DecodeAny([]byte(raw))
		if !ok {
			return map[string]any{"error": "filters must be a valid JSON string"}, nil
		}
		parsedFilters = decoded
	}
	token, problem := batch7Lane4WandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	perPage := intArg(input, "per_page", 50)
	query := `
        query ListRuns($project: String!, $entity: String!, $perPage: Int!, $filters: JSONString) {
          project(name: $project, entityName: $entity) {
            runs(first: $perPage, filters: $filters) {
              edges {
                node {
                  name
                  id
                  state
                  createdAt
                  config
                  summaryMetrics
                }
              }
            }
          }
        }
        `
	variables := map[string]any{"project": project, "entity": entity, "perPage": perPage}
	if parsedFilters != nil {
		variables["filters"] = parsedFilters
	}
	data, ok := batch7Lane4WandbCall(ctx, rt, token, query, variables)
	if !ok {
		return data, nil
	}
	edges := batch7Lane4AsSlice(
		batch7Lane4AsMap(batch7Lane4AsMap(data["project"])["runs"])["edges"],
	)
	runs := make([]map[string]any, 0, len(edges))
	for _, raw := range edges {
		node := batch7Lane4AsMap(batch7Lane4AsMap(raw)["node"])
		runs = append(runs, map[string]any{
			"id":           batch7Lane4AsString(node["name"]),
			"display_name": batch7Lane4AsString(node["id"]),
			"state":        batch7Lane4AsString(node["state"]),
			"created_at":   batch7Lane4AsString(node["createdAt"]),
			"config":       batch7Lane4WandbObject(node["config"]),
		})
	}
	return map[string]any{"entity": entity, "project": project, "runs": runs}, nil
}

func batch7Lane4WandbGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	token, problem := batch7Lane4WandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	query := `
        query GetRun($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              name
              id
              state
              createdAt
              config
              summaryMetrics
              tags
              notes
            }
          }
        }
        `
	data, ok := batch7Lane4WandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := batch7Lane4AsMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	return map[string]any{
		"id":           batch7Lane4AsString(node["name"]),
		"display_name": batch7Lane4AsString(node["id"]),
		"state":        batch7Lane4AsString(node["state"]),
		"created_at":   batch7Lane4AsString(node["createdAt"]),
		"config":       batch7Lane4WandbObject(node["config"]),
		"summary":      batch7Lane4WandbObject(node["summaryMetrics"]),
		"tags":         batch7Lane4AnyList(node["tags"]),
		"notes":        batch7Lane4AsString(node["notes"]),
	}, nil
}

func batch7Lane4WandbGetRunMetrics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4WandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	metricKeys := stringArg(input, "metric_keys")
	if metricKeys == "" {
		return map[string]any{
			"error": "metric_keys is required (comma-separated, e.g. 'loss,accuracy')",
		}, nil
	}
	keys := batch7Lane4SplitCSV(metricKeys)
	if len(keys) == 0 {
		return map[string]any{"error": "metric_keys must include at least one non-empty key"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	specs := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		specs = append(specs, map[string]any{"key": key})
	}
	encodedSpecs, err := json.Marshal(specs)
	if err != nil {
		return map[string]any{"error": "metric_keys must include at least one non-empty key"}, nil
	}
	query := fmt.Sprintf(`
        query GetRunMetrics($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              sampledHistory(specs: %s)
            }
          }
        }
        `, string(encodedSpecs))
	data, ok := batch7Lane4WandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := batch7Lane4AsMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	return map[string]any{
		"run_id":      runID,
		"metric_keys": keys,
		"history":     batch7Lane4AnyList(node["sampledHistory"]),
	}, nil
}

func batch7Lane4WandbListArtifacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	token, problem := batch7Lane4WandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	query := `
        query ListArtifacts($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              outputArtifacts {
                edges {
                  node {
                    name
                    type
                    description
                    createdAt
                  }
                }
              }
            }
          }
        }
        `
	data, ok := batch7Lane4WandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := batch7Lane4AsMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	edges := batch7Lane4AsSlice(batch7Lane4AsMap(node["outputArtifacts"])["edges"])
	artifacts := make([]map[string]any, 0, len(edges))
	for _, raw := range edges {
		artifact := batch7Lane4AsMap(batch7Lane4AsMap(raw)["node"])
		artifacts = append(artifacts, map[string]any{
			"name":        batch7Lane4AsString(artifact["name"]),
			"type":        batch7Lane4AsString(artifact["type"]),
			"description": batch7Lane4AsString(artifact["description"]),
			"created_at":  batch7Lane4AsString(artifact["createdAt"]),
		})
	}
	return map[string]any{"run_id": runID, "artifacts": artifacts}, nil
}

func batch7Lane4WandbGetSummary(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	runID := stringArg(input, "run_id")
	if runID == "" {
		return map[string]any{"error": "run_id is required"}, nil
	}
	entity := stringArg(input, "entity")
	project := stringArg(input, "project")
	token, problem := batch7Lane4WandbAuth(rt)
	if problem != nil {
		return problem, nil
	}
	query := `
        query GetSummary($project: String!, $entity: String!, $run: String!) {
          project(name: $project, entityName: $entity) {
            run(name: $run) {
              summaryMetrics
            }
          }
        }
        `
	data, ok := batch7Lane4WandbCall(
		ctx, rt, token, query,
		map[string]any{"project": project, "entity": entity, "run": runID},
	)
	if !ok {
		return data, nil
	}
	node, found := batch7Lane4AsMap(data["project"])["run"].(map[string]any)
	if !found || node == nil {
		return map[string]any{"error": "Weights & Biases resource not found"}, nil
	}
	summary := map[string]any{}
	for key, value := range batch7Lane4WandbObject(node["summaryMetrics"]) {
		if strings.HasPrefix(key, "_") {
			continue
		}
		summary[key] = value
	}
	return map[string]any{"run_id": runID, "summary": summary}, nil
}

// ---------------------------------------------------------------- langfuse ---

var batch7Lane4LangfuseTool = &Tool{
	ID:          "langfuse_tool",
	Name:        "langfuse",
	DisplayName: "Langfuse",
	Summary:     "Langfuse LLM observability: traces, scores and prompts (6 actions).",
	Actions: []Action{
		{
			Name:    "langfuse_list_traces",
			Summary: "List traces from Langfuse with optional filters.",
			Source:  "langfuse_tool/langfuse_tool.py",
			Params: map[string]any{
				"name":       paramDefault("string", ""),
				"user_id":    paramDefault("string", ""),
				"session_id": paramDefault("string", ""),
				"tags":       paramDefault("string", ""),
				"page":       paramDefault("integer", 1),
				"limit":      paramDefault("integer", 50),
			},
		},
		{
			Name:     "langfuse_get_trace",
			Summary:  "Get full details of a specific Langfuse trace.",
			Source:   "langfuse_tool/langfuse_tool.py",
			Required: []string{"trace_id"},
			Params: map[string]any{
				"trace_id": param("string"),
			},
		},
		{
			Name:    "langfuse_list_scores",
			Summary: "List scores from Langfuse with optional filters.",
			Source:  "langfuse_tool/langfuse_tool.py",
			Params: map[string]any{
				"trace_id":  paramDefault("string", ""),
				"name":      paramDefault("string", ""),
				"source":    paramDefault("string", ""),
				"data_type": paramDefault("string", ""),
				"page":      paramDefault("integer", 1),
				"limit":     paramDefault("integer", 50),
			},
		},
		{
			Name:     "langfuse_create_score",
			Summary:  "Create a score for a Langfuse trace or observation.",
			Source:   "langfuse_tool/langfuse_tool.py",
			Required: []string{"trace_id", "name", "value"},
			Params: map[string]any{
				"trace_id":       param("string"),
				"name":           param("string"),
				"value":          param("number"),
				"data_type":      paramDefault("string", "NUMERIC"),
				"comment":        paramDefault("string", ""),
				"observation_id": paramDefault("string", ""),
			},
		},
		{
			Name:    "langfuse_list_prompts",
			Summary: "List prompts from Langfuse prompt management.",
			Source:  "langfuse_tool/langfuse_tool.py",
			Params: map[string]any{
				"name":  paramDefault("string", ""),
				"label": paramDefault("string", ""),
				"tag":   paramDefault("string", ""),
				"page":  paramDefault("integer", 1),
				"limit": paramDefault("integer", 50),
			},
		},
		{
			Name:     "langfuse_get_prompt",
			Summary:  "Get a specific Langfuse prompt by name.",
			Source:   "langfuse_tool/langfuse_tool.py",
			Required: []string{"prompt_name"},
			Params: map[string]any{
				"prompt_name": param("string"),
				"version":     paramDefault("integer", 0),
				"label":       paramDefault("string", ""),
			},
		},
	},
}

func batch7Lane4LangfuseImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"langfuse_list_traces":  batch7Lane4LangfuseListTraces,
		"langfuse_get_trace":    batch7Lane4LangfuseGetTrace,
		"langfuse_list_scores":  batch7Lane4LangfuseListScores,
		"langfuse_create_score": batch7Lane4LangfuseCreateScore,
		"langfuse_list_prompts": batch7Lane4LangfuseListPrompts,
		"langfuse_get_prompt":   batch7Lane4LangfuseGetPrompt,
	}
}

// batch7Lane4LangfuseAuth resolves the public/secret/host bundle.
func batch7Lane4LangfuseAuth(rt *Runtime) (string, string, string, map[string]any) {
	getenv := batch7Lane4Getenv(rt)
	publicKey := strings.TrimSpace(getenv("LANGFUSE_PUBLIC_KEY"))
	secretKey := strings.TrimSpace(getenv("LANGFUSE_SECRET_KEY"))
	if publicKey == "" || secretKey == "" {
		return "", "", "", map[string]any{
			"error": "Langfuse credentials not configured",
			"help":  "Set LANGFUSE_PUBLIC_KEY and LANGFUSE_SECRET_KEY environment variables or configure via credential store",
		}
	}
	host := batch7Lane4ModuleURL(rt, "langfuse_tool")
	if host == "" {
		host = strings.TrimSpace(getenv("LANGFUSE_HOST"))
	}
	if host == "" {
		host = batch7Lane4LangfuseHost
	}
	return publicKey, secretKey, strings.TrimRight(host, "/"), nil
}

// batch7Lane4LangfuseCall performs one request and maps HTTP failures the way
// _handle_response does. The boolean reports success.
func batch7Lane4LangfuseCall(
	ctx context.Context,
	rt *Runtime,
	publicKey, secretKey, host, method, path string,
	query url.Values,
	payload any,
) (map[string]any, bool) {
	target := host + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": batch7Lane4BasicAuth(publicKey, secretKey),
		"Content-Type":  "application/json",
	}
	reply, err := batch7Lane4Do(ctx, rt, method, target, headers, payload)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Langfuse API keys"}, false
	case http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient permissions for this Langfuse resource",
		}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Langfuse resource not found"}, false
	case http.StatusTooManyRequests:
		return map[string]any{
			"error": "Langfuse rate limit exceeded. Try again later.",
		}, false
	}
	if reply.Status >= 400 {
		detail := string(reply.Body)
		if payload, ok := batch7Lane4DecodeObject(reply.Body); ok {
			if message := batch7Lane4AsString(payload["message"]); message != "" {
				detail = message
			} else if message := batch7Lane4AsString(payload["error"]); message != "" {
				detail = message
			}
		}
		return map[string]any{
			"error": fmt.Sprintf("Langfuse API error (HTTP %d): %s", reply.Status, detail),
		}, false
	}
	value, ok := batch7Lane4DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Network error: invalid JSON response"}, false
	}
	return value, true
}

func batch7Lane4LangfuseListTraces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := batch7Lane4LangfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	page := intArg(input, "page", 1)
	limit := intArg(input, "limit", 50)
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if name := stringArg(input, "name"); name != "" {
		query.Set("name", name)
	}
	if userID := stringArg(input, "user_id"); userID != "" {
		query.Set("userId", userID)
	}
	if sessionID := stringArg(input, "session_id"); sessionID != "" {
		query.Set("sessionId", sessionID)
	}
	for _, tag := range batch7Lane4SplitCSV(stringArg(input, "tags")) {
		query.Add("tags", tag)
	}
	data, ok := batch7Lane4LangfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet, "/api/public/traces", query, nil,
	)
	if !ok {
		return data, nil
	}
	traces := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["data"]) {
		trace := batch7Lane4AsMap(raw)
		traces = append(traces, map[string]any{
			"id":                trace["id"],
			"name":              trace["name"],
			"timestamp":         trace["timestamp"],
			"user_id":           trace["userId"],
			"session_id":        trace["sessionId"],
			"tags":              batch7Lane4AnyList(trace["tags"]),
			"latency":           trace["latency"],
			"total_cost":        trace["totalCost"],
			"observation_count": len(batch7Lane4AsSlice(trace["observations"])),
		})
	}
	meta := batch7Lane4AsMap(data["meta"])
	return map[string]any{
		"count":       len(traces),
		"total_items": batch7Lane4JSONNumber(batch7Lane4IntOr(meta["totalItems"], 0)),
		"page":        batch7Lane4JSONNumber(batch7Lane4IntOr(meta["page"], page)),
		"total_pages": batch7Lane4JSONNumber(batch7Lane4IntOr(meta["totalPages"], 0)),
		"traces":      traces,
	}, nil
}

func batch7Lane4LangfuseGetTrace(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := batch7Lane4LangfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	traceID := stringArg(input, "trace_id")
	if traceID == "" {
		return map[string]any{"error": "trace_id is required"}, nil
	}
	data, ok := batch7Lane4LangfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet,
		"/api/public/traces/"+url.PathEscape(traceID), nil, nil,
	)
	if !ok {
		return data, nil
	}
	observations := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["observations"]) {
		observation := batch7Lane4AsMap(raw)
		observations = append(observations, map[string]any{
			"id":         observation["id"],
			"type":       observation["type"],
			"name":       observation["name"],
			"model":      observation["model"],
			"start_time": observation["startTime"],
			"end_time":   observation["endTime"],
			"usage":      observation["usage"],
		})
	}
	scores := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["scores"]) {
		score := batch7Lane4AsMap(raw)
		scores = append(scores, map[string]any{
			"id":        score["id"],
			"name":      score["name"],
			"value":     score["value"],
			"data_type": score["dataType"],
			"source":    score["source"],
			"comment":   score["comment"],
		})
	}
	return map[string]any{
		"id":           data["id"],
		"name":         data["name"],
		"timestamp":    data["timestamp"],
		"user_id":      data["userId"],
		"session_id":   data["sessionId"],
		"tags":         batch7Lane4AnyList(data["tags"]),
		"latency":      data["latency"],
		"total_cost":   data["totalCost"],
		"input":        data["input"],
		"output":       data["output"],
		"observations": observations,
		"scores":       scores,
	}, nil
}

func batch7Lane4LangfuseListScores(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := batch7Lane4LangfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	page := intArg(input, "page", 1)
	limit := intArg(input, "limit", 50)
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if traceID := stringArg(input, "trace_id"); traceID != "" {
		query.Set("traceId", traceID)
	}
	if name := stringArg(input, "name"); name != "" {
		query.Set("name", name)
	}
	if source := stringArg(input, "source"); source != "" {
		query.Set("source", source)
	}
	if dataType := stringArg(input, "data_type"); dataType != "" {
		query.Set("dataType", dataType)
	}
	data, ok := batch7Lane4LangfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet, "/api/public/v2/scores", query, nil,
	)
	if !ok {
		return data, nil
	}
	scores := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["data"]) {
		score := batch7Lane4AsMap(raw)
		scores = append(scores, map[string]any{
			"id":             score["id"],
			"trace_id":       score["traceId"],
			"observation_id": score["observationId"],
			"name":           score["name"],
			"value":          score["value"],
			"data_type":      score["dataType"],
			"source":         score["source"],
			"comment":        score["comment"],
			"timestamp":      score["timestamp"],
		})
	}
	meta := batch7Lane4AsMap(data["meta"])
	return map[string]any{
		"count":       len(scores),
		"total_items": batch7Lane4JSONNumber(batch7Lane4IntOr(meta["totalItems"], 0)),
		"page":        batch7Lane4JSONNumber(batch7Lane4IntOr(meta["page"], page)),
		"total_pages": batch7Lane4JSONNumber(batch7Lane4IntOr(meta["totalPages"], 0)),
		"scores":      scores,
	}, nil
}

func batch7Lane4LangfuseCreateScore(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := batch7Lane4LangfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	traceID := stringArg(input, "trace_id")
	name := stringArg(input, "name")
	if traceID == "" || name == "" {
		return map[string]any{"error": "trace_id and name are required"}, nil
	}
	body := map[string]any{
		"traceId":  traceID,
		"name":     name,
		"value":    floatArg(input, "value", 0),
		"dataType": batch7Lane4StringDefault(input["data_type"], "NUMERIC"),
	}
	if comment := stringArg(input, "comment"); comment != "" {
		body["comment"] = comment
	}
	if observationID := stringArg(input, "observation_id"); observationID != "" {
		body["observationId"] = observationID
	}
	data, ok := batch7Lane4LangfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodPost, "/api/public/scores", nil, body,
	)
	if !ok {
		return data, nil
	}
	return data, nil
}

func batch7Lane4LangfuseListPrompts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := batch7Lane4LangfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	page := intArg(input, "page", 1)
	limit := intArg(input, "limit", 50)
	query := url.Values{
		"page":  {strconv.Itoa(page)},
		"limit": {strconv.Itoa(limit)},
	}
	if name := stringArg(input, "name"); name != "" {
		query.Set("name", name)
	}
	if label := stringArg(input, "label"); label != "" {
		query.Set("label", label)
	}
	if tag := stringArg(input, "tag"); tag != "" {
		query.Set("tag", tag)
	}
	data, ok := batch7Lane4LangfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet, "/api/public/v2/prompts", query, nil,
	)
	if !ok {
		return data, nil
	}
	prompts := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data["data"]) {
		prompt := batch7Lane4AsMap(raw)
		prompts = append(prompts, map[string]any{
			"name":            prompt["name"],
			"versions":        batch7Lane4AnyList(prompt["versions"]),
			"labels":          batch7Lane4AnyList(prompt["labels"]),
			"tags":            batch7Lane4AnyList(prompt["tags"]),
			"last_updated_at": prompt["lastUpdatedAt"],
		})
	}
	meta := batch7Lane4AsMap(data["meta"])
	return map[string]any{
		"count":       len(prompts),
		"total_items": batch7Lane4JSONNumber(batch7Lane4IntOr(meta["totalItems"], 0)),
		"page":        batch7Lane4JSONNumber(batch7Lane4IntOr(meta["page"], page)),
		"total_pages": batch7Lane4JSONNumber(batch7Lane4IntOr(meta["totalPages"], 0)),
		"prompts":     prompts,
	}, nil
}

func batch7Lane4LangfuseGetPrompt(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	publicKey, secretKey, host, problem := batch7Lane4LangfuseAuth(rt)
	if problem != nil {
		return problem, nil
	}
	promptName := stringArg(input, "prompt_name")
	if promptName == "" {
		return map[string]any{"error": "prompt_name is required"}, nil
	}
	query := url.Values{}
	if version := intArg(input, "version", 0); version > 0 {
		query.Set("version", strconv.Itoa(version))
	}
	if label := stringArg(input, "label"); label != "" {
		query.Set("label", label)
	}
	data, ok := batch7Lane4LangfuseCall(
		ctx, rt, publicKey, secretKey, host, http.MethodGet,
		"/api/public/v2/prompts/"+url.PathEscape(promptName), query, nil,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"name":    data["name"],
		"version": data["version"],
		"type":    data["type"],
		"prompt":  data["prompt"],
		"config":  data["config"],
		"labels":  batch7Lane4AnyList(data["labels"]),
		"tags":    batch7Lane4AnyList(data["tags"]),
	}, nil
}

// ------------------------------------------------------------- huggingface ---

var batch7Lane4HuggingFaceTool = &Tool{
	ID:          "huggingface_tool",
	Name:        "huggingface",
	DisplayName: "Hugging Face",
	Summary:     "Hugging Face Hub discovery plus Inference API calls (9 actions).",
	Actions: []Action{
		{
			Name:    "huggingface_search_models",
			Summary: "Search for models on HuggingFace Hub.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"query":  paramDefault("string", ""),
				"author": paramDefault("string", ""),
				"sort":   paramDefault("string", "downloads"),
				"limit":  paramDefault("integer", 20),
			},
		},
		{
			Name:     "huggingface_get_model",
			Summary:  "Get details about a specific model on HuggingFace Hub.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"model_id"},
			Params: map[string]any{
				"model_id": param("string"),
			},
		},
		{
			Name:    "huggingface_search_datasets",
			Summary: "Search for datasets on HuggingFace Hub.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"query":  paramDefault("string", ""),
				"author": paramDefault("string", ""),
				"sort":   paramDefault("string", "downloads"),
				"limit":  paramDefault("integer", 20),
			},
		},
		{
			Name:     "huggingface_get_dataset",
			Summary:  "Get details about a specific dataset on HuggingFace Hub.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"dataset_id"},
			Params: map[string]any{
				"dataset_id": param("string"),
			},
		},
		{
			Name:    "huggingface_search_spaces",
			Summary: "Search for Spaces on HuggingFace Hub.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"query":  paramDefault("string", ""),
				"author": paramDefault("string", ""),
				"sort":   paramDefault("string", "likes"),
				"limit":  paramDefault("integer", 20),
			},
		},
		{
			Name:    "huggingface_whoami",
			Summary: "Get info about the authenticated HuggingFace user.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "huggingface_run_inference",
			Summary:  "Run inference on a HuggingFace model via the Inference API.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"model_id", "inputs"},
			Params: map[string]any{
				"model_id":   param("string"),
				"inputs":     param("string"),
				"task":       paramDefault("string", ""),
				"parameters": paramDefault("string", ""),
			},
		},
		{
			Name:     "huggingface_run_embedding",
			Summary:  "Generate text embeddings using a HuggingFace model via the Inference API.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"model_id", "inputs"},
			Params: map[string]any{
				"model_id": param("string"),
				"inputs":   param("string"),
			},
		},
		{
			Name:    "huggingface_list_inference_endpoints",
			Summary: "List deployed Inference Endpoints on HuggingFace.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"namespace": paramDefault("string", ""),
			},
		},
	},
}

func batch7Lane4HuggingFaceImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"huggingface_search_models":            batch7Lane4HuggingFaceSearchModels,
		"huggingface_get_model":                batch7Lane4HuggingFaceGetModel,
		"huggingface_search_datasets":          batch7Lane4HuggingFaceSearchDatasets,
		"huggingface_get_dataset":              batch7Lane4HuggingFaceGetDataset,
		"huggingface_search_spaces":            batch7Lane4HuggingFaceSearchSpaces,
		"huggingface_whoami":                   batch7Lane4HuggingFaceWhoami,
		"huggingface_run_inference":            batch7Lane4HuggingFaceRunInference,
		"huggingface_run_embedding":            batch7Lane4HuggingFaceRunEmbedding,
		"huggingface_list_inference_endpoints": batch7Lane4HuggingFaceListInferenceEndpoints,
	}
}

// batch7Lane4HuggingFaceAuth returns the token or the upstream auth error.
func batch7Lane4HuggingFaceAuth(rt *Runtime) (string, map[string]any) {
	token := batch7Lane4ModuleToken(rt, "huggingface_tool")
	if token == "" {
		return "", map[string]any{
			"error": "HUGGINGFACE_TOKEN not set",
			"help":  "Get a token at https://huggingface.co/settings/tokens",
		}
	}
	return token, nil
}

// batch7Lane4HuggingFaceGet performs one Hub API GET. The boolean reports
// success and the value keeps the upstream list/dict distinction.
func batch7Lane4HuggingFaceGet(
	ctx context.Context,
	rt *Runtime,
	token, path string,
	params url.Values,
) (any, bool) {
	target := batch7Lane4ModuleURL(rt, "huggingface_tool")
	if target == "" {
		target = batch7Lane4HuggingFaceBase
	}
	target += path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	reply, err := batch7Lane4Do(ctx, rt, http.MethodGet, target, headers, nil)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to HuggingFace timed out"}, false
		}
		return map[string]any{"error": "HuggingFace request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your HUGGINGFACE_TOKEN."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Not found: " + path}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"HuggingFace API error %d: %s",
				reply.Status,
				batch7Lane4TruncateText(string(reply.Body), batch7Lane4MaxError),
			),
		}, false
	}
	value, ok := batch7Lane4DecodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "HuggingFace request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane4HuggingFaceInferencePOST calls the Inference API for one model.
func batch7Lane4HuggingFaceInferencePOST(
	ctx context.Context,
	rt *Runtime,
	token, modelID string,
	payload map[string]any,
) (any, bool) {
	base := batch7Lane4ModuleURL(rt, "huggingface_inference_tool")
	if base == "" {
		base = batch7Lane4HuggingFaceInference
	}
	target := base + "/" + modelID
	headers := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	reply, err := batch7Lane4Do(ctx, rt, http.MethodPost, target, headers, payload)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{
				"error": "Inference request timed out. Try a smaller input or a faster model.",
			}, false
		}
		return map[string]any{
			"error": "HuggingFace inference request failed: " + err.Error(),
		}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your HUGGINGFACE_TOKEN."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Model not found: " + target}, false
	case http.StatusServiceUnavailable:
		estimated := any("unknown")
		if strings.HasPrefix(strings.ToLower(reply.ContentType), "application/json") {
			if body, ok := batch7Lane4DecodeObject(reply.Body); ok {
				if value, present := body["estimated_time"]; present && value != nil {
					estimated = value
				}
			}
		}
		return map[string]any{
			"error":          "Model is loading",
			"estimated_time": estimated,
			"help":           "The model is being loaded. Retry after the estimated time.",
		}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"HuggingFace Inference API error %d: %s",
				reply.Status,
				batch7Lane4TruncateText(string(reply.Body), batch7Lane4MaxError),
			),
		}, false
	}
	value, ok := batch7Lane4DecodeAny(reply.Body)
	if !ok {
		return map[string]any{
			"error": "HuggingFace inference request failed: invalid JSON response",
		}, false
	}
	return value, true
}

// batch7Lane4HuggingFaceListEndpoints calls the Inference Endpoints API.
func batch7Lane4HuggingFaceListEndpoints(
	ctx context.Context,
	rt *Runtime,
	token, path string,
) (any, bool) {
	base := batch7Lane4ModuleURL(rt, "huggingface_endpoints_tool")
	if base == "" {
		base = batch7Lane4HuggingFaceEndpoints
	}
	reply, err := batch7Lane4Do(
		ctx, rt, http.MethodGet, base+path,
		map[string]string{"Authorization": "Bearer " + token}, nil,
	)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to HuggingFace Endpoints API timed out"}, false
		}
		return map[string]any{"error": "Endpoints request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your HUGGINGFACE_TOKEN."}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"Failed to list endpoints (HTTP %d): %s",
				reply.Status,
				batch7Lane4TruncateText(string(reply.Body), batch7Lane4MaxError),
			),
		}, false
	}
	value, ok := batch7Lane4DecodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Endpoints request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane4HuggingFaceSearchParams mirrors the shared search parameter block.
func batch7Lane4HuggingFaceSearchParams(input map[string]any, defaultSort string) url.Values {
	params := url.Values{
		"sort":      {batch7Lane4StringDefault(input["sort"], defaultSort)},
		"direction": {"-1"},
		"limit":     {strconv.Itoa(batch7Lane4Clamp(intArg(input, "limit", 20), 1, 100))},
	}
	if query := stringArg(input, "query"); query != "" {
		params.Set("search", query)
	}
	if author := stringArg(input, "author"); author != "" {
		params.Set("author", author)
	}
	return params
}

func batch7Lane4HuggingFaceSearchModels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := batch7Lane4HuggingFaceGet(
		ctx, rt, token, "/models", batch7Lane4HuggingFaceSearchParams(input, "downloads"),
	)
	if !ok {
		return data, nil
	}
	models := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data) {
		model := batch7Lane4AsMap(raw)
		models = append(models, map[string]any{
			"id":            batch7Lane4AsString(model["id"]),
			"author":        batch7Lane4AsString(model["author"]),
			"downloads":     batch7Lane4JSONNumber(batch7Lane4IntOr(model["downloads"], 0)),
			"likes":         batch7Lane4JSONNumber(batch7Lane4IntOr(model["likes"], 0)),
			"pipeline_tag":  batch7Lane4AsString(model["pipeline_tag"]),
			"tags":          batch7Lane4FirstN(batch7Lane4StringList(model["tags"]), 10),
			"last_modified": batch7Lane4AsString(model["lastModified"]),
		})
	}
	return map[string]any{"models": models, "count": len(models)}, nil
}

func batch7Lane4HuggingFaceGetModel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	modelID := stringArg(input, "model_id")
	if modelID == "" {
		return map[string]any{"error": "model_id is required"}, nil
	}
	data, ok := batch7Lane4HuggingFaceGet(ctx, rt, token, "/models/"+modelID, nil)
	if !ok {
		return data, nil
	}
	model := batch7Lane4AsMap(data)
	return map[string]any{
		"id":            batch7Lane4AsString(model["id"]),
		"author":        batch7Lane4AsString(model["author"]),
		"downloads":     batch7Lane4JSONNumber(batch7Lane4IntOr(model["downloads"], 0)),
		"likes":         batch7Lane4JSONNumber(batch7Lane4IntOr(model["likes"], 0)),
		"pipeline_tag":  batch7Lane4AsString(model["pipeline_tag"]),
		"tags":          batch7Lane4StringList(model["tags"]),
		"library_name":  batch7Lane4AsString(model["library_name"]),
		"model_index":   model["model-index"],
		"card_data":     model["cardData"],
		"private":       batch7Lane4BoolOr(model["private"], false),
		"last_modified": batch7Lane4AsString(model["lastModified"]),
		"created_at":    batch7Lane4AsString(model["createdAt"]),
	}, nil
}

func batch7Lane4HuggingFaceSearchDatasets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := batch7Lane4HuggingFaceGet(
		ctx, rt, token, "/datasets", batch7Lane4HuggingFaceSearchParams(input, "downloads"),
	)
	if !ok {
		return data, nil
	}
	datasets := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data) {
		dataset := batch7Lane4AsMap(raw)
		datasets = append(datasets, map[string]any{
			"id":            batch7Lane4AsString(dataset["id"]),
			"author":        batch7Lane4AsString(dataset["author"]),
			"downloads":     batch7Lane4JSONNumber(batch7Lane4IntOr(dataset["downloads"], 0)),
			"likes":         batch7Lane4JSONNumber(batch7Lane4IntOr(dataset["likes"], 0)),
			"tags":          batch7Lane4FirstN(batch7Lane4StringList(dataset["tags"]), 10),
			"last_modified": batch7Lane4AsString(dataset["lastModified"]),
		})
	}
	return map[string]any{"datasets": datasets, "count": len(datasets)}, nil
}

func batch7Lane4HuggingFaceGetDataset(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	datasetID := stringArg(input, "dataset_id")
	if datasetID == "" {
		return map[string]any{"error": "dataset_id is required"}, nil
	}
	data, ok := batch7Lane4HuggingFaceGet(ctx, rt, token, "/datasets/"+datasetID, nil)
	if !ok {
		return data, nil
	}
	dataset := batch7Lane4AsMap(data)
	return map[string]any{
		"id":            batch7Lane4AsString(dataset["id"]),
		"author":        batch7Lane4AsString(dataset["author"]),
		"downloads":     batch7Lane4JSONNumber(batch7Lane4IntOr(dataset["downloads"], 0)),
		"likes":         batch7Lane4JSONNumber(batch7Lane4IntOr(dataset["likes"], 0)),
		"tags":          batch7Lane4StringList(dataset["tags"]),
		"card_data":     dataset["cardData"],
		"private":       batch7Lane4BoolOr(dataset["private"], false),
		"last_modified": batch7Lane4AsString(dataset["lastModified"]),
		"created_at":    batch7Lane4AsString(dataset["createdAt"]),
	}, nil
}

func batch7Lane4HuggingFaceSearchSpaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := batch7Lane4HuggingFaceGet(
		ctx, rt, token, "/spaces", batch7Lane4HuggingFaceSearchParams(input, "likes"),
	)
	if !ok {
		return data, nil
	}
	spaces := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(data) {
		space := batch7Lane4AsMap(raw)
		spaces = append(spaces, map[string]any{
			"id":            batch7Lane4AsString(space["id"]),
			"author":        batch7Lane4AsString(space["author"]),
			"likes":         batch7Lane4JSONNumber(batch7Lane4IntOr(space["likes"], 0)),
			"sdk":           batch7Lane4AsString(space["sdk"]),
			"tags":          batch7Lane4FirstN(batch7Lane4StringList(space["tags"]), 10),
			"last_modified": batch7Lane4AsString(space["lastModified"]),
		})
	}
	return map[string]any{"spaces": spaces, "count": len(spaces)}, nil
}

func batch7Lane4HuggingFaceWhoami(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := batch7Lane4HuggingFaceGet(ctx, rt, token, "/whoami-v2", nil)
	if !ok {
		return data, nil
	}
	user := batch7Lane4AsMap(data)
	orgs := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(user["orgs"]) {
		org := batch7Lane4AsMap(raw)
		orgs = append(orgs, map[string]any{
			"name": batch7Lane4AsString(org["name"]),
			"role": batch7Lane4AsString(org["roleInOrg"]),
		})
	}
	return map[string]any{
		"name":       batch7Lane4AsString(user["name"]),
		"fullname":   batch7Lane4AsString(user["fullname"]),
		"email":      batch7Lane4AsString(user["email"]),
		"avatar_url": batch7Lane4AsString(user["avatarUrl"]),
		"orgs":       orgs,
		"type":       batch7Lane4AsString(user["type"]),
	}, nil
}

func batch7Lane4HuggingFaceRunInference(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	modelID := stringArg(input, "model_id")
	if modelID == "" {
		return map[string]any{"error": "model_id is required"}, nil
	}
	inputs := stringArg(input, "inputs")
	if inputs == "" {
		return map[string]any{"error": "inputs is required"}, nil
	}
	payload := map[string]any{"inputs": inputs}
	if raw := stringArg(input, "parameters"); raw != "" {
		parameters, ok := batch7Lane4DecodeAny([]byte(raw))
		if !ok {
			return map[string]any{"error": "parameters must be a valid JSON string"}, nil
		}
		payload["parameters"] = parameters
	}
	data, ok := batch7Lane4HuggingFaceInferencePOST(ctx, rt, token, modelID, payload)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"model_id": modelID,
		"task":     batch7Lane4StringDefault(input["task"], "auto"),
		"output":   data,
	}, nil
}

func batch7Lane4HuggingFaceRunEmbedding(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	modelID := stringArg(input, "model_id")
	if modelID == "" {
		return map[string]any{"error": "model_id is required"}, nil
	}
	inputs := stringArg(input, "inputs")
	if inputs == "" {
		return map[string]any{"error": "inputs is required"}, nil
	}
	data, ok := batch7Lane4HuggingFaceInferencePOST(
		ctx, rt, token, modelID, map[string]any{"inputs": inputs},
	)
	if !ok {
		return data, nil
	}
	embedding := batch7Lane4AsSlice(data)
	dims := 0
	if len(embedding) > 0 {
		switch embedding[0].(type) {
		case float64, int, int64:
			dims = len(embedding)
		}
	}
	return map[string]any{
		"model_id":   modelID,
		"embedding":  embedding,
		"dimensions": dims,
	}, nil
}

func batch7Lane4HuggingFaceListInferenceEndpoints(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := batch7Lane4HuggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	path := "/api/endpoints"
	if namespace := stringArg(input, "namespace"); namespace != "" {
		path += "/" + namespace
	}
	data, ok := batch7Lane4HuggingFaceListEndpoints(ctx, rt, token, path)
	if !ok {
		return data, nil
	}
	items := data
	if object, isObject := data.(map[string]any); isObject {
		if nested, present := object["items"]; present && nested != nil {
			items = nested
		}
	}
	endpoints := make([]map[string]any, 0)
	for _, raw := range batch7Lane4AsSlice(items) {
		endpoint := batch7Lane4AsMap(raw)
		model := ""
		if modelObject, ok := endpoint["model"].(map[string]any); ok {
			model = batch7Lane4AsString(modelObject["repository"])
		} else {
			model = batch7Lane4AsString(endpoint["model"])
		}
		status := ""
		endpointURL := ""
		if statusObject, ok := endpoint["status"].(map[string]any); ok {
			status = batch7Lane4AsString(statusObject["state"])
			endpointURL = batch7Lane4AsString(statusObject["url"])
		} else {
			status = batch7Lane4AsString(endpoint["status"])
		}
		provider := ""
		region := ""
		if providerObject, ok := endpoint["provider"].(map[string]any); ok {
			provider = batch7Lane4AsString(providerObject["vendor"])
			region = batch7Lane4AsString(providerObject["region"])
		}
		endpoints = append(endpoints, map[string]any{
			"name":     batch7Lane4AsString(endpoint["name"]),
			"model":    model,
			"status":   status,
			"url":      endpointURL,
			"type":     batch7Lane4AsString(endpoint["type"]),
			"provider": provider,
			"region":   region,
		})
	}
	return map[string]any{"endpoints": endpoints, "count": len(endpoints)}, nil
}

// ------------------------------------------------------------------ vision ---

var batch7Lane4VisionTool = &Tool{
	ID:          "vision_tool",
	Name:        "vision",
	DisplayName: "Google Cloud Vision",
	Summary:     "Google Cloud Vision image analysis over the REST API (9 actions).",
	Actions: []Action{
		{
			Name:     "vision_detect_labels",
			Summary:  "Detect labels (objects, scenes, activities) in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_labels":   paramDefault("integer", 10),
			},
		},
		{
			Name:     "vision_detect_text",
			Summary:  "Extract text from an image (OCR).",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
		{
			Name:     "vision_detect_faces",
			Summary:  "Detect faces and emotions in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_faces":    paramDefault("integer", 10),
			},
		},
		{
			Name:     "vision_localize_objects",
			Summary:  "Detect objects with bounding box coordinates in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_objects":  paramDefault("integer", 10),
			},
		},
		{
			Name:     "vision_detect_logos",
			Summary:  "Detect brand logos in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_logos":    paramDefault("integer", 5),
			},
		},
		{
			Name:     "vision_detect_landmarks",
			Summary:  "Detect famous landmarks in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source":  param("string"),
				"max_landmarks": paramDefault("integer", 5),
			},
		},
		{
			Name:     "vision_image_properties",
			Summary:  "Get image properties including dominant colors and crop hints.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
		{
			Name:     "vision_web_detection",
			Summary:  "Find similar images and web references for an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
		{
			Name:     "vision_safe_search",
			Summary:  "Detect inappropriate content in an image (safe search).",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
	},
}

func batch7Lane4VisionImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"vision_detect_labels":    batch7Lane4VisionDetectLabels,
		"vision_detect_text":      batch7Lane4VisionDetectText,
		"vision_detect_faces":     batch7Lane4VisionDetectFaces,
		"vision_localize_objects": batch7Lane4VisionLocalizeObjects,
		"vision_detect_logos":     batch7Lane4VisionDetectLogos,
		"vision_detect_landmarks": batch7Lane4VisionDetectLandmarks,
		"vision_image_properties": batch7Lane4VisionImageProperties,
		"vision_web_detection":    batch7Lane4VisionWebDetection,
		"vision_safe_search":      batch7Lane4VisionSafeSearch,
	}
}

// batch7Lane4VisionAuth returns the API key or the upstream auth error.
func batch7Lane4VisionAuth(rt *Runtime) (string, map[string]any) {
	key := batch7Lane4ModuleToken(rt, "vision_tool")
	if key == "" {
		return "", map[string]any{
			"error": "GOOGLE_CLOUD_VISION_API_KEY not configured",
			"help":  "Get an API key at https://console.cloud.google.com/apis/credentials",
		}
	}
	return key, nil
}

// batch7Lane4VisionImage builds the `image` member of one annotate request
// from a public URL or a sandboxed local file.
func batch7Lane4VisionImage(rt *Runtime, source string) (map[string]any, map[string]any) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return map[string]any{"source": map[string]any{"imageUri": source}}, nil
	}
	if source == "" {
		return nil, map[string]any{"error": "Not a file: " + source}
	}
	path, err := rt.resolveSafePath(source)
	if err != nil {
		return nil, map[string]any{"error": "File not found: " + source}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, map[string]any{"error": "File not found: " + source}
	}
	if info.IsDir() {
		return nil, map[string]any{"error": "Not a file: " + source}
	}
	if info.Size() > batch7Lane4VisionMaxBytes {
		sizeMB := float64(info.Size()) / (1024 * 1024)
		return nil, map[string]any{
			"error": fmt.Sprintf("File exceeds 10MB limit (%.1fMB)", sizeMB),
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, map[string]any{"error": "Failed to read file: " + err.Error()}
	}
	return map[string]any{
		"content": base64.StdEncoding.EncodeToString(content),
	}, nil
}

// batch7Lane4VisionFeature builds the single-feature list each detector sends.
func batch7Lane4VisionFeature(kind string, maxResults int) []any {
	feature := map[string]any{"type": kind}
	if maxResults > 0 {
		feature["maxResults"] = maxResults
	}
	return []any{feature}
}

// batch7Lane4VisionCall posts one annotate request and maps HTTP failures the
// way _handle_response does. The boolean reports success.
func batch7Lane4VisionCall(
	ctx context.Context,
	rt *Runtime,
	key string,
	image map[string]any,
	features []any,
) (map[string]any, bool) {
	base := batch7Lane4ModuleURL(rt, "vision_tool")
	if base == "" {
		base = batch7Lane4VisionURL
	}
	query := url.Values{"key": {key}}
	reply, err := batch7Lane4Do(
		ctx, rt, http.MethodPost, base+"?"+query.Encode(),
		map[string]string{"Content-Type": "application/json"},
		map[string]any{"requests": []any{map[string]any{"image": image, "features": features}}},
	)
	if err != nil {
		if batch7Lane4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusBadRequest:
		return map[string]any{
			"error": "Invalid request. Check image format and size.",
		}, false
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid API key"}, false
	case http.StatusForbidden:
		return map[string]any{
			"error": "API key not authorized. Enable Vision API in Google Cloud Console.",
		}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limit exceeded. Try again later."}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf("Vision API error (HTTP %d)", reply.Status),
		}, false
	}
	payload, ok := batch7Lane4DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Vision API returned invalid JSON"}, false
	}
	responses := batch7Lane4AsSlice(payload["responses"])
	if len(responses) == 0 {
		return map[string]any{"error": "Empty response from API"}, false
	}
	result := batch7Lane4AsMap(responses[0])
	if problem, present := result["error"].(map[string]any); present {
		message := batch7Lane4AsString(problem["message"])
		if message == "" {
			message = "Unknown API error"
		}
		return map[string]any{"error": message}, false
	}
	return result, true
}

// batch7Lane4VisionRun loads the image, calls the API and hands the first
// response object to the detector projection.
func batch7Lane4VisionRun(
	ctx context.Context,
	rt *Runtime,
	input map[string]any,
	features []any,
	project func(map[string]any) any,
) (any, error) {
	key, problem := batch7Lane4VisionAuth(rt)
	if problem != nil {
		return problem, nil
	}
	image, imageProblem := batch7Lane4VisionImage(rt, stringArg(input, "image_source"))
	if imageProblem != nil {
		return imageProblem, nil
	}
	result, ok := batch7Lane4VisionCall(ctx, rt, key, image, features)
	if !ok {
		return result, nil
	}
	return project(result), nil
}

// batch7Lane4VisionVertices reads `node.poly.vertices` (defaulting to []).
func batch7Lane4VisionVertices(node map[string]any, poly, vertices string) []any {
	container, _ := node[poly].(map[string]any)
	return batch7Lane4AnyList(container[vertices])
}

func batch7Lane4VisionDetectLabels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxLabels := batch7Lane4Clamp(intArg(input, "max_labels", 10), 1, 100)
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("LABEL_DETECTION", maxLabels),
		func(result map[string]any) any {
			labels := make([]map[string]any, 0)
			for _, raw := range batch7Lane4AsSlice(result["labelAnnotations"]) {
				label := batch7Lane4AsMap(raw)
				labels = append(labels, map[string]any{
					"description": batch7Lane4AsString(label["description"]),
					"score":       batch7Lane4Round3(batch7Lane4FloatOr(label["score"], 0)),
				})
			}
			return map[string]any{"labels": labels}
		},
	)
}

func batch7Lane4VisionDetectText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("TEXT_DETECTION", 0),
		func(result map[string]any) any {
			annotations := batch7Lane4AsSlice(result["textAnnotations"])
			if len(annotations) == 0 {
				return map[string]any{"text": "", "blocks": []any{}}
			}
			blocks := make([]map[string]any, 0, len(annotations)-1)
			for _, raw := range annotations[1:] {
				annotation := batch7Lane4AsMap(raw)
				blocks = append(blocks, map[string]any{
					"text":   batch7Lane4AsString(annotation["description"]),
					"bounds": batch7Lane4VisionVertices(annotation, "boundingPoly", "vertices"),
				})
			}
			return map[string]any{
				"text":   batch7Lane4AsString(batch7Lane4AsMap(annotations[0])["description"]),
				"blocks": blocks,
			}
		},
	)
}

func batch7Lane4VisionDetectFaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxFaces := batch7Lane4Clamp(intArg(input, "max_faces", 10), 1, 100)
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("FACE_DETECTION", maxFaces),
		func(result map[string]any) any {
			faces := make([]map[string]any, 0)
			for _, raw := range batch7Lane4AsSlice(result["faceAnnotations"]) {
				face := batch7Lane4AsMap(raw)
				faces = append(faces, map[string]any{
					"joy":        batch7Lane4StringDefault(face["joyLikelihood"], "UNKNOWN"),
					"sorrow":     batch7Lane4StringDefault(face["sorrowLikelihood"], "UNKNOWN"),
					"anger":      batch7Lane4StringDefault(face["angerLikelihood"], "UNKNOWN"),
					"surprise":   batch7Lane4StringDefault(face["surpriseLikelihood"], "UNKNOWN"),
					"confidence": batch7Lane4Round3(batch7Lane4FloatOr(face["detectionConfidence"], 0)),
					"bounds":     batch7Lane4VisionVertices(face, "boundingPoly", "vertices"),
				})
			}
			return map[string]any{"faces": faces}
		},
	)
}

func batch7Lane4VisionLocalizeObjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxObjects := batch7Lane4Clamp(intArg(input, "max_objects", 10), 1, 100)
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("OBJECT_LOCALIZATION", maxObjects),
		func(result map[string]any) any {
			objects := make([]map[string]any, 0)
			for _, raw := range batch7Lane4AsSlice(result["localizedObjectAnnotations"]) {
				object := batch7Lane4AsMap(raw)
				objects = append(objects, map[string]any{
					"name":   batch7Lane4AsString(object["name"]),
					"score":  batch7Lane4Round3(batch7Lane4FloatOr(object["score"], 0)),
					"bounds": batch7Lane4VisionVertices(object, "boundingPoly", "normalizedVertices"),
				})
			}
			return map[string]any{"objects": objects}
		},
	)
}

func batch7Lane4VisionDetectLogos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxLogos := batch7Lane4Clamp(intArg(input, "max_logos", 5), 1, 20)
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("LOGO_DETECTION", maxLogos),
		func(result map[string]any) any {
			logos := make([]map[string]any, 0)
			for _, raw := range batch7Lane4AsSlice(result["logoAnnotations"]) {
				logo := batch7Lane4AsMap(raw)
				logos = append(logos, map[string]any{
					"description": batch7Lane4AsString(logo["description"]),
					"score":       batch7Lane4Round3(batch7Lane4FloatOr(logo["score"], 0)),
				})
			}
			return map[string]any{"logos": logos}
		},
	)
}

func batch7Lane4VisionDetectLandmarks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxLandmarks := batch7Lane4Clamp(intArg(input, "max_landmarks", 5), 1, 20)
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("LANDMARK_DETECTION", maxLandmarks),
		func(result map[string]any) any {
			landmarks := make([]map[string]any, 0)
			for _, raw := range batch7Lane4AsSlice(result["landmarkAnnotations"]) {
				landmark := batch7Lane4AsMap(raw)
				location := map[string]any{}
				if locations := batch7Lane4AsSlice(landmark["locations"]); len(locations) > 0 {
					latLng := batch7Lane4AsMap(batch7Lane4AsMap(locations[0])["latLng"])
					location = map[string]any{
						"latitude":  latLng["latitude"],
						"longitude": latLng["longitude"],
					}
				}
				landmarks = append(landmarks, map[string]any{
					"description": batch7Lane4AsString(landmark["description"]),
					"score":       batch7Lane4Round3(batch7Lane4FloatOr(landmark["score"], 0)),
					"location":    location,
				})
			}
			return map[string]any{"landmarks": landmarks}
		},
	)
}

func batch7Lane4VisionImageProperties(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane4VisionRun(
		ctx, rt, input,
		[]any{
			map[string]any{"type": "IMAGE_PROPERTIES"},
			map[string]any{"type": "CROP_HINTS"},
		},
		func(result map[string]any) any {
			colors := make([]map[string]any, 0)
			dominant := batch7Lane4AsMap(
				batch7Lane4AsMap(result["imagePropertiesAnnotation"])["dominantColors"],
			)
			for index, raw := range batch7Lane4AsSlice(dominant["colors"]) {
				if index >= 5 {
					break
				}
				color := batch7Lane4AsMap(raw)
				rgb := batch7Lane4AsMap(color["color"])
				colors = append(colors, map[string]any{
					"red":            int(batch7Lane4FloatOr(rgb["red"], 0)),
					"green":          int(batch7Lane4FloatOr(rgb["green"], 0)),
					"blue":           int(batch7Lane4FloatOr(rgb["blue"], 0)),
					"score":          batch7Lane4Round3(batch7Lane4FloatOr(color["score"], 0)),
					"pixel_fraction": batch7Lane4Round3(batch7Lane4FloatOr(color["pixelFraction"], 0)),
				})
			}
			cropHints := make([]map[string]any, 0)
			hints := batch7Lane4AsMap(result["cropHintsAnnotation"])
			for _, raw := range batch7Lane4AsSlice(hints["cropHints"]) {
				hint := batch7Lane4AsMap(raw)
				cropHints = append(cropHints, map[string]any{
					"bounds":     batch7Lane4VisionVertices(hint, "boundingPoly", "vertices"),
					"confidence": batch7Lane4Round3(batch7Lane4FloatOr(hint["confidence"], 0)),
				})
			}
			return map[string]any{"colors": colors, "crop_hints": cropHints}
		},
	)
}

func batch7Lane4VisionWebDetection(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("WEB_DETECTION", 0),
		func(result map[string]any) any {
			web := batch7Lane4AsMap(result["webDetection"])
			webEntities := make([]map[string]any, 0)
			for index, raw := range batch7Lane4AsSlice(web["webEntities"]) {
				if index >= 10 {
					break
				}
				entity := batch7Lane4AsMap(raw)
				webEntities = append(webEntities, map[string]any{
					"description": batch7Lane4AsString(entity["description"]),
					"score":       batch7Lane4Round3(batch7Lane4FloatOr(entity["score"], 0)),
				})
			}
			similarImages := make([]string, 0)
			for index, raw := range batch7Lane4AsSlice(web["visuallySimilarImages"]) {
				if index >= 5 {
					break
				}
				similarImages = append(
					similarImages, batch7Lane4AsString(batch7Lane4AsMap(raw)["url"]),
				)
			}
			pages := make([]map[string]any, 0)
			for index, raw := range batch7Lane4AsSlice(web["pagesWithMatchingImages"]) {
				if index >= 5 {
					break
				}
				page := batch7Lane4AsMap(raw)
				pages = append(pages, map[string]any{
					"url":   batch7Lane4AsString(page["url"]),
					"title": batch7Lane4AsString(page["pageTitle"]),
				})
			}
			return map[string]any{
				"web_entities":     webEntities,
				"similar_images":   similarImages,
				"pages_with_image": pages,
			}
		},
	)
}

func batch7Lane4VisionSafeSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch7Lane4VisionRun(
		ctx, rt, input,
		batch7Lane4VisionFeature("SAFE_SEARCH_DETECTION", 0),
		func(result map[string]any) any {
			safe := batch7Lane4AsMap(result["safeSearchAnnotation"])
			return map[string]any{
				"adult":    batch7Lane4StringDefault(safe["adult"], "UNKNOWN"),
				"spoof":    batch7Lane4StringDefault(safe["spoof"], "UNKNOWN"),
				"medical":  batch7Lane4StringDefault(safe["medical"], "UNKNOWN"),
				"violence": batch7Lane4StringDefault(safe["violence"], "UNKNOWN"),
				"racy":     batch7Lane4StringDefault(safe["racy"], "UNKNOWN"),
			}
		},
	)
}

// ------------------------------------------------------------ tiny helpers ---

// batch7Lane4StringDefault reads a string value with a fallback.
func batch7Lane4StringDefault(value any, fallback string) string {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return fallback
}

// batch7Lane4AnyOrMap normalises an optional JSON object to a map.
func batch7Lane4AnyOrMap(value any) map[string]any {
	if object, ok := value.(map[string]any); ok && object != nil {
		return object
	}
	return map[string]any{}
}

// batch7Lane4ToStrings renders a decoded JSON array as []string.
func batch7Lane4ToStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, batch7Lane4StringID(value))
	}
	return out
}
