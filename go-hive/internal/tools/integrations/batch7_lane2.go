// Batch 7 lane 2 of the integration port: the Cloudflare module — zones, DNS,
// analytics, security, cache, Workers/R2/Pages and account administration.
// 54 actions, the largest upstream tool.
//
// Upstream reference:
// tools/src/aden_tools/tools/cloudflare_tool/cloudflare_tool.py. Action names,
// parameter names/defaults and response shapes are preserved, with one
// documented exception: upstream's `action` argument of
// cloudflare_create_firewall_rule is exposed as `rule_action`, because the Go
// port reserves `action` for the tool's action selector that every batch-7
// tool dispatches through.
//
// Design:
//   - One `cloudflare` tool with 54 actions. Each action is a row in
//     batch7Lane2Actions that declares method, path template, parameters,
//     defaults, query/body field mapping and response wrapper. Actions that
//     need several upstream calls or conditional bodies carry a Run override;
//     everything else is executed by the shared table-driven dispatcher.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server through WithBaseURL.
//   - HTTP failures mirror the upstream _make_request helper, including the
//     exact 401/403/404/429 messages, the Retry-After hint, the 204 success
//     payload and the 500-rune provider error truncation.
//   - zone_id keeps the upstream 32-hex validation; domain checks mirror
//     _validate_domain.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane2MaxBody     = 8 << 20
	batch7Lane2MaxError    = 500
	batch7Lane2MaxPageSize = 100
	batch7Lane2ModuleID    = "cloudflare_tool"
	batch7Lane2DefaultBase = "https://api.cloudflare.com/client/v4"
)

// InstallBatch7Lane2 publishes the batch-7 lane-2 Cloudflare tool into a shared
// registry and returns the number of tools registered.
func InstallBatch7Lane2(target *tools.ToolRegistry) int {
	return batch7Lane2NewRegistry().Install(target)
}

// batch7Lane2NewRegistry builds a registry containing only the Cloudflare
// tool, sharing the Runtime surface used by the rest of the port.
func batch7Lane2NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane2Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	tool := batch7Lane2Tool()
	registry.tools[tool.Name] = tool
	registry.order = append(registry.order, tool.Name)
	for _, action := range batch7Lane2Actions {
		registry.addImpl(tool.Name, action.Name, batch7Lane2Impl(action))
	}
	return registry
}

// batch7Lane2Modules declares the public endpoint and credential variable.
func batch7Lane2Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		batch7Lane2ModuleID: {
			ID:      batch7Lane2ModuleID,
			BaseURL: batch7Lane2DefaultBase,
			EnvVars: []string{"CLOUDFLARE_API_TOKEN"},
		},
	}
}

// ------------------------------------------------------------- catalog row ---

// batch7Lane2Action is one upstream @mcp.tool function. The default runner
// sends a single request built from Path/Query/BodyMap/Consts and wraps the
// decoded result. Shape post-processes that result; Run replaces the whole
// single-request flow for multi-call or conditional actions.
type batch7Lane2Action struct {
	Name     string
	Summary  string
	Params   map[string]any
	Required []string
	Method   string
	Path     string
	Query    []string
	BodyMap  map[string]string
	Consts   map[string]any
	// Wrap is the response key the result is published under. Actions without
	// an upstream `if "error" in result: return result` guard set WrapErrors so
	// the provider failure lands under that key untouched.
	Wrap       string
	WrapErrors bool
	Shape      func(batch7Lane2Reply, map[string]any) any
	Run        func(context.Context, *Runtime, map[string]any) (any, error)
}

// batch7Lane2AnyParam declares a parameter with no type restriction, matching
// the upstream `value: Any` signature.
func batch7Lane2AnyParam() map[string]any { return map[string]any{} }

// batch7Lane2ListParam declares a JSON array parameter.
func batch7Lane2ListParam(items string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": items}}
}

// batch7Lane2ZoneParams builds the zone-scoped parameter map.
func batch7Lane2ZoneParams(extra map[string]any) map[string]any {
	out := map[string]any{"zone_id": param("string")}
	for name, schema := range extra {
		out[name] = schema
	}
	return out
}

