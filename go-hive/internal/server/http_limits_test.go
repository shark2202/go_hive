package server

// Parity-exempt: Go-only HTTP hardening; no upstream test pins the body cap.

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity-exempt: Go-only HTTP hardening; no upstream test pins the body cap.
func TestReadLimitedBodyRejectsOversizedJSON(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), int(maxJSONBodyBytes)+1)
	request := httptest.NewRequest("POST", "/api/anything", bytes.NewReader(payload))
	if _, err := readLimitedBody(request, maxJSONBodyBytes); err != errBodyTooLarge {
		t.Fatalf("oversized body error = %v, want errBodyTooLarge", err)
	}

	request = httptest.NewRequest("POST", "/api/anything", strings.NewReader(`{"ok":true}`))
	data, err := readLimitedBody(request, maxJSONBodyBytes)
	if err != nil {
		t.Fatalf("small body: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("small body = %q", data)
	}
}
