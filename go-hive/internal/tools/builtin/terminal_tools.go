package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Canonical terminal tool names.
const (
	ToolTerminalRG        = "terminal_rg"
	ToolTerminalGlob      = "terminal_glob"
	ToolTerminalOutputGet = "terminal_output_get"
	ToolTerminalJobStart  = "terminal_job_start"
	ToolTerminalJobLogs   = "terminal_job_logs"
	ToolTerminalJobManage = "terminal_job_manage"
	ToolTerminalPTYOpen   = "terminal_pty_open"
	ToolTerminalPTYRun    = "terminal_pty_run"
	ToolTerminalPTYClose  = "terminal_pty_close"
)

// terminalToolHandler runs one terminal tool. Every handler reports expected
// failures as a JSON envelope, so the error return is reserved for
// infrastructure faults.
type terminalToolHandler func(t *Toolbox, ctx context.Context, input map[string]any) (Result, error)

// terminalToolHandlers is the single registration point for the terminal tool
// surface.
var terminalToolHandlers = map[string]terminalToolHandler{
	ToolTerminalRG:        (*Toolbox).terminalRG,
	ToolTerminalGlob:      (*Toolbox).terminalGlob,
	ToolTerminalOutputGet: (*Toolbox).terminalOutputGet,
	ToolTerminalJobStart:  (*Toolbox).terminalJobStart,
	ToolTerminalJobLogs:   (*Toolbox).terminalJobLogs,
	ToolTerminalJobManage: (*Toolbox).terminalJobManage,
	ToolTerminalPTYOpen:   (*Toolbox).terminalPTYOpen,
	ToolTerminalPTYRun:    (*Toolbox).terminalPTYRun,
	ToolTerminalPTYClose:  (*Toolbox).terminalPTYClose,
}

// executeTerminalTool dispatches a terminal tool, or reports the same
// unknown-tool error the builtin toolbox has always used.
func (t *Toolbox) executeTerminalTool(ctx context.Context, name string, input map[string]any) (Result, error) {
	if handler, ok := terminalToolHandlers[name]; ok {
		return handler(t, ctx, input)
	}
	return Result{}, fmt.Errorf("builtin: unknown tool %q", name)
}

// terminalToolDefs returns the terminal-tools harness group in the stable
// order the tool library expects: search, output, jobs, PTY.
//
// Schemas are the Python runtime's model-visible pydantic output, not a
// hand-designed Go dialect (see pySchema* below). Descriptions come from
// terminal_tool_descriptions.go, which is byte-exact contract data. session_cwd
// is a framework CONTEXT_PARAM: declared on the Python functions, stripped from
// the LLM-facing schema, injected at execution time — so it must never reach
// Defs().
func terminalToolDefs() []llm.ToolDef {
	defs := terminalSearchToolDefs()
	defs = append(defs, terminalJobToolDefs()...)
	return append(defs, terminalPTYToolDefs()...)
}

func terminalSearchToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name:        ToolTerminalRG,
			Description: TerminalRgDescription,
			Parameters: pySchemaObject(map[string]any{
				"pattern":        pySchemaString(),                    // Regex pattern.
				"path":           pyDefault(pySchemaString(), "."),    // Directory or file to search; default session workdir.
				"glob":           pyNullable(pySchemaString()),        // Filename glob, e.g. "*.py".
				"type_filter":    pyNullable(pySchemaString()),        // rg filetype shortcut, e.g. "py".
				"ignore_case":    pyDefault(pySchemaBoolean(), false), // Case-insensitive search.
				"context":        pyDefault(pySchemaInteger(), 0),     // Lines of context above and below each match.
				"max_count":      pyNullable(pySchemaInteger()),       // Stop after N matches per file.
				"max_depth":      pyNullable(pySchemaInteger()),       // Limit directory recursion depth.
				"hidden":         pyDefault(pySchemaBoolean(), false), // Include hidden files.
				"no_ignore":      pyDefault(pySchemaBoolean(), false), // Do not respect .gitignore.
				"extra_args":     pyNullable(pySchemaStringList()),    // Raw rg flags appended verbatim.
				"allow_fallback": pyDefault(pySchemaBoolean(), false), // Accept a Python-walk fallback when rg is missing.
				"session_cwd":    pyNullable(pySchemaString()),        // CONTEXT_PARAM, stripped before the model.
			}, "pattern"),
		},
		{
			Name:        ToolTerminalGlob,
			Description: TerminalGlobDescription,
			Parameters: pySchemaObject(map[string]any{
				"pattern":         pySchemaString(),                    // Filename glob or bare substring.
				"path":            pyDefault(pySchemaString(), "."),    // Directory to search under; default session workdir.
				"max_results":     pyDefault(pySchemaInteger(), 1000),  // Cap on returned paths; stops early at the cap.
				"include_ignored": pyDefault(pySchemaBoolean(), false), // Include .gitignored, hidden and build-cache files.
				"session_cwd":     pyNullable(pySchemaString()),        // CONTEXT_PARAM, stripped before the model.
			}, "pattern"),
		},
	}
}

func terminalJobToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name:        ToolTerminalOutputGet,
			Description: TerminalOutputGetDescription,
			Parameters: pySchemaObject(map[string]any{
				"output_handle": pySchemaString(),                 // Handle from a prior tool envelope.
				"since_offset":  pyDefault(pySchemaInteger(), 0),  // Pass 0 first, then next_offset.
				"max_kb":        pyDefault(pySchemaInteger(), 64), // Max KB per call.
			}, "output_handle"),
		},
		{
			Name:        ToolTerminalJobStart,
			Description: TerminalJobStartDescription,
			Parameters: pySchemaObject(map[string]any{
				"command":       pySchemaString(),                    // Command to run; shell=false splits on whitespace.
				"cwd":           pyNullable(pySchemaString()),        // Working directory; default session workdir.
				"env":           pyNullable(pySchemaStringMap()),     // Environment override merged into a sanitized base.
				"merge_stderr":  pyDefault(pySchemaBoolean(), false), // Interleave stderr into stdout in one buffer.
				"shell":         pyDefault(pySchemaBoolean(), false), // Run through the platform shell; zsh is refused.
				"name":          pyNullable(pySchemaString()),        // Human label for terminal_job_manage(list).
				"limits":        pyNullable(pySchemaIntMap()),        // setrlimit caps: cpu_sec, rss_mb, fsize_mb, nofile.
				"crm_principal": pyNullable(pySchemaString()),        // Acting CRM principal (HIVE_CRM_PRINCIPAL).
				"session_cwd":   pyNullable(pySchemaString()),        // CONTEXT_PARAM, stripped before the model.
			}, "command"),
		},
		{
			Name:        ToolTerminalJobLogs,
			Description: TerminalJobLogsDescription,
			Parameters: pySchemaObject(map[string]any{
				"job_id":           pySchemaString(),                                  // From terminal_job_start or an auto-promoted exec.
				"stream":           pyDefault(pySchemaString(), "stdout"),             // "stdout" | "stderr" | "merged".
				"since_offset":     pyDefault(pySchemaInteger(), 0),                   // Absolute byte offset; pass next_offset afterwards.
				"max_bytes":        pyDefault(pySchemaInteger(), 64000),               // Max decoded bytes returned inline.
				"wait_until_exit":  pyDefault(pySchemaBoolean(), false),               // Block until the job exits (or the wait times out).
				"wait_timeout_sec": pyDefault(pySchemaNumber(), json.Number("600.0")), // Longest wait in seconds (up to 600).
				"tail":             pyDefault(pySchemaBoolean(), false),               // Ignore since_offset; return the last max_bytes.
			}, "job_id"),
		},
		{
			Name:        ToolTerminalJobManage,
			Description: TerminalJobManageDescription,
			Parameters: pySchemaObject(map[string]any{
				"action": pySchemaString(),             // capabilities | list | signal_* | stdin | close_stdin.
				"job_id": pyNullable(pySchemaString()), // Required for signals and stdin actions.
				"data":   pyNullable(pySchemaString()), // Text to write for action=stdin.
			}, "action"),
		},
	}
}

func terminalPTYToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name:        ToolTerminalPTYOpen,
			Description: TerminalPtyOpenDescription,
			Parameters: pySchemaObject(map[string]any{
				"cwd":              pyNullable(pySchemaString()),       // Initial working directory; default session workdir.
				"env":              pyNullable(pySchemaStringMap()),    // Environment override merged into a sanitized base.
				"cols":             pyDefault(pySchemaInteger(), 120),  // Terminal columns.
				"rows":             pyDefault(pySchemaInteger(), 40),   // Terminal rows.
				"idle_timeout_sec": pyDefault(pySchemaInteger(), 1800), // Drop the session after this many idle seconds.
				"session_cwd":      pyNullable(pySchemaString()),       // CONTEXT_PARAM, stripped before the model.
			}),
		},
		{
			Name:        ToolTerminalPTYRun,
			Description: TerminalPtyRunDescription,
			Parameters: pySchemaObject(map[string]any{
				"session_id":  pySchemaString(),                                 // From terminal_pty_open.
				"command":     pyNullable(pySchemaString()),                     // Text to send; omitted when read_only.
				"expect":      pyNullable(pySchemaString()),                     // Regex to wait for instead of the prompt sentinel.
				"raw_send":    pyDefault(pySchemaBoolean(), false),              // Write without waiting for the prompt.
				"read_only":   pyDefault(pySchemaBoolean(), false),              // Send nothing; drain the buffer.
				"timeout_sec": pyDefault(pySchemaNumber(), json.Number("60.0")), // Max wait in seconds.
			}, "session_id"),
		},
		{
			Name:        ToolTerminalPTYClose,
			Description: TerminalPtyCloseDescription,
			Parameters: pySchemaObject(map[string]any{
				"session_id": pySchemaString(),                    // From terminal_pty_open.
				"force":      pyDefault(pySchemaBoolean(), false), // Skip graceful exit and SIGTERM/SIGKILL the shell.
			}, "session_id"),
		},
	}
}

// The pySchema* builders produce the JSON Schema shape the Python runtime
// advertises for these tools: pydantic's output. Concretely:
//
//   - no per-property description (pydantic field descriptions are absent in
//     the upstream terminal tools; the semantics live in the tool docstring's
//     Args section, which stays model-visible via Description);
//   - a nullable argument is anyOf: [<non-null schema>, {"type": "null"}]
//     with "default": null, not a single node with type ["X","null"];
//   - every argument with a declared default carries it, so a model sees the
//     same optionality upstream does.
//
// The Go source keeps one trailing comment per property as the internal
// documentation, so nothing is lost by dropping it from the wire.
func pySchemaObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func pySchemaString() map[string]any  { return map[string]any{"type": "string"} }
func pySchemaInteger() map[string]any { return map[string]any{"type": "integer"} }
func pySchemaNumber() map[string]any  { return map[string]any{"type": "number"} }
func pySchemaBoolean() map[string]any { return map[string]any{"type": "boolean"} }

func pySchemaStringMap() map[string]any {
	return map[string]any{"additionalProperties": map[string]any{"type": "string"}, "type": "object"}
}

func pySchemaIntMap() map[string]any {
	return map[string]any{"additionalProperties": map[string]any{"type": "integer"}, "type": "object"}
}

func pySchemaStringList() map[string]any {
	return map[string]any{"items": map[string]any{"type": "string"}, "type": "array"}
}

// pyNullable wraps a non-null schema the way pydantic emits an Optional.
func pyNullable(inner map[string]any) map[string]any {
	return map[string]any{
		"anyOf":   []any{inner, map[string]any{"type": "null"}},
		"default": nil,
	}
}

// pyDefault attaches a declared default to a non-null schema.
func pyDefault(schema map[string]any, value any) map[string]any {
	schema["default"] = value
	return schema
}
