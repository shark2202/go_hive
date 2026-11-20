// Jira integration (upstream jira_tool, 9 actions). Part of batch 5 lane 1;
// see atlassian_asana.go for the registry and shared helpers.
package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var batch5Lane1JiraTool = &Tool{
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

func batch5Lane1JiraImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"jira_search_issues":    batch5Lane1JiraSearchIssues,
		"jira_get_issue":        batch5Lane1JiraGetIssue,
		"jira_create_issue":     batch5Lane1JiraCreateIssue,
		"jira_list_projects":    batch5Lane1JiraListProjects,
		"jira_get_project":      batch5Lane1JiraGetProject,
		"jira_add_comment":      batch5Lane1JiraAddComment,
		"jira_update_issue":     batch5Lane1JiraUpdateIssue,
		"jira_list_transitions": batch5Lane1JiraListTransitions,
		"jira_transition_issue": batch5Lane1JiraTransitionIssue,
	}
}

func batch5Lane1JiraAuthError() map[string]any {
	return map[string]any{
		"error": "JIRA_DOMAIN, JIRA_EMAIL, and JIRA_API_TOKEN not set",
		"help":  "Create an API token at https://id.atlassian.com/manage/api-tokens",
	}
}

// batch5Lane1JiraClient resolves the domain-derived API base plus Basic auth
// pair. WithBaseURL supplies the base in tests; JIRA_DOMAIN supplies it in
// production.
func batch5Lane1JiraClient(rt *Runtime) (base, email, token string, ok bool) {
	base = batch5Lane1ModuleBase(rt, "jira_tool")
	if base == "" {
		if domain := batch5Lane1Getenv(rt, "JIRA_DOMAIN"); domain != "" {
			base = "https://" + domain + "/rest/api/3"
		}
	}
	email = batch5Lane1Getenv(rt, "JIRA_EMAIL")
	token = batch5Lane1ModuleToken(rt, "jira_tool")
	return base, email, token, base != "" && email != "" && token != ""
}

// batch5Lane1JiraDomain returns the host used in browse URLs: JIRA_DOMAIN when
// configured, otherwise the fake upstream host.
func batch5Lane1JiraDomain(rt *Runtime, base string) string {
	if domain := batch5Lane1Getenv(rt, "JIRA_DOMAIN"); domain != "" {
		return domain
	}
	if parsed, err := url.Parse(base); err == nil {
		return parsed.Host
	}
	return ""
}

