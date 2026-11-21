// Batch 2 of the integration port: research, weather, market-data, media and
// network-scanning modules. Everything here lives in batch2_*.go so the first
// batch's catalog and registrations stay untouched.
//
// Tool IDs, tool names and per-action parameter schemas follow the rules in
// gen_catalog.py (including its single-action naming rule), so the generated
// catalog and this hand-written one stay consistent.
package integrations

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// InstallBatch2 publishes the second batch of ported tools into a shared
// registry and returns the number of tools registered. It never returns nil
// dependencies: scanners use the real network unless a test builds the batch2
// registry with injected probes.
func InstallBatch2(target *tools.ToolRegistry) int {
	return newBatch2Registry().Install(target)
}

// batch2Deps holds the injectable side effects used by the network scanners.
type batch2Deps struct {
	dns      dnsResolver
	tlsProbe tlsProbe
	ports    portProbe
	hosts    hostResolver
}

type batch2Option func(*batch2Deps)

// withBatch2DNS swaps the DNS resolver used by dns_security_scan.
func withBatch2DNS(resolver dnsResolver) batch2Option {
	return func(deps *batch2Deps) { deps.dns = resolver }
}

// withBatch2TLS swaps the TLS probe used by ssl_tls_scan.
func withBatch2TLS(probe tlsProbe) batch2Option {
	return func(deps *batch2Deps) { deps.tlsProbe = probe }
}

// withBatch2Ports swaps the TCP connect probe used by port_scan.
func withBatch2Ports(probe portProbe) batch2Option {
	return func(deps *batch2Deps) { deps.ports = probe }
}

// withBatch2Hosts swaps the hostname resolver used by port_scan.
func withBatch2Hosts(resolver hostResolver) batch2Option {
	return func(deps *batch2Deps) { deps.hosts = resolver }
}

func defaultBatch2Deps() batch2Deps {
	return batch2Deps{
		dns:      newSystemDNSResolver(),
		tlsProbe: defaultTLSProbe,
		ports:    defaultPortProbe,
		hosts:    defaultHostResolver,
	}
}

// newBatch2Registry builds a registry containing only batch 2 tools, sharing
// the same Runtime surface as New() so base URLs and transports stay
// injectable.
func newBatch2Registry(opts ...batch2Option) *Registry {
	deps := defaultBatch2Deps()
	for _, opt := range opts {
		opt(&deps)
	}
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch2Modules(),
		Getenv:  os.Getenv,
	}
	registry := NewRegistry(runtime)
	for _, tool := range batch2Catalog {
		registry.AddTool(tool)
	}

	impls := map[string]map[string]ActionFunc{
		"arxiv": {
			"search_papers":  arxivSearchPapers,
			"download_paper": arxivDownloadPaper,
		},
		"duckduckgo": {
			"duckduckgo_search": duckduckgoSearch,
			"duckduckgo_news":   duckduckgoNews,
			"duckduckgo_images": duckduckgoImages,
		},
		"search_wikipedia": {"search_wikipedia": wikipediaSearch},
		"openmeteo": {
			"weather_get_current":  openMeteoCurrent,
			"weather_get_forecast": openMeteoForecast,
		},
		"yahoo_finance": {
			"yahoo_finance_quote":      yahooFinanceQuote,
			"yahoo_finance_history":    yahooFinanceHistory,
			"yahoo_finance_financials": yahooFinanceFinancials,
			"yahoo_finance_info":       yahooFinanceInfo,
			"yahoo_finance_search":     yahooFinanceSearch,
		},
		"youtube_transcript": {
			"youtube_get_transcript":   youtubeGetTranscript,
			"youtube_list_transcripts": youtubeListTranscripts,
		},
		"dns_security_scan": {
			"dns_security_scan": func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
				return dnsSecurityScan(ctx, rt, input, deps.dns)
			},
		},
		"http_headers_scan": {"http_headers_scan": httpHeadersScan},
		"ssl_tls_scan": {
			"ssl_tls_scan": func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
				return sslTLSScan(ctx, rt, input, deps.tlsProbe)
			},
		},
		"port_scan": {
			"port_scan": func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
				return portScan(ctx, rt, input, deps.hosts, deps.ports)
			},
		},
	}
	for tool, actions := range impls {
		for action, fn := range actions {
			registry.AddImpl(tool, action, fn)
		}
	}
	return registry
}

// batch2Modules declares the public endpoints of the network-backed modules.
// None of them require credentials; tests override BaseURL with WithBaseURL.
func batch2Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"arxiv_tool":              {ID: "arxiv_tool", BaseURL: "https://export.arxiv.org"},
		"duckduckgo_tool":         {ID: "duckduckgo_tool", BaseURL: "https://duckduckgo.com"},
		"wikipedia_tool":          {ID: "wikipedia_tool", BaseURL: "https://{lang}.wikipedia.org"},
		"openmeteo_tool":          {ID: "openmeteo_tool", BaseURL: "https://api.open-meteo.com"},
		"yahoo_finance_tool":      {ID: "yahoo_finance_tool", BaseURL: "https://query1.finance.yahoo.com"},
		"youtube_transcript_tool": {ID: "youtube_transcript_tool", BaseURL: "https://www.youtube.com"},
	}
}

