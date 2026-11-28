package nativehttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Parity-exempt: Go-only transport; cookie jar isolation policy.
func TestCookieJarIsolation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "host", Value: "1", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "dom", Value: "1", Domain: "example.com", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "__Host-bad", Value: "1", Path: "/x"})
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Header.Get("Cookie")))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client, _ := newTestClient(t, testConfig())
	if result := client.Do(context.Background(), baseRequest(server.URL+"/set")); result.Error != "" {
		t.Fatal(result.Error)
	}
	result := client.Do(context.Background(), baseRequest(server.URL+"/echo"))
	if !strings.Contains(result.Body, "host=1") {
		t.Fatalf("host cookie not sent: %q", result.Body)
	}
	if strings.Contains(result.Body, "dom=") || strings.Contains(result.Body, "__Host-bad") {
		t.Fatalf("rejected cookies were sent: %q", result.Body)
	}
	if rejected := client.RejectedCookies(); rejected < 2 {
		t.Fatalf("rejected = %d, want >= 2", rejected)
	}

	clear := baseRequest(server.URL + "/echo")
	clear.Cookies.Clear = true
	result = client.Do(context.Background(), clear)
	if result.Body != "" {
		t.Fatalf("Clear did not empty the jar: %q", result.Body)
	}

	other, _ := newTestClient(t, testConfig())
	result = other.Do(context.Background(), baseRequest(server.URL+"/echo"))
	if result.Body != "" {
		t.Fatalf("second session saw the first session's cookies: %q", result.Body)
	}

	stateless, _ := newTestClient(t, testConfig())
	_ = stateless.Do(context.Background(), baseRequest(server.URL+"/set"))
	none := baseRequest(server.URL + "/echo")
	none.Cookies.Jar = "none"
	result = stateless.Do(context.Background(), none)
	if result.Body != "" {
		t.Fatalf("jar=none sent automatic cookies: %q", result.Body)
	}
}

