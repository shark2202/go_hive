package cli

// Tool Library projection for GET /api/queen/{id}/tools.
//
// The route's catalog comes from the same in-process harness catalogue the
// queen's chat session is built from (session_tools.go): terminal-tools,
// files-tools, chart-tools and hive_tools. Upstream serves those groups from a
// bare registry when no queen session is live, so this projection is built once
// at boot and reused until a live session's registry supersedes it.
//
// Two presentation rules mirror the upstream Tool Library:
//
//   - the tool schema is the tool's contract surface, not the model-visible
//     one: CONTEXT_PARAMS (session_cwd) stay in the schema the API returns,
//     exactly as upstream's catalog entries do;
//   - a tool the upstream group lists but this build does not implement
//     (csv_sql: DuckDB is not ported) is still listed, with its schema and a
//     description that says it is unavailable and what to use instead. No
//     stub is registered for execution.

import (
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/skills"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// toolLibraryHarnessGroups is the upstream bare registry's harness set, in the
// order the Tool Library lists servers.
var toolLibraryHarnessGroups = []string{"chart-tools", "files-tools", "hive_tools", "terminal-tools"}

// toolLibraryHiveExcluded is dropped from the hive_tools group:
//   - read_file / edit_file are registered by files-tools first upstream, so
//     the later hive_tools registration is shadowed and they are not listed
//     twice;
//   - excel_* tools only exist upstream when openpyxl is installed, which the
//     frozen contract's hive_tools group does not include (they remain in the
//     integration registry and the spreadsheet_advanced category).
var toolLibraryHiveExcluded = map[string]struct{}{
	"read_file":        {},
	"edit_file":        {},
	"excel_read":       {},
	"excel_write":      {},
	"excel_append":     {},
	"excel_info":       {},
	"excel_sheet_list": {},
	"excel_search":     {},
}

// toolLibrarySessionCwdTools are the tools whose upstream Tool Library schema
// carries the session_cwd context parameter. The contract-parity manifest holds
// the model-visible schema (context params stripped); the API surface adds this
// one back, matching tools/src and the frozen reference response.
var toolLibrarySessionCwdTools = map[string]struct{}{
	"attach_file":        {},
	"edit_file":          {},
	"read_file":          {},
	"terminal_exec":      {},
	"terminal_glob":      {},
	"terminal_job_start": {},
	"terminal_pty_open":  {},
	"terminal_rg":        {},
}

// toolLibraryMissingPlaybookTools are standalone upstream lifecycle tools this
// build folds into run_playbook. They stay in the lifecycle list — the Tool
// Library contract has 16 entries — with a description that states they are not
// callable here and names the replacement.
var toolLibraryMissingPlaybookTools = map[string]string{
	"get_playbook_status": "Unavailable as a standalone tool in this build. Use run_playbook(action='status', run_id=...) — " +
		"it reports the run's live progress (dispatched, dead-lettered, phase, recent log lines) and its run-log row.",
	"stop_playbook": "Unavailable as a standalone tool in this build. Use run_playbook(action='stop', run_id=...) — " +
		"it cancels one run; rows already marked done stay done and re-running resumes the gap.",
	"list_playbook_runs": "Unavailable as a standalone tool in this build. run_playbook(action='status') lists the " +
		"colony's runs (live and recent history from colony.db).",
}

// newServeAppDeps wires the Tool Library projection into the HTTP app. Called
// once from the serve path; the loader file keeps its frozen line budget.
func newServeAppDeps(home hivehome.Home, deps hiveserver.AppDeps) hiveserver.AppDeps {
	// The live-tools diagnostic and the MCP admission gate both read this
	// process's credential snapshot; install it before any session exists.
	wireMCPGateCredSnapshot(home)
	library := NewQueenToolLibrary(home)
	deps.QueenToolCatalog = library.Catalog
	deps.QueenLifecycleTools = library.Lifecycle
	return deps
}

// QueenToolLibrary is the process-wide Tool Library snapshot. Its hooks match
// server.AppDeps.QueenToolCatalog / QueenLifecycleTools.
type QueenToolLibrary struct {
	catalog   map[string][]map[string]any
	lifecycle []map[string]any
}

// NewQueenToolLibrary builds the catalog from the real harness catalogue. The
// home is only used for the integrations' sandbox roots; nothing is executed
// while building.
func NewQueenToolLibrary(home hivehome.Home) *QueenToolLibrary {
	library := &QueenToolLibrary{
		catalog:   map[string][]map[string]any{},
		lifecycle: toolLibraryLifecycle(),
	}
	catalogue := newSessionToolCatalogue(home.Root, builtin.New(""), home)
	full := catalogue.registry.GetFullMCPCatalog()
	for _, group := range toolLibraryHarnessGroups {
		entries := full[group]
		projected := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			name, _ := entry["name"].(string)
			if name == "" {
				continue
			}
			if group == "hive_tools" {
				if _, skip := toolLibraryHiveExcluded[name]; skip {
					continue
				}
			}
			projected = append(projected, map[string]any{
				"name":         name,
				"description":  entry["description"],
				"input_schema": toolLibrarySchema(name, entry["input_schema"]),
				"provider":     nil,
			})
		}
		if group == "hive_tools" {
			projected = append(projected, toolLibraryCSVSQL())
		}
		library.catalog[group] = projected
	}
	return library
}

