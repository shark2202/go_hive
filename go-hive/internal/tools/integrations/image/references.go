package image

// Reference-image loading: local paths are read through the artifact home
// expansion; http(s) URLs pass the SSRF policy before the download. Limits and
// the single refusal message mirror the upstream image_gen_tool.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// loadReference turns one local path or http(s) URL into base64 plus its
// decoded byte length, applying the upstream size and SSRF limits.
func loadReference(ctx context.Context, rt *Runtime, deps Deps, source string) (string, int, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return downloadReference(ctx, rt, deps, source)
	}
	path := expandHome(source, storageEnv{userHomeDir: deps.UserHomeDir})
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("Reference image not found: %s", source)
	}
	if info.Size() > maxReferenceBytes {
		return "", 0, fmt.Errorf("Reference image exceeds %dMB: %s", maxReferenceBytes/(1024*1024), source)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fmt.Errorf("Failed to read reference image %s: %v", source, err)
	}
	return base64.StdEncoding.EncodeToString(raw), len(raw), nil
}

// downloadReference fetches a reference image URL after the SSRF policy,
// keeping upstream's single block message for every refusal.
func downloadReference(ctx context.Context, rt *Runtime, deps Deps, source string) (string, int, error) {
	policy := urlPolicy{resolver: deps.Resolver}
	if !policy.allows(ctx, source) {
		return "", 0, fmt.Errorf("Blocked non-public or unresolvable image URL: %s", source)
	}
	requestCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, source, nil)
	if err != nil {
		return "", 0, fmt.Errorf("Failed to fetch reference image %s: %v", source, err)
	}
	client := httpClient(rt, downloadTimeout)
	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			return "", 0, fmt.Errorf("Reference image download timed out: %s", source)
		}
		return "", 0, fmt.Errorf("Failed to fetch reference image %s: %v", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("Reference image fetch failed (HTTP %d): %s", resp.StatusCode, source)
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, "image/") {
		if contentType == "" {
			contentType = "unknown"
		}
		return "", 0, fmt.Errorf("Reference URL is not an image (content-type %s): %s", contentType, source)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReferenceBytes+1))
	if err != nil {
		return "", 0, fmt.Errorf("Failed to fetch reference image %s: %v", source, err)
	}
	if len(raw) > maxReferenceBytes {
		return "", 0, fmt.Errorf("Reference image exceeds %dMB: %s", maxReferenceBytes/(1024*1024), source)
	}
	return base64.StdEncoding.EncodeToString(raw), len(raw), nil
}
