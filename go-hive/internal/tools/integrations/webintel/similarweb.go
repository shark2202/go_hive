package webintel

// SimilarWeb V5 module: the whole upstream surface is table-driven — every
// action is a GET against the website-analysis/segment-analysis API with an
// api-key header, so one endpoint table plus one executor covers all 29
// upstream functions. Successful bodies are returned unchanged, exactly like
// the upstream `response.json()`.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ---------------------------------------------------------- SimilarWeb V5 ---

// swAction is one row of the table-driven SimilarWeb port: the
// upstream action name, endpoint, JSON-schema params, and how inputs map onto
// the query string.
type swAction struct {
	action   string
	summary  string
	path     string
	params   map[string]any
	required []string
	// send lists input names forwarded as query parameters (empty values are
	// dropped, like the upstream optional-date handling).
	send []string
	// rename maps input names onto provider field names (segment_id ->
	// segment).
	rename map[string]string
	// constants are always sent (metrics, web_source).
	constants map[string]any
}

// swDomainAction is the plain domain-only endpoint shape.
func swDomainAction(action, summary, path string) swAction {
	return swAction{
		action:   action,
		summary:  summary,
		path:     path,
		params:   map[string]any{"domain": param("string")},
		required: []string{"domain"},
		send:     []string{"domain"},
	}
}

// swRangeAction is the domain + start/end + country shape.
func swRangeAction(action, summary, path string) swAction {
	return swAction{
		action:  action,
		summary: summary,
		path:    path,
		params: map[string]any{
			"domain":     param("string"),
			"start_date": paramDefault("string", ""),
			"end_date":   paramDefault("string", ""),
			"country":    paramDefault("string", "world"),
		},
		required: []string{"domain"},
		send:     []string{"domain", "start_date", "end_date", "country"},
	}
}

// swGeographyAction is the domain + start/end shape without a
// country filter.
func swGeographyAction(action, summary, path string) swAction {
	return swAction{
		action:  action,
		summary: summary,
		path:    path,
		params: map[string]any{
			"domain":     param("string"),
			"start_date": paramDefault("string", ""),
			"end_date":   paramDefault("string", ""),
		},
		required: []string{"domain"},
		send:     []string{"domain", "start_date", "end_date"},
	}
}

