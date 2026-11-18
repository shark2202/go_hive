package llm

import (
	"strings"
	"testing"
)

func setPoolClock(pool *KeyPool, seconds float64) {
	pool.now = func() float64 { return seconds }
}

// Parity-exempt: core/framework/llm/key_pool.py has no dedicated Python test
func TestNewKeyPoolRequiresKeys(t *testing.T) {
	if _, err := NewKeyPool(nil); err == nil {
		t.Fatal("expected error for empty keys")
	} else if !strings.Contains(err.Error(), "KeyPool requires at least one key") {
		t.Errorf("error = %v", err)
	}
	pool, err := NewKeyPool([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if pool.Size() != 3 {
		t.Errorf("size = %d, want 3", pool.Size())
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestGetKeyRotatesRoundRobin(t *testing.T) {
	pool, _ := NewKeyPool([]string{"k1", "k2", "k3"})
	setPoolClock(pool, 100)
	want := []string{"k1", "k2", "k3", "k1", "k2"}
	for i, expected := range want {
		if got := pool.GetKey(); got != expected {
			t.Fatalf("call %d = %q, want %q", i, got, expected)
		}
	}
	stats := pool.GetStats()
	for _, key := range []string{"...k1", "...k2"} {
		if stats[key]["requests"].(int) != 2 {
			t.Errorf("%s requests = %v, want 2", key, stats[key]["requests"])
		}
	}
	if stats["...k3"]["requests"].(int) != 1 {
		t.Errorf("k3 requests = %v, want 1", stats["...k3"]["requests"])
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestGetKeySkipsRateLimitedKeys(t *testing.T) {
	pool, _ := NewKeyPool([]string{"k1", "k2", "k3"})
	setPoolClock(pool, 100)
	pool.MarkRateLimited("k2", 30)
	if got := pool.GetKey(); got != "k1" {
		t.Fatalf("first = %q", got)
	}
	if got := pool.GetKey(); got != "k3" {
		t.Fatalf("second = %q", got)
	}
	if got := pool.GetKey(); got != "k1" {
		t.Fatalf("third = %q", got)
	}
	stats := pool.GetStats()
	if stats["...k2"]["healthy"].(bool) {
		t.Error("k2 should be cooling down")
	}
	if !stats["...k1"]["healthy"].(bool) || !stats["...k3"]["healthy"].(bool) {
		t.Error("k1 and k3 should be healthy")
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestGetKeyReturnsSoonestExpiryWhenAllLimited(t *testing.T) {
	pool, _ := NewKeyPool([]string{"k1", "k2", "k3"})
	setPoolClock(pool, 100)
	pool.MarkRateLimited("k1", 50)
	pool.MarkRateLimited("k2", 10)
	pool.MarkRateLimited("k3", 10)
	if got := pool.GetKey(); got != "k2" {
		t.Errorf("got %q, want k2 (first with soonest expiry)", got)
	}
	stats := pool.GetStats()
	if stats["...k2"]["requests"].(int) != 1 {
		t.Errorf("k2 requests = %v", stats["...k2"]["requests"])
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestKeysBecomeHealthyAfterCooldown(t *testing.T) {
	now := 100.0
	pool, _ := NewKeyPool([]string{"k1", "k2"})
	pool.now = func() float64 { return now }
	pool.MarkRateLimited("k1", 5)
	if got := pool.GetKey(); got != "k2" {
		t.Fatalf("while cooling down = %q", got)
	}
	now = 106
	if got := pool.GetKey(); got != "k1" {
		t.Errorf("after cooldown = %q, want k1", got)
	}
	if !pool.GetStats()["...k1"]["healthy"].(bool) {
		t.Error("k1 should be healthy after cooldown")
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestMarkSuccessResetsErrors(t *testing.T) {
	pool, _ := NewKeyPool([]string{"k1"})
	setPoolClock(pool, 100)
	pool.MarkRateLimited("k1", 10)
	pool.MarkSuccess("k1")
	stats := pool.GetStats()["...k1"]
	if stats["consecutive_errors"].(int) != 0 {
		t.Errorf("consecutive_errors = %v, want 0", stats["consecutive_errors"])
	}
	if stats["successes"].(int) != 1 {
		t.Errorf("successes = %v, want 1", stats["successes"])
	}
	if stats["healthy"].(bool) {
		t.Error("mark_success clears errors but not the cooldown")
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestUnknownKeyIsIgnored(t *testing.T) {
	pool, _ := NewKeyPool([]string{"k1"})
	setPoolClock(pool, 100)
	pool.MarkRateLimited("missing", 10)
	pool.MarkSuccess("missing")
	stats := pool.GetStats()
	if len(stats) != 1 {
		t.Errorf("stats = %v, want only k1", stats)
	}
	if stats["...k1"]["consecutive_errors"].(int) != 0 {
		t.Error("unknown key should not touch health")
	}
}

// Parity-exempt: see TestNewKeyPoolRequiresKeys.
func TestStatsKeyShortKeysWholeString(t *testing.T) {
	pool, _ := NewKeyPool([]string{"short", "a-longer-secret"})
	setPoolClock(pool, 100)
	stats := pool.GetStats()
	if _, ok := stats["...short"]; !ok {
		t.Errorf("short key stat missing: %v", stats)
	}
	if _, ok := stats["...secret"]; !ok {
		t.Errorf("long key stat should use last six chars: %v", stats)
	}
}
