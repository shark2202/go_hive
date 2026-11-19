package agents

// Fuzz target for worker/agent spec parsing. Agent spec files arrive from
// imported colonies and copied agent directories, so the JSON and YAML
// decoders must reject malformed input without panicking.

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoadAgentSpec feeds arbitrary bytes through the same file-discovery and
// decode path LoadAgentSpec uses for real agent directories. The first byte
// selects the JSON or YAML decoder so both config formats stay covered.
func FuzzLoadAgentSpec(f *testing.F) {
	f.Add(byte(0), []byte(`{"name":"fuzz-worker","tools":["Read"],"max_tokens":10}`))
	f.Add(byte(1), []byte("name: fuzz-worker\ntools:\n  - Read\nmax_tokens: 10\n"))
	f.Add(byte(0), []byte(`{"tools":{"policy":"all","allowed":["Read"]}}`))
	f.Add(byte(1), []byte(""))
	f.Add(byte(2), []byte("name:\n\t- unbalanced\n  - yaml"))

	f.Fuzz(func(t *testing.T, mode byte, data []byte) {
		dir := t.TempDir()
		ext := ".json"
		switch mode % 3 {
		case 1:
			ext = ".yaml"
		case 2:
			ext = ".yml"
		}
		if err := os.WriteFile(filepath.Join(dir, "worker"+ext), data, 0o600); err != nil {
			t.Fatalf("write spec: %v", err)
		}
		spec, err := LoadAgentSpec(dir)
		if err == nil && spec == nil {
			t.Fatalf("LoadAgentSpec returned (nil, nil) for %q", data)
		}
		if spec != nil && spec.Dir == "" {
			t.Fatalf("LoadAgentSpec returned a spec without a dir for %q", data)
		}
	})
}
