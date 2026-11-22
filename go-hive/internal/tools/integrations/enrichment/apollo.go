package enrichment

// Apollo.io: catalog, seven contact/company enrichment actions and the
// shared call helper.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

var apolloTool = &Tool{
	ID:          "apollo_tool",
	Name:        "apollo",
	DisplayName: "Apollo.io",
	Summary:     "Apollo.io contact and company data enrichment (7 actions).",
	Actions: []Action{
		{
			Name:    "apollo_enrich_person",
			Summary: "Enrich a person by email, LinkedIn URL, or name and domain.",
			Source:  "apollo_tool/apollo_tool.py",
			Params: map[string]any{
				"email":                  integrationkit.ParamDefault("string", ""),
				"linkedin_url":           integrationkit.ParamDefault("string", ""),
				"first_name":             integrationkit.ParamDefault("string", ""),
				"last_name":              integrationkit.ParamDefault("string", ""),
				"name":                   integrationkit.ParamDefault("string", ""),
				"domain":                 integrationkit.ParamDefault("string", ""),
				"reveal_personal_emails": integrationkit.ParamDefault("boolean", false),
				"reveal_phone_number":    integrationkit.ParamDefault("boolean", false),
			},
		},
		{
			Name:     "apollo_enrich_company",
			Summary:  "Enrich a company by domain.",
			Source:   "apollo_tool/apollo_tool.py",
			Required: []string{"domain"},
			Params: map[string]any{
				"domain": integrationkit.Param("string"),
			},
		},
		{
			Name:    "apollo_search_people",
			Summary: "Search for contacts with titles, seniority, location and firmographic filters.",
			Source:  "apollo_tool/apollo_tool.py",
			Params: map[string]any{
				"titles":        integrationkit.Param("array"),
				"seniorities":   integrationkit.Param("array"),
				"locations":     integrationkit.Param("array"),
				"company_sizes": integrationkit.Param("array"),
				"industries":    integrationkit.Param("array"),
				"technologies":  integrationkit.Param("array"),
				"limit":         integrationkit.ParamDefault("integer", 10),
			},
		},
		{
			Name:     "apollo_get_person_activities",
			Summary:  "Get activity history for a person (emails, calls, tasks).",
			Source:   "apollo_tool/apollo_tool.py",
			Required: []string{"person_id"},
			Params: map[string]any{
				"person_id": integrationkit.Param("string"),
			},
		},
		{
			Name:    "apollo_list_email_accounts",
			Summary: "List email accounts connected to Apollo.",
			Source:  "apollo_tool/apollo_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "apollo_bulk_enrich_people",
			Summary:  "Bulk enrich up to 10 people at once by email or domain+name.",
			Source:   "apollo_tool/apollo_tool.py",
			Required: []string{"details_json"},
			Params: map[string]any{
				"details_json": integrationkit.Param("string"),
			},
		},
		{
			Name:    "apollo_search_companies",
			Summary: "Search for companies with industry, size, location and technology filters.",
			Source:  "apollo_tool/apollo_tool.py",
			Params: map[string]any{
				"industries":      integrationkit.Param("array"),
				"employee_counts": integrationkit.Param("array"),
				"locations":       integrationkit.Param("array"),
				"technologies":    integrationkit.Param("array"),
				"limit":           integrationkit.ParamDefault("integer", 10),
			},
		},
	},
}

func apolloImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"apollo_enrich_person":         apolloEnrichPerson,
		"apollo_enrich_company":        apolloEnrichCompany,
		"apollo_search_people":         apolloSearchPeople,
		"apollo_get_person_activities": apolloGetPersonActivities,
		"apollo_list_email_accounts":   apolloListEmailAccounts,
		"apollo_bulk_enrich_people":    apolloBulkEnrichPeople,
		"apollo_search_companies":      apolloSearchCompanies,
	}
}

// apolloAuthError mirrors the upstream missing-key payload.
func apolloAuthError() map[string]any {
	return map[string]any{
		"error": "Apollo credentials not configured",
		"help": "Set APOLLO_API_KEY environment variable " +
			"or configure via credential store. " +
			"Get your API key at https://app.apollo.io/#/settings/integrations/api",
	}
}

// apolloBaseURL resolves the API root.
func apolloBaseURL(rt *Runtime) string {
	return baseURL(rt, "apollo_tool", "https://api.apollo.io/api/v1")
}

