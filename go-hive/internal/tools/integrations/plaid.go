// Plaid banking and financial-data aggregation (upstream plaid_tool, 6
// actions). Part of batch 6 lane 3; see finance_commerce.go for the
// registry and shared helpers.
//
// Upstream POSTs JSON with client_id/secret merged into every body and selects
// the host from PLAID_ENV (sandbox by default). Both behaviours are preserved;
// WithBaseURL overrides the host for tests. Transaction amounts are signed
// JSON numbers and are passed through unchanged, with the ISO currency code
// surfaced as "currency".
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	batch6Lane3PlaidSandboxURL     = "https://sandbox.plaid.com"
	batch6Lane3PlaidDevelopmentURL = "https://development.plaid.com"
	batch6Lane3PlaidProductionURL  = "https://production.plaid.com"
)

var batch6Lane3PlaidTool = &Tool{
	ID:          "plaid_tool",
	Name:        "plaid",
	DisplayName: "Plaid",
	Summary:     "Plaid banking integration (6 actions).",
	Actions: []Action{
		{
			Name:     "plaid_get_accounts",
			Summary:  "Get all accounts linked to a Plaid Item.",
			Source:   "plaid_tool/plaid_tool.py",
			Required: []string{"access_token"},
			Params: map[string]any{
				"access_token": param("string"),
			},
		},
		{
			Name:     "plaid_get_balance",
			Summary:  "Get real-time balance for all accounts linked to a Plaid Item.",
			Source:   "plaid_tool/plaid_tool.py",
			Required: []string{"access_token"},
			Params: map[string]any{
				"access_token": param("string"),
			},
		},
		{
			Name:     "plaid_sync_transactions",
			Summary:  "Get incremental transaction updates using cursor-based sync.",
			Source:   "plaid_tool/plaid_tool.py",
			Required: []string{"access_token"},
			Params: map[string]any{
				"access_token": param("string"),
				"cursor":       paramDefault("string", ""),
				"count":        paramDefault("integer", 100),
			},
		},
		{
			Name:     "plaid_get_transactions",
			Summary:  "Get transactions for a date range (non-incremental).",
			Source:   "plaid_tool/plaid_tool.py",
			Required: []string{"access_token", "start_date", "end_date"},
			Params: map[string]any{
				"access_token": param("string"),
				"start_date":   param("string"),
				"end_date":     param("string"),
				"count":        paramDefault("integer", 100),
				"offset":       paramDefault("integer", 0),
			},
		},
		{
			Name:     "plaid_get_institution",
			Summary:  "Get details about a financial institution by ID.",
			Source:   "plaid_tool/plaid_tool.py",
			Required: []string{"institution_id"},
			Params: map[string]any{
				"institution_id": param("string"),
				"country_codes":  param("array"),
			},
		},
		{
			Name:     "plaid_search_institutions",
			Summary:  "Search for financial institutions by name.",
			Source:   "plaid_tool/plaid_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":         param("string"),
				"country_codes": param("array"),
				"products":      param("array"),
				"limit":         paramDefault("integer", 10),
			},
		},
	},
}

func batch6Lane3PlaidImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"plaid_get_accounts":        batch6Lane3PlaidGetAccounts,
		"plaid_get_balance":         batch6Lane3PlaidGetBalance,
		"plaid_sync_transactions":   batch6Lane3PlaidSyncTransactions,
		"plaid_get_transactions":    batch6Lane3PlaidGetTransactions,
		"plaid_get_institution":     batch6Lane3PlaidGetInstitution,
		"plaid_search_institutions": batch6Lane3PlaidSearchInstitutions,
	}
}

func batch6Lane3PlaidAuthError() map[string]any {
	return map[string]any{
		"error": "PLAID_CLIENT_ID and PLAID_SECRET not set",
		"help":  "Get credentials at https://dashboard.plaid.com/developers/keys",
	}
}

// batch6Lane3PlaidCredentials resolves the client id and secret pair.
func batch6Lane3PlaidCredentials(rt *Runtime) (string, string, map[string]any) {
	clientID, secret, paired := batch6Lane3SplitCredential(batch6Lane3ModuleToken(rt, "plaid_tool"))
	if !paired {
		secret = batch6Lane3Env(rt, "PLAID_SECRET")
	}
	if clientID == "" || secret == "" {
		return "", "", batch6Lane3PlaidAuthError()
	}
	return clientID, secret, nil
}

// batch6Lane3PlaidBase resolves the environment host, honouring WithBaseURL.
func batch6Lane3PlaidBase(rt *Runtime) string {
	if base := batch6Lane3ModuleURL(rt, "plaid_tool"); base != "" {
		return base
	}
	switch strings.ToLower(batch6Lane3Env(rt, "PLAID_ENV")) {
	case "development":
		return batch6Lane3PlaidDevelopmentURL
	case "production":
		return batch6Lane3PlaidProductionURL
	default:
		return batch6Lane3PlaidSandboxURL
	}
}

