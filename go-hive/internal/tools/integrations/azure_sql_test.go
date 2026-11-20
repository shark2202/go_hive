package integrations

import (
	"net/http"
	"strings"
	"testing"
)

var batch6Lane4AzureEnv = map[string]string{
	"AZURE_SUBSCRIPTION_ID": "sub-123",
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLListServers::test_missing_credentials
func TestBatch6Lane4AzureSQLMissingCredentials(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{"action": "azure_sql_list_servers"})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "AZURE_SQL_ACCESS_TOKEN") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLListServers::test_successful_list
func TestBatch6Lane4AzureSQLListServers(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "azure_sql_tool", batch6Lane4AzureEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet {
				t.Fatalf("method = %s", r.Method)
			}
			if r.URL.Path != "/subscriptions/sub-123/providers/Microsoft.Sql/servers" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if got := r.URL.Query().Get("api-version"); got != "2023-08-01" {
				t.Fatalf("api-version = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"id":       "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Sql/servers/myserver",
					"name":     "myserver",
					"location": "eastus",
					"properties": map[string]any{
						"fullyQualifiedDomainName": "myserver.database.windows.net",
						"state":                    "Ready",
						"version":                  "12.0",
						"administratorLogin":       "adminuser",
					},
				},
			}})
		}, WithCredential("azure_sql_tool", "test-token"))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{"action": "azure_sql_list_servers"})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	server := batch4BodyMap(t, batch4BodySlice(t, result["servers"])[0])
	if server["name"] != "myserver" || server["fqdn"] != "myserver.database.windows.net" || server["state"] != "Ready" {
		t.Fatalf("server = %v", server)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLGetServer::test_missing_params
func TestBatch6Lane4AzureSQLGetServerMissingParams(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "azure_sql_tool", batch6Lane4AzureEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		}, WithCredential("azure_sql_tool", "test-token"))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{
		"action": "azure_sql_get_server", "resource_group": "", "server_name": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLGetServer::test_successful_get
func TestBatch6Lane4AzureSQLGetServer(t *testing.T) {
	registry, recorder := batch6Lane4TestRegistry(t, "azure_sql_tool", batch6Lane4AzureEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Sql/servers/myserver" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id":       "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Sql/servers/myserver",
				"name":     "myserver",
				"location": "eastus",
				"properties": map[string]any{
					"fullyQualifiedDomainName": "myserver.database.windows.net",
					"state":                    "Ready",
					"administratorLogin":       "adminuser",
				},
			})
		}, WithCredential("azure_sql_tool", "test-token"))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{
		"action": "azure_sql_get_server", "resource_group": "rg", "server_name": "myserver",
	})
	if result["name"] != "myserver" || result["state"] != "Ready" {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Query.Get("api-version") != "2023-08-01" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLListDatabases::test_successful_list
func TestBatch6Lane4AzureSQLListDatabases(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "azure_sql_tool", batch6Lane4AzureEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Sql/servers/myserver/databases" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"id":       "/subscriptions/sub-123/.../databases/mydb",
					"name":     "mydb",
					"location": "eastus",
					"sku":      map[string]any{"name": "S0", "tier": "Standard"},
					"properties": map[string]any{
						"status":                      "Online",
						"maxSizeBytes":                float64(268435456000),
						"collation":                   "SQL_Latin1_General_CP1_CI_AS",
						"creationDate":                "2024-01-15T10:30:00Z",
						"currentServiceObjectiveName": "S0",
						"zoneRedundant":               false,
					},
				},
			}})
		}, WithCredential("azure_sql_tool", "test-token"))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{
		"action": "azure_sql_list_databases", "resource_group": "rg", "server_name": "myserver",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	database := batch4BodyMap(t, batch4BodySlice(t, result["databases"])[0])
	if database["name"] != "mydb" || database["status"] != "Online" || database["sku_tier"] != "Standard" {
		t.Fatalf("database = %v", database)
	}
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLGetDatabase::test_successful_get
func TestBatch6Lane4AzureSQLGetDatabase(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "azure_sql_tool", batch6Lane4AzureEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Sql/servers/myserver/databases/mydb" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"name":     "mydb",
				"location": "eastus",
				"sku":      map[string]any{"name": "GP_S_Gen5_2", "tier": "GeneralPurpose"},
				"properties": map[string]any{
					"status":                      "Online",
					"maxSizeBytes":                float64(34359738368),
					"collation":                   "SQL_Latin1_General_CP1_CI_AS",
					"creationDate":                "2024-01-15T10:30:00Z",
					"currentServiceObjectiveName": "GP_S_Gen5_2",
					"zoneRedundant":               true,
				},
			})
		}, WithCredential("azure_sql_tool", "test-token"))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{
		"action": "azure_sql_get_database", "resource_group": "rg", "server_name": "myserver", "database_name": "mydb",
	})
	if result["name"] != "mydb" || result["zone_redundant"] != true || result["sku_name"] != "GP_S_Gen5_2" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_azure_sql_tool.py::TestAzureSQLListFirewallRules::test_successful_list
func TestBatch6Lane4AzureSQLListFirewallRules(t *testing.T) {
	registry, _ := batch6Lane4TestRegistry(t, "azure_sql_tool", batch6Lane4AzureEnv,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/subscriptions/sub-123/resourceGroups/rg/providers/Microsoft.Sql/servers/myserver/firewallRules" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"id":   "/subscriptions/sub-123/.../firewallRules/AllowAll",
					"name": "AllowAll",
					"properties": map[string]any{
						"startIpAddress": "0.0.0.0",
						"endIpAddress":   "255.255.255.255",
					},
				},
			}})
		}, WithCredential("azure_sql_tool", "test-token"))
	result := batch4Execute(t, registry, "azure_sql", map[string]any{
		"action": "azure_sql_list_firewall_rules", "resource_group": "rg", "server_name": "myserver",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	rule := batch4BodyMap(t, batch4BodySlice(t, result["firewall_rules"])[0])
	if rule["name"] != "AllowAll" || rule["start_ip"] != "0.0.0.0" || rule["end_ip"] != "255.255.255.255" {
		t.Fatalf("rule = %v", rule)
	}
}
