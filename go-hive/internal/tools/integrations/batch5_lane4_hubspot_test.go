package integrations

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotSearchContacts::test_success
func TestBatch5HubSpotSearchContacts(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{
			"total":   1,
			"results": []any{map[string]any{"id": "1", "properties": map[string]any{"email": "jane@example.com"}}},
		})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane",
	})
	if result["total"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/crm/v3/objects/contacts/search")
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	body := request.JSON(t)
	if body["limit"] != float64(10) || body["query"] != "jane" {
		t.Fatalf("body = %v", body)
	}
	properties, _ := body["properties"].([]any)
	if len(properties) != 3 || properties[0] != "email" || properties[2] != "lastname" {
		t.Fatalf("properties = %v", body["properties"])
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotClientSearchObjects::test_search_clamps_limit_to_100
func TestBatch5HubSpotSearchClampsLimitAndOmitsEmptyQuery(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 0, "results": []any{}})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "limit": 200,
		"properties": []any{"email", "phone"},
	})
	if result["total"] != float64(0) {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).JSON(t)
	if body["limit"] != float64(100) {
		t.Fatalf("limit = %v", body["limit"])
	}
	if _, present := body["query"]; present {
		t.Fatalf("query should be omitted: %v", body)
	}
	properties, _ := body["properties"].([]any)
	if len(properties) != 2 || properties[1] != "phone" {
		t.Fatalf("properties = %v", body["properties"])
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotSearchCompanies::test_success
func TestBatch5HubSpotSearchCompanies(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 2, "results": []any{}})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{"action": "hubspot_search_companies", "query": "acme"})
	if result["total"] != float64(2) {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/crm/v3/objects/companies/search")
	properties, _ := request.JSON(t)["properties"].([]any)
	if len(properties) != 3 || properties[0] != "name" || properties[2] != "industry" {
		t.Fatalf("properties = %v", properties)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotSearchDeals::test_success
func TestBatch5HubSpotSearchDeals(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 3, "results": []any{}})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{"action": "hubspot_search_deals", "query": "big deal"})
	if result["total"] != float64(3) {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/crm/v3/objects/deals/search")
	properties, _ := request.JSON(t)["properties"].([]any)
	if len(properties) != 3 || properties[0] != "dealname" || properties[1] != "amount" {
		t.Fatalf("properties = %v", properties)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotGetContact::test_success
func TestBatch5HubSpotGetContact(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "123", "properties": map[string]any{"email": "test@example.com"}})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_get_contact", "contact_id": "123",
		"properties": []any{"email", "firstname"},
	})
	if result["id"] != "123" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodGet, "/crm/v3/objects/contacts/123")
	if got := request.Query.Get("properties"); got != "email,firstname" {
		t.Fatalf("properties query = %q", got)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotClientGetObject::test_get_object_url
func TestBatch5HubSpotGetContactWithoutProperties(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "123"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_get_contact", "contact_id": "123",
	}); result["id"] != "123" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodGet, "/crm/v3/objects/contacts/123")
	if _, present := request.Query["properties"]; present {
		t.Fatalf("properties query should be absent: %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotGetCompany::test_success
func TestBatch5HubSpotGetCompany(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "10"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_get_company", "company_id": "10",
	}); result["id"] != "10" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane4GetRequest(t, recorder, http.MethodGet, "/crm/v3/objects/companies/10")
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotGetDeal::test_success
func TestBatch5HubSpotGetDeal(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "20"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_get_deal", "deal_id": "20",
	}); result["id"] != "20" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane4GetRequest(t, recorder, http.MethodGet, "/crm/v3/objects/deals/20")
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotCreateContact::test_success
func TestBatch5HubSpotCreateContact(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusCreated, map[string]any{"id": "456"})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action":     "hubspot_create_contact",
		"properties": map[string]any{"email": "new@example.com", "firstname": "Jane"},
	})
	if result["id"] != "456" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/crm/v3/objects/contacts")
	properties, _ := request.JSON(t)["properties"].(map[string]any)
	if properties["email"] != "new@example.com" || properties["firstname"] != "Jane" {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotCreateCompany::test_success
func TestBatch5HubSpotCreateCompany(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusCreated, map[string]any{"id": "11"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_create_company", "properties": map[string]any{"name": "Acme"},
	}); result["id"] != "11" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/crm/v3/objects/companies")
	if properties, _ := request.JSON(t)["properties"].(map[string]any); properties["name"] != "Acme" {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotCreateDeal::test_success
func TestBatch5HubSpotCreateDeal(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusCreated, map[string]any{"id": "21"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_create_deal", "properties": map[string]any{"dealname": "New Deal"},
	}); result["id"] != "21" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/crm/v3/objects/deals")
	if properties, _ := request.JSON(t)["properties"].(map[string]any); properties["dealname"] != "New Deal" {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotUpdateContact::test_success
func TestBatch5HubSpotUpdateContact(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "123"})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_update_contact", "contact_id": "123",
		"properties": map[string]any{"phone": "+1234567890"},
	})
	if result["id"] != "123" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPatch, "/crm/v3/objects/contacts/123")
	if properties, _ := request.JSON(t)["properties"].(map[string]any); properties["phone"] != "+1234567890" {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotUpdateCompany::test_success
func TestBatch5HubSpotUpdateCompany(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "10"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_update_company", "company_id": "10",
		"properties": map[string]any{"industry": "Tech"},
	}); result["id"] != "10" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPatch, "/crm/v3/objects/companies/10")
	if properties, _ := request.JSON(t)["properties"].(map[string]any); properties["industry"] != "Tech" {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotUpdateDeal::test_success
func TestBatch5HubSpotUpdateDeal(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"id": "20"})
	})
	if result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_update_deal", "deal_id": "20",
		"properties": map[string]any{"amount": "5000"},
	}); result["id"] != "20" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPatch, "/crm/v3/objects/deals/20")
	if properties, _ := request.JSON(t)["properties"].(map[string]any); properties["amount"] != "5000" {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotDeleteObject::test_success
func TestBatch5HubSpotDeleteObject(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_delete_object", "object_type": "contacts", "object_id": "42",
	})
	if result["status"] != "deleted" || result["object_type"] != "contacts" || result["object_id"] != "42" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane4GetRequest(t, recorder, http.MethodDelete, "/crm/v3/objects/contacts/42")
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotDeleteObject::test_invalid_object_type
func TestBatch5HubSpotDeleteObjectRejectsInvalidType(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusNoContent, map[string]any{})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_delete_object", "object_type": "tickets", "object_id": "1",
	})
	message := batch5Lane4ErrorText(t, result)
	if !strings.Contains(message, "Unsupported object_type") || !strings.Contains(message, "'tickets'") {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotClientDeleteObject::test_delete_non_204_delegates_to_handle_response
func TestBatch5HubSpotDeleteObjectNotFound(t *testing.T) {
	registry, _ := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusNotFound, map[string]any{"message": "Not found"})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_delete_object", "object_type": "deals", "object_id": "7",
	})
	if message := batch5Lane4ErrorText(t, result); message != "Resource not found" {
		t.Fatalf("error = %q", message)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotListAssociations::test_success
func TestBatch5HubSpotListAssociations(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"results": []any{map[string]any{"toObjectId": "c1"}}})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_list_associations", "from_object_type": "contacts",
		"from_object_id": "1", "to_object_type": "companies",
	})
	results, _ := result["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodGet, "/crm/v4/objects/contacts/1/associations/companies")
	if got := request.Query.Get("limit"); got != "100" {
		t.Fatalf("limit = %q", got)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotClientAssociations::test_list_associations_clamps_limit
func TestBatch5HubSpotListAssociationsClampsLimit(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"results": []any{}})
	})
	batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_list_associations", "from_object_type": "contacts",
		"from_object_id": "1", "to_object_type": "companies", "limit": 999,
	})
	if got := recorder.last(t).Query.Get("limit"); got != "500" {
		t.Fatalf("limit = %q", got)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotCreateAssociation::test_success
func TestBatch5HubSpotCreateAssociation(t *testing.T) {
	registry, recorder := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_create_association", "from_object_type": "contacts",
		"from_object_id": "1", "to_object_type": "companies", "to_object_id": "2",
		"association_type_id": 3,
	})
	if result["status"] != "ok" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPut, "/crm/v4/objects/contacts/1/associations/companies/2")
	body, _ := request.JSONValue(t).([]any)
	if len(body) != 1 {
		t.Fatalf("body = %s", request.text())
	}
	association, _ := body[0].(map[string]any)
	if association["associationCategory"] != "HUBSPOT_DEFINED" || association["associationTypeId"] != float64(3) {
		t.Fatalf("body = %s", request.text())
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotClientHandleResponse::test_known_error_codes
func TestBatch5HubSpotHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid or expired HubSpot access token"},
		{http.StatusForbidden, "Insufficient permissions"},
		{http.StatusNotFound, "Resource not found"},
		{http.StatusTooManyRequests, "rate limit exceeded"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			registry, _ := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
				batch5Lane4JSON(w, tc.status, map[string]any{"message": "failure"})
			})
			result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
				"action": "hubspot_search_contacts", "query": "jane",
			})
			if message := batch5Lane4ErrorText(t, result); !strings.Contains(message, tc.fragment) {
				t.Fatalf("error = %q, want %q", message, tc.fragment)
			}
		})
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotClientHandleResponse::test_generic_4xx_with_json_message
func TestBatch5HubSpotGenericErrorUsesProviderMessage(t *testing.T) {
	registry, _ := batch5Lane4HubSpotRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusInternalServerError, map[string]any{"message": "Internal Server Error"})
	})
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane",
	})
	message := batch5Lane4ErrorText(t, result)
	if !strings.Contains(message, "HubSpot API error (HTTP 500)") || !strings.Contains(message, "Internal Server Error") {
		t.Fatalf("error = %q", message)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotCredentials::test_no_credentials_returns_error
func TestBatch5HubSpotMissingCredentials(t *testing.T) {
	registry := newBatch5Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane",
	})
	message := batch5Lane4ErrorText(t, result)
	if !strings.Contains(message, "not configured") {
		t.Fatalf("error = %q", message)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotCredentials::test_env_var_credential
func TestBatch5HubSpotCredentialsFromEnvironment(t *testing.T) {
	recorder := &batch5Lane4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 0, "results": []any{}})
	}))
	t.Cleanup(server.Close)
	registry := newBatch5Lane4Registry(
		WithBaseURL("hubspot_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string {
			if name == "HUBSPOT_ACCESS_TOKEN" {
				return "env-token"
			}
			return ""
		}),
	)
	batch5Lane4Run(t, registry, "hubspot", map[string]any{"action": "hubspot_search_contacts", "query": "test"})
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer env-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotSearchContacts::test_timeout
func TestBatch5HubSpotTimeout(t *testing.T) {
	registry := newBatch5Lane4Registry(
		WithCredential("hubspot_tool", "test-token"),
		WithHTTPClient(&http.Client{Transport: batch5Lane4TimeoutTransport{}}),
	)
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane",
	})
	if message := batch5Lane4ErrorText(t, result); !strings.Contains(message, "timed out") {
		t.Fatalf("error = %q", message)
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::TestHubSpotSearchContacts::test_network_error
func TestBatch5HubSpotNetworkError(t *testing.T) {
	registry := newBatch5Lane4Registry(
		WithCredential("hubspot_tool", "test-token"),
		WithHTTPClient(&http.Client{Transport: batch5Lane4NetworkTransport{}}),
	)
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane",
	})
	message := batch5Lane4ErrorText(t, result)
	if !strings.Contains(message, "Network error") || !strings.Contains(message, "connection failed") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: Go-side catalogue/install contract; upstream tests assert the
