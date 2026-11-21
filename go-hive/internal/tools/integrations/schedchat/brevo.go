package schedchat

// Brevo: contacts, transactional email/SMS and email campaigns (9 actions).

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------- brevo ---

// brevoTool mirrors gen_catalog.py output for brevo (9 actions).
var brevoTool = &Tool{
	ID:          "brevo_tool",
	Name:        "brevo",
	DisplayName: "Brevo",
	Summary:     "Brevo integration (9 actions).",
	Actions: []Action{
		{
			Name:     "brevo_create_contact",
			Summary:  "Create a new contact in Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email":      param("string"),
				"first_name": paramDefault("string", ""),
				"last_name":  paramDefault("string", ""),
				"phone":      paramDefault("string", ""),
				"list_ids":   paramDefault("string", ""),
			},
		},
		{
			Name:     "brevo_delete_contact",
			Summary:  "Delete a contact from Brevo by email address.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email": param("string"),
			},
		},
		{
			Name:     "brevo_get_contact",
			Summary:  "Retrieve a contact from Brevo by email address.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email": param("string"),
			},
		},
		{
			Name:     "brevo_get_email_stats",
			Summary:  "Get delivery statistics for a sent transactional email.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
			},
		},
		{
			Name:    "brevo_list_contacts",
			Summary: "List contacts in Brevo with pagination.",
			Source:  "brevo_tool/brevo_tool.py",
			Params: map[string]any{
				"limit":          paramDefault("integer", 50),
				"offset":         paramDefault("integer", 0),
				"modified_since": paramDefault("string", ""),
			},
		},
		{
			Name:    "brevo_list_email_campaigns",
			Summary: "List email campaigns from Brevo.",
			Source:  "brevo_tool/brevo_tool.py",
			Params: map[string]any{
				"status": paramDefault("string", ""),
				"limit":  paramDefault("integer", 50),
				"offset": paramDefault("integer", 0),
			},
		},
		{
			Name:     "brevo_send_email",
			Summary:  "Send a transactional email via Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"to_email", "to_name", "subject", "html_content", "from_email", "from_name"},
			Params: map[string]any{
				"to_email":     param("string"),
				"to_name":      param("string"),
				"subject":      param("string"),
				"html_content": param("string"),
				"from_email":   param("string"),
				"from_name":    param("string"),
				"text_content": paramDefault("string", ""),
			},
		},
		{
			Name:     "brevo_send_sms",
			Summary:  "Send a transactional SMS via Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"to", "content", "sender"},
			Params: map[string]any{
				"to":      param("string"),
				"content": param("string"),
				"sender":  param("string"),
			},
		},
		{
			Name:     "brevo_update_contact",
			Summary:  "Update an existing contact in Brevo.",
			Source:   "brevo_tool/brevo_tool.py",
			Required: []string{"email"},
			Params: map[string]any{
				"email":      param("string"),
				"first_name": paramDefault("string", ""),
				"last_name":  paramDefault("string", ""),
				"phone":      paramDefault("string", ""),
				"list_ids":   paramDefault("string", ""),
			},
		},
	},
}

// brevoImpls binds every brevo action to its implementation.
func brevoImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"brevo_create_contact":       brevoCreateContact,
		"brevo_delete_contact":       brevoDeleteContact,
		"brevo_get_contact":          brevoGetContact,
		"brevo_get_email_stats":      brevoGetEmailStats,
		"brevo_list_contacts":        brevoListContacts,
		"brevo_list_email_campaigns": brevoListEmailCampaigns,
		"brevo_send_email":           brevoSendEmail,
		"brevo_send_sms":             brevoSendSms,
		"brevo_update_contact":       brevoUpdateContact,
	}
}

// brevoListIDs parses the upstream comma-separated list_ids string.
func brevoListIDs(input map[string]any) ([]any, map[string]any) {
	raw := strings.TrimSpace(stringArg(input, "list_ids"))
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]any, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, map[string]any{
				"error": "list_ids must be comma-separated integers (e.g. '2,5,8')",
			}
		}
		ids = append(ids, parsed)
	}
	return ids, nil
}

