package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func mustMkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	return path
}

func expectAllowed(t *testing.T, agentPath string, roots []string) string {
	t.Helper()
	resolved, err := ValidateAgentPath(agentPath, roots)
	if err != nil {
		t.Fatalf("ValidateAgentPath(%q) rejected: %v", agentPath, err)
	}
	return resolved
}

func expectRejected(t *testing.T, agentPath string, roots []string) error {
	t.Helper()
	resolved, err := ValidateAgentPath(agentPath, roots)
	if err == nil {
		t.Fatalf("ValidateAgentPath(%q) accepted as %q", agentPath, resolved)
	}
	return err
}

// Parity: tests/test_validate_agent_path.py::TestGetAllowedAgentRoots::test_returns_tuple
func TestReturnsTuple(t *testing.T) {
	tmp := t.TempDir()
	roots, err := NewAllowedRoots(tmp, tmp, tmp).Resolve()
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	if reflect.TypeOf(roots).Kind() != reflect.Slice {
		t.Fatalf("roots kind = %s, want slice", reflect.TypeOf(roots).Kind())
	}
}

// Parity: tests/test_validate_agent_path.py::TestGetAllowedAgentRoots::test_contains_four_roots
func TestContainsFourRoots(t *testing.T) {
	tmp := t.TempDir()
	roots, err := NewAllowedRoots(tmp, tmp, tmp).Resolve()
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	if len(roots) != 4 {
		t.Fatalf("roots = %d, want 4", len(roots))
	}
}

// Parity: tests/test_validate_agent_path.py::TestGetAllowedAgentRoots::test_cached_on_repeated_calls
func TestCachedOnRepeatedCalls(t *testing.T) {
	tmp := t.TempDir()
	allowed := NewAllowedRoots(filepath.Join(tmp, "colonies"), tmp, filepath.Join(tmp, "agents"))
	first, err := allowed.Resolve()
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	second, err := allowed.Resolve()
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("empty roots: %v %v", first, second)
	}
	if &first[0] != &second[0] {
		t.Fatal("repeated calls returned different slices")
	}
}

// Parity: tests/test_validate_agent_path.py::TestGetAllowedAgentRoots::test_roots_are_resolved_paths
func TestRootsAreResolvedPaths(t *testing.T) {
	tmp := t.TempDir()
	roots, err := NewAllowedRoots(
		filepath.Join(tmp, "colonies"),
		filepath.Join(tmp, "repo"),
		filepath.Join(tmp, "agents"),
	).Resolve()
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			t.Fatalf("root %q is not absolute", root)
		}
		for _, part := range strings.Split(root, string(filepath.Separator)) {
			if part == ".." {
				t.Fatalf("root %q still contains '..'", root)
			}
		}
	}
}

