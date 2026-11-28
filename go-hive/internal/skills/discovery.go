package skills

// Parity: framework/skills/discovery.py::SkillDiscovery

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

var skipDirs = map[string]bool{
	".git":          true,
	"node_modules":  true,
	"__pycache__":   true,
	".venv":         true,
	"venv":          true,
	".mypy_cache":   true,
	".pytest_cache": true,
	".ruff_cache":   true,
}

var ScopePriority = map[string]int{
	"framework": 0,
	"preset":    1,
	"user":      2,
	"queen_ui":  3,
	"colony_ui": 4,
	"project":   5,
}

type ExtraScope struct {
	Directory string
	Label     string
	Priority  int
}

type DiscoveryConfig struct {
	ProjectRoot        string
	HomeDir            string
	HiveHome           string
	SkipUserScope      bool
	SkipFrameworkScope bool
	MaxDepth           int
	MaxDirs            int
	ExtraScopes        []ExtraScope
	ExternalSkills     []string
}

//go:embed all:_default_skills all:_preset_skills
var bundledSkillsFS embed.FS

var (
	// bundledRoots caches the extracted root per content-addressed target
	// path. The key carries both the resolved cache base (and therefore
	// HIVE_HOME) and the bundle content id, so an in-process home change
	// resolves the new home instead of reusing the first home's stale path.
	// A cached entry is revalidated before use; if the directory disappeared
	// (a removed temp home, a cleaned cache) or its files no longer match the
	// embedded bytes (a partial or corrupt extraction), it is dropped and
	// re-extracted.
	bundledRootsMu sync.Mutex
	bundledRoots   = make(map[string]string)

	bundledIDOnce sync.Once
	bundledID     string
	bundledIDErr  error
)

// bundledSkillsRoot returns the directory holding the extracted embedded skill
// trees.
//
// The root must be deterministic for a given embedded bundle and Hive home:
// ParsedSkill.Location embeds it in the system prompt, /api/skills and
// `hive skill list`. A per-process os.MkdirTemp("", "hive-sk-") root changed
// that path on every start, so the bundled trees are extracted once into a
// content-addressed cache directory under <HIVE_HOME>/cache/skills instead.
//
// The extracted-root cache is keyed by that target path rather than held in a
// single process-global sync.Once: HIVE_HOME can change within one process
// (tests, embedded use), and a single cached root then pointed at a home that
// no longer existed. Extraction itself is idempotent and safe across
// concurrent starters, so a cache miss just ensures the new target.
func bundledSkillsRoot() (string, error) {
	base, err := bundledSkillsCacheBase()
	if err != nil {
		return "", err
	}
	id, err := bundledSkillsContentID()
	if err != nil {
		return "", err
	}
	key := bundledTarget(base, id)

	bundledRootsMu.Lock()
	defer bundledRootsMu.Unlock()
	if root, ok := bundledRoots[key]; ok {
		// Revalidate: the home that produced this root may have been removed
		// since it was cached, or the cached tree may be partial or corrupt
		// (an interrupted extraction, a cleaned file). Returning either would
		// surface as SKILL_NOT_FOUND / SKILL_ACTIVATION_FAILED for framework
		// skills, so drop the entry and let ensureBundledSkillsAt repair it.
		if complete, completeErr := bundledSkillsComplete(root); completeErr == nil && complete {
			return root, nil
		}
		delete(bundledRoots, key)
	}
	root, err := ensureBundledSkillsAt(base, id)
	if err != nil {
		return "", err
	}
	bundledRoots[key] = root
	return root, nil
}

// bundledSkillsCacheBase is <HIVE_HOME>/cache/skills. If HIVE_HOME cannot be
// resolved (no override and no usable user home), it falls back to the OS user
// cache dir (<user-cache>/hive/skills). Both are stable across process starts;
// this never returns a random TMPDIR name, and an error here disables the
// framework/preset scopes instead of reintroducing a per-process path.
func bundledSkillsCacheBase() (string, error) {
	home, homeErr := hivehome.Resolve()
	if homeErr == nil {
		return filepath.Join(home.CacheDir(), "skills"), nil
	}
	cache, cacheErr := os.UserCacheDir()
	if cacheErr == nil {
		return filepath.Join(cache, "hive", "skills"), nil
	}
	return "", fmt.Errorf("bundled skills cache root: hive home: %v; user cache dir: %w", homeErr, cacheErr)
}

// bundledSkillsContentID is the content-addressed directory name: SHA-256 of
// the embedded trees, truncated to 64 bits. Only a handful of bundle versions
// coexist, so 16 hex chars keep versions distinct while the short directory
// name leaves the 12-skill default catalog under CompactThresholdChars (a
// longer bundled path would flip it to compact mode and drop every description
// from the system prompt).
const bundledSkillsIDBytes = 8

