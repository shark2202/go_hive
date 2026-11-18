package integrations

import (
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetAccounts::test_successful_get
func TestBatch6Lane3PlaidGetAccounts(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/get" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if body["client_id"] != "test-client" || body["secret"] != "test-secret" {
			t.Fatalf("body = %v", body)
		}
		if body["access_token"] != "access-sandbox-123" {
			t.Fatalf("body = %v", body)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"accounts": []any{map[string]any{
				"account_id": "acc-1", "name": "Checking", "official_name": "Primary Checking",
				"type": "depository", "subtype": "checking", "mask": "1234",
				"balances": map[string]any{"available": 1000.50, "current": 1100.00, "iso_currency_code": "USD"},
			}},
			"request_id": "req-1",
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_get_accounts", "access_token": "access-sandbox-123",
	})
	accounts := batch6Lane3Slice(t, result["accounts"])
	if len(accounts) != 1 || result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	account := batch6Lane3BodyMap(t, accounts[0])
	if account["name"] != "Checking" || account["available_balance"] != float64(1000.5) || account["currency"] != "USD" {
		t.Fatalf("account = %v", account)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetAccounts::test_missing_access_token
func TestBatch6Lane3PlaidGetAccountsMissingToken(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without access_token")
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{"action": "plaid_get_accounts", "access_token": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "access_token is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetAccounts::test_missing_credentials
func TestBatch6Lane3PlaidMissingCredentials(t *testing.T) {
	registry := newBatch6Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{"action": "plaid_get_accounts", "access_token": "tok"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "PLAID_CLIENT_ID") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetBalance::test_successful_balance
func TestBatch6Lane3PlaidGetBalance(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/accounts/balance/get" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"accounts": []any{map[string]any{
				"account_id": "acc-1", "name": "Savings", "type": "depository",
				"balances": map[string]any{"available": 5000, "current": 5000, "limit": nil, "iso_currency_code": "USD"},
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_get_balance", "access_token": "access-sandbox-123",
	})
	account := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["accounts"])[0])
	if account["available"] != float64(5000) || account["current"] != float64(5000) {
		t.Fatalf("account = %v", account)
	}
	if _, present := result["count"]; present {
		t.Fatalf("balance response must not include count: %v", result)
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidSyncTransactions::test_successful_sync
func TestBatch6Lane3PlaidSyncTransactions(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/transactions/sync" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if body["count"] != float64(100) || body["cursor"] != "cursor-old" {
			t.Fatalf("body = %v", body)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"added": []any{map[string]any{
				"transaction_id": "txn-1", "account_id": "acc-1", "amount": 42.50,
				"date": "2024-01-15", "name": "Coffee Shop", "merchant_name": "Starbucks",
				"category": []any{"Food and Drink"}, "pending": false, "iso_currency_code": "USD",
			}},
			"modified":    []any{},
			"removed":     []any{map[string]any{"transaction_id": "txn-2"}},
			"next_cursor": "cursor-abc",
			"has_more":    true,
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_sync_transactions", "access_token": "access-sandbox-123", "cursor": "cursor-old",
	})
	added := batch6Lane3Slice(t, result["added"])
	if len(added) != 1 {
		t.Fatalf("result = %v", result)
	}
	transaction := batch6Lane3BodyMap(t, added[0])
	if transaction["amount"] != float64(42.5) || transaction["currency"] != "USD" || transaction["merchant_name"] != "Starbucks" {
		t.Fatalf("transaction = %v", transaction)
	}
	removed, ok := result["removed"].([]string)
	if !ok || len(removed) != 1 || removed[0] != "txn-2" {
		t.Fatalf("removed = %v", result["removed"])
	}
	if result["next_cursor"] != "cursor-abc" || result["has_more"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidSyncTransactions::test_missing_token
func TestBatch6Lane3PlaidSyncMissingToken(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without access_token")
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{"action": "plaid_sync_transactions", "access_token": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "access_token is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetTransactions::test_successful_get
func TestBatch6Lane3PlaidGetTransactions(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if body["start_date"] != "2024-01-01" || body["end_date"] != "2024-01-31" {
			t.Fatalf("body = %v", body)
		}
		options := batch6Lane3BodyMap(t, body["options"])
		if options["count"] != float64(500) || options["offset"] != float64(0) {
			t.Fatalf("options = %v", options)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"transactions": []any{map[string]any{
				"transaction_id": "txn-1", "account_id": "acc-1", "amount": 25.00,
				"date": "2024-01-10", "name": "Grocery Store", "merchant_name": "Whole Foods",
				"category": []any{"Shops", "Groceries"}, "pending": false, "iso_currency_code": "USD",
			}},
			"total_transactions": 1,
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_get_transactions", "access_token": "access-sandbox-123",
		"start_date": "2024-01-01", "end_date": "2024-01-31", "count": 900, "offset": -5,
	})
	if len(batch6Lane3Slice(t, result["transactions"])) != 1 || result["total_transactions"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetTransactions::test_missing_params
func TestBatch6Lane3PlaidGetTransactionsMissingParams(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without dates")
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_get_transactions", "access_token": "", "start_date": "", "end_date": "",
	})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "are required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetInstitution::test_successful_get
func TestBatch6Lane3PlaidGetInstitution(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/institutions/get_by_id" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		codes := batch6Lane3Slice(t, body["country_codes"])
		if len(codes) != 1 || codes[0] != "US" {
			t.Fatalf("country_codes = %v", body["country_codes"])
		}
		if batch6Lane3BodyMap(t, body["options"])["include_optional_metadata"] != true {
			t.Fatalf("options = %v", body["options"])
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"institution": map[string]any{
				"institution_id": "ins_1", "name": "Bank of America",
				"products": []any{"transactions", "auth", "balance"}, "country_codes": []any{"US"},
				"url": "https://www.bankofamerica.com", "logo": nil, "oauth": true,
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_get_institution", "institution_id": "ins_1",
	})
	if result["name"] != "Bank of America" || result["oauth"] != true {
		t.Fatalf("result = %v", result)
	}
	if _, present := result["logo"]; !present || result["logo"] != nil {
		t.Fatalf("logo = %v", result["logo"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidGetInstitution::test_missing_id
func TestBatch6Lane3PlaidGetInstitutionMissingID(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without institution_id")
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{"action": "plaid_get_institution", "institution_id": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "institution_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidSearchInstitutions::test_successful_search
func TestBatch6Lane3PlaidSearchInstitutions(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/institutions/search" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		options := batch6Lane3BodyMap(t, body["options"])
		if options["limit"] != float64(50) {
			t.Fatalf("options = %v", options)
		}
		products := batch6Lane3Slice(t, body["products"])
		if len(products) != 1 || products[0] != "transactions" {
			t.Fatalf("products = %v", body["products"])
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"institutions": []any{map[string]any{
				"institution_id": "ins_1", "name": "Chase", "products": []any{"transactions"},
				"country_codes": []any{"US"}, "url": "https://www.chase.com", "oauth": false,
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{
		"action": "plaid_search_institutions", "query": "Chase", "limit": 500,
		"products": []any{"transactions"},
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	if batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["institutions"])[0])["name"] != "Chase" {
		t.Fatalf("institutions = %v", result["institutions"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_plaid_tool.py::TestPlaidSearchInstitutions::test_missing_query
func TestBatch6Lane3PlaidSearchInstitutionsMissingQuery(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without query")
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{"action": "plaid_search_institutions", "query": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "query is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream tests mock httpx and never exercise status mapping;
// the message format is ported from plaid_tool._post.
func TestBatch6Lane3PlaidHTTPErrors(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "plaid_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusBadRequest, map[string]any{
			"error_type": "INVALID_INPUT", "error_code": "INVALID_ACCESS_TOKEN", "error_message": "invalid token",
		})
	})
	result := batch6Lane3Execute(t, registry, "plaid", map[string]any{"action": "plaid_get_accounts", "access_token": "tok"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "Plaid API error: invalid token") {
		t.Fatalf("error = %q", got)
	}
}
