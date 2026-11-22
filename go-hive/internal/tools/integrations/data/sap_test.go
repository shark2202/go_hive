package data

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var batch6Lane4SAPEnv = map[string]string{
	"SAP_BASE_URL": "https://my-tenant-api.s4hana.ondemand.com",
	"SAP_USERNAME": "COMM_USER",
	"SAP_PASSWORD": "test-password",
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPListPurchaseOrders::test_missing_credentials
func TestBatch6Lane4SAPMissingCredentials(t *testing.T) {
	registry := NewPlatformRegistry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "sap", map[string]any{"action": "sap_list_purchase_orders"})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "SAP credentials not configured") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPListPurchaseOrders::test_successful_list
func TestBatch6Lane4SAPListPurchaseOrders(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "sap_tool", batch6Lane4SAPEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/sap/opu/odata/sap/API_PURCHASEORDER_PROCESS_SRV/A_PurchaseOrder" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Basic ") {
				t.Fatalf("Authorization = %q", got)
			} else if decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Basic ")); err != nil || string(decoded) != "COMM_USER:test-password" {
				t.Fatalf("basic auth = %q (%v)", decoded, err)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"d": map[string]any{
				"__count": "1",
				"results": []any{map[string]any{
					"PurchaseOrder":          "4500000001",
					"PurchaseOrderType":      "NB",
					"CompanyCode":            "1010",
					"Supplier":               "17300001",
					"CreationDate":           "/Date(1672531200000)/",
					"PurchaseOrderNetAmount": "15000.00",
					"DocumentCurrency":       "USD",
				}},
			}})
		})
	result := batch4Execute(t, registry, "sap", map[string]any{
		"action": "sap_list_purchase_orders", "top": 10, "filter_expr": "CompanyCode eq '1010'",
	})
	if result["count"] != 1 || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	order := batch4BodyMap(t, batch4BodySlice(t, result["purchase_orders"])[0])
	if order["purchase_order"] != "4500000001" || order["net_amount"] != "15000.00" || order["currency"] != "USD" {
		t.Fatalf("order = %v", order)
	}
	query := recorder.last(t).Query
	if query.Get("$top") != "10" || query.Get("$skip") != "0" ||
		query.Get("$inlinecount") != "allpages" || query.Get("$format") != "json" ||
		query.Get("$filter") != "CompanyCode eq '1010'" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPGetPurchaseOrder::test_missing_id
func TestBatch6Lane4SAPGetPurchaseOrderMissingID(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "sap_tool", batch6Lane4SAPEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "sap", map[string]any{"action": "sap_get_purchase_order", "purchase_order": ""})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPGetPurchaseOrder::test_successful_get
func TestBatch6Lane4SAPGetPurchaseOrder(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "sap_tool", batch6Lane4SAPEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/sap/opu/odata/sap/API_PURCHASEORDER_PROCESS_SRV/A_PurchaseOrder('4500000001')" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"d": map[string]any{
				"PurchaseOrder":          "4500000001",
				"PurchaseOrderType":      "NB",
				"CompanyCode":            "1010",
				"Supplier":               "17300001",
				"PurchasingOrganization": "1010",
				"CreationDate":           "/Date(1672531200000)/",
				"PurchaseOrderNetAmount": "15000.00",
				"DocumentCurrency":       "USD",
			}})
		})
	result := batch4Execute(t, registry, "sap", map[string]any{
		"action": "sap_get_purchase_order", "purchase_order": "4500000001",
	})
	if result["purchase_order"] != "4500000001" || result["purchasing_org"] != "1010" {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Query.Get("$format"); got != "json" {
		t.Fatalf("$format = %q", got)
	}
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPListBusinessPartners::test_successful_list
func TestBatch6Lane4SAPListBusinessPartners(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "sap_tool", batch6Lane4SAPEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/sap/opu/odata/sap/API_BUSINESS_PARTNER/A_BusinessPartner" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"d": map[string]any{
				"__count": "1",
				"results": []any{map[string]any{
					"BusinessPartner":         "1000000",
					"BusinessPartnerCategory": "1",
					"BusinessPartnerFullName": "Acme Corp",
					"Customer":                "CUST001",
					"Supplier":                "",
					"CreationDate":            "/Date(1672531200000)/",
				}},
			}})
		})
	result := batch4Execute(t, registry, "sap", map[string]any{"action": "sap_list_business_partners"})
	if result["count"] != 1 || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	partner := batch4BodyMap(t, batch4BodySlice(t, result["business_partners"])[0])
	if partner["name"] != "Acme Corp" || partner["is_customer"] != true || partner["is_supplier"] != false {
		t.Fatalf("partner = %v", partner)
	}
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPListProducts::test_successful_list
func TestBatch6Lane4SAPListProducts(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "sap_tool", batch6Lane4SAPEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/sap/opu/odata/sap/API_PRODUCT_SRV/A_Product" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"d": map[string]any{
				"__count": "1",
				"results": []any{map[string]any{
					"Product":      "FG001",
					"ProductType":  "FERT",
					"BaseUnit":     "EA",
					"ProductGroup": "001",
					"CreationDate": "/Date(1672531200000)/",
				}},
			}})
		})
	result := batch4Execute(t, registry, "sap", map[string]any{"action": "sap_list_products"})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	product := batch4BodyMap(t, batch4BodySlice(t, result["products"])[0])
	if product["product"] != "FG001" || product["product_type"] != "FERT" {
		t.Fatalf("product = %v", product)
	}
}

// Parity: tools/tests/tools/test_sap_tool.py::TestSAPListSalesOrders::test_successful_list
func TestBatch6Lane4SAPListSalesOrders(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "sap_tool", batch6Lane4SAPEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/sap/opu/odata/sap/API_SALES_ORDER_SRV/A_SalesOrder" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"d": map[string]any{
				"__count": "1",
				"results": []any{map[string]any{
					"SalesOrder":          "1",
					"SalesOrderType":      "OR",
					"SalesOrganization":   "1010",
					"SoldToParty":         "CUST001",
					"CreationDate":        "/Date(1672531200000)/",
					"TotalNetAmount":      "25000.00",
					"TransactionCurrency": "USD",
				}},
			}})
		})
	result := batch4Execute(t, registry, "sap", map[string]any{"action": "sap_list_sales_orders"})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	order := batch4BodyMap(t, batch4BodySlice(t, result["sales_orders"])[0])
	if order["sales_order"] != "1" || order["net_amount"] != "25000.00" {
		t.Fatalf("order = %v", order)
	}
}

// Parity: tools/tests/tools/test_sap_tool.py::TestCredentialStoreAdapter::test_env_fallback_when_no_adapter
func TestBatch6Lane4SAPBaseURLFromEnvironment(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		batch4WriteJSON(w, http.StatusOK, map[string]any{"d": map[string]any{"__count": "0", "results": []any{}}})
	}))
	t.Cleanup(server.Close)
	env := map[string]string{
		"SAP_BASE_URL": server.URL,
		"SAP_USERNAME": "COMM_USER",
		"SAP_PASSWORD": "test-password",
	}
	registry := NewPlatformRegistry(
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	)
	result := batch4Execute(t, registry, "sap", map[string]any{"action": "sap_list_purchase_orders"})
	if result["count"] != 0 {
		t.Fatalf("result = %v", result)
	}
	if seenPath != "/sap/opu/odata/sap/API_PURCHASEORDER_PROCESS_SRV/A_PurchaseOrder" {
		t.Fatalf("path = %q", seenPath)
	}
}
