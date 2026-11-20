// Package builtin implements the in-process agent tools: read_file,
// write_file, edit_file, search_files, terminal_exec and the terminal tool
// family.
//
// The package is deliberately free of server and agent-loop dependencies:
// callers publish Defs() to the model and route tool calls to Execute.
package builtin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

// Canonical tool names.
const (
	ToolReadFile     = "read_file"
	ToolWriteFile    = "write_file"
	ToolEditFile     = "edit_file"
	ToolSearchFiles  = "search_files"
	ToolTerminalExec = "terminal_exec"
)

// Limits shared by the tools. They mirror the Python file/terminal tools
// closely enough for P0 while keeping output bounded in context.
const (
	// DefaultReadLimit is the number of lines returned when limit is 0.
	DefaultReadLimit = 2000
	// MaxReadLineLength is the maximum number of runes kept per line.
	MaxReadLineLength = 2000
	// MaxReadOutputBytes is the byte budget for one read_file result.
	MaxReadOutputBytes = 50 * 1024
	// MaxReadFileBytes is the largest file read_file will open.
	MaxReadFileBytes = 10 << 20
	// MaxCommandOutputBytes caps each captured stream of terminal_exec.
	MaxCommandOutputBytes = 256 * 1024
	// DefaultCommandTimeout applies when timeout_seconds is omitted.
	DefaultCommandTimeout = 60 * time.Second
	// MaxCommandTimeout is the largest accepted timeout_seconds.
	MaxCommandTimeout = 600 * time.Second
)

// Result is the outcome of one tool call. Content is shown to the model;
// IsError marks expected tool-level failures (missing file, non-zero exit,
// path escape) that the model should see and recover from. Infrastructure
// failures (unknown tool, cancelled context, unkillable process) are
// returned as the error value instead.
type Result struct {
	Content string
	IsError bool
}

// Toolbox executes the built-in tools inside a sandbox root. The zero value
// is usable and uses the process working directory as the root.
type Toolbox struct {
	root        string
	outputLimit int
	maxTimeout  time.Duration
	maxReadSize int

	// stateMu guards the lazily created per-Toolbox read-state cache and the
	// optional write_safe_root ceiling. One Toolbox is one session, so the
	// cache is the session's view of the files the model has read.
	stateMu        sync.Mutex
	fileState      *fileStateCache
	safeWriteRoots []string
}

// New returns a Toolbox rooted at root. An empty root means the process
// working directory at call time.
func New(root string) *Toolbox {
	return &Toolbox{
		root:        strings.TrimSpace(root),
		outputLimit: MaxCommandOutputBytes,
		maxTimeout:  MaxCommandTimeout,
		maxReadSize: MaxReadFileBytes,
		fileState:   newFileStateCache(),
	}
}

// readState returns the Toolbox's read-state cache, creating it on first use so
// the zero value stays usable.
func (t *Toolbox) readState() *fileStateCache {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if t.fileState == nil {
		t.fileState = newFileStateCache()
	}
	return t.fileState
}

var defaultToolbox = New("")

// Defs returns the model-visible tool definitions for read_file, write_file,
// edit_file, search_files, terminal_exec and the terminal tool family, in a
// stable order. Framework context params (session_cwd, ...) are stripped here;
// the host injects their values at execution time.
func Defs() []llm.ToolDef { return defs() }

// Defs returns the same definitions regardless of the root.
func (t *Toolbox) Defs() []llm.ToolDef { return defs() }

// Execute runs the named tool against the process working directory.
func Execute(ctx context.Context, name string, input map[string]any) (Result, error) {
	return defaultToolbox.Execute(ctx, name, input)
}

// Execute runs the named tool inside the toolbox root.
func (t *Toolbox) Execute(ctx context.Context, name string, input map[string]any) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil {
		t = defaultToolbox
	}
	switch name {
	case ToolReadFile:
		return t.readFile(ctx, input)
	case ToolWriteFile:
		return t.writeFile(ctx, input)
	case ToolEditFile:
		return t.editFile(ctx, input)
	case ToolSearchFiles:
		return t.searchFiles(ctx, input)
	case ToolTerminalExec:
		return t.terminalExec(ctx, input)
	}
	return t.executeTerminalTool(ctx, name, input)
}

func defs() []llm.ToolDef {
	return tools.StripContextParams(rawDefs())
}

