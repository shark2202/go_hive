// Batch 8 (final) of the integration port: the three modules that were still
// missing after wave 8 — attach_file, subdomain_enumerate and tech_stack_detect.
//
// Everything lives in batch8_final*.go so the earlier batches stay untouched.
// The two scanners are part of the default session surface, matching upstream
// aden_tools (which registers all seven security tools unconditionally);
// attach_file is always installed too.
//
// Parity: tools/src/aden_tools/tools/attach_file_tool/attach_file_tool.py::register_tools
// Parity: tools/src/aden_tools/tools/subdomain_enumerator/subdomain_enumerator.py::register_tools
// Parity: tools/src/aden_tools/tools/tech_stack_detector/tech_stack_detector.py::register_tools
package integrations

import (
	"net/http"
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// InstallBatch8Final publishes the last three ported modules into a shared
// registry and returns the number of tools registered. The default registry
// uses the real network for the two scanners; tests build one with
// newBatch8FinalRegistry and injected transports/base URLs.
func InstallBatch8Final(target *tools.ToolRegistry) int {
	return newBatch8FinalRegistry().Install(target)
}

// batch8Deps holds the injectable side effects used by the final batch.
type batch8Deps struct {
	client   *http.Client
	baseURLs map[string]string
}

type batch8Option func(*batch8Deps)

// withBatch8Client swaps the HTTP transport used by both scanners.
func withBatch8Client(client *http.Client) batch8Option {
	return func(deps *batch8Deps) { deps.client = client }
}

// withBatch8BaseURL points one scanner module at a fake server.
func withBatch8BaseURL(moduleID, baseURL string) batch8Option {
	return func(deps *batch8Deps) {
		if deps.baseURLs == nil {
			deps.baseURLs = map[string]string{}
		}
		deps.baseURLs[moduleID] = baseURL
	}
}

func defaultBatch8Deps() batch8Deps {
	return batch8Deps{baseURLs: map[string]string{}}
}

// newBatch8FinalRegistry builds a registry containing only batch 8 tools.
func newBatch8FinalRegistry(opts ...batch8Option) *Registry {
	deps := defaultBatch8Deps()
	for _, opt := range opts {
		opt(&deps)
	}
	modules := map[string]*ModuleConfig{
		"subdomain_enumerator": {ID: "subdomain_enumerator", BaseURL: "https://crt.sh"},
	}
	for moduleID, baseURL := range deps.baseURLs {
		cfg := modules[moduleID]
		if cfg == nil {
			cfg = &ModuleConfig{ID: moduleID}
			modules[moduleID] = cfg
		}
		cfg.BaseURL = baseURL
	}
	runtime := &Runtime{
		HTTP:    newHTTPClient(deps.client),
		Now:     time.Now,
		Modules: modules,
		Getenv:  os.Getenv,
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range batch8Catalog {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	impls := map[string]map[string]ActionFunc{
		"attach_file":         {"attach_file": attachFile},
		"subdomain_enumerate": {"subdomain_enumerate": subdomainEnumerate},
		"tech_stack_detect":   {"tech_stack_detect": techStackDetect},
	}
	for tool, actions := range impls {
		for action, fn := range actions {
			registry.addImpl(tool, action, fn)
		}
	}
	return registry
}

// batch8Catalog mirrors gen_catalog.py output for the three final modules. The
// single-action naming rule applies: the tool name is the action name.
var batch8Catalog = []*Tool{
	{
		ID:          "attach_file_tool",
		Name:        "attach_file",
		DisplayName: "Attach File",
		Summary:     "Surface one or more local files to the user as downloadable chat attachments.",
		Actions: []Action{
			{
				Name: "attach_file",
				Summary: "Attach local files so the user gets a clickable download chip. Any file type is accepted. " +
					"Text files also get an inline preview in the result (capped at 8 KiB) so you can read them. " +
					"Images are returned as image content blocks when the runtime supports them; PDFs and other " +
					"binary files are attachment references only — this build does not put their bytes into the " +
					"model context, so use a terminal/PDF tool to read them. Paths may be absolute, relative to " +
					"the session working directory, or relative to the session storage root (e.g. data/attachments/X.pdf).",
				Source:   "attach_file_tool/attach_file_tool.py",
				Required: []string{"paths"},
				Params: map[string]any{
					"paths": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "One or more paths (max 10 per call). A single bare string is also accepted.",
					},
					"max_bytes_mb": map[string]any{
						"type":        "number",
						"default":     attachFileDefaultMaxBytesMB,
						"description": "Per-file size cap. Larger files are reported in errors instead of being attached.",
					},
				},
			},
		},
	},
	{
		ID:          "subdomain_enumerator",
		Name:        "subdomain_enumerate",
		DisplayName: "Subdomain Enumerator",
		Summary:     "Discover subdomains through Certificate Transparency logs (crt.sh), with security-relevant findings.",
		Actions: []Action{
			{
				Name: "subdomain_enumerate",
				Summary: "Discover subdomains from public Certificate Transparency logs (passive OSINT, no brute force). " +
					"Flags staging/dev/admin/debug/backup-style hosts and returns grade_input for risk scoring.",
				Source:   "subdomain_enumerator/subdomain_enumerator.py",
				Required: []string{"domain"},
				Params: map[string]any{
					"domain": map[string]any{
						"type":        "string",
						"description": "Base domain to enumerate (e.g. example.com). No protocol prefix required.",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"default":     50,
						"description": "Maximum number of subdomains to return (max 200).",
					},
				},
			},
		},
	},
	{
		ID:          "tech_stack_detector",
		Name:        "tech_stack_detect",
		DisplayName: "Tech Stack Detector",
		Summary:     "Fingerprint the web server, framework, CMS, JS libraries, CDN and security posture of a site.",
		Actions: []Action{
			{
				Name: "tech_stack_detect",
				Summary: "Detect the technology stack of a website through passive analysis: response headers, HTML, " +
					"cookies and a few common path probes. Returns grade_input for risk scoring. Non-intrusive.",
				Source:   "tech_stack_detector/tech_stack_detector.py",
				Required: []string{"url"},
				Params: map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "URL to analyse (e.g. https://example.com). https:// is assumed when missing.",
					},
				},
			},
		},
	},
}
