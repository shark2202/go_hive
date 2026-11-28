package integrations

// SimilarWeb alias guards: upstream exposes each of the 29 V5 calls as its own
// MCP tool (similarweb_v5_*). The Go port keeps the aggregate `similarweb`
// tool (grouped action surface, frozen by the webintel catalog digest) and must
// also publish the upstream names as flat single-action registry tools that
// execute exactly the same actions — same endpoint, query mapping, defaults,
// credential envelope and error wording.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/webintel"
)

// swAliasSpec is the upstream contract one alias must publish: its parameter
// names, required arguments, wire path and query keys. Constants are the values
// the upstream function always sends (traffic metrics / web_source).
type swAliasSpec struct {
	path      string
	params    []string
	required  []string
	query     []string
	constants map[string]string
}

// swAliasDomain is the domain-only endpoint shape.
func swAliasDomain(path string) swAliasSpec {
	return swAliasSpec{
		path:     path,
		params:   []string{"domain"},
		required: []string{"domain"},
		query:    []string{"domain"},
	}
}

// swAliasRange is domain + start/end + country.
func swAliasRange(path string) swAliasSpec {
	return swAliasSpec{
		path:     path,
		params:   []string{"domain", "start_date", "end_date", "country"},
		required: []string{"domain"},
		query:    []string{"domain", "start_date", "end_date", "country"},
	}
}

// swAliasGeography is domain + start/end (no country filter).
func swAliasGeography(path string) swAliasSpec {
	return swAliasSpec{
		path:     path,
		params:   []string{"domain", "start_date", "end_date"},
		required: []string{"domain"},
		query:    []string{"domain", "start_date", "end_date"},
	}
}

// swAliasOrder is the upstream registration order (tools/src/aden_tools/tools/
// similarweb_tool/similarweb_tool.py).
var swAliasOrder = []string{
	"similarweb_v5_traffic_and_engagement",
	"similarweb_v5_website_rank",
	"similarweb_v5_traffic_sources",
	"similarweb_v5_geography",
	"similarweb_v5_demographics",
	"similarweb_v5_company_info",
	"similarweb_v5_top_sites_by_category",
	"similarweb_v5_referrals",
	"similarweb_v5_ppc_spend",
	"similarweb_v5_geography_details",
	"similarweb_v5_similar_sites",
	"similarweb_v5_ad_networks",
	"similarweb_v5_demographics_traffic",
	"similarweb_v5_deduplicated_audience",
	"similarweb_v5_audience_interests",
	"similarweb_v5_audience_overlap",
	"similarweb_v5_technologies",
	"similarweb_v5_leading_folders",
	"similarweb_v5_popular_pages",
	"similarweb_v5_subdomains",
	"similarweb_v5_keyword_competitors",
	"similarweb_v5_keyword_opportunities",
	"similarweb_v5_serp_features",
	"similarweb_v5_organic_keywords",
	"similarweb_v5_paid_keywords",
	"similarweb_v5_serp_players",
	"similarweb_v5_social_referrals",
	"similarweb_v5_segments_list",
	"similarweb_v5_segment_analysis",
}

