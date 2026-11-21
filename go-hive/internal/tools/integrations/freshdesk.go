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

var freshdeskTool = &Tool{
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

func freshdeskImpls() map[string]ActionFunc {
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
	return supportModuleToken(rt, "freshdesk_tool")
}

// batch6Lane2FreshdeskDomain resolves the account host (e.g. acme.freshdesk.com).
func batch6Lane2FreshdeskDomain(rt *Runtime) string {
	return strings.TrimSpace(runtimeGetenv(rt)("FRESHDESK_DOMAIN"))
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
	if base := moduleBaseURL(rt, "freshdesk_tool"); base != "" {
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
		"Authorization": basicAuth(apiKey, "X"),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	reply, err := supportDo(ctx, rt, method, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
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
				truncateText(string(reply.Body), supportMaxError),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Failed to parse Freshdesk response"}, false
	}
	return value, true
}

// batch6Lane2FreshdeskTicket flattens a ticket the way _extract_ticket does.
func batch6Lane2FreshdeskTicket(source map[string]any) map[string]any {
	return map[string]any{
		"id":           jsonNumber(source["id"]),
		"subject":      asString(source["subject"]),
		"description":  truncateText(asString(source["description"]), supportMaxError),
		"status":       jsonNumber(source["status"]),
		"priority":     jsonNumber(source["priority"]),
		"type":         jsonNumber(source["type"]),
		"tags":         supportStringList(source["tags"]),
		"requester_id": jsonNumber(source["requester_id"]),
		"responder_id": jsonNumber(source["responder_id"]),
		"created_at":   source["created_at"],
		"updated_at":   source["updated_at"],
	}
}

// batch6Lane2FreshdeskContact flattens a contact.
func batch6Lane2FreshdeskContact(source map[string]any) map[string]any {
	return map[string]any{
		"id":         jsonNumber(source["id"]),
		"name":       asString(source["name"]),
		"email":      asString(source["email"]),
		"phone":      asString(source["phone"]),
		"mobile":     asString(source["mobile"]),
		"company_id": jsonNumber(source["company_id"]),
		"active":     boolOr(source["active"], false),
		"created_at": source["created_at"],
		"updated_at": source["updated_at"],
	}
}

// batch6Lane2FreshdeskAgent flattens an agent.
func batch6Lane2FreshdeskAgent(source map[string]any) map[string]any {
	contact := asMap(source["contact"])
	return map[string]any{
		"id":         jsonNumber(source["id"]),
		"contact_id": jsonNumber(source["contact_id"]),
		"email":      asString(source["email"]),
		"occasional": boolOr(source["occasional"], false),
		"available":  boolOr(source["available"], false),
		"name":       asString(contact["name"]),
	}
}

// batch6Lane2FreshdeskGroup flattens a group.
func batch6Lane2FreshdeskGroup(source map[string]any) map[string]any {
	return map[string]any{
		"id":             jsonNumber(source["id"]),
		"name":           asString(source["name"]),
		"description":    asString(source["description"]),
		"unassigned_for": asString(source["unassigned_for"]),
		"created_at":     source["created_at"],
		"updated_at":     source["updated_at"],
	}
}

// batch6Lane2FreshdeskCompany flattens a company.
func batch6Lane2FreshdeskCompany(source map[string]any) map[string]any {
	return map[string]any{
		"id":          jsonNumber(source["id"]),
		"name":        asString(source["name"]),
		"description": asString(source["description"]),
		"domains":     supportStringList(source["domains"]),
		"note":        asString(source["note"]),
		"created_at":  source["created_at"],
		"updated_at":  source["updated_at"],
	}
}

// batch6Lane2FreshdeskItems extracts an array payload into flattened items.
func batch6Lane2FreshdeskItems(value any, extract func(map[string]any) map[string]any) []map[string]any {
	items := make([]map[string]any, 0)
	for _, raw := range asSlice(value) {
		items = append(items, extract(asMap(raw)))
	}
	return items
}
