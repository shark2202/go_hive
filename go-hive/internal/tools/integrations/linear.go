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

const batch4LinearMaxBody = 8 << 20

const (
	batch4LinearIssueCreateMutation = `
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

	batch4LinearIssueQuery = `
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

	batch4LinearIssueUpdateMutation = `
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

	batch4LinearIssueDeleteMutation = `
	mutation IssueDelete($id: String!) {
		issueDelete(id: $id) {
			success
		}
	}
	`

	batch4LinearIssuesQuery = `
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

	batch4LinearCommentCreateMutation = `
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

	batch4LinearProjectCreateMutation = `
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

	batch4LinearProjectQuery = `
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

	batch4LinearProjectUpdateMutation = `
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

	batch4LinearProjectsQuery = `
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

	batch4LinearTeamsQuery = `
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

	batch4LinearTeamQuery = `
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

	batch4LinearWorkflowStatesQuery = `
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

	batch4LinearIssueLabelCreateMutation = `
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

	batch4LinearIssueLabelsQuery = `
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

	batch4LinearCyclesQuery = `
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

	batch4LinearIssueCommentsQuery = `
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

	batch4LinearIssueRelationCreateMutation = `
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

	batch4LinearUsersQuery = `
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

	batch4LinearUserQuery = `
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

	batch4LinearViewerQuery = `
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

var batch4LinearTool = &Tool{
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

func batch4LinearImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"linear_cycles_list":           batch4LinearCyclesList,
		"linear_issue_add_comment":     batch4LinearIssueAddComment,
		"linear_issue_comments_list":   batch4LinearIssueCommentsList,
		"linear_issue_create":          batch4LinearIssueCreate,
		"linear_issue_delete":          batch4LinearIssueDelete,
		"linear_issue_get":             batch4LinearIssueGet,
		"linear_issue_relation_create": batch4LinearIssueRelationCreate,
		"linear_issue_search":          batch4LinearIssueSearch,
		"linear_issue_update":          batch4LinearIssueUpdate,
		"linear_label_create":          batch4LinearLabelCreate,
		"linear_labels_list":           batch4LinearLabelsList,
		"linear_project_create":        batch4LinearProjectCreate,
		"linear_project_get":           batch4LinearProjectGet,
		"linear_project_list":          batch4LinearProjectList,
		"linear_project_update":        batch4LinearProjectUpdate,
		"linear_team_get":              batch4LinearTeamGet,
		"linear_teams_list":            batch4LinearTeamsList,
		"linear_user_get":              batch4LinearUserGet,
		"linear_users_list":            batch4LinearUsersList,
		"linear_viewer":                batch4LinearViewer,
		"linear_workflow_states_get":   batch4LinearWorkflowStatesGet,
	}
}

func batch4LinearAuthError() map[string]any {
	return map[string]any{
		"error": "Linear credentials not configured",
		"help":  "Set LINEAR_API_KEY environment variable or configure via credential store. Get an API key at https://linear.app/settings/api",
	}
}

// batch4LinearExecute posts one GraphQL document. Transport failures are
// returned as Go errors so the action wrappers can apply the upstream
// timeout/network messages; API failures are returned as error dicts.
func batch4LinearExecute(ctx context.Context, rt *Runtime, query string, variables map[string]any) (map[string]any, error) {
	token := batch4ModuleToken(rt, "linear_tool")
	if token == "" {
		return batch4LinearAuthError(), nil
	}
	base := batch4ModuleURL(rt, "linear_tool")
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

	resp, err := batch4HTTP(rt).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch4LinearMaxBody))
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
			"error": fmt.Sprintf("Linear API error (HTTP %d): %s", resp.StatusCode, batch4LinearErrorDetail(raw)),
		}, nil
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return map[string]any{
			"error": fmt.Sprintf("Linear API error (HTTP %d): %s", resp.StatusCode, batch4LinearErrorDetail(raw)),
		}, nil
	}

	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return map[string]any{"error": "Linear API error: invalid JSON response"}, nil
	}
	if errors, present := envelope["errors"]; present {
		messages := make([]string, 0)
		for _, rawError := range batch4AsSlice(errors) {
			object := batch4AsMap(rawError)
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

// batch4LinearErrorDetail mirrors response.json().get("message", response.text).
func batch4LinearErrorDetail(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		if message, ok := payload["message"].(string); ok {
			return message
		}
	}
	return string(raw)
}

// batch4LinearCall runs one document and translates transport errors.
func batch4LinearCall(ctx context.Context, rt *Runtime, query string, variables map[string]any) (map[string]any, error) {
	result, err := batch4LinearExecute(ctx, rt, query, variables)
	if err != nil {
		if batch4IsTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, nil
		}
		return map[string]any{"error": "Network error: " + err.Error()}, nil
	}
	return result, nil
}

