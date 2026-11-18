// QuickBooks Online Accounting API (upstream quickbooks_tool, 8 actions).
// Part of batch 6 lane 3; see batch6_lane3_registry.go for the registry and
// shared helpers.
//
// Upstream authenticates with an OAuth bearer token plus a realm id embedded in
// the base path, and switches to the sandbox host when QUICKBOOKS_SANDBOX is
// truthy. Here the module credential may carry "<token>|<realm>"; a bare token
// falls back to QUICKBOOKS_REALM_ID. WithBaseURL replaces the host prefix while
// the realm segment and paths stay upstream-shaped.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	batch6Lane3QuickBooksProdURL      = "https://quickbooks.api.intuit.com/v3/company"
	batch6Lane3QuickBooksSandboxURL   = "https://sandbox-quickbooks.api.intuit.com/v3/company"
	batch6Lane3QuickBooksMinorVersion = "73"
)

var batch6Lane3QuickBooksTool = &Tool{
	ID:          "quickbooks_tool",
	Name:        "quickbooks",
	DisplayName: "QuickBooks",
	Summary:     "QuickBooks Online integration (8 actions).",
	Actions: []Action{
		{
			Name:     "quickbooks_query",
			Summary:  "Query QuickBooks entities using the query API.",
			Source:   "quickbooks_tool/quickbooks_tool.py",
			Required: []string{"entity"},
			Params: map[string]any{
				"entity":         param("string"),
				"where":          paramDefault("string", ""),
				"order_by":       paramDefault("string", ""),
				"max_results":    paramDefault("integer", 100),
				"start_position": paramDefault("integer", 1),
			},
		},
		{
			Name:     "quickbooks_get_entity",
			Summary:  "Get a specific QuickBooks entity by ID.",
			Source:   "quickbooks_tool/quickbooks_tool.py",
			Required: []string{"entity", "entity_id"},
			Params: map[string]any{
				"entity":    param("string"),
				"entity_id": param("string"),
			},
		},
		{
			Name:     "quickbooks_create_customer",
			Summary:  "Create a new customer in QuickBooks.",
			Source:   "quickbooks_tool/quickbooks_tool.py",
			Required: []string{"display_name"},
			Params: map[string]any{
				"display_name": param("string"),
				"email":        paramDefault("string", ""),
				"phone":        paramDefault("string", ""),
			},
		},
		{
			Name:     "quickbooks_create_invoice",
			Summary:  "Create an invoice in QuickBooks.",
			Source:   "quickbooks_tool/quickbooks_tool.py",
			Required: []string{"customer_id", "line_items"},
			Params: map[string]any{
				"customer_id": param("string"),
				"line_items":  param("string"),
			},
		},
		{
			Name:    "quickbooks_get_company_info",
			Summary: "Get QuickBooks company information.",
			Source:  "quickbooks_tool/quickbooks_tool.py",
		},
		{
			Name:    "quickbooks_list_invoices",
			Summary: "List invoices from QuickBooks with optional filters.",
			Source:  "quickbooks_tool/quickbooks_tool.py",
			Params: map[string]any{
				"status":      paramDefault("string", ""),
				"customer_id": paramDefault("string", ""),
				"max_results": paramDefault("integer", 100),
			},
		},
		{
			Name:     "quickbooks_get_customer",
			Summary:  "Get detailed customer information from QuickBooks.",
			Source:   "quickbooks_tool/quickbooks_tool.py",
			Required: []string{"customer_id"},
			Params: map[string]any{
				"customer_id": param("string"),
			},
		},
		{
			Name:     "quickbooks_create_payment",
			Summary:  "Record a payment in QuickBooks.",
			Source:   "quickbooks_tool/quickbooks_tool.py",
			Required: []string{"customer_id", "total_amt"},
			Params: map[string]any{
				"customer_id": param("string"),
				"total_amt":   param("number"),
				"invoice_id":  paramDefault("string", ""),
			},
		},
	},
}

