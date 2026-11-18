package server

import (
	"errors"
	"io"
	"net/http"
)

// Request bodies cross a process boundary before any handler sees them. JSON
// APIs get a generous but finite cap; streaming upload paths have their own.
const (
	maxJSONBodyBytes         int64 = 8 << 20  // 8 MiB
	maxWebhookBodyBytes      int64 = 10 << 20 // 10 MiB
	maxAttachmentUploadBytes int64 = 100 << 20
)

var errBodyTooLarge = errors.New("request body too large")

// readLimitedBody reads at most limit bytes and reports errBodyTooLarge rather
// than truncating silently. Callers that stream (multipart) use
// http.MaxBytesReader instead.
func readLimitedBody(r *http.Request, limit int64) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}