// batch4LinearUnwrap returns the named payload or the whole data map, matching
// Python's result.get(key, result).
func batch4LinearUnwrap(result map[string]any, key string) any {
	if value, present := result[key]; present {
		return value
	}
	return result
}

// batch4LinearFailure reports an API/GraphQL error carried in the result map.
// List actions must return it before reshaping, like upstream's
// `if "error" in result: return result`.
func batch4LinearFailure(result map[string]any) (map[string]any, bool) {
	if _, failed := result["error"]; failed {
		return result, true
	}
	return nil, false
}

// batch4LinearNodes reads a connection's nodes, defaulting to an empty list.
func batch4LinearNodes(result map[string]any, connection string) []any {
	payload := batch4AsMap(result[connection])
	nodes := batch4AsSlice(payload["nodes"])
	if nodes == nil {
		return []any{}
	}
	return nodes
}

// batch4LinearHasNextPage reads connection.pageInfo.hasNextPage.
func batch4LinearHasNextPage(result map[string]any, connection string) bool {
	payload := batch4AsMap(result[connection])
	return batch4AsBool(batch4AsMap(payload["pageInfo"])["hasNextPage"])
}

func batch4LinearIssueCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	if priority, present := batch4OptionalInt(input, "priority"); present {
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
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueCreateMutation, map[string]any{"input": inputData})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "issueCreate"), nil
}

func batch4LinearIssueGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueQuery, map[string]any{"id": stringArg(input, "issue_id")})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "issue"), nil
}

func batch4LinearIssueUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{}
	if value, present := batch4OptionalString(input, "title"); present {
		inputData["title"] = value
	}
	if value, present := batch4OptionalString(input, "description"); present {
		inputData["description"] = value
	}
	if value, present := batch4OptionalString(input, "state_id"); present {
		inputData["stateId"] = value
	}
	if value, present := batch4OptionalString(input, "assignee_id"); present {
		inputData["assigneeId"] = value
	}
	if value, present := batch4OptionalInt(input, "priority"); present {
		inputData["priority"] = value
	}
	if value, present := batch4OptionalStrings(input, "label_ids"); present {
		inputData["labelIds"] = value
	}
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueUpdateMutation, map[string]any{
		"id":    stringArg(input, "issue_id"),
		"input": inputData,
	})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "issueUpdate"), nil
}

func batch4LinearIssueDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueDeleteMutation, map[string]any{"id": stringArg(input, "issue_id")})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "issueDelete"), nil
}

func batch4LinearIssueSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssuesQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	issues := batch4LinearNodes(result, "issues")
	return map[string]any{
		"issues":      issues,
		"total":       len(issues),
		"hasNextPage": batch4LinearHasNextPage(result, "issues"),
	}, nil
}

func batch4LinearIssueAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearCommentCreateMutation, map[string]any{
		"input": map[string]any{
			"issueId": stringArg(input, "issue_id"),
			"body":    stringArg(input, "body"),
		},
	})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "commentCreate"), nil
}

func batch4LinearProjectCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	result, err := batch4LinearCall(ctx, rt, batch4LinearProjectCreateMutation, map[string]any{"input": inputData})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "projectCreate"), nil
}

func batch4LinearProjectGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearProjectQuery, map[string]any{"id": stringArg(input, "project_id")})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "project"), nil
}

