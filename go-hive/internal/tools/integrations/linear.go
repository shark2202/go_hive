// Linear integration (upstream linear_tool, 21 actions). Part of batch 4; see
// notion.go for the registry and shared helpers.
//
// The upstream client posts GraphQL documents to a single endpoint. This port
// preserves the same queries/mutations, variables and response projections:
// list actions unbox {nodes, pageInfo}; get/create/update actions return the
// payload object (or the whole data map when the field is absent).
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const linearMaxBody = 8 << 20

const (
	linearIssueCreateMutation = `
	mutation IssueCreate($input: IssueCreateInput!) {
		issueCreate(input: $input) {
			success
			issue {
				id
				identifier
				title
				description
				url
				priority
				state { id name }
				assignee { id name }
				labels { nodes { id name } }
				project { id name }
				createdAt
			}
		}
	}
	`

	linearIssueQuery = `
	query Issue($id: String!) {
		issue(id: $id) {
			id
			identifier
			title
			description
			url
			priority
			priorityLabel
			state { id name color }
			assignee { id name email }
			labels { nodes { id name color } }
			project { id name }
			team { id name key }
			comments { nodes { id body createdAt user { name } } }
			createdAt
			updatedAt
		}
	}
	`

	linearIssueUpdateMutation = `
	mutation IssueUpdate($id: String!, $input: IssueUpdateInput!) {
		issueUpdate(id: $id, input: $input) {
			success
			issue {
				id
				identifier
				title
				description
				url
				priority
				state { id name }
				assignee { id name }
				labels { nodes { id name } }
				updatedAt
			}
		}
	}
	`

	linearIssueDeleteMutation = `
	mutation IssueDelete($id: String!) {
		issueDelete(id: $id) {
			success
		}
	}
	`

	linearIssuesQuery = `
	query Issues($filter: IssueFilter, $first: Int) {
		issues(filter: $filter, first: $first) {
			nodes {
				id
				identifier
				title
				description
				url
				priority
				priorityLabel
				state { id name color }
				assignee { id name }
				labels { nodes { id name } }
				project { id name }
				team { id name key }
				createdAt
				updatedAt
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
	`

	linearCommentCreateMutation = `
	mutation CommentCreate($input: CommentCreateInput!) {
		commentCreate(input: $input) {
			success
			comment {
				id
				body
				createdAt
				user { id name }
			}
		}
	}
	`

	linearProjectCreateMutation = `
	mutation ProjectCreate($input: ProjectCreateInput!) {
		projectCreate(input: $input) {
			success
			project {
				id
				name
				description
				url
				state
				progress
				targetDate
				lead { id name }
				teams { nodes { id name } }
				createdAt
			}
		}
	}
	`

	linearProjectQuery = `
	query Project($id: String!) {
		project(id: $id) {
			id
			name
			description
			url
			state
			progress
			targetDate
			lead { id name email }
			teams { nodes { id name key } }
			issues { nodes { id identifier title state { name } } }
			createdAt
			updatedAt
		}
	}
	`

	linearProjectUpdateMutation = `
	mutation ProjectUpdate($id: String!, $input: ProjectUpdateInput!) {
		projectUpdate(id: $id, input: $input) {
			success
			project {
				id
				name
				description
				url
				state
				progress
				targetDate
				updatedAt
			}
		}
	}
	`

	linearProjectsQuery = `
	query Projects($filter: ProjectFilter, $first: Int) {
		projects(filter: $filter, first: $first) {
			nodes {
				id
				name
				description
				url
				state
				progress
				targetDate
				lead { id name }
				teams { nodes { id name } }
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
	`

	linearTeamsQuery = `
	query Teams {
		teams {
			nodes {
				id
				name
				key
				description
				private
				timezone
			}
		}
	}
	`

	linearTeamQuery = `
	query Team($id: String!) {
		team(id: $id) {
			id
			name
			key
			description
			private
			timezone
			states { nodes { id name color type position } }
			labels { nodes { id name color } }
			members { nodes { id name email } }
		}
	}
	`

	linearWorkflowStatesQuery = `
	query WorkflowStates($teamId: ID!) {
		workflowStates(filter: { team: { id: { eq: $teamId } } }) {
			nodes {
				id
				name
				color
				type
				position
				description
			}
		}
	}
	`

	linearIssueLabelCreateMutation = `
	mutation IssueLabelCreate($input: IssueLabelCreateInput!) {
		issueLabelCreate(input: $input) {
			success
			issueLabel {
				id
				name
				color
				description
			}
		}
	}
	`

	linearIssueLabelsQuery = `
	query IssueLabels($filter: IssueLabelFilter) {
		issueLabels(filter: $filter) {
			nodes {
				id
				name
				color
				description
				team { id name }
			}
		}
	}
	`

	linearCyclesQuery = `
	query Cycles($filter: CycleFilter, $first: Int) {
		cycles(filter: $filter, first: $first) {
			nodes {
				id
				number
				name
				startsAt
				endsAt
				completedAt
				progress
				scopeHistory
				issueCountHistory
			}
			pageInfo {
				hasNextPage
				endCursor
			}
		}
	}
	`

	linearIssueCommentsQuery = `
	query Issue($id: String!) {
		issue(id: $id) {
			comments(first: 50) {
				nodes {
					id
					body
					createdAt
					updatedAt
					user { id name email }
				}
			}
		}
	}
	`

	linearIssueRelationCreateMutation = `
	mutation IssueRelationCreate($input: IssueRelationCreateInput!) {
		issueRelationCreate(input: $input) {
			success
			issueRelation {
				id
				type
				issue { id identifier title }
				relatedIssue { id identifier title }
			}
		}
	}
	`

	linearUsersQuery = `
	query Users {
		users {
			nodes {
				id
				name
				displayName
				email
				active
				admin
				avatarUrl
			}
		}
	}
	`

	linearUserQuery = `
	query User($id: String!) {
		user(id: $id) {
			id
			name
			displayName
			email
			active
			admin
			avatarUrl
			assignedIssues {
				nodes {
					id
					identifier
					title
					state { name }
				}
			}
		}
	}
	`

	linearViewerQuery = `
	query Viewer {
		viewer {
			id
			name
			displayName
			email
			active
			admin
			avatarUrl
			assignedIssues {
				nodes {
					id
					identifier
					title
					state { name }
					priority
				}
			}
		}
	}
	`
)

