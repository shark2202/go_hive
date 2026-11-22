// Batch 5 lane 2 of the integration port: database modules.
//
// Modules and transports (32 actions total):
//   - postgres (8 actions, upstream postgres_tool): PostgreSQL v3
//     frontend/backend protocol implemented directly in pgwire.go.
//     No driver dependency is introduced.
//   - redis (11 actions, upstream redis_tool): RESP2 commands written directly
//     on a TCP or TLS connection.
//   - mongodb (6 actions, upstream mongodb_tool): the MongoDB Atlas Data API
//     over HTTP. The upstream Python module also uses HTTP, not the Mongo wire
//     protocol, so no driver is needed here either.
//   - supabase (7 actions, upstream supabase_tool): PostgREST, GoTrue and Edge
//     Functions over HTTP.
//
// Every action keeps the upstream name, parameter names/defaults and response
// shape. Tool-level failures are returned as {"error": ...} values, matching
// the rest of the port. Credentials resolve from WithCredential or the module
// environment variables, and HTTP endpoints can be redirected with WithBaseURL
// so tests run against local fake servers.
//
// Deliberate limits:
//   - postgres: no connection pool (one connection per action); the SQL guard
//     plus SET default_transaction_read_only enforce read-only; statement
//     timeout is 3s; authentication supports trust, cleartext, MD5 and
//     SCRAM-SHA-256; parameters are bound as text (JSON objects/arrays must be
//     passed as PostgreSQL literal strings); TLS is verified only for
//     sslmode=verify-ca/verify-full.
//   - redis: no pool, no pub/sub subscriptions (publish only), RESP2 only,
//     rediss:// verifies the server certificate against the system roots.
//   - mongodb: needs an Atlas Data API compatible endpoint; the upstream
//     module documents the same EOL caveat.
//   - supabase: anon key only, like upstream; no service-role switching.
package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// databaseMaxBody caps response bodies for the HTTP-backed modules.
	databaseMaxBody = 8 << 20
	// maxError caps provider error text embedded in responses,
	// mirroring the upstream resp.text[:500] slicing.
	maxError = 500
)

// newBatch5Lane2Registry builds a registry containing only the batch-5 lane-2
// tools, sharing the Runtime surface used by the rest of the port.
func NewDatabasesRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: databaseModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := newRegistry(runtime)
	for _, tool := range []*Tool{
		postgresTool,
		redisTool,
		mongoTool,
		supabaseTool,
	} {
		registry.AddTool(tool)
	}
	for action, impl := range postgresImpls() {
		registry.AddImpl("postgres", action, impl)
	}
	for action, impl := range redisImpls() {
		registry.AddImpl("redis", action, impl)
	}
	for action, impl := range mongoImpls() {
		registry.AddImpl("mongodb", action, impl)
	}
	for action, impl := range supabaseImpls() {
		registry.AddImpl("supabase", action, impl)
	}
	return registry
}

// databaseModules declares the credential variables for the four modules.
// BaseURL stays empty for mongodb/supabase because their endpoints come from
// environment variables (MONGODB_DATA_API_URL / SUPABASE_URL); tests override
// them with WithBaseURL.
func databaseModules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"postgres_tool": {ID: "postgres_tool", EnvVars: []string{"DATABASE_URL"}},
		"redis_tool":    {ID: "redis_tool", EnvVars: []string{"REDIS_URL"}},
		"mongodb_tool":  {ID: "mongodb_tool", EnvVars: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"}},
		"supabase_tool": {ID: "supabase_tool", EnvVars: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"}},
	}
}

// ------------------------------------------------------------ shared bits ---

// getenv resolves an environment variable through the runtime
// lookup so tests can inject credentials without touching the process env.
func getenv(rt *Runtime, name string) string {
	if rt != nil && rt.Getenv != nil {
		return strings.TrimSpace(rt.Getenv(name))
	}
	return strings.TrimSpace(os.Getenv(name))
}

// configuredToken returns the WithCredential value when present,
// otherwise the named environment variable.
func configuredToken(rt *Runtime, moduleID, envName string) string {
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil && strings.TrimSpace(cfg.ConfiguredToken()) != "" {
			return strings.TrimSpace(cfg.ConfiguredToken())
		}
	}
	return getenv(rt, envName)
}

// moduleBaseURL returns WithBaseURL when set, otherwise the named
// environment variable.
func databaseBaseURL(rt *Runtime, moduleID, envName string) string {
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			if base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"); base != "" {
				return base
			}
		}
	}
	return strings.TrimRight(getenv(rt, envName), "/")
}

// err builds the upstream-style error map.
func databaseErr(message string) map[string]any {
	return map[string]any{"error": message}
}

// truncate caps text at limit runes, like Python slicing.
func truncate(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// objectArg normalizes a JSON object parameter. The agent runtime
// decodes JSON objects into map[string]any; tests sometimes pass
// map[string]string, so both are accepted.
func objectArg(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	default:
		return nil
	}
}

// jsonParam decodes a JSON string parameter, returning the upstream
// style error map when the text is invalid.
func jsonParam(raw, label string) (any, map[string]any) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, databaseErr(label + " must be valid JSON")
	}
	return value, nil
}

// databaseLen mirrors Python len() for the JSON shapes these modules return.
func databaseLen(value any) int {
	switch typed := value.(type) {
	case []any:
		return len(typed)
	case map[string]any:
		return len(typed)
	case string:
		return len(typed)
	default:
		return 0
	}
}

// databaseResponse is one buffered HTTP response.
type databaseResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

// do performs one HTTP request through the runtime transport and
// buffers the response body up to databaseMaxBody.
func do(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (*databaseResponse, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close
	raw, err := io.ReadAll(io.LimitReader(resp.Body, databaseMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > databaseMaxBody {
		return nil, fmt.Errorf("response body exceeds %d bytes", databaseMaxBody)
	}
	return &databaseResponse{Status: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}

// decodeJSON decodes a buffered response body.
func decodeJSON(raw []byte) (any, error) {
	var value any
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty response body")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
