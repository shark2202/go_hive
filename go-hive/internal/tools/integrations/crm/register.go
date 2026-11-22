package crm

// Family-level assembly: NewRegistry builds the union of the three lanes
// (CRM first, then the support desk, then hubspot/obsidian) and Register
// attaches the same tools to an existing kernel registry. The three per-lane
// constructors (NewCRMRegistry / NewSupportRegistry /
// NewHubSpotObsidianRegistry) keep the historical InstallBatch6Lane1 /
// InstallBatch6Lane2 / InstallBatch5Lane4 registries byte-identical.

import (
	"os"
	"time"
)

// NewRegistry builds a registry containing the whole CRM family: salesforce,
// pipedrive, zoho_crm, attio, intercom, zendesk, freshdesk, greenhouse,
// hubspot and obsidian, sharing the Runtime surface used by the port.
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
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{
		salesforceTool,
		pipedriveTool,
		zohoCRMTool,
		attioTool,
		intercomTool,
		zendeskTool,
		freshdeskTool,
		greenhouseTool,
		hubSpotTool,
		obsidianTool,
	} {
		reg.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"salesforce", salesforceImpls()},
		{"pipedrive", pipedriveImpls()},
		{"zoho_crm", zohoCRMImpls()},
		{"attio", attioImpls()},
		{"intercom", intercomImpls()},
		{"zendesk", zendeskImpls()},
		{"freshdesk", freshdeskImpls()},
		{"greenhouse", greenhouseImpls()},
		{"hubspot", hubSpotImpls()},
		{"obsidian", obsidianImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			reg.AddImpl(entry.tool, action, impl)
		}
	}
	return reg
}

// allModules merges the three lane module maps; module ids are disjoint.
func allModules() map[string]*ModuleConfig {
	merged := crmModules()
	for id, cfg := range supportModules() {
		merged[id] = cfg
	}
	for id, cfg := range modules() {
		merged[id] = cfg
	}
	return merged
}
