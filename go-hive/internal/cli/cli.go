package cli

// Parity: framework/cli.py::main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-isatty"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/llmjudge"
	"github.com/aden-hive/hive-go/internal/mcpregistry"
	"github.com/aden-hive/hive-go/internal/session"
	"github.com/aden-hive/hive-go/internal/skills"
	"github.com/aden-hive/hive-go/internal/version"
)

type registryClient interface {
	FetchIndex(forceRefresh bool) map[string]any
	Search(query string) []map[string]any
	GetSkillEntry(name string) map[string]any
	GetPack(packName string) []string
}

type completionProvider interface {
	Complete(ctx context.Context, messages []map[string]any, opts llm.CompleteOptions) (llm.Completion, error)
}

type judgeEvaluator interface {
	Evaluate(constraint, sourceDocument, summary, criteria string) map[string]any
}

type trustStore interface {
	Trust(repoKey, projectPath string)
}

type commandOutput struct {
	Stdout string
	Stderr string
}

type commandExitError struct {
	Command    []string
	ReturnCode int
	Stdout     string
	Stderr     string
}

func (e *commandExitError) Error() string {
	return fmt.Sprintf("command %s exited %d", strings.Join(e.Command, " "), e.ReturnCode)
}

type commandNotFound struct{ Command string }

func (e *commandNotFound) Error() string { return e.Command + ": executable file not found in $PATH" }

var errGitRemoteTimeout = errors.New("git remote lookup timed out")

type Runner struct {
	Stdout     io.Writer
	Stderr     io.Writer
	Stdin      io.Reader
	StdinIsTTY bool
	WorkDir    string
	Version    string
	GOOS       string

	HomeRoot            func() (string, error)
	ShowInstallNotice   func()
	InstallFromGit      func(gitURL, skillName, subdirectory, version, targetDir string) (string, error)
	InstallFromRegistry func(entry map[string]any, targetDir, version string) (string, error)
	RemoveSkill         func(name, skillsDir string) (bool, error)
	ForkSkill           func(source *skills.ParsedSkill, newName, targetDir string) (string, error)
	Discover            func(projectDir string) []*skills.ParsedSkill
	NewRegistry         func() registryClient
	NewProvider         func(model string) (completionProvider, error)
	NewJudge            func(provider completionProvider) judgeEvaluator
	NewTrustStore       func() trustStore
	LookPath            func(name string) (string, error)
	IsExecutable        func(path string) bool
	Readable            func(path string) bool
	RunGitRemote        func(projectPath string) (string, error)
	RunCommand          func(dir string, command []string) (commandOutput, error)
	StartPing           func(source string)
	Serve               func(args *parsedArgs) int
	HTTPGet             func(url string, timeout time.Duration) (map[string]any, error)
	HTTPPost            func(url string, body map[string]any, timeout time.Duration) (map[string]any, error)
	HTTPDelete          func(url string, timeout time.Duration) (map[string]any, error)
	ColdSessions        func() []map[string]any
	OpenBrowser         func(url string)
	MCPMain             func(argv []string) int
	DebuggerScript      func() string
	DebuggerExec        func(command []string) int
	DebuggerPython      string
}

func NewRunner() *Runner {
	r := &Runner{
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
		StdinIsTTY: stdinIsTTY(),
		WorkDir:    "",
		Version:    version.Version,
		GOOS:       "",
	}
	r.HomeRoot = func() (string, error) {
		home, err := hivehome.Resolve()
		if err != nil {
			return "", err
		}
		return home.Root, nil
	}
	r.ShowInstallNotice = skills.MaybeShowInstallNotice
	r.InstallFromGit = skills.InstallFromGit
	r.InstallFromRegistry = skills.InstallFromRegistry
	r.RemoveSkill = skills.RemoveInstalledSkill
	r.ForkSkill = skills.ForkSkill
	r.Discover = defaultDiscover
	r.NewRegistry = func() registryClient { return skills.NewRegistryClient("", "") }
	r.NewProvider = func(model string) (completionProvider, error) {
		return llm.NewAnthropicProvider("", model)
	}
	r.NewJudge = func(provider completionProvider) judgeEvaluator { return llmjudge.New(provider) }
	r.NewTrustStore = func() trustStore { return skills.NewTrustedRepoStore("") }
	r.LookPath = exec.LookPath
	r.IsExecutable = defaultIsExecutable
	r.Readable = defaultReadable
	r.RunGitRemote = defaultGitRemote
	r.RunCommand = defaultRunCommand
	r.StartPing = func(source string) {
		go defaultPing(source)
	}
	r.Serve = r.defaultServe
	r.HTTPGet = httpGet
	r.HTTPPost = httpPost
	r.HTTPDelete = httpDelete
	r.ColdSessions = func() []map[string]any {
		home, err := hivehome.Resolve()
		if err != nil {
			return []map[string]any{}
		}
		return session.ListColdSessions(home.QueensDir())
	}
	r.OpenBrowser = r.openBrowser
	r.MCPMain = func(argv []string) int {
		// The mcp verbs are parsed by their own argparse-shaped engine; it
		// needs the hive root page so a deferred "unrecognized arguments"
		// failure prints the root usage, exactly as upstream's top-level
		// parse_args does.
		root := r.rootCommand()
		mcpCLI := &mcpregistry.CLI{
			Out: r.Stdout, Err: r.Stderr, In: r.Stdin,
			RootProg:  "hive",
			RootUsage: func() string { return root.helpSpec().UsageText() },
		}
		return mcpCLI.Main(argv)
	}
	r.DebuggerScript = defaultDebuggerScript
	r.DebuggerExec = r.defaultDebuggerExec
	r.DebuggerPython = "python3"
	return r
}

