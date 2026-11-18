package senders

// Rotation ports tools/src/aden_tools/senders/rotation.py. Upstream ships no
// unit test for pick(), so these are exempt from parity targets and pin the
// documented policy semantics instead.

import (
	"path/filepath"
	"testing"
)

type memoryCursor struct{ positions map[string]int }

func newMemoryCursor() *memoryCursor {
	return &memoryCursor{positions: map[string]int{}}
}

func (m *memoryCursor) Next(key string, size int) (int, error) {
	if size <= 0 {
		return 0, nil
	}
	index := m.positions[key] % size
	m.positions[key] = (index + 1) % size
	return index, nil
}

func rotationSender(id, name string, weight int, limit *int, enabled bool) Sender {
	return Sender{
		ID:         id,
		Name:       name,
		Provider:   ProviderSMTP,
		FromEmail:  id + "@example.com",
		Weight:     weight,
		DailyLimit: limit,
		Enabled:    enabled,
	}
}

func intPtr(value int) *int { return &value }

// Parity-exempt: rotation.pick has no Python test; this pins round-robin order.
func TestRotationRoundRobinAdvances(t *testing.T) {
	pool := []Sender{
		rotationSender("a", "A", 1, nil, true),
		rotationSender("b", "B", 1, nil, true),
	}
	cursors := newMemoryCursor()
	want := []string{"a", "b", "a", "b"}
	for index, expected := range want {
		chosen, err := Pick(pool, PolicyRoundRobin, map[string]int{}, cursors)
		if err != nil {
			t.Fatalf("pick %d: %v", index, err)
		}
		if chosen == nil || chosen.ID != expected {
			t.Fatalf("pick %d = %+v, want %s", index, chosen, expected)
		}
	}
}

// Parity-exempt: rotation._eligible has no Python test; this pins the
// disabled/daily-limit exclusions that make a cap real.
func TestRotationSkipsDisabledAndExhausted(t *testing.T) {
	pool := []Sender{
		rotationSender("a", "A", 1, intPtr(2), true),
		rotationSender("b", "B", 1, nil, false),
		rotationSender("c", "C", 1, intPtr(5), true),
	}
	chosen, err := Pick(pool, PolicyRoundRobin, map[string]int{"a": 2}, newMemoryCursor())
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if chosen == nil || chosen.ID != "c" {
		t.Fatalf("chosen = %+v, want c", chosen)
	}

	none, err := Pick(pool, PolicyRoundRobin, map[string]int{"a": 2, "c": 5}, newMemoryCursor())
	if err != nil || none != nil {
		t.Fatalf("all exhausted = %+v (err %v), want nil", none, err)
	}
}

// Parity-exempt: rotation.pick's weighted branch has no Python test; this pins
// the 1:3 expansion and the persisted cursor advancing across it.
func TestRotationWeightedUsesWeights(t *testing.T) {
	pool := []Sender{
		rotationSender("a", "A", 1, nil, true),
		rotationSender("b", "B", 3, nil, true),
	}
	cursors := newMemoryCursor()
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		chosen, err := Pick(pool, PolicyWeighted, map[string]int{}, cursors)
		if err != nil || chosen == nil {
			t.Fatalf("pick %d: %+v (err %v)", i, chosen, err)
		}
		counts[chosen.ID]++
	}
	if counts["a"] != 1 || counts["b"] != 3 {
		t.Fatalf("weighted distribution = %#v, want a=1 b=3", counts)
	}
}

// Parity-exempt: rotation.pick's least_used branch has no Python test; this
// pins first-of-ties and the usage comparison.
func TestRotationLeastUsedPicksFewestSends(t *testing.T) {
	pool := []Sender{
		rotationSender("a", "A", 1, nil, true),
		rotationSender("b", "B", 1, nil, true),
	}
	chosen, err := Pick(pool, PolicyLeastUsed, map[string]int{"a": 5, "b": 2}, newMemoryCursor())
	if err != nil || chosen == nil || chosen.ID != "b" {
		t.Fatalf("least_used = %+v (err %v), want b", chosen, err)
	}

	tie, err := Pick(pool, PolicyLeastUsed, map[string]int{"a": 3, "b": 3}, newMemoryCursor())
	if err != nil || tie == nil || tie.ID != "a" {
		t.Fatalf("tie = %+v (err %v), want the first candidate", tie, err)
	}
}

// Parity-exempt: unknown policies default to round_robin upstream; no Python
// test covers the fallback.
func TestRotationUnknownPolicyFallsBackToRoundRobin(t *testing.T) {
	pool := []Sender{rotationSender("a", "A", 1, nil, true), rotationSender("b", "B", 1, nil, true)}
	cursors := newMemoryCursor()
	first, err := Pick(pool, "not-a-policy", map[string]int{}, cursors)
	if err != nil || first == nil || first.ID != "a" {
		t.Fatalf("first = %+v (err %v)", first, err)
	}
	second, err := Pick(pool, "not-a-policy", map[string]int{}, cursors)
	if err != nil || second == nil || second.ID != "b" {
		t.Fatalf("second = %+v (err %v)", second, err)
	}
}

// Parity-exempt: CursorStore is the local file equivalent of rotation.py's
// cursors; this pins persistence across Open.
func TestCursorStorePersistsAcrossReopen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "senders")
	first := OpenCursorStore(dir, testClock())
	index, err := first.Next("r:a|b", 2)
	if err != nil || index != 0 {
		t.Fatalf("first next = %d (err %v)", index, err)
	}
	second := OpenCursorStore(dir, testClock())
	index, err = second.Next("r:a|b", 2)
	if err != nil || index != 1 {
		t.Fatalf("second next = %d (err %v), want 1", index, err)
	}
}
