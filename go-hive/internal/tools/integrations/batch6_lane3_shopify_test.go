package integrations

import (
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyListOrders::test_successful_list
func TestBatch6Lane3ShopifyListOrders(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/orders.json" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"orders": []any{
				map[string]any{
					"id":                 450789469,
					"name":               "#1001",
					"email":              "bob@example.com",
					"created_at":         "2025-01-10T11:00:00-05:00",
					"financial_status":   "paid",
					"fulfillment_status": nil,
					"total_price":        "199.00",
					"currency":           "USD",
					"line_items":         []any{map[string]any{"id": 1, "title": "Widget"}},
				},
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_list_orders"})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	orders := batch6Lane3Slice(t, result["orders"])
	order := batch6Lane3BodyMap(t, orders[0])
	if order["id"] != float64(450789469) || order["total_price"] != "199.00" || order["line_item_count"] != 1 {
		t.Fatalf("order = %v", order)
	}
	request := recorder.last(t)
	if got := request.Header.Get("X-Shopify-Access-Token"); got != "test-token" {
		t.Fatalf("X-Shopify-Access-Token = %q", got)
	}
	if got := request.Query.Get("status"); got != "any" {
		t.Fatalf("status = %q", got)
	}
	if got := request.Query.Get("limit"); got != "50" {
		t.Fatalf("limit = %q", got)
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyListOrders::test_missing_credentials
func TestBatch6Lane3ShopifyMissingCredentials(t *testing.T) {
	registry := newBatch6Lane3Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_list_orders"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
	// A token without a store is still incomplete.
	registry = newBatch6Lane3Registry(
		WithCredential("shopify_tool", "bare-token"),
		WithGetenv(func(string) string { return "" }),
	)
	result = batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_list_customers"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyGetOrder::test_successful_get
func TestBatch6Lane3ShopifyGetOrder(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/orders/450789469.json" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"order": map[string]any{
				"id":               450789469,
				"name":             "#1001",
				"financial_status": "paid",
				"total_price":      "199.00",
				"subtotal_price":   "189.00",
				"total_tax":        "10.00",
				"currency":         "USD",
				"line_items": []any{map[string]any{
					"title": "Hiking Backpack", "quantity": 1, "price": "189.00",
					"sku": "HB-001", "variant_id": 39072856, "product_id": 632910392,
				}},
				"customer": map[string]any{
					"id": 207119551, "email": "bob@example.com",
					"first_name": "Bob", "last_name": "Smith",
				},
				"note": "Rush order",
				"tags": "vip",
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_get_order", "order_id": "450789469"})
	if result["id"] != float64(450789469) || result["total_price"] != "199.00" {
		t.Fatalf("result = %v", result)
	}
	items := batch6Lane3Slice(t, result["line_items"])
	item := batch6Lane3BodyMap(t, items[0])
	if item["title"] != "Hiking Backpack" || item["price"] != "189.00" || item["sku"] != "HB-001" {
		t.Fatalf("line item = %v", item)
	}
	if batch6Lane3BodyMap(t, result["customer"])["first_name"] != "Bob" {
		t.Fatalf("customer = %v", result["customer"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyGetOrder::test_missing_id
func TestBatch6Lane3ShopifyGetOrderMissingID(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without order_id")
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_get_order", "order_id": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "order_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyListProducts::test_successful_list
func TestBatch6Lane3ShopifyListProducts(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/products.json" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"products": []any{map[string]any{
				"id": 632910392, "title": "Hiking Backpack", "vendor": "TrailCo",
				"product_type": "Outdoor Gear", "status": "active", "handle": "hiking-backpack",
				"created_at": "2025-01-10T11:00:00-05:00",
				"variants":   []any{map[string]any{"id": 1}, map[string]any{"id": 2}},
				"tags":       "hiking, outdoor",
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{
		"action": "shopify_list_products", "status": "active", "vendor": "TrailCo", "limit": 300,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	product := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["products"])[0])
	if product["title"] != "Hiking Backpack" || product["variant_count"] != 2 {
		t.Fatalf("product = %v", product)
	}
	request := recorder.last(t)
	if got := request.Query.Get("status"); got != "active" {
		t.Fatalf("status = %q", got)
	}
	if got := request.Query.Get("vendor"); got != "TrailCo" {
		t.Fatalf("vendor = %q", got)
	}
	if got := request.Query.Get("limit"); got != "250" {
		t.Fatalf("limit = %q, want the 250 cap", got)
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyGetProduct::test_successful_get
func TestBatch6Lane3ShopifyGetProduct(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"product": map[string]any{
				"id": 632910392, "title": "Hiking Backpack", "vendor": "TrailCo",
				"status": "active", "tags": "hiking",
				"variants": []any{map[string]any{
					"id": 39072856, "title": "Large / Blue", "price": "199.00",
					"sku": "HB-LG-BL", "inventory_quantity": 25, "option1": "Large", "option2": "Blue",
				}},
				"options": []any{map[string]any{"name": "Size"}},
				"images":  []any{map[string]any{"id": 850703190, "src": "https://cdn.shopify.com/test.jpg", "position": 1}},
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_get_product", "product_id": "632910392"})
	variant := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["variants"])[0])
	if variant["price"] != "199.00" || variant["sku"] != "HB-LG-BL" {
		t.Fatalf("variant = %v", variant)
	}
	if len(batch6Lane3Slice(t, result["images"])) != 1 {
		t.Fatalf("images = %v", result["images"])
	}
	if len(batch6Lane3Slice(t, result["options"])) != 1 {
		t.Fatalf("options = %v", result["options"])
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyGetProduct::test_missing_id
func TestBatch6Lane3ShopifyGetProductMissingID(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without product_id")
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_get_product", "product_id": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "product_id is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifyListCustomers::test_successful_list
func TestBatch6Lane3ShopifyListCustomers(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"customers": []any{map[string]any{
				"id": 207119551, "first_name": "Bob", "last_name": "Smith",
				"email": "bob@example.com", "phone": "+161****1234", "orders_count": 5,
				"total_spent": "995.00", "state": "enabled", "tags": "vip",
				"created_at": "2025-01-10T11:00:00-05:00",
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_list_customers"})
	customer := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["customers"])[0])
	if customer["email"] != "bob@example.com" || customer["total_spent"] != "995.00" {
		t.Fatalf("customer = %v", customer)
	}
	if customer["created_at"] != "2025-01-10T11:00:00-05:00" {
		t.Fatalf("created_at = %v", customer["created_at"])
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifySearchCustomers::test_successful_search
func TestBatch6Lane3ShopifySearchCustomers(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/customers/search.json" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"customers": []any{map[string]any{
				"id": 207119551, "first_name": "Bob", "last_name": "Smith",
				"email": "bob@example.com", "orders_count": 5, "total_spent": "995.00",
				"state": "enabled", "tags": "vip",
			}},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{
		"action": "shopify_search_customers", "query": "email:bob@example.com",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	if _, present := batch6Lane3BodyMap(t, batch6Lane3Slice(t, result["customers"])[0])["created_at"]; present {
		t.Fatalf("search projection must not include created_at")
	}
	if got := recorder.last(t).Query.Get("query"); got != "email:bob@example.com" {
		t.Fatalf("query = %q", got)
	}
}

// Parity: tools/tests/tools/test_shopify_tool.py::TestShopifySearchCustomers::test_missing_query
func TestBatch6Lane3ShopifySearchCustomersMissingQuery(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without query")
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_search_customers", "query": ""})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "query is required") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream has no test for shopify_update_product; the request
// and response projection mirror shopify_tool.py.
func TestBatch6Lane3ShopifyUpdateProduct(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPut || r.URL.Path != "/products/632910392.json" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		product := batch6Lane3BodyMap(t, body["product"])
		if product["title"] != "New Title" || product["status"] != "draft" {
			t.Fatalf("product = %v", product)
		}
		if _, present := product["vendor"]; present {
			t.Fatalf("empty fields must be omitted: %v", product)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"product": map[string]any{"id": 632910392, "title": "New Title", "status": "draft", "updated_at": "2025-01-11T00:00:00Z"},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{
		"action": "shopify_update_product", "product_id": "632910392", "title": "New Title", "status": "draft",
	})
	if result["result"] != "updated" || result["title"] != "New Title" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}

	empty, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("request must not be sent without update fields")
	})
	result = batch6Lane3Execute(t, empty, "shopify", map[string]any{"action": "shopify_update_product", "product_id": "1"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "At least one field") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream has no test for shopify_get_customer; the projection
// mirrors shopify_tool.py including the address "default" fallback.
func TestBatch6Lane3ShopifyGetCustomer(t *testing.T) {
	registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/customers/207119551.json" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		batch6Lane3WriteJSON(w, http.StatusOK, map[string]any{
			"customer": map[string]any{
				"id": 207119551, "first_name": "Bob", "email": "bob@example.com",
				"orders_count": 5, "total_spent": "995.00", "state": "enabled",
				"verified_email": true, "tax_exempt": false,
				"addresses": []any{
					map[string]any{"id": 1, "city": "Ottawa", "zip": "K1A0B1", "default": true},
					map[string]any{"id": 2, "city": "Toronto"},
				},
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_get_customer", "customer_id": "207119551"})
	addresses := batch6Lane3Slice(t, result["addresses"])
	if batch6Lane3BodyMap(t, addresses[0])["default"] != true {
		t.Fatalf("addresses = %v", addresses)
	}
	if batch6Lane3BodyMap(t, addresses[1])["default"] != false {
		t.Fatalf("addresses = %v", addresses)
	}
	if result["verified_email"] != true || result["tax_exempt"] != false {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream has no test for shopify_create_draft_order; the body
// and projection mirror shopify_tool.py.
func TestBatch6Lane3ShopifyCreateDraftOrder(t *testing.T) {
	registry, recorder := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		draft := batch6Lane3BodyMap(t, body["draft_order"])
		items := batch6Lane3Slice(t, draft["line_items"])
		if len(items) != 1 {
			t.Fatalf("line_items = %v", draft["line_items"])
		}
		if batch6Lane3BodyMap(t, draft["customer"])["id"] != float64(207119551) {
			t.Fatalf("customer = %v", draft["customer"])
		}
		if draft["note"] != "Rush" || draft["tags"] != "vip" {
			t.Fatalf("draft = %v", draft)
		}
		batch6Lane3WriteJSON(w, http.StatusCreated, map[string]any{
			"draft_order": map[string]any{
				"id": 994118539, "name": "#D1", "status": "open",
				"total_price": "199.00", "subtotal_price": "199.00", "total_tax": "0.00",
				"currency": "USD", "invoice_url": "https://shopify.com/invoice",
				"created_at": "2025-01-10T11:00:00-05:00",
				"line_items": []any{map[string]any{"title": "Widget"}},
			},
		})
	})
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{
		"action":          "shopify_create_draft_order",
		"line_items_json": `[{"variant_id": 123, "quantity": 2}]`,
		"customer_id":     "207119551",
		"note":            "Rush",
		"tags":            "vip",
	})
	if result["result"] != "created" || result["line_item_count"] != 1 || result["total_price"] != "199.00" {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Path != "/draft_orders.json" {
		t.Fatalf("path = %q", recorder.last(t).Path)
	}

	for _, invalid := range []string{"not json", `{"variant_id": 1}`, `[]`} {
		result = batch6Lane3Execute(t, registry, "shopify", map[string]any{
			"action": "shopify_create_draft_order", "line_items_json": invalid,
		})
		if _, failed := result["error"]; !failed {
			t.Fatalf("line_items_json %q accepted: %v", invalid, result)
		}
	}
}

// Parity-exempt: upstream tests mock httpx and never exercise status mapping;
// the messages are ported from shopify_tool._handle_response.
func TestBatch6Lane3ShopifyHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid Shopify access token"},
		{http.StatusPaymentRequired, "frozen or payment required"},
		{http.StatusForbidden, "Insufficient API scopes"},
		{http.StatusNotFound, "resource not found"},
		{http.StatusTooManyRequests, "rate limit"},
		{http.StatusInternalServerError, "Shopify API error (HTTP 500)"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch6Lane3TestRegistry(t, "shopify_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch6Lane3WriteJSON(w, tc.status, map[string]any{"errors": "failure"})
			})
			result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_list_products"})
			if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

// Parity-exempt: upstream tests mock httpx instead of failing the transport.
func TestBatch6Lane3ShopifyTimeout(t *testing.T) {
	registry := newBatch6Lane3Registry(
		WithCredential("shopify_tool", "test-token|test-store"),
		WithGetenv(func(string) string { return "" }),
		WithHTTPClient(&http.Client{Transport: batch6Lane3TimeoutTransport{}}),
	)
	result := batch6Lane3Execute(t, registry, "shopify", map[string]any{"action": "shopify_list_orders"})
	if got := batch6Lane3ErrorText(t, result); !strings.Contains(got, "timed out") {
		t.Fatalf("error = %q", got)
	}
}
