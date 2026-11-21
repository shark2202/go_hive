package enrichment

// Lusha: catalog, eight contact/company enrichment actions and the
// shared call helper.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

var lushaTool = &Tool{
	ID:          "lusha_tool",
	Name:        "lusha",
	DisplayName: "Lusha",
	Summary:     "Lusha B2B contact and company enrichment (8 actions).",
	Actions: []Action{
		{
			Name:    "lusha_enrich_person",
			Summary: "Enrich a person with Lusha data (emails, phones, job info).",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"first_name":     integrationkit.ParamDefault("string", ""),
				"last_name":      integrationkit.ParamDefault("string", ""),
				"company_domain": integrationkit.ParamDefault("string", ""),
				"email":          integrationkit.ParamDefault("string", ""),
				"linkedin_url":   integrationkit.ParamDefault("string", ""),
			},
		},
		{
			Name:    "lusha_enrich_company",
			Summary: "Enrich a company with Lusha firmographic data.",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"domain":       integrationkit.ParamDefault("string", ""),
				"company_name": integrationkit.ParamDefault("string", ""),
			},
		},
		{
			Name:    "lusha_search_contacts",
			Summary: "Search for B2B contacts using Lusha prospecting filters.",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"seniorities":     integrationkit.ParamDefault("string", ""),
				"departments":     integrationkit.ParamDefault("string", ""),
				"company_names":   integrationkit.ParamDefault("string", ""),
				"company_domains": integrationkit.ParamDefault("string", ""),
				"country":         integrationkit.ParamDefault("string", ""),
				"page":            integrationkit.ParamDefault("integer", 0),
				"page_size":       integrationkit.ParamDefault("integer", 20),
			},
		},
		{
			Name:    "lusha_search_companies",
			Summary: "Search for companies using Lusha prospecting filters.",
			Source:  "lusha_tool/lusha_tool.py",
			Params: map[string]any{
				"company_names": integrationkit.ParamDefault("string", ""),
				"domains":       integrationkit.ParamDefault("string", ""),
				"country":       integrationkit.ParamDefault("string", ""),
				"min_employees": integrationkit.ParamDefault("integer", 0),
				"max_employees": integrationkit.ParamDefault("integer", 0),
				"page":          integrationkit.ParamDefault("integer", 0),
				"page_size":     integrationkit.ParamDefault("integer", 20),
			},
		},
		{
			Name:    "lusha_get_usage",
			Summary: "Get Lusha API credit usage statistics.",
			Source:  "lusha_tool/lusha_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "lusha_bulk_enrich_persons",
			Summary:  "Bulk enrich multiple persons in a single request.",
			Source:   "lusha_tool/lusha_tool.py",
			Required: []string{"details_json"},
			Params: map[string]any{
				"details_json": integrationkit.Param("string"),
			},
		},
		{
			Name:     "lusha_get_technologies",
			Summary:  "Get the technology stack used by a company.",
			Source:   "lusha_tool/lusha_tool.py",
			Required: []string{"domain"},
			Params: map[string]any{
				"domain": integrationkit.Param("string"),
			},
		},
		{
			Name:     "lusha_search_decision_makers",
			Summary:  "Search for decision makers (VP, C-level, Director) at companies.",
			Source:   "lusha_tool/lusha_tool.py",
			Required: []string{"company_domains"},
			Params: map[string]any{
				"company_domains": integrationkit.Param("string"),
				"country":         integrationkit.ParamDefault("string", ""),
				"page":            integrationkit.ParamDefault("integer", 0),
				"page_size":       integrationkit.ParamDefault("integer", 20),
			},
		},
	},
}

func lushaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"lusha_enrich_person":          lushaEnrichPerson,
		"lusha_enrich_company":         lushaEnrichCompany,
		"lusha_search_contacts":        lushaSearchContacts,
		"lusha_search_companies":       lushaSearchCompanies,
		"lusha_get_usage":              lushaGetUsage,
		"lusha_bulk_enrich_persons":    lushaBulkEnrichPersons,
		"lusha_get_technologies":       lushaGetTechnologies,
		"lusha_search_decision_makers": lushaSearchDecisionMakers,
	}
}

