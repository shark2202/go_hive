package cli

// Parity-exempt: Go-only listen-address guard; no upstream test.

import "testing"

// Parity-exempt: Go-only listen-address guard; no upstream test.
func TestLoopbackListenHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "[::1]", "localhost", " 127.0.0.2 "} {
		if !isLoopbackListenHost(host) {
			t.Fatalf("isLoopbackListenHost(%q) = false", host)
		}
	}
	for _, host := range []string{"", "0.0.0.0", "192.168.1.10", "::", "example.com"} {
		if isLoopbackListenHost(host) {
			t.Fatalf("isLoopbackListenHost(%q) = true", host)
		}
	}
}

// Parity-exempt: Go-only wildcard listen policy; no upstream test.
func TestWildcardListenDoesNotRequireDesktopToken(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "[::]"} {
		if requiresDesktopToken(host) {
			t.Fatalf("requiresDesktopToken(%q) = true, want false", host)
		}
	}
	for _, host := range []string{"127.0.0.1", "localhost"} {
		if requiresDesktopToken(host) {
			t.Fatalf("requiresDesktopToken(%q) = true, want false", host)
		}
	}
	for _, host := range []string{"192.168.1.10", "example.com", ""} {
		if !requiresDesktopToken(host) {
			t.Fatalf("requiresDesktopToken(%q) = false, want true", host)
		}
	}
}