// apolloCall performs one Apollo call and maps HTTP failures to the
// upstream error dicts. The boolean reports success.
func apolloCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := moduleToken(rt, "apollo_tool")
	if token == "" {
		return apolloAuthError(), false
	}
	target := apolloBaseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"Cache-Control": "no-cache",
		"X-Api-Key":     token,
	}
	reply, err := doJSON(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	if failure := apolloStatus(reply); failure != nil {
		return failure, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Apollo request failed: invalid JSON response"}, false
	}
	return value, true
}

// apolloStatus maps the upstream _handle_response status table.
func apolloStatus(reply httpReply) map[string]any {
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid Apollo API key"}
	case http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient credits or permissions. Check your Apollo plan.",
			"help":  "Apollo uses export credits for enrichment. Visit https://app.apollo.io/#/settings/plans",
		}
	case http.StatusNotFound:
		return map[string]any{"error": "Resource not found"}
	case http.StatusUnprocessableEntity:
		return map[string]any{"error": "Invalid parameters: " + errorDetail(reply.Body)}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Apollo rate limit exceeded. Try again later."}
	}
	if reply.Status >= 400 {
		return map[string]any{
			"error": fmt.Sprintf(
				"Apollo API error (HTTP %d): %s",
				reply.Status,
				errorDetail(reply.Body),
			),
		}
	}
	return nil
}

// apolloPerson flattens a person the way enrich_person does.
func apolloPerson(person map[string]any) map[string]any {
	organization := asMap(person["organization"])
	phoneNumbers := sliceOr(person["phone_numbers"])
	return map[string]any{
		"id":            person["id"],
		"first_name":    person["first_name"],
		"last_name":     person["last_name"],
		"name":          person["name"],
		"title":         person["title"],
		"email":         person["email"],
		"email_status":  person["email_status"],
		"phone_numbers": phoneNumbers,
		"linkedin_url":  person["linkedin_url"],
		"twitter_url":   person["twitter_url"],
		"city":          person["city"],
		"state":         person["state"],
		"country":       person["country"],
		"organization": map[string]any{
			"id":             organization["id"],
			"name":           organization["name"],
			"domain":         organization["primary_domain"],
			"industry":       organization["industry"],
			"employee_count": organization["estimated_num_employees"],
		},
	}
}

// apolloOrganization flattens a company the way enrich_company does.
func apolloOrganization(source map[string]any) map[string]any {
	return map[string]any{
		"id":                        source["id"],
		"name":                      source["name"],
		"domain":                    source["primary_domain"],
		"website_url":               source["website_url"],
		"linkedin_url":              source["linkedin_url"],
		"twitter_url":               source["twitter_url"],
		"facebook_url":              source["facebook_url"],
		"industry":                  source["industry"],
		"keywords":                  sliceOr(source["keywords"]),
		"employee_count":            source["estimated_num_employees"],
		"employee_count_range":      source["employee_count_range"],
		"annual_revenue":            source["annual_revenue"],
		"annual_revenue_printed":    source["annual_revenue_printed"],
		"total_funding":             source["total_funding"],
		"total_funding_printed":     source["total_funding_printed"],
		"latest_funding_round_date": source["latest_funding_round_date"],
		"latest_funding_stage":      source["latest_funding_stage"],
		"founded_year":              source["founded_year"],
		"phone":                     source["phone"],
		"city":                      source["city"],
		"state":                     source["state"],
		"country":                   source["country"],
		"street_address":            source["street_address"],
		"technologies":              sliceOr(source["technologies"]),
		"short_description":         source["short_description"],
	}
}

// apolloScope flattens a search result's organization block.
func apolloScope(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{"id": nil, "name": nil, "domain": nil}
	}
	return map[string]any{
		"id":     source["id"],
		"name":   source["name"],
		"domain": source["primary_domain"],
	}
}

// apolloPagination reads the upstream pagination defaults.
func apolloPagination(result map[string]any, total, limit int) (any, any, any) {
	pagination := asMap(result["pagination"])
	totalEntries := any(total)
	if value, present := pagination["total_entries"]; present && value != nil {
		totalEntries = jsonNumber(value)
	}
	page := any(1)
	if value, present := pagination["page"]; present && value != nil {
		page = jsonNumber(value)
	}
	perPage := any(limit)
	if value, present := pagination["per_page"]; present && value != nil {
		perPage = jsonNumber(value)
	}
	return totalEntries, page, perPage
}

// apolloFilters appends the non-empty list filters upstream sends.
func apolloFilters(input map[string]any, body map[string]any, pairs map[string]string) {
	for name, field := range pairs {
		values := integrationkit.StringSliceArg(input, name)
		if len(values) > 0 {
			body[field] = interfaceSlice(values)
		}
	}
}

