package integrationkit

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// newPaginationRuntime points one fake module at serverURL with a static token,
// so the pagination walk can be exercised without credentials or network.
func newPaginationRuntime(serverURL string) *Runtime {
	return &Runtime{
		HTTP: NewHTTPClient(&http.Client{}),
		Modules: map[string]*ModuleConfig{
			"pagination_tool": {
				ID:      "pagination_tool",
				BaseURL: serverURL,
				Headers: func(token string) map[string]string {
					return map[string]string{"Authorization": "Bearer " + token}
				},
				token: "pagination-token",
			},
		},
		Getenv: func(string) string { return "" },
	}
}

type paginationRecorder struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (r *paginationRecorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
}

func (r *paginationRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

func (r *paginationRecorder) queryAt(t *testing.T, index int) url.Values {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if index >= len(r.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(r.requests))
	}
	return r.requests[index].URL.Query()
}

// Parity-exempt: Go-side declarative Link-header pagination; upstream
// github_tool exposes page/per_page parameters and never follows the header.
func TestPaginationLinkHeaderWalksRelNext(t *testing.T) {
	recorder := &paginationRecorder{}
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		if got := r.Header.Get("Authorization"); got != "Bearer pagination-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", fmt.Sprintf("<%s/items?per_page=30&page=2>; rel=\"next\", <%s/items?per_page=30&page=2>; rel=\"last\"", serverURL, serverURL))
			_, _ = io.WriteString(w, `[{"id":"1"},{"id":"2"}]`)
		case "2":
			if got := r.URL.Query().Get("per_page"); got != "30" {
				t.Errorf("follow-up query dropped per_page: %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `[{"id":"3"}]`)
		default:
			t.Errorf("unexpected page = %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	rt := newPaginationRuntime(server.URL)
	result, err := rt.CallREST(context.Background(), &Tool{ID: "pagination_tool", DisplayName: "Fake"}, Action{Name: "list"},
		RestSpec{Method: http.MethodGet, Path: "/items", Pagination: &PaginationSpec{LinkHeader: true}},
		map[string]any{"per_page": 30})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	items, ok := result.([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("merged = %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d", recorder.count())
	}
	if got := recorder.queryAt(t, 1).Get("page"); got != "2" {
		t.Fatalf("follow-up page = %q", got)
	}
}

// Parity-exempt: a page without a next link ends the Go-side walk; upstream
// github_tool always returns a single page and never inspects the header.
func TestPaginationLinkHeaderStopsWithoutNext(t *testing.T) {
	recorder := &paginationRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"1"},{"id":"2"}]`)
	}))
	t.Cleanup(server.Close)

	rt := newPaginationRuntime(server.URL)
	result, err := rt.CallREST(context.Background(), &Tool{ID: "pagination_tool", DisplayName: "Fake"}, Action{Name: "list"},
		RestSpec{Method: http.MethodGet, Path: "/items", Pagination: &PaginationSpec{LinkHeader: true}}, nil)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	items, ok := result.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("merged = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1 (no next page)", recorder.count())
	}
}

// Parity-exempt: the page cap is a Go-side DoS guard on the pagination walk.
func TestPaginationLinkHeaderStopsAtMaxPages(t *testing.T) {
	recorder := &paginationRecorder{}
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		next := recorder.count() + 1
		w.Header().Set("Link", fmt.Sprintf("<%s/items?page=%d>; rel=\"next\"", serverURL, next))
		_, _ = io.WriteString(w, `[{"id":"x"}]`)
	}))
	t.Cleanup(server.Close)
	serverURL = server.URL

	rt := newPaginationRuntime(server.URL)
	result, err := rt.CallREST(context.Background(), &Tool{ID: "pagination_tool", DisplayName: "Fake"}, Action{Name: "list"},
		RestSpec{Method: http.MethodGet, Path: "/items", Pagination: &PaginationSpec{LinkHeader: true, MaxPages: 2}}, nil)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	items, ok := result.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("merged = %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want MaxPages=2", recorder.count())
	}
}

// Parity-exempt: Go-side declarative last-item pagination; upstream
// stripe_list_* document starting_after but never follow has_more themselves.
func TestPaginationLastItemFieldWalksStartingAfter(t *testing.T) {
	recorder := &paginationRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("starting_after") {
		case "":
			_, _ = io.WriteString(w, `{"data":[{"id":"obj_1"},{"id":"obj_2"}],"has_more":true}`)
		case "obj_2":
			_, _ = io.WriteString(w, `{"data":[{"id":"obj_3"}],"has_more":false}`)
		default:
			t.Errorf("unexpected starting_after = %q", r.URL.Query().Get("starting_after"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	rt := newPaginationRuntime(server.URL)
	result, err := rt.CallREST(context.Background(), &Tool{ID: "pagination_tool", DisplayName: "Fake"}, Action{Name: "list"},
		RestSpec{Method: http.MethodGet, Path: "/items", Pagination: &PaginationSpec{
			ItemsPath: "data", CursorParam: "starting_after", LastItemField: "id",
		}}, nil)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", result)
	}
	items, ok := object["data"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("merged = %v", object)
	}
	if _, present := object["has_more"]; present {
		t.Fatalf("has_more still advertised after the walk: %v", object)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2 (has_more=false must stop without a third call)", recorder.count())
	}
	if got := recorder.queryAt(t, 1).Get("starting_after"); got != "obj_2" {
		t.Fatalf("follow-up starting_after = %q", got)
	}
}

// Parity-exempt: a failed follow-up page returns the provider error envelope
// instead of a half-merged list; upstream has no following code at all.
func TestPaginationLastItemFieldStopsOnErrorPage(t *testing.T) {
	recorder := &paginationRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("starting_after") == "" {
			_, _ = io.WriteString(w, `{"data":[{"id":"obj_1"}],"has_more":true}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	}))
	t.Cleanup(server.Close)

	rt := newPaginationRuntime(server.URL)
	result, err := rt.CallREST(context.Background(), &Tool{ID: "pagination_tool", DisplayName: "Fake"}, Action{Name: "list"},
		RestSpec{Method: http.MethodGet, Path: "/items", Pagination: &PaginationSpec{
			ItemsPath: "data", CursorParam: "starting_after", LastItemField: "id",
		}}, nil)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", result)
	}
	message, _ := object["error"].(string)
	if !strings.Contains(message, "HTTP 500") {
		t.Fatalf("error = %v", object)
	}
	if _, merged := object["data"]; merged {
		t.Fatalf("failed page leaked a merged list: %v", object)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2", recorder.count())
	}
}

