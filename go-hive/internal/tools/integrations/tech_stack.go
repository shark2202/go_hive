// tech_stack_detect — fingerprint a website's technology stack through passive
// analysis of one HTTP response plus a small set of path probes.
//
// Parity: tools/src/aden_tools/tools/tech_stack_detector/tech_stack_detector.py
//
// Network scanner: registered in the default session catalogue like the rest
// of batch 2 (upstream aden_tools has no opt-in gate). The http.Client is
// injected through Runtime.HTTP, so tests run against a local fake server and
// never touch the real network.
//
// Declared differences from upstream:
//   - Detection order and tables match upstream (server/CDN/JS/CMS/analytics,
//     cookie→tech map, probe paths). The response body is capped at 4 MiB.
//   - Cookie jar iteration is replaced by the raw Set-Cookie list; upstream's
//     jar can reject cookies (domain/path rules) that this port still shows.
//   - Python's `ConnectError` vs other httpx errors is approximated by
//     connection-refused/DNS inspection, so the error wording can differ for
//     transport failures that are neither a timeout nor a refused connection.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var techServerHeaderRe = regexp.MustCompile(`^([\w.-]+)(?:/(\S+))?`)

var techPoweredByPHPRe = regexp.MustCompile(`(?i)php`)
var techPoweredByAspNetRe = regexp.MustCompile(`(?i)asp\.net`)
var techPoweredByExpressRe = regexp.MustCompile(`(?i)express`)

// techJSFindings mirrors JS_PATTERNS in upstream order.
type techJSFinding struct {
	name     string
	patterns []*regexp.Regexp
	version  *regexp.Regexp
}

var techJSFindings = []techJSFinding{
	{"React", techCompileAll(`react(?:\.min)?\.js`, `data-reactroot`, `__NEXT_DATA__`), nil},
	{"Angular", techCompileAll(`angular(?:\.min)?\.js`, `ng-app`, `ng-version`), nil},
	{"Vue.js", techCompileAll(`vue(?:\.min)?\.js`, `data-v-[a-f0-9]`, `__vue__`), nil},
	{"jQuery", techCompileAll(`jquery[.-](\d+\.\d+(?:\.\d+)?)`, `jquery(?:\.min)?\.js`), nil},
	{"Bootstrap", techCompileAll(`bootstrap[.-](\d+\.\d+(?:\.\d+)?)`, `bootstrap(?:\.min)?\.(?:js|css)`), nil},
	{"Tailwind CSS", techCompileAll(`tailwind`), nil},
	{"Svelte", techCompileAll(`svelte`, `__svelte`), nil},
	{"Next.js", techCompileAll(`_next/static`, `__NEXT_DATA__`), nil},
	{"Nuxt.js", techCompileAll(`__nuxt`, `_nuxt/`), nil},
}

// techCookieTechMap mirrors COOKIE_TECH_MAP; the order matches the upstream dict.
type techCookieTech struct{ name, tech string }

var techCookieTechMap = []techCookieTech{
	{"PHPSESSID", "PHP"},
	{"JSESSIONID", "Java"},
	{"ASP.NET_SessionId", "ASP.NET"},
	{"csrftoken", "Django"},
	{"laravel_session", "Laravel"},
	{"rack.session", "Ruby/Rails"},
	{"connect.sid", "Node.js/Express"},
	{"_rails_session", "Ruby on Rails"},
}

func techCookieTechFor(name string) string {
	for _, entry := range techCookieTechMap {
		if entry.name == name {
			return entry.tech
		}
	}
	return ""
}

// techAnalyticsPatterns mirrors ANALYTICS_PATTERNS in upstream order.
type techAnalyticsFinding struct {
	name     string
	patterns []*regexp.Regexp
}

