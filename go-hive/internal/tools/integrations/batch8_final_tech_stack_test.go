package integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func batch8Header(values map[string]string) http.Header {
	headers := http.Header{}
	for name, value := range values {
		headers.Set(name, value)
	}
	return headers
}

// batch8TechServer is a fake site whose headers, HTML and probe paths cover
// every detection table without touching the network.
func batch8TechServer(t *testing.T, html string, headers map[string]string, cookies []string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			for name, value := range headers {
				w.Header().Set(name, value)
			}
			for _, cookie := range cookies {
				w.Header().Add("Set-Cookie", cookie)
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(html))
			return
		}
		switch r.URL.Path {
		case "/wp-admin/":
			w.WriteHeader(http.StatusMovedPermanently)
		case "/robots.txt", "/.well-known/security.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/admin/":
			w.WriteHeader(http.StatusForbidden)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_server_with_version
func TestBatch8TechDetectServerWithVersion(t *testing.T) {
	result := techDetectServer(batch8Header(map[string]string{"server": "nginx/1.21.0"}))
	if result == nil || result["name"] != "nginx" || result["version"] != "1.21.0" {
		t.Fatalf("server = %v", result)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_server_without_version
func TestBatch8TechDetectServerWithoutVersion(t *testing.T) {
	result := techDetectServer(batch8Header(map[string]string{"server": "cloudflare"}))
	if result == nil || result["name"] != "cloudflare" || result["version"] != nil {
		t.Fatalf("server = %v", result)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_no_server_header
func TestBatch8TechDetectNoServerHeader(t *testing.T) {
	if result := techDetectServer(http.Header{}); result != nil {
		t.Fatalf("server = %v, want nil", result)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_cloudflare_detected,
// tools/tests/tools/test_tech_stack_detector.py::test_vercel_detected,
// tools/tests/tools/test_tech_stack_detector.py::test_no_cdn
func TestBatch8TechDetectCDN(t *testing.T) {
	if got := techDetectCDN(batch8Header(map[string]string{"cf-ray": "123abc"})); got != "Cloudflare" {
		t.Fatalf("cf-ray cdn = %q", got)
	}
	if got := techDetectCDN(batch8Header(map[string]string{"x-vercel-id": "abc123"})); got != "Vercel" {
		t.Fatalf("x-vercel-id cdn = %q", got)
	}
	if got := techDetectCDN(batch8Header(map[string]string{"content-type": "text/html"})); got != "" {
		t.Fatalf("plain cdn = %q, want none", got)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_react_detected
func TestBatch8TechDetectReact(t *testing.T) {
	found := techDetectJSLibraries(`<script src="/static/react.min.js"></script>`)
	if len(found) != 1 || found[0] != "React" {
		t.Fatalf("libraries = %v", found)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_jquery_detected
func TestBatch8TechDetectJQueryVersion(t *testing.T) {
	found := techDetectJSLibraries(`<script src="https://cdn.example.com/jquery-3.6.0.min.js"></script>`)
	if len(found) != 1 || found[0] != "jQuery 3.6.0" {
		t.Fatalf("libraries = %v", found)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_nextjs_detected
func TestBatch8TechDetectNextJS(t *testing.T) {
	found := techDetectJSLibraries(`<script id="__NEXT_DATA__" type="application/json">{}</script>`)
	foundNext := false
	for _, library := range found {
		if library == "Next.js" {
			foundNext = true
		}
	}
	if !foundNext {
		t.Fatalf("libraries = %v", found)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_no_libraries
func TestBatch8TechDetectNoLibraries(t *testing.T) {
	if found := techDetectJSLibraries("<html><body>Simple page</body></html>"); len(found) != 0 {
		t.Fatalf("libraries = %v", found)
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_wordpress_detected,
// tools/tests/tools/test_tech_stack_detector.py::test_shopify_detected,
// tools/tests/tools/test_tech_stack_detector.py::test_drupal_detected,
// tools/tests/tools/test_tech_stack_detector.py::test_no_cms
func TestBatch8TechDetectCMS(t *testing.T) {
	cases := map[string]string{
		`<link href="/wp-content/themes/theme/style.css">`:                   "WordPress",
		`<script src="https://cdn.shopify.com/s/files/1/theme.js"></script>`: "Shopify",
		`<script src="/core/misc/drupal.js"></script>`:                       "Drupal",
	}
	for html, want := range cases {
		if got := techDetectCMSFromHTML(html); got != want {
			t.Fatalf("cms(%q) = %q, want %q", html, got, want)
		}
	}
	if got := techDetectCMSFromHTML("<html><body>Custom site</body></html>"); got != "" {
		t.Fatalf("custom cms = %q", got)
	}
}

// batch8TechFullHTML exercises the JS, CMS and analytics tables at once.
const batch8TechFullHTML = `<html><head>` +
	`<script src="/static/react.min.js"></script>` +
	`<script src="https://cdn.example.com/jquery-3.6.0.min.js"></script>` +
	`<link href="/wp-content/themes/x/style.css">` +
	`<script>gtag('config','UA-1')</script>` +
	`</head><body></body></html>`

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_detects_server
func TestBatch8TechFullDetection(t *testing.T) {
	server := batch8TechServer(t, batch8TechFullHTML, map[string]string{
		"Server":       "nginx/1.21.0",
		"X-Powered-By": "Express",
		"Cf-Ray":       "123abc",
	}, []string{
		"PHPSESSID=abc; Secure; HttpOnly; SameSite=Lax",
		"theme=dark; SameSite=None",
	})
	registry := newBatch8FinalRegistry()
	result := batch8ExecuteMap(t, registry, "tech_stack_detect", map[string]any{"url": server.URL})

	serverInfo, _ := result["server"].(map[string]any)
	if serverInfo["name"] != "nginx" || serverInfo["version"] != "1.21.0" {
		t.Fatalf("server = %v", result["server"])
	}
	if result["framework"] != "Express" {
		t.Fatalf("framework = %v", result["framework"])
	}
	if result["cms"] != "WordPress" {
		t.Fatalf("cms = %v", result["cms"])
	}
	if result["language"] != "Node.js" {
		t.Fatalf("language = %v", result["language"])
	}
	if result["cdn"] != "Cloudflare" {
		t.Fatalf("cdn = %v", result["cdn"])
	}
	libraries, _ := result["javascript_libraries"].([]string)
	if strings.Join(libraries, ",") != "React,jQuery 3.6.0" {
		t.Fatalf("libraries = %v", libraries)
	}
	analytics, _ := result["analytics"].([]string)
	if len(analytics) != 1 || analytics[0] != "Google Analytics" {
		t.Fatalf("analytics = %v", analytics)
	}
	if result["security_txt"] != true || result["robots_txt"] != true {
		t.Fatalf("security/robots = %v/%v", result["security_txt"], result["robots_txt"])
	}
	cookies, _ := result["cookies"].([]techCookie)
	if len(cookies) != 2 || cookies[0].Name != "PHPSESSID" || !cookies[0].Secure || !cookies[0].HTTPOnly {
		t.Fatalf("cookies = %+v", cookies)
	}
	if cookies[1].Secure || cookies[1].HTTPOnly {
		t.Fatalf("theme cookie flags = %+v", cookies[1])
	}
	if cookies[0].SameSite != "Lax" || cookies[1].SameSite != "None" {
		t.Fatalf("samesite = %v/%v", cookies[0].SameSite, cookies[1].SameSite)
	}
	// The response URL is authoritative, including trailing slash removal.
	if !strings.HasPrefix(result["url"].(string), server.URL) {
		t.Fatalf("url = %v", result["url"])
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_detects_framework
func TestBatch8TechFrameworkFromHeader(t *testing.T) {
	server := batch8TechServer(t, "<html></html>", map[string]string{"X-Powered-By": "Express"}, nil)
	result := batch8ExecuteMap(t, newBatch8FinalRegistry(), "tech_stack_detect", map[string]any{"url": server.URL})
	if result["framework"] != "Express" {
		t.Fatalf("framework = %v", result["framework"])
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_grade_input_keys_present
func TestBatch8TechGradeInputKeys(t *testing.T) {
	server := batch8TechServer(t, "<html></html>", nil, nil)
	result := batch8ExecuteMap(t, newBatch8FinalRegistry(), "tech_stack_detect", map[string]any{"url": server.URL})
	grade, ok := result["grade_input"].(map[string]any)
	if !ok {
		t.Fatalf("grade_input = %#v", result["grade_input"])
	}
	for _, key := range []string{
		"server_version_hidden", "framework_version_hidden", "security_txt_present",
		"cookies_secure", "cookies_httponly",
	} {
		if _, present := grade[key]; !present {
			t.Fatalf("grade_input misses %s: %v", key, grade)
		}
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_server_version_exposed
func TestBatch8TechServerVersionExposed(t *testing.T) {
	server := batch8TechServer(t, "<html></html>", map[string]string{"Server": "Apache/2.4.41"}, nil)
	result := batch8ExecuteMap(t, newBatch8FinalRegistry(), "tech_stack_detect", map[string]any{"url": server.URL})
	grade := result["grade_input"].(map[string]any)
	if grade["server_version_hidden"] != false {
		t.Fatalf("server_version_hidden = %v", grade["server_version_hidden"])
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_connection_error
func TestBatch8TechConnectionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	url := server.URL
	server.Close()

	result := batch8ExecuteMap(t, newBatch8FinalRegistry(), "tech_stack_detect", map[string]any{"url": url})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "Connection failed") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_tech_stack_detector.py::test_timeout_error
func TestBatch8TechTimeout(t *testing.T) {
	client := &http.Client{Transport: batch8RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	registry := newBatch8FinalRegistry(withBatch8Client(client))
	result := batch8ExecuteMap(t, registry, "tech_stack_detect", map[string]any{"url": "https://example.invalid"})
	message, _ := result["error"].(string)
	if !strings.Contains(message, "timed out") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity-exempt: path-probe CMS fallback is Go-side coverage of the probe table
// (upstream only covers the HTML helpers).
func TestBatch8TechCMSFromProbePath(t *testing.T) {
	server := batch8TechServer(t, "<html></html>", nil, nil)
	result := batch8ExecuteMap(t, newBatch8FinalRegistry(), "tech_stack_detect", map[string]any{"url": server.URL})
	if result["cms"] != "WordPress" {
		t.Fatalf("cms = %v (a 301 on /wp-admin/ should map to WordPress)", result["cms"])
	}
}
