package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	terminalSearchTimeout  = 30 * time.Second
	terminalSearchMaxBytes = 256 * 1024
	terminalGlobMaxDefault = 1000
	terminalStderrTail     = 2000
	terminalGlobMaxAlts    = 256
)

var (
	terminalSearchSkipDirs = map[string]struct{}{
		".git": {}, "__pycache__": {}, "node_modules": {}, ".venv": {},
		".tox": {}, ".mypy_cache": {}, ".ruff_cache": {},
	}
	terminalGlobMetaRE = regexp.MustCompile(`[*?\[{]`)
)

// terminalTypeFilterExts is the minimal rg filetype table used by the
// os.walk fallback; unknown types are rejected rather than searching
// everything.
var terminalTypeFilterExts = map[string][]string{
	"py":   {".py", ".pyi"},
	"js":   {".js", ".jsx", ".mjs", ".cjs"},
	"ts":   {".ts", ".tsx"},
	"rust": {".rs"},
	"go":   {".go"},
	"java": {".java"},
	"c":    {".c", ".h"},
	"cpp":  {".cpp", ".cc", ".cxx", ".hpp", ".hh"},
	"md":   {".md", ".markdown"},
	"json": {".json"},
	"yaml": {".yaml", ".yml"},
	"toml": {".toml"},
	"sh":   {".sh", ".bash"},
	"html": {".html", ".htm"},
	"css":  {".css"},
	"txt":  {".txt"},
}

// terminalInstallationHint tells the model how to get ripgrep on this host.
func terminalInstallationHint() string {
	platform := "macOS: brew install ripgrep; Debian/Ubuntu: sudo apt-get install ripgrep"
	if runtime.GOOS == "windows" {
		platform = "Windows: winget install --exact --id BurntSushi.ripgrep.MSVC --source winget --scope user"
	}
	return "Run quickstart again to install ripgrep, or set HIVE_RIPGREP_PATH to a working absolute executable path. " +
		"Remove or correct that override if it is invalid. " + platform
}