// similarWebActions is the entire upstream V5 surface; every entry
// is executed by swExecute.
var similarWebActions = []swAction{
	{
		action:  "similarweb_v5_traffic_and_engagement",
		summary: "Get traffic and engagement metrics for a website using V5 API.",
		path:    "/website-analysis/websites/traffic-and-engagement",
		params: map[string]any{
			"domain":      param("string"),
			"start_date":  paramDefault("string", ""),
			"end_date":    paramDefault("string", ""),
			"country":     paramDefault("string", "world"),
			"granularity": paramDefault("string", "monthly"),
		},
		required: []string{"domain"},
		send:     []string{"domain", "start_date", "end_date", "country", "granularity"},
		constants: map[string]any{
			"metrics":    "visits,bounce_rate,avg_visit_duration,pages_per_visit,total_page_views",
			"web_source": "desktop",
		},
	},
	swDomainAction(
		"similarweb_v5_website_rank",
		"Get global, country, and category ranks for a website.",
		"/website-analysis/websites/website-rank",
	),
	swRangeAction(
		"similarweb_v5_traffic_sources",
		"Get marketing channels (traffic sources) breakdown for a website.",
		"/website-analysis/websites/traffic-sources",
	),
	swGeographyAction(
		"similarweb_v5_geography",
		"Get traffic distribution by geography for a website.",
		"/website-analysis/websites/traffic-geography",
	),
	swDomainAction(
		"similarweb_v5_demographics",
		"Get audience demographics (age and gender) for a website.",
		"/website-analysis/websites/demographics/aggregated",
	),
	swDomainAction(
		"similarweb_v5_company_info",
		"Get company information (HQ, industry, etc.) for a website domain.",
		"/website-analysis/websites/company-info/company-info",
	),
	{
		action:   "similarweb_v5_top_sites_by_category",
		summary:  "Get top sites in a specific category (e.g., 'Games', 'Lifestyle').",
		path:     "/website-analysis/websites/top-sites-by-category/aggregated",
		params:   map[string]any{"category": param("string"), "country": paramDefault("string", "world")},
		required: []string{"category"},
		send:     []string{"category", "country"},
	},
	swRangeAction(
		"similarweb_v5_referrals",
		"Get detailed referral traffic sources for a domain.",
		"/website-analysis/websites/referrals/aggregated",
	),
	swRangeAction(
		"similarweb_v5_ppc_spend",
		"Get estimated PPC spend for a website domain.",
		"/website-analysis/websites/ppc-spend",
	),
	swGeographyAction(
		"similarweb_v5_geography_details",
		"Get detailed traffic distribution by country (aggregated).",
		"/website-analysis/websites/geography/aggregated",
	),
	swDomainAction(
		"similarweb_v5_similar_sites",
		"Get a list of websites similar to the given domain.",
		"/website-analysis/websites/similar-sites/aggregated",
	),
	swRangeAction(
		"similarweb_v5_ad_networks",
		"Get performance data across different ad networks for a domain.",
		"/website-analysis/ad-networks/aggregated",
	),
	swDomainAction(
		"similarweb_v5_demographics_traffic",
		"Get traffic breakdown by audience demographic segments.",
		"/website-analysis/websites/traffic-by-demographics/aggregated",
	),
	{
		action:  "similarweb_v5_deduplicated_audience",
		summary: "Get unique visitor count across multiple domains (comma-separated).",
		path:    "/website-analysis/websites/deduplicated-audience",
		params: map[string]any{
			"domains":    param("string"),
			"start_date": paramDefault("string", ""),
			"end_date":   paramDefault("string", ""),
			"country":    paramDefault("string", "world"),
		},
		required: []string{"domains"},
		send:     []string{"domains", "start_date", "end_date", "country"},
	},
	swDomainAction(
		"similarweb_v5_audience_interests",
		"Get interests and categories relevant to the website's audience.",
		"/website-analysis/websites/audience-interests/aggregated",
	),
	{
		action:   "similarweb_v5_audience_overlap",
		summary:  "Get shared audience between the main domain and a comparison domain.",
		path:     "/website-analysis/websites/audience-overlap/aggregated",
		params:   map[string]any{"domain": param("string"), "compare_to": param("string")},
		required: []string{"domain", "compare_to"},
		send:     []string{"domain", "compare_to"},
	},
	swDomainAction(
		"similarweb_v5_technologies",
		"Get technologies used on the website (CMS, Ads, Analytics, etc.).",
		"/website-analysis/websites/technologies/aggregated",
	),
	swRangeAction(
		"similarweb_v5_leading_folders",
		"Get top sub-folders by traffic for a domain.",
		"/website-analysis/websites/pages/leading-folders/aggregated",
	),
	swRangeAction(
		"similarweb_v5_popular_pages",
		"Get most visited pages on the given domain.",
		"/website-content/pages/popular-pages/aggregated",
	),
	swRangeAction(
		"similarweb_v5_subdomains",
		"Get traffic breakdown by subdomain for a domain.",
		"/website-content/subdomains/aggregated",
	),
	swDomainAction(
		"similarweb_v5_keyword_competitors",
		"Get organic and paid keyword competitors for a domain.",
		"/website-analysis/websites/keywords-competitors/aggregated",
	),
	swDomainAction(
		"similarweb_v5_keyword_opportunities",
		"Get keyword gap analysis and opportunities for a domain.",
		"/website-analysis/websites/keywords-opportunities/aggregated",
	),
	swDomainAction(
		"similarweb_v5_serp_features",
		"Get SERP features analysis for the domain.",
		"/website-analysis/websites/keywords/serp-features",
	),
	swRangeAction(
		"similarweb_v5_organic_keywords",
		"Get detailed organic keyword performance for a domain.",
		"/website-analysis/websites/keywords/organic/aggregated",
	),
	swRangeAction(
		"similarweb_v5_paid_keywords",
		"Get detailed paid keyword performance for a domain.",
		"/website-analysis/websites/keywords/paid/aggregated",
	),
	swDomainAction(
		"similarweb_v5_serp_players",
		"Get top websites driving search traffic for keywords (SERP players).",
		"/website-analysis/websites/keywords/serp-players/aggregated",
	),
	swRangeAction(
		"similarweb_v5_social_referrals",
		"Get traffic distribution from social networks for a domain.",
		"/website-analysis/websites/social-referrals/aggregated",
	),
	swDomainAction(
		"similarweb_v5_segments_list",
		"List custom segments available for the domain.",
		"/segment-analysis/segments/describe",
	),
	{
		action:  "similarweb_v5_segment_analysis",
		summary: "Get traffic and engagement for a specific segment ID.",
		path:    "/segment-analysis/segments/traffic-and-engagement",
		params: map[string]any{
			"segment_id": param("string"),
			"start_date": paramDefault("string", ""),
			"end_date":   paramDefault("string", ""),
			"country":    paramDefault("string", "world"),
		},
		required: []string{"segment_id"},
		send:     []string{"segment_id", "start_date", "end_date", "country"},
		rename:   map[string]string{"segment_id": "segment"},
	},
}