// batch2BaseURL resolves the base URL for a module, honouring WithBaseURL and
// substituting "{lang}"/"{token}" style placeholders.
func batch2BaseURL(rt *Runtime, moduleID string, vars map[string]string) string {
	base := ""
	if cfg := rt.Modules[moduleID]; cfg != nil {
		base = cfg.BaseURL
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	for name, value := range vars {
		base = strings.ReplaceAll(base, "{"+name+"}", value)
	}
	return base
}

// batch2Catalog mirrors gen_catalog.py output for the ten batch-2 modules.
var batch2Catalog = []*Tool{
	{
		ID:          "arxiv_tool",
		Name:        "arxiv",
		DisplayName: "Arxiv",
		Summary:     "Arxiv integration (2 actions).",
		Actions: []Action{
			{
				Name:    "search_papers",
				Summary: "Searches arXiv for scientific papers using keywords or specific IDs.",
				Source:  "arxiv_tool/arxiv_tool.py",
				Params: map[string]any{
					"query":       paramDefault("string", ""),
					"id_list":     paramItems("array", "string"),
					"max_results": paramDefault("integer", 10),
					"sort_by":     paramEnumDefault("string", "relevance", []string{"relevance", "lastUpdatedDate", "submittedDate"}),
					"sort_order":  paramEnumDefault("string", "descending", []string{"descending", "ascending"}),
				},
			},
			{
				Name:     "download_paper",
				Summary:  "Downloads a paper from arXiv by its ID and saves it to a managed temporary directory for the lifetime of the server process.",
				Source:   "arxiv_tool/arxiv_tool.py",
				Required: []string{"paper_id"},
				Params: map[string]any{
					"paper_id": param("string"),
				},
			},
		},
	},
	{
		ID:          "duckduckgo_tool",
		Name:        "duckduckgo",
		DisplayName: "Duckduckgo",
		Summary:     "Duckduckgo integration (3 actions).",
		Actions: []Action{
			{
				Name:     "duckduckgo_search",
				Summary:  "Search the web using DuckDuckGo.",
				Source:   "duckduckgo_tool/duckduckgo_tool.py",
				Required: []string{"query"},
				Params: map[string]any{
					"query":       param("string"),
					"max_results": paramDefault("integer", 10),
					"region":      paramDefault("string", "us-en"),
					"safesearch":  paramDefault("string", "moderate"),
					"timelimit":   paramDefault("string", ""),
				},
			},
			{
				Name:     "duckduckgo_news",
				Summary:  "Search news using DuckDuckGo.",
				Source:   "duckduckgo_tool/duckduckgo_tool.py",
				Required: []string{"query"},
				Params: map[string]any{
					"query":       param("string"),
					"max_results": paramDefault("integer", 10),
					"region":      paramDefault("string", "us-en"),
					"timelimit":   paramDefault("string", ""),
				},
			},
			{
				Name:     "duckduckgo_images",
				Summary:  "Search images using DuckDuckGo.",
				Source:   "duckduckgo_tool/duckduckgo_tool.py",
				Required: []string{"query"},
				Params: map[string]any{
					"query":       param("string"),
					"max_results": paramDefault("integer", 10),
					"region":      paramDefault("string", "us-en"),
					"safesearch":  paramDefault("string", "moderate"),
					"size":        paramDefault("string", ""),
				},
			},
		},
	},
	{
		ID:          "wikipedia_tool",
		Name:        "search_wikipedia",
		DisplayName: "Wikipedia",
		Summary:     "Search Wikipedia for a given query and return summaries of top matching articles.",
		Actions: []Action{
			{
				Name:     "search_wikipedia",
				Summary:  "Search Wikipedia for a given query and return summaries of top matching articles.",
				Source:   "wikipedia_tool/wikipedia_tool.py",
				Required: []string{"query"},
				Params: map[string]any{
					"query":       param("string"),
					"lang":        paramDefault("string", "en"),
					"num_results": paramDefault("integer", 3),
				},
			},
		},
	},
	{
		ID:          "openmeteo_tool",
		Name:        "openmeteo",
		DisplayName: "Openmeteo",
		Summary:     "Openmeteo integration (2 actions).",
		Actions: []Action{
			{
				Name:     "weather_get_current",
				Summary:  "Get current weather conditions for any location.",
				Source:   "openmeteo_tool/openmeteo_tool.py",
				Required: []string{"latitude", "longitude"},
				Params: map[string]any{
					"latitude":  param("number"),
					"longitude": param("number"),
				},
			},
			{
				Name:     "weather_get_forecast",
				Summary:  "Get a multi-day weather forecast for any location.",
				Source:   "openmeteo_tool/openmeteo_tool.py",
				Required: []string{"latitude", "longitude"},
				Params: map[string]any{
					"latitude":  param("number"),
					"longitude": param("number"),
					"days":      paramDefault("integer", 7),
				},
			},
		},
	},
	{
		ID:          "yahoo_finance_tool",
		Name:        "yahoo_finance",
		DisplayName: "Yahoo Finance",
		Summary:     "Yahoo Finance integration (5 actions).",
		Actions: []Action{
			{
				Name:     "yahoo_finance_quote",
				Summary:  "Get current stock quote and key statistics.",
				Source:   "yahoo_finance_tool/yahoo_finance_tool.py",
				Required: []string{"symbol"},
				Params: map[string]any{
					"symbol": param("string"),
				},
			},
			{
				Name:     "yahoo_finance_history",
				Summary:  "Get historical price data for a stock.",
				Source:   "yahoo_finance_tool/yahoo_finance_tool.py",
				Required: []string{"symbol"},
				Params: map[string]any{
					"symbol":   param("string"),
					"period":   paramDefault("string", "1mo"),
					"interval": paramDefault("string", "1d"),
				},
			},
			{
				Name:     "yahoo_finance_financials",
				Summary:  "Get financial statements for a company.",
				Source:   "yahoo_finance_tool/yahoo_finance_tool.py",
				Required: []string{"symbol"},
				Params: map[string]any{
					"symbol":    param("string"),
					"statement": paramDefault("string", "income"),
				},
			},
			{
				Name:     "yahoo_finance_info",
				Summary:  "Get detailed company information.",
				Source:   "yahoo_finance_tool/yahoo_finance_tool.py",
				Required: []string{"symbol"},
				Params: map[string]any{
					"symbol": param("string"),
				},
			},
			{
				Name:     "yahoo_finance_search",
				Summary:  "Search for stock tickers by company name or keyword.",
				Source:   "yahoo_finance_tool/yahoo_finance_tool.py",
				Required: []string{"query"},
				Params: map[string]any{
					"query": param("string"),
				},
			},
		},
	},
	{
		ID:          "youtube_transcript_tool",
		Name:        "youtube_transcript",
		DisplayName: "Youtube Transcript",
		Summary:     "Youtube Transcript integration (2 actions).",
		Actions: []Action{
			{
				Name:     "youtube_get_transcript",
				Summary:  "Get the transcript/captions for a YouTube video.",
				Source:   "youtube_transcript_tool/youtube_transcript_tool.py",
				Required: []string{"video_id"},
				Params: map[string]any{
					"video_id":            param("string"),
					"language":            paramDefault("string", "en"),
					"preserve_formatting": paramDefault("boolean", false),
				},
			},
			{
				Name:     "youtube_list_transcripts",
				Summary:  "List available transcripts/caption tracks for a YouTube video.",
				Source:   "youtube_transcript_tool/youtube_transcript_tool.py",
				Required: []string{"video_id"},
				Params: map[string]any{
					"video_id": param("string"),
				},
			},
		},
	},
	{
		ID:          "dns_security_scanner",
		Name:        "dns_security_scan",
		DisplayName: "Dns Security",
		Summary:     "Scan a domain's DNS records for email security and infrastructure hardening.",
		Actions: []Action{
			{
				Name:     "dns_security_scan",
				Summary:  "Scan a domain's DNS records for email security and infrastructure hardening.",
				Source:   "dns_security_scanner/dns_security_scanner.py",
				Required: []string{"domain"},
				Params: map[string]any{
					"domain": param("string"),
				},
			},
		},
	},
	{
		ID:          "http_headers_scanner",
		Name:        "http_headers_scan",
		DisplayName: "Http Headers",
		Summary:     "Scan a URL for OWASP-recommended security headers and information leaks.",
		Actions: []Action{
			{
				Name:     "http_headers_scan",
				Summary:  "Scan a URL for OWASP-recommended security headers and information leaks.",
				Source:   "http_headers_scanner/http_headers_scanner.py",
				Required: []string{"url"},
				Params: map[string]any{
					"url":              param("string"),
					"follow_redirects": paramDefault("boolean", true),
				},
			},
		},
	},
	{
		ID:          "ssl_tls_scanner",
		Name:        "ssl_tls_scan",
		DisplayName: "Ssl Tls",
		Summary:     "Scan a host's SSL/TLS configuration and certificate.",
		Actions: []Action{
			{
				Name:     "ssl_tls_scan",
				Summary:  "Scan a host's SSL/TLS configuration and certificate.",
				Source:   "ssl_tls_scanner/ssl_tls_scanner.py",
				Required: []string{"hostname"},
				Params: map[string]any{
					"hostname": param("string"),
					"port":     paramDefault("integer", 443),
				},
			},
		},
	},
	{
		ID:          "port_scanner",
		Name:        "port_scan",
		DisplayName: "Port",
		Summary:     "Scan a host for open ports using TCP connect probes.",
		Actions: []Action{
			{
				Name:     "port_scan",
				Summary:  "Scan a host for open ports using TCP connect probes.",
				Source:   "port_scanner/port_scanner.py",
				Required: []string{"hostname"},
				Params: map[string]any{
					"hostname": param("string"),
					"ports":    paramDefault("string", "top20"),
					"timeout":  paramDefault("number", 3.0),
				},
			},
		},
	},
}
