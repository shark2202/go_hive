// Jira integration (upstream jira_tool, 9 actions). Part of batch 5 lane 1;
// see atlassian_asana.go for the registry and shared helpers.
package collab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var jiraTool = &Tool{
	ID:          "jira_tool",
	Name:        "jira",
	DisplayName: "Jira",
	Summary:     "Jira integration (9 actions).",
	Actions: []Action{
		{
			Name:     "jira_search_issues",
			Summary:  "Search Jira issues using JQL.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"jql"},
			Params: map[string]any{
				"jql":         param("string"),
				"max_results": paramDefault("integer", 25),
				"fields":      paramDefault("string", "summary,status,assignee,priority,issuetype"),
			},
		},
		{
			Name:     "jira_get_issue",
			Summary:  "Get details about a Jira issue.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"issue_key"},
			Params: map[string]any{
				"issue_key": param("string"),
			},
		},
		{
			Name:     "jira_create_issue",
			Summary:  "Create a new Jira issue.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"project_key", "summary"},
			Params: map[string]any{
				"project_key": param("string"),
				"summary":     param("string"),
				"issue_type":  paramDefault("string", "Task"),
				"description": paramDefault("string", ""),
				"priority":    paramDefault("string", ""),
				"labels":      paramDefault("string", ""),
			},
		},
		{
			Name:    "jira_list_projects",
			Summary: "List Jira projects.",
			Source:  "jira_tool/jira_tool.py",
			Params: map[string]any{
				"max_results": paramDefault("integer", 50),
				"query":       paramDefault("string", ""),
			},
		},
		{
			Name:     "jira_get_project",
			Summary:  "Get details about a Jira project.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"project_key"},
			Params: map[string]any{
				"project_key": param("string"),
			},
		},
		{
			Name:     "jira_add_comment",
			Summary:  "Add a comment to a Jira issue.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"issue_key", "body"},
			Params: map[string]any{
				"issue_key": param("string"),
				"body":      param("string"),
			},
		},
		{
			Name:     "jira_update_issue",
			Summary:  "Update fields on an existing Jira issue.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"issue_key"},
			Params: map[string]any{
				"issue_key":           param("string"),
				"summary":             paramDefault("string", ""),
				"description":         paramDefault("string", ""),
				"priority":            paramDefault("string", ""),
				"labels":              paramDefault("string", ""),
				"assignee_account_id": paramDefault("string", ""),
			},
		},
		{
			Name:     "jira_list_transitions",
			Summary:  "List available status transitions for a Jira issue.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"issue_key"},
			Params: map[string]any{
				"issue_key": param("string"),
			},
		},
		{
			Name:     "jira_transition_issue",
			Summary:  "Transition a Jira issue to a new status.",
			Source:   "jira_tool/jira_tool.py",
			Required: []string{"issue_key", "transition_id"},
			Params: map[string]any{
				"issue_key":     param("string"),
				"transition_id": param("string"),
				"comment":       paramDefault("string", ""),
			},
		},
	},
}

func jiraImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"jira_search_issues":    jiraSearchIssues,
		"jira_get_issue":        jiraGetIssue,
		"jira_create_issue":     jiraCreateIssue,
		"jira_list_projects":    jiraListProjects,
		"jira_get_project":      jiraGetProject,
		"jira_add_comment":      jiraAddComment,
		"jira_update_issue":     jiraUpdateIssue,
		"jira_list_transitions": jiraListTransitions,
		"jira_transition_issue": jiraTransitionIssue,
	}
}

func jiraAuthError() map[string]any {
	return map[string]any{
		"error": "JIRA_DOMAIN, JIRA_EMAIL, and JIRA_API_TOKEN not set",
		"help":  "Create an API token at https://id.atlassian.com/manage/api-tokens",
	}
}

// jiraClient resolves the domain-derived API base plus Basic auth
// pair. WithBaseURL supplies the base in tests; JIRA_DOMAIN supplies it in
// production.
func jiraClient(rt *Runtime) (base, email, token string, ok bool) {
	base = moduleBaseURL(rt, "jira_tool")
	if base == "" {
		if domain := getenv(rt, "JIRA_DOMAIN"); domain != "" {
			base = "https://" + domain + "/rest/api/3"
		}
	}
	email = getenv(rt, "JIRA_EMAIL")
	token = moduleToken(rt, "jira_tool")
	return base, email, token, base != "" && email != "" && token != ""
}

// jiraDomain returns the host used in browse URLs: JIRA_DOMAIN when
// configured, otherwise the fake upstream host.
func jiraDomain(rt *Runtime, base string) string {
	if domain := getenv(rt, "JIRA_DOMAIN"); domain != "" {
		return domain
	}
	if parsed, err := url.Parse(base); err == nil {
		return parsed.Host
	}
	return ""
}