// batch7Lane2Actions is the single source of truth for the 54 actions: it
// feeds both the model-facing schema and the dispatcher.
var batch7Lane2Actions = []batch7Lane2Action{
	// ------------------------------------------------ infrastructure & zones
	{
		Name:    "cloudflare_list_zones",
		Summary: "List Cloudflare zones (domains) in the account.",
		Method:  http.MethodGet,
		Path:    "/zones",
		Params: map[string]any{
			"page":     paramDefault("integer", 1),
			"per_page": paramDefault("integer", 20),
			"name":     param("string"),
		},
		Run: batch7Lane2ListZones,
	},
	{
		Name:     "cloudflare_get_zone",
		Summary:  "Get details for a specific Cloudflare zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeZoneDetail,
	},
	{
		Name:     "cloudflare_get_zone_settings",
		Summary:  "Get all common settings for a zone (TLS, IPv6, WebSockets, etc.).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "settings",
	},
	{
		Name:     "cloudflare_list_zone_custom_pages",
		Summary:  "List custom error and challenge pages for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/custom_pages",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeListOrEmpty("custom_pages"),
	},
	{
		Name:     "cloudflare_get_ssl_verification",
		Summary:  "Get SSL certificate verification status for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/ssl/verification",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "ssl_verification",
	},
	{
		Name:     "cloudflare_list_zone_certificates",
		Summary:  "List SSL/TLS certificates for a zone (universal plus custom).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/ssl/universal/settings",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2ListZoneCertificates,
	},
	{
		Name:     "cloudflare_list_zone_subscriptions",
		Summary:  "List active paid subscriptions/apps for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/subscriptions",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeListOrEmpty("subscriptions"),
	},
	{
		Name:     "cloudflare_get_dnssec_status",
		Summary:  "Get DNSSEC status and configuration for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/dnssec",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "dnssec",
	},
	{
		Name:       "cloudflare_update_zone_setting",
		Summary:    "Update a specific zone setting (IPv6, WebSockets, ssl, etc.).",
		Method:     http.MethodPatch,
		Path:       "/zones/{zone_id}/settings/{setting_id}",
		Params:     batch7Lane2ZoneParams(map[string]any{"setting_id": param("string"), "value": batch7Lane2AnyParam()}),
		Required:   []string{"zone_id", "setting_id", "value"},
		BodyMap:    map[string]string{"value": "value"},
		Wrap:       "updated_setting",
		WrapErrors: true,
	},

	// -------------------------------------------------------- dns management
	{
		Name:    "cloudflare_list_dns_records",
		Summary: "List DNS records for a zone, optionally filtered by name and type.",
		Method:  http.MethodGet,
		Path:    "/zones/{zone_id}/dns_records",
		Params: batch7Lane2ZoneParams(map[string]any{
			"name":     param("string"),
			"type":     param("string"),
			"page":     paramDefault("integer", 1),
			"per_page": paramDefault("integer", 20),
		}),
		Required: []string{"zone_id"},
		Run:      batch7Lane2ListDNSRecords,
	},
	{
		Name:     "cloudflare_get_dns_record",
		Summary:  "Get a specific DNS record by ID.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/dns_records/{record_id}",
		Params:   batch7Lane2ZoneParams(map[string]any{"record_id": param("string")}),
		Required: []string{"zone_id", "record_id"},
		Shape:    batch7Lane2ShapeRecordDetail,
	},
	{
		Name:     "cloudflare_list_dns_record_scan",
		Summary:  "Scan for common DNS records that are missing from Cloudflare.",
		Method:   http.MethodPost,
		Path:     "/zones/{zone_id}/dns_records/scan",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "scan_result",
	},
	{
		Name:     "cloudflare_get_dns_settings",
		Summary:  "Get DNS-specific settings for a zone (multi-provider, CNAME flattening).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/dns_settings",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "dns_settings",
	},
	{
		Name:     "cloudflare_list_dns_analytics_report",
		Summary:  "Get DNS query analytics and statistics for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/dns_analytics/report",
		Params:   batch7Lane2ZoneParams(map[string]any{"limit": paramDefault("integer", 10)}),
		Required: []string{"zone_id"},
		Wrap:     "dns_analytics",
	},
	{
		Name:     "cloudflare_check_domain_dns_health",
		Summary:  "Check DNS health and configuration for a domain.",
		Method:   http.MethodGet,
		Path:     "/zones",
		Params:   map[string]any{"domain": param("string")},
		Required: []string{"domain"},
		Run:      batch7Lane2DomainHealth,
	},
	{
		Name:    "cloudflare_create_dns_record",
		Summary: "Create a new DNS record for a zone.",
		Method:  http.MethodPost,
		Path:    "/zones/{zone_id}/dns_records",
		Params: batch7Lane2ZoneParams(map[string]any{
			"type":     param("string"),
			"name":     param("string"),
			"content":  param("string"),
			"ttl":      paramDefault("integer", 1),
			"proxied":  paramDefault("boolean", false),
			"priority": param("integer"),
		}),
		Required: []string{"zone_id", "type", "name", "content"},
		Run:      batch7Lane2CreateDNSRecord,
	},
	{
		Name:    "cloudflare_update_dns_record",
		Summary: "Update an existing DNS record (only supplied fields change).",
		Method:  http.MethodPatch,
		Path:    "/zones/{zone_id}/dns_records/{record_id}",
		Params: batch7Lane2ZoneParams(map[string]any{
			"record_id": param("string"),
			"type":      param("string"),
			"name":      param("string"),
			"content":   param("string"),
			"ttl":       param("integer"),
			"proxied":   param("boolean"),
		}),
		Required: []string{"zone_id", "record_id"},
		Run:      batch7Lane2UpdateDNSRecord,
	},
	{
		Name:       "cloudflare_delete_dns_record",
		Summary:    "Delete a single DNS record permanently.",
		Method:     http.MethodDelete,
		Path:       "/zones/{zone_id}/dns_records/{record_id}",
		Params:     batch7Lane2ZoneParams(map[string]any{"record_id": param("string")}),
		Required:   []string{"zone_id", "record_id"},
		Wrap:       "deleted",
		WrapErrors: true,
	},

	// ------------------------------------------------- analytics & monitoring
	{
		Name:     "cloudflare_get_zone_analytics",
		Summary:  "Get traffic analytics for a zone (requests, bandwidth, threats).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeZoneAnalytics,
	},
	{
		Name:     "cloudflare_get_top_analytics",
		Summary:  "Get top paths, countries and status codes for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeTopAnalytics,
	},
	{
		Name:     "cloudflare_get_security_analytics",
		Summary:  "Get summarized security analytics (WAF, Bot, etc.).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/security/analytics",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "security_analytics",
	},
	{
		Name:     "cloudflare_get_cache_analytics",
		Summary:  "Get cache performance analytics (hit ratio, bandwidth saved).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/cache/analytics/dashboard",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "cache_analytics",
	},
	{
		Name:     "cloudflare_get_performance_analytics",
		Summary:  "Get performance metrics (TTFB, loading times) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapePerformance,
	},
	{
		Name:     "cloudflare_get_http_analytics_report",
		Summary:  "Get HTTP traffic reports (status codes, content types, threats).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/analytics/dashboard",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeHTTPReport,
	},

	// ---------------------------------------------------- security & firewall
	{
		Name:     "cloudflare_list_firewall_events",
		Summary:  "List recent firewall/security events for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/firewall/events",
		Params:   batch7Lane2ZoneParams(map[string]any{"limit": paramDefault("integer", 10)}),
		Required: []string{"zone_id"},
		Run:      batch7Lane2ListFirewallEvents,
	},
	{
		Name:     "cloudflare_get_security_settings",
		Summary:  "Get current security settings for a zone (level, SSL mode, WAF).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/security_level",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2GetSecuritySettings,
	},
	{
		Name:     "cloudflare_list_page_rules",
		Summary:  "List custom page rules (redirects, cache overrides) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/pagerules",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapePageRules,
	},
	{
		Name:     "cloudflare_list_waf_rulesets",
		Summary:  "List WAF rulesets for a zone (modern WAF engine).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/rulesets",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "waf_rulesets",
	},
	{
		Name:     "cloudflare_get_bot_management_settings",
		Summary:  "Get Bot Management configuration for a zone (Bot Fight Mode, etc.).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/bot_management",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "bot_management",
	},
	{
		Name:     "cloudflare_list_managed_transforms",
		Summary:  "List Managed Transforms (HTTP header modifications) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/managed_headers",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "managed_transforms",
	},
	{
		Name:       "cloudflare_get_ddos_protection_settings",
		Summary:    "Get HTTP/L7 DDoS protection settings for a zone.",
		Method:     http.MethodGet,
		Path:       "/zones/{zone_id}/ddos_protection/settings",
		Params:     batch7Lane2ZoneParams(nil),
		Required:   []string{"zone_id"},
		Wrap:       "ddos_protection",
		WrapErrors: true,
	},
	{
		Name:    "cloudflare_create_firewall_rule",
		Summary: "Create a custom firewall rule (rule_action: block/challenge/js_challenge/managed_challenge/skip/log) via the Rulesets API.",
		Method:  http.MethodPost,
		Path:    "/zones/{zone_id}/rulesets/phases/http_request_firewall_custom/entrypoint/rules",
		Params: batch7Lane2ZoneParams(map[string]any{
			"rule_action": param("string"),
			"expression":  param("string"),
			"description": param("string"),
			"priority":    paramDefault("integer", 1),
		}),
		Required: []string{"zone_id", "rule_action", "expression"},
		Run:      batch7Lane2CreateFirewallRule,
	},
	{
		Name:       "cloudflare_delete_firewall_rule",
		Summary:    "Delete a firewall rule by its Ruleset rule ID.",
		Method:     http.MethodDelete,
		Path:       "/zones/{zone_id}/rulesets/phases/http_request_firewall_custom/entrypoint/rules/{rule_id}",
		Params:     batch7Lane2ZoneParams(map[string]any{"rule_id": param("string")}),
		Required:   []string{"zone_id", "rule_id"},
		Wrap:       "deleted",
		WrapErrors: true,
	},
	{
		Name:     "cloudflare_list_firewall_rules",
		Summary:  "List custom firewall rules for a zone (modern Rulesets API).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/rulesets/phases/http_request_firewall_custom/entrypoint",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeFirewallRules,
	},

	// ------------------------------------------------ performance & speed
	{
		Name:     "cloudflare_get_speed_settings",
		Summary:  "Check speed optimization settings (Minify, Brotli, Rocket Loader).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/minify",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2GetSpeedSettings,
	},
	{
		Name:     "cloudflare_get_cache_settings",
		Summary:  "Get cache settings for a zone (browser TTL, dev mode, cache level).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/browser_cache_ttl",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2GetCacheSettings,
	},
	{
		Name:     "cloudflare_get_http_config",
		Summary:  "Get HTTP protocol settings (HTTP/2, HTTP/3, 0-RTT) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/http2",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2GetHTTPConfig,
	},
	{
		Name:     "cloudflare_get_network_settings",
		Summary:  "Get network-level performance settings (WebSockets, Onion Routing, IPv6).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/websockets",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2GetNetworkSettings,
	},
	{
		Name:       "cloudflare_purge_cache_all",
		Summary:    "Purge everything from the Cloudflare cache for this zone.",
		Method:     http.MethodPost,
		Path:       "/zones/{zone_id}/purge_cache",
		Params:     batch7Lane2ZoneParams(nil),
		Required:   []string{"zone_id"},
		Consts:     map[string]any{"purge_everything": true},
		Wrap:       "purge_status",
		WrapErrors: true,
	},
	{
		Name:       "cloudflare_purge_cache_files",
		Summary:    "Purge specific URLs from the Cloudflare cache.",
		Method:     http.MethodPost,
		Path:       "/zones/{zone_id}/purge_cache",
		Params:     batch7Lane2ZoneParams(map[string]any{"urls": batch7Lane2ListParam("string")}),
		Required:   []string{"zone_id", "urls"},
		BodyMap:    map[string]string{"files": "urls"},
		Wrap:       "purge_status",
		WrapErrors: true,
	},
	{
		Name:     "cloudflare_list_advanced_services",
		Summary:  "List Workers scripts and Load Balancers for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/workers/scripts",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Run:      batch7Lane2ListAdvancedServices,
	},

	// ------------------------------------------------- account level insights
	{
		Name:    "cloudflare_list_accounts",
		Summary: "List all Cloudflare accounts the token has access to.",
		Method:  http.MethodGet,
		Path:    "/accounts",
		Params: map[string]any{
			"page":     paramDefault("integer", 1),
			"per_page": paramDefault("integer", 20),
		},
		Run: batch7Lane2ListAccounts,
	},
	{
		Name:     "cloudflare_get_account_details",
		Summary:  "Get details for a specific Cloudflare account.",
		Method:   http.MethodGet,
		Path:     "/accounts/{account_id}",
		Params:   map[string]any{"account_id": param("string")},
		Required: []string{"account_id"},
		Shape:    batch7Lane2ShapeAccountDetails,
	},
	{
		Name:       "cloudflare_list_account_members",
		Summary:    "List all members/users in a specific account.",
		Method:     http.MethodGet,
		Path:       "/accounts/{account_id}/members",
		Params:     map[string]any{"account_id": param("string")},
		Required:   []string{"account_id"},
		Wrap:       "members",
		WrapErrors: true,
	},
	{
		Name:    "cloudflare_invite_account_member",
		Summary: "Invite a new member to an account with specific roles.",
		Method:  http.MethodPost,
		Path:    "/accounts/{account_id}/members",
		Params: map[string]any{
			"account_id": param("string"),
			"email":      param("string"),
			"roles":      batch7Lane2ListParam("string"),
		},
		Required:   []string{"account_id", "email", "roles"},
		BodyMap:    map[string]string{"email": "email", "roles": "roles"},
		Wrap:       "invitation",
		WrapErrors: true,
	},
	{
		Name:       "cloudflare_delete_account_member",
		Summary:    "Remove a member's access from an account.",
		Method:     http.MethodDelete,
		Path:       "/accounts/{account_id}/members/{member_id}",
		Params:     map[string]any{"account_id": param("string"), "member_id": param("string")},
		Required:   []string{"account_id", "member_id"},
		Wrap:       "deleted",
		WrapErrors: true,
	},
	{
		Name:     "cloudflare_list_audit_logs",
		Summary:  "List recent audit logs for an account (who changed what).",
		Method:   http.MethodGet,
		Path:     "/accounts/{account_id}/audit_logs",
		Params:   map[string]any{"account_id": param("string"), "limit": paramDefault("integer", 20)},
		Required: []string{"account_id"},
		Run:      batch7Lane2ListAuditLogs,
	},

	// ------------------------------------------- advanced services & routing
	{
		Name:     "cloudflare_list_custom_hostnames",
		Summary:  "List custom hostnames (SSL for SaaS) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/custom_hostnames",
		Params:   batch7Lane2ZoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    batch7Lane2ShapeCustomHostnames,
	},
	{
		Name:     "cloudflare_list_access_applications",
		Summary:  "List Cloudflare Access (Zero Trust) applications for an account.",
		Method:   http.MethodGet,
		Path:     "/accounts/{account_id}/access/apps",
		Params:   map[string]any{"account_id": param("string")},
		Required: []string{"account_id"},
		Shape:    batch7Lane2ShapeAccessApplications,
	},
	{
		Name:       "cloudflare_list_r2_buckets",
		Summary:    "List all R2 storage buckets for an account.",
		Method:     http.MethodGet,
		Path:       "/accounts/{account_id}/r2/buckets",
		Params:     map[string]any{"account_id": param("string")},
		Required:   []string{"account_id"},
		Wrap:       "r2_buckets",
		WrapErrors: true,
	},
	{
		Name:       "cloudflare_list_pages_projects",
		Summary:    "List all Cloudflare Pages projects for an account.",
		Method:     http.MethodGet,
		Path:       "/accounts/{account_id}/pages/projects",
		Params:     map[string]any{"account_id": param("string")},
		Required:   []string{"account_id"},
		Wrap:       "pages_projects",
		WrapErrors: true,
	},
	{
		Name:    "cloudflare_create_access_policy",
		Summary: "Create a new Zero Trust Access policy for an application.",
		Method:  http.MethodPost,
		Path:    "/accounts/{account_id}/access/apps/{application_id}/policies",
		Params: map[string]any{
			"account_id":     param("string"),
			"application_id": param("string"),
			"name":           param("string"),
			"decision":       param("string"),
			"include":        batch7Lane2ListParam("object"),
		},
		Required: []string{"account_id", "application_id", "name", "decision", "include"},
		BodyMap: map[string]string{
			"name": "name", "decision": "decision", "include": "include",
		},
		Wrap:       "policy",
		WrapErrors: true,
	},
	{
		Name:    "cloudflare_create_worker_route",
		Summary: "Activate a Worker script on a specific URL pattern.",
		Method:  http.MethodPost,
		Path:    "/zones/{zone_id}/workers/routes",
		Params: batch7Lane2ZoneParams(map[string]any{
			"pattern":     param("string"),
			"script_name": param("string"),
		}),
		Required:   []string{"zone_id", "pattern", "script_name"},
		BodyMap:    map[string]string{"pattern": "pattern", "script": "script_name"},
		Wrap:       "route",
		WrapErrors: true,
	},
	{
		Name:       "cloudflare_set_ssl_mode",
		Summary:    "Directly set the SSL mode for a zone (off/flexible/full/strict).",
		Method:     http.MethodPatch,
		Path:       "/zones/{zone_id}/settings/ssl",
		Params:     batch7Lane2ZoneParams(map[string]any{"mode": param("string")}),
		Required:   []string{"zone_id", "mode"},
		BodyMap:    map[string]string{"value": "mode"},
		Wrap:       "ssl_update",
		WrapErrors: true,
	},
}

