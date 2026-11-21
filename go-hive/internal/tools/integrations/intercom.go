// Intercom module of batch 6 lane 2: 11 actions over Intercom API v2.11.
//
// The admin id used by reply/assign/tag/close is resolved lazily from GET /me
// on every call. Upstream builds a fresh _IntercomClient per tool invocation,
// so its per-client cache never survives across calls either.

package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

var intercomTool = &Tool{
	ID:          "intercom_tool",
	Name:        "intercom",
	DisplayName: "Intercom",
	Summary:     "Intercom customer messaging, conversations, and support automation (11 actions).",
	Actions: []Action{
		{
			Name:    "intercom_search_conversations",
			Summary: "Search Intercom conversations with optional status, assignee, tag, and created-after filters.",
			Source:  "intercom_tool/intercom_tool.py",
			Params: map[string]any{
				"status":        paramDefault("string", ""),
				"assignee_id":   paramDefault("string", ""),
				"tag":           paramDefault("string", ""),
				"created_after": paramDefault("string", ""),
				"limit":         paramDefault("integer", 20),
			},
		},
		{
			Name:     "intercom_get_conversation",
			Summary:  "Get full conversation details including message history.",
			Source:   "intercom_tool/intercom_tool.py",
			Required: []string{"conversation_id"},
			Params: map[string]any{
				"conversation_id": param("string"),
			},
		},
		{
			Name:    "intercom_get_contact",
			Summary: "Get an Intercom contact by ID, or fall back to an email search.",
			Source:  "intercom_tool/intercom_tool.py",
			Params: map[string]any{
				"contact_id": paramDefault("string", ""),
				"email":      paramDefault("string", ""),
			},
		},
		{
			Name:     "intercom_search_contacts",
			Summary:  "Search contacts by email, name, or custom attributes.",
			Source:   "intercom_tool/intercom_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query": param("string"),
				"limit": paramDefault("integer", 20),
			},
		},
		{
			Name:     "intercom_add_note",
			Summary:  "Add an internal note to a conversation.",
			Source:   "intercom_tool/intercom_tool.py",
			Required: []string{"conversation_id", "body"},
			Params: map[string]any{
				"conversation_id": param("string"),
				"body":            param("string"),
			},
		},
		{
			Name:     "intercom_add_tag",
			Summary:  "Add a tag to a conversation or contact (created if missing).",
			Source:   "intercom_tool/intercom_tool.py",
			Required: []string{"name"},
			Params: map[string]any{
				"name":            param("string"),
				"conversation_id": paramDefault("string", ""),
				"contact_id":      paramDefault("string", ""),
			},
		},
		{
			Name:     "intercom_assign_conversation",
			Summary:  "Assign a conversation to an admin or team.",
			Source:   "intercom_tool/intercom_tool.py",
			Required: []string{"conversation_id", "assignee_id"},
			Params: map[string]any{
				"conversation_id": param("string"),
				"assignee_id":     param("string"),
				"assignee_type":   paramDefault("string", "admin"),
				"body":            paramDefault("string", ""),
			},
		},
		{
			Name:    "intercom_list_teams",
			Summary: "List available Intercom teams for conversation routing.",
			Source:  "intercom_tool/intercom_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "intercom_close_conversation",
			Summary:  "Close an Intercom conversation with an optional closing message.",
			Source:   "intercom_tool/intercom_tool.py",
			Required: []string{"conversation_id"},
			Params: map[string]any{
				"conversation_id": param("string"),
				"body":            paramDefault("string", ""),
			},
		},
		{
			Name:    "intercom_create_contact",
			Summary: "Create a new Intercom contact (user or lead).",
			Source:  "intercom_tool/intercom_tool.py",
			Params: map[string]any{
				"role":        paramDefault("string", "user"),
				"email":       paramDefault("string", ""),
				"name":        paramDefault("string", ""),
				"phone":       paramDefault("string", ""),
				"external_id": paramDefault("string", ""),
			},
		},
		{
			Name:    "intercom_list_conversations",
			Summary: "List Intercom conversations with cursor pagination.",
			Source:  "intercom_tool/intercom_tool.py",
			Params: map[string]any{
				"limit":          paramDefault("integer", 20),
				"starting_after": paramDefault("string", ""),
			},
		},
	},
}

func intercomImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"intercom_search_conversations": batch6Lane2IntercomSearchConversations,
		"intercom_get_conversation":     batch6Lane2IntercomGetConversation,
		"intercom_get_contact":          batch6Lane2IntercomGetContact,
		"intercom_search_contacts":      batch6Lane2IntercomSearchContacts,
		"intercom_add_note":             batch6Lane2IntercomAddNote,
		"intercom_add_tag":              batch6Lane2IntercomAddTag,
		"intercom_assign_conversation":  batch6Lane2IntercomAssignConversation,
		"intercom_list_teams":           batch6Lane2IntercomListTeams,
		"intercom_close_conversation":   batch6Lane2IntercomCloseConversation,
		"intercom_create_contact":       batch6Lane2IntercomCreateContact,
		"intercom_list_conversations":   batch6Lane2IntercomListConversations,
	}
}

// batch6Lane2IntercomAuthError mirrors the missing-credential payload.
func batch6Lane2IntercomAuthError() map[string]any {
	return map[string]any{
		"error": "Intercom credentials not configured",
		"help":  "Set INTERCOM_ACCESS_TOKEN environment variable or configure via credential store",
	}
}

// batch6Lane2IntercomCall performs one Intercom API call and maps HTTP
// failures to the upstream error dicts. The boolean reports success.
func batch6Lane2IntercomCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := supportModuleToken(rt, "intercom_tool")
	if token == "" {
		return batch6Lane2IntercomAuthError(), false
	}
	base := moduleBaseURL(rt, "intercom_tool")
	if base == "" {
		base = "https://api.intercom.io"
	}
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization":    "Bearer " + token,
		"Content-Type":     "application/json",
		"Accept":           "application/json",
		"Intercom-Version": "2.11",
	}
	reply, err := supportDo(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Intercom access token"}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Insufficient permissions. Check your Intercom app scopes."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Resource not found"}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Intercom rate limit exceeded. Try again later."}, false
	}
	if reply.Status >= 400 {
		detail := string(reply.Body)
		if object, ok := decodeObject(reply.Body); ok {
			if entries := asSlice(object["errors"]); len(entries) > 0 {
				if message := asString(asMap(entries[0])["message"]); message != "" {
					detail = message
				}
			}
		}
		return map[string]any{
			"error": fmt.Sprintf("Intercom API error (HTTP %d): %s", reply.Status, detail),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{}, true
	}
	return value, true
}

// batch6Lane2IntercomAdminID resolves the acting admin from GET /me.
func batch6Lane2IntercomAdminID(ctx context.Context, rt *Runtime) (string, map[string]any) {
	value, ok := batch6Lane2IntercomCall(ctx, rt, http.MethodGet, "/me", nil, nil)
	if !ok {
		failure, _ := value.(map[string]any)
		return "", failure
	}
	adminID := stringID(asMap(value)["id"])
	if adminID == "" {
		return "", map[string]any{"error": "Resource not found"}
	}
	return adminID, nil
}

// batch6Lane2IntercomListTags fetches the workspace tags for name resolution.
func batch6Lane2IntercomListTags(ctx context.Context, rt *Runtime) (any, bool) {
	return batch6Lane2IntercomCall(ctx, rt, http.MethodGet, "/tags", nil, nil)
}

func batch6Lane2IntercomSearchConversations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := intArg(input, "limit", 20)
	if limit < 1 || limit > supportMaxItems {
		return map[string]any{"error": "limit must be between 1 and 150"}, nil
	}
	status := stringArg(input, "status")
	if status != "" && status != "open" && status != "closed" && status != "snoozed" {
		return map[string]any{"error": "status must be 'open', 'closed', or 'snoozed'"}, nil
	}

	filters := make([]any, 0, 4)
	if status != "" {
		filters = append(filters, map[string]any{
			"field": "state", "operator": "=", "value": status,
		})
	}
	if assigneeID := stringArg(input, "assignee_id"); assigneeID != "" {
		filters = append(filters, map[string]any{
			"field": "admin_assignee_id", "operator": "=", "value": assigneeID,
		})
	}
	if tag := stringArg(input, "tag"); tag != "" {
		tagsResult, ok := batch6Lane2IntercomListTags(ctx, rt)
		if !ok {
			return tagsResult, nil
		}
		var tagID any
		for _, entry := range asSlice(asMap(tagsResult)["data"]) {
			tagObject := asMap(entry)
			if asString(tagObject["name"]) == tag {
				tagID = tagObject["id"]
				break
			}
		}
		if tagID == nil {
			return map[string]any{"error": "Tag not found: " + tag}, nil
		}
		filters = append(filters, map[string]any{
			"field": "tag_ids", "operator": "IN", "value": []any{tagID},
		})
	}
	if createdAfter := stringArg(input, "created_after"); createdAfter != "" {
		parsed, ok := supportParseISO(createdAfter)
		if !ok {
			return map[string]any{
				"error": "created_after must be a valid ISO date string (e.g., '2026-01-15')",
			}, nil
		}
		filters = append(filters, map[string]any{
			"field": "created_at", "operator": ">", "value": parsed.Unix(),
		})
	}

	var query map[string]any
	switch len(filters) {
	case 0:
		query = map[string]any{"field": "created_at", "operator": ">", "value": 0}
	case 1:
		query = asMap(filters[0])
	default:
		query = map[string]any{"operator": "AND", "value": filters}
	}

	payload := map[string]any{
		"query":      query,
		"pagination": map[string]any{"per_page": clampInt(limit, 1, supportMaxItems)},
	}
	value, _ := batch6Lane2IntercomCall(ctx, rt, http.MethodPost, "/conversations/search", nil, payload)
	return value, nil
}