// swAliasContract mirrors the upstream Python signatures for all 29 tools.
var swAliasContract = map[string]swAliasSpec{
	"similarweb_v5_traffic_and_engagement": {
		path:     "/website-analysis/websites/traffic-and-engagement",
		params:   []string{"domain", "start_date", "end_date", "country", "granularity"},
		required: []string{"domain"},
		query:    []string{"domain", "start_date", "end_date", "country", "granularity"},
		constants: map[string]string{
			"metrics":    "visits,bounce_rate,avg_visit_duration,pages_per_visit,total_page_views",
			"web_source": "desktop",
		},
	},
	"similarweb_v5_website_rank":          swAliasDomain("/website-analysis/websites/website-rank"),
	"similarweb_v5_traffic_sources":       swAliasRange("/website-analysis/websites/traffic-sources"),
	"similarweb_v5_geography":             swAliasGeography("/website-analysis/websites/traffic-geography"),
	"similarweb_v5_demographics":          swAliasDomain("/website-analysis/websites/demographics/aggregated"),
	"similarweb_v5_company_info":          swAliasDomain("/website-analysis/websites/company-info/company-info"),
	"similarweb_v5_referrals":             swAliasRange("/website-analysis/websites/referrals/aggregated"),
	"similarweb_v5_ppc_spend":             swAliasRange("/website-analysis/websites/ppc-spend"),
	"similarweb_v5_geography_details":     swAliasGeography("/website-analysis/websites/geography/aggregated"),
	"similarweb_v5_similar_sites":         swAliasDomain("/website-analysis/websites/similar-sites/aggregated"),
	"similarweb_v5_ad_networks":           swAliasRange("/website-analysis/ad-networks/aggregated"),
	"similarweb_v5_demographics_traffic":  swAliasDomain("/website-analysis/websites/traffic-by-demographics/aggregated"),
	"similarweb_v5_audience_interests":    swAliasDomain("/website-analysis/websites/audience-interests/aggregated"),
	"similarweb_v5_technologies":          swAliasDomain("/website-analysis/websites/technologies/aggregated"),
	"similarweb_v5_leading_folders":       swAliasRange("/website-analysis/websites/pages/leading-folders/aggregated"),
	"similarweb_v5_popular_pages":         swAliasRange("/website-content/pages/popular-pages/aggregated"),
	"similarweb_v5_subdomains":            swAliasRange("/website-content/subdomains/aggregated"),
	"similarweb_v5_keyword_competitors":   swAliasDomain("/website-analysis/websites/keywords-competitors/aggregated"),
	"similarweb_v5_keyword_opportunities": swAliasDomain("/website-analysis/websites/keywords-opportunities/aggregated"),
	"similarweb_v5_serp_features":         swAliasDomain("/website-analysis/websites/keywords/serp-features"),
	"similarweb_v5_organic_keywords":      swAliasRange("/website-analysis/websites/keywords/organic/aggregated"),
	"similarweb_v5_paid_keywords":         swAliasRange("/website-analysis/websites/keywords/paid/aggregated"),
	"similarweb_v5_serp_players":          swAliasDomain("/website-analysis/websites/keywords/serp-players/aggregated"),
	"similarweb_v5_social_referrals":      swAliasRange("/website-analysis/websites/social-referrals/aggregated"),
	"similarweb_v5_segments_list":         swAliasDomain("/segment-analysis/segments/describe"),
	"similarweb_v5_top_sites_by_category": {
		path:     "/website-analysis/websites/top-sites-by-category/aggregated",
		params:   []string{"category", "country"},
		required: []string{"category"},
		query:    []string{"category", "country"},
	},
	"similarweb_v5_deduplicated_audience": {
		path:     "/website-analysis/websites/deduplicated-audience",
		params:   []string{"domains", "start_date", "end_date", "country"},
		required: []string{"domains"},
		query:    []string{"domains", "start_date", "end_date", "country"},
	},
	"similarweb_v5_audience_overlap": {
		path:     "/website-analysis/websites/audience-overlap/aggregated",
		params:   []string{"domain", "compare_to"},
		required: []string{"domain", "compare_to"},
		query:    []string{"domain", "compare_to"},
	},
	"similarweb_v5_segment_analysis": {
		path:     "/segment-analysis/segments/traffic-and-engagement",
		params:   []string{"segment_id", "start_date", "end_date", "country"},
		required: []string{"segment_id"},
		query:    []string{"segment", "start_date", "end_date", "country"},
	},
}

// swAliasInput is the minimal non-default input for one upstream tool.
func swAliasInput(name string) map[string]any {
	switch name {
	case "similarweb_v5_top_sites_by_category":
		return map[string]any{"category": "Games"}
	case "similarweb_v5_deduplicated_audience":
		return map[string]any{"domains": "amazon.com,ebay.com"}
	case "similarweb_v5_audience_overlap":
		return map[string]any{"domain": "amazon.com", "compare_to": "ebay.com"}
	case "similarweb_v5_segment_analysis":
		return map[string]any{"segment_id": "seg1"}
	default:
		return map[string]any{"domain": "example.com"}
	}
}

