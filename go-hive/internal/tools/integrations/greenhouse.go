// Greenhouse module of batch 6 lane 2: 9 recruiting actions over the Harvest
// API v1 (Basic auth with the API token as username and an empty password).
//
// GET and POST keep the upstream error split: GET maps 401/403/404/429
// explicitly, POST only distinguishes 401/403 and treats everything else as a
// generic provider error.

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

var greenhouseTool = &Tool{
	ID:          "greenhouse_tool",
	Name:        "greenhouse",
	DisplayName: "Greenhouse",
	Summary:     "Greenhouse ATS jobs, candidates, applications, offers, and scorecards (9 actions).",
	Actions: []Action{
		{
			Name:    "greenhouse_list_jobs",
			Summary: "List jobs in Greenhouse.",
			Source:  "greenhouse_tool/greenhouse_tool.py",
			Params: map[string]any{
				"status":   paramDefault("string", ""),
				"per_page": paramDefault("integer", 50),
				"page":     paramDefault("integer", 1),
			},
		},
		{
			Name:     "greenhouse_get_job",
			Summary:  "Get details about a specific job, including hiring team and openings.",
			Source:   "greenhouse_tool/greenhouse_tool.py",
			Required: []string{"job_id"},
			Params: map[string]any{
				"job_id": param("integer"),
			},
		},
		{
			Name:    "greenhouse_list_candidates",
			Summary: "List candidates in Greenhouse, optionally filtered by job or email.",
			Source:  "greenhouse_tool/greenhouse_tool.py",
			Params: map[string]any{
				"job_id":   paramDefault("integer", 0),
				"email":    paramDefault("string", ""),
				"per_page": paramDefault("integer", 50),
				"page":     paramDefault("integer", 1),
			},
		},
		{
			Name:     "greenhouse_get_candidate",
			Summary:  "Get details about a specific candidate including applications and contact info.",
			Source:   "greenhouse_tool/greenhouse_tool.py",
			Required: []string{"candidate_id"},
			Params: map[string]any{
				"candidate_id": param("integer"),
			},
		},
		{
			Name:    "greenhouse_list_applications",
			Summary: "List applications in Greenhouse, optionally filtered by job or status.",
			Source:  "greenhouse_tool/greenhouse_tool.py",
			Params: map[string]any{
				"job_id":   paramDefault("integer", 0),
				"status":   paramDefault("string", ""),
				"per_page": paramDefault("integer", 50),
				"page":     paramDefault("integer", 1),
			},
		},
		{
			Name:     "greenhouse_get_application",
			Summary:  "Get details about a specific application including stage, source, and answers.",
			Source:   "greenhouse_tool/greenhouse_tool.py",
			Required: []string{"application_id"},
			Params: map[string]any{
				"application_id": param("integer"),
			},
		},
		{
			Name:    "greenhouse_list_offers",
			Summary: "List offers in Greenhouse, optionally for one application.",
			Source:  "greenhouse_tool/greenhouse_tool.py",
			Params: map[string]any{
				"application_id": paramDefault("integer", 0),
				"per_page":       paramDefault("integer", 50),
				"page":           paramDefault("integer", 1),
			},
		},
		{
			Name:     "greenhouse_add_candidate_note",
			Summary:  "Add a note to a candidate in Greenhouse.",
			Source:   "greenhouse_tool/greenhouse_tool.py",
			Required: []string{"candidate_id", "body"},
			Params: map[string]any{
				"candidate_id": param("integer"),
				"body":         param("string"),
				"visibility":   paramDefault("string", "public"),
			},
		},
		{
			Name:     "greenhouse_list_scorecards",
			Summary:  "List scorecards for a specific application.",
			Source:   "greenhouse_tool/greenhouse_tool.py",
			Required: []string{"application_id"},
			Params: map[string]any{
				"application_id": param("integer"),
				"per_page":       paramDefault("integer", 50),
				"page":           paramDefault("integer", 1),
			},
		},
	},
}

func greenhouseImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"greenhouse_list_jobs":          batch6Lane2GreenhouseListJobs,
		"greenhouse_get_job":            batch6Lane2GreenhouseGetJob,
		"greenhouse_list_candidates":    batch6Lane2GreenhouseListCandidates,
		"greenhouse_get_candidate":      batch6Lane2GreenhouseGetCandidate,
		"greenhouse_list_applications":  batch6Lane2GreenhouseListApplications,
		"greenhouse_get_application":    batch6Lane2GreenhouseGetApplication,
		"greenhouse_list_offers":        batch6Lane2GreenhouseListOffers,
		"greenhouse_add_candidate_note": batch6Lane2GreenhouseAddCandidateNote,
		"greenhouse_list_scorecards":    batch6Lane2GreenhouseListScorecards,
	}
}

// batch6Lane2GreenhouseAuthError mirrors the upstream missing-token payload.
func batch6Lane2GreenhouseAuthError() map[string]any {
	return map[string]any{
		"error": "GREENHOUSE_API_TOKEN not set",
		"help":  "Get your API key from Greenhouse: Configure > Dev Center > API Credential Management",
	}
}

// batch6Lane2GreenhouseBaseURL resolves the Harvest API root.
func batch6Lane2GreenhouseBaseURL(rt *Runtime) string {
	if base := moduleBaseURL(rt, "greenhouse_tool"); base != "" {
		return base
	}
	return "https://harvest.greenhouse.io/v1"
}

// batch6Lane2GreenhouseGet performs one authenticated GET. The boolean reports
// success; failures come back as the upstream error dict.
func batch6Lane2GreenhouseGet(
	ctx context.Context,
	rt *Runtime,
	path string,
	query url.Values,
) (any, bool) {
	token := supportModuleToken(rt, "greenhouse_tool")
	if token == "" {
		return batch6Lane2GreenhouseAuthError(), false
	}
	target := batch6Lane2GreenhouseBaseURL(rt) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": basicAuth(token, ""),
	}
	reply, err := supportDo(ctx, rt, http.MethodGet, target, headers, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Greenhouse timed out"}, false
		}
		return map[string]any{"error": "Greenhouse request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Greenhouse API token."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Your API key may lack the required permissions."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Resource not found."}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limited. Try again shortly."}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"Greenhouse API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), supportMaxError),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Greenhouse request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch6Lane2GreenhousePost performs one authenticated POST.
func batch6Lane2GreenhousePost(
	ctx context.Context,
	rt *Runtime,
	path string,
	payload any,
) (any, bool) {
	token := supportModuleToken(rt, "greenhouse_tool")
	if token == "" {
		return batch6Lane2GreenhouseAuthError(), false
	}
	target := batch6Lane2GreenhouseBaseURL(rt) + path
	headers := map[string]string{
		"Authorization": basicAuth(token, ""),
		"Content-Type":  "application/json",
		"On-Behalf-Of":  "",
	}
	reply, err := supportDo(ctx, rt, http.MethodPost, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Greenhouse timed out"}, false
		}
		return map[string]any{"error": "Greenhouse request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Greenhouse API token."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Your API key may lack the required permissions."}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Greenhouse API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), supportMaxError),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Greenhouse request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch6Lane2GreenhousePage builds the standard pagination query.
func batch6Lane2GreenhousePage(input map[string]any) url.Values {
	return url.Values{
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 50), 1, 500))},
		"page":     {fmt.Sprint(max(1, intArg(input, "page", 1)))},
	}
}

// batch6Lane2GreenhouseNames extracts `name` from a list of objects.
func batch6Lane2GreenhouseNames(value any) []string {
	items := asSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(asMap(item)["name"]))
	}
	return out
}

func batch6Lane2GreenhouseListJobs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := batch6Lane2GreenhousePage(input)
	if status := stringArg(input, "status"); status != "" {
		query.Set("status", status)
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, "/jobs", query)
	if !ok {
		return value, nil
	}
	jobs := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		item := asMap(raw)
		jobs = append(jobs, map[string]any{
			"id":          jsonNumber(item["id"]),
			"name":        asString(item["name"]),
			"status":      asString(item["status"]),
			"departments": batch6Lane2GreenhouseNames(item["departments"]),
			"offices":     batch6Lane2GreenhouseNames(item["offices"]),
			"created_at":  asString(item["created_at"]),
			"updated_at":  asString(item["updated_at"]),
		})
	}
	return map[string]any{"jobs": jobs, "count": len(jobs)}, nil
}