func batch6Lane2IntercomGetConversation(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	conversationID := stringArg(input, "conversation_id")
	if conversationID == "" {
		return map[string]any{"error": "conversation_id is required"}, nil
	}
	query := url.Values{"display_as": {"plaintext"}}
	value, _ := batch6Lane2IntercomCall(
		ctx, rt, http.MethodGet, "/conversations/"+url.PathEscape(conversationID), query, nil,
	)
	return value, nil
}

func batch6Lane2IntercomGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	contactID := stringArg(input, "contact_id")
	email := stringArg(input, "email")
	if contactID == "" && email == "" {
		return map[string]any{"error": "Either contact_id or email must be provided"}, nil
	}
	if contactID != "" {
		value, _ := batch6Lane2IntercomCall(
			ctx, rt, http.MethodGet, "/contacts/"+url.PathEscape(contactID), nil, nil,
		)
		return value, nil
	}

	query := map[string]any{"field": "email", "operator": "=", "value": email}
	payload := map[string]any{"query": query, "pagination": map[string]any{"per_page": 1}}
	value, ok := batch6Lane2IntercomCall(ctx, rt, http.MethodPost, "/contacts/search", nil, payload)
	if !ok {
		return value, nil
	}
	contacts := asSlice(asMap(value)["data"])
	if len(contacts) == 0 {
		return map[string]any{"error": "No contact found with email: " + email}, nil
	}
	return contacts[0], nil
}

func batch6Lane2IntercomSearchContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	search := stringArg(input, "query")
	if search == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	limit := intArg(input, "limit", 20)
	if limit < 1 || limit > supportMaxItems {
		return map[string]any{"error": "limit must be between 1 and 150"}, nil
	}
	payload := map[string]any{
		"query": map[string]any{
			"operator": "OR",
			"value": []any{
				map[string]any{"field": "email", "operator": "=", "value": search},
				map[string]any{"field": "name", "operator": "~", "value": search},
			},
		},
		"pagination": map[string]any{"per_page": clampInt(limit, 1, supportMaxItems)},
	}
	value, _ := batch6Lane2IntercomCall(ctx, rt, http.MethodPost, "/contacts/search", nil, payload)
	return value, nil
}

func batch6Lane2IntercomAddNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	conversationID := stringArg(input, "conversation_id")
	body := stringArg(input, "body")
	if conversationID == "" {
		return map[string]any{"error": "conversation_id is required"}, nil
	}
	if body == "" {
		return map[string]any{"error": "body is required"}, nil
	}
	return batch6Lane2IntercomReply(ctx, rt, conversationID, body, "note"), nil
}

// batch6Lane2IntercomReply posts a reply/note part to a conversation.
func batch6Lane2IntercomReply(
	ctx context.Context,
	rt *Runtime,
	conversationID, body, messageType string,
) any {
	adminID, failure := batch6Lane2IntercomAdminID(ctx, rt)
	if failure != nil {
		return failure
	}
	payload := map[string]any{
		"type":         "admin",
		"admin_id":     adminID,
		"message_type": messageType,
		"body":         body,
	}
	value, _ := batch6Lane2IntercomCall(
		ctx, rt, http.MethodPost,
		"/conversations/"+url.PathEscape(conversationID)+"/reply", nil, payload,
	)
	return value
}

