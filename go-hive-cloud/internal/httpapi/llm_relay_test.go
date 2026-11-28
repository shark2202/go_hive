package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCapRequestBodyRejectsDeclaredOversize(t *testing.T) {
	r := &http.Request{
		Body:          io.NopCloser(strings.NewReader("x")),
		ContentLength: maxLLMRequestBody + 1,
	}
	if _, err := capRequestBody(r, maxLLMRequestBody); !errors.Is(err, errRequestBodyTooLarge) {
		t.Fatalf("err = %v, want errRequestBodyTooLarge", err)
	}
}

func TestCapRequestBodyRejectsStreamedOversize(t *testing.T) {
	r := &http.Request{
		Body:          io.NopCloser(strings.NewReader("0123456789")),
		ContentLength: -1,
	}
	body, err := capRequestBody(r, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	if _, err := io.ReadAll(body); !errors.Is(err, errRequestBodyTooLarge) {
		t.Fatalf("err = %v, want errRequestBodyTooLarge", err)
	}
}

func TestCapRequestBodyAllowsExactLimit(t *testing.T) {
	r := &http.Request{
		Body:          io.NopCloser(strings.NewReader("0123")),
		ContentLength: 4,
	}
	body, err := capRequestBody(r, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read exact-limit body: %v", err)
	}
	if string(raw) != "0123" {
		t.Fatalf("body = %q", raw)
	}
}