// fileToolDefs returns the four in-process file tools.
func fileToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{
			Name: ToolReadFile,
			Description: "Read a UTF-8 text file from the workspace and return it as numbered lines. " +
				"Relative paths are resolved against the workspace root; absolute paths must stay " +
				"inside it. Use offset and limit to page through long files.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to read.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Starting line number, 1-indexed. Default 1.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum lines to return. 0 means up to 2000. Default 0.",
					},
					"hashline": map[string]any{
						"type": "boolean",
						"description": "If true, return lines in N:hhhh|content format with content-hash " +
							"anchors. Line truncation is disabled in this mode to preserve hash integrity. Default false.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name: ToolWriteFile,
			Description: "Create or overwrite a UTF-8 text file inside the workspace. Parent " +
				"directories are created automatically and the write is atomic. Paths outside " +
				"the workspace root are refused.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path to write.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Complete file content. An empty string creates an empty file.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name: ToolEditFile,
			Description: "Edit files: one string in one file (replace mode), or many edits " +
				"across many files (patch mode). Use this instead of `sed`, `awk`, or " +
				"shell redirects. Returns a unified diff. If old_string doesn't match " +
				"in replace mode, re-read the file with read_file or use search_files " +
				"to locate the exact text — don't retry blindly. Paths outside the " +
				"workspace root are refused, and a failed patch writes nothing.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"mode": map[string]any{
						"type":        "string",
						"description": "Edit mode. 'replace' (default) for single-file find-and-replace. 'patch' for multi-file structured patches that can Update/Add/Delete/Move files atomically.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Replace mode only. File path to edit, relative to the workspace root (or absolute inside it). Ignored in patch mode (paths live inside patch_text).",
					},
					"old_string": map[string]any{
						"type": "string",
						"description": "Replace mode only. Text to find. Must be unique in the file " +
							"unless replace_all=True; include surrounding context to " +
							"disambiguate. Fuzzy matching tolerates whitespace/indent drift, " +
							"tabs vs spaces, smart quotes vs ASCII, and literal backslash-n/" +
							"backslash-t/backslash-r sequences vs real control chars.",
					},
					"new_string": map[string]any{
						"type":        "string",
						"description": "Replace mode only. Replacement text. Pass an empty string to delete the matched text.",
					},
					"replace_all": map[string]any{
						"type":        "boolean",
						"description": "Replace mode only. Replace every occurrence instead of requiring a unique match. Default false.",
					},
					"patch_text": map[string]any{
						"type": "string",
						"description": "Patch mode only. Structured patch body. File paths are embedded " +
							"inside the body via '*** Update File: <path>' / " +
							"'*** Add File: <path>' / '*** Delete File: <path>' / " +
							"'*** Move File: <src> -> <dst>' markers, so one call can touch " +
							"many files. Hunks use unified-diff syntax: lines starting with " +
							"' ' (space) are context, '-' lines are removed, '+' lines are " +
							"added. Separate hunks with '@@', or '@@ hint @@' to narrow fuzzy " +
							"matching to a window around the hint. If any operation fails " +
							"validation, no files are written. Example:\n" +
							"*** Begin Patch\n" +
							"*** Update File: a.py\n" +
							"@@ def hello @@\n" +
							" def hello():\n" +
							"-    return 1\n" +
							"+    return 42\n" +
							"*** Add File: new.py\n" +
							"+x = 1\n" +
							"*** End Patch",
					},
				},
			},
		},
		{
			Name: ToolSearchFiles,
			Description: "Search file contents OR find files by name. Use this instead of " +
				"grep, find, or ls. target='content' (default) regex-greps inside " +
				"files; target='files' globs file names (mtime-sorted, newest first). " +
				"Pagination via limit/offset; truncated responses include a hint with " +
				"the next offset. Paths are resolved inside the workspace root. " +
				"Repeating the same exact query consecutively is warned at 3 calls " +
				"and blocked at 4 — use the results you already have.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "Regex (content mode) or glob (files mode, e.g. '*.py'). For an 'ls'-style listing pass '*' or '*.<ext>'.",
					},
					"target": map[string]any{
						"type":        "string",
						"description": "'content' to grep inside files, 'files' to list/find files. Legacy aliases: 'grep' -> 'content', 'find'/'ls' -> 'files'. Default 'content'.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Directory (or, in content mode, a single file) to search, relative to the workspace root. Default '.'.",
					},
					"file_glob": map[string]any{
						"type":        "string",
						"description": "Restrict content search to filenames matching this glob. Ignored in files mode (use the 'pattern' argument instead).",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Max results to return. Default 50.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "Skip first N results for pagination. Default 0.",
					},
					"output_mode": map[string]any{
						"type":        "string",
						"description": "Content-mode output shape: 'content' (lines + line numbers, default), 'files_only' (paths only), 'count' (per-file match counts).",
					},
					"context": map[string]any{
						"type":        "integer",
						"description": "Lines of context before and after each match (content mode only). Default 0.",
					},
					"hashline": map[string]any{
						"type":        "boolean",
						"description": "Content mode: include N:hhhh hash anchors in matched lines. Default false.",
					},
					"task_id": map[string]any{
						"type":        "string",
						"description": "Optional anti-loop scope key. Defaults to a shared bucket; pass a per-task id when multiple agents share a process.",
					},
				},
				"required": []string{"pattern"},
			},
		},
	}
}

