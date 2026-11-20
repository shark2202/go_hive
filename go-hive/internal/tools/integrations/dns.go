package integrations

import (
	"context"
	"strconv"
	"strings"
)

// dnsResolver is the injectable DNS surface used by dns_security_scan. Every
// lookup reports "no records" as an error, matching dnspython's exceptions.
type dnsResolver interface {
	TXT(ctx context.Context, name string) ([]string, error)
	MX(ctx context.Context, name string) ([]dnsMXRecord, error)
	NS(ctx context.Context, name string) ([]string, error)
	CAA(ctx context.Context, name string) ([]string, error)
	DNSKEY(ctx context.Context, name string) (bool, error)
	ZoneTransfer(ctx context.Context, nameserver, domain string) (int, error)
}

type dnsMXRecord struct {
	Preference uint16
	Exchange   string
}

// dkimSelectors mirrors DKIM_SELECTORS in the upstream module.
var dkimSelectors = []string{"default", "google", "selector1", "selector2", "k1", "mail", "dkim", "s1"}

// cleanScanTarget strips protocol, path and port, like the upstream scanners.
func cleanScanTarget(raw string) string {
	target := strings.ReplaceAll(raw, "https://", "")
	target = strings.ReplaceAll(target, "http://", "")
	target = strings.Trim(target, "/")
	if index := strings.Index(target, "/"); index >= 0 {
		target = target[:index]
	}
	if index := strings.Index(target, ":"); index >= 0 {
		target = target[:index]
	}
	return strings.TrimSpace(target)
}

// dnsQuotedText strips the surrounding quotes dnspython's to_text() adds.
func dnsQuotedText(value string) string {
	return strings.Trim(strings.TrimSpace(value), "\"")
}

func dnsCheckSPF(ctx context.Context, resolver dnsResolver, domain string) map[string]any {
	records, err := resolver.TXT(ctx, domain)
	if err == nil {
		for _, record := range records {
			text := dnsQuotedText(record)
			if !strings.HasPrefix(text, "v=spf1") {
				continue
			}
			policy := "unknown"
			issues := []string{}
			switch {
			case strings.Contains(text, "~all"):
				policy = "softfail"
				issues = append(issues, "Uses ~all (softfail) instead of -all (hardfail). Spoofed emails may still be delivered.")
			case strings.Contains(text, "-all"):
				policy = "hardfail"
			case strings.Contains(text, "+all"):
				policy = "pass_all"
				issues = append(issues, "Uses +all which allows ANY server to send email for this domain. This effectively disables SPF protection.")
			case strings.Contains(text, "?all"):
				policy = "neutral"
				issues = append(issues, "Uses ?all (neutral). SPF results are not used for filtering.")
			default:
				issues = append(issues, "No 'all' mechanism found in SPF record.")
			}
			return map[string]any{
				"present": true,
				"record":  text,
				"policy":  policy,
				"issues":  issues,
			}
		}
	}
	return map[string]any{
		"present": false,
		"record":  nil,
		"policy":  nil,
		"issues":  []string{"No SPF record found. Any server can send email as this domain."},
	}
}

func dnsCheckDMARC(ctx context.Context, resolver dnsResolver, domain string) map[string]any {
	records, err := resolver.TXT(ctx, "_dmarc."+domain)
	if err == nil {
		for _, record := range records {
			text := dnsQuotedText(record)
			if !strings.HasPrefix(text, "v=DMARC1") {
				continue
			}
			policy := "none"
			for _, part := range strings.Split(text, ";") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "p=") {
					policy = strings.TrimSpace(part[2:])
				}
			}
			issues := []string{}
			if policy == "none" {
				issues = append(issues, "DMARC policy is 'none' — spoofed emails are not blocked. Upgrade to p=quarantine or p=reject.")
			}
			return map[string]any{
				"present": true,
				"record":  text,
				"policy":  policy,
				"issues":  issues,
			}
		}
	}
	return map[string]any{
		"present": false,
		"record":  nil,
		"policy":  nil,
		"issues":  []string{"No DMARC record found. Email spoofing is not actively monitored or blocked."},
	}
}

