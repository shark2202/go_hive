package lsp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
)

// Parity-exempt: LSP Content-Length framing is new Go-only transport work
// required by docs/lsp-design.json R2/A4; there is no Python source test.
func TestReadFrameAcceptsExtraHeadersAndSplitWrites(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":null}`
	var framed bytes.Buffer
	framed.WriteString("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n")
	framed.WriteString("Content-Length: ")
	framed.WriteString(strconv.Itoa(len(body)))
	framed.WriteString("\r\n\r\n")
	framed.WriteString(body)

	reader := bufio.NewReader(iotest.OneByteReader(bytes.NewReader(framed.Bytes())))
	got, err := readFrame(reader, MaxFrameBytes)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// Parity-exempt: no upstream LSP frame-header validation exists; this is the
// Go stdio transport contract (A4).
func TestReadFrameRejectsMissingAndInvalidHeaders(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"missing length", "Content-Type: application/json\r\n\r\n{}", ErrMissingContentLength},
		{"negative length", "Content-Length: -1\r\n\r\n", nil},
		{"non-numeric length", "Content-Length: abc\r\n\r\n", nil},
		{"malformed header", "not-a-header\r\n\r\n", nil},
		{"truncated header", "Content-Length: 10\r\n", nil},
		{"empty", "", io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.in))
			_, err := readFrame(reader, MaxFrameBytes)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// Parity-exempt: oversized frame refusal is a new resource bound from
// docs/lsp-design.json R8/A10.
func TestReadFrameRejectsOversizedBeforeAllocatingBody(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("Content-Length: 999999999\r\n\r\n"))
	_, err := readFrame(reader, 1024)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

// Parity-exempt: a newline-free header stream must not grow memory without
// bound; this is part of the R8/A10 transport limits.
func TestReadFrameRejectsOversizedHeaderLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(strings.Repeat("A", maxHeaderBytes+1)))
	_, err := readFrame(reader, MaxFrameBytes)
	if err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatalf("err = %v, want header-size failure", err)
	}
}

// Parity-exempt: writeFrame has no Python counterpart; it is the client half
// of the stdio JSON-RPC transport (R2).
func TestWriteFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	body := []byte(`{"jsonrpc":"2.0","method":"initialized","params":{}}`)
	if err := writeFrame(&buf, body); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	if !strings.HasPrefix(buf.String(), fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))) {
		t.Fatalf("header = %q", buf.String())
	}
	got, err := readFrame(bufio.NewReader(&buf), MaxFrameBytes)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("round trip = %q, want %q", got, body)
	}
}

// Parity-exempt: stderr redaction is a security requirement from
// docs/lsp-design.json R10/A11 with no upstream test.
func TestRedactSecretsDropsCredentialLines(t *testing.T) {
	raw := "starting gopls\napi_token=supersecret\nlistening on stdio\npassword: hunter2\n"
	got := redactSecrets(raw, 2048, 40)
	if strings.Contains(got, "supersecret") || strings.Contains(got, "hunter2") {
		t.Fatalf("secret survived redaction: %q", got)
	}
	if !strings.Contains(got, "starting gopls") || !strings.Contains(got, "listening on stdio") {
		t.Fatalf("non-secret lines were dropped: %q", got)
	}
	if strings.Count(got, "[redacted stderr line]") != 2 {
		t.Fatalf("redaction count wrong: %q", got)
	}
}

// Parity-exempt: bounded stderr tail is a new Go process-lifecycle guard (R8).
func TestTailBufferKeepsOnlyTail(t *testing.T) {
	tail := newTailBuffer(8)
	if _, err := tail.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := tail.String(); got != "89abcdef" {
		t.Fatalf("tail = %q, want 89abcdef", got)
	}
}