// batch5Lane1JiraRequest performs one Jira API call and maps HTTP failures to
// the upstream error dicts.
func batch5Lane1JiraRequest(ctx context.Context, rt *Runtime, base, email, token, method, path string, query url.Values, payload any) map[string]any {
	headers := map[string]string{
		"Authorization": batch5Lane1BasicAuth(email, token),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	resp, err := batch5Lane1Do(ctx, rt, method, base+path, query, payload, headers)
	if err != nil {
		if batch5Lane1IsTimeout(ctx, err) {
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
			"error": fmt.Sprintf("Jira API error %d: %s", resp.Status, batch5Lane1Truncate(string(resp.Body), 500)),
		}
	}
	object, err := batch5Lane1Decode(resp.Body)
	if err != nil {
		return map[string]any{"error": "Jira request failed: " + err.Error()}
	}
	return object
}

// batch5Lane1JiraTextToADF converts plain text to an Atlassian Document Format
// document, mirroring upstream _text_to_adf.
func batch5Lane1JiraTextToADF(text string) map[string]any {
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

// batch5Lane1JiraADFToText extracts plain text from an ADF document, mirroring
// upstream _adf_to_text (blocks are newline separated, then stripped).
func batch5Lane1JiraADFToText(value any) string {
	document := batch5Lane1Map(value)
	if len(document) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, rawBlock := range batch5Lane1Slice(document["content"]) {
		block := batch5Lane1Map(rawBlock)
		for _, rawInline := range batch5Lane1Slice(block["content"]) {
			inline := batch5Lane1Map(rawInline)
			if batch5Lane1Text(inline, "type") == "text" {
				builder.WriteString(batch5Lane1Text(inline, "text"))
			}
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

// batch5Lane1JiraLabels splits the upstream comma-separated labels argument.
func batch5Lane1JiraLabels(text string) []string {
	labels := make([]string, 0)
	for _, item := range strings.Split(text, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			labels = append(labels, trimmed)
		}
	}
	return labels
}

func batch5Lane1JiraSearchIssues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
	}
	jql := stringArg(input, "jql")
	if jql == "" {
		return map[string]any{"error": "jql is required"}, nil
	}
	query := url.Values{}
	query.Set("jql", jql)
	query.Set("maxResults", strconv.Itoa(batch5Lane1Clamp(intArg(input, "max_results", 25), 1, 100)))
	query.Set("fields", stringArg(input, "fields"))

	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodGet, "/search/jql", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	issues := make([]map[string]any, 0)
	for _, rawIssue := range batch5Lane1Slice(data["issues"]) {
		issue := batch5Lane1Map(rawIssue)
		fields := batch5Lane1Map(issue["fields"])
		issues = append(issues, map[string]any{
			"key":       batch5Lane1Text(issue, "key"),
			"summary":   batch5Lane1Text(fields, "summary"),
			"status":    batch5Lane1Text(batch5Lane1Map(fields["status"]), "name"),
			"assignee":  batch5Lane1Text(batch5Lane1Map(fields["assignee"]), "displayName"),
			"priority":  batch5Lane1Text(batch5Lane1Map(fields["priority"]), "name"),
			"issuetype": batch5Lane1Text(batch5Lane1Map(fields["issuetype"]), "name"),
		})
	}
	return map[string]any{"issues": issues, "count": len(issues)}, nil
}

func batch5Lane1JiraGetIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	if issueKey == "" {
		return map[string]any{"error": "issue_key is required"}, nil
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodGet, "/issue/"+issueKey, nil, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	fields := batch5Lane1Map(data["fields"])
	labels := make([]string, 0)
	for _, rawLabel := range batch5Lane1Slice(fields["labels"]) {
		if label, ok := rawLabel.(string); ok {
			labels = append(labels, label)
		}
	}
	return map[string]any{
		"key":         batch5Lane1Text(data, "key"),
		"summary":     batch5Lane1Text(fields, "summary"),
		"description": batch5Lane1JiraADFToText(fields["description"]),
		"status":      batch5Lane1Text(batch5Lane1Map(fields["status"]), "name"),
		"assignee":    batch5Lane1Text(batch5Lane1Map(fields["assignee"]), "displayName"),
		"reporter":    batch5Lane1Text(batch5Lane1Map(fields["reporter"]), "displayName"),
		"priority":    batch5Lane1Text(batch5Lane1Map(fields["priority"]), "name"),
		"issuetype":   batch5Lane1Text(batch5Lane1Map(fields["issuetype"]), "name"),
		"project":     batch5Lane1Text(batch5Lane1Map(fields["project"]), "name"),
		"labels":      labels,
		"created":     batch5Lane1Text(fields, "created"),
		"updated":     batch5Lane1Text(fields, "updated"),
	}, nil
}

func batch5Lane1JiraCreateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
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
		fields["description"] = batch5Lane1JiraTextToADF(description)
	}
	if priority := stringArg(input, "priority"); priority != "" {
		fields["priority"] = map[string]any{"name": priority}
	}
	if labels := stringArg(input, "labels"); labels != "" {
		fields["labels"] = batch5Lane1JiraLabels(labels)
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodPost, "/issue", nil, map[string]any{"fields": fields})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	key := batch5Lane1Text(data, "key")
	return map[string]any{
		"key":    key,
		"id":     batch5Lane1Text(data, "id"),
		"url":    "https://" + batch5Lane1JiraDomain(rt, base) + "/browse/" + key,
		"status": "created",
	}, nil
}

func batch5Lane1JiraListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
	}
	query := url.Values{}
	query.Set("maxResults", strconv.Itoa(batch5Lane1Clamp(intArg(input, "max_results", 50), 1, 100)))
	if filter := stringArg(input, "query"); filter != "" {
		query.Set("query", filter)
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodGet, "/project/search", query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	projects := make([]map[string]any, 0)
	for _, rawProject := range batch5Lane1Slice(data["values"]) {
		project := batch5Lane1Map(rawProject)
		projects = append(projects, map[string]any{
			"key":          batch5Lane1Text(project, "key"),
			"name":         batch5Lane1Text(project, "name"),
			"id":           batch5Lane1Text(project, "id"),
			"project_type": batch5Lane1Text(project, "projectTypeKey"),
		})
	}
	return map[string]any{"projects": projects, "count": len(projects)}, nil
}