// MCP tool count, not a registry surface.
func TestBatch5HubSpotCatalogue(t *testing.T) {
	registry := newBatch5Lane4Registry()
	if got := len(registry.Names()); got != 2 {
		t.Fatalf("tools = %d, want 2", got)
	}
	if got := registry.ActionCount(); got != 21 {
		t.Fatalf("actions = %d, want 21", got)
	}
	if got := registry.ImplementedActions(); got != 21 {
		t.Fatalf("implemented = %d, want 21", got)
	}
	names := batch5Lane4ActionNames(registry, "hubspot")
	if len(names) != 15 {
		t.Fatalf("hubspot actions = %d, want 15", len(names))
	}
	for _, action := range []string{
		"hubspot_search_contacts", "hubspot_get_contact", "hubspot_create_contact",
		"hubspot_update_contact", "hubspot_search_companies", "hubspot_get_company",
		"hubspot_create_company", "hubspot_update_company", "hubspot_search_deals",
		"hubspot_get_deal", "hubspot_create_deal", "hubspot_update_deal",
		"hubspot_delete_object", "hubspot_list_associations", "hubspot_create_association",
	} {
		if !batch5Lane4HasAction(names, action) {
			t.Fatalf("missing action %s", action)
		}
	}
	installed, shared := batch5Lane4InstallCount(t, registry)
	if installed != 2 {
		t.Fatalf("installed = %d, want 2", installed)
	}
	tool, ok := shared.GetTool("hubspot")
	if !ok {
		t.Fatal("hubspot not installed")
	}
	schema, _ := tool.Parameters["properties"].(map[string]any)
	action, _ := schema["action"].(map[string]any)
	enum, _ := action["enum"].([]string)
	if len(enum) != 15 {
		t.Fatalf("schema enum = %v", action["enum"])
	}
}
