package image

// SSRF gate for model-supplied reference-image URLs: raw IP literals skip DNS,
// every resolved address must be public, and a resolution failure is a refusal
// rather than a fallback to the system stack. The deny list mirrors Python's
// `not ipaddress.is_global or is_multicast`, including the ranges Go's
// IsGlobalUnicast still calls global (carrier-grade NAT, documentation and
// benchmark nets, IPv6 documentation/discard/local-translation prefixes).
//
// This file deliberately mirrors the root batch3_shared.go policy that
// web_scrape still uses; the two converge when the guard moves to
// integrationkit.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// urlPolicy is the SSRF gate for image reference downloads.
type urlPolicy struct {
	resolver Resolver
}

// check returns a non-empty reason when target must not be fetched.
func (policy urlPolicy) check(ctx context.Context, target string) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" {
		return "Invalid URL: missing hostname"
	}
	host := parsed.Hostname()
	// Raw IP literals skip DNS: a loopback or private literal must be refused
	// even when the resolver would echo it back.
	if ip := net.ParseIP(stripZone(host)); ip != nil {
		if isInternalIP(ip) {
			return fmt.Sprintf("Blocked: direct request to internal address (%s)", host)
		}
		return ""
	}
	resolver := policy.resolver
	if resolver == nil {
		resolver = net.DefaultResolver.LookupHost
	}
	// Cap resolution at 5s so a hung resolver cannot burn the tool budget.
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := resolver(lookupCtx, host)
	if err != nil {
		return "DNS resolution failed for host: " + host
	}
	if len(addresses) == 0 {
		return "No DNS records found for host: " + host
	}
	// Every answer must be public: a host resolving to one public and one
	// internal address is a rebinding attempt, not a public host.
	for _, address := range addresses {
		ip := net.ParseIP(stripZone(address))
		if ip == nil || isInternalIP(ip) {
			return fmt.Sprintf("Blocked: %s resolves to internal address", host)
		}
	}
	return ""
}

// allows is check in boolean form, the reference-image loader's contract.
func (policy urlPolicy) allows(ctx context.Context, target string) bool {
	return policy.check(ctx, target) == ""
}

// stripZone drops an IPv6 zone suffix ("fe80::1%en0") before parsing.
func stripZone(address string) string {
	if index := strings.IndexByte(address, '%'); index >= 0 {
		return address[:index]
	}
	return address
}

// isInternalIP reports whether ip sits outside the public unicast space.
func isInternalIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		!ip.IsGlobalUnicast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0, v4[0] >= 240: // 0/8 "this network", 240/4 reserved
			return true
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // 100.64/10 CGNAT
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24 IETF
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2: // 192.0.2/24 TEST-NET-1
			return true
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18/15 benchmark
			return true
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100: // TEST-NET-2
			return true
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113: // TEST-NET-3
			return true
		}
		return false
	}
	for _, prefix := range blockedIPv6 {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// blockedIPv6 lists reserved IPv6 ranges that net.IP still reports as global
// unicast.
var blockedIPv6 = mustCIDRs(
	"2001:db8::/32",  // documentation (RFC 3849)
	"2001:2::/48",    // benchmarking (RFC 5180)
	"100::/64",       // discard-only (RFC 6666)
	"64:ff9b:1::/48", // local-use IPv4/IPv6 translation (RFC 8215)
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("integrations/image: invalid SSRF prefix " + cidr)
		}
		out = append(out, network)
	}
	return out
}
