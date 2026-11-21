package cloudflare

// Accounts face: account list/detail shaping and audit logs.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// shapeAccountDetails mirrors cloudflare_get_account_details.
func shapeAccountDetails(reply apiReply, _ map[string]any) any {
	account := asMap(reply.result)
	if account == nil {
		if items := sliceValue(reply.result); len(items) > 0 {
			account = asMap(items[0])
		}
	}
	if account == nil {
		account = map[string]any{}
	}
	return map[string]any{
		"id":         account["id"],
		"name":       textAt(account, "name"),
		"settings":   mapAt(account, "settings"),
		"created_on": textAt(account, "created_on"),
	}
}

// listAccounts mirrors cloudflare_list_accounts.
func listAccounts(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	page := intArg(input, "page", 1)
	perPage := intArg(input, "per_page", 20)
	if perPage > 50 {
		perPage = 50
	}
	query := url.Values{
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	reply := doRequest(ctx, rt, http.MethodGet, "/accounts", query, nil)
	if !reply.ok() {
		return reply.failure, nil
	}
	accounts := make([]map[string]any, 0)
	items := sliceValue(reply.result)
	if items == nil {
		items = sliceAt(asMap(reply.result), "accounts")
	}
	for _, raw := range items {
		account := asMap(raw)
		accounts = append(accounts, map[string]any{
			"id":     account["id"],
			"name":   textAt(account, "name"),
			"status": textAt(account, "status"),
		})
	}
	total := len(accounts)
	if info := asMap(reply.envelope["result_info"]); info != nil {
		total = intAt(info, "total_count", intAt(info, "count", total))
	}
	return map[string]any{
		"accounts": accounts, "total": total, "page": page, "per_page": perPage,
	}, nil
}

// listAuditLogs mirrors cloudflare_list_audit_logs.
func listAuditLogs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	limit := clampInt(intArg(input, "limit", 20), 0, 1<<31-1)
	query := url.Values{"per_page": {strconv.Itoa(clampInt(limit, 0, 50))}}
	reply := doRequest(
		ctx, rt, http.MethodGet,
		"/accounts/"+url.PathEscape(stringArg(input, "account_id"))+"/audit_logs", query, nil,
	)
	if !reply.ok() {
		return reply.failure, nil
	}
	logs := sliceValue(reply.result)
	if logs == nil {
		logs = sliceAt(asMap(reply.result), "result")
	}
	if limit < len(logs) {
		logs = logs[:limit]
	}
	return map[string]any{"audit_logs": logs}, nil
}
