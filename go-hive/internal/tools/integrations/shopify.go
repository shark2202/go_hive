// Shopify Admin REST API (upstream shopify_tool, 9 actions). Part of batch 6
// lane 3; see finance_commerce.go for the registry and shared helpers.
//
// The upstream module keeps the store name in a second credential and builds
// https://{store}.myshopify.com/admin/api/2025-01. Here the module credential
// may carry "<token>|<store>"; a bare token falls back to SHOPIFY_STORE_NAME,
// and WithBaseURL replaces the whole store-specific prefix.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const batch6Lane3ShopifyAPIVersion = "2025-01"

var batch6Lane3ShopifyTool = &Tool{
	ID:          "shopify_tool",
	Name:        "shopify",
	DisplayName: "Shopify",
	Summary:     "Shopify Admin integration (9 actions).",
	Actions: []Action{
		{
			Name:    "shopify_list_orders",
			Summary: "List orders from a Shopify store.",
			Source:  "shopify_tool/shopify_tool.py",
			Params: map[string]any{
				"status":             paramDefault("string", "any"),
				"financial_status":   paramDefault("string", ""),
				"fulfillment_status": paramDefault("string", ""),
				"limit":              paramDefault("integer", 50),
			},
		},
		{
			Name:     "shopify_get_order",
			Summary:  "Get a single Shopify order by ID.",
			Source:   "shopify_tool/shopify_tool.py",
			Required: []string{"order_id"},
			Params: map[string]any{
				"order_id": param("string"),
			},
		},
		{
			Name:    "shopify_list_products",
			Summary: "List products from a Shopify store.",
			Source:  "shopify_tool/shopify_tool.py",
			Params: map[string]any{
				"status":       paramDefault("string", ""),
				"product_type": paramDefault("string", ""),
				"vendor":       paramDefault("string", ""),
				"limit":        paramDefault("integer", 50),
			},
		},
		{
			Name:     "shopify_get_product",
			Summary:  "Get a single Shopify product by ID.",
			Source:   "shopify_tool/shopify_tool.py",
			Required: []string{"product_id"},
			Params: map[string]any{
				"product_id": param("string"),
			},
		},
		{
			Name:    "shopify_list_customers",
			Summary: "List customers from a Shopify store.",
			Source:  "shopify_tool/shopify_tool.py",
			Params: map[string]any{
				"limit": paramDefault("integer", 50),
			},
		},
		{
			Name:     "shopify_search_customers",
			Summary:  "Search Shopify customers by email, name, or other fields.",
			Source:   "shopify_tool/shopify_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query": param("string"),
				"limit": paramDefault("integer", 50),
			},
		},
		{
			Name:     "shopify_update_product",
			Summary:  "Update an existing Shopify product.",
			Source:   "shopify_tool/shopify_tool.py",
			Required: []string{"product_id"},
			Params: map[string]any{
				"product_id":   param("string"),
				"title":        paramDefault("string", ""),
				"body_html":    paramDefault("string", ""),
				"vendor":       paramDefault("string", ""),
				"product_type": paramDefault("string", ""),
				"tags":         paramDefault("string", ""),
				"status":       paramDefault("string", ""),
			},
		},
		{
			Name:     "shopify_get_customer",
			Summary:  "Get a single Shopify customer by ID.",
			Source:   "shopify_tool/shopify_tool.py",
			Required: []string{"customer_id"},
			Params: map[string]any{
				"customer_id": param("string"),
			},
		},
		{
			Name:     "shopify_create_draft_order",
			Summary:  "Create a draft order in Shopify.",
			Source:   "shopify_tool/shopify_tool.py",
			Required: []string{"line_items_json"},
			Params: map[string]any{
				"line_items_json": param("string"),
				"customer_id":     paramDefault("string", ""),
				"note":            paramDefault("string", ""),
				"tags":            paramDefault("string", ""),
			},
		},
	},
}

func batch6Lane3ShopifyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"shopify_list_orders":        batch6Lane3ShopifyListOrders,
		"shopify_get_order":          batch6Lane3ShopifyGetOrder,
		"shopify_list_products":      batch6Lane3ShopifyListProducts,
		"shopify_get_product":        batch6Lane3ShopifyGetProduct,
		"shopify_list_customers":     batch6Lane3ShopifyListCustomers,
		"shopify_search_customers":   batch6Lane3ShopifySearchCustomers,
		"shopify_update_product":     batch6Lane3ShopifyUpdateProduct,
		"shopify_get_customer":       batch6Lane3ShopifyGetCustomer,
		"shopify_create_draft_order": batch6Lane3ShopifyCreateDraftOrder,
	}
}

