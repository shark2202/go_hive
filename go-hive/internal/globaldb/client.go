package globaldb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	EnvJWT  = "HIVE_CLOUD_JWT"
	EnvBase = "HIVE_CLOUD_BASE"
)

type NotSignedInError struct {
	Message string
}

func (e *NotSignedInError) Error() string { return e.Message }

type GlobalDbError struct {
	Message string
	Status  int
}

func (e *GlobalDbError) Error() string { return e.Message }

type Client struct {
	BaseURL string
	JWT     string
	HTTP    *http.Client
}

func NewClientFromEnv() (*Client, error) {
	jwt := strings.TrimSpace(os.Getenv(EnvJWT))
	base := strings.TrimSpace(os.Getenv(EnvBase))
	if jwt == "" || base == "" {
		return nil, &NotSignedInError{Message: "Sign in to use the shared global DB — no cloud session (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE unset)."}
	}
	return NewClient(strings.TrimRight(base, "/"), jwt), nil
}

func NewClient(baseURL, jwt string) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		JWT:     jwt,
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				DialContext: dialer.DialContext,
			},
		},
	}
}

func (c *Client) SetTransport(rt http.RoundTripper) {
	c.HTTP.Transport = rt
}

func (c *Client) Request(ctx context.Context, method, path string, body any, params url.Values) (any, error) {
	target := c.BaseURL + path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "jwt "+c.JWT)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &NotSignedInError{Message: "Cloud session rejected (401) — sign in again."}
	}
	if resp.StatusCode >= 400 {
		return nil, &GlobalDbError{Message: extractError(raw, resp.StatusCode), Status: resp.StatusCode}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func extractError(raw []byte, status int) string {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Sprintf("HTTP %d", status)
	}
	switch errVal := body["error"].(type) {
	case map[string]any:
		if msg, ok := errVal["message"].(string); ok && msg != "" {
			return msg
		}
		if code, ok := errVal["code"].(string); ok && code != "" {
			return code
		}
		return fmt.Sprintf("HTTP %d", status)
	case string:
		return errVal
	default:
		return fmt.Sprintf("HTTP %d", status)
	}
}

func (c *Client) SQL(ctx context.Context, sqlText string, rowCap *int) (any, error) {
	body := map[string]any{"sql": sqlText}
	if rowCap != nil && *rowCap != 0 {
		body["row_cap"] = *rowCap
	}
	return c.Request(ctx, http.MethodPost, "/v1/global-db/sql", body, nil)
}

func (c *Client) Query(ctx context.Context, sqlText string, rowCap *int) (any, error) {
	body := map[string]any{"sql": sqlText}
	if rowCap != nil && *rowCap != 0 {
		body["row_cap"] = *rowCap
	}
	return c.Request(ctx, http.MethodPost, "/v1/global-db/query", body, nil)
}

func (c *Client) Upsert(ctx context.Context, table string, row map[string]any, sourceColony, mode string) (any, error) {
	body := map[string]any{"table": table, "row": row}
	if sourceColony != "" {
		body["source_colony"] = sourceColony
	}
	if mode != "" {
		body["mode"] = mode
	}
	return c.Request(ctx, http.MethodPost, "/v1/global-db/upsert", body, nil)
}

func (c *Client) ImportRows(ctx context.Context, table string, rows []map[string]any, sourceColony string) (any, error) {
	body := map[string]any{"table": table, "rows": rows}
	if sourceColony != "" {
		body["source_colony"] = sourceColony
	}
	return c.Request(ctx, http.MethodPost, "/v1/global-db/import", body, nil)
}

func (c *Client) ListTables(ctx context.Context) (any, error) {
	return c.Request(ctx, http.MethodGet, "/v1/global-db/tables", nil, nil)
}

func (c *Client) ListChanges(ctx context.Context, since string) (any, error) {
	var params url.Values
	if since != "" {
		params = url.Values{"since": []string{since}}
	}
	return c.Request(ctx, http.MethodGet, "/v1/global-db/changes", nil, params)
}

func (c *Client) ListRows(ctx context.Context, table string, params url.Values) (any, error) {
	return c.Request(ctx, http.MethodGet, "/v1/global-db/tables/"+pythonQuote(table)+"/rows", nil, params)
}

func (c *Client) UpdateRow(ctx context.Context, table string, pk, updates map[string]any) (any, error) {
	body := map[string]any{"pk": pk, "updates": updates}
	return c.Request(ctx, http.MethodPatch, "/v1/global-db/tables/"+pythonQuote(table)+"/rows", body, nil)
}

func pythonQuote(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}
