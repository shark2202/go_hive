package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/nativehttp"
)

// HTTPToolbox executes the native HTTP tool family for one Hive chat session.
// The client (and its cookie jar) is per catalogue; WithRoot creates a fresh
// client so a colony worker never inherits the queen's cookies.
type HTTPToolbox struct {
	root   string
	home   string
	cfg    nativehttp.Config
	client *nativehttp.Client
}

// HTTPEnabled reports whether the family is enabled by <HIVE_HOME>/http_tools.json.
func HTTPEnabled(home string) bool {
	return nativehttp.LoadConfig(home).Enabled
}

// NewHTTPToolbox builds a toolbox rooted at the session sandbox.
func NewHTTPToolbox(root, home string) *HTTPToolbox {
	cfg := nativehttp.LoadConfig(home)
	return &HTTPToolbox{
		root:   root,
		home:   home,
		cfg:    cfg,
		client: nativehttp.New(cfg, root, home),
	}
}

// WithRoot re-roots the toolbox for a colony worker: path containment and the
// cookie jar follow the worker's storage directory, and the jar starts empty.
func (t *HTTPToolbox) WithRoot(root string) *HTTPToolbox {
	if t == nil {
		return nil
	}
	return &HTTPToolbox{
		root:   root,
		home:   t.home,
		cfg:    t.cfg,
		client: nativehttp.New(t.cfg, root, t.home),
	}
}

// Close releases idle connections.
func (t *HTTPToolbox) Close() error {
	if t == nil || t.client == nil {
		return nil
	}
	return t.client.Close()
}

// Defs returns the model-visible definitions.
func (t *HTTPToolbox) Defs() []llm.ToolDef { return HTTPToolDefs() }

// Execute runs one HTTP tool call.
func (t *HTTPToolbox) Execute(ctx context.Context, name string, input map[string]any) (string, bool, error) {
	if t == nil || !t.cfg.Enabled {
		return "Error: http tools are disabled by configuration", true, nil
	}
	if input == nil {
		input = map[string]any{}
	}
	switch name {
	case ToolHTTPRequest:
		request, err := parseHTTPRequest(input, t.cfg)
		if err != nil {
			return "Error: " + err.Error(), true, nil
		}
		return renderHTTPResult(t.client.Do(ctx, request))
	case ToolHTTPDownload:
		request, err := parseHTTPDownload(input, t.cfg)
		if err != nil {
			return "Error: " + err.Error(), true, nil
		}
		return renderHTTPResult(t.client.Download(ctx, request))
	case ToolHTTPUpload:
		request, err := parseHTTPUpload(input, t.cfg)
		if err != nil {
			return "Error: " + err.Error(), true, nil
		}
		return renderHTTPResult(t.client.Upload(ctx, request))
	default:
		return "", false, nil
	}
}

func renderHTTPResult(result nativehttp.Result) (string, bool, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return "", true, err
	}
	return string(data), result.Error != "", nil
}

type httpCommon struct {
	headers             http.Header
	auth                *nativehttp.Auth
	followRedirects     bool
	maxRedirects        int
	allowHTTPSDowngrade bool
	insecure            bool
	tls                 nativehttp.TLSOptions
	proxy               string
	noProxy             bool
	httpVersion         string
	compressed          bool
	timeout             time.Duration
	connectTimeout      time.Duration
	retry               nativehttp.RetryPolicy
}

func parseHTTPCommon(input map[string]any, cfg nativehttp.Config) (httpCommon, error) {
	common := httpCommon{
		followRedirects:     boolArgOr(input, "follow_redirects", cfg.FollowRedirects),
		maxRedirects:        intArgOr(input, "max_redirects", cfg.MaxRedirects),
		allowHTTPSDowngrade: boolArgOr(input, "allow_https_to_http", cfg.AllowHTTPSToHTTP),
		insecure:            boolArgOr(input, "insecure", false),
		noProxy:             boolArgOr(input, "no_proxy", false),
		httpVersion:         "auto",
		compressed:          boolArgOr(input, "compressed", true),
		timeout:             clampDuration(durationArg(input, "timeout_seconds", cfg.DefaultTimeout), cfg.MaxTimeout),
		connectTimeout:      durationArg(input, "connect_timeout_seconds", cfg.ConnectTimeout),
		retry:               nativehttp.RetryPolicy{Delay: 500 * time.Millisecond, MaxDelay: 8 * time.Second},
	}
	if raw, present := input["headers"]; present && raw != nil {
		headers, err := parseHTTPHeaders(raw)
		if err != nil {
			return common, err
		}
		common.headers = headers
	}
	if raw, present := input["auth"]; present && raw != nil {
		auth, err := parseHTTPAuth(raw)
		if err != nil {
			return common, err
		}
		common.auth = auth
	}
	if raw, present := input["tls"]; present && raw != nil {
		tlsOptions, err := parseHTTPTLS(raw)
		if err != nil {
			return common, err
		}
		common.tls = tlsOptions
	}
	if raw, present := input["proxy"]; present && raw != nil {
		proxy, ok := raw.(string)
		if !ok {
			return common, errors.New("proxy must be a string")
		}
		common.proxy = proxy
	}
	if version, ok := stringArg(input, "http_version"); ok && version != "" {
		common.httpVersion = version
	}
	if raw, present := input["retry"]; present && raw != nil {
		retry, err := parseHTTPRetry(raw)
		if err != nil {
			return common, err
		}
		common.retry = retry
	}
	return common, nil
}

