package integrations

// Domain assembly entry points for the families that moved out in the
// int-split-p3 lane (gworkspace, data, collab, crm, commerce). Each thin
// facade keeps the historical InstallBatch*/newBatch*Registry names used by
// the CLI and by the cross-family test suites, and forwards to the domain
// subpackage so the CLI assembly is unchanged. The files were merged into one
// assembly file to keep the root package's file budget (P3 target: <100 .go
// files) with a single discoverable seam.

import (
	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrations/collab"
	"github.com/aden-hive/hive-go/internal/tools/integrations/commerce"
	"github.com/aden-hive/hive-go/internal/tools/integrations/crm"
	"github.com/aden-hive/hive-go/internal/tools/integrations/data"
	"github.com/aden-hive/hive-go/internal/tools/integrations/gworkspace"
)

// The Google Workspace family lives in its domain subpackage
// (internal/tools/integrations/gworkspace); this thin entry point keeps the
// historical assembly name used by the CLI and by the cross-family test
// suites.

// InstallBatch5Lane3 publishes the batch-5 lane-3 Google Workspace tools
// (google_sheets, google_docs, gmail, calendar) into a shared registry and
// returns the tool count.
func InstallBatch5Lane3(target *tools.ToolRegistry) int {
	return gworkspace.NewRegistry().Install(target)
}

// newBatch5Lane3Registry builds a registry containing only the lane-3 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch5Lane3Registry(opts ...Option) *Registry {
	return gworkspace.NewRegistry(opts...)
}

// The database / data-platform family lives in its domain subpackage
// (internal/tools/integrations/data); these thin entry points keep the
// historical assembly names used by the CLI and by the cross-family test
// suites.

// InstallBatch5Lane2 publishes the batch-5 lane-2 database tools (postgres,
// redis, mongodb, supabase) into a shared registry and returns the tool count.
func InstallBatch5Lane2(target *tools.ToolRegistry) int {
	return data.NewDatabasesRegistry().Install(target)
}

// newBatch5Lane2Registry builds a registry containing only the lane-2 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch5Lane2Registry(opts ...Option) *Registry {
	return data.NewDatabasesRegistry(opts...)
}

// InstallBatch6Lane4 publishes the batch-6 lane-4 data-platform tools
// (snowflake, bigquery, databricks, redshift, azure_sql, powerbi, sap) into a
// shared registry and returns the number of tools registered.
func InstallBatch6Lane4(target *tools.ToolRegistry) int {
	return data.NewPlatformRegistry().Install(target)
}

// newBatch6Lane4Registry builds a registry containing only the lane-4 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane4Registry(opts ...Option) *Registry {
	return data.NewPlatformRegistry(opts...)
}

// The collaboration family lives in its domain subpackage
// (internal/tools/integrations/collab); these thin entry points keep the
// historical assembly names used by the CLI and by the cross-family test
// suites.

// InstallBatch5Lane1 publishes the batch-5 lane-1 collaboration tools (jira,
// confluence, asana) into a shared registry and returns the number of tools
// registered.
func InstallBatch5Lane1(target *tools.ToolRegistry) int {
	return collab.NewAtlassianRegistry().Install(target)
}

// newBatch5Lane1Registry builds a registry containing only the lane-1 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch5Lane1Registry(opts ...Option) *Registry {
	return collab.NewAtlassianRegistry(opts...)
}

// InstallBatch4 publishes the batch-4 SaaS tools (notion and linear) into a
// shared registry and returns the number of tools registered.
func InstallBatch4(target *tools.ToolRegistry) int {
	return collab.NewNotionRegistry().Install(target)
}

// newBatch4Registry builds a registry containing only the batch-4 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch4Registry(opts ...Option) *Registry {
	return collab.NewNotionRegistry(opts...)
}

// The CRM / support family lives in its domain subpackage
// (internal/tools/integrations/crm); these thin entry points keep the
// historical assembly names used by the CLI and by the cross-family test
// suites.

// InstallBatch6Lane1 publishes the batch-6 lane-1 CRM tools (salesforce,
// pipedrive, zoho_crm, attio) into a shared registry and returns the tool
// count.
func InstallBatch6Lane1(target *tools.ToolRegistry) int {
	return crm.NewCRMRegistry().Install(target)
}

// newBatch6Lane1Registry builds a registry containing only the lane-1 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane1Registry(opts ...Option) *Registry {
	return crm.NewCRMRegistry(opts...)
}

// InstallBatch6Lane2 publishes the batch-6 lane-2 support tools (intercom,
// zendesk, freshdesk, greenhouse) into a shared registry and returns the tool
// count.
func InstallBatch6Lane2(target *tools.ToolRegistry) int {
	return crm.NewSupportRegistry().Install(target)
}

// newBatch6Lane2Registry builds a registry containing only the lane-2 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane2Registry(opts ...Option) *Registry {
	return crm.NewSupportRegistry(opts...)
}

// InstallBatch5Lane4 publishes the batch-5 lane-4 tools (hubspot and obsidian)
// into a shared registry and returns the number of tools registered.
func InstallBatch5Lane4(target *tools.ToolRegistry) int {
	return crm.NewHubSpotObsidianRegistry().Install(target)
}

// newBatch5Lane4Registry builds a registry containing only the batch-5 lane-4
// tools, sharing the Runtime surface used by the rest of the port.
func newBatch5Lane4Registry(opts ...Option) *Registry {
	return crm.NewHubSpotObsidianRegistry(opts...)
}

// The commerce / finance family lives in its domain subpackage
// (internal/tools/integrations/commerce); this thin entry point keeps the
// historical assembly name used by the CLI and by the cross-family test
// suites.

// InstallBatch6Lane3 publishes the batch-6 lane-3 tools (shopify, quickbooks,
// plaid, razorpay) into a shared registry and returns the tool count.
func InstallBatch6Lane3(target *tools.ToolRegistry) int {
	return commerce.NewRegistry().Install(target)
}

// newBatch6Lane3Registry builds a registry containing only the lane-3 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane3Registry(opts ...Option) *Registry {
	return commerce.NewRegistry(opts...)
}