var techAnalyticsFindings = []techAnalyticsFinding{
	{"Google Analytics", techCompileAll(`google-analytics\.com/analytics\.js`, `googletagmanager\.com`, `gtag\(`)},
	{"Facebook Pixel", techCompileAll(`connect\.facebook\.net`)},
	{"Hotjar", techCompileAll(`static\.hotjar\.com`)},
	{"Mixpanel", techCompileAll(`cdn\.mxpnl\.com`)},
	{"Segment", techCompileAll(`cdn\.segment\.com`)},
}

// techCDNHeaders mirrors CDN_HEADERS in upstream order ("" = infer from value).
type techCDNHeader struct{ header, name string }

var techCDNHeaders = []techCDNHeader{
	{"cf-ray", "Cloudflare"},
	{"x-cdn", ""},
	{"x-served-by", "Fastly"},
	{"x-amz-cf-id", "AWS CloudFront"},
	{"x-cache", ""},
	{"via", ""},
	{"x-vercel-id", "Vercel"},
	{"x-netlify-request-id", "Netlify"},
	{"fly-request-id", "Fly.io"},
}

// techProbePaths mirrors PROBE_PATHS in upstream order ("" = generic path).
type techProbePath struct{ path, tech string }

var techProbePaths = []techProbePath{
	{"/wp-admin/", "WordPress"},
	{"/wp-json/wp/v2/", "WordPress"},
	{"/wp-login.php", "WordPress"},
	{"/administrator/", "Joomla"},
	{"/user/login", "Drupal"},
	{"/admin/", ""},
	{"/api/", ""},
	{"/.well-known/security.txt", ""},
	{"/robots.txt", ""},
	{"/sitemap.xml", ""},
}

var techGeneratorRe = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<meta[^>]+name=["']generator["'][^>]+content=["'](.*?)["']`),
	regexp.MustCompile(`(?is)<meta[^>]+content=["'](.*?)["'][^>]+name=["']generator["']`),
}

func techCompileAll(patterns ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		out = append(out, regexp.MustCompile("(?i)"+pattern))
	}
	return out
}

// techStackDetect ports `tech_stack_detect`.
func techStackDetect(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	raw := strings.TrimSpace(stringArg(input, "url"))
	if raw == "" {
		return map[string]any{"error": "url is required"}, nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	baseURL := strings.TrimRight(raw, "/")

	client := *rt.HTTP.Inner
	if client.Timeout == 0 || client.Timeout > 15*time.Second {
		client.Timeout = 15 * time.Second
	}
	resp, body, err := techFetch(ctx, &client, baseURL)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errTimeout(err) {
			return map[string]any{"error": fmt.Sprintf("Request to %s timed out", raw)}, nil
		}
		if techConnectionError(err) {
			return map[string]any{"error": "Connection failed: " + err.Error()}, nil
		}
		return map[string]any{"error": "Detection failed: " + err.Error()}, nil
	}
	html := string(body)
	headers := resp.Header

	server := techDetectServer(headers)
	cdn := techDetectCDN(headers)
	framework := techDetectFrameworkFromHeaders(headers)
	cookies := techAnalyzeCookies(headers)
	language := techDetectLanguage(headers, cookies)
	jsLibraries := techDetectJSLibraries(html)
	analytics := techDetectAnalytics(html)
	cms := techDetectCMSFromHTML(html)
	if language == "" {
		for _, cookie := range cookies {
			if tech := techCookieTechFor(cookie.Name); tech != "" {
				language = tech
				break
			}
		}
	}

	securityTxt := false
	robotsTxt := false
	interestingPaths := []string{}
	cmsFromPaths := ""
	probeClient := *rt.HTTP.Inner
	if probeClient.Timeout == 0 || probeClient.Timeout > 15*time.Second {
		probeClient.Timeout = 15 * time.Second
	}
	probeClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, probe := range techProbePaths {
		status, ok := techProbeStatus(ctx, &probeClient, baseURL+probe.path)
		if !ok {
			continue
		}
		switch {
		case status != 200 && status != 301 && status != 302 && status != 403:
			continue
		case probe.path == "/.well-known/security.txt":
			securityTxt = status == 200
		case probe.path == "/robots.txt":
			robotsTxt = status == 200
		case probe.tech != "" && (status == 200 || status == 301 || status == 302):
			cmsFromPaths = probe.tech
		case status == 200 || status == 301 || status == 302:
			interestingPaths = append(interestingPaths, probe.path)
		}
	}
	if cms == "" && cmsFromPaths != "" {
		cms = cmsFromPaths
	}
	if framework == "" {
		framework = techDetectFrameworkFromHTML(html)
	}

	serverVersionHidden := true
	if server != nil && server["version"] != nil {
		serverVersionHidden = false
	}
	frameworkVersionHidden := !techHasVersion(framework)
	cookiesSecure := true
	cookiesHTTPOnly := true
	for _, cookie := range cookies {
		if !cookie.Secure {
			cookiesSecure = false
		}
		if !cookie.HTTPOnly {
			cookiesHTTPOnly = false
		}
	}

	finalURL := baseURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	return map[string]any{
		"url":                  finalURL,
		"server":               server,
		"framework":            techNullableString(framework),
		"language":             techNullableString(language),
		"cms":                  techNullableString(cms),
		"javascript_libraries": jsLibraries,
		"cdn":                  techNullableString(cdn),
		"analytics":            analytics,
		"security_txt":         securityTxt,
		"robots_txt":           robotsTxt,
		"interesting_paths":    interestingPaths,
		"cookies":              cookies,
		"grade_input": map[string]any{
			"server_version_hidden":    serverVersionHidden,
			"framework_version_hidden": frameworkVersionHidden,
			"security_txt_present":     securityTxt,
			"cookies_secure":           cookiesSecure,
			"cookies_httponly":         cookiesHTTPOnly,
		},
	}, nil
}

func techNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func techFetch(ctx context.Context, client *http.Client, url string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "AdenAgentFramework/1.0 (https://adenhq.com)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

func techProbeStatus(ctx context.Context, client *http.Client, url string) (int, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", "AdenAgentFramework/1.0 (https://adenhq.com)")
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, true
}

// techConnectionError approximates httpx.ConnectError.
func techConnectionError(err error) bool {
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection refused") || strings.Contains(text, "no such host")
}

// techDetectServer ports _detect_server.
func techDetectServer(headers http.Header) map[string]any {
	serverHeader := headers.Get("Server")
	if serverHeader == "" {
		return nil
	}
	match := techServerHeaderRe.FindStringSubmatch(serverHeader)
	if match != nil {
		var version any
		if match[2] != "" {
			version = match[2]
		}
		return map[string]any{"name": match[1], "version": version, "raw": serverHeader}
	}
	return map[string]any{"name": serverHeader, "version": nil, "raw": serverHeader}
}

// techDetectCDN ports _detect_cdn.
func techDetectCDN(headers http.Header) string {
	for _, entry := range techCDNHeaders {
		value := headers.Get(entry.header)
		if value == "" {
			continue
		}
		if entry.name != "" {
			return entry.name
		}
		lower := strings.ToLower(value)
		switch {
		case strings.Contains(lower, "cloudflare"):
			return "Cloudflare"
		case strings.Contains(lower, "cloudfront"):
			return "AWS CloudFront"
		case strings.Contains(lower, "fastly"):
			return "Fastly"
		case strings.Contains(lower, "akamai"):
			return "Akamai"
		case strings.Contains(lower, "varnish"):
			return "Varnish"
		}
	}
	return ""
}

// techDetectFrameworkFromHeaders ports _detect_framework_from_headers.
func techDetectFrameworkFromHeaders(headers http.Header) string {
	return headers.Get("X-Powered-By")
}

// techDetectFrameworkFromHTML ports _detect_framework_from_html.
func techDetectFrameworkFromHTML(html string) string {
	if strings.Contains(html, "csrfmiddlewaretoken") {
		return "Django"
	}
	if strings.Contains(html, "csrf-token") && strings.Contains(html, "data-turbo") {
		return "Ruby on Rails"
	}
	if strings.Contains(strings.ToLower(html), "laravel") {
		return "Laravel"
	}
	return ""
}

// techDetectLanguage ports _detect_language.
func techDetectLanguage(headers http.Header, cookies []techCookie) string {
	poweredBy := strings.ToLower(headers.Get("X-Powered-By"))
	switch {
	case techPoweredByPHPRe.MatchString(poweredBy):
		return "PHP"
	case techPoweredByAspNetRe.MatchString(poweredBy):
		return "ASP.NET"
	case techPoweredByExpressRe.MatchString(poweredBy):
		return "Node.js"
	}
	for _, cookie := range cookies {
		tech := techCookieTechFor(cookie.Name)
		switch tech {
		case "PHP", "Java", "ASP.NET", "Node.js/Express":
			return tech
		}
	}
	return ""
}

// techDetectJSLibraries ports _detect_js_libraries.
func techDetectJSLibraries(html string) []string {
	found := []string{}
	for _, library := range techJSFindings {
		matched := false
		for _, pattern := range library.patterns {
			if pattern.MatchString(html) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		versionRe := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(strings.ToLower(library.name)) + `[/-](\d+\.\d+(?:\.\d+)?)`)
		if version := versionRe.FindStringSubmatch(html); version != nil {
			found = append(found, library.name+" "+version[1])
			continue
		}
		found = append(found, library.name)
	}
	return found
}

// techDetectAnalytics ports _detect_analytics.
func techDetectAnalytics(html string) []string {
	found := []string{}
	for _, analytics := range techAnalyticsFindings {
		for _, pattern := range analytics.patterns {
			if pattern.MatchString(html) {
				found = append(found, analytics.name)
				break
			}
		}
	}
	return found
}

// techDetectCMSFromHTML ports _detect_cms_from_html.
func techDetectCMSFromHTML(html string) string {
	switch {
	case strings.Contains(html, "wp-content") || strings.Contains(html, "wp-includes"):
		return "WordPress"
	case strings.Contains(html, "Drupal") || strings.Contains(html, "drupal.js"):
		return "Drupal"
	case strings.Contains(html, "/media/jui/") || strings.Contains(html, "Joomla"):
		return "Joomla"
	case strings.Contains(html, "cdn.shopify.com"):
		return "Shopify"
	case strings.Contains(strings.ToLower(html), "squarespace"):
		return "Squarespace"
	case strings.Contains(html, "wix.com"):
		return "Wix"
	case strings.Contains(html, "ghost-") || strings.Contains(html, "ghost/"):
		return "Ghost"
	}
	for _, pattern := range techGeneratorRe {
		if match := pattern.FindStringSubmatch(html); match != nil {
			return match[1]
		}
	}
	return ""
}

// techCookie is one parsed Set-Cookie header.
type techCookie struct {
	Name     string
	Secure   bool
	HTTPOnly bool
	SameSite any
}

// techAnalyzeCookies ports _analyze_cookies (raw Set-Cookie headers).
func techAnalyzeCookies(headers http.Header) []techCookie {
	result := []techCookie{}
	for _, raw := range headers.Values("Set-Cookie") {
		name := strings.TrimSpace(strings.SplitN(raw, "=", 2)[0])
		secure := false
		httpOnly := false
		sameSite := any(nil)
		for _, part := range strings.Split(raw, ";") {
			lower := strings.ToLower(strings.TrimSpace(part))
			switch {
			case lower == "secure":
				secure = true
			case lower == "httponly":
				httpOnly = true
			case strings.HasPrefix(lower, "samesite="):
				value := strings.TrimSpace(strings.SplitN(lower, "=", 2)[1])
				if value != "" {
					sameSite = strings.ToUpper(value[:1]) + value[1:]
				}
			}
		}
		result = append(result, techCookie{Name: name, Secure: secure, HTTPOnly: httpOnly, SameSite: sameSite})
	}
	return result
}

// techHasVersion ports _has_version.
func techHasVersion(value string) bool {
	return regexp.MustCompile(`\d+\.\d+`).MatchString(value)
}
