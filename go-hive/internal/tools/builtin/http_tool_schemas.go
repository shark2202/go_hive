package builtin

import "github.com/shark2202/go_hive/go-hive/internal/llm"

// Native HTTP tool names. The family is Go-only and post-parity: the tools
// join the lazy tier, never the frozen 39-name default surface.
const (
	ToolHTTPRequest  = "http_request"
	ToolHTTPDownload = "http_download"
	ToolHTTPUpload   = "http_upload"
)

const (
	HTTPRequestDescription = "Send an HTTP request and return a bounded, structured response. " +
		"Supports GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS, multi-value headers and query, " +
		"basic/bearer auth, and exactly one body source (raw, JSON, form, inline multipart). " +
		"Redirects are re-validated; a response larger than response.max_inline_bytes is saved " +
		"to a workspace file and returned via saved_to. Cookie state is per Hive chat session."
	HTTPDownloadDescription = "Download a URL to a workspace file. Writes <path>.part, fsyncs and renames " +
		"atomically; supports resume, overwrite policy, max_bytes and expected_sha256."
	HTTPUploadDescription = "Upload multipart fields/files or a raw workspace file. Streams from disk " +
		"(no whole-file buffering) and returns the bounded response envelope."
)

// HTTPToolDefs returns the three model-visible definitions in a stable order.
func HTTPToolDefs() []llm.ToolDef {
	return []llm.ToolDef{
		{Name: ToolHTTPRequest, Description: HTTPRequestDescription, Parameters: httpRequestSchema(), ConcurrencySafe: false},
		{Name: ToolHTTPDownload, Description: HTTPDownloadDescription, Parameters: httpDownloadSchema(), ConcurrencySafe: false},
		{Name: ToolHTTPUpload, Description: HTTPUploadDescription, Parameters: httpUploadSchema(), ConcurrencySafe: false},
	}
}

// HTTPToolDef returns one definition by name.
func HTTPToolDef(name string) (llm.ToolDef, bool) {
	for _, def := range HTTPToolDefs() {
		if def.Name == name {
			return def, true
		}
	}
	return llm.ToolDef{}, false
}

const (
	httpProxyDescription          = "Explicit proxy URL. Default: HTTP_PROXY / HTTPS_PROXY / NO_PROXY. Proxy userinfo is redacted from results and logs."
	httpNoProxyDescription        = "Disable the environment proxy for this call (curl --noproxy '*')."
	httpVersionDescription        = "auto negotiates h2 over TLS; 1.1 forces HTTP/1.1; 2 forces HTTP/2 over TLS (h2c is not supported without a new dependency)."
	httpCompressedDescription     = "Send Accept-Encoding: gzip and decode gzip responses."
	httpAllowDowngradeDescription = "Allow a redirect that downgrades https to http (refused by default)."
)

func httpStringOrStringsSchema() map[string]any {
	return map[string]any{
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func httpHeadersSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "Request headers. Array values repeat the header; a null value removes a " +
			"default header. Header names are case-insensitive.",
		"additionalProperties": map[string]any{
			"anyOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				map[string]any{"type": "null"},
			},
		},
	}
}

func httpAuthSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "Authorization helper. type=basic uses username/password; type=bearer " +
			"uses token. An explicit Authorization header wins.",
		"properties": map[string]any{
			"type":     map[string]any{"type": "string", "enum": []string{"basic", "bearer"}},
			"username": map[string]any{"type": "string"},
			"password": map[string]any{"type": "string"},
			"token":    map[string]any{"type": "string"},
		},
		"required": []string{"type"},
	}
}

func httpTLSSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "TLS overrides. Path fields are absolute and must resolve inside the session workspace root.",
		"properties": map[string]any{
			"ca_file":          map[string]any{"type": "string"},
			"client_cert_file": map[string]any{"type": "string"},
			"client_key_file":  map[string]any{"type": "string"},
			"server_name":      map[string]any{"type": "string"},
		},
	}
}

func httpRetrySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "Retry policy. Idempotent methods retry on 408/425/429/500/502/503/504; " +
			"other methods need retry_non_idempotent. Retry-After is honored up to 30s.",
		"properties": map[string]any{
			"count":                map[string]any{"type": "integer", "minimum": 0, "maximum": 10, "default": 0},
			"delay_seconds":        map[string]any{"type": "number", "minimum": 0, "default": 0.5},
			"max_delay_seconds":    map[string]any{"type": "number", "minimum": 0, "maximum": 30, "default": 8},
			"retry_non_idempotent": map[string]any{"type": "boolean", "default": false},
		},
	}
}

func httpCookiesSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "Cookie handling for THIS Hive chat session. The jar is owned by the session's " +
			"tool catalogue and is never shared with another session or colony worker. jar=session " +
			"(default) keeps cookies across calls in this session; jar=none sends only the explicit " +
			"values. Nothing is written to disk in v1.",
		"properties": map[string]any{
			"jar": map[string]any{
				"type":        "string",
				"enum":        []string{"session", "none"},
				"default":     "session",
				"description": "session: use this session's in-memory jar. none: stateless.",
			},
			"send": map[string]any{
				"type":                 "object",
				"description":          "Explicit Cookie values, merged with the jar (redacted in logs).",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"clear": map[string]any{
				"type":        "boolean",
				"default":     false,
				"description": "Clear this session's jar before sending (logout).",
			},
		},
	}
}

func httpRequestSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http(s) URL. A bare host is upgraded to https://.",
			},
			"method": map[string]any{
				"type":        "string",
				"enum":        []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
				"default":     "GET",
				"description": "HTTP method; upper-cased.",
			},
			"query": map[string]any{
				"type": "object",
				"description": "Query parameters appended to url. Array values repeat the key. " +
					"replace_query=true drops the URL's existing query string.",
				"additionalProperties": httpStringOrStringsSchema(),
			},
			"replace_query": map[string]any{"type": "boolean", "default": false},
			"headers":       httpHeadersSchema(),
			"body": map[string]any{
				"type":        "string",
				"description": "Raw request body. Exactly one of body, json, form, multipart may be set.",
			},
			"json": map[string]any{
				"description": "JSON body (any JSON value). Compact-encoded and sent as " +
					"application/json unless Content-Type is set explicitly.",
			},
			"form": map[string]any{
				"type":                 "object",
				"description":          "application/x-www-form-urlencoded body; array values repeat the key.",
				"additionalProperties": httpStringOrStringsSchema(),
			},
			"multipart": map[string]any{
				"type": "object",
				"description": "multipart/form-data with inline string content. Use http_upload for " +
					"workspace file parts; this mode never opens files.",
				"properties": map[string]any{
					"fields": map[string]any{
						"type":                 "object",
						"additionalProperties": httpStringOrStringsSchema(),
					},
					"files": map[string]any{
						"type": "object",
						"additionalProperties": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content":      map[string]any{"type": "string"},
								"filename":     map[string]any{"type": "string"},
								"content_type": map[string]any{"type": "string"},
							},
							"required": []string{"content"},
						},
					},
				},
			},
			"auth":                    httpAuthSchema(),
			"follow_redirects":        map[string]any{"type": "boolean", "default": true},
			"max_redirects":           map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "default": 10},
			"allow_https_to_http":     map[string]any{"type": "boolean", "default": false, "description": "Allow a redirect that downgrades https to http (refused by default)."},
			"insecure":                map[string]any{"type": "boolean", "default": false, "description": "Skip TLS certificate verification (curl -k)."},
			"tls":                     httpTLSSchema(),
			"proxy":                   map[string]any{"type": []string{"string", "null"}, "description": "Explicit proxy URL. Default: HTTP_PROXY / HTTPS_PROXY / NO_PROXY. Proxy userinfo is redacted from results and logs."},
			"no_proxy":                map[string]any{"type": "boolean", "default": false, "description": "Disable the environment proxy for this call (curl --noproxy '*')."},
			"http_version":            map[string]any{"type": "string", "enum": []string{"auto", "1.1", "2"}, "default": "auto", "description": "auto negotiates h2 over TLS; 1.1 forces HTTP/1.1; 2 forces HTTP/2 over TLS (h2c is not supported without a new dependency)."},
			"compressed":              map[string]any{"type": "boolean", "default": true, "description": "Send Accept-Encoding: gzip and decode gzip responses."},
			"timeout_seconds":         map[string]any{"type": "integer", "minimum": 1, "maximum": 300, "default": 30, "description": "Total budget for the call including retries, in seconds."},
			"connect_timeout_seconds": httpConnectTimeoutSchema(),
			"retry":                   httpRetrySchema(),
			"response":                httpResponseSchema(),
			"cookies":                 httpCookiesSchema(),
			"fail_on_http_error":      map[string]any{"type": "boolean", "default": false, "description": "Report non-2xx as IsError (curl --fail-with-body) instead of a normal result."},
		},
		"required": []string{"url"},
	}
}

func httpDownloadSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"url":                     map[string]any{"type": "string", "description": "Absolute http(s) URL to download."},
			"path":                    map[string]any{"type": "string", "description": "Absolute destination file inside the session workspace root. Parent directories are created."},
			"overwrite":               map[string]any{"type": "boolean", "default": false},
			"resume":                  map[string]any{"type": "boolean", "default": false, "description": "Resume a .part file with Range + If-Range (curl -C -)."},
			"expected_sha256":         map[string]any{"type": "string", "description": "Fail with checksum_mismatch when the downloaded digest differs."},
			"headers":                 httpHeadersSchema(),
			"auth":                    httpAuthSchema(),
			"follow_redirects":        map[string]any{"type": "boolean", "default": true},
			"max_redirects":           map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "default": 10},
			"allow_https_to_http":     map[string]any{"type": "boolean", "default": false, "description": httpAllowDowngradeDescription},
			"insecure":                map[string]any{"type": "boolean", "default": false},
			"tls":                     httpTLSSchema(),
			"proxy":                   map[string]any{"type": []string{"string", "null"}, "description": httpProxyDescription},
			"no_proxy":                map[string]any{"type": "boolean", "default": false, "description": httpNoProxyDescription},
			"http_version":            map[string]any{"type": "string", "enum": []string{"auto", "1.1", "2"}, "default": "auto", "description": httpVersionDescription},
			"compressed":              map[string]any{"type": "boolean", "default": true, "description": httpCompressedDescription},
			"timeout_seconds":         map[string]any{"type": "integer", "minimum": 1, "maximum": 1800, "default": 300, "description": "Total download budget in seconds (cap 1800)."},
			"connect_timeout_seconds": httpConnectTimeoutSchema(),
			"max_bytes":               map[string]any{"type": "integer", "minimum": 1, "default": 1073741824, "description": "Download cap (default 1 GiB); exceeded means response_too_large."},
			"retry":                   httpRetrySchema(),
		},
		"required": []string{"url", "path"},
	}
}

func httpUploadSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "Absolute http(s) URL."},
			"method": map[string]any{
				"type":        "string",
				"enum":        []string{"POST", "PUT", "PATCH"},
				"default":     "POST",
				"description": "Multipart default POST; body_file default PUT (curl -T).",
			},
			"fields": map[string]any{
				"type":                 "object",
				"description":          "multipart/form-data text fields; array values repeat the key.",
				"additionalProperties": httpStringOrStringsSchema(),
			},
			"files": map[string]any{
				"type":        "array",
				"description": "Multipart file parts streamed from absolute workspace file paths.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"field":        map[string]any{"type": "string"},
						"path":         map[string]any{"type": "string", "description": "Absolute path to a workspace file."},
						"filename":     map[string]any{"type": "string"},
						"content_type": map[string]any{"type": "string"},
					},
					"required": []string{"field", "path"},
				},
			},
			"body_file":               map[string]any{"type": "string", "description": "Absolute path to a workspace file, streamed as the raw body (curl -T). Mutually exclusive with fields/files."},
			"content_type":            map[string]any{"type": "string", "description": "Raw-body Content-Type (body_file mode); defaults to application/octet-stream."},
			"headers":                 httpHeadersSchema(),
			"auth":                    httpAuthSchema(),
			"follow_redirects":        map[string]any{"type": "boolean", "default": true},
			"max_redirects":           map[string]any{"type": "integer", "minimum": 0, "maximum": 20, "default": 10},
			"allow_https_to_http":     map[string]any{"type": "boolean", "default": false, "description": httpAllowDowngradeDescription},
			"insecure":                map[string]any{"type": "boolean", "default": false},
			"tls":                     httpTLSSchema(),
			"proxy":                   map[string]any{"type": []string{"string", "null"}, "description": httpProxyDescription},
			"no_proxy":                map[string]any{"type": "boolean", "default": false, "description": httpNoProxyDescription},
			"http_version":            map[string]any{"type": "string", "enum": []string{"auto", "1.1", "2"}, "default": "auto", "description": httpVersionDescription},
			"compressed":              map[string]any{"type": "boolean", "default": true, "description": httpCompressedDescription},
			"timeout_seconds":         map[string]any{"type": "integer", "minimum": 1, "maximum": 1800, "default": 300},
			"connect_timeout_seconds": httpConnectTimeoutSchema(),
			"max_bytes":               map[string]any{"type": "integer", "minimum": 1, "default": 1073741824, "description": "Upload cap (default 1 GiB); exceeded means request_too_large."},
			"max_response_bytes":      map[string]any{"type": "integer", "minimum": 1, "default": 16777216, "description": "Response body cap for the upload reply."},
			"retry":                   httpRetrySchema(),
		},
		"required": []string{"url"},
	}
}

func httpConnectTimeoutSchema() map[string]any {
	return map[string]any{
		"type":        "integer",
		"minimum":     1,
		"maximum":     60,
		"default":     10,
		"description": "TCP + TLS connect budget per attempt, in seconds.",
	}
}

func httpResponseSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Response handling: inline vs file, body cap, header/cookie visibility.",
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "inline", "file"},
				"default":     "auto",
				"description": "auto keeps bodies <= max_inline_bytes inline and saves the rest.",
			},
			"path":             map[string]any{"type": "string", "description": "Absolute destination for mode=file (or auto overflow); must be inside the session workspace root."},
			"max_inline_bytes": map[string]any{"type": "integer", "minimum": 0, "maximum": 10485760, "default": 65536},
			"max_bytes":        map[string]any{"type": "integer", "minimum": 1, "default": 16777216, "description": "Hard fetch cap; exceeded means response_too_large."},
			"include_headers":  map[string]any{"type": "boolean", "default": true},
			"include_body":     map[string]any{"type": "boolean", "default": true},
			"include_cookies":  map[string]any{"type": "boolean", "default": false, "description": "Return cookie metadata and unredacted Set-Cookie values."},
		},
	}
}
