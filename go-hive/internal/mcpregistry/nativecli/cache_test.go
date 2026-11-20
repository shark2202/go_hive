package nativecli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Parity-exempt: direct port of mcp2cli's cache_key_for / load_cached / save_cache.
func TestCacheKeyAndRoundTrip(t *testing.T) {
	first := cacheKeyFor("srv --x", nil, "auto", nil, true)
	second := cacheKeyFor("srv --x", nil, "auto", nil, true)
	if first != second {
		t.Fatalf("cache key is not stable: %q vs %q", first, second)
	}
	if len(first) != 16 {
		t.Fatalf("cache key length = %d, want 16", len(first))
	}
	if other := cacheKeyFor("srv --y", nil, "auto", nil, true); other == first {
		t.Fatal("different sources share a cache key")
	}
	if other := cacheKeyFor("srv --x", []Field{{Key: "Authorization", Value: "t"}}, "auto", nil, true); other == first {
		t.Fatal("auth headers do not affect the cache key")
	}

	dir := t.TempDir()
	tools := []any{Ordered{{Key: "name", Value: "echo"}}}
	if err := saveCachedTools(dir, first, tools); err != nil {
		t.Fatalf("saveCachedTools: %v", err)
	}
	now := time.Now()
	loaded, ok := loadCachedTools(dir, first, DefaultCacheTTL, now)
	if !ok || len(loaded) != 1 {
		t.Fatalf("loadCachedTools = %v, %v", loaded, ok)
	}
	if _, ok := loadCachedTools(dir, first, 0, now); ok {
		t.Fatal("a zero TTL must always miss")
	}
}

// Parity-exempt: direct port of mcp2cli's cache staleness and corruption handling.
func TestCacheTreatsStaleAndCorruptEntriesAsMisses(t *testing.T) {
	dir := t.TempDir()
	key := "stale"
	path := cacheFile(dir, key)
	if err := os.WriteFile(path, []byte(`[{"name": "old"}]`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if _, ok := loadCachedTools(dir, key, 3600, time.Now()); ok {
		t.Fatal("stale entry was treated as a hit")
	}
	if _, ok := loadCachedTools(dir, key, 10*3600, time.Now()); !ok {
		t.Fatal("entry inside a longer TTL was treated as stale")
	}

	corrupt := cacheFile(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := loadCachedTools(dir, "corrupt", 3600, time.Now()); ok {
		t.Fatal("corrupt entry was treated as a hit")
	}
}

// Parity-exempt: direct port of mcp2cli's usage tracking and sort_commands.
func TestUsageSorting(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	recordUsage(dir, "hash", "alpha", now.Add(-2*time.Minute))
	recordUsage(dir, "hash", "beta", now)
	recordUsage(dir, "hash", "beta", now)

	if mode := resolveSortMode("", dir, "hash"); mode != "usage" {
		t.Fatalf("default sort = %q, want usage", mode)
	}
	if mode := resolveSortMode("", t.TempDir(), "hash"); mode != "default" {
		t.Fatalf("empty default sort = %q", mode)
	}
	if mode := resolveSortMode("alpha", dir, "hash"); mode != "alpha" {
		t.Fatalf("explicit sort = %q", mode)
	}

	commands := []Command{{Name: "alpha", ToolName: "alpha"}, {Name: "beta", ToolName: "beta"}}
	sorted := sortCommands(commands, "usage", dir, "hash")
	if sorted[0].Name != "beta" {
		t.Fatalf("usage sort = %q first", sorted[0].Name)
	}
	recent := sortCommands(commands, "recent", dir, "hash")
	if recent[0].Name != "beta" {
		t.Fatalf("recent sort = %q first", recent[0].Name)
	}
	alpha := sortCommands([]Command{{Name: "b"}, {Name: "a"}}, "alpha", dir, "hash")
	if alpha[0].Name != "a" {
		t.Fatalf("alpha sort = %q first", alpha[0].Name)
	}
}

// Parity-exempt: cache location resolution for the native CLI.
func TestDefaultCacheDirHonoursInvocationEnvironment(t *testing.T) {
	env := []string{"HOME=/tmp/x", "MCP2CLI_CACHE_DIR=/tmp/first", "MCP2CLI_CACHE_DIR=/tmp/second"}
	if got := defaultCacheDir(env); got != "/tmp/second" {
		t.Fatalf("cache dir = %q, want the last assignment", got)
	}
	t.Setenv("MCP2CLI_CACHE_DIR", "")
	t.Setenv("HIVE_HOME", filepath.Join(t.TempDir(), "hive-home"))
	if got := defaultCacheDir(nil); got == "" || filepath.Base(got) != "mcp2cli" {
		t.Fatalf("default cache dir = %q", got)
	}
}