func batch6Lane3ShopifyAuthError() map[string]any {
	return map[string]any{
		"error": "Shopify credentials not configured",
		"help":  "Set SHOPIFY_ACCESS_TOKEN and SHOPIFY_STORE_NAME environment variables or configure via credential store",
	}
}

// batch6Lane3ShopifyCredentials resolves the access token and store name.
func batch6Lane3ShopifyCredentials(rt *Runtime) (string, string, map[string]any) {
	token, store, paired := batch6Lane3SplitCredential(moduleToken(rt, "shopify_tool"))
	if !paired {
		store = runtimeEnvTrimmed(rt, "SHOPIFY_STORE_NAME")
	}
	if token == "" || store == "" {
		return "", "", batch6Lane3ShopifyAuthError()
	}
	return token, store, nil
}

// batch6Lane3ShopifyBase resolves the store-specific Admin API prefix.
func batch6Lane3ShopifyBase(rt *Runtime, store string) string {
	if base := moduleBaseURL(rt, "shopify_tool"); base != "" {
		return base
	}
	return "https://" + store + ".myshopify.com/admin/api/" + batch6Lane3ShopifyAPIVersion
}

// batch6Lane3ShopifyRequest performs one Admin API call and maps failures to
// the upstream error dicts.
func batch6Lane3ShopifyRequest(ctx context.Context, rt *Runtime, method, path string, query url.Values, payload any) map[string]any {
	token, store, failure := batch6Lane3ShopifyCredentials(rt)
	if failure != nil {
		return failure
	}
	target := batch6Lane3ShopifyBase(rt, store) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return map[string]any{"error": "Shopify request failed: " + err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return map[string]any{"error": "Shopify request failed: " + err.Error()}
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
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
		return map[string]any{"error": "Invalid Shopify access token"}
	case http.StatusPaymentRequired:
		return map[string]any{"error": "Shopify store is frozen or payment required"}
	case http.StatusForbidden:
		return map[string]any{"error": "Insufficient API scopes for this Shopify resource"}
	case http.StatusNotFound:
		return map[string]any{"error": "Shopify resource not found"}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Shopify rate limit exceeded. Try again later."}
	}
	if resp.StatusCode >= 400 {
		return map[string]any{
			"error": fmt.Sprintf("Shopify API error (HTTP %d): %s", resp.StatusCode, batch6Lane3ShopifyDetail(raw)),
		}
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"error": "Shopify API error: invalid JSON response"}
	}
	if object == nil {
		object = map[string]any{}
	}
	return object
}

// batch6Lane3ShopifyDetail mirrors response.json().get("errors", response.text).
func batch6Lane3ShopifyDetail(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if detail, present := payload["errors"]; present {
			return batch6Lane3DetailText(detail)
		}
	}
	return string(raw)
}

func batch6Lane3ShopifyListOrders(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	params := url.Values{}
	params.Set("status", stringArg(input, "status"))
	params.Set("limit", strconv.Itoa(min(intArg(input, "limit", 50), 250)))
	if financial := stringArg(input, "financial_status"); financial != "" {
		params.Set("financial_status", financial)
	}
	if fulfillment := stringArg(input, "fulfillment_status"); fulfillment != "" {
		params.Set("fulfillment_status", fulfillment)
	}

	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/orders.json", params, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	orders := make([]map[string]any, 0)
	for _, raw := range asSlice(data["orders"]) {
		order := asMap(raw)
		orders = append(orders, map[string]any{
			"id":                 order["id"],
			"name":               order["name"],
			"email":              order["email"],
			"created_at":         order["created_at"],
			"financial_status":   order["financial_status"],
			"fulfillment_status": order["fulfillment_status"],
			"total_price":        order["total_price"],
			"currency":           order["currency"],
			"line_item_count":    len(asSlice(order["line_items"])),
		})
	}
	return map[string]any{"count": len(orders), "orders": orders}, nil
}

