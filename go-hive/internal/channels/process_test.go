package channels

// Process helpers that do not need a subprocess.

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

// TestMergeEnvReplacesInheritedKeys keeps configured KEY=value entries
// authoritative over the serve process' environment.
//
// Parity-exempt: env merging is part of the Go stdio process extension.
func TestMergeEnvReplacesInheritedKeys(t *testing.T) {
	merged := mergeEnv(
		[]string{"PATH=/usr/bin", "TOKEN=inherited", "KEEP=1"},
		[]string{"TOKEN=configured", "ADDED=2"},
	)
	joined := strings.Join(merged, "\n")
	if strings.Count(joined, "TOKEN=") != 1 || !strings.Contains(joined, "TOKEN=configured") {
		t.Fatalf("merged env = %#v, want TOKEN replaced", merged)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "KEEP=1") || !strings.Contains(joined, "ADDED=2") {
		t.Fatalf("merged env = %#v, want inherited entries kept and ADDED appended", merged)
	}
}

// TestReadLineLimitedBoundsAHugeStdoutLine refuses an over-limit line without
// buffering it all.
//
// Parity-exempt: the stdout frame cap is a stdio-transport rule.
func TestReadLineLimitedBoundsAHugeStdoutLine(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 64)+"\n"), 8)
	if _, err := readLineLimited(reader, 16); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("readLineLimited error = %v, want errFrameTooLarge", err)
	}
	reader = bufio.NewReaderSize(strings.NewReader("short\n"), 8)
	line, err := readLineLimited(reader, 16)
	if err != nil || string(line) != "short\n" {
		t.Fatalf("readLineLimited = %q, %v", line, err)
	}
}