// terminalExecToolDef is terminal_exec's definition. session_cwd is declared
// here so the host knows the tool accepts it; defs() strips it before the
// model ever sees the schema.
func terminalExecToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: ToolTerminalExec,
		Description: "Run a shell command and capture its output." + "\n" +
			"" + "\n" +
			"Past auto_background_after_sec, the call auto-promotes to a background" + "\n" +
			"job and returns immediately with `auto_backgrounded=True, job_id=...`" + "\n" +
			"— poll with terminal_job_logs(job_id, since_offset=...) to read the rest." + "\n" +
			"Set auto_background_after_sec=0 to force pure foreground (kill on" + "\n" +
			"timeout_sec)." + "\n" +
			"" + "\n" +
			"The agent loop may first return a bg_* handle; use collect_result for" + "\n" +
			"that handle, then terminal_job_logs for any job_id in the result." + "\n" +
			"POSIX uses bash when shell=True; Windows selects Git Bash, PowerShell," + "\n" +
			"then cmd. Read shell_kind before choosing platform-specific syntax." + "\n" +
			"" + "\n" +
			"Args:" + "\n" +
			"    command: The command. With shell=False POSIX uses shlex splitting;" + "\n" +
			"        shell syntax is detected automatically. Use shell=True for scripts." + "\n" +
			"    cwd: Working directory. Defaults to the session workdir when" + "\n" +
			"        omitted; pass an absolute path to override (loose default," + "\n" +
			"        not a sandbox — you can point anywhere)." + "\n" +
			"    env: Environment override (merged into a sanitized base — zsh" + "\n" +
			"        dotfile vars are stripped)." + "\n" +
			"    timeout_sec: Total deadline from command start, including background" + "\n" +
			"        execution. Expiry terminates the process tree and reports" + "\n" +
			"        timed_out=True. 0 means unlimited, requiring auto-promotion." + "\n" +
			"    auto_background_after_sec: Inline budget. Past this, promote to" + "\n" +
			"        a background job and return. 0 disables auto-promotion. The" + "\n" +
			"        foreground wait must be finite and at most 220 seconds." + "\n" +
			"    shell: True to use the platform shell. zsh refused." + "\n" +
			"    stdin: Optional stdin payload (string)." + "\n" +
			"    limits: Optional setrlimit caps. Keys: cpu_sec, rss_mb," + "\n" +
			"        fsize_mb, nofile." + "\n" +
			"    max_output_kb: Inline output cap. Overflow stashes to an" + "\n" +
			"        output_handle for retrieval via terminal_output_get." + "\n" +
			"" + "\n" +
			"Returns the standard envelope: see `terminal-tools-foundations` skill.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Shell command to run.",
				},
				"cwd": map[string]any{
					"type":        []string{"string", "null"},
					"description": "Working directory. Defaults to the session workdir when omitted.",
					"default":     nil,
				},
				"env": map[string]any{
					"type":                 []string{"object", "null"},
					"additionalProperties": map[string]any{"type": "string"},
					"description":          "Environment override merged into a sanitized base.",
					"default":              nil,
				},
				"timeout_sec": map[string]any{
					"type":        "integer",
					"description": "Total deadline in seconds from command start; 0 means unlimited (requires auto-promotion).",
					"default":     60,
				},
				"auto_background_after_sec": map[string]any{
					"type":        "integer",
					"description": "Inline budget in seconds; past it the call promotes to a background job. 0 disables promotion.",
					"default":     30,
				},
				"shell": map[string]any{
					"type":        "boolean",
					"description": "Run through the platform shell instead of direct exec.",
					"default":     false,
				},
				"stdin": map[string]any{
					"type":        []string{"string", "null"},
					"description": "Optional stdin payload.",
					"default":     nil,
				},
				"limits": map[string]any{
					"type":                 []string{"object", "null"},
					"additionalProperties": map[string]any{"type": "integer"},
					"description":          "Optional resource caps (cpu_sec, rss_mb, fsize_mb, nofile).",
					"default":              nil,
				},
				"max_output_kb": map[string]any{
					"type":        "integer",
					"description": "Inline output cap in KB; overflow stashes the full output under output_handle.",
					"default":     256,
				},
				// Framework-injected: declared so the host knows this tool
				// accepts it, then stripped by tools.StripContextParams.
				"session_cwd": map[string]any{
					"type":        []string{"string", "null"},
					"description": "Session workdir injected by the host.",
					"default":     nil,
				},
				"crm_principal": map[string]any{
					"type":        []string{"string", "null"},
					"description": "Acting CRM principal forwarded as HIVE_CRM_PRINCIPAL.",
					"default":     nil,
				},
			},
			"required": []string{"command"},
		},
	}
}

// rawDefs returns the definitions as the implementations declare them,
// including the framework context params (CONTEXT_PARAMS) that the host
// injects at call time. defs() strips those before the definitions reach the
// model; tests use rawDefs to prove the declarations still exist.
func rawDefs() []llm.ToolDef {
	defs := fileToolDefs()
	defs = append(defs, terminalExecToolDef())
	return append(defs, terminalToolDefs()...)
}

// fail builds an expected tool failure that the model sees as content.
func fail(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
