package streamtoken

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RefreshPath is the cloud endpoint that re-mints a streamToken from the
// streamToken itself (its signature is the proof of identity; no user JWT).
//
// Parity: core/framework/server/streamtoken_refresh.py::post_refresh (URL)
const RefreshPath = "/user/refresh-stream-token-from-stream"

// refreshTimeout bounds one POST, matching the reference client's 15s.
//
// Parity: core/framework/server/streamtoken_refresh.py::post_refresh
const refreshTimeout = 15 * time.Second

// ErrMissingToken is returned when the backend answers 200 without a usable
// streamToken. Treating that as a failure keeps an empty value from
// overwriting the stored credential.
var ErrMissingToken = errors.New("streamtoken: response missing streamToken")

// Client calls the refresh endpoint.
type Client struct {
	HTTP *http.Client
}

// NewClient returns the production client (15s timeout, no extra deps).
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: refreshTimeout}}
}

// Refresh POSTs the current token and returns the rotated one. Any non-200
// status, transport error or missing body token is an error; the caller leaves
// the stored credential untouched and retries on a later tick.
//
// Parity: core/framework/server/streamtoken_refresh.py::post_refresh
func (c *Client) Refresh(ctx context.Context, baseURL, currentToken string) (string, error) {
	client := http.DefaultClient
	if c != nil && c.HTTP != nil {
		client = c.HTTP
	}
	endpoint := strings.TrimRight(strings.TrimSpace(baseURL), "/") + RefreshPath
	body, err := json.Marshal(map[string]string{"streamToken": currentToken})
	if err != nil {
		return "", fmt.Errorf("streamtoken: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("streamtoken: build request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("streamtoken: POST %s: %w", endpoint, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if response.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(payload))
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return "", fmt.Errorf("streamtoken: backend returned %d: %s", response.StatusCode, detail)
	}
	var decoded struct {
		StreamToken string `json:"streamToken"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("streamtoken: decode response: %w", err)
	}
	if decoded.StreamToken == "" {
		return "", ErrMissingToken
	}
	return decoded.StreamToken, nil
}
