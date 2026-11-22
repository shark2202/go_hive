package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type fakeModelKeyStore struct {
	saved map[string]string
}

func newFakeModelKeyStore() *fakeModelKeyStore {
	return &fakeModelKeyStore{saved: map[string]string{}}
}

func (s *fakeModelKeyStore) SaveCredential(credential *credentials.CredentialObject) error {
	value, _ := credential.GetKeyValue("api_key")
	s.saved[credential.ID] = value
	return nil
}

func (s *fakeModelKeyStore) DeleteCredential(credentialID string) (bool, error) {
	_, found := s.saved[credentialID]
	delete(s.saved, credentialID)
	return found, nil
}

type modelReconfigureCall struct {
	Model   string
	APIKey  *string
	APIBase *string
	Extra   any
}

type fakeModelProvider struct {
	calls []modelReconfigureCall
}

func (p *fakeModelProvider) Reconfigure(fullModel string, apiKey, apiBase *string, extraBody any) {
	p.calls = append(p.calls, modelReconfigureCall{Model: fullModel, APIKey: apiKey, APIBase: apiBase, Extra: extraBody})
}

func modelTestHome(t *testing.T) hivehome.Home {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HIVE_HOME", root)
	llm.Forget(llm.Magpie)
	llm.Forget("ollama")
	t.Cleanup(func() {
		llm.Forget(llm.Magpie)
		llm.Forget("ollama")
	})
	return hivehome.Home{Root: root}
}

func writeModelTestConfig(t *testing.T, home hivehome.Home, config map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(home.ConfigurationFile()), 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readModelTestConfig(t *testing.T, home hivehome.Home) map[string]any {
	t.Helper()
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return config
}

func modelTestDeps(home hivehome.Home, store *fakeModelKeyStore) ModelRouteDeps {
	deps := ModelRouteDeps{Home: home}
	if store != nil {
		deps.Credentials = func() ModelCredentialStore { return store }
		deps.StoredKey = func(name string) (string, bool) {
			value, found := store.saved[llm.CredentialID(name)]
			return value, found
		}
		deps.EndpointKey = func(name string) string {
			if value, found := store.saved[llm.CredentialID(name)]; found {
				return value
			}
			if name == llm.Magpie {
				return "magpie"
			}
			return "no-key"
		}
	}
	return deps
}

func modelTestMux(deps ModelRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterModelRoutes(mux, deps)
	return mux
}

func modelRequest(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(method, target, reader))
	return recorder
}

func decodeModelBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
	return body
}

var modelGatewayModels = []map[string]any{
	{"id": "kimi/k3", "context_length": 262144, "magpie_label": "Kimi K3 · Moonshot"},
	{"id": "deepseek/deepseek-v4-pro"},
	{"id": "group/opus-anywhere"},
}

func newModelGateway(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	seen := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		*seen = append(*seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": modelGatewayModels})
	}))
	t.Cleanup(server.Close)
	return server, seen
}

func gatewayBase(server *httptest.Server) string { return server.URL + "/v1" }