func apolloEnrichPerson(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := integrationkit.StringArg(input, "email")
	linkedinURL := integrationkit.StringArg(input, "linkedin_url")
	firstName := integrationkit.StringArg(input, "first_name")
	lastName := integrationkit.StringArg(input, "last_name")
	name := integrationkit.StringArg(input, "name")
	domain := integrationkit.StringArg(input, "domain")
	hasEmailOrLinkedIn := email != "" || linkedinURL != ""
	hasNameAndDomain := (firstName != "" && lastName != "" && domain != "") || (name != "" && domain != "")
	if !hasEmailOrLinkedIn && !hasNameAndDomain {
		return map[string]any{
			"error": "Invalid search criteria. Provide either (email), (linkedin_url), or (name/first_name+last_name AND domain).",
		}, nil
	}
	body := map[string]any{
		"reveal_personal_emails": integrationkit.BoolArg(input, "reveal_personal_emails", false),
		"reveal_phone_number":    integrationkit.BoolArg(input, "reveal_phone_number", false),
	}
	for field, value := range map[string]string{
		"email":        email,
		"linkedin_url": linkedinURL,
		"first_name":   firstName,
		"last_name":    lastName,
		"name":         name,
		"domain":       domain,
	} {
		if value != "" {
			body[field] = value
		}
	}
	var query url.Values
	if !hasEmailOrLinkedIn {
		query = url.Values{}
		for field, value := range body {
			switch typed := value.(type) {
			case string:
				query.Set(field, typed)
			case bool:
				query.Set(field, strconv.FormatBool(typed))
			default:
				query.Set(field, fmt.Sprint(typed))
			}
		}
	}
	value, ok := apolloCall(ctx, rt, http.MethodPost, "/people/match", query, body)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	person, present := result["person"]
	if !present || person == nil {
		return map[string]any{"match_found": false, "message": "No matching person found"}, nil
	}
	return map[string]any{
		"match_found": true,
		"person":      apolloPerson(asMap(person)),
	}, nil
}

func apolloEnrichCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	domain := integrationkit.StringArg(input, "domain")
	value, ok := apolloCall(
		ctx, rt, http.MethodPost, "/organizations/enrich", nil,
		map[string]any{"domain": domain},
	)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	organization, present := result["organization"]
	if !present || organization == nil {
		return map[string]any{"match_found": false, "message": "No matching company found"}, nil
	}
	return map[string]any{
		"match_found":  true,
		"organization": apolloOrganization(asMap(organization)),
	}, nil
}

func apolloSearchPeople(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := integrationkit.IntArg(input, "limit", 10)
	body := map[string]any{
		"per_page": minInt(limit, 100),
		"page":     1,
	}
	apolloFilters(input, body, map[string]string{
		"titles":        "person_titles",
		"seniorities":   "person_seniorities",
		"locations":     "person_locations",
		"company_sizes": "organization_num_employees_ranges",
		"industries":    "organization_industry_tag_ids",
		"technologies":  "currently_using_any_of_technology_uids",
	})
	value, ok := apolloCall(ctx, rt, http.MethodPost, "/mixed_people/search", nil, body)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	people := asSlice(result["people"])
	total, page, perPage := apolloPagination(result, len(people), limit)
	results := make([]map[string]any, 0, len(people))
	for _, raw := range people {
		person := asMap(raw)
		var organization map[string]any
		if scope, present := person["organization"]; present && scope != nil {
			organization = apolloScope(asMap(scope))
		} else {
			organization = apolloScope(nil)
		}
		results = append(results, map[string]any{
			"id":           person["id"],
			"first_name":   person["first_name"],
			"last_name":    person["last_name"],
			"name":         person["name"],
			"title":        person["title"],
			"email":        person["email"],
			"email_status": person["email_status"],
			"linkedin_url": person["linkedin_url"],
			"city":         person["city"],
			"state":        person["state"],
			"country":      person["country"],
			"seniority":    person["seniority"],
			"organization": organization,
		})
	}
	return map[string]any{
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"results":  results,
	}, nil
}

