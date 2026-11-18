package integrations

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Parity-exempt: upstream has no Python test; behavior ported from
// tools/src/aden_tools/tools/stripe_tool/stripe_tool.py::get_subscription_status
func TestStripeSubscriptionStatus(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"data":[{"id":"sub_1","status":"active"}],"has_more":false}`
	})
	registry := New(
		WithBaseURL("stripe_tool", provider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_get_subscription_status", "customer_id": "cus_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := result.(map[string]any)
	if object["status"] != "active" || object["customer_id"] != "cus_1" {
		t.Fatalf("result = %v", object)
	}
	request := provider.snapshot()[0]
	if request.path != "/v1/subscriptions" || request.query["customer"] != "cus_1" || request.query["limit"] != "10" {
		t.Fatalf("request = %s %v", request.path, request.query)
	}
}

// Parity-exempt: upstream has no Python test; behavior ported from
// tools/src/aden_tools/tools/stripe_tool/stripe_tool.py::cancel_subscription
func TestStripeCancelSubscriptionBranches(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"id":"sub_1","cancel_at_period_end":true}`
	})
	registry := New(
		WithBaseURL("stripe_tool", provider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_cancel_subscription", "subscription_id": "sub_1", "at_period_end": true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_cancel_subscription", "subscription_id": "sub_1",
	}); err != nil {
		t.Fatal(err)
	}
	requests := provider.snapshot()
	if requests[0].method != http.MethodPost || requests[0].path != "/v1/subscriptions/sub_1" {
		t.Fatalf("scheduled = %s %s", requests[0].method, requests[0].path)
	}
	if !strings.Contains(string(requests[0].body), "cancel_at_period_end=true") {
		t.Fatalf("body = %s", requests[0].body)
	}
	if requests[1].method != http.MethodDelete || requests[1].path != "/v1/subscriptions/sub_1" {
		t.Fatalf("immediate = %s %s", requests[1].method, requests[1].path)
	}
}