// Parity-exempt: pure Go header parser for RFC 8288 Link values.
func TestParseLinkNextHeaderVariants(t *testing.T) {
	base, err := url.Parse("https://api.github.com/repos/acme/widgets/issues?per_page=30")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "next before last",
			raw:  `<https://api.github.com/repos/acme/widgets/issues?per_page=30&page=2>; rel="next", <https://api.github.com/repos/acme/widgets/issues?per_page=30&page=5>; rel="last"`,
			want: "https://api.github.com/repos/acme/widgets/issues?per_page=30&page=2",
		},
		{
			name: "relative reference resolves against the request URL",
			raw:  `</repos/acme/widgets/issues?page=2>; rel="next"; type="application/json"`,
			want: "https://api.github.com/repos/acme/widgets/issues?page=2",
		},
		{
			name: "comma inside the URI does not split the link value",
			raw:  `<https://api.github.com/a,b?page=2>; rel="next"`,
			want: "https://api.github.com/a,b?page=2",
		},
		{
			name: "only prev",
			raw:  `<https://api.github.com/a?page=9>; rel="prev"`,
			want: "",
		},
		{
			name: "absent",
			raw:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLinkNextHeader(tc.raw, base); got != tc.want {
				t.Fatalf("parseLinkNextHeader = %q, want %q", got, tc.want)
			}
		})
	}
}
