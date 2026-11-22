// Azure SQL Database management API (upstream azure_sql_tool, 5 actions).
//
// Uses the Azure Resource Manager REST API with the upstream api-version
// 2023-08-01 and the same response projections.
package data

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const azureAPIVersion = "2023-08-01"

var azureSQLTool = &Tool{
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

func azureSQLImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"azure_sql_list_servers":        azureSQLListServers,
		"azure_sql_get_server":          azureSQLGetServer,
		"azure_sql_list_databases":      azureSQLListDatabases,
		"azure_sql_get_database":        azureSQLGetDatabase,
		"azure_sql_list_firewall_rules": azureSQLListFirewallRules,
	}
}

func azureSQLConfig(rt *Runtime) (map[string]string, string, map[string]any) {
	token := moduleToken(rt, "azure_sql_tool")
	subscription := strings.TrimSpace(runtimeEnv(rt, "AZURE_SUBSCRIPTION_ID"))
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

// azureSQLGet performs one ARM GET with api-version.
func azureSQLGet(ctx context.Context, rt *Runtime, headers map[string]string, path string) map[string]any {
	base := moduleBaseURL(rt, "azure_sql_tool")
	if base == "" {
		base = "https://management.azure.com"
	}
	params := url.Values{"api-version": []string{azureAPIVersion}}
	resp, err := doJSON(ctx, rt, http.MethodGet, strings.TrimRight(base, "/")+path+"?"+params.Encode(), headers, nil, 30*time.Second)
	if err != nil {
		return transportError(ctx, "Azure SQL", err)
	}
	if resp.Status >= 400 {
		return httpError(resp)
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Azure SQL API error: invalid JSON response"}
	}
	return data
}

// azureServer extracts the same fields as upstream _extract_server.
func azureServer(raw any) map[string]any {
	server := asMap(raw)
	properties := asMap(server["properties"])
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

// azureDatabase extracts the same fields as upstream
// _extract_database.
func azureDatabase(raw any) map[string]any {
	database := asMap(raw)
	properties := asMap(database["properties"])
	sku := asMap(database["sku"])
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

func azureSQLListServers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := azureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	var path string
	if resourceGroup != "" {
		path = "/subscriptions/" + pathEscape(subscription) +
			"/resourceGroups/" + pathEscape(resourceGroup) +
			"/providers/Microsoft.Sql/servers"
	} else {
		path = "/subscriptions/" + pathEscape(subscription) + "/providers/Microsoft.Sql/servers"
	}
	data := azureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	servers := asSlice(data["value"])
	if servers == nil {
		servers = []any{}
	}
	extracted := make([]any, 0, len(servers))
	for _, server := range servers {
		extracted = append(extracted, azureServer(server))
	}
	return map[string]any{"count": len(extracted), "servers": extracted}, nil
}

func azureSQLGetServer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := azureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	if resourceGroup == "" || serverName == "" {
		return map[string]any{"error": "resource_group and server_name are required"}, nil
	}
	path := "/subscriptions/" + pathEscape(subscription) +
		"/resourceGroups/" + pathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + pathEscape(serverName)
	data := azureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return azureServer(data), nil
}

func azureSQLListDatabases(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := azureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	if resourceGroup == "" || serverName == "" {
		return map[string]any{"error": "resource_group and server_name are required"}, nil
	}
	path := "/subscriptions/" + pathEscape(subscription) +
		"/resourceGroups/" + pathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + pathEscape(serverName) + "/databases"
	data := azureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	databases := asSlice(data["value"])
	if databases == nil {
		databases = []any{}
	}
	extracted := make([]any, 0, len(databases))
	for _, database := range databases {
		extracted = append(extracted, azureDatabase(database))
	}
	return map[string]any{"count": len(extracted), "databases": extracted}, nil
}

func azureSQLGetDatabase(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := azureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	databaseName := stringArg(input, "database_name")
	if resourceGroup == "" || serverName == "" || databaseName == "" {
		return map[string]any{"error": "resource_group, server_name, and database_name are required"}, nil
	}
	path := "/subscriptions/" + pathEscape(subscription) +
		"/resourceGroups/" + pathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + pathEscape(serverName) +
		"/databases/" + pathEscape(databaseName)
	data := azureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return azureDatabase(data), nil
}

func azureSQLListFirewallRules(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	headers, subscription, failure := azureSQLConfig(rt)
	if failure != nil {
		return failure, nil
	}
	resourceGroup := stringArg(input, "resource_group")
	serverName := stringArg(input, "server_name")
	if resourceGroup == "" || serverName == "" {
		return map[string]any{"error": "resource_group and server_name are required"}, nil
	}
	path := "/subscriptions/" + pathEscape(subscription) +
		"/resourceGroups/" + pathEscape(resourceGroup) +
		"/providers/Microsoft.Sql/servers/" + pathEscape(serverName) + "/firewallRules"
	data := azureSQLGet(ctx, rt, headers, path)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	rules := asSlice(data["value"])
	if rules == nil {
		rules = []any{}
	}
	extracted := make([]any, 0, len(rules))
	for _, rawRule := range rules {
		rule := asMap(rawRule)
		properties := asMap(rule["properties"])
		extracted = append(extracted, map[string]any{
			"id":       rule["id"],
			"name":     rule["name"],
			"start_ip": properties["startIpAddress"],
			"end_ip":   properties["endIpAddress"],
		})
	}
	return map[string]any{"count": len(extracted), "firewall_rules": extracted}, nil
}
