package skills

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTrustSkill(name, scope string) *ParsedSkill {
	return &ParsedSkill{
		Name:        name,
		Description: "Test skill",
		Location:    "/fake/" + name + "/SKILL.md",
		BaseDir:     "/fake/" + name,
		SourceScope: scope,
		Body:        "Test skill instructions.",
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_ssh_scp_format
func TestNormalizeRemoteURLSCP(t *testing.T) {
	if got := NormalizeRemoteURL("git@github.com:org/repo.git"); got != "github.com/org/repo" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_https_format
func TestNormalizeRemoteURLHTTPS(t *testing.T) {
	if got := NormalizeRemoteURL("https://github.com/org/repo.git"); got != "github.com/org/repo" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_https_no_dot_git
func TestNormalizeRemoteURLNoDotGit(t *testing.T) {
	if got := NormalizeRemoteURL("https://github.com/org/repo"); got != "github.com/org/repo" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_ssh_url_format
func TestNormalizeRemoteURLSSHURL(t *testing.T) {
	if got := NormalizeRemoteURL("ssh://git@github.com/org/repo.git"); got != "github.com/org/repo" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_lowercased
func TestNormalizeRemoteURLLowercased(t *testing.T) {
	if got := NormalizeRemoteURL("git@GitHub.COM:Org/Repo.git"); got != "github.com/org/repo" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_trailing_slash_stripped
func TestNormalizeRemoteURLTrailingSlash(t *testing.T) {
	if got := NormalizeRemoteURL("https://github.com/org/repo/"); got != "github.com/org/repo" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestNormalizeRemoteUrl::test_gitlab
func TestNormalizeRemoteURLGitLab(t *testing.T) {
	if got := NormalizeRemoteURL("git@gitlab.com:team/project.git"); got != "gitlab.com/team/project" {
		t.Fatalf("got = %q", got)
	}
}

// Parity: tests/test_skill_trust.py::TestIsLocalhostRemote::test_localhost_https
func TestIsLocalhostRemoteHTTP(t *testing.T) {
	if !IsLocalhostRemote("http://localhost/org/repo") {
		t.Fatal("want localhost")
	}
}

// Parity: tests/test_skill_trust.py::TestIsLocalhostRemote::test_127_0_0_1
func TestIsLocalhostRemoteLoopback(t *testing.T) {
	if !IsLocalhostRemote("https://127.0.0.1/repo") {
		t.Fatal("want localhost")
	}
}

// Parity: tests/test_skill_trust.py::TestIsLocalhostRemote::test_github_not_local
func TestIsLocalhostRemoteGitHub(t *testing.T) {
	if IsLocalhostRemote("https://github.com/org/repo") {
		t.Fatal("github should not be localhost")
	}
}

// Parity: tests/test_skill_trust.py::TestIsLocalhostRemote::test_scp_localhost
func TestIsLocalhostRemoteSCP(t *testing.T) {
	if !IsLocalhostRemote("git@localhost:org/repo") {
		t.Fatal("want localhost")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_empty_store_is_not_trusted
func TestTrustedStoreEmptyNotTrusted(t *testing.T) {
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "trusted.json"))
	if store.IsTrusted("github.com/org/repo") {
		t.Fatal("empty store should not trust")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_trust_and_lookup
func TestTrustedStoreTrustAndLookup(t *testing.T) {
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "trusted.json"))
	store.Trust("github.com/org/repo", "/some/path")
	if !store.IsTrusted("github.com/org/repo") {
		t.Fatal("want trusted")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_revoke
func TestTrustedStoreRevoke(t *testing.T) {
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "trusted.json"))
	store.Trust("github.com/org/repo", "")
	if !store.Revoke("github.com/org/repo") {
		t.Fatal("revoke should return true")
	}
	if store.IsTrusted("github.com/org/repo") {
		t.Fatal("still trusted after revoke")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_revoke_nonexistent_returns_false
func TestTrustedStoreRevokeMissing(t *testing.T) {
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "trusted.json"))
	if store.Revoke("github.com/nobody/nowhere") {
		t.Fatal("revoke of missing key should return false")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_persists_across_instances
func TestTrustedStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted.json")
	NewTrustedRepoStore(path).Trust("github.com/org/repo", "")
	if !NewTrustedRepoStore(path).IsTrusted("github.com/org/repo") {
		t.Fatal("trust not persisted")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_atomic_write
func TestTrustedStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trusted.json")
	NewTrustedRepoStore(path).Trust("github.com/org/repo", "")
	if _, err := os.Stat(filepath.Join(dir, "trusted.tmp")); err == nil {
		t.Fatal("leftover tmp file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file missing: %v", err)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_corrupted_json_recovers_gracefully
func TestTrustedStoreCorruptRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted.json")
	if err := os.WriteFile(path, []byte("{not valid json{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewTrustedRepoStore(path)
	if store.IsTrusted("github.com/any/repo") {
		t.Fatal("corrupt store should be empty")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_json_schema
func TestTrustedStoreJSONSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trusted.json")
	NewTrustedRepoStore(path).Trust("github.com/org/repo", "/work/repo")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"] != float64(1) {
		t.Fatalf("version = %v", raw["version"])
	}
	entries, _ := raw["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	entry, _ := entries[0].(map[string]any)
	if entry["repo_key"] != "github.com/org/repo" {
		t.Fatalf("repo_key = %v", entry["repo_key"])
	}
	if _, ok := entry["added_at"]; !ok {
		t.Fatal("added_at missing")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustedRepoStore::test_list_entries
func TestTrustedStoreListEntries(t *testing.T) {
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	store.Trust("github.com/a/b", "")
	store.Trust("github.com/c/d", "")
	entries := store.ListEntries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].RepoKey != "github.com/a/b" || entries[1].RepoKey != "github.com/c/d" {
		t.Fatalf("order = %s, %s", entries[0].RepoKey, entries[1].RepoKey)
	}
}

func newTrustDetector(t *testing.T, remote string) (*ProjectTrustDetector, *TrustedRepoStore) {
	t.Helper()
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	det := NewProjectTrustDetector(store)
	det.RemoteOrigin = func(string) string { return remote }
	det.HiveHome = t.TempDir()
	return det, store
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_none_project_dir_always_trusted
func TestDetectorEmptyDirAlwaysTrusted(t *testing.T) {
	det, _ := newTrustDetector(t, "")
	if cls, _ := det.Classify(""); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_nonexistent_dir_always_trusted
func TestDetectorMissingDirAlwaysTrusted(t *testing.T) {
	det, _ := newTrustDetector(t, "")
	if cls, _ := det.Classify(filepath.Join(t.TempDir(), "nonexistent")); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_no_git_dir_always_trusted
func TestDetectorNoGitAlwaysTrusted(t *testing.T) {
	det, _ := newTrustDetector(t, "")
	if cls, _ := det.Classify(t.TempDir()); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_no_remote_always_trusted
func TestDetectorNoRemoteAlwaysTrusted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	det, _ := newTrustDetector(t, "")
	if cls, _ := det.Classify(dir); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_localhost_remote_always_trusted
func TestDetectorLocalhostAlwaysTrusted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	det, _ := newTrustDetector(t, "http://localhost/org/repo.git")
	if cls, _ := det.Classify(dir); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_trusted_by_store
func TestDetectorTrustedByStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	det, store := newTrustDetector(t, "git@github.com:trusted/repo.git")
	store.Trust("github.com/trusted/repo", "")
	cls, key := det.Classify(dir)
	if cls != TrustedByUser {
		t.Fatalf("classification = %q", cls)
	}
	if key != "github.com/trusted/repo" {
		t.Fatalf("key = %q", key)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_unknown_remote_untrusted
func TestDetectorUnknownRemoteUntrusted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	det, _ := newTrustDetector(t, "https://github.com/stranger/repo.git")
	cls, key := det.Classify(dir)
	if cls != Untrusted {
		t.Fatalf("classification = %q", cls)
	}
	if key != "github.com/stranger/repo" {
		t.Fatalf("key = %q", key)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_own_remotes_env_var
func TestDetectorOwnRemotesEnv(t *testing.T) {
	t.Setenv(EnvOwnRemotes, "github.com/myorg/*")
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	det, _ := newTrustDetector(t, "git@github.com:myorg/myrepo.git")
	if cls, _ := det.Classify(dir); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_git_timeout_treated_as_trusted
func TestDetectorGitTimeoutTrusted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	det := NewProjectTrustDetector(store)
	det.RemoteOrigin = func(string) string { return "" }
	det.HiveHome = t.TempDir()
	if cls, _ := det.Classify(dir); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

// Parity: tests/test_skill_trust.py::TestProjectTrustDetector::test_git_not_found_treated_as_trusted
func TestDetectorGitNotFoundTrusted(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	det := NewProjectTrustDetector(store)
	det.RemoteOrigin = func(string) string { return "" }
	det.HiveHome = t.TempDir()
	if cls, _ := det.Classify(dir); cls != AlwaysTrusted {
		t.Fatalf("classification = %q", cls)
	}
}

func newInteractiveGate(t *testing.T, store *TrustedRepoStore, input string, lines *[]string) *TrustGate {
	t.Helper()
	gate := NewTrustGate(store, true)
	gate.PrintFn = func(line string) {
		if lines != nil {
			*lines = append(*lines, line)
		}
	}
	gate.InputFn = func(string) (string, error) { return input, nil }
	gate.StdinIsTTY = func() bool { return true }
	gate.StdoutIsTTY = func() bool { return true }
	gate.NoticeSentinelPath = filepath.Join(t.TempDir(), ".skill_trust_notice_shown")
	return gate
}

func gitProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_framework_scope_always_passes
func TestGateFrameworkScopePasses(t *testing.T) {
	gate := NewTrustGate(NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json")), false)
	skill := makeTrustSkill("fw-skill", "framework")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, "")
	if len(result) != 1 || result[0].Name != "fw-skill" {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_user_scope_always_passes
func TestGateUserScopePasses(t *testing.T) {
	gate := NewTrustGate(NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json")), false)
	skill := makeTrustSkill("user-skill", "user")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, "")
	if len(result) != 1 || result[0].Name != "user-skill" {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_no_project_skills_returns_early
func TestGateNoProjectSkillsEarlyReturn(t *testing.T) {
	gate := NewTrustGate(NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json")), false)
	fw := makeTrustSkill("fw", "framework")
	result := gate.FilterAndGate([]*ParsedSkill{fw}, t.TempDir())
	if len(result) != 1 || result[0] != fw {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_trusted_project_skills_pass
func TestGateTrustedProjectPasses(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	store.Trust("github.com/trusted/repo", "")
	gate := NewTrustGate(store, false)
	gate.detector.RemoteOrigin = func(string) string { return "git@github.com:trusted/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	skill := makeTrustSkill("proj-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, dir)
	if len(result) != 1 || result[0].Name != "proj-skill" {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_untrusted_headless_skips_and_logs
func TestGateUntrustedHeadlessSkips(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := NewTrustGate(store, false)
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/evil.git" }
	gate.detector.HiveHome = t.TempDir()
	skill := makeTrustSkill("evil-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, dir)
	if len(result) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_interactive_consent_session_only
func TestGateConsentSession(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := newInteractiveGate(t, store, "1", nil)
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	skill := makeTrustSkill("session-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, dir)
	if len(result) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if store.IsTrusted("github.com/stranger/repo") {
		t.Fatal("session consent must not persist")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_interactive_consent_permanent
func TestGateConsentPermanent(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := newInteractiveGate(t, store, "2", nil)
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	skill := makeTrustSkill("perm-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, dir)
	if len(result) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if !store.IsTrusted("github.com/stranger/repo") {
		t.Fatal("permanent consent must persist")
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_interactive_consent_deny
func TestGateConsentDeny(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := newInteractiveGate(t, store, "3", nil)
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	skill := makeTrustSkill("bad-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, dir)
	if len(result) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_env_var_override_trusts_all
func TestGateEnvOverrideTrustsAll(t *testing.T) {
	t.Setenv(EnvTrustAll, "1")
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := NewTrustGate(store, false)
	skill := makeTrustSkill("env-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, t.TempDir())
	if len(result) != 1 || result[0].Name != "env-skill" {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_keyboard_interrupt_treated_as_deny
func TestGateInterruptDenies(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := newInteractiveGate(t, store, "3", nil)
	gate.InputFn = func(string) (string, error) { return "", errors.New("interrupted") }
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	skill := makeTrustSkill("interrupted-skill", "project")
	result := gate.FilterAndGate([]*ParsedSkill{skill}, dir)
	if len(result) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_security_notice_shown_once
func TestGateSecurityNoticeOnce(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	var lines []string
	gate := newInteractiveGate(t, store, "3", &lines)
	sentinel := gate.NoticeSentinelPath
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("sentinel exists before first run")
	}
	gate.FilterAndGate([]*ParsedSkill{makeTrustSkill("notice-skill", "project")}, dir)

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("sentinel not written: %v", err)
	}
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Security notice") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notice not printed: %v", lines)
	}

	lines = nil
	gate.PrintFn = func(line string) { lines = append(lines, line) }
	gate.FilterAndGate([]*ParsedSkill{makeTrustSkill("notice-skill-2", "project")}, dir)
	for _, line := range lines {
		if strings.Contains(line, "Security notice") {
			t.Fatal("notice shown twice")
		}
	}
}

// Parity: tests/test_skill_trust.py::TestTrustGate::test_mixed_scopes_only_project_gated
func TestGateMixedScopesOnlyProjectGated(t *testing.T) {
	dir := gitProject(t)
	store := NewTrustedRepoStore(filepath.Join(t.TempDir(), "t.json"))
	gate := newInteractiveGate(t, store, "3", nil)
	gate.detector.RemoteOrigin = func(string) string { return "https://github.com/stranger/repo.git" }
	gate.detector.HiveHome = t.TempDir()
	result := gate.FilterAndGate([]*ParsedSkill{
		makeTrustSkill("fw", "framework"),
		makeTrustSkill("usr", "user"),
		makeTrustSkill("proj", "project"),
	}, dir)
	names := map[string]bool{}
	for _, skill := range result {
		names[skill.Name] = true
	}
	if !names["fw"] || !names["usr"] {
		t.Fatalf("names = %v", names)
	}
	if names["proj"] {
		t.Fatalf("project skill should be denied: %v", names)
	}
}