// batch7Lane2Tool builds the model-facing tool from the action table.
func batch7Lane2Tool() *Tool {
	actions := make([]Action, 0, len(batch7Lane2Actions))
	for _, row := range batch7Lane2Actions {
		actions = append(actions, Action{
			Name:     row.Name,
			Summary:  row.Summary,
			Source:   "cloudflare_tool/cloudflare_tool.py",
			Params:   row.Params,
			Required: row.Required,
		})
	}
	return &Tool{
		ID:          batch7Lane2ModuleID,
		Name:        "cloudflare",
		DisplayName: "Cloudflare",
		Summary:     "Cloudflare zone, DNS, analytics, security, cache, Workers and account management (54 actions).",
		Actions:     actions,
	}
}

// --------------------------------------------------------------- dispatch ---

// batch7Lane2Impl turns a catalog row into an ActionFunc.
func batch7Lane2Impl(action batch7Lane2Action) ActionFunc {
	return func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		for _, name := range action.Required {
			if _, present := input[name]; !present {
				return map[string]any{"error": name + " is required"}, nil
			}
		}
		if action.Run != nil {
			return action.Run(ctx, rt, input)
		}
		return batch7Lane2Single(ctx, rt, action, input)
	}
}

// batch7Lane2Single executes the declarative single-request flow.
func batch7Lane2Single(
	ctx context.Context,
	rt *Runtime,
	action batch7Lane2Action,
	input map[string]any,
) (any, error) {
	if strings.Contains(action.Path, "{zone_id}") {
		if failure := batch7Lane2ValidateZoneID(stringArg(input, "zone_id")); failure != nil {
			return failure, nil
		}
	}
	path, fillErr := batch7Lane2FillPath(action.Path, input)
	if fillErr != nil {
		return map[string]any{"error": fillErr.Error()}, nil
	}
	query := url.Values{}
	for _, name := range action.Query {
		if value := strings.TrimSpace(stringArg(input, name)); value != "" {
			query.Set(name, value)
		}
	}
	payload := map[string]any{}
	for field, source := range action.BodyMap {
		value, present := input[source]
		if !present || value == nil {
			continue
		}
		payload[field] = value
	}
	for field, value := range action.Consts {
		payload[field] = value
	}
	var body any
	if len(payload) > 0 {
		body = payload
	}
	reply := batch7Lane2Call(ctx, rt, action.Method, path, query, body)
	if !reply.ok() {
		if action.WrapErrors && action.Wrap != "" {
			return map[string]any{action.Wrap: reply.failure}, nil
		}
		return reply.failure, nil
	}
	if action.Shape != nil {
		return action.Shape(reply, input), nil
	}
	if action.Wrap != "" {
		return map[string]any{action.Wrap: reply.result}, nil
	}
	return reply.result, nil
}

