package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mattn/go-isatty"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const EnvTrustAll = "HIVE_TRUST_PROJECT_SKILLS"

const EnvOwnRemotes = "HIVE_OWN_REMOTES"

type TrustedRepoEntry struct {
	RepoKey     string
	AddedAt     time.Time
	ProjectPath string
}

type TrustedRepoStore struct {
	path    string
	entries map[string]*TrustedRepoEntry
	order   []string
	loaded  bool
}

func NewTrustedRepoStore(path string) *TrustedRepoStore {
	if path == "" {
		if home, err := hivehome.Resolve(); err == nil {
			path = filepath.Join(home.Root, "trusted_repos.json")
		} else {
			path = filepath.Join(os.TempDir(), "hive-trusted_repos.json")
		}
	}
	return &TrustedRepoStore{path: path, entries: map[string]*TrustedRepoEntry{}}
}

func (s *TrustedRepoStore) Path() string { return s.path }

func (s *TrustedRepoStore) IsTrusted(repoKey string) bool {
	s.ensureLoaded()
	_, ok := s.entries[repoKey]
	return ok
}

func (s *TrustedRepoStore) Trust(repoKey, projectPath string) {
	s.ensureLoaded()
	if _, exists := s.entries[repoKey]; !exists {
		s.order = append(s.order, repoKey)
	}
	s.entries[repoKey] = &TrustedRepoEntry{
		RepoKey:     repoKey,
		AddedAt:     time.Now().UTC(),
		ProjectPath: projectPath,
	}
	_ = s.save()
	log.Printf("skill_trust_store: trusted repo_key=%s", repoKey)
}

func (s *TrustedRepoStore) Revoke(repoKey string) bool {
	s.ensureLoaded()
	if _, ok := s.entries[repoKey]; !ok {
		return false
	}
	delete(s.entries, repoKey)
	for i, key := range s.order {
		if key == repoKey {
			s.order = append(s.order[:i:i], s.order[i+1:]...)
			break
		}
	}
	_ = s.save()
	log.Printf("skill_trust_store: revoked repo_key=%s", repoKey)
	return true
}

func (s *TrustedRepoStore) ListEntries() []*TrustedRepoEntry {
	s.ensureLoaded()
	out := make([]*TrustedRepoEntry, 0, len(s.order))
	for _, key := range s.order {
		if entry, ok := s.entries[key]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func (s *TrustedRepoStore) ensureLoaded() {
	if !s.loaded {
		s.load()
		s.loaded = true
	}
}

func (s *TrustedRepoStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("skill_trust_store: could not read %s (%v); treating as empty", s.path, err)
		return
	}
	entries, _ := raw["entries"].([]any)
	for _, item := range entries {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		repoKey, _ := entry["repo_key"].(string)
		if repoKey == "" {
			continue
		}
		addedAt := time.Now().UTC()
		if rawTime, ok := entry["added_at"].(string); ok {
			if parsed, err := parseISOTime(rawTime); err == nil {
				addedAt = parsed
			}
		}
		projectPath, _ := entry["project_path"].(string)
		if _, exists := s.entries[repoKey]; !exists {
			s.order = append(s.order, repoKey)
		}
		s.entries[repoKey] = &TrustedRepoEntry{
			RepoKey:     repoKey,
			AddedAt:     addedAt,
			ProjectPath: projectPath,
		}
	}
}

func (s *TrustedRepoStore) save() error {
	entries := make([]map[string]any, 0, len(s.order))
	for _, key := range s.order {
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		entries = append(entries, map[string]any{
			"repo_key":     entry.RepoKey,
			"added_at":     entry.AddedAt.Format(time.RFC3339Nano),
			"project_path": entry.ProjectPath,
		})
	}
	payload, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"entries": entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(s.path, payload, 0o644)
}

func parseISOTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, raw)
}

type ProjectTrustClassification string

const (
	AlwaysTrusted ProjectTrustClassification = "always_trusted"
	TrustedByUser ProjectTrustClassification = "trusted_by_user"
	Untrusted     ProjectTrustClassification = "untrusted"
)

type ProjectTrustDetector struct {
	store        *TrustedRepoStore
	RemoteOrigin func(projectDir string) string
	HiveHome     string
}

func NewProjectTrustDetector(store *TrustedRepoStore) *ProjectTrustDetector {
	if store == nil {
		store = NewTrustedRepoStore("")
	}
	return &ProjectTrustDetector{store: store}
}

