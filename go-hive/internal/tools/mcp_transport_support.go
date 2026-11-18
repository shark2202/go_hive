package tools

import (
	"bufio"
	"errors"
	"io"
	"os/exec"
	"sort"
)

type execCmd struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
}

func startMCPProcess(command string, args, env []string, cwd string) (*execCmd, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = env
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &execCmd{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

func (c *execCmd) pipes() (io.WriteCloser, io.Reader, error) {
	if c.stdin == nil || c.stdout == nil {
		return nil, nil, errors.New("MCP process pipes are not initialized")
	}
	return c.stdin, c.stdout, nil
}

func (c *execCmd) kill() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
}

type mcpLineReader struct {
	reader *bufio.Reader
}

func newMCPLineReader(r io.Reader) *mcpLineReader {
	return &mcpLineReader{reader: bufio.NewReaderSize(r, 64*1024)}
}

func (r *mcpLineReader) ReadLine() (string, error) {
	line, err := r.reader.ReadBytes('\n')
	if len(line) == 0 && err != nil {
		return "", err
	}
	for len(line) > 0 && (line[len(line)-1] == '\n' || line[len(line)-1] == '\r') {
		line = line[:len(line)-1]
	}
	return string(line), nil
}

func sortMCPTools(tools []MCPTool) {
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
}