// lushaAuthError mirrors the upstream missing-key payload.
func lushaAuthError() map[string]any {
	return map[string]any{
		"error": "LUSHA_API_KEY is required",
		"help":  "Set LUSHA_API_KEY environment variable",
	}
}

// lushaBaseURL resolves the API root.
func lushaBaseURL(rt *Runtime) string {
	return baseURL(rt, "lusha_tool", "https://api.lusha.com")
}

// lushaCall performs one Lusha call; every 4xx/5xx maps to the
// upstream "HTTP <status>: <text>" error dict.
func lushaCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := moduleToken(rt, "lusha_tool")
	if token == "" {
		return lushaAuthError(), false
	}
	target := lushaBaseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{"api_key": token, "Content-Type": "application/json"}
	reply, err := doJSON(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Lusha timed out"}, false
		}
		return map[string]any{"error": "Lusha request failed: " + err.Error()}, false
	}
	if reply.Status >= 400 {
		return map[string]any{
			"error": fmt.Sprintf(
				"HTTP %d: %s",
				reply.Status,
				integrationkit.TruncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Lusha request failed: invalid JSON response"}, false
	}
	return value, true
}

// lushaPerson extracts the upstream person fields.
func lushaPerson(source map[string]any) map[string]any {
	return map[string]any{
		"first_name":      source["firstName"],
		"last_name":       source["lastName"],
		"full_name":       source["fullName"],
		"job_title":       source["jobTitle"],
		"company":         source["company"],
		"email_addresses": sliceOr(source["emailAddresses"]),
		"phone_numbers":   sliceOr(source["phoneNumbers"]),
		"linkedin_url":    source["linkedinUrl"],
		"location":        source["location"],
	}
}

// lushaCompany extracts the upstream company fields.
func lushaCompany(source map[string]any) map[string]any {
	name := firstNonEmpty(source["name"], source["companyName"])
	domain := firstNonEmpty(source["domain"], source["companyDomain"])
	return map[string]any{
		"name":           name,
		"domain":         domain,
		"industry":       source["industry"],
		"employee_count": source["employeeCount"],
		"revenue":        source["revenue"],
		"location":       source["location"],
		"description":    source["description"],
		"founded_year":   source["foundedYear"],
		"technologies":   sliceOr(source["technologies"]),
	}
}

// lushaContacts flattens a prospecting contact page.
func lushaContacts(data map[string]any) map[string]any {
	contacts := asSlice(data["data"])
	out := make([]map[string]any, 0, len(contacts))
	for _, raw := range contacts {
		contact := asMap(raw)
		out = append(out, map[string]any{
			"id":             contact["contactId"],
			"first_name":     contact["firstName"],
			"last_name":      contact["lastName"],
			"job_title":      contact["jobTitle"],
			"seniority":      contact["seniority"],
			"department":     contact["department"],
			"company_name":   contact["companyName"],
			"company_domain": contact["companyDomain"],
			"location":       contact["location"],
		})
	}
	total := any(nil)
	if value, present := data["total"]; present {
		total = jsonNumber(value)
	}
	return map[string]any{"count": len(out), "total": total, "contacts": out}
}

func lushaEnrichPerson(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := integrationkit.StringArg(input, "email")
	linkedinURL := integrationkit.StringArg(input, "linkedin_url")
	firstName := integrationkit.StringArg(input, "first_name")
	lastName := integrationkit.StringArg(input, "last_name")
	query := url.Values{}
	switch {
	case email != "":
		query.Set("email", email)
	case linkedinURL != "":
		query.Set("linkedinUrl", linkedinURL)
	case firstName != "" && lastName != "":
		query.Set("firstName", firstName)
		query.Set("lastName", lastName)
		if domain := integrationkit.StringArg(input, "company_domain"); domain != "" {
			query.Set("companyDomain", domain)
		}
	default:
		return map[string]any{"error": "Provide email, linkedinUrl, or firstName+lastName"}, nil
	}
	value, ok := lushaCall(ctx, rt, http.MethodGet, "/v2/person", query, nil)
	if !ok {
		return value, nil
	}
	return lushaPerson(asMap(value)), nil
}

func lushaEnrichCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{}
	if domain := integrationkit.StringArg(input, "domain"); domain != "" {
		query.Set("domain", domain)
	} else if name := integrationkit.StringArg(input, "company_name"); name != "" {
		query.Set("companyName", name)
	} else {
		return map[string]any{"error": "Provide domain or companyName"}, nil
	}
	value, ok := lushaCall(ctx, rt, http.MethodGet, "/v2/company", query, nil)
	if !ok {
		return value, nil
	}
	return lushaCompany(asMap(value)), nil
}

func lushaSearchContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactsInclude := map[string]any{}
	companiesInclude := map[string]any{}
	if raw := integrationkit.StringArg(input, "seniorities"); raw != "" {
		contactsInclude["seniorities"] = interfaceSlice(integrationkit.SplitCSV(raw))
	}
	if raw := integrationkit.StringArg(input, "departments"); raw != "" {
		contactsInclude["departments"] = interfaceSlice(integrationkit.SplitCSV(raw))
	}
	if country := integrationkit.StringArg(input, "country"); country != "" {
		contactsInclude["locations"] = []any{map[string]any{"country": country}}
	}
	if raw := integrationkit.StringArg(input, "company_names"); raw != "" {
		companiesInclude["names"] = interfaceSlice(integrationkit.SplitCSV(raw))
	}
	if raw := integrationkit.StringArg(input, "company_domains"); raw != "" {
		companiesInclude["domains"] = interfaceSlice(integrationkit.SplitCSV(raw))
	}
	if len(contactsInclude) == 0 && len(companiesInclude) == 0 {
		return map[string]any{"error": "At least one filter is required"}, nil
	}
	filters := map[string]any{}
	if len(contactsInclude) > 0 {
		filters["contacts"] = map[string]any{"include": contactsInclude}
	}
	if len(companiesInclude) > 0 {
		filters["companies"] = map[string]any{"include": companiesInclude}
	}
	payload := map[string]any{
		"pages":   map[string]any{"page": integrationkit.IntArg(input, "page", 0), "size": minInt(integrationkit.IntArg(input, "page_size", 20), 100)},
		"filters": filters,
	}
	value, ok := lushaCall(ctx, rt, http.MethodPost, "/prospecting/contact/search", nil, payload)
	if !ok {
		return value, nil
	}
	return lushaContacts(asMap(value)), nil
}

func lushaSearchCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	include := map[string]any{}
	if raw := integrationkit.StringArg(input, "company_names"); raw != "" {
		include["names"] = interfaceSlice(integrationkit.SplitCSV(raw))
	}
	if raw := integrationkit.StringArg(input, "domains"); raw != "" {
		include["domains"] = interfaceSlice(integrationkit.SplitCSV(raw))
	}
	if country := integrationkit.StringArg(input, "country"); country != "" {
		include["locations"] = []any{map[string]any{"country": country}}
	}
	minimum := integrationkit.IntArg(input, "min_employees", 0)
	maximum := integrationkit.IntArg(input, "max_employees", 0)
	if minimum > 0 || maximum > 0 {
		sizes := map[string]any{}
		if minimum > 0 {
			sizes["min"] = minimum
		}
		if maximum > 0 {
			sizes["max"] = maximum
		}
		include["sizes"] = []any{sizes}
	}
	if len(include) == 0 {
		return map[string]any{"error": "At least one filter is required"}, nil
	}
	payload := map[string]any{
		"pages": map[string]any{
			"page": integrationkit.IntArg(input, "page", 0),
			"size": minInt(integrationkit.IntArg(input, "page_size", 20), 100),
		},
		"filters": map[string]any{"companies": map[string]any{"include": include}},
	}
	value, ok := lushaCall(ctx, rt, http.MethodPost, "/prospecting/company/search", nil, payload)
	if !ok {
		return value, nil
	}
	data := asMap(value)
	companies := asSlice(data["data"])
	out := make([]map[string]any, 0, len(companies))
	for _, raw := range companies {
		out = append(out, lushaCompany(asMap(raw)))
	}
	total := any(nil)
	if totalRaw, present := data["total"]; present {
		total = jsonNumber(totalRaw)
	}
	return map[string]any{"count": len(out), "total": total, "companies": out}, nil
}