func batch6Lane2GreenhouseGetJob(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	jobID := intArg(input, "job_id", 0)
	if jobID == 0 {
		return map[string]any{"error": "job_id is required"}, nil
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, fmt.Sprintf("/jobs/%d", jobID), nil)
	if !ok {
		return value, nil
	}
	source, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	openings := make([]map[string]any, 0)
	for _, raw := range asSlice(source["openings"]) {
		opening := asMap(raw)
		openings = append(openings, map[string]any{
			"id":     jsonNumber(opening["id"]),
			"status": asString(opening["status"]),
		})
	}
	return map[string]any{
		"id":           jsonNumber(source["id"]),
		"name":         asString(source["name"]),
		"status":       asString(source["status"]),
		"confidential": boolOr(source["confidential"], false),
		"departments":  batch6Lane2GreenhouseNames(source["departments"]),
		"offices":      batch6Lane2GreenhouseNames(source["offices"]),
		"openings":     openings,
		"created_at":   asString(source["created_at"]),
		"updated_at":   asString(source["updated_at"]),
		"notes":        truncateText(asString(source["notes"]), supportMaxError),
	}, nil
}

func batch6Lane2GreenhouseListCandidates(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := batch6Lane2GreenhousePage(input)
	if jobID := intArg(input, "job_id", 0); jobID != 0 {
		query.Set("job_id", fmt.Sprint(jobID))
	}
	if email := stringArg(input, "email"); email != "" {
		query.Set("email", email)
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, "/candidates", query)
	if !ok {
		return value, nil
	}
	candidates := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		item := asMap(raw)
		candidates = append(candidates, map[string]any{
			"id":              jsonNumber(item["id"]),
			"first_name":      asString(item["first_name"]),
			"last_name":       asString(item["last_name"]),
			"company":         asString(item["company"]),
			"title":           asString(item["title"]),
			"tags":            supportStringList(item["tags"]),
			"application_ids": supportNumberList(item["application_ids"]),
			"created_at":      asString(item["created_at"]),
		})
	}
	return map[string]any{"candidates": candidates, "count": len(candidates)}, nil
}

func batch6Lane2GreenhouseGetCandidate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	candidateID := intArg(input, "candidate_id", 0)
	if candidateID == 0 {
		return map[string]any{"error": "candidate_id is required"}, nil
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, fmt.Sprintf("/candidates/%d", candidateID), nil)
	if !ok {
		return value, nil
	}
	source, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	emails := make([]string, 0)
	for _, raw := range asSlice(source["email_addresses"]) {
		emails = append(emails, asString(asMap(raw)["value"]))
	}
	phones := make([]string, 0)
	for _, raw := range asSlice(source["phone_numbers"]) {
		phones = append(phones, asString(asMap(raw)["value"]))
	}
	return map[string]any{
		"id":              jsonNumber(source["id"]),
		"first_name":      asString(source["first_name"]),
		"last_name":       asString(source["last_name"]),
		"company":         asString(source["company"]),
		"title":           asString(source["title"]),
		"emails":          emails,
		"phones":          phones,
		"tags":            supportStringList(source["tags"]),
		"application_ids": supportNumberList(source["application_ids"]),
		"created_at":      asString(source["created_at"]),
		"updated_at":      asString(source["updated_at"]),
	}, nil
}

func batch6Lane2GreenhouseListApplications(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := batch6Lane2GreenhousePage(input)
	if jobID := intArg(input, "job_id", 0); jobID != 0 {
		query.Set("job_id", fmt.Sprint(jobID))
	}
	if status := stringArg(input, "status"); status != "" {
		query.Set("status", status)
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, "/applications", query)
	if !ok {
		return value, nil
	}
	applications := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		item := asMap(raw)
		stage := asMap(item["current_stage"])
		applications = append(applications, map[string]any{
			"id":               jsonNumber(item["id"]),
			"candidate_id":     jsonNumber(item["candidate_id"]),
			"status":           asString(item["status"]),
			"current_stage":    asString(stage["name"]),
			"jobs":             batch6Lane2GreenhouseNames(item["jobs"]),
			"applied_at":       asString(item["applied_at"]),
			"last_activity_at": asString(item["last_activity_at"]),
		})
	}
	return map[string]any{"applications": applications, "count": len(applications)}, nil
}

