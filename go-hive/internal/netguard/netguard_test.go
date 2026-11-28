package netguard

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
)

// Parity-exempt: Go-only dial-time SSRF guard; no Python counterpart.
func TestBlockReason(t *testing.T) {
	resolver := func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "public.example":
			return []string{"93.184.216.34"}, nil
		case "private.example":
			return []string{"10.0.0.5"}, nil
		case "mixed.example":
			return []string{"93.184.216.34", "192.168.1.9"}, nil
		default:
			return nil, &net.DNSError{Err: "no such host", Name: host}
		}
	}
	policy := Policy{Resolver: resolver}

	cases := []struct {
		name   string
		url    string
		block  bool
		reason string
	}{
		{"public literal", "https://93.184.216.34/x", false, ""},
		{"public dns", "https://public.example/x", false, ""},
		{"loopback literal", "http://127.0.0.1/", true, "internal address"},
		{"private literal", "http://10.1.2.3/", true, "internal address"},
		{"cgnat", "http://100.64.0.1/", true, "internal address"},
		{"documentation", "http://203.0.113.9/", true, "internal address"},
		{"ipv6 loopback", "http://[::1]/", true, "internal address"},
		{"ipv4 mapped", "http://[::ffff:127.0.0.1]/", true, "internal address"},
		{"nat64 local", "http://[64:ff9b:1::1]/", true, "internal address"},
		{"private dns", "https://private.example/", true, "resolves to internal"},
		{"mixed dns", "https://mixed.example/", true, "resolves to internal"},
		{"metadata literal", "http://169.254.169.254/latest", true, "internal address"},
		{"metadata host", "http://metadata.google.internal/", true, "metadata host"},
		{"local suffix", "http://printer.local/", true, "metadata host"},
		{"internal suffix", "http://db.internal/", true, "metadata host"},
		{"ftp scheme", "ftp://public.example/", true, "only http and https"},
		{"missing host", "https:///path", true, "missing hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := policy.BlockReason(context.Background(), tc.url)
			if tc.block && reason == "" {
				t.Fatalf("BlockReason(%q) = allowed, want blocked", tc.url)
			}
			if !tc.block && reason != "" {
				t.Fatalf("BlockReason(%q) = %q, want allowed", tc.url, reason)
			}
			if tc.reason != "" && !strings.Contains(strings.ToLower(reason), strings.ToLower(tc.reason)) {
				t.Fatalf("BlockReason(%q) = %q, want %q", tc.url, reason, tc.reason)
			}
		})
	}
}

// Parity-exempt: Go-only host allow/deny list evaluation.
func TestBlockReasonHostLists(t *testing.T) {
	policy := Policy{
		AllowedHosts: []string{"api.example.com", "*.trusted.test"},
		DeniedHosts:  []string{"evil.example.com"},
		Resolver: func(context.Context, string) ([]string, error) {
			return []string{"93.184.216.34"}, nil
		},
	}
	for _, allowed := range []string{"https://api.example.com/x", "https://a.trusted.test/x"} {
		if reason := policy.BlockReason(context.Background(), allowed); reason != "" {
			t.Fatalf("BlockReason(%q) = %q, want allowed", allowed, reason)
		}
	}
	for _, blocked := range []string{"https://other.example.com/x", "https://evil.example.com/x"} {
		if reason := policy.BlockReason(context.Background(), blocked); reason == "" {
			t.Fatalf("BlockReason(%q) = allowed, want blocked", blocked)
		}
	}

	// allow_private_hosts disables only the IP deny-list, never the host lists.
	private := Policy{
		AllowPrivateHosts: true,
		DeniedHosts:       []string{"private.example"},
		Resolver: func(context.Context, string) ([]string, error) {
			return []string{"10.0.0.5"}, nil
		},
	}
	if reason := private.BlockReason(context.Background(), "http://127.0.0.1/"); reason != "" {
		t.Fatalf("allow_private_hosts should allow loopback, got %q", reason)
	}
	if reason := private.BlockReason(context.Background(), "http://private.example/"); reason == "" {
		t.Fatal("denied_hosts must win over allow_private_hosts")
	}
}

// Parity-exempt: Go-only internal address classification helper.
func TestIsInternalIP(t *testing.T) {
	internal := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "224.0.0.1", "192.0.2.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "240.0.0.1", "::1", "fc00::1", "fe80::1",
		"::ffff:127.0.0.1", "64:ff9b:1::1",
	}
	for _, raw := range internal {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("parse %q", raw)
		}
		if !IsInternalIP(ip) {
			t.Fatalf("IsInternalIP(%s) = false, want true", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946"} {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("parse %q", raw)
		}
		if IsInternalIP(ip) {
			t.Fatalf("IsInternalIP(%s) = true, want false", raw)
		}
	}
}

// Parity-exempt: Go-only dialer; pins dialing the validated IP.
func TestDialContextDialsValidatedIP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	accepted := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()

	var lookups atomic.Int32
	policy := Policy{
		AllowPrivateHosts: true,
		Resolver: func(context.Context, string) ([]string, error) {
			lookups.Add(1)
			return []string{"127.0.0.1"}, nil
		},
	}
	conn, err := policy.DialContext(context.Background(), "tcp", net.JoinHostPort("rebind.test", itoa(port)))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
	<-accepted
	if lookups.Load() != 1 {
		t.Fatalf("lookups = %d, want 1 (dial the validated IP, do not re-resolve)", lookups.Load())
	}
}

// Parity-exempt: Go-only dialer; internal address policy.
func TestDialContextBlocksInternalUnlessAllowed(t *testing.T) {
	policy := Policy{
		Resolver: func(context.Context, string) ([]string, error) {
			return []string{"127.0.0.1"}, nil
		},
	}
	if _, err := policy.DialContext(context.Background(), "tcp", "rebind.test:80"); err == nil {
		t.Fatal("DialContext allowed an internal address without allow_private_hosts")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
