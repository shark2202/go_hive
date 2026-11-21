package cloudflare

// Account-level rows: accounts, members, audit logs and invitations.

import "net/http"

var accountRows = []actionSpec{
	{
		Name:    "cloudflare_list_accounts",
		Summary: "List all Cloudflare accounts the token has access to.",
		Method:  http.MethodGet,
		Path:    "/accounts",
		Params: map[string]any{
			"page":     paramDefault("integer", 1),
			"per_page": paramDefault("integer", 20),
		},
		Run: listAccounts,
	},
	{
		Name:     "cloudflare_get_account_details",
		Summary:  "Get details for a specific Cloudflare account.",
		Method:   http.MethodGet,
		Path:     "/accounts/{account_id}",
		Params:   map[string]any{"account_id": param("string")},
		Required: []string{"account_id"},
		Shape:    shapeAccountDetails,
	},
	{
		Name:       "cloudflare_list_account_members",
		Summary:    "List all members/users in a specific account.",
		Method:     http.MethodGet,
		Path:       "/accounts/{account_id}/members",
		Params:     map[string]any{"account_id": param("string")},
		Required:   []string{"account_id"},
		Wrap:       "members",
		WrapErrors: true,
	},
	{
		Name:    "cloudflare_invite_account_member",
		Summary: "Invite a new member to an account with specific roles.",
		Method:  http.MethodPost,
		Path:    "/accounts/{account_id}/members",
		Params: map[string]any{
			"account_id": param("string"),
			"email":      param("string"),
			"roles":      listParam("string"),
		},
		Required:   []string{"account_id", "email", "roles"},
		BodyMap:    map[string]string{"email": "email", "roles": "roles"},
		Wrap:       "invitation",
		WrapErrors: true,
	},
	{
		Name:       "cloudflare_delete_account_member",
		Summary:    "Remove a member's access from an account.",
		Method:     http.MethodDelete,
		Path:       "/accounts/{account_id}/members/{member_id}",
		Params:     map[string]any{"account_id": param("string"), "member_id": param("string")},
		Required:   []string{"account_id", "member_id"},
		Wrap:       "deleted",
		WrapErrors: true,
	},
	{
		Name:     "cloudflare_list_audit_logs",
		Summary:  "List recent audit logs for an account (who changed what).",
		Method:   http.MethodGet,
		Path:     "/accounts/{account_id}/audit_logs",
		Params:   map[string]any{"account_id": param("string"), "limit": paramDefault("integer", 20)},
		Required: []string{"account_id"},
		Run:      listAuditLogs,
	},
}
