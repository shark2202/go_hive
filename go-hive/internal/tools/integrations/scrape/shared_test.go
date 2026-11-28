package scrape

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestResolveArtifactDir pins the artifact resolution order, including "~"
// expansion in either variable and the process-home fallback.
// Parity-exempt: the upstream fixtures only export HIVE_STORAGE_PATH; no test
// pins the resolution order both Python resolvers document.
func TestResolveArtifactDir(t *testing.T) {
	home := t.TempDir()
	env := func(values map[string]string) storageEnv {
		return storageEnv{
			getenv:      func(name string) string { return values[name] },
			userHomeDir: func() (string, error) { return home, nil },
		}
	}

	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{
			name:   "storage wins over hive home",
			values: map[string]string{"HIVE_STORAGE_PATH": filepath.Join(home, "srv", "storage"), "HIVE_HOME": filepath.Join(home, "srv", "hive")},
			want:   filepath.Join(home, "srv", "storage", "data"),
		},
		{
			name:   "storage expands tilde",
			values: map[string]string{"HIVE_STORAGE_PATH": "~/sessions"},
			want:   filepath.Join(home, "sessions", "data"),
		},
		{
			name:   "hive home absolute",
			values: map[string]string{"HIVE_HOME": filepath.Join(home, "opt", "hive")},
			want:   filepath.Join(home, "opt", "hive", "tool-artifacts"),
		},
		{
			name:   "hive home expands tilde",
			values: map[string]string{"HIVE_HOME": "~/hive-root"},
			want:   filepath.Join(home, "hive-root", "tool-artifacts"),
		},
		{
			name:   "bare tilde",
			values: map[string]string{"HIVE_HOME": "~"},
			want:   filepath.Join(home, "tool-artifacts"),
		},
		{
			name:   "defaults to dot hive",
			values: nil,
			want:   filepath.Join(home, ".hive", "tool-artifacts"),
		},
		{
			name:   "blank values are ignored",
			values: map[string]string{"HIVE_STORAGE_PATH": "   ", "HIVE_HOME": ""},
			want:   filepath.Join(home, ".hive", "tool-artifacts"),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := resolveArtifactDir(env(testCase.values))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("dir = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity-exempt: Go-side fail-closed guard (Python falls back to Path.home(),
// which cannot fail the same way); no upstream test.
func TestResolveArtifactDirHomeError(t *testing.T) {
	_, err := resolveArtifactDir(storageEnv{
		getenv:      func(string) string { return "" },
		userHomeDir: func() (string, error) { return "", errors.New("no home") },
	})
	if err == nil {
		t.Fatal("missing home must be an error, not a relative artifact dir")
	}
}

// TestIsInternalIPIPv6 covers the IPv6 half of the shared deny list:
// loopback, unspecified, ULA, link-local, multicast and the reserved prefixes
// Go still reports as global unicast.
// Parity: tools/tests/tools/test_web_scrape_tool.py::TestIsInternalAddress::test_classification
// (extended with the IPv6 and reserved-range cases the shared predicate adds).
func TestIsInternalIPIPv6(t *testing.T) {
	cases := map[string]bool{
		"::1":                      true,
		"::":                       true,
		"fd00::1":                  true,
		"fc00::1":                  true,
		"fe80::1":                  true,
		"ff02::1":                  true,
		"2001:db8::1":              true,
		"100::1":                   true,
		"64:ff9b:1::1":             true,
		"::ffff:127.0.0.1":         true,
		"::ffff:8.8.8.8":           false,
		"2606:4700:4700::1111":     false,
		"2607:f8b0:4004:800::200e": false,
	}
	for address, want := range cases {
		ip := parseIPForTest(address)
		if ip == nil {
			t.Fatalf("parse %q", address)
		}
		if got := isInternalIP(ip); got != want {
			t.Fatalf("internal(%q) = %v, want %v", address, got, want)
		}
	}
}

// TestURLPolicyZoneSuffix keeps an IPv6 zone suffix from smuggling a
// link-local address past the resolved-address check.
// Parity-exempt: Go-side hardening; Python strips no zone in a URL test, and no
// upstream fixture covers a zoned answer.
func TestURLPolicyZoneSuffix(t *testing.T) {
	policy := urlPolicy{resolver: func(context.Context, string) ([]string, error) {
		return []string{"fe80::1%en0"}, nil
	}}
	if reason := policy.check(context.Background(), "https://zoned.example/"); reason == "" {
		t.Fatal("zoned link-local resolution must be blocked")
	}
	if policy.allows(context.Background(), "https://zoned.example/") {
		t.Fatal("allows must agree with check")
	}
}
