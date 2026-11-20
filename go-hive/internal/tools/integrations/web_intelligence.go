// Batch 7, lane 6 of the integration port: web-intelligence and location
// modules from the Python upstream — similarweb_tool (29 actions), news_tool
// (7) and google_maps_tool (6); 42 actions in total.
//
// SimilarWeb is fully table-driven: every action is a GET against the V5
// website-analysis/segment-analysis API with an api-key header, so one endpoint
// table plus one executor covers all 29 upstream functions. Successful bodies
// are returned unchanged, exactly like the upstream `response.json()`.
//
// News keeps the upstream two-provider model: NewsData is primary and Finlight
// is the fallback (and the only provider for sentiment). 429 responses retry
// with exponential backoff up to three times, then the other provider is
// tried; when both fail the upstream "All providers failed" envelope is
// returned. The backoff sleeper is a package variable so tests run without
// real delays.
//
// Google Maps keeps the upstream split between transport/HTTP failures and
// API-level status values: non-200 responses become "HTTP <status>:
// <body[:200]>", API statuses (REQUEST_DENIED, OVER_QUERY_LIMIT, ...) map onto
// the upstream message table, and OK/ZERO_RESULTS pass through.
//
// Deliberate limits:
//   - SimilarWeb and Google Maps credentials are single API keys; the news
//     module keeps per-provider keys because either provider may be absent.
//   - Every request goes through Runtime.HTTP so each action is exercisable
//     against an httptest server; base URLs honour WithBaseURL.
//   - Google Maps geo/place URLs are the classic Web Service endpoints, not
//     the newer Places API (New); upstream uses the same ones.
package integrations

import (
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
	batch7Lane6MaxBody = 8 << 20
	// batch7Lane6HTTPText caps provider error bodies like upstream
	// response.text[:200].
	batch7Lane6HTTPText = 200
	// batch7Lane6Retries is the upstream max_retries for 429 backoff.
	batch7Lane6Retries = 3
)

// batch7Lane6Sleep is the rate-limit backoff hook; tests replace it so the
// upstream 1s/2s/4s waits do not slow the suite down.
var batch7Lane6Sleep = time.Sleep

// InstallBatch7Lane6 publishes the batch-7 lane-6 tools (similarweb, news,
// google_maps) into a shared registry and returns the tool count.
func InstallBatch7Lane6(target *tools.ToolRegistry) int {
	return newBatch7Lane6Registry().Install(target)
}

// newBatch7Lane6Registry builds a registry containing only the lane-6 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane6Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane6Modules(),
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
	for _, tool := range []*Tool{
		batch7Lane6SimilarWebTool,
		batch7Lane6NewsTool,
		batch7Lane6GoogleMapsTool,
	} {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for _, entry := range []struct {
		tool  string
		funcs map[string]ActionFunc
	}{
		{"similarweb", batch7Lane6SimilarWebImpls()},
		{"news", batch7Lane6NewsImpls()},
		{"google_maps", batch7Lane6GoogleMapsImpls()},
	} {
		for action, impl := range entry.funcs {
			registry.addImpl(entry.tool, action, impl)
		}
	}
	return registry
}

