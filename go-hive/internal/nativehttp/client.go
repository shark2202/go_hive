package nativehttp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/netguard"
)

var (
	errTooManyRedirects = errors.New("too many redirects")
	errRedirectLoop     = errors.New("redirect loop")
	errHTTPSDowngrade   = errors.New("https to http redirect refused")
)

type blockedError struct{ reason string }

func (e *blockedError) Error() string { return e.reason }

// Client is one Hive chat session's HTTP client: one transport, one in-memory
// cookie jar. No package-level state; WithRoot creates a fresh Client.
type Client struct {
	cfg    Config
	root   string
	home   string
	base   *http.Transport
	policy netguard.Policy
	jar    *cookieJar
}

// New builds a client. root is the session sandbox (path containment, spill
// files); home is $HIVE_HOME (already merged into cfg).
func New(cfg Config, root, home string) *Client {
	policy := netguard.Policy{
		AllowPrivateHosts: cfg.AllowPrivateHosts,
		AllowedHosts:      cfg.AllowedHosts,
		DeniedHosts:       cfg.DeniedHosts,
	}
	base := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           policy.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		cfg:    cfg,
		root:   root,
		home:   home,
		base:   base,
		policy: policy,
		jar:    newCookieJar(cfg.Cookies),
	}
}

// Close releases idle connections. The jar dies with the client.
func (c *Client) Close() error {
	if c == nil || c.base == nil {
		return nil
	}
	c.base.CloseIdleConnections()
	return nil
}

// ClearCookies empties this session's jar (logout).
func (c *Client) ClearCookies() {
	if c != nil && c.jar != nil {
		c.jar.Clear()
	}
}

// RejectedCookies reports how many cookies the policy refused.
func (c *Client) RejectedCookies() int {
	if c == nil || c.jar == nil {
		return 0
	}
	return c.jar.Rejected()
}

type callOptions struct {
	followRedirects  bool
	maxRedirects     int
	allowHTTPSToHTTP bool
	insecure         bool
	tls              TLSOptions
	proxy            string
	noProxy          bool
	httpVersion      string
	compressed       bool
	connectTimeout   time.Duration
	jarMode          string
}

type callState struct {
	mu sync.Mutex

	start time.Time

	timing     Timing
	tls        *TLSInfo
	remoteAddr string
	redirects  []Redirect
}

func newCallState() *callState {
	return &callState{start: time.Now()}
}