func lushaGetUsage(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	value, _ := lushaCall(ctx, rt, http.MethodGet, "/account/usage", nil, nil)
	return value, nil
}

func lushaBulkEnrichPersons(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if moduleToken(rt, "lusha_tool") == "" {
		return lushaAuthError(), nil
	}
	raw := integrationkit.StringArg(input, "details_json")
	var persons []any
	if err := json.Unmarshal([]byte(raw), &persons); err != nil {
		return map[string]any{"error": "Invalid JSON: " + err.Error()}, nil
	}
	if len(persons) == 0 {
		return map[string]any{"error": "details_json must be a non-empty JSON array"}, nil
	}
	if len(persons) > 50 {
		return map[string]any{"error": "Maximum 50 persons per request"}, nil
	}
	value, ok := lushaCall(
		ctx, rt, http.MethodPost, "/v2/person/bulk", nil,
		map[string]any{"contacts": persons},
	)
	if !ok {
		return value, nil
	}
	data := asMap(value)
	contacts := asSlice(data["data"])
	if contacts == nil {
		contacts = asSlice(data["contacts"])
	}
	results := make([]map[string]any, 0, len(contacts))
	for _, raw := range contacts {
		results = append(results, lushaPerson(asMap(raw)))
	}
	return map[string]any{"results": results, "count": len(results)}, nil
}

func lushaGetTechnologies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	domain := integrationkit.StringArg(input, "domain")
	if domain == "" {
		return map[string]any{"error": "domain is required"}, nil
	}
	value, ok := lushaCall(
		ctx, rt, http.MethodGet, "/v2/company",
		url.Values{"domain": {domain}}, nil,
	)
	if !ok {
		return value, nil
	}
	data := asMap(value)
	name := firstNonEmpty(data["name"], data["companyName"])
	if name == nil {
		name = ""
	}
	industry := data["industry"]
	if industry == nil {
		industry = ""
	}
	return map[string]any{
		"domain":       domain,
		"company_name": name,
		"technologies": sliceOr(data["technologies"]),
		"industry":     industry,
	}, nil
}

func lushaSearchDecisionMakers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	rawDomains := integrationkit.StringArg(input, "company_domains")
	if rawDomains == "" {
		return map[string]any{"error": "company_domains is required"}, nil
	}
	contactsInclude := map[string]any{
		"seniorities": []any{"4", "5", "6", "7"},
	}
	if country := integrationkit.StringArg(input, "country"); country != "" {
		contactsInclude["locations"] = []any{map[string]any{"country": country}}
	}
	companiesInclude := map[string]any{
		"domains": interfaceSlice(integrationkit.SplitCSV(rawDomains)),
	}
	payload := map[string]any{
		"pages": map[string]any{
			"page": integrationkit.IntArg(input, "page", 0),
			"size": minInt(integrationkit.IntArg(input, "page_size", 20), 100),
		},
		"filters": map[string]any{
			"contacts":  map[string]any{"include": contactsInclude},
			"companies": map[string]any{"include": companiesInclude},
		},
	}
	value, ok := lushaCall(ctx, rt, http.MethodPost, "/prospecting/contact/search", nil, payload)
	if !ok {
		return value, nil
	}
	return lushaContacts(asMap(value)), nil
}
