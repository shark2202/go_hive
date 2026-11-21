package pdf

// Input resolution and URL download: local-path checks, the literal-host SSRF
// deny-list, bounded downloads with redirect re-validation, and the terminal
// failure envelopes shared by every error path. The DNS-level guard lives in
// ssrf.go.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func pdfLocalPath(raw string) (string, map[string]any) {
	path, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", map[string]any{"error": "Failed to open PDF: " + err.Error()}
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", map[string]any{"error": "PDF file not found: " + raw}
		}
		return "", map[string]any{"error": "Failed to open PDF: " + err.Error()}
	}
	if info.IsDir() {
		return "", map[string]any{"error": "Not a file: " + raw}
	}
	if !strings.EqualFold(filepath.Ext(path), ".pdf") {
		return "", map[string]any{"error": "Not a PDF file (expected .pdf): " + raw}
	}
	return path, nil
}

func pdfURLName(target string) string {
	trimmed := strings.SplitN(target, "?", 2)[0]
	trimmed = strings.SplitN(trimmed, "#", 2)[0]
	name := filepath.Base(trimmed)
	if name == "" || name == "." || name == "/" {
		return "download.pdf"
	}
	return name
}

// pdfIsPrivateURL mirrors the upstream string-level SSRF deny-list. DNS
// rebinding is out of scope here, exactly as in the Python tool.
func pdfIsPrivateURL(target string) bool {
	parsed, err := url.Parse(target)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" || host == "::1" || host == "0.0.0.0" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return true
		}
	}
	for _, prefix := range []string{"127.", "10.", "192.168.", "169.254."} {
		if strings.HasPrefix(host, prefix) {
			return true
		}
	}
	if strings.HasPrefix(host, "172.") {
		parts := strings.Split(host, ".")
		if len(parts) >= 2 {
			if second, err := strconv.Atoi(parts[1]); err == nil && second >= 16 && second <= 31 {
				return true
			}
		}
	}
	return false
}

func pdfDownload(ctx context.Context, rt *Runtime, target string, maxBytes int) ([]byte, map[string]any) {
	if pdfIsPrivateURL(target) {
		return nil, map[string]any{"error": "URL refused (private/loopback host): " + target}
	}
	if block := checkURLTarget(ctx, target, rt.ResolveHost); block != "" {
		return nil, map[string]any{
			"error":                      "URL refused (SSRF protection): " + block,
			"blocked_by_ssrf_protection": true,
			"url":                        target,
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, map[string]any{"error": "Failed to download PDF: " + err.Error()}
	}
	request.Header.Set("User-Agent", "AdenBot/1.0 (PDF Reader)")
	client := &http.Client{Timeout: 30 * time.Second}
	if rt.HTTP != nil && rt.HTTP.Inner != nil {
		clone := *rt.HTTP.Inner
		client = &clone
	}
	previousRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if block := checkURLTarget(req.Context(), req.URL.String(), rt.ResolveHost); block != "" {
			return &ssrfBlockedError{url: req.URL.String(), message: block}
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		var blocked *ssrfBlockedError
		if errors.As(err, &blocked) {
			return nil, map[string]any{
				"error":                      "URL refused (SSRF protection): " + blocked.Error(),
				"blocked_by_ssrf_protection": true,
				"url":                        blocked.url,
			}
		}
		if ctx.Err() != nil || errTimeout(err) {
			return nil, map[string]any{"error": "PDF download timed out"}
		}
		return nil, map[string]any{"error": "Failed to download PDF: " + err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, map[string]any{"error": fmt.Sprintf("Failed to download PDF: HTTP %d", response.StatusCode)}
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/pdf") {
		return nil, map[string]any{
			"error":        "URL does not point to a PDF file. Content-Type: " + contentType,
			"content_type": contentType,
			"url":          target,
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(maxBytes)+1))
	if err != nil {
		return nil, map[string]any{"error": "Failed to download PDF: " + err.Error()}
	}
	if len(body) > maxBytes {
		return nil, map[string]any{"error": fmt.Sprintf(
			"PDF exceeds max_bytes (%d > %d). Increase max_bytes_mb or use a smaller PDF.", len(body), maxBytes)}
	}
	return body, nil
}

func pdfTerminalYield(path, reason, message string) map[string]any {
	if message == "" {
		message = reason
	}
	return map[string]any{
		"error":         message,
		"path":          path,
		"next_step":     pdfNextStep(reason, path),
		"terminal_hint": terminalHint,
	}
}

func pdfOpenFailure(path string, err error) map[string]any {
	return pdfTerminalYield(path, "pdf_open_failed", "Failed to open PDF: "+err.Error())
}

func pdfNextStep(reason, path string) string {
	switch reason {
	case "pdf_encrypted":
		return "qpdf --decrypt --password=<pw> '" + path + "' /tmp/decrypted.pdf, then call pdf_read again on /tmp/decrypted.pdf"
	case "pdf_scanned_no_text":
		return "Attach the original PDF to a vision-capable model, or run pdftoppm + tesseract."
	default:
		return "pdfinfo '" + path + "'"
	}
}