// batch7Lane2FillPath substitutes {token} placeholders from the action input.
func batch7Lane2FillPath(template string, input map[string]any) (string, error) {
	path := template
	for _, token := range []string{
		"zone_id", "account_id", "record_id", "setting_id",
		"rule_id", "member_id", "application_id",
	} {
		placeholder := "{" + token + "}"
		if !strings.Contains(path, placeholder) {
			continue
		}
		value := strings.TrimSpace(stringArg(input, token))
		if value == "" {
			return "", fmt.Errorf("%s is required", token)
		}
		path = strings.ReplaceAll(path, placeholder, url.PathEscape(value))
	}
	return path, nil
}

// batch7Lane2ValidateZoneID mirrors upstream _validate_zone_id.
func batch7Lane2ValidateZoneID(zoneID string) map[string]any {
	if zoneID == "" {
		return map[string]any{"error": "zone_id must be a non-empty string"}
	}
	lowered := strings.ToLower(zoneID)
	valid := len(zoneID) == 32
	if valid {
		for _, char := range lowered {
			if !strings.ContainsRune("0123456789abcdef", char) {
				valid = false
				break
			}
		}
	}
	if !valid {
		return map[string]any{"error": "Invalid zone_id format: " + zoneID}
	}
	return nil
}

// batch7Lane2ValidateDomain mirrors upstream _validate_domain.
func batch7Lane2ValidateDomain(domain string) map[string]any {
	if domain == "" {
		return map[string]any{"error": "domain must be a non-empty string"}
	}
	lowered := strings.ToLower(strings.TrimSpace(domain))
	if len(lowered) < 3 || len(lowered) > 255 {
		return map[string]any{"error": "Domain length must be 3-255 characters: " + lowered}
	}
	for _, char := range lowered {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyz0123456789.-", char) {
			return map[string]any{"error": "Invalid domain format: " + lowered}
		}
	}
	return nil
}

// ------------------------------------------------------------------ wire ---

// batch7Lane2Reply carries a decoded Cloudflare envelope or an error payload.
type batch7Lane2Reply struct {
	status   int
	result   any
	envelope map[string]any
	failure  map[string]any
}

func (reply batch7Lane2Reply) ok() bool { return reply.failure == nil }

// value returns the failure payload when the call failed, mirroring the
// upstream habit of assigning an error dict to a result variable.
func (reply batch7Lane2Reply) value() any {
	if reply.failure != nil {
		return reply.failure
	}
	return reply.result
}

// batch7Lane2Getenv resolves the runtime environment lookup.
func batch7Lane2Getenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// batch7Lane2HTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func batch7Lane2HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch7Lane2Token resolves CLOUDFLARE_API_TOKEN from WithCredential or env.
func batch7Lane2Token(rt *Runtime) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[batch7Lane2ModuleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.resolveToken(batch7Lane2Getenv(rt))
	return strings.TrimSpace(token)
}

// batch7Lane2BaseURL resolves the API root, honouring WithBaseURL.
func batch7Lane2BaseURL(rt *Runtime) string {
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[batch7Lane2ModuleID]; cfg != nil {
			if base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"); base != "" {
				return base
			}
		}
	}
	return batch7Lane2DefaultBase
}

// batch7Lane2Call performs one authenticated request and maps the response.
func batch7Lane2Call(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) batch7Lane2Reply {
	token := batch7Lane2Token(rt)
	if token == "" {
		return batch7Lane2Reply{failure: map[string]any{
			"error": "CLOUDFLARE_API_TOKEN is required",
			"help":  "Set CLOUDFLARE_API_TOKEN env var or configure credential store",
		}}
	}
	target := batch7Lane2BaseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return batch7Lane2Failure("Unexpected error: " + err.Error())
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return batch7Lane2Failure("Unexpected error: " + err.Error())
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := batch7Lane2HTTP(rt).Do(request)
	if err != nil {
		if batch7Lane2IsTimeout(ctx, err) {
			return batch7Lane2Failure("Request timed out")
		}
		return batch7Lane2Failure("Request failed: " + err.Error())
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, batch7Lane2MaxBody))
	if err != nil {
		return batch7Lane2Failure("Request failed: " + err.Error())
	}
	return batch7Lane2DecodeReply(response.StatusCode, response.Header.Get("Retry-After"), raw)
}

// batch7Lane2Failure builds an error reply.
func batch7Lane2Failure(message string) batch7Lane2Reply {
	return batch7Lane2Reply{failure: map[string]any{"error": message}}
}

// batch7Lane2DecodeReply maps one HTTP response to a reply, matching the
// upstream _make_request branches.
func batch7Lane2DecodeReply(status int, retryAfter string, raw []byte) batch7Lane2Reply {
	switch status {
	case http.StatusUnauthorized:
		return batch7Lane2Reply{status: status, failure: map[string]any{
			"error":       "Unauthorized - invalid or missing CLOUDFLARE_API_TOKEN",
			"status_code": status,
		}}
	case http.StatusForbidden:
		return batch7Lane2Reply{status: status, failure: map[string]any{
			"error":       "Forbidden - token lacks required permissions (Zone/DNS read or edit)",
			"status_code": status,
		}}
	case http.StatusNotFound:
		return batch7Lane2Reply{status: status, failure: map[string]any{
			"error":       "Not found",
			"status_code": status,
		}}
	case http.StatusTooManyRequests:
		var hint any
		if retryAfter != "" {
			hint = retryAfter
		}
		return batch7Lane2Reply{status: status, failure: map[string]any{
			"error":       "Too many requests - rate limited",
			"status_code": status,
			"retry_after": hint,
		}}
	case http.StatusNoContent:
		return batch7Lane2Reply{status: status, result: map[string]any{
			"success": true,
			"message": "Operation completed successfully",
		}}
	}
	if status >= 400 {
		message := batch7Lane2ProviderError(raw)
		return batch7Lane2Reply{status: status, failure: map[string]any{
			"error":       fmt.Sprintf("HTTP %d: %s", status, message),
			"status_code": status,
		}}
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return batch7Lane2Failure("Unexpected error: invalid JSON response")
	}
	if success, _ := envelope["success"].(bool); !success {
		message := "Unknown error"
		if detail := batch7Lane2EnvelopeError(envelope); detail != "" {
			message = detail
		}
		return batch7Lane2Failure(message)
	}
	result, present := envelope["result"]
	if !present {
		result = map[string]any{}
	}
	return batch7Lane2Reply{status: status, result: result, envelope: envelope}
}

// batch7Lane2EnvelopeError reads errors[0].message from a decoded envelope.
func batch7Lane2EnvelopeError(envelope map[string]any) string {
	errorsList, _ := envelope["errors"].([]any)
	if len(errorsList) == 0 {
		return ""
	}
	first, _ := errorsList[0].(map[string]any)
	return batch7Lane2TextAt(first, "message")
}

