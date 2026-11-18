package integrations

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// tlsProbeInfo is the certificate and connection data ssl_tls_scan analyses.
// Keeping the analysis separate from the handshake lets tests inject fixed
// certificates instead of negotiating TLS.
type tlsProbeInfo struct {
	Version     string
	Cipher      string
	CipherBits  int
	Subject     string
	Issuer      string
	NotBefore   time.Time
	NotAfter    time.Time
	SAN         []string
	SelfSigned  bool
	Fingerprint string
	VerifyError string
}

// tlsProbe performs the TLS handshake for one host/port pair.
type tlsProbe func(ctx context.Context, hostname string, port int) (*tlsProbeInfo, error)

var (
	// errScanTimeout marks probe timeouts so actions can return the upstream
	// "timed out" wording.
	errScanTimeout = errors.New("scan: timeout")
	// errScanRefused marks a refused TCP connection.
	errScanRefused = errors.New("scan: connection refused")
)

// cipherBits approximates the key size Go's CipherSuiteState does not expose.
func cipherBits(name string) int {
	upper := strings.ToUpper(name)
	switch {
	case strings.Contains(upper, "AES_256"), strings.Contains(upper, "CHACHA20"), strings.Contains(upper, "AES256"):
		return 256
	case strings.Contains(upper, "3DES"), strings.Contains(upper, "DES"):
		return 112
	case strings.Contains(upper, "RC4_40"):
		return 40
	case strings.Contains(upper, "NULL"):
		return 0
	default:
		return 128
	}
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "TLSv1.3"
	case tls.VersionTLS12:
		return "TLSv1.2"
	case tls.VersionTLS11:
		return "TLSv1.1"
	case tls.VersionTLS10:
		return "TLSv1"
	default:
		return "unknown"
	}
}

// tlsInsecureVersion mirrors INSECURE_TLS_VERSIONS in the upstream module.
func tlsInsecureVersion(version string) bool {
	switch version {
	case "TLSv1", "TLSv1.0", "TLSv1.1", "SSLv2", "SSLv3":
		return true
	default:
		return false
	}
}

// defaultTLSProbe dials the host, verifies the certificate and, on
// verification failure, retries without verification to still describe the
// certificate (the same two-phase approach as the Python scanner).
func defaultTLSProbe(ctx context.Context, hostname string, port int) (*tlsProbeInfo, error) {
	address := net.JoinHostPort(hostname, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, scanDialError(err)
	}
	defer rawConn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = rawConn.SetDeadline(deadline)
	} else {
		_ = rawConn.SetDeadline(time.Now().Add(10 * time.Second))
	}

	info, verifyErr := tlsHandshakeInfo(ctx, rawConn, hostname, false)
	if info != nil {
		return info, nil
	}
	if verifyErr == nil {
		return nil, errors.New("TLS handshake failed")
	}
	// Retry without verification to describe the rejected certificate.
	rawConn, err = dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, scanDialError(err)
	}
	defer rawConn.Close()
	_ = rawConn.SetDeadline(time.Now().Add(10 * time.Second))
	info, handshakeErr := tlsHandshakeInfo(ctx, rawConn, hostname, true)
	if info == nil {
		if handshakeErr != nil {
			return nil, scanDialError(handshakeErr)
		}
		return nil, errors.New("TLS handshake failed")
	}
	info.VerifyError = verifyErr.Error()
	return info, nil
}

// tlsHandshakeInfo runs one handshake and extracts the certificate details.
func tlsHandshakeInfo(ctx context.Context, conn net.Conn, hostname string, insecure bool) (*tlsProbeInfo, error) {
	config := &tls.Config{
		ServerName:         hostname,
		InsecureSkipVerify: insecure, // #nosec G402 -- inspection requires it
		MinVersion:         tls.VersionTLS10,
	}
	client := tls.Client(conn, config)
	if err := client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	state := client.ConnectionState()
	info := &tlsProbeInfo{
		Version:    tlsVersionName(state.Version),
		Cipher:     tls.CipherSuiteName(state.CipherSuite),
		CipherBits: cipherBits(tls.CipherSuiteName(state.CipherSuite)),
	}
	if len(state.PeerCertificates) > 0 {
		leaf := state.PeerCertificates[0]
		info.Subject = leaf.Subject.String()
		info.Issuer = leaf.Issuer.String()
		info.NotBefore = leaf.NotBefore
		info.NotAfter = leaf.NotAfter
		info.SAN = append([]string(nil), leaf.DNSNames...)
		info.SelfSigned = len(leaf.RawSubject) > 0 && string(leaf.RawSubject) == string(leaf.RawIssuer)
		sum := sha256.Sum256(leaf.Raw)
		info.Fingerprint = hex.EncodeToString(sum[:])
	}
	_ = client.Close()
	return info, nil
}

// scanDialError maps transport failures onto the scanner's error vocabulary.
func scanDialError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: %v", errScanRefused, err)
	}
	if errTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", errScanTimeout, err)
	}
	return err
}

