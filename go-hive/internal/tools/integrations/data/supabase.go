// Supabase integration (upstream supabase_tool, 7 actions).
//
// The upstream module uses the Supabase REST surface directly: PostgREST for
// table CRUD, GoTrue for signup/signin and Edge Functions for invocation. This
// port keeps those endpoints, headers, status checks and response projections.
//
// Deliberate limits: anon key only (no service-role switching), like upstream;
// no automatic retries; request bodies are capped at 8 MiB.
package data

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const supabaseHelpText = "Get your keys at https://supabase.com/dashboard → Project Settings → API"

// supabaseConfig resolves the project URL and anon key.
func supabaseConfig(rt *Runtime) (string, string, bool) {
	baseURL := databaseBaseURL(rt, "supabase_tool", "SUPABASE_URL")
	key := configuredToken(rt, "supabase_tool", "SUPABASE_ANON_KEY")
	if baseURL == "" || key == "" {
		return "", "", false
	}
	return baseURL, key, true
}

func supabaseMissingConfig() map[string]any {
	return map[string]any{
		"error": "SUPABASE_ANON_KEY or SUPABASE_URL not set",
		"help":  supabaseHelpText,
	}
}

func supabaseRESTHeaders(key string) map[string]string {
	return map[string]string{
		"apikey":        key,
		"Authorization": "Bearer " + key,
		"Content-Type":  "application/json",
		"Prefer":        "return=representation",
	}
}

// supabaseFilters parses a PostgREST filter string ("a=eq.1&b=eq.2").
func supabaseFilters(raw string) url.Values {
	values := url.Values{}
	for _, part := range strings.Split(raw, "&") {
		index := strings.IndexByte(part, '=')
		if index <= 0 {
			continue
		}
		values.Set(part[:index], part[index+1:])
	}
	return values
}