// batch7Lane2ProviderError renders the provider message for a >=400 response,
// falling back to the upstream `response.text[:500]` slice.
func batch7Lane2ProviderError(raw []byte) string {
	if envelope, ok := batch7Lane2DecodeObject(raw); ok {
		if message := batch7Lane2EnvelopeError(envelope); message != "" {
			return message
		}
	}
	return batch7Lane2TruncateText(string(raw), batch7Lane2MaxError)
}

// batch7Lane2IsTimeout reports whether err is a deadline or a net timeout.
func batch7Lane2IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// ------------------------------------------------------------- accessors ---

// batch7Lane2DecodeObject decodes a JSON object body.
func batch7Lane2DecodeObject(raw []byte) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, true
}

// batch7Lane2MapValue views a decoded value as an object.
func batch7Lane2MapValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch7Lane2SliceValue views a decoded value as an array, returning nil when
// the value is not an array (an empty array stays non-nil).
func batch7Lane2SliceValue(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	return items
}

// batch7Lane2TextValue reads a decoded string.
func batch7Lane2TextValue(value any) string {
	text, _ := value.(string)
	return text
}

// batch7Lane2MapAt reads an object field, returning an empty object when the
// field is absent or not an object (the upstream `.get(key, {})` default).
func batch7Lane2MapAt(source map[string]any, key string) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	if object := batch7Lane2MapValue(source[key]); object != nil {
		return object
	}
	return map[string]any{}
}

// batch7Lane2SliceAt reads an array field, returning an empty array when the
// field is absent or not an array.
func batch7Lane2SliceAt(source map[string]any, key string) []any {
	if source == nil {
		return []any{}
	}
	if items := batch7Lane2SliceValue(source[key]); items != nil {
		return items
	}
	return []any{}
}

// batch7Lane2TextAt reads a string field.
func batch7Lane2TextAt(source map[string]any, key string) string {
	if source == nil {
		return ""
	}
	return batch7Lane2TextValue(source[key])
}

// batch7Lane2IntAt reads an integer field with a fallback.
func batch7Lane2IntAt(source map[string]any, key string, fallback int) int {
	if source == nil {
		return fallback
	}
	return batch7Lane2Int(source[key], fallback)
}

// batch7Lane2Int converts a decoded JSON number to int.
func batch7Lane2Int(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		return fallback
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}

// batch7Lane2OptionalInt distinguishes an absent/null argument from 0.
func batch7Lane2OptionalInt(input map[string]any, name string) (int, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	return batch7Lane2Int(value, 0), true
}

// batch7Lane2Clamp bounds an integer to [low, high].
func batch7Lane2Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch7Lane2TruncateText caps provider text the way upstream slices [:500].
func batch7Lane2TruncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// batch7Lane2PayloadError reads an error string out of a payload.
func batch7Lane2PayloadError(payload map[string]any) string {
	return batch7Lane2TextAt(payload, "error")
}

// batch7Lane2ResultArray applies the upstream "result is a list, or a single
// object with an id" normalisation.
func batch7Lane2ResultArray(value any) []any {
	if items := batch7Lane2SliceValue(value); items != nil {
		return items
	}
	if object := batch7Lane2MapValue(value); object != nil {
		if _, present := object["id"]; present {
			return []any{object}
		}
	}
	return []any{}
}

// ------------------------------------------------------------- shapes ---

// batch7Lane2ZoneSummary flattens a zone for the list action.
func batch7Lane2ZoneSummary(source map[string]any) map[string]any {
	return map[string]any{
		"id":           source["id"],
		"name":         batch7Lane2TextAt(source, "name"),
		"status":       batch7Lane2TextAt(source, "status"),
		"name_servers": batch7Lane2TextList(batch7Lane2SliceAt(source, "name_servers")),
		"plan":         batch7Lane2PlanName(batch7Lane2MapAt(source, "plan")),
	}
}

// batch7Lane2ZoneDetail flattens a zone for the get action.
func batch7Lane2ZoneDetail(source map[string]any) map[string]any {
	return map[string]any{
		"id":           source["id"],
		"name":         batch7Lane2TextAt(source, "name"),
		"status":       batch7Lane2TextAt(source, "status"),
		"name_servers": batch7Lane2TextList(batch7Lane2SliceAt(source, "name_servers")),
		"created_on":   batch7Lane2TextAt(source, "created_on"),
		"modified_on":  batch7Lane2TextAt(source, "modified_on"),
		"plan":         batch7Lane2PlanName(batch7Lane2MapAt(source, "plan")),
		"type":         batch7Lane2TextAt(source, "type"),
	}
}

// batch7Lane2PlanName reads plan.name, preserving the upstream None.
func batch7Lane2PlanName(plan map[string]any) any {
	if plan == nil {
		return nil
	}
	return plan["name"]
}

// batch7Lane2TextList renders a decoded array of strings as []string.
func batch7Lane2TextList(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, batch7Lane2TextValue(item))
	}
	return out
}

// batch7Lane2RecordSummary flattens a DNS record for list responses.
func batch7Lane2RecordSummary(source map[string]any) map[string]any {
	return map[string]any{
		"id":       source["id"],
		"type":     batch7Lane2TextAt(source, "type"),
		"name":     batch7Lane2TextAt(source, "name"),
		"content":  source["content"],
		"ttl":      source["ttl"],
		"proxied":  source["proxied"],
		"priority": source["priority"],
	}
}

// batch7Lane2RecordDetail flattens a DNS record for the get action.
func batch7Lane2RecordDetail(source map[string]any) map[string]any {
	detail := batch7Lane2RecordSummary(source)
	detail["created_on"] = batch7Lane2TextAt(source, "created_on")
	detail["modified_on"] = batch7Lane2TextAt(source, "modified_on")
	return detail
}

// batch7Lane2ShapeZoneDetail applies batch7Lane2ZoneDetail to a reply.
func batch7Lane2ShapeZoneDetail(reply batch7Lane2Reply, _ map[string]any) any {
	return batch7Lane2ZoneDetail(batch7Lane2MapValue(reply.result))
}

// batch7Lane2ShapeRecordDetail applies batch7Lane2RecordDetail to a reply.
func batch7Lane2ShapeRecordDetail(reply batch7Lane2Reply, _ map[string]any) any {
	return batch7Lane2RecordDetail(batch7Lane2MapValue(reply.result))
}

// batch7Lane2Dashboard normalises the analytics dashboard payload, which the
// upstream code accepts as either an object or a one-element array.
func batch7Lane2Dashboard(value any) map[string]any {
	if items := batch7Lane2SliceValue(value); items != nil {
		if len(items) == 0 {
			return map[string]any{}
		}
		return batch7Lane2MapValue(items[0])
	}
	return batch7Lane2MapValue(value)
}

// batch7Lane2AllValue reads the "all" bucket of a dashboard metric.
func batch7Lane2AllValue(source map[string]any, key string) any {
	metric := batch7Lane2MapAt(source, key)
	value, present := metric["all"]
	if !present {
		return nil
	}
	return value
}

// batch7Lane2ShapeZoneAnalytics mirrors cloudflare_get_zone_analytics.
func batch7Lane2ShapeZoneAnalytics(reply batch7Lane2Reply, _ map[string]any) any {
	dashboard := batch7Lane2Dashboard(reply.result)
	totals := batch7Lane2MapAt(dashboard, "totals")
	query := batch7Lane2MapAt(dashboard, "query")
	return map[string]any{
		"requests":        batch7Lane2AllValue(totals, "requests"),
		"bandwidth_bytes": batch7Lane2AllValue(totals, "bandwidth"),
		"threats":         batch7Lane2AllValue(totals, "threats"),
		"pageviews":       batch7Lane2AllValue(totals, "pageviews"),
		"uniques":         batch7Lane2AllValue(totals, "uniques"),
		"since":           query["since"],
		"until":           query["until"],
	}
}

