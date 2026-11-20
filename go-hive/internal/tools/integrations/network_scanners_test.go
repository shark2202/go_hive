package integrations

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeDNSResolver struct {
	txt    map[string][]string
	mx     map[string][]dnsMXRecord
	ns     map[string][]string
	caa    map[string][]string
	dnskey map[string]bool
	zone   map[string]int
}

var errFakeNoRecords = errors.New("fake dns: no records")

func (f *fakeDNSResolver) TXT(_ context.Context, name string) ([]string, error) {
	if values, ok := f.txt[name]; ok {
		return values, nil
	}
	return nil, errFakeNoRecords
}

func (f *fakeDNSResolver) MX(_ context.Context, name string) ([]dnsMXRecord, error) {
	if values, ok := f.mx[name]; ok {
		return values, nil
	}
	return nil, errFakeNoRecords
}

func (f *fakeDNSResolver) NS(_ context.Context, name string) ([]string, error) {
	if values, ok := f.ns[name]; ok {
		return values, nil
	}
	return nil, errFakeNoRecords
}

func (f *fakeDNSResolver) CAA(_ context.Context, name string) ([]string, error) {
	if values, ok := f.caa[name]; ok {
		return values, nil
	}
	return nil, errFakeNoRecords
}

func (f *fakeDNSResolver) DNSKEY(_ context.Context, name string) (bool, error) {
	return f.dnskey[name], nil
}

func (f *fakeDNSResolver) ZoneTransfer(_ context.Context, nameserver, _ string) (int, error) {
	if count, ok := f.zone[nameserver]; ok {
		return count, nil
	}
	return 0, errFakeNoRecords
}

func batch2DNSFixture() *fakeDNSResolver {
	return &fakeDNSResolver{
		txt: map[string][]string{
			"example.com":                   {"v=spf1 include:_spf.google.com -all"},
			"_dmarc.example.com":            {"v=DMARC1; p=reject; rua=mailto:dmarc@example.com"},
			"google._domainkey.example.com": {"v=DKIM1; k=rsa; p=MIIB"},
		},
		mx:     map[string][]dnsMXRecord{"example.com": {{Preference: 10, Exchange: "mx.example.com"}}},
		ns:     map[string][]string{"example.com": {"ns1.example.com"}},
		caa:    map[string][]string{"example.com": {`0 issue "letsencrypt.org"`}},
		dnskey: map[string]bool{"example.com": true},
		zone:   map[string]int{"ns1.example.com": 42},
	}
}

// Parity: tools/tests/tools/test_dns_security_scanner.py::TestSpfChecks
func TestBatch2DNSSecurityScanSPF(t *testing.T) {
	registry := newBatch2Registry(withBatch2DNS(batch2DNSFixture()))
	result := batch2Execute(t, registry, "dns_security_scan", map[string]any{
		"domain": "https://example.com/login",
	})
	if result["domain"] != "example.com" {
		t.Fatalf("domain = %v", result["domain"])
	}
	spf, _ := result["spf"].(map[string]any)
	if spf["present"] != true || spf["policy"] != "hardfail" {
		t.Fatalf("spf = %v", spf)
	}
	if !strings.Contains(spf["record"].(string), "v=spf1") {
		t.Fatalf("record = %v", spf["record"])
	}
	dmarc, _ := result["dmarc"].(map[string]any)
	if dmarc["policy"] != "reject" {
		t.Fatalf("dmarc = %v", dmarc)
	}
	dkim, _ := result["dkim"].(map[string]any)
	found, _ := dkim["selectors_found"].([]string)
	if len(found) != 1 || found[0] != "google" {
		t.Fatalf("dkim = %v", dkim)
	}
	mx, _ := result["mx_records"].([]string)
	if len(mx) != 1 || mx[0] != "10 mx.example.com." {
		t.Fatalf("mx = %v", result["mx_records"])
	}
	caa, _ := result["caa_records"].([]string)
	if len(caa) != 1 || caa[0] != `0 issue "letsencrypt.org"` {
		t.Fatalf("caa = %v", result["caa_records"])
	}
	if dnssec, _ := result["dnssec"].(map[string]any); dnssec["enabled"] != true {
		t.Fatalf("dnssec = %v", result["dnssec"])
	}
}

