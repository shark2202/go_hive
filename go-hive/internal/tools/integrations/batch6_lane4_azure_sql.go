// Azure SQL Database management API (upstream azure_sql_tool, 5 actions).
//
// Uses the Azure Resource Manager REST API with the upstream api-version
// 2023-08-01 and the same response projections.
package integrations

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const batch6Lane4AzureAPIVersion = "2023-08-01"

var batch6Lane4AzureSQLTool = &Tool{
	ID:          "azure_sql_tool",
	Name:        "azure_sql",
	DisplayName: "Azure SQL",
	Summary:     "Azure SQL Database management integration (5 actions).",
	Actions: []Action{
		{
			Name:    "azure_sql_list_servers",
			Summary: "List Azure SQL servers in the subscription or a resource group.",
			Source:  "azure_sql_tool/azure_sql_tool.py",
			Params: map[string]any{
				"resource_group": paramDefault("string", ""),
			},
		},
		{
			Name:     "azure_sql_get_server",
			Summary:  "Get details of a specific Azure SQL server.",
			Source:   "azure_sql_tool/azure_sql_tool.py",
			Required: []string{"resource_group", "server_name"},
			Params: map[string]any{
				"resource_group": param("string"),
				"server_name":    param("string"),
			},
		},
		{
			Name:     "azure_sql_list_databases",
			Summary:  "List databases on an Azure SQL server.",
			Source:   "azure_sql_tool/azure_sql_tool.py",
			Required: []string{"resource_group", "server_name"},
			Params: map[string]any{
				"resource_group": param("string"),
				"server_name":    param("string"),
			},
		},
		{
			Name:     "azure_sql_get_database",
			Summary:  "Get details of a specific Azure SQL database.",
			Source:   "azure_sql_tool/azure_sql_tool.py",
			Required: []string{"resource_group", "server_name", "database_name"},
			Params: map[string]any{
				"resource_group": param("string"),
				"server_name":    param("string"),
				"database_name":  param("string"),
			},
		},
		{
			Name:     "azure_sql_list_firewall_rules",
			Summary:  "List firewall rules for an Azure SQL server.",
			Source:   "azure_sql_tool/azure_sql_tool.py",
			Required: []string{"resource_group", "server_name"},
			Params: map[string]any{
				"resource_group": param("string"),
				"server_name":    param("string"),
			},
		},
	},
}

func batch6Lane4AzureSQLImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"azure_sql_list_servers":        batch6Lane4AzureSQLListServers,
		"azure_sql_get_server":          batch6Lane4AzureSQLGetServer,
		"azure_sql_list_databases":      batch6Lane4AzureSQLListDatabases,
		"azure_sql_get_database":        batch6Lane4AzureSQLGetDatabase,
		"azure_sql_list_firewall_rules": batch6Lane4AzureSQLListFirewallRules,
	}
}

func batch6Lane4AzureSQLConfig(rt *Runtime) (map[string]string, string, map[string]any) {
	token := batch6Lane4ModuleToken(rt, "azure_sql_tool")
	subscription := strings.TrimSpace(batch6Lane4Getenv(rt, "AZURE_SUBSCRIPTION_ID"))
	if token == "" || subscription == "" {
		return nil, "", map[string]any{
			"error": "AZURE_SQL_ACCESS_TOKEN and AZURE_SUBSCRIPTION_ID are required",
			"help":  "Set AZURE_SQL_ACCESS_TOKEN and AZURE_SUBSCRIPTION_ID environment variables",
		}
	}
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}, subscription, nil
}

// batch6Lane4AzureSQLGet performs one ARM GET with api-version.
func batch6Lane4AzureSQLGet(ctx context.Context, rt *Runtime, headers map[string]string, path string) map[string]any {
	base := batch6Lane4ModuleURL(rt, "azure_sql_tool")
	if base == "" {
		base = "https://management.azure.com"
	}
	params := url.Values{"api-version": []string{batch6Lane4AzureAPIVersion}}
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodGet, strings.TrimRight(base, "/")+path+"?"+params.Encode(), headers, nil, 30*time.Second)
	if err != nil {
		return batch6Lane4TransportError(ctx, "Azure SQL", err)
	}
	if resp.Status >= 400 {
		return batch6Lane4HTTPError(resp)
	}
	data, ok := batch6Lane4JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Azure SQL API error: invalid JSON response"}
	}
	return data
}

// batch6Lane4AzureServer extracts the same fields as upstream _extract_server.
func batch6Lane4AzureServer(raw any) map[string]any {
	server := batch4AsMap(raw)
	properties := batch4AsMap(server["properties"])
	return map[string]any{
		"id":          server["id"],
		"name":        server["name"],
		"location":    server["location"],
		"fqdn":        properties["fullyQualifiedDomainName"],
		"state":       properties["state"],
		"version":     properties["version"],
		"admin_login": properties["administratorLogin"],
	}
}

