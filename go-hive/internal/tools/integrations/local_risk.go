package integrations

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
)

// riskCheck mirrors one upstream check: points, finding text, and whether a
// true value is bad (invert).
type riskCheck struct {
	key     string
	points  int
	finding string
	invert  bool
}

type riskCategory struct {
	name   string
	weight float64
	checks []riskCheck
}

// riskCategories is a direct port of the upstream tables (order matters:
// findings keep this order).
var riskCategories = []riskCategory{
	{name: "ssl_tls", weight: 0.20, checks: []riskCheck{
		{key: "tls_version_ok", points: 25, finding: "Insecure TLS version in use"},
		{key: "cert_valid", points: 30, finding: "SSL certificate is invalid or untrusted"},
		{key: "cert_expiring_soon", points: 10, finding: "SSL certificate expiring soon", invert: true},
		{key: "strong_cipher", points: 20, finding: "Weak cipher suite in use"},
		{key: "self_signed", points: 15, finding: "Self-signed certificate detected", invert: true},
	}},
	{name: "http_headers", weight: 0.20, checks: []riskCheck{
		{key: "hsts", points: 20, finding: "Missing Strict-Transport-Security header"},
		{key: "csp", points: 20, finding: "Missing Content-Security-Policy header"},
		{key: "x_frame_options", points: 15, finding: "Missing X-Frame-Options header"},
		{key: "x_content_type_options", points: 15, finding: "Missing X-Content-Type-Options header"},
		{key: "referrer_policy", points: 10, finding: "Missing Referrer-Policy header"},
		{key: "permissions_policy", points: 10, finding: "Missing Permissions-Policy header"},
		{key: "no_leaky_headers", points: 10, finding: "Server information leaked via headers"},
	}},
	{name: "dns_security", weight: 0.15, checks: []riskCheck{
		{key: "spf_present", points: 15, finding: "No SPF record found"},
		{key: "spf_strict", points: 10, finding: "SPF policy is not strict (hardfail)"},
		{key: "dmarc_present", points: 20, finding: "No DMARC record found"},
		{key: "dmarc_enforcing", points: 15, finding: "DMARC policy is not enforcing"},
		{key: "dkim_found", points: 15, finding: "No DKIM selector found"},
		{key: "dnssec_enabled", points: 15, finding: "DNSSEC not enabled"},
		{key: "zone_transfer_blocked", points: 10, finding: "DNS zone transfer allowed"},
	}},
	{name: "network_exposure", weight: 0.15, checks: []riskCheck{
		{key: "no_database_ports_exposed", points: 35, finding: "Database port(s) exposed to internet"},
		{key: "no_admin_ports_exposed", points: 30, finding: "Admin/remote access port(s) exposed to internet"},
		{key: "no_legacy_ports_exposed", points: 20, finding: "Legacy protocol port(s) still active"},
		{key: "only_web_ports", points: 15, finding: "Non-web ports open"},
	}},
	{name: "technology", weight: 0.15, checks: []riskCheck{
		{key: "server_version_hidden", points: 25, finding: "Server version disclosed in headers"},
		{key: "framework_version_hidden", points: 20, finding: "Framework/runtime version disclosed"},
		{key: "security_txt_present", points: 20, finding: "No security.txt file found"},
		{key: "cookies_secure", points: 20, finding: "Cookies missing Secure flag"},
		{key: "cookies_httponly", points: 15, finding: "Cookies missing HttpOnly flag"},
	}},
	{name: "attack_surface", weight: 0.15, checks: []riskCheck{
		{key: "no_dev_staging_exposed", points: 40, finding: "Dev/staging environment subdomains exposed"},
		{key: "no_admin_exposed", points: 35, finding: "Admin/backup subdomains exposed"},
		{key: "reasonable_surface_area", points: 25, finding: "Large attack surface (many subdomains)"},
	}},
}

var gradeScale = map[string]string{
	"A": "90-100: Excellent security posture",
	"B": "75-89: Good, minor improvements needed",
	"C": "60-74: Fair, notable security gaps",
	"D": "40-59: Poor, significant vulnerabilities",
	"F": "0-39: Critical, immediate action required",
}

// riskScore ports risk_scorer.risk_score. It is pure: it consumes the JSON
// output of the six scanners and never performs I/O.
func riskScore(_ context.Context, _ *Runtime, input map[string]any) (any, error) {
	rawInputs := map[string]map[string]any{
		"ssl_tls":          parseJSONObject(stringArg(input, "ssl_results")),
		"http_headers":     parseJSONObject(stringArg(input, "headers_results")),
		"dns_security":     parseJSONObject(stringArg(input, "dns_results")),
		"network_exposure": parseJSONObject(stringArg(input, "ports_results")),
		"technology":       parseJSONObject(stringArg(input, "tech_results")),
		"attack_surface":   parseJSONObject(stringArg(input, "subdomain_results")),
	}

	categories := map[string]any{}
	type finding struct {
		category string
		text     string
		score    int
	}
	var findings []finding
	weightedSum, totalWeight := 0.0, 0.0

	for _, category := range riskCategories {
		raw := rawInputs[category.name]
		if raw == nil {
			categories[category.name] = map[string]any{
				"score": nil, "grade": "N/A", "weight": category.weight, "findings_count": 0, "skipped": true,
			}
			continue
		}
		gradeInput := raw
		if nested, ok := raw["grade_input"].(map[string]any); ok {
			gradeInput = nested
		}
		score, categoryFindings := scoreRiskCategory(gradeInput, category.checks)
		categories[category.name] = map[string]any{
			"score":          score,
			"grade":          scoreToGrade(score),
			"weight":         category.weight,
			"findings_count": len(categoryFindings),
			"skipped":        false,
		}
		weightedSum += float64(score) * category.weight
		totalWeight += category.weight
		for _, text := range categoryFindings {
			findings = append(findings, finding{category: category.name, text: text, score: score})
		}
	}

	overall := 0
	if totalWeight > 0 {
		overall = int(math.Round(weightedSum / totalWeight))
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].score != findings[j].score {
			return findings[i].score < findings[j].score
		}
		return findings[i].category < findings[j].category
	})
	topRisks := make([]string, 0, 10)
	for _, item := range findings {
		if len(topRisks) >= 10 {
			break
		}
		category := categories[item.category].(map[string]any)
		topRisks = append(topRisks, item.text+" ("+titleWords(strings.ReplaceAll(item.category, "_", " "))+": "+category["grade"].(string)+")")
	}

	return map[string]any{
		"overall_score": overall,
		"overall_grade": scoreToGrade(overall),
		"categories":    categories,
		"top_risks":     topRisks,
		"grade_scale":   gradeScale,
	}, nil
}

func scoreRiskCategory(gradeInput map[string]any, checks []riskCheck) (int, []string) {
	total := 0
	for _, check := range checks {
		total += check.points
	}
	earned := 0
	var findings []string
	for _, check := range checks {
		value, present := gradeInput[check.key]
		if !present || value == nil {
			earned += check.points / 2
			continue
		}
		truthy := truthyValue(value)
		passed := truthy
		if check.invert {
			passed = !truthy
		}
		if passed {
			earned += check.points
		} else {
			findings = append(findings, check.finding)
		}
	}
	if total == 0 {
		return 50, findings
	}
	return int(math.Round(float64(earned) / float64(total) * 100)), findings
}

func truthyValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != "" && typed != "false"
	case float64:
		return typed != 0
	case nil:
		return false
	default:
		return true
	}
}

func scoreToGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

func parseJSONObject(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func titleWords(value string) string {
	words := strings.Fields(value)
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}