// Parity-exempt: Go-only transport; download, resume and atomic rename.
func TestDownloadAndResume(t *testing.T) {
	payload := []byte("abcdefghij")
	var sawRange, sawIfRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		if rng := r.Header.Get("Range"); rng != "" {
			sawRange = rng
			sawIfRange = r.Header.Get("If-Range")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 5-%d/%d", len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[5:])
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	client, root := newTestClient(t, testConfig())

	req := DownloadRequest{
		URL:             server.URL,
		Path:            filepath.Join(root, "file.bin"),
		FollowRedirects: true,
		MaxRedirects:    10,
		NoProxy:         true,
		Compressed:      true,
		Timeout:         5 * time.Second,
		MaxBytes:        1 << 20,
	}
	result := client.Download(context.Background(), req)
	if result.Error != "" || !result.OK || result.Bytes != int64(len(payload)) {
		t.Fatalf("download: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(root, "file.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded body: %v %q", err, got)
	}

	part := filepath.Join(root, "file.bin.part")
	if err := os.WriteFile(part, payload[:5], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part+".meta", []byte(`{"etag":"\"v1\"","total":10}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resume := req
	resume.Resume = true
	result = client.Download(context.Background(), resume)
	if result.Error != "" || !result.Resumed {
		t.Fatalf("resume: %+v", result)
	}
	if sawRange != "bytes=5-" || sawIfRange != `"v1"` {
		t.Fatalf("range = %q if-range = %q", sawRange, sawIfRange)
	}
	got, err = os.ReadFile(filepath.Join(root, "file.bin"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("resumed body: %v %q", err, got)
	}

	bad := req
	bad.Path = filepath.Join(root, "bad.bin")
	bad.ExpectedSHA256 = "00"
	if result = client.Download(context.Background(), bad); result.ErrorKind != ErrChecksumMismatch {
		t.Fatalf("checksum kind = %q (%s)", result.ErrorKind, result.Error)
	}

	if result = client.Download(context.Background(), req); result.ErrorKind != ErrWriteFailed {
		t.Fatalf("overwrite kind = %q (%s)", result.ErrorKind, result.Error)
	}
}

// Parity-exempt: Go-only transport; multipart and raw upload handling.
func TestUserPathsMustBeAbsolute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client, root := newTestClient(t, testConfig())

	download := func(path string) Result {
		return client.Download(context.Background(), DownloadRequest{
			URL: server.URL, Path: path,
			FollowRedirects: true, MaxRedirects: 10, NoProxy: true, Compressed: true,
			Timeout: 5 * time.Second, MaxBytes: 1 << 20,
		})
	}
	if result := download("relative.bin"); result.ErrorKind != ErrPathNotAbsolute {
		t.Fatalf("relative download kind = %q (%s)", result.ErrorKind, result.Error)
	}
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if result := download(outside); result.ErrorKind != ErrPathEscape {
		t.Fatalf("outside download kind = %q (%s)", result.ErrorKind, result.Error)
	}

	upload := func(req UploadRequest) Result {
		req.URL = server.URL
		req.FollowRedirects = true
		req.MaxRedirects = 10
		req.NoProxy = true
		req.Compressed = true
		req.Timeout = 5 * time.Second
		req.MaxBytes = 1 << 20
		return client.Upload(context.Background(), req)
	}
	if result := upload(UploadRequest{BodyFile: "relative.txt"}); result.ErrorKind != ErrPathNotAbsolute {
		t.Fatalf("relative body_file kind = %q (%s)", result.ErrorKind, result.Error)
	}
	if result := upload(UploadRequest{Files: []UploadFile{{Field: "f", Path: "relative.txt"}}}); result.ErrorKind != ErrPathNotAbsolute {
		t.Fatalf("relative upload file kind = %q (%s)", result.ErrorKind, result.Error)
	}

	responsePath := baseRequest(server.URL)
	responsePath.Response.Mode = "file"
	responsePath.Response.Path = "relative.bin"
	if result := client.Do(context.Background(), responsePath); result.ErrorKind != ErrPathNotAbsolute {
		t.Fatalf("relative response.path kind = %q (%s)", result.ErrorKind, result.Error)
	}

	tlsPath := baseRequest(server.URL)
	tlsPath.TLS = TLSOptions{CAFile: "ca.pem"}
	if result := client.Do(context.Background(), tlsPath); result.ErrorKind != ErrPathNotAbsolute {
		t.Fatalf("relative ca_file kind = %q (%s)", result.ErrorKind, result.Error)
	}

	// Absolute under the session root still works.
	okPath := filepath.Join(root, "ok.bin")
	if result := download(okPath); result.Error != "" || !result.OK {
		t.Fatalf("absolute download: %+v", result)
	}
}

func TestUploadMultipartAndRaw(t *testing.T) {
	var method, contentType, field, fileContent, filename string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		contentType = r.Header.Get("Content-Type")
		if strings.HasPrefix(contentType, "multipart/") {
			_ = r.ParseMultipartForm(1 << 20)
			field = r.FormValue("a")
			file, header, err := r.FormFile("f")
			if err == nil {
				data, _ := io.ReadAll(file)
				fileContent = string(data)
				filename = header.Filename
				_ = file.Close()
			}
		} else {
			data, _ := io.ReadAll(r.Body)
			fileContent = string(data)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client, root := newTestClient(t, testConfig())
	if err := os.WriteFile(filepath.Join(root, "upload.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := UploadRequest{
		URL:              server.URL,
		Fields:           url.Values{"a": {"1"}},
		Files:            []UploadFile{{Field: "f", Path: filepath.Join(root, "upload.txt"), Filename: "../evil.txt"}},
		FollowRedirects:  true,
		MaxRedirects:     10,
		NoProxy:          true,
		Compressed:       true,
		Timeout:          5 * time.Second,
		MaxBytes:         1 << 20,
		MaxResponseBytes: 1 << 20,
	}
	result := client.Upload(context.Background(), req)
	if result.Error != "" || result.Status != http.StatusOK || result.Parts != 2 {
		t.Fatalf("multipart upload: %+v", result)
	}
	if field != "1" || fileContent != "hello" || filename != "evil.txt" {
		t.Fatalf("multipart parts: field=%q content=%q filename=%q", field, fileContent, filename)
	}

	raw := req
	raw.Fields = nil
	raw.Files = nil
	raw.BodyFile = filepath.Join(root, "upload.txt")
	fileContent = ""
	result = client.Upload(context.Background(), raw)
	if result.Error != "" || method != http.MethodPut || fileContent != "hello" {
		t.Fatalf("raw upload: method=%q content=%q result=%+v", method, fileContent, result)
	}

	tooLarge := raw
	tooLarge.MaxBytes = 4
	result = client.Upload(context.Background(), tooLarge)
	if result.ErrorKind != ErrRequestTooLarge {
		t.Fatalf("max kind = %q (%s)", result.ErrorKind, result.Error)
	}

	missing := raw
	missing.BodyFile = filepath.Join(root, "missing.txt")
	result = client.Upload(context.Background(), missing)
	if result.Error == "" {
		t.Fatal("missing body_file was accepted")
	}
}
