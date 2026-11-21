// Freshdesk directory actions: agents, groups and companies (split from
// freshdesk.go).

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func batch6Lane2FreshdeskListAgents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 30), 1, 100))},
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/agents", query, nil)
	if !ok {
		return value, nil
	}
	agents := batch6Lane2FreshdeskItems(value, batch6Lane2FreshdeskAgent)
	return map[string]any{"agents": agents, "count": len(agents)}, nil
}

func batch6Lane2FreshdeskGetAgent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	agentID := intArg(input, "agent_id", 0)
	if agentID == 0 {
		return map[string]any{"error": "agent_id is required"}, nil
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/agents/%d", agentID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskAgent(asMap(value)), nil
}

func batch6Lane2FreshdeskListGroups(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 30), 1, 100))},
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/groups", query, nil)
	if !ok {
		return value, nil
	}
	groups := batch6Lane2FreshdeskItems(value, batch6Lane2FreshdeskGroup)
	return map[string]any{"groups": groups, "count": len(groups)}, nil
}

func batch6Lane2FreshdeskGetGroup(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	groupID := intArg(input, "group_id", 0)
	if groupID == 0 {
		return map[string]any{"error": "group_id is required"}, nil
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/groups/%d", groupID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskGroup(asMap(value)), nil
}

func batch6Lane2FreshdeskListCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 30), 1, 100))},
	}
	if updatedSince := stringArg(input, "updated_since"); updatedSince != "" {
		query.Set("updated_since", updatedSince)
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/companies", query, nil)
	if !ok {
		return value, nil
	}
	companies := batch6Lane2FreshdeskItems(value, batch6Lane2FreshdeskCompany)
	return map[string]any{"companies": companies, "count": len(companies)}, nil
}

func batch6Lane2FreshdeskGetCompany(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	companyID := intArg(input, "company_id", 0)
	if companyID == 0 {
		return map[string]any{"error": "company_id is required"}, nil
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/companies/%d", companyID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskCompany(asMap(value)), nil
}