// batch6Lane3PlaidPost performs one Plaid POST with the credentials merged into
// the body and maps failures to the upstream error dicts.
func batch6Lane3PlaidPost(ctx context.Context, rt *Runtime, path string, body map[string]any) map[string]any {
	clientID, secret, failure := batch6Lane3PlaidCredentials(rt)
	if failure != nil {
		return failure
	}
	payload := make(map[string]any, len(body)+2)
	for key, value := range body {
		payload[key] = value
	}
	payload["client_id"] = clientID
	payload["secret"] = secret

	encoded, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": "Plaid request failed: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, batch6Lane3PlaidBase(rt)+path, bytes.NewReader(encoded))
	if err != nil {
		return map[string]any{"error": "Plaid request failed: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := batch6Lane3HTTP(rt).Do(req)
	if err != nil {
		if batch6Lane3IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Plaid timed out"}
		}
		return map[string]any{"error": "Plaid request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := batch6Lane3ReadAll(resp)
	if err != nil {
		return map[string]any{"error": "Plaid request failed: " + err.Error()}
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"error": "Plaid request failed: " + err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		return map[string]any{"error": "Plaid API error: " + batch6Lane3PlaidErrorText(object, resp.StatusCode)}
	}
	if object == nil {
		object = map[string]any{}
	}
	return object
}

// batch6Lane3PlaidErrorText mirrors
// data.get("error_message", data.get("error_code", f"HTTP {status}")).
func batch6Lane3PlaidErrorText(data map[string]any, status int) string {
	if message, ok := data["error_message"].(string); ok {
		return message
	}
	if code, ok := data["error_code"].(string); ok {
		return code
	}
	return fmt.Sprintf("HTTP %d", status)
}

// batch6Lane3PlaidTransaction projects a transaction the way upstream does.
func batch6Lane3PlaidTransaction(transaction map[string]any) map[string]any {
	return map[string]any{
		"transaction_id": batch6Lane3ValueOr(transaction, "transaction_id", ""),
		"account_id":     batch6Lane3ValueOr(transaction, "account_id", ""),
		"amount":         batch6Lane3ValueOr(transaction, "amount", 0),
		"date":           batch6Lane3ValueOr(transaction, "date", ""),
		"name":           batch6Lane3ValueOr(transaction, "name", ""),
		"merchant_name":  batch6Lane3ValueOr(transaction, "merchant_name", ""),
		"category":       batch6Lane3ValueOr(transaction, "category", []any{}),
		"pending":        batch6Lane3ValueOr(transaction, "pending", false),
		"currency":       batch6Lane3ValueOr(transaction, "iso_currency_code", ""),
	}
}

// batch6Lane3PlaidCountryCodes applies the upstream `country_codes or ["US"]`
// default and returns a JSON array value.
func batch6Lane3PlaidCountryCodes(input map[string]any) []string {
	codes := stringSliceArg(input, "country_codes")
	if len(codes) == 0 {
		return []string{"US"}
	}
	return codes
}

func batch6Lane3PlaidGetAccounts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	accessToken := stringArg(input, "access_token")
	if accessToken == "" {
		return map[string]any{"error": "access_token is required"}, nil
	}
	data := batch6Lane3PlaidPost(ctx, rt, "/accounts/get", map[string]any{"access_token": accessToken})
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	accounts := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(data["accounts"]) {
		account := batch6Lane3AsMap(raw)
		balances := batch6Lane3AsMap(account["balances"])
		accounts = append(accounts, map[string]any{
			"account_id":        batch6Lane3ValueOr(account, "account_id", ""),
			"name":              batch6Lane3ValueOr(account, "name", ""),
			"official_name":     batch6Lane3ValueOr(account, "official_name", ""),
			"type":              batch6Lane3ValueOr(account, "type", ""),
			"subtype":           batch6Lane3ValueOr(account, "subtype", ""),
			"mask":              batch6Lane3ValueOr(account, "mask", ""),
			"available_balance": balances["available"],
			"current_balance":   balances["current"],
			"currency":          batch6Lane3ValueOr(balances, "iso_currency_code", ""),
		})
	}
	return map[string]any{"accounts": accounts, "count": len(accounts)}, nil
}

func batch6Lane3PlaidGetBalance(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	accessToken := stringArg(input, "access_token")
	if accessToken == "" {
		return map[string]any{"error": "access_token is required"}, nil
	}
	data := batch6Lane3PlaidPost(ctx, rt, "/accounts/balance/get", map[string]any{"access_token": accessToken})
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	accounts := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(data["accounts"]) {
		account := batch6Lane3AsMap(raw)
		balances := batch6Lane3AsMap(account["balances"])
		accounts = append(accounts, map[string]any{
			"account_id": batch6Lane3ValueOr(account, "account_id", ""),
			"name":       batch6Lane3ValueOr(account, "name", ""),
			"type":       batch6Lane3ValueOr(account, "type", ""),
			"available":  balances["available"],
			"current":    balances["current"],
			"limit":      balances["limit"],
			"currency":   batch6Lane3ValueOr(balances, "iso_currency_code", ""),
		})
	}
	return map[string]any{"accounts": accounts}, nil
}

func batch6Lane3PlaidSyncTransactions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	accessToken := stringArg(input, "access_token")
	if accessToken == "" {
		return map[string]any{"error": "access_token is required"}, nil
	}
	body := map[string]any{
		"access_token": accessToken,
		"count":        max(1, min(intArg(input, "count", 100), 500)),
	}
	if cursor := stringArg(input, "cursor"); cursor != "" {
		body["cursor"] = cursor
	}

	data := batch6Lane3PlaidPost(ctx, rt, "/transactions/sync", body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	added := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(data["added"]) {
		added = append(added, batch6Lane3PlaidTransaction(batch6Lane3AsMap(raw)))
	}
	modified := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(data["modified"]) {
		modified = append(modified, batch6Lane3PlaidTransaction(batch6Lane3AsMap(raw)))
	}
	removed := make([]string, 0)
	for _, raw := range batch6Lane3AsSlice(data["removed"]) {
		removed = append(removed, batch6Lane3AsString(batch6Lane3AsMap(raw)["transaction_id"]))
	}
	return map[string]any{
		"added":       added,
		"modified":    modified,
		"removed":     removed,
		"next_cursor": batch6Lane3ValueOr(data, "next_cursor", ""),
		"has_more":    batch6Lane3ValueOr(data, "has_more", false),
	}, nil
}

func batch6Lane3PlaidGetTransactions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	accessToken := stringArg(input, "access_token")
	startDate := stringArg(input, "start_date")
	endDate := stringArg(input, "end_date")
	if accessToken == "" || startDate == "" || endDate == "" {
		return map[string]any{"error": "access_token, start_date, and end_date are required"}, nil
	}
	body := map[string]any{
		"access_token": accessToken,
		"start_date":   startDate,
		"end_date":     endDate,
		"options": map[string]any{
			"count":  max(1, min(intArg(input, "count", 100), 500)),
			"offset": max(0, intArg(input, "offset", 0)),
		},
	}

	data := batch6Lane3PlaidPost(ctx, rt, "/transactions/get", body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	transactions := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(data["transactions"]) {
		transactions = append(transactions, batch6Lane3PlaidTransaction(batch6Lane3AsMap(raw)))
	}
	return map[string]any{
		"transactions":       transactions,
		"total_transactions": batch6Lane3ValueOr(data, "total_transactions", 0),
	}, nil
}

func batch6Lane3PlaidGetInstitution(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	institutionID := stringArg(input, "institution_id")
	if institutionID == "" {
		return map[string]any{"error": "institution_id is required"}, nil
	}
	body := map[string]any{
		"institution_id": institutionID,
		"country_codes":  batch6Lane3PlaidCountryCodes(input),
		"options":        map[string]any{"include_optional_metadata": true},
	}
	data := batch6Lane3PlaidPost(ctx, rt, "/institutions/get_by_id", body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	institution := batch6Lane3AsMap(data["institution"])
	return map[string]any{
		"institution_id": batch6Lane3ValueOr(institution, "institution_id", ""),
		"name":           batch6Lane3ValueOr(institution, "name", ""),
		"products":       batch6Lane3ValueOr(institution, "products", []any{}),
		"country_codes":  batch6Lane3ValueOr(institution, "country_codes", []any{}),
		"url":            batch6Lane3ValueOr(institution, "url", ""),
		"logo":           batch6Lane3ValueOr(institution, "logo", ""),
		"oauth":          batch6Lane3ValueOr(institution, "oauth", false),
	}, nil
}

func batch6Lane3PlaidSearchInstitutions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	body := map[string]any{
		"query":         query,
		"country_codes": batch6Lane3PlaidCountryCodes(input),
		"options": map[string]any{
			"include_optional_metadata": true,
			"limit":                     max(1, min(intArg(input, "limit", 10), 50)),
		},
	}
	if products := stringSliceArg(input, "products"); len(products) > 0 {
		body["products"] = products
	}

	data := batch6Lane3PlaidPost(ctx, rt, "/institutions/search", body)
	if failure, failed := batch6Lane3Failure(data); failed {
		return failure, nil
	}
	institutions := make([]map[string]any, 0)
	for _, raw := range batch6Lane3AsSlice(data["institutions"]) {
		institution := batch6Lane3AsMap(raw)
		institutions = append(institutions, map[string]any{
			"institution_id": batch6Lane3ValueOr(institution, "institution_id", ""),
			"name":           batch6Lane3ValueOr(institution, "name", ""),
			"products":       batch6Lane3ValueOr(institution, "products", []any{}),
			"country_codes":  batch6Lane3ValueOr(institution, "country_codes", []any{}),
			"url":            batch6Lane3ValueOr(institution, "url", ""),
			"oauth":          batch6Lane3ValueOr(institution, "oauth", false),
		})
	}
	return map[string]any{"institutions": institutions, "count": len(institutions)}, nil
}