func batch6Lane2IntercomAddTag(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	name := stringArg(input, "name")
	conversationID := stringArg(input, "conversation_id")
	contactID := stringArg(input, "contact_id")
	if name == "" {
		return map[string]any{"error": "name is required"}, nil
	}
	if conversationID == "" && contactID == "" {
		return map[string]any{"error": "Either conversation_id or contact_id must be provided"}, nil
	}
	if conversationID != "" && contactID != "" {
		return map[string]any{"error": "Provide conversation_id or contact_id, not both"}, nil
	}

	tagValue, ok := batch6Lane2IntercomCall(
		ctx, rt, http.MethodPost, "/tags", nil, map[string]any{"name": name},
	)
	if !ok {
		return tagValue, nil
	}
	tagID := stringID(asMap(tagValue)["id"])
	if tagID == "" {
		return map[string]any{"error": "Resource not found"}, nil
	}

	if conversationID != "" {
		adminID, failure := batch6Lane2IntercomAdminID(ctx, rt)
		if failure != nil {
			return failure, nil
		}
		value, _ := batch6Lane2IntercomCall(
			ctx, rt, http.MethodPost,
			"/conversations/"+url.PathEscape(conversationID)+"/tags", nil,
			map[string]any{"id": tagID, "admin_id": adminID},
		)
		return value, nil
	}
	value, _ := batch6Lane2IntercomCall(
		ctx, rt, http.MethodPost,
		"/contacts/"+url.PathEscape(contactID)+"/tags", nil,
		map[string]any{"id": tagID},
	)
	return value, nil
}

func batch6Lane2IntercomAssignConversation(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	conversationID := stringArg(input, "conversation_id")
	assigneeID := stringArg(input, "assignee_id")
	assigneeType := stringArg(input, "assignee_type")
	if assigneeType == "" {
		assigneeType = "admin"
	}
	if conversationID == "" {
		return map[string]any{"error": "conversation_id is required"}, nil
	}
	if assigneeID == "" {
		return map[string]any{"error": "assignee_id is required"}, nil
	}
	if assigneeType != "admin" && assigneeType != "team" {
		return map[string]any{"error": "assignee_type must be 'admin' or 'team'"}, nil
	}

	adminID, failure := batch6Lane2IntercomAdminID(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	payload := map[string]any{
		"type":          "admin",
		"admin_id":      adminID,
		"assignee_id":   assigneeID,
		"assignee_type": assigneeType,
		"message_type":  "assignment",
		"body":          stringArg(input, "body"),
	}
	value, _ := batch6Lane2IntercomCall(
		ctx, rt, http.MethodPost,
		"/conversations/"+url.PathEscape(conversationID)+"/parts", nil, payload,
	)
	return value, nil
}

func batch6Lane2IntercomListTeams(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	value, _ := batch6Lane2IntercomCall(ctx, rt, http.MethodGet, "/teams", nil, nil)
	return value, nil
}

func batch6Lane2IntercomCloseConversation(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	conversationID := stringArg(input, "conversation_id")
	if conversationID == "" {
		return map[string]any{"error": "conversation_id is required"}, nil
	}
	adminID, failure := batch6Lane2IntercomAdminID(ctx, rt)
	if failure != nil {
		return failure, nil
	}
	payload := map[string]any{
		"type":         "admin",
		"admin_id":     adminID,
		"message_type": "close",
	}
	if body := stringArg(input, "body"); body != "" {
		payload["body"] = body
	}
	value, _ := batch6Lane2IntercomCall(
		ctx, rt, http.MethodPost,
		"/conversations/"+url.PathEscape(conversationID)+"/parts", nil, payload,
	)
	return value, nil
}

func batch6Lane2IntercomCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	role := stringArg(input, "role")
	if role == "" {
		role = "user"
	}
	if role != "user" && role != "lead" {
		return map[string]any{"error": "role must be 'user' or 'lead'"}, nil
	}
	payload := map[string]any{"role": role}
	for _, field := range []string{"email", "name", "phone", "external_id"} {
		if value := stringArg(input, field); value != "" {
			payload[field] = value
		}
	}
	value, _ := batch6Lane2IntercomCall(ctx, rt, http.MethodPost, "/contacts", nil, payload)
	return value, nil
}

func batch6Lane2IntercomListConversations(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := intArg(input, "limit", 20)
	query := url.Values{
		"per_page":   {fmt.Sprint(clampInt(limit, 1, supportMaxItems))},
		"display_as": {"plaintext"},
	}
	if cursor := stringArg(input, "starting_after"); cursor != "" {
		query.Set("starting_after", cursor)
	}
	value, _ := batch6Lane2IntercomCall(ctx, rt, http.MethodGet, "/conversations", query, nil)
	return value, nil
}