// Catalog projects the per-server MCP catalog for the route. A live session's
// registry is authoritative when it exposes one; otherwise the boot snapshot is
// served.
func (l *QueenToolLibrary) Catalog(queenID string, session *hiveserver.Session) map[string][]map[string]any {
	if session != nil && session.QueenToolRegistry != nil {
		if source, ok := session.QueenToolRegistry.(interface {
			GetFullMCPCatalog() map[string][]map[string]any
		}); ok {
			if catalog := source.GetFullMCPCatalog(); len(catalog) > 0 {
				return catalog
			}
		}
	}
	return l.catalog
}

// Lifecycle projects the queen lifecycle tools for the route.
func (l *QueenToolLibrary) Lifecycle(queenID string, session *hiveserver.Session) []map[string]any {
	return l.lifecycle
}

// toolLibrarySchema returns the contract schema for one tool: the frozen
// Python Tool Library schema where the parity manifest carries it (the eight
// tools with session_cwd get that context parameter added back), else the
// catalog's own schema. Numbers keep the manifest's JSON literals so a float
// default stays a float.
func toolLibrarySchema(name string, live any) map[string]any {
	schema, _ := live.(map[string]any)
	raw, ok := toolContractRawParameters(name)
	if !ok {
		return schema
	}
	projected := cloneToolSchema(raw)
	if _, needsCwd := toolLibrarySessionCwdTools[name]; needsCwd {
		properties, _ := projected["properties"].(map[string]any)
		if properties == nil {
			properties = map[string]any{}
			projected["properties"] = properties
		}
		properties["session_cwd"] = map[string]any{
			"anyOf":   []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}},
			"default": nil,
		}
	}
	return projected
}

// cloneToolSchema deep-copies a JSON schema so the served schema never aliases
// the embedded manifest.
func cloneToolSchema(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneToolSchemaValue(item)
		}
		return out
	default:
		return map[string]any{}
	}
}

func cloneToolSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneToolSchemaValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneToolSchemaValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

// toolLibraryCSVSQL is the catalog-only csv_sql entry. The upstream group lists
// it; this build has no DuckDB, so the row is advertised as unavailable with
// the replacement path instead of a fake implementation.
func toolLibraryCSVSQL() map[string]any {
	return map[string]any{
		"name": "csv_sql",
		"description": "Query a CSV file using SQL (DuckDB). NOT AVAILABLE IN THIS BUILD: DuckDB is not ported, " +
			"so this call is not supported and dispatches nothing. Read the file with csv_read and filter the rows instead.",
		"input_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"path", "query"},
		},
		"provider": nil,
	}
}

// toolLibraryLifecycle returns the 16 lifecycle entries the Tool Library
// contract carries: the real defs this build registers (ten session-bound
// controls, run_playbook / run_worker, write_skill) plus the three
// run_playbook-folded playbook tools, each marked unavailable.
func toolLibraryLifecycle() []map[string]any {
	defs := map[string]string{}
	for _, def := range hiveserver.QueenLifecycleToolDefs() {
		defs[def.Name] = def.Description
	}
	defs[colony.RunPlaybookToolName] = colony.RunPlaybookToolDef().Description
	defs[colony.RunWorkerToolName] = colony.RunWorkerToolDef().Description
	defs[skills.WriteSkillToolName] = skills.WriteSkillToolDef().Description
	for name, description := range toolLibraryMissingPlaybookTools {
		defs[name] = description
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		description := defs[name]
		if strings.TrimSpace(description) == "" {
			description = "Lifecycle tool " + name + "."
		}
		entries = append(entries, map[string]any{
			"name":        name,
			"description": description,
			"editable":    false,
		})
	}
	return entries
}
