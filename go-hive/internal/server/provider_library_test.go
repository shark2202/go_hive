package server

import (
	"reflect"
	"testing"
)

func providerEntry() map[string]any {
	return map[string]any{
		"provider":     "deepseek",
		"model":        "deepseek-chat",
		"api_key":      "sk-test-1234",
		"custom_extra": map[string]any{"nested": true},
	}
}

type validatorCall struct {
	provider string
	apiKey   string
	apiBase  string
	model    string
}

// Parity: tests/test_routes_provider_library.py::test_get_library_empty
func TestTheProviderLibraryStartsEmpty(t *testing.T) {
	home := newFeatureHome(t)
	got := ProviderLibraryResponse(home)
	want := map[string]any{"library": map[string]any{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("library = %#v, want %#v", got, want)
	}
}

// Parity: tests/test_routes_provider_library.py::test_put_saves_entry_verbatim
func TestPuttingALibraryEntryStoresItVerbatim(t *testing.T) {
	home := newFeatureHome(t)
	entry := providerEntry()

	status, body := PutProviderLibrary(home, map[string]any{"name": "ds-prod", "section": entry})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if !reflect.DeepEqual(body["section"], entry) {
		t.Fatalf("response section = %#v, want verbatim %#v", body["section"], entry)
	}
	onDisk := readFeatureConfig(t, home)
	library := onDisk["provider_library"].(map[string]any)
	if !reflect.DeepEqual(library["ds-prod"], entry) {
		t.Fatalf("on-disk entry = %#v, want verbatim %#v", library["ds-prod"], entry)
	}
	got := ProviderLibraryResponse(home)["library"].(map[string]any)
	if !reflect.DeepEqual(got, map[string]any{"ds-prod": entry}) {
		t.Fatalf("GET library = %#v", got)
	}
}

// Parity: tests/test_routes_provider_library.py::test_put_validation
func TestPuttingALibraryEntryValidatesIt(t *testing.T) {
	home := newFeatureHome(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing name", map[string]any{"section": providerEntry()}},
		{"missing model", map[string]any{"name": "x", "section": map[string]any{"provider": "openai"}}},
		{"non-object section", map[string]any{"name": "x", "section": []any{1}}},
	}
	for _, tc := range cases {
		status, _ := PutProviderLibrary(home, tc.body)
		if status != 400 {
			t.Fatalf("%s: status = %d, want 400", tc.name, status)
		}
	}
}

// Parity: tests/test_routes_provider_library.py::test_put_null_deletes_and_prunes_empty_library
func TestDeletingLibraryEntriesPrunesAnEmptyLibrary(t *testing.T) {
	home := newFeatureHome(t)
	entry := providerEntry()

	if status, _ := PutProviderLibrary(home, map[string]any{"name": "a", "section": entry}); status != 200 {
		t.Fatalf("save status = %d, want 200", status)
	}
	status, body := PutProviderLibrary(home, map[string]any{"name": "a", "section": nil})
	if status != 200 {
		t.Fatalf("delete status = %d, want 200", status)
	}
	if body["section"] != nil {
		t.Fatalf("delete section = %#v, want nil", body["section"])
	}
	if _, present := readFeatureConfig(t, home)["provider_library"]; present {
		t.Fatal("deleting the last entry must remove provider_library from configuration.json")
	}

	status, _ = PutProviderLibrary(home, map[string]any{"name": "ghost", "section": nil})
	if status != 200 {
		t.Fatalf("deleting a nonexistent entry = %d, want 200", status)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_to_worker_slot
func TestApplyingALibraryEntryToTheWorkerSlotCopiesItVerbatim(t *testing.T) {
	home := newFeatureHome(t)
	entry := providerEntry()

	if status, _ := PutProviderLibrary(home, map[string]any{"name": "ds", "section": entry}); status != 200 {
		t.Fatalf("save status = %d, want 200", status)
	}
	status, body := ApplyProviderLibrary(home, map[string]any{"name": "ds", "role": "worker_llm"}, RoleWriteDeps{})
	if status != 200 {
		t.Fatalf("apply status = %d, want 200", status)
	}
	if body["role"] != "worker_llm" {
		t.Fatalf("role = %#v", body["role"])
	}
	if !reflect.DeepEqual(body["section"], entry) {
		t.Fatalf("section = %#v, want verbatim %#v", body["section"], entry)
	}
	if body["sessions_swapped"] != 0 {
		t.Fatalf("sessions_swapped = %#v, want 0", body["sessions_swapped"])
	}

	onDisk := readFeatureConfig(t, home)
	if !reflect.DeepEqual(onDisk["worker_llm"], entry) {
		t.Fatalf("worker_llm = %#v, want verbatim %#v", onDisk["worker_llm"], entry)
	}
	library := onDisk["provider_library"].(map[string]any)
	if !reflect.DeepEqual(library["ds"], entry) {
		t.Fatal("the library must keep its own copy")
	}
	sections := LLMSectionsResponse(home)
	if !reflect.DeepEqual(sections["worker_llm"], entry) {
		t.Fatalf("GET llm-sections worker_llm = %#v, want %#v", sections["worker_llm"], entry)
	}
}

type hotSwapRecorder struct {
	model   string
	apiKey  string
	apiBase string
	calls   int
}

func (h *hotSwapRecorder) swap(fullModel, apiKey, apiBase string) int {
	h.model = fullModel
	h.apiKey = apiKey
	h.apiBase = apiBase
	h.calls++
	return 1
}

// Parity: tests/test_routes_provider_library.py::test_apply_to_llm_slot_hot_swaps_sessions
func TestApplyingALibraryEntryToTheMainSlotHotSwapsSessions(t *testing.T) {
	home := newFeatureHome(t)
	recorder := &hotSwapRecorder{}
	deps := RoleWriteDeps{HotSwap: recorder.swap}

	if status, _ := PutProviderLibrary(home, map[string]any{"name": "ds", "section": providerEntry()}); status != 200 {
		t.Fatalf("save status = %d, want 200", status)
	}
	status, body := ApplyProviderLibrary(home, map[string]any{"name": "ds", "role": "llm"}, deps)
	if status != 200 {
		t.Fatalf("apply status = %d, want 200", status)
	}
	if body["sessions_swapped"] != 1 {
		t.Fatalf("sessions_swapped = %#v, want 1", body["sessions_swapped"])
	}
	if recorder.calls != 1 {
		t.Fatalf("hot swaps = %d, want 1", recorder.calls)
	}
	if recorder.model != "deepseek/deepseek-chat" {
		t.Fatalf("hot-swap model = %q, want deepseek/deepseek-chat", recorder.model)
	}
	if recorder.apiKey != "sk-test-1234" {
		t.Fatalf("hot-swap api key = %q", recorder.apiKey)
	}
	if recorder.apiBase != "" {
		t.Fatalf("hot-swap api base = %q, want empty (None)", recorder.apiBase)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_unknown_name_and_role
func TestApplyingRejectsAnUnknownNameOrRole(t *testing.T) {
	home := newFeatureHome(t)

	if status, _ := ApplyProviderLibrary(home, map[string]any{"name": "nope", "role": "llm"}, RoleWriteDeps{}); status != 404 {
		t.Fatalf("unknown name status = %d, want 404", status)
	}
	if status, _ := PutProviderLibrary(home, map[string]any{"name": "ds", "section": providerEntry()}); status != 200 {
		t.Fatalf("save status = %d, want 200", status)
	}
	if status, _ := ApplyProviderLibrary(home, map[string]any{"name": "ds", "role": "bogus"}, RoleWriteDeps{}); status != 400 {
		t.Fatalf("unknown role status = %d, want 400", status)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_health_checks_key_against_base
func TestApplyingHealthChecksTheKeyAgainstTheBase(t *testing.T) {
	home := newFeatureHome(t)
	var calls []validatorCall
	validate := func(provider, apiKey, apiBase, model string) (any, string) {
		calls = append(calls, validatorCall{provider, apiKey, apiBase, model})
		return false, "bad key"
	}
	deps := RoleWriteDeps{Validate: validate}

	entry := providerEntry()
	entry["api_base"] = "https://ds.example.com/v1"
	if status, _ := PutProviderLibrary(home, map[string]any{"name": "ds", "section": entry}); status != 200 {
		t.Fatalf("save status = %d, want 200", status)
	}
	if len(calls) != 0 {
		t.Fatal("saving a library entry must never health-check")
	}

	status, _ := ApplyProviderLibrary(home, map[string]any{"name": "ds", "role": "worker_llm"}, deps)
	if status != 400 {
		t.Fatalf("apply with a failing key check status = %d, want 400", status)
	}
	want := []validatorCall{{"deepseek", "sk-test-1234", "https://ds.example.com/v1", "deepseek-chat"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("validator calls = %#v, want %#v", calls, want)
	}
	if got := LLMSectionsResponse(home)["worker_llm"]; got != nil {
		t.Fatalf("failed apply must not touch the slot, got %#v", got)
	}
}
