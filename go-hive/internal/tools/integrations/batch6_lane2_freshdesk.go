// Freshdesk module of batch 6 lane 2: 17 ticket/contact/agent/group/company
// actions over Freshdesk API v2 (Basic auth: API key as username, "X" as the
// password, account host from FRESHDESK_DOMAIN).

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var batch6Lane2FreshdeskTool = &Tool{
	ID:          "freshdesk_tool",
	Name:        "freshdesk",
	DisplayName: "Freshdesk",
	Summary:     "Freshdesk tickets, contacts, agents, groups, and companies (17 actions).",
	Actions: []Action{
		{
			Name:    "freshdesk_list_tickets",
			Summary: "List a page of Freshdesk tickets for digests, triage, or reporting.",
			Source:  "freshdesk_tool/freshdesk_tool.py",
			Params: map[string]any{
				"page":          paramDefault("integer", 1),
				"per_page":      paramDefault("integer", 30),
				"filter":        paramDefault("string", ""),
				"updated_since": paramDefault("string", ""),
			},
		},
		{
			Name:     "freshdesk_filter_tickets",
			Summary:  "Search/filter Freshdesk tickets by Freshdesk query syntax.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query": param("string"),
				"page":  paramDefault("integer", 1),
			},
		},
		{
			Name:     "freshdesk_list_ticket_conversations",
			Summary:  "List conversations (replies and notes) for a Freshdesk ticket.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"ticket_id"},
			Params: map[string]any{
				"ticket_id": param("integer"),
				"page":      paramDefault("integer", 1),
				"per_page":  paramDefault("integer", 30),
			},
		},
		{
			Name:     "freshdesk_get_ticket",
			Summary:  "Get details about a specific Freshdesk ticket.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"ticket_id"},
			Params: map[string]any{
				"ticket_id": param("integer"),
			},
		},
		{
			Name:     "freshdesk_create_ticket",
			Summary:  "Create a new Freshdesk ticket.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"email", "subject", "description"},
			Params: map[string]any{
				"email":       param("string"),
				"subject":     param("string"),
				"description": param("string"),
				"priority":    param("integer"),
				"status":      param("integer"),
				"tags":        paramDefault("string", ""),
			},
		},
		{
			Name:     "freshdesk_update_ticket",
			Summary:  "Update a Freshdesk ticket and optionally add a note.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"ticket_id"},
			Params: map[string]any{
				"ticket_id":    param("integer"),
				"status":       param("integer"),
				"priority":     param("integer"),
				"tags":         paramDefault("string", ""),
				"note":         paramDefault("string", ""),
				"note_private": paramDefault("boolean", true),
			},
		},
		{
			Name:     "freshdesk_add_ticket_reply",
			Summary:  "Add a public reply or a private note to a Freshdesk ticket.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"ticket_id", "body"},
			Params: map[string]any{
				"ticket_id":  param("integer"),
				"body":       param("string"),
				"public":     paramDefault("boolean", true),
				"from_email": paramDefault("string", ""),
			},
		},
		{
			Name:    "freshdesk_list_contacts",
			Summary: "List Freshdesk contacts, optionally filtered by email.",
			Source:  "freshdesk_tool/freshdesk_tool.py",
			Params: map[string]any{
				"page":     paramDefault("integer", 1),
				"per_page": paramDefault("integer", 30),
				"email":    paramDefault("string", ""),
			},
		},
		{
			Name:    "freshdesk_get_contact",
			Summary: "Get a Freshdesk contact by ID or email.",
			Source:  "freshdesk_tool/freshdesk_tool.py",
			Params: map[string]any{
				"contact_id": param("integer"),
				"email":      paramDefault("string", ""),
			},
		},
		{
			Name:     "freshdesk_create_contact",
			Summary:  "Create a new Freshdesk contact.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email":      param("string"),
				"name":       paramDefault("string", ""),
				"phone":      paramDefault("string", ""),
				"company_id": param("integer"),
			},
		},
		{
			Name:     "freshdesk_update_contact",
			Summary:  "Update a Freshdesk contact.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"contact_id"},
			Params: map[string]any{
				"contact_id": param("integer"),
				"name":       paramDefault("string", ""),
				"email":      paramDefault("string", ""),
				"phone":      paramDefault("string", ""),
				"company_id": param("integer"),
			},
		},
		{
			Name:    "freshdesk_list_agents",
			Summary: "List agents in Freshdesk.",
			Source:  "freshdesk_tool/freshdesk_tool.py",
			Params: map[string]any{
				"page":     paramDefault("integer", 1),
				"per_page": paramDefault("integer", 30),
			},
		},
		{
			Name:     "freshdesk_get_agent",
			Summary:  "Get a single Freshdesk agent by ID.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"agent_id"},
			Params: map[string]any{
				"agent_id": param("integer"),
			},
		},
		{
			Name:    "freshdesk_list_groups",
			Summary: "List groups in Freshdesk.",
			Source:  "freshdesk_tool/freshdesk_tool.py",
			Params: map[string]any{
				"page":     paramDefault("integer", 1),
				"per_page": paramDefault("integer", 30),
			},
		},
		{
			Name:     "freshdesk_get_group",
			Summary:  "Get a single Freshdesk group by ID.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"group_id"},
			Params: map[string]any{
				"group_id": param("integer"),
			},
		},
		{
			Name:    "freshdesk_list_companies",
			Summary: "List companies in Freshdesk.",
			Source:  "freshdesk_tool/freshdesk_tool.py",
			Params: map[string]any{
				"page":          paramDefault("integer", 1),
				"per_page":      paramDefault("integer", 30),
				"updated_since": paramDefault("string", ""),
			},
		},
		{
			Name:     "freshdesk_get_company",
			Summary:  "Get a single Freshdesk company by ID.",
			Source:   "freshdesk_tool/freshdesk_tool.py",
			Required: []string{"company_id"},
			Params: map[string]any{
				"company_id": param("integer"),
			},
		},
	},
}

