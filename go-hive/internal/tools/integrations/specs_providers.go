package integrations

import "net/http"

func githubSpecs() map[string]RestSpec {
	// GitHub list endpoints advertise the follow-up page as a Link header
	// (rel="next"). The walk is wired only where the upstream tool exposes a
	// page parameter (issues / pull requests / stargazers), i.e. where the
	// model could reach the same pages through repeated upstream calls.
	linkPagination := func() *PaginationSpec { return &PaginationSpec{LinkHeader: true} }
	return map[string]RestSpec{
		"github_get_repo":            {Method: http.MethodGet, Path: "/repos/{owner}/{repo}"},
		"github_search_repos":        {Method: http.MethodGet, Path: "/search/repositories", Rename: map[string]string{"query": "q", "limit": "per_page"}},
		"github_list_issues":         {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/issues", Rename: map[string]string{"limit": "per_page"}, Pagination: linkPagination()},
		"github_get_issue":           {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/issues/{issue_number}"},
		"github_create_issue":        {Method: http.MethodPost, Path: "/repos/{owner}/{repo}/issues"},
		"github_update_issue":        {Method: http.MethodPatch, Path: "/repos/{owner}/{repo}/issues/{issue_number}"},
		"github_list_pull_requests":  {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/pulls", Rename: map[string]string{"limit": "per_page"}, Pagination: linkPagination()},
		"github_get_pull_request":    {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/pulls/{pull_number}"},
		"github_create_pull_request": {Method: http.MethodPost, Path: "/repos/{owner}/{repo}/pulls"},
		"github_search_code":         {Method: http.MethodGet, Path: "/search/code", Rename: map[string]string{"query": "q", "limit": "per_page"}},
		"github_list_branches":       {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/branches", Rename: map[string]string{"limit": "per_page"}},
		"github_get_branch":          {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/branches/{branch}"},
		"github_list_stargazers":     {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/stargazers", Rename: map[string]string{"limit": "per_page"}, Pagination: linkPagination()},
		"github_get_user_profile":    {Method: http.MethodGet, Path: "/users/{username}"},
		"github_list_commits":        {Method: http.MethodGet, Path: "/repos/{owner}/{repo}/commits", Rename: map[string]string{"limit": "per_page"}},
		"github_create_release":      {Method: http.MethodPost, Path: "/repos/{owner}/{repo}/releases"},
	}
}

func telegramSpecs() map[string]RestSpec {
	routes := map[string]string{
		"telegram_send_message":          "sendMessage",
		"telegram_send_document":         "sendDocument",
		"telegram_edit_message":          "editMessageText",
		"telegram_delete_message":        "deleteMessage",
		"telegram_forward_message":       "forwardMessage",
		"telegram_send_photo":            "sendPhoto",
		"telegram_send_chat_action":      "sendChatAction",
		"telegram_get_chat":              "getChat",
		"telegram_pin_message":           "pinChatMessage",
		"telegram_unpin_message":         "unpinChatMessage",
		"telegram_get_chat_member_count": "getChatMemberCount",
		"telegram_send_video":            "sendVideo",
		"telegram_set_chat_description":  "setChatDescription",
	}
	specs := make(map[string]RestSpec, len(routes))
	for action, method := range routes {
		specs[action] = RestSpec{
			Method: http.MethodPost,
			Path:   "/" + method,
			Unwrap: "result",
		}
	}
	return specs
}

func trelloSpecs() map[string]RestSpec {
	return map[string]RestSpec{
		"trello_list_boards": {Method: http.MethodGet, Path: "/members/{member_id}/boards"},
		"trello_get_member":  {Method: http.MethodGet, Path: "/members/{member_id}"},
		"trello_list_lists":  {Method: http.MethodGet, Path: "/boards/{board_id}/lists"},
		"trello_list_cards":  {Method: http.MethodGet, Path: "/lists/{list_id}/cards"},
		"trello_create_card": {
			Method: http.MethodPost, Path: "/cards",
			Rename: map[string]string{"list_id": "idList", "id_members": "idMembers", "id_labels": "idLabels"},
		},
		"trello_move_card": {
			Method: http.MethodPut, Path: "/cards/{card_id}",
			Rename: map[string]string{"list_id": "idList"},
		},
		"trello_update_card": {
			Method: http.MethodPut, Path: "/cards/{card_id}",
			Rename: map[string]string{"list_id": "idList"},
		},
		"trello_add_comment": {
			Method: http.MethodPost, Path: "/cards/{card_id}/actions/comments",
			Rename: map[string]string{"text": "text"},
		},
		"trello_add_attachment": {
			Method: http.MethodPost, Path: "/cards/{card_id}/attachments",
			Rename: map[string]string{"attachment_url": "url"},
		},
		"trello_get_card":    {Method: http.MethodGet, Path: "/cards/{card_id}"},
		"trello_create_list": {Method: http.MethodPost, Path: "/lists", Rename: map[string]string{"board_id": "idBoard"}},
		"trello_search_cards": {
			Method: http.MethodGet, Path: "/search",
			Rename: map[string]string{"query": "query", "board_id": "idBoards", "limit": "cards_limit"},
		},
	}
}

func airtableSpecs() map[string]RestSpec {
	return map[string]RestSpec{
		// The Airtable API returns at most 100 records per call and reports a
		// follow-up `offset`; the declarative walk makes list_records return
		// the merged set (upstream surfaces has_more/offset for the model to
		// continue; the Go client follows it directly).
		"airtable_list_records": {
			Method: http.MethodGet, Path: "/{base_id}/{table_name}",
			Rename: map[string]string{
				"filter_formula": "filterByFormula",
				"sort_field":     "sort[0][field]",
				"sort_direction": "sort[0][direction]",
				"max_records":    "maxRecords",
				"fields":         "fields[]",
			},
			Pagination: &PaginationSpec{
				ItemsPath:      "records",
				NextCursorPath: "offset",
				CursorParam:    "offset",
			},
		},
		"airtable_get_record":      {Method: http.MethodGet, Path: "/{base_id}/{table_name}/{record_id}"},
		"airtable_create_records":  {Method: http.MethodPost, Path: "/{base_id}/{table_name}"},
		"airtable_update_records":  {Method: http.MethodPatch, Path: "/{base_id}/{table_name}"},
		"airtable_list_bases":      {Method: http.MethodGet, Path: "/meta/bases"},
		"airtable_get_base_schema": {Method: http.MethodGet, Path: "/meta/bases/{base_id}/tables"},
		"airtable_delete_records": {
			Method: http.MethodDelete, Path: "/{base_id}/{table_name}",
			Rename: map[string]string{"record_ids": "records[]"},
		},
		"airtable_search_records": {
			Method: http.MethodGet, Path: "/{base_id}/{table_name}",
			Rename: map[string]string{"max_records": "maxRecords"},
			Build: func(input map[string]any) map[string]any {
				field := stringArg(input, "field_name")
				value := stringArg(input, "search_value")
				if field == "" || value == "" {
					return nil
				}
				return map[string]any{
					"filterByFormula": "{`" + field + "`}='" + value + "'",
				}
			},
		},
		"airtable_list_collaborators": {Method: http.MethodGet, Path: "/meta/bases/{base_id}/collaborators"},
	}
}
