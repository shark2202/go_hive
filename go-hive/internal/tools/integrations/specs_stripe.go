package integrations

import "net/http"

// Stripe's REST surface is regular: resource collections plus the handful of
// action endpoints (confirm/cancel/pay/finalize/void/capture/detach).
// Bodies are form-encoded; nested objects use bracket notation.
func stripeCreate(resource string) RestSpec {
	return RestSpec{Method: http.MethodPost, Path: "/v1/" + resource}
}

func stripeGet(resource, idParam string) RestSpec {
	return RestSpec{Method: http.MethodGet, Path: "/v1/" + resource + "/{" + idParam + "}"}
}

func stripeUpdate(resource, idParam string) RestSpec {
	return RestSpec{Method: http.MethodPost, Path: "/v1/" + resource + "/{" + idParam + "}"}
}

func stripeDelete(resource, idParam string) RestSpec {
	return RestSpec{Method: http.MethodDelete, Path: "/v1/" + resource + "/{" + idParam + "}"}
}

// stripeList declares one list endpoint. Stripe advertises one page per call
// and accepts starting_after=<id of the last item on the previous page>; the
// declarative walk derives that cursor from the merged item's id and stops when
// the page reports has_more=false (upstream stripe_list_* document
// starting_after for every list tool).
func stripeList(resource, listKey string) RestSpec {
	return RestSpec{
		Method: http.MethodGet, Path: "/v1/" + resource,
		Pagination: &PaginationSpec{
			ItemsPath:     "data",
			CursorParam:   "starting_after",
			LastItemField: "id",
		},
		Shape: stripeListShape(listKey, stripeResourceFormatter(listKey)),
	}
}

// stripeResourceFormatter returns the resource formatter for a list key.
func stripeResourceFormatter(listKey string) func(any) map[string]any {
	switch listKey {
	case "customers":
		return stripeCustomer
	case "subscriptions":
		return stripeSubscription
	case "payment_intents":
		return stripePaymentIntent
	case "charges":
		return stripeCharge
	case "refunds":
		return stripeRefund
	case "invoices":
		return stripeInvoice
	case "invoice_items":
		return stripeInvoiceItem
	case "products":
		return stripeProduct
	case "prices":
		return stripePrice
	case "payment_links":
		return stripePaymentLink
	case "coupons":
		return stripeCoupon
	case "transactions":
		return stripeBalanceTransaction
	case "webhook_endpoints":
		return stripeWebhookEndpoint
	case "payment_methods":
		return stripePaymentMethod
	case "disputes":
		return stripeDispute
	case "events":
		return stripeEvent
	}
	return func(any) map[string]any { return map[string]any{} }
}

func stripeAction(resource, idParam, action, bodyKey string) RestSpec {
	return RestSpec{Method: http.MethodPost, Path: "/v1/" + resource + "/{" + idParam + "}/" + action}
}