func terminalRipgrepCandidates() []string {
	if override := os.Getenv("HIVE_RIPGREP_PATH"); override != "" {
		if filepath.IsAbs(override) {
			return []string{override}
		}
		return nil
	}
	var candidates []string
	if found, err := exec.LookPath("rg"); err == nil {
		candidates = append(candidates, found)
	}
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "windows" {
		roots := []string{
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, "scoop", "shims"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links"),
			filepath.Join(os.Getenv("ProgramFiles"), "WinGet", "Links"),
			filepath.Join(os.Getenv("ChocolateyInstall"), "bin"),
		}
		for _, root := range roots {
			if root != "" {
				candidates = append(candidates, filepath.Join(root, "rg.exe"))
			}
		}
	} else {
		candidates = append(candidates,
			"/usr/bin/rg", "/usr/local/bin/rg", "/opt/homebrew/bin/rg", "/home/linuxbrew/.linuxbrew/bin/rg")
		cargo := os.Getenv("CARGO_HOME")
		if cargo == "" {
			cargo = filepath.Join(home, ".cargo")
		}
		candidates = append(candidates, filepath.Join(cargo, "bin", "rg"))
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func terminalUsableRipgrep(binary string) bool {
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(string(out), "ripgrep ")
}

// terminalResolveRipgrep returns a verified ripgrep binary, or "".
func terminalResolveRipgrep() string {
	for _, candidate := range terminalRipgrepCandidates() {
		if terminalUsableRipgrep(candidate) {
			return candidate
		}
	}
	return ""
}

// terminalExpandGlobPattern widens a user glob so bare stems and bare basenames
// recurse instead of silently matching nothing.
func terminalExpandGlobPattern(pattern string) string {
	if strings.Contains(pattern, "/") {
		return pattern
	}
	if !terminalGlobMetaRE.MatchString(pattern) {
		return "**/*" + pattern + "*"
	}
	return "**/" + pattern
}

// terminalGlobMatcher compiles a glob with shell wildcards, ** segments and
// bounded brace alternatives into a predicate over slash-separated paths.
func terminalGlobMatcher(pattern string) (func(string) bool, error) {
	patterns := []string{pattern}
	var expanded [][]string
	for len(patterns) > 0 {
		current := patterns[len(patterns)-1]
		patterns = patterns[:len(patterns)-1]
		open := strings.IndexByte(current, '{')
		closeIdx := strings.IndexByte(current, '}')
		switch {
		case open >= 0 && closeIdx > open:
			alternatives := strings.Split(current[open+1:closeIdx], ",")
			for _, alternative := range alternatives {
				patterns = append(patterns, current[:open]+alternative+current[closeIdx+1:])
			}
			if len(patterns)+len(expanded) > terminalGlobMaxAlts {
				return nil, fmt.Errorf("glob has more than %d brace alternatives", terminalGlobMaxAlts)
			}
		case strings.ContainsAny(current, "{}"):
			return nil, fmt.Errorf("glob has unbalanced braces")
		default:
			expanded = append(expanded, strings.Split(strings.TrimPrefix(current, "./"), "/"))
		}
	}

	match := func(segments, parts []string) bool {
		type key struct{ p, s int }
		memo := make(map[key]bool)
		var walk func(p, s int) bool
		walk = func(p, s int) bool {
			if p == len(segments) {
				return s == len(parts)
			}
			if cached, ok := memo[key{p, s}]; ok {
				return cached
			}
			var result bool
			if segments[p] == "**" {
				result = walk(p+1, s) || (s < len(parts) && walk(p, s+1))
			} else if s < len(parts) {
				matched, err := path.Match(segments[p], parts[s])
				result = err == nil && matched && walk(p+1, s+1)
			}
			memo[key{p, s}] = result
			return result
		}
		return walk(0, 0)
	}

	return func(candidate string) bool {
		parts := strings.Split(candidate, "/")
		for _, segments := range expanded {
			if match(segments, parts) {
				return true
			}
		}
		return false
	}, nil
}

// terminalStreamPaths runs argv and collects accepted paths until maxResults or
// the deadline, killing the child once the cap is reached.
func terminalStreamPaths(argv []string, maxResults int, accept func(string) bool) ([]string, bool, bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), terminalSearchTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, false, ""
	}
	stderrRing := newTerminalRing(terminalStderrTail)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, false, false, ""
	}
	if err := cmd.Start(); err != nil {
		return nil, false, false, ""
	}
	var stderrWG sync.WaitGroup
	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				stderrRing.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	paths := make([]string, 0, 16)
	truncated := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if len(paths) >= maxResults {
			truncated = true
			break
		}
		candidate := strings.TrimRight(scanner.Text(), "\r")
		if candidate == "" {
			continue
		}
		if accept == nil || accept(candidate) {
			paths = append(paths, candidate)
		}
	}
	timedOut := ctx.Err() == context.DeadlineExceeded
	cancel()
	_ = cmd.Wait()
	stderrWG.Wait()
	tail := stderrRing.Tail(terminalStderrTail)
	return paths, truncated, timedOut, string(tail.Data)
}

// terminalWalkFiles walks root without ripgrep. Best-effort: no .gitignore.
func terminalWalkFiles(root string, matcher func(string) bool, maxResults int, includeIgnored bool) ([]string, bool) {
	paths := make([]string, 0, 16)
	addMatch := func(full, rel string) bool {
		if !matcher(rel) {
			return false
		}
		paths = append(paths, full)
		if len(paths) > maxResults {
			paths = paths[:maxResults]
			return true
		}
		return false
	}
	info, err := os.Stat(root)
	if err != nil {
		return paths, false
	}
	if !info.IsDir() {
		rel := filepath.Base(root)
		return paths, addMatch(root, rel)
	}
	stop := false
	_ = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if stop {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			if current == root {
				return nil
			}
			name := entry.Name()
			if !includeIgnored && (strings.HasPrefix(name, ".") || terminalSearchSkipDir(name)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !includeIgnored && strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return nil
		}
		if addMatch(current, filepath.ToSlash(rel)) {
			stop = true
			return filepath.SkipAll
		}
		return nil
	})
	return paths, stop
}

func terminalSearchSkipDir(name string) bool {
	_, skip := terminalSearchSkipDirs[name]
	return skip
}

func terminalWalkGlob(pattern string, root string, maxResults int, includeIgnored bool) ([]string, bool) {
	matcher, err := terminalGlobMatcher(pattern)
	if err != nil {
		return nil, false
	}
	return terminalWalkFiles(root, matcher, maxResults, includeIgnored)
}

