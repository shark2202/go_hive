package enrichment

// SerpAPI: catalog, eight scholar/patent/search actions and the shared
// call helper.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

var serpapiTool = &Tool{
	ID:          "serpapi_tool",
	Name:        "serpapi",
	DisplayName: "SerpAPI",
	Summary:     "Google Scholar, Google Patents and Google web search via SerpAPI (8 actions).",
	Actions: []Action{
		{
			Name:     "scholar_search",
			Summary:  "Search Google Scholar for academic papers, articles, and citations.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":        integrationkit.Param("string"),
				"num_results":  integrationkit.ParamDefault("integer", 10),
				"start":        integrationkit.ParamDefault("integer", 0),
				"year_low":     integrationkit.Param("integer"),
				"year_high":    integrationkit.Param("integer"),
				"sort_by_date": integrationkit.ParamDefault("boolean", false),
			},
		},
		{
			Name:     "scholar_get_citations",
			Summary:  "Get formatted citations for a Google Scholar paper.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"result_id"},
			Params: map[string]any{
				"result_id": integrationkit.Param("string"),
			},
		},
		{
			Name:     "scholar_get_author",
			Summary:  "Get a Google Scholar author profile with metrics and articles.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"author_id"},
			Params: map[string]any{
				"author_id":    integrationkit.Param("string"),
				"num_articles": integrationkit.ParamDefault("integer", 20),
				"start":        integrationkit.ParamDefault("integer", 0),
				"sort_by":      integrationkit.ParamDefault("string", "citedby"),
			},
		},
		{
			Name:     "patents_search",
			Summary:  "Search Google Patents for patents and patent applications.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       integrationkit.Param("string"),
				"page":        integrationkit.ParamDefault("integer", 1),
				"country":     integrationkit.Param("string"),
				"status":      integrationkit.Param("string"),
				"before_date": integrationkit.Param("string"),
				"after_date":  integrationkit.Param("string"),
			},
		},
		{
			Name:     "patents_get_details",
			Summary:  "Get detailed information for a specific patent.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"patent_id"},
			Params: map[string]any{
				"patent_id": integrationkit.Param("string"),
			},
		},
		{
			Name:     "scholar_cited_by",
			Summary:  "Get papers that cite a specific Google Scholar paper.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"cites_id"},
			Params: map[string]any{
				"cites_id":    integrationkit.Param("string"),
				"num_results": integrationkit.ParamDefault("integer", 10),
				"start":       integrationkit.ParamDefault("integer", 0),
			},
		},
		{
			Name:     "scholar_search_profiles",
			Summary:  "Search for Google Scholar author profiles by name or affiliation.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       integrationkit.Param("string"),
				"num_results": integrationkit.ParamDefault("integer", 10),
			},
		},
		{
			Name:     "serpapi_google_search",
			Summary:  "Search Google web results via SerpAPI.",
			Source:   "serpapi_tool/serpapi_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       integrationkit.Param("string"),
				"num_results": integrationkit.ParamDefault("integer", 10),
				"country":     integrationkit.Param("string"),
			},
		},
	},
}

func serpapiImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"scholar_search":          serpapiScholarSearch,
		"scholar_get_citations":   serpapiScholarCitations,
		"scholar_get_author":      serpapiScholarAuthor,
		"patents_search":          serpapiPatentsSearch,
		"patents_get_details":     serpapiPatentsDetails,
		"scholar_cited_by":        serpapiScholarCitedBy,
		"scholar_search_profiles": serpapiScholarProfiles,
		"serpapi_google_search":   serpapiGoogleSearch,
	}
}

// serpapiAuthError mirrors the upstream missing-key payload.
func serpapiAuthError() map[string]any {
	return map[string]any{
		"error": "SerpAPI credentials not configured",
		"help": "Set SERPAPI_API_KEY environment variable or configure via " +
			"credential store. Get a key at https://serpapi.com/manage-api-key",
	}
}

// serpapiBaseURL resolves the API root.
func serpapiBaseURL(rt *Runtime) string {
	return baseURL(rt, "serpapi_tool", "https://serpapi.com")
}

