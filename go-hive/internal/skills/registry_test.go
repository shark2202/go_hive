package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleRegistryIndex() map[string]any {
	return map[string]any{
		"version": float64(1),
		"skills": []any{
			map[string]any{
				"name":         "deep-research",
				"description":  "Multi-step web research with source verification.",
				"version":      "1.0.0",
				"author":       "anthropics",
				"license":      "MIT",
				"tags":         []any{"research", "web"},
				"git_url":      "https://github.com/anthropics/skills",
				"subdirectory": "deep-research",
				"trust_tier":   "official",
			},
			map[string]any{
				"name":         "code-review",
				"description":  "Automated code review for style and correctness.",
				"version":      "0.9.0",
				"author":       "contributor",
				"tags":         []any{"code", "review"},
				"git_url":      "https://github.com/contributor/code-review",
				"subdirectory": nil,
				"trust_tier":   "community",
			},
		},
		"packs": []any{
			map[string]any{
				"name":        "research-starter",
				"description": "Research-focused skill bundle",
				"skills":      []any{"deep-research"},
			},
		},
	}
}

func newTestRegistryClient(t *testing.T) (*RegistryClient, string) {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), "registry_cache")
	return NewRegistryClient("https://example.com/skill_index.json", cacheDir), cacheDir
}