// terminalRGLine is one match or context line.
type terminalRGLine struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// terminalRGResult is the rg reply. Field order mirrors the Python dict.
type terminalRGResult struct {
	Matches   []terminalRGLine `json:"matches"`
	Context   []terminalRGLine `json:"context"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
	ExitCode  int              `json:"exit_code"`
	Stderr    string           `json:"stderr"`
}

type terminalRGFallbackResult struct {
	Matches             []terminalRGLine `json:"matches"`
	Context             []terminalRGLine `json:"context"`
	Total               int              `json:"total"`
	Truncated           bool             `json:"truncated"`
	ExitCode            int              `json:"exit_code"`
	Stderr              string           `json:"stderr"`
	Fallback            string           `json:"fallback"`
	FallbackLimitations []string         `json:"fallback_limitations"`
	Note                string           `json:"note"`
}

type terminalRipgrepRequired struct {
	Error                 string   `json:"error"`
	Code                  string   `json:"code"`
	UnsupportedParameters []string `json:"unsupported_parameters"`
	FallbackLimitations   []string `json:"fallback_limitations"`
	Hint                  string   `json:"hint"`
}

type terminalTimeoutError struct {
	Error   string   `json:"error"`
	Command []string `json:"command"`
}

func terminalFallbackLimitations() []string {
	return []string{"No .gitignore awareness", "Go regexp (RE2) syntax", "Basename globs and a limited filetype table"}
}

// terminalRG implements terminal_rg: content search through ripgrep, with an
// explicit opt-in approximate walk when rg is missing.
func (t *Toolbox) terminalRG(ctx context.Context, input map[string]any) (Result, error) {
	pattern, ok := stringArg(input, "pattern")
	if !ok || pattern == "" {
		return terminalErr("terminal_rg: pattern is required"), nil
	}
	target, err := t.terminalTargetPath(input)
	if err != nil {
		return Result{}, err
	}
	glob, _ := stringArg(input, "glob")
	typeFilter, _ := stringArg(input, "type_filter")
	ignoreCase := terminalBool(input, "ignore_case")
	hidden := terminalBool(input, "hidden")
	noIgnore := terminalBool(input, "no_ignore")
	allowFallback := terminalBool(input, "allow_fallback")
	extraArgs, ok := terminalStringSlice(input, "extra_args")
	if !ok {
		return terminalErr("terminal_rg: extra_args must be a list of strings"), nil
	}
	contextLines := 0
	if v, present := input["context"]; present && v != nil {
		parsed, ok := intArg(input, "context")
		if !ok {
			return terminalErr("terminal_rg: context must be an integer"), nil
		}
		contextLines = parsed
	}
	var maxCount, maxDepth *int
	if v, present := input["max_count"]; present && v != nil {
		parsed, ok := intArg(input, "max_count")
		if !ok {
			return terminalErr("terminal_rg: max_count must be an integer"), nil
		}
		maxCount = &parsed
	}
	if v, present := input["max_depth"]; present && v != nil {
		parsed, ok := intArg(input, "max_depth")
		if !ok {
			return terminalErr("terminal_rg: max_depth must be an integer"), nil
		}
		maxDepth = &parsed
	}

	rg := terminalResolveRipgrep()
	if rg == "" {
		return terminalRGFallback(pattern, target, glob, typeFilter, ignoreCase, contextLines, maxCount, maxDepth, hidden, noIgnore, extraArgs, allowFallback)
	}

	argv := []string{rg, "--json", "--no-heading"}
	if ignoreCase {
		argv = append(argv, "-i")
	}
	if contextLines > 0 {
		argv = append(argv, "-C", itoa(contextLines))
	}
	if maxCount != nil {
		argv = append(argv, "-m", itoa(*maxCount))
	}
	if maxDepth != nil {
		argv = append(argv, "--max-depth", itoa(*maxDepth))
	}
	if hidden {
		argv = append(argv, "--hidden")
	}
	if noIgnore {
		argv = append(argv, "--no-ignore")
	}
	if typeFilter != "" {
		argv = append(argv, "-t", typeFilter)
	}
	if glob != "" {
		argv = append(argv, "-g", glob)
	}
	argv = append(argv, extraArgs...)
	argv = append(argv, "--", pattern, target)

	runCtx, cancel := context.WithTimeout(context.Background(), terminalSearchTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return terminalEncode(terminalTimeoutError{Error: "ripgrep timed out", Command: argv}), nil
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !asExitError(runErr, &exitErr) {
			return terminalRGFallback(pattern, target, glob, typeFilter, ignoreCase, contextLines, maxCount, maxDepth, hidden, noIgnore, extraArgs, allowFallback)
		}
	}

	matches := make([]terminalRGLine, 0)
	contextOut := make([]terminalRGLine, 0)
	truncated := false
	bytesSeen := 0
	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		bytesSeen += len(line)
		if bytesSeen > terminalSearchMaxBytes {
			truncated = true
			break
		}
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				LineNumber int `json:"line_number"`
				Lines      struct {
					Text string `json:"text"`
				} `json:"lines"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Type != "match" && event.Type != "context" {
			continue
		}
		entry := terminalRGLine{
			Path: event.Data.Path.Text,
			Line: event.Data.LineNumber,
			Text: strings.TrimRight(event.Data.Lines.Text, "\r\n"),
		}
		if event.Type == "match" {
			matches = append(matches, entry)
		} else {
			contextOut = append(contextOut, entry)
		}
	}

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	stderrText := ""
	if stderr.Len() > 0 {
		stderrText = tailString(stderr.String(), terminalStderrTail)
	}
	return terminalEncode(terminalRGResult{
		Matches:   matches,
		Context:   contextOut,
		Total:     len(matches),
		Truncated: truncated,
		ExitCode:  exitCode,
		Stderr:    stderrText,
	}), nil
}

