// Package builtin implements the P0 built-in agent tools: read_file,
// write_file and terminal_exec.
//
// The package is deliberately free of server and agent-loop dependencies:
// callers publish Defs() to the model and route tool calls to Execute.
package builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Canonical tool names.
const (
	ToolReadFile     = "read_file"
	ToolWriteFile    = "write_file"
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
}

// New returns a Toolbox rooted at root. An empty root means the process
// working directory at call time.
func New(root string) *Toolbox {
	return &Toolbox{
		root:        strings.TrimSpace(root),
		outputLimit: MaxCommandOutputBytes,
		maxTimeout:  MaxCommandTimeout,
		maxReadSize: MaxReadFileBytes,
	}
}

var defaultToolbox = New("")

// Defs returns the tool definitions for read_file, write_file and
// terminal_exec, in a stable order.
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
	case ToolTerminalExec:
		return t.terminalExec(ctx, input)
	}
	return t.executeTerminalTool(ctx, name, input)
}

func defs() []llm.ToolDef {
	return append([]llm.ToolDef{
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
			Name: ToolTerminalExec,
			Description: "Run a shell command with /bin/bash -lc in the workspace root and capture " +
				"its output. Output is capped; the result reports the exit code and stderr. " +
				"Commands that exceed timeout_seconds are killed along with their child processes.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Shell command to run.",
					},
					"timeout_seconds": map[string]any{
						"type":        "number",
						"description": "Wall-clock timeout in seconds. Default 60, maximum 600.",
					},
				},
				"required": []string{"command"},
			},
		},
	}, terminalToolDefs()...)
}

// fail builds an expected tool failure that the model sees as content.
func fail(format string, args ...any) Result {
	return Result{Content: fmt.Sprintf(format, args...), IsError: true}
}
