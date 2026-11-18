package builtin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func terminalTestSearchTool(t *testing.T, tb *Toolbox, name string, input map[string]any) map[string]any {
	t.Helper()
	var (
		res Result
		err error
	)
	switch name {
	case ToolTerminalRG:
		res, err = tb.terminalRG(context.Background(), input)
	case ToolTerminalGlob:
		res, err = tb.terminalGlob(context.Background(), input)
	default:
		t.Fatalf("unknown search tool %q", name)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return terminalTestDecode(t, res)
}

func terminalTestWrite(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func terminalTestRequireRipgrep(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixtures use POSIX paths")
	}
	if terminalResolveRipgrep() == "" {
		t.Skip("ripgrep not installed")
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_expand_glob_pattern_rules
func TestTerminalExpandGlobPatternRules(t *testing.T) {
	cases := map[string]string{
		"lk_scan":         "**/*lk_scan*",
		"*.py":            "**/*.py",
		"src/**/*.py":     "src/**/*.py",
		"a/b":             "a/b",
		"lk_scan_post.py": "**/*lk_scan_post.py*",
	}
	for input, want := range cases {
		if got := terminalExpandGlobPattern(input); got != want {
			t.Fatalf("expand(%q) = %q, want %q", input, got, want)
		}
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_path_segments_braces_and_recursion
func TestTerminalGlobMatcherSegmentsBracesRecursion(t *testing.T) {
	matcher, err := terminalGlobMatcher("src/**/*.py")
	if err != nil {
		t.Fatalf("matcher: %v", err)
	}
	for _, candidate := range []string{"src/a.py", "src/pkg/deep/b.py"} {
		if !matcher(candidate) {
			t.Fatalf("%q not matched by src/**/*.py", candidate)
		}
	}
	if matcher("lib/a.py") {
		t.Fatal("lib/a.py matched src/**/*.py")
	}

	braces, err := terminalGlobMatcher("**/*.{go,py}")
	if err != nil {
		t.Fatalf("brace matcher: %v", err)
	}
	for _, candidate := range []string{"main.go", "pkg/util.go", "x/y.py"} {
		if !braces(candidate) {
			t.Fatalf("%q not matched by **/*.{go,py}", candidate)
		}
	}
	if braces("notes.txt") {
		t.Fatal("notes.txt matched **/*.{go,py}")
	}

	if _, err := terminalGlobMatcher("{unbalanced"); err == nil {
		t.Fatal("unbalanced brace accepted")
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_finds_pattern
func TestTerminalRGFindsPattern(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a.txt", "hello\nworld\nfoo\n")
	terminalTestWrite(t, dir, "b.txt", "bar\nworld\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{"pattern": "world", "path": dir})
	total, _ := result["total"].(float64)
	if total < 2 {
		t.Fatalf("total = %v, want >= 2", result["total"])
	}
	matches, _ := result["matches"].([]any)
	found := false
	for _, entry := range matches {
		line, _ := entry.(map[string]any)
		if strings.Contains(line["path"].(string), "a.txt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("matches = %v", matches)
	}
	if result["exit_code"].(float64) != 0 {
		t.Fatalf("exit_code = %v", result["exit_code"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_no_matches
func TestTerminalRGNoMatches(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a.txt", "hello\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{
		"pattern": "zzz_no_match_zzz",
		"path":    dir,
	})
	if result["total"].(float64) != 0 {
		t.Fatalf("total = %v, want 0", result["total"])
	}
	if matches, _ := result["matches"].([]any); len(matches) != 0 {
		t.Fatalf("matches = %v, want []", result["matches"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_preserves_context_events
func TestTerminalRGPreservesContextEvents(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a.txt", "before\nneedle\nafter\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{
		"pattern": "needle",
		"path":    dir,
		"context": 1,
	})
	if result["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1", result["total"])
	}
	contextLines, _ := result["context"].([]any)
	if len(contextLines) != 2 {
		t.Fatalf("context = %v, want 2 lines", result["context"])
	}
	if matches, _ := result["matches"].([]any); len(matches) != 1 {
		t.Fatalf("matches = %v, want 1 (context must not inflate the count)", result["matches"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_by_name
func TestTerminalGlobByName(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "alpha.log", "a")
	terminalTestWrite(t, dir, "beta.log", "b")
	terminalTestWrite(t, dir, "ignore.txt", "c")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{"pattern": "*.log", "path": dir})
	if result["count"].(float64) != 2 {
		t.Fatalf("count = %v, want 2 (%v)", result["count"], result["paths"])
	}
	paths, _ := result["paths"].([]any)
	for _, entry := range paths {
		if !strings.HasSuffix(entry.(string), ".log") {
			t.Fatalf("unexpected path %v", entry)
		}
	}
	if result["expanded_pattern"] != "**/*.log" {
		t.Fatalf("expanded_pattern = %v", result["expanded_pattern"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_bare_stem_matches_file_with_extension
func TestTerminalGlobBareStem(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "scripts/lk_scan_post_reactors.py", "x")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{
		"pattern": "lk_scan_post_reactors",
		"path":    dir,
	})
	if result["expanded_pattern"] != "**/*lk_scan_post_reactors*" {
		t.Fatalf("expanded_pattern = %v", result["expanded_pattern"])
	}
	paths, _ := result["paths"].([]any)
	found := false
	for _, entry := range paths {
		if strings.HasSuffix(entry.(string), "lk_scan_post_reactors.py") {
			found = true
		}
	}
	if !found {
		t.Fatalf("paths = %v", paths)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_recurses_by_default
func TestTerminalGlobRecursesByDefault(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a/b/config.py", "x")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{"pattern": "*.py", "path": dir})
	paths, _ := result["paths"].([]any)
	if len(paths) != 1 || !strings.HasSuffix(paths[0].(string), "config.py") {
		t.Fatalf("paths = %v", paths)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_no_matches
func TestTerminalGlobNoMatches(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a.txt", "x")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{
		"pattern": "zzz_no_such_file_zzz",
		"path":    dir,
	})
	if result["count"].(float64) != 0 {
		t.Fatalf("count = %v, want 0", result["count"])
	}
	if paths, _ := result["paths"].([]any); len(paths) != 0 {
		t.Fatalf("paths = %v, want []", result["paths"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_respects_ignore_rules_unless_opted_out
func TestTerminalGlobRespectsIgnoreRules(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	terminalTestWrite(t, dir, ".gitignore", "ignored.txt\n")
	terminalTestWrite(t, dir, ".ignore", "extra.txt\n")
	for _, name := range []string{"visible.txt", "ignored.txt", "extra.txt", ".hidden.txt"} {
		terminalTestWrite(t, dir, name, "x")
	}

	tb := New(dir)
	respect := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{"pattern": "*.txt", "path": dir})
	if respect["count"].(float64) != 1 {
		t.Fatalf("count = %v, want 1 (%v)", respect["count"], respect["paths"])
	}
	if paths, _ := respect["paths"].([]any); !strings.HasSuffix(paths[0].(string), "visible.txt") {
		t.Fatalf("paths = %v, want visible.txt only", paths)
	}
	optOut := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{
		"pattern":         "*.txt",
		"path":            dir,
		"include_ignored": true,
	})
	if optOut["count"].(float64) != 4 {
		t.Fatalf("include_ignored count = %v, want 4 (%v)", optOut["count"], optOut["paths"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_fallback_requires_explicit_opt_in
func TestTerminalRGFallbackRequiresOptIn(t *testing.T) {
	t.Setenv("HIVE_RIPGREP_PATH", filepath.Join(t.TempDir(), "missing-rg"))
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a.txt", "needle\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{"pattern": "needle", "path": dir})
	if result["code"] != "ripgrep_required" {
		t.Fatalf("code = %v, want ripgrep_required (%v)", result["code"], result)
	}
	if result["hint"] == nil {
		t.Fatalf("missing hint: %v", result)
	}
	unsupported, _ := result["unsupported_parameters"].([]any)
	if len(unsupported) != 0 {
		t.Fatalf("unsupported_parameters = %v, want []", unsupported)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_fallback_rejects_unsupported_parameters_before_search
func TestTerminalRGFallbackRejectsUnsupportedParameters(t *testing.T) {
	t.Setenv("HIVE_RIPGREP_PATH", filepath.Join(t.TempDir(), "missing-rg"))
	dir := t.TempDir()
	terminalTestWrite(t, dir, "a.txt", "needle\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{
		"pattern":        "needle",
		"path":           dir,
		"allow_fallback": true,
		"context":        2,
	})
	if result["code"] != "ripgrep_required" {
		t.Fatalf("code = %v, want ripgrep_required", result["code"])
	}
	unsupported, _ := result["unsupported_parameters"].([]any)
	if len(unsupported) != 1 || unsupported[0] != "context" {
		t.Fatalf("unsupported_parameters = %v, want [context]", unsupported)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_fallback_respects_glob_and_case
func TestTerminalRGFallbackRespectsGlobAndCase(t *testing.T) {
	t.Setenv("HIVE_RIPGREP_PATH", filepath.Join(t.TempDir(), "missing-rg"))
	dir := t.TempDir()
	terminalTestWrite(t, dir, "one.py", "Needle\n")
	terminalTestWrite(t, dir, "two.txt", "Needle\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{
		"pattern":        "needle",
		"path":           dir,
		"glob":           "*.py",
		"ignore_case":    true,
		"allow_fallback": true,
	})
	if result["fallback"] != "go-walk" {
		t.Fatalf("fallback = %v, want go-walk", result["fallback"])
	}
	if result["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1 (%v)", result["total"], result["matches"])
	}
	match := result["matches"].([]any)[0].(map[string]any)
	if !strings.HasSuffix(match["path"].(string), "one.py") {
		t.Fatalf("match path = %v", match["path"])
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_stream_paths_stops_at_cap_and_handles_final_line
func TestTerminalStreamPathsCapAndFinalLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses sh")
	}
	paths, truncated, timedOut, _ := terminalStreamPaths([]string{"sh", "-c", "printf 'a.py\\nb.py\\nc.py'"}, 10, nil)
	if strings.Join(paths, ",") != "a.py,b.py,c.py" {
		t.Fatalf("paths = %v", paths)
	}
	if truncated || timedOut {
		t.Fatalf("truncated=%v timedOut=%v", truncated, timedOut)
	}
	capped, cappedTruncated, _, _ := terminalStreamPaths([]string{"sh", "-c", "printf 'a.py\\nb.py\\nc.py\\n'"}, 1, nil)
	if len(capped) != 1 || capped[0] != "a.py" {
		t.Fatalf("capped = %v", capped)
	}
	if !cappedTruncated {
		t.Fatal("cap did not report truncation")
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_stream_paths_filters_before_applying_result_cap
func TestTerminalStreamPathsFiltersBeforeCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses sh")
	}
	paths, truncated, timedOut, _ := terminalStreamPaths(
		[]string{"sh", "-c", "printf 'skip.log\\nkeep.py\\nskip.log\\nlast.py'"},
		2,
		func(candidate string) bool { return strings.HasSuffix(candidate, ".py") },
	)
	if strings.Join(paths, ",") != "keep.py,last.py" {
		t.Fatalf("paths = %v", paths)
	}
	if truncated || timedOut {
		t.Fatalf("truncated=%v timedOut=%v", truncated, timedOut)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_native_context_and_gitignore
func TestTerminalRGNativeContextAndGitignore(t *testing.T) {
	terminalTestRequireRipgrep(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	terminalTestWrite(t, dir, ".gitignore", "ignored.txt\n")
	terminalTestWrite(t, dir, "ignored.txt", "needle\n")
	terminalTestWrite(t, dir, "visible.txt", "before\nneedle\nafter\n")

	tb := New(dir)
	result := terminalTestSearchTool(t, tb, ToolTerminalRG, map[string]any{
		"pattern": "needle",
		"path":    dir,
		"context": 1,
	})
	if result["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1 (%v)", result["total"], result["matches"])
	}
	match := result["matches"].([]any)[0].(map[string]any)
	if !strings.HasSuffix(match["path"].(string), "visible.txt") {
		t.Fatalf("match path = %v, want visible.txt (.gitignore must be honored)", match["path"])
	}
	contextLines, _ := result["context"].([]any)
	if len(contextLines) != 2 {
		t.Fatalf("context = %v, want 2 entries", contextLines)
	}
	first := contextLines[0].(map[string]any)
	second := contextLines[1].(map[string]any)
	if first["line"].(float64) != 1 || first["text"] != "before" || second["line"].(float64) != 3 || second["text"] != "after" {
		t.Fatalf("context = %v, want [(1,before),(3,after)]", contextLines)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_path_segments_braces_and_recursion
func TestTerminalGlobSegmentsBracesRecursion(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		if !fallback {
			terminalTestRequireRipgrep(t)
		} else {
			t.Setenv("HIVE_RIPGREP_PATH", filepath.Join(t.TempDir(), "missing-rg"))
		}
		dir := t.TempDir()
		for _, name := range []string{"src/a.py", "src/deep/b.py", "src/deep/b.ts", "src/deep/c.txt", "other/a.py"} {
			terminalTestWrite(t, dir, name, "x")
		}
		tb := New(dir)
		braced := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{
			"pattern": "src/**/*.{py,ts}",
			"path":    dir,
		})
		got := make(map[string]bool)
		for _, entry := range braced["paths"].([]any) {
			rel, err := filepath.Rel(dir, entry.(string))
			if err != nil {
				t.Fatal(err)
			}
			got[filepath.ToSlash(rel)] = true
		}
		if len(got) != 3 || !got["src/a.py"] || !got["src/deep/b.py"] || !got["src/deep/b.ts"] {
			t.Fatalf("fallback=%v braced paths = %v", fallback, got)
		}
		single := terminalTestSearchTool(t, tb, ToolTerminalGlob, map[string]any{
			"pattern": "src/*.py",
			"path":    dir,
		})
		if single["count"].(float64) != 1 {
			t.Fatalf("fallback=%v src/*.py count = %v (%v)", fallback, single["count"], single["paths"])
		}
		rel, _ := filepath.Rel(dir, single["paths"].([]any)[0].(string))
		if filepath.ToSlash(rel) != "src/a.py" {
			t.Fatalf("fallback=%v src/*.py = %v", fallback, rel)
		}
	}
}
