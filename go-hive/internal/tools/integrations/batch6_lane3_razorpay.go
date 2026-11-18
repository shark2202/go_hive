// Razorpay payments and billing (upstream razorpay_tool, 6 actions). Part of
// batch 6 lane 3; see batch6_lane3_registry.go for the registry and shared
// helpers.
//
// Upstream authenticates with HTTP Basic (key/secret) and treats amounts as
// integers in the smallest currency unit (paise for INR); those integers are
// forwarded unchanged. Payment/invoice IDs are validated against the upstream
// prefixes before any request is made.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

const batch6Lane3RazorpayAPIBase = "https://api.razorpay.com/v1"

var (
	batch6Lane3RazorpayPaymentPattern = regexp.MustCompile(`^pay_[A-Za-z0-9]+$`)
	batch6Lane3RazorpayInvoicePattern = regexp.MustCompile(`^inv_[A-Za-z0-9]+$`)
)

var batch6Lane3RazorpayTool = &Tool{
	ID:          "razorpay_tool",
	Name:        "razorpay",
	DisplayName: "Razorpay",
	Summary:     "Razorpay payments integration (6 actions).",
	Actions: []Action{
		{
			Name:    "razorpay_list_payments",
			Summary: "List recent payments with optional filters.",
			Source:  "razorpay_tool/razorpay_tool.py",
			Params: map[string]any{
				"count":          paramDefault("integer", 10),
				"skip":           paramDefault("integer", 0),
				"from_timestamp": param("integer"),
				"to_timestamp":   param("integer"),
			},
		},
		{
			Name:     "razorpay_get_payment",
			Summary:  "Fetch a single payment by ID.",
			Source:   "razorpay_tool/razorpay_tool.py",
			Required: []string{"payment_id"},
			Params: map[string]any{
				"payment_id": param("string"),
			},
		},
		{
			Name:     "razorpay_create_payment_link",
			Summary:  "Create a one-time payment link.",
			Source:   "razorpay_tool/razorpay_tool.py",
			Required: []string{"amount", "currency", "description"},
			Params: map[string]any{
				"amount":           param("integer"),
				"currency":         param("string"),
				"description":      param("string"),
				"customer_name":    param("string"),
				"customer_email":   param("string"),
				"customer_contact": param("string"),
			},
		},
		{
			Name:    "razorpay_list_invoices",
			Summary: "List invoices with optional filters.",
			Source:  "razorpay_tool/razorpay_tool.py",
			Params: map[string]any{
				"count":       paramDefault("integer", 10),
				"skip":        paramDefault("integer", 0),
				"type_filter": param("string"),
			},
		},
		{
			Name:     "razorpay_get_invoice",
			Summary:  "Fetch invoice details and line items.",
			Source:   "razorpay_tool/razorpay_tool.py",
			Required: []string{"invoice_id"},
			Params: map[string]any{
				"invoice_id": param("string"),
			},
		},
		{
			Name:     "razorpay_create_refund",
			Summary:  "Create a full or partial refund for a payment.",
			Source:   "razorpay_tool/razorpay_tool.py",
			Required: []string{"payment_id"},
			Params: map[string]any{
				"payment_id": param("string"),
				"amount":     param("integer"),
				"notes":      param("object"),
			},
		},
	},
}

func batch6Lane3RazorpayImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"razorpay_list_payments":       batch6Lane3RazorpayListPayments,
		"razorpay_get_payment":         batch6Lane3RazorpayGetPayment,
		"razorpay_create_payment_link": batch6Lane3RazorpayCreatePaymentLink,
		"razorpay_list_invoices":       batch6Lane3RazorpayListInvoices,
		"razorpay_get_invoice":         batch6Lane3RazorpayGetInvoice,
		"razorpay_create_refund":       batch6Lane3RazorpayCreateRefund,
	}
}

func batch6Lane3RazorpayAuthError() map[string]any {
	return map[string]any{
		"error": "Razorpay credentials not configured",
		"help":  "Set RAZORPAY_API_KEY and RAZORPAY_API_SECRET environment variables. Get your credentials at https://dashboard.razorpay.com/app/keys",
	}
}

// batch6Lane3RazorpayCredentials resolves the Basic-auth key/secret pair.
func batch6Lane3RazorpayCredentials(rt *Runtime) (string, string, map[string]any) {
	key, secret, paired := batch6Lane3SplitCredential(batch6Lane3ModuleToken(rt, "razorpay_tool"))
	if !paired {
		secret = batch6Lane3Env(rt, "RAZORPAY_API_SECRET")
	}
	if key == "" || secret == "" {
		return "", "", batch6Lane3RazorpayAuthError()
	}
	return key, secret, nil
}