func batch6Lane2GreenhouseGetApplication(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	applicationID := intArg(input, "application_id", 0)
	if applicationID == 0 {
		return map[string]any{"error": "application_id is required"}, nil
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, fmt.Sprintf("/applications/%d", applicationID), nil)
	if !ok {
		return value, nil
	}
	source, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	stage := asMap(source["current_stage"])
	origin := asMap(source["source"])
	answers := make([]map[string]any, 0)
	for _, raw := range asSlice(source["answers"]) {
		answer := asMap(raw)
		answers = append(answers, map[string]any{
			"question": asString(answer["question"]),
			"answer":   asString(answer["answer"]),
		})
	}
	return map[string]any{
		"id":               jsonNumber(source["id"]),
		"candidate_id":     jsonNumber(source["candidate_id"]),
		"status":           asString(source["status"]),
		"current_stage":    asString(stage["name"]),
		"source":           asString(origin["public_name"]),
		"jobs":             batch6Lane2GreenhouseNames(source["jobs"]),
		"answers":          answers,
		"applied_at":       asString(source["applied_at"]),
		"rejected_at":      source["rejected_at"],
		"last_activity_at": asString(source["last_activity_at"]),
	}, nil
}

func batch6Lane2GreenhouseListOffers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := batch6Lane2GreenhousePage(input)
	path := "/offers"
	if applicationID := intArg(input, "application_id", 0); applicationID != 0 {
		path = fmt.Sprintf("/applications/%d/offers", applicationID)
	}
	value, ok := batch6Lane2GreenhouseGet(ctx, rt, path, query)
	if !ok {
		return value, nil
	}
	offers := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		item := asMap(raw)
		offers = append(offers, map[string]any{
			"id":             jsonNumber(item["id"]),
			"application_id": jsonNumber(item["application_id"]),
			"version":        jsonNumber(item["version"]),
			"status":         asString(item["status"]),
			"starts_at":      asString(item["starts_at"]),
			"created_at":     asString(item["created_at"]),
			"updated_at":     asString(item["updated_at"]),
			"sent_at":        item["sent_at"],
			"resolved_at":    item["resolved_at"],
		})
	}
	return map[string]any{"offers": offers, "count": len(offers)}, nil
}

func batch6Lane2GreenhouseAddCandidateNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	candidateID := intArg(input, "candidate_id", 0)
	body := stringArg(input, "body")
	if candidateID == 0 || body == "" {
		return map[string]any{"error": "candidate_id and body are required"}, nil
	}
	visibility := stringArg(input, "visibility")
	if visibility == "" {
		visibility = "public"
	}
	payload := map[string]any{"body": body, "visibility": visibility}
	value, ok := batch6Lane2GreenhousePost(
		ctx, rt, fmt.Sprintf("/candidates/%d/activity_feed/notes", candidateID), payload,
	)
	if !ok {
		return value, nil
	}
	created := asMap(value)
	return map[string]any{
		"id":         jsonNumber(created["id"]),
		"body":       asString(created["body"]),
		"visibility": asString(created["visibility"]),
		"created_at": asString(created["created_at"]),
		"status":     "created",
	}, nil
}

func batch6Lane2GreenhouseListScorecards(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	applicationID := intArg(input, "application_id", 0)
	if applicationID == 0 {
		return map[string]any{"error": "application_id is required"}, nil
	}
	value, ok := batch6Lane2GreenhouseGet(
		ctx, rt, fmt.Sprintf("/applications/%d/scorecards", applicationID), batch6Lane2GreenhousePage(input),
	)
	if !ok {
		return value, nil
	}
	scorecards := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		item := asMap(raw)
		interviewer := asMap(item["interviewer"])
		interview := asMap(item["interview"])
		scorecards = append(scorecards, map[string]any{
			"id":                     jsonNumber(item["id"]),
			"interviewer_name":       asString(interviewer["name"]),
			"interviewer_id":         jsonNumber(interviewer["id"]),
			"overall_recommendation": asString(item["overall_recommendation"]),
			"submitted_at":           asString(item["submitted_at"]),
			"interview":              asString(interview["name"]),
			"created_at":             asString(item["created_at"]),
			"updated_at":             asString(item["updated_at"]),
		})
	}
	return map[string]any{"scorecards": scorecards, "count": len(scorecards)}, nil
}
