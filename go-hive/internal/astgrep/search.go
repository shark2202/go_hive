package astgrep

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultResultCap is the model-visible result cap. There is no limit
	// input in the frozen contract; use Request.Skip to page.
	DefaultResultCap = 50
	// DefaultParseTimeout bounds one file's parse. A stopped parse becomes a
	// parse issue, not a partial set of matches.
	DefaultParseTimeout = 2 * time.Second
	// DefaultScanWorkers is the bounded worker pool size for file parsing.
	DefaultScanWorkers = 4
	// maxParseErrorDetails caps the deduplicated parse issues rendered in a
	// result, mirroring oh-my-pi's 20-line display cap.
	maxParseErrorDetails = 20
)

// Config controls one search. Zero values take the package defaults. The
// WorkspaceRoot is the security boundary; an empty root uses the process
// working directory so a standalone caller still works.
type Config struct {
	WorkspaceRoot string
	MaxFileBytes  int64
	ResultCap     int
	ParseTimeout  time.Duration
	Workers       int
}

func (c Config) normalized() Config {
	if c.MaxFileBytes <= 0 {
		c.MaxFileBytes = DefaultMaxFileBytes
	}
	if c.ResultCap <= 0 {
		c.ResultCap = DefaultResultCap
	}
	if c.ParseTimeout <= 0 {
		c.ParseTimeout = DefaultParseTimeout
	}
	if c.Workers <= 0 {
		c.Workers = DefaultScanWorkers
	}
	return c
}

// Request is the core ast_grep input contract. The model-facing adapter
// requires Path to be absolute (go_hive has no project cwd); the core also
// resolves relative paths for standalone callers and its own tests. An empty
// Path defaults to the workspace root. Lang overrides language inference for
// every candidate. Skip is the match offset, never negative.
type Request struct {
	Pattern string
	Path    string
	Lang    string
	Skip    int
}

// Match is one rendered structural hit. Path is workspace-relative and
// slash-separated; Line is 1-based; Text is the whole matched node.
type Match struct {
	Path      string
	Line      int
	Text      string
	StartByte int
	EndByte   int
	Captures  []Capture
}

// ParseError is one deduplicated syntax or pattern problem. Line is 0 for a
// file-level issue such as an uncompilable pattern for that language.
type ParseError struct {
	Path    string
	Line    int
	Message string
}

// Result is the complete outcome of one search. Matches is already paged by
// Skip and capped. TotalMatches is the pre-page count collected before the
// bounded scan stopped (at most Skip+ResultCap+1).
type Result struct {
	Matches          []Match
	TotalMatches     int
	FileCount        int
	FilesSearched    int
	LimitReached     bool
	ParseErrors      []ParseError
	ParseErrorsTotal int
	ScopePath        string
	SearchPath       string
	SkippedFiles     int
}

// Search validates input, resolves paths and languages, compiles the pattern
// per language, scans candidate files with a bounded worker pool, and returns
// sorted/paged matches plus parse issues.
func Search(ctx context.Context, cfg Config, req Request) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, &Error{Kind: ErrCancelled, Message: "ast_grep cancelled", Err: err}
	}
	cfg = cfg.normalized()
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return nil, NewError(ErrInvalidInput, "pattern is required")
	}
	if req.Skip < 0 {
		return nil, NewError(ErrInvalidInput, "skip must be non-negative")
	}
	if req.Skip > math.MaxInt-cfg.ResultCap-1 {
		return nil, NewError(ErrInvalidInput, "skip is too large")
	}
	root, err := resolveWorkspaceRoot(cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	var explicit *Lang
	if strings.TrimSpace(req.Lang) != "" {
		explicit, err = ResolveLanguage(req.Lang)
		if err != nil {
			return nil, err
		}
	}
	// Compile an explicit-language pattern before walking paths: an invalid
	// pattern should fail fast even when the scope has no matching file.
	earlyPattern := map[string]*Pattern{}
	if explicit != nil {
		compiled, compileErr := CompilePattern(explicit, pattern)
		if compileErr != nil {
			return nil, compileErr
		}
		earlyPattern[explicit.Name()] = compiled
	}
	scope, err := expandScope(root, req.Path)
	if err != nil {
		return nil, err
	}
	candidates, err := assignLanguages(scope.candidates, explicit)
	if err != nil {
		return nil, err
	}
	result := &Result{
		ScopePath:    scopePath(req.Path),
		SearchPath:   scope.display,
		SkippedFiles: scope.skipped,
	}
	if len(candidates) == 0 {
		return result, nil
	}
	patterns := earlyPattern
	patternFailures := map[string]error{}
	if explicit == nil {
		for _, candidate := range candidates {
			name := candidate.lang.Name()
			if _, done := patterns[name]; done {
				continue
			}
			if _, failed := patternFailures[name]; failed {
				continue
			}
			compiled, compileErr := CompilePattern(candidate.lang, pattern)
			if compileErr != nil {
				patternFailures[name] = compileErr
				continue
			}
			patterns[name] = compiled
		}
	}
	if len(patterns) == 0 {
		var first error
		for _, compileErr := range patternFailures {
			first = compileErr
			break
		}
		if first == nil {
			first = NewError(ErrInvalidPattern, "pattern did not compile for any candidate language")
		}
		return nil, first
	}
	files := make([]candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := patterns[candidate.lang.Name()]; ok {
			files = append(files, candidate)
			continue
		}
		result.ParseErrors = append(result.ParseErrors, ParseError{
			Path:    candidate.display,
			Message: "pattern: " + patternFailures[candidate.lang.Name()].Error(),
		})
	}
	maxCollect := req.Skip + cfg.ResultCap + 1
	scans, err := scanOrdered(ctx, files, maxCollect, cfg.Workers, func(scanCtx context.Context, file candidate) fileScanResult {
		return scanOne(scanCtx, file, patterns[file.lang.Name()], cfg, maxCollect)
	})
	if err != nil {
		return nil, err
	}
	var all []Match
	searched := 0
	skipped := result.SkippedFiles
	for _, scan := range scans {
		all = append(all, scan.matches...)
		if scan.searched {
			searched++
		}
		skipped += scan.skipped
		result.ParseErrors = append(result.ParseErrors, scan.parseErrors...)
	}
	result.FilesSearched = searched
	result.SkippedFiles = skipped
	result.TotalMatches = len(all)
	result.LimitReached = len(all) > req.Skip+cfg.ResultCap
	start := req.Skip
	if start > len(all) {
		start = len(all)
	}
	end := start + cfg.ResultCap
	if end > len(all) {
		end = len(all)
	}
	result.Matches = append([]Match(nil), all[start:end]...)
	result.FileCount = countFiles(result.Matches)
	result.ParseErrors, result.ParseErrorsTotal = dedupeParseErrors(result.ParseErrors)
	return result, nil
}