// batch7Lane2ShapeTopAnalytics mirrors cloudflare_get_top_analytics.
func batch7Lane2ShapeTopAnalytics(reply batch7Lane2Reply, _ map[string]any) any {
	requests := batch7Lane2MapAt(batch7Lane2MapAt(batch7Lane2Dashboard(reply.result), "totals"), "requests")
	return map[string]any{
		"top_countries":     batch7Lane2MapAt(requests, "country"),
		"top_http_status":   batch7Lane2MapAt(requests, "http_status"),
		"top_content_types": batch7Lane2MapAt(requests, "content_type"),
	}
}

// batch7Lane2ShapePerformance mirrors cloudflare_get_performance_analytics.
func batch7Lane2ShapePerformance(reply batch7Lane2Reply, _ map[string]any) any {
	totals := batch7Lane2MapAt(batch7Lane2Dashboard(reply.result), "totals")
	return map[string]any{"performance_metrics": batch7Lane2MapAt(totals, "performance")}
}

// batch7Lane2ShapeHTTPReport mirrors cloudflare_get_http_analytics_report.
func batch7Lane2ShapeHTTPReport(reply batch7Lane2Reply, _ map[string]any) any {
	requests := batch7Lane2MapAt(batch7Lane2MapAt(batch7Lane2Dashboard(reply.result), "totals"), "requests")
	return map[string]any{
		"status_codes":  batch7Lane2MapAt(requests, "http_status"),
		"content_types": batch7Lane2MapAt(requests, "content_type"),
		"threats":       batch7Lane2MapAt(requests, "threats"),
	}
}

// batch7Lane2ShapePageRules mirrors cloudflare_list_page_rules.
func batch7Lane2ShapePageRules(reply batch7Lane2Reply, _ map[string]any) any {
	rules := make([]map[string]any, 0)
	for _, raw := range batch7Lane2SliceAt(map[string]any{"rules": reply.result}, "rules") {
		source := batch7Lane2MapValue(raw)
		rules = append(rules, map[string]any{
			"id":       source["id"],
			"targets":  source["targets"],
			"actions":  source["actions"],
			"priority": source["priority"],
			"status":   batch7Lane2TextAt(source, "status"),
		})
	}
	return map[string]any{"rules": rules}
}

// batch7Lane2ShapeFirewallRules mirrors cloudflare_list_firewall_rules.
func batch7Lane2ShapeFirewallRules(reply batch7Lane2Reply, _ map[string]any) any {
	entrypoint := batch7Lane2MapValue(reply.result)
	return map[string]any{"firewall_rules": batch7Lane2SliceAt(entrypoint, "rules")}
}

// batch7Lane2ShapeListOrEmpty mirrors the upstream
// `result if isinstance(result, list) else []` wrapper for list endpoints.
func batch7Lane2ShapeListOrEmpty(key string) func(batch7Lane2Reply, map[string]any) any {
	return func(reply batch7Lane2Reply, _ map[string]any) any {
		items := batch7Lane2SliceValue(reply.result)
		if items == nil {
			items = []any{}
		}
		return map[string]any{key: items}
	}
}

// batch7Lane2ShapeCustomHostnames mirrors cloudflare_list_custom_hostnames.
func batch7Lane2ShapeCustomHostnames(reply batch7Lane2Reply, _ map[string]any) any {
	if items := batch7Lane2SliceValue(reply.result); items != nil {
		return map[string]any{"custom_hostnames": items}
	}
	return map[string]any{
		"custom_hostnames": batch7Lane2SliceAt(batch7Lane2MapValue(reply.result), "custom_hostnames"),
	}
}

// batch7Lane2ShapeAccessApplications mirrors cloudflare_list_access_applications.
func batch7Lane2ShapeAccessApplications(reply batch7Lane2Reply, _ map[string]any) any {
	items := batch7Lane2SliceValue(reply.result)
	if items == nil {
		items = []any{}
	}
	return map[string]any{"access_applications": items}
}

// batch7Lane2ShapeAccountDetails mirrors cloudflare_get_account_details.
func batch7Lane2ShapeAccountDetails(reply batch7Lane2Reply, _ map[string]any) any {
	account := batch7Lane2MapValue(reply.result)
	if account == nil {
		if items := batch7Lane2SliceValue(reply.result); len(items) > 0 {
			account = batch7Lane2MapValue(items[0])
		}
	}
	if account == nil {
		account = map[string]any{}
	}
	return map[string]any{
		"id":         account["id"],
		"name":       batch7Lane2TextAt(account, "name"),
		"settings":   batch7Lane2MapAt(account, "settings"),
		"created_on": batch7Lane2TextAt(account, "created_on"),
	}
}

// ------------------------------------------------------- custom actions ---

