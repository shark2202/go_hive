// Linear workspace resources: teams, workflow states, labels, users and
// cycles.
package integrations

import "context"

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