// brevoAttributes builds the FIRSTNAME/LASTNAME/SMS block.
func brevoAttributes(input map[string]any) map[string]any {
	attributes := map[string]any{}
	if value := stringArg(input, "first_name"); value != "" {
		attributes["FIRSTNAME"] = value
	}
	if value := stringArg(input, "last_name"); value != "" {
		attributes["LASTNAME"] = value
	}
	if value := stringArg(input, "phone"); value != "" {
		attributes["SMS"] = value
	}
	return attributes
}

// brevoSendEmail sends a transactional email.
func brevoSendEmail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	toEmail := stringArg(input, "to_email")
	if toEmail == "" || !strings.Contains(toEmail, "@") {
		return map[string]any{"error": "Invalid recipient email address"}, nil
	}
	subject := stringArg(input, "subject")
	if subject == "" {
		return map[string]any{"error": "Email subject cannot be empty"}, nil
	}
	htmlContent := stringArg(input, "html_content")
	if htmlContent == "" {
		return map[string]any{"error": "Email content cannot be empty"}, nil
	}
	body := map[string]any{
		"sender": map[string]any{
			"email": stringArg(input, "from_email"),
			"name":  stringArg(input, "from_name"),
		},
		"to": []any{map[string]any{
			"email": toEmail,
			"name":  stringArg(input, "to_name"),
		}},
		"subject":     subject,
		"htmlContent": htmlContent,
	}
	if text := stringArg(input, "text_content"); text != "" {
		body["textContent"] = text
	}
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodPost, "/smtp/email", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"success":    true,
		"message_id": asMap(parsed)["messageId"],
	}, nil
}

// brevoSendSms sends a transactional SMS.
func brevoSendSms(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	to := stringArg(input, "to")
	if !strings.HasPrefix(to, "+") {
		return map[string]any{"error": "Phone number must be in international format starting with '+'"}, nil
	}
	content := stringArg(input, "content")
	if content == "" {
		return map[string]any{"error": "SMS content cannot be empty"}, nil
	}
	if utf8.RuneCountInString(content) > 640 {
		return map[string]any{"error": "SMS content too long (max 640 characters)"}, nil
	}
	body := map[string]any{
		"sender":    stringArg(input, "sender"),
		"recipient": to,
		"content":   content,
	}
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodPost, "/transactionalSMS/sms", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	result := asMap(parsed)
	return map[string]any{
		"success":           true,
		"reference":         result["reference"],
		"remaining_credits": result["remainingCredits"],
	}, nil
}

// brevoCreateContact creates a contact.
func brevoCreateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	listIDs, errDict := brevoListIDs(input)
	if errDict != nil {
		return errDict, nil
	}
	body := map[string]any{
		"email":      email,
		"attributes": brevoAttributes(input),
	}
	if listIDs != nil {
		body["listIds"] = listIDs
	}
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodPost, "/contacts", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"success": true,
		"id":      asMap(parsed)["id"],
		"email":   email,
	}, nil
}

// brevoGetContact reads one contact.
func brevoGetContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodGet,
		"/contacts/"+url.PathEscape(email), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	contact := asMap(parsed)
	attributes := asMap(contact["attributes"])
	return map[string]any{
		"success":           true,
		"id":                contact["id"],
		"email":             contact["email"],
		"first_name":        attributes["FIRSTNAME"],
		"last_name":         attributes["LASTNAME"],
		"phone":             attributes["SMS"],
		"list_ids":          sliceOrEmpty(contact["listIds"]),
		"email_blacklisted": asBool(contact["emailBlacklisted"]),
		"sms_blacklisted":   asBool(contact["smsBlacklisted"]),
		"created_at":        contact["createdAt"],
		"modified_at":       contact["modifiedAt"],
	}, nil
}