func stdinIsTTY() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

func defaultDiscover(projectDir string) []*skills.ParsedSkill {
	hiveHome := ""
	if home, err := hivehome.Resolve(); err == nil {
		hiveHome = home.Root
	}
	config := skills.DiscoveryConfig{ProjectRoot: projectDir, HiveHome: hiveHome}
	return skills.NewSkillDiscovery(config).Discover()
}

func defaultIsExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if runtimeGOOS() == "windows" {
		return !info.IsDir()
	}
	return !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

func defaultReadable(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = file.Close()
	return true
}

func runtimeGOOS() string {
	return goos
}

func defaultGitRemote(projectPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "-C", projectPath, "remote", "get-url", "origin")
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", errGitRemoteTimeout
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &commandExitError{
				Command:    []string{"git", "-C", projectPath, "remote", "get-url", "origin"},
				ReturnCode: exitErr.ExitCode(), Stdout: stdout.String(), Stderr: stderr.String(),
			}
		}
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return "", &commandNotFound{Command: "git"}
		}
		return "", err
	}
	return stdout.String(), nil
}

func defaultRunCommand(dir string, command []string) (commandOutput, error) {
	if len(command) == 0 {
		return commandOutput{}, errors.New("empty command")
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := commandOutput{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return out, &commandExitError{
				Command: command, ReturnCode: exitErr.ExitCode(),
				Stdout: out.Stdout, Stderr: out.Stderr,
			}
		}
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return out, &commandNotFound{Command: command[0]}
		}
		return out, err
	}
	return out, nil
}

func (r *Runner) Run(argv []string) int {
	spec := r.rootCommand()
	args, err := parseCommand(spec, argv)
	if err != nil {
		var help *helpRequest
		if errors.As(err, &help) {
			fmt.Fprint(r.Stdout, help.spec.helpText())
			return 0
		}
		var versionReq *versionRequest
		if errors.As(err, &versionReq) {
			fmt.Fprintf(r.Stdout, "hive %s\n", r.versionString())
			return 0
		}
		var usage *usageError
		if errors.As(err, &usage) {
			return r.reportUsageError(spec, usage)
		}
		fmt.Fprintf(r.Stderr, "%s\n", err.Error())
		return 2
	}
	if args.cmd == nil || args.cmd.Run == nil {
		return 0
	}
	return args.cmd.Run(args)
}

// reportUsageError prints the page argparse would print for a parse failure:
// the responsible parser's usage block, then "<prog>: error: <message>" on
// stderr. argparse's usage errors exit 2.
func (r *Runner) reportUsageError(root *commandSpec, err *usageError) int {
	if block := usageErrorBlock(root, err); block != "" {
		fmt.Fprintln(r.Stderr, block)
	}
	fmt.Fprintf(r.Stderr, "%s: error: %s\n", err.prog, err.msg)
	return 2
}

func (r *Runner) versionString() string {
	if r.Version != "" {
		return r.Version
	}
	return version.Version
}

