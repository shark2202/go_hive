// Linear issue actions: create/get/update/delete/search, comments and
// issue relations.
package integrations

import "context"

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
