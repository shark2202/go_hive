package pdf

// SSRF guard for URL downloads. The root package's web-scrape file owns an
// equivalent check, but a domain subpackage may not import the root; the
// helper below mirrors that implementation (same deny-list, DNS checks and
// lookup timeout) until scrape.go splits and the check can move to
// integrationkit.

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// hostResolver resolves a hostname to addresses; Runtime.ResolveHost has this
// shape, so tests can inject a fake resolver.
type hostResolver func(ctx context.Context, host string) ([]string, error)

type ssrfBlockedError struct {
	url     string
	message string
}

func (e *ssrfBlockedError) Error() string { return e.message }

// checkURLTarget resolves a URL's host and refuses internal targets. An empty
// string means the URL is allowed.
func checkURLTarget(ctx context.Context, target string, resolver hostResolver) string {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Hostname() == "" {
		return "Invalid URL: missing hostname"
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isInternalIP(ip) {
			return fmt.Sprintf("Blocked: direct request to internal address (%s)", host)
		}
		return ""
	}
	if resolver == nil {
		resolver = defaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := resolver(lookupCtx, host)
	if err != nil {
		return "DNS resolution failed for host: " + host
	}
	if len(addresses) == 0 {
		return "No DNS records found for host: " + host
	}
	for _, address := range addresses {
		candidate := address
		if index := strings.IndexByte(candidate, '%'); index >= 0 {
			candidate = candidate[:index]
		}
		ip := net.ParseIP(candidate)
		if ip == nil || isInternalIP(ip) {
			return fmt.Sprintf("Blocked: %s resolves to internal address", host)
		}
	}
	return ""
}

func defaultResolver(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

func isInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// Carrier-grade NAT (100.64.0.0/10) is not global either.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
	}
	return false
}
