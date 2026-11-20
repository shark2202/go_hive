package skills

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
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
	bundledOnce sync.Once
	bundledRoot string
	bundledErr  error
)

func bundledSkillsRoot() (string, error) {
	bundledOnce.Do(func() {
		root, err := os.MkdirTemp("", "hive-bundled-skills-")
		if err != nil {
			bundledErr = err
			return
		}
		for _, top := range []string{"_default_skills", "_preset_skills"} {
			walkErr := fs.WalkDir(bundledSkillsFS, top, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				target := filepath.Join(root, filepath.FromSlash(path))
				if d.IsDir() {
					return os.MkdirAll(target, 0o755)
				}
				data, err := bundledSkillsFS.ReadFile(path)
				if err != nil {
					return err
				}
				return os.WriteFile(target, data, 0o644)
			})
			if walkErr != nil {
				bundledErr = walkErr
				return
			}
		}
		bundledRoot = root
	})
	return bundledRoot, bundledErr
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
