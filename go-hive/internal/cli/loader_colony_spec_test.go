package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
)

func writeColonySpecFixture(t *testing.T, home hivehome.Home, colonyID, config string) string {
	t.Helper()
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	configPath := home.ConfigurationFile()
	if err := os.WriteFile(configPath, []byte(`{"llm":{"model":"ollama/llama3"}}`), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	colonyPath := filepath.Join(home.ColoniesDir(), colonyID)
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	if config != "" {
		if err := os.WriteFile(filepath.Join(colonyPath, "worker.json"), []byte(config), 0o644); err != nil {
			t.Fatalf("write worker config: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(colonyPath, "metadata.json"), []byte(`{"queen_name":"queen_alpha"}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	return colonyPath
}

// Parity-exempt: serve --colony wiring is Go-only (upstream preloads a session
// through SessionManager); pins that the live host carries the on-disk worker
// spec and goal instead of a synthetic colony placeholder.
func TestBuildColonyHostUsesWorkerSpecOnDisk(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := writeColonySpecFixture(t, home, "honeycomb",
		`{"name":"worker","description":"trader","system_prompt":"be a careful trader",`+
			`"tools":["read_file"],"goal":{"description":"trade honeycomb"}}`)

	var stdout bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stdout}
	built := runner.buildColonyHost(colonyPath, "honeycomb", home, "")
	if built == nil {
		t.Fatalf("host is nil; output: %s", stdout.String())
	}
	if built.StreamID() != "honeycomb" {
		t.Fatalf("stream id = %q", built.StreamID())
	}
	host, ok := built.(*colonyhost.Host)
	if !ok {
		t.Fatalf("host type = %T", built)
	}
	goal := host.Runtime().Goal()
	if goal == nil {
		t.Fatal("runtime goal is nil")
	}
	if goal.ID != "honeycomb-goal" || goal.Description != "trade honeycomb" {
		t.Fatalf("goal = %#v", goal)
	}
}

// Parity-exempt: a colony with no worker config has nothing to run; pins the
// fall-back-to-disk path instead of a synthetic worker.
func TestBuildColonyHostSkipsColonyWithoutWorkerSpec(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := writeColonySpecFixture(t, home, "empty_colony", "")

	var stdout bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stdout}
	if built := runner.buildColonyHost(colonyPath, "empty_colony", home, ""); built != nil {
		t.Fatalf("host = %#v, want nil", built)
	}
	if !strings.Contains(stdout.String(), "No worker config found") {
		t.Fatalf("output = %q", stdout.String())
	}
}
