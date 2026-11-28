package tools

import (
	"sort"
	"strings"
)

var ToolCategories = map[string][]string{
	"file_ops": {
		"pdf_read",
		"attach_file",
	},
	"code_editing": {"read_file", "edit_file"},
	"terminal_basic": {
		"terminal_exec",
		"terminal_rg",
		"terminal_glob",
		"terminal_output_get",
		"terminal_job_logs",
		"terminal_job_manage",
	},
	"terminal_advanced": {
		"terminal_job_start",
		"terminal_job_manage",
		"terminal_job_logs",
		"terminal_output_get",
		"terminal_pty_open",
		"terminal_pty_run",
		"terminal_pty_close",
	},
	"spreadsheet_advanced": {
		"csv_read",
		"csv_info",
		"csv_write",
		"csv_append",
		"csv_sql",
		"excel_read",
		"excel_info",
		"excel_write",
		"excel_append",
		"excel_search",
		"excel_sheet_list",
		"excel_sql",
	},
	"browser_basic":       {"browser_setup"},
	"browser_interaction": {"browser_setup"},
	"browser_core":        {"browser_setup"},
	"browser_extended":    {"browser_setup"},
	"terminal_core": {
		"terminal_exec",
		"terminal_output_get",
		"terminal_job_logs",
		"terminal_job_manage",
	},
	"terminal_extended": {
		"terminal_rg",
		"terminal_glob",
	},
	"files_core": {"attach_file"},
	"context_core": {
		"get_current_time",
		"get_account_info",
	},
	"web_core": {"web_scrape"},
	// Native HTTP family. It is not a member of web_core/research: the
	// reference manifest must stay chart_render + web_scrape. The tools are
	// loadable through the lazy tier, and CategoryAdditions[web_core] heals
	// saved sidecars that already cover web_scrape.
	"http_core": {
		"http_request",
		"http_download",
		"http_upload",
	},
	"research": {"web_scrape", "pdf_read"},
	"context_awareness": {
		"get_current_time",
		"get_account_info",
		"search_messages",
		"search_timeline",
	},
	"charts": {"@server:chart-tools"},
	"media":  {"image_generate"},
	"email_senders": {
		"list_senders",
		"setup_email_sender",
		"send_from_sender",
		"pick_sender",
		"send_campaign",
		"sender_history",
		"suppress_recipient",
		"list_suppressed",
		"adjust_sender",
	},
}

var categoryOrder = []string{
	"file_ops",
	"code_editing",
	"terminal_basic",
	"terminal_advanced",
	"spreadsheet_advanced",
	"browser_basic",
	"browser_interaction",
	"browser_core",
	"browser_extended",
	"terminal_core",
	"terminal_extended",
	"files_core",
	"context_core",
	"web_core",
	"http_core",
	"research",
	"context_awareness",
	"charts",
	"media",
	"email_senders",
}

var AlwaysEnabledCategories = map[string]struct{}{
	"file_ops":          {},
	"terminal_basic":    {},
	"context_awareness": {},
	"browser_core":      {},
}

var WorkerAlwaysEnabledCategories = map[string]struct{}{
	"browser_core":  {},
	"terminal_core": {},
	"files_core":    {},
	"context_core":  {},
	"web_core":      {},
}

func ListCategoryNames() []string {
	return append([]string(nil), categoryOrder...)
}

func ResolveCategoryTools(category string, mcpCatalog map[string][]map[string]any) []string {
	names := make([]string, 0)
	seen := map[string]struct{}{}
	for _, entry := range ToolCategories[category] {
		if strings.HasPrefix(entry, "@server:") {
			if mcpCatalog == nil {
				continue
			}
			serverName := strings.TrimPrefix(entry, "@server:")
			for _, tool := range mcpCatalog[serverName] {
				tname, _ := tool["name"].(string)
				if tname == "" {
					continue
				}
				if _, dup := seen[tname]; dup {
					continue
				}
				seen[tname] = struct{}{}
				names = append(names, tname)
			}
			continue
		}
		if _, dup := seen[entry]; dup {
			continue
		}
		seen[entry] = struct{}{}
		names = append(names, entry)
	}
	return names
}

func AlwaysEnabledToolNames(mcpCatalog map[string][]map[string]any) map[string]struct{} {
	return expandCategories(AlwaysEnabledCategories, mcpCatalog)
}

func WorkerAlwaysEnabledToolNames(mcpCatalog map[string][]map[string]any) map[string]struct{} {
	return expandCategories(WorkerAlwaysEnabledCategories, mcpCatalog)
}

func expandCategories(categories map[string]struct{}, mcpCatalog map[string][]map[string]any) map[string]struct{} {
	ordered := make([]string, 0, len(categories))
	for c := range categories {
		ordered = append(ordered, c)
	}
	sort.Strings(ordered)
	names := map[string]struct{}{}
	for _, category := range ordered {
		for _, name := range ResolveCategoryTools(category, mcpCatalog) {
			names[name] = struct{}{}
		}
	}
	return names
}