// swAliasWireQuery is the value one query key must carry for input, applying
// the catalog defaults the aggregate executor fills before the request.
func swAliasWireQuery(key string, input map[string]any) string {
	if key == "segment" {
		return fmt.Sprint(input["segment_id"])
	}
	if value, ok := input[key]; ok {
		return fmt.Sprint(value)
	}
	switch key {
	case "country":
		return "world"
	case "granularity":
		return "monthly"
	}
	return ""
}

// swAliasRequest is one recorded fake-provider request.
type swAliasRequest struct {
	method string
	path   string
	query  url.Values
	header http.Header
}

// swAliasRecorder keeps the provider requests in call order.
type swAliasRecorder struct {
	mu       sync.Mutex
	requests []swAliasRequest
}

func (r *swAliasRecorder) add(request swAliasRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
}

func (r *swAliasRecorder) last(t *testing.T) swAliasRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("no provider request recorded")
	}
	return r.requests[len(r.requests)-1]
}

// swAliasServer starts the fake SimilarWeb provider and returns its recorder.
func swAliasServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *swAliasRecorder) {
	t.Helper()
	recorder := &swAliasRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(swAliasRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.Query(),
			header: r.Header.Clone(),
		})
		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": r.URL.Path})
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

// swAliasProbe installs the aggregate webintel tools plus the upstream-name
// aliases into a shared registry pointed at the fake provider.
func swAliasProbe(t *testing.T, server *httptest.Server, credential string) *tools.ToolRegistry {
	t.Helper()
	probe := tools.NewToolRegistry()
	opts := []webintel.Option{
		webintel.WithBaseURL("similarweb_tool", server.URL),
		webintel.WithHTTPClient(server.Client()),
		webintel.WithGetenv(func(string) string { return "" }),
	}
	if credential != "" {
		opts = append(opts, webintel.WithCredential("similarweb_tool", credential))
	}
	if installed := webintel.NewRegistry(opts...).Install(probe); installed != 3 {
		t.Fatalf("aggregate install = %d tools, want 3", installed)
	}
	if installed := InstallSimilarWebAliases(probe); installed != 29 {
		t.Fatalf("alias install = %d tools, want 29", installed)
	}
	return probe
}

// swAliasExecute runs one registry tool and decodes its JSON envelope.
func swAliasExecute(t *testing.T, probe *tools.ToolRegistry, name string, input map[string]any) (map[string]any, bool) {
	t.Helper()
	result, err := probe.GetExecutor().Execute(context.Background(), tools.ToolUse{Name: name, Input: input})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("%s content %q: %v", name, result.Content, err)
	}
	return payload, result.IsError
}