func (d *ProjectTrustDetector) Classify(projectDir string) (ProjectTrustClassification, string) {
	if projectDir == "" {
		return AlwaysTrusted, ""
	}
	info, err := os.Stat(projectDir)
	if err != nil || !info.IsDir() {
		return AlwaysTrusted, ""
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".git")); err != nil {
		return AlwaysTrusted, ""
	}

	remoteURL := d.remoteOrigin(projectDir)
	if strings.TrimSpace(remoteURL) == "" {
		return AlwaysTrusted, ""
	}
	remoteURL = strings.TrimSpace(remoteURL)

	repoKey := NormalizeRemoteURL(remoteURL)

	if d.store.IsTrusted(repoKey) {
		return TrustedByUser, repoKey
	}
	if IsLocalhostRemote(remoteURL) {
		return AlwaysTrusted, repoKey
	}
	if d.matchesOwnRemotes(repoKey) {
		return AlwaysTrusted, repoKey
	}
	return Untrusted, repoKey
}

func (d *ProjectTrustDetector) remoteOrigin(projectDir string) string {
	if d.RemoteOrigin != nil {
		return d.RemoteOrigin(projectDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", projectDir, "remote", "get-url", "origin").Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			log.Printf("skill_trust: git remote lookup timed out for %s; treating as trusted", projectDir)
		}
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (d *ProjectTrustDetector) matchesOwnRemotes(repoKey string) bool {
	var patterns []string
	if raw := os.Getenv(EnvOwnRemotes); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				patterns = append(patterns, p)
			}
		}
	}

	hiveHome := d.HiveHome
	if hiveHome == "" {
		if home, err := hivehome.Resolve(); err == nil {
			hiveHome = home.Root
		}
	}
	if hiveHome != "" {
		if data, err := os.ReadFile(filepath.Join(hiveHome, "own_remotes")); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					patterns = append(patterns, line)
				}
			}
		}
	}

	for _, pattern := range patterns {
		if globMatch(pattern, repoKey) {
			return true
		}
	}
	return false
}

func NormalizeRemoteURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)

	if strings.HasPrefix(rawURL, "git@") && strings.Contains(rawURL, ":") && !strings.Contains(rawURL, "://") {
		rawURL = strings.TrimPrefix(rawURL, "git@")
		rawURL = strings.Replace(rawURL, ":", "/", 1)
	} else if strings.Contains(rawURL, "://") {
		if parsed, err := url.Parse(rawURL); err == nil {
			host := parsed.Hostname()
			path := strings.TrimPrefix(parsed.Path, "/")
			rawURL = host + "/" + path
		}
	}

	rawURL = strings.TrimSuffix(rawURL, ".git")
	return strings.ToLower(strings.Trim(rawURL, "/"))
}

func IsLocalhostRemote(remoteURL string) bool {
	localHosts := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	if strings.Contains(remoteURL, "://") {
		parsed, err := url.Parse(remoteURL)
		if err != nil {
			return false
		}
		return localHosts[strings.ToLower(parsed.Hostname())]
	}
	if idx := strings.Index(remoteURL, "@"); idx >= 0 {
		hostPart := remoteURL[idx+1:]
		if colon := strings.Index(hostPart, ":"); colon >= 0 {
			hostPart = hostPart[:colon]
		}
		return localHosts[strings.ToLower(hostPart)]
	}
	return false
}

