package nativehttp

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
)

// Upload streams a multipart form or a raw workspace file to a URL.
func (c *Client) Upload(ctx context.Context, req UploadRequest) Result {
	result := Result{Tool: ToolUpload}
	if !c.cfg.Enabled {
		return result.fail(ErrConfigDisabled, "http tools are disabled by configuration")
	}
	target, err := normalizeURL(req.URL, nil, false)
	if err != nil {
		return result.fail(ErrInvalidInput, err.Error())
	}
	result.URL = RedactURL(target.String(), c.cfg.RedactQueryKeys)
	if reason := c.policy.BlockReason(ctx, target.String()); reason != "" {
		return result.fail(ErrSSRFBlocked, reason)
	}
	hasMultipart := len(req.Fields) > 0 || len(req.Files) > 0
	if req.BodyFile != "" && hasMultipart {
		return result.fail(ErrInvalidInput, "body_file is mutually exclusive with fields/files")
	}
	if req.BodyFile == "" && !hasMultipart {
		return result.fail(ErrInvalidInput, "upload requires body_file or fields/files")
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		if req.BodyFile != "" {
			method = http.MethodPut
		} else {
			method = http.MethodPost
		}
	}
	result.Method = method
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = c.cfg.MaxUploadBytes
	}
	maxResponseBytes := req.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = c.cfg.MaxResponseBytes
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
	retry := req.Retry
	if hasMultipart {
		// A multipart body is a one-shot stream; retrying would require
		// replaying every file. Raw body_file retries by reopening the file.
		retry.Count = 0
	}
	var uploaded int64
	var multipartUploaded atomic.Int64
	parts := len(req.Files) + countValues(req.Fields)
	response, state, tries, err := c.doWithRetry(ctx, method, retry, func(ctx context.Context) (*http.Response, *callState, error) {
		state := newCallState()
		var reader io.ReadCloser
		length := int64(-1)
		contentType := req.ContentType
		if req.BodyFile != "" {
			path, resolveErr := resolveUserPath(c.root, req.BodyFile, false)
			if resolveErr != nil {
				return nil, state, resolveErr
			}
			file, openErr := openUnder(c.root, path, os.O_RDONLY, 0)
			if openErr != nil {
				return nil, state, openErr
			}
			info, statErr := file.Stat()
			if statErr != nil {
				_ = file.Close()
				return nil, state, statErr
			}
			if info.Size() > maxBytes {
				_ = file.Close()
				return nil, state, fmt.Errorf("upload exceeds max_bytes (%d)", maxBytes)
			}
			reader = file
			length = info.Size()
			uploaded = info.Size()
			if contentType == "" {
				contentType = "application/octet-stream"
			}
		} else {
			pipeReader, pipeWriter := io.Pipe()
			writer := multipart.NewWriter(&maxWriter{w: pipeWriter, max: maxBytes, counter: &multipartUploaded})
			contentType = writer.FormDataContentType()
			go func() {
				writeErr := writeMultipart(writer, req, c.root)
				if writeErr != nil {
					_ = pipeWriter.CloseWithError(writeErr)
					return
				}
				_ = pipeWriter.Close()
			}()
			reader = pipeReader
		}
		httpRequest, buildErr := http.NewRequestWithContext(ctx, method, target.String(), reader)
		if buildErr != nil {
			_ = reader.Close()
			return nil, state, buildErr
		}
		httpRequest.ContentLength = length
		c.applyUploadHeaders(httpRequest, req, contentType)
		client, clientErr := c.newHTTPClient(opts, state)
		if clientErr != nil {
			_ = reader.Close()
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
	result = c.readResponse(response, state, ResponseOptions{
		Mode:           "auto",
		MaxBytes:       maxResponseBytes,
		MaxInlineBytes: c.cfg.MaxInlineBytes,
		IncludeHeaders: true,
		IncludeBody:    true,
	})
	result.Tool = ToolUpload
	result.URL = RedactURL(target.String(), c.cfg.RedactQueryKeys)
	result.Method = method
	if hasMultipart {
		uploaded = multipartUploaded.Load()
	}
	result.UploadedBytes = uploaded
	result.Parts = parts
	result.Retries = tries
	return result
}

func (c *Client) applyUploadHeaders(request *http.Request, req UploadRequest, contentType string) {
	for name, values := range req.Headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if request.Header.Get("User-Agent") == "" && c.cfg.UserAgent != "" {
		request.Header.Set("User-Agent", c.cfg.UserAgent)
	}
	if contentType != "" && request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", contentType)
	}
	if req.Auth != nil && request.Header.Get("Authorization") == "" {
		switch strings.ToLower(strings.TrimSpace(req.Auth.Type)) {
		case "basic":
			request.SetBasicAuth(req.Auth.Username, req.Auth.Password)
		case "bearer":
			request.Header.Set("Authorization", "Bearer "+req.Auth.Token)
		}
	}
}

func writeMultipart(writer *multipart.Writer, req UploadRequest, root string) error {
	for key, values := range req.Fields {
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				return err
			}
		}
	}
	for _, filePart := range req.Files {
		if err := writeMultipartFile(writer, filePart, root); err != nil {
			return err
		}
	}
	return writer.Close()
}

func writeMultipartFile(writer *multipart.Writer, filePart UploadFile, root string) error {
	path, err := resolveUserPath(root, filePart.Path, false)
	if err != nil {
		return err
	}
	file, err := openUnder(root, path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	filename := filePart.Filename
	if filename == "" {
		filename = filePart.Path
	}
	contentType := filePart.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, filePart.Field, sanitizeFilename(filename)))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)
	return err
}

type maxWriter struct {
	w       io.Writer
	written int64
	max     int64
	counter *atomic.Int64
}

func (m *maxWriter) Write(data []byte) (int, error) {
	if m.max > 0 && m.written+int64(len(data)) > m.max {
		return 0, fmt.Errorf("upload exceeds max_bytes (%d)", m.max)
	}
	written, err := m.w.Write(data)
	m.written += int64(written)
	if m.counter != nil {
		m.counter.Store(m.written)
	}
	return written, err
}

func countValues(values url.Values) int {
	count := 0
	for _, items := range values {
		count += len(items)
	}
	return count
}