func apolloGetPersonActivities(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	personID := integrationkit.StringArg(input, "person_id")
	if personID == "" {
		return map[string]any{"error": "person_id is required"}, nil
	}
	value, ok := apolloCall(
		ctx, rt, http.MethodGet, "/activities",
		url.Values{"contact_id": {personID}}, nil,
	)
	if !ok {
		return value, nil
	}
	activities := asSlice(asMap(value)["activities"])
	limit := len(activities)
	if limit > 50 {
		limit = 50
	}
	out := make([]map[string]any, 0, limit)
	for _, raw := range activities[:limit] {
		activity := asMap(raw)
		out = append(out, map[string]any{
			"id":           activity["id"],
			"type":         activity["type"],
			"subject":      activity["subject"],
			"body":         integrationkit.TruncateText(asString(activity["body"]), maxErrorBytes),
			"created_at":   activity["created_at"],
			"completed_at": activity["completed_at"],
			"status":       activity["status"],
			"priority":     activity["priority"],
		})
	}
	return map[string]any{
		"contact_id": personID,
		"count":      len(out),
		"activities": out,
	}, nil
}

func apolloListEmailAccounts(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	value, ok := apolloCall(ctx, rt, http.MethodGet, "/email_accounts", nil, nil)
	if !ok {
		return value, nil
	}
	accounts := asSlice(asMap(value)["email_accounts"])
	out := make([]map[string]any, 0, len(accounts))
	for _, raw := range accounts {
		account := asMap(raw)
		out = append(out, map[string]any{
			"id":                  account["id"],
			"email":               account["email"],
			"type":                account["type"],
			"active":              account["active"],
			"default":             account["default"],
			"last_synced_at":      account["last_synced_at"],
			"sending_daily_limit": account["sending_daily_limit"],
			"emails_sent_today":   account["emails_sent_today"],
		})
	}
	return map[string]any{"count": len(out), "email_accounts": out}, nil
}

func apolloBulkEnrichPeople(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	raw := integrationkit.StringArg(input, "details_json")
	if raw == "" {
		return map[string]any{"error": "details_json is required"}, nil
	}
	var details []any
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		return map[string]any{"error": "details_json must be valid JSON"}, nil
	}
	if len(details) == 0 {
		return map[string]any{"error": "details_json must be a non-empty JSON array"}, nil
	}
	if len(details) > 10 {
		return map[string]any{"error": "maximum 10 people per bulk request"}, nil
	}
	value, ok := apolloCall(
		ctx, rt, http.MethodPost, "/people/bulk_match", nil,
		map[string]any{"details": details},
	)
	if !ok {
		return value, nil
	}
	matches := asSlice(asMap(value)["matches"])
	results := make([]map[string]any, 0, len(matches))
	for _, raw := range matches {
		match := asMap(raw)
		if match == nil {
			results = append(results, map[string]any{"match_found": false})
			continue
		}
		results = append(results, map[string]any{
			"match_found":       true,
			"id":                match["id"],
			"name":              match["name"],
			"title":             match["title"],
			"email":             match["email"],
			"email_status":      match["email_status"],
			"linkedin_url":      match["linkedin_url"],
			"organization_name": asMap(match["organization"])["name"],
		})
	}
	return map[string]any{"count": len(results), "results": results}, nil
}

func apolloSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := integrationkit.IntArg(input, "limit", 10)
	body := map[string]any{
		"per_page": minInt(limit, 100),
		"page":     1,
	}
	apolloFilters(input, body, map[string]string{
		"industries":      "organization_industry_tag_ids",
		"employee_counts": "organization_num_employees_ranges",
		"locations":       "organization_locations",
		"technologies":    "currently_using_any_of_technology_uids",
	})
	value, ok := apolloCall(ctx, rt, http.MethodPost, "/mixed_companies/search", nil, body)
	if !ok {
		return value, nil
	}
	result := asMap(value)
	organizations := asSlice(result["organizations"])
	total, page, perPage := apolloPagination(result, len(organizations), limit)
	results := make([]map[string]any, 0, len(organizations))
	for _, raw := range organizations {
		organization := asMap(raw)
		results = append(results, map[string]any{
			"id":                     organization["id"],
			"name":                   organization["name"],
			"domain":                 organization["primary_domain"],
			"website_url":            organization["website_url"],
			"linkedin_url":           organization["linkedin_url"],
			"industry":               organization["industry"],
			"employee_count":         organization["estimated_num_employees"],
			"employee_count_range":   organization["employee_count_range"],
			"annual_revenue_printed": organization["annual_revenue_printed"],
			"city":                   organization["city"],
			"state":                  organization["state"],
			"country":                organization["country"],
			"short_description":      organization["short_description"],
		})
	}
	return map[string]any{
		"total":    total,
		"page":     page,
		"per_page": perPage,
		"results":  results,
	}, nil
}
