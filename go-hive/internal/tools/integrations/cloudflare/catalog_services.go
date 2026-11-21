package cloudflare

// Advanced services & routing rows: custom hostnames, Zero Trust access,
// R2, Pages, worker routes and SSL mode.

import "net/http"

var serviceRows = []actionSpec{
	{
		Name:     "cloudflare_list_custom_hostnames",
		Summary:  "List custom hostnames (SSL for SaaS) for a zone.",
		Method:   http.MethodGet,
		Path:     "/zones/{zone_id}/custom_hostnames",
		Params:   zoneParams(nil),
		Required: []string{"zone_id"},
		Shape:    shapeCustomHostnames,
	},
	{
		Name:     "cloudflare_list_access_applications",
		Summary:  "List Cloudflare Access (Zero Trust) applications for an account.",
		Method:   http.MethodGet,
		Path:     "/accounts/{account_id}/access/apps",
		Params:   map[string]any{"account_id": param("string")},
		Required: []string{"account_id"},
		Shape:    shapeAccessApplications,
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
			"include":        listParam("object"),
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
		Params: zoneParams(map[string]any{
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
		Params:     zoneParams(map[string]any{"mode": param("string")}),
		Required:   []string{"zone_id", "mode"},
		BodyMap:    map[string]string{"value": "mode"},
		Wrap:       "ssl_update",
		WrapErrors: true,
	},
}
