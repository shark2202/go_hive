package cloudflare

// Performance & speed rows: speed/cache/HTTP/network settings, cache purge
// and the Workers script + load balancer summary.

import "net/http"

var performanceRows = []actionSpec{
	{
		Name:     "cloudflare_get_speed_settings",
		Summary:  "Check speed optimization settings (Minify, Brotli, Rocket Loader).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/minify",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Run:      getSpeedSettings,
	},
	{
		Name:     "cloudflare_get_cache_settings",
		Summary:  "Get cache settings for a zone (browser TTL, dev mode, cache level).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/browser_cache_ttl",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Run:      getCacheSettings,
	},
	{
		Name:     "cloudflare_get_http_config",
		Summary:  "Get HTTP protocol settings (HTTP/2, HTTP/3, 0-RTT) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/http2",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Run:      getHTTPConfig,
	},
	{
		Name:     "cloudflare_get_network_settings",
		Summary:  "Get network-level performance settings (WebSockets, Onion Routing, IPv6).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings/websockets",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Run:      getNetworkSettings,
	},
	{
		Name:       "cloudflare_purge_cache_all",
		Summary:    "Purge everything from the Cloudflare cache for this zone.",
		Method:     http.MethodPost,
		Path:       "/zones/{zone_id}/purge_cache",
		Params:     zoneParams(nil),
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
		Params:     zoneParams(map[string]any{"urls": listParam("string")}),
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
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Run:      listAdvancedServices,
	},
}