func globMatch(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("(?s)^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := i + 1
			if j < len(pattern) && (pattern[j] == '!' || pattern[j] == '^') {
				j++
			}
			if j < len(pattern) && pattern[j] == ']' {
				j++
			}
			for j < len(pattern) && pattern[j] != ']' {
				j++
			}
			if j >= len(pattern) {
				b.WriteString(`\[`)
				continue
			}
			class := pattern[i+1 : j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			class = strings.ReplaceAll(class, `\`, `\\`)
			b.WriteString("[" + class + "]")
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(name)
}

type TrustGate struct {
	store       *TrustedRepoStore
	detector    *ProjectTrustDetector
	interactive bool

	PrintFn            func(string)
	InputFn            func(prompt string) (string, error)
	StdinIsTTY         func() bool
	StdoutIsTTY        func() bool
	NoticeSentinelPath string
}

func NewTrustGate(store *TrustedRepoStore, interactive bool) *TrustGate {
	if store == nil {
		store = NewTrustedRepoStore("")
	}
	gate := &TrustGate{
		store:       store,
		detector:    NewProjectTrustDetector(store),
		interactive: interactive,
	}
	gate.PrintFn = func(line string) { fmt.Println(line) }
	gate.InputFn = func(prompt string) (string, error) {
		fmt.Print(prompt)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimSpace(line), err
	}
	gate.StdinIsTTY = func() bool { return isTTY(os.Stdin) }
	gate.StdoutIsTTY = func() bool { return isTTY(os.Stdout) }
	gate.NoticeSentinelPath = defaultNoticeSentinelPath()
	return gate
}

func (g *TrustGate) FilterAndGate(skills []*ParsedSkill, projectDir string) []*ParsedSkill {
	bypassScopes := map[string]bool{
		"framework": true,
		"preset":    true,
		"user":      true,
		"queen_ui":  true,
		"colony_ui": true,
	}

	var alwaysTrusted, projectSkills []*ParsedSkill
	for _, skill := range skills {
		if bypassScopes[skill.SourceScope] {
			alwaysTrusted = append(alwaysTrusted, skill)
		} else if skill.SourceScope == "project" {
			projectSkills = append(projectSkills, skill)
		}
	}

	if len(projectSkills) == 0 {
		return alwaysTrusted
	}

	if strings.TrimSpace(os.Getenv(EnvTrustAll)) == "1" {
		log.Printf("skill_trust: %s=1 set; trusting %d project skill(s) without consent", EnvTrustAll, len(projectSkills))
		return append(alwaysTrusted, projectSkills...)
	}

	classification, repoKey := g.detector.Classify(projectDir)

	if classification == AlwaysTrusted || classification == TrustedByUser {
		log.Printf("skill_trust: project skills trusted classification=%s repo=%s count=%d",
			classification, repoLabel(repoKey), len(projectSkills))
		return append(alwaysTrusted, projectSkills...)
	}

	if !g.interactive || !g.StdinIsTTY() {
		label := projectDir
		if label == "" {
			label = "."
		}
		log.Printf("skill_trust: skipping %d project-scope skill(s) from untrusted repo %q "+
			"(non-interactive mode). To trust permanently run: hive skill trust %s",
			len(projectSkills), repoKey, label)
		log.Printf("skill_trust_decision repo=%s skills=%d decision=denied mode=headless", repoKey, len(projectSkills))
		return alwaysTrusted
	}

	decision := g.runConsentFlow(projectSkills, projectDir, repoKey)
	log.Printf("skill_trust_decision repo=%s skills=%d decision=%s mode=interactive", repoKey, len(projectSkills), decision)

	switch decision {
	case "session":
		return append(alwaysTrusted, projectSkills...)
	case "permanent":
		g.store.Trust(repoKey, projectDir)
		return append(alwaysTrusted, projectSkills...)
	default:
		return alwaysTrusted
	}
}

func (g *TrustGate) runConsentFlow(projectSkills []*ParsedSkill, projectDir, repoKey string) string {
	g.maybeShowSecurityNotice()
	g.printConsentPrompt(projectSkills, projectDir, repoKey)
	return g.promptConsent()
}

func (g *TrustGate) maybeShowSecurityNotice() {
	sentinel := g.NoticeSentinelPath
	if sentinel != "" {
		if _, err := os.Stat(sentinel); err == nil {
			return
		}
	}
	g.PrintFn("")
	g.PrintFn("Security notice: Skills inject instructions into the agent's system prompt.")
	g.PrintFn("  Only load skills from sources you trust. Registry skills at tier 'verified' or 'official' have been audited.")
	g.PrintFn("")
	if sentinel != "" {
		_ = os.MkdirAll(filepath.Dir(sentinel), 0o755)
		_ = os.WriteFile(sentinel, nil, 0o644)
	}
}

func (g *TrustGate) printConsentPrompt(projectSkills []*ParsedSkill, projectDir, repoKey string) {
	p := g.PrintFn
	p("")
	p(strings.Repeat("=", 60))
	p("  SKILL TRUST REQUIRED")
	p(strings.Repeat("=", 60))
	p("")
	projLabel := projectDir
	if projLabel == "" {
		projLabel = "this project"
	}
	p(fmt.Sprintf("  The project at %s wants to load %d skill(s)", projLabel, len(projectSkills)))
	p("  that will inject instructions into the agent's system prompt.")
	if repoKey != "" {
		p("  Source: " + repoKey)
	}
	p("")
	p("  Skills requesting access:")
	for _, skill := range projectSkills {
		p("    • " + skill.Name)
		p(fmt.Sprintf("      %q", skill.Description))
		p("      " + skill.Location)
	}
	p("")
	p("  Options:")
	p("    1) Trust this session only")
	p("    2) Trust permanently  — remember for future runs")
	p("    3) Deny              — skip all project-scope skills from this repo")
	p(strings.Repeat("─", 60))
}

func (g *TrustGate) promptConsent() string {
	mapping := map[string]string{"1": "session", "2": "permanent", "3": "denied"}
	for {
		choice, err := g.InputFn("Select option (1-3): ")
		if err != nil {
			return "denied"
		}
		if decision, ok := mapping[strings.TrimSpace(choice)]; ok {
			return decision
		}
		g.PrintFn("Invalid choice. Enter 1, 2, or 3.")
	}
}

func repoLabel(repoKey string) string {
	if repoKey == "" {
		return "(no remote)"
	}
	return repoKey
}

func isTTY(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

func defaultNoticeSentinelPath() string {
	if home, err := hivehome.Resolve(); err == nil {
		return filepath.Join(home.Root, ".skill_trust_notice_shown")
	}
	return filepath.Join(os.TempDir(), ".skill_trust_notice_shown")
}
