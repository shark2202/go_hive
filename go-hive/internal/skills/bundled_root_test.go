package skills

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const bundledRootHelperEnv = "HIVE_BUNDLED_ROOT_HELPER"

// TestBundledSkillsRootStable: the bundled root must not be a per-process
// hive-sk-* temp directory. Two calls agree, the parent is the documented
// cache root, and the directory holds the embedded trees.
// Parity-exempt: Go-only cache-root test for the embedded bundled-skill packaging; no Python counterpart.
func TestBundledSkillsRootStable(t *testing.T) {
	first, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	second, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot (second call): %v", err)
	}
	if first != second {
		t.Fatalf("bundled root changed between calls: %q vs %q", first, second)
	}

	base, err := bundledSkillsCacheBase()
	if err != nil {
		t.Fatalf("bundledSkillsCacheBase: %v", err)
	}
	id, err := bundledSkillsContentID()
	if err != nil {
		t.Fatalf("bundledSkillsContentID: %v", err)
	}
	if got := filepath.Dir(first); got != base {
		t.Fatalf("bundled root parent = %q, want stable cache root %q", got, base)
	}
	if got, want := filepath.Base(first), "bundled-"+id; got != want {
		t.Fatalf("bundled root name = %q, want %q", got, want)
	}
	if strings.Contains(first, "hive-sk-") {
		t.Fatalf("bundled root %q still uses a hive-sk- temp segment", first)
	}
	if _, err := os.Stat(filepath.Join(first, "_default_skills", "writing-hive-skills", "SKILL.md")); err != nil {
		t.Fatalf("bundled default skill missing under %q: %v", first, err)
	}
}

// TestBundledSkillsRootStableAcrossProcesses: a fresh process resolves the
// same root for the same bundle and home. It also re-execs this test binary
// with an isolated HIVE_HOME twice so two cold starts agree with each other.
// Parity-exempt: Go-only process-guard test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsRootStableAcrossProcesses(t *testing.T) {
	parentRoot, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	// Same environment (and therefore the same Hive home) as this process.
	freshRoot := runBundledRootHelperProcess(t)
	if freshRoot != parentRoot {
		t.Fatalf("fresh process root = %q, want %q from this process", freshRoot, parentRoot)
	}

	hiveHome := t.TempDir()
	first := runBundledRootHelperProcess(t, "HIVE_HOME="+hiveHome)
	second := runBundledRootHelperProcess(t, "HIVE_HOME="+hiveHome)
	if first != second {
		t.Fatalf("fresh processes resolved different bundled roots: %q vs %q", first, second)
	}
	wantParent := filepath.Join(hiveHome, "cache", "skills")
	if got := filepath.Dir(first); got != wantParent {
		t.Fatalf("bundled root parent = %q, want %q", got, wantParent)
	}
	if !strings.HasPrefix(filepath.Base(first), "bundled-") {
		t.Fatalf("bundled root %q is not content-addressed", first)
	}
	if strings.Contains(first, "hive-sk-") {
		t.Fatalf("bundled root %q still uses a hive-sk- temp segment", first)
	}
	if info, err := os.Stat(first); err != nil || !info.IsDir() {
		t.Fatalf("bundled root %q is not a directory: %v", first, err)
	}
}

func runBundledRootHelperProcess(t *testing.T, extraEnv ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBundledSkillsRootHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), bundledRootHelperEnv+"=1")
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bundled root helper process failed: %v\n%s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "BUNDLED_ROOT=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "BUNDLED_ROOT="))
		}
	}
	t.Fatalf("bundled root helper process printed no root:\n%s", out)
	return ""
}

// Parity-exempt: exists only to re-exec the test binary for the cross-process bundled root equality check; no Python counterpart.
func TestBundledSkillsRootHelperProcess(t *testing.T) {
	if os.Getenv(bundledRootHelperEnv) != "1" {
		t.Skip("helper process for TestBundledSkillsRootStableAcrossProcesses")
	}
	root, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	fmt.Printf("BUNDLED_ROOT=%s\n", root)
}