// Parity: tests/test_validate_agent_path.py::TestGetAllowedAgentRoots::test_roots_anchored_to_repo_not_cwd
func TestRootsAnchoredToRepoNotCwd(t *testing.T) {
	repoRoot := t.TempDir()
	workDir := t.TempDir()
	t.Chdir(workDir)

	roots, err := NewAllowedRoots(filepath.Join(repoRoot, "colonies"), repoRoot, filepath.Join(repoRoot, "agents")).Resolve()
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	wantExports, err := ResolvePath(filepath.Join(repoRoot, "exports"))
	if err != nil {
		t.Fatalf("resolve exports: %v", err)
	}
	wantExamples, err := ResolvePath(filepath.Join(repoRoot, "examples"))
	if err != nil {
		t.Fatalf("resolve examples: %v", err)
	}
	if roots[1] != wantExports {
		t.Fatalf("roots[1] = %q, want %q", roots[1], wantExports)
	}
	if roots[2] != wantExamples {
		t.Fatalf("roots[2] = %q, want %q", roots[2], wantExamples)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathPositive::test_path_inside_exports
func TestPathInsideExports(t *testing.T) {
	tmp := t.TempDir()
	agentDir := mustMkdir(t, filepath.Join(tmp, "my_agent"))
	result := expectAllowed(t, agentDir, []string{tmp})
	want, err := ResolvePath(agentDir)
	if err != nil {
		t.Fatalf("resolve agent dir: %v", err)
	}
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathPositive::test_path_inside_examples
func TestPathInsideExamples(t *testing.T) {
	tmp := t.TempDir()
	examplesRoot := mustMkdir(t, filepath.Join(tmp, "examples"))
	agentDir := mustMkdir(t, filepath.Join(examplesRoot, "some_agent"))
	result := expectAllowed(t, agentDir, []string{examplesRoot})
	want, err := ResolvePath(agentDir)
	if err != nil {
		t.Fatalf("resolve agent dir: %v", err)
	}
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathPositive::test_path_inside_hive_agents
func TestPathInsideHiveAgents(t *testing.T) {
	tmp := t.TempDir()
	hiveRoot := mustMkdir(t, filepath.Join(tmp, ".hive", "agents"))
	agentDir := mustMkdir(t, filepath.Join(hiveRoot, "my_agent"))
	result := expectAllowed(t, agentDir, []string{hiveRoot})
	want, err := ResolvePath(agentDir)
	if err != nil {
		t.Fatalf("resolve agent dir: %v", err)
	}
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathPositive::test_returns_path_object
func TestReturnsPathObject(t *testing.T) {
	tmp := t.TempDir()
	agentDir := mustMkdir(t, filepath.Join(tmp, "agent"))
	result := expectAllowed(t, agentDir, []string{tmp})
	if !filepath.IsAbs(result) {
		t.Fatalf("result = %q, want an absolute path", result)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_absolute_path_outside_roots
func TestAbsolutePathOutsideRoots(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	err := expectRejected(t, "/tmp/evil", []string{exports})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_traversal_escape
func TestTraversalEscape(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	traversal := filepath.Join(exports, "..", "..", "tmp", "evil")
	err := expectRejected(t, traversal, []string{exports})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_sibling_directory_name
func TestSiblingDirectoryName(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	sibling := mustMkdir(t, filepath.Join(tmp, "exports-evil", "agent"))
	err := expectRejected(t, sibling, []string{exports})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_empty_string
func TestEmptyString(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	err := expectRejected(t, "", []string{exports})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_home_directory
func TestHomeDirectory(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	err := expectRejected(t, "~", []string{exports})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_root
func TestRoot(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	err := expectRejected(t, "/", []string{exports})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_null_byte
func TestNullByte(t *testing.T) {
	tmp := t.TempDir()
	exports := mustMkdir(t, filepath.Join(tmp, "exports"))
	expectRejected(t, "exports/\x00evil", []string{exports})
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_symlink_escape
func TestSymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	allowed := mustMkdir(t, filepath.Join(tmp, "exports"))
	outside := mustMkdir(t, filepath.Join(tmp, "outside"))
	link := filepath.Join(allowed, "sneaky")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	err := expectRejected(t, link, []string{allowed})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_root_itself_rejected
func TestRootItselfRejected(t *testing.T) {
	tmp := t.TempDir()
	allowed := mustMkdir(t, filepath.Join(tmp, "exports"))
	err := expectRejected(t, allowed, []string{allowed})
	if !strings.Contains(err.Error(), "allowed directory") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: tests/test_validate_agent_path.py::TestValidateAgentPathNegative::test_tilde_expansion
func TestTildeExpansion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	hiveAgents := mustMkdir(t, filepath.Join(tmp, ".hive", "agents"))
	agentDir := mustMkdir(t, filepath.Join(hiveAgents, "my_agent"))
	result := expectAllowed(t, "~/.hive/agents/my_agent", []string{hiveAgents})
	want, err := ResolvePath(agentDir)
	if err != nil {
		t.Fatalf("resolve agent dir: %v", err)
	}
	if result != want {
		t.Fatalf("result = %q, want %q", result, want)
	}
}

// Parity: tests/test_validate_agent_path.py::TestAllowedRootsImmutability::test_is_tuple_not_list
func TestIsTupleNotList(t *testing.T) {
	tmp := t.TempDir()
	roots, err := NewAllowedRoots(tmp, tmp, tmp).Resolve()
	if err != nil {
		t.Fatalf("resolve roots: %v", err)
	}
	if reflect.TypeOf(roots).Kind() != reflect.Slice {
		t.Fatalf("roots kind = %s, want slice", reflect.TypeOf(roots).Kind())
	}
}
