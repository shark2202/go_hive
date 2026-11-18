package sentinel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: runtime_identity.py has no Python test; these cover the env
// precedence and the persisted-local-id lifecycle.
func TestRuntimeKindExplicitAndInferred(t *testing.T) {
	identity := &RuntimeIdentity{Getenv: func(name string) string {
		switch name {
		case "HIVE_RUNTIME_KIND":
			return "Cloud"
		}
		return ""
	}}
	if got := identity.Kind(); got != "cloud" {
		t.Fatalf("explicit kind = %q, want cloud", got)
	}

	identity = &RuntimeIdentity{Getenv: func(name string) string {
		if name == "HIVE_RUNTIME_ID" {
			return "sandbox-1"
		}
		return ""
	}}
	if got := identity.Kind(); got != "cloud" {
		t.Fatalf("inferred kind = %q, want cloud", got)
	}

	identity = &RuntimeIdentity{Getenv: func(string) string { return "" }}
	if got := identity.Kind(); got != "local" {
		t.Fatalf("default kind = %q, want local", got)
	}
}

// Parity-exempt: runtime_identity.py has no Python test.
func TestRuntimeIDEnvOverrideWinsAndSkipsDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), runtimeIDFilename)
	identity := &RuntimeIdentity{IDPath: path, Getenv: func(name string) string {
		if name == "HIVE_RUNTIME_ID" {
			return "explicit-runtime"
		}
		return ""
	}}
	if got := identity.ID(); got != "explicit-runtime" {
		t.Fatalf("id = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("env-pinned id should not be persisted")
	}
}

// Parity-exempt: runtime_identity.py has no Python test.
func TestRuntimeIDPersistsLocalUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), runtimeIDFilename)
	first := &RuntimeIdentity{IDPath: path, Getenv: func(string) string { return "" }}
	id := first.ID()
	if !strings.HasPrefix(id, "local-") || len(id) != len("local-")+32 {
		t.Fatalf("id = %q, want local-<32 hex>", id)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted id: %v", err)
	}
	if strings.TrimSpace(string(data)) != id {
		t.Fatalf("persisted %q, want %q", data, id)
	}
	second := &RuntimeIdentity{IDPath: path, Getenv: func(string) string { return "" }}
	if got := second.ID(); got != id {
		t.Fatalf("restart id = %q, want %q", got, id)
	}
}

// Parity-exempt: runtime_identity.py has no Python test.
func TestRuntimeIDSandboxEnvFallback(t *testing.T) {
	identity := &RuntimeIdentity{IDPath: filepath.Join(t.TempDir(), runtimeIDFilename), Getenv: func(name string) string {
		if name == "E2B_SANDBOX_ID" {
			return "e2b-42"
		}
		return ""
	}}
	if got := identity.ID(); got != "e2b-42" {
		t.Fatalf("id = %q, want e2b-42", got)
	}
}
