package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func configureHome(t *testing.T, config string) {
	t.Helper()
	dir := t.TempDir()
	if config != "" {
		if err := os.WriteFile(filepath.Join(dir, "configuration.json"), []byte(config), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	t.Setenv("HIVE_HOME", dir)
}

func configureEndpoint(t *testing.T, name, base string) {
	t.Helper()
	configureHome(t, fmt.Sprintf(`{"model_endpoints":{%q:{"api_base":%q}}}`, name, base))
}

func modelsServer(t *testing.T, status int, body string, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if r.Header.Get("Authorization") != "Bearer no-key" {
			t.Errorf("Authorization = %q, want Bearer no-key", r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// Parity-exempt: core/framework/llm/endpoints.py has no dedicated Python test
func TestValidEndpointName(t *testing.T) {
	for _, name := range []string{"magpie", "work", "a", "a_b-c9"} {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false", name)
		}
	}
	invalid := []string{"", "Work", "-bad", "_bad", "has space", strings.Repeat("a", 41)}
	for _, name := range invalid {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true", name)
		}
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestEndpointsAlwaysIncludesMagpie(t *testing.T) {
	configureHome(t, "")
	endpoints := Endpoints()
	magpie, ok := endpoints[Magpie]
	if !ok {
		t.Fatal("magpie missing")
	}
	if magpie.APIBase != MagpieAPIBase || !magpie.Builtin {
		t.Errorf("magpie = %+v", magpie)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestEndpointsReadsConfiguration(t *testing.T) {
	configureHome(t, `{
		"model_endpoints": {
			"work": {"api_base": "  http://127.0.0.1:8080/v1/  "},
			"Bad": {"api_base": "http://bad.test/v1"},
			"blank": {"api_base": "   "},
			"notbase": {"api_base": 7},
			"notdict": "http://nope.test",
			"localhost": {"api_base": "/"}
		}
	}`)
	endpoints := Endpoints()
	work, ok := endpoints["work"]
	if !ok {
		t.Fatal("work missing")
	}
	if work.APIBase != "http://127.0.0.1:8080/v1" || work.Builtin {
		t.Errorf("work = %+v", work)
	}
	if _, ok := endpoints["Bad"]; ok {
		t.Error("uppercase name should be rejected")
	}
	if _, ok := endpoints["blank"]; ok {
		t.Error("blank api_base should be rejected")
	}
	if _, ok := endpoints["notbase"]; ok {
		t.Error("non-string api_base should be rejected")
	}
	if _, ok := endpoints["notdict"]; ok {
		t.Error("non-dict entry should be rejected")
	}
	if got, ok := endpoints["localhost"]; !ok || got.APIBase != "" {
		t.Errorf("slash-only base = %+v, %v", got, ok)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestConfiguredMagpieStaysBuiltin(t *testing.T) {
	configureHome(t, `{"model_endpoints":{"magpie":{"api_base":"http://127.0.0.1:9999/v1/"}}}`)
	magpie, ok := Get(Magpie)
	if !ok {
		t.Fatal("magpie missing")
	}
	if magpie.APIBase != "http://127.0.0.1:9999/v1" || !magpie.Builtin {
		t.Errorf("magpie = %+v", magpie)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestConfigWithBOMAndBadJSON(t *testing.T) {
	configureHome(t, "\xef\xbb\xbf"+`{"model_endpoints":{"work":{"api_base":"http://bom.test/v1"}}}`)
	if got, ok := Get("work"); !ok || got.APIBase != "http://bom.test/v1" {
		t.Errorf("BOM config endpoint = %+v, %v", got, ok)
	}
	configureHome(t, "{not json")
	if _, ok := Get("work"); ok {
		t.Error("broken config should fall back to builtins only")
	}
	endpoints := Endpoints()
	if len(endpoints) != 1 {
		t.Errorf("endpoints = %v, want only magpie", endpoints)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestCredentialIDAndKeyFor(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	if got := CredentialID("work"); got != "endpoint_work" {
		t.Errorf("credential id = %q", got)
	}
	if got := KeyFor(Magpie); got != "magpie" {
		t.Errorf("magpie key = %q", got)
	}
	if got := KeyFor("work"); got != "no-key" {
		t.Errorf("keyless endpoint key = %q", got)
	}
	if _, ok := StoredKey("work"); ok {
		t.Error("stored key should be absent without HIVE_CREDENTIAL_KEY")
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestListModelsParsesSortsAndFilters(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	body := `{"data":[
		{"id":"Zeta","name":" Zeta label ","context_length":128000},
		{"id":"alpha","display_name":"Alpha","max_model_len":4096},
		{"id":"bad","context_length":true},
		{"id":"float","context_length":5.0},
		{"id":"neg","context_window":-1},
		{"id":"strctx","context_window":"7"},
		"bare-model",
		3,
		{"name":"no id"}
	]}`
	var hits int32
	server := modelsServer(t, http.StatusOK, body, &hits)
	configureEndpoint(t, "work", server.URL+"/v1/")
	t.Cleanup(func() { Forget("work") })

	models, err := ListModels("work", false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model["id"].(string))
	}
	want := []string{"alpha", "bad", "bare-model", "float", "neg", "strctx", "Zeta"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	byID := map[string]map[string]any{}
	for _, model := range models {
		byID[model["id"].(string)] = model
	}
	if !reflect.DeepEqual(byID["Zeta"], map[string]any{"id": "Zeta", "label": "Zeta label", "max_context_tokens": 128000}) {
		t.Errorf("Zeta = %v", byID["Zeta"])
	}
	if !reflect.DeepEqual(byID["alpha"], map[string]any{"id": "alpha", "label": "Alpha", "max_context_tokens": 4096}) {
		t.Errorf("alpha = %v", byID["alpha"])
	}
	for _, id := range []string{"bad", "float", "neg", "strctx", "bare-model"} {
		if !reflect.DeepEqual(byID[id], map[string]any{"id": id, "label": nil, "max_context_tokens": nil}) {
			t.Errorf("%s = %v", id, byID[id])
		}
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestListModelsAcceptsBareList(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	server := modelsServer(t, http.StatusOK, `[{"id":"b"},{"id":"a"}]`, nil)
	configureEndpoint(t, "barelist", server.URL+"/v1")
	t.Cleanup(func() { Forget("barelist") })
	models, err := ListModels("barelist", false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 2 || models[0]["id"] != "a" || models[1]["id"] != "b" {
		t.Errorf("models = %v", models)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestListModelsErrors(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"refused", http.StatusUnauthorized, "", "refused the key (HTTP 401)"},
		{"forbidden", http.StatusForbidden, "", "refused the key (HTTP 403)"},
		{"server", http.StatusInternalServerError, "", "answered HTTP 500"},
		{"notjson", http.StatusOK, "not json", "didn't answer with JSON"},
		{"notlist", http.StatusOK, `{"data":5}`, "didn't answer with a list of models"},
		{"scalar", http.StatusOK, `5`, "didn't answer with a list of models"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := modelsServer(t, c.status, c.body, nil)
			configureEndpoint(t, c.name+"ep", server.URL+"/v1")
			t.Cleanup(func() { Forget(c.name + "ep") })
			_, err := ListModels(c.name+"ep", false)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want contains %q", err, c.want)
			}
		})
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestListModelsUnknownEndpointMessage(t *testing.T) {
	configureHome(t, "")
	_, err := ListModels("nope", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "There's no endpoint 'nope'" {
		t.Errorf("error = %q", got)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestUnreachableEndpointNamesPythonErrorClass(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	configureEndpoint(t, "unreachable", "http://127.0.0.1:1/v1")
	original := endpointHTTPGet
	defer func() { endpointHTTPGet = original }()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"connect", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, "Can't reach http://127.0.0.1:1/v1/models: ConnectError"},
		{"timeout", timeoutError{}, "Can't reach http://127.0.0.1:1/v1/models: TimeoutException"},
		{"other", errors.New("boom"), "Can't reach http://127.0.0.1:1/v1/models: HTTPError"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			endpointHTTPGet = func(_ context.Context, _ string, _ map[string]string) (*http.Response, error) {
				return nil, c.err
			}
			_, err := ListModels("unreachable", true)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := err.Error(); got != c.want {
				t.Errorf("error = %q, want %q", got, c.want)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timed out" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// Parity-exempt: see TestValidEndpointName.
func TestListModelsCacheFreshAndForget(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	var hits int32
	server := modelsServer(t, http.StatusOK, `[{"id":"one"}]`, &hits)
	configureEndpoint(t, "cachetest", server.URL+"/v1")
	t.Cleanup(func() { Forget("cachetest") })
	Forget("cachetest")

	if _, err := ListModels("cachetest", false); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := ListModels("cachetest", false); err != nil {
		t.Fatalf("cached: %v", err)
	}
	if hits != 1 {
		t.Errorf("hits after cached read = %d, want 1", hits)
	}
	if _, err := ListModels("cachetest", true); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if hits != 2 {
		t.Errorf("hits after fresh read = %d, want 2", hits)
	}
	Forget("cachetest")
	if _, err := ListModels("cachetest", false); err != nil {
		t.Fatalf("after forget: %v", err)
	}
	if hits != 3 {
		t.Errorf("hits after forget = %d, want 3", hits)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestListModelsCacheExpiresAfterTTL(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	var hits int32
	server := modelsServer(t, http.StatusOK, `[{"id":"one"}]`, &hits)
	configureEndpoint(t, "ttltest", server.URL+"/v1")
	t.Cleanup(func() { Forget("ttltest") })
	Forget("ttltest")

	original := endpointClock
	defer func() { endpointClock = original }()
	now := 1000.0
	endpointClock = func() float64 { return now }

	if _, err := ListModels("ttltest", false); err != nil {
		t.Fatalf("first: %v", err)
	}
	now += 59
	if _, err := ListModels("ttltest", false); err != nil {
		t.Fatalf("within TTL: %v", err)
	}
	if hits != 1 {
		t.Errorf("hits within TTL = %d, want 1", hits)
	}
	now += 2
	if _, err := ListModels("ttltest", false); err != nil {
		t.Fatalf("after TTL: %v", err)
	}
	if hits != 2 {
		t.Errorf("hits after TTL = %d, want 2", hits)
	}
}

// Parity-exempt: see TestValidEndpointName.
func TestProbeReportsReachability(t *testing.T) {
	configureHome(t, "")
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	var hits int32
	okServer := modelsServer(t, http.StatusOK, `[{"id":"one"}]`, &hits)
	configureEndpoint(t, "probeok", okServer.URL+"/v1")
	t.Cleanup(func() { Forget("probeok") })
	Forget("probeok")
	if !Probe("probeok") {
		t.Error("probe should succeed")
	}
	if _, err := ListModels("probeok", false); err != nil {
		t.Fatalf("list after probe: %v", err)
	}
	if hits != 1 {
		t.Errorf("probe should warm the cache; hits = %d", hits)
	}

	badServer := modelsServer(t, http.StatusUnauthorized, "", nil)
	configureEndpoint(t, "probebad", badServer.URL+"/v1")
	if Probe("probebad") {
		t.Error("probe should fail on 401")
	}
	if Probe("missing") {
		t.Error("probe of unknown endpoint should fail")
	}
}