func batch4LinearProjectUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	inputData := map[string]any{}
	if value, present := batch4OptionalString(input, "name"); present {
		inputData["name"] = value
	}
	if value, present := batch4OptionalString(input, "description"); present {
		inputData["description"] = value
	}
	if value, present := batch4OptionalString(input, "state"); present {
		inputData["state"] = value
	}
	if value, present := batch4OptionalString(input, "target_date"); present {
		inputData["targetDate"] = value
	}
	result, err := batch4LinearCall(ctx, rt, batch4LinearProjectUpdateMutation, map[string]any{
		"id":    stringArg(input, "project_id"),
		"input": inputData,
	})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "projectUpdate"), nil
}

func batch4LinearProjectList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	result, err := batch4LinearCall(ctx, rt, batch4LinearProjectsQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	projects := batch4LinearNodes(result, "projects")
	return map[string]any{
		"projects":    projects,
		"total":       len(projects),
		"hasNextPage": batch4LinearHasNextPage(result, "projects"),
	}, nil
}

func batch4LinearTeamsList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearTeamsQuery, nil)
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	teams := batch4LinearNodes(result, "teams")
	return map[string]any{"teams": teams, "total": len(teams)}, nil
}

func batch4LinearTeamGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearTeamQuery, map[string]any{"id": stringArg(input, "team_id")})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "team"), nil
}

func batch4LinearWorkflowStatesGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearWorkflowStatesQuery, map[string]any{"teamId": stringArg(input, "team_id")})
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	states := batch4LinearNodes(result, "workflowStates")
	return map[string]any{"states": states, "total": len(states)}, nil
}

func batch4LinearLabelCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueLabelCreateMutation, map[string]any{"input": inputData})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "issueLabelCreate"), nil
}

func batch4LinearLabelsList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	var variables map[string]any
	if team := stringArg(input, "team_id"); team != "" {
		variables = map[string]any{"filter": map[string]any{"team": map[string]any{"id": map[string]any{"eq": team}}}}
	}
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueLabelsQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	labels := batch4LinearNodes(result, "issueLabels")
	return map[string]any{"labels": labels, "total": len(labels)}, nil
}

func batch4LinearUsersList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearUsersQuery, nil)
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	users := batch4LinearNodes(result, "users")
	return map[string]any{"users": users, "total": len(users)}, nil
}

func batch4LinearUserGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearUserQuery, map[string]any{"id": stringArg(input, "user_id")})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "user"), nil
}

func batch4LinearViewer(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearViewerQuery, nil)
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "viewer"), nil
}

func batch4LinearCyclesList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := intArg(input, "limit", 50)
	first := limit
	if first > 100 {
		first = 100
	}
	variables := map[string]any{
		"first":  first,
		"filter": map[string]any{"team": map[string]any{"id": map[string]any{"eq": stringArg(input, "team_id")}}},
	}
	result, err := batch4LinearCall(ctx, rt, batch4LinearCyclesQuery, variables)
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	cycles := batch4LinearNodes(result, "cycles")
	return map[string]any{
		"cycles":      cycles,
		"total":       len(cycles),
		"hasNextPage": batch4LinearHasNextPage(result, "cycles"),
	}, nil
}

func batch4LinearIssueCommentsList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueCommentsQuery, map[string]any{"id": stringArg(input, "issue_id")})
	if err != nil {
		return nil, err
	}
	if failure, failed := batch4LinearFailure(result); failed {
		return failure, nil
	}
	issue := batch4AsMap(result["issue"])
	comments := batch4AsSlice(batch4AsMap(issue["comments"])["nodes"])
	if comments == nil {
		comments = []any{}
	}
	return map[string]any{"comments": comments, "total": len(comments)}, nil
}

func batch4LinearIssueRelationCreate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	relationType := stringArg(input, "relation_type")
	if relationType == "" {
		relationType = "related"
	}
	result, err := batch4LinearCall(ctx, rt, batch4LinearIssueRelationCreateMutation, map[string]any{
		"input": map[string]any{
			"issueId":        stringArg(input, "issue_id"),
			"relatedIssueId": stringArg(input, "related_issue_id"),
			"type":           relationType,
		},
	})
	if err != nil {
		return nil, err
	}
	return batch4LinearUnwrap(result, "issueRelationCreate"), nil
}