// TestBundledSkillsCacheBaseUsesHiveHomeAndFallback pins the two stable roots:
// <HIVE_HOME>/cache/skills when the home resolves, and the OS user cache dir
// when it does not. Neither is TMPDIR randomness.
// Parity-exempt: Go-only cache-base test for the embedded bundled-skill packaging; no Python counterpart.
func TestBundledSkillsCacheBaseUsesHiveHomeAndFallback(t *testing.T) {
	hiveHome := t.TempDir()
	t.Setenv("HIVE_HOME", hiveHome)
	base, err := bundledSkillsCacheBase()
	if err != nil {
		t.Fatalf("bundledSkillsCacheBase with HIVE_HOME: %v", err)
	}
	if want := filepath.Join(hiveHome, "cache", "skills"); base != want {
		t.Fatalf("cache base = %q, want %q", base, want)
	}

	// An unresolvable ~user makes hivehome.Resolve fail; the fallback must be
	// the OS user cache dir, not a random temp directory.
	t.Setenv("HIVE_HOME", "~hive-user-that-does-not-exist")
	userCache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no user cache dir on this host: %v", err)
	}
	fallback, err := bundledSkillsCacheBase()
	if err != nil {
		t.Fatalf("bundledSkillsCacheBase fallback: %v", err)
	}
	if want := filepath.Join(userCache, "hive", "skills"); fallback != want {
		t.Fatalf("fallback cache base = %q, want %q", fallback, want)
	}
}

// Parity-exempt: Go-only extraction test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsExtractionIdempotentAndRepairs(t *testing.T) {
	base := t.TempDir()
	id, err := bundledSkillsContentID()
	if err != nil {
		t.Fatalf("bundledSkillsContentID: %v", err)
	}
	first, err := ensureBundledSkillsAt(base, id)
	if err != nil {
		t.Fatalf("initial extraction: %v", err)
	}
	second, err := ensureBundledSkillsAt(base, id)
	if err != nil {
		t.Fatalf("second extraction: %v", err)
	}
	if first != second {
		t.Fatalf("extraction root changed: %q vs %q", first, second)
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read cache base: %v", err)
	}
	bundledDirs := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "bundled-") {
			bundledDirs++
		}
	}
	if bundledDirs != 1 {
		t.Fatalf("cache base has %d bundled-* directories, want 1", bundledDirs)
	}

	// A complete tree is reused untouched: an old mtime survives the second
	// ensure, so it did not rewrite (or even touch) the file.
	skillPath := filepath.Join(first, "_default_skills", "pdf", "SKILL.md")
	old := time.Unix(946684800, 0)
	if err := os.Chtimes(skillPath, old, old); err != nil {
		t.Fatalf("chtimes %s: %v", skillPath, err)
	}
	if _, err := ensureBundledSkillsAt(base, id); err != nil {
		t.Fatalf("reuse extraction: %v", err)
	}
	info, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat reused file: %v", err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("complete tree was rewritten (mtime %v, want %v)", info.ModTime(), old)
	}

	// A truncated file is not "complete": the next ensure rewrites the tree
	// from the embedded bytes.
	if err := os.WriteFile(skillPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatalf("corrupt %s: %v", skillPath, err)
	}
	if _, err := ensureBundledSkillsAt(base, id); err != nil {
		t.Fatalf("repair extraction: %v", err)
	}
	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read repaired file: %v", err)
	}
	want, err := bundledSkillsFS.ReadFile("_default_skills/pdf/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("repaired file does not match embedded bytes")
	}
}

// TestBundledSkillsExtractionLeavesStaleVersionAlone documents the safe
// half of "remove or ignore stale versions": an old bundled-* directory may
// still be serving a running process, so it is ignored, not deleted.
// Parity-exempt: Go-only stale-version test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsExtractionLeavesStaleVersionAlone(t *testing.T) {
	base := t.TempDir()
	stale := filepath.Join(base, "bundled-0000000000000000")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale root: %v", err)
	}
	sentinel := filepath.Join(stale, "SKILL.md")
	if err := os.WriteFile(sentinel, []byte("old bundle"), 0o644); err != nil {
		t.Fatalf("write stale sentinel: %v", err)
	}
	id, err := bundledSkillsContentID()
	if err != nil {
		t.Fatalf("bundledSkillsContentID: %v", err)
	}
	active, err := ensureBundledSkillsAt(base, id)
	if err != nil {
		t.Fatalf("extraction: %v", err)
	}
	if active == stale {
		t.Fatalf("active root points at the stale directory")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "old bundle" {
		t.Fatalf("stale bundled-* directory was modified: %q, %v", got, err)
	}
	if complete, err := bundledSkillsComplete(active); err != nil || !complete {
		t.Fatalf("active tree incomplete: complete=%v, err=%v", complete, err)
	}
}

