// Zendesk module of batch 6 lane 2: 8 ticket/user actions over the Zendesk
// Support API v2 (Basic auth with email + API token).
//
// The host is account-specific (https://<subdomain>.zendesk.com/api/v2), so it
// is derived per call from ZENDESK_SUBDOMAIN unless WithBaseURL overrides it.

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var batch6Lane2ZendeskTool = &Tool{
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

func batch6Lane2ZendeskImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"zendesk_list_tickets":        batch6Lane2ZendeskListTickets,
		"zendesk_get_ticket":          batch6Lane2ZendeskGetTicket,
		"zendesk_create_ticket":       batch6Lane2ZendeskCreateTicket,
		"zendesk_update_ticket":       batch6Lane2ZendeskUpdateTicket,
		"zendesk_search_tickets":      batch6Lane2ZendeskSearchTickets,
		"zendesk_get_ticket_comments": batch6Lane2ZendeskGetTicketComments,
		"zendesk_add_ticket_comment":  batch6Lane2ZendeskAddTicketComment,
		"zendesk_list_users":          batch6Lane2ZendeskListUsers,
	}
}

// batch6Lane2ZendeskCredentials reads the three-part credential bundle.
func batch6Lane2ZendeskCredentials(rt *Runtime) (subdomain, email, token string) {
	getenv := batch6Lane2Getenv(rt)
	return strings.TrimSpace(getenv("ZENDESK_SUBDOMAIN")),
		strings.TrimSpace(getenv("ZENDESK_EMAIL")),
		strings.TrimSpace(getenv("ZENDESK_API_TOKEN"))
}

// batch6Lane2ZendeskAuthError mirrors the upstream missing-credential payload.
func batch6Lane2ZendeskAuthError() map[string]any {
	return map[string]any{
		"error": "ZENDESK_SUBDOMAIN, ZENDESK_EMAIL, and ZENDESK_API_TOKEN not set",
		"help":  "Create an API token in Zendesk Admin > Apps and integrations > APIs > Zendesk API",
	}
}

// batch6Lane2ZendeskBaseURL resolves the account API root.
func batch6Lane2ZendeskBaseURL(rt *Runtime, subdomain string) string {
	if base := batch6Lane2ModuleURL(rt, "zendesk_tool"); base != "" {
		return base
	}
	return "https://" + subdomain + ".zendesk.com/api/v2"
}

// batch6Lane2ZendeskCall performs one Zendesk API call and maps HTTP failures
// to the upstream error dicts. The boolean reports success.
func batch6Lane2ZendeskCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	subdomain, email, token := batch6Lane2ZendeskCredentials(rt)
	if subdomain == "" || email == "" || token == "" {
		return batch6Lane2ZendeskAuthError(), false
	}
	target := batch6Lane2ZendeskBaseURL(rt, subdomain) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": batch6Lane2BasicAuth(email+"/token", token),
		"Content-Type":  "application/json",
	}
	reply, err := batch6Lane2Do(ctx, rt, method, target, headers, payload)
	if err != nil {
		if batch6Lane2IsTimeout(ctx, err) {
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
				batch6Lane2TruncateText(string(reply.Body), batch6Lane2MaxError),
			),
		}, false
	}
	value, ok := batch6Lane2DecodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Zendesk request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch6Lane2ZendeskTicket flattens a ticket the way _extract_ticket does.
func batch6Lane2ZendeskTicket(source map[string]any) map[string]any {
	return map[string]any{
		"id":           batch6Lane2JSONNumber(source["id"]),
		"subject":      batch6Lane2AsString(source["subject"]),
		"description":  batch6Lane2TruncateText(batch6Lane2AsString(source["description"]), batch6Lane2MaxError),
		"status":       batch6Lane2AsString(source["status"]),
		"priority":     batch6Lane2AsString(source["priority"]),
		"type":         batch6Lane2AsString(source["type"]),
		"tags":         batch6Lane2StringList(source["tags"]),
		"requester_id": batch6Lane2JSONNumber(source["requester_id"]),
		"assignee_id":  batch6Lane2JSONNumber(source["assignee_id"]),
		"created_at":   batch6Lane2AsString(source["created_at"]),
		"updated_at":   batch6Lane2AsString(source["updated_at"]),
	}
}

func batch6Lane2ZendeskListTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page[size]": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page_size", 25), 1, 100))},
	}
	value, ok := batch6Lane2ZendeskCall(ctx, rt, http.MethodGet, "/tickets", query, nil)
	if !ok {
		return value, nil
	}
	tickets := make([]map[string]any, 0)
	for _, raw := range batch6Lane2AsSlice(batch6Lane2AsMap(value)["tickets"]) {
		tickets = append(tickets, batch6Lane2ZendeskTicket(batch6Lane2AsMap(raw)))
	}
	return map[string]any{"tickets": tickets, "count": len(tickets)}, nil
}

func batch6Lane2ZendeskGetTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	value, ok := batch6Lane2ZendeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d", ticketID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2ZendeskTicket(batch6Lane2AsMap(batch6Lane2AsMap(value)["ticket"])), nil
}