func writeRegistryCache(t *testing.T, cacheDir string, lastFetched string) {
	t.Helper()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sampleRegistryIndex())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "skill_index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if lastFetched != "" {
		meta, _ := json.Marshal(map[string]any{"last_fetched": lastFetched})
		if err := os.WriteFile(filepath.Join(cacheDir, "metadata.json"), meta, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_returns_none_on_network_error
func TestRegistryFetchIndexNetworkNil(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	client.httpFetch = func(string) ([]byte, error) { return nil, nil }
	if result := client.FetchIndex(false); result != nil {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_returns_none_on_url_error
func TestRegistryFetchIndexURLError(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	client.httpFetch = func(string) ([]byte, error) { return nil, os.ErrDeadlineExceeded }
	if result := client.FetchIndex(false); result != nil {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_fetches_and_caches_index
func TestRegistryFetchIndexFetchesAndCaches(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	raw, err := json.Marshal(sampleRegistryIndex())
	if err != nil {
		t.Fatal(err)
	}
	client.httpFetch = func(string) ([]byte, error) { return raw, nil }

	result := client.FetchIndex(false)
	if result == nil {
		t.Fatal("result is nil")
	}
	skills, _ := result["skills"].([]any)
	if len(skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(skills))
	}
	if _, err := os.Stat(client.IndexPath()); err != nil {
		t.Fatalf("index cache missing: %v", err)
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_uses_fresh_cache_without_network
func TestRegistryFetchIndexUsesFreshCache(t *testing.T) {
	client, cacheDir := newTestRegistryClient(t)
	writeRegistryCache(t, cacheDir, time.Now().UTC().Format(time.RFC3339Nano))

	fetched := 0
	client.httpFetch = func(string) ([]byte, error) {
		fetched++
		return nil, nil
	}
	result := client.FetchIndex(false)
	if fetched != 0 {
		t.Fatalf("network hit %d times, want 0", fetched)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_refreshes_when_cache_is_stale
func TestRegistryFetchIndexRefreshesStale(t *testing.T) {
	client, cacheDir := newTestRegistryClient(t)
	old := time.Now().UTC().Add(-time.Duration(CacheTTLSeconds+60) * time.Second).Format(time.RFC3339Nano)
	writeRegistryCache(t, cacheDir, old)

	raw, _ := json.Marshal(sampleRegistryIndex())
	fetched := 0
	client.httpFetch = func(string) ([]byte, error) {
		fetched++
		return raw, nil
	}
	client.FetchIndex(false)
	if fetched != 1 {
		t.Fatalf("network hit %d times, want 1", fetched)
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_force_refresh_bypasses_fresh_cache
func TestRegistryFetchIndexForceRefresh(t *testing.T) {
	client, cacheDir := newTestRegistryClient(t)
	writeRegistryCache(t, cacheDir, time.Now().UTC().Format(time.RFC3339Nano))

	raw, _ := json.Marshal(sampleRegistryIndex())
	fetched := 0
	client.httpFetch = func(string) ([]byte, error) {
		fetched++
		return raw, nil
	}
	client.FetchIndex(true)
	if fetched != 1 {
		t.Fatalf("network hit %d times, want 1", fetched)
	}
}

// Parity: tests/test_skill_registry.py::TestFetchIndex::test_falls_back_to_stale_cache_on_network_error
func TestRegistryFetchIndexStaleFallback(t *testing.T) {
	client, cacheDir := newTestRegistryClient(t)
	writeRegistryCache(t, cacheDir, "")

	client.httpFetch = func(string) ([]byte, error) { return nil, nil }
	result := client.FetchIndex(false)
	if result == nil {
		t.Fatal("result is nil")
	}
	if result["version"] != float64(1) {
		t.Fatalf("version = %v", result["version"])
	}
}

func stubIndexClient(t *testing.T) *RegistryClient {
	t.Helper()
	client, _ := newTestRegistryClient(t)
	client.fetchIndexHook = func(bool) map[string]any { return sampleRegistryIndex() }
	return client
}

// Parity: tests/test_skill_registry.py::TestSearch::test_filters_by_name
func TestRegistrySearchByName(t *testing.T) {
	results := stubIndexClient(t).Search("deep")
	if len(results) != 1 || results[0]["name"] != "deep-research" {
		t.Fatalf("results = %+v", results)
	}
}

// Parity: tests/test_skill_registry.py::TestSearch::test_filters_by_description
func TestRegistrySearchByDescription(t *testing.T) {
	results := stubIndexClient(t).Search("source verification")
	found := false
	for _, r := range results {
		if r["name"] == "deep-research" {
			found = true
		}
	}
	if !found {
		t.Fatalf("results = %+v", results)
	}
}

// Parity: tests/test_skill_registry.py::TestSearch::test_filters_by_tag
func TestRegistrySearchByTag(t *testing.T) {
	results := stubIndexClient(t).Search("review")
	found := false
	for _, r := range results {
		if r["name"] == "code-review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("results = %+v", results)
	}
}

// Parity: tests/test_skill_registry.py::TestSearch::test_case_insensitive
func TestRegistrySearchCaseInsensitive(t *testing.T) {
	results := stubIndexClient(t).Search("DEEP")
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
}

// Parity: tests/test_skill_registry.py::TestSearch::test_returns_empty_when_unavailable
func TestRegistrySearchUnavailable(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	client.fetchIndexHook = func(bool) map[string]any { return nil }
	if results := client.Search("anything"); len(results) != 0 {
		t.Fatalf("results = %+v", results)
	}
}

// Parity: tests/test_skill_registry.py::TestSearch::test_returns_empty_on_no_match
func TestRegistrySearchNoMatch(t *testing.T) {
	if results := stubIndexClient(t).Search("xyzzy-no-match"); len(results) != 0 {
		t.Fatalf("results = %+v", results)
	}
}

// Parity: tests/test_skill_registry.py::TestGetSkillEntry::test_finds_by_exact_name
func TestRegistryGetSkillEntryFound(t *testing.T) {
	entry := stubIndexClient(t).GetSkillEntry("deep-research")
	if entry == nil || entry["name"] != "deep-research" {
		t.Fatalf("entry = %+v", entry)
	}
}

// Parity: tests/test_skill_registry.py::TestGetSkillEntry::test_returns_none_when_not_found
func TestRegistryGetSkillEntryNotFound(t *testing.T) {
	if entry := stubIndexClient(t).GetSkillEntry("nonexistent"); entry != nil {
		t.Fatalf("entry = %+v", entry)
	}
}

// Parity: tests/test_skill_registry.py::TestGetSkillEntry::test_returns_none_when_index_unavailable
func TestRegistryGetSkillEntryUnavailable(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	client.fetchIndexHook = func(bool) map[string]any { return nil }
	if entry := client.GetSkillEntry("deep-research"); entry != nil {
		t.Fatalf("entry = %+v", entry)
	}
}

// Parity: tests/test_skill_registry.py::TestGetPack::test_returns_skill_names
func TestRegistryGetPackReturnsNames(t *testing.T) {
	skills := stubIndexClient(t).GetPack("research-starter")
	if !reflect.DeepEqual(skills, []string{"deep-research"}) {
		t.Fatalf("skills = %+v", skills)
	}
}

// Parity: tests/test_skill_registry.py::TestGetPack::test_returns_none_when_pack_not_found
func TestRegistryGetPackNotFound(t *testing.T) {
	if result := stubIndexClient(t).GetPack("nonexistent-pack"); result != nil {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_registry.py::TestGetPack::test_returns_none_when_index_unavailable
func TestRegistryGetPackUnavailable(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	client.fetchIndexHook = func(bool) map[string]any { return nil }
	if result := client.GetPack("research-starter"); result != nil {
		t.Fatalf("result = %+v", result)
	}
}

// Parity: tests/test_skill_registry.py::TestResolveGitUrl::test_returns_git_url_and_subdirectory
func TestRegistryResolveGitURLWithSubdir(t *testing.T) {
	src := stubIndexClient(t).ResolveGitURL("deep-research")
	if src == nil {
		t.Fatal("src is nil")
	}
	if src.GitURL != "https://github.com/anthropics/skills" {
		t.Fatalf("git_url = %q", src.GitURL)
	}
	if src.Subdirectory == nil || *src.Subdirectory != "deep-research" {
		t.Fatalf("subdirectory = %v", src.Subdirectory)
	}
}

// Parity: tests/test_skill_registry.py::TestResolveGitUrl::test_returns_none_subdirectory_when_absent
func TestRegistryResolveGitURLNoSubdir(t *testing.T) {
	src := stubIndexClient(t).ResolveGitURL("code-review")
	if src == nil {
		t.Fatal("src is nil")
	}
	if src.Subdirectory != nil {
		t.Fatalf("subdirectory = %v", *src.Subdirectory)
	}
}

// Parity: tests/test_skill_registry.py::TestResolveGitUrl::test_returns_none_when_not_in_registry
func TestRegistryResolveGitURLMissing(t *testing.T) {
	if src := stubIndexClient(t).ResolveGitURL("not-there"); src != nil {
		t.Fatalf("src = %+v", src)
	}
}

// Parity: tests/test_skill_registry.py::TestCacheAtomicWrite::test_atomic_write_uses_tmp_then_replace
func TestRegistryCacheAtomicWrite(t *testing.T) {
	client, cacheDir := newTestRegistryClient(t)
	client.SaveCache(sampleRegistryIndex())

	if _, err := os.Stat(client.IndexPath()); err != nil {
		t.Fatalf("index missing: %v", err)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("leftover temp file %q", entry.Name())
		}
	}
}

// Parity: tests/test_skill_registry.py::TestCacheAtomicWrite::test_save_and_load_round_trip
func TestRegistryCacheRoundTrip(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	client.SaveCache(sampleRegistryIndex())
	if loaded := client.LoadCache(); !reflect.DeepEqual(loaded, sampleRegistryIndex()) {
		t.Fatalf("loaded = %+v", loaded)
	}
}

// Parity: tests/test_skill_registry.py::TestCacheAtomicWrite::test_load_returns_none_when_absent
func TestRegistryLoadCacheAbsent(t *testing.T) {
	client, _ := newTestRegistryClient(t)
	if loaded := client.LoadCache(); loaded != nil {
		t.Fatalf("loaded = %+v", loaded)
	}
}

// Parity-exempt: the HIVE_REGISTRY_URL env fallback has no pytest in
func TestNewRegistryClientEnvFallback(t *testing.T) {
	t.Setenv("HIVE_REGISTRY_URL", "https://example.org/custom_index.json")
	client := NewRegistryClient("", filepath.Join(t.TempDir(), "cache"))
	if client.url != "https://example.org/custom_index.json" {
		t.Fatalf("url = %q", client.url)
	}
}
