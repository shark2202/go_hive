// HubSpot CRM integration (upstream hubspot_tool, 15 actions). Part of batch 5
// lane 4; see hubspot_obsidian.go for the registry and shared helpers.
//
// Every action maps to one upstream @mcp.tool function and one _HubSpotClient
// method. Search/get/create/update differ only in CRM object type, so the
// handlers are thin wrappers over shared helpers; error strings and status
// handling come from upstream _handle_response.
package crm

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

const (
	hubSpotMaxBody  = 8 << 20
	hubSpotMaxLimit = 100
	hubSpotMaxAssoc = 500
)

// hubSpotCRMTypes are the object types upstream delete validates.
var hubSpotCRMTypes = []string{"contacts", "companies", "deals"}

var hubSpotTool = &Tool{
	ID:          "hubspot_tool",
	Name:        "hubspot",
	DisplayName: "HubSpot",
	Summary:     "HubSpot CRM integration (15 actions).",
	Actions: []Action{
		{
			Name:    "hubspot_search_contacts",
			Summary: "Search HubSpot contacts.",
			Source:  "hubspot_tool/hubspot_tool.py",
			Params: map[string]any{
				"query":      paramDefault("string", ""),
				"properties": param("array"),
				"limit":      paramDefault("integer", 10),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_get_contact",
			Summary:  "Get a HubSpot contact by ID.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"contact_id"},
			Params: map[string]any{
				"contact_id": param("string"),
				"properties": param("array"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_create_contact",
			Summary:  "Create a new HubSpot contact.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"properties"},
			Params: map[string]any{
				"properties": param("object"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_update_contact",
			Summary:  "Update an existing HubSpot contact.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"contact_id", "properties"},
			Params: map[string]any{
				"contact_id": param("string"),
				"properties": param("object"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:    "hubspot_search_companies",
			Summary: "Search HubSpot companies.",
			Source:  "hubspot_tool/hubspot_tool.py",
			Params: map[string]any{
				"query":      paramDefault("string", ""),
				"properties": param("array"),
				"limit":      paramDefault("integer", 10),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_get_company",
			Summary:  "Get a HubSpot company by ID.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"company_id"},
			Params: map[string]any{
				"company_id": param("string"),
				"properties": param("array"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_create_company",
			Summary:  "Create a new HubSpot company.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"properties"},
			Params: map[string]any{
				"properties": param("object"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_update_company",
			Summary:  "Update an existing HubSpot company.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"company_id", "properties"},
			Params: map[string]any{
				"company_id": param("string"),
				"properties": param("object"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:    "hubspot_search_deals",
			Summary: "Search HubSpot deals.",
			Source:  "hubspot_tool/hubspot_tool.py",
			Params: map[string]any{
				"query":      paramDefault("string", ""),
				"properties": param("array"),
				"limit":      paramDefault("integer", 10),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_get_deal",
			Summary:  "Get a HubSpot deal by ID.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"deal_id"},
			Params: map[string]any{
				"deal_id":    param("string"),
				"properties": param("array"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_create_deal",
			Summary:  "Create a new HubSpot deal.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"properties"},
			Params: map[string]any{
				"properties": param("object"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_update_deal",
			Summary:  "Update an existing HubSpot deal.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"deal_id", "properties"},
			Params: map[string]any{
				"deal_id":    param("string"),
				"properties": param("object"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_delete_object",
			Summary:  "Delete (archive) a HubSpot CRM object.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"object_type", "object_id"},
			Params: map[string]any{
				"object_type": param("string"),
				"object_id":   param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_list_associations",
			Summary:  "List associations between HubSpot CRM objects.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"from_object_type", "from_object_id", "to_object_type"},
			Params: map[string]any{
				"from_object_type": param("string"),
				"from_object_id":   param("string"),
				"to_object_type":   param("string"),
				"limit":            paramDefault("integer", 100),
				"account":          paramDefault("string", ""),
			},
		},
		{
			Name:     "hubspot_create_association",
			Summary:  "Create an association between two HubSpot CRM objects.",
			Source:   "hubspot_tool/hubspot_tool.py",
			Required: []string{"from_object_type", "from_object_id", "to_object_type", "to_object_id"},
			Params: map[string]any{
				"from_object_type":    param("string"),
				"from_object_id":      param("string"),
				"to_object_type":      param("string"),
				"to_object_id":        param("string"),
				"association_type_id": paramDefault("integer", 0),
				"account":             paramDefault("string", ""),
			},
		},
	},
}

func hubSpotImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"hubspot_search_contacts":    hubSpotSearchContacts,
		"hubspot_get_contact":        hubSpotGetContact,
		"hubspot_create_contact":     hubSpotCreateContact,
		"hubspot_update_contact":     hubSpotUpdateContact,
		"hubspot_search_companies":   hubSpotSearchCompanies,
		"hubspot_get_company":        hubSpotGetCompany,
		"hubspot_create_company":     hubSpotCreateCompany,
		"hubspot_update_company":     hubSpotUpdateCompany,
		"hubspot_search_deals":       hubSpotSearchDeals,
		"hubspot_get_deal":           hubSpotGetDeal,
		"hubspot_create_deal":        hubSpotCreateDeal,
		"hubspot_update_deal":        hubSpotUpdateDeal,
		"hubspot_delete_object":      hubSpotDeleteObject,
		"hubspot_list_associations":  hubSpotListAssociations,
		"hubspot_create_association": hubSpotCreateAssociation,
	}
}

func hubSpotAuthError() map[string]any {
	return map[string]any{
		"error": "HubSpot credentials not configured",
		"help":  "Set HUBSPOT_ACCESS_TOKEN environment variable or configure via credential store",
	}
}

// accessToken resolves a lane-4 credential for the current call: an
// account alias wins over the module token (upstream `_get_token(account)` ->
// credentials.get_by_alias(provider, account)); without an alias the module
// token is refreshed when its OAuth2 access token is near expiry.
func accessToken(ctx context.Context, rt *Runtime, moduleID, provider string) (string, bool, map[string]any) {
	if token, handled := rt.ResolveAccountToken(provider); handled {
		return token, false, nil
	}
	return oauthModuleToken(ctx, rt, moduleID)
}

// hubSpotClient mirrors upstream _HubSpotClient: one token and base URL
// bound to a single action invocation.
type hubSpotClient struct {
	ctx         context.Context
	rt          *Runtime
	base        string
	token       string
	refreshable bool
}

func newHubSpotClient(ctx context.Context, rt *Runtime) (*hubSpotClient, map[string]any) {
	token, refreshable, problem := accessToken(ctx, rt, "hubspot_tool", "hubspot")
	if problem != nil {
		return nil, problem
	}
	if token == "" {
		return nil, hubSpotAuthError()
	}
	return &hubSpotClient{
		ctx:         ctx,
		rt:          rt,
		base:        moduleURL(rt, "hubspot_tool", "https://api.hubapi.com"),
		token:       token,
		refreshable: refreshable,
	}, nil
}

// send performs one request and returns the response metadata plus the body.
// Transport and request-construction failures become error dicts. A 401 with a
// refreshable token is retried once with a freshly minted access token.
func (c *hubSpotClient) send(method, path string, query url.Values, payload any) (*http.Response, []byte, map[string]any) {
	resp, raw, problem := c.attempt(method, path, query, payload)
	if resp == nil || problem != nil || resp.StatusCode != http.StatusUnauthorized || !c.refreshable {
		return resp, raw, problem
	}
	resp.Body.Close()
	resolution := c.rt.ResolveUnauthorized(c.ctx, "hubspot_tool", "HubSpot")
	if !resolution.Retry {
		if resolution.Failure != nil {
			return nil, nil, resolution.Failure
		}
		return nil, nil, problem
	}
	c.token = resolution.Token
	return c.attempt(method, path, query, payload)
}

// attempt is one authorized request against the HubSpot API.
func (c *hubSpotClient) attempt(method, path string, query url.Values, payload any) (*http.Response, []byte, map[string]any) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, map[string]any{"error": "HubSpot request failed: " + err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(c.ctx, method, c.base+path, body)
	if err != nil {
		return nil, nil, map[string]any{"error": "HubSpot request failed: " + err.Error()}
	}
	if query != nil {
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := runtimeHTTP(c.rt).Do(req)
	if err != nil {
		if isTimeout(c.ctx, err) {
			return nil, nil, map[string]any{"error": "Request timed out"}
		}
		return nil, nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, hubSpotMaxBody))
	if err != nil {
		return resp, nil, map[string]any{"error": "HubSpot request failed: " + err.Error()}
	}
	return resp, raw, nil
}

// handle mirrors upstream _handle_response: known status codes get fixed
// messages, other 4xx/5xx carry the provider's message, and success returns the
// decoded JSON body.
func hubSpotHandle(resp *http.Response, raw []byte) (any, map[string]any) {
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Invalid or expired HubSpot access token"}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Insufficient permissions. Check your HubSpot app scopes."}
	case http.StatusNotFound:
		return nil, map[string]any{"error": "Resource not found"}
	case http.StatusTooManyRequests:
		return nil, map[string]any{"error": "HubSpot rate limit exceeded. Try again later."}
	}
	if resp.StatusCode >= 400 {
		detail := string(raw)
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err == nil {
			if message, ok := payload["message"].(string); ok {
				detail = message
			}
		}
		return nil, map[string]any{
			"error": fmt.Sprintf("HubSpot API error (HTTP %d): %s", resp.StatusCode, detail),
		}
	}
	if resp.StatusCode == http.StatusNoContent || len(raw) == 0 {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, map[string]any{"error": "HubSpot API error: invalid JSON response"}
	}
	return value, nil
}

// call sends a request and maps the provider response.
func (c *hubSpotClient) call(method, path string, query url.Values, payload any) (any, map[string]any) {
	resp, raw, failure := c.send(method, path, query, payload)
	if failure != nil {
		return nil, failure
	}
	return hubSpotHandle(resp, raw)
}

// hubSpotProperties reads the required `properties` object.
func hubSpotProperties(input map[string]any) (map[string]any, map[string]any) {
	value, present := input["properties"]
	if !present || value == nil {
		return nil, map[string]any{"error": "properties is required"}
	}
	properties, ok := value.(map[string]any)
	if !ok {
		return nil, map[string]any{"error": "properties must be an object"}
	}
	return properties, nil
}

// hubSpotSearch implements search_objects for one CRM object type.
func hubSpotSearch(ctx context.Context, rt *Runtime, input map[string]any, objectType string, defaultProperties []string) (any, error) {
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	properties := stringSliceArg(input, "properties")
	if len(properties) == 0 {
		properties = defaultProperties
	}
	limit := intArg(input, "limit", 10)
	if limit > hubSpotMaxLimit {
		limit = hubSpotMaxLimit
	}
	body := map[string]any{"limit": limit}
	if query := stringArg(input, "query"); query != "" {
		body["query"] = query
	}
	if len(properties) > 0 {
		body["properties"] = properties
	}
	value, failure := client.call(http.MethodPost, "/crm/v3/objects/"+objectType+"/search", nil, body)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

// hubSpotGet implements get_object for one CRM object type.
func hubSpotGet(ctx context.Context, rt *Runtime, input map[string]any, objectType, idParam string) (any, error) {
	objectID := stringArg(input, idParam)
	if objectID == "" {
		return map[string]any{"error": idParam + " is required"}, nil
	}
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	query := url.Values{}
	if properties := stringSliceArg(input, "properties"); len(properties) > 0 {
		query.Set("properties", strings.Join(properties, ","))
	}
	value, failure := client.call(http.MethodGet, "/crm/v3/objects/"+objectType+"/"+objectID, query, nil)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

// hubSpotCreate implements create_object for one CRM object type.
func hubSpotCreate(ctx context.Context, rt *Runtime, input map[string]any, objectType string) (any, error) {
	properties, failure := hubSpotProperties(input)
	if failure != nil {
		return failure, nil
	}
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	value, failure := client.call(http.MethodPost, "/crm/v3/objects/"+objectType, nil, map[string]any{"properties": properties})
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

// hubSpotUpdate implements update_object for one CRM object type.
func hubSpotUpdate(ctx context.Context, rt *Runtime, input map[string]any, objectType, idParam string) (any, error) {
	objectID := stringArg(input, idParam)
	if objectID == "" {
		return map[string]any{"error": idParam + " is required"}, nil
	}
	properties, failure := hubSpotProperties(input)
	if failure != nil {
		return failure, nil
	}
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	value, failure := client.call(http.MethodPatch, "/crm/v3/objects/"+objectType+"/"+objectID, nil, map[string]any{"properties": properties})
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

func hubSpotSearchContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotSearch(ctx, rt, input, "contacts", []string{"email", "firstname", "lastname"})
}

func hubSpotGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotGet(ctx, rt, input, "contacts", "contact_id")
}

func hubSpotCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotCreate(ctx, rt, input, "contacts")
}

func hubSpotUpdateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotUpdate(ctx, rt, input, "contacts", "contact_id")
}

func hubSpotSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotSearch(ctx, rt, input, "companies", []string{"name", "domain", "industry"})
}

func hubSpotGetCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotGet(ctx, rt, input, "companies", "company_id")
}

func hubSpotCreateCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotCreate(ctx, rt, input, "companies")
}

func hubSpotUpdateCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotUpdate(ctx, rt, input, "companies", "company_id")
}

func hubSpotSearchDeals(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotSearch(ctx, rt, input, "deals", []string{"dealname", "amount", "dealstage"})
}

func hubSpotGetDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotGet(ctx, rt, input, "deals", "deal_id")
}

func hubSpotCreateDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotCreate(ctx, rt, input, "deals")
}

func hubSpotUpdateDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return hubSpotUpdate(ctx, rt, input, "deals", "deal_id")
}

func hubSpotDeleteObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	supported := false
	for _, candidate := range hubSpotCRMTypes {
		if candidate == objectType {
			supported = true
			break
		}
	}
	if !supported {
		return map[string]any{
			"error": fmt.Sprintf("Unsupported object_type: '%s'. Use contacts, companies, or deals.", objectType),
		}, nil
	}
	objectID := stringArg(input, "object_id")
	if objectID == "" {
		return map[string]any{"error": "object_id is required"}, nil
	}
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	resp, raw, failure := client.send(http.MethodDelete, "/crm/v3/objects/"+objectType+"/"+objectID, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if resp.StatusCode == http.StatusNoContent {
		return map[string]any{
			"status":      "deleted",
			"object_type": objectType,
			"object_id":   objectID,
		}, nil
	}
	value, failure := hubSpotHandle(resp, raw)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

func hubSpotListAssociations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	fromType := stringArg(input, "from_object_type")
	fromID := stringArg(input, "from_object_id")
	toType := stringArg(input, "to_object_type")
	if fromType == "" || fromID == "" || toType == "" {
		return map[string]any{"error": "from_object_type, from_object_id and to_object_type are required"}, nil
	}
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	limit := intArg(input, "limit", 100)
	if limit > hubSpotMaxAssoc {
		limit = hubSpotMaxAssoc
	}
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	path := "/crm/v4/objects/" + fromType + "/" + fromID + "/associations/" + toType
	value, failure := client.call(http.MethodGet, path, query, nil)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

func hubSpotCreateAssociation(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	fromType := stringArg(input, "from_object_type")
	fromID := stringArg(input, "from_object_id")
	toType := stringArg(input, "to_object_type")
	toID := stringArg(input, "to_object_id")
	if fromType == "" || fromID == "" || toType == "" || toID == "" {
		return map[string]any{"error": "from_object_type, from_object_id, to_object_type and to_object_id are required"}, nil
	}
	client, failure := newHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	// Upstream always sends HUBSPOT_DEFINED and only exposes the type id.
	body := []any{map[string]any{
		"associationCategory": "HUBSPOT_DEFINED",
		"associationTypeId":   intArg(input, "association_type_id", 0),
	}}
	path := "/crm/v4/objects/" + fromType + "/" + fromID + "/associations/" + toType + "/" + toID
	value, failure := client.call(http.MethodPut, path, nil, body)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}