// similarWebTool mirrors the upstream module as one Go tool.
var similarWebTool = buildSimilarWebTool()

// buildSimilarWebTool projects the endpoint table onto catalog
// actions.
func buildSimilarWebTool() *Tool {
	actions := make([]Action, 0, len(similarWebActions))
	for _, endpoint := range similarWebActions {
		actions = append(actions, Action{
			Name:     endpoint.action,
			Summary:  endpoint.summary,
			Source:   "similarweb_tool/similarweb_tool.py",
			Params:   endpoint.params,
			Required: endpoint.required,
		})
	}
	return &Tool{
		ID:          "similarweb_tool",
		Name:        "similarweb",
		DisplayName: "SimilarWeb",
		Summary:     "SimilarWeb integration (29 actions).",
		Actions:     actions,
	}
}

// similarWebImpls binds every table row to the shared executor.
func similarWebImpls() map[string]ActionFunc {
	impls := make(map[string]ActionFunc, len(similarWebActions))
	for _, endpoint := range similarWebActions {
		impls[endpoint.action] = swExecute(endpoint)
	}
	return impls
}

// swExecute performs one V5 GET and returns the decoded body
// unchanged. Failures keep the upstream "HTTP error <status>: <text>" and
// "Request failed: <error>" wording.
func swExecute(endpoint swAction) ActionFunc {
	return func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		token := moduleToken(rt, "similarweb_tool")
		if token == "" {
			return map[string]any{
				"error": "SimilarWeb credentials not configured",
				"help":  "Set SIMILARWEB_API_KEY environment variable or configure via credential store. Get a key at https://developer.similarweb.com/",
			}, nil
		}
		query := url.Values{}
		for _, name := range endpoint.send {
			value := stringArg(input, name)
			if value == "" {
				continue
			}
			key := name
			if renamed, ok := endpoint.rename[name]; ok {
				key = renamed
			}
			query.Set(key, value)
		}
		for name, value := range endpoint.constants {
			query.Set(name, fmt.Sprint(value))
		}
		target := joinQuery(moduleBaseURL(rt, "similarweb_tool")+endpoint.path, query)
		resp, err := doHTTP(ctx, rt, http.MethodGet, target, map[string]string{
			"api-key": token,
			"Accept":  "application/json",
		}, nil)
		if err != nil {
			return map[string]any{"error": "Request failed: " + err.Error()}, nil
		}
		if resp.Status >= 400 {
			return map[string]any{"error": fmt.Sprintf("HTTP error %d: %s", resp.Status, string(resp.Body))}, nil
		}
		if !resp.Decoded {
			return map[string]any{"error": "Request failed: invalid JSON response"}, nil
		}
		return resp.Parsed, nil
	}
}
