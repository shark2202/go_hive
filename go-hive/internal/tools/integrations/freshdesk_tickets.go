// Freshdesk ticket actions: list/filter/get/create/update, conversations
// and replies (split from freshdesk.go, which keeps the catalog, the shared
// call helper and the object extractors).

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func batch6Lane2FreshdeskListTickets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"page":     {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 30), 1, 100))},
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
		"page":  {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 10))},
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodGet, "/search/tickets", query, nil)
	if !ok {
		return value, nil
	}
	object := asMap(value)
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
		"page":     {fmt.Sprint(clampInt(intArg(input, "page", 1), 1, 1<<30))},
		"per_page": {fmt.Sprint(clampInt(intArg(input, "per_page", 30), 1, 100))},
	}
	value, ok := batch6Lane2FreshdeskCall(
		ctx, rt, http.MethodGet, fmt.Sprintf("/tickets/%d/conversations", ticketID), query, nil,
	)
	if !ok {
		return value, nil
	}
	raw := asSlice(value)
	if raw == nil {
		raw = asSlice(asMap(value)["conversations"])
	}
	conversations := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item := asMap(entry)
		conversations = append(conversations, map[string]any{
			"id":         jsonNumber(item["id"]),
			"body_text":  truncateText(asString(item["body_text"]), supportMaxError),
			"private":    item["private"],
			"user_id":    jsonNumber(item["user_id"]),
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
	return batch6Lane2FreshdeskTicket(asMap(value)), nil
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
	if priority, present := optionalInt(input, "priority"); present {
		payload["priority"] = priority
	}
	if status, present := optionalInt(input, "status"); present {
		payload["status"] = status
	}
	if tags := stringArg(input, "tags"); tags != "" {
		payload["tags"] = splitCSV(tags)
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodPost, "/tickets", nil, payload)
	if !ok {
		return value, nil
	}
	created := asMap(value)
	id := jsonNumber(created["id"])
	var ticketURL any
	if stringID(id) != "" {
		ticketURL = "https://" + batch6Lane2FreshdeskDomain(rt) + "/a/tickets/" + stringID(id)
	}
	return map[string]any{
		"id":      id,
		"subject": asString(created["subject"]),
		"status":  jsonNumber(created["status"]),
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
	if status, present := optionalInt(input, "status"); present {
		updates["status"] = status
	}
	if priority, present := optionalInt(input, "priority"); present {
		updates["priority"] = priority
	}
	if tags := stringArg(input, "tags"); tags != "" {
		updates["tags"] = splitCSV(tags)
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

	result := batch6Lane2FreshdeskTicket(asMap(ticketData))
	if note != "" {
		notePayload := map[string]any{
			"body":    note,
			"private": boolArg(input, "note_private", true),
		}
		noteResult, noteOK := batch6Lane2FreshdeskCall(
			ctx, rt, http.MethodPost, fmt.Sprintf("/tickets/%d/notes", ticketID), nil, notePayload,
		)
		if !noteOK {
			if failure := asMap(noteResult); failure != nil {
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
	} else if fromEmail, present := optionalString(input, "from_email"); present {
		payload["from_email"] = fromEmail
	}
	value, ok := batch6Lane2FreshdeskCall(ctx, rt, http.MethodPost, path, nil, payload)
	if !ok {
		return value, nil
	}
	created := asMap(value)
	renderedBody := asString(created["body"])
	if renderedBody == "" {
		renderedBody = asString(created["body_text"])
	}
	return map[string]any{
		"id":         jsonNumber(created["id"]),
		"body":       renderedBody,
		"public":     isPublic,
		"created_at": created["created_at"],
	}, nil
}