// Parity: tools/tests/tools/test_dns_security_scanner.py::TestGradeInput
func TestBatch2DNSSecurityGradeInput(t *testing.T) {
	registry := newBatch2Registry(withBatch2DNS(batch2DNSFixture()))
	result := batch2Execute(t, registry, "dns_security_scan", map[string]any{"domain": "example.com"})
	grade, _ := result["grade_input"].(map[string]any)
	want := map[string]any{
		"spf_present":           true,
		"spf_strict":            true,
		"dmarc_present":         true,
		"dmarc_enforcing":       true,
		"dkim_found":            true,
		"dnssec_enabled":        true,
		"zone_transfer_blocked": false,
	}
	for key, expected := range want {
		if grade[key] != expected {
			t.Fatalf("grade[%s] = %v, want %v (full %v)", key, grade[key], expected, grade)
		}
	}
	zone, _ := result["zone_transfer"].(map[string]any)
	if zone["vulnerable"] != true || zone["record_count"] != 42 || zone["nameserver"] != "ns1.example.com" {
		t.Fatalf("zone = %v", zone)
	}
}

// Parity: tools/tests/tools/test_dns_security_scanner.py::TestDmarcChecks
func TestBatch2DNSSecuritySoftfail(t *testing.T) {
	resolver := &fakeDNSResolver{
		txt: map[string][]string{
			"soft.example":        {`"v=spf1 ~all"`},
			"_dmarc.soft.example": {"v=DMARC1; p=none"},
		},
	}
	registry := newBatch2Registry(withBatch2DNS(resolver))
	result := batch2Execute(t, registry, "dns_security_scan", map[string]any{"domain": "soft.example"})
	spf, _ := result["spf"].(map[string]any)
	if spf["policy"] != "softfail" {
		t.Fatalf("spf = %v", spf)
	}
	issues, _ := spf["issues"].([]string)
	if len(issues) != 1 || !strings.Contains(issues[0], "softfail") {
		t.Fatalf("issues = %v", spf["issues"])
	}
	dmarc, _ := result["dmarc"].(map[string]any)
	dmarcIssues, _ := dmarc["issues"].([]string)
	if len(dmarcIssues) != 1 || !strings.Contains(dmarcIssues[0], "'none'") {
		t.Fatalf("dmarc = %v", dmarc)
	}
	grade, _ := result["grade_input"].(map[string]any)
	if grade["spf_strict"] != false || grade["dmarc_enforcing"] != false {
		t.Fatalf("grade = %v", grade)
	}
}

