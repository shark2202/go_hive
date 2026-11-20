package integrations

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// securityHeader describes one OWASP-recommended header; the order matches
// SECURITY_HEADERS in the upstream module so output lists are deterministic.
type securityHeader struct {
	name        string
	severity    string
	description string
	remediation string
}

var securityHeaders = []securityHeader{
	{
		name:        "Strict-Transport-Security",
		severity:    "high",
		description: "No HSTS header. Browsers may connect over plain HTTP, enabling man-in-the-middle attacks.",
		remediation: "Add the header: Strict-Transport-Security: max-age=31536000; includeSubDomains",
	},
	{
		name:        "Content-Security-Policy",
		severity:    "high",
		description: "No CSP header. The site is more vulnerable to XSS attacks from inline scripts and untrusted sources.",
		remediation: "Add a Content-Security-Policy header. Start restrictive: default-src 'self'; script-src 'self'",
	},
	{
		name:        "X-Frame-Options",
		severity:    "medium",
		description: "No X-Frame-Options header. The site may be vulnerable to clickjacking.",
		remediation: "Add the header: X-Frame-Options: DENY (or SAMEORIGIN)",
	},
	{
		name:        "X-Content-Type-Options",
		severity:    "medium",
		description: "No X-Content-Type-Options header. Browsers may MIME-sniff responses, potentially executing malicious content.",
		remediation: "Add the header: X-Content-Type-Options: nosniff",
	},
	{
		name:        "Referrer-Policy",
		severity:    "low",
		description: "No Referrer-Policy header. Full URLs (including query params) may leak to third-party sites via the Referer header.",
		remediation: "Add the header: Referrer-Policy: strict-origin-when-cross-origin",
	},
	{
		name:        "Permissions-Policy",
		severity:    "low",
		description: "No Permissions-Policy header. Browser features like camera, microphone, and geolocation are not explicitly restricted.",
		remediation: "Add the header: Permissions-Policy: camera=(), microphone=(), geolocation=()",
	},
}

type leakyHeader struct {
	name        string
	remediation string
}

var leakyHeaders = []leakyHeader{
	{name: "Server", remediation: "Remove or genericize the Server header to avoid version disclosure."},
	{name: "X-Powered-By", remediation: "Remove the X-Powered-By header to hide the backend framework."},
	{name: "X-AspNet-Version", remediation: "Remove the X-AspNet-Version header from IIS/ASP.NET configuration."},
	{name: "X-AspNetMvc-Version", remediation: "Remove the X-AspNetMvc-Version header."},
	{name: "X-Generator", remediation: "Remove the X-Generator header to hide the CMS/platform in use."},
}

func httpHeadersScan(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	url := strings.TrimSpace(stringArg(input, "url"))
	if url == "" {
		return map[string]any{"error": "url is required"}, nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	client := *rt.HTTP.Inner
	if !boolArg(input, "follow_redirects", true) {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return map[string]any{"error": "Request failed: " + err.Error()}, nil
	}
	req.Header.Set("User-Agent", "AdenAgentFramework/1.0 (https://adenhq.com)")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return map[string]any{"error": fmt.Sprintf("Request to %s timed out", url)}, nil
		}
		if errTimeout(err) {
			return map[string]any{"error": fmt.Sprintf("Request to %s timed out", url)}, nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "connection refused") ||
			strings.Contains(strings.ToLower(err.Error()), "connect:") {
			return map[string]any{"error": "Connection failed: " + err.Error()}, nil
		}
		return map[string]any{"error": "Request failed: " + err.Error()}, nil
	}
	defer resp.Body.Close()

	headerLower := map[string]bool{}
	for name := range resp.Header {
		headerLower[strings.ToLower(name)] = true
	}

	present := []string{}
	missing := []map[string]any{}
	for _, header := range securityHeaders {
		if headerLower[strings.ToLower(header.name)] {
			present = append(present, header.name)
			continue
		}
		missing = append(missing, map[string]any{
			"header":      header.name,
			"severity":    header.severity,
			"description": header.description,
			"remediation": header.remediation,
		})
	}

	leakyFound := []map[string]any{}
	for _, header := range leakyHeaders {
		value := resp.Header.Get(header.name)
		if value == "" {
			continue
		}
		leakyFound = append(leakyFound, map[string]any{
			"header":      header.name,
			"value":       value,
			"severity":    "low",
			"remediation": header.remediation,
		})
	}

	if resp.Header.Get("X-XSS-Protection") != "" {
		present = append(present, "X-XSS-Protection (deprecated)")
	}

	gradeInput := map[string]any{
		"hsts":                   headerLower["strict-transport-security"],
		"csp":                    headerLower["content-security-policy"],
		"x_frame_options":        headerLower["x-frame-options"],
		"x_content_type_options": headerLower["x-content-type-options"],
		"referrer_policy":        headerLower["referrer-policy"],
		"permissions_policy":     headerLower["permissions-policy"],
		"no_leaky_headers":       len(leakyFound) == 0,
	}
	finalURL := url
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return map[string]any{
		"url":             finalURL,
		"status_code":     resp.StatusCode,
		"headers_present": present,
		"headers_missing": missing,
		"leaky_headers":   leakyFound,
		"grade_input":     gradeInput,
	}, nil
}