// swAliasSorted returns the sorted copy of names for order-free comparison.
func swAliasSorted(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

// swAliasMapKeys returns the sorted property keys of a schema object.
func swAliasMapKeys(t *testing.T, value any) []string {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map[string]any", value)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Parity-exempt: assembly guard. Upstream registers 29 @mcp.tool functions; the
// Go port keeps the aggregate `similarweb` tool and must publish the same names
// so allowlists, prompts and provider maps resolve. The frozen default model
// surface (39 names) deliberately does not carry these aliases.
func TestSimilarWebAliasesExposeUpstreamNames(t *testing.T) {
	probe := tools.NewToolRegistry()
	if installed := webintel.NewRegistry().Install(probe); installed != 3 {
		t.Fatalf("aggregate install = %d tools, want 3", installed)
	}
	if installed := InstallSimilarWebAliases(probe); installed != 29 {
		t.Fatalf("alias install = %d tools, want 29", installed)
	}

	if got := similarWebAggregate().ActionNames(); !reflect.DeepEqual(got, swAliasOrder) {
		t.Fatalf("alias names = %v, want the 29 upstream names in order", got)
	}
	if got, want := len(swAliasContract), len(swAliasOrder); got != want {
		t.Fatalf("contract table holds %d tools, want %d", got, want)
	}

	for _, name := range swAliasOrder {
		spec, ok := swAliasContract[name]
		if !ok {
			t.Fatalf("%s: no upstream contract entry in the test fixture", name)
		}
		tool, ok := probe.GetTool(name)
		if !ok {
			t.Fatalf("alias tool %q missing from the registry", name)
		}
		if tool.Description == "" {
			t.Fatalf("%s: alias has no description", name)
		}
		properties, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: parameters have no properties map: %#v", name, tool.Parameters)
		}
		if _, hasAction := properties["action"]; hasAction {
			t.Fatalf("%s is aliased with an action selector; it must be a single-action tool", name)
		}
		if got, want := swAliasMapKeys(t, properties), swAliasSorted(spec.params); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s parameter names = %v, want %v", name, got, want)
		}
		if got, want := swAliasSorted(requiredStrings(t, tool.Parameters)), swAliasSorted(spec.required); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s required = %v, want %v", name, got, want)
		}
		required := map[string]struct{}{}
		for _, param := range spec.required {
			required[param] = struct{}{}
		}
		for _, param := range spec.params {
			property, _ := properties[param].(map[string]any)
			if property["type"] != "string" {
				t.Fatalf("%s.%s type = %v, want string", name, param, property["type"])
			}
			defaultValue, hasDefault := property["default"]
			if _, isRequired := required[param]; isRequired {
				if hasDefault {
					t.Fatalf("%s.%s is required but declares default %v", name, param, defaultValue)
				}
				continue
			}
			if !hasDefault {
				t.Fatalf("%s.%s is optional but has no catalog default", name, param)
			}
			switch param {
			case "country":
				if defaultValue != "world" {
					t.Fatalf("%s.country default = %v, want world", name, defaultValue)
				}
			case "granularity":
				if defaultValue != "monthly" {
					t.Fatalf("%s.granularity default = %v, want monthly", name, defaultValue)
				}
			case "start_date", "end_date":
				if defaultValue != "" {
					t.Fatalf("%s.%s default = %v, want the empty (dropped) value", name, param, defaultValue)
				}
			}
		}
	}

	// The aggregate tool must keep its 29-action grouped surface.
	aggregate, ok := probe.GetTool("similarweb")
	if !ok {
		t.Fatal("aggregate similarweb tool was removed")
	}
	actionEnum, _ := aggregate.Parameters["properties"].(map[string]any)["action"].(map[string]any)
	enum, _ := actionEnum["enum"].([]string)
	if len(enum) != 29 {
		t.Fatalf("aggregate similarweb action enum = %d, want 29", len(enum))
	}
}

// requiredStrings reads a schema's required list as []string.
func requiredStrings(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["required"]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			out = append(out, text)
		}
		return out
	default:
		t.Fatalf("required has unexpected type %T", raw)
		return nil
	}
}

// Parity: tools/tests/tools/test_similarweb_tool.py::test_similarweb_v5_traffic_and_engagement_success
func TestSimilarWebAliasesDispatchThroughAggregate(t *testing.T) {
	for _, name := range swAliasOrder {
		name := name
		t.Run(name, func(t *testing.T) {
			spec := swAliasContract[name]
			server, recorder := swAliasServer(t, nil)
			probe := swAliasProbe(t, server, "test-key")
			input := swAliasInput(name)

			payload, isError := swAliasExecute(t, probe, name, input)
			if isError || payload["ok"] != true {
				t.Fatalf("%s = %v (isError=%v)", name, payload, isError)
			}
			request := recorder.last(t)
			if request.method != http.MethodGet {
				t.Fatalf("%s method = %s, want GET", name, request.method)
			}
			if request.path != spec.path {
				t.Fatalf("%s path = %s, want %s", name, request.path, spec.path)
			}
			if got := request.header.Get("api-key"); got != "test-key" {
				t.Fatalf("%s api-key = %q", name, got)
			}
			for _, key := range spec.query {
				want := swAliasWireQuery(key, input)
				got := request.query.Get(key)
				if want == "" {
					if _, present := request.query[key]; present {
						t.Fatalf("%s query %q = %q, want the empty value dropped", name, key, got)
					}
					continue
				}
				if got != want {
					t.Fatalf("%s query %q = %q, want %q", name, key, got, want)
				}
			}
			for key, want := range spec.constants {
				if got := request.query.Get(key); got != want {
					t.Fatalf("%s constant %q = %q, want %q", name, key, got, want)
				}
			}
		})
	}
}