func batch5Lane1JiraGetProject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
	}
	projectKey := stringArg(input, "project_key")
	if projectKey == "" {
		return map[string]any{"error": "project_key is required"}, nil
	}
	query := url.Values{}
	query.Set("expand", "description,lead,issueTypes")
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodGet, "/project/"+projectKey, query, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	issueTypes := make([]map[string]any, 0)
	for _, rawType := range batch5Lane1Slice(data["issueTypes"]) {
		issueType := batch5Lane1Map(rawType)
		issueTypes = append(issueTypes, map[string]any{
			"name":    batch5Lane1Text(issueType, "name"),
			"subtask": batch5Lane1Bool(issueType, "subtask"),
		})
	}
	description := data["description"]
	if description == nil {
		description = ""
	}
	return map[string]any{
		"key":          batch5Lane1Text(data, "key"),
		"name":         batch5Lane1Text(data, "name"),
		"id":           batch5Lane1Text(data, "id"),
		"description":  description,
		"lead":         batch5Lane1Text(batch5Lane1Map(data["lead"]), "displayName"),
		"project_type": batch5Lane1Text(data, "projectTypeKey"),
		"issue_types":  issueTypes,
	}, nil
}

func batch5Lane1JiraAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	body := stringArg(input, "body")
	if issueKey == "" || body == "" {
		return map[string]any{"error": "issue_key and body are required"}, nil
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodPost, "/issue/"+issueKey+"/comment", nil, map[string]any{
		"body": batch5Lane1JiraTextToADF(body),
	})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"id":      batch5Lane1Text(data, "id"),
		"author":  batch5Lane1Text(batch5Lane1Map(data["author"]), "displayName"),
		"created": batch5Lane1Text(data, "created"),
		"status":  "created",
	}, nil
}

func batch5Lane1JiraUpdateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
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
		fields["description"] = batch5Lane1JiraTextToADF(description)
	}
	if priority := stringArg(input, "priority"); priority != "" {
		fields["priority"] = map[string]any{"name": priority}
	}
	if labels := stringArg(input, "labels"); labels != "" {
		fields["labels"] = batch5Lane1JiraLabels(labels)
	}
	if assignee := stringArg(input, "assignee_account_id"); assignee != "" {
		fields["assignee"] = map[string]any{"accountId": assignee}
	}
	if len(fields) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodPut, "/issue/"+issueKey, nil, map[string]any{"fields": fields})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"key":    issueKey,
		"status": "updated",
		"url":    "https://" + batch5Lane1JiraDomain(rt, base) + "/browse/" + issueKey,
	}, nil
}

func batch5Lane1JiraListTransitions(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
	}
	issueKey := stringArg(input, "issue_key")
	if issueKey == "" {
		return map[string]any{"error": "issue_key is required"}, nil
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodGet, "/issue/"+issueKey+"/transitions", nil, nil)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	transitions := make([]map[string]any, 0)
	for _, rawTransition := range batch5Lane1Slice(data["transitions"]) {
		transition := batch5Lane1Map(rawTransition)
		transitions = append(transitions, map[string]any{
			"id":        batch5Lane1Text(transition, "id"),
			"name":      batch5Lane1Text(transition, "name"),
			"to_status": batch5Lane1Text(batch5Lane1Map(transition["to"]), "name"),
		})
	}
	return map[string]any{"transitions": transitions, "count": len(transitions)}, nil
}

func batch5Lane1JiraTransitionIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, email, token, ok := batch5Lane1JiraClient(rt)
	if !ok {
		return batch5Lane1JiraAuthError(), nil
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
				map[string]any{"add": map[string]any{"body": batch5Lane1JiraTextToADF(comment)}},
			},
		}
	}
	data := batch5Lane1JiraRequest(ctx, rt, base, email, token, http.MethodPost, "/issue/"+issueKey+"/transitions", nil, body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"key":    issueKey,
		"status": "transitioned",
		"url":    "https://" + batch5Lane1JiraDomain(rt, base) + "/browse/" + issueKey,
	}, nil
}
