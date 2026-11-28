// LLM proxy surface: POST /v1/messages (Anthropic Messages pass-through) and
// POST /v1/images/generations (client-contract pass-through).
//
// The M3 Bearer credential check is unchanged and runs before any upstream
// call. The upstream base URL, credential and auth header style are explicit
// configuration: model routing, billing and provider-key management are not
// guessed here. See docs/m5-notes.md for the evidence boundary.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
)

const (
	// LLMTokenInvalidType is the error type the hive-llm proxy is observed to
	// return for a bad/expired streamToken:
	//   HTTP 401 {"error":{"type":"hive_stream_token_invalid", ...}}
	// The Python client only matches the marker inside the body
	// (core/framework/llm/litellm.py::_is_hive_stream_token_invalid).
	LLMTokenInvalidType = "hive_stream_token_invalid"

	// Local proxy error types. They are not observed cloud contract values;
	// they make configuration/transport failures explicit instead of faking a
	// completion. The envelope follows the proxy shape documented by
	// tools/src/aden_tools/tools/image_gen_tool/image_gen_tool.py::_proxy_error:
	//   {"type":"error","error":{"type","message"}}
	LLMUpstreamNotConfiguredType = "llm_upstream_not_configured"
	LLMUpstreamUnavailableType   = "llm_upstream_unavailable"
	LLMUpstreamTimeoutType       = "llm_upstream_timeout"
	LLMRequestTooLargeType       = "llm_request_too_large"

	// maxLLMRequestBody bounds one proxied request. Anthropic's documented
	// request ceiling is ~32MB; 64MB leaves room for base64 image blocks while
	// still bounding memory/bandwidth in a private deployment.
	maxLLMRequestBody = 64 << 20
)

const (
	defaultLLMUpstreamAuth  = "x-api-key"
	defaultAnthropicVersion = "2023-06-01"
	authStyleAPIKey         = "x-api-key"
	authStyleBearer         = "bearer"
)

// llmProxy holds the resolved upstream configuration. An empty endpoint means
// "not configured": the handlers answer an explicit error and never return a
// fake 200.
type llmProxy struct {
	messagesURL      string
	imagesURL        string
	messagesKey      string
	imagesKey        string
	authStyle        string
	anthropicVersion string
	requestTimeout   time.Duration
	imagesTimeout    time.Duration
	client           *http.Client
	imagesClient     *http.Client
	logger           *log.Logger
}

func newLLMProxy(cfg Config) (*llmProxy, error) {
	p := &llmProxy{
		messagesKey:      strings.TrimSpace(cfg.LLMUpstreamKey),
		imagesKey:        strings.TrimSpace(cfg.ImagesUpstreamKey),
		authStyle:        strings.TrimSpace(cfg.LLMUpstreamAuth),
		anthropicVersion: strings.TrimSpace(cfg.LLMAnthropicVersion),
		requestTimeout:   cfg.LLMRequestTimeout,
		imagesTimeout:    cfg.ImagesRequestTimeout,
		client:           cfg.LLMUpstreamClient,
		imagesClient:     cfg.ImagesUpstreamClient,
		logger:           cfg.Logger,
	}
	if p.authStyle == "" {
		p.authStyle = defaultLLMUpstreamAuth
	}
	switch p.authStyle {
	case authStyleAPIKey, authStyleBearer:
	default:
		return nil, fmt.Errorf("httpapi: llm upstream auth %q must be %q or %q", p.authStyle, authStyleAPIKey, authStyleBearer)
	}
	if p.anthropicVersion == "" {
		p.anthropicVersion = defaultAnthropicVersion
	}
	if p.requestTimeout <= 0 {
		p.requestTimeout = 10 * time.Minute
	}
	if p.imagesTimeout <= 0 {
		p.imagesTimeout = 225 * time.Second
	}
	var err error
	if p.messagesURL, err = normalizeMessagesURL(cfg.LLMUpstreamURL); err != nil {
		return nil, err
	}
	if p.imagesURL, err = normalizeImagesURL(cfg.ImagesUpstreamURL); err != nil {
		return nil, err
	}
	if p.client == nil {
		p.client = newUpstreamClient()
	}
	if p.imagesClient == nil {
		p.imagesClient = newUpstreamClient()
	}
	return p, nil
}