// TestBundledSkillsExtractionConcurrent covers two starters racing on the same
// content-addressed directory: both converge on the complete tree.
// Parity-exempt: Go-only concurrent-extraction test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsExtractionConcurrent(t *testing.T) {
	base := t.TempDir()
	id, err := bundledSkillsContentID()
	if err != nil {
		t.Fatalf("bundledSkillsContentID: %v", err)
	}
	const starters = 6
	roots := make([]string, starters)
	errs := make([]error, starters)
	var wg sync.WaitGroup
	for i := 0; i < starters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			roots[i], errs[i] = ensureBundledSkillsAt(base, id)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("starter %d: %v", i, err)
		}
		if roots[i] != roots[0] {
			t.Fatalf("starter %d root = %q, want %q", i, roots[i], roots[0])
		}
	}
	if complete, err := bundledSkillsComplete(roots[0]); err != nil || !complete {
		t.Fatalf("concurrent extraction left an incomplete tree (complete=%v, err=%v)", complete, err)
	}
}

// TestBundledSkillsRootFollowsHiveHome: the extracted-root cache is keyed by
// the resolved home, so an in-process HIVE_HOME change yields the new home's
// root instead of the first home's path. This is the regression that broke
// `hive skill doctor` after a test home was removed while a process-global
// sync.Once still held the deleted path.
// Parity-exempt: Go-only cache-key regression test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsRootFollowsHiveHome(t *testing.T) {
	firstHome := t.TempDir()
	secondHome := t.TempDir()

	t.Setenv("HIVE_HOME", firstHome)
	first, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot under first home: %v", err)
	}

	t.Setenv("HIVE_HOME", secondHome)
	second, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot under second home: %v", err)
	}
	if first == second {
		t.Fatalf("bundled root stayed at the first home's path after HIVE_HOME changed: %q", first)
	}

	id, err := bundledSkillsContentID()
	if err != nil {
		t.Fatalf("bundledSkillsContentID: %v", err)
	}
	for _, tc := range []struct {
		name string
		home string
		root string
	}{
		{"first", firstHome, first},
		{"second", secondHome, second},
	} {
		want := filepath.Join(tc.home, "cache", "skills", "bundled-"+id)
		if tc.root != want {
			t.Fatalf("%s home root = %q, want %q", tc.name, tc.root, want)
		}
		if info, err := os.Stat(tc.root); err != nil || !info.IsDir() {
			t.Fatalf("%s home root %q is not an existing directory: %v", tc.name, tc.root, err)
		}
	}

	// The second home stays stable across repeated calls (the first call may
	// not be the only one; the cache must not fall back to an older key).
	again, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot under second home (repeat): %v", err)
	}
	if again != second {
		t.Fatalf("bundled root under second home changed between calls: %q vs %q", again, second)
	}
}

// TestBundledSkillsRootReExtractsDeletedCache: a cached root whose directory
// was removed is repaired on the next call rather than returned as a missing
// path.
// Parity-exempt: Go-only cache-repair regression test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsRootReExtractsDeletedCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)

	first, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	if err := os.RemoveAll(first); err != nil {
		t.Fatalf("remove cached bundled root %q: %v", first, err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("cached bundled root %q still exists after removal: %v", first, err)
	}

	repaired, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot after cache deletion: %v", err)
	}
	if repaired != first {
		t.Fatalf("repaired root = %q, want the content-addressed path %q", repaired, first)
	}
	if info, err := os.Stat(repaired); err != nil || !info.IsDir() {
		t.Fatalf("repaired root %q is not an existing directory: %v", repaired, err)
	}
	if complete, err := bundledSkillsComplete(repaired); err != nil || !complete {
		t.Fatalf("repaired tree incomplete: complete=%v, err=%v", complete, err)
	}
	if _, err := os.Stat(filepath.Join(repaired, "_default_skills", "pdf", "SKILL.md")); err != nil {
		t.Fatalf("repaired tree is missing a bundled skill: %v", err)
	}
}

