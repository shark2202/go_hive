package nativehttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func (c *Client) readResponse(response *http.Response, state *callState, opts ResponseOptions) Result {
	result := Result{
		Status:      response.StatusCode,
		StatusText:  http.StatusText(response.StatusCode),
		OK:          response.StatusCode >= 200 && response.StatusCode < 300,
		HTTPVersion: response.Proto,
		ContentType: response.Header.Get("Content-Type"),
		Charset:     charsetOf(response.Header.Get("Content-Type")),
		Rejected:    c.RejectedCookies(),
	}
	if opts.IncludeHeaders {
		result.Headers = RedactHeaders(response.Header, opts.IncludeCookies)
	}
	if opts.IncludeCookies {
		result.Cookies = cookieInfos(response.Cookies())
	}
	if !opts.IncludeBody {
		c.finishState(state, &result)
		return result
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = c.cfg.MaxResponseBytes
	}
	body, tooLarge, err := readBounded(response.Body, maxBytes)
	if err != nil {
		return result.fail(ErrReadFailed, "read response: "+err.Error())
	}
	if tooLarge {
		return result.fail(ErrResponseTooLarge, fmt.Sprintf("response exceeds max_bytes (%d)", maxBytes))
	}
	sum := sha256.Sum256(body)
	result.BodySHA256 = hex.EncodeToString(sum[:])
	result.BodyBytes = int64(len(body))

	inline := opts.MaxInlineBytes
	if inline <= 0 {
		inline = c.cfg.MaxInlineBytes
	}
	binary := isBinary(body)
	full := body
	truncated := false
	savedPath := ""
	save := func() bool {
		path, writeErr := spillPath(c.root, opts.Path, response.Request.Method, response.Request.URL.String())
		if writeErr != nil {
			kind, message := pathFailure(writeErr, ErrWriteFailed)
			result = result.fail(kind, message)
			return false
		}
		if writeErr = atomicWrite(path, full); writeErr != nil {
			result = result.fail(ErrWriteFailed, writeErr.Error())
			return false
		}
		savedPath = path
		return true
	}
	if result.Error == "" {
		switch opts.Mode {
		case "file":
			if !save() {
				return result
			}
		case "inline":
			if int64(len(full)) > inline {
				if binary {
					// Truncating binary is misleading; spill it instead.
					if !save() {
						return result
					}
				} else {
					full = full[:inline]
					truncated = true
				}
			}
		default: // auto
			if int64(len(full)) > inline && !save() {
				return result
			}
		}
	}
	if savedPath != "" {
		result.Body = ""
		result.BodyEncoding = "file"
		result.SavedTo = &savedPath
	} else if binary || !utf8.Valid(full) {
		result.Body = base64.StdEncoding.EncodeToString(full)
		result.BodyEncoding = "base64"
	} else {
		result.Body = string(full)
		result.BodyEncoding = "utf8"
	}
	result.Truncated = truncated
	c.finishState(state, &result)
	return result
}

func (c *Client) finishState(state *callState, result *Result) {
	if state == nil {
		return
	}
	state.mu.Lock()
	state.timing.TotalMS = millis(time.Since(state.start))
	result.TimingMS = state.timing
	result.RemoteAddr = state.remoteAddr
	result.TLS = state.tls
	result.Redirects = append([]Redirect(nil), state.redirects...)
	state.mu.Unlock()
}

func readBounded(reader io.Reader, max int64) ([]byte, bool, error) {
	if max <= 0 {
		max = 1
	}
	body, err := io.ReadAll(io.LimitReader(reader, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > max {
		return body[:max], true, nil
	}
	return body, false, nil
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	contentType := strings.ToLower(http.DetectContentType(data))
	if strings.HasPrefix(contentType, "text/") {
		return false
	}
	for _, prefix := range []string{
		"image/", "audio/", "video/", "application/pdf", "application/zip",
		"application/gzip", "application/x-tar", "application/wasm",
		"application/octet-stream", "application/x-binary",
	} {
		if strings.HasPrefix(contentType, prefix) {
			return true
		}
	}
	for _, marker := range []string{"json", "xml", "javascript", "x-www-form-urlencoded", "graphql", "svg"} {
		if strings.Contains(contentType, marker) {
			return false
		}
	}
	return !utf8.Valid(data)
}

func charsetOf(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	if charset := strings.TrimSpace(params["charset"]); charset != "" {
		return strings.ToLower(charset)
	}
	if strings.HasPrefix(strings.ToLower(contentType), "text/") {
		return "utf-8"
	}
	return ""
}

func cookieInfos(cookies []*http.Cookie) []CookieInfo {
	if len(cookies) == 0 {
		return nil
	}
	out := make([]CookieInfo, 0, len(cookies))
	for _, cookie := range cookies {
		info := CookieInfo{
			Name:     cookie.Name,
			Domain:   cookie.Domain,
			Path:     cookie.Path,
			Secure:   cookie.Secure,
			HTTPOnly: cookie.HttpOnly,
		}
		if !cookie.Expires.IsZero() {
			info.Expires = cookie.Expires.UTC().Format(time.RFC3339)
		}
		out = append(out, info)
	}
	return out
}