func batch6Lane3QuickBooksImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"quickbooks_query":            batch6Lane3QuickBooksQuery,
		"quickbooks_get_entity":       batch6Lane3QuickBooksGetEntity,
		"quickbooks_create_customer":  batch6Lane3QuickBooksCreateCustomer,
		"quickbooks_create_invoice":   batch6Lane3QuickBooksCreateInvoice,
		"quickbooks_get_company_info": batch6Lane3QuickBooksGetCompanyInfo,
		"quickbooks_list_invoices":    batch6Lane3QuickBooksListInvoices,
		"quickbooks_get_customer":     batch6Lane3QuickBooksGetCustomer,
		"quickbooks_create_payment":   batch6Lane3QuickBooksCreatePayment,
	}
}

func batch6Lane3QuickBooksAuthError() map[string]any {
	return map[string]any{
		"error": "QUICKBOOKS_ACCESS_TOKEN and QUICKBOOKS_REALM_ID are required",
		"help":  "Set QUICKBOOKS_ACCESS_TOKEN and QUICKBOOKS_REALM_ID environment variables",
	}
}

// batch6Lane3QuickBooksConfig resolves the company base URL, bearer token and
// realm id. The base always ends at the realm segment, like upstream.
func batch6Lane3QuickBooksConfig(rt *Runtime) (string, string, string, map[string]any) {
	token, realm, paired := batch6Lane3SplitCredential(batch6Lane3ModuleToken(rt, "quickbooks_tool"))
	if !paired {
		realm = batch6Lane3Env(rt, "QUICKBOOKS_REALM_ID")
	}
	if token == "" || realm == "" {
		return "", "", "", batch6Lane3QuickBooksAuthError()
	}
	base := batch6Lane3ModuleURL(rt, "quickbooks_tool")
	if base == "" {
		switch strings.ToLower(batch6Lane3Env(rt, "QUICKBOOKS_SANDBOX")) {
		case "1", "true":
			base = batch6Lane3QuickBooksSandboxURL
		default:
			base = batch6Lane3QuickBooksProdURL
		}
	}
	return strings.TrimRight(base, "/") + "/" + realm, token, realm, nil
}

// batch6Lane3QuickBooksRequest performs one QuickBooks call. HTTP failures keep
// the upstream "HTTP <status>: <body[:500]>" format; transport failures become
// error dicts instead of propagating.
func batch6Lane3QuickBooksRequest(ctx context.Context, rt *Runtime, method, path string, query url.Values, payload any) map[string]any {
	base, token, _, failure := batch6Lane3QuickBooksConfig(rt)
	if failure != nil {
		return failure
	}
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return map[string]any{"error": "QuickBooks request failed: " + err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return map[string]any{"error": "QuickBooks request failed: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
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
	if resp.StatusCode >= 400 {
		return map[string]any{
			"error": fmt.Sprintf("HTTP %d: %s", resp.StatusCode, batch6Lane3Truncate(string(raw), batch6Lane3MaxError)),
		}
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"error": "QuickBooks API error: invalid JSON response"}
	}
	if object == nil {
		object = map[string]any{}
	}
	return object
}

// batch6Lane3QuickBooksQueryParams builds the shared {query, minorversion}
// parameter set.
func batch6Lane3QuickBooksQueryParams(query string) url.Values {
	return url.Values{"query": {query}, "minorversion": {batch6Lane3QuickBooksMinorVersion}}
}

func batch6Lane3QuickBooksQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	entity := stringArg(input, "entity")
	if entity == "" {
		return map[string]any{"error": "entity is required"}, nil
	}
	query := "SELECT * FROM " + entity
	if where := stringArg(input, "where"); where != "" {
		query += " WHERE " + where
	}
	if orderBy := stringArg(input, "order_by"); orderBy != "" {
		query += " ORDERBY " + orderBy
	}
	query += fmt.Sprintf(" STARTPOSITION %d MAXRESULTS %d", intArg(input, "start_position", 1), min(intArg(input, "max_results", 100), 1000))

	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodGet, "/query", batch6Lane3QuickBooksQueryParams(query), nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	response := batch6Lane3AsMap(data["QueryResponse"])
	entities := batch6Lane3AsSlice(response[entity])
	if entities == nil {
		entities = []any{}
	}
	return map[string]any{
		"count":       len(entities),
		"total_count": response["totalCount"],
		"entities":    entities,
	}, nil
}