// jiraRequest performs one Jira API call and maps HTTP failures to
// the upstream error dicts.
func jiraRequest(ctx context.Context, rt *Runtime, base, email, token, method, path string, query url.Values, payload any) map[string]any {
	headers := map[string]string{
		"Authorization": basicAuth(email, token),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	resp, err := do(ctx, rt, method, base+path, query, payload, headers)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Jira timed out"}
		}
		return map[string]any{"error": "Jira request failed: " + err.Error()}
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Jira credentials."}
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Check your Jira permissions."}
	case http.StatusNotFound:
		return map[string]any{"error": "Not found."}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limited. Try again shortly."}
	case http.StatusNoContent:
		return map[string]any{"status": "success"}
	case http.StatusOK, http.StatusCreated:
		// Success: fall through to decoding.
	default:
		return map[string]any{
			"error": fmt.Sprintf("Jira API error %d: %s", resp.Status, truncateTextClamped(string(resp.Body), 500)),
		}
	}
	object, err := decode(resp.Body)
	if err != nil {
		return map[string]any{"error": "Jira request failed: " + err.Error()}
	}
	return object
}

// jiraTextToADF converts plain text to an Atlassian Document Format
// document, mirroring upstream _text_to_adf.
func jiraTextToADF(text string) map[string]any {
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type": "paragraph",
				"content": []any{
					map[string]any{"type": "text", "text": text},
				},
			},
		},
	}
}

// jiraADFToText extracts plain text from an ADF document, mirroring
// upstream _adf_to_text (blocks are newline separated, then stripped).
func jiraADFToText(value any) string {
	document := asMap(value)
	if len(document) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, rawBlock := range asSlice(document["content"]) {
		block := asMap(rawBlock)
		for _, rawInline := range asSlice(block["content"]) {
			inline := asMap(rawInline)
			if atlassianText(inline, "type") == "text" {
				builder.WriteString(atlassianText(inline, "text"))
			}
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

// jiraLabels splits the upstream comma-separated labels argument.
func jiraLabels(text string) []string {
	labels := make([]string, 0)
	for _, item := range strings.Split(text, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			labels = append(labels, trimmed)
		}
	}
	return labels
}

func jiraSearchIssues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	jql := stringArg(input, "jql")
	if jql == "" {
		return map[string]any{"error": "jql is required"}, nil
	}
	query := url.Values{}
	query.Set("jql", jql)
	query.Set("maxResults", strconv.Itoa(clampInt(intArg(input, "max_results", 25), 1, 100)))
	query.Set("fields", stringArg(input, "fields"))

	data := jiraRequest(ctx, rt, base, email, token, http.MethodGet, "/search/jql", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	issues := make([]map[string]any, 0)
	for _, rawIssue := range asSlice(data["issues"]) {
		issue := asMap(rawIssue)
		fields := asMap(issue["fields"])
		issues = append(issues, map[string]any{
			"key":       atlassianText(issue, "key"),
			"summary":   atlassianText(fields, "summary"),
			"status":    atlassianText(asMap(fields["status"]), "name"),
			"assignee":  atlassianText(asMap(fields["assignee"]), "displayName"),
			"priority":  atlassianText(asMap(fields["priority"]), "name"),
			"issuetype": atlassianText(asMap(fields["issuetype"]), "name"),
		})
	}
	return map[string]any{"issues": issues, "count": len(issues)}, nil
}

func jiraGetIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	if issueKey == "" {
		return map[string]any{"error": "issue_key is required"}, nil
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodGet, "/issue/"+issueKey, nil, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	fields := asMap(data["fields"])
	labels := make([]string, 0)
	for _, rawLabel := range asSlice(fields["labels"]) {
		if label, ok := rawLabel.(string); ok {
			labels = append(labels, label)
		}
	}
	return map[string]any{
		"key":         atlassianText(data, "key"),
		"summary":     atlassianText(fields, "summary"),
		"description": jiraADFToText(fields["description"]),
		"status":      atlassianText(asMap(fields["status"]), "name"),
		"assignee":    atlassianText(asMap(fields["assignee"]), "displayName"),
		"reporter":    atlassianText(asMap(fields["reporter"]), "displayName"),
		"priority":    atlassianText(asMap(fields["priority"]), "name"),
		"issuetype":   atlassianText(asMap(fields["issuetype"]), "name"),
		"project":     atlassianText(asMap(fields["project"]), "name"),
		"labels":      labels,
		"created":     atlassianText(fields, "created"),
		"updated":     atlassianText(fields, "updated"),
	}, nil
}

func jiraCreateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	projectKey := stringArg(input, "project_key")
	summary := stringArg(input, "summary")
	if projectKey == "" || summary == "" {
		return map[string]any{"error": "project_key and summary are required"}, nil
	}
	fields := map[string]any{
		"project":   map[string]any{"key": projectKey},
		"summary":   summary,
		"issuetype": map[string]any{"name": stringArg(input, "issue_type")},
	}
	if description := stringArg(input, "description"); description != "" {
		fields["description"] = jiraTextToADF(description)
	}
	if priority := stringArg(input, "priority"); priority != "" {
		fields["priority"] = map[string]any{"name": priority}
	}
	if labels := stringArg(input, "labels"); labels != "" {
		fields["labels"] = jiraLabels(labels)
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodPost, "/issue", nil, map[string]any{"fields": fields})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	key := atlassianText(data, "key")
	return map[string]any{
		"key":    key,
		"id":     atlassianText(data, "id"),
		"url":    "https://" + jiraDomain(rt, base) + "/browse/" + key,
		"status": "created",
	}, nil
}

func jiraListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	query := url.Values{}
	query.Set("maxResults", strconv.Itoa(clampInt(intArg(input, "max_results", 50), 1, 100)))
	if filter := stringArg(input, "query"); filter != "" {
		query.Set("query", filter)
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodGet, "/project/search", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	projects := make([]map[string]any, 0)
	for _, rawProject := range asSlice(data["values"]) {
		project := asMap(rawProject)
		projects = append(projects, map[string]any{
			"key":          atlassianText(project, "key"),
			"name":         atlassianText(project, "name"),
			"id":           atlassianText(project, "id"),
			"project_type": atlassianText(project, "projectTypeKey"),
		})
	}
	return map[string]any{"projects": projects, "count": len(projects)}, nil
}

func jiraGetProject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	projectKey := stringArg(input, "project_key")
	if projectKey == "" {
		return map[string]any{"error": "project_key is required"}, nil
	}
	query := url.Values{}
	query.Set("expand", "description,lead,issueTypes")
	data := jiraRequest(ctx, rt, base, email, token, http.MethodGet, "/project/"+projectKey, query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	issueTypes := make([]map[string]any, 0)
	for _, rawType := range asSlice(data["issueTypes"]) {
		issueType := asMap(rawType)
		issueTypes = append(issueTypes, map[string]any{
			"name":    atlassianText(issueType, "name"),
			"subtask": atlassianBool(issueType, "subtask"),
		})
	}
	description := data["description"]
	if description == nil {
		description = ""
	}
	return map[string]any{
		"key":          atlassianText(data, "key"),
		"name":         atlassianText(data, "name"),
		"id":           atlassianText(data, "id"),
		"description":  description,
		"lead":         atlassianText(asMap(data["lead"]), "displayName"),
		"project_type": atlassianText(data, "projectTypeKey"),
		"issue_types":  issueTypes,
	}, nil
}

func jiraAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	body := stringArg(input, "body")
	if issueKey == "" || body == "" {
		return map[string]any{"error": "issue_key and body are required"}, nil
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodPost, "/issue/"+issueKey+"/comment", nil, map[string]any{
		"body": jiraTextToADF(body),
	})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":      atlassianText(data, "id"),
		"author":  atlassianText(asMap(data["author"]), "displayName"),
		"created": atlassianText(data, "created"),
		"status":  "created",
	}, nil
}

func jiraUpdateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	if issueKey == "" {
		return map[string]any{"error": "issue_key is required"}, nil
	}
	fields := map[string]any{}
	if summary := stringArg(input, "summary"); summary != "" {
		fields["summary"] = summary
	}
	if description := stringArg(input, "description"); description != "" {
		fields["description"] = jiraTextToADF(description)
	}
	if priority := stringArg(input, "priority"); priority != "" {
		fields["priority"] = map[string]any{"name": priority}
	}
	if labels := stringArg(input, "labels"); labels != "" {
		fields["labels"] = jiraLabels(labels)
	}
	if assignee := stringArg(input, "assignee_account_id"); assignee != "" {
		fields["assignee"] = map[string]any{"accountId": assignee}
	}
	if len(fields) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodPut, "/issue/"+issueKey, nil, map[string]any{"fields": fields})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"key":    issueKey,
		"status": "updated",
		"url":    "https://" + jiraDomain(rt, base) + "/browse/" + issueKey,
	}, nil
}

func jiraListTransitions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	if issueKey == "" {
		return map[string]any{"error": "issue_key is required"}, nil
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodGet, "/issue/"+issueKey+"/transitions", nil, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	transitions := make([]map[string]any, 0)
	for _, rawTransition := range asSlice(data["transitions"]) {
		transition := asMap(rawTransition)
		transitions = append(transitions, map[string]any{
			"id":        atlassianText(transition, "id"),
			"name":      atlassianText(transition, "name"),
			"to_status": atlassianText(asMap(transition["to"]), "name"),
		})
	}
	return map[string]any{"transitions": transitions, "count": len(transitions)}, nil
}

func jiraTransitionIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := jiraClient(rt)
	if !ok {
		return jiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	transitionID := stringArg(input, "transition_id")
	if issueKey == "" || transitionID == "" {
		return map[string]any{"error": "issue_key and transition_id are required"}, nil
	}
	body := map[string]any{"transition": map[string]any{"id": transitionID}}
	if comment := stringArg(input, "comment"); comment != "" {
		body["update"] = map[string]any{
			"comment": []any{
				map[string]any{"add": map[string]any{"body": jiraTextToADF(comment)}},
			},
		}
	}
	data := jiraRequest(ctx, rt, base, email, token, http.MethodPost, "/issue/"+issueKey+"/transitions", nil, body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"key":    issueKey,
		"status": "transitioned",
		"url":    "https://" + jiraDomain(rt, base) + "/browse/" + issueKey,
	}, nil
}