func batch6Lane2FreshdeskImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"freshdesk_list_tickets":              batch6Lane2FreshdeskListTickets,
		"freshdesk_filter_tickets":            batch6Lane2FreshdeskFilterTickets,
		"freshdesk_list_ticket_conversations": batch6Lane2FreshdeskListTicketConversations,
		"freshdesk_get_ticket":                batch6Lane2FreshdeskGetTicket,
		"freshdesk_create_ticket":             batch6Lane2FreshdeskCreateTicket,
		"freshdesk_update_ticket":             batch6Lane2FreshdeskUpdateTicket,
		"freshdesk_add_ticket_reply":          batch6Lane2FreshdeskAddTicketReply,
		"freshdesk_list_contacts":             batch6Lane2FreshdeskListContacts,
		"freshdesk_get_contact":               batch6Lane2FreshdeskGetContact,
		"freshdesk_create_contact":            batch6Lane2FreshdeskCreateContact,
		"freshdesk_update_contact":            batch6Lane2FreshdeskUpdateContact,
		"freshdesk_list_agents":               batch6Lane2FreshdeskListAgents,
		"freshdesk_get_agent":                 batch6Lane2FreshdeskGetAgent,
		"freshdesk_list_groups":               batch6Lane2FreshdeskListGroups,
		"freshdesk_get_group":                 batch6Lane2FreshdeskGetGroup,
		"freshdesk_list_companies":            batch6Lane2FreshdeskListCompanies,
		"freshdesk_get_company":               batch6Lane2FreshdeskGetCompany,
	}
}

// batch6Lane2FreshdeskKey resolves the API key from WithCredential or env.
func batch6Lane2FreshdeskKey(rt *Runtime) string {
	return batch6Lane2ModuleToken(rt, "freshdesk_tool")
}

// batch6Lane2FreshdeskDomain resolves the account host (e.g. acme.freshdesk.com).
func batch6Lane2FreshdeskDomain(rt *Runtime) string {
	return strings.TrimSpace(batch6Lane2Getenv(rt)("FRESHDESK_DOMAIN"))
}

// batch6Lane2FreshdeskAuthError mirrors the upstream missing-key payload.
func batch6Lane2FreshdeskAuthError() map[string]any {
	return map[string]any{
		"error": "FRESHDESK_API_KEY environment variable not set",
		"help":  "Get your API key from Freshdesk profile settings and set FRESHDESK_API_KEY, or configure it via the credential store.",
	}
}

