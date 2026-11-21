// Supabase integration (upstream supabase_tool, 7 actions).
//
// The upstream module uses the Supabase REST surface directly: PostgREST for
// table CRUD, GoTrue for signup/signin and Edge Functions for invocation. This
// port keeps those endpoints, headers, status checks and response projections.
//
// Deliberate limits: anon key only (no service-role switching), like upstream;
// no automatic retries; request bodies are capped at 8 MiB.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const batch5SupabaseHelpText = "Get your keys at https://supabase.com/dashboard → Project Settings → API"

// batch5SupabaseConfig resolves the project URL and anon key.
func batch5SupabaseConfig(rt *Runtime) (string, string, bool) {
	baseURL := batch5Lane2ModuleBaseURL(rt, "supabase_tool", "SUPABASE_URL")
	key := batch5Lane2ConfiguredToken(rt, "supabase_tool", "SUPABASE_ANON_KEY")
	if baseURL == "" || key == "" {
		return "", "", false
	}
	return baseURL, key, true
}

func batch5SupabaseMissingConfig() map[string]any {
	return map[string]any{
		"error": "SUPABASE_ANON_KEY or SUPABASE_URL not set",
		"help":  batch5SupabaseHelpText,
	}
}

func batch5SupabaseRESTHeaders(key string) map[string]string {
	return map[string]string{
		"apikey":        key,
		"Authorization": "Bearer " + key,
		"Content-Type":  "application/json",
		"Prefer":        "return=representation",
	}
}