func batch6Lane3QuickBooksGetEntity(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	entity := stringArg(input, "entity")
	entityID := stringArg(input, "entity_id")
	if entity == "" || entityID == "" {
		return map[string]any{"error": "entity and entity_id are required"}, nil
	}
	params := url.Values{"minorversion": {batch6Lane3QuickBooksMinorVersion}}
	path := "/" + strings.ToLower(entity) + "/" + url.PathEscape(entityID)
	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodGet, path, params, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	return batch6Lane3ValueOr(data, entity, data), nil
}

func batch6Lane3QuickBooksCreateCustomer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	displayName := stringArg(input, "display_name")
	if displayName == "" {
		return map[string]any{"error": "display_name is required"}, nil
	}
	body := map[string]any{"DisplayName": displayName}
	if email := stringArg(input, "email"); email != "" {
		body["PrimaryEmailAddr"] = map[string]any{"Address": email}
	}
	if phone := stringArg(input, "phone"); phone != "" {
		body["PrimaryPhone"] = map[string]any{"FreeFormNumber": phone}
	}

	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodPost, "/customer", nil, body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	customer := batch6Lane3AsMap(data["Customer"])
	return map[string]any{
		"result":       "created",
		"id":           customer["Id"],
		"display_name": customer["DisplayName"],
		"sync_token":   customer["SyncToken"],
	}, nil
}

func batch6Lane3QuickBooksCreateInvoice(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	customerID := stringArg(input, "customer_id")
	lineItemsJSON := stringArg(input, "line_items")
	if customerID == "" || lineItemsJSON == "" {
		return map[string]any{"error": "customer_id and line_items are required"}, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(lineItemsJSON), &decoded); err != nil {
		return map[string]any{"error": "line_items must be valid JSON"}, nil
	}
	items, isArray := decoded.([]any)
	if !isArray || len(items) == 0 {
		return map[string]any{"error": "line_items must be a non-empty JSON array"}, nil
	}

	lines := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item := batch6Lane3AsMap(raw)
		detail := map[string]any{}
		line := map[string]any{
			"Amount":              batch6Lane3ValueOr(item, "amount", 0),
			"DetailType":          "SalesItemLineDetail",
			"Description":         batch6Lane3ValueOr(item, "description", ""),
			"SalesItemLineDetail": detail,
		}
		if itemID, present := item["item_id"]; present {
			detail["ItemRef"] = map[string]any{"value": itemID}
		}
		quantity, hasQuantity := item["quantity"]
		unitPrice, hasUnitPrice := item["unit_price"]
		if hasQuantity && hasUnitPrice {
			detail["Qty"] = quantity
			detail["UnitPrice"] = unitPrice
		}
		lines = append(lines, line)
	}

	body := map[string]any{
		"CustomerRef": map[string]any{"value": customerID},
		"Line":        lines,
	}
	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodPost, "/invoice", nil, body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	invoice := batch6Lane3AsMap(data["Invoice"])
	return map[string]any{
		"result":     "created",
		"id":         invoice["Id"],
		"doc_number": invoice["DocNumber"],
		"total_amt":  invoice["TotalAmt"],
		"balance":    invoice["Balance"],
		"sync_token": invoice["SyncToken"],
	}, nil
}

func batch6Lane3QuickBooksGetCompanyInfo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	_, _, realm, failure := batch6Lane3QuickBooksConfig(rt)
	if failure != nil {
		return failure, nil
	}
	params := url.Values{"minorversion": {batch6Lane3QuickBooksMinorVersion}}
	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodGet, "/companyinfo/"+url.PathEscape(realm), params, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	info := batch6Lane3AsMap(data["CompanyInfo"])
	var email any
	if address, ok := info["Email"].(map[string]any); ok {
		email = address["Address"]
	}
	return map[string]any{
		"company_name":      info["CompanyName"],
		"legal_name":        info["LegalName"],
		"country":           info["Country"],
		"email":             email,
		"fiscal_year_start": info["FiscalYearStartMonth"],
	}, nil
}

