package data

// Family-level assembly: NewRegistry builds the union of both lanes
// (databases first, then the data platforms) and Register attaches the same
// tools to an existing kernel registry. The two per-lane constructors
// (NewDatabasesRegistry / NewPlatformRegistry) keep the historical
// InstallBatch5Lane2 / InstallBatch6Lane4 registries byte-identical.

import (
	"os"
	"time"
)

// NewRegistry builds a registry containing the whole data family: postgres,
// redis, mongodb, supabase, snowflake, bigquery, databricks, redshift,
// azure_sql, powerbi and sap, sharing the Runtime surface used by the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: allModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	return Register(newRegistry(runtime))
}

// Register adds the family catalogs and their action implementations to reg.
// It is the assembly hook for callers that own the kernel registry.
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{
		postgresTool,
		redisTool,
		mongoTool,
		supabaseTool,
		snowflakeTool,
		bigQueryTool,
		databricksTool,
		redshiftTool,
		azureSQLTool,
		powerBITool,
		sapTool,
	} {
		reg.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"postgres", postgresImpls()},
		{"redis", redisImpls()},
		{"mongodb", mongoImpls()},
		{"supabase", supabaseImpls()},
		{"snowflake", snowflakeImpls()},
		{"bigquery", bigQueryImpls()},
		{"databricks", databricksImpls()},
		{"redshift", redshiftImpls()},
		{"azure_sql", azureSQLImpls()},
		{"powerbi", powerBIToolImpls()},
		{"sap", sapImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			reg.AddImpl(entry.tool, action, impl)
		}
	}
	return reg
}

// allModules merges the two lane module maps; module ids are disjoint.
func allModules() map[string]*ModuleConfig {
	modules := databaseModules()
	for id, cfg := range platformModules() {
		modules[id] = cfg
	}
	return modules
}