// Parity: tools/tests/tools/test_similarweb_tool.py::test_similarweb_v5_traffic_sources_success
func TestSimilarWebAliasesMapInputs(t *testing.T) {
	t.Run("explicit-dates-forward", func(t *testing.T) {
		server, recorder := swAliasServer(t, nil)
		probe := swAliasProbe(t, server, "test-key")
		payload, isError := swAliasExecute(t, probe, "similarweb_v5_traffic_sources", map[string]any{
			"domain":     "amazon.com",
			"start_date": "2026-01-01",
			"end_date":   "2026-01-31",
			"country":    "us",
		})
		if isError || payload["ok"] != true {
			t.Fatalf("traffic_sources = %v (isError=%v)", payload, isError)
		}
		request := recorder.last(t)
		for key, want := range map[string]string{
			"domain": "amazon.com", "start_date": "2026-01-01", "end_date": "2026-01-31", "country": "us",
		} {
			if got := request.query.Get(key); got != want {
				t.Fatalf("query %q = %q, want %q", key, got, want)
			}
		}
	})

	t.Run("explicit-empty-dates-drop", func(t *testing.T) {
		server, recorder := swAliasServer(t, nil)
		probe := swAliasProbe(t, server, "test-key")
		payload, isError := swAliasExecute(t, probe, "similarweb_v5_traffic_sources", map[string]any{
			"domain": "amazon.com", "start_date": "", "end_date": "",
		})
		if isError || payload["ok"] != true {
			t.Fatalf("traffic_sources = %v (isError=%v)", payload, isError)
		}
		request := recorder.last(t)
		for _, key := range []string{"start_date", "end_date"} {
			if _, present := request.query[key]; present {
				t.Fatalf("query %q = %q, want the empty value dropped", key, request.query.Get(key))
			}
		}
		if got := request.query.Get("country"); got != "world" {
			t.Fatalf("country = %q, want the default world", got)
		}
	})

	t.Run("segment-id-renames-on-the-wire", func(t *testing.T) {
		server, recorder := swAliasServer(t, nil)
		probe := swAliasProbe(t, server, "test-key")
		payload, isError := swAliasExecute(t, probe, "similarweb_v5_segment_analysis", map[string]any{
			"segment_id": "seg-42", "country": "de",
		})
		if isError || payload["ok"] != true {
			t.Fatalf("segment_analysis = %v (isError=%v)", payload, isError)
		}
		request := recorder.last(t)
		if got := request.query.Get("segment"); got != "seg-42" {
			t.Fatalf("segment = %q, want seg-42", got)
		}
		if _, present := request.query["segment_id"]; present {
			t.Fatal("segment_id leaked onto the wire; upstream sends `segment`")
		}
		if got := request.query.Get("country"); got != "de" {
			t.Fatalf("country = %q, want de", got)
		}
	})
}

