package cloudflare

// Infrastructure & zone rows: zone inventory, settings, SSL and DNSSEC.

import "net/http"

var zoneRows = []actionSpec{
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
		Run: listZones,
	},
	{
		Name:     "cloudflare_get_zone",
		Summary:  "Get details for a specific Cloudflare zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeZoneDetail,
	},
	{
		Name:     "cloudflare_get_zone_settings",
		Summary:  "Get all common settings for a zone (TLS, IPv6, WebSockets, etc.).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/settings",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "settings",
	},
	{
		Name:     "cloudflare_list_zone_custom_pages",
		Summary:  "List custom error and challenge pages for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/custom_pages",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeListOrEmpty("custom_pages"),
	},
	{
		Name:     "cloudflare_get_ssl_verification",
		Summary:  "Get SSL certificate verification status for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/ssl/verification",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "ssl_verification",
	},
	{
		Name:     "cloudflare_list_zone_certificates",
		Summary:  "List SSL/TLS certificates for a zone (universal plus custom).",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/ssl/universal/settings",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Run:      listZoneCertificates,
	},
	{
		Name:     "cloudflare_list_zone_subscriptions",
		Summary:  "List active paid subscriptions/apps for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/subscriptions",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeListOrEmpty("subscriptions"),
	},
	{
		Name:     "cloudflare_get_dnssec_status",
		Summary:  "Get DNSSEC status and configuration for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/dnssec",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Wrap:     "dnssec",
	},
	{
		Name:       "cloudflare_update_zone_setting",
		Summary:    "Update a specific zone setting (IPv6, WebSockets, ssl, etc.).",
		Method:     http.MethodPatch,
		Path:       "/zones/{zone_id}/settings/{setting_id}",
		Params:     zoneParams(map[string]any{"setting_id": param("string"), "value": anyParam()}),
		Required:   []string{"zone_id", "setting_id", "value"},
		BodyMap:    map[string]string{"value": "value"},
		Wrap:       "updated_setting",
		WrapErrors: true,
	},
}
