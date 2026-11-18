package integrations

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksQuery::test_successful_query
func TestBatch6Lane3QuickBooksQuery(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/123456789/query" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"QueryResponse": map[string]any{
				"Customer": []any{
					map[string]any{"Id": "1", "DisplayName": "ABC Corp", "Balance": 1250.00},
					map[string]any{"Id": "2", "DisplayName": "XYZ Inc", "Balance": 500.00},
				},
				"totalCount": 2,
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action": "quickbooks_query", "entity": "Customer", "where": "Active = true",
		"order_by": "DisplayName ASC", "max_results": 5000, "start_position": 5,
	})
	if result["count"] != 2 || result["total_count"] != float64(2) {
		t.Fatalf("result = %v", result)
	}
	entity := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["entities"])[0])
	if entity["DisplayName"] != "ABC Corp" || entity["Balance"] != float64(1250) {
		t.Fatalf("entity = %v", entity)
	}
	request := recorder.last(t)
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	want := "SELECT * FROM Customer WHERE Active = true ORDERBY DisplayName ASC STARTPOSITION 5 MAXRESULTS 1000"
	if got := request.Query.Get("query"); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got := request.Query.Get("minorversion"); got != "73" {
		t.Fatalf("minorversion = %q", got)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksQuery::test_missing_entity
func TestBatch6Lane3QuickBooksQueryMissingEntity(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without entity")
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_query", "entity": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "entity is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksQuery::test_missing_credentials
func TestBatch6Lane3QuickBooksMissingCredentials(t *testing.T) {
	registry := newBatch6Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_query", "entity": "Customer"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "QUICKBOOKS_ACCESS_TOKEN") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksGetEntity::test_successful_get
func TestBatch6Lane3QuickBooksGetEntity(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/123456789/customer/1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"Customer": map[string]any{"Id": "1", "DisplayName": "ABC Corp", "Balance": 1250.00, "SyncToken": "0"},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action": "quickbooks_get_entity", "entity": "Customer", "entity_id": "1",
	})
	if result["DisplayName"] != "ABC Corp" || result["Balance"] != float64(1250) {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Query.Get("minorversion"); got != "73" {
		t.Fatalf("minorversion = %q", got)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksGetEntity::test_missing_params
func TestBatch6Lane3QuickBooksGetEntityMissingParams(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without entity and id")
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_get_entity", "entity": "", "entity_id": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "entity and entity_id are required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksCreateCustomer::test_successful_create
func TestBatch6Lane3QuickBooksCreateCustomer(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/123456789/customer" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if body["DisplayName"] != "New Customer" {
			t.Fatalf("body = %v", body)
		}
		if batch6Lane3BodyMap(t, body["PrimaryEmailAddr"])["Address"] != "new@example.com" {
			t.Fatalf("body = %v", body)
		}
		if _, present := body["PrimaryPhone"]; present {
			t.Fatalf("empty phone must be omitted: %v", body)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"Customer": map[string]any{"Id": "59", "DisplayName": "New Customer", "SyncToken": "0"},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action": "quickbooks_create_customer", "display_name": "New Customer", "email": "new@example.com",
	})
	if result["result"] != "created" || result["id"] != "59" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksCreateCustomer::test_missing_name
func TestBatch6Lane3QuickBooksCreateCustomerMissingName(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without display_name")
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_create_customer", "display_name": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "display_name is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksCreateInvoice::test_successful_create
func TestBatch6Lane3QuickBooksCreateInvoice(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/123456789/invoice" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if batch6Lane3BodyMap(t, body["CustomerRef"])["value"] != "1" {
			t.Fatalf("body = %v", body)
		}
		line := batch6Lane3BodyMap(t, batch6Lane3Slice(t, body["Line"])[0])
		if line["Amount"] != float64(100) || line["DetailType"] != "SalesItemLineDetail" || line["Description"] != "Consulting" {
			t.Fatalf("line = %v", line)
		}
		detail := batch6Lane3BodyMap(t, line["SalesItemLineDetail"])
		if batch6Lane3BodyMap(t, detail["ItemRef"])["value"] != "1" {
			t.Fatalf("detail = %v", detail)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"Invoice": map[string]any{"Id": "130", "DocNumber": "1001", "TotalAmt": 100.00, "Balance": 100.00, "SyncToken": "0"},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action":      "quickbooks_create_invoice",
		"customer_id": "1",
		"line_items":  `[{"description": "Consulting", "amount": 100.00, "item_id": "1", "quantity": 2, "unit_price": 50.0}]`,
	})
	if result["result"] != "created" || result["id"] != "130" || result["total_amt"] != float64(100) {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksCreateInvoice::test_invalid_json
func TestBatch6Lane3QuickBooksCreateInvoiceInvalidJSON(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent for invalid line_items")
	})
	for _, payload := range []string{"not json", `{}`, `[]`} {
		result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
			"action": "quickbooks_create_invoice", "customer_id": "1", "line_items": payload,
		})
		if _, failed := result["error"]; !failed {
			t.Fatalf("line_items %q accepted: %v", payload, result)
		}
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksCreateInvoice::test_missing_params
func TestBatch6Lane3QuickBooksCreateInvoiceMissingParams(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without customer_id and line_items")
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action": "quickbooks_create_invoice", "customer_id": "", "line_items": "",
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "customer_id and line_items are required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksGetCompanyInfo::test_successful_get
func TestBatch6Lane3QuickBooksGetCompanyInfo(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/123456789/companyinfo/123456789" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"CompanyInfo": map[string]any{
				"CompanyName": "My Company", "LegalName": "My Company LLC", "Country": "US",
				"Email": map[string]any{"Address": "info@mycompany.com"}, "FiscalYearStartMonth": "January",
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_get_company_info"})
	if result["company_name"] != "My Company" || result["email"] != "info@mycompany.com" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_quickbooks_tool.py::TestQuickbooksGetCompanyInfo::test_missing_credentials
func TestBatch6Lane3QuickBooksCompanyInfoMissingCredentials(t *testing.T) {
	registry := newBatch6Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_get_company_info"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "QUICKBOOKS_ACCESS_TOKEN") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream has no test for quickbooks_list_invoices; the query
// clauses mirror quickbooks_tool.py (today comes from the runtime clock).
func TestBatch6Lane3QuickBooksListInvoices(t *testing.T) {
	clock := func() time.Time { return time.Date(2024, 1, 15, 9, 0, 0, 0, time.UTC) }
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"QueryResponse": map[string]any{"Invoice": []any{
				map[string]any{
					"Id": "130", "DocNumber": "1001", "TotalAmt": 100.25, "Balance": 25.25,
					"DueDate": "2024-01-01", "TxnDate": "2023-12-20", "EmailStatus": "NotSet",
					"CustomerRef": map[string]any{"value": "1", "name": "ABC Corp"},
				},
			}},
		})
	}, WithClock(clock))
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action": "quickbooks_list_invoices", "status": "Overdue", "customer_id": "1",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	invoice := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["invoices"])[0])
	if invoice["customer_name"] != "ABC Corp" || invoice["total_amt"] != float64(100.25) || invoice["balance"] != float64(25.25) {
		t.Fatalf("invoice = %v", invoice)
	}
	want := "SELECT * FROM Invoice WHERE DueDate < '2024-01-15' AND Balance > '0' AND CustomerRef = '1' MAXRESULTS 100"
	if got := recorder.last(t).Query.Get("query"); got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
}

// Parity-exempt: upstream has no test for quickbooks_get_customer; the
// projection mirrors quickbooks_tool.py, including address defaults.
func TestBatch6Lane3QuickBooksGetCustomer(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"Customer": map[string]any{
				"Id": "1", "DisplayName": "ABC Corp", "CompanyName": "ABC",
				"GivenName": "Ann", "FamilyName": "Lee", "Balance": 1250.00, "Active": true,
				"PrimaryEmailAddr": map[string]any{"Address": "ann@example.com"},
				"PrimaryPhone":     map[string]any{"FreeFormNumber": "555-0100"},
				"BillAddr":         map[string]any{"Line1": "1 Main St", "City": "Springfield"},
				"SyncToken":        "3",
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_get_customer", "customer_id": "1"})
	if result["email"] != "ann@example.com" || result["phone"] != "555-0100" || result["balance"] != float64(1250) {
		t.Fatalf("result = %v", result)
	}
	address := batch6Lane3BodyMap(t, result["billing_address"])
	if address["line1"] != "1 Main St" || address["city"] != "Springfield" || address["state"] != "" {
		t.Fatalf("billing_address = %v", address)
	}
}

// Parity-exempt: upstream has no test for quickbooks_create_payment; the body
// and projection mirror quickbooks_tool.py.
func TestBatch6Lane3QuickBooksCreatePayment(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/123456789/payment" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if body["TotalAmt"] != float64(100.5) {
			t.Fatalf("body = %v", body)
		}
		line := batch6Lane3BodyMap(t, batch6Lane3Slice(t, body["Line"])[0])
		linked := batch6Lane3BodyMap(t, batch6Lane3Slice(t, line["LinkedTxn"])[0])
		if linked["TxnId"] != "130" || linked["TxnType"] != "Invoice" {
			t.Fatalf("linked = %v", linked)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"Payment": map[string]any{
				"Id": "9", "TotalAmt": 100.50, "TxnDate": "2024-01-15", "SyncToken": "0",
				"CustomerRef": map[string]any{"value": "1"},
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{
		"action": "quickbooks_create_payment", "customer_id": "1", "total_amt": 100.5, "invoice_id": "130",
	})
	if result["result"] != "created" || result["customer_id"] != "1" || result["total_amt"] != float64(100.5) {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}

	invalid, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent for a non-positive payment")
	})
	result = batch6Lane3Execute(t, invalid, "quickbooks", map[string]any{
		"action": "quickbooks_create_payment", "customer_id": "1", "total_amt": 0,
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "positive total_amt") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream tests mock httpx and never exercise status mapping;
// the message format is ported from quickbooks_tool._get/_post.
func TestBatch6Lane3QuickBooksHTTPErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			registry, _ := batch6Lane3TestRegistry(t, "quickbooks_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6Lane3WriteJSON(w, status, map[string]any{"Fault": "failure"})
			})
			result := batch6Lane3Execute(t, registry, "quickbooks", map[string]any{"action": "quickbooks_query", "entity": "Customer"})
			if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "HTTP ") {
				t.Fatalf("error = %q", got)
			}
		})
	}
}
