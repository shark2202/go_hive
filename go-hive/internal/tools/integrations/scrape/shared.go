// Artifact-directory resolution and the SSRF gate for web_scrape.
//
// These helpers mirror the copies the image subpackage carries (image is the
// other tool that writes files and fetches a model-supplied URL). The two
// converge when the guard moves to integrationkit; until then the scrape-side
// copy keeps this file dependent on integrationkit and the standard library
// only, with tests targeting it rather than the action.
package scrape

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------- artifacts --

// storageEnv carries the injectable filesystem environment of the
// batch-3 artifact writers. The zero value reads the process environment;
// tests inject both seams so no result depends on $HOME or on a developer's
// HIVE_* exports.
type storageEnv struct {
	getenv      func(string) string
	userHomeDir func() (string, error)
}

func (env storageEnv) value(name string) string {
	if env.getenv == nil {
		return strings.TrimSpace(os.Getenv(name))
	}
	return strings.TrimSpace(env.getenv(name))
}

func (env storageEnv) home() (string, error) {
	if env.userHomeDir != nil {
		return env.userHomeDir()
	}
	return os.UserHomeDir()
}

// resolveArtifactDir is the single artifact-directory resolver for the
// batch-3 tools, mirroring the Python order:
//
//	<HIVE_STORAGE_PATH>/data       per-agent session data dir
//	<HIVE_HOME>/tool-artifacts     shared install dir
//	~/.hive/tool-artifacts         OSS default
//
// A leading "~" is expanded in either variable: the framework injects
// absolute paths, but a hand-run process may export "~/…" and must not create
// a literal "~" directory. Python expands HIVE_HOME only; expanding
// HIVE_STORAGE_PATH too is the stricter of the two and is what this port
// already did for generated images.
func resolveArtifactDir(env storageEnv) (string, error) {
	if storage := env.value("HIVE_STORAGE_PATH"); storage != "" {
		return filepath.Join(expandHome(storage, env), "data"), nil
	}
	if home := env.value("HIVE_HOME"); home != "" {
		return filepath.Join(expandHome(home, env), "tool-artifacts"), nil
	}
	userHome, err := env.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".hive", "tool-artifacts"), nil
}

// expandHome expands a leading "~" without touching any other path.
func expandHome(path string, env storageEnv) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := env.home()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// -------------------------------------------------------------------- SSRF --

// urlPolicy is the SSRF gate for web_scrape: the page URL and every redirect
// hop go through it, so the deny list, the DNS-failure behaviour and the raw-IP
// fast path stay in one place.
type urlPolicy struct {
	resolver Resolver
}

// check returns a non-empty reason when target must not be fetched. Message
// text is model-visible; the scrape action forwards it verbatim.
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

// isInternalIP reports whether ip sits outside the public unicast
// space. It mirrors Python's `not ipaddress.is_global or is_multicast`,
// including the ranges Go's IsGlobalUnicast still calls global: carrier-grade
// NAT, the IPv4 documentation/benchmark nets, and the IPv6 documentation,
// discard and local-translation prefixes.
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

// blockedIPv6 lists reserved IPv6 ranges that net.IP still reports as
// global unicast.
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
			panic("integrations: invalid SSRF prefix " + cidr)
		}
		out = append(out, network)
	}
	return out
}
