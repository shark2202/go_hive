// Zendesk module of batch 6 lane 2: 8 ticket/user actions over the Zendesk
// Support API v2 (Basic auth with email + API token).
//
// The host is account-specific (https://<subdomain>.zendesk.com/api/v2), so it
// is derived per call from ZENDESK_SUBDOMAIN unless WithBaseURL overrides it.

package crm

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var zendeskTool = &Tool{
	ID:          "zendesk_tool",
	Name:        "zendesk",
	DisplayName: "Zendesk",
	Summary:     "Zendesk ticket management and search (8 actions).",
	Actions: []Action{
		{
			Name:    "zendesk_list_tickets",
			Summary: "List tickets in Zendesk.",
			Source:  "zendesk_tool/zendesk_tool.py",
			Params: map[string]any{
				"page_size": paramDefault("integer", 25),
			},
		},
		{
			Name:     "zendesk_get_ticket",
			Summary:  "Get details about a specific Zendesk ticket.",
			Source:   "zendesk_tool/zendesk_tool.py",
			Required: []string{"ticket_id"},
			Params: map[string]any{
				"ticket_id": param("integer"),
			},
		},
		{
			Name:     "zendesk_create_ticket",
			Summary:  "Create a new Zendesk ticket.",
			Source:   "zendesk_tool/zendesk_tool.py",
			Required: []string{"subject", "body"},
			Params: map[string]any{
				"subject":     param("string"),
				"body":        param("string"),
				"priority":    paramDefault("string", "normal"),
				"ticket_type": paramDefault("string", ""),
				"tags":        paramDefault("string", ""),
			},
		},
		{
			Name:     "zendesk_update_ticket",
			Summary:  "Update a Zendesk ticket and optionally add a comment.",
			Source:   "zendesk_tool/zendesk_tool.py",
			Required: []string{"ticket_id"},
			Params: map[string]any{
				"ticket_id":      param("integer"),
				"status":         paramDefault("string", ""),
				"priority":       paramDefault("string", ""),
				"comment":        paramDefault("string", ""),
				"comment_public": paramDefault("boolean", true),
				"tags":           paramDefault("string", ""),
			},
		},
		{
			Name:     "zendesk_search_tickets",
			Summary:  "Search Zendesk tickets using Zendesk search syntax.",
			Source:   "zendesk_tool/zendesk_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":      param("string"),
				"sort_by":    paramDefault("string", "updated_at"),
				"sort_order": paramDefault("string", "desc"),
			},
		},
		{
			Name:     "zendesk_get_ticket_comments",
			Summary:  "List comments on a Zendesk ticket (conversation history).",
			Source:   "zendesk_tool/zendesk_tool.py",
			Required: []string{"ticket_id"},
			Params: map[string]any{
				"ticket_id": param("integer"),
				"page_size": paramDefault("integer", 25),
			},
		},
		{
			Name:     "zendesk_add_ticket_comment",
			Summary:  "Add a comment to an existing Zendesk ticket.",
			Source:   "zendesk_tool/zendesk_tool.py",
			Required: []string{"ticket_id", "body"},
			Params: map[string]any{
				"ticket_id": param("integer"),
				"body":      param("string"),
				"public":    paramDefault("boolean", true),
			},
		},
		{
			Name:    "zendesk_list_users",
			Summary: "List users in Zendesk, optionally filtered by role.",
			Source:  "zendesk_tool/zendesk_tool.py",
			Params: map[string]any{
				"role":      paramDefault("string", ""),
				"page_size": paramDefault("integer", 25),
			},
		},
	},
}

func zendeskImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"zendesk_list_tickets":        zendeskListTickets,
		"zendesk_get_ticket":          zendeskGetTicket,
		"zendesk_create_ticket":       zendeskCreateTicket,
		"zendesk_update_ticket":       zendeskUpdateTicket,
		"zendesk_search_tickets":      zendeskSearchTickets,
		"zendesk_get_ticket_comments": zendeskGetTicketComments,
		"zendesk_add_ticket_comment":  zendeskAddTicketComment,
		"zendesk_list_users":          zendeskListUsers,
	}
}

// zendeskCredentials reads the three-part credential bundle.
func zendeskCredentials(rt *Runtime) (subdomain, email, token string) {
	getenv := runtimeGetenv(rt)
	return strings.TrimSpace(getenv("ZENDESK_SUBDOMAIN")),
		strings.TrimSpace(getenv("ZENDESK_EMAIL")),
		strings.TrimSpace(getenv("ZENDESK_API_TOKEN"))
}

