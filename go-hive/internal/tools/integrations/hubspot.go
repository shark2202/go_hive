// HubSpot CRM integration (upstream hubspot_tool, 15 actions). Part of batch 5
// lane 4; see hubspot_obsidian.go for the registry and shared helpers.
//
// Every action maps to one upstream @mcp.tool function and one _HubSpotClient
// method. Search/get/create/update differ only in CRM object type, so the
// handlers are thin wrappers over shared helpers; error strings and status
// handling come from upstream _handle_response.
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

const (
	batch5HubSpotMaxBody  = 8 << 20
	batch5HubSpotMaxLimit = 100
	batch5HubSpotMaxAssoc = 500
)

// batch5HubSpotCRMTypes are the object types upstream delete validates.
var batch5HubSpotCRMTypes = []string{"contacts", "companies", "deals"}

var batch5HubSpotTool = &Tool{
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

func batch5HubSpotImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"hubspot_search_contacts":    batch5HubSpotSearchContacts,
		"hubspot_get_contact":        batch5HubSpotGetContact,
		"hubspot_create_contact":     batch5HubSpotCreateContact,
		"hubspot_update_contact":     batch5HubSpotUpdateContact,
		"hubspot_search_companies":   batch5HubSpotSearchCompanies,
		"hubspot_get_company":        batch5HubSpotGetCompany,
		"hubspot_create_company":     batch5HubSpotCreateCompany,
		"hubspot_update_company":     batch5HubSpotUpdateCompany,
		"hubspot_search_deals":       batch5HubSpotSearchDeals,
		"hubspot_get_deal":           batch5HubSpotGetDeal,
		"hubspot_create_deal":        batch5HubSpotCreateDeal,
		"hubspot_update_deal":        batch5HubSpotUpdateDeal,
		"hubspot_delete_object":      batch5HubSpotDeleteObject,
		"hubspot_list_associations":  batch5HubSpotListAssociations,
		"hubspot_create_association": batch5HubSpotCreateAssociation,
	}
}

func batch5HubSpotAuthError() map[string]any {
	return map[string]any{
		"error": "HubSpot credentials not configured",
		"help":  "Set HUBSPOT_ACCESS_TOKEN environment variable or configure via credential store",
	}
}

// batch5Lane4AccessToken resolves a lane-4 credential for the current call: an
// account alias wins over the module token (upstream `_get_token(account)` ->
// credentials.get_by_alias(provider, account)); without an alias the module
// token is refreshed when its OAuth2 access token is near expiry.
func batch5Lane4AccessToken(ctx context.Context, rt *Runtime, moduleID, provider string) (string, bool, map[string]any) {
	if token, handled := rt.ResolveAccountToken(provider); handled {
		return token, false, nil
	}
	return oauthModuleToken(ctx, rt, moduleID)
}

// batch5HubSpotClient mirrors upstream _HubSpotClient: one token and base URL
// bound to a single action invocation.
type batch5HubSpotClient struct {
	ctx         context.Context
	rt          *Runtime
	base        string
	token       string
	refreshable bool
}

func batch5NewHubSpotClient(ctx context.Context, rt *Runtime) (*batch5HubSpotClient, map[string]any) {
	token, refreshable, problem := batch5Lane4AccessToken(ctx, rt, "hubspot_tool", "hubspot")
	if problem != nil {
		return nil, problem
	}
	if token == "" {
		return nil, batch5HubSpotAuthError()
	}
	return &batch5HubSpotClient{
		ctx:         ctx,
		rt:          rt,
		base:        batch5Lane4ModuleURL(rt, "hubspot_tool", "https://api.hubapi.com"),
		token:       token,
		refreshable: refreshable,
	}, nil
}

// send performs one request and returns the response metadata plus the body.
// Transport and request-construction failures become error dicts. A 401 with a
// refreshable token is retried once with a freshly minted access token.
func (c *batch5HubSpotClient) send(method, path string, query url.Values, payload any) (*http.Response, []byte, map[string]any) {
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
func (c *batch5HubSpotClient) attempt(method, path string, query url.Values, payload any) (*http.Response, []byte, map[string]any) {
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

	resp, err := batch4HTTP(c.rt).Do(req)
	if err != nil {
		if batch4IsTimeout(c.ctx, err) {
			return nil, nil, map[string]any{"error": "Request timed out"}
		}
		return nil, nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch5HubSpotMaxBody))
	if err != nil {
		return resp, nil, map[string]any{"error": "HubSpot request failed: " + err.Error()}
	}
	return resp, raw, nil
}