func batch6Lane2ZendeskCreateTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
		ticket["tags"] = batch6Lane2SplitTags(tags)
	}
	value, ok := batch6Lane2ZendeskCall(
		ctx, rt, http.MethodPost, "/tickets", nil, map[string]any{"ticket": ticket},
	)
	if !ok {
		return value, nil
	}
	created := batch6Lane2AsMap(batch6Lane2AsMap(value)["ticket"])
	id := batch6Lane2JSONNumber(created["id"])
	subdomain, _, _ := batch6Lane2ZendeskCredentials(rt)
	return map[string]any{
		"id":      id,
		"subject": batch6Lane2AsString(created["subject"]),
		"status":  batch6Lane2AsString(created["status"]),
		"url":     "https://" + subdomain + ".zendesk.com/agent/tickets/" + batch6Lane2StringID(id),
		"result":  "created",
	}, nil
}

func batch6Lane2ZendeskUpdateTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
		ticket["tags"] = batch6Lane2SplitTags(tags)
	}
	if len(ticket) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	value, ok := batch6Lane2ZendeskCall(
		ctx, rt, http.MethodPut, fmt.Sprintf("/tickets/%d", ticketID), nil,
		map[string]any{"ticket": ticket},
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2ZendeskTicket(batch6Lane2AsMap(batch6Lane2AsMap(value)["ticket"])), nil
}

func batch6Lane2ZendeskSearchTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	value, ok := batch6Lane2ZendeskCall(ctx, rt, http.MethodGet, "/search", query, nil)
	if !ok {
		return value, nil
	}
	object := batch6Lane2AsMap(value)
	results := make([]map[string]any, 0)
	for _, raw := range batch6Lane2AsSlice(object["results"]) {
		item := batch6Lane2AsMap(raw)
		results = append(results, map[string]any{
			"id":       batch6Lane2JSONNumber(item["id"]),
			"subject":  batch6Lane2AsString(item["subject"]),
			"status":   batch6Lane2AsString(item["status"]),
			"priority": batch6Lane2AsString(item["priority"]),
		})
	}
	count := len(results)
	if raw, present := object["count"]; present && raw != nil {
		count = intArg(object, "count", len(results))
	}
	return map[string]any{"results": results, "count": count}, nil
}

func batch6Lane2ZendeskGetTicketComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	query := url.Values{
		"page[size]": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page_size", 25), 1, 100))},
	}
	value, ok := batch6Lane2ZendeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d/comments", ticketID), query, nil,
	)
	if !ok {
		return value, nil
	}
	comments := make([]map[string]any, 0)
	for _, raw := range batch6Lane2AsSlice(batch6Lane2AsMap(value)["comments"]) {
		item := batch6Lane2AsMap(raw)
		comments = append(comments, map[string]any{
			"id":         batch6Lane2JSONNumber(item["id"]),
			"body":       batch6Lane2TruncateText(batch6Lane2AsString(item["body"]), batch6Lane2MaxError),
			"author_id":  batch6Lane2JSONNumber(item["author_id"]),
			"public":     batch6Lane2BoolOr(item["public"], true),
			"created_at": batch6Lane2AsString(item["created_at"]),
		})
	}
	return map[string]any{"ticket_id": ticketID, "comments": comments, "count": len(comments)}, nil
}

func batch6Lane2ZendeskAddTicketComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	body := stringArg(input, "body")
	if ticketID == 0 || body == "" {
		return map[string]any{"error": "ticket_id and body are required"}, nil
	}
	ticket := map[string]any{
		"comment": map[string]any{"body": body, "public": boolArg(input, "public", true)},
	}
	value, ok := batch6Lane2ZendeskCall(
		ctx, rt, http.MethodPut, fmt.Sprintf("/tickets/%d", ticketID), nil,
		map[string]any{"ticket": ticket},
	)
	if !ok {
		return value, nil
	}
	updated := batch6Lane2AsMap(batch6Lane2AsMap(value)["ticket"])
	return map[string]any{
		"id":      batch6Lane2JSONNumber(updated["id"]),
		"subject": batch6Lane2AsString(updated["subject"]),
		"status":  batch6Lane2AsString(updated["status"]),
		"result":  "comment_added",
	}, nil
}

func batch6Lane2ZendeskListUsers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page[size]": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page_size", 25), 1, 100))},
	}
	if role := stringArg(input, "role"); role != "" {
		query.Set("role", role)
	}
	value, ok := batch6Lane2ZendeskCall(ctx, rt, http.MethodGet, "/users", query, nil)
	if !ok {
		return value, nil
	}
	users := make([]map[string]any, 0)
	for _, raw := range batch6Lane2AsSlice(batch6Lane2AsMap(value)["users"]) {
		item := batch6Lane2AsMap(raw)
		users = append(users, map[string]any{
			"id":         batch6Lane2JSONNumber(item["id"]),
			"name":       batch6Lane2AsString(item["name"]),
			"email":      batch6Lane2AsString(item["email"]),
			"role":       batch6Lane2AsString(item["role"]),
			"active":     batch6Lane2BoolOr(item["active"], false),
			"created_at": batch6Lane2AsString(item["created_at"]),
		})
	}
	return map[string]any{"users": users, "count": len(users)}, nil
}