// batch6Lane3RazorpayBase resolves the API prefix, honouring WithBaseURL.
func batch6Lane3RazorpayBase(rt *Runtime) string {
	if base := batch6Lane3ModuleURL(rt, "razorpay_tool"); base != "" {
		return base
	}
	return batch6Lane3RazorpayAPIBase
}

// batch6Lane3RazorpayRequest performs one API call and maps status codes to the
// upstream error dicts.
func batch6Lane3RazorpayRequest(ctx context.Context, rt *Runtime, method, path string, query url.Values, payload any) map[string]any {
	key, secret, failure := batch6Lane3RazorpayCredentials(rt)
	if failure != nil {
		return failure
	}
	target := batch6Lane3RazorpayBase(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return map[string]any{"error": "Razorpay request failed: " + err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return map[string]any{"error": "Razorpay request failed: " + err.Error()}
	}
	req.SetBasicAuth(key, secret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := batch6Lane3HTTP(rt).Do(req)
	if err != nil {
		if batch6Lane3IsTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}
		}
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := batch6Lane3ReadAll(resp)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Razorpay API credentials"}
	case http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions. Check your Razorpay account access."}
	case http.StatusNotFound:
		return map[string]any{"error": "Resource not found"}
	case http.StatusBadRequest:
		return map[string]any{"error": "Bad request: " + batch6Lane3RazorpayDetail(raw)}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Razorpay rate limit exceeded. Try again later."}
	}
	if resp.StatusCode >= 400 {
		return map[string]any{
			"error": fmt.Sprintf("Razorpay API error (HTTP %d): %s", resp.StatusCode, batch6Lane3RazorpayDetail(raw)),
		}
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"error": "Razorpay API error: invalid JSON response"}
	}
	if object == nil {
		object = map[string]any{}
	}
	return object
}

// batch6Lane3RazorpayDetail mirrors response.json()["error"]["description"]
// with the raw body as the fallback.
func batch6Lane3RazorpayDetail(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if description, ok := batch6Lane3AsMap(payload["error"])["description"].(string); ok {
			return description
		}
	}
	return string(raw)
}

// batch6Lane3RazorpayCount normalizes a page size into [1, 100], mirroring the
// tool-layer clamp upstream.
func batch6Lane3RazorpayCount(input map[string]any) string {
	return strconv.Itoa(max(1, min(intArg(input, "count", 10), 100)))
}

func batch6Lane3RazorpayListPayments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	params := url.Values{}
	params.Set("count", batch6Lane3RazorpayCount(input))
	params.Set("skip", strconv.Itoa(intArg(input, "skip", 0)))
	if from, present := batch6Lane3OptionalInt(input, "from_timestamp"); present {
		params.Set("from", strconv.Itoa(from))
	}
	if to, present := batch6Lane3OptionalInt(input, "to_timestamp"); present {
		params.Set("to", strconv.Itoa(to))
	}

	data := batch6Lane3RazorpayRequest(ctx, rt, http.MethodGet, "/payments", params, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	items := batch6Lane3AsSlice(data["items"])
	payments := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		payment := batch6Lane3AsMap(raw)
		payments = append(payments, map[string]any{
			"id":          payment["id"],
			"amount":      payment["amount"],
			"currency":    payment["currency"],
			"status":      payment["status"],
			"method":      payment["method"],
			"email":       payment["email"],
			"contact":     payment["contact"],
			"created_at":  payment["created_at"],
			"description": payment["description"],
			"order_id":    payment["order_id"],
		})
	}
	return map[string]any{
		"count":    batch6Lane3ValueOr(data, "count", len(items)),
		"payments": payments,
	}, nil
}

func batch6Lane3RazorpayGetPayment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	paymentID := stringArg(input, "payment_id")
	if !batch6Lane3RazorpayPaymentPattern.MatchString(paymentID) {
		return map[string]any{"error": "Invalid payment_id. Must match pattern: pay_[A-Za-z0-9]+"}, nil
	}
	data := batch6Lane3RazorpayRequest(ctx, rt, http.MethodGet, "/payments/"+url.PathEscape(paymentID), nil, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	return map[string]any{
		"id":                data["id"],
		"amount":            data["amount"],
		"currency":          data["currency"],
		"status":            data["status"],
		"method":            data["method"],
		"email":             data["email"],
		"contact":           data["contact"],
		"created_at":        data["created_at"],
		"description":       data["description"],
		"order_id":          data["order_id"],
		"error_code":        data["error_code"],
		"error_description": data["error_description"],
		"captured":          data["captured"],
		"fee":               data["fee"],
		"tax":               data["tax"],
		"refund_status":     data["refund_status"],
		"amount_refunded":   data["amount_refunded"],
	}, nil
}

