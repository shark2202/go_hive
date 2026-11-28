// Package netguard owns the outbound-request policy shared by the native HTTP
// tools and the existing web_scrape/pdf downloaders.
//
// It is deliberately stdlib-only and independent of the session, tool registry
// and agent loop: callers construct a Policy, ask BlockReason for a model-facing
// refusal, and hand DialContext to an http.Transport so the address that was
// validated is the address that is dialed (DNS rebinding cannot swap in a
// private address between the check and the connect).
//
// Configuration comes from $HIVE_HOME only. The session directory is the file
// tools' sandbox root and is agent-writable; it must never gate outbound
// policy. See go_hive/docs/config-roots.html.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Resolver resolves a hostname to addresses. Runtime.ResolveHost has this
// shape, so tests can inject a fake resolver without touching DNS.
type Resolver func(ctx context.Context, host string) ([]string, error)

// Policy is one immutable outbound policy.
type Policy struct {
	// AllowPrivateHosts disables the private/loopback/link-local/CGNAT/ULA
	// deny-list. It is an explicit, logged downgrade.
	AllowPrivateHosts bool
	// AllowedHosts, when non-empty, restricts requests to these hosts.
	// Entries are exact hosts, ".example.com" or "*.example.com".
	AllowedHosts []string
	// DeniedHosts always wins over AllowedHosts and the private-IP list.
	DeniedHosts []string
	// Resolver defaults to net.DefaultResolver.LookupHost.
	Resolver Resolver
	// LookupTimeout bounds one DNS lookup; defaults to 5s.
	LookupTimeout time.Duration
	// Dialer defaults to a net.Dialer with a 10s timeout.
	Dialer *net.Dialer
}

func (p Policy) resolver() Resolver {
	if p.Resolver != nil {
		return p.Resolver
	}
	return func(ctx context.Context, host string) ([]string, error) {
		return net.DefaultResolver.LookupHost(ctx, host)
	}
}

func (p Policy) lookupTimeout() time.Duration {
	if p.LookupTimeout > 0 {
		return p.LookupTimeout
	}
	return 5 * time.Second
}

func (p Policy) dialer() *net.Dialer {
	if p.Dialer != nil {
		return p.Dialer
	}
	return &net.Dialer{Timeout: 10 * time.Second}
}

// BlockReason resolves a URL's host and returns a model-facing refusal, or ""
// when the URL is allowed. Every request and every redirect hop must call it.
func (p Policy) BlockReason(ctx context.Context, rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" {
		return "Invalid URL: missing hostname"
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "Blocked: only http and https are allowed"
	}
	host := canonicalHost(parsed.Hostname())
	if host == "" {
		return "Invalid URL: missing hostname"
	}
	if matchesHost(host, p.DeniedHosts) {
		return fmt.Sprintf("Blocked: host %s is denied by policy", host)
	}
	if len(p.AllowedHosts) > 0 && !matchesHost(host, p.AllowedHosts) {
		return fmt.Sprintf("Blocked: host %s is not in allowed_hosts", host)
	}
	if p.AllowPrivateHosts {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if IsInternalIP(ip) {
			return fmt.Sprintf("Blocked: direct request to internal address (%s)", host)
		}
		return ""
	}
	if metadataHost(host) {
		return fmt.Sprintf("Blocked: metadata host %s", host)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, p.lookupTimeout())
	defer cancel()
	addresses, err := p.resolver()(lookupCtx, host)
	if err != nil {
		return "DNS resolution failed for host: " + host
	}
	if len(addresses) == 0 {
		return "No DNS records found for host: " + host
	}
	for _, address := range addresses {
		candidate := stripZone(address)
		ip := net.ParseIP(candidate)
		if ip == nil || IsInternalIP(ip) {
			return fmt.Sprintf("Blocked: %s resolves to internal address", host)
		}
	}
	return ""
}

// DialContext resolves and validates the host, then dials one of the validated
// IPs. The transport's TLS handshake still uses the URL hostname as ServerName.
func (p Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !p.AllowPrivateHosts && IsInternalIP(ip) {
			return nil, fmt.Errorf("netguard: blocked internal address %s", host)
		}
		return p.dialer().DialContext(ctx, network, address)
	}
	lookupCtx, cancel := context.WithTimeout(ctx, p.lookupTimeout())
	defer cancel()
	addresses, err := p.resolver()(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("netguard: resolve %s: %w", host, err)
	}
	var lastErr error
	for _, address := range addresses {
		candidate := stripZone(address)
		ip := net.ParseIP(candidate)
		if ip == nil {
			continue
		}
		if !p.AllowPrivateHosts && IsInternalIP(ip) {
			return nil, fmt.Errorf("netguard: %s resolves to internal address %s", host, candidate)
		}
		conn, dialErr := p.dialer().DialContext(ctx, network, net.JoinHostPort(candidate, port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("netguard: no addresses for %s", host)
	}
	return nil, lastErr
}

// IsInternalIP reports whether an address is non-public. The list covers
// loopback, RFC 1918, ULA, link-local, CGNAT, multicast, unspecified,
// documentation/benchmark ranges, IPv4-mapped IPv6 and NAT64 local-use.
func IsInternalIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		for _, cidr := range internalV4 {
			if cidr.Contains(v4) {
				return true
			}
		}
		return false
	}
	if v6 := ip.To16(); v6 != nil {
		// IPv4-mapped addresses are handled by To4 above; NAT64 local-use
		// (64:ff9b:1::/48) is not a global destination.
		if nat64Local.Contains(v6) {
			return true
		}
	}
	return false
}

var (
	internalV4 = mustCIDRs(
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved
	)
	nat64Local = mustCIDR("64:ff9b:1::/48")
)

func mustCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic(err)
	}
	return network
}

func mustCIDRs(raw ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(raw))
	for _, item := range raw {
		out = append(out, mustCIDR(item))
	}
	return out
}

func canonicalHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}

func stripZone(address string) string {
	if index := strings.IndexByte(address, '%'); index >= 0 {
		return address[:index]
	}
	return address
}

// matchesHost reports whether host matches any entry. Entries are exact hosts,
// ".example.com" or "*.example.com"; matching is case-insensitive.
func matchesHost(host string, patterns []string) bool {
	for _, raw := range patterns {
		pattern := canonicalHost(raw)
		pattern = strings.TrimPrefix(pattern, ".")
		if pattern == "" {
			continue
		}
		pattern = strings.TrimPrefix(pattern, "*.")
		if host == pattern || strings.HasSuffix(host, "."+pattern) {
			return true
		}
	}
	return false
}

// metadataHost blocks cloud metadata and cluster-internal names even when they
// resolve to a public address.
func metadataHost(host string) bool {
	switch host {
	case "metadata", "metadata.google.internal", "instance-data", "kubernetes.default.svc":
		return true
	}
	for _, suffix := range []string{".local", ".internal", ".svc.cluster.local"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
