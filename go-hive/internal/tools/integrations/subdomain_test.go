package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// batch8ExecuteMap runs a tool expected to return a plain object (the scanner
// shape), failing on transport errors or a non-map result.
func batch8ExecuteMap(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

// batch8CRTSHServer serves the crt.sh JSON shape: a list of {name_value}.
func batch8CRTSHServer(t *testing.T, names []string, status int) *httptest.Server {
	t.Helper()
	entries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entries = append(entries, map[string]any{"name_value": name})
	}
	payload, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("output"); got != "json" {
			t.Errorf("output query = %q, want json", got)
		}
		if got := r.URL.Query().Get("q"); !strings.HasPrefix(got, "%.") {
			t.Errorf("q = %q, want a %%.domain wildcard query", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// batch8RoundTripFunc lets a test fail the transport deterministically.
type batch8RoundTripFunc func(*http.Request) (*http.Response, error)

func (f batch8RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_strips_https_prefix,
// tools/tests/tools/test_subdomain_enumerator.py::test_strips_http_prefix,
// tools/tests/tools/test_subdomain_enumerator.py::test_strips_path
func TestBatch8SubdomainStripsPrefixes(t *testing.T) {
	server := batch8CRTSHServer(t, nil, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	for _, raw := range []string{"https://example.com", "http://example.com", "example.com/path"} {
		result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": raw})
		if result["domain"] != "example.com" {
			t.Fatalf("%s: domain = %v", raw, result["domain"])
		}
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_max_results_clamped
func TestBatch8SubdomainMaxResultsClamped(t *testing.T) {
	names := make([]string, 0, 250)
	for index := 0; index < 250; index++ {
		names = append(names, fmt.Sprintf("sub%03d.example.com", index))
	}
	server := batch8CRTSHServer(t, names, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))

	result := batch8ExecuteMap(t, registry, "subdomain_enumerate",
		map[string]any{"domain": "example.com", "max_results": 500})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	subdomains := result["subdomains"].([]string)
	if len(subdomains) != 200 {
		t.Fatalf("subdomains = %d, want the 200 clamp", len(subdomains))
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_timeout_error
func TestBatch8SubdomainTimeout(t *testing.T) {
	client := &http.Client{Transport: batch8RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	registry := newBatch8FinalRegistry(
		withBatch8Client(client),
		withBatch8BaseURL("subdomain_enumerator", "https://crt.invalid"),
	)
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "timed out") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_http_error
func TestBatch8SubdomainHTTPError(t *testing.T) {
	server := batch8CRTSHServer(t, nil, http.StatusInternalServerError)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "500") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_subdomains_extracted
func TestBatch8SubdomainExtracted(t *testing.T) {
	server := batch8CRTSHServer(t, []string{
		"www.example.com",
		"api.example.com",
		"mail.example.com",
	}, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})

	if result["total_found"] != 3 {
		t.Fatalf("total_found = %v", result["total_found"])
	}
	if result["source"] != "crt.sh (Certificate Transparency)" {
		t.Fatalf("source = %v", result["source"])
	}
	subdomains := result["subdomains"].([]string)
	want := []string{"api.example.com", "mail.example.com", "www.example.com"}
	if strings.Join(subdomains, ",") != strings.Join(want, ",") {
		t.Fatalf("subdomains = %v, want %v (sorted)", subdomains, want)
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_wildcards_filtered
func TestBatch8SubdomainWildcardsFiltered(t *testing.T) {
	server := batch8CRTSHServer(t, []string{"*.example.com", "www.example.com", "*.api.example.com"}, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	subdomains := result["subdomains"].([]string)
	for _, name := range subdomains {
		if strings.HasPrefix(name, "*.") {
			t.Fatalf("wildcard survived: %v", subdomains)
		}
	}
	// Both wildcard entries are dropped, leaving only www.
	if len(subdomains) != 1 || subdomains[0] != "www.example.com" {
		t.Fatalf("subdomains = %v", subdomains)
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_duplicates_removed
func TestBatch8SubdomainDuplicatesRemoved(t *testing.T) {
	server := batch8CRTSHServer(t, []string{
		"www.example.com",
		"www.example.com",
		"www.example.com",
	}, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	if result["total_found"] != 1 {
		t.Fatalf("total_found = %v", result["total_found"])
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_staging_flagged,
// tools/tests/tools/test_subdomain_enumerator.py::test_admin_flagged,
// tools/tests/tools/test_subdomain_enumerator.py::test_dev_flagged
func TestBatch8SubdomainInterestingHosts(t *testing.T) {
	cases := map[string]string{
		"staging.example.com": "medium",
		"admin.example.com":   "high",
		"dev.example.com":     "medium",
	}
	for host, severity := range cases {
		server := batch8CRTSHServer(t, []string{host, "www.example.com"}, http.StatusOK)
		registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
		result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
		interesting, _ := result["interesting"].([]map[string]any)
		if len(interesting) != 1 {
			t.Fatalf("%s: interesting = %v", host, result["interesting"])
		}
		if interesting[0]["subdomain"] != host || interesting[0]["severity"] != severity {
			t.Fatalf("%s: finding = %v", host, interesting[0])
		}
		if interesting[0]["remediation"] == "" || interesting[0]["reason"] == "" {
			t.Fatalf("%s: finding lacks guidance: %v", host, interesting[0])
		}
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_grade_input_keys_present
func TestBatch8SubdomainGradeInputKeys(t *testing.T) {
	server := batch8CRTSHServer(t, nil, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	grade, ok := result["grade_input"].(map[string]any)
	if !ok {
		t.Fatalf("grade_input = %#v", result["grade_input"])
	}
	for _, key := range []string{"no_dev_staging_exposed", "no_admin_exposed", "reasonable_surface_area"} {
		if _, present := grade[key]; !present {
			t.Fatalf("grade_input misses %s: %v", key, grade)
		}
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_no_dev_staging_true_when_clean
func TestBatch8SubdomainCleanSurface(t *testing.T) {
	server := batch8CRTSHServer(t, []string{"www.example.com", "api.example.com"}, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	grade := result["grade_input"].(map[string]any)
	if grade["no_dev_staging_exposed"] != true {
		t.Fatalf("no_dev_staging_exposed = %v", grade["no_dev_staging_exposed"])
	}
	if grade["no_admin_exposed"] != true {
		t.Fatalf("no_admin_exposed = %v", grade["no_admin_exposed"])
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_reasonable_surface_area
func TestBatch8SubdomainReasonableSurface(t *testing.T) {
	names := make([]string, 0, 30)
	for index := 0; index < 30; index++ {
		names = append(names, fmt.Sprintf("sub%d.example.com", index))
	}
	server := batch8CRTSHServer(t, names, http.StatusOK)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	grade := result["grade_input"].(map[string]any)
	if grade["reasonable_surface_area"] != true {
		t.Fatalf("reasonable_surface_area = %v (30 subdomains)", grade["reasonable_surface_area"])
	}
}

// Parity: tools/tests/tools/test_subdomain_enumerator.py::test_http_error
func TestBatch8SubdomainMalformedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(server.Close)
	registry := newBatch8FinalRegistry(withBatch8BaseURL("subdomain_enumerator", server.URL))
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	if _, failed := result["error"]; !failed {
		t.Fatalf("malformed body reported success: %v", result)
	}
}

// Parity-exempt: transport injectability is a Go-side guard; no upstream test
// drives the error type directly.
func TestBatch8SubdomainNetworkErrorIsValue(t *testing.T) {
	client := &http.Client{Transport: batch8RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})}
	registry := newBatch8FinalRegistry(
		withBatch8Client(client),
		withBatch8BaseURL("subdomain_enumerator", "https://crt.invalid"),
	)
	result := batch8ExecuteMap(t, registry, "subdomain_enumerate", map[string]any{"domain": "example.com"})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "connection refused") {
		t.Fatalf("error = %v", result["error"])
	}
}