// batch5SupabaseFilters parses a PostgREST filter string ("a=eq.1&b=eq.2").
func batch5SupabaseFilters(raw string) url.Values {
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

// batch5SupabaseCall performs one REST call and returns the response.
func batch5SupabaseCall(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (*batch5Lane2Response, error) {
	return batch5Lane2Do(ctx, rt, method, target, headers, payload)
}

// batch5SupabaseFailure renders the upstream "Supabase error <status>: <body>"
// message.
func batch5SupabaseFailure(response *batch5Lane2Response) map[string]any {
	return batch5Lane2Err(fmt.Sprintf(
		"Supabase error %d: %s",
		response.Status,
		batch5Lane2Truncate(string(response.Body), batch5Lane2MaxError),
	))
}

// batch5SupabaseBody decodes a successful response body.
func batch5SupabaseBody(response *batch5Lane2Response) (any, map[string]any) {
	decoded, err := batch5Lane2DecodeJSON(response.Body)
	if err != nil {
		return nil, batch5Lane2Err("Supabase returned an invalid JSON response")
	}
	return decoded, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_select
func batch5Lane2SupabaseSelect(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	table := stringArg(input, "table")
	if table == "" {
		return batch5Lane2Err("table is required"), nil
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
	for name, values := range batch5SupabaseFilters(stringArg(input, "filters")) {
		for _, value := range values {
			params.Set(name, value)
		}
	}
	if order := stringArg(input, "order"); order != "" {
		params.Set("order", order)
	}
	target := baseURL + "/rest/v1/" + table + "?" + params.Encode()
	response, err := batch5SupabaseCall(ctx, rt, http.MethodGet, target, batch5SupabaseRESTHeaders(key), nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return batch5Lane2Err("Request to Supabase timed out"), nil
		}
		return batch5Lane2Err("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return batch5SupabaseFailure(response), nil
	}
	rows, failure := batch5SupabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "rows": rows, "count": batch5Lane2Len(rows)}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_insert
func batch5Lane2SupabaseInsert(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	table, rawRows := stringArg(input, "table"), stringArg(input, "rows")
	if table == "" || rawRows == "" {
		return batch5Lane2Err("table and rows are required"), nil
	}
	var body any
	if err := json.Unmarshal([]byte(rawRows), &body); err != nil {
		return batch5Lane2Err("Invalid JSON in rows: " + err.Error()), nil
	}
	response, err := batch5SupabaseCall(ctx, rt, http.MethodPost, baseURL+"/rest/v1/"+table, batch5SupabaseRESTHeaders(key), body)
	if err != nil {
		if isTimeout(ctx, err) {
			return batch5Lane2Err("Request to Supabase timed out"), nil
		}
		return batch5Lane2Err("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK && response.Status != http.StatusCreated {
		return batch5SupabaseFailure(response), nil
	}
	inserted, failure := batch5SupabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "inserted": inserted}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_update
func batch5Lane2SupabaseUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	table, filters, rawData := stringArg(input, "table"), stringArg(input, "filters"), stringArg(input, "data")
	if table == "" || filters == "" || rawData == "" {
		return batch5Lane2Err("table, filters, and data are required"), nil
	}
	var body any
	if err := json.Unmarshal([]byte(rawData), &body); err != nil {
		return batch5Lane2Err("Invalid JSON in data: " + err.Error()), nil
	}
	target := baseURL + "/rest/v1/" + table + "?" + batch5SupabaseFilters(filters).Encode()
	response, err := batch5SupabaseCall(ctx, rt, http.MethodPatch, target, batch5SupabaseRESTHeaders(key), body)
	if err != nil {
		if isTimeout(ctx, err) {
			return batch5Lane2Err("Request to Supabase timed out"), nil
		}
		return batch5Lane2Err("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return batch5SupabaseFailure(response), nil
	}
	updated, failure := batch5SupabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "updated": updated}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_delete
func batch5Lane2SupabaseDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	table, filters := stringArg(input, "table"), stringArg(input, "filters")
	if table == "" || filters == "" {
		return batch5Lane2Err("table and filters are required"), nil
	}
	target := baseURL + "/rest/v1/" + table + "?" + batch5SupabaseFilters(filters).Encode()
	headers := batch5SupabaseRESTHeaders(key)
	headers["Prefer"] = "return=representation"
	response, err := batch5SupabaseCall(ctx, rt, http.MethodDelete, target, headers, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return batch5Lane2Err("Request to Supabase timed out"), nil
		}
		return batch5Lane2Err("Supabase request failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return batch5SupabaseFailure(response), nil
	}
	deleted, failure := batch5SupabaseBody(response)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{"table": table, "deleted": deleted}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_auth_signup
func batch5Lane2SupabaseAuthSignup(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	email, password := stringArg(input, "email"), stringArg(input, "password")
	if email == "" || password == "" {
		return batch5Lane2Err("email and password are required"), nil
	}
	if len(password) < 6 {
		return batch5Lane2Err("password must be at least 6 characters"), nil
	}
	response, err := batch5SupabaseCall(ctx, rt, http.MethodPost, baseURL+"/auth/v1/signup", map[string]string{
		"apikey":       key,
		"Content-Type": "application/json",
	}, map[string]any{"email": email, "password": password})
	if err != nil {
		return batch5Lane2Err("Auth signup failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK && response.Status != http.StatusCreated {
		return batch5Lane2Err(fmt.Sprintf(
			"Auth error %d: %s",
			response.Status,
			batch5Lane2Truncate(string(response.Body), batch5Lane2MaxError),
		)), nil
	}
	decoded, failure := batch5SupabaseBody(response)
	if failure != nil {
		return batch5Lane2Err("Auth signup failed: invalid JSON response"), nil
	}
	data, _ := decoded.(map[string]any)
	user := data
	if nested, present := data["user"].(map[string]any); present {
		user = nested
	}
	confirmed := user["confirmed_at"] != nil
	return map[string]any{
		"user_id":   batch5Lane2String(user["id"]),
		"email":     batch5Lane2String(user["email"]),
		"confirmed": confirmed,
	}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_auth_signin
func batch5Lane2SupabaseAuthSignin(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	email, password := stringArg(input, "email"), stringArg(input, "password")
	if email == "" || password == "" {
		return batch5Lane2Err("email and password are required"), nil
	}
	response, err := batch5SupabaseCall(ctx, rt, http.MethodPost, baseURL+"/auth/v1/token?grant_type=password", map[string]string{
		"apikey":       key,
		"Content-Type": "application/json",
	}, map[string]any{"email": email, "password": password})
	if err != nil {
		return batch5Lane2Err("Auth signin failed: " + err.Error()), nil
	}
	if response.Status != http.StatusOK {
		return batch5Lane2Err(fmt.Sprintf(
			"Auth error %d: %s",
			response.Status,
			batch5Lane2Truncate(string(response.Body), batch5Lane2MaxError),
		)), nil
	}
	decoded, failure := batch5SupabaseBody(response)
	if failure != nil {
		return batch5Lane2Err("Auth signin failed: invalid JSON response"), nil
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
		"user_id":       batch5Lane2String(user["id"]),
		"email":         batch5Lane2String(user["email"]),
		"expires_in":    expires,
	}, nil
}

// Parity: tools/src/aden_tools/tools/supabase_tool/supabase_tool.py::supabase_edge_invoke
func batch5Lane2SupabaseEdgeInvoke(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	baseURL, key, ok := batch5SupabaseConfig(rt)
	if !ok {
		return batch5SupabaseMissingConfig(), nil
	}
	functionName := stringArg(input, "function_name")
	if functionName == "" {
		return batch5Lane2Err("function_name is required"), nil
	}
	rawBody := stringArgDefault(input, "body", "{}")
	var parsed any
	if err := json.Unmarshal([]byte(rawBody), &parsed); err != nil {
		return batch5Lane2Err("Invalid JSON in body: " + err.Error()), nil
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
	response, err := batch5SupabaseCall(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return batch5Lane2Err("Edge function invocation timed out"), nil
		}
		return batch5Lane2Err("Edge function invocation failed: " + err.Error()), nil
	}
	var responseData any
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		decoded, decodeErr := batch5Lane2DecodeJSON(response.Body)
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

// batch5Lane2String renders a JSON scalar as a string ("" for nil).
func batch5Lane2String(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

// batch5Lane2SupabaseImpls maps the upstream action names to handlers.
func batch5Lane2SupabaseImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"supabase_select":      batch5Lane2SupabaseSelect,
		"supabase_insert":      batch5Lane2SupabaseInsert,
		"supabase_update":      batch5Lane2SupabaseUpdate,
		"supabase_delete":      batch5Lane2SupabaseDelete,
		"supabase_auth_signup": batch5Lane2SupabaseAuthSignup,
		"supabase_auth_signin": batch5Lane2SupabaseAuthSignin,
		"supabase_edge_invoke": batch5Lane2SupabaseEdgeInvoke,
	}
}

// batch5Lane2SupabaseTool is the model-facing catalog entry.
var batch5Lane2SupabaseTool = &Tool{
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