func parseHTTPRequest(input map[string]any, cfg nativehttp.Config) (nativehttp.Request, error) {
	request := nativehttp.Request{}
	rawURL, ok := stringArg(input, "url")
	if !ok || strings.TrimSpace(rawURL) == "" {
		return request, errors.New("url is required")
	}
	request.URL = rawURL
	request.Method = "GET"
	if method, ok := stringArg(input, "method"); ok && method != "" {
		request.Method = method
	}
	if raw, present := input["query"]; present && raw != nil {
		values, err := parseURLValues(raw)
		if err != nil {
			return request, fmt.Errorf("query: %w", err)
		}
		request.Query = values
	}
	request.ReplaceQuery = boolArgOr(input, "replace_query", false)

	sources := 0
	for _, key := range []string{"body", "json", "form", "multipart"} {
		if _, present := input[key]; present {
			sources++
		}
	}
	if sources > 1 {
		return request, errors.New("body, json, form and multipart are mutually exclusive")
	}
	if raw, present := input["body"]; present {
		text, ok := raw.(string)
		if !ok {
			return request, errors.New("body must be a string")
		}
		request.Body = []byte(text)
	}
	if raw, present := input["json"]; present {
		if raw == nil {
			request.Body = []byte("null")
			if request.Headers == nil {
				request.Headers = http.Header{}
			}
			request.Headers.Set("Content-Type", "application/json")
		} else {
			request.JSON = raw
		}
	}
	if raw, present := input["form"]; present {
		values, err := parseURLValues(raw)
		if err != nil {
			return request, fmt.Errorf("form: %w", err)
		}
		request.Form = values
	}
	if raw, present := input["multipart"]; present {
		multipart, err := parseHTTPMultipart(raw)
		if err != nil {
			return request, err
		}
		request.Multipart = multipart
	}

	common, err := parseHTTPCommon(input, cfg)
	if err != nil {
		return request, err
	}
	if common.headers != nil {
		request.Headers = common.headers
	}
	request.Auth = common.auth
	request.FollowRedirects = common.followRedirects
	request.MaxRedirects = common.maxRedirects
	request.AllowHTTPSToHTTP = common.allowHTTPSDowngrade
	request.Insecure = common.insecure
	request.TLS = common.tls
	request.Proxy = common.proxy
	request.NoProxy = common.noProxy
	request.HTTPVersion = common.httpVersion
	request.Compressed = common.compressed
	request.Timeout = common.timeout
	request.ConnectTimeout = common.connectTimeout
	request.Retry = common.retry
	request.Response = parseHTTPResponse(input["response"], cfg)
	request.Cookies = parseHTTPCookies(input["cookies"])
	request.FailOnHTTPError = boolArgOr(input, "fail_on_http_error", false)
	return request, nil
}

func parseHTTPDownload(input map[string]any, cfg nativehttp.Config) (nativehttp.DownloadRequest, error) {
	request := nativehttp.DownloadRequest{}
	rawURL, ok := stringArg(input, "url")
	if !ok || strings.TrimSpace(rawURL) == "" {
		return request, errors.New("url is required")
	}
	path, ok := stringArg(input, "path")
	if !ok || strings.TrimSpace(path) == "" {
		return request, errors.New("path is required")
	}
	request.URL = rawURL
	request.Path = path
	request.Overwrite = boolArgOr(input, "overwrite", false)
	request.Resume = boolArgOr(input, "resume", false)
	request.ExpectedSHA256, _ = stringArg(input, "expected_sha256")
	request.MaxBytes = int64Arg(input, "max_bytes", cfg.MaxDownloadBytes)

	common, err := parseHTTPCommon(input, cfg)
	if err != nil {
		return request, err
	}
	request.Headers = common.headers
	request.Auth = common.auth
	request.FollowRedirects = common.followRedirects
	request.MaxRedirects = common.maxRedirects
	request.AllowHTTPSToHTTP = common.allowHTTPSDowngrade
	request.Insecure = common.insecure
	request.TLS = common.tls
	request.Proxy = common.proxy
	request.NoProxy = common.noProxy
	request.HTTPVersion = common.httpVersion
	request.Compressed = common.compressed
	request.Timeout = common.timeout
	request.ConnectTimeout = common.connectTimeout
	request.Retry = common.retry
	return request, nil
}