// supabaseCall performs one REST call and returns the response.
func supabaseCall(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (*databaseResponse, error) {
	return do(ctx, rt, method, target, headers, payload)
}

// supabaseFailure renders the upstream "Supabase error <status>: <body>"
// message.
func supabaseFailure(response *databaseResponse) map[string]any {
	return databaseErr(fmt.Sprintf(
		"Supabase error %d: %s",
		response.Status,
		truncate(string(response.Body), maxError),
	))
}

// supabaseBody decodes a successful response body.
func supabaseBody(response *databaseResponse) (any, map[string]any) {
	decoded, err := decodeJSON(response.Body)
	if err != nil {
		return nil, databaseErr("Supabase returned an invalid JSON response")
	}
	return decoded, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_select
func supabaseSelect(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	table := stringArg(input, "table")
	if table == "" {
		return databaseErr("table is required"), nil
	}
	limit := intArg(input, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	columns := stringArgDefault(input, "columns", "*")
	params := url.Values{}
	params.Set("select", columns)
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(intArg(input, "offset", 0)))
	for name, values := range supabaseFilters(stringArg(input, "filters")) {
		for _, value := range values {
			params.Set(name, value)
		}
	}
	if order := stringArg(input, "order"); order != "" {
		params.Set("order", order)
	}
	target := baseURL + "/rest/v1/" + table + "?" + params.Encode()
	response, err := supabaseCall(ctx, rt, http.MethodGet, target, supabaseRESTHeaders(key), nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return databaseErr("Request to Supabase timed out"), nil
		}
		return databaseErr("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return supabaseFailure(response), nil
	}
	rows, failure := supabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "rows": rows, "count": databaseLen(rows)}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_insert
func supabaseInsert(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	table, rawRows := stringArg(input, "table"), stringArg(input, "rows")
	if table == "" || rawRows == "" {
		return databaseErr("table and rows are required"), nil
	}
	var body any
	if err := json.Unmarshal([]byte(rawRows), &body); err != nil {
		return databaseErr("Invalid JSON in rows: " + err.Error()), nil
	}
	response, err := supabaseCall(ctx, rt, http.MethodPost, baseURL+"/rest/v1/"+table, supabaseRESTHeaders(key), body)
	if err != nil {
		if isTimeout(ctx, err) {
			return databaseErr("Request to Supabase timed out"), nil
		}
		return databaseErr("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK && response.Status != http.StatusCreated {
		return supabaseFailure(response), nil
	}
	inserted, failure := supabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "inserted": inserted}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_update
func supabaseUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	table, filters, rawData := stringArg(input, "table"), stringArg(input, "filters"), stringArg(input, "data")
	if table == "" || filters == "" || rawData == "" {
		return databaseErr("table, filters, and data are required"), nil
	}
	var body any
	if err := json.Unmarshal([]byte(rawData), &body); err != nil {
		return databaseErr("Invalid JSON in data: " + err.Error()), nil
	}
	target := baseURL + "/rest/v1/" + table + "?" + supabaseFilters(filters).Encode()
	response, err := supabaseCall(ctx, rt, http.MethodPatch, target, supabaseRESTHeaders(key), body)
	if err != nil {
		if isTimeout(ctx, err) {
			return databaseErr("Request to Supabase timed out"), nil
		}
		return databaseErr("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return supabaseFailure(response), nil
	}
	updated, failure := supabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "updated": updated}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_delete
func supabaseDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	table, filters := stringArg(input, "table"), stringArg(input, "filters")
	if table == "" || filters == "" {
		return databaseErr("table and filters are required"), nil
	}
	target := baseURL + "/rest/v1/" + table + "?" + supabaseFilters(filters).Encode()
	headers := supabaseRESTHeaders(key)
	headers["Prefer"] = "return=representation"
	response, err := supabaseCall(ctx, rt, http.MethodDelete, target, headers, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return databaseErr("Request to Supabase timed out"), nil
		}
		return databaseErr("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return supabaseFailure(response), nil
	}
	deleted, failure := supabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "deleted": deleted}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_auth_signup
func supabaseAuthSignup(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	email, password := stringArg(input, "email"), stringArg(input, "password")
	if email == "" || password == "" {
		return databaseErr("email and password are required"), nil
	}
	if len(password) < 6 {
		return databaseErr("password must be at least 6 characters"), nil
	}
	response, err := supabaseCall(ctx, rt, http.MethodPost, baseURL+"/auth/v1/signup", map[string]string{
		"apikey":       key,
		"Content-Type": "application/json",
	}, map[string]any{"email": email, "password": password})
	if err != nil {
		return databaseErr("Auth signup failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK && response.Status != http.StatusCreated {
		return databaseErr(fmt.Sprintf(
			"Auth error %d: %s",
			response.Status,
			truncate(string(response.Body), maxError),
		)), nil
	}
	decoded, failure := supabaseBody(response)
	if failure != nil {
		return databaseErr("Auth signup failed: invalid JSON response"), nil
	}
	data, _ := decoded.(map[string]any)
	user := data
	if nested, present := data["user"].(map[string]any); present {
		user = nested
	}
	confirmed := user["confirmed_at"] != nil
	return map[string]any{
		"user_id":   stringValue(user["id"]),
		"email":     stringValue(user["email"]),
		"confirmed": confirmed,
	}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_auth_signin
func supabaseAuthSignin(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	email, password := stringArg(input, "email"), stringArg(input, "password")
	if email == "" || password == "" {
		return databaseErr("email and password are required"), nil
	}
	response, err := supabaseCall(ctx, rt, http.MethodPost, baseURL+"/auth/v1/token?grant_type=password", map[string]string{
		"apikey":       key,
		"Content-Type": "application/json",
	}, map[string]any{"email": email, "password": password})
	if err != nil {
		return databaseErr("Auth signin failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return databaseErr(fmt.Sprintf(
			"Auth error %d: %s",
			response.Status,
			truncate(string(response.Body), maxError),
		)), nil
	}
	decoded, failure := supabaseBody(response)
	if failure != nil {
		return databaseErr("Auth signin failed: invalid JSON response"), nil
	}
	data, _ := decoded.(map[string]any)
	user, _ := data["user"].(map[string]any)
	expires := 0
	switch typed := data["expires_in"].(type) {
	case float64:
		expires = int(typed)
	case int:
		expires = typed
	}
	return map[string]any{
		"authenticated": true,
		"user_id":       stringValue(user["id"]),
		"email":         stringValue(user["email"]),
		"expires_in":    expires,
	}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_edge_invoke
func supabaseEdgeInvoke(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := supabaseConfig(rt)
	if !ok {
		return supabaseMissingConfig(), nil
	}
	functionName := stringArg(input, "function_name")
	if functionName == "" {
		return databaseErr("function_name is required"), nil
	}
	rawBody := stringArgDefault(input, "body", "{}")
	var parsed any
	if err := json.Unmarshal([]byte(rawBody), &parsed); err != nil {
		return databaseErr("Invalid JSON in body: " + err.Error()), nil
	}
	headers := map[string]string{
		"apikey":        key,
		"Authorization": "Bearer " + key,
		"Content-Type":  "application/json",
	}
	method := http.MethodPost
	payload := parsed
	if strings.EqualFold(stringArgDefault(input, "method", "POST"), "GET") {
		method = http.MethodGet
		payload = nil
	}
	target := baseURL + "/functions/v1/" + functionName
	response, err := supabaseCall(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return databaseErr("Edge function invocation timed out"), nil
		}
		return databaseErr("Edge function invocation failed: " + err.Error()), nil
	}
	var responseData any
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		decoded, decodeErr := decodeJSON(response.Body)
		if decodeErr != nil {
			responseData = string(response.Body)
		} else {
			responseData = decoded
		}
	} else {
		responseData = string(response.Body)
	}
	if response.Status >= 400 {
		return map[string]any{
			"error":    fmt.Sprintf("Edge function error %d", response.Status),
			"response": responseData,
		}, nil
	}
	return map[string]any{"status_code": response.Status, "response": responseData}, nil
}

// string renders a JSON scalar as a string ("" for nil).
func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

// supabaseImpls maps the upstream action names to handlers.
func supabaseImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"supabase_select":      supabaseSelect,
		"supabase_insert":      supabaseInsert,
		"supabase_update":      supabaseUpdate,
		"supabase_delete":      supabaseDelete,
		"supabase_auth_signup": supabaseAuthSignup,
		"supabase_auth_signin": supabaseAuthSignin,
		"supabase_edge_invoke": supabaseEdgeInvoke,
	}
}

