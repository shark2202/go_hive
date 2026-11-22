// Linear project actions: create/get/update/list.
package collab

import "context"

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
