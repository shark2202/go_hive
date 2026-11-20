package integrations

import (
	"context"
	"net/http"
)

// stripeSubscriptionStatus ports the upstream two-branch status lookup: list
// the customer's subscriptions and report the first status.
func stripeSubscriptionStatus(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	customerID := stringArg(input, "customer_id")
	if customerID == "" {
		return map[string]any{"error": "customer_id is required"}, nil
	}
	result, err := rt.CallHelper(ctx, "stripe_tool", "Stripe", RestSpec{
		Method:    http.MethodGet,
		Path:      "/v1/subscriptions",
		ListKey:   "subscriptions",
		HasMore:   true,
		Rename:    map[string]string{"customer_id": "customer"},
		Constants: map[string]any{"limit": 10},
	}, input)
	if err != nil {
		return nil, err
	}
	object, _ := result.(map[string]any)
	subscriptions, _ := object["subscriptions"].([]any)
	status := "no_subscription"
	if len(subscriptions) > 0 {
		if first, ok := subscriptions[0].(map[string]any); ok {
			if value := stringArg(first, "status"); value != "" {
				status = value
			}
		}
	}
	return map[string]any{
		"customer_id":   customerID,
		"status":        status,
		"subscriptions": subscriptions,
	}, nil
}

// stripeCancelSubscription honours at_period_end: a scheduled cancel is an
// update, an immediate cancel is the provider's cancel endpoint.
func stripeCancelSubscription(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !boolArg(input, "at_period_end", false) {
		return rt.CallHelper(ctx, "stripe_tool", "Stripe", RestSpec{
			Method: http.MethodDelete,
			Path:   "/v1/subscriptions/{subscription_id}",
		}, input)
	}
	return rt.CallHelper(ctx, "stripe_tool", "Stripe", RestSpec{
		Method: http.MethodPost,
		Path:   "/v1/subscriptions/{subscription_id}",
		Body:   []string{},
		Build: func(map[string]any) map[string]any {
			return map[string]any{"cancel_at_period_end": true}
		},
	}, input)
}