func batch6Lane3QuickBooksListInvoices(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	whereParts := make([]string, 0, 2)
	switch status := stringArg(input, "status"); status {
	case "Unpaid":
		whereParts = append(whereParts, "Balance > '0'")
	case "Paid":
		whereParts = append(whereParts, "Balance = '0'")
	case "Overdue":
		today := batch6Lane3Now(rt).Format("2006-01-02")
		whereParts = append(whereParts, fmt.Sprintf("DueDate < '%s' AND Balance > '0'", today))
	}
	if customerID := stringArg(input, "customer_id"); customerID != "" {
		whereParts = append(whereParts, fmt.Sprintf("CustomerRef = '%s'", customerID))
	}

	query := "SELECT * FROM Invoice"
	if len(whereParts) > 0 {
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}
	query += fmt.Sprintf(" MAXRESULTS %d", min(intArg(input, "max_results", 100), 1000))

	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodGet, "/query", batch6Lane3QuickBooksQueryParams(query), nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	invoices := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(batch6Lane3AsMap(data["QueryResponse"])["Invoice"]) {
		invoice := batch6Lane3AsMap(raw)
		customer := batch6Lane3AsMap(invoice["CustomerRef"])
		invoices = append(invoices, map[string]any{
			"id":            invoice["Id"],
			"doc_number":    invoice["DocNumber"],
			"customer_name": batch6Lane3ValueOr(customer, "name", ""),
			"customer_id":   batch6Lane3ValueOr(customer, "value", ""),
			"total_amt":     invoice["TotalAmt"],
			"balance":       invoice["Balance"],
			"due_date":      invoice["DueDate"],
			"txn_date":      invoice["TxnDate"],
			"email_status":  invoice["EmailStatus"],
		})
	}
	return map[string]any{"count": len(invoices), "invoices": invoices}, nil
}

func batch6Lane3QuickBooksGetCustomer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	customerID := stringArg(input, "customer_id")
	if customerID == "" {
		return map[string]any{"error": "customer_id is required"}, nil
	}
	params := url.Values{"minorversion": {batch6Lane3QuickBooksMinorVersion}}
	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodGet, "/customer/"+url.PathEscape(customerID), params, nil)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	customer := batch6Lane3AsMap(data["Customer"])
	var email, phone any
	if address, ok := customer["PrimaryEmailAddr"].(map[string]any); ok {
		email = address["Address"]
	}
	if number, ok := customer["PrimaryPhone"].(map[string]any); ok {
		phone = number["FreeFormNumber"]
	}
	address := batch6Lane3AsMap(customer["BillAddr"])
	return map[string]any{
		"id":           customer["Id"],
		"display_name": customer["DisplayName"],
		"company_name": customer["CompanyName"],
		"given_name":   customer["GivenName"],
		"family_name":  customer["FamilyName"],
		"email":        email,
		"phone":        phone,
		"balance":      customer["Balance"],
		"active":       customer["Active"],
		"billing_address": map[string]any{
			"line1":       batch6Lane3ValueOr(address, "Line1", ""),
			"city":        batch6Lane3ValueOr(address, "City", ""),
			"state":       batch6Lane3ValueOr(address, "CountrySubDivisionCode", ""),
			"postal_code": batch6Lane3ValueOr(address, "PostalCode", ""),
			"country":     batch6Lane3ValueOr(address, "Country", ""),
		},
		"sync_token": customer["SyncToken"],
	}, nil
}

func batch6Lane3QuickBooksCreatePayment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	customerID := stringArg(input, "customer_id")
	totalAmt, _ := batch6Lane3OptionalFloat(input, "total_amt")
	if customerID == "" || totalAmt <= 0 {
		return map[string]any{"error": "customer_id and a positive total_amt are required"}, nil
	}
	body := map[string]any{
		"CustomerRef": map[string]any{"value": customerID},
		"TotalAmt":    totalAmt,
	}
	if invoiceID := stringArg(input, "invoice_id"); invoiceID != "" {
		body["Line"] = []any{
			map[string]any{
				"Amount": totalAmt,
				"LinkedTxn": []any{
					map[string]any{"TxnId": invoiceID, "TxnType": "Invoice"},
				},
			},
		}
	}

	data := batch6Lane3QuickBooksRequest(ctx, rt, http.MethodPost, "/payment", nil, body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	payment := batch6Lane3AsMap(data["Payment"])
	return map[string]any{
		"result":      "created",
		"id":          payment["Id"],
		"total_amt":   payment["TotalAmt"],
		"customer_id": batch6Lane3AsMap(payment["CustomerRef"])["value"],
		"txn_date":    payment["TxnDate"],
		"sync_token":  payment["SyncToken"],
	}, nil
}