func batch2HeaderServer(t *testing.T, headers map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, value := range headers {
			w.Header().Set(name, value)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// Parity: tools/tests/tools/test_http_headers_scanner.py::TestSecurityHeaders
func TestBatch2HTTPHeadersScan(t *testing.T) {
	server := batch2HeaderServer(t, map[string]string{
		"Strict-Transport-Security": "max-age=31536000",
		"Content-Security-Policy":   "default-src 'self'",
		"X-Frame-Options":           "DENY",
		"X-Content-Type-Options":    "nosniff",
		"Referrer-Policy":           "strict-origin-when-cross-origin",
		"Permissions-Policy":        "camera=()",
	})
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "http_headers_scan", map[string]any{"url": server.URL})
	if result["status_code"] != 200 {
		t.Fatalf("result = %v", result)
	}
	present, _ := result["headers_present"].([]string)
	if len(present) != 6 {
		t.Fatalf("present = %v", present)
	}
	missing, _ := result["headers_missing"].([]map[string]any)
	if len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	grade, _ := result["grade_input"].(map[string]any)
	if grade["hsts"] != true || grade["csp"] != true || grade["no_leaky_headers"] != true {
		t.Fatalf("grade = %v", grade)
	}
	if result["url"] != server.URL+"/" && result["url"] != server.URL {
		t.Fatalf("url = %v", result["url"])
	}
}

// Parity: tools/tests/tools/test_http_headers_scanner.py::TestInputValidation
func TestBatch2HTTPHeadersAutoPrefix(t *testing.T) {
	server := batch2HeaderServer(t, map[string]string{"Strict-Transport-Security": "max-age=1"})
	defer server.Close()
	registry := batch2TestRegistry(t, server)
	// A bare host is treated as https://; the plain-HTTP fake refuses the
	// handshake, which surfaces as a request error that names the https URL.
	result := batch2Execute(t, registry, "http_headers_scan", map[string]any{
		"url": strings.TrimPrefix(server.URL, "http://"),
	})
	if message, _ := result["error"].(string); !strings.Contains(message, "https://") {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_http_headers_scanner.py::TestLeakyHeaders
func TestBatch2HTTPHeadersLeakyAndMissing(t *testing.T) {
	server := batch2HeaderServer(t, map[string]string{
		"Server":       "nginx/1.25.3",
		"X-Powered-By": "Express",
	})
	defer server.Close()
	registry := batch2TestRegistry(t, server)

	result := batch2Execute(t, registry, "http_headers_scan", map[string]any{"url": server.URL})
	present, _ := result["headers_present"].([]string)
	if len(present) != 0 {
		t.Fatalf("present = %v", present)
	}
	missing, _ := result["headers_missing"].([]map[string]any)
	if len(missing) != 6 {
		t.Fatalf("missing = %v", missing)
	}
	leaky, _ := result["leaky_headers"].([]map[string]any)
	if len(leaky) != 2 || leaky[0]["header"] != "Server" || leaky[1]["header"] != "X-Powered-By" {
		t.Fatalf("leaky = %v", leaky)
	}
	if leaky[0]["value"] != "nginx/1.25.3" || leaky[0]["severity"] != "low" {
		t.Fatalf("leaky entry = %v", leaky[0])
	}
	grade, _ := result["grade_input"].(map[string]any)
	if grade["no_leaky_headers"] != false || grade["hsts"] != false {
		t.Fatalf("grade = %v", grade)
	}
}

// Parity: tools/tests/tools/test_ssl_tls_scanner.py::TestCertificateValidation
func TestBatch2SSLTLSInjectedCertificate(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	probe := func(context.Context, string, int) (*tlsProbeInfo, error) {
		return &tlsProbeInfo{
			Version:     "TLSv1.3",
			Cipher:      "TLS_AES_256_GCM_SHA384",
			CipherBits:  256,
			Subject:     "CN=example.com",
			Issuer:      "CN=Example CA",
			NotBefore:   now.AddDate(-1, 0, 0),
			NotAfter:    now.AddDate(0, 0, 10),
			SAN:         []string{"example.com", "www.example.com"},
			Fingerprint: strings.Repeat("ab", 32),
		}, nil
	}
	registry := newBatch2Registry(withBatch2TLS(probe))
	registry.Runtime().Now = func() time.Time { return now }

	result := batch2Execute(t, registry, "ssl_tls_scan", map[string]any{"hostname": "https://example.com/path"})
	if result["hostname"] != "example.com" || result["port"] != 443 {
		t.Fatalf("result = %v", result)
	}
	if result["tls_version"] != "TLSv1.3" || result["cipher_bits"] != 256 {
		t.Fatalf("result = %v", result)
	}
	certificate, _ := result["certificate"].(map[string]any)
	if certificate["days_until_expiry"] != 10 || certificate["self_signed"] != false {
		t.Fatalf("certificate = %v", certificate)
	}
	san, _ := certificate["san"].([]string)
	if len(san) != 2 || san[1] != "www.example.com" {
		t.Fatalf("san = %v", certificate["san"])
	}
	grade, _ := result["grade_input"].(map[string]any)
	if grade["cert_expiring_soon"] != true || grade["cert_valid"] != true || grade["strong_cipher"] != true || grade["tls_version_ok"] != true {
		t.Fatalf("grade = %v", grade)
	}
	issues, _ := result["issues"].([]map[string]any)
	if len(issues) != 1 || !strings.Contains(issues[0]["finding"].(string), "expires in 10 days") {
		t.Fatalf("issues = %v", issues)
	}
}

// Parity: tools/tests/tools/test_ssl_tls_scanner.py::TestTlsVersion
func TestBatch2SSLTLSWeakConfiguration(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	probe := func(context.Context, string, int) (*tlsProbeInfo, error) {
		return &tlsProbeInfo{
			Version:     "TLSv1.1",
			Cipher:      "TLS_RSA_WITH_3DES_EDE_CBC_SHA",
			CipherBits:  112,
			Subject:     "CN=old.example",
			Issuer:      "CN=old.example",
			NotAfter:    now.AddDate(0, 0, -3),
			SelfSigned:  true,
			Fingerprint: "deadbeef",
		}, nil
	}
	registry := newBatch2Registry(withBatch2TLS(probe))
	registry.Runtime().Now = func() time.Time { return now }

	result := batch2Execute(t, registry, "ssl_tls_scan", map[string]any{"hostname": "old.example"})
	if result["tls_version"] != "TLSv1.1" {
		t.Fatalf("version = %v", result["tls_version"])
	}
	grade, _ := result["grade_input"].(map[string]any)
	if grade["tls_version_ok"] != false || grade["cert_valid"] != false || grade["self_signed"] != true || grade["strong_cipher"] != false {
		t.Fatalf("grade = %v", grade)
	}
	issues, _ := result["issues"].([]map[string]any)
	findings := []string{}
	for _, issue := range issues {
		findings = append(findings, issue["finding"].(string))
	}
	for _, want := range []string{"Insecure TLS version", "Weak cipher suite", "has expired", "Self-signed certificate"} {
		matched := false
		for _, finding := range findings {
			if strings.Contains(finding, want) {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("missing finding %q in %v", want, findings)
		}
	}
}

// Parity: tools/tests/tools/test_ssl_tls_scanner.py::TestConnectionErrors
func TestBatch2SSLTLSRefused(t *testing.T) {
	probe := func(context.Context, string, int) (*tlsProbeInfo, error) {
		return nil, errScanRefused
	}
	registry := newBatch2Registry(withBatch2TLS(probe))
	result := batch2Execute(t, registry, "ssl_tls_scan", map[string]any{"hostname": "closed.example", "port": 8443})
	if result["error"] != "Connection to closed.example:8443 refused. Port may be closed." {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_ssl_tls_scanner.py::TestGradeInput
func TestBatch2SSLTLSRealHandshake(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	registry := newBatch2Registry()
	result := batch2Execute(t, registry, "ssl_tls_scan", map[string]any{
		"hostname": parsed.Hostname(),
		"port":     port,
	})
	version, _ := result["tls_version"].(string)
	if version != "TLSv1.2" && version != "TLSv1.3" {
		t.Fatalf("version = %v", result["tls_version"])
	}
	certificate, _ := result["certificate"].(map[string]any)
	if certificate["self_signed"] != true {
		t.Fatalf("certificate = %v", certificate)
	}
	fingerprint, _ := certificate["sha256_fingerprint"].(string)
	if len(fingerprint) != 64 {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if issues, _ := result["issues"].([]map[string]any); len(issues) == 0 {
		t.Fatalf("expected verification issues, got none")
	}
}

func loopbackListener(t *testing.T) (net.Listener, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("SSH-2.0-HiveTest\r\n"))
			_ = conn.Close()
		}
	}()
	return listener, listener.Addr().(*net.TCPAddr).Port
}

// Parity: tools/tests/tools/test_port_scanner.py::TestPortScanning
func TestBatch2PortScan(t *testing.T) {
	_, openPort := loopbackListener(t)
	closedListener, closedPort := loopbackListener(t)
	_ = closedListener.Close()

	registry := newBatch2Registry()
	result := batch2Execute(t, registry, "port_scan", map[string]any{
		"hostname": "127.0.0.1",
		"ports":    strconv.Itoa(openPort) + "," + strconv.Itoa(closedPort),
		"timeout":  2,
	})
	if result["ip"] != "127.0.0.1" || result["ports_scanned"] != 2 {
		t.Fatalf("result = %v", result)
	}
	openPorts, _ := result["open_ports"].([]map[string]any)
	if len(openPorts) != 1 || openPorts[0]["port"] != openPort {
		t.Fatalf("open = %v", result["open_ports"])
	}
	if openPorts[0]["service"] != "unknown" || !strings.Contains(openPorts[0]["banner"].(string), "SSH-2.0") {
		t.Fatalf("open entry = %v", openPorts[0])
	}
	closedPorts, _ := result["closed_ports"].([]int)
	if len(closedPorts) != 1 || closedPorts[0] != closedPort {
		t.Fatalf("closed = %v", result["closed_ports"])
	}
	if grade, _ := result["grade_input"].(map[string]any); grade["only_web_ports"] != false {
		t.Fatalf("grade = %v", result["grade_input"])
	}
}

// Parity: tools/tests/tools/test_port_scanner.py::TestRiskyPorts
func TestBatch2PortScanRiskyExposures(t *testing.T) {
	probe := func(_ context.Context, address string, _ time.Duration) (bool, string) {
		if strings.HasSuffix(address, ":3306") || strings.HasSuffix(address, ":21") || strings.HasSuffix(address, ":80") {
			return true, "banner"
		}
		return false, ""
	}
	resolve := func(context.Context, string) (string, error) { return "203.0.113.10", nil }
	registry := newBatch2Registry(withBatch2Ports(probe), withBatch2Hosts(resolve))

	result := batch2Execute(t, registry, "port_scan", map[string]any{
		"hostname": "risk.example",
		"ports":    "3306,21,80",
	})
	openPorts, _ := result["open_ports"].([]map[string]any)
	if len(openPorts) != 3 {
		t.Fatalf("open = %v", result["open_ports"])
	}
	byPort := map[int]map[string]any{}
	for _, entry := range openPorts {
		byPort[entry["port"].(int)] = entry
	}
	if byPort[3306]["severity"] != "high" || !strings.Contains(byPort[3306]["finding"].(string), "MySQL port (3306) exposed") {
		t.Fatalf("database entry = %v", byPort[3306])
	}
	if byPort[21]["severity"] != "medium" || !strings.Contains(byPort[21]["finding"].(string), "Legacy protocol FTP") {
		t.Fatalf("legacy entry = %v", byPort[21])
	}
	if _, present := byPort[80]["severity"]; present {
		t.Fatalf("http entry should not be risky: %v", byPort[80])
	}
	if result["ip"] != "203.0.113.10" || result["hostname"] != "risk.example" {
		t.Fatalf("result = %v", result)
	}
	grade, _ := result["grade_input"].(map[string]any)
	if grade["no_database_ports_exposed"] != false || grade["no_legacy_ports_exposed"] != false || grade["no_admin_ports_exposed"] != true {
		t.Fatalf("grade = %v", grade)
	}
}

// Parity: tools/tests/tools/test_port_scanner.py::TestPortLists
func TestBatch2PortScanTop100(t *testing.T) {
	probe := func(context.Context, string, time.Duration) (bool, string) { return false, "" }
	resolve := func(context.Context, string) (string, error) { return "127.0.0.1", nil }
	registry := newBatch2Registry(withBatch2Ports(probe), withBatch2Hosts(resolve))
	result := batch2Execute(t, registry, "port_scan", map[string]any{
		"hostname": "127.0.0.1",
		"ports":    "top100",
	})
	if result["ports_scanned"] != 95 {
		t.Fatalf("ports_scanned = %v", result["ports_scanned"])
	}
	closed, _ := result["closed_ports"].([]int)
	if len(closed) != 95 {
		t.Fatalf("closed = %d", len(closed))
	}
}

// Parity: tools/tests/tools/test_port_scanner.py::TestInputValidation
func TestBatch2PortScanValidation(t *testing.T) {
	registry := newBatch2Registry()
	result := batch2Execute(t, registry, "port_scan", map[string]any{
		"hostname": "127.0.0.1",
		"ports":    "eighty",
	})
	if !strings.Contains(result["error"].(string), "Invalid port list") {
		t.Fatalf("result = %v", result)
	}
	missing := batch2Execute(t, registry, "port_scan", map[string]any{"ports": "80"})
	if missing["error"] != "hostname is required" {
		t.Fatalf("missing = %v", missing)
	}
}
