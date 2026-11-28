// Package nativehttp is the stdlib-only core of the native HTTP tool family:
// http_request, http_download and http_upload.
//
// It owns request building, the outbound policy (internal/netguard),
// transport, redirect/retry, bounded response spooling, redaction and result
// rendering. It deliberately does not import internal/server, internal/cli or
// internal/tools/builtin, so it is exercisable without a session or tool
// registry.
//
// Cookie state is per Client (one Client per Hive chat session catalogue) and
// in-memory only. There is no package-level or on-disk jar. See
// go_hive/docs/http-tools-design.json and go_hive/docs/config-roots.html.
package nativehttp

import (
	"net/http"
	"net/url"
	"time"
)

// Tool names, frozen by the design's tool contract.
const (
	ToolRequest  = "http_request"
	ToolDownload = "http_download"
	ToolUpload   = "http_upload"
)

// Error kinds. The set is closed; callers must not invent new values.
const (
	ErrInvalidInput     = "invalid_input"
	ErrPathNotAbsolute  = "path_not_absolute"
	ErrPathEscape       = "path_escape"
	ErrConfigDisabled   = "config_disabled"
	ErrSSRFBlocked      = "ssrf_blocked"
	ErrDNSFailed        = "dns_failed"
	ErrConnectFailed    = "connect_failed"
	ErrTLSFailed        = "tls_failed"
	ErrProxyFailed      = "proxy_failed"
	ErrTimeout          = "timeout"
	ErrTooManyRedirects = "too_many_redirects"
	ErrHTTPSDowngrade   = "https_downgrade_blocked"
	ErrResponseTooLarge = "response_too_large"
	ErrRequestTooLarge  = "request_too_large"
	ErrWriteFailed      = "write_failed"
	ErrReadFailed       = "read_failed"
	ErrChecksumMismatch = "checksum_mismatch"
	ErrCookieJarInvalid = "cookie_jar_invalid"
	ErrHTTPStatus       = "http_status"
)

// Timing is the per-phase breakdown in milliseconds.
type Timing struct {
	TotalMS     int64 `json:"total"`
	DNSMS       int64 `json:"dns"`
	ConnectMS   int64 `json:"connect"`
	TLSMS       int64 `json:"tls"`
	FirstByteMS int64 `json:"first_byte"`
}

// TLSInfo is the negotiated connection's certificate summary.
type TLSInfo struct {
	Version     string   `json:"version,omitempty"`
	CipherSuite string   `json:"cipher_suite,omitempty"`
	ServerName  string   `json:"server_name,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	Issuer      string   `json:"issuer,omitempty"`
	NotAfter    string   `json:"not_after,omitempty"`
	SAN         []string `json:"san,omitempty"`
}

// Redirect is one hop in the redirect chain.
type Redirect struct {
	Status   int    `json:"status,omitempty"`
	URL      string `json:"url,omitempty"`
	Location string `json:"location,omitempty"`
}

// CookieInfo is cookie metadata without the value.
type CookieInfo struct {
	Name     string `json:"name,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Expires  string `json:"expires,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	HTTPOnly bool   `json:"http_only,omitempty"`
}

// Result is the shared JSON envelope. Download and upload add their own fields.
type Result struct {
	Tool         string              `json:"tool"`
	URL          string              `json:"url,omitempty"`
	FinalURL     string              `json:"final_url,omitempty"`
	Method       string              `json:"method,omitempty"`
	HTTPVersion  string              `json:"http_version,omitempty"`
	Status       int                 `json:"status,omitempty"`
	StatusText   string              `json:"status_text,omitempty"`
	OK           bool                `json:"ok"`
	Headers      map[string][]string `json:"headers,omitempty"`
	ContentType  string              `json:"content_type,omitempty"`
	Charset      string              `json:"charset,omitempty"`
	Body         string              `json:"body,omitempty"`
	BodyEncoding string              `json:"body_encoding,omitempty"`
	BodyBytes    int64               `json:"body_bytes,omitempty"`
	BodySHA256   string              `json:"body_sha256,omitempty"`
	SavedTo      *string             `json:"saved_to,omitempty"`
	Truncated    bool                `json:"truncated,omitempty"`
	Redirects    []Redirect          `json:"redirects,omitempty"`
	Cookies      []CookieInfo        `json:"cookies,omitempty"`
	Rejected     int                 `json:"cookies_rejected,omitempty"`
	TimingMS     Timing              `json:"timing_ms"`
	RemoteAddr   string              `json:"remote_addr,omitempty"`
	TLS          *TLSInfo            `json:"tls,omitempty"`
	Retries      int                 `json:"retries,omitempty"`

	// Download extras.
	Path         string `json:"path,omitempty"`
	Bytes        int64  `json:"bytes,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	Resumed      bool   `json:"resumed,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`

	// Upload extras.
	UploadedBytes int64 `json:"uploaded_bytes,omitempty"`
	Parts         int   `json:"parts,omitempty"`

	Error     string `json:"error,omitempty"`
	ErrorKind string `json:"error_kind,omitempty"`
}

