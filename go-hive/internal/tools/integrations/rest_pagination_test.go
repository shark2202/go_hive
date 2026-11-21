package integrations

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// githubPaginationRegistry points the real github module at one fake upstream.
func githubPaginationRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	registry := New(
		WithBaseURL("github_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithCredential("github_tool", "gh-fake"),
	)
	return registry, recorder
}

// stripePaginationRegistry points the real stripe module at one fake upstream.
func stripePaginationRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	registry := New(
		WithBaseURL("stripe_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithCredential("stripe_tool", "sk_test_fake"),
	)
	return registry, recorder
}

// Parity-exempt: Go-side Link-header pagination; upstream github_tool exposes
// page/per_page parameters and returns one page per call.
func TestGitHubListIssuesFollowsLinkHeaderPages(t *testing.T) {
	registry, recorder := githubPaginationRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			w.Header().Set("Link", "<http://"+r.Host+"/repos/acme/widgets/issues?per_page=30&page=2>; rel=\"next\", <http://"+r.Host+"/repos/acme/widgets/issues?per_page=30&page=9>; rel=\"last\"")
			_, _ = io.WriteString(w, `[{"number":1},{"number":2}]`)
		case "2":
			_, _ = io.WriteString(w, `[{"number":3}]`)
		default:
			http.Error(w, "unexpected page", http.StatusBadRequest)
		}
	})

	result, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_list_issues", "owner": "acme", "repo": "widgets", "limit": 30,
	})
	if err != nil {
		t.Fatalf("github_list_issues: %v", err)
	}
	issues, ok := result.([]any)
	if !ok || len(issues) != 3 {
		t.Fatalf("merged issues = %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("github requests = %d, want 2", recorder.count())
	}
	second := recorder.at(t, 1)
	if got := second.Query.Get("page"); got != "2" {
		t.Fatalf("follow-up page = %q", got)
	}
	if got := second.Query.Get("per_page"); got != "30" {
		t.Fatalf("follow-up per_page = %q (the Link URL must be used verbatim)", got)
	}
}

// Parity-exempt: a github page without a next link is returned as-is (single
// request), matching the upstream single-page call.
func TestGitHubListStargazersStopsWithoutLinkHeader(t *testing.T) {
	registry, recorder := githubPaginationRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"login":"octocat"}]`)
	})

	result, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_list_stargazers", "owner": "acme", "repo": "widgets",
	})
	if err != nil {
		t.Fatalf("github_list_stargazers: %v", err)
	}
	users, ok := result.([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("stargazers = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("github requests = %d, want 1 (no next page)", recorder.count())
	}
}

// Parity-exempt: Go-side last-item pagination; upstream stripe_list_* document
// starting_after but never follow has_more themselves.
func TestStripeListCustomersFollowsLastItemId(t *testing.T) {
	registry, recorder := stripePaginationRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("starting_after") {
		case "":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"cus_1"},{"id":"cus_2"}],"has_more":true}`)
		case "cus_2":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"cus_3"}],"has_more":false}`)
		default:
			t.Errorf("unexpected starting_after = %q", r.URL.Query().Get("starting_after"))
			http.Error(w, "unexpected cursor", http.StatusBadRequest)
		}
	})

	result := batch4Execute(t, registry, "stripe", map[string]any{
		"action": "stripe_list_customers", "limit": 2,
	})
	customers, ok := result["customers"].([]any)
	if !ok || len(customers) != 3 {
		t.Fatalf("merged customers = %v", result["customers"])
	}
	if _, present := result["has_more"]; present {
		t.Fatalf("has_more still advertised after the walk: %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("stripe requests = %d, want 2 (has_more=false stops without a third call)", recorder.count())
	}
	if got := recorder.at(t, 1).Query.Get("starting_after"); got != "cus_2" {
		t.Fatalf("follow-up starting_after = %q", got)
	}
	if got := recorder.at(t, 1).Query.Get("limit"); got != "2" {
		t.Fatalf("follow-up limit = %q", got)
	}
}

// Parity-exempt: stripe reports has_more=false on a complete page; the walk
// must not issue a speculative extra request.
func TestStripeListInvoicesStopsOnHasMoreFalse(t *testing.T) {
	registry, recorder := stripePaginationRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"in_1"}],"has_more":false}`)
	})

	result := batch4Execute(t, registry, "stripe", map[string]any{
		"action": "stripe_list_invoices",
	})
	invoices, ok := result["invoices"].([]any)
	if !ok || len(invoices) != 1 {
		t.Fatalf("invoices = %v", result["invoices"])
	}
	if recorder.count() != 1 {
		t.Fatalf("stripe requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: a failed follow-up page surfaces the provider error mapping
// instead of a half-merged list.
func TestStripeListChargesPaginationErrorSurfaces(t *testing.T) {
	registry, recorder := stripePaginationRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("starting_after") == "" {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"ch_1"}],"has_more":true}`)
			return
		}
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Your card was declined.","type":"card_error"}}`)
	})

	result := batch4Execute(t, registry, "stripe", map[string]any{
		"action": "stripe_list_charges",
	})
	if result["error"] != "Your card was declined." {
		t.Fatalf("error = %v", result)
	}
	if result["type"] != "card_error" {
		t.Fatalf("type = %v", result)
	}
	if _, merged := result["charges"]; merged {
		t.Fatalf("failed page leaked a merged list: %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("stripe requests = %d, want 2", recorder.count())
	}
}