func bundledSkillsContentID() (string, error) {
	bundledIDOnce.Do(func() {
		digest, err := hashBundledSkills(bundledSkillsFS)
		if err != nil {
			bundledIDErr = err
			return
		}
		bundledID = hex.EncodeToString(digest[:bundledSkillsIDBytes])
	})
	return bundledID, bundledIDErr
}

// hashBundledSkills hashes every embedded file with its path and length, in a
// deterministic order. The embedded bytes stay the source of truth: changing
// any bundled SKILL.md yields a new cache directory.
func hashBundledSkills(fsys fs.FS) ([]byte, error) {
	h := sha256.New()
	h.Write([]byte("hive-bundled-skills-v1\x00"))
	var paths []string
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// WalkDir is lexical, but sorting keeps the digest independent of walk
	// implementation details.
	sort.Strings(paths)
	for _, path := range paths {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil, err
		}
		h.Write([]byte(strconv.Itoa(len(data))))
		h.Write([]byte{0})
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(data)
	}
	return h.Sum(nil), nil
}

// bundledTarget is the content-addressed directory for one bundle version
// under a resolved cache base: <base>/bundled-<id>.
func bundledTarget(base, id string) string {
	return filepath.Join(base, "bundled-"+id)
}

// ensureBundledSkillsAt extracts the embedded trees to base/bundled-<id> and
// returns that directory. A directory whose files already match the embedded
// bytes is reused untouched. Stale bundled-* roots are deliberately left in
// place: another process running an older binary may still be serving
// SKILL.md paths out of one, so deleting them is not safe.
func ensureBundledSkillsAt(base, id string) (string, error) {
	target := bundledTarget(base, id)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	if complete, err := bundledSkillsComplete(target); err == nil && complete {
		return target, nil
	}
	if err := extractBundledSkills(target); err != nil {
		return "", err
	}
	complete, err := bundledSkillsComplete(target)
	if err != nil {
		return "", err
	}
	if !complete {
		return "", fmt.Errorf("bundled skills: %s is incomplete after extraction", target)
	}
	return target, nil
}

// bundledSkillsComplete reports whether target already holds the embedded
// bytes exactly. Comparing content (not just existence) repairs a truncated or
// corrupted file from an earlier interrupted run.
func bundledSkillsComplete(target string) (bool, error) {
	complete := true
	err := fs.WalkDir(bundledSkillsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		want, err := bundledSkillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(path)))
		if err != nil || !bytes.Equal(got, want) {
			complete = false
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return complete, nil
}

func extractBundledSkills(target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(bundledSkillsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dest := filepath.Join(target, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		data, err := bundledSkillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		return writeBundledSkillsFile(dest, data)
	})
}

// writeBundledSkillsFile publishes one embedded file with a temp-file+rename so
// a concurrent starter never observes a partially written SKILL.md.
func writeBundledSkillsFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bundled-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Another starter may have published identical bytes between our
		// completeness check and this write; that is success, not a failure.
		if existing, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(existing, data) {
			return nil
		}
		return err
	}
	return nil
}

type SkillDiscovery struct {
	config      DiscoveryConfig
	scannedDirs []string
}

func NewSkillDiscovery(config DiscoveryConfig) *SkillDiscovery {
	if config.MaxDepth == 0 {
		config.MaxDepth = 4
	}
	if config.MaxDirs == 0 {
		config.MaxDirs = 2000
	}
	return &SkillDiscovery{config: config}
}

func (d *SkillDiscovery) ScannedDirectories() []string {
	out := make([]string, 0, len(d.scannedDirs))
	for _, dir := range d.scannedDirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}

func (d *SkillDiscovery) Discover() []*ParsedSkill {
	var all []*ParsedSkill
	d.scannedDirs = nil

	if !d.config.SkipFrameworkScope {
		if root, err := bundledSkillsRoot(); err == nil {
			if dir := filepath.Join(root, "_default_skills"); isDir(dir) {
				d.scannedDirs = append(d.scannedDirs, dir)
				all = append(all, d.scanScope(dir, "framework")...)
			}
			if dir := filepath.Join(root, "_preset_skills"); isDir(dir) {
				d.scannedDirs = append(d.scannedDirs, dir)
				all = append(all, d.scanScope(dir, "preset")...)
			}
		}
	}

	if !d.config.SkipUserScope {
		home := d.config.HomeDir
		if home == "" {
			if h, err := os.UserHomeDir(); err == nil {
				home = h
			}
		}

		userAgents := filepath.Join(home, ".agents", "skills")
		if isDir(userAgents) {
			d.scannedDirs = append(d.scannedDirs, userAgents)
			all = append(all, d.scanScope(userAgents, "user")...)
		}

		for _, raw := range d.externalSkills() {
			ext := expandUserPath(raw, home)
			if isDir(ext) {
				d.scannedDirs = append(d.scannedDirs, ext)
				all = append(all, d.scanScope(ext, "user")...)
			}
		}

		userHive := filepath.Join(d.hiveHome(), "skills")
		if isDir(userHive) {
			d.scannedDirs = append(d.scannedDirs, userHive)
			all = append(all, d.scanScope(userHive, "user")...)
		}
	}

	for _, extra := range d.config.ExtraScopes {
		if isDir(extra.Directory) {
			d.scannedDirs = append(d.scannedDirs, extra.Directory)
			all = append(all, d.scanScope(extra.Directory, extra.Label)...)
		}
	}

	if d.config.ProjectRoot != "" {
		root := d.config.ProjectRoot
		projectAgents := filepath.Join(root, ".agents", "skills")
		if isDir(projectAgents) {
			d.scannedDirs = append(d.scannedDirs, projectAgents)
			all = append(all, d.scanScope(projectAgents, "project")...)
		}
		projectHive := filepath.Join(root, ".hive", "skills")
		if isDir(projectHive) {
			d.scannedDirs = append(d.scannedDirs, projectHive)
			all = append(all, d.scanScope(projectHive, "project")...)
		}
	}

	return d.resolveCollisions(all)
}