// batch7Lane6Modules declares the credential variables and public endpoints
// for the modules. The Finlight entry is a credential/base-URL carrier for the
// news fallback provider, not a tool of its own. Tests override BaseURL with
// WithBaseURL.
func batch7Lane6Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"similarweb_tool": {
			ID:      "similarweb_tool",
			BaseURL: "https://api.similarweb.com/v5",
			EnvVars: []string{"SIMILARWEB_API_KEY"},
		},
		"news_tool": {
			ID:      "news_tool",
			BaseURL: "https://newsdata.io/api/1",
			EnvVars: []string{"NEWSDATA_API_KEY"},
		},
		"finlight_tool": {
			ID:      "finlight_tool",
			BaseURL: "https://api.finlight.me/v2",
			EnvVars: []string{"FINLIGHT_API_KEY"},
		},
		"google_maps_tool": {
			ID:      "google_maps_tool",
			BaseURL: "https://maps.googleapis.com/maps/api",
			EnvVars: []string{"GOOGLE_MAPS_API_KEY"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch7Lane6Base resolves a module base URL, honouring WithBaseURL.
func batch7Lane6Base(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch7Lane6Token resolves the module credential from WithCredential or the
// configured environment lookup.
func batch7Lane6Token(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	token, _ := cfg.ResolvedToken(getenv)
	return strings.TrimSpace(token)
}

// batch7Lane6Clock reads the runtime clock, defaulting to time.Now.
func batch7Lane6Clock(rt *Runtime) time.Time {
	if rt != nil && rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// batch7Lane6IsTimeout reports whether err is a deadline or a net timeout.
func batch7Lane6IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch7Lane6Truncate caps text at limit runes, matching upstream slicing on
// Python strings.
func batch7Lane6Truncate(text string, limit int) string {
	if len([]rune(text)) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// batch7Lane6AsMap safely views a decoded JSON object.
func batch7Lane6AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch7Lane6MapOrEmpty views a decoded object, substituting {} when absent.
func batch7Lane6MapOrEmpty(value any) map[string]any {
	if object := batch7Lane6AsMap(value); object != nil {
		return object
	}
	return map[string]any{}
}

// batch7Lane6AsSlice safely views a decoded JSON array.
func batch7Lane6AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch7Lane6SliceOrEmpty views a decoded array, substituting [] when absent.
func batch7Lane6SliceOrEmpty(value any) []any {
	if items := batch7Lane6AsSlice(value); items != nil {
		return items
	}
	return []any{}
}

// batch7Lane6AsString reads a decoded JSON string.
func batch7Lane6AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch7Lane6StringOr reads a string field with a fallback alternative: the
// second value wins only when the first is an empty string, mirroring the
// upstream `a or b` projections.
func batch7Lane6StringOr(primary, fallback any) string {
	if text := batch7Lane6AsString(primary); text != "" {
		return text
	}
	return batch7Lane6AsString(fallback)
}

// batch7Lane6Float distinguishes an absent/null argument from 0, which matters
// for coordinates where 0 is a valid value.
func batch7Lane6Float(input map[string]any, name string) (float64, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// batch7Lane6HTTPResponse is one completed HTTP exchange with its decoded
// JSON body (when the body is valid JSON).
type batch7Lane6HTTPResponse struct {
	Status  int
	Body    []byte
	Parsed  any
	Decoded bool
}

// batch7Lane6Do performs one request through the runtime transport, reading
// the body under the shared size cap. Transport failures are returned as Go
// errors; HTTP status handling belongs to the caller.
func batch7Lane6Do(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any) (*batch7Lane6HTTPResponse, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	client := newHTTPClient(nil)
	if rt != nil && rt.HTTP != nil {
		client = rt.HTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane6MaxBody))
	if err != nil {
		return nil, err
	}
	out := &batch7Lane6HTTPResponse{Status: resp.StatusCode, Body: raw}
	if len(raw) > 0 {
		var parsed any
		if err := json.Unmarshal(raw, &parsed); err == nil {
			out.Parsed = parsed
			out.Decoded = true
		}
	}
	return out, nil
}

// batch7Lane6Join appends a non-empty query string to a URL.
func batch7Lane6Join(endpoint string, query url.Values) string {
	if encoded := query.Encode(); encoded != "" {
		return endpoint + "?" + encoded
	}
	return endpoint
}

// ---------------------------------------------------------- SimilarWeb V5 ---

// batch7Lane6SWAction is one row of the table-driven SimilarWeb port: the
// upstream action name, endpoint, JSON-schema params, and how inputs map onto
// the query string.
type batch7Lane6SWAction struct {
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

// batch7Lane6SWDomainAction is the plain domain-only endpoint shape.
func batch7Lane6SWDomainAction(action, summary, path string) batch7Lane6SWAction {
	return batch7Lane6SWAction{
		action:   action,
		summary:  summary,
		path:     path,
		params:   map[string]any{"domain": param("string")},
		required: []string{"domain"},
		send:     []string{"domain"},
	}
}

// batch7Lane6SWRangeAction is the domain + start/end + country shape.
func batch7Lane6SWRangeAction(action, summary, path string) batch7Lane6SWAction {
	return batch7Lane6SWAction{
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

// batch7Lane6SWGeographyAction is the domain + start/end shape without a
// country filter.
func batch7Lane6SWGeographyAction(action, summary, path string) batch7Lane6SWAction {
	return batch7Lane6SWAction{
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

// batch7Lane6SimilarWebActions is the entire upstream V5 surface; every entry
// is executed by batch7Lane6SWExecute.
var batch7Lane6SimilarWebActions = []batch7Lane6SWAction{
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
	batch7Lane6SWDomainAction(
		"similarweb_v5_website_rank",
		"Get global, country, and category ranks for a website.",
		"/website-analysis/websites/website-rank",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_traffic_sources",
		"Get marketing channels (traffic sources) breakdown for a website.",
		"/website-analysis/websites/traffic-sources",
	),
	batch7Lane6SWGeographyAction(
		"similarweb_v5_geography",
		"Get traffic distribution by geography for a website.",
		"/website-analysis/websites/traffic-geography",
	),
	batch7Lane6SWDomainAction(
		"similarweb_v5_demographics",
		"Get audience demographics (age and gender) for a website.",
		"/website-analysis/websites/demographics/aggregated",
	),
	batch7Lane6SWDomainAction(
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
	batch7Lane6SWRangeAction(
		"similarweb_v5_referrals",
		"Get detailed referral traffic sources for a domain.",
		"/website-analysis/websites/referrals/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_ppc_spend",
		"Get estimated PPC spend for a website domain.",
		"/website-analysis/websites/ppc-spend",
	),
	batch7Lane6SWGeographyAction(
		"similarweb_v5_geography_details",
		"Get detailed traffic distribution by country (aggregated).",
		"/website-analysis/websites/geography/aggregated",
	),
	batch7Lane6SWDomainAction(
		"similarweb_v5_similar_sites",
		"Get a list of websites similar to the given domain.",
		"/website-analysis/websites/similar-sites/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_ad_networks",
		"Get performance data across different ad networks for a domain.",
		"/website-analysis/ad-networks/aggregated",
	),
	batch7Lane6SWDomainAction(
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
	batch7Lane6SWDomainAction(
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
	batch7Lane6SWDomainAction(
		"similarweb_v5_technologies",
		"Get technologies used on the website (CMS, Ads, Analytics, etc.).",
		"/website-analysis/websites/technologies/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_leading_folders",
		"Get top sub-folders by traffic for a domain.",
		"/website-analysis/websites/pages/leading-folders/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_popular_pages",
		"Get most visited pages on the given domain.",
		"/website-content/pages/popular-pages/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_subdomains",
		"Get traffic breakdown by subdomain for a domain.",
		"/website-content/subdomains/aggregated",
	),
	batch7Lane6SWDomainAction(
		"similarweb_v5_keyword_competitors",
		"Get organic and paid keyword competitors for a domain.",
		"/website-analysis/websites/keywords-competitors/aggregated",
	),
	batch7Lane6SWDomainAction(
		"similarweb_v5_keyword_opportunities",
		"Get keyword gap analysis and opportunities for a domain.",
		"/website-analysis/websites/keywords-opportunities/aggregated",
	),
	batch7Lane6SWDomainAction(
		"similarweb_v5_serp_features",
		"Get SERP features analysis for the domain.",
		"/website-analysis/websites/keywords/serp-features",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_organic_keywords",
		"Get detailed organic keyword performance for a domain.",
		"/website-analysis/websites/keywords/organic/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_paid_keywords",
		"Get detailed paid keyword performance for a domain.",
		"/website-analysis/websites/keywords/paid/aggregated",
	),
	batch7Lane6SWDomainAction(
		"similarweb_v5_serp_players",
		"Get top websites driving search traffic for keywords (SERP players).",
		"/website-analysis/websites/keywords/serp-players/aggregated",
	),
	batch7Lane6SWRangeAction(
		"similarweb_v5_social_referrals",
		"Get traffic distribution from social networks for a domain.",
		"/website-analysis/websites/social-referrals/aggregated",
	),
	batch7Lane6SWDomainAction(
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

// batch7Lane6SimilarWebTool mirrors the upstream module as one Go tool.
var batch7Lane6SimilarWebTool = batch7Lane6BuildSimilarWebTool()

// batch7Lane6BuildSimilarWebTool projects the endpoint table onto catalog
// actions.
func batch7Lane6BuildSimilarWebTool() *Tool {
	actions := make([]Action, 0, len(batch7Lane6SimilarWebActions))
	for _, endpoint := range batch7Lane6SimilarWebActions {
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

// batch7Lane6SimilarWebImpls binds every table row to the shared executor.
func batch7Lane6SimilarWebImpls() map[string]ActionFunc {
	impls := make(map[string]ActionFunc, len(batch7Lane6SimilarWebActions))
	for _, endpoint := range batch7Lane6SimilarWebActions {
		impls[endpoint.action] = batch7Lane6SWExecute(endpoint)
	}
	return impls
}

// batch7Lane6SWExecute performs one V5 GET and returns the decoded body
// unchanged. Failures keep the upstream "HTTP error <status>: <text>" and
// "Request failed: <error>" wording.
func batch7Lane6SWExecute(endpoint batch7Lane6SWAction) ActionFunc {
	return func(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
		token := batch7Lane6Token(rt, "similarweb_tool")
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
		target := batch7Lane6Join(batch7Lane6Base(rt, "similarweb_tool")+endpoint.path, query)
		resp, err := batch7Lane6Do(ctx, rt, http.MethodGet, target, map[string]string{
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

// ----------------------------------------------------------------- News ---

// batch7Lane6NewsTool mirrors the upstream module: seven actions over the
// NewsData and Finlight providers.
var batch7Lane6NewsTool = &Tool{
	ID:          "news_tool",
	Name:        "news",
	DisplayName: "News",
	Summary:     "News integration (7 actions).",
	Actions: []Action{
		{
			Name:     "news_search",
			Summary:  "Search news articles with filters.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":     param("string"),
				"from_date": paramDefault("string", ""),
				"to_date":   paramDefault("string", ""),
				"language":  paramDefault("string", "en"),
				"limit":     paramDefault("integer", 10),
				"sources":   paramDefault("string", ""),
				"category":  paramDefault("string", ""),
				"country":   paramDefault("string", ""),
			},
		},
		{
			Name:     "news_headlines",
			Summary:  "Get top news headlines by category and country.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"category", "country"},
			Params: map[string]any{
				"category": param("string"),
				"country":  param("string"),
				"limit":    paramDefault("integer", 10),
			},
		},
		{
			Name:     "news_by_company",
			Summary:  "Get news mentioning a specific company.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"company_name"},
			Params: map[string]any{
				"company_name": param("string"),
				"days_back":    paramDefault("integer", 7),
				"limit":        paramDefault("integer", 10),
				"language":     paramDefault("string", "en"),
			},
		},
		{
			Name:     "news_sentiment",
			Summary:  "Get news with sentiment analysis (Finlight provider).",
			Source:   "news_tool/news_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":     param("string"),
				"from_date": paramDefault("string", ""),
				"to_date":   paramDefault("string", ""),
			},
		},
		{
			Name:    "news_latest",
			Summary: "Get the latest breaking news without a search query.",
			Source:  "news_tool/news_tool.py",
			Params: map[string]any{
				"language": paramDefault("string", "en"),
				"country":  paramDefault("string", ""),
				"category": paramDefault("string", ""),
				"limit":    paramDefault("integer", 10),
			},
		},
		{
			Name:     "news_by_source",
			Summary:  "Get news from specific sources.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"sources"},
			Params: map[string]any{
				"sources":   param("string"),
				"query":     paramDefault("string", ""),
				"days_back": paramDefault("integer", 7),
				"language":  paramDefault("string", "en"),
				"limit":     paramDefault("integer", 10),
			},
		},
		{
			Name:     "news_by_topic",
			Summary:  "Get news articles about a broad topic or industry.",
			Source:   "news_tool/news_tool.py",
			Required: []string{"topic"},
			Params: map[string]any{
				"topic":     param("string"),
				"days_back": paramDefault("integer", 3),
				"language":  paramDefault("string", "en"),
				"country":   paramDefault("string", ""),
				"limit":     paramDefault("integer", 10),
			},
		},
	},
}

// batch7Lane6NewsArgs is the shared normalized search request.
type batch7Lane6NewsArgs struct {
	Query    string
	FromDate string
	ToDate   string
	Language string
	Sources  string
	Category string
	Country  string
	Limit    int
}

// batch7Lane6NewsImpls binds every action name to its implementation.
func batch7Lane6NewsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"news_search":     batch7Lane6NewsSearch,
		"news_headlines":  batch7Lane6NewsHeadlines,
		"news_by_company": batch7Lane6NewsByCompany,
		"news_sentiment":  batch7Lane6NewsSentiment,
		"news_latest":     batch7Lane6NewsLatest,
		"news_by_source":  batch7Lane6NewsBySource,
		"news_by_topic":   batch7Lane6NewsByTopic,
	}
}

// batch7Lane6NewsKeys resolves the two provider credentials independently.
func batch7Lane6NewsKeys(rt *Runtime) (string, string) {
	return batch7Lane6Token(rt, "news_tool"), batch7Lane6Token(rt, "finlight_tool")
}

// batch7Lane6NewsNoCredentials is the upstream missing-pair error.
func batch7Lane6NewsNoCredentials() map[string]any {
	return map[string]any{
		"error": "No news credentials configured",
		"help":  "Set NEWSDATA_API_KEY or FINLIGHT_API_KEY environment variable",
	}
}

// batch7Lane6NewsLimit normalizes an optional limit like upstream
// _normalize_limit: None -> 10, otherwise at least 1.
func batch7Lane6NewsLimit(input map[string]any) int {
	limit := intArg(input, "limit", 10)
	if limit < 1 {
		return 1
	}
	return limit
}

// batch7Lane6NewsDateRange builds the upstream from/to pair for the past N
// days (both inclusive).
func batch7Lane6NewsDateRange(rt *Runtime, days int) (string, string) {
	now := batch7Lane6Clock(rt)
	end := now.Format("2006-01-02")
	start := now.AddDate(0, 0, -days).Format("2006-01-02")
	return start, end
}

// batch7Lane6NewsArticle normalizes one article payload. Every upstream key is
// present, defaulting to "".
func batch7Lane6NewsArticle(title, source, published, link, snippet any) map[string]any {
	return map[string]any{
		"title":   batch7Lane6AsString(title),
		"source":  batch7Lane6AsString(source),
		"date":    batch7Lane6AsString(published),
		"url":     batch7Lane6AsString(link),
		"snippet": batch7Lane6AsString(snippet),
	}
}

// batch7Lane6NewsDataArticles projects the NewsData results array.
func batch7Lane6NewsDataArticles(parsed any) []any {
	results := make([]any, 0)
	for _, raw := range batch7Lane6AsSlice(batch7Lane6AsMap(parsed)["results"]) {
		item := batch7Lane6AsMap(raw)
		results = append(results, batch7Lane6NewsArticle(
			item["title"], item["source_id"], item["pubDate"], item["link"], item["description"],
		))
	}
	return results
}

// batch7Lane6NewsDataError maps NewsData non-200 responses onto the upstream
// friendly messages.
func batch7Lane6NewsDataError(status int, parsed any, body []byte) map[string]any {
	switch status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid NewsData API key"}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "NewsData rate limit exceeded. Try again later."}
	case http.StatusUnprocessableEntity:
		detail := string(body)
		if results, ok := batch7Lane6AsMap(parsed)["results"].(map[string]any); ok {
			if message, present := results["message"]; present {
				detail = fmt.Sprint(message)
			}
		}
		return map[string]any{"error": "Invalid NewsData parameters: " + detail}
	default:
		return map[string]any{"error": fmt.Sprintf("NewsData request failed: HTTP %d", status)}
	}
}

// batch7Lane6FinlightError maps Finlight non-200 responses onto the upstream
// friendly messages.
func batch7Lane6FinlightError(status int, parsed any, body []byte) map[string]any {
	switch status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Finlight API key"}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Finlight rate limit exceeded. Try again later."}
	case http.StatusUnprocessableEntity:
		detail := string(body)
		if message, present := batch7Lane6AsMap(parsed)["message"]; present {
			detail = fmt.Sprint(message)
		}
		return map[string]any{"error": "Invalid Finlight parameters: " + detail}
	default:
		return map[string]any{"error": fmt.Sprintf("Finlight request failed: HTTP %d", status)}
	}
}

// batch7Lane6NewsRetry sends one request, retrying 429 with exponential
// backoff exactly like upstream (three retries, then the final response).
func batch7Lane6NewsRetry(ctx context.Context, rt *Runtime, method, endpoint string, headers map[string]string, payload any) (*batch7Lane6HTTPResponse, error) {
	for attempt := 0; attempt <= batch7Lane6Retries; attempt++ {
		resp, err := batch7Lane6Do(ctx, rt, method, endpoint, headers, payload)
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusTooManyRequests && attempt < batch7Lane6Retries {
			batch7Lane6Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("news: retry loop exhausted")
}

// batch7Lane6NewsDataSearch queries NewsData, switching to the archive
// endpoint when any date filter is present.
func batch7Lane6NewsDataSearch(ctx context.Context, rt *Runtime, args batch7Lane6NewsArgs, key string) map[string]any {
	path := "/news"
	useArchive := args.FromDate != "" || args.ToDate != ""
	if useArchive {
		path = "/archive"
	}
	query := url.Values{}
	query.Set("apikey", key)
	if args.Query != "" {
		query.Set("q", args.Query)
	}
	if useArchive {
		if args.FromDate != "" {
			query.Set("from_date", args.FromDate)
		}
		if args.ToDate != "" {
			query.Set("to_date", args.ToDate)
		}
	}
	if args.Language != "" {
		query.Set("language", args.Language)
	}
	if args.Category != "" {
		query.Set("category", args.Category)
	}
	if args.Country != "" {
		query.Set("country", args.Country)
	}
	query.Set("size", strconv.Itoa(args.Limit))
	if args.Sources != "" {
		query.Set("sources", args.Sources)
	}
	target := batch7Lane6Join(batch7Lane6Base(rt, "news_tool")+path, query)
	resp, err := batch7Lane6NewsRetry(ctx, rt, http.MethodGet, target, nil, nil)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	if resp.Status != http.StatusOK {
		return batch7Lane6NewsDataError(resp.Status, resp.Parsed, resp.Body)
	}
	results := batch7Lane6NewsDataArticles(resp.Parsed)
	return map[string]any{
		"results":  results,
		"total":    len(results),
		"provider": "newsdata",
	}
}

// batch7Lane6NewsSentimentValue normalizes an upstream sentiment value to a
// float in [-1, 1], or reports that no value is available.
func batch7Lane6NewsSentimentValue(raw any) (float64, bool) {
	switch typed := raw.(type) {
	case nil:
		return 0, false
	case float64:
		return batch7Lane6ClampFloat(typed, -1, 1), true
	case int:
		return batch7Lane6ClampFloat(float64(typed), -1, 1), true
	case int64:
		return batch7Lane6ClampFloat(float64(typed), -1, 1), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return batch7Lane6ClampFloat(parsed, -1, 1), true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "positive":
			return 1, true
		case "negative":
			return -1, true
		case "neutral":
			return 0, true
		default:
			return 0, false
		}
	default:
		return 0, false
	}
}

// batch7Lane6ClampFloat bounds a float to [low, high].
func batch7Lane6ClampFloat(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch7Lane6NewsFalsy mirrors Python truthiness for the sentiment fallback
// (`item.get("sentiment") or item.get("sentiment_score")`).
func batch7Lane6NewsFalsy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case float64:
		return typed == 0
	case int:
		return typed == 0
	case bool:
		return !typed
	default:
		return false
	}
}

// batch7Lane6FinlightSearch queries the Finlight articles endpoint.
func batch7Lane6FinlightSearch(ctx context.Context, rt *Runtime, args batch7Lane6NewsArgs, key string, includeSentiment bool) map[string]any {
	query := args.Query
	if query == "" && args.Category != "" {
		query = args.Category
	}
	body := map[string]any{"page": 1, "pageSize": args.Limit}
	if query != "" {
		body["query"] = query
	}
	if args.FromDate != "" {
		body["from"] = args.FromDate
	}
	if args.ToDate != "" {
		body["to"] = args.ToDate
	}
	if args.Language != "" {
		body["language"] = args.Language
	}
	if args.Sources != "" {
		sources := make([]any, 0)
		for _, part := range strings.Split(args.Sources, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				sources = append(sources, trimmed)
			}
		}
		if len(sources) > 0 {
			body["sources"] = sources
		}
	}
	if args.Country != "" {
		body["countries"] = []any{strings.ToUpper(args.Country)}
	}
	target := batch7Lane6Base(rt, "finlight_tool") + "/articles"
	resp, err := batch7Lane6NewsRetry(ctx, rt, http.MethodPost, target, map[string]string{
		"X-API-KEY": key,
		"Accept":    "application/json",
	}, body)
	if err != nil {
		return map[string]any{"error": "Network error: " + err.Error()}
	}
	if resp.Status != http.StatusOK {
		return batch7Lane6FinlightError(resp.Status, resp.Parsed, resp.Body)
	}
	payload := batch7Lane6AsMap(resp.Parsed)
	rawResults := payload["articles"]
	if rawResults == nil {
		rawResults = payload["data"]
	}
	if rawResults == nil {
		rawResults = payload["results"]
	}
	results := make([]any, 0)
	for _, raw := range batch7Lane6AsSlice(rawResults) {
		item := batch7Lane6AsMap(raw)
		article := batch7Lane6NewsArticle(
			item["title"],
			item["source"],
			batch7Lane6StringOr(item["publishDate"], item["published_at"]),
			batch7Lane6StringOr(item["link"], item["url"]),
			batch7Lane6StringOr(item["summary"], item["description"]),
		)
		if includeSentiment {
			rawSentiment := item["sentiment"]
			if batch7Lane6NewsFalsy(rawSentiment) {
				if alternative, present := item["sentiment_score"]; present {
					rawSentiment = alternative
				}
			}
			if sentiment, ok := batch7Lane6NewsSentimentValue(rawSentiment); ok {
				article["sentiment"] = sentiment
			}
		}
		results = append(results, article)
	}
	return map[string]any{
		"results":  results,
		"total":    len(results),
		"provider": "finlight",
	}
}

// batch7Lane6NewsWithFallback tries NewsData first and falls back to Finlight
// only when the primary reports an error, exactly like the upstream helper.
func batch7Lane6NewsWithFallback(ctx context.Context, rt *Runtime, args batch7Lane6NewsArgs) map[string]any {
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	var primary map[string]any
	if newsKey != "" {
		primary = batch7Lane6NewsDataSearch(ctx, rt, args, newsKey)
	} else {
		primary = map[string]any{"error": "NewsData credentials not configured"}
	}
	if _, failed := primary["error"]; !failed {
		return primary
	}
	if finlightKey == "" {
		return primary
	}
	fallback := batch7Lane6FinlightSearch(ctx, rt, args, finlightKey, false)
	if _, failed := fallback["error"]; !failed {
		return fallback
	}
	return map[string]any{
		"error":     "All providers failed",
		"providers": map[string]any{"primary": primary, "fallback": fallback},
	}
}

// batch7Lane6NewsSearch searches articles with filters.
func batch7Lane6NewsSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "Query is required"}, nil
	}
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return batch7Lane6NewsNoCredentials(), nil
	}
	result := batch7Lane6NewsWithFallback(ctx, rt, batch7Lane6NewsArgs{
		Query:    query,
		FromDate: stringArg(input, "from_date"),
		ToDate:   stringArg(input, "to_date"),
		Language: stringArg(input, "language"),
		Sources:  stringArg(input, "sources"),
		Category: stringArg(input, "category"),
		Country:  stringArg(input, "country"),
		Limit:    batch7Lane6NewsLimit(input),
	})
	result["query"] = query
	return result, nil
}

// batch7Lane6NewsHeadlines lists top headlines by category and country.
func batch7Lane6NewsHeadlines(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	category := stringArg(input, "category")
	if category == "" {
		return map[string]any{"error": "Category is required"}, nil
	}
	country := stringArg(input, "country")
	if country == "" {
		return map[string]any{"error": "Country is required"}, nil
	}
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return batch7Lane6NewsNoCredentials(), nil
	}
	result := batch7Lane6NewsWithFallback(ctx, rt, batch7Lane6NewsArgs{
		Category: category,
		Country:  country,
		Limit:    batch7Lane6NewsLimit(input),
	})
	result["category"] = category
	result["country"] = country
	return result, nil
}

// batch7Lane6NewsByCompany searches news mentioning a company over the past
// N days.
func batch7Lane6NewsByCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	companyName := stringArg(input, "company_name")
	if companyName == "" {
		return map[string]any{"error": "Company name is required"}, nil
	}
	daysBack := intArg(input, "days_back", 7)
	if daysBack < 0 {
		return map[string]any{"error": "days_back must be 0 or greater"}, nil
	}
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return batch7Lane6NewsNoCredentials(), nil
	}
	fromDate, toDate := batch7Lane6NewsDateRange(rt, daysBack)
	result := batch7Lane6NewsWithFallback(ctx, rt, batch7Lane6NewsArgs{
		Query:    `"` + companyName + `"`,
		FromDate: fromDate,
		ToDate:   toDate,
		Language: stringArg(input, "language"),
		Limit:    batch7Lane6NewsLimit(input),
	})
	result["company_name"] = companyName
	result["days_back"] = daysBack
	return result, nil
}

// batch7Lane6NewsSentiment queries Finlight and normalizes each article's
// sentiment score.
func batch7Lane6NewsSentiment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "Query is required"}, nil
	}
	_, finlightKey := batch7Lane6NewsKeys(rt)
	if finlightKey == "" {
		return map[string]any{
			"error": "Finlight credentials not configured",
			"help":  "Set FINLIGHT_API_KEY environment variable",
		}, nil
	}
	result := batch7Lane6FinlightSearch(ctx, rt, batch7Lane6NewsArgs{
		Query:    query,
		FromDate: stringArg(input, "from_date"),
		ToDate:   stringArg(input, "to_date"),
		Limit:    10,
	}, finlightKey, true)
	result["query"] = query
	return result, nil
}

// batch7Lane6NewsLatest fetches breaking news, falling back to a broad search
// when the NewsData latest endpoint fails.
func batch7Lane6NewsLatest(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return batch7Lane6NewsNoCredentials(), nil
	}
	language := stringArg(input, "language")
	country := stringArg(input, "country")
	category := stringArg(input, "category")
	limit := batch7Lane6NewsLimit(input)
	if newsKey != "" {
		query := url.Values{}
		query.Set("apikey", newsKey)
		if language != "" {
			query.Set("language", language)
		}
		if category != "" {
			query.Set("category", category)
		}
		if country != "" {
			query.Set("country", country)
		}
		query.Set("size", strconv.Itoa(limit))
		target := batch7Lane6Join(batch7Lane6Base(rt, "news_tool")+"/news", query)
		resp, err := batch7Lane6NewsRetry(ctx, rt, http.MethodGet, target, nil, nil)
		if err == nil && resp.Status == http.StatusOK {
			results := batch7Lane6NewsDataArticles(resp.Parsed)
			return map[string]any{
				"results":  results,
				"total":    len(results),
				"provider": "newsdata",
			}, nil
		}
	}
	broadQuery := category
	if broadQuery == "" {
		broadQuery = "breaking news"
	}
	return batch7Lane6NewsWithFallback(ctx, rt, batch7Lane6NewsArgs{
		Query:    broadQuery,
		Language: language,
		Category: category,
		Country:  country,
		Limit:    limit,
	}), nil
}

// batch7Lane6NewsBySource lists news from specific source IDs.
func batch7Lane6NewsBySource(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sources := stringArg(input, "sources")
	if sources == "" {
		return map[string]any{"error": "sources is required (comma-separated source IDs)"}, nil
	}
	daysBack := intArg(input, "days_back", 7)
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return batch7Lane6NewsNoCredentials(), nil
	}
	fromDate, toDate := batch7Lane6NewsDateRange(rt, daysBack)
	query := stringArg(input, "query")
	result := batch7Lane6NewsWithFallback(ctx, rt, batch7Lane6NewsArgs{
		Query:    query,
		FromDate: fromDate,
		ToDate:   toDate,
		Language: stringArg(input, "language"),
		Sources:  sources,
		Limit:    batch7Lane6NewsLimit(input),
	})
	result["sources"] = sources
	if query != "" {
		result["query"] = query
	}
	return result, nil
}

// batch7Lane6NewsByTopic discovers articles about a broad topic.
func batch7Lane6NewsByTopic(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	topic := stringArg(input, "topic")
	if topic == "" {
		return map[string]any{"error": "topic is required"}, nil
	}
	daysBack := intArg(input, "days_back", 3)
	newsKey, finlightKey := batch7Lane6NewsKeys(rt)
	if newsKey == "" && finlightKey == "" {
		return batch7Lane6NewsNoCredentials(), nil
	}
	fromDate, toDate := batch7Lane6NewsDateRange(rt, daysBack)
	result := batch7Lane6NewsWithFallback(ctx, rt, batch7Lane6NewsArgs{
		Query:    topic,
		FromDate: fromDate,
		ToDate:   toDate,
		Language: stringArg(input, "language"),
		Country:  stringArg(input, "country"),
		Limit:    batch7Lane6NewsLimit(input),
	})
	result["topic"] = topic
	result["days_back"] = daysBack
	return result, nil
}

// ---------------------------------------------------------- Google Maps ---

// batch7Lane6GoogleMapsTool mirrors the upstream module: six Web Service
// actions authenticated by a key query parameter.
var batch7Lane6GoogleMapsTool = &Tool{
	ID:          "google_maps_tool",
	Name:        "google_maps",
	DisplayName: "Google Maps",
	Summary:     "Google Maps integration (6 actions).",
	Actions: []Action{
		{
			Name:    "maps_geocode",
			Summary: "Convert an address to geographic coordinates (latitude/longitude).",
			Source:  "google_maps_tool/google_maps_tool.py",
			Params: map[string]any{
				"address":    paramDefault("string", ""),
				"components": paramDefault("string", ""),
				"bounds":     paramDefault("string", ""),
				"region":     paramDefault("string", ""),
				"language":   paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_reverse_geocode",
			Summary:  "Convert geographic coordinates to a human-readable address.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"latitude", "longitude"},
			Params: map[string]any{
				"latitude":      param("number"),
				"longitude":     param("number"),
				"result_type":   paramDefault("string", ""),
				"location_type": paramDefault("string", ""),
				"language":      paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_directions",
			Summary:  "Calculate routes between two or more locations.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"origin", "destination"},
			Params: map[string]any{
				"origin":         param("string"),
				"destination":    param("string"),
				"mode":           paramDefault("string", "driving"),
				"waypoints":      paramDefault("string", ""),
				"alternatives":   paramDefault("boolean", false),
				"units":          paramDefault("string", "metric"),
				"avoid":          paramDefault("string", ""),
				"departure_time": paramDefault("string", ""),
				"language":       paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_distance_matrix",
			Summary:  "Calculate travel distance and time for multiple origins and destinations.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"origins", "destinations"},
			Params: map[string]any{
				"origins":        param("string"),
				"destinations":   param("string"),
				"mode":           paramDefault("string", "driving"),
				"units":          paramDefault("string", "metric"),
				"avoid":          paramDefault("string", ""),
				"departure_time": paramDefault("string", ""),
				"language":       paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_place_details",
			Summary:  "Get detailed information about a specific place.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"place_id"},
			Params: map[string]any{
				"place_id": param("string"),
				"fields": paramDefault("string",
					"name,formatted_address,geometry,rating,formatted_phone_number,website,opening_hours,reviews,price_level,types"),
				"language":     paramDefault("string", ""),
				"reviews_sort": paramDefault("string", "most_relevant"),
			},
		},
		{
			Name:    "maps_place_search",
			Summary: "Search for places by text query (name, address, or type of place).",
			Source:  "google_maps_tool/google_maps_tool.py",
			Params: map[string]any{
				"query":      paramDefault("string", ""),
				"location":   paramDefault("string", ""),
				"radius":     paramDefault("integer", 0),
				"type":       paramDefault("string", ""),
				"language":   paramDefault("string", ""),
				"opennow":    paramDefault("boolean", false),
				"minprice":   paramDefault("integer", -1),
				"maxprice":   paramDefault("integer", -1),
				"region":     paramDefault("string", ""),
				"page_token": paramDefault("string", ""),
			},
		},
	},
}

// batch7Lane6GoogleMapsImpls binds every action name to its implementation.
func batch7Lane6GoogleMapsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"maps_geocode":         batch7Lane6MapsGeocode,
		"maps_reverse_geocode": batch7Lane6MapsReverseGeocode,
		"maps_directions":      batch7Lane6MapsDirections,
		"maps_distance_matrix": batch7Lane6MapsDistanceMatrix,
		"maps_place_details":   batch7Lane6MapsPlaceDetails,
		"maps_place_search":    batch7Lane6MapsPlaceSearch,
	}
}

// batch7Lane6MapsMissingKey is the upstream missing-credential error.
func batch7Lane6MapsMissingKey() map[string]any {
	return map[string]any{
		"error": "Google Maps API key not configured",
		"help": "Set GOOGLE_MAPS_API_KEY environment variable. " +
			"Get a key at https://console.cloud.google.com/apis/credentials " +
			"and enable the Geocoding, Directions, Distance Matrix, and Places APIs.",
	}
}

// batch7Lane6MapsStatus checks an API-level status and returns the upstream
// error dict for anything other than OK / ZERO_RESULTS.
func batch7Lane6MapsStatus(status, errorMessage string) map[string]any {
	if status == "" {
		status = "UNKNOWN_ERROR"
	}
	if status == "OK" || status == "ZERO_RESULTS" {
		return nil
	}
	messages := map[string]string{
		"OVER_DAILY_LIMIT":        "API key invalid, billing not enabled, or daily limit exceeded",
		"OVER_QUERY_LIMIT":        "Too many requests. Try again later",
		"REQUEST_DENIED":          "Request denied — check that the API is enabled and the key is valid",
		"INVALID_REQUEST":         "Invalid request — check required parameters",
		"MAX_ELEMENTS_EXCEEDED":   "Too many origins × destinations (max 625 elements)",
		"MAX_DIMENSIONS_EXCEEDED": "Too many origins or destinations (max 25 each)",
		"MAX_WAYPOINTS_EXCEEDED":  "Too many waypoints (max 25)",
		"NOT_FOUND":               "One or more locations could not be found",
		"UNKNOWN_ERROR":           "Server error — please retry",
	}
	message, ok := messages[status]
	if !ok {
		message = "API error: " + status
	}
	if errorMessage != "" {
		message = message + ". " + errorMessage
	}
	return map[string]any{"error": message}
}

// batch7Lane6MapsGet performs one Maps request and applies the upstream
// HTTP-then-API-status error order. The second return value is the error dict
// when the call failed.
func batch7Lane6MapsGet(ctx context.Context, rt *Runtime, key, path string, query url.Values, label string) (map[string]any, map[string]any) {
	query.Set("key", key)
	target := batch7Lane6Join(batch7Lane6Base(rt, "google_maps_tool")+path, query)
	resp, err := batch7Lane6Do(ctx, rt, http.MethodGet, target, nil, nil)
	if err != nil {
		if batch7Lane6IsTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request timed out"}
		}
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	if resp.Status != http.StatusOK {
		return nil, map[string]any{
			"error": fmt.Sprintf("HTTP %d: %s", resp.Status, batch7Lane6Truncate(string(resp.Body), batch7Lane6HTTPText)),
		}
	}
	data := batch7Lane6AsMap(resp.Parsed)
	if data == nil {
		return nil, map[string]any{"error": label + " failed: invalid JSON response"}
	}
	status := batch7Lane6AsString(data["status"])
	errorMessage := batch7Lane6AsString(data["error_message"])
	if errDict := batch7Lane6MapsStatus(status, errorMessage); errDict != nil {
		return nil, errDict
	}
	return data, nil
}

// batch7Lane6MapsResults projects the geocoding/reverse-geocoding result
// array.
func batch7Lane6MapsResults(data map[string]any) []any {
	results := make([]any, 0)
	for _, raw := range batch7Lane6AsSlice(data["results"]) {
		item := batch7Lane6AsMap(raw)
		geometry := batch7Lane6AsMap(item["geometry"])
		location := geometry["location"]
		if location == nil {
			location = map[string]any{}
		}
		results = append(results, map[string]any{
			"formatted_address":  batch7Lane6AsString(item["formatted_address"]),
			"location":           location,
			"location_type":      batch7Lane6AsString(geometry["location_type"]),
			"place_id":           batch7Lane6AsString(item["place_id"]),
			"types":              batch7Lane6SliceOrEmpty(item["types"]),
			"address_components": batch7Lane6SliceOrEmpty(item["address_components"]),
		})
	}
	return results
}

// batch7Lane6MapsGeocode converts an address (or component filter) to
// coordinates.
func batch7Lane6MapsGeocode(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	address := stringArg(input, "address")
	components := stringArg(input, "components")
	if address == "" && components == "" {
		return map[string]any{"error": "Either address or components is required"}, nil
	}
	key := batch7Lane6Token(rt, "google_maps_tool")
	if key == "" {
		return batch7Lane6MapsMissingKey(), nil
	}
	query := url.Values{}
	if address != "" {
		query.Set("address", address)
	}
	for _, name := range []string{"components", "bounds", "region", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	data, errDict := batch7Lane6MapsGet(ctx, rt, key, "/geocode/json", query, "Geocoding")
	if errDict != nil {
		return errDict, nil
	}
	results := batch7Lane6MapsResults(data)
	queryText := address
	if queryText == "" {
		queryText = components
	}
	return map[string]any{"query": queryText, "results": results, "total": len(results)}, nil
}

// batch7Lane6FormatCoordinate renders a coordinate like Python's float
// formatting in the upstream f-string: integral values keep a ".0" suffix.
func batch7Lane6FormatCoordinate(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

// batch7Lane6MapsReverseGeocode converts coordinates to an address.
func batch7Lane6MapsReverseGeocode(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	latitude, latOK := batch7Lane6Float(input, "latitude")
	if !latOK {
		return map[string]any{"error": "latitude is required"}, nil
	}
	longitude, lngOK := batch7Lane6Float(input, "longitude")
	if !lngOK {
		return map[string]any{"error": "longitude is required"}, nil
	}
	if latitude < -90 || latitude > 90 {
		return map[string]any{"error": "Latitude must be between -90 and 90"}, nil
	}
	if longitude < -180 || longitude > 180 {
		return map[string]any{"error": "Longitude must be between -180 and 180"}, nil
	}
	key := batch7Lane6Token(rt, "google_maps_tool")
	if key == "" {
		return batch7Lane6MapsMissingKey(), nil
	}
	query := url.Values{}
	query.Set("latlng", batch7Lane6FormatCoordinate(latitude)+","+batch7Lane6FormatCoordinate(longitude))
	for _, name := range []string{"result_type", "location_type", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	data, errDict := batch7Lane6MapsGet(ctx, rt, key, "/geocode/json", query, "Reverse geocoding")
	if errDict != nil {
		return errDict, nil
	}
	results := batch7Lane6MapsResults(data)
	return map[string]any{
		"coordinates": map[string]any{"lat": latitude, "lng": longitude},
		"results":     results,
		"total":       len(results),
	}, nil
}

// batch7Lane6MapsDirections calculates routes between locations.
func batch7Lane6MapsDirections(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	origin := stringArg(input, "origin")
	if origin == "" {
		return map[string]any{"error": "Origin is required"}, nil
	}
	destination := stringArg(input, "destination")
	if destination == "" {
		return map[string]any{"error": "Destination is required"}, nil
	}
	key := batch7Lane6Token(rt, "google_maps_tool")
	if key == "" {
		return batch7Lane6MapsMissingKey(), nil
	}
	mode := stringArg(input, "mode")
	query := url.Values{
		"origin":      {origin},
		"destination": {destination},
		"mode":        {mode},
		"units":       {stringArg(input, "units")},
	}
	for _, name := range []string{"waypoints", "avoid", "departure_time", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	if boolArg(input, "alternatives", false) {
		query.Set("alternatives", "true")
	}
	data, errDict := batch7Lane6MapsGet(ctx, rt, key, "/directions/json", query, "Directions")
	if errDict != nil {
		return errDict, nil
	}
	routes := make([]any, 0)
	for _, rawRoute := range batch7Lane6AsSlice(data["routes"]) {
		route := batch7Lane6AsMap(rawRoute)
		legs := make([]any, 0)
		for _, rawLeg := range batch7Lane6AsSlice(route["legs"]) {
			leg := batch7Lane6AsMap(rawLeg)
			steps := make([]any, 0)
			for _, rawStep := range batch7Lane6AsSlice(leg["steps"]) {
				step := batch7Lane6AsMap(rawStep)
				steps = append(steps, map[string]any{
					"instruction": batch7Lane6AsString(step["html_instructions"]),
					"distance":    batch7Lane6MapOrEmpty(step["distance"]),
					"duration":    batch7Lane6MapOrEmpty(step["duration"]),
					"travel_mode": batch7Lane6AsString(step["travel_mode"]),
				})
			}
			legs = append(legs, map[string]any{
				"start_address":       batch7Lane6AsString(leg["start_address"]),
				"end_address":         batch7Lane6AsString(leg["end_address"]),
				"distance":            batch7Lane6MapOrEmpty(leg["distance"]),
				"duration":            batch7Lane6MapOrEmpty(leg["duration"]),
				"duration_in_traffic": leg["duration_in_traffic"],
				"steps":               steps,
			})
		}
		routes = append(routes, map[string]any{
			"summary":           batch7Lane6AsString(route["summary"]),
			"legs":              legs,
			"overview_polyline": batch7Lane6AsString(batch7Lane6AsMap(route["overview_polyline"])["points"]),
			"warnings":          batch7Lane6SliceOrEmpty(route["warnings"]),
			"waypoint_order":    batch7Lane6SliceOrEmpty(route["waypoint_order"]),
		})
	}
	return map[string]any{
		"origin":       origin,
		"destination":  destination,
		"mode":         mode,
		"routes":       routes,
		"total_routes": len(routes),
	}, nil
}

// batch7Lane6MapsDistanceMatrix calculates many-to-many travel estimates.
func batch7Lane6MapsDistanceMatrix(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	origins := stringArg(input, "origins")
	if origins == "" {
		return map[string]any{"error": "Origins is required"}, nil
	}
	destinations := stringArg(input, "destinations")
	if destinations == "" {
		return map[string]any{"error": "Destinations is required"}, nil
	}
	key := batch7Lane6Token(rt, "google_maps_tool")
	if key == "" {
		return batch7Lane6MapsMissingKey(), nil
	}
	query := url.Values{
		"origins":      {origins},
		"destinations": {destinations},
		"mode":         {stringArg(input, "mode")},
		"units":        {stringArg(input, "units")},
	}
	for _, name := range []string{"avoid", "departure_time", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	data, errDict := batch7Lane6MapsGet(ctx, rt, key, "/distancematrix/json", query, "Distance matrix")
	if errDict != nil {
		return errDict, nil
	}
	rows := make([]any, 0)
	for _, rawRow := range batch7Lane6AsSlice(data["rows"]) {
		row := batch7Lane6AsMap(rawRow)
		elements := make([]any, 0)
		for _, rawElement := range batch7Lane6AsSlice(row["elements"]) {
			element := batch7Lane6AsMap(rawElement)
			projected := map[string]any{
				"status":   batch7Lane6AsString(element["status"]),
				"distance": batch7Lane6MapOrEmpty(element["distance"]),
				"duration": batch7Lane6MapOrEmpty(element["duration"]),
			}
			if value, present := element["duration_in_traffic"]; present {
				projected["duration_in_traffic"] = value
			}
			elements = append(elements, projected)
		}
		rows = append(rows, map[string]any{"elements": elements})
	}
	return map[string]any{
		"origin_addresses":      batch7Lane6SliceOrEmpty(data["origin_addresses"]),
		"destination_addresses": batch7Lane6SliceOrEmpty(data["destination_addresses"]),
		"rows":                  rows,
	}, nil
}

// batch7Lane6MapsPlaceDetails reads one place by ID.
func batch7Lane6MapsPlaceDetails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	placeID := stringArg(input, "place_id")
	if placeID == "" {
		return map[string]any{"error": "place_id is required"}, nil
	}
	key := batch7Lane6Token(rt, "google_maps_tool")
	if key == "" {
		return batch7Lane6MapsMissingKey(), nil
	}
	query := url.Values{
		"place_id":     {placeID},
		"fields":       {stringArg(input, "fields")},
		"reviews_sort": {stringArg(input, "reviews_sort")},
	}
	if language := stringArg(input, "language"); language != "" {
		query.Set("language", language)
	}
	data, errDict := batch7Lane6MapsGet(ctx, rt, key, "/place/details/json", query, "Place details")
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"place_id": placeID,
		"result":   batch7Lane6MapOrEmpty(data["result"]),
	}, nil
}

// batch7Lane6MapsPlaceSearch runs a text search for places.
func batch7Lane6MapsPlaceSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	pageToken := stringArg(input, "page_token")
	if query == "" && pageToken == "" {
		return map[string]any{"error": "Query or page_token is required"}, nil
	}
	key := batch7Lane6Token(rt, "google_maps_tool")
	if key == "" {
		return batch7Lane6MapsMissingKey(), nil
	}
	params := url.Values{}
	if pageToken != "" {
		params.Set("pagetoken", pageToken)
	}
	if query != "" {
		params.Set("query", query)
	}
	for _, name := range []string{"location", "type", "language", "region"} {
		if value := stringArg(input, name); value != "" {
			params.Set(name, value)
		}
	}
	if radius := intArg(input, "radius", 0); radius > 0 {
		params.Set("radius", strconv.Itoa(batch7Lane6Min(radius, 50000)))
	}
	if boolArg(input, "opennow", false) {
		params.Set("opennow", "true")
	}
	if minPrice := intArg(input, "minprice", -1); minPrice >= 0 && minPrice <= 4 {
		params.Set("minprice", strconv.Itoa(minPrice))
	}
	if maxPrice := intArg(input, "maxprice", -1); maxPrice >= 0 && maxPrice <= 4 {
		params.Set("maxprice", strconv.Itoa(maxPrice))
	}
	data, errDict := batch7Lane6MapsGet(ctx, rt, key, "/place/textsearch/json", params, "Place search")
	if errDict != nil {
		return errDict, nil
	}
	results := make([]any, 0)
	for _, raw := range batch7Lane6AsSlice(data["results"]) {
		item := batch7Lane6AsMap(raw)
		place := map[string]any{
			"name":               batch7Lane6AsString(item["name"]),
			"formatted_address":  batch7Lane6AsString(item["formatted_address"]),
			"location":           batch7Lane6MapOrEmpty(batch7Lane6AsMap(item["geometry"])["location"]),
			"place_id":           batch7Lane6AsString(item["place_id"]),
			"types":              batch7Lane6SliceOrEmpty(item["types"]),
			"rating":             item["rating"],
			"user_ratings_total": item["user_ratings_total"],
			"price_level":        item["price_level"],
			"business_status":    batch7Lane6AsString(item["business_status"]),
		}
		if openingHours, present := item["opening_hours"]; present {
			place["open_now"] = batch7Lane6AsMap(openingHours)["open_now"]
		}
		results = append(results, place)
	}
	response := map[string]any{"query": query, "results": results, "total": len(results)}
	if token := batch7Lane6AsString(data["next_page_token"]); token != "" {
		response["next_page_token"] = token
	}
	return response, nil
}

// batch7Lane6Min returns the smaller of two integers.
func batch7Lane6Min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
