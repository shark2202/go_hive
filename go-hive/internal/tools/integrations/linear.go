// Linear integration (upstream linear_tool, 21 actions). Part of batch 4; the
// batch-4 registry lives in notion.go, shared helpers in notion_common.go.
//
// The upstream client posts GraphQL documents to a single endpoint. This port
// preserves the same queries/mutations, variables and response projections:
// list actions unbox {nodes, pageInfo}; get/create/update actions return the
// payload object (or the whole data map when the field is absent).
//
// File layout after the phase-2 split: linear_queries.go carries the GraphQL
// documents, linear_common.go the transport helpers, and linear_issues.go /
// linear_projects.go / linear_resources.go the action wrappers.
package integrations

const linearMaxBody = 8 << 20

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