// Parity: framework/server/tests/test_models_page.py::test_magpie_is_there_unconfigured_and_its_models_are_listed_live
func TestModelsOverviewListsMagpieAndItsModels(t *testing.T) {
	home := modelTestHome(t)
	gateway, seen := newModelGateway(t)
	base := gatewayBase(gateway)
	writeModelTestConfig(t, home, map[string]any{
		"model_endpoints": map[string]any{"magpie": map[string]any{"api_base": base}},
		"llm":             map[string]any{"provider": "deepseek", "model": "deepseek-chat"},
	})
	t.Setenv("OPENAI_API_KEY", "sk-ambient")

	store := newFakeModelKeyStore()
	mux := modelTestMux(modelTestDeps(home, store))

	overview := modelRequest(t, mux, http.MethodGet, "/api/models/overview", "")
	if overview.Code != http.StatusOK {
		t.Fatalf("overview status = %d (%s)", overview.Code, overview.Body.String())
	}
	body := decodeModelBody(t, overview)
	endpoints, _ := body["endpoints"].([]any)
	if len(endpoints) != 1 {
		t.Fatalf("endpoints = %#v", body["endpoints"])
	}
	row := endpoints[0].(map[string]any)
	expected := map[string]any{"name": "magpie", "api_base": base, "builtin": true, "has_key": false, "running": true}
	for key, want := range expected {
		if row[key] != want {
			t.Fatalf("endpoint[%s] = %#v, want %#v (row %#v)", key, row[key], want, row)
		}
	}
	slots := body["slots"].(map[string]any)
	llmSlot, _ := slots["llm"].(map[string]any)
	if llmSlot["source"] != "catalog" {
		t.Fatalf("llm slot = %#v", slots["llm"])
	}
	if slots["worker_llm"] != nil {
		t.Fatalf("worker_llm = %#v, want null", slots["worker_llm"])
	}
	subscriptions, _ := body["subscriptions"].([]any)
	if len(subscriptions) == 0 {
		t.Fatalf("subscriptions = %#v", body["subscriptions"])
	}
	for _, item := range subscriptions {
		if item.(map[string]any)["own_risk"] != true {
			t.Fatalf("subscription missing own_risk: %#v", item)
		}
	}

	listed := modelRequest(t, mux, http.MethodGet, "/api/models/endpoints/magpie/models", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("models status = %d (%s)", listed.Code, listed.Body.String())
	}
	listBody := decodeModelBody(t, listed)
	models, _ := listBody["models"].([]any)
	last, _ := models[len(models)-1].(map[string]any)
	if last["id"] != "kimi/k3" || last["label"] != "Kimi K3 · Moonshot" || last["max_context_tokens"] != float64(262144) {
		t.Fatalf("last model = %#v", last)
	}
	if len(*seen) != 1 || (*seen)[0] != "Bearer magpie" {
		t.Fatalf("gateway saw %#v, want its magpie placeholder", *seen)
	}
}

// Parity: framework/server/tests/test_models_page.py::test_an_endpoint_that_isnt_running_says_so
func TestModelsEndpointNotRunning(t *testing.T) {
	home := modelTestHome(t)
	writeModelTestConfig(t, home, map[string]any{
		"model_endpoints": map[string]any{"magpie": map[string]any{"api_base": "http://127.0.0.1:9/v1"}},
	})
	store := newFakeModelKeyStore()
	mux := modelTestMux(modelTestDeps(home, store))

	overview := modelRequest(t, mux, http.MethodGet, "/api/models/overview", "")
	body := decodeModelBody(t, overview)
	endpoints := body["endpoints"].([]any)
	if endpoints[0].(map[string]any)["running"] != false {
		t.Fatalf("running = %#v", endpoints[0])
	}

	listed := modelRequest(t, mux, http.MethodGet, "/api/models/endpoints/magpie/models", "")
	if listed.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", listed.Code)
	}
	listedBody := decodeModelBody(t, listed)
	message, _ := listedBody["error"].(string)
	if !strings.Contains(message, "Can't reach") {
		t.Fatalf("error = %q", message)
	}
}