// tlsProbeSucceeded reports whether verification succeeded.
func (info *tlsProbeInfo) verified() bool { return info.VerifyError == "" }

func sslTLSScan(ctx context.Context, rt *Runtime, input map[string]any, probe tlsProbe) (any, error) {
	hostname := cleanScanTarget(stringArg(input, "hostname"))
	if hostname == "" {
		return map[string]any{"error": "hostname is required"}, nil
	}
	port := intArg(input, "port", 443)
	if probe == nil {
		probe = defaultTLSProbe
	}
	info, err := probe(ctx, hostname, port)
	if err != nil {
		if errors.Is(err, errScanTimeout) || ctx.Err() == context.DeadlineExceeded {
			return map[string]any{"error": fmt.Sprintf("Connection to %s:%d timed out", hostname, port)}, nil
		}
		if errors.Is(err, errScanRefused) {
			return map[string]any{"error": fmt.Sprintf("Connection to %s:%d refused. Port may be closed.", hostname, port)}, nil
		}
		return map[string]any{"error": "Connection failed: " + err.Error()}, nil
	}

	now := rt.Now()
	issues := []map[string]any{}
	versionOK := !tlsInsecureVersion(info.Version)
	if !versionOK {
		issues = append(issues, map[string]any{
			"severity":    "high",
			"finding":     "Insecure TLS version: " + info.Version,
			"remediation": "Disable TLS 1.0 and 1.1 in your server configuration. Use TLS 1.2 or 1.3 only.",
		})
	}

	strongCipher := true
	upperCipher := strings.ToUpper(info.Cipher)
	for _, weak := range []string{"RC4", "DES", "3DES", "MD5", "NULL", "EXPORT", "ANON"} {
		if strings.Contains(upperCipher, weak) {
			strongCipher = false
			issues = append(issues, map[string]any{
				"severity":    "high",
				"finding":     "Weak cipher suite: " + info.Cipher,
				"remediation": "Configure your server to use strong cipher suites only. Prefer AES-GCM and ChaCha20-Poly1305.",
			})
			break
		}
	}
	if info.CipherBits > 0 && info.CipherBits < 128 {
		strongCipher = false
		issues = append(issues, map[string]any{
			"severity":    "high",
			"finding":     fmt.Sprintf("Cipher key length too short: %d bits", info.CipherBits),
			"remediation": "Use cipher suites with at least 128-bit keys.",
		})
	}

	certValid := true
	certExpiringSoon := false
	var daysUntilExpiry any
	if !info.NotAfter.IsZero() {
		days := int(info.NotAfter.Sub(now).Hours() / 24)
		daysUntilExpiry = days
		if now.After(info.NotAfter) {
			certValid = false
			issues = append(issues, map[string]any{
				"severity":    "critical",
				"finding":     "SSL certificate has expired",
				"remediation": "Renew the SSL certificate immediately.",
			})
		} else if days <= 30 {
			certExpiringSoon = true
			issues = append(issues, map[string]any{
				"severity":    "medium",
				"finding":     fmt.Sprintf("SSL certificate expires in %d days", days),
				"remediation": "Renew the SSL certificate before it expires.",
			})
		}
	}
	if !info.verified() {
		issues = append(issues, map[string]any{
			"severity":    "critical",
			"finding":     "SSL certificate verification failed: " + info.VerifyError,
			"remediation": "Obtain a valid certificate from a trusted CA. Let's Encrypt provides free certificates.",
		})
	}
	if info.SelfSigned {
		certValid = false
		issues = append(issues, map[string]any{
			"severity":    "high",
			"finding":     "Self-signed certificate detected",
			"remediation": "Replace with a certificate from a trusted CA. Let's Encrypt provides free certificates.",
		})
	}

	notBefore := any("")
	if !info.NotBefore.IsZero() {
		notBefore = info.NotBefore.UTC().Format(time.RFC3339)
	}
	notAfter := any("")
	if !info.NotAfter.IsZero() {
		notAfter = info.NotAfter.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"hostname":    hostname,
		"port":        port,
		"tls_version": info.Version,
		"cipher":      info.Cipher,
		"cipher_bits": info.CipherBits,
		"certificate": map[string]any{
			"subject":            info.Subject,
			"issuer":             info.Issuer,
			"not_before":         notBefore,
			"not_after":          notAfter,
			"days_until_expiry":  daysUntilExpiry,
			"san":                info.SAN,
			"self_signed":        info.SelfSigned,
			"sha256_fingerprint": info.Fingerprint,
		},
		"issues": issues,
		"grade_input": map[string]any{
			"tls_version_ok":     versionOK,
			"cert_valid":         certValid,
			"cert_expiring_soon": certExpiringSoon,
			"strong_cipher":      strongCipher,
			"self_signed":        info.SelfSigned,
		},
	}, nil
}