// normalizeMessagesURL accepts a base URL or a full /v1/messages endpoint and
// returns the exact upstream endpoint. Empty means "not configured".
func normalizeMessagesURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := parseUpstreamURL(raw)
	if err != nil {
		return "", fmt.Errorf("httpapi: llm upstream url: %w", err)
	}
	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/v1/messages"):
		u.Path = path
	case path == "":
		u.Path = "/v1/messages"
	case strings.HasSuffix(path, "/v1"):
		u.Path = path + "/messages"
	default:
		u.Path = path + "/v1/messages"
	}
	return u.String(), nil
}

// normalizeImagesURL accepts a base URL, a /v1 base, or a full images endpoint.
// Empty means "not configured"; a non-empty custom path is used as-is.
func normalizeImagesURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := parseUpstreamURL(raw)
	if err != nil {
		return "", fmt.Errorf("httpapi: images upstream url: %w", err)
	}
	path := strings.TrimRight(u.Path, "/")
	switch {
	case path == "":
		u.Path = "/v1/images/generations"
	case strings.HasSuffix(path, "/v1"):
		u.Path = path + "/images/generations"
	case strings.HasSuffix(path, "/v1/images/generations"):
		u.Path = path
	default:
		u.Path = path
	}
	return u.String(), nil
}

func parseUpstreamURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("host is required")
	}
	return u, nil
}

// newUpstreamClient keeps redirects un-followed so an upstream 3xx is returned
// to the caller instead of replaying the credential to a redirect target.
func newUpstreamClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// verifyLLMCredential is the M3 boundary: a Bearer streamToken or an operator
// JWT for a usable team. It writes the observed 401 body on failure.
func (s *Server) verifyLLMCredential(w http.ResponseWriter, r *http.Request) bool {
	token, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeLLMTokenInvalid(w)
		return false
	}
	if _, err := auth.VerifyStreamToken(s.cfg.StreamSecrets, token); err == nil {
		return true
	}
	claims, err := auth.Verify(s.cfg.JWTSecret, token)
	if err != nil || claims.Typ == auth.TokenTypeStream ||
		claims.UserID == "" || !auth.ValidTeamID(claims.TeamID) {
		writeLLMTokenInvalid(w)
		return false
	}
	return true
}

// handleLLMMessages proxies an Anthropic Messages request to the configured
// upstream. A valid credential with no configured upstream gets an explicit
// 400 (llm_upstream_not_configured): the go_hive keycheck treats 200/400/429
// as a valid `hive` key, so the M3 credential smoke stays meaningful without
// ever faking a completion.
func (s *Server) handleLLMMessages(w http.ResponseWriter, r *http.Request) {
	if !s.verifyLLMCredential(w, r) {
		return
	}
	if s.llm == nil || s.llm.messagesURL == "" {
		writeLLMProxyError(w, http.StatusBadRequest, LLMUpstreamNotConfiguredType,
			"credential valid; no LLM upstream is configured (set --llm-upstream-url)")
		return
	}
	s.llm.proxyMessages(w, r)
}

// handleLLMImages proxies the client-contract images request to the configured
// upstream. The observed client sends JSON (including base64 `image` entries
// for edits) with Authorization: Bearer <HIVE_API_KEY>; the body is forwarded
// verbatim so unknown fields are not dropped.
func (s *Server) handleLLMImages(w http.ResponseWriter, r *http.Request) {
	if !s.verifyLLMCredential(w, r) {
		return
	}
	if s.llm == nil || s.llm.imagesURL == "" {
		writeLLMProxyError(w, http.StatusServiceUnavailable, LLMUpstreamNotConfiguredType,
			"credential valid; no images upstream is configured (set --images-upstream-url)")
		return
	}
	s.llm.proxyImages(w, r)
}

