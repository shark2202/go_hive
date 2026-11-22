// SAP S/4HANA Cloud OData V2, read-only (upstream sap_tool, 5 actions).
//
// Preserves the upstream API_PURCHASEORDER_PROCESS_SRV / API_BUSINESS_PARTNER /
// API_PRODUCT_SRV / API_SALES_ORDER_SRV endpoints, OData query parameters and
// projection of OData "d" envelopes.
//
// Limit: the upstream credential-store adapter (sap_base_url/sap_username/
// sap_password aliases) is not reproduced; base URL and basic-auth credentials
// come from SAP_BASE_URL/SAP_USERNAME/SAP_PASSWORD (or WithBaseURL + env).
package data

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var sapTool = &Tool{
	ID:          "sap_tool",
	Name:        "sap",
	DisplayName: "SAP S/4HANA",
	Summary:     "SAP S/4HANA Cloud read-only OData integration (5 actions).",
	Actions: []Action{
		{
			Name:    "sap_list_purchase_orders",
			Summary: "List SAP S/4HANA purchase orders.",
			Source:  "sap_tool/sap_tool.py",
			Params: map[string]any{
				"top":         paramDefault("integer", 50),
				"skip":        paramDefault("integer", 0),
				"filter_expr": paramDefault("string", ""),
			},
		},
		{
			Name:     "sap_get_purchase_order",
			Summary:  "Get details of a specific SAP purchase order.",
			Source:   "sap_tool/sap_tool.py",
			Required: []string{"purchase_order"},
			Params: map[string]any{
				"purchase_order": param("string"),
			},
		},
		{
			Name:    "sap_list_business_partners",
			Summary: "List SAP S/4HANA business partners.",
			Source:  "sap_tool/sap_tool.py",
			Params: map[string]any{
				"top":         paramDefault("integer", 50),
				"skip":        paramDefault("integer", 0),
				"filter_expr": paramDefault("string", ""),
			},
		},
		{
			Name:    "sap_list_products",
			Summary: "List SAP S/4HANA products/materials.",
			Source:  "sap_tool/sap_tool.py",
			Params: map[string]any{
				"top":         paramDefault("integer", 50),
				"skip":        paramDefault("integer", 0),
				"filter_expr": paramDefault("string", ""),
			},
		},
		{
			Name:    "sap_list_sales_orders",
			Summary: "List SAP S/4HANA sales orders.",
			Source:  "sap_tool/sap_tool.py",
			Params: map[string]any{
				"top":         paramDefault("integer", 50),
				"skip":        paramDefault("integer", 0),
				"filter_expr": paramDefault("string", ""),
			},
		},
	},
}

func sapImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"sap_list_purchase_orders":   sapListPurchaseOrders,
		"sap_get_purchase_order":     sapGetPurchaseOrder,
		"sap_list_business_partners": sapListBusinessPartners,
		"sap_list_products":          sapListProducts,
		"sap_list_sales_orders":      sapListSalesOrders,
	}
}

// sapConfig mirrors upstream _get_config: base URL plus basic auth.
func sapConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	base := strings.TrimRight(moduleBaseURL(rt, "sap_tool"), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(runtimeEnv(rt, "SAP_BASE_URL")), "/")
	}
	username := runtimeEnv(rt, "SAP_USERNAME")
	password := runtimeEnv(rt, "SAP_PASSWORD")
	if base == "" || username == "" || password == "" {
		return "", nil, map[string]any{
			"error": "SAP credentials not configured",
			"help":  "Set SAP_BASE_URL, SAP_USERNAME, and SAP_PASSWORD environment variables or configure via credential store",
		}
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return base, map[string]string{
		"Authorization": "Basic " + encoded,
		"Accept":        "application/json",
	}, nil
}

// sapGet performs one OData GET.
func sapGet(ctx context.Context, rt *Runtime, base string, headers map[string]string, path string, params url.Values) map[string]any {
	target := base + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	resp, err := doJSON(ctx, rt, http.MethodGet, target, headers, nil, 30*time.Second)
	if err != nil {
		return transportError(ctx, "SAP", err)
	}
	if resp.Status >= 400 {
		return httpError(resp)
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "SAP API error: invalid JSON response"}
	}
	return data
}

// sapODataList mirrors upstream _odata_list.
func sapODataList(data map[string]any) ([]any, any) {
	envelope := asMap(data["d"])
	results := asSlice(envelope["results"])
	if results == nil {
		results = []any{}
	}
	var total any
	if rawCount, present := envelope["__count"]; present {
		if parsed, err := strconv.Atoi(strings.TrimSpace(asString(rawCount))); err == nil {
			total = parsed
		}
	}
	return results, total
}

func sapListParams(input map[string]any) url.Values {
	params := url.Values{
		"$top":         []string{strconv.Itoa(intArg(input, "top", 50))},
		"$skip":        []string{strconv.Itoa(intArg(input, "skip", 0))},
		"$inlinecount": []string{"allpages"},
		"$format":      []string{"json"},
	}
	if filter := stringArg(input, "filter_expr"); filter != "" {
		params.Set("$filter", filter)
	}
	return params
}