func parseHTTPUpload(input map[string]any, cfg nativehttp.Config) (nativehttp.UploadRequest, error) {
	request := nativehttp.UploadRequest{}
	rawURL, ok := stringArg(input, "url")
	if !ok || strings.TrimSpace(rawURL) == "" {
		return request, errors.New("url is required")
	}
	request.URL = rawURL
	request.Method, _ = stringArg(input, "method")
	if raw, present := input["fields"]; present && raw != nil {
		values, err := parseURLValues(raw)
		if err != nil {
			return request, fmt.Errorf("fields: %w", err)
		}
		request.Fields = values
	}
	if raw, present := input["files"]; present && raw != nil {
		files, err := parseUploadFiles(raw)
		if err != nil {
			return request, err
		}
		request.Files = files
	}
	request.BodyFile, _ = stringArg(input, "body_file")
	request.ContentType, _ = stringArg(input, "content_type")
	request.MaxBytes = int64Arg(input, "max_bytes", cfg.MaxUploadBytes)
	request.MaxResponseBytes = int64Arg(input, "max_response_bytes", cfg.MaxResponseBytes)

	common, err := parseHTTPCommon(input, cfg)
	if err != nil {
		return request, err
	}
	request.Headers = common.headers
	request.Auth = common.auth
	request.FollowRedirects = common.followRedirects
	request.MaxRedirects = common.maxRedirects
	request.AllowHTTPSToHTTP = common.allowHTTPSDowngrade
	request.Insecure = common.insecure
	request.TLS = common.tls
	request.Proxy = common.proxy
	request.NoProxy = common.noProxy
	request.HTTPVersion = common.httpVersion
	request.Compressed = common.compressed
	request.Timeout = common.timeout
	request.ConnectTimeout = common.connectTimeout
	request.Retry = common.retry
	return request, nil
}

func parseHTTPHeaders(raw any) (http.Header, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("headers must be an object")
	}
	headers := http.Header{}
	for key, value := range object {
		if value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			headers.Add(key, typed)
		case []any:
			for _, item := range typed {
				text, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("header %q values must be strings", key)
				}
				headers.Add(key, text)
			}
		case []string:
			for _, text := range typed {
				headers.Add(key, text)
			}
		default:
			return nil, fmt.Errorf("header %q must be a string or array of strings", key)
		}
	}
	return headers, nil
}

func parseURLValues(raw any) (url.Values, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("expected an object of strings or string arrays")
	}
	values := url.Values{}
	for key, value := range object {
		switch typed := value.(type) {
		case string:
			values.Add(key, typed)
		case []any:
			for _, item := range typed {
				text, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("%q values must be strings", key)
				}
				values.Add(key, text)
			}
		case []string:
			for _, text := range typed {
				values.Add(key, text)
			}
		default:
			return nil, fmt.Errorf("%q must be a string or array of strings", key)
		}
	}
	return values, nil
}

func parseHTTPAuth(raw any) (*nativehttp.Auth, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("auth must be an object")
	}
	auth := &nativehttp.Auth{}
	auth.Type, _ = stringArg(object, "type")
	auth.Username, _ = stringArg(object, "username")
	auth.Password, _ = stringArg(object, "password")
	auth.Token, _ = stringArg(object, "token")
	if auth.Type == "" {
		return nil, errors.New("auth.type is required")
	}
	return auth, nil
}

func parseHTTPTLS(raw any) (nativehttp.TLSOptions, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nativehttp.TLSOptions{}, errors.New("tls must be an object")
	}
	options := nativehttp.TLSOptions{}
	options.CAFile, _ = stringArg(object, "ca_file")
	options.ClientCertFile, _ = stringArg(object, "client_cert_file")
	options.ClientKeyFile, _ = stringArg(object, "client_key_file")
	options.ServerName, _ = stringArg(object, "server_name")
	return options, nil
}

