// subdomain_enumerate — passive subdomain discovery through Certificate
// Transparency logs (crt.sh).
//
// Parity: tools/src/aden_tools/tools/subdomain_enumerator/subdomain_enumerator.py
//
// Network scanner: like the batch-2 scanners it is registered and then dropped
// from the session catalogue unless HIVE_NETWORK_SCANNERS (or network_scanners
// in hive.config.json) is on. The HTTP client and crt.sh base URL are injected
// through Runtime/ModuleConfig, so tests never touch the real network.
//
// Difference from upstream: cookie-free, no credentials, and the endpoint is a
// module base URL instead of a hard-coded constant (so a fake server can stand
// in). Result shaping, sorting, interesting-keyword rules and grade_input match
// the upstream dict.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// subdomainInterestingKeywords mirrors INTERESTING_KEYWORDS in upstream order,
// so `interesting` lists are deterministic.
type subdomainKeyword struct {
	keyword     string
	reason      string
	severity    string
	remediation string
	pattern     *regexp.Regexp
}

var subdomainInterestingKeywords = []subdomainKeyword{
	{"staging", "Staging environment exposed publicly", "medium", "Restrict staging to VPN or internal network access.", nil},
	{"dev", "Development environment exposed publicly", "medium", "Restrict development servers to internal access only.", nil},
	{"test", "Test environment exposed publicly", "medium", "Restrict test servers to internal access only.", nil},
	{"admin", "Admin panel subdomain exposed publicly", "high", "Restrict admin panels to VPN or trusted IP ranges.", nil},
	{"internal", "Internal subdomain exposed in CT logs", "medium", "Review if internal subdomains should have public certificates.", nil},
	{"vpn", "VPN endpoint discoverable via CT logs", "low", "Consider if VPN endpoint exposure is acceptable for your threat model.", nil},
	{"api", "API subdomain discovered — potential attack surface", "low", "Ensure API is properly authenticated and rate-limited.", nil},
	{"mail", "Mail server subdomain discovered", "info", "Ensure mail server has proper SPF, DKIM, and DMARC configuration.", nil},
	{"ftp", "FTP subdomain discovered — legacy protocol", "medium", "Replace FTP with SFTP. Restrict access to trusted networks.", nil},
	{"debug", "Debug subdomain exposed publicly", "high", "Remove debug endpoints from production. Restrict to internal access.", nil},
	{"backup", "Backup subdomain exposed publicly", "high", "Restrict backup infrastructure to internal access only.", nil},
}

func init() {
	for index := range subdomainInterestingKeywords {
		keyword := subdomainInterestingKeywords[index].keyword
		subdomainInterestingKeywords[index].pattern = regexp.MustCompile(`\b` + regexp.QuoteMeta(keyword) + `\b`)
	}
}

// subdomainEnumerate ports `subdomain_enumerate`.
func subdomainEnumerate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	domain := cleanScanTarget(strings.TrimSpace(stringArg(input, "domain")))
	if domain == "" {
		return map[string]any{"error": "domain is required"}, nil
	}
	maxResults := intArg(input, "max_results", 50)
	if maxResults > 200 {
		maxResults = 200
	}
	if maxResults < 1 {
		maxResults = 1
	}

	base := ""
	if cfg := rt.Modules["subdomain_enumerator"]; cfg != nil {
		base = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	}
	if base == "" {
		return map[string]any{"error": "CT log query failed: no crt.sh endpoint configured", "domain": domain}, nil
	}
	query := url.Values{}
	query.Set("q", "%."+domain)
	query.Set("output", "json")
	endpoint := base + "/?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return map[string]any{"error": "CT log query failed: " + err.Error(), "domain": domain}, nil
	}
	req.Header.Set("User-Agent", "AdenAgentFramework/1.0 (https://adenhq.com)")
	resp, err := rt.HTTP.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded || errTimeout(err) {
			return map[string]any{"error": "crt.sh request timed out (try again later)", "domain": domain}, nil
		}
		return map[string]any{"error": "CT log query failed: " + err.Error(), "domain": domain}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return map[string]any{"error": fmt.Sprintf("crt.sh returned HTTP %d", resp.StatusCode), "domain": domain}, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return map[string]any{"error": "CT log query failed: " + err.Error(), "domain": domain}, nil
	}
	var data []any
	if err := json.Unmarshal(body, &data); err != nil {
		return map[string]any{"error": "CT log query failed: " + err.Error(), "domain": domain}, nil
	}

	rawNames := map[string]struct{}{}
	suffix := "." + domain
	for _, entry := range data {
		record, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		nameValue, _ := record["name_value"].(string)
		for _, name := range strings.Split(nameValue, "\n") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if name == domain || strings.HasSuffix(name, suffix) {
				rawNames[name] = struct{}{}
			}
		}
	}

	subdomains := make([]string, 0, len(rawNames))
	for name := range rawNames {
		if strings.HasPrefix(name, "*.") {
			continue
		}
		subdomains = append(subdomains, name)
	}
	sort.Strings(subdomains)
	if len(subdomains) > maxResults {
		subdomains = subdomains[:maxResults]
	}

	interesting := make([]map[string]any, 0)
	for _, sub := range subdomains {
		prefix := strings.ToLower(strings.TrimSuffix(sub, suffix))
		for _, keyword := range subdomainInterestingKeywords {
			if !keyword.pattern.MatchString(prefix) {
				continue
			}
			interesting = append(interesting, map[string]any{
				"subdomain":   sub,
				"reason":      keyword.reason,
				"severity":    keyword.severity,
				"remediation": keyword.remediation,
			})
			break
		}
	}

	hasDevStaging := false
	hasAdmin := false
	for _, finding := range interesting {
		sub, _ := finding["subdomain"].(string)
		severity, _ := finding["severity"].(string)
		if (severity == "medium" || severity == "high") &&
			(strings.Contains(sub, "staging") || strings.Contains(sub, "dev") ||
				strings.Contains(sub, "test") || strings.Contains(sub, "debug")) {
			hasDevStaging = true
		}
		if strings.Contains(sub, "admin") || strings.Contains(sub, "backup") {
			hasAdmin = true
		}
	}

	return map[string]any{
		"domain":      domain,
		"source":      "crt.sh (Certificate Transparency)",
		"total_found": len(subdomains),
		"subdomains":  subdomains,
		"interesting": interesting,
		"grade_input": map[string]any{
			"no_dev_staging_exposed":  !hasDevStaging,
			"no_admin_exposed":        !hasAdmin,
			"reasonable_surface_area": len(subdomains) < 50,
		},
	}, nil
}
