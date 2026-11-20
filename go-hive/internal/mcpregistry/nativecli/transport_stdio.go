package nativecli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// stdioTransport speaks MCP over a child process's stdin/stdout. Messages are
// newline-delimited JSON per the MCP spec; a server that answers with
// Content-Length framing switches the connection into LSP framing.
type stdioTransport struct {
	command     *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	stderr      io.Writer
	writeHeader bool
}

// newStdioTransport spawns the server described by commandLine.
func newStdioTransport(commandLine string, env []string, dir string, stderr io.Writer) (*stdioTransport, error) {
	parts, err := splitCommandLine(commandLine)
	if err != nil {
		return nil, err
	}
	if len(parts) == 0 {
		return nil, errors.New("empty MCP stdio command")
	}
	command := exec.Command(parts[0], parts[1:]...)
	command.Env = env
	if dir != "" {
		command.Dir = dir
	}
	command.Stderr = stderr
	configureProcessGroup(command)

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &stdioTransport{
		command: command,
		stdin:   stdin,
		stdout:  bufio.NewReaderSize(stdout, 1<<20),
		stderr:  stderr,
	}, nil
}

// Send writes one JSON-RPC message using the connection's framing.
func (t *stdioTransport) Send(message []byte) error {
	if t.writeHeader {
		if _, err := fmt.Fprintf(t.stdin, "Content-Length: %d\r\n\r\n", len(message)); err != nil {
			return err
		}
		_, err := t.stdin.Write(message)
		return err
	}
	payload := make([]byte, 0, len(message)+1)
	payload = append(payload, message...)
	payload = append(payload, '\n')
	_, err := t.stdin.Write(payload)
	return err
}

// Receive reads the next JSON-RPC message, adopting Content-Length framing
// when the server uses it.
func (t *stdioTransport) Receive() ([]byte, error) {
	for {
		line, err := t.stdout.ReadString('\n')
		if err != nil && line == "" {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(trimmed) == "" {
			if err != nil {
				return nil, err
			}
			continue
		}
		if length, ok := contentLength(trimmed); ok {
			if err := t.drainHeaders(); err != nil {
				return nil, err
			}
			payload := make([]byte, length)
			if _, err := io.ReadFull(t.stdout, payload); err != nil {
				return nil, err
			}
			t.writeHeader = true
			return payload, nil
		}
		return []byte(trimmed), nil
	}
}

// contentLength parses a Content-Length header line.
func contentLength(line string) (int, bool) {
	name, value, found := strings.Cut(line, ":")
	if !found || !strings.EqualFold(strings.TrimSpace(name), "content-length") {
		return 0, false
	}
	length, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || length < 0 {
		return 0, false
	}
	return length, true
}

// drainHeaders consumes the remaining header lines up to the blank separator.
func (t *stdioTransport) drainHeaders() error {
	for {
		line, err := t.stdout.ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		if strings.TrimSpace(strings.TrimRight(line, "\r\n")) == "" {
			return nil
		}
	}
}

// Close shuts the server down: end its stdin, wait briefly, then kill the
// whole process group so no MCP server outlives the CLI.
func (t *stdioTransport) Close() error {
	if t.command == nil || t.command.Process == nil {
		return nil
	}
	if t.stdin != nil {
		_ = t.stdin.Close()
	}
	done := make(chan struct{})
	go func() {
		_ = t.command.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
	}
	terminateProcessGroup(t.command)
	_ = t.command.Process.Kill()
	<-done
	return nil
}

// splitCommandLine splits a shell-style command string the way Python's
// shlex.split does: single quotes are literal, double quotes honour backslash
// escapes, and a backslash escapes the next character elsewhere.
func splitCommandLine(line string) ([]string, error) {
	words := []string{}
	var current strings.Builder
	haveToken := false
	inSingle, inDouble := false, false

	for index := 0; index < len(line); {
		ch := line[index]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				current.WriteByte(ch)
			}
			index++
		case inDouble:
			switch {
			case ch == '"':
				inDouble = false
				index++
			case ch == '\\' && index+1 < len(line) && strings.ContainsRune(`"\$`+"`", rune(line[index+1])):
				current.WriteByte(line[index+1])
				index += 2
			default:
				current.WriteByte(ch)
				index++
			}
		default:
			switch ch {
			case '\'':
				inSingle = true
				haveToken = true
				index++
			case '"':
				inDouble = true
				haveToken = true
				index++
			case '\\':
				if index+1 < len(line) {
					current.WriteByte(line[index+1])
					haveToken = true
					index += 2
				} else {
					index++
				}
			case ' ', '\t', '\n':
				if haveToken {
					words = append(words, current.String())
					current.Reset()
					haveToken = false
				}
				index++
			default:
				current.WriteByte(ch)
				haveToken = true
				index++
			}
		}
	}
	if inSingle || inDouble {
		return nil, errors.New("no closing quotation")
	}
	if haveToken {
		words = append(words, current.String())
	}
	return words, nil
}

// mergedEnv applies overrides on top of a base environment.
func mergedEnv(base []string, overrides []Field) []string {
	environ := append([]string{}, base...)
	index := map[string]int{}
	for position, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		index[key] = position
	}
	for _, override := range overrides {
		entry := override.Key + "=" + stringOf(override.Value)
		if position, ok := index[override.Key]; ok {
			environ[position] = entry
			continue
		}
		index[override.Key] = len(environ)
		environ = append(environ, entry)
	}
	return environ
}