// Parity: framework/cli.py::main
func (r *Runner) rootCommand() *commandSpec {
	root := &commandSpec{
		Prog:        "hive",
		Description: "Aden Hive — Queens, colonies, and live agent sessions",
		Dest:        "command",
		Flags: []argSpec{
			{Dest: "version", Long: "version", Kind: kindBool, Action: "version",
				Help: "show program's version number and exit"},
			{Dest: "model", Long: "model", Kind: kindString,
				Default: llm.DefaultAnthropicModel, Help: "Default LLM model (Anthropic ID)"},
		},
	}
	root.Subcommands = append(root.Subcommands, r.loaderCommands()...)
	root.Subcommands = append(root.Subcommands, r.skillCommands()...)
	root.Subcommands = append(root.Subcommands, r.debuggerCommands()...)
	root.Subcommands = append(root.Subcommands, r.mcpCommands()...)
	root.Subcommands = append(root.Subcommands, r.janitorCommands()...)
	root.Subcommands = append(root.Subcommands, r.queenMailMCPCommands()...)
	return root
}

// Parity: framework/loader/mcp_registry_cli.py::register_mcp_commands
func (r *Runner) mcpCommands() []*commandSpec {
	return []*commandSpec{{
		Prog:        "hive mcp",
		Help:        "Manage MCP servers",
		Dest:        "mcp",
		Passthrough: true,
		Run: func(args *parsedArgs) int {
			return r.MCPMain(args.rawArgs())
		},
	}}
}

func writeJSONOut(w io.Writer, value any, indented bool) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if indented {
		encoder.SetIndent("", "  ")
	}
	_ = encoder.Encode(value)
}

func pyStr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return typed
	case float64:
		return pyFloatRepr(typed)
	case float32:
		return pyFloatRepr(float64(typed))
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	}
	return fmt.Sprintf("%v", value)
}

func pyFloatRepr(f float64) string {
	if math.Trunc(f) == f && !math.IsInf(f, 0) && math.Abs(f) < 1e16 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func pyRepr(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return "'" + strings.ReplaceAll(typed, "'", "\\'") + "'"
	case float64:
		return pyFloatRepr(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, pyRepr(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, "'"+key+"': "+pyRepr(typed[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprintf("%v", value)
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, pyStr(item))
		}
		return out
	}
	return nil
}

func joinValues(value any, separator string) string {
	return strings.Join(stringSlice(value), separator)
}

func capitalize(text string) string {
	if text == "" {
		return ""
	}
	runes := []rune(text)
	for index, letter := range runes {
		if index == 0 {
			runes[index] = unicode.ToUpper(letter)
		} else {
			runes[index] = unicode.ToLower(letter)
		}
	}
	return string(runes)
}

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
		}
	}
	return path
}

func resolvePath(path string) string {
	expanded := expandUser(path)
	if absolute, err := filepath.Abs(expanded); err == nil {
		expanded = absolute
	}
	if resolved, err := filepath.EvalSymlinks(expanded); err == nil {
		return resolved
	}
	return expanded
}

func (r *Runner) workDir() string {
	if r.WorkDir != "" {
		return r.WorkDir
	}
	if dir, err := os.Getwd(); err == nil {
		return dir
	}
	return "."
}

func (r *Runner) goos() string {
	if r.GOOS != "" {
		return r.GOOS
	}
	return goos
}

func (r *Runner) userSkillsDir() string {
	return skills.UserSkillsDir()
}

func (r *Runner) registry() registryClient { return r.NewRegistry() }

func formatSubprocessOutput(value any, limit int) string {
	var text string
	switch typed := value.(type) {
	case nil:
		text = ""
	case string:
		text = typed
	case []byte:
		text = string(typed)
	default:
		text = fmt.Sprintf("%v", value)
	}
	if text == "" {
		return ""
	}
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

func httpGet(url string, timeout time.Duration) (map[string]any, error) {
	return httpRequest(http.MethodGet, url, nil, timeout)
}

func httpPost(url string, body map[string]any, timeout time.Duration) (map[string]any, error) {
	return httpRequest(http.MethodPost, url, body, timeout)
}

func httpDelete(url string, timeout time.Duration) (map[string]any, error) {
	return httpRequest(http.MethodDelete, url, nil, timeout)
}

func httpRequest(method, url string, body map[string]any, timeout time.Duration) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = strings.NewReader(string(payload))
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 400 {
		detail := strings.TrimSpace(string(raw))
		if detail != "" {
			return nil, fmt.Errorf("HTTP Error %d: %s", response.StatusCode, detail)
		}
		return nil, fmt.Errorf("HTTP Error %d: %s", response.StatusCode, http.StatusText(response.StatusCode))
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if object, ok := decoded.(map[string]any); ok {
		return object, nil
	}
	return map[string]any{}, nil
}