func (d *SkillDiscovery) hiveHome() string {
	if d.config.HiveHome != "" {
		return d.config.HiveHome
	}
	if home, err := hivehome.Resolve(); err == nil {
		return home.Root
	}
	return ""
}

func (d *SkillDiscovery) externalSkills() []string {
	if d.config.ExternalSkills != nil {
		return d.config.ExternalSkills
	}
	data, err := os.ReadFile(filepath.Join(d.hiveHome(), "configuration.json"))
	if err != nil {
		return nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	raw, _ := cfg["external_skills"].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (d *SkillDiscovery) scanScope(root, scope string) []*ParsedSkill {
	var skills []*ParsedSkill
	dirsScanned := 0
	for _, skillMD := range d.findSkillFiles(root, 0) {
		if dirsScanned >= d.config.MaxDirs {
			break
		}
		if parsed := ParseSkillMD(skillMD, scope); parsed != nil {
			skills = append(skills, parsed)
		}
		dirsScanned++
	}
	return skills
}

func (d *SkillDiscovery) findSkillFiles(dir string, depth int) []string {
	if depth > d.config.MaxDepth {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var results []string
	for _, entry := range entries {
		if skipDirs[entry.Name()] {
			continue
		}
		child := filepath.Join(dir, entry.Name())
		// Stat (not DirEntry.IsDir) follows symlinks, matching Python's
		// Path.is_dir(): a skill installed as a symlinked directory (e.g.
		// ~/.agents/skills/ego-browser -> ...) must be discovered. A symlink
		// that points at a file is skipped, as it is upstream.
		if !isDir(child) {
			continue
		}
		skillMD := filepath.Join(child, "SKILL.md")
		if info, err := os.Stat(skillMD); err == nil && !info.IsDir() {
			results = append(results, skillMD)
		} else {
			results = append(results, d.findSkillFiles(child, depth+1)...)
		}
	}
	return results
}

func (d *SkillDiscovery) resolveCollisions(skills []*ParsedSkill) []*ParsedSkill {
	seen := make(map[string]*ParsedSkill, len(skills))
	order := make([]string, 0, len(skills))
	for _, skill := range skills {
		if existing, ok := seen[skill.Name]; ok {
			LogSkillError("warning", SkillCollision,
				"Skill name collision: '"+skill.Name+"'",
				"'"+skill.Location+"' overrides '"+existing.Location+"'.",
				"Rename one of the conflicting skill directories to use a unique name.")
		} else {
			order = append(order, skill.Name)
		}
		seen[skill.Name] = skill
	}
	out := make([]*ParsedSkill, 0, len(seen))
	for _, name := range order {
		out = append(out, seen[name])
	}
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func expandUserPath(raw, home string) string {
	s := expandVars(raw)
	if s == "~" {
		return home
	}
	if strings.HasPrefix(s, "~/") || strings.HasPrefix(s, "~"+string(filepath.Separator)) {
		return filepath.Join(home, s[2:])
	}
	return s
}

func expandVars(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteByte(s[i])
				continue
			}
			name := s[i+2 : i+2+end]
			if val, ok := os.LookupEnv(name); ok {
				b.WriteString(val)
			} else {
				b.WriteString(s[i : i+2+end+1])
			}
			i += 2 + end
			continue
		}
		j := i + 1
		for j < len(s) && (isAlnum(s[j]) || s[j] == '_') {
			j++
		}
		if j == i+1 {
			b.WriteByte(s[i])
			continue
		}
		name := s[i+1 : j]
		if val, ok := os.LookupEnv(name); ok {
			b.WriteString(val)
		} else {
			b.WriteString(s[i:j])
		}
		i = j - 1
	}
	return b.String()
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