// serpapiCall performs one search.json request. timeoutLabel is the
// upstream per-action timeout message.
func serpapiCall(
	ctx context.Context,
	rt *Runtime,
	params url.Values,
	timeoutLabel string,
) (map[string]any, bool) {
	token := moduleToken(rt, "serpapi_tool")
	if token == "" {
		return serpapiAuthError(), false
	}
	params.Set("api_key", token)
	target := serpapiBaseURL(rt) + "/search.json?" + params.Encode()
	reply, err := doJSON(ctx, rt, http.MethodGet, target, nil, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": timeoutLabel}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{
			"error": "Invalid SerpAPI API key",
			"help":  "Check your key at https://serpapi.com/manage-api-key",
		}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "SerpAPI rate limit exceeded. Try again later."}, false
	}
	if reply.Status >= 400 {
		return map[string]any{
			"error": fmt.Sprintf(
				"SerpAPI error (HTTP %d): %s",
				reply.Status,
				errorDetail(reply.Body),
			),
		}, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "SerpAPI request failed: invalid JSON response"}, false
	}
	if rawError, present := value["error"]; present {
		return map[string]any{"error": "SerpAPI error: " + asString(rawError)}, false
	}
	return value, true
}

// serpapiAuthors renders the optional publication authors list.
func serpapiAuthors(item map[string]any) []any {
	publication := asMap(item["publication_info"])
	rawAuthors := asSlice(publication["authors"])
	if len(rawAuthors) == 0 {
		return nil
	}
	authors := make([]any, 0, len(rawAuthors))
	for _, raw := range rawAuthors {
		author := asMap(raw)
		authors = append(authors, map[string]any{
			"name":      asString(author["name"]),
			"author_id": asString(author["author_id"]),
		})
	}
	return authors
}

// serpapiCitedBy reads inline_links.cited_by defaults.
func serpapiCitedBy(item map[string]any) (any, string) {
	citedBy := asMap(asMap(item["inline_links"])["cited_by"])
	total := any(0)
	if value, present := citedBy["total"]; present {
		total = jsonNumber(value)
	}
	return total, asString(citedBy["cites_id"])
}

func serpapiScholarSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	params := url.Values{
		"engine": {"google_scholar"},
		"q":      {query},
		"num":    {strconv.Itoa(minInt(integrationkit.IntArg(input, "num_results", 10), 20))},
		"start":  {strconv.Itoa(integrationkit.IntArg(input, "start", 0))},
	}
	if year, present := integrationkit.OptionalInt(input, "year_low"); present {
		params.Set("as_ylo", strconv.Itoa(year))
	}
	if year, present := integrationkit.OptionalInt(input, "year_high"); present {
		params.Set("as_yhi", strconv.Itoa(year))
	}
	if integrationkit.BoolArg(input, "sort_by_date", false) {
		params.Set("scisbd", "1")
	}
	data, ok := serpapiCall(ctx, rt, params, "Search request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		item := asMap(raw)
		total, citesID := serpapiCitedBy(item)
		result := map[string]any{
			"title":            asString(item["title"]),
			"link":             asString(item["link"]),
			"snippet":          asString(item["snippet"]),
			"result_id":        asString(item["result_id"]),
			"publication_info": asString(asMap(item["publication_info"])["summary"]),
			"cited_by_count":   total,
			"cites_id":         citesID,
		}
		if authors := serpapiAuthors(item); authors != nil {
			result["authors"] = authors
		}
		if resources := asSlice(item["resources"]); len(resources) > 0 {
			result["pdf_link"] = asString(asMap(resources[0])["link"])
		}
		results = append(results, result)
	}
	totalResults := any(0)
	if value, present := asMap(data["search_information"])["total_results"]; present {
		totalResults = jsonNumber(value)
	}
	return map[string]any{
		"query":         query,
		"total_results": totalResults,
		"results":       results,
		"count":         len(results),
	}, nil
}

func serpapiScholarCitations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	resultID := integrationkit.StringArg(input, "result_id")
	if resultID == "" {
		return map[string]any{"error": "result_id is required"}, nil
	}
	params := url.Values{"engine": {"google_scholar_cite"}, "q": {resultID}}
	data, ok := serpapiCall(ctx, rt, params, "Citation request timed out")
	if !ok {
		return data, nil
	}
	citations := data["citations"]
	if citations == nil {
		citations = []any{}
	}
	links := data["links"]
	if links == nil {
		links = []any{}
	}
	return map[string]any{
		"result_id": resultID,
		"citations": citations,
		"links":     links,
	}, nil
}