// brevoUpdateContact updates a contact.
func brevoUpdateContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	listIDs, errDict := brevoListIDs(input)
	if errDict != nil {
		return errDict, nil
	}
	body := map[string]any{"attributes": brevoAttributes(input)}
	if listIDs != nil {
		body["listIds"] = listIDs
	}
	if _, errDict := call(ctx, rt, "brevo_tool", http.MethodPut,
		"/contacts/"+url.PathEscape(email), nil, body); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "email": email}, nil
}

// brevoListContacts lists contacts with pagination.
func brevoListContacts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	limit := clampInt(intArg(input, "limit", 50), 1, 1000)
	query := queryValues(map[string]any{
		"limit":         limit,
		"offset":        intArg(input, "offset", 0),
		"modifiedSince": stringArg(input, "modified_since"),
	})
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodGet, "/contacts", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	result := asMap(parsed)
	contacts := make([]any, 0)
	for _, raw := range sliceOrEmpty(result["contacts"]) {
		contact := asMap(raw)
		attributes := asMap(contact["attributes"])
		contacts = append(contacts, map[string]any{
			"id":                contact["id"],
			"email":             contact["email"],
			"first_name":        attributes["FIRSTNAME"],
			"last_name":         attributes["LASTNAME"],
			"list_ids":          sliceOrEmpty(contact["listIds"]),
			"email_blacklisted": asBool(contact["emailBlacklisted"]),
			"modified_at":       contact["modifiedAt"],
		})
	}
	return map[string]any{
		"count":    len(contacts),
		"total":    intFrom(result["count"], len(contacts)),
		"contacts": contacts,
	}, nil
}

// brevoDeleteContact deletes a contact.
func brevoDeleteContact(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	email := stringArg(input, "email")
	if email == "" || !strings.Contains(email, "@") {
		return map[string]any{"error": "Invalid email address"}, nil
	}
	if _, errDict := call(ctx, rt, "brevo_tool", http.MethodDelete,
		"/contacts/"+url.PathEscape(email), nil, nil); errDict != nil {
		return errDict, nil
	}
	return map[string]any{"success": true, "email": email, "status": "deleted"}, nil
}

// brevoListEmailCampaigns lists email campaigns.
func brevoListEmailCampaigns(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	limit := clampInt(intArg(input, "limit", 50), 1, 1000)
	query := queryValues(map[string]any{
		"status": stringArg(input, "status"),
		"limit":  limit,
		"offset": intArg(input, "offset", 0),
	})
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodGet, "/emailCampaigns", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	result := asMap(parsed)
	campaigns := make([]any, 0)
	for _, raw := range sliceOrEmpty(result["campaigns"]) {
		campaign := asMap(raw)
		statistics := asMap(asMap(campaign["statistics"])["globalStats"])
		if statistics == nil {
			statistics = map[string]any{}
		}
		campaigns = append(campaigns, map[string]any{
			"id":           campaign["id"],
			"name":         campaign["name"],
			"subject":      campaign["subject"],
			"status":       campaign["status"],
			"type":         campaign["type"],
			"created_at":   campaign["createdAt"],
			"scheduled_at": campaign["scheduledAt"],
			"statistics":   statistics,
		})
	}
	return map[string]any{
		"count":     len(campaigns),
		"total":     intFrom(result["count"], len(campaigns)),
		"campaigns": campaigns,
	}, nil
}

// brevoGetEmailStats reads delivery stats for one message.
func brevoGetEmailStats(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "brevo_tool"); errDict != nil {
		return errDict, nil
	}
	messageID := stringArg(input, "message_id")
	if messageID == "" {
		return map[string]any{"error": "message_id cannot be empty"}, nil
	}
	parsed, errDict := call(ctx, rt, "brevo_tool", http.MethodGet,
		"/smtp/emails/"+url.PathEscape(messageID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	result := asMap(parsed)
	return map[string]any{
		"success":    true,
		"message_id": result["messageId"],
		"email":      result["email"],
		"subject":    result["subject"],
		"date":       result["date"],
		"events":     sliceOrEmpty(result["events"]),
	}, nil
}