var linearTool = &Tool{
	ID:          "linear_tool",
	Name:        "linear",
	DisplayName: "Linear",
	Summary:     "Linear integration (21 actions).",
	Actions: []Action{
		{
			Name:     "linear_cycles_list",
			Summary:  "List cycles (sprints) for a Linear team.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"team_id"},
			Params: map[string]any{
				"team_id": param("string"),
				"limit":   paramDefault("integer", 50),
			},
		},
		{
			Name:     "linear_issue_add_comment",
			Summary:  "Add a comment to a Linear issue.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"issue_id", "body"},
			Params: map[string]any{
				"issue_id": param("string"),
				"body":     param("string"),
			},
		},
		{
			Name:     "linear_issue_comments_list",
			Summary:  "List comments on a Linear issue.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"issue_id"},
			Params: map[string]any{
				"issue_id": param("string"),
			},
		},
		{
			Name:     "linear_issue_create",
			Summary:  "Create a new Linear issue.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"title", "team_id"},
			Params: map[string]any{
				"title":       param("string"),
				"team_id":     param("string"),
				"description": param("string"),
				"assignee_id": param("string"),
				"priority":    param("integer"),
				"label_ids":   param("array"),
				"project_id":  param("string"),
				"state_id":    param("string"),
			},
		},
		{
			Name:     "linear_issue_delete",
			Summary:  "Delete a Linear issue.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"issue_id"},
			Params: map[string]any{
				"issue_id": param("string"),
			},
		},
		{
			Name:     "linear_issue_get",
			Summary:  "Get a Linear issue by ID or identifier.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"issue_id"},
			Params: map[string]any{
				"issue_id": param("string"),
			},
		},
		{
			Name:     "linear_issue_relation_create",
			Summary:  "Create a relation between two Linear issues.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"issue_id", "related_issue_id"},
			Params: map[string]any{
				"issue_id":         param("string"),
				"related_issue_id": param("string"),
				"relation_type":    paramDefault("string", "related"),
			},
		},
		{
			Name:    "linear_issue_search",
			Summary: "Search Linear issues with filters.",
			Source:  "linear_tool/linear_tool.py",
			Params: map[string]any{
				"query":       param("string"),
				"team_id":     param("string"),
				"assignee_id": param("string"),
				"state_id":    param("string"),
				"label_ids":   param("array"),
				"project_id":  param("string"),
				"limit":       paramDefault("integer", 50),
			},
		},
		{
			Name:     "linear_issue_update",
			Summary:  "Update an existing Linear issue.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"issue_id"},
			Params: map[string]any{
				"issue_id":    param("string"),
				"title":       param("string"),
				"description": param("string"),
				"state_id":    param("string"),
				"assignee_id": param("string"),
				"priority":    param("integer"),
				"label_ids":   param("array"),
			},
		},
		{
			Name:     "linear_label_create",
			Summary:  "Create a new label for a team.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"name", "team_id"},
			Params: map[string]any{
				"name":        param("string"),
				"team_id":     param("string"),
				"color":       param("string"),
				"description": param("string"),
			},
		},
		{
			Name:    "linear_labels_list",
			Summary: "List all labels, optionally filtered by team.",
			Source:  "linear_tool/linear_tool.py",
			Params: map[string]any{
				"team_id": param("string"),
			},
		},
		{
			Name:     "linear_project_create",
			Summary:  "Create a new Linear project.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"name", "team_ids"},
			Params: map[string]any{
				"name":        param("string"),
				"team_ids":    param("array"),
				"description": param("string"),
				"state":       param("string"),
				"target_date": param("string"),
				"lead_id":     param("string"),
			},
		},
		{
			Name:     "linear_project_get",
			Summary:  "Get a Linear project by ID.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
			},
		},
		{
			Name:    "linear_project_list",
			Summary: "List Linear projects with optional filters.",
			Source:  "linear_tool/linear_tool.py",
			Params: map[string]any{
				"team_id": param("string"),
				"state":   param("string"),
				"limit":   paramDefault("integer", 50),
			},
		},
		{
			Name:     "linear_project_update",
			Summary:  "Update a Linear project.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id":  param("string"),
				"name":        param("string"),
				"description": param("string"),
				"state":       param("string"),
				"target_date": param("string"),
			},
		},
		{
			Name:     "linear_team_get",
			Summary:  "Get team details including workflow states and members.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"team_id"},
			Params: map[string]any{
				"team_id": param("string"),
			},
		},
		{
			Name:    "linear_teams_list",
			Summary: "List all teams in the Linear workspace.",
			Source:  "linear_tool/linear_tool.py",
		},
		{
			Name:     "linear_user_get",
			Summary:  "Get user details and assigned issues.",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"user_id"},
			Params: map[string]any{
				"user_id": param("string"),
			},
		},
		{
			Name:    "linear_users_list",
			Summary: "List all users in the Linear workspace.",
			Source:  "linear_tool/linear_tool.py",
		},
		{
			Name:    "linear_viewer",
			Summary: "Get details about the authenticated user (viewer).",
			Source:  "linear_tool/linear_tool.py",
		},
		{
			Name:     "linear_workflow_states_get",
			Summary:  "Get workflow states for a team (e.g., Backlog, Todo, In Progress, Done).",
			Source:   "linear_tool/linear_tool.py",
			Required: []string{"team_id"},
			Params: map[string]any{
				"team_id": param("string"),
			},
		},
	},
}

func linearImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"linear_cycles_list":           linearCyclesList,
		"linear_issue_add_comment":     linearIssueAddComment,
		"linear_issue_comments_list":   linearIssueCommentsList,
		"linear_issue_create":          linearIssueCreate,
		"linear_issue_delete":          linearIssueDelete,
		"linear_issue_get":             linearIssueGet,
		"linear_issue_relation_create": linearIssueRelationCreate,
		"linear_issue_search":          linearIssueSearch,
		"linear_issue_update":          linearIssueUpdate,
		"linear_label_create":          linearLabelCreate,
		"linear_labels_list":           linearLabelsList,
		"linear_project_create":        linearProjectCreate,
		"linear_project_get":           linearProjectGet,
		"linear_project_list":          linearProjectList,
		"linear_project_update":        linearProjectUpdate,
		"linear_team_get":              linearTeamGet,
		"linear_teams_list":            linearTeamsList,
		"linear_user_get":              linearUserGet,
		"linear_users_list":            linearUsersList,
		"linear_viewer":                linearViewer,
		"linear_workflow_states_get":   linearWorkflowStatesGet,
	}
}

func linearAuthError() map[string]any {
	return map[string]any{
		"error": "Linear credentials not configured",
		"help":  "Set LINEAR_API_KEY environment variable or configure via credential store. Get an API key at https://linear.app/settings/api",
	}
}