func stripeSpecs() map[string]RestSpec {
	return map[string]RestSpec{
		"stripe_create_customer":       stripeCreate("customers"),
		"stripe_get_customer":          stripeGet("customers", "customer_id"),
		"stripe_update_customer":       stripeUpdate("customers", "customer_id"),
		"stripe_list_customers":        stripeList("customers", "customers"),
		"stripe_get_customer_by_email": {Method: http.MethodGet, Path: "/v1/customers", Constants: map[string]any{"limit": 1}, FirstOf: "data"},

		"stripe_get_subscription":        stripeGet("subscriptions", "subscription_id"),
		"stripe_list_subscriptions":      withRename(stripeList("subscriptions", "subscriptions"), map[string]string{"customer_id": "customer"}),
		"stripe_get_subscription_status": {Method: http.MethodGet, Path: "/v1/subscriptions", ListKey: "subscriptions", HasMore: true, Rename: map[string]string{"customer_id": "customer"}},
		"stripe_create_subscription": {
			Method: http.MethodPost, Path: "/v1/subscriptions",
			Rename: map[string]string{
				"customer_id": "customer",
				"price_id":    "items[0][price]",
				"quantity":    "items[0][quantity]",
			},
		},
		"stripe_update_subscription": {
			Method: http.MethodPost, Path: "/v1/subscriptions/{subscription_id}",
			Rename: map[string]string{
				"price_id": "items[0][price]",
				"quantity": "items[0][quantity]",
			},
		},
		"stripe_cancel_subscription": stripeDelete("subscriptions", "subscription_id"),

		"stripe_create_payment_intent": {
			Method: http.MethodPost, Path: "/v1/payment_intents",
			Rename: map[string]string{"customer_id": "customer"},
		},
		"stripe_get_payment_intent":     stripeGet("payment_intents", "payment_intent_id"),
		"stripe_confirm_payment_intent": stripeAction("payment_intents", "payment_intent_id", "confirm", ""),
		"stripe_cancel_payment_intent":  stripeAction("payment_intents", "payment_intent_id", "cancel", ""),
		"stripe_list_payment_intents":   withRename(stripeList("payment_intents", "payment_intents"), map[string]string{"customer_id": "customer"}),
		"stripe_list_charges":           withRename(stripeList("charges", "charges"), map[string]string{"customer_id": "customer", "payment_intent_id": "payment_intent"}),
		"stripe_get_charge":             stripeGet("charges", "charge_id"),
		"stripe_capture_charge":         stripeAction("charges", "charge_id", "capture", ""),
		"stripe_create_refund":          withRename(stripeCreate("refunds"), map[string]string{"charge_id": "charge", "payment_intent_id": "payment_intent"}),
		"stripe_get_refund":             stripeGet("refunds", "refund_id"),
		"stripe_list_refunds":           withRename(stripeList("refunds", "refunds"), map[string]string{"charge_id": "charge", "payment_intent_id": "payment_intent"}),

		"stripe_list_invoices":  withRename(stripeList("invoices", "invoices"), map[string]string{"customer_id": "customer", "subscription_id": "subscription"}),
		"stripe_get_invoice":    stripeGet("invoices", "invoice_id"),
		"stripe_create_invoice": withRename(stripeCreate("invoices"), map[string]string{"customer_id": "customer"}),
		"stripe_finalize_invoice": {
			Method: http.MethodPost, Path: "/v1/invoices/{invoice_id}/finalize",
		},
		"stripe_pay_invoice":  {Method: http.MethodPost, Path: "/v1/invoices/{invoice_id}/pay"},
		"stripe_void_invoice": {Method: http.MethodPost, Path: "/v1/invoices/{invoice_id}/void"},
		"stripe_create_invoice_item": withRename(stripeCreate("invoiceitems"), map[string]string{
			"customer_id": "customer", "invoice_id": "invoice",
		}),
		"stripe_list_invoice_items": withRename(stripeList("invoiceitems", "invoice_items"), map[string]string{
			"customer_id": "customer", "invoice_id": "invoice",
		}),
		"stripe_delete_invoice_item": stripeDelete("invoiceitems", "invoice_item_id"),

		"stripe_create_product": stripeCreate("products"),
		"stripe_get_product":    stripeGet("products", "product_id"),
		"stripe_list_products":  stripeList("products", "products"),
		"stripe_update_product": stripeUpdate("products", "product_id"),
		"stripe_create_price": withRename(stripeCreate("prices"), map[string]string{
			"product_id":               "product",
			"recurring_interval":       "recurring[interval]",
			"recurring_interval_count": "recurring[interval_count]",
		}),
		"stripe_get_price":    stripeGet("prices", "price_id"),
		"stripe_list_prices":  withRename(stripeList("prices", "prices"), map[string]string{"product_id": "product"}),
		"stripe_update_price": stripeUpdate("prices", "price_id"),
		"stripe_create_payment_link": withRename(stripeCreate("payment_links"), map[string]string{
			"price_id": "line_items[0][price]",
			"quantity": "line_items[0][quantity]",
		}),
		"stripe_get_payment_link":   stripeGet("payment_links", "payment_link_id"),
		"stripe_list_payment_links": stripeList("payment_links", "payment_links"),

		"stripe_create_coupon": stripeCreate("coupons"),
		"stripe_list_coupons":  stripeList("coupons", "coupons"),
		"stripe_delete_coupon": stripeDelete("coupons", "coupon_id"),

		"stripe_get_balance":               {Method: http.MethodGet, Path: "/v1/balance"},
		"stripe_list_balance_transactions": withRename(stripeList("balance_transactions", "balance_transactions"), map[string]string{"type_filter": "type"}),
		"stripe_list_webhook_endpoints":    stripeList("webhook_endpoints", "webhook_endpoints"),
		"stripe_list_payment_methods": withRename(stripeList("payment_methods", "payment_methods"), map[string]string{
			"customer_id": "customer", "type_filter": "type",
		}),
		"stripe_get_payment_method":    stripeGet("payment_methods", "payment_method_id"),
		"stripe_detach_payment_method": {Method: http.MethodPost, Path: "/v1/payment_methods/{payment_method_id}/detach"},
		"stripe_list_disputes":         stripeList("disputes", "disputes"),
		"stripe_list_events":           withRename(stripeList("events", "events"), map[string]string{"type_filter": "type"}),
		"stripe_create_checkout_session": {
			Method: http.MethodPost, Path: "/v1/checkout/sessions",
			Rename:     map[string]string{"customer_id": "customer", "line_items_json": "line_items"},
			JSONParams: []string{"line_items"},
		},
	}
}

func withRename(spec RestSpec, rename map[string]string) RestSpec {
	spec.Rename = rename
	return spec
}