func sapListPurchaseOrders(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := sapConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data := sapGet(ctx, rt, base, headers,
		"/sap/opu/odata/sap/API_PURCHASEORDER_PROCESS_SRV/A_PurchaseOrder", sapListParams(input))
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results, total := sapODataList(data)
	purchaseOrders := make([]any, 0, len(results))
	for _, rawOrder := range results {
		order := asMap(rawOrder)
		purchaseOrders = append(purchaseOrders, map[string]any{
			"purchase_order": stringOr(order, "PurchaseOrder", ""),
			"type":           stringOr(order, "PurchaseOrderType", ""),
			"company_code":   stringOr(order, "CompanyCode", ""),
			"supplier":       stringOr(order, "Supplier", ""),
			"creation_date":  order["CreationDate"],
			"net_amount":     order["PurchaseOrderNetAmount"],
			"currency":       stringOr(order, "DocumentCurrency", ""),
		})
	}
	return map[string]any{"count": len(purchaseOrders), "total": total, "purchase_orders": purchaseOrders}, nil
}

func sapGetPurchaseOrder(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := sapConfig(rt)
	if failure != nil {
		return failure, nil
	}
	purchaseOrder := stringArg(input, "purchase_order")
	if purchaseOrder == "" {
		return map[string]any{"error": "purchase_order is required"}, nil
	}
	data := sapGet(ctx, rt, base, headers,
		"/sap/opu/odata/sap/API_PURCHASEORDER_PROCESS_SRV/A_PurchaseOrder('"+purchaseOrder+"')",
		url.Values{"$format": []string{"json"}})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	order := asMap(data["d"])
	return map[string]any{
		"purchase_order": stringOr(order, "PurchaseOrder", ""),
		"type":           stringOr(order, "PurchaseOrderType", ""),
		"company_code":   stringOr(order, "CompanyCode", ""),
		"supplier":       stringOr(order, "Supplier", ""),
		"purchasing_org": stringOr(order, "PurchasingOrganization", ""),
		"creation_date":  order["CreationDate"],
		"net_amount":     order["PurchaseOrderNetAmount"],
		"currency":       stringOr(order, "DocumentCurrency", ""),
	}, nil
}

func sapListBusinessPartners(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := sapConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data := sapGet(ctx, rt, base, headers,
		"/sap/opu/odata/sap/API_BUSINESS_PARTNER/A_BusinessPartner", sapListParams(input))
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results, total := sapODataList(data)
	partners := make([]any, 0, len(results))
	for _, rawPartner := range results {
		partner := asMap(rawPartner)
		name := stringOr(partner, "BusinessPartnerFullName", "")
		if name == "" {
			name = stringOr(partner, "BusinessPartnerName", "")
		}
		partners = append(partners, map[string]any{
			"business_partner": stringOr(partner, "BusinessPartner", ""),
			"category":         stringOr(partner, "BusinessPartnerCategory", ""),
			"name":             name,
			"is_customer":      stringOr(partner, "Customer", "") != "",
			"is_supplier":      stringOr(partner, "Supplier", "") != "",
			"creation_date":    partner["CreationDate"],
		})
	}
	return map[string]any{"count": len(partners), "total": total, "business_partners": partners}, nil
}

func sapListProducts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := sapConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data := sapGet(ctx, rt, base, headers,
		"/sap/opu/odata/sap/API_PRODUCT_SRV/A_Product", sapListParams(input))
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results, total := sapODataList(data)
	products := make([]any, 0, len(results))
	for _, rawProduct := range results {
		product := asMap(rawProduct)
		products = append(products, map[string]any{
			"product":       stringOr(product, "Product", ""),
			"product_type":  stringOr(product, "ProductType", ""),
			"base_unit":     stringOr(product, "BaseUnit", ""),
			"product_group": stringOr(product, "ProductGroup", ""),
			"creation_date": product["CreationDate"],
		})
	}
	return map[string]any{"count": len(products), "total": total, "products": products}, nil
}

func sapListSalesOrders(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := sapConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data := sapGet(ctx, rt, base, headers,
		"/sap/opu/odata/sap/API_SALES_ORDER_SRV/A_SalesOrder", sapListParams(input))
	if _, failed := data["error"]; failed {
		return data, nil
	}
	results, total := sapODataList(data)
	salesOrders := make([]any, 0, len(results))
	for _, rawOrder := range results {
		order := asMap(rawOrder)
		salesOrders = append(salesOrders, map[string]any{
			"sales_order":        stringOr(order, "SalesOrder", ""),
			"sales_order_type":   stringOr(order, "SalesOrderType", ""),
			"sales_organization": stringOr(order, "SalesOrganization", ""),
			"sold_to_party":      stringOr(order, "SoldToParty", ""),
			"creation_date":      order["CreationDate"],
			"net_amount":         order["TotalNetAmount"],
			"currency":           stringOr(order, "TransactionCurrency", ""),
		})
	}
	return map[string]any{"count": len(salesOrders), "total": total, "sales_orders": salesOrders}, nil
}
