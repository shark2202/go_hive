package integrations

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_razorpay_tool.py::TestListPaymentsTool::test_list_payments_success
func TestBatch6Lane3RazorpayListPayments(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/payments" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"count": 1,
			"items": []any{map[string]any{
				"id": "pay_123", "amount": 50000, "currency": "INR", "status": "captured",
				"method": "card", "email": "bob@example.com", "contact": "+919999999999",
				"created_at": 1700000000, "description": "Order #1", "order_id": "order_1",
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments"})
	if result["count"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	payment := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["payments"])[0])
	if payment["id"] != "pay_123" || payment["amount"] != float64(50000) || payment["currency"] != "INR" {
		t.Fatalf("payment = %v", payment)
	}
	request := recorder.last(t)
	if got := request.Query.Get("count"); got != "10" {
		t.Fatalf("count = %q", got)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("rzp_test_key:test-secret"))
	if got := request.Header.Get("Authorization"); got != wantAuth {
		t.Fatalf("Authorization = %q, want %q", got, wantAuth)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestListPaymentsTool::test_list_payments_normalizes_count
func TestBatch6Lane3RazorpayListPaymentsCountClamp(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{"count": 0, "items": []any{}})
	})
	batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments", "count": 500})
	if got := recorder.last(t).Query.Get("count"); got != "100" {
		t.Fatalf("count = %q, want 100", got)
	}
	batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments", "count": -5})
	if got := recorder.last(t).Query.Get("count"); got != "1" {
		t.Fatalf("count = %q, want 1", got)
	}
}

type batch6Lane3NetworkTransport struct{}

func (batch6Lane3NetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection failed")
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestListPaymentsTool::test_list_payments_timeout
func TestBatch6Lane3RazorpayTimeout(t *testing.T) {
	registry := newBatch6Lane3Registry(
		WithCredential("razorpay_tool", "rzp_test_key|test-secret"),
		WithGetenv(func(string) string { return "" }),
		WithHTTPClient(&http.Client{Transport: batch6Lane3TimeoutTransport{}}),
	)
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "timed out") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestListPaymentsTool::test_list_payments_network_error
func TestBatch6Lane3RazorpayNetworkError(t *testing.T) {
	registry := newBatch6Lane3Registry(
		WithCredential("razorpay_tool", "rzp_test_key|test-secret"),
		WithGetenv(func(string) string { return "" }),
		WithHTTPClient(&http.Client{Transport: batch6Lane3NetworkTransport{}}),
	)
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "Network error") || !strings.Contains(got, "connection failed") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestGetPaymentTool::test_get_payment_success
func TestBatch6Lane3RazorpayGetPayment(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/payments/pay_123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"id": "pay_123", "amount": 50000, "currency": "INR", "status": "captured",
			"method": "card", "captured": true, "fee": 1180, "tax": 180,
			"amount_refunded": 0, "refund_status": nil,
		})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_get_payment", "payment_id": "pay_123",
	})
	if result["id"] != "pay_123" || result["status"] != "captured" || result["amount"] != float64(50000) {
		t.Fatalf("result = %v", result)
	}
	if result["fee"] != float64(1180) || result["captured"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestGetPaymentTool::test_get_payment_invalid_id
func TestBatch6Lane3RazorpayGetPaymentInvalidID(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent for an invalid payment id")
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_get_payment", "payment_id": "invalid_id",
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "Must match pattern") {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestCreatePaymentLinkTool::test_create_payment_link_success
func TestBatch6Lane3RazorpayCreatePaymentLink(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/payment_links" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if body["amount"] != float64(50000) || body["currency"] != "INR" || body["description"] != "Test" {
			t.Fatalf("body = %v", body)
		}
		customer := batch6Lane3BodyMap(t, body["customer"])
		if customer["name"] != "Bob" || customer["email"] != "bob@example.com" {
			t.Fatalf("customer = %v", customer)
		}
		if _, present := customer["contact"]; present {
			t.Fatalf("empty contact must be omitted: %v", customer)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"id": "plink_123", "short_url": "https://rzp.io/rzp/test", "amount": 50000,
			"currency": "INR", "description": "Test", "status": "created", "created_at": 1700000000,
			"customer": map[string]any{"name": "Bob"},
		})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_create_payment_link", "amount": 50000, "currency": "INR",
		"description": "Test", "customer_name": "Bob", "customer_email": "bob@example.com",
	})
	if result["id"] != "plink_123" || result["short_url"] != "https://rzp.io/rzp/test" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestCreatePaymentLinkTool::test_create_payment_link_validation