// Parity: tools/tests/tools/test_similarweb_tool.py::test_similarweb_v5_website_rank_success
func TestSimilarWebAliasKeepsAggregateRoute(t *testing.T) {
	server, recorder := swAliasServer(t, nil)
	probe := swAliasProbe(t, server, "test-key")

	payload, isError := swAliasExecute(t, probe, "similarweb", map[string]any{
		"action": "similarweb_v5_website_rank", "domain": "amazon.com",
	})
	if isError || payload["ok"] != true {
		t.Fatalf("aggregate similarweb = %v (isError=%v)", payload, isError)
	}
	if got := recorder.last(t).path; got != "/website-analysis/websites/website-rank" {
		t.Fatalf("aggregate path = %s", got)
	}

	// The alias call must produce the same provider effect as the aggregate.
	aliasPayload, aliasError := swAliasExecute(t, probe, "similarweb_v5_website_rank", map[string]any{
		"domain": "amazon.com",
	})
	if aliasError || aliasPayload["ok"] != true {
		t.Fatalf("alias rank = %v (isError=%v)", aliasPayload, aliasError)
	}
	if got := recorder.last(t).path; got != "/website-analysis/websites/website-rank" {
		t.Fatalf("alias path = %s", got)
	}
}

// Parity: tools/tests/tools/test_similarweb_tool.py::test_make_request_error_handling
func TestSimilarWebAliasesKeepErrorEnvelopes(t *testing.T) {
	t.Run("http-status", func(t *testing.T) {
		server, _ := swAliasServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("slow down"))
		})
		probe := swAliasProbe(t, server, "test-key")
		payload, isError := swAliasExecute(t, probe, "similarweb_v5_website_rank", map[string]any{
			"domain": "amazon.com",
		})
		if isError {
			t.Fatalf("expected the upstream {error} value envelope, got a tool error: %v", payload)
		}
		if got, _ := payload["error"].(string); got != "HTTP error 429: slow down" {
			t.Fatalf("error = %q, want the upstream wording", got)
		}
	})

	t.Run("missing-credentials", func(t *testing.T) {
		server, _ := swAliasServer(t, nil)
		probe := swAliasProbe(t, server, "")
		payload, isError := swAliasExecute(t, probe, "similarweb_v5_website_rank", map[string]any{
			"domain": "amazon.com",
		})
		if isError {
			t.Fatalf("expected the upstream credentials envelope, got a tool error: %v", payload)
		}
		if got, _ := payload["error"].(string); got != "SimilarWeb credentials not configured" {
			t.Fatalf("error = %q", got)
		}
		if help, _ := payload["help"].(string); help == "" {
			t.Fatal("credentials envelope lost its help text")
		}
	})
}

// Parity-exempt: assembly guard. A name a real MCP server (or an earlier lane)
// already registered wins over the alias installer, and a second install must
// not create duplicates.
func TestSimilarWebAliasesSkipExistingNamesAndDoNotDuplicate(t *testing.T) {
	probe := tools.NewToolRegistry()
	const taken = "similarweb_v5_website_rank"
	probe.Register(taken, tools.Tool{Name: taken, Description: "sentinel"},
		func(map[string]any) (any, error) { return "sentinel", nil })
	if installed := webintel.NewRegistry().Install(probe); installed != 3 {
		t.Fatalf("aggregate install = %d tools, want 3", installed)
	}

	if installed := InstallSimilarWebAliases(probe); installed != 28 {
		t.Fatalf("installed %d aliases with %s taken, want 28", installed, taken)
	}
	result, err := probe.GetExecutor().Execute(context.Background(), tools.ToolUse{Name: taken})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "sentinel" {
		t.Fatalf("%s was overwritten: %q", taken, result.Content)
	}

	before := len(probe.RegisteredNames())
	if installed := InstallSimilarWebAliases(probe); installed != 0 {
		t.Fatalf("second install registered %d tools, want 0", installed)
	}
	if got := len(probe.RegisteredNames()); got != before {
		t.Fatalf("second install changed the registry size: %d -> %d", before, got)
	}
	counts := map[string]int{}
	for _, name := range probe.RegisteredNames() {
		counts[name]++
	}
	for _, name := range swAliasOrder {
		if counts[name] != 1 {
			t.Fatalf("%s registered %d times, want exactly 1", name, counts[name])
		}
	}
	if counts["similarweb"] != 1 {
		t.Fatalf("aggregate similarweb registered %d times, want 1", counts["similarweb"])
	}
}