// batch7Lane2ListZones mirrors cloudflare_list_zones (full envelope).
func batch7Lane2ListZones(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	page := batch7Lane2Clamp(intArg(input, "page", 1), 1, 1<<31-1)
	perPage := batch7Lane2Clamp(intArg(input, "per_page", 20), 1, batch7Lane2MaxPageSize)
	query := url.Values{
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	if name := strings.TrimSpace(stringArg(input, "name")); name != "" {
		query.Set("name", name)
	}
	reply := batch7Lane2Call(ctx, rt, http.MethodGet, "/zones", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	zones := make([]map[string]any, 0)
	for _, raw := range batch7Lane2ResultArray(reply.result) {
		zones = append(zones, batch7Lane2ZoneSummary(batch7Lane2MapValue(raw)))
	}
	total := len(zones)
	if info := batch7Lane2MapValue(reply.envelope["result_info"]); info != nil {
		total = batch7Lane2IntAt(info, "total_count", total)
	}
	return map[string]any{
		"zones": zones, "page": page, "per_page": perPage, "total": total,
	}, nil
}

// batch7Lane2ListDNSRecords mirrors cloudflare_list_dns_records.
func batch7Lane2ListDNSRecords(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	page := batch7Lane2Clamp(intArg(input, "page", 1), 1, 1<<31-1)
	perPage := batch7Lane2Clamp(intArg(input, "per_page", 20), 1, batch7Lane2MaxPageSize)
	query := url.Values{
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	if name := strings.TrimSpace(stringArg(input, "name")); name != "" {
		query.Set("name", name)
	}
	if recordType := strings.TrimSpace(stringArg(input, "type")); recordType != "" {
		query.Set("type", strings.ToUpper(recordType))
	}
	reply := batch7Lane2Call(ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	records := make([]map[string]any, 0)
	if items := batch7Lane2SliceValue(reply.result); items != nil {
		for _, raw := range items {
			records = append(records, batch7Lane2RecordSummary(batch7Lane2MapValue(raw)))
		}
	} else if object := batch7Lane2MapValue(reply.result); object != nil {
		records = append(records, batch7Lane2RecordSummary(object))
	}
	total := len(records)
	if info := batch7Lane2MapValue(reply.envelope["result_info"]); info != nil {
		total = batch7Lane2IntAt(info, "total_count", total)
	}
	return map[string]any{
		"records": records, "zone_id": zoneID, "page": page, "per_page": perPage, "total": total,
	}, nil
}

// batch7Lane2CreateDNSRecord mirrors cloudflare_create_dns_record.
func batch7Lane2CreateDNSRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	payload := map[string]any{
		"type":    strings.ToUpper(stringArg(input, "type")),
		"name":    stringArg(input, "name"),
		"content": stringArg(input, "content"),
		"ttl":     intArg(input, "ttl", 1),
		"proxied": boolArg(input, "proxied", false),
	}
	if priority, present := batch7Lane2OptionalInt(input, "priority"); present {
		payload["priority"] = priority
	}
	reply := batch7Lane2Call(
		ctx, rt, http.MethodPost, "/zones/"+url.PathEscape(zoneID)+"/dns_records", nil, payload,
	)
	if !reply.ok() {
		return map[string]any{"created_record": reply.failure}, nil
	}
	return map[string]any{"created_record": reply.result}, nil
}

// batch7Lane2UpdateDNSRecord mirrors cloudflare_update_dns_record, including
// the upstream "only non-empty fields are sent" rule.
func batch7Lane2UpdateDNSRecord(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	recordID := stringArg(input, "record_id")
	payload := map[string]any{}
	if value := stringArg(input, "type"); value != "" {
		payload["type"] = strings.ToUpper(value)
	}
	if value := stringArg(input, "name"); value != "" {
		payload["name"] = value
	}
	if value := stringArg(input, "content"); value != "" {
		payload["content"] = value
	}
	if ttl := intArg(input, "ttl", 0); ttl != 0 {
		payload["ttl"] = ttl
	}
	if value, present := input["proxied"]; present && value != nil {
		payload["proxied"] = boolArg(input, "proxied", false)
	}
	reply := batch7Lane2Call(
		ctx, rt, http.MethodPatch,
		"/zones/"+url.PathEscape(zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, payload,
	)
	if !reply.ok() {
		return map[string]any{"updated_record": reply.failure}, nil
	}
	return map[string]any{"updated_record": reply.result}, nil
}

// batch7Lane2ListZoneCertificates mirrors cloudflare_list_zone_certificates:
// universal SSL settings plus custom certificates, each surfaced even when one
// of the two calls fails.
func batch7Lane2ListZoneCertificates(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	base := "/zones/" + url.PathEscape(zoneID)
	universal := batch7Lane2Call(ctx, rt, http.MethodGet, base+"/ssl/universal/settings", nil, nil)
	custom := batch7Lane2Call(ctx, rt, http.MethodGet, base+"/custom_certificates", nil, nil)
	certificates := batch7Lane2SliceValue(custom.value())
	if certificates == nil {
		certificates = []any{}
	}
	return map[string]any{
		"universal_ssl":       universal.value(),
		"custom_certificates": certificates,
	}, nil
}

// batch7Lane2DomainHealth mirrors cloudflare_check_domain_dns_health.
func batch7Lane2DomainHealth(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	rawDomain := stringArg(input, "domain")
	if failure := batch7Lane2ValidateDomain(rawDomain); failure != nil {
		return failure, nil
	}
	domain := strings.ToLower(strings.TrimSpace(rawDomain))
	zoneMissing := []any{map[string]any{
		"code": "ZONE_NOT_FOUND", "message": "No zone found for domain " + domain,
	}}
	zonesReply := batch7Lane2Call(ctx, rt, http.MethodGet, "/zones", url.Values{
		"name":     {domain},
		"per_page": {"1"},
	}, nil)
	if !zonesReply.ok() {
		return map[string]any{
			"domain": domain, "zone_found": false,
			"error":  batch7Lane2PayloadError(zonesReply.failure),
			"issues": zoneMissing,
		}, nil
	}
	var zone map[string]any
	if zones := batch7Lane2SliceValue(zonesReply.result); len(zones) > 0 {
		zone = batch7Lane2MapValue(zones[0])
	}
	if zone == nil || batch7Lane2TextAt(zone, "name") != domain {
		return map[string]any{
			"domain": domain, "zone_found": false,
			"issues":  zoneMissing,
			"summary": "Zone not found for " + domain + ".",
		}, nil
	}
	zoneID := batch7Lane2TextAt(zone, "id")
	zoneStatus := batch7Lane2TextAt(zone, "status")
	recordsReply := batch7Lane2Call(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/dns_records",
		url.Values{"per_page": {"100"}}, nil,
	)
	if !recordsReply.ok() {
		return map[string]any{
			"domain": domain, "zone_found": true, "zone_status": zoneStatus,
			"error": batch7Lane2PayloadError(recordsReply.failure),
			"issues": []any{map[string]any{
				"code": "FETCH_ERROR", "message": "Failed to fetch DNS records",
			}},
		}, nil
	}
	records := batch7Lane2SliceValue(recordsReply.result)
	if records == nil {
		records = []any{}
	}
	rootRecords := make([]any, 0)
	wwwRecords := make([]any, 0)
	cnameRecords := make([]any, 0)
	mxRecords := make([]any, 0)
	nsRecords := make([]any, 0)
	for _, raw := range records {
		record := batch7Lane2MapValue(raw)
		name := batch7Lane2TextAt(record, "name")
		recordType := batch7Lane2TextAt(record, "type")
		if name == domain && (recordType == "A" || recordType == "AAAA") {
			rootRecords = append(rootRecords, raw)
		}
		if name == "www."+domain && (recordType == "A" || recordType == "AAAA") {
			wwwRecords = append(wwwRecords, raw)
		}
		switch recordType {
		case "CNAME":
			cnameRecords = append(cnameRecords, raw)
		case "MX":
			mxRecords = append(mxRecords, raw)
		case "NS":
			nsRecords = append(nsRecords, raw)
		}
	}
	issues := make([]any, 0)
	if zoneStatus != "active" {
		issues = append(issues, map[string]any{
			"code":    "ZONE_INACTIVE",
			"message": "Zone status is " + zoneStatus + ", should be 'active'",
		})
	}
	if len(rootRecords) == 0 {
		issues = append(issues, map[string]any{
			"code":    "ROOT_MISSING",
			"message": "No A/AAAA records found for root domain " + domain,
		})
	}
	if len(wwwRecords) == 0 && len(cnameRecords) == 0 {
		issues = append(issues, map[string]any{
			"code":    "WWW_MISSING",
			"message": "No www DNS record (A/AAAA or CNAME) found for " + domain,
		})
	}
	if len(mxRecords) == 0 {
		issues = append(issues, map[string]any{
			"code":    "MX_MISSING",
			"message": "No MX records configured for " + domain,
		})
	}
	for _, raw := range append(append([]any{}, rootRecords...), wwwRecords...) {
		record := batch7Lane2MapValue(raw)
		if proxied, _ := record["proxied"].(bool); !proxied {
			continue
		}
		if batch7Lane2TextAt(record, "content") == "" {
			issues = append(issues, map[string]any{
				"code":    "PROXY_INVALID",
				"message": "Proxied record " + batch7Lane2TextAt(record, "name") + " has no target",
			})
		}
	}
	summary := fmt.Sprintf("Zone is %s. Found %d DNS records.", zoneStatus, len(records))
	if len(issues) > 0 {
		codes := make([]string, 0, len(issues))
		for _, raw := range issues {
			codes = append(codes, batch7Lane2TextAt(batch7Lane2MapValue(raw), "code"))
		}
		summary += " Issues detected: " + strings.Join(codes, ", ") + "."
	} else {
		summary += " No obvious DNS configuration issues detected."
	}
	return map[string]any{
		"domain":        domain,
		"zone_found":    true,
		"zone_id":       zoneID,
		"zone_status":   zoneStatus,
		"root_records":  rootRecords,
		"www_records":   wwwRecords,
		"mx_records":    mxRecords,
		"ns_records":    nsRecords,
		"total_records": len(records),
		"issues":        issues,
		"summary":       summary,
	}, nil
}

// batch7Lane2ListFirewallEvents mirrors cloudflare_list_firewall_events.
func batch7Lane2ListFirewallEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	limit := batch7Lane2Clamp(intArg(input, "limit", 10), 0, 1<<31-1)
	query := url.Values{"per_page": {strconv.Itoa(batch7Lane2Clamp(limit, 0, 50))}}
	reply := batch7Lane2Call(ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/firewall/events", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	events := batch7Lane2SliceValue(reply.result)
	if events == nil {
		events = batch7Lane2SliceAt(batch7Lane2MapValue(reply.result), "events")
	}
	if limit < len(events) {
		events = events[:limit]
	}
	return map[string]any{"events": events}, nil
}

// batch7Lane2SettingValue renders one /settings/<name> response the way the
// upstream `.get("value")` does, reporting whether the payload was an object.
func batch7Lane2SettingValue(reply batch7Lane2Reply) (any, bool) {
	if reply.failure != nil {
		return nil, true
	}
	object := batch7Lane2MapValue(reply.result)
	if object == nil {
		return nil, false
	}
	return object["value"], true
}

// batch7Lane2SettingProbe names one setting request and its output key.
type batch7Lane2SettingProbe struct {
	Output  string
	Setting string
}

// batch7Lane2GetSettings performs an ordered group of setting reads.
func batch7Lane2GetSettings(
	ctx context.Context,
	rt *Runtime,
	zoneID string,
	probes []batch7Lane2SettingProbe,
) map[string]any {
	out := make(map[string]any, len(probes))
	for _, probe := range probes {
		reply := batch7Lane2Call(
			ctx, rt, http.MethodGet,
			"/zones/"+url.PathEscape(zoneID)+"/settings/"+probe.Setting, nil, nil,
		)
		value, _ := batch7Lane2SettingValue(reply)
		out[probe.Output] = value
	}
	return out
}

// batch7Lane2GetSecuritySettings mirrors cloudflare_get_security_settings.
func batch7Lane2GetSecuritySettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	level, _ := batch7Lane2SettingValue(batch7Lane2Call(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/security_level", nil, nil,
	))
	ssl, _ := batch7Lane2SettingValue(batch7Lane2Call(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/ssl", nil, nil,
	))
	wafValue, wafIsObject := batch7Lane2SettingValue(batch7Lane2Call(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/settings/waf", nil, nil,
	))
	var wafEnabled any
	if wafIsObject {
		text, _ := wafValue.(string)
		wafEnabled = text == "on"
	}
	return map[string]any{
		"security_level": level,
		"ssl_mode":       ssl,
		"waf_enabled":    wafEnabled,
	}, nil
}

// batch7Lane2GetSpeedSettings mirrors cloudflare_get_speed_settings.
func batch7Lane2GetSpeedSettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return batch7Lane2GetSettings(ctx, rt, zoneID, []batch7Lane2SettingProbe{
		{Output: "minify", Setting: "minify"},
		{Output: "brotli", Setting: "brotli"},
		{Output: "rocket_loader", Setting: "rocket_loader"},
	}), nil
}

// batch7Lane2GetCacheSettings mirrors cloudflare_get_cache_settings.
func batch7Lane2GetCacheSettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return batch7Lane2GetSettings(ctx, rt, zoneID, []batch7Lane2SettingProbe{
		{Output: "browser_cache_ttl", Setting: "browser_cache_ttl"},
		{Output: "development_mode", Setting: "development_mode"},
		{Output: "cache_level", Setting: "cache_level"},
	}), nil
}

// batch7Lane2GetHTTPConfig mirrors cloudflare_get_http_config.
func batch7Lane2GetHTTPConfig(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return batch7Lane2GetSettings(ctx, rt, zoneID, []batch7Lane2SettingProbe{
		{Output: "http2", Setting: "http2"},
		{Output: "http3", Setting: "http3"},
		{Output: "0rtt", Setting: "0rtt"},
	}), nil
}

// batch7Lane2GetNetworkSettings mirrors cloudflare_get_network_settings.
func batch7Lane2GetNetworkSettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return batch7Lane2GetSettings(ctx, rt, zoneID, []batch7Lane2SettingProbe{
		{Output: "websockets", Setting: "websockets"},
		{Output: "onion_routing", Setting: "onion_routing"},
		{Output: "ipv6", Setting: "ipv6"},
	}), nil
}