func batch6Lane3ShopifyGetOrder(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	orderID := stringArg(input, "order_id")
	if orderID == "" {
		return map[string]any{"error": "order_id is required"}, nil
	}
	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/orders/"+url.PathEscape(orderID)+".json", nil, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	order := asMap(data["order"])
	lineItems := make([]map[string]any, 0)
	for _, raw := range asSlice(order["line_items"]) {
		item := asMap(raw)
		lineItems = append(lineItems, map[string]any{
			"title":      item["title"],
			"quantity":   item["quantity"],
			"price":      item["price"],
			"sku":        item["sku"],
			"variant_id": item["variant_id"],
			"product_id": item["product_id"],
		})
	}
	customer := asMap(order["customer"])
	return map[string]any{
		"id":                 order["id"],
		"name":               order["name"],
		"email":              order["email"],
		"created_at":         order["created_at"],
		"updated_at":         order["updated_at"],
		"financial_status":   order["financial_status"],
		"fulfillment_status": order["fulfillment_status"],
		"total_price":        order["total_price"],
		"subtotal_price":     order["subtotal_price"],
		"total_tax":          order["total_tax"],
		"currency":           order["currency"],
		"line_items":         lineItems,
		"shipping_address":   order["shipping_address"],
		"billing_address":    order["billing_address"],
		"customer": map[string]any{
			"id":         customer["id"],
			"email":      customer["email"],
			"first_name": customer["first_name"],
			"last_name":  customer["last_name"],
		},
		"note": order["note"],
		"tags": order["tags"],
	}, nil
}

func batch6Lane3ShopifyListProducts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	params := url.Values{}
	params.Set("limit", strconv.Itoa(min(intArg(input, "limit", 50), 250)))
	if status := stringArg(input, "status"); status != "" {
		params.Set("status", status)
	}
	if productType := stringArg(input, "product_type"); productType != "" {
		params.Set("product_type", productType)
	}
	if vendor := stringArg(input, "vendor"); vendor != "" {
		params.Set("vendor", vendor)
	}

	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/products.json", params, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	products := make([]map[string]any, 0)
	for _, raw := range asSlice(data["products"]) {
		product := asMap(raw)
		products = append(products, map[string]any{
			"id":            product["id"],
			"title":         product["title"],
			"vendor":        product["vendor"],
			"product_type":  product["product_type"],
			"status":        product["status"],
			"handle":        product["handle"],
			"created_at":    product["created_at"],
			"variant_count": len(asSlice(product["variants"])),
			"tags":          product["tags"],
		})
	}
	return map[string]any{"count": len(products), "products": products}, nil
}

func batch6Lane3ShopifyGetProduct(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	productID := stringArg(input, "product_id")
	if productID == "" {
		return map[string]any{"error": "product_id is required"}, nil
	}
	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/products/"+url.PathEscape(productID)+".json", nil, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	product := asMap(data["product"])
	variants := make([]map[string]any, 0)
	for _, raw := range asSlice(product["variants"]) {
		variant := asMap(raw)
		variants = append(variants, map[string]any{
			"id":                 variant["id"],
			"title":              variant["title"],
			"price":              variant["price"],
			"sku":                variant["sku"],
			"inventory_quantity": variant["inventory_quantity"],
			"option1":            variant["option1"],
			"option2":            variant["option2"],
			"option3":            variant["option3"],
		})
	}
	images := make([]map[string]any, 0)
	for _, raw := range asSlice(product["images"]) {
		image := asMap(raw)
		images = append(images, map[string]any{
			"id":       image["id"],
			"src":      image["src"],
			"position": image["position"],
		})
	}
	return map[string]any{
		"id":           product["id"],
		"title":        product["title"],
		"body_html":    product["body_html"],
		"vendor":       product["vendor"],
		"product_type": product["product_type"],
		"handle":       product["handle"],
		"status":       product["status"],
		"created_at":   product["created_at"],
		"updated_at":   product["updated_at"],
		"tags":         product["tags"],
		"variants":     variants,
		"options":      valueOr(product, "options", []any{}),
		"images":       images,
	}, nil
}

func batch6Lane3ShopifyCustomerSummary(customer map[string]any) map[string]any {
	return map[string]any{
		"id":           customer["id"],
		"first_name":   customer["first_name"],
		"last_name":    customer["last_name"],
		"email":        customer["email"],
		"phone":        customer["phone"],
		"orders_count": customer["orders_count"],
		"total_spent":  customer["total_spent"],
		"state":        customer["state"],
		"tags":         customer["tags"],
	}
}

func batch6Lane3ShopifyListCustomers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	params := url.Values{}
	params.Set("limit", strconv.Itoa(min(intArg(input, "limit", 50), 250)))

	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/customers.json", params, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	customers := make([]map[string]any, 0)
	for _, raw := range asSlice(data["customers"]) {
		source := asMap(raw)
		customer := batch6Lane3ShopifyCustomerSummary(source)
		customer["created_at"] = source["created_at"]
		customers = append(customers, customer)
	}
	return map[string]any{"count": len(customers), "customers": customers}, nil
}

