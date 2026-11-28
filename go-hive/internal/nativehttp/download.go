package nativehttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type downloadMeta struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Total        int64  `json:"total,omitempty"`
}

// Download streams a URL to a workspace file with atomic replace and resume.
func (c *Client) Download(ctx context.Context, req DownloadRequest) Result {
	return c.download(ctx, req, true)
}

func (c *Client) download(ctx context.Context, req DownloadRequest, allowRestart bool) Result {
	result := Result{Tool: ToolDownload}
	if !c.cfg.Enabled {
		return result.fail(ErrConfigDisabled, "http tools are disabled by configuration")
	}
	target, err := normalizeURL(req.URL, nil, false)
	if err != nil {
		return result.fail(ErrInvalidInput, err.Error())
	}
	result.URL = RedactURL(target.String(), c.cfg.RedactQueryKeys)
	result.Method = http.MethodGet
	if reason := c.policy.BlockReason(ctx, target.String()); reason != "" {
		return result.fail(ErrSSRFBlocked, reason)
	}
	dest, err := resolveUserPath(c.root, req.Path, true)
	if err != nil {
		kind, message := pathFailure(err, ErrWriteFailed)
		return result.fail(kind, message)
	}
	partPath := dest + ".part"
	metaPath := partPath + ".meta"
	if !req.Overwrite && !req.Resume {
		if _, statErr := os.Stat(dest); statErr == nil {
			return result.fail(ErrWriteFailed, fmt.Sprintf("destination %s already exists (set overwrite=true)", req.Path))
		}
	}

	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = c.cfg.MaxDownloadBytes
	}
	headers := cloneHeader(req.Headers)
	existingSize := int64(0)
	resumed := false
	var meta downloadMeta
	if req.Resume {
		if data, readErr := readFileUnder(c.root, metaPath); readErr == nil {
			_ = json.Unmarshal(data, &meta)
		}
		if info, statErr := os.Stat(partPath); statErr == nil && info.Size() > 0 {
			existingSize = info.Size()
			resumed = true
			headers.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
			switch {
			case meta.ETag != "":
				headers.Set("If-Range", meta.ETag)
			case meta.LastModified != "":
				headers.Set("If-Range", meta.LastModified)
			}
		}
	}

	timeout := c.timeout(req.Timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	opts := callOptions{
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
		jarMode:          "session",
	}
	response, state, tries, err := c.doWithRetry(ctx, http.MethodGet, req.Retry, func(ctx context.Context) (*http.Response, *callState, error) {
		state := newCallState()
		httpRequest, buildErr := c.buildHTTPRequest(ctx, http.MethodGet, target, nil, "", Request{
			Headers: headers,
			Auth:    req.Auth,
		})
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
	result.Retries = tries
	result.Status = response.StatusCode
	result.StatusText = http.StatusText(response.StatusCode)
	result.ContentType = response.Header.Get("Content-Type")
	result.ETag = response.Header.Get("ETag")
	result.LastModified = response.Header.Get("Last-Modified")
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if allowRestart && resumed {
			_ = os.Remove(partPath)
			_ = os.Remove(metaPath)
			req.Resume = false
			return c.download(ctx, req, false)
		}
		return result.fail(ErrReadFailed, "resume range not satisfiable")
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		preview, _, _ := readBounded(response.Body, 4096)
		result = result.fail(ErrHTTPStatus, fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(preview))))
		c.finishState(state, &result)
		return result
	}
	if response.StatusCode == http.StatusOK {
		existingSize = 0
		resumed = false
	}
	if response.ContentLength >= 0 && existingSize+response.ContentLength > maxBytes {
		return result.fail(ErrResponseTooLarge, fmt.Sprintf("download exceeds max_bytes (%d)", maxBytes))
	}

	flags := os.O_CREATE | os.O_WRONLY
	if response.StatusCode == http.StatusPartialContent && existingSize > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := openUnder(c.root, partPath, flags, 0o600)
	if err != nil {
		return result.fail(ErrWriteFailed, "open download: "+err.Error())
	}
	hasher := sha256.New()
	if existingSize > 0 {
		existing, openErr := openUnder(c.root, partPath, os.O_RDONLY, 0)
		if openErr != nil {
			_ = file.Close()
			return result.fail(ErrReadFailed, "resume read: "+openErr.Error())
		}
		if _, copyErr := io.Copy(hasher, existing); copyErr != nil {
			_ = existing.Close()
			_ = file.Close()
			return result.fail(ErrReadFailed, "resume hash: "+copyErr.Error())
		}
		_ = existing.Close()
	}
	remaining := maxBytes - existingSize
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, remaining+1))
	if copyErr != nil {
		_ = file.Close()
		return result.fail(ErrReadFailed, "download copy: "+copyErr.Error())
	}
	if written > remaining {
		_ = file.Close()
		_ = os.Remove(partPath)
		_ = os.Remove(metaPath)
		return result.fail(ErrResponseTooLarge, fmt.Sprintf("download exceeds max_bytes (%d)", maxBytes))
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return result.fail(ErrWriteFailed, "fsync download: "+err.Error())
	}
	if err := file.Close(); err != nil {
		return result.fail(ErrWriteFailed, "close download: "+err.Error())
	}
	if response.StatusCode == http.StatusOK {
		meta = downloadMeta{
			ETag:         result.ETag,
			LastModified: result.LastModified,
			Total:        existingSize + written,
		}
		if data, marshalErr := json.Marshal(meta); marshalErr == nil {
			_ = atomicWrite(metaPath, data)
		}
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if req.ExpectedSHA256 != "" && !strings.EqualFold(req.ExpectedSHA256, digest) {
		return result.fail(ErrChecksumMismatch, fmt.Sprintf("sha256 mismatch: got %s, want %s", digest, req.ExpectedSHA256))
	}
	if err := os.Rename(partPath, dest); err != nil {
		return result.fail(ErrWriteFailed, "rename download: "+err.Error())
	}
	_ = os.Remove(metaPath)
	result.OK = true
	result.Path = dest
	result.SavedTo = &dest
	result.Bytes = existingSize + written
	result.SHA256 = digest
	result.Resumed = resumed
	result.Charset = charsetOf(result.ContentType)
	c.finishState(state, &result)
	return result
}

func cloneHeader(source http.Header) http.Header {
	out := http.Header{}
	for name, values := range source {
		out[name] = append([]string(nil), values...)
	}
	return out
}