// linearExecute posts one GraphQL document. Transport failures are
// returned as Go errors so the action wrappers can apply the upstream
// timeout/network messages; API failures are returned as error dicts.
func linearExecute(ctx context.Context, rt *Runtime, query string, variables map[string]any) (map[string]any, error) {
	token := moduleToken(rt, "linear_tool")
	if token == "" {
		return linearAuthError(), nil
	}
	base := moduleBaseURL(rt, "linear_tool")
	if base == "" {
		base = "https://api.linear.app"
	}
	payload := map[string]any{"query": query}
	if variables != nil {
		payload["variables"] = variables
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": "Linear request failed: " + err.Error()}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/graphql", bytes.NewReader(encoded))
	if err != nil {
		return map[string]any{"error": "Linear request failed: " + err.Error()}, nil
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, linearMaxBody))
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Linear API key"}, nil
	case resp.StatusCode == http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions. Check your Linear API key scopes."}, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return map[string]any{"error": "Linear rate limit exceeded. Try again later."}, nil
	case resp.StatusCode >= 400:
		return map[string]any{
			"error": fmt.Sprintf("Linear API error (HTTP %d): %s", resp.StatusCode, linearErrorDetail(raw)),
		}, nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return map[string]any{
			"error": fmt.Sprintf("Linear API error (HTTP %d): %s", resp.StatusCode, linearErrorDetail(raw)),
		}, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return map[string]any{"error": "Linear API error: invalid JSON response"}, nil
	}
	if errors, present := envelope["errors"]; present {
		messages := make([]string, 0)
		for _, rawError := range asSlice(errors) {
			object := asMap(rawError)
			if message, ok := object["message"].(string); ok {
				messages = append(messages, message)
				continue
			}
			messages = append(messages, fmt.Sprint(rawError))
		}
		return map[string]any{"error": "GraphQL error: " + strings.Join(messages, "; ")}, nil
	}
	if data, ok := envelope["data"].(map[string]any); ok {
		return data, nil
	}
	return envelope, nil
}

// linearErrorDetail mirrors response.json().get("message", response.text).
func linearErrorDetail(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if message, ok := payload["message"].(string); ok {
			return message
		}
	}
	return string(raw)
}

// linearCall runs one document and translates transport errors.
func linearCall(ctx context.Context, rt *Runtime, query string, variables map[string]any) (map[string]any, error) {
	result, err := linearExecute(ctx, rt, query, variables)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, nil
		}
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	return result, nil
}

// linearUnwrap returns the named payload or the whole data map, matching
// Python's result.get(key, result).
func linearUnwrap(result map[string]any, key string) any {
	if value, present := result[key]; present {
		return value
	}
	return result
}

// linearNodes reads a connection's nodes, defaulting to an empty list.
func linearNodes(result map[string]any, connection string) []any {
	payload := asMap(result[connection])
	nodes := asSlice(payload["nodes"])
	if nodes == nil {
		return []any{}
	}
	return nodes
}

// linearHasNextPage reads connection.pageInfo.hasNextPage.
func linearHasNextPage(result map[string]any, connection string) bool {
	payload := asMap(result[connection])
	return asBool(asMap(payload["pageInfo"])["hasNextPage"])
}

func linearIssueCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{
		"title":  stringArg(input, "title"),
		"teamId": stringArg(input, "team_id"),
	}
	if description := stringArg(input, "description"); description != "" {
		inputData["description"] = description
	}
	if assignee := stringArg(input, "assignee_id"); assignee != "" {
		inputData["assigneeId"] = assignee
	}
	if priority, present := optionalInt(input, "priority"); present {
		inputData["priority"] = priority
	}
	if labels := stringSliceArg(input, "label_ids"); len(labels) > 0 {
		inputData["labelIds"] = labels
	}
	if project := stringArg(input, "project_id"); project != "" {
		inputData["projectId"] = project
	}
	if state := stringArg(input, "state_id"); state != "" {
		inputData["stateId"] = state
	}
	result, err := linearCall(ctx, rt, linearIssueCreateMutation, map[string]any{"input": inputData})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "issueCreate"), nil
}

func linearIssueGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearIssueQuery, map[string]any{"id": stringArg(input, "issue_id")})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "issue"), nil
}

func linearIssueUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{}
	if value, present := optionalString(input, "title"); present {
		inputData["title"] = value
	}
	if value, present := optionalString(input, "description"); present {
		inputData["description"] = value
	}
	if value, present := optionalString(input, "state_id"); present {
		inputData["stateId"] = value
	}
	if value, present := optionalString(input, "assignee_id"); present {
		inputData["assigneeId"] = value
	}
	if value, present := optionalInt(input, "priority"); present {
		inputData["priority"] = value
	}
	if value, present := notionOptionalStrings(input, "label_ids"); present {
		inputData["labelIds"] = value
	}
	result, err := linearCall(ctx, rt, linearIssueUpdateMutation, map[string]any{
		"id":    stringArg(input, "issue_id"),
		"input": inputData,
	})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "issueUpdate"), nil
}

func linearIssueDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearIssueDeleteMutation, map[string]any{"id": stringArg(input, "issue_id")})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "issueDelete"), nil
}

func linearIssueSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	filterData := map[string]any{}
	if query := stringArg(input, "query"); query != "" {
		filterData["or"] = []any{
			map[string]any{"title": map[string]any{"containsIgnoreCase": query}},
			map[string]any{"description": map[string]any{"containsIgnoreCase": query}},
		}
	}
	if team := stringArg(input, "team_id"); team != "" {
		filterData["team"] = map[string]any{"id": map[string]any{"eq": team}}
	}
	if assignee := stringArg(input, "assignee_id"); assignee != "" {
		filterData["assignee"] = map[string]any{"id": map[string]any{"eq": assignee}}
	}
	if state := stringArg(input, "state_id"); state != "" {
		filterData["state"] = map[string]any{"id": map[string]any{"eq": state}}
	}
	if labels := stringSliceArg(input, "label_ids"); len(labels) > 0 {
		filterData["labels"] = map[string]any{"id": map[string]any{"in": labels}}
	}
	if project := stringArg(input, "project_id"); project != "" {
		filterData["project"] = map[string]any{"id": map[string]any{"eq": project}}
	}
	limit := intArg(input, "limit", 50)
	variables := map[string]any{"first": limit}
	if limit > 100 {
		variables["first"] = 100
	}
	if len(filterData) > 0 {
		variables["filter"] = filterData
	}
	result, err := linearCall(ctx, rt, linearIssuesQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	issues := linearNodes(result, "issues")
	return map[string]any{
		"issues":      issues,
		"total":       len(issues),
		"hasNextPage": linearHasNextPage(result, "issues"),
	}, nil
}

func linearIssueAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearCommentCreateMutation, map[string]any{
		"input": map[string]any{
			"issueId": stringArg(input, "issue_id"),
			"body":    stringArg(input, "body"),
		},
	})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "commentCreate"), nil
}

func linearProjectCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{
		"name":    stringArg(input, "name"),
		"teamIds": stringSliceArg(input, "team_ids"),
	}
	if description := stringArg(input, "description"); description != "" {
		inputData["description"] = description
	}
	if state := stringArg(input, "state"); state != "" {
		inputData["state"] = state
	}
	if targetDate := stringArg(input, "target_date"); targetDate != "" {
		inputData["targetDate"] = targetDate
	}
	if lead := stringArg(input, "lead_id"); lead != "" {
		inputData["leadId"] = lead
	}
	result, err := linearCall(ctx, rt, linearProjectCreateMutation, map[string]any{"input": inputData})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "projectCreate"), nil
}

func linearProjectGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearProjectQuery, map[string]any{"id": stringArg(input, "project_id")})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "project"), nil
}

func linearProjectUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{}
	if value, present := optionalString(input, "name"); present {
		inputData["name"] = value
	}
	if value, present := optionalString(input, "description"); present {
		inputData["description"] = value
	}
	if value, present := optionalString(input, "state"); present {
		inputData["state"] = value
	}
	if value, present := optionalString(input, "target_date"); present {
		inputData["targetDate"] = value
	}
	result, err := linearCall(ctx, rt, linearProjectUpdateMutation, map[string]any{
		"id":    stringArg(input, "project_id"),
		"input": inputData,
	})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "projectUpdate"), nil
}