// batch6Lane2FreshdeskDomainError mirrors the upstream missing-domain payload.
func batch6Lane2FreshdeskDomainError() map[string]any {
	return map[string]any{
		"error": "FRESHDESK_DOMAIN is required",
		"help":  "Set FRESHDESK_DOMAIN (e.g. acme.freshdesk.com) or configure freshdesk_domain.",
	}
}

// batch6Lane2FreshdeskBaseURL resolves the account API root.
func batch6Lane2FreshdeskBaseURL(rt *Runtime, domain string) string {
	if base := batch6Lane2ModuleURL(rt, "freshdesk_tool"); base != "" {
		return base
	}
	host := strings.TrimSpace(domain)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	return "https://" + host + "/api/v2"
}

// batch6Lane2FreshdeskCall performs one Freshdesk API call. The returned
// boolean reports success; failures come back as the upstream error dict.
func batch6Lane2FreshdeskCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	apiKey := batch6Lane2FreshdeskKey(rt)
	if apiKey == "" {
		return batch6Lane2FreshdeskAuthError(), false
	}
	domain := batch6Lane2FreshdeskDomain(rt)
	if domain == "" {
		return batch6Lane2FreshdeskDomainError(), false
	}
	target := batch6Lane2FreshdeskBaseURL(rt, domain) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": batch6Lane2BasicAuth(apiKey, "X"),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	reply, err := batch6Lane2Do(ctx, rt, method, target, headers, payload)
	if err != nil {
		if batch6Lane2IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Freshdesk timed out"}, false
		}
		return map[string]any{"error": "Freshdesk request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Freshdesk API key."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Check your Freshdesk permissions."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Not found."}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limited. Try again shortly."}, false
	}
	if reply.Status != http.StatusOK &&
		reply.Status != http.StatusCreated &&
		reply.Status != http.StatusAccepted {
		return map[string]any{
			"error": fmt.Sprintf(
				"Freshdesk API error %d: %s",
				reply.Status,
				batch6Lane2TruncateText(string(reply.Body), batch6Lane2MaxError),
			),
		}, false
	}
	value, ok := batch6Lane2DecodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Failed to parse Freshdesk response"}, false
	}
	return value, true
}

// batch6Lane2FreshdeskTicket flattens a ticket the way _extract_ticket does.
func batch6Lane2FreshdeskTicket(source map[string]any) map[string]any {
	return map[string]any{
		"id":           batch6Lane2JSONNumber(source["id"]),
		"subject":      batch6Lane2AsString(source["subject"]),
		"description":  batch6Lane2TruncateText(batch6Lane2AsString(source["description"]), batch6Lane2MaxError),
		"status":       batch6Lane2JSONNumber(source["status"]),
		"priority":     batch6Lane2JSONNumber(source["priority"]),
		"type":         batch6Lane2JSONNumber(source["type"]),
		"tags":         batch6Lane2StringList(source["tags"]),
		"requester_id": batch6Lane2JSONNumber(source["requester_id"]),
		"responder_id": batch6Lane2JSONNumber(source["responder_id"]),
		"created_at":   source["created_at"],
		"updated_at":   source["updated_at"],
	}
}

// batch6Lane2FreshdeskContact flattens a contact.
func batch6Lane2FreshdeskContact(source map[string]any) map[string]any {
	return map[string]any{
		"id":         batch6Lane2JSONNumber(source["id"]),
		"name":       batch6Lane2AsString(source["name"]),
		"email":      batch6Lane2AsString(source["email"]),
		"phone":      batch6Lane2AsString(source["phone"]),
		"mobile":     batch6Lane2AsString(source["mobile"]),
		"company_id": batch6Lane2JSONNumber(source["company_id"]),
		"active":     batch6Lane2BoolOr(source["active"], false),
		"created_at": source["created_at"],
		"updated_at": source["updated_at"],
	}
}