// handle mirrors upstream _handle_response: known status codes get fixed
// messages, other 4xx/5xx carry the provider's message, and success returns the
// decoded JSON body.
func batch5HubSpotHandle(resp *http.Response, raw []byte) (any, map[string]any) {
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
func (c *batch5HubSpotClient) call(method, path string, query url.Values, payload any) (any, map[string]any) {
	resp, raw, failure := c.send(method, path, query, payload)
	if failure != nil {
		return nil, failure
	}
	return batch5HubSpotHandle(resp, raw)
}

// batch5HubSpotProperties reads the required `properties` object.
func batch5HubSpotProperties(input map[string]any) (map[string]any, map[string]any) {
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

// batch5HubSpotSearch implements search_objects for one CRM object type.
func batch5HubSpotSearch(ctx context.Context, rt *Runtime, input map[string]any, objectType string, defaultProperties []string) (any, error) {
	client, failure := batch5NewHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	properties := stringSliceArg(input, "properties")
	if len(properties) == 0 {
		properties = defaultProperties
	}
	limit := intArg(input, "limit", 10)
	if limit > batch5HubSpotMaxLimit {
		limit = batch5HubSpotMaxLimit
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

// batch5HubSpotGet implements get_object for one CRM object type.
func batch5HubSpotGet(ctx context.Context, rt *Runtime, input map[string]any, objectType, idParam string) (any, error) {
	objectID := stringArg(input, idParam)
	if objectID == "" {
		return map[string]any{"error": idParam + " is required"}, nil
	}
	client, failure := batch5NewHubSpotClient(ctx, rt)
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

// batch5HubSpotCreate implements create_object for one CRM object type.
func batch5HubSpotCreate(ctx context.Context, rt *Runtime, input map[string]any, objectType string) (any, error) {
	properties, failure := batch5HubSpotProperties(input)
	if failure != nil {
		return failure, nil
	}
	client, failure := batch5NewHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	value, failure := client.call(http.MethodPost, "/crm/v3/objects/"+objectType, nil, map[string]any{"properties": properties})
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

// batch5HubSpotUpdate implements update_object for one CRM object type.
func batch5HubSpotUpdate(ctx context.Context, rt *Runtime, input map[string]any, objectType, idParam string) (any, error) {
	objectID := stringArg(input, idParam)
	if objectID == "" {
		return map[string]any{"error": idParam + " is required"}, nil
	}
	properties, failure := batch5HubSpotProperties(input)
	if failure != nil {
		return failure, nil
	}
	client, failure := batch5NewHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	value, failure := client.call(http.MethodPatch, "/crm/v3/objects/"+objectType+"/"+objectID, nil, map[string]any{"properties": properties})
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

func batch5HubSpotSearchContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotSearch(ctx, rt, input, "contacts", []string{"email", "firstname", "lastname"})
}

func batch5HubSpotGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotGet(ctx, rt, input, "contacts", "contact_id")
}

func batch5HubSpotCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotCreate(ctx, rt, input, "contacts")
}

func batch5HubSpotUpdateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotUpdate(ctx, rt, input, "contacts", "contact_id")
}

func batch5HubSpotSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotSearch(ctx, rt, input, "companies", []string{"name", "domain", "industry"})
}

func batch5HubSpotGetCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotGet(ctx, rt, input, "companies", "company_id")
}

func batch5HubSpotCreateCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotCreate(ctx, rt, input, "companies")
}

func batch5HubSpotUpdateCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotUpdate(ctx, rt, input, "companies", "company_id")
}

func batch5HubSpotSearchDeals(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotSearch(ctx, rt, input, "deals", []string{"dealname", "amount", "dealstage"})
}

func batch5HubSpotGetDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotGet(ctx, rt, input, "deals", "deal_id")
}

func batch5HubSpotCreateDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotCreate(ctx, rt, input, "deals")
}

func batch5HubSpotUpdateDeal(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5HubSpotUpdate(ctx, rt, input, "deals", "deal_id")
}

func batch5HubSpotDeleteObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	objectType := stringArg(input, "object_type")
	supported := false
	for _, candidate := range batch5HubSpotCRMTypes {
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
	client, failure := batch5NewHubSpotClient(ctx, rt)
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
	value, failure := batch5HubSpotHandle(resp, raw)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

func batch5HubSpotListAssociations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	fromType := stringArg(input, "from_object_type")
	fromID := stringArg(input, "from_object_id")
	toType := stringArg(input, "to_object_type")
	if fromType == "" || fromID == "" || toType == "" {
		return map[string]any{"error": "from_object_type, from_object_id and to_object_type are required"}, nil
	}
	client, failure := batch5NewHubSpotClient(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	limit := intArg(input, "limit", 100)
	if limit > batch5HubSpotMaxAssoc {
		limit = batch5HubSpotMaxAssoc
	}
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	path := "/crm/v4/objects/" + fromType + "/" + fromID + "/associations/" + toType
	value, failure := client.call(http.MethodGet, path, query, nil)
	if failure != nil {
		return failure, nil
	}
	return value, nil
}

func batch5HubSpotCreateAssociation(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	fromType := stringArg(input, "from_object_type")
	fromID := stringArg(input, "from_object_id")
	toType := stringArg(input, "to_object_type")
	toID := stringArg(input, "to_object_id")
	if fromType == "" || fromID == "" || toType == "" || toID == "" {
		return map[string]any{"error": "from_object_type, from_object_id, to_object_type and to_object_id are required"}, nil
	}
	client, failure := batch5NewHubSpotClient(ctx, rt)
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