func linearProjectList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	filterData := map[string]any{}
	if team := stringArg(input, "team_id"); team != "" {
		filterData["accessibleTeams"] = map[string]any{"id": map[string]any{"eq": team}}
	}
	if state := stringArg(input, "state"); state != "" {
		filterData["state"] = map[string]any{"eq": state}
	}
	limit := intArg(input, "limit", 50)
	variables := map[string]any{"first": limit}
	if limit > 100 {
		variables["first"] = 100
	}
	if len(filterData) > 0 {
		variables["filter"] = filterData
	}
	result, err := linearCall(ctx, rt, linearProjectsQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	projects := linearNodes(result, "projects")
	return map[string]any{
		"projects":    projects,
		"total":       len(projects),
		"hasNextPage": linearHasNextPage(result, "projects"),
	}, nil
}

func linearTeamsList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearTeamsQuery, nil)
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	teams := linearNodes(result, "teams")
	return map[string]any{"teams": teams, "total": len(teams)}, nil
}

func linearTeamGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearTeamQuery, map[string]any{"id": stringArg(input, "team_id")})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "team"), nil
}

func linearWorkflowStatesGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearWorkflowStatesQuery, map[string]any{"teamId": stringArg(input, "team_id")})
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	states := linearNodes(result, "workflowStates")
	return map[string]any{"states": states, "total": len(states)}, nil
}

func linearLabelCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{
		"name":   stringArg(input, "name"),
		"teamId": stringArg(input, "team_id"),
	}
	if color := stringArg(input, "color"); color != "" {
		inputData["color"] = color
	}
	if description := stringArg(input, "description"); description != "" {
		inputData["description"] = description
	}
	result, err := linearCall(ctx, rt, linearIssueLabelCreateMutation, map[string]any{"input": inputData})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "issueLabelCreate"), nil
}

func linearLabelsList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	var variables map[string]any
	if team := stringArg(input, "team_id"); team != "" {
		variables = map[string]any{"filter": map[string]any{"team": map[string]any{"id": map[string]any{"eq": team}}}}
	}
	result, err := linearCall(ctx, rt, linearIssueLabelsQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	labels := linearNodes(result, "issueLabels")
	return map[string]any{"labels": labels, "total": len(labels)}, nil
}

func linearUsersList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearUsersQuery, nil)
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	users := linearNodes(result, "users")
	return map[string]any{"users": users, "total": len(users)}, nil
}

func linearUserGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearUserQuery, map[string]any{"id": stringArg(input, "user_id")})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "user"), nil
}

func linearViewer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearViewerQuery, nil)
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "viewer"), nil
}

func linearCyclesList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := intArg(input, "limit", 50)
	first := limit
	if first > 100 {
		first = 100
	}
	variables := map[string]any{
		"first":  first,
		"filter": map[string]any{"team": map[string]any{"id": map[string]any{"eq": stringArg(input, "team_id")}}},
	}
	result, err := linearCall(ctx, rt, linearCyclesQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	cycles := linearNodes(result, "cycles")
	return map[string]any{
		"cycles":      cycles,
		"total":       len(cycles),
		"hasNextPage": linearHasNextPage(result, "cycles"),
	}, nil
}

func linearIssueCommentsList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := linearCall(ctx, rt, linearIssueCommentsQuery, map[string]any{"id": stringArg(input, "issue_id")})
	if err != nil {
		return nil, err
	}
	if failure, failed := failureResult(result); failed {
		return failure, nil
	}
	issue := asMap(result["issue"])
	comments := asSlice(asMap(issue["comments"])["nodes"])
	if comments == nil {
		comments = []any{}
	}
	return map[string]any{"comments": comments, "total": len(comments)}, nil
}

func linearIssueRelationCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	relationType := stringArg(input, "relation_type")
	if relationType == "" {
		relationType = "related"
	}
	result, err := linearCall(ctx, rt, linearIssueRelationCreateMutation, map[string]any{
		"input": map[string]any{
			"issueId":        stringArg(input, "issue_id"),
			"relatedIssueId": stringArg(input, "related_issue_id"),
			"type":           relationType,
		},
	})
	if err != nil {
		return nil, err
	}
	return linearUnwrap(result, "issueRelationCreate"), nil
}