// batch6Lane2FreshdeskAgent flattens an agent.
func batch6Lane2FreshdeskAgent(source map[string]any) map[string]any {
	contact := batch6Lane2AsMap(source["contact"])
	return map[string]any{
		"id":         batch6Lane2JSONNumber(source["id"]),
		"contact_id": batch6Lane2JSONNumber(source["contact_id"]),
		"email":      batch6Lane2AsString(source["email"]),
		"occasional": batch6Lane2BoolOr(source["occasional"], false),
		"available":  batch6Lane2BoolOr(source["available"], false),
		"name":       batch6Lane2AsString(contact["name"]),
	}
}

// batch6Lane2FreshdeskGroup flattens a group.
func batch6Lane2FreshdeskGroup(source map[string]any) map[string]any {
	return map[string]any{
		"id":             batch6Lane2JSONNumber(source["id"]),
		"name":           batch6Lane2AsString(source["name"]),
		"description":    batch6Lane2AsString(source["description"]),
		"unassigned_for": batch6Lane2AsString(source["unassigned_for"]),
		"created_at":     source["created_at"],
		"updated_at":     source["updated_at"],
	}
}

// batch6Lane2FreshdeskCompany flattens a company.
func batch6Lane2FreshdeskCompany(source map[string]any) map[string]any {
	return map[string]any{
		"id":          batch6Lane2JSONNumber(source["id"]),
		"name":        batch6Lane2AsString(source["name"]),
		"description": batch6Lane2AsString(source["description"]),
		"domains":     batch6Lane2StringList(source["domains"]),
		"note":        batch6Lane2AsString(source["note"]),
		"created_at":  source["created_at"],
		"updated_at":  source["updated_at"],
	}
}

// batch6Lane2FreshdeskItems extracts an array payload into flattened items.
func batch6Lane2FreshdeskItems(value any, extract func(map[string]any) map[string]any) []map[string]any {
	items := make([]map[string]any, 0)
	for _, raw := range batch6Lane2AsSlice(value) {
		items = append(items, extract(batch6Lane2AsMap(raw)))
	}
	return items
}

func batch6Lane2FreshdeskListTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "per_page", 30), 1, 100))},
	}
	if filter := stringArg(input, "filter"); filter != "" {
		query.Set("filter", filter)
	}
	if updatedSince := stringArg(input, "updated_since"); updatedSince != "" {
		query.Set("updated_since", updatedSince)
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/tickets", query, nil)
	if !ok {
		return value, nil
	}
	tickets := batch6Lane2FreshdeskItems(value, batch6Lane2FreshdeskTicket)
	return map[string]any{"tickets": tickets, "count": len(tickets)}, nil
}

func batch6Lane2FreshdeskFilterTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	search := stringArg(input, "query")
	if search == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	quoted := strings.TrimSpace(search)
	if !(strings.HasPrefix(quoted, "\"") && strings.HasSuffix(quoted, "\"")) {
		quoted = "\"" + quoted + "\""
	}
	query := url.Values{
		"query": {quoted},
		"page":  {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 10))},
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/search/tickets", query, nil)
	if !ok {
		return value, nil
	}
	object := batch6Lane2AsMap(value)
	tickets := batch6Lane2FreshdeskItems(object["results"], batch6Lane2FreshdeskTicket)
	total := len(tickets)
	if raw, present := object["total"]; present && raw != nil {
		total = intArg(object, "total", len(tickets))
	}
	return map[string]any{"tickets": tickets, "count": len(tickets), "total": total}, nil
}

func batch6Lane2FreshdeskListTicketConversations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	query := url.Values{
		"page":     {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "per_page", 30), 1, 100))},
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d/conversations", ticketID), query, nil,
	)
	if !ok {
		return value, nil
	}
	raw := batch6Lane2AsSlice(value)
	if raw == nil {
		raw = batch6Lane2AsSlice(batch6Lane2AsMap(value)["conversations"])
	}
	conversations := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item := batch6Lane2AsMap(entry)
		conversations = append(conversations, map[string]any{
			"id":         batch6Lane2JSONNumber(item["id"]),
			"body_text":  batch6Lane2TruncateText(batch6Lane2AsString(item["body_text"]), batch6Lane2MaxError),
			"private":    item["private"],
			"user_id":    batch6Lane2JSONNumber(item["user_id"]),
			"created_at": item["created_at"],
		})
	}
	return map[string]any{"conversations": conversations, "count": len(conversations)}, nil
}