func parseHTTPRetry(raw any) (nativehttp.RetryPolicy, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nativehttp.RetryPolicy{}, errors.New("retry must be an object")
	}
	retry := nativehttp.RetryPolicy{Delay: 500 * time.Millisecond, MaxDelay: 8 * time.Second}
	retry.Count = intArgOr(object, "count", 0)
	if seconds, ok := numberArg(object, "delay_seconds"); ok && seconds >= 0 {
		retry.Delay = time.Duration(seconds * float64(time.Second))
	}
	if seconds, ok := numberArg(object, "max_delay_seconds"); ok && seconds >= 0 {
		retry.MaxDelay = time.Duration(seconds * float64(time.Second))
	}
	retry.RetryNonIdempotent = boolArgOr(object, "retry_non_idempotent", false)
	return retry, nil
}

func parseHTTPResponse(raw any, cfg nativehttp.Config) nativehttp.ResponseOptions {
	options := nativehttp.ResponseOptions{
		Mode:           "auto",
		MaxInlineBytes: cfg.MaxInlineBytes,
		MaxBytes:       cfg.MaxResponseBytes,
		IncludeHeaders: true,
		IncludeBody:    true,
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return options
	}
	if mode, ok := stringArg(object, "mode"); ok && mode != "" {
		options.Mode = mode
	}
	options.Path, _ = stringArg(object, "path")
	options.MaxInlineBytes = int64Arg(object, "max_inline_bytes", options.MaxInlineBytes)
	options.MaxBytes = int64Arg(object, "max_bytes", options.MaxBytes)
	options.IncludeHeaders = boolArgOr(object, "include_headers", true)
	options.IncludeBody = boolArgOr(object, "include_body", true)
	options.IncludeCookies = boolArgOr(object, "include_cookies", false)
	return options
}

func parseHTTPCookies(raw any) nativehttp.CookieOptions {
	options := nativehttp.CookieOptions{Jar: "session"}
	object, ok := raw.(map[string]any)
	if !ok {
		return options
	}
	if jar, ok := stringArg(object, "jar"); ok && jar != "" {
		options.Jar = jar
	}
	if rawSend, present := object["send"]; present && rawSend != nil {
		if send, ok := rawSend.(map[string]any); ok {
			options.Send = map[string]string{}
			for key, value := range send {
				if text, ok := value.(string); ok {
					options.Send[key] = text
				}
			}
		}
	}
	options.Clear = boolArgOr(object, "clear", false)
	return options
}

func parseHTTPMultipart(raw any) (*nativehttp.Multipart, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("multipart must be an object")
	}
	multipart := &nativehttp.Multipart{
		Fields: url.Values{},
		Files:  map[string]nativehttp.MultipartFile{},
	}
	if rawFields, present := object["fields"]; present && rawFields != nil {
		fields, err := parseURLValues(rawFields)
		if err != nil {
			return nil, fmt.Errorf("multipart.fields: %w", err)
		}
		multipart.Fields = fields
	}
	if rawFiles, present := object["files"]; present && rawFiles != nil {
		files, ok := rawFiles.(map[string]any)
		if !ok {
			return nil, errors.New("multipart.files must be an object")
		}
		for field, rawFile := range files {
			fileObject, ok := rawFile.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("multipart.files[%q] must be an object", field)
			}
			content, _ := stringArg(fileObject, "content")
			filename, _ := stringArg(fileObject, "filename")
			contentType, _ := stringArg(fileObject, "content_type")
			multipart.Files[field] = nativehttp.MultipartFile{
				Content:     content,
				Filename:    filename,
				ContentType: contentType,
			}
		}
	}
	return multipart, nil
}

func parseUploadFiles(raw any) ([]nativehttp.UploadFile, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("files must be an array")
	}
	files := make([]nativehttp.UploadFile, 0, len(items))
	for index, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("files[%d] must be an object", index)
		}
		field, _ := stringArg(object, "field")
		path, _ := stringArg(object, "path")
		if field == "" || path == "" {
			return nil, fmt.Errorf("files[%d].field and files[%d].path are required", index, index)
		}
		filename, _ := stringArg(object, "filename")
		contentType, _ := stringArg(object, "content_type")
		files = append(files, nativehttp.UploadFile{
			Field:       field,
			Path:        path,
			Filename:    filename,
			ContentType: contentType,
		})
	}
	return files, nil
}

func intArgOr(input map[string]any, key string, fallback int) int {
	if value, ok := intArg(input, key); ok {
		return value
	}
	return fallback
}

func int64Arg(input map[string]any, key string, fallback int64) int64 {
	if value, ok := finiteNumberArg(input, key); ok && value >= 0 {
		return int64(value)
	}
	return fallback
}

func durationArg(input map[string]any, key string, fallback time.Duration) time.Duration {
	if value, ok := intArg(input, key); ok && value > 0 {
		return time.Duration(value) * time.Second
	}
	return fallback
}

func clampDuration(value, max time.Duration) time.Duration {
	if value > max {
		return max
	}
	return value
}