func dnsCheckDKIM(ctx context.Context, resolver dnsResolver, domain string) map[string]any {
	found := []string{}
	missing := []string{}
	for _, selector := range dkimSelectors {
		records, err := resolver.TXT(ctx, selector+"._domainkey."+domain)
		if err == nil && len(records) > 0 {
			found = append(found, selector)
			continue
		}
		missing = append(missing, selector)
	}
	return map[string]any{"selectors_found": found, "selectors_missing": missing}
}

func dnsCheckDNSSEC(ctx context.Context, resolver dnsResolver, domain string) map[string]any {
	enabled, err := resolver.DNSKEY(ctx, domain)
	if err == nil && enabled {
		return map[string]any{"enabled": true, "issues": []string{}}
	}
	return map[string]any{
		"enabled": false,
		"issues":  []string{"DNSSEC not enabled. The domain is vulnerable to DNS spoofing and cache poisoning."},
	}
}

func dnsCheckMX(ctx context.Context, resolver dnsResolver, domain string) []string {
	records, err := resolver.MX(ctx, domain)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(records))
	for _, record := range records {
		exchange := record.Exchange
		if exchange != "" && !strings.HasSuffix(exchange, ".") {
			exchange += "."
		}
		out = append(out, strings.TrimSpace(strconv.Itoa(int(record.Preference))+" "+exchange))
	}
	return out
}

func dnsCheckCAA(ctx context.Context, resolver dnsResolver, domain string) []string {
	records, err := resolver.CAA(ctx, domain)
	if err != nil {
		return []string{}
	}
	return records
}

func dnsCheckZoneTransfer(ctx context.Context, resolver dnsResolver, domain string) map[string]any {
	nameservers, err := resolver.NS(ctx, domain)
	if err != nil {
		return map[string]any{"vulnerable": false, "error": "Could not resolve NS records"}
	}
	for _, nameserver := range nameservers {
		records, err := resolver.ZoneTransfer(ctx, nameserver, domain)
		if err != nil || records <= 0 {
			continue
		}
		return map[string]any{
			"vulnerable":   true,
			"nameserver":   nameserver,
			"record_count": records,
			"severity":     "critical",
			"finding":      "Zone transfer allowed on " + nameserver,
			"remediation":  "Disable AXFR for public-facing nameservers. Restrict zone transfers to authorized secondary DNS servers only.",
		}
	}
	return map[string]any{"vulnerable": false}
}

func dnsSecurityScan(ctx context.Context, _ *Runtime, input map[string]any, resolver dnsResolver) (any, error) {
	raw := stringArg(input, "domain")
	domain := cleanScanTarget(raw)
	if domain == "" {
		return map[string]any{"error": "domain is required"}, nil
	}
	if resolver == nil {
		resolver = newSystemDNSResolver()
	}
	spf := dnsCheckSPF(ctx, resolver, domain)
	dmarc := dnsCheckDMARC(ctx, resolver, domain)
	dkim := dnsCheckDKIM(ctx, resolver, domain)
	dnssec := dnsCheckDNSSEC(ctx, resolver, domain)
	mx := dnsCheckMX(ctx, resolver, domain)
	caa := dnsCheckCAA(ctx, resolver, domain)
	zoneTransfer := dnsCheckZoneTransfer(ctx, resolver, domain)

	selectorsFound, _ := dkim["selectors_found"].([]string)
	policy, _ := dmarc["policy"].(string)
	zoneVulnerable, _ := zoneTransfer["vulnerable"].(bool)
	dnssecEnabled, _ := dnssec["enabled"].(bool)
	spfPolicy, _ := spf["policy"].(string)
	spfPresent, _ := spf["present"].(bool)
	dmarcPresent, _ := dmarc["present"].(bool)

	gradeInput := map[string]any{
		"spf_present":           spfPresent,
		"spf_strict":            spfPolicy == "hardfail",
		"dmarc_present":         dmarcPresent,
		"dmarc_enforcing":       policy == "quarantine" || policy == "reject",
		"dkim_found":            len(selectorsFound) > 0,
		"dnssec_enabled":        dnssecEnabled,
		"zone_transfer_blocked": !zoneVulnerable,
	}
	return map[string]any{
		"domain":        domain,
		"spf":           spf,
		"dmarc":         dmarc,
		"dkim":          dkim,
		"dnssec":        dnssec,
		"mx_records":    mx,
		"caa_records":   caa,
		"zone_transfer": zoneTransfer,
		"grade_input":   gradeInput,
	}, nil
}