func serpapiScholarAuthor(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	authorID := integrationkit.StringArg(input, "author_id")
	if authorID == "" {
		return map[string]any{"error": "author_id is required"}, nil
	}
	sortBy := integrationkit.StringArg(input, "sort_by")
	if sortBy == "" {
		sortBy = "citedby"
	}
	params := url.Values{
		"engine":    {"google_scholar_author"},
		"author_id": {authorID},
		"start":     {strconv.Itoa(integrationkit.IntArg(input, "start", 0))},
		"num":       {strconv.Itoa(minInt(integrationkit.IntArg(input, "num_articles", 20), 100))},
		"sort":      {sortBy},
	}
	data, ok := serpapiCall(ctx, rt, params, "Author lookup timed out")
	if !ok {
		return data, nil
	}
	author := asMap(data["author"])
	citedBy := asMap(data["cited_by"])
	metrics := map[string]any{}
	for _, raw := range asSlice(citedBy["table"]) {
		for key, value := range asMap(raw) {
			metrics[key] = value
		}
	}
	interests := make([]string, 0)
	for _, raw := range asSlice(author["interests"]) {
		interests = append(interests, asString(asMap(raw)["title"]))
	}
	articles := make([]map[string]any, 0)
	for _, raw := range asSlice(data["articles"]) {
		article := asMap(raw)
		citedByCount := any(0)
		if value, present := asMap(article["cited_by"])["value"]; present {
			citedByCount = jsonNumber(value)
		}
		articles = append(articles, map[string]any{
			"title":          asString(article["title"]),
			"authors":        asString(article["authors"]),
			"publication":    asString(article["publication"]),
			"year":           asString(article["year"]),
			"cited_by_count": citedByCount,
			"citation_id":    asString(article["citation_id"]),
		})
	}
	return map[string]any{
		"author_id":     authorID,
		"name":          asString(author["name"]),
		"affiliations":  asString(author["affiliations"]),
		"email":         asString(author["email"]),
		"interests":     interests,
		"thumbnail":     asString(author["thumbnail"]),
		"metrics":       metrics,
		"articles":      articles,
		"article_count": len(articles),
	}, nil
}

// serpapiPatent flattens one patent result.
func serpapiPatent(item map[string]any) map[string]any {
	return map[string]any{
		"title":              asString(item["title"]),
		"snippet":            asString(item["snippet"]),
		"link":               asString(item["link"]),
		"patent_id":          asString(item["patent_id"]),
		"publication_number": asString(item["publication_number"]),
		"inventor":           asString(item["inventor"]),
		"assignee":           asString(item["assignee"]),
		"filing_date":        asString(item["filing_date"]),
		"grant_date":         item["grant_date"],
		"publication_date":   asString(item["publication_date"]),
		"priority_date":      asString(item["priority_date"]),
		"pdf":                asString(item["pdf"]),
	}
}

func serpapiPatentsSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	page := integrationkit.IntArg(input, "page", 1)
	params := url.Values{
		"engine": {"google_patents"},
		"q":      {query},
		"page":   {strconv.Itoa(page)},
	}
	if country := integrationkit.StringArg(input, "country"); country != "" {
		params.Set("country", country)
	}
	if status := integrationkit.StringArg(input, "status"); status != "" {
		params.Set("status", status)
	}
	if before := integrationkit.StringArg(input, "before_date"); before != "" {
		params.Set("before", "priority:"+before)
	}
	if after := integrationkit.StringArg(input, "after_date"); after != "" {
		params.Set("after", "priority:"+after)
	}
	data, ok := serpapiCall(ctx, rt, params, "Patent search timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		results = append(results, serpapiPatent(asMap(raw)))
	}
	totalResults := any(0)
	if value, present := asMap(data["search_information"])["total_results"]; present {
		totalResults = jsonNumber(value)
	}
	return map[string]any{
		"query":         query,
		"total_results": totalResults,
		"results":       results,
		"count":         len(results),
		"page":          page,
	}, nil
}

func serpapiPatentsDetails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	patentID := integrationkit.StringArg(input, "patent_id")
	if patentID == "" {
		return map[string]any{"error": "patent_id is required"}, nil
	}
	params := url.Values{"engine": {"google_patents"}, "q": {patentID}}
	data, ok := serpapiCall(ctx, rt, params, "Patent detail request timed out")
	if !ok {
		return data, nil
	}
	results := asSlice(data["organic_results"])
	if len(results) == 0 {
		return map[string]any{"error": "No patent found for ID: " + patentID}, nil
	}
	patent := serpapiPatent(asMap(results[0]))
	patent["patent_id"] = patentID
	classifications := any(map[string]any{})
	if value, present := asMap(results[0])["classifications"]; present {
		classifications = value
	}
	patent["classifications"] = classifications
	return patent, nil
}