func (p *llmProxy) proxyMessages(w http.ResponseWriter, r *http.Request) {
	body, err := capRequestBody(r, maxLLMRequestBody)
	if err != nil {
		writeLLMProxyError(w, http.StatusRequestEntityTooLarge, LLMRequestTooLargeType, "request body exceeds the 64MB proxy limit")
		return
	}
	defer body.Close()

	ctx, cancel := context.WithTimeout(r.Context(), p.requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.messagesURL, body)
	if err != nil {
		p.logger.Printf("[cloud] llm build request: %v", err)
		writeLLMProxyError(w, http.StatusBadGateway, LLMUpstreamUnavailableType, "could not build the upstream request")
		return
	}
	req.ContentLength = r.ContentLength
	if r.ContentLength < 0 {
		req.ContentLength = -1
	}
	forwardMessagesHeaders(req.Header, r.Header, p.anthropicVersion, p.authStyle, p.messagesKey)

	resp, err := p.client.Do(req)
	if err != nil {
		p.writeUpstreamFailure(w, r, err, "LLM")
		return
	}
	defer resp.Body.Close()
	copyUpstreamHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if isEventStream(resp.Header.Get("Content-Type")) {
		if err := relaySSE(w, resp.Body); err != nil && !isClientGone(r, err) {
			p.logger.Printf("[cloud] llm SSE relay ended: %v", err)
		}
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil && !isClientGone(r, err) {
		p.logger.Printf("[cloud] llm response relay ended: %v", err)
	}
}

func (p *llmProxy) proxyImages(w http.ResponseWriter, r *http.Request) {
	body, err := capRequestBody(r, maxLLMRequestBody)
	if err != nil {
		writeLLMProxyError(w, http.StatusRequestEntityTooLarge, LLMRequestTooLargeType, "request body exceeds the 64MB proxy limit")
		return
	}
	defer body.Close()

	ctx, cancel := context.WithTimeout(r.Context(), p.imagesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.imagesURL, body)
	if err != nil {
		p.logger.Printf("[cloud] images build request: %v", err)
		writeLLMProxyError(w, http.StatusBadGateway, LLMUpstreamUnavailableType, "could not build the upstream request")
		return
	}
	req.ContentLength = r.ContentLength
	if r.ContentLength < 0 {
		req.ContentLength = -1
	}
	forwardImagesHeaders(req.Header, r.Header, p.imagesKey)

	resp, err := p.client.Do(req)
	if err != nil {
		p.writeUpstreamFailure(w, r, err, "images")
		return
	}
	defer resp.Body.Close()
	copyUpstreamHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil && !isClientGone(r, err) {
		p.logger.Printf("[cloud] images response relay ended: %v", err)
	}
}

// writeUpstreamFailure maps transport/timeout failures without leaking the
// upstream URL or credential to the client. A client cancellation is silent:
// there is nobody left to answer.
func (p *llmProxy) writeUpstreamFailure(w http.ResponseWriter, r *http.Request, err error, label string) {
	if errors.Is(err, errRequestBodyTooLarge) {
		writeLLMProxyError(w, http.StatusRequestEntityTooLarge, LLMRequestTooLargeType, "request body exceeds the 64MB proxy limit")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeLLMProxyError(w, http.StatusGatewayTimeout, LLMUpstreamTimeoutType, label+" upstream timed out")
		return
	}
	p.logger.Printf("[cloud] %s upstream error: %v", label, err)
	writeLLMProxyError(w, http.StatusBadGateway, LLMUpstreamUnavailableType, label+" upstream request failed")
}

func writeLLMTokenInvalid(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error": map[string]any{"type": LLMTokenInvalidType},
	})
}

// writeLLMProxyError uses the proxy error envelope documented by the image
// client (`{"type":"error","error":{...}}`), not Anthropic's own envelope,
// because these errors are produced by this proxy before/without an upstream.
func writeLLMProxyError(w http.ResponseWriter, status int, kind, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    kind,
			"message": message,
		},
	})
}

func isClientGone(r *http.Request, err error) bool {
	return r.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed)
}