// Parity: framework/server/tests/test_models_page.py::test_the_main_model_moved_to_an_endpoint_leaves_the_old_key_behind
func TestModelsMainModelMovedToEndpointLeavesOldKeyBehind(t *testing.T) {
	home := modelTestHome(t)
	gateway, _ := newModelGateway(t)
	base := gatewayBase(gateway)
	writeModelTestConfig(t, home, map[string]any{
		"model_endpoints": map[string]any{"magpie": map[string]any{"api_base": base}},
		"llm": map[string]any{
			"provider": "deepseek", "model": "deepseek-chat",
			"api_key": "sk-old", "extra_body": map[string]any{"reasoning_effort": "max"},
		},
	})
	provider := &fakeModelProvider{}
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "session_a", LLM: provider})

	store := newFakeModelKeyStore()
	deps := modelTestDeps(home, store)
	deps.Manager = manager
	mux := modelTestMux(deps)

	response := modelRequest(t, mux, http.MethodPut, "/api/models/slots/llm",
		`{"source":"endpoint","endpoint":"magpie","model":"kimi/k3"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	config := readModelTestConfig(t, home)
	section := config["llm"].(map[string]any)
	expected := map[string]any{
		"provider": "openai", "endpoint": "magpie", "model": "kimi/k3",
		"api_base": base, "max_context_tokens": float64(262144),
	}
	if len(section) != len(expected) {
		t.Fatalf("llm section = %#v, want %#v", section, expected)
	}
	for key, want := range expected {
		if section[key] != want {
			t.Fatalf("llm[%s] = %#v, want %#v", key, section[key], want)
		}
	}
	if len(provider.calls) != 1 {
		t.Fatalf("reconfigure calls = %#v", provider.calls)
	}
	call := provider.calls[0]
	if call.Model != "openai/kimi/k3" {
		t.Fatalf("model = %q", call.Model)
	}
	if call.APIKey == nil || *call.APIKey != "magpie" {
		t.Fatalf("api_key = %#v", call.APIKey)
	}
	if call.APIBase == nil || *call.APIBase != base {
		t.Fatalf("api_base = %#v", call.APIBase)
	}
	if call.Extra != nil {
		t.Fatalf("extra = %#v, want nil", call.Extra)
	}
	if _, present := section["api_key"]; present {
		t.Fatal("old api_key followed the model to the endpoint")
	}
	if _, present := section["extra_body"]; present {
		t.Fatal("old extra_body followed the model to the endpoint")
	}
}

// Parity: framework/server/tests/test_models_page.py::test_a_model_change_within_one_endpoint_keeps_its_extra_body
func TestModelsModelChangeWithinEndpointKeepsExtraBody(t *testing.T) {
	home := modelTestHome(t)
	gateway, _ := newModelGateway(t)
	base := gatewayBase(gateway)
	extra := map[string]any{"reasoning_effort": "high"}
	writeModelTestConfig(t, home, map[string]any{
		"model_endpoints": map[string]any{"magpie": map[string]any{"api_base": base}},
		"llm": map[string]any{
			"provider": "openai", "endpoint": "magpie", "model": "kimi/k3",
			"api_base": base, "extra_body": extra,
		},
	})
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	response := modelRequest(t, mux, http.MethodPut, "/api/models/slots/llm",
		`{"source":"endpoint","endpoint":"magpie","model":"deepseek/deepseek-v4-pro"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	section := readModelTestConfig(t, home)["llm"].(map[string]any)
	got, ok := section["extra_body"].(map[string]any)
	if !ok || got["reasoning_effort"] != "high" {
		t.Fatalf("extra_body = %#v", section["extra_body"])
	}
}

// Parity: framework/server/tests/test_models_page.py::test_a_slot_refuses_what_it_cant_use
func TestModelsSlotRefusesWhatItCantUse(t *testing.T) {
	home := modelTestHome(t)
	gateway, _ := newModelGateway(t)
	base := gatewayBase(gateway)
	writeModelTestConfig(t, home, map[string]any{
		"model_endpoints": map[string]any{"magpie": map[string]any{"api_base": base}},
	})
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	cases := []struct {
		body  string
		error string
	}{
		{`{"source":"endpoint","endpoint":"magpie","model":"no/such-model"}`, "magpie has no model 'no/such-model'"},
		{`{"source":"endpoint","endpoint":"nowhere","model":"kimi/k3"}`, "There's no endpoint 'nowhere'"},
		{`{"source":"subscription","model":"claude"}`, "'source' must be"},
		{`{"source":"endpoint","endpoint":"magpie"}`, "'model' is required"},
	}
	for _, testCase := range cases {
		response := modelRequest(t, mux, http.MethodPut, "/api/models/slots/worker_llm", testCase.body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d", testCase.body, response.Code)
		}
		body := decodeModelBody(t, response)
		message, _ := body["error"].(string)
		if !strings.Contains(message, testCase.error) {
			t.Fatalf("body %s: error = %q, want %q", testCase.body, message, testCase.error)
		}
		if _, present := readModelTestConfig(t, home)["worker_llm"]; present {
			t.Fatalf("body %s wrote worker_llm", testCase.body)
		}
	}
}

// Parity: framework/server/tests/test_models_page.py::test_every_slot_kind_takes_an_endpoints_key_not_an_ambient_one
func TestModelsEverySlotKindTakesEndpointKey(t *testing.T) {
	home := modelTestHome(t)
	gateway, seen := newModelGateway(t)
	base := gatewayBase(gateway)
	queenDir := filepath.Join(home.QueensDir(), "queen_ava")
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatalf("mkdir queen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "profile.yaml"), []byte("name: Ava\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	writeModelTestConfig(t, home, map[string]any{
		"model_endpoints": map[string]any{"ollama": map[string]any{"api_base": base}},
		"llm":             map[string]any{"provider": "deepseek", "model": "deepseek-chat"},
	})
	t.Setenv("OPENAI_API_KEY", "sk-ambient")

	// A real encrypted store so llm.ListModels' own KeyFor sees the endpoint
	// key too — that is the ambient-key guarantee the upstream test asserts.
	fernetKey, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate fernet key: %v", err)
	}
	t.Setenv("HIVE_CREDENTIAL_KEY", fernetKey)
	realStore, err := credentials.WithEncryptedStorage("")
	if err != nil {
		t.Fatalf("encrypted store: %v", err)
	}
	credential := credentials.NewCredentialObject("endpoint_ollama")
	credential.SetKey("api_key", "ollama-key-0000", nil, nil)
	if err := realStore.SaveCredential(credential); err != nil {
		t.Fatalf("save endpoint key: %v", err)
	}
	deps := ModelRouteDeps{
		Home:        home,
		Credentials: func() ModelCredentialStore { return realStore },
	}
	queenProvider := &fakeModelProvider{}
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "session_queen", LLM: queenProvider, QueenName: "queen_ava"})
	deps.Manager = manager
	mux := modelTestMux(deps)

	for _, slot := range []string{"worker_llm", "vision_fallback", "queen:queen_ava"} {
		response := modelRequest(t, mux, http.MethodPut, "/api/models/slots/"+url.PathEscape(slot),
			`{"source":"endpoint","endpoint":"ollama","model":"kimi/k3"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("slot %s: status = %d (%s)", slot, response.Code, response.Body.String())
		}
		config := readModelTestConfig(t, home)
		var section map[string]any
		if strings.HasPrefix(slot, "queen:") {
			section = config["queen_llm"].(map[string]any)["queen_ava"].(map[string]any)
		} else {
			section = config[slot].(map[string]any)
		}
		if section["provider"] != "openai" || section["endpoint"] != "ollama" || section["model"] != "kimi/k3" || section["api_base"] != base {
			t.Fatalf("slot %s section = %#v", slot, section)
		}
	}
	if len(*seen) != 1 || (*seen)[0] != "Bearer ollama-key-0000" {
		t.Fatalf("gateway saw %#v", *seen)
	}
	if stored, err := realStore.Get("endpoint_ollama"); err != nil || stored == nil || *stored != "ollama-key-0000" {
		t.Fatalf("stored key = %#v, err = %v", stored, err)
	}

	removed := modelRequest(t, mux, http.MethodDelete, "/api/models/slots/queen:queen_ava", "")
	if removed.Code != http.StatusOK {
		t.Fatalf("remove queen status = %d", removed.Code)
	}
	if _, present := readModelTestConfig(t, home)["queen_llm"]; present {
		t.Fatal("queen_llm survived its last section removal")
	}
	if len(queenProvider.calls) != 2 {
		t.Fatalf("queen provider calls = %#v, want own-model then main-model", queenProvider.calls)
	}
	if queenProvider.calls[0].Model != "openai/kimi/k3" || queenProvider.calls[1].Model != "deepseek/deepseek-chat" {
		t.Fatalf("queen provider models = %q then %q", queenProvider.calls[0].Model, queenProvider.calls[1].Model)
	}
}

// Parity: framework/server/tests/test_models_page.py::test_a_catalog_model_is_written_clean
func TestModelsCatalogModelWrittenClean(t *testing.T) {
	home := modelTestHome(t)
	writeModelTestConfig(t, home, map[string]any{
		"llm": map[string]any{
			"provider": "openai", "endpoint": "magpie", "model": "kimi/k3",
			"api_base": "http://x/v1", "api_key": "stale",
		},
	})
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-0000")
	t.Setenv("OPENAI_API_KEY", "")

	deps := modelTestDeps(home, newFakeModelKeyStore())
	deps.LLM.Validate = func(provider, apiKey string, apiBase, model *string) map[string]any {
		return map[string]any{"valid": true}
	}
	mux := modelTestMux(deps)

	anthropicModels := llm.GetModelsCatalogue()["anthropic"]
	if len(anthropicModels) == 0 {
		t.Fatal("catalog has no anthropic models")
	}
	model := anthropicModels[0].ID
	ok := modelRequest(t, mux, http.MethodPut, "/api/models/slots/llm",
		`{"source":"catalog","provider":"anthropic","model":"`+model+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("catalog status = %d (%s)", ok.Code, ok.Body.String())
	}
	section := readModelTestConfig(t, home)["llm"].(map[string]any)
	if section["provider"] != "anthropic" || section["model"] != model {
		t.Fatalf("llm = %#v", section)
	}
	for _, forbidden := range []string{"api_key", "endpoint", "api_base", "extra_body"} {
		if _, present := section[forbidden]; present {
			t.Fatalf("catalog write kept stale %q: %#v", forbidden, section)
		}
	}
	if section["max_tokens"] == nil || section["max_context_tokens"] == nil {
		t.Fatalf("catalog limits missing: %#v", section)
	}

	openaiModels := llm.GetModelsCatalogue()["openai"]
	keyless := modelRequest(t, mux, http.MethodPut, "/api/models/slots/worker_llm",
		`{"source":"catalog","provider":"openai","model":"`+openaiModels[0].ID+`"}`)
	if keyless.Code != http.StatusBadRequest {
		t.Fatalf("keyless status = %d (%s)", keyless.Code, keyless.Body.String())
	}
	body := decodeModelBody(t, keyless)
	if message, _ := body["error"].(string); !strings.Contains(message, "no API key for openai") {
		t.Fatalf("keyless error = %q", message)
	}
}

// Parity: framework/server/tests/test_models_page.py::test_endpoints_are_added_moved_and_removed
func TestModelsEndpointsAddedMovedRemoved(t *testing.T) {
	home := modelTestHome(t)
	gateway, _ := newModelGateway(t)
	base := gatewayBase(gateway)
	writeModelTestConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "deepseek", "model": "deepseek-chat"},
	})
	store := newFakeModelKeyStore()
	mux := modelTestMux(modelTestDeps(home, store))

	added := modelRequest(t, mux, http.MethodPut, "/api/models/endpoints/ollama",
		`{"api_base":"http://127.0.0.1:9/v1/","api_key":"ollama-key-0000"}`)
	if added.Code != http.StatusOK {
		t.Fatalf("add status = %d (%s)", added.Code, added.Body.String())
	}
	body := decodeModelBody(t, added)
	if body["name"] != "ollama" || body["api_base"] != "http://127.0.0.1:9/v1" || body["builtin"] != false || body["has_key"] != true {
		t.Fatalf("add body = %#v", body)
	}
	if store.saved["endpoint_ollama"] != "ollama-key-0000" {
		t.Fatalf("saved key = %#v", store.saved)
	}

	moved := modelRequest(t, mux, http.MethodPut, "/api/models/endpoints/ollama", `{"api_base":"`+base+`"}`)
	if moved.Code != http.StatusOK {
		t.Fatalf("move status = %d (%s)", moved.Code, moved.Body.String())
	}
	slot := modelRequest(t, mux, http.MethodPut, "/api/models/slots/worker_llm",
		`{"source":"endpoint","endpoint":"ollama","model":"kimi/k3"}`)
	if slot.Code != http.StatusOK {
		t.Fatalf("slot status = %d (%s)", slot.Code, slot.Body.String())
	}
	followed := modelRequest(t, mux, http.MethodPut, "/api/models/endpoints/ollama", `{"api_base":"`+base+`/"}`)
	if followed.Code != http.StatusOK {
		t.Fatalf("follow status = %d (%s)", followed.Code, followed.Body.String())
	}
	section := readModelTestConfig(t, home)["worker_llm"].(map[string]any)
	if section["api_base"] != base {
		t.Fatalf("worker_llm api_base = %#v, want %q", section["api_base"], base)
	}

	inUse := modelRequest(t, mux, http.MethodDelete, "/api/models/endpoints/ollama", "")
	if inUse.Code != http.StatusConflict {
		t.Fatalf("in-use delete status = %d (%s)", inUse.Code, inUse.Body.String())
	}
	inUseBody := decodeModelBody(t, inUse)
	if message, _ := inUseBody["error"].(string); !strings.Contains(message, "worker_llm") {
		t.Fatalf("in-use error = %q", message)
	}
	if removed := modelRequest(t, mux, http.MethodDelete, "/api/models/slots/worker_llm", ""); removed.Code != http.StatusOK {
		t.Fatalf("slot delete status = %d", removed.Code)
	}
	gone := modelRequest(t, mux, http.MethodDelete, "/api/models/endpoints/ollama", "")
	if gone.Code != http.StatusOK {
		t.Fatalf("delete status = %d (%s)", gone.Code, gone.Body.String())
	}
	if _, present := readModelTestConfig(t, home)["model_endpoints"]; present {
		t.Fatal("model_endpoints survived its last entry removal")
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved keys = %#v, want empty", store.saved)
	}
}