func (c *Client) newHTTPClient(opts callOptions, state *callState) (*http.Client, error) {
	transport := c.base.Clone()
	if opts.connectTimeout > 0 {
		policy := c.policy
		policy.Dialer = &net.Dialer{Timeout: opts.connectTimeout}
		transport.DialContext = policy.DialContext
	}
	switch {
	case opts.noProxy:
		transport.Proxy = nil
	case strings.TrimSpace(opts.proxy) != "":
		proxyURL, err := url.Parse(strings.TrimSpace(opts.proxy))
		if err != nil {
			return nil, fmt.Errorf("invalid proxy: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	default:
		transport.Proxy = http.ProxyFromEnvironment
	}
	tlsConfig, err := buildTLSConfig(opts, c.root)
	if err != nil {
		return nil, err
	}
	transport.TLSClientConfig = tlsConfig
	switch opts.httpVersion {
	case "1.1":
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	case "2":
		transport.ForceAttemptHTTP2 = true
	}
	transport.DisableCompression = !opts.compressed

	client := &http.Client{Transport: transport}
	if opts.jarMode != "none" && c.jar != nil && c.cfg.Cookies.Enabled {
		client.Jar = c.jar
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !opts.followRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= opts.maxRedirects {
			return errTooManyRedirects
		}
		if reason := c.policy.BlockReason(req.Context(), req.URL.String()); reason != "" {
			return &blockedError{reason: reason}
		}
		if len(via) > 0 {
			previous := via[len(via)-1].URL
			if !opts.allowHTTPSToHTTP && previous.Scheme == "https" && req.URL.Scheme == "http" {
				return errHTTPSDowngrade
			}
			for _, prior := range via {
				if prior.URL.String() == req.URL.String() {
					return errRedirectLoop
				}
			}
			if !sameHost(previous, req.URL) {
				req.Header.Del("Authorization")
				req.Header.Del("Cookie")
				req.Header.Del("Cookie2")
				req.Header.Del("Proxy-Authorization")
			}
		}
		if resp := req.Response; resp != nil {
			state.mu.Lock()
			state.redirects = append(state.redirects, Redirect{
				Status:   resp.StatusCode,
				URL:      req.URL.String(),
				Location: resp.Header.Get("Location"),
			})
			state.mu.Unlock()
		}
		return nil
	}
	return client, nil
}

func buildTLSConfig(opts callOptions, root string) (*tls.Config, error) {
	// insecure is the documented per-request "curl -k" opt-in and stays false
	// unless the caller sets it, so verification is on by default.
	config := &tls.Config{InsecureSkipVerify: opts.insecure, ServerName: opts.tls.ServerName} // #nosec G402 -- per-request curl -k opt-in, default false
	if opts.tls.CAFile != "" {
		path, err := resolveUserPath(root, opts.tls.CAFile, false)
		if err != nil {
			return nil, fmt.Errorf("ca_file: %w", err)
		}
		pem, err := readFileUnder(root, path)
		if err != nil {
			return nil, fmt.Errorf("read ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file contains no certificates")
		}
		config.RootCAs = pool
	}
	if opts.tls.ClientCertFile != "" || opts.tls.ClientKeyFile != "" {
		if opts.tls.ClientCertFile == "" || opts.tls.ClientKeyFile == "" {
			return nil, errors.New("client_cert_file and client_key_file must be set together")
		}
		certPath, err := resolveUserPath(root, opts.tls.ClientCertFile, false)
		if err != nil {
			return nil, fmt.Errorf("client_cert_file: %w", err)
		}
		keyPath, err := resolveUserPath(root, opts.tls.ClientKeyFile, false)
		if err != nil {
			return nil, fmt.Errorf("client_key_file: %w", err)
		}
		certPEM, err := readFileUnder(root, certPath)
		if err != nil {
			return nil, fmt.Errorf("read client_cert_file: %w", err)
		}
		keyPEM, err := readFileUnder(root, keyPath)
		if err != nil {
			return nil, fmt.Errorf("read client_key_file: %w", err)
		}
		certificate, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func (c *Client) doWithRetry(
	ctx context.Context,
	method string,
	retry RetryPolicy,
	attempt func(context.Context) (*http.Response, *callState, error),
) (*http.Response, *callState, int, error) {
	tries := 0
	for {
		response, state, err := attempt(ctx)
		if err != nil {
			if !retryableMethod(method, retry) || tries >= retry.Count || ctx.Err() != nil {
				return nil, state, tries, err
			}
			tries++
			if !sleep(ctx, retryDelay(retry, tries, nil)) {
				return nil, state, tries, ctx.Err()
			}
			continue
		}
		if !retryableStatus(response.StatusCode) || !retryableMethod(method, retry) || tries >= retry.Count {
			return response, state, tries, nil
		}
		delay := retryDelay(retry, tries+1, response)
		_ = response.Body.Close()
		tries++
		if !sleep(ctx, delay) {
			return nil, state, tries, ctx.Err()
		}
	}
}

func retryableMethod(method string, retry RetryPolicy) bool {
	if retry.Count <= 0 {
		return false
	}
	if retry.RetryNonIdempotent {
		return true
	}
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryDelay(retry RetryPolicy, attempt int, response *http.Response) time.Duration {
	delay := retry.Delay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	if attempt > 1 {
		delay *= time.Duration(1 << minInt(attempt-1, 5))
	}
	maxDelay := retry.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 8 * time.Second
	}
	if response != nil {
		if seconds, err := strconv.Atoi(strings.TrimSpace(response.Header.Get("Retry-After"))); err == nil && seconds > 0 {
			delay = time.Duration(minInt(seconds, 30)) * time.Second
		}
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	// Jitter spreads concurrent retries; it is timing, not a secret.
	jitter := time.Duration(rand.Int63n(int64(delay/5) + 1)) // #nosec G404 -- retry jitter is timing-only, never used as key material
	return delay + jitter
}

func sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func classifyError(err error) (kind, message string) {
	if err == nil {
		return "", ""
	}
	var pathErr *pathError
	if errors.As(err, &pathErr) {
		return pathErr.kind, pathErr.message
	}
	var blocked *blockedError
	if errors.As(err, &blocked) {
		return ErrSSRFBlocked, blocked.reason
	}
	switch {
	case errors.Is(err, errTooManyRedirects):
		return ErrTooManyRedirects, "Too many redirects"
	case errors.Is(err, errRedirectLoop):
		return ErrTooManyRedirects, "Redirect loop detected"
	case errors.Is(err, errHTTPSDowngrade):
		return ErrHTTPSDowngrade, "Refused redirect from https to http"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return ErrTimeout, "Request timed out"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ErrDNSFailed, "DNS resolution failed: " + dnsErr.Error()
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ErrTimeout, "Request timed out"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return ErrTLSFailed, "TLS verification failed: " + certErr.Error()
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "max_bytes"):
		return ErrRequestTooLarge, err.Error()
	case strings.Contains(lower, "proxyconnect"), strings.Contains(lower, "proxy"):
		return ErrProxyFailed, "Proxy connection failed: " + err.Error()
	case strings.Contains(lower, "tls"), strings.Contains(lower, "certificate"):
		return ErrTLSFailed, "TLS handshake failed: " + err.Error()
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "server misbehaving"):
		return ErrDNSFailed, "DNS resolution failed: " + err.Error()
	default:
		return ErrConnectFailed, "Request failed: " + err.Error()
	}
}

func sameHost(left, right *url.URL) bool {
	return strings.EqualFold(left.Hostname(), right.Hostname()) && left.Port() == right.Port()
}

func millis(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	return duration.Milliseconds()
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