// supabaseTool is the model-facing catalog entry.
var supabaseTool = &Tool{
	ID:          "supabase_tool",
	Name:        "supabase",
	DisplayName: "Supabase",
	Summary:     "Supabase database, auth and edge function integration (7 actions).",
	Actions: []Action{
		{
			Name:     "supabase_select",
			Summary:  "Query rows from a table using PostgREST.",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"table"},
			Params: map[string]any{
				"table":   param("string"),
				"columns": paramDefault("string", "*"),
				"filters": paramDefault("string", ""),
				"order":   paramDefault("string", ""),
				"limit":   paramDefault("integer", 100),
				"offset":  paramDefault("integer", 0),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
		{
			Name:     "supabase_insert",
			Summary:  "Insert one or more rows (JSON object or array).",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"table", "rows"},
			Params: map[string]any{
				"table": param("string"),
				"rows":  param("string"),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
		{
			Name:     "supabase_update",
			Summary:  "Update rows matching PostgREST filters.",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"table", "filters", "data"},
			Params: map[string]any{
				"table":   param("string"),
				"filters": param("string"),
				"data":    param("string"),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
		{
			Name:     "supabase_delete",
			Summary:  "Delete rows matching PostgREST filters.",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"table", "filters"},
			Params: map[string]any{
				"table":   param("string"),
				"filters": param("string"),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
		{
			Name:     "supabase_auth_signup",
			Summary:  "Register a new user via GoTrue.",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"email", "password"},
			Params: map[string]any{
				"email":    param("string"),
				"password": param("string"),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
		{
			Name:     "supabase_auth_signin",
			Summary:  "Verify user credentials and return session metadata.",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"email", "password"},
			Params: map[string]any{
				"email":    param("string"),
				"password": param("string"),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
		{
			Name:     "supabase_edge_invoke",
			Summary:  "Invoke an Edge Function with an optional JSON body.",
			Source:   "supabase_tool/supabase_tool.py",
			Required: []string{"function_name"},
			Params: map[string]any{
				"function_name": param("string"),
				"body":          paramDefault("string", "{}"),
				"method":        paramDefault("string", "POST"),
			},
			Requires: []string{"SUPABASE_ANON_KEY", "SUPABASE_URL"},
		},
	},
}
