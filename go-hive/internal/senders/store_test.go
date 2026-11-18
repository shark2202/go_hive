package senders

// The local sender store has no Python counterpart: upstream keeps the pool in
// Aden cloud (registry.py) and only reads it. These tests pin the local-file
// behaviour the tool surface depends on.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testClock() func() time.Time {
	fixed := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}

// Parity-exempt: the pool is cloud-backed upstream (registry.py), so there is
// no Python store test; this pins create/reload and the 0600 secret handling.
func TestStoreCreatesAndReloadsSenders(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "senders")
	store := OpenStore(dir)

	created, err := store.Create(Sender{
		Name:      "Richard",
		Provider:  ProviderSMTP,
		AuthType:  AuthAPIKey,
		FromEmail: "richard@example.com",
		Secret:    map[string]any{"password": "app-password"},
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created sender has no id")
	}
	if created.Weight != 1 {
		t.Fatalf("weight = %d, want the default 1", created.Weight)
	}

	reloaded, err := OpenStore(dir).Get("richard")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded == nil || reloaded.ID != created.ID {
		t.Fatalf("reloaded = %+v, want id %s", reloaded, created.ID)
	}

	info, err := os.Stat(filepath.Join(dir, sendersFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("senders.json perm = %o, want 600 (it holds passwords)", info.Mode().Perm())
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("senders dir perm = %o, want 700", info.Mode().Perm())
	}
}

// Parity-exempt: SenderRegistry.get resolves by id then case-insensitive name;
// upstream has no test for it. This pins the same lookup rules locally.
func TestStoreResolvesByIDAndCaseInsensitiveName(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "senders"))
	created, err := store.Create(Sender{Name: "Outbound One", Provider: ProviderSMTP, FromEmail: "one@example.com", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	byID, err := store.Get(created.ID)
	if err != nil || byID == nil {
		t.Fatalf("get by id: %v %+v", err, byID)
	}
	byName, err := store.Get("  outbound ONE ")
	if err != nil || byName == nil || byName.ID != created.ID {
		t.Fatalf("get by name = %+v (err %v)", byName, err)
	}
	missing, err := store.Get("nobody")
	if err != nil || missing != nil {
		t.Fatalf("missing sender = %+v (err %v), want nil", missing, err)
	}
}

// Parity-exempt: no Python counterpart; pins before/after reporting used by
// adjust_sender's `changed` field.
func TestStoreUpdateReturnsBeforeAndAfter(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "senders"))
	created, err := store.Create(Sender{Name: "One", Provider: ProviderSMTP, FromEmail: "one@example.com", Enabled: true, Weight: 1})
	if err != nil {
		t.Fatal(err)
	}

	before, after, err := store.Update(created.ID, func(row *Sender) {
		row.Weight = 4
		row.Enabled = false
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if before.Weight != 1 || after.Weight != 4 {
		t.Fatalf("before/after weight = %d/%d", before.Weight, after.Weight)
	}
	if after.Enabled {
		t.Fatal("update did not persist enabled=false")
	}
	if _, _, err := store.Update("missing", func(*Sender) {}); err == nil {
		t.Fatal("updating a missing sender must fail")
	}
}

// Parity-exempt: no Python counterpart; list(include_disabled) is the local
// equivalent of SenderRegistry.list.
func TestStoreListFiltersDisabled(t *testing.T) {
	store := OpenStore(filepath.Join(t.TempDir(), "senders"))
	if _, err := store.Create(Sender{Name: "On", Provider: ProviderSMTP, FromEmail: "on@example.com", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Sender{Name: "Off", Provider: ProviderSMTP, FromEmail: "off@example.com", Enabled: false}); err != nil {
		t.Fatal(err)
	}

	enabled, err := store.List(false)
	if err != nil || len(enabled) != 1 || enabled[0].Name != "On" {
		t.Fatalf("enabled = %+v (err %v)", enabled, err)
	}
	all, err := store.List(true)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v (err %v)", all, err)
	}
}