func TestBatch6Lane3RazorpayCreatePaymentLinkValidation(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent for invalid payment links")
	})
	cases := []struct {
		input    map[string]any
		fragment string
	}{
		{map[string]any{"amount": -100, "currency": "INR", "description": "Test"}, "positive"},
		{map[string]any{"amount": 50000, "currency": "INVALID", "description": "Test"}, "3-letter code"},
		{map[string]any{"amount": 50000, "currency": "INR", "description": ""}, "required"},
	}
	for _, tc := range cases {
		tc.input["action"] = "razorpay_create_payment_link"
		result := batch6Lane3Execute(t, registry, "razorpay", tc.input)
		if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, tc.fragment) {
			t.Fatalf("error = %q, want %q", got, tc.fragment)
		}
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestListInvoicesTool::test_list_invoices_success
func TestBatch6Lane3RazorpayListInvoices(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/invoices" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"count": 1,
			"items": []any{map[string]any{
				"id": "inv_123", "amount": 50000, "currency": "INR", "status": "issued",
				"customer_id": "cust_1", "created_at": 1700000000, "description": "Invoice",
				"short_url": "https://rzp.io/i/test",
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_list_invoices", "type_filter": "invoice",
	})
	if result["count"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	invoice := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["invoices"])[0])
	if invoice["id"] != "inv_123" || invoice["customer_id"] != "cust_1" {
		t.Fatalf("invoice = %v", invoice)
	}
	if got := recorder.last(t).Query.Get("type"); got != "invoice" {
		t.Fatalf("type = %q", got)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestGetInvoiceTool::test_get_invoice_success
func TestBatch6Lane3RazorpayGetInvoice(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/invoices/inv_123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"id": "inv_123", "amount": 50000, "currency": "INR", "status": "paid",
			"customer_id": "cust_1", "customer_details": map[string]any{"name": "Bob"},
			"line_items": []any{map[string]any{"name": "Widget", "amount": 50000}},
			"created_at": 1700000000, "description": "Invoice",
			"short_url": "https://rzp.io/i/test", "paid_at": 1700000100,
		})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_get_invoice", "invoice_id": "inv_123",
	})
	if result["id"] != "inv_123" || result["status"] != "paid" {
		t.Fatalf("result = %v", result)
	}
	if len(batch6Lane3Slice(t, result["line_items"])) != 1 {
		t.Fatalf("line_items = %v", result["line_items"])
	}
	if batch6Lane3BodyMap(t, result["customer_details"])["name"] != "Bob" {
		t.Fatalf("customer_details = %v", result["customer_details"])
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestGetInvoiceTool::test_get_invoice_invalid_id
func TestBatch6Lane3RazorpayGetInvoiceInvalidID(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent for an invalid invoice id")
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_get_invoice", "invoice_id": "invalid",
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "inv_[A-Za-z0-9]+") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestCreateRefundTool::test_create_refund_success
func TestBatch6Lane3RazorpayCreateRefund(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/payments/pay_123/refund" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"id": "rfnd_123", "payment_id": "pay_123", "amount": 10000, "currency": "INR",
			"status": "processed", "created_at": 1700000000,
			"notes": map[string]any{"reason": "Customer request"}, "speed_processed": "normal",
		})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_create_refund", "payment_id": "pay_123", "amount": 10000,
		"notes": map[string]any{"reason": "Customer request"},
	})
	if result["id"] != "rfnd_123" || result["amount"] != float64(10000) || result["status"] != "processed" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["amount"] != float64(10000) {
		t.Fatalf("body = %v", body)
	}
	if batch6Lane3BodyMap(t, body["notes"])["reason"] != "Customer request" {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestCreateRefundTool::test_create_refund_validation
func TestBatch6Lane3RazorpayCreateRefundValidation(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{"id": "rfnd_1", "payment_id": "pay_123"})
	})
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_create_refund", "payment_id": "invalid", "amount": 100,
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "Must match pattern") {
		t.Fatalf("error = %q", got)
	}
	result = batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_create_refund", "payment_id": "pay_123", "amount": -1,
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "must be positive") {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d", recorder.count())
	}

	// A full refund omits the amount and notes entirely.
	result = batch6Lane3Execute(t, registry, "razorpay", map[string]any{
		"action": "razorpay_create_refund", "payment_id": "pay_123",
	})
	if result["id"] != "rfnd_1" {
		t.Fatalf("result = %v", result)
	}
	if body := recorder.last(t).Body; len(body) != 0 {
		t.Fatalf("full refund body = %v, want empty", body)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestToolRegistration::test_no_credentials_returns_error
func TestBatch6Lane3RazorpayMissingCredentials(t *testing.T) {
	registry := newBatch6Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_razorpay_tool.py::TestRazorpayClient::test_handle_response_errors
func TestBatch6Lane3RazorpayHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid Razorpay API credentials"},
		{http.StatusForbidden, "Insufficient permissions"},
		{http.StatusNotFound, "not found"},
		{http.StatusBadRequest, "Bad request"},
		{http.StatusTooManyRequests, "rate limit"},
		{http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch6Lane3TestRegistry(t, "razorpay_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6Lane3WriteJSON(w, tc.status, map[string]any{
					"error": map[string]any{"description": "provider detail"},
				})
			})
			result := batch6Lane3Execute(t, registry, "razorpay", map[string]any{"action": "razorpay_list_payments"})
			if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}