// batch7Lane2CreateFirewallRule mirrors cloudflare_create_firewall_rule,
// including the PUT fallback when the phase entrypoint does not exist yet.
func batch7Lane2CreateFirewallRule(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	ruleAction := strings.ToLower(strings.TrimSpace(stringArg(input, "rule_action")))
	if ruleAction == "" {
		return map[string]any{"error": "rule_action is required"}, nil
	}
	expression := stringArg(input, "expression")
	if expression == "" {
		return map[string]any{"error": "expression is required"}, nil
	}
	description := stringArg(input, "description")
	if description == "" {
		description = "Rule created via MCP"
	}
	rule := map[string]any{
		"action":      ruleAction,
		"expression":  expression,
		"description": description,
		"enabled":     true,
	}
	base := "/zones/" + url.PathEscape(zoneID) + "/rulesets/phases/http_request_firewall_custom/entrypoint"
	reply := batch7Lane2Call(ctx, rt, http.MethodPost, base+"/rules", nil, rule)
	if reply.ok() {
		return map[string]any{"created_rules": reply.result}, nil
	}
	if reply.status != http.StatusNotFound {
		return map[string]any{"created_rules": reply.failure}, nil
	}
	reply = batch7Lane2Call(ctx, rt, http.MethodPut, base, nil, map[string]any{"rules": []any{rule}})
	if !reply.ok() {
		return map[string]any{"created_rules": reply.failure}, nil
	}
	return map[string]any{"created_rules": reply.result}, nil
}

// batch7Lane2ListAdvancedServices mirrors cloudflare_list_advanced_services.
func batch7Lane2ListAdvancedServices(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := batch7Lane2ValidateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	workers := batch7Lane2Call(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/workers/scripts", nil, nil,
	)
	loadBalancers := batch7Lane2Call(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/load_balancers", nil, nil,
	)
	workerCount := len(batch7Lane2SliceValue(workers.value()))
	balancers := batch7Lane2SliceValue(loadBalancers.value())
	if balancers == nil {
		balancers = []any{}
	}
	return map[string]any{
		"workers_count":        workerCount,
		"load_balancers_count": len(balancers),
		"load_balancers":       balancers,
	}, nil
}

// batch7Lane2ListAccounts mirrors cloudflare_list_accounts.
func batch7Lane2ListAccounts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	page := intArg(input, "page", 1)
	perPage := intArg(input, "per_page", 20)
	if perPage > 50 {
		perPage = 50
	}
	query := url.Values{
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	reply := batch7Lane2Call(ctx, rt, http.MethodGet, "/accounts", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	accounts := make([]map[string]any, 0)
	items := batch7Lane2SliceValue(reply.result)
	if items == nil {
		items = batch7Lane2SliceAt(batch7Lane2MapValue(reply.result), "accounts")
	}
	for _, raw := range items {
		account := batch7Lane2MapValue(raw)
		accounts = append(accounts, map[string]any{
			"id":     account["id"],
			"name":   batch7Lane2TextAt(account, "name"),
			"status": batch7Lane2TextAt(account, "status"),
		})
	}
	total := len(accounts)
	if info := batch7Lane2MapValue(reply.envelope["result_info"]); info != nil {
		total = batch7Lane2IntAt(info, "total_count", batch7Lane2IntAt(info, "count", total))
	}
	return map[string]any{
		"accounts": accounts, "total": total, "page": page, "per_page": perPage,
	}, nil
}

// batch7Lane2ListAuditLogs mirrors cloudflare_list_audit_logs.
func batch7Lane2ListAuditLogs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := batch7Lane2Clamp(intArg(input, "limit", 20), 0, 1<<31-1)
	query := url.Values{"per_page": {strconv.Itoa(batch7Lane2Clamp(limit, 0, 50))}}
	reply := batch7Lane2Call(
		ctx, rt, http.MethodGet,
		"/accounts/"+url.PathEscape(stringArg(input, "account_id"))+"/audit_logs", query, nil,
	)
	if !reply.ok() {
		return reply.failure, nil
	}
	logs := batch7Lane2SliceValue(reply.result)
	if logs == nil {
		logs = batch7Lane2SliceAt(batch7Lane2MapValue(reply.result), "result")
	}
	if limit < len(logs) {
		logs = logs[:limit]
	}
	return map[string]any{"audit_logs": logs}, nil
}