func batch6Lane2FreshdeskGetTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d", ticketID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskTicket(batch6Lane2AsMap(value)), nil
}

func batch6Lane2FreshdeskCreateTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := stringArg(input, "email")
	subject := stringArg(input, "subject")
	description := stringArg(input, "description")
	if email == "" || subject == "" || description == "" {
		return map[string]any{"error": "email, subject, and description are required"}, nil
	}
	payload := map[string]any{
		"email":       email,
		"subject":     subject,
		"description": description,
	}
	if priority, present := batch6Lane2OptionalInt(input, "priority"); present {
		payload["priority"] = priority
	}
	if status, present := batch6Lane2OptionalInt(input, "status"); present {
		payload["status"] = status
	}
	if tags := stringArg(input, "tags"); tags != "" {
		payload["tags"] = batch6Lane2SplitTags(tags)
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodPost, "/tickets", nil, payload)
	if !ok {
		return value, nil
	}
	created := batch6Lane2AsMap(value)
	id := batch6Lane2JSONNumber(created["id"])
	var ticketURL any
	if batch6Lane2StringID(id) != "" {
		ticketURL = "https://" + batch6Lane2FreshdeskDomain(rt) + "/a/tickets/" + batch6Lane2StringID(id)
	}
	return map[string]any{
		"id":      id,
		"subject": batch6Lane2AsString(created["subject"]),
		"status":  batch6Lane2JSONNumber(created["status"]),
		"url":     ticketURL,
		"result":  "created",
	}, nil
}

func batch6Lane2FreshdeskUpdateTicket(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	updates := map[string]any{}
	if status, present := batch6Lane2OptionalInt(input, "status"); present {
		updates["status"] = status
	}
	if priority, present := batch6Lane2OptionalInt(input, "priority"); present {
		updates["priority"] = priority
	}
	if tags := stringArg(input, "tags"); tags != "" {
		updates["tags"] = batch6Lane2SplitTags(tags)
	}
	note := stringArg(input, "note")
	if len(updates) == 0 && note == "" {
		return map[string]any{"error": "At least one field (status, priority, tags, note) is required"}, nil
	}

	var ticketData any
	if len(updates) > 0 {
		value, ok := batch6Lane2FreshdeskCall(
			ctx, rt, http.MethodPut, fmt.Sprintf("/tickets/%d", ticketID), nil, updates,
		)
		if !ok {
			return value, nil
		}
		ticketData = value
	} else {
		value, ok := batch6Lane2FreshdeskCall(
			ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d", ticketID), nil, nil,
		)
		if !ok {
			return value, nil
		}
		ticketData = value
	}

	result := batch6Lane2FreshdeskTicket(batch6Lane2AsMap(ticketData))
	if note != "" {
		notePayload := map[string]any{
			"body":    note,
			"private": boolArg(input, "note_private", true),
		}
		noteResult, noteOK := batch6Lane2FreshdeskCall(
			ctx, rt, http.MethodPost, fmt.Sprintf("/tickets/%d/notes", ticketID), nil, notePayload,
		)
		if !noteOK {
			if failure := batch6Lane2AsMap(noteResult); failure != nil {
				result["note_error"] = failure["error"]
			}
		}
	}
	return result, nil
}

func batch6Lane2FreshdeskAddTicketReply(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	ticketID := intArg(input, "ticket_id", 0)
	body := stringArg(input, "body")
	if ticketID == 0 {
		return map[string]any{"error": "ticket_id is required"}, nil
	}
	if body == "" {
		return map[string]any{"error": "body is required"}, nil
	}
	isPublic := boolArg(input, "public", true)
	path := fmt.Sprintf("/tickets/%d/reply", ticketID)
	payload := map[string]any{"body": body}
	if !isPublic {
		path = fmt.Sprintf("/tickets/%d/notes", ticketID)
		payload["private"] = true
	} else if fromEmail, present := batch6Lane2OptionalString(input, "from_email"); present {
		payload["from_email"] = fromEmail
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodPost, path, nil, payload)
	if !ok {
		return value, nil
	}
	created := batch6Lane2AsMap(value)
	renderedBody := batch6Lane2AsString(created["body"])
	if renderedBody == "" {
		renderedBody = batch6Lane2AsString(created["body_text"])
	}
	return map[string]any{
		"id":         batch6Lane2JSONNumber(created["id"]),
		"body":       renderedBody,
		"public":     isPublic,
		"created_at": created["created_at"],
	}, nil
}