func batch6Lane3RazorpayCreatePaymentLink(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	amount, _ := batch6Lane3OptionalInt(input, "amount")
	if amount <= 0 {
		return map[string]any{"error": "Amount must be positive"}, nil
	}
	currency := stringArg(input, "currency")
	if len(currency) != 3 {
		return map[string]any{"error": "Currency must be a 3-letter code (e.g., INR, USD)"}, nil
	}
	description := stringArg(input, "description")
	if description == "" {
		return map[string]any{"error": "Description is required"}, nil
	}

	body := map[string]any{
		"amount":      amount,
		"currency":    currency,
		"description": description,
	}
	customer := map[string]any{}
	if name := stringArg(input, "customer_name"); name != "" {
		customer["name"] = name
	}
	if email := stringArg(input, "customer_email"); email != "" {
		customer["email"] = email
	}
	if contact := stringArg(input, "customer_contact"); contact != "" {
		customer["contact"] = contact
	}
	if len(customer) > 0 {
		body["customer"] = customer
	}

	data := batch6Lane3RazorpayRequest(ctx, rt, http.MethodPost, "/payment_links", nil, body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	return map[string]any{
		"id":          data["id"],
		"short_url":   data["short_url"],
		"amount":      data["amount"],
		"currency":    data["currency"],
		"description": data["description"],
		"status":      data["status"],
		"created_at":  data["created_at"],
		"customer":    data["customer"],
	}, nil
}

func batch6Lane3RazorpayListInvoices(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	params := url.Values{}
	params.Set("count", batch6Lane3RazorpayCount(input))
	params.Set("skip", strconv.Itoa(intArg(input, "skip", 0)))
	if typeFilter := stringArg(input, "type_filter"); typeFilter != "" {
		params.Set("type", typeFilter)
	}

	data := batch6Lane3RazorpayRequest(ctx, rt, http.MethodGet, "/invoices", params, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	items := batch6Lane3AsSlice(data["items"])
	invoices := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		invoice := batch6Lane3AsMap(raw)
		invoices = append(invoices, map[string]any{
			"id":          invoice["id"],
			"amount":      invoice["amount"],
			"currency":    invoice["currency"],
			"status":      invoice["status"],
			"customer_id": invoice["customer_id"],
			"created_at":  invoice["created_at"],
			"description": invoice["description"],
			"short_url":   invoice["short_url"],
		})
	}
	return map[string]any{
		"count":    batch6Lane3ValueOr(data, "count", len(items)),
		"invoices": invoices,
	}, nil
}

func batch6Lane3RazorpayGetInvoice(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	invoiceID := stringArg(input, "invoice_id")
	if !batch6Lane3RazorpayInvoicePattern.MatchString(invoiceID) {
		return map[string]any{"error": "Invalid invoice_id. Must match pattern: inv_[A-Za-z0-9]+"}, nil
	}
	data := batch6Lane3RazorpayRequest(ctx, rt, http.MethodGet, "/invoices/"+url.PathEscape(invoiceID), nil, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	return map[string]any{
		"id":               data["id"],
		"amount":           data["amount"],
		"currency":         data["currency"],
		"status":           data["status"],
		"customer_id":      data["customer_id"],
		"customer_details": data["customer_details"],
		"line_items":       batch6Lane3ValueOr(data, "line_items", []any{}),
		"created_at":       data["created_at"],
		"description":      data["description"],
		"short_url":        data["short_url"],
		"paid_at":          data["paid_at"],
		"cancelled_at":     data["cancelled_at"],
	}, nil
}

func batch6Lane3RazorpayCreateRefund(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	paymentID := stringArg(input, "payment_id")
	if !batch6Lane3RazorpayPaymentPattern.MatchString(paymentID) {
		return map[string]any{"error": "Invalid payment_id. Must match pattern: pay_[A-Za-z0-9]+"}, nil
	}
	body := map[string]any{}
	if amount, present := batch6Lane3OptionalInt(input, "amount"); present {
		if amount <= 0 {
			return map[string]any{"error": "Refund amount must be positive"}, nil
		}
		body["amount"] = amount
	}
	if notes, present := batch6Lane3OptionalMap(input, "notes"); present && len(notes) > 0 {
		body["notes"] = notes
	}

	data := batch6Lane3RazorpayRequest(ctx, rt, http.MethodPost, "/payments/"+url.PathEscape(paymentID)+"/refund", nil, body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	return map[string]any{
		"id":              data["id"],
		"payment_id":      data["payment_id"],
		"amount":          data["amount"],
		"currency":        data["currency"],
		"status":          data["status"],
		"created_at":      data["created_at"],
		"notes":           data["notes"],
		"speed_processed": data["speed_processed"],
	}, nil
}