// Parity: framework/server/tests/test_models_page.py::test_magpie_at_its_own_address_needs_no_entry
func TestModelsMagpieAtOwnAddressNeedsNoEntry(t *testing.T) {
	home := modelTestHome(t)
	writeModelTestConfig(t, home, map[string]any{})
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	response := modelRequest(t, mux, http.MethodPut, "/api/models/endpoints/magpie",
		`{"api_base":"`+llm.MagpieAPIBase+`"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	if _, present := readModelTestConfig(t, home)["model_endpoints"]; present {
		t.Fatal("magpie at its own address wrote a model_endpoints entry")
	}
}

// Parity: framework/server/tests/test_models_page.py::test_an_endpoint_needs_a_plain_name_and_a_web_address
func TestModelsEndpointNameAndAddressValidation(t *testing.T) {
	home := modelTestHome(t)
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	cases := []struct {
		name  string
		body  string
		error string
	}{
		{"Bad Name", `{"api_base":"http://x/v1"}`, "lowercase"},
		{"lmstudio", `{"api_base":"ftp://x"}`, "http:// or https://"},
		{"lmstudio", `{}`, "http:// or https://"},
	}
	for _, testCase := range cases {
		response := modelRequest(t, mux, http.MethodPut,
			"/api/models/endpoints/"+url.PathEscape(testCase.name), testCase.body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d", testCase.name, response.Code)
		}
		body := decodeModelBody(t, response)
		if message, _ := body["error"].(string); !strings.Contains(message, testCase.error) {
			t.Fatalf("%s: error = %q, want %q", testCase.name, message, testCase.error)
		}
	}
}

// Parity-exempt: no upstream test asserts the overview's judge/queens/
// providers blocks; this pins their shape against a configured home.
func TestModelsOverviewJudgeQueensProviders(t *testing.T) {
	home := modelTestHome(t)
	queenDir := filepath.Join(home.QueensDir(), "queen_x")
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatalf("mkdir queen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "profile.yaml"), []byte("name: Xena\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	writeModelTestConfig(t, home, map[string]any{
		"judge":     map[string]any{"api_base": "http://judge", "api_key": "judge-key", "model": "judge-model"},
		"queen_llm": map[string]any{"queen_x": map[string]any{"provider": "openai", "model": "gpt-4o"}},
	})
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	response := modelRequest(t, mux, http.MethodGet, "/api/models/overview", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := decodeModelBody(t, response)
	judge := body["judge"].(map[string]any)
	if judge["configured"] != true || judge["model"] != "judge-model" || judge["api_base"] != "http://judge" {
		t.Fatalf("judge = %#v", judge)
	}
	queens, _ := body["queens"].([]any)
	if len(queens) != 1 {
		t.Fatalf("queens = %#v", body["queens"])
	}
	queen := queens[0].(map[string]any)
	if queen["id"] != "queen_x" || queen["name"] != "Xena" {
		t.Fatalf("queen = %#v", queen)
	}
	providers, _ := body["providers"].([]any)
	if len(providers) == 0 {
		t.Fatal("providers empty")
	}
	for _, item := range providers {
		provider := item.(map[string]any)
		if _, ok := provider["id"].(string); !ok {
			t.Fatalf("provider id = %#v", provider)
		}
		if _, ok := provider["connected"].(bool); !ok {
			t.Fatalf("provider connected = %#v", provider)
		}
		if _, ok := provider["models"].([]any); !ok {
			t.Fatalf("provider models = %#v", provider)
		}
	}
}

// Parity-exempt: unknown endpoint reads and deletes are 404s upstream.
func TestModelsUnknownEndpointRoutes(t *testing.T) {
	home := modelTestHome(t)
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	models := modelRequest(t, mux, http.MethodGet, "/api/models/endpoints/ghost/models", "")
	if models.Code != http.StatusNotFound {
		t.Fatalf("models status = %d", models.Code)
	}
	if message, _ := decodeModelBody(t, models)["error"].(string); message != "There's no endpoint 'ghost'" {
		t.Fatalf("models error = %q", message)
	}
	removed := modelRequest(t, mux, http.MethodDelete, "/api/models/endpoints/ghost", "")
	if removed.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d", removed.Code)
	}
}

// Parity-exempt: slot validation (unknown name, unknown queen, main removal).
func TestModelsSlotValidation(t *testing.T) {
	home := modelTestHome(t)
	mux := modelTestMux(modelTestDeps(home, newFakeModelKeyStore()))

	unknown := modelRequest(t, mux, http.MethodPut, "/api/models/slots/nope",
		`{"source":"endpoint","endpoint":"x","model":"m"}`)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown slot status = %d", unknown.Code)
	}
	if message, _ := decodeModelBody(t, unknown)["error"].(string); !strings.Contains(message, "There's no slot 'nope'") {
		t.Fatalf("unknown slot error = %q", message)
	}

	ghost := modelRequest(t, mux, http.MethodPut, "/api/models/slots/queen:ghost",
		`{"source":"endpoint","endpoint":"x","model":"m"}`)
	if ghost.Code != http.StatusNotFound {
		t.Fatalf("ghost queen status = %d", ghost.Code)
	}
	if message, _ := decodeModelBody(t, ghost)["error"].(string); message != "There's no queen 'ghost'" {
		t.Fatalf("ghost queen error = %q", message)
	}

	remove := modelRequest(t, mux, http.MethodDelete, "/api/models/slots/llm", "")
	if remove.Code != http.StatusBadRequest {
		t.Fatalf("remove main status = %d", remove.Code)
	}
	if message, _ := decodeModelBody(t, remove)["error"].(string); message != "The main model can be changed, not removed" {
		t.Fatalf("remove main error = %q", message)
	}
}

// Parity-exempt: the no-store fallback message when no credential store is
// wired (upstream's "isn't available" branch).
func TestModelsEndpointKeyWithoutCredentialStore(t *testing.T) {
	home := modelTestHome(t)
	deps := ModelRouteDeps{
		Home:        home,
		Credentials: func() ModelCredentialStore { return nil },
	}
	mux := modelTestMux(deps)
	response := modelRequest(t, mux, http.MethodPut, "/api/models/endpoints/ollama",
		`{"api_base":"http://x/v1","api_key":"key-1"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d (%s)", response.Code, response.Body.String())
	}
	message, _ := decodeModelBody(t, response)["error"].(string)
	if message != "The credential store isn't available, so the key can't be kept" {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: section_model's OpenRouter prefix strip is pinned here.
func TestModelsSectionFullModel(t *testing.T) {
	cases := []struct {
		section map[string]any
		want    string
	}{
		{map[string]any{"provider": "anthropic", "model": "claude-sonnet-4"}, "anthropic/claude-sonnet-4"},
		{map[string]any{"provider": "openrouter", "model": "openrouter/openai/gpt-4o"}, "openrouter/openai/gpt-4o"},
		{map[string]any{"provider": "OpenRouter", "model": "anthropic/claude-sonnet-4"}, "OpenRouter/anthropic/claude-sonnet-4"},
	}
	for _, testCase := range cases {
		if got := sectionFullModel(testCase.section); got != testCase.want {
			t.Fatalf("sectionFullModel(%#v) = %q, want %q", testCase.section, got, testCase.want)
		}
	}
}