type fileScanResult struct {
	index       int
	matches     []Match
	parseErrors []ParseError
	searched    bool
	skipped     int
}

func scanOne(ctx context.Context, file candidate, pattern *Pattern, cfg Config, maxCollect int) fileScanResult {
	out := fileScanResult{}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return out
		default:
		}
	}
	data, ok, err := readCandidate(file.abs, cfg.MaxFileBytes)
	if err != nil {
		out.parseErrors = append(out.parseErrors, ParseError{Path: file.display, Message: "read: " + err.Error()})
		return out
	}
	if !ok {
		out.skipped = 1
		return out
	}
	out.searched = true
	tree, err := parseTree(file.lang, data, cfg.ParseTimeout)
	if err != nil {
		message := "parse: " + err.Error()
		if errors.Is(err, errParseStopped) {
			message = "parse stopped early (timeout or work limit)"
		}
		out.parseErrors = append(out.parseErrors, ParseError{Path: file.display, Message: message})
		return out
	}
	defer tree.Release()
	root := tree.RootNode()
	if root != nil && root.HasError() {
		line, message := firstParseIssue(root, file.lang, data)
		out.parseErrors = append(out.parseErrors, ParseError{Path: file.display, Line: line, Message: message})
	}
	for _, match := range pattern.findInTreeLimit(root, data, maxCollect) {
		out.matches = append(out.matches, Match{
			Path:      file.display,
			Line:      match.Line,
			Text:      match.Text,
			StartByte: match.StartByte,
			EndByte:   match.EndByte,
			Captures:  match.Captures,
		})
	}
	return out
}

// scanOrdered runs a bounded worker pool but consumes results strictly in
// path order, so skip paging and sorting stay deterministic even when workers
// finish out of order. Once maxCollect matches are consumed, workers are
// cancelled and later files are not reported as searched.
func scanOrdered(ctx context.Context, files []candidate, maxCollect, workers int,
	scan func(context.Context, candidate) fileScanResult) ([]fileScanResult, error) {
	if len(files) == 0 || maxCollect <= 0 {
		return nil, nil
	}
	if workers > len(files) {
		workers = len(files)
	}
	if workers <= 0 {
		workers = 1
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	out := make(chan fileScanResult, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				result := scan(runCtx, files[index])
				result.index = index
				select {
				case out <- result:
				case <-runCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range files {
			select {
			case jobs <- index:
			case <-runCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(out)
	}()

	pending := map[int]fileScanResult{}
	results := make([]fileScanResult, 0, len(files))
	next := 0
	collected := 0
	for collected < maxCollect && next < len(files) {
		select {
		case scan, open := <-out:
			if !open {
				return results, nil
			}
			pending[scan.index] = scan
			for {
				current, ok := pending[next]
				if !ok {
					break
				}
				delete(pending, next)
				remaining := maxCollect - collected
				if len(current.matches) > remaining {
					current.matches = current.matches[:remaining]
				}
				collected += len(current.matches)
				results = append(results, current)
				next++
				if collected >= maxCollect {
					break
				}
			}
		case <-ctx.Done():
			cancel()
			wg.Wait()
			return nil, &Error{Kind: ErrCancelled, Message: "ast_grep cancelled", Err: ctx.Err()}
		}
	}
	cancel()
	wg.Wait()
	return results, nil
}

func resolveWorkspaceRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", NewError(ErrInvalidInput, "cannot resolve workspace root: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", NewError(ErrInvalidInput, "cannot resolve workspace root: %v", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", NewError(ErrInvalidInput, "workspace root is not a directory: %s", root)
	}
	return filepath.Clean(resolved), nil
}

func scopePath(pathArg string) string {
	pathArg = strings.TrimSpace(pathArg)
	if pathArg == "" {
		return "."
	}
	return pathArg
}

func countFiles(matches []Match) int {
	if len(matches) == 0 {
		return 0
	}
	seen := map[string]struct{}{}
	for _, match := range matches {
		seen[match.Path] = struct{}{}
	}
	return len(seen)
}

func dedupeParseErrors(in []ParseError) ([]ParseError, int) {
	if len(in) == 0 {
		return nil, 0
	}
	unique := make([]ParseError, 0, len(in))
	seen := map[string]struct{}{}
	for _, issue := range in {
		key := fmt.Sprintf("%s\x00%d\x00%s", issue.Path, issue.Line, issue.Message)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, issue)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		if unique[i].Path != unique[j].Path {
			return unique[i].Path < unique[j].Path
		}
		return unique[i].Line < unique[j].Line
	})
	total := len(unique)
	if len(unique) > maxParseErrorDetails {
		unique = unique[:maxParseErrorDetails]
	}
	return unique, total
}