func (r *Result) fail(kind, message string) Result {
	r.OK = false
	r.Error = message
	r.ErrorKind = kind
	return *r
}

// Auth is one of the supported authorization helpers.
type Auth struct {
	Type     string
	Username string
	Password string
	Token    string
}

// TLSOptions are per-call TLS overrides. File paths resolve under the session
// root before reaching this package.
type TLSOptions struct {
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string
	ServerName     string
}

// RetryPolicy bounds the retry loop.
type RetryPolicy struct {
	Count              int
	Delay              time.Duration
	MaxDelay           time.Duration
	RetryNonIdempotent bool
}

// ResponseOptions control inline vs file handling and visibility.
type ResponseOptions struct {
	Mode           string
	Path           string
	MaxInlineBytes int64
	MaxBytes       int64
	IncludeHeaders bool
	IncludeBody    bool
	IncludeCookies bool
}

// CookieOptions select the per-session jar and explicit cookie values.
type CookieOptions struct {
	Jar   string
	Send  map[string]string
	Clear bool
}

// MultipartFile is one inline multipart part (content lives in memory).
type MultipartFile struct {
	Content     string
	Filename    string
	ContentType string
}

// Multipart is the inline multipart body.
type Multipart struct {
	Fields url.Values
	Files  map[string]MultipartFile
}

// Request is one http_request call.
type Request struct {
	URL              string
	Method           string
	Query            url.Values
	ReplaceQuery     bool
	Headers          http.Header
	Body             []byte
	JSON             any
	Form             url.Values
	Multipart        *Multipart
	Auth             *Auth
	FollowRedirects  bool
	MaxRedirects     int
	AllowHTTPSToHTTP bool
	Insecure         bool
	TLS              TLSOptions
	Proxy            string
	NoProxy          bool
	HTTPVersion      string
	Compressed       bool
	Timeout          time.Duration
	ConnectTimeout   time.Duration
	Retry            RetryPolicy
	Response         ResponseOptions
	Cookies          CookieOptions
	FailOnHTTPError  bool
}

// DownloadRequest is one http_download call.
type DownloadRequest struct {
	URL              string
	Path             string
	Overwrite        bool
	Resume           bool
	ExpectedSHA256   string
	Headers          http.Header
	Auth             *Auth
	FollowRedirects  bool
	MaxRedirects     int
	AllowHTTPSToHTTP bool
	Insecure         bool
	TLS              TLSOptions
	Proxy            string
	NoProxy          bool
	HTTPVersion      string
	Compressed       bool
	Timeout          time.Duration
	ConnectTimeout   time.Duration
	MaxBytes         int64
	Retry            RetryPolicy
}

// UploadFile is one multipart file part streamed from disk.
type UploadFile struct {
	Field       string
	Path        string
	Filename    string
	ContentType string
}

// UploadRequest is one http_upload call.
type UploadRequest struct {
	URL              string
	Method           string
	Fields           url.Values
	Files            []UploadFile
	BodyFile         string
	ContentType      string
	Headers          http.Header
	Auth             *Auth
	FollowRedirects  bool
	MaxRedirects     int
	AllowHTTPSToHTTP bool
	Insecure         bool
	TLS              TLSOptions
	Proxy            string
	NoProxy          bool
	HTTPVersion      string
	Compressed       bool
	Timeout          time.Duration
	ConnectTimeout   time.Duration
	MaxBytes         int64
	MaxResponseBytes int64
	Retry            RetryPolicy
}