func batch6Lane2FreshdeskListContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "per_page", 30), 1, 100))},
	}
	if email := stringArg(input, "email"); email != "" {
		query.Set("email", email)
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/contacts", query, nil)
	if !ok {
		return value, nil
	}
	contacts := batch6Lane2FreshdeskItems(value, batch6Lane2FreshdeskContact)
	return map[string]any{"contacts": contacts, "count": len(contacts)}, nil
}

func batch6Lane2FreshdeskGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactID, hasID := batch6Lane2OptionalInt(input, "contact_id")
	email := stringArg(input, "email")
	if (!hasID || contactID == 0) && email == "" {
		return map[string]any{"error": "contact_id or email is required"}, nil
	}
	if hasID && contactID != 0 {
		value, ok := batch6Lane2FreshdeskCall(
			ctx, rt, http.MethodGet, fmt.Sprintf("/contacts/%d", contactID), nil, nil,
		)
		if !ok {
			return value, nil
		}
		return batch6Lane2FreshdeskContact(batch6Lane2AsMap(value)), nil
	}
	query := url.Values{"email": {email}}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/contacts", query, nil)
	if !ok {
		return value, nil
	}
	if items := batch6Lane2AsSlice(value); len(items) == 0 {
		if object := batch6Lane2AsMap(value); len(object) == 0 {
			return map[string]any{"error": "Contact not found"}, nil
		}
		return batch6Lane2FreshdeskContact(batch6Lane2AsMap(value)), nil
	}
	return batch6Lane2FreshdeskContact(batch6Lane2AsMap(batch6Lane2AsSlice(value)[0])), nil
}

func batch6Lane2FreshdeskCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	email := stringArg(input, "email")
	if email == "" {
		return map[string]any{"error": "email is required"}, nil
	}
	payload := map[string]any{"email": email}
	if name := stringArg(input, "name"); name != "" {
		payload["name"] = name
	}
	if phone := stringArg(input, "phone"); phone != "" {
		payload["phone"] = phone
	}
	if companyID, present := batch6Lane2OptionalInt(input, "company_id"); present {
		payload["company_id"] = companyID
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodPost, "/contacts", nil, payload)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskContact(batch6Lane2AsMap(value)), nil
}

func batch6Lane2FreshdeskUpdateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactID := intArg(input, "contact_id", 0)
	if contactID == 0 {
		return map[string]any{"error": "contact_id is required"}, nil
	}
	payload := map[string]any{}
	if name, present := batch6Lane2OptionalString(input, "name"); present {
		payload["name"] = name
	}
	if email, present := batch6Lane2OptionalString(input, "email"); present {
		payload["email"] = email
	}
	if phone, present := batch6Lane2OptionalString(input, "phone"); present {
		payload["phone"] = phone
	}
	if companyID, present := batch6Lane2OptionalInt(input, "company_id"); present {
		payload["company_id"] = companyID
	}
	if len(payload) == 0 {
		return map[string]any{
			"error": "At least one field (name, email, phone, company_id) is required",
		}, nil
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodPut, fmt.Sprintf("/contacts/%d", contactID), nil, payload,
	)
	if !ok {
		return value, nil
	}
	return batch6Lane2FreshdeskContact(batch6Lane2AsMap(value)), nil
}

func batch6Lane2FreshdeskListAgents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "per_page", 30), 1, 100))},
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
	return batch6Lane2FreshdeskAgent(batch6Lane2AsMap(value)), nil
}

func batch6Lane2FreshdeskListGroups(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "per_page", 30), 1, 100))},
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
	return batch6Lane2FreshdeskGroup(batch6Lane2AsMap(value)), nil
}

func batch6Lane2FreshdeskListCompanies(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(batch6Lane2Clamp(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(batch6Lane2Clamp(intArg(input, "per_page", 30), 1, 100))},
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
	return batch6Lane2FreshdeskCompany(batch6Lane2AsMap(value)), nil
}
