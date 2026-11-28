package nativehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

func (c *Client) Do(ctx context.Context, req Request) Result {
	result := Result{Tool: ToolRequest}
	if !c.cfg.Enabled {
		return result.fail(ErrConfigDisabled, "http tools are disabled by configuration")
	}
	if req.Cookies.Clear {
		c.ClearCookies()
	}
	target, err := normalizeURL(req.URL, req.Query, req.ReplaceQuery)
	if err != nil {
		return result.fail(ErrInvalidInput, err.Error())
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	result.URL = RedactURL(target.String(), c.cfg.RedactQueryKeys)
	result.Method = method
	if reason := c.policy.BlockReason(ctx, target.String()); reason != "" {
		return result.fail(ErrSSRFBlocked, reason)
	}
	if strings.TrimSpace(req.HTTPVersion) == "2" && target.Scheme == "http" {
		return result.fail(ErrInvalidInput, "http_version=2 requires https (h2c is not supported)")
	}
	body, contentType, err := buildBody(req)
	if err != nil {
		return result.fail(ErrInvalidInput, err.Error())
	}
	if int64(len(body)) > c.cfg.MaxUploadBytes {
		return result.fail(ErrRequestTooLarge, fmt.Sprintf("request body %d bytes exceeds max_upload_bytes %d", len(body), c.cfg.MaxUploadBytes))
	}
	timeout := c.timeout(req.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := optionsForRequest(req)
	response, state, tries, err := c.doWithRetry(ctx, method, req.Retry, func(ctx context.Context) (*http.Response, *callState, error) {
		state := newCallState()
		httpRequest, buildErr := c.buildHTTPRequest(ctx, method, target, body, contentType, req)
		if buildErr != nil {
			return nil, state, buildErr
		}
		client, clientErr := c.newHTTPClient(opts, state)
		if clientErr != nil {
			return nil, state, clientErr
		}
		response, doErr := client.Do(httpRequest)
		return response, state, doErr
	})
	if err != nil {
		kind, message := classifyError(err)
		return result.fail(kind, message)
	}
	if response == nil {
		return result.fail(ErrConnectFailed, "Request failed: empty response")
	}
	defer response.Body.Close()

	result = c.readResponse(response, state, req.Response)
	result.Tool = ToolRequest
	result.URL = RedactURL(target.String(), c.cfg.RedactQueryKeys)
	result.Method = method
	result.Retries = tries
	if req.FailOnHTTPError && !result.OK {
		result.ErrorKind = ErrHTTPStatus
		result.Error = fmt.Sprintf("HTTP %d: %s", result.Status, result.StatusText)
	}
	return result
}

func (c *Client) timeout(requested time.Duration) time.Duration {
	if requested <= 0 {
		requested = c.cfg.DefaultTimeout
	}
	if requested > c.cfg.MaxTimeout {
		requested = c.cfg.MaxTimeout
	}
	return requested
}

func optionsForRequest(req Request) callOptions {
	return callOptions{
		followRedirects:  req.FollowRedirects,
		maxRedirects:     req.MaxRedirects,
		allowHTTPSToHTTP: req.AllowHTTPSToHTTP,
		insecure:         req.Insecure,
		tls:              req.TLS,
		proxy:            req.Proxy,
		noProxy:          req.NoProxy,
		httpVersion:      req.HTTPVersion,
		compressed:       req.Compressed,
		connectTimeout:   req.ConnectTimeout,
		jarMode:          req.Cookies.Jar,
	}
}

func normalizeURL(raw string, query url.Values, replaceQuery bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url is required")
	}
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return nil, errors.New("invalid URL: missing hostname")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, errors.New("invalid URL: only http and https are allowed")
	}
	parsed.Scheme = scheme
	if len(query) > 0 {
		values := parsed.Query()
		if replaceQuery {
			values = url.Values{}
		}
		for key, items := range query {
			for _, item := range items {
				values.Add(key, item)
			}
		}
		parsed.RawQuery = values.Encode()
	}
	return parsed, nil
}

// buildBody returns exactly one body source. A conflict is invalid_input.
func buildBody(req Request) ([]byte, string, error) {
	sources := 0
	if req.Body != nil {
		sources++
	}
	if req.JSON != nil {
		sources++
	}
	if req.Form != nil {
		sources++
	}
	if req.Multipart != nil {
		sources++
	}
	if sources > 1 {
		return nil, "", errors.New("body, json, form and multipart are mutually exclusive")
	}
	switch {
	case req.JSON != nil:
		body, err := json.Marshal(req.JSON)
		if err != nil {
			return nil, "", fmt.Errorf("encode json body: %w", err)
		}
		return body, "application/json", nil
	case req.Form != nil:
		return []byte(req.Form.Encode()), "application/x-www-form-urlencoded", nil
	case req.Multipart != nil:
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		for key, values := range req.Multipart.Fields {
			for _, value := range values {
				if err := writer.WriteField(key, value); err != nil {
					return nil, "", err
				}
			}
		}
		for field, filePart := range req.Multipart.Files {
			filename := filePart.Filename
			if filename == "" {
				filename = field
			}
			contentType := filePart.ContentType
			if contentType == "" {
				contentType = "application/octet-stream"
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, sanitizeFilename(filename)))
			header.Set("Content-Type", contentType)
			partWriter, err := writer.CreatePart(header)
			if err != nil {
				return nil, "", err
			}
			if _, err := partWriter.Write([]byte(filePart.Content)); err != nil {
				return nil, "", err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", err
		}
		return buffer.Bytes(), writer.FormDataContentType(), nil
	case req.Body != nil:
		return req.Body, "", nil
	default:
		return nil, "", nil
	}
}

func (c *Client) buildHTTPRequest(ctx context.Context, method string, target *url.URL, body []byte, contentType string, req Request) (*http.Request, error) {
	var reader *bytes.Reader
	if method != http.MethodGet && method != http.MethodHead || len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	var httpRequest *http.Request
	var err error
	if reader == nil {
		httpRequest, err = http.NewRequestWithContext(ctx, method, target.String(), nil)
	} else {
		httpRequest, err = http.NewRequestWithContext(ctx, method, target.String(), reader)
	}
	if err != nil {
		return nil, err
	}
	for name, values := range req.Headers {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}
	if httpRequest.Header.Get("User-Agent") == "" && c.cfg.UserAgent != "" {
		httpRequest.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	if contentType != "" && httpRequest.Header.Get("Content-Type") == "" {
		httpRequest.Header.Set("Content-Type", contentType)
	}
	if req.Auth != nil && httpRequest.Header.Get("Authorization") == "" {
		switch strings.ToLower(strings.TrimSpace(req.Auth.Type)) {
		case "basic":
			httpRequest.SetBasicAuth(req.Auth.Username, req.Auth.Password)
		case "bearer":
			httpRequest.Header.Set("Authorization", "Bearer "+req.Auth.Token)
		default:
			return nil, fmt.Errorf("unsupported auth type %q", req.Auth.Type)
		}
	}
	for name, value := range req.Cookies.Send {
		// Secure/HttpOnly/SameSite are response attributes: net/http serializes
		// only Name=Value into the outgoing Cookie request header.
		httpRequest.AddCookie(&http.Cookie{Name: name, Value: value}) // #nosec G124 -- outgoing request cookie, not a Set-Cookie response
	}
	return httpRequest, nil
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	name = strings.ReplaceAll(name, "\x00", "")
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "upload.bin"
	}
	return name
}
