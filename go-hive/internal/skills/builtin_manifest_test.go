package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The bundled skill trees are frozen byte-for-byte by
// go_hive/contracts/builtin-skills.json. Any added, removed, or edited file
// (SKILL.md frontmatter included, since whole files are hashed) fails these
// tests. Regenerate the manifest from the Python reference tree with:
//
//	uv run --no-project python go_hive/scripts/gen_builtin_skills_manifest.py
const (
	builtinManifestEnv      = "HIVE_BUILTIN_SKILLS_MANIFEST"
	builtinManifestRel      = "contracts/builtin-skills.json"
	builtinManifestRepoRel  = "go_hive/" + builtinManifestRel
	builtinManifestGenerate = "python go_hive/scripts/gen_builtin_skills_manifest.py"
)

var builtinScopes = []string{"_default_skills", "_preset_skills"}

// builtinSkillsManifest mirrors go_hive/contracts/builtin-skills.json.
type builtinSkillsManifest struct {
	Schema     int               `json:"schema"`
	Generator  string            `json:"generator"`
	Source     string            `json:"source"`
	Count      int               `json:"count"`
	TotalBytes int64             `json:"total_bytes"`
	Files      []builtinSkillRef `json:"files"`
}

type builtinSkillRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// builtinManifestPath locates the checked-in manifest. go test runs with the
// package directory as the working directory, so the common case is three
// levels up (internal/skills -> go-hive -> go_hive). The upward walk also
// finds it from the module root; HIVE_BUILTIN_SKILLS_MANIFEST overrides both.
func builtinManifestPath(t *testing.T) string {
	t.Helper()
	if override := os.Getenv(builtinManifestEnv); override != "" {
		return override
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for dir := wd; ; {
		for _, rel := range []string{builtinManifestRepoRel, builtinManifestRel} {
			candidate := filepath.Join(dir, rel)
			if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("cannot locate %s above %s (set %s to override)", builtinManifestRepoRel, wd, builtinManifestEnv)
	return ""
}

func loadBuiltinSkillsManifest(t *testing.T) builtinSkillsManifest {
	t.Helper()
	manifestPath := builtinManifestPath(t)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest builtinSkillsManifest
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	return manifest
}

// validateBuiltinManifest checks the manifest's own shape (canonical order,
// totals, path and digest formats) and returns its entries by path.
func validateBuiltinManifest(t *testing.T, manifest builtinSkillsManifest) map[string]builtinSkillRef {
	t.Helper()
	if manifest.Schema != 1 {
		t.Fatalf("manifest schema = %d, want 1 (%s)", manifest.Schema, builtinManifestGenerate)
	}
	if manifest.Count != len(manifest.Files) {
		t.Fatalf("manifest count = %d, but files has %d entries", manifest.Count, len(manifest.Files))
	}
	refs := make(map[string]builtinSkillRef, len(manifest.Files))
	var total int64
	previous := ""
	for _, ref := range manifest.Files {
		if ref.Path == "" || path.Clean(ref.Path) != ref.Path || strings.HasPrefix(ref.Path, "/") {
			t.Fatalf("manifest path %q is not a canonical relative path", ref.Path)
		}
		if previous != "" && strings.Compare(previous, ref.Path) >= 0 {
			t.Fatalf("manifest files are not sorted strictly: %q must precede %q", previous, ref.Path)
		}
		previous = ref.Path
		inScope := false
		for _, scope := range builtinScopes {
			if strings.HasPrefix(ref.Path, scope+"/") {
				inScope = true
				break
			}
		}
		if !inScope {
			t.Fatalf("manifest path %q is outside %v", ref.Path, builtinScopes)
		}
		if len(ref.SHA256) != sha256.Size*2 || strings.ToLower(ref.SHA256) != ref.SHA256 {
			t.Fatalf("manifest path %q has malformed sha256 %q", ref.Path, ref.SHA256)
		}
		if _, err := hex.DecodeString(ref.SHA256); err != nil {
			t.Fatalf("manifest path %q has non-hex sha256 %q: %v", ref.Path, ref.SHA256, err)
		}
		if ref.Bytes < 0 {
			t.Fatalf("manifest path %q has negative size %d", ref.Path, ref.Bytes)
		}
		if _, duplicate := refs[ref.Path]; duplicate {
			t.Fatalf("manifest path %q appears twice", ref.Path)
		}
		refs[ref.Path] = ref
		total += ref.Bytes
	}
	if total != manifest.TotalBytes {
		t.Fatalf("manifest total_bytes = %d, but file sizes sum to %d", manifest.TotalBytes, total)
	}
	return refs
}

// collectBuiltinSkills hashes every regular file under the two bundled scopes.
func collectBuiltinSkills(t *testing.T, fsys fs.FS) map[string]builtinSkillRef {
	t.Helper()
	refs := make(map[string]builtinSkillRef)
	for _, scope := range builtinScopes {
		walkErr := fs.WalkDir(fsys, scope, func(relPath string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s: not a regular file (%s)", relPath, info.Mode())
			}
			data, err := fs.ReadFile(fsys, relPath)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			refs[relPath] = builtinSkillRef{
				Path:   relPath,
				SHA256: hex.EncodeToString(sum[:]),
				Bytes:  int64(len(data)),
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", scope, walkErr)
		}
	}
	return refs
}

// assertBuiltinSkillsMatch reports every added, removed, or changed file.
func assertBuiltinSkillsMatch(t *testing.T, label string, expected, actual map[string]builtinSkillRef) {
	t.Helper()
	var missing, extra, changed []string
	for relPath, want := range expected {
		got, ok := actual[relPath]
		switch {
		case !ok:
			missing = append(missing, relPath)
		case got.Bytes != want.Bytes:
			changed = append(changed, fmt.Sprintf("%s: %d bytes, manifest says %d", relPath, got.Bytes, want.Bytes))
		case got.SHA256 != want.SHA256:
			changed = append(changed, fmt.Sprintf("%s: sha256 %s, manifest says %s", relPath, got.SHA256, want.SHA256))
		}
	}
	for relPath := range actual {
		if _, ok := expected[relPath]; !ok {
			extra = append(extra, relPath)
		}
	}
	if len(missing)+len(extra)+len(changed) == 0 {
		return
	}
	for _, list := range [][]string{missing, extra, changed} {
		sort.Strings(list)
	}
	var report strings.Builder
	fmt.Fprintf(&report, "%s does not match %s:\n", label, builtinManifestRepoRel)
	for _, relPath := range missing {
		fmt.Fprintf(&report, "  missing file: %s\n", relPath)
	}
	for _, relPath := range extra {
		fmt.Fprintf(&report, "  unexpected file: %s\n", relPath)
	}
	for _, line := range changed {
		fmt.Fprintf(&report, "  modified file: %s\n", line)
	}
	fmt.Fprintf(&report, "regenerate with: uv run --no-project %s", builtinManifestGenerate)
	t.Errorf("%s", report.String())
}

// Parity-exempt: the byte-level freeze of bundled skills is a Go packaging
// invariant; upstream has no manifest of its shipped skill files.
func TestBuiltinSkillsDiskMatchesManifest(t *testing.T) {
	manifest := loadBuiltinSkillsManifest(t)
	expected := validateBuiltinManifest(t, manifest)
	actual := collectBuiltinSkills(t, os.DirFS("."))
	assertBuiltinSkillsMatch(t, "internal/skills on disk", expected, actual)
}

// Parity-exempt: the embedded copies are what ships in the binary; upstream
// has no equivalent embed step or manifest test.
func TestBuiltinSkillsEmbedMatchesManifest(t *testing.T) {
	manifest := loadBuiltinSkillsManifest(t)
	expected := validateBuiltinManifest(t, manifest)
	actual := collectBuiltinSkills(t, bundledSkillsFS)
	assertBuiltinSkillsMatch(t, "embedded bundled skills", expected, actual)
}
