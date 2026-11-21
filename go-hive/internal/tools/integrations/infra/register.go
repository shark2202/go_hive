package infra

// Registry construction and assembly hooks. The root package calls NewRegistry;
// tests call it with options to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

const (
	maxBody              = 8 << 20
	defaultTerraformURL  = "https://app.terraform.io"
	defaultTimeout       = 30 * time.Second
	defaultPrometheusSec = 5
)

// prometheusHelp mirrors the credential spec instructions upstream returns
// with a missing-base-URL response.
const prometheusHelp = "To configure Prometheus access:\n\n" +
	"1. Set your Prometheus base URL:\n   export PROMETHEUS_BASE_URL=http://localhost:9090\n\n" +
	"Optional authentication:\n\n" +
	"2. For Bearer Token:\n   export PROMETHEUS_TOKEN=your-token\n\n" +
	"3. For Basic Auth:\n   export PROMETHEUS_USERNAME=admin\n   export PROMETHEUS_PASSWORD=secret\n\n" +
	"Notes:\n- PROMETHEUS_BASE_URL is required\n- Authentication is optional (most local setups don’t need it)\n"

// NewRegistry builds a registry containing only the infrastructure automation
// tools (terraform, pagerduty, prometheus, kafka, n8n, tines), sharing the
// Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	return Register(integrationkit.NewRegistry(runtime))
}

// Register adds the six catalogs and their action implementations to reg. It
// is the assembly hook the root uses when it owns the kernel registry.
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{
		terraformTool,
		pagerDutyTool,
		prometheusTool,
		kafkaTool,
		n8nTool,
		tinesTool,
	} {
		reg.AddTool(tool)
	}
	impls := []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"terraform", terraformImpls()},
		{"pagerduty", pagerDutyImpls()},
		{"prometheus", prometheusImpls()},
		{"kafka", kafkaImpls()},
		{"n8n", n8nImpls()},
		{"tines", tinesImpls()},
	}
	for _, entry := range impls {
		for action, impl := range entry.funcs {
			reg.AddImpl(entry.tool, action, impl)
		}
	}
	return reg
}

// modules declares the credential variables for the six modules.
// Base URLs that upstream derives from environment variables stay empty here
// and are resolved per call; WithBaseURL overrides them in tests.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"terraform_tool":  {ID: "terraform_tool", EnvVars: []string{"TFC_TOKEN"}},
		"pagerduty_tool":  {ID: "pagerduty_tool", BaseURL: "https://api.pagerduty.com", EnvVars: []string{"PAGERDUTY_API_KEY"}},
		"prometheus_tool": {ID: "prometheus_tool"},
		"kafka_tool":      {ID: "kafka_tool"},
		"n8n_tool":        {ID: "n8n_tool", EnvVars: []string{"N8N_API_KEY"}},
		"tines_tool":      {ID: "tines_tool", EnvVars: []string{"TINES_API_KEY"}},
	}
}