// terminalRGFallback is the opt-in approximate search used when ripgrep is
// missing; unsupported parameters are rejected instead of silently dropped.
func terminalRGFallback(pattern, target, glob, typeFilter string, ignoreCase bool, contextLines int, maxCount, maxDepth *int, hidden, noIgnore bool, extraArgs []string, allowFallback bool) (Result, error) {
	limitations := terminalFallbackLimitations()
	unsupported := make([]string, 0, 4)
	if contextLines != 0 {
		unsupported = append(unsupported, "context")
	}
	if len(extraArgs) > 0 {
		unsupported = append(unsupported, "extra_args")
	}
	if typeFilter != "" {
		if _, known := terminalTypeFilterExts[typeFilter]; !known {
			unsupported = append(unsupported, "type_filter")
		}
	}
	if glob != "" && (strings.ContainsAny(glob, "/\\{}") || strings.HasPrefix(glob, "!")) {
		unsupported = append(unsupported, "glob")
	}
	if !allowFallback || len(unsupported) > 0 {
		message := "ripgrep is unavailable; approximate Go search requires allow_fallback=True."
		if len(unsupported) > 0 {
			message = "ripgrep is unavailable; requested parameters require ripgrep: " + strings.Join(unsupported, ", ")
		}
		return terminalEncode(terminalRipgrepRequired{
			Error:                 message,
			Code:                  "ripgrep_required",
			UnsupportedParameters: unsupported,
			FallbackLimitations:   limitations,
			Hint:                  terminalInstallationHint(),
		}), nil
	}

	expression := pattern
	if ignoreCase {
		expression = "(?i)" + pattern
	}
	compiled, err := regexp.Compile(expression)
	if err != nil {
		return terminalEncode(struct {
			Error    string `json:"error"`
			Fallback string `json:"fallback"`
		}{Error: "invalid regex: " + err.Error(), Fallback: "go-walk"}), nil
	}

	var exts []string
	if typeFilter != "" {
		exts = terminalTypeFilterExts[typeFilter]
	}
	included := func(name string) bool {
		if !hidden && strings.HasPrefix(name, ".") {
			return false
		}
		if glob != "" {
			if matched, err := path.Match(glob, name); err != nil || !matched {
				return false
			}
		}
		if len(exts) > 0 {
			matched := false
			for _, ext := range exts {
				if strings.HasSuffix(strings.ToLower(name), ext) {
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
		return true
	}

	matches := make([]terminalRGLine, 0)
	truncated := false
	bytesSeen := 0
	scan := func(file string) bool {
		data, err := os.ReadFile(file)
		if err != nil {
			return true
		}
		hits := 0
		for index, raw := range strings.Split(string(data), "\n") {
			if !compiled.MatchString(raw) {
				continue
			}
			bytesSeen += len(raw)
			if bytesSeen > terminalSearchMaxBytes {
				truncated = true
				return false
			}
			matches = append(matches, terminalRGLine{Path: file, Line: index + 1, Text: strings.TrimSuffix(raw, "\r")})
			hits++
			if maxCount != nil && hits >= *maxCount {
				break
			}
		}
		return true
	}

	info, err := os.Stat(target)
	if err == nil && !info.IsDir() {
		scan(target)
	} else if err == nil {
		baseDepth := strings.Count(strings.TrimRight(target, string(filepath.Separator)), string(filepath.Separator))
		stop := false
		_ = filepath.WalkDir(target, func(current string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if stop {
				return filepath.SkipAll
			}
			if entry.IsDir() {
				if current == target {
					return nil
				}
				name := entry.Name()
				if !hidden && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				if !noIgnore && terminalSearchSkipDir(name) {
					return filepath.SkipDir
				}
				if maxDepth != nil {
					depth := strings.Count(current, string(filepath.Separator)) - baseDepth
					if depth >= *maxDepth {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if !included(entry.Name()) {
				return nil
			}
			if !scan(current) {
				stop = true
				return filepath.SkipAll
			}
			return nil
		})
	}

	return terminalEncode(terminalRGFallbackResult{
		Matches:             matches,
		Context:             make([]terminalRGLine, 0),
		Total:               len(matches),
		Truncated:           truncated,
		ExitCode:            0,
		Stderr:              "",
		Fallback:            "go-walk",
		FallbackLimitations: limitations,
		Note:                "ripgrep unavailable; explicitly requested approximate Go search without .gitignore support.",
	}), nil
}

// terminalGlobResult is the terminal_glob reply.
type terminalGlobResult struct {
	Paths           []string `json:"paths"`
	Count           int      `json:"count"`
	Truncated       bool     `json:"truncated"`
	TimedOut        bool     `json:"timed_out"`
	ExpandedPattern string   `json:"expanded_pattern"`
	Stderr          string   `json:"stderr,omitempty"`
	Command         []string `json:"command"`
}

// terminalGlob implements terminal_glob: name/glob listing with a recursive
// default so a bare filename stem still finds the file.
func (t *Toolbox) terminalGlob(ctx context.Context, input map[string]any) (Result, error) {
	pattern, ok := stringArg(input, "pattern")
	if !ok || pattern == "" {
		return terminalErr("terminal_glob: pattern is required"), nil
	}
	target, err := t.terminalTargetPath(input)
	if err != nil {
		return Result{}, err
	}
	maxResults := terminalGlobMaxDefault
	if v, present := input["max_results"]; present && v != nil {
		parsed, ok := intArg(input, "max_results")
		if !ok {
			return terminalErr("terminal_glob: max_results must be an integer"), nil
		}
		maxResults = parsed
	}
	if maxResults < 0 {
		maxResults = 0
	}
	includeIgnored := terminalBool(input, "include_ignored")
	expanded := terminalExpandGlobPattern(pattern)
	matcher, err := terminalGlobMatcher(expanded)
	if err != nil {
		return terminalEncode(struct {
			Error           string `json:"error"`
			ExpandedPattern string `json:"expanded_pattern"`
		}{Error: err.Error(), ExpandedPattern: expanded}), nil
	}

	rg := terminalResolveRipgrep()
	if rg == "" {
		paths, truncated := terminalWalkGlob(expanded, target, maxResults, includeIgnored)
		return terminalEncode(struct {
			Paths           []string `json:"paths"`
			Count           int      `json:"count"`
			Truncated       bool     `json:"truncated"`
			TimedOut        bool     `json:"timed_out"`
			ExpandedPattern string   `json:"expanded_pattern"`
			Command         []string `json:"command"`
			Fallback        string   `json:"fallback"`
			Note            string   `json:"note"`
		}{
			Paths:           nonNilStrings(paths),
			Count:           len(paths),
			Truncated:       truncated,
			TimedOut:        false,
			ExpandedPattern: expanded,
			Command:         []string{"os.walk", target, expanded},
			Fallback:        "go-walk",
			Note:            "ripgrep unavailable; approximate filename walk does not honor .gitignore.",
		}), nil
	}

	argv := []string{rg, "--files", "--no-messages"}
	if includeIgnored {
		argv = append(argv, "-uu", "--hidden")
	}
	argv = append(argv, "--", target)
	base := target
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		base = filepath.Dir(target)
	}
	accept := func(candidate string) bool {
		rel, err := filepath.Rel(base, candidate)
		if err != nil {
			return false
		}
		return matcher(filepath.ToSlash(rel))
	}
	paths, truncated, timedOut, stderrTail := terminalStreamPaths(argv, maxResults, accept)
	return terminalEncode(terminalGlobResult{
		Paths:           nonNilStrings(paths),
		Count:           len(paths),
		Truncated:       truncated,
		TimedOut:        timedOut,
		ExpandedPattern: expanded,
		Stderr:          stderrTail,
		Command:         argv,
	}), nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return make([]string, 0)
	}
	return values
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var buf [20]byte
	pos := len(buf)
	for value > 0 {
		pos--
		buf[pos] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// asExitError reports whether err is an exec.ExitError without importing the
// errors package at every call site.
func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*target = exitErr
	return true
}