func serpapiScholarCitedBy(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	citesID := integrationkit.StringArg(input, "cites_id")
	if citesID == "" {
		return map[string]any{"error": "cites_id is required"}, nil
	}
	params := url.Values{
		"engine": {"google_scholar"},
		"cites":  {citesID},
		"num":    {strconv.Itoa(minInt(integrationkit.IntArg(input, "num_results", 10), 20))},
		"start":  {strconv.Itoa(integrationkit.IntArg(input, "start", 0))},
	}
	data, ok := serpapiCall(ctx, rt, params, "Cited-by request timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		item := asMap(raw)
		total, _ := serpapiCitedBy(item)
		result := map[string]any{
			"title":            asString(item["title"]),
			"link":             asString(item["link"]),
			"snippet":          asString(item["snippet"]),
			"result_id":        asString(item["result_id"]),
			"publication_info": asString(asMap(item["publication_info"])["summary"]),
			"cited_by_count":   total,
		}
		if authors := serpapiAuthors(item); authors != nil {
			result["authors"] = authors
		}
		results = append(results, result)
	}
	return map[string]any{"cites_id": citesID, "results": results, "count": len(results)}, nil
}

func serpapiScholarProfiles(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	params := url.Values{
		"engine":   {"google_scholar_profiles"},
		"mauthors": {query},
		"num":      {strconv.Itoa(minInt(integrationkit.IntArg(input, "num_results", 10), 20))},
	}
	data, ok := serpapiCall(ctx, rt, params, "Profile search timed out")
	if !ok {
		return data, nil
	}
	profiles := make([]map[string]any, 0)
	for _, raw := range asSlice(data["profiles"]) {
		profile := asMap(raw)
		interests := make([]string, 0)
		for _, interest := range asSlice(profile["interests"]) {
			interests = append(interests, asString(asMap(interest)["title"]))
		}
		citedBy := any(0)
		if value, present := profile["cited_by"]; present {
			citedBy = jsonNumber(value)
		}
		profiles = append(profiles, map[string]any{
			"name":         asString(profile["name"]),
			"author_id":    asString(profile["author_id"]),
			"affiliations": asString(profile["affiliations"]),
			"email":        asString(profile["email"]),
			"cited_by":     citedBy,
			"interests":    interests,
			"thumbnail":    asString(profile["thumbnail"]),
		})
	}
	return map[string]any{"query": query, "profiles": profiles, "count": len(profiles)}, nil
}

func serpapiGoogleSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if !validQuery(query) {
		return map[string]any{"error": "Query must be 1-500 characters"}, nil
	}
	params := url.Values{
		"engine": {"google"},
		"q":      {query},
		"num":    {strconv.Itoa(minInt(integrationkit.IntArg(input, "num_results", 10), 20))},
	}
	if country := integrationkit.StringArg(input, "country"); country != "" {
		params.Set("gl", country)
	}
	data, ok := serpapiCall(ctx, rt, params, "Google search timed out")
	if !ok {
		return data, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(data["organic_results"]) {
		item := asMap(raw)
		results = append(results, map[string]any{
			"title":          asString(item["title"]),
			"link":           asString(item["link"]),
			"snippet":        asString(item["snippet"]),
			"displayed_link": asString(item["displayed_link"]),
			"position":       item["position"],
		})
	}
	output := map[string]any{"query": query, "results": results, "count": len(results)}
	if answerBox := asMap(data["answer_box"]); answerBox != nil {
		answer := answerBox["answer"]
		if answer == nil {
			answer = answerBox["snippet"]
		}
		output["answer_box"] = map[string]any{
			"type":   asString(answerBox["type"]),
			"title":  asString(answerBox["title"]),
			"answer": answer,
		}
	}
	if graph := asMap(data["knowledge_graph"]); graph != nil {
		output["knowledge_graph"] = map[string]any{
			"title":       asString(graph["title"]),
			"type":        asString(graph["type"]),
			"description": asString(graph["description"]),
		}
	}
	return output, nil
}