// TestBundledSkillsRootRepairsIncompleteCache: a cache hit with the directory
// still present but a corrupt or missing bundled file must drop the entry and
// repair the tree, not return the stale root. An existence-only cache check
// returned the broken tree as-is.
// Parity-exempt: Go-only cache-repair regression test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsRootRepairsIncompleteCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)

	root, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	if complete, err := bundledSkillsComplete(root); err != nil || !complete {
		t.Fatalf("primed tree incomplete: complete=%v, err=%v", complete, err)
	}

	// Corrupt one bundled file in place: the directory and the file both
	// exist, so only a content check can see the damage.
	corrupt := filepath.Join(root, "_default_skills", "pdf", "SKILL.md")
	want, err := bundledSkillsFS.ReadFile("_default_skills/pdf/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded file: %v", err)
	}
	if err := os.WriteFile(corrupt, []byte("corrupt"), 0o644); err != nil {
		t.Fatalf("corrupt %s: %v", corrupt, err)
	}

	repaired, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot after cache corruption: %v", err)
	}
	if repaired != root {
		t.Fatalf("repaired root = %q, want the content-addressed path %q", repaired, root)
	}
	got, err := os.ReadFile(corrupt)
	if err != nil {
		t.Fatalf("read repaired file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("repaired file does not match embedded bytes")
	}

	// A removed bundled file (directory still present) must be restored too.
	missing := filepath.Join(root, "_default_skills", "writing-hive-skills", "SKILL.md")
	if err := os.Remove(missing); err != nil {
		t.Fatalf("remove %s: %v", missing, err)
	}
	again, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot after bundled file removal: %v", err)
	}
	if again != root {
		t.Fatalf("repaired root after removal = %q, want %q", again, root)
	}
	if complete, err := bundledSkillsComplete(root); err != nil || !complete {
		t.Fatalf("repaired tree incomplete: complete=%v, err=%v", complete, err)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("removed bundled file was not restored: %v", err)
	}
}

// TestBundledSkillsCatalogLocationUsesStableRoot: the framework skill the
// catalog exposes must carry the stable bundled root, and the rendered prompt
// must use that Location verbatim.
// Parity-exempt: Go-only catalog-location test for the embedded bundled-skill cache; no Python counterpart.
func TestBundledSkillsCatalogLocationUsesStableRoot(t *testing.T) {
	root, err := bundledSkillsRoot()
	if err != nil {
		t.Fatalf("bundledSkillsRoot: %v", err)
	}
	manager := NewSkillsManager(&SkillsManagerConfig{
		HomeDir:                t.TempDir(),
		HiveHome:               t.TempDir(),
		SkipCommunityDiscovery: true,
	})
	manager.Load()

	skill := manager.Catalog().Get("hive.writing-hive-skills")
	if skill == nil {
		t.Fatal("hive.writing-hive-skills missing from the catalog")
	}
	resolvedRoot := root
	if evaluated, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		resolvedRoot = evaluated
	}
	wantLocation := filepath.Join(resolvedRoot, "_default_skills", "writing-hive-skills", "SKILL.md")
	if skill.Location != wantLocation {
		t.Fatalf("catalog location = %q, want %q", skill.Location, wantLocation)
	}
	if strings.Contains(skill.Location, "hive-sk-") {
		t.Fatalf("catalog location %q still uses a hive-sk- temp segment", skill.Location)
	}
	if prompt := manager.SkillsCatalogPrompt(); !strings.Contains(prompt, skill.Location) {
		t.Fatalf("catalog prompt does not contain the stable location %q", skill.Location)
	}
}