// zendeskAuthError mirrors the upstream missing-credential payload.
func zendeskAuthError() map[string]any {
	return map[string]any{
		"error": "ZENDESK_SUBDOMAIN, ZENDESK_EMAIL, and ZENDESK_API_TOKEN not set",
		"help":  "Create an API token in Zendesk Admin > Apps and integrations > APIs > Zendesk API",
	}
}

// zendeskBaseURL resolves the account API root.
func zendeskBaseURL(rt *Runtime, subdomain string) string {
	if base := moduleBaseURL(rt, "zendesk_tool"); base != "" {
		return base
	}
	return "https://" + subdomain + ".zendesk.com/api/v2"
}

// zendeskCall performs one Zendesk API call and maps HTTP failures
// to the upstream error dicts. The boolean reports success.
func zendeskCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	subdomain, email, token := zendeskCredentials(rt)
	if subdomain == "" || email == "" || token == "" {
		return zendeskAuthError(), false
	}
	target := zendeskBaseURL(rt, subdomain) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": basicAuth(email+"/token", token),
		"Content-Type":  "application/json",
	}
	reply, err := supportDo(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Zendesk timed out"}, false
		}
		return map[string]any{"error": "Zendesk request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Zendesk credentials."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Check your Zendesk permissions."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Not found."}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limited. Try again shortly."}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Zendesk API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), supportMaxError),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Zendesk request failed: invalid JSON response"}, false
	}
	return value, true
}

// zendeskTicket flattens a ticket the way _extract_ticket does.
func zendeskTicket(source map[string]any) map[string]any {
	return map[string]any{
		"id":           jsonNumber(source["id"]),
		"subject":      asString(source["subject"]),
		"description":  truncateText(asString(source["description"]), supportMaxError),
		"status":       asString(source["status"]),
		"priority":     asString(source["priority"]),
		"type":         asString(source["type"]),
		"tags":         supportStringList(source["tags"]),
		"requester_id": jsonNumber(source["requester_id"]),
		"assignee_id":  jsonNumber(source["assignee_id"]),
		"created_at":   asString(source["created_at"]),
		"updated_at":   asString(source["updated_at"]),
	}
}

func zendeskListTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page[size]": {fmt.Sprint(clampInt(intArg(input, "page_size", 25), 1, 100))},
	}
	value, ok := zendeskCall(ctx, rt, http.MethodGet, "/tickets", query, nil)
	if !ok {
		return value, nil
	}
	tickets := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["tickets"]) {
		tickets = append(tickets, zendeskTicket(asMap(raw)))
	}
	return map[string]any{"tickets": tickets, "count": len(tickets)}, nil
}

func zendeskGetTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	value, ok := zendeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d", ticketID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	return zendeskTicket(asMap(asMap(value)["ticket"])), nil
}

func zendeskCreateTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	subject := stringArg(input, "subject")
	body := stringArg(input, "body")
	if subject == "" || body == "" {
		return map[string]any{"error": "subject and body are required"}, nil
	}
	priority := stringArg(input, "priority")
	if priority == "" {
		priority = "normal"
	}
	ticket := map[string]any{
		"subject":  subject,
		"comment":  map[string]any{"body": body},
		"priority": priority,
	}
	if ticketType := stringArg(input, "ticket_type"); ticketType != "" {
		ticket["type"] = ticketType
	}
	if tags := stringArg(input, "tags"); tags != "" {
		ticket["tags"] = splitCSV(tags)
	}
	value, ok := zendeskCall(
		ctx, rt, http.MethodPost, "/tickets", nil, map[string]any{"ticket": ticket},
	)
	if !ok {
		return value, nil
	}
	created := asMap(asMap(value)["ticket"])
	id := jsonNumber(created["id"])
	subdomain, _, _ := zendeskCredentials(rt)
	return map[string]any{
		"id":      id,
		"subject": asString(created["subject"]),
		"status":  asString(created["status"]),
		"url":     "https://" + subdomain + ".zendesk.com/agent/tickets/" + stringID(id),
		"result":  "created",
	}, nil
}

func zendeskUpdateTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	ticket := map[string]any{}
	if status := stringArg(input, "status"); status != "" {
		ticket["status"] = status
	}
	if priority := stringArg(input, "priority"); priority != "" {
		ticket["priority"] = priority
	}
	if comment := stringArg(input, "comment"); comment != "" {
		ticket["comment"] = map[string]any{
			"body":   comment,
			"public": boolArg(input, "comment_public", true),
		}
	}
	if tags := stringArg(input, "tags"); tags != "" {
		ticket["tags"] = splitCSV(tags)
	}
	if len(ticket) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	value, ok := zendeskCall(
		ctx, rt, http.MethodPut, fmt.Sprintf("/tickets/%d", ticketID), nil,
		map[string]any{"ticket": ticket},
	)
	if !ok {
		return value, nil
	}
	return zendeskTicket(asMap(asMap(value)["ticket"])), nil
}

func zendeskSearchTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	search := stringArg(input, "query")
	if search == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	fullQuery := search
	if !strings.Contains(search, "type:") {
		fullQuery = "type:ticket " + search
	}
	sortBy := stringArg(input, "sort_by")
	if sortBy == "" {
		sortBy = "updated_at"
	}
	sortOrder := stringArg(input, "sort_order")
	if sortOrder == "" {
		sortOrder = "desc"
	}
	query := url.Values{"query": {fullQuery}, "sort_by": {sortBy}, "sort_order": {sortOrder}}
	value, ok := zendeskCall(ctx, rt, http.MethodGet, "/search", query, nil)
	if !ok {
		return value, nil
	}
	object := asMap(value)
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(object["results"]) {
		item := asMap(raw)
		results = append(results, map[string]any{
			"id":       jsonNumber(item["id"]),
			"subject":  asString(item["subject"]),
			"status":   asString(item["status"]),
			"priority": asString(item["priority"]),
		})
	}
	count := len(results)
	if raw, present := object["count"]; present && raw != nil {
		count = intArg(object, "count", len(results))
	}
	return map[string]any{"results": results, "count": count}, nil
}

func zendeskGetTicketComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	query := url.Values{
		"page[size]": {fmt.Sprint(clampInt(intArg(input, "page_size", 25), 1, 100))},
	}
	value, ok := zendeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d/comments", ticketID), query, nil,
	)
	if !ok {
		return value, nil
	}
	comments := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["comments"]) {
		item := asMap(raw)
		comments = append(comments, map[string]any{
			"id":         jsonNumber(item["id"]),
			"body":       truncateText(asString(item["body"]), supportMaxError),
			"author_id":  jsonNumber(item["author_id"]),
			"public":     boolOr(item["public"], true),
			"created_at": asString(item["created_at"]),
		})
	}
	return map[string]any{"ticket_id": ticketID, "comments": comments, "count": len(comments)}, nil
}

func zendeskAddTicketComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	body := stringArg(input, "body")
	if ticketID == 0 || body == "" {
		return map[string]any{"error": "ticket_id and body are required"}, nil
	}
	ticket := map[string]any{
		"comment": map[string]any{"body": body, "public": boolArg(input, "public", true)},
	}
	value, ok := zendeskCall(
		ctx, rt, http.MethodPut, fmt.Sprintf("/tickets/%d", ticketID), nil,
		map[string]any{"ticket": ticket},
	)
	if !ok {
		return value, nil
	}
	updated := asMap(asMap(value)["ticket"])
	return map[string]any{
		"id":      jsonNumber(updated["id"]),
		"subject": asString(updated["subject"]),
		"status":  asString(updated["status"]),
		"result":  "comment_added",
	}, nil
}

func zendeskListUsers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page[size]": {fmt.Sprint(clampInt(intArg(input, "page_size", 25), 1, 100))},
	}
	if role := stringArg(input, "role"); role != "" {
		query.Set("role", role)
	}
	value, ok := zendeskCall(ctx, rt, http.MethodGet, "/users", query, nil)
	if !ok {
		return value, nil
	}
	users := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["users"]) {
		item := asMap(raw)
		users = append(users, map[string]any{
			"id":         jsonNumber(item["id"]),
			"name":       asString(item["name"]),
			"email":      asString(item["email"]),
			"role":       asString(item["role"]),
			"active":     boolOr(item["active"], false),
			"created_at": asString(item["created_at"]),
		})
	}
	return map[string]any{"users": users, "count": len(users)}, nil
}
