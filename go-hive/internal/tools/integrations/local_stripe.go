package integrations

import (
	"context"
	"net/http"
)

// stripeSubscriptionStatus ports the upstream two-branch status lookup: list
// the customer's subscriptions, report the first status, and format each
// subscription like stripe_get_subscription.
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
	if problem, failed := result.(map[string]any); failed {
		if _, ok := problem["error"]; ok {
			return problem, nil
		}
	}
	raw, _ := result.(map[string]any)
	subscriptions := shapeList(raw, "subscriptions")
	formatted := make([]any, 0, len(subscriptions))
	status := "no_subscription"
	for index, subscription := range subscriptions {
		projected := stripeSubscription(subscription)
		if index == 0 {
			if value, ok := projected["status"].(string); ok && value != "" {
				status = value
			}
		}
		formatted = append(formatted, projected)
	}
	return map[string]any{
		"customer_id":   customerID,
		"status":        status,
		"subscriptions": formatted,
	}, nil
}

// stripeCancelSubscription honours at_period_end: a scheduled cancel is an
// update, an immediate cancel is the provider's cancel endpoint. Both return
// the formatted subscription like stripe_get_subscription.
func stripeCancelSubscription(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	spec := RestSpec{
		Method: http.MethodDelete,
		Path:   "/v1/subscriptions/{subscription_id}",
	}
	if boolArg(input, "at_period_end", false) {
		spec = RestSpec{
			Method: http.MethodPost,
			Path:   "/v1/subscriptions/{subscription_id}",
			Body:   []string{},
			Build: func(map[string]any) map[string]any {
				return map[string]any{"cancel_at_period_end": true}
			},
		}
	}
	result, err := rt.CallHelper(ctx, "stripe_tool", "Stripe", spec, input)
	if err != nil {
		return result, err
	}
	if problem, ok := result.(map[string]any); ok {
		if _, failed := problem["error"]; failed {
			return problem, nil
		}
	}
	return stripeSubscription(result), nil
}
