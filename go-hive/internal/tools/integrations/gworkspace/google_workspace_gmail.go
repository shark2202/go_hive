// Gmail catalog entry for the batch-5 lane-3 Google Workspace registry
// (split from google_workspace.go).

package gworkspace

var gmailTool = &Tool{
	ID:          gmailModule,
	Name:        "gmail",
	DisplayName: "Gmail",
	Summary:     "Gmail integration (10 actions).",
	Actions: []Action{
		{
			Name:    "gmail_list_messages",
			Summary: "List Gmail messages matching a search query.",
			Source:  "gmail_tool/gmail_tool.py",
			Params: map[string]any{
				"query":       paramDefault("string", "is:unread"),
				"max_results": paramDefault("integer", 100),
				"page_token":  param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_get_message",
			Summary:  "Get a Gmail message by ID.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
				"format":     paramDefault("string", "metadata"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_trash_message",
			Summary:  "Move a Gmail message to trash.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_modify_message",
			Summary:  "Modify labels on a Gmail message.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id":    param("string"),
				"add_labels":    param("string"),
				"remove_labels": param("string"),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_batch_modify_messages",
			Summary:  "Modify labels on multiple Gmail messages at once.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_ids"},
			Params: map[string]any{
				"message_ids":   param("string"),
				"add_labels":    param("string"),
				"remove_labels": param("string"),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_batch_get_messages",
			Summary:  "Fetch multiple Gmail messages by ID in a single call.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_ids"},
			Params: map[string]any{
				"message_ids": param("array"),
				"format":      paramDefault("string", "metadata"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_create_draft",
			Summary:  "Create a draft email in the user's Gmail Drafts folder.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"html"},
			Params: map[string]any{
				"html":                param("string"),
				"to":                  paramDefault("string", ""),
				"subject":             paramDefault("string", ""),
				"account":             paramDefault("string", ""),
				"reply_to_message_id": paramDefault("string", ""),
				"signature":           paramDefault("string", ""),
			},
		},
		{
			Name:    "gmail_get_signature",
			Summary: "Get the user's Gmail signature from their account settings.",
			Source:  "gmail_tool/gmail_tool.py",
			Params: map[string]any{
				"send_as_email": paramDefault("string", ""),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:    "gmail_list_labels",
			Summary: "List all Gmail labels for the user's account.",
			Source:  "gmail_tool/gmail_tool.py",
			Params: map[string]any{
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_create_label",
			Summary:  "Create a new Gmail label.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"name"},
			Params: map[string]any{
				"name":                    param("string"),
				"label_list_visibility":   paramDefault("string", "labelShow"),
				"message_list_visibility": paramDefault("string", "show"),
				"account":                 paramDefault("string", ""),
			},
		},
	},
}