// batch6Lane4AzureDatabase extracts the same fields as upstream
// _extract_database.
func batch6Lane4AzureDatabase(raw any) map[string]any {
	database := batch4AsMap(raw)
	properties := batch4AsMap(database["properties"])
	sku := batch4AsMap(database["sku"])
	return map[string]any{
		"id":                        database["id"],
		"name":                      database["name"],
		"location":                  database["location"],
		"status":                    properties["status"],
		"sku_name":                  sku["name"],
		"sku_tier":                  sku["tier"],
		"max_size_bytes":            properties["maxSizeBytes"],
		"collation":                 properties["collation"],
		"creation_date":             properties["creationDate"],
		"current_service_objective": properties["currentServiceObjectiveName"],
		"zone_redundant":            properties["zoneRedundant"],
	}
}

func batch6Lane4AzureSQLListServers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := batch6Lane4AzureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	var path string
	if resourceGroup != "" {
		path = "/subscriptions/" + batch6Lane4PathEscape(subscription) +
			"/resourceGroups/" + batch6Lane4PathEscape(resourceGroup) +
			"/providers/Microsoft.Sql/servers"
	} else {
		path = "/subscriptions/" + batch6Lane4PathEscape(subscription) + "/providers/Microsoft.Sql/servers"
	}
	data := batch6Lane4AzureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	servers := batch4AsSlice(data["value"])
	if servers == nil {
		servers = []any{}
	}
	extracted := make([]any, 0, len(servers))
	for _, server := range servers {
		extracted = append(extracted, batch6Lane4AzureServer(server))
	}
	return map[string]any{"count": len(extracted), "servers": extracted}, nil
}

func batch6Lane4AzureSQLGetServer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := batch6Lane4AzureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	if resourceGroup == "" || serverName == "" {
		return map[string]any{"error": "resource_group and server_name are required"}, nil
	}
	path := "/subscriptions/" + batch6Lane4PathEscape(subscription) +
		"/resourceGroups/" + batch6Lane4PathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + batch6Lane4PathEscape(serverName)
	data := batch6Lane4AzureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return batch6Lane4AzureServer(data), nil
}

func batch6Lane4AzureSQLListDatabases(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := batch6Lane4AzureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	if resourceGroup == "" || serverName == "" {
		return map[string]any{"error": "resource_group and server_name are required"}, nil
	}
	path := "/subscriptions/" + batch6Lane4PathEscape(subscription) +
		"/resourceGroups/" + batch6Lane4PathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + batch6Lane4PathEscape(serverName) + "/databases"
	data := batch6Lane4AzureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	databases := batch4AsSlice(data["value"])
	if databases == nil {
		databases = []any{}
	}
	extracted := make([]any, 0, len(databases))
	for _, database := range databases {
		extracted = append(extracted, batch6Lane4AzureDatabase(database))
	}
	return map[string]any{"count": len(extracted), "databases": extracted}, nil
}

func batch6Lane4AzureSQLGetDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := batch6Lane4AzureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	databaseName := stringArg(input, "database_name")
	if resourceGroup == "" || serverName == "" || databaseName == "" {
		return map[string]any{"error": "resource_group, server_name, and database_name are required"}, nil
	}
	path := "/subscriptions/" + batch6Lane4PathEscape(subscription) +
		"/resourceGroups/" + batch6Lane4PathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + batch6Lane4PathEscape(serverName) +
		"/databases/" + batch6Lane4PathEscape(databaseName)
	data := batch6Lane4AzureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return batch6Lane4AzureDatabase(data), nil
}

func batch6Lane4AzureSQLListFirewallRules(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := batch6Lane4AzureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	if resourceGroup == "" || serverName == "" {
		return map[string]any{"error": "resource_group and server_name are required"}, nil
	}
	path := "/subscriptions/" + batch6Lane4PathEscape(subscription) +
		"/resourceGroups/" + batch6Lane4PathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + batch6Lane4PathEscape(serverName) + "/firewallRules"
	data := batch6Lane4AzureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	rules := batch4AsSlice(data["value"])
	if rules == nil {
		rules = []any{}
	}
	extracted := make([]any, 0, len(rules))
	for _, rawRule := range rules {
		rule := batch4AsMap(rawRule)
		properties := batch4AsMap(rule["properties"])
		extracted = append(extracted, map[string]any{
			"id":       rule["id"],
			"name":     rule["name"],
			"start_ip": properties["startIpAddress"],
			"end_ip":   properties["endIpAddress"],
		})
	}
	return map[string]any{"count": len(extracted), "firewall_rules": extracted}, nil
}