func batch6Lane3ShopifySearchCustomers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("limit", strconv.Itoa(min(intArg(input, "limit", 50), 250)))

	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/customers/search.json", params, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	customers := make([]map[string]any, 0)
	for _, raw := range asSlice(data["customers"]) {
		customers = append(customers, batch6Lane3ShopifyCustomerSummary(asMap(raw)))
	}
	return map[string]any{"count": len(customers), "customers": customers}, nil
}

func batch6Lane3ShopifyUpdateProduct(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	productID := stringArg(input, "product_id")
	if productID == "" {
		return map[string]any{"error": "product_id is required"}, nil
	}
	product := map[string]any{}
	for _, field := range []string{"title", "body_html", "vendor", "product_type", "tags", "status"} {
		if value := stringArg(input, field); value != "" {
			product[field] = value
		}
	}
	if len(product) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}

	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodPut, "/products/"+url.PathEscape(productID)+".json", nil, map[string]any{"product": product})
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	updated := asMap(data["product"])
	return map[string]any{
		"id":           updated["id"],
		"title":        updated["title"],
		"vendor":       updated["vendor"],
		"product_type": updated["product_type"],
		"status":       updated["status"],
		"tags":         updated["tags"],
		"updated_at":   updated["updated_at"],
		"result":       "updated",
	}, nil
}

func batch6Lane3ShopifyGetCustomer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	customerID := stringArg(input, "customer_id")
	if customerID == "" {
		return map[string]any{"error": "customer_id is required"}, nil
	}
	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodGet, "/customers/"+url.PathEscape(customerID)+".json", nil, nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	customer := asMap(data["customer"])
	addresses := make([]map[string]any, 0)
	for _, raw := range asSlice(customer["addresses"]) {
		address := asMap(raw)
		addresses = append(addresses, map[string]any{
			"id":       address["id"],
			"address1": address["address1"],
			"city":     address["city"],
			"province": address["province"],
			"country":  address["country"],
			"zip":      address["zip"],
			"default":  valueOr(address, "default", false),
		})
	}
	return map[string]any{
		"id":             customer["id"],
		"first_name":     customer["first_name"],
		"last_name":      customer["last_name"],
		"email":          customer["email"],
		"phone":          customer["phone"],
		"orders_count":   customer["orders_count"],
		"total_spent":    customer["total_spent"],
		"state":          customer["state"],
		"tags":           customer["tags"],
		"note":           customer["note"],
		"verified_email": customer["verified_email"],
		"tax_exempt":     customer["tax_exempt"],
		"created_at":     customer["created_at"],
		"updated_at":     customer["updated_at"],
		"addresses":      addresses,
	}, nil
}

func batch6Lane3ShopifyCreateDraftOrder(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	lineItemsJSON := stringArg(input, "line_items_json")
	if lineItemsJSON == "" {
		return map[string]any{"error": "line_items_json is required"}, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(lineItemsJSON), &decoded); err != nil {
		return map[string]any{"error": "line_items_json must be valid JSON"}, nil
	}
	lineItems, isArray := decoded.([]any)
	if !isArray || len(lineItems) == 0 {
		return map[string]any{"error": "line_items_json must be a non-empty JSON array"}, nil
	}

	draftOrder := map[string]any{"line_items": lineItems}
	if customerID := stringArg(input, "customer_id"); customerID != "" {
		parsed, err := strconv.ParseInt(strings.TrimSpace(customerID), 10, 64)
		if err != nil {
			return map[string]any{"error": "customer_id must be a numeric ID"}, nil
		}
		draftOrder["customer"] = map[string]any{"id": parsed}
	}
	if note := stringArg(input, "note"); note != "" {
		draftOrder["note"] = note
	}
	if tags := stringArg(input, "tags"); tags != "" {
		draftOrder["tags"] = tags
	}

	data := batch6Lane3ShopifyRequest(ctx, rt, http.MethodPost, "/draft_orders.json", nil, map[string]any{"draft_order": draftOrder})
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	draft := asMap(data["draft_order"])
	return map[string]any{
		"id":              draft["id"],
		"name":            draft["name"],
		"status":          draft["status"],
		"total_price":     draft["total_price"],
		"subtotal_price":  draft["subtotal_price"],
		"total_tax":       draft["total_tax"],
		"currency":        draft["currency"],
		"invoice_url":     draft["invoice_url"],
		"created_at":      draft["created_at"],
		"line_item_count": len(asSlice(draft["line_items"])),
		"result":          "created",
	}, nil
}
